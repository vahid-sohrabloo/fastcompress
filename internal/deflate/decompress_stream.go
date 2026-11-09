// Resumable DEFLATE decoding for the streaming Reader. This is new design
// (libdeflate v1.26 has no streaming decompressor; see the T3.4 design note
// and decision D2 of the plan): a variant of FUNCNAME of
// lib/decompress_template.h (Copyright 2016 Eric Biggers, MIT; see NOTICE)
// that stops at a symbol or block boundary when the input or the output
// space runs out, and continues from there on the next call.
//
//   - The fast loop is a verbatim copy of the one in decompress_generic.go
//     (TestStreamFastloopCopyIdentical keeps them identical); it only runs
//     with C's input and output margins, so it never needs to stop early.
//     On amd64 with BMI2 the same assembly fast loop runs first, through
//     the same hook as in decompress_generic.go.
//   - The generic ("slow") loop is C's, with zero padding past the end of
//     in as in C, but a symbol that consumed padding bits is undone and the
//     call returns asking for more input (C would decode garbage and fail
//     at the end of the stream).
//   - Block headers are parsed all-or-nothing: C's header code runs
//     unchanged, and if it consumed padding bits the state is restored to
//     the block start.
//   - Between calls the bit buffer holds fewer than 8 bits: whole unconsumed
//     bytes are given back to the caller (C's actual_in_nbytes accounting).
//
// Every change against C is marked "deviation from C:". The whole-buffer
// decoder (decompress_generic.go) is not used or changed by this file.

package deflate

import "encoding/binary"

// Block states of a suspended stream (streamState.state).
const (
	stHeader  uint8 = iota // at a block boundary: the next block header is read next
	stStored               // inside the body of a stored block (storedLeft bytes left)
	stHuffman              // inside a Huffman block, its decode tables built
)

// streamStop is why decompressStream returned successfully.
type streamStop uint8

const (
	// Every symbol whose bits are all in the input was decoded; the next
	// one (or the next block header) needs more input.
	stopNeedInput streamStop = iota
	// The next literal, match or stored byte does not fit in the output.
	stopOutputFull
	// The final block is finished: the stream is complete.
	stopEnd
)

// streamState is the state of a suspended stream between calls of
// decompressStream (with the decode tables, litlenTablebits and
// staticCodesLoaded, which live in the Decompressor). The zero value is the
// start of a stream.
type streamState struct {
	bitbuf     uint64 // the unconsumed bits of the last partly consumed byte
	bitsleft   uint32 // number of bits in bitbuf, < 8
	state      uint8  // stHeader, stStored or stHuffman
	isFinal    bool   // BFINAL of the current block
	storedLeft int    // stStored: bytes of the stored block still to copy
	stop       streamStop

	// slow: the fast loop is not used. Its bad-offset exit (the same code
	// as the whole-buffer decoder's) does not report the output decoded
	// before the invalid data; the generic loop does.
	slow bool
	// The last block start reached by the latest call (blkSet), in the
	// form of the state between calls: in[blkIn:] and out[:blkOut] with
	// the bits blkBitbuf/blkBitsleft. On libdeflateBadData the call is
	// decoded again from there (the decode tables of the block the call
	// started in may have been replaced by a later block's).
	blkSet      bool
	blkBitbuf   uint64
	blkBitsleft uint32
	blkIn       int
	blkOut      int
}

