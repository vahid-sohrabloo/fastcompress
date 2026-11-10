// Port of libdeflate v1.26 lib/decompress_template.h (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): FUNCNAME, compiled by deflate_decompress.c as
// deflate_decompress_default, with the bitstream macros of
// lib/deflate_decompress.c (REFILL_BITS, REFILL_BITS_BRANCHLESS,
// REFILL_BITS_IN_FASTLOOP, SAFETY_CHECK) expanded in place.
//
// Pointers become offsets: in_next/in_end index in, out_next/out_end index
// out. The C gotos map to Go labels of the same names (next_block is the
// outer for loop, have_decode_tables, generic_loop and block_done are
// labels); each is marked with a "C: goto"/"C: label" comment. Every
// SAFETY_CHECK is "if !(expr) { return libdeflateBadData }".
//
// bitsleft keeps C's micro-optimization: bits 8 and higher may hold garbage
// ('bitsleft -= entry' subtracts the whole decode table entry), so it is
// read through uint8(bitsleft) wherever C casts it to u8, and truncated with
// bitsleft = uint32(uint8(bitsleft)) where C does. Shift amounts that C
// writes as (u8)x are written x&63 where x's low byte is known to be < 64
// (refill: uint8(bitsleft) <= MAX_BITSLEFT = 63; entries: the low byte is at
// most OFFSET_MAXBITS = 28), so Go emits a plain shift; the values are
// identical.

package deflate

import "encoding/binary"