// decompressStream continues the DEFLATE stream described by s with the
// input in (which directly follows the input of the previous call) and
// decodes into out[outNext:]. out[:outNext] must be the stream's output so
// far (with a preset dictionary before it), or at least its last 32 KiB:
// matches refer back into it, and an offset beyond outNext is invalid (C's
// offset <= out_next - out). On success it returns the number of bytes of
// in consumed (actualIn; the bytes after it must be passed again on the
// next call), the new output position (actualOut) and the reason it
// stopped in s.stop. On libdeflateBadData actualIn is the number of bytes
// of in up to the last bit read of the invalid block header or match (with
// the bits held from the previous call, it is the input position just after
// the invalid item; 0 if the fast loop found it), actualOut is the output
// position before that item (0 too from the fast loop, which s.slow
// prevents), the block start record of s is set if a block start was
// reached, and the rest of s is undefined. It must not be called again
// after stopEnd.
//
// deviation from C: a resumable variant of FUNCNAME (C decodes whole
// buffers only); see the file comment.
func (d *Decompressor) decompressStream(s *streamState, in, out []byte, outNext int) (res libdeflateResult, actualIn, actualOut int) {
	outEnd := len(out)                                              // C: out_end
	outFastloopEnd := outEnd - min(outEnd, fastloopMaxBytesWritten) // C: out_fastloop_end

	// Input bitstream state; see deflate_decompress.c for documentation.
	inNext := 0                                               // C: in_next
	inEnd := len(in)                                          // C: in_end
	inFastloopEnd := inEnd - min(inEnd, fastloopMaxBytesRead) // C: in_fastloop_end
	if s.slow {
		inFastloopEnd = 0 // deviation from C: the fast loop is never entered
	}
	bitbuf := s.bitbuf     // C: bitbuf
	var savedBitbuf uint64 // C: saved_bitbuf
	bitsleft := s.bitsleft // C: bitsleft
	overreadCount := 0     // C: overread_count

	isFinalBlock := s.isFinal         // C: is_final_block
	var blockType uint32              // C: block_type
	var numLitlenSyms uint32          // C: num_litlen_syms
	var numOffsetSyms uint32          // C: num_offset_syms
	var litlenTablemask uint64        // C: litlen_tablemask
	var entry uint32                  // C: entry
	var i uint32                      // C: i (dynamic block header)
	var numExplicitPrecodeLens uint32 // C: num_explicit_precode_lens
	storedLeft := s.storedLeft

	// deviation from C: the bitstream state at the start of the block
	// header or symbol being decoded, restored when it cannot be completed
	// from in.
	var restoreBitbuf uint64
	var restoreBitsleft uint32
	var restoreInNext, restoreOverread int

	litlenTable := &d.litlenDecodeTable // C: d->u.litlen_decode_table
	offsetTable := &d.offsetDecodeTable // C: d->offset_decode_table

	// deviation from C: resume where the previous call stopped.
	s.blkSet = false
	switch s.state {
	case stStored:
		goto storedBody
	case stHuffman:
		goto haveDecodeTables
	}

nextBlock: // C: label next_block
	// Starting to read the next block.

	// deviation from C: the header is parsed all-or-nothing.
	restoreBitbuf, restoreBitsleft, restoreInNext, restoreOverread = bitbuf, bitsleft, inNext, overreadCount

	// deviation from C: record the block start, normalized as at suspend.
	{
		held := uint32(uint8(bitsleft)) - 8*uint32(overreadCount) // real bits held
		s.blkSet = true
		s.blkIn = inNext - int(held>>3)
		s.blkBitsleft = held & 7
		s.blkBitbuf = bitbuf & (1<<(held&7) - 1)
		s.blkOut = outNext
	}

	// C: REFILL_BITS()
	if inEnd-inNext >= 8 {
		bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
		inNext += 8 - 1
		inNext -= int((bitsleft >> 3) & 0x7)
		bitsleft |= maxBitsleft &^ 7
	} else {
		for uint8(bitsleft) < consumableNbits {
			if inNext != inEnd {
				bitbuf |= uint64(in[inNext]) << uint8(bitsleft)
				inNext++
			} else {
				overreadCount++
				// C's SAFETY_CHECK(overread_count <= 8) is dropped
				// here: it cannot fail (at most 7 padding bytes are
				// added to fewer than 8 held bits).
			}
			bitsleft += 8
		}
	}
	// C: end of REFILL_BITS()

	// BFINAL: 1 bit
	isFinalBlock = bitbuf&1 != 0

	// BTYPE: 2 bits
	blockType = uint32(bitbuf>>1) & 3

	if blockType == deflateBlocktypeDynamicHuffman {
		// Dynamic Huffman block.

		// Read the codeword length counts.
		numLitlenSyms = 257 + uint32(bitbuf>>3)&0x1f
		numOffsetSyms = 1 + uint32(bitbuf>>8)&0x1f
		numExplicitPrecodeLens = 4 + uint32(bitbuf>>13)&0xf

		d.staticCodesLoaded = false

		// Read the precode codeword lengths (C: the 64-bit branch, as in
		// decompress_generic.go).
		d.lens[deflatePrecodeLensPermutation[0]] = uint8(bitbuf>>17) & 7
		bitbuf >>= 20
		bitsleft -= 20
		// C: REFILL_BITS()
		if inEnd-inNext >= 8 {
			bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
			inNext += 8 - 1
			inNext -= int((bitsleft >> 3) & 0x7)
			bitsleft |= maxBitsleft &^ 7
		} else {
			for uint8(bitsleft) < consumableNbits {
				if inNext != inEnd {
					bitbuf |= uint64(in[inNext]) << uint8(bitsleft)
					inNext++
				} else {
					overreadCount++
					if !(overreadCount <= 8) { // SAFETY_CHECK(overread_count <= sizeof(bitbuf_t))
						// deviation from C: past the input: wait for more.
						goto headerNeedInput
					}
				}
				bitsleft += 8
			}
		}
		// C: end of REFILL_BITS()
		i = 1
		for { // C: do { ... } while (++i < num_explicit_precode_lens)
			d.lens[deflatePrecodeLensPermutation[i]] = uint8(bitbuf) & 7
			bitbuf >>= 3
			bitsleft -= 3
			i++
			if i >= numExplicitPrecodeLens {
				break
			}
		}
		for ; i < deflateNumPrecodeSyms; i++ {
			d.lens[deflatePrecodeLensPermutation[i]] = 0
		}

		// Build the decode table for the precode.
		if !d.buildPrecodeDecodeTable() { // SAFETY_CHECK
			goto headerBad
		}

		// Decode the litlen and offset codeword lengths.
		i = 0
		for ok := true; ok; ok = i < numLitlenSyms+numOffsetSyms { // C: do { ... } while (i < num_litlen_syms + num_offset_syms)
			var presym uint32   // C: presym
			var repVal uint8    // C: rep_val
			var repCount uint32 // C: rep_count

			if uint8(bitsleft) < deflateMaxPreCodewordLen+7 {
				// C: REFILL_BITS()
				if inEnd-inNext >= 8 {
					bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
					inNext += 8 - 1
					inNext -= int((bitsleft >> 3) & 0x7)
					bitsleft |= maxBitsleft &^ 7
				} else {
					for uint8(bitsleft) < consumableNbits {
						if inNext != inEnd {
							bitbuf |= uint64(in[inNext]) << uint8(bitsleft)
							inNext++
						} else {
							overreadCount++
							if !(overreadCount <= 8) { // SAFETY_CHECK(overread_count <= sizeof(bitbuf_t))
								// deviation from C: past the input: wait for more.
								goto headerNeedInput
							}
						}
						bitsleft += 8
					}
				}
				// C: end of REFILL_BITS()
			}

			// Decode the next precode symbol (PRECODE_TABLEBITS ==
			// DEFLATE_MAX_PRE_CODEWORD_LEN: no subtables).
			entry = d.precodeDecodeTable[bitbuf&(1<<deflateMaxPreCodewordLen-1)]
			bitbuf >>= entry & 63 // C: (u8)entry
			bitsleft -= entry     // optimization: subtract full entry
			presym = entry >> 16

			if presym < 16 {
				// Explicit codeword length
				d.lens[i] = uint8(presym)
				i++
				continue
			}

			// Run-length encoded codeword lengths (lens has room for the
			// worst-case overrun, DEFLATE_MAX_LENS_OVERRUN).
			if presym == 16 {
				// Repeat the previous length 3 - 6 times.
				if !(i != 0) { // SAFETY_CHECK(i != 0)
					goto headerBad
				}
				repVal = d.lens[i-1]
				repCount = 3 + uint32(bitbuf)&3
				bitbuf >>= 2
				bitsleft -= 2
				l := d.lens[i : i+6]
				l[0] = repVal
				l[1] = repVal
				l[2] = repVal
				l[3] = repVal
				l[4] = repVal
				l[5] = repVal
				i += repCount
			} else if presym == 17 {
				// Repeat zero 3 - 10 times.
				repCount = 3 + uint32(bitbuf)&7
				bitbuf >>= 3
				bitsleft -= 3
				l := d.lens[i : i+10]
				l[0] = 0
				l[1] = 0
				l[2] = 0
				l[3] = 0
				l[4] = 0
				l[5] = 0
				l[6] = 0
				l[7] = 0
				l[8] = 0
				l[9] = 0
				i += repCount
			} else {
				// Repeat zero 11 - 138 times.
				repCount = 11 + uint32(bitbuf)&0x7f
				bitbuf >>= 7
				bitsleft -= 7
				clear(d.lens[i : i+repCount]) // C: memset
				i += repCount
			}
		}

		// Unnecessary, but check this for consistency with zlib.
		if !(i == numLitlenSyms+numOffsetSyms) { // SAFETY_CHECK
			goto headerBad
		}

		// deviation from C: the lengths must come from real input.
		if int(uint8(bitsleft)) < 8*overreadCount {
			goto headerNeedInput
		}

	} else if blockType == deflateBlocktypeUncompressed {
		// Uncompressed block: copy 'len' bytes literally from the input
		// buffer to the output buffer.

		bitsleft -= 3 // for BTYPE and BFINAL

		// Align the bitstream to the next byte boundary (C: rewind
		// in_next by the refilled but unconsumed bytes, not counting
		// overread bytes).
		bitsleft = uint32(uint8(bitsleft))
		if !(overreadCount <= int(bitsleft>>3)) { // SAFETY_CHECK
			// deviation from C: the 3 header bits are past the input.
			goto headerNeedInput
		}
		inNext -= int(bitsleft>>3) - overreadCount
		overreadCount = 0
		bitbuf = 0
		bitsleft = 0

		if !(inEnd-inNext >= 4) { // SAFETY_CHECK
			// deviation from C: LEN and NLEN are past the input.
			goto headerNeedInput
		}
		ln := binary.LittleEndian.Uint16(in[inNext:])     // C: len
		nlen := binary.LittleEndian.Uint16(in[inNext+2:]) // C: nlen
		inNext += 4

		if !(ln == ^nlen) { // SAFETY_CHECK(len == (u16)~nlen)
			return libdeflateBadData, inNext, outNext
		}

		// deviation from C: no check of len against the output space and
		// the input left: the body is copied in pieces (storedBody).
		storedLeft = int(ln)
		goto storedBody

	} else {
		if !(blockType == deflateBlocktypeStaticHuffman) { // SAFETY_CHECK
			// BTYPE 3 is always real input: padding bits are zero, so
			// both BTYPE bits (and the BFINAL bit before them) are real.
			return libdeflateBadData, inNext - int((uint32(uint8(bitsleft))-3-8*uint32(overreadCount))>>3), outNext
		}

		// Static Huffman block: build the decode tables for the static
		// codes, unless they are already set up (static_codes_loaded).

		bitbuf >>= 3 // for BTYPE and BFINAL
		bitsleft -= 3

		// deviation from C: the 3 header bits must be real input.
		if int(uint8(bitsleft)) < 8*overreadCount {
			goto headerNeedInput
		}

		// C: if (d->static_codes_loaded) goto have_decode_tables; then the
		// static lens (static_codes.go).
		var loaded bool
		loaded, numLitlenSyms, numOffsetSyms = d.loadStaticCodes()
		if loaded {
			goto haveDecodeTables // C: goto have_decode_tables
		}
	}

	// Decompressing a Huffman block (either dynamic or static).

	if !d.buildOffsetDecodeTable(numLitlenSyms, numOffsetSyms) { // SAFETY_CHECK
		goto headerBad
	}
	if !d.buildLitlenDecodeTable(numLitlenSyms, numOffsetSyms) { // SAFETY_CHECK
		goto headerBad
	}
haveDecodeTables: // C: label have_decode_tables
	litlenTablemask = 1<<d.litlenTablebits - 1

	// amd64 with BMI2: the assembly runs the iterations of the fast loop
	// below, as in decompress_generic.go (see the comment there), from the
	// same state and under the same condition; its exits are the Go loop's:
	// the loop's state when the condition becomes false, the end of the
	// block, or a bad match offset (0, 0 as the Go loop's exit). Never in
	// the reader's rerun: s.slow sets inFastloopEnd = 0.
	for useFastloopBMI2 && inNext < inFastloopEnd && outNext < outFastloopEnd {
		var status int
		bitbuf, bitsleft, inNext, outNext, status = decompressFastloopBMI2(in, out, litlenTable, offsetTable,
			d.litlenTablebits, inFastloopEnd, min(outFastloopEnd, outNext+fastloopChunk), bitbuf, bitsleft,
			inNext, outNext)
		if status == fastloopEndOfBlock {
			goto blockDone // C: goto block_done
		}
		if status == fastloopBadData {
			return libdeflateBadData, 0, 0 // C: SAFETY_CHECK(offset <= out_next - (const u8 *)out)
		}
	}

	// The fast loop: a verbatim copy of decompress_generic.go's (see the
	// comments there). The margins inFastloopEnd and outFastloopEnd are C's,
	// relative to this call's in and out, so it never reads past in and
	// never decodes padding bits.
	if inNext >= inFastloopEnd || outNext >= outFastloopEnd {
		goto genericLoop // C: goto generic_loop
	}
	// C: REFILL_BITS_IN_FASTLOOP()
	bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
	inNext += 8 - 1
	inNext -= int((bitsleft >> 3) & 0x7)
	bitsleft |= maxBitsleft &^ 7
	// C: end of REFILL_BITS_IN_FASTLOOP()
	entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
	// C: do { ... } while (in_next < in_fastloop_end && out_next < out_fastloop_end);
	// 'continue' runs the condition, as in C.
	for ok := true; ok; ok = inNext < inFastloopEnd && outNext < outFastloopEnd {
		var length, offset, lit uint32 // C: length, offset, lit
		var src, dst int               // C: src, dst

		// Consume the bits for the litlen decode table entry. Save
		// the original bitbuf for later, in case the extra match
		// length bits need to be extracted from it.
		savedBitbuf = bitbuf
		bitbuf >>= entry & 63 // C: (u8)entry
		bitsleft -= entry     // optimization: subtract full entry

		// Begin by checking for a "fast" literal, i.e. a literal that
		// doesn't need a subtable.
		if entry&huffdecLiteral != 0 {
			// On 64-bit platforms, up to 2 extra fast literals are
			// decoded in addition to the primary item. C: both
			// CAN_CONSUME_AND_THEN_PRELOAD(2 * LITLEN_TABLEBITS +
			// LENGTH_MAXBITS, OFFSET_TABLEBITS) and
			// CAN_CONSUME_AND_THEN_PRELOAD(2 * LITLEN_TABLEBITS +
			// DEFLATE_MAX_LITLEN_CODEWORD_LEN, LITLEN_TABLEBITS) are
			// true; the else branch (one literal, then preload and
			// refill) is not ported.
			//
			// Note: fastloopMaxBytesWritten and fastloopMaxBytesRead
			// must be updated if the number of extra literals decoded
			// here is changed.

			// 1st extra fast literal
			lit = entry >> 16
			entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
			savedBitbuf = bitbuf
			bitbuf >>= entry & 63
			bitsleft -= entry
			out[outNext] = byte(lit)
			outNext++
			if entry&huffdecLiteral != 0 {
				// 2nd extra fast literal
				lit = entry >> 16
				entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
				savedBitbuf = bitbuf
				bitbuf >>= entry & 63
				bitsleft -= entry
				out[outNext] = byte(lit)
				outNext++
				if entry&huffdecLiteral != 0 {
					// Another fast literal, but this one is in
					// lieu of the primary item, so it doesn't
					// count as one of the extras.
					lit = entry >> 16
					entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
					// C: REFILL_BITS_IN_FASTLOOP()
					bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
					inNext += 8 - 1
					inNext -= int((bitsleft >> 3) & 0x7)
					bitsleft |= maxBitsleft &^ 7
					// C: end of REFILL_BITS_IN_FASTLOOP()
					out[outNext] = byte(lit)
					outNext++
					continue
				}
			}
		}

		// It's not a literal entry, so it can be a length entry, a
		// subtable pointer entry, or an end-of-block entry. Detect the
		// two unlikely cases by testing the HUFFDEC_EXCEPTIONAL flag.
		if entry&huffdecExceptional != 0 {
			// Subtable pointer or end-of-block entry

			if entry&huffdecEndOfBlock != 0 {
				goto blockDone // C: goto block_done
			}

			// A subtable is required. Load and consume the subtable
			// entry. The subtable entry can be of any type: literal,
			// length, or end-of-block.
			entry = litlenTable[(entry>>16)+uint32(bitbuf&(1<<((entry>>8)&0x3f)-1))]
			savedBitbuf = bitbuf
			bitbuf >>= entry & 63
			bitsleft -= entry

			// C: the refill here is only for 32-bit platforms
			// (!CAN_CONSUME_AND_THEN_PRELOAD(...) is false).
			if entry&huffdecLiteral != 0 {
				// Decode a literal that required a subtable.
				lit = entry >> 16
				entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
				// C: REFILL_BITS_IN_FASTLOOP()
				bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
				inNext += 8 - 1
				inNext -= int((bitsleft >> 3) & 0x7)
				bitsleft |= maxBitsleft &^ 7
				// C: end of REFILL_BITS_IN_FASTLOOP()
				out[outNext] = byte(lit)
				outNext++
				continue
			}
			if entry&huffdecEndOfBlock != 0 {
				goto blockDone // C: goto block_done
			}
			// Else, it's a length that required a subtable.
		}

		// Decode the match length: the length base value associated
		// with the litlen symbol (which we extract from the decode
		// table entry), plus the extra length bits. The extra length
		// bits need not be consumed here: they were included in the
		// bits consumed by the entry earlier. Too-long matches need
		// not be checked here: the fastloop condition ensures the
		// output buffer has room for a max-length match.
		length = entry >> 16
		length += uint32((savedBitbuf & (1<<(entry&63) - 1)) >> ((entry >> 8) & 63)) // C: EXTRACT_VARBITS8(saved_bitbuf, entry) >> (u8)(entry >> 8)

		// Decode the match offset. There are enough "preloadable"
		// bits remaining to preload the offset decode table entry,
		// but a refill might be needed before consuming it.
		entry = offsetTable[bitbuf&(1<<offsetTablebits-1)]
		// C: CAN_CONSUME_AND_THEN_PRELOAD(OFFSET_MAXBITS,
		// LITLEN_TABLEBITS) is true: decoding a match offset on a
		// 64-bit platform. We may need to refill once, but then we
		// can decode the whole offset and preload the next litlen
		// table entry. (The 32-bit else branch is not ported.)
		if entry&huffdecExceptional != 0 {
			// Offset codeword requires a subtable
			if uint8(bitsleft) < offsetMaxbits+litlenTablebits-preloadSlack {
				// C: REFILL_BITS_IN_FASTLOOP()
				bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
				inNext += 8 - 1
				inNext -= int((bitsleft >> 3) & 0x7)
				bitsleft |= maxBitsleft &^ 7
				// C: end of REFILL_BITS_IN_FASTLOOP()
			}
			bitbuf >>= offsetTablebits
			bitsleft -= offsetTablebits
			entry = offsetTable[(entry>>16)+uint32(bitbuf&(1<<((entry>>8)&0x3f)-1))]
		} else if uint8(bitsleft) < offsetMaxfastbits+litlenTablebits-preloadSlack {
			// C: REFILL_BITS_IN_FASTLOOP()
			bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
			inNext += 8 - 1
			inNext -= int((bitsleft >> 3) & 0x7)
			bitsleft |= maxBitsleft &^ 7
			// C: end of REFILL_BITS_IN_FASTLOOP()
		}
		savedBitbuf = bitbuf
		bitbuf >>= entry & 63
		bitsleft -= entry // optimization: subtract full entry
		offset = entry >> 16
		offset += uint32((savedBitbuf & (1<<(entry&63) - 1)) >> ((entry >> 8) & 63)) // C: EXTRACT_VARBITS8(saved_bitbuf, entry) >> (u8)(entry >> 8)

		// Validate the match offset; needed even in the fastloop.
		if !(int(offset) <= outNext) { // SAFETY_CHECK(offset <= out_next - (const u8 *)out)
			return libdeflateBadData, 0, 0
		}
		src = outNext - int(offset)
		dst = outNext
		outNext += int(length)

		// Before starting to issue the instructions to copy the match,
		// refill the bitbuffer and preload the litlen decode table
		// entry for the next loop iteration. (C: the refill before the
		// preload is only for 32-bit platforms: the
		// !CAN_CONSUME_AND_THEN_PRELOAD(...) test is false.)
		entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
		// C: REFILL_BITS_IN_FASTLOOP()
		bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
		inNext += 8 - 1
		inNext -= int((bitsleft >> 3) & 0x7)
		bitsleft |= maxBitsleft &^ 7
		// C: end of REFILL_BITS_IN_FASTLOOP()

		// Copy the match. The fastest method on most CPUs is a
		// word-at-a-time copy, unconditionally copying about 5 words
		// since this is enough for most matches without being too
		// much.
		//
		// The normal word-at-a-time copy works for offset >=
		// WORDBYTES, which is most cases. The case of offset == 1 is
		// also common and is worth optimizing for, since it is just
		// RLE encoding of the previous byte.
		//
		// Writing past the match 'length' is allowed here, since it's
		// been ensured there is enough output space left for a slight
		// overrun. fastloopMaxBytesWritten needs to be updated if the
		// maximum possible overrun here is changed. Each word is
		// loaded before it is stored, as in C (load_word_unaligned,
		// store_word_unaligned), so overlapping copies give the same
		// bytes.
		// (C: UNALIGNED_ACCESS_IS_FAST is 1 in the ported
		// configuration; the bytewise else branch is not ported.)
		if offset >= wordBytes {
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += wordBytes
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += wordBytes
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += wordBytes
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += wordBytes
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += wordBytes
			dst += wordBytes
			for dst < outNext {
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += wordBytes
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += wordBytes
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += wordBytes
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += wordBytes
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += wordBytes
				dst += wordBytes
			}
		} else if offset == 1 {
			// C: "This part tends to get auto-vectorized, so keep it
			// copying a multiple of 16 bytes at a time."
			v := 0x0101010101010101 * uint64(out[src]) // C: machine_word_t v
			binary.LittleEndian.PutUint64(out[dst:], v)
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], v)
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], v)
			dst += wordBytes
			binary.LittleEndian.PutUint64(out[dst:], v)
			dst += wordBytes
			for dst < outNext {
				binary.LittleEndian.PutUint64(out[dst:], v)
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], v)
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], v)
				dst += wordBytes
				binary.LittleEndian.PutUint64(out[dst:], v)
				dst += wordBytes
			}
		} else {
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += int(offset)
			dst += int(offset)
			binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
			src += int(offset)
			dst += int(offset)
			for { // C: do { ... } while (dst < out_next)
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += int(offset)
				dst += int(offset)
				binary.LittleEndian.PutUint64(out[dst:], binary.LittleEndian.Uint64(out[src:]))
				src += int(offset)
				dst += int(offset)
				if dst >= outNext {
					break
				}
			}
		}
	}

	// The generic loop for decoding literals and matches near the end of in
	// or out: C's generic_loop, resumable at every symbol.