// decompressDefault decompresses the DEFLATE stream at the start of in into
// out. With wantIn it returns the number of input bytes the stream occupies
// (C: actual_in_nbytes_ret != NULL); with wantOut the number of bytes written
// (C: actual_out_nbytes_ret != NULL), otherwise the output must fill out
// exactly or libdeflateShortOutput is returned. The counts are 0 unless the
// result is libdeflateSuccess.
// port of FUNCNAME (decompress_template.h) as deflate_decompress_default
func (d *Decompressor) decompressDefault(in, out []byte, wantIn, wantOut bool) (res libdeflateResult, actualIn, actualOut int) {
	outNext := 0                                                    // C: out_next
	outEnd := len(out)                                              // C: out_end
	outFastloopEnd := outEnd - min(outEnd, fastloopMaxBytesWritten) // C: out_fastloop_end

	// Input bitstream state; see deflate_decompress.c for documentation.
	inNext := 0                                               // C: in_next
	inEnd := len(in)                                          // C: in_end
	inFastloopEnd := inEnd - min(inEnd, fastloopMaxBytesRead) // C: in_fastloop_end
	var bitbuf uint64                                         // C: bitbuf
	var savedBitbuf uint64                                    // C: saved_bitbuf
	var bitsleft uint32                                       // C: bitsleft
	overreadCount := 0                                        // C: overread_count

	var isFinalBlock bool             // C: is_final_block
	var blockType uint32              // C: block_type
	var numLitlenSyms uint32          // C: num_litlen_syms
	var numOffsetSyms uint32          // C: num_offset_syms
	var litlenTablemask uint64        // C: litlen_tablemask
	var entry uint32                  // C: entry
	var i uint32                      // C: i (dynamic block header)
	var numExplicitPrecodeLens uint32 // C: num_explicit_precode_lens

	litlenTable := &d.litlenDecodeTable // C: d->u.litlen_decode_table
	offsetTable := &d.offsetDecodeTable // C: d->offset_decode_table

	for { // C: label next_block
		// Starting to read the next block.

		// STATIC_ASSERT(CAN_CONSUME(1 + 2 + 5 + 5 + 4 + 3)): decompress.go.
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
						return libdeflateBadData, 0, 0
					}
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

			// Read the precode codeword lengths.
			//
			// A 64-bit bitbuffer is just one bit too small to hold the
			// maximum number of precode lens, so to minimize branches we
			// merge one len with the previous fields.
			// C: if (CAN_CONSUME(3 * (DEFLATE_NUM_PRECODE_SYMS - 1))) is
			// true; the else branch (bitbuf >>= 17 and a refill check per
			// len) is for 32-bit bitbufs and is not ported.
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
							return libdeflateBadData, 0, 0
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
				return libdeflateBadData, 0, 0
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
									return libdeflateBadData, 0, 0
								}
							}
							bitsleft += 8
						}
					}
					// C: end of REFILL_BITS()
				}

				// The code below assumes that the precode decode table
				// doesn't have any subtables (PRECODE_TABLEBITS ==
				// DEFLATE_MAX_PRE_CODEWORD_LEN).

				// Decode the next precode symbol.
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

				// Run-length encoded codeword lengths.
				//
				// The repeat count is not checked against the number of
				// elements here: lens has room for the worst-case overrun
				// (DEFLATE_MAX_LENS_OVERRUN, 138 zeroes when only 1 length
				// was remaining). For presyms 16 and 17 the maximum number
				// of entries is always written, which avoids branches.
				if presym == 16 {
					// Repeat the previous length 3 - 6 times.
					if !(i != 0) { // SAFETY_CHECK(i != 0)
						return libdeflateBadData, 0, 0
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
				return libdeflateBadData, 0, 0
			}

		} else if blockType == deflateBlocktypeUncompressed {
			// Uncompressed block: copy 'len' bytes literally from the
			// input buffer to the output buffer.

			bitsleft -= 3 // for BTYPE and BFINAL

			// Align the bitstream to the next byte boundary. This means
			// the next byte boundary as if we were reading a byte at a
			// time. Therefore, rewind in_next by any bytes that have been
			// refilled but not actually consumed yet (not counting
			// overread bytes, which don't increment in_next).
			bitsleft = uint32(uint8(bitsleft))
			if !(overreadCount <= int(bitsleft>>3)) { // SAFETY_CHECK
				return libdeflateBadData, 0, 0
			}
			inNext -= int(bitsleft>>3) - overreadCount
			overreadCount = 0
			bitbuf = 0
			bitsleft = 0

			if !(inEnd-inNext >= 4) { // SAFETY_CHECK
				return libdeflateBadData, 0, 0
			}
			ln := binary.LittleEndian.Uint16(in[inNext:])     // C: len
			nlen := binary.LittleEndian.Uint16(in[inNext+2:]) // C: nlen
			inNext += 4

			if !(ln == ^nlen) { // SAFETY_CHECK(len == (u16)~nlen)
				return libdeflateBadData, 0, 0
			}
			if int(ln) > outEnd-outNext {
				return libdeflateInsufficientSpace, 0, 0
			}
			if !(int(ln) <= inEnd-inNext) { // SAFETY_CHECK
				return libdeflateBadData, 0, 0
			}

			copy(out[outNext:outNext+int(ln)], in[inNext:inNext+int(ln)]) // C: memcpy
			inNext += int(ln)
			outNext += int(ln)

			goto blockDone // C: goto block_done

		} else {
			if !(blockType == deflateBlocktypeStaticHuffman) { // SAFETY_CHECK
				return libdeflateBadData, 0, 0
			}

			// Static Huffman block: build the decode tables for the
			// static codes. Skip doing so if the tables are already set
			// up from an earlier static block; this speeds up
			// decompression of degenerate input of many empty or very
			// short static blocks.
			//
			// Afterwards, the remainder is the same as decompressing a
			// dynamic Huffman block.

			bitbuf >>= 3 // for BTYPE and BFINAL
			bitsleft -= 3

			// C: if (d->static_codes_loaded) goto have_decode_tables;
			// then the static lens, num_litlen_syms = 288,
			// num_offset_syms = 32 (static_codes.go).
			var loaded bool
			loaded, numLitlenSyms, numOffsetSyms = d.loadStaticCodes()
			if loaded {
				goto haveDecodeTables // C: goto have_decode_tables
			}
		}

		// Decompressing a Huffman block (either dynamic or static).

		if !d.buildOffsetDecodeTable(numLitlenSyms, numOffsetSyms) { // SAFETY_CHECK
			return libdeflateBadData, 0, 0
		}
		if !d.buildLitlenDecodeTable(numLitlenSyms, numOffsetSyms) { // SAFETY_CHECK
			return libdeflateBadData, 0, 0
		}
	haveDecodeTables: // C: label have_decode_tables
		litlenTablemask = 1<<d.litlenTablebits - 1

		// amd64 with BMI2 (C: deflate_decompress_bmi2): the assembly runs
		// the iterations of the fast loop below, under the same loop
		// condition, and returns the loop's state when the condition
		// becomes false (the Go loop below then goes to generic_loop), at
		// the end of the block, or at a bad match offset. One call's output
		// end is lowered to outNext + fastloopChunk (it then returns
		// fastloopContinue at an iteration boundary) so that a huge block
		// does not hold off a GC stop-the-world; it is called again while
		// the real loop condition holds.
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

		// This is the "fastloop" for decoding literals and matches. It does
		// bounds checks on in_next and out_next in the loop conditions so
		// that additional bounds checks aren't needed inside the loop body.
		// (Go still checks the slice accesses; the C margins guarantee
		// they pass.)
		//
		// To reduce latency, the bitbuffer is refilled and the next litlen
		// decode table entry is preloaded before each loop iteration.
		//
		// Main table lookups are masked with litlenTablemask and then with
		// the constant 1<<LITLEN_TABLEBITS - 1 (a no-op, litlen_tablebits
		// <= LITLEN_TABLEBITS) so the index is provably in range.
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

		// This is the generic loop for decoding literals and matches. It
		// handles cases where in_next and out_next are close to the end of
		// their respective buffers. Usually this loop isn't performance-
		// critical, as most time is spent in the fastloop above instead,
		// so some optimizations are omitted here in favor of smaller code.
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
						overreadCount++
						if !(overreadCount <= 8) { // SAFETY_CHECK(overread_count <= sizeof(bitbuf_t))
							return libdeflateBadData, 0, 0
						}
					}
					bitsleft += 8
				}
			}
			// C: end of REFILL_BITS()
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
				if outNext == outEnd {
					return libdeflateInsufficientSpace, 0, 0
				}
				out[outNext] = byte(length)
				outNext++
				continue
			}
			if entry&huffdecEndOfBlock != 0 {
				goto blockDone // C: goto block_done
			}
			length += uint32((savedBitbuf & (1<<(entry&63) - 1)) >> ((entry >> 8) & 63)) // C: EXTRACT_VARBITS8(saved_bitbuf, entry) >> (u8)(entry >> 8)
			if int(length) > outEnd-outNext {
				return libdeflateInsufficientSpace, 0, 0
			}

			// C: if (!CAN_CONSUME(LENGTH_MAXBITS + OFFSET_MAXBITS))
			// REFILL_BITS(); is false (no refill).
			entry = offsetTable[bitbuf&(1<<offsetTablebits-1)]
			if entry&huffdecExceptional != 0 {
				bitbuf >>= offsetTablebits
				bitsleft -= offsetTablebits
				entry = offsetTable[(entry>>16)+uint32(bitbuf&(1<<((entry>>8)&0x3f)-1))]
				// C: if (!CAN_CONSUME(OFFSET_MAXBITS)) REFILL_BITS();
				// is false (no refill).
			}
			offset = entry >> 16
			offset += uint32((bitbuf & (1<<(entry&63) - 1)) >> ((entry >> 8) & 63)) // C: EXTRACT_VARBITS8(bitbuf, entry) >> (u8)(entry >> 8)
			bitbuf >>= entry & 63
			bitsleft -= entry

			if !(int(offset) <= outNext) { // SAFETY_CHECK(offset <= out_next - (const u8 *)out)
				return libdeflateBadData, 0, 0
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

	blockDone: // C: label block_done
		// Finished decoding a block.

		if !isFinalBlock {
			continue // C: goto next_block
		}
		break
	}

	// That was the last block.

	bitsleft = uint32(uint8(bitsleft))

	// If any of the implicit appended zero bytes were consumed (not just
	// refilled) before hitting end of stream, then the data is bad.
	if !(overreadCount <= int(bitsleft>>3)) { // SAFETY_CHECK
		return libdeflateBadData, 0, 0
	}

	// Optionally return the actual number of bytes consumed.
	if wantIn {
		// Don't count bytes that were refilled but not consumed.
		inNext -= int(bitsleft>>3) - overreadCount

		actualIn = inNext
	}

	// Optionally return the actual number of bytes written.
	if wantOut {
		actualOut = outNext
	} else {
		if outNext != outEnd {
			return libdeflateShortOutput, 0, 0
		}
	}
	return libdeflateSuccess, actualIn, actualOut
}