genericLoop: // C: label generic_loop
	for {
		var length, offset uint32 // C: length, offset
		var src, dst int          // C: src, dst

		// C: REFILL_BITS()
		if inEnd-inNext >= 8 {
			bitbuf |= binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)
			inNext += 8 - 1
			inNext -= int((bitsleft >> 3) & 0x7)
			bitsleft |= maxBitsleft &^ 7
		} else {
			for uint8(bitsleft) < consumableNbits {
				if inNext != inEnd {
					bitbuf |= uint64(in[inNext]) << uint8(bitsleft)
					inNext++
				} else {
					// deviation from C: no overread limit: a symbol that
					// consumed padding is undone below, so the padding
					// bytes held never exceed the 7 that fit beside the
					// real bits (SAFETY_CHECK(overread_count <= 8)
					// cannot fail).
					overreadCount++
				}
				bitsleft += 8
			}
		}
		// C: end of REFILL_BITS()

		// deviation from C: remember the symbol start.
		restoreBitbuf, restoreBitsleft, restoreInNext, restoreOverread = bitbuf, bitsleft, inNext, overreadCount

		entry = litlenTable[bitbuf&litlenTablemask&(1<<litlenTablebits-1)]
		savedBitbuf = bitbuf
		bitbuf >>= entry & 63
		bitsleft -= entry
		if entry&huffdecSubtablePointer != 0 {
			entry = litlenTable[(entry>>16)+uint32(bitbuf&(1<<((entry>>8)&0x3f)-1))]
			savedBitbuf = bitbuf
			bitbuf >>= entry & 63
			bitsleft -= entry
		}
		length = entry >> 16
		if entry&huffdecLiteral != 0 {
			// deviation from C: the literal must be real input and fit.
			if int(uint8(bitsleft)) < 8*overreadCount {
				goto symbolNeedInput
			}
			if outNext == outEnd {
				goto symbolOutputFull
			}
			out[outNext] = byte(length)
			outNext++
			continue
		}
		if entry&huffdecEndOfBlock != 0 {
			// deviation from C: the end-of-block code must be real input.
			if int(uint8(bitsleft)) < 8*overreadCount {
				goto symbolNeedInput
			}
			goto blockDone // C: goto block_done
		}
		length += uint32((savedBitbuf & (1<<(entry&63) - 1)) >> ((entry >> 8) & 63)) // C: EXTRACT_VARBITS8(saved_bitbuf, entry) >> (u8)(entry >> 8)

		// deviation from C: the output space is checked after the whole
		// match is decoded (below), not here.

		// C: if (!CAN_CONSUME(LENGTH_MAXBITS + OFFSET_MAXBITS))
		// REFILL_BITS(); is false (no refill).
		entry = offsetTable[bitbuf&(1<<offsetTablebits-1)]
		if entry&huffdecExceptional != 0 {
			bitbuf >>= offsetTablebits
			bitsleft -= offsetTablebits
			entry = offsetTable[(entry>>16)+uint32(bitbuf&(1<<((entry>>8)&0x3f)-1))]
		}
		offset = entry >> 16
		offset += uint32((bitbuf & (1<<(entry&63) - 1)) >> ((entry >> 8) & 63)) // C: EXTRACT_VARBITS8(bitbuf, entry) >> (u8)(entry >> 8)
		bitbuf >>= entry & 63
		bitsleft -= entry

		// deviation from C: the match must be real input and fit.
		if int(uint8(bitsleft)) < 8*overreadCount {
			goto symbolNeedInput
		}
		if int(length) > outEnd-outNext {
			goto symbolOutputFull
		}

		if !(int(offset) <= outNext) { // SAFETY_CHECK(offset <= out_next - (const u8 *)out)
			return libdeflateBadData, inNext - int((uint32(uint8(bitsleft))-8*uint32(overreadCount))>>3), outNext
		}
		src = outNext - int(offset)
		dst = outNext
		outNext += int(length)

		// STATIC_ASSERT(DEFLATE_MIN_MATCH_LEN == 3)
		out[dst] = out[src]
		dst++
		src++
		out[dst] = out[src]
		dst++
		src++
		for { // C: do { *dst++ = *src++; } while (dst < out_next)
			out[dst] = out[src]
			dst++
			src++
			if dst >= outNext {
				break
			}
		}
	}

	// deviation from C: the body of a stored block, copied as far as the
	// input and the output space allow.
storedBody:
	{
		n := min(storedLeft, inEnd-inNext, outEnd-outNext)
		copy(out[outNext:outNext+n], in[inNext:inNext+n]) // C: memcpy
		inNext += n
		outNext += n
		storedLeft -= n
	}
	if storedLeft != 0 {
		s.state = stStored
		s.storedLeft = storedLeft
		// Output full is preferred when the input ran out too: the caller
		// returns the output before reading more input.
		s.stop = stopNeedInput
		if outNext == outEnd {
			s.stop = stopOutputFull
		}
		goto suspend
	}

blockDone: // C: label block_done
	// Finished decoding a block.

	if !isFinalBlock {
		goto nextBlock // C: goto next_block
	}

	// That was the last block. (C's final SAFETY_CHECK(overread_count <=
	// bitsleft >> 3) holds: no consumed bit is padding.)
	s.stop = stopEnd
	goto suspend

	// deviation from C: the exits that stop at a boundary.
symbolNeedInput:
	s.stop = stopNeedInput
	goto symbolRestore
symbolOutputFull:
	s.stop = stopOutputFull
symbolRestore:
	bitbuf, bitsleft, inNext, overreadCount = restoreBitbuf, restoreBitsleft, restoreInNext, restoreOverread
	s.state = stHuffman
	goto suspend

headerBad:
	// A SAFETY_CHECK of the block header failed: an error only if every
	// bit it consumed is real input; otherwise wait for more input.
	if int(uint8(bitsleft)) >= 8*overreadCount {
		return libdeflateBadData, inNext - int((uint32(uint8(bitsleft))-8*uint32(overreadCount))>>3), outNext
	}
headerNeedInput:
	bitbuf, bitsleft, inNext, overreadCount = restoreBitbuf, restoreBitsleft, restoreInNext, restoreOverread
	s.state = stHeader
	s.stop = stopNeedInput

suspend:
	// Give back the refilled but unconsumed whole bytes, as C computes
	// actual_in_nbytes (in_next -= (bitsleft >> 3) - overread_count), so
	// that fewer than 8 bits stay buffered.
	bitsleft = uint32(uint8(bitsleft)) - 8*uint32(overreadCount)
	inNext -= int(bitsleft >> 3)
	bitsleft &= 7
	s.bitbuf = bitbuf & (1<<bitsleft - 1)
	s.bitsleft = bitsleft
	s.isFinal = isFinalBlock
	return libdeflateSuccess, inNext, outNext
}
