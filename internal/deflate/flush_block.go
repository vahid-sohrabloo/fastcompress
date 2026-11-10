// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): struct deflate_sequence, deflate_flush_block
// (block type choice by exact cost, the output space check, the
// uncompressed / static / dynamic block writers, WRITE_MATCH, the
// literal-run loop and the near-optimal minimum-cost path output),
// deflate_finish_block, deflate_begin_sequences,
// deflate_choose_literal and deflate_choose_match.
//
// libdeflate v1.26 has no separate deflate_write_block_header /
// deflate_write_huffman_header / deflate_write_sequences functions any more:
// everything is inline in deflate_flush_block, and so it is here.

package deflate

import (
	"encoding/binary"
	"math"
)

// deflateSequence represents a run of literals followed by a match or
// end-of-block. Sequences are stored until the block's Huffman codes are
// known; the literals are read from the uncompressed data.
// C: struct deflate_sequence
type deflateSequence struct {
	// Bits 0..22: the number of literals in this run (may be 0, at most
	// MAX_BLOCK_LENGTH). Bits 23..31: the length of the match that follows
	// the literals, or 0 if this literal run is the last of the block.
	// C: litrunlen_and_length
	litrunlenAndLength uint32

	// The match offset, if a match follows the literals. C: offset (u16)
	offset uint16

	// The offset slot of that match. C: offset_slot (u16)
	offsetSlot uint16
}

// C: SEQ_LENGTH_SHIFT, SEQ_LITRUNLEN_MASK
const (
	seqLengthShift   = 23
	seqLitrunlenMask = (uint32(1) << seqLengthShift) - 1
)

// Compile-time checks of the CAN_BUFFER conditions deflate_flush_block
// branches on. The port always uses the 64-bit bit buffer (outBitbufNbits
// 63), for which every one of them is true, so only C's "true" branches
// exist here:
//   - WRITE_MATCH: CAN_BUFFER(MAX_LITLEN_CODEWORD_LEN +
//     DEFLATE_MAX_EXTRA_LENGTH_BITS + MAX_OFFSET_CODEWORD_LEN +
//     DEFLATE_MAX_EXTRA_OFFSET_BITS) (no FLUSH_BITS between the length and
//     the offset symbol) and CAN_BUFFER(MAX_OFFSET_CODEWORD_LEN +
//     DEFLATE_MAX_EXTRA_OFFSET_BITS) (none before the extra offset bits);
//   - dynamic header: CAN_BUFFER(1 + 2 + 5 + 5 + 4 + 3),
//     CAN_BUFFER(3 * (DEFLATE_NUM_PRECODE_SYMS - 1)) (the first precode
//     length goes with the header fields, the other 18 in one go) and
//     CAN_BUFFER(MAX_PRE_CODEWORD_LEN + 7);
//   - literal runs: CAN_BUFFER(4 * MAX_LITLEN_CODEWORD_LEN) (4 literals
//     per FLUSH_BITS);
//   - STATIC_ASSERT(MAX_BLOCK_LENGTH <= SEQ_LITRUNLEN_MASK).
const (
	_ = uint(outBitbufNbits - (7 + maxLitlenCodewordLen + deflateMaxExtraLengthBits +
		maxOffsetCodewordLen + deflateMaxExtraOffsetBits))
	_ = uint(outBitbufNbits - (7 + maxOffsetCodewordLen + deflateMaxExtraOffsetBits))
	_ = uint(outBitbufNbits - (7 + 1 + 2 + 5 + 5 + 4 + 3))
	_ = uint(outBitbufNbits - (7 + 3*(deflateNumPrecodeSyms-1)))
	_ = uint(outBitbufNbits - (7 + maxPreCodewordLen + 7))
	_ = uint(outBitbufNbits - (7 + 4*maxLitlenCodewordLen))
	_ = uint(seqLitrunlenMask - maxBlockLength)
)

// beginSequences starts a new block's sequence list at firstSeq and
// clears the symbol frequencies. port of deflate_begin_sequences
func (c *Compressor) beginSequences(firstSeq *deflateSequence) {
	c.resetSymbolFrequencies()
	firstSeq.litrunlenAndLength = 0
}

// chooseLiteral is chooseLiteralStats with gather_split_stats false.
// port of deflate_choose_literal (gather_split_stats = false)
func (c *Compressor) chooseLiteral(literal uint32, seq *deflateSequence) {
	c.chooseLiteralStats(literal, false, seq)
}

// chooseLiteralStats appends a literal to the current sequence's literal
// run and counts it; with gatherSplitStats it is also observed by the
// block splitting heuristic (c.splitStats).
// port of deflate_choose_literal
func (c *Compressor) chooseLiteralStats(literal uint32, gatherSplitStats bool, seq *deflateSequence) {
	c.freqs.litlen[literal&0xFF]++ // literal < 256: the mask only drops the bounds check

	if gatherSplitStats {
		c.splitStats.observeLiteral(uint8(literal))
	}

	seq.litrunlenAndLength++
}

// chooseMatch is chooseMatchStats with gather_split_stats false.
// port of deflate_choose_match (gather_split_stats = false)
func (c *Compressor) chooseMatch(length, offset uint32, seqs []deflateSequence, i int) int {
	return c.chooseMatchStats(length, offset, false, seqs, i)
}

// chooseMatchStats ends the sequence seqs[i] with a match of the given
// length (3..258) and offset (1..32768), counts its symbols (and, with
// gatherSplitStats, observes it for the block splitting heuristic), starts
// the next sequence and returns its index i+1 (C advances *seq_p).
// port of deflate_choose_match
func (c *Compressor) chooseMatchStats(length, offset uint32, gatherSplitStats bool, seqs []deflateSequence,
	i int) int {
	seq := &seqs[i]
	lengthSlot := uint32(deflateLengthSlot[length])
	offsetSlot := deflateGetOffsetSlot(offset)

	c.freqs.litlen[deflateFirstLenSym+lengthSlot]++
	c.freqs.offset[offsetSlot]++
	if gatherSplitStats {
		c.splitStats.observeMatch(length)
	}

	seq.litrunlenAndLength |= length << seqLengthShift
	seq.offset = uint16(offset)
	seq.offsetSlot = uint16(offsetSlot)

	i++
	seqs[i].litrunlenAndLength = 0
	return i
}

// finishBlock counts the end-of-block symbol, builds the block's Huffman
// codes and flushes the block. port of deflate_finish_block
func (c *Compressor) finishBlock(os *outputBitstream, block []byte, sequences []deflateSequence,
	isFinalBlock bool) {
	c.freqs.litlen[deflateEndOfBlock]++
	deflateMakeHuffmanCodes(&c.freqs, &c.codes)
	c.flushBlock(os, block, sequences, isFinalBlock)
}

// flushBlock chooses the cheapest type of block (dynamic Huffman, static
// Huffman or uncompressed) for the uncompressed data block, compressed as
// the literals and matches of sequences (terminated by a sequence with
// match length 0), and outputs it. c.freqs and c.codes must already be set
// from those literals, matches and the end-of-block symbol. If the block
// does not fit in the rest of os.out, nothing is written and os.overflow
// is set (the only place it is set).
//
// Preconditions as in C: 1 <= len(block) <= MAX_BLOCK_LENGTH (C asserts
// len(block) >= MIN_BLOCK_LENGTH for non-final blocks; the output does not
// depend on it), os.bitcount <= 7 with no bits set above it, !os.overflow.
//
// If sequences is nil, the literals and matches are instead those of the
// near-optimal parser's minimum-cost path through c.pnState.optimumNodes
// (from node 0 to node len(block); levels 10..12 only).
// port of deflate_flush_block
func (c *Compressor) flushBlock(os *outputBitstream, block []byte, sequences []deflateSequence,
	isFinalBlock bool) {
	// All the output bitstream state is kept in a local copy (C: locals
	// bitbuf, bitcount, out_next, out_fast_end) and stored back at the end.
	w := *os
	w.setFastEnd()
	blockLength := uint32(len(block))

	// The cost for each block type, in bits. Start with the cost of the
	// block header which is 3 bits.
	dynamicCost := uint32(3)
	staticCost := uint32(3)
	uncompressedCost := uint32(3)

	// Precompute the precode items and build the precode.
	c.precomputeHuffmanHeader()
	pc := &c.o.precode

	// Account for the cost of encoding dynamic Huffman codes.
	dynamicCost += 5 + 5 + 4 + (3 * pc.numExplicitLens)
	for sym := range deflateNumPrecodeSyms {
		extra := uint32(deflateExtraPrecodeBits[sym])
		dynamicCost += pc.freqs[sym] * (extra + uint32(pc.lens[sym]))
	}

	// Account for the cost of encoding literals.
	sym := 0
	for ; sym < 144; sym++ {
		dynamicCost += c.freqs.litlen[sym] * uint32(c.codes.lens[sym])
		staticCost += c.freqs.litlen[sym] * 8
	}
	for ; sym < 256; sym++ {
		dynamicCost += c.freqs.litlen[sym] * uint32(c.codes.lens[sym])
		staticCost += c.freqs.litlen[sym] * 9
	}

	// Account for the cost of encoding the end-of-block symbol.
	dynamicCost += uint32(c.codes.lens[deflateEndOfBlock])
	staticCost += 7

	// Account for the cost of encoding lengths.
	for sym = deflateFirstLenSym; sym < deflateFirstLenSym+len(deflateExtraLengthBits); sym++ {
		extra := uint32(deflateExtraLengthBits[sym-deflateFirstLenSym])
		dynamicCost += c.freqs.litlen[sym] * (extra + uint32(c.codes.lens[sym]))
		staticCost += c.freqs.litlen[sym] * (extra + uint32(c.staticCodes.lens[sym]))
	}

	// Account for the cost of encoding offsets.
	for sym = 0; sym < len(deflateExtraOffsetBits); sym++ {
		extra := uint32(deflateExtraOffsetBits[sym])
		dynamicCost += c.freqs.offset[sym] * (extra + uint32(c.codes.lens[lensOffsetStart+sym]))
		staticCost += c.freqs.offset[sym] * (extra + 5)
	}

	// Compute the cost of using uncompressed blocks: alignment, LEN/NLEN,
	// 40 bits per extra block of at most UINT16_MAX bytes, and the data.
	bitcount := uint32(w.bitcount)
	uncompressedCost += (-(bitcount + 3) & 7) + 32 +
		(40 * ((blockLength+math.MaxUint16-1)/math.MaxUint16 - 1)) +
		(8 * blockLength)

	// Choose and output the cheapest type of block. If there is a tie,
	// prefer uncompressed, then static, then dynamic.
	bestCost := min(dynamicCost, min(staticCost, uncompressedCost))

	// If the block isn't going to fit, then stop early.
	if int64((bitcount+bestCost+7)/8) > int64(len(w.out)-w.next) {
		os.overflow = true
		return
	}
	// Else, now we know that the block fits, so no further bounds checks
	// on the output buffer are required until the next block.

	if bestCost == uncompressedCost {
		c.writeUncompressedBlocks(&w, block, isFinalBlock)
		*os = w
		return
	}

	var codes *deflateCodes
	if bestCost == staticCost {
		// Static Huffman block
		codes = &c.staticCodes
		w.addBits(b2u(isFinalBlock), 1)
		w.addBits(deflateBlocktypeStaticHuffman, 2)
		w.flushBits()
	} else {
		// Dynamic Huffman block
		codes = &c.codes
		c.writeDynamicHeader(&w, isFinalBlock)
	}

	// Output the literals and matches for a dynamic or static block.
	c.computeFullLenCodewords(codes)
	if sequences == nil {
		c.writeOptimumPath(&w, codes, blockLength)
	} else if useWriteSequencesAsm {
		// writeSequences in assembly, for the whole sequence list.
		r := w.regs()
		writeSequencesAsm(&r, w.out, codes, &c.o.length, block, &sequences[0])
		w.setRegs(r)
	} else {
		c.writeSequences(&w, codes, block, sequences)
	}

	// Output the end-of-block symbol.
	w.addBits(codes.codewords.litlen[deflateEndOfBlock], uint(codes.lens[deflateEndOfBlock]))
	w.flushBits()
	*os = w
}

// writeUncompressedBlocks is deflate_flush_block's uncompressed-block
// branch: DEFLATE limits uncompressed blocks to UINT16_MAX bytes, so a
// longer block is output as several, only the last one carrying
// isFinalBlock. The caller has checked that everything fits.
// port of deflate_flush_block (uncompressed blocks)
func (c *Compressor) writeUncompressedBlocks(w *outputBitstream, block []byte, isFinalBlock bool) {
	inNext := 0
	for {
		bfinal := uint64(0)
		ln := math.MaxUint16

		if len(block)-inNext <= math.MaxUint16 {
			bfinal = uint64(b2u(isFinalBlock))
			ln = len(block) - inNext
		}
		// Output BFINAL (1 bit) and BTYPE (2 bits, 0), then align to a
		// byte boundary.
		w.out[w.next] = byte((bfinal << w.bitcount) | w.bitbuf)
		w.next++
		if w.bitcount > 5 {
			w.out[w.next] = 0
			w.next++
		}
		w.bitbuf = 0
		w.bitcount = 0
		// Output LEN and NLEN, then the data itself.
		binary.LittleEndian.PutUint16(w.out[w.next:], uint16(ln))
		w.next += 2
		binary.LittleEndian.PutUint16(w.out[w.next:], ^uint16(ln))
		w.next += 2
		copy(w.out[w.next:w.next+ln], block[inNext:inNext+ln])
		w.next += ln
		inNext += ln
		if inNext == len(block) {
			break
		}
	}
}

// writeDynamicHeader outputs a dynamic Huffman block header: BFINAL, BTYPE,
// HLIT, HDIST, HCLEN, the precode lengths and the precode-encoded litlen
// and offset codeword lengths (from c.o.precode).
// port of deflate_flush_block (dynamic Huffman block header)
func (c *Compressor) writeDynamicHeader(w *outputBitstream, isFinalBlock bool) {
	pc := &c.o.precode
	numExplicitLens := pc.numExplicitLens
	numPrecodeItems := pc.numItems

	w.addBits(b2u(isFinalBlock), 1)
	w.addBits(deflateBlocktypeDynamicHuffman, 2)
	w.addBits(pc.numLitlenSyms-257, 5)
	w.addBits(pc.numOffsetSyms-1, 5)
	w.addBits(numExplicitLens-4, 4)

	// Output the lengths of the codewords in the precode. A 64-bit
	// bitbuffer is just one bit too small to hold the maximum number of
	// precode lens, so to minimize flushes one len is merged with the
	// previous fields.
	precodeSym := deflatePrecodeLensPermutation[0]
	w.addBits(uint32(pc.lens[precodeSym]), 3)
	w.flushBits()
	for i := uint32(1); i < numExplicitLens; i++ { // num_explicit_lens >= 4
		precodeSym = deflatePrecodeLensPermutation[i]
		w.addBits(uint32(pc.lens[precodeSym]), 3)
	}
	w.flushBits()

	// Output the lengths of the codewords in the litlen and offset codes,
	// encoded by the precode.
	for i := uint32(0); i < numPrecodeItems; i++ { // num_items >= 1
		precodeItem := pc.items[i]
		precodeSym := precodeItem & 0x1F
		w.addBits(pc.codewords[precodeSym], uint(pc.lens[precodeSym]))
		w.addBits(precodeItem>>5, uint(deflateExtraPrecodeBits[precodeSym]))
		w.flushBits()
	}
}

// writeSequences outputs the literals and matches of sequences with codes
// (c.o.length must hold codes' full length codewords).
// port of deflate_flush_block (output of the sequences list, with
// WRITE_MATCH)
func (c *Compressor) writeSequences(w *outputBitstream, codes *deflateCodes, block []byte,
	sequences []deflateSequence) {
	// The bit writer state is kept in registers for the whole loop (C: the
	// locals of deflate_flush_block), as are the table pointers.
	r := w.regs()
	out := w.out
	litCodewords := &codes.codewords.litlen
	offCodewords := &codes.codewords.offset
	lens := &codes.lens
	full := &c.o.length
	inNext := 0
	for s := 0; ; s++ {
		seq := &sequences[s]
		litrunlen := seq.litrunlenAndLength & seqLitrunlenMask
		length := seq.litrunlenAndLength >> seqLengthShift

		// Output a run of literals, 4 per flush.
		for ; litrunlen >= 4; litrunlen -= 4 {
			lits := block[inNext : inNext+4]
			lit := lits[0]
			r = r.addBits(litCodewords[lit], uint(lens[lit]))
			lit = lits[1]
			r = r.addBits(litCodewords[lit], uint(lens[lit]))
			lit = lits[2]
			r = r.addBits(litCodewords[lit], uint(lens[lit]))
			lit = lits[3]
			r = r.addBits(litCodewords[lit], uint(lens[lit]))
			inNext += 4
			r = r.flushBits(out)
		}
		if litrunlen != 0 {
			for ; litrunlen != 0; litrunlen-- { // 1..3 literals
				lit := block[inNext]
				inNext++
				r = r.addBits(litCodewords[lit], uint(lens[lit]))
			}
			r = r.flushBits(out)
		}

		if length == 0 { // Last sequence?
			break
		}

		// Output a match. C: WRITE_MATCH. offsetSlot is 0..29, so the mask
		// (an identity) only drops the bounds checks of the 32-entry tables.
		offsetSlot := uint32(seq.offsetSlot) & 31

		// Litlen symbol and extra length bits
		r = r.addBits(full.codewords[length], uint(full.lens[length]))

		// Offset symbol
		r = r.addBits(offCodewords[offsetSlot], uint(lens[lensOffsetStart+offsetSlot]))

		// Extra offset bits
		r = r.addBits(uint32(seq.offset)-deflateOffsetSlotBase[offsetSlot],
			uint(deflateExtraOffsetBits[offsetSlot]))

		r = r.flushBits(out)
		inNext += int(length)
	}
	w.setRegs(r)
}

// writeOptimumPath outputs the literals and matches of the minimum-cost
// path from c.pnState.optimumNodes[0] to the end node blockLength with codes
// (c.o.length must hold codes' full length codewords).
// port of deflate_flush_block (output of the minimum-cost path, with
// WRITE_MATCH)
func (c *Compressor) writeOptimumPath(w *outputBitstream, codes *deflateCodes, blockLength uint32) {
	nodes := &c.pnState.optimumNodes
	slotFull := c.offsetSlotFull
	// As in writeSequences: bit writer state and tables in registers.
	r := w.regs()
	out := w.out
	litCodewords := &codes.codewords.litlen
	offCodewords := &codes.codewords.offset
	lens := &codes.lens
	full := &c.o.length
	for cur := uint32(0); cur != blockLength; {
		item := nodes[cur].item
		length := item & optimumLenMask
		offset := item >> optimumOffsetShift

		if length == 1 {
			// Literal
			r = r.addBits(litCodewords[offset&0xFF], uint(lens[offset&0xFF]))
			r = r.flushBits(out)
		} else {
			// Match. C: WRITE_MATCH
			offsetSlot := uint32(slotFull[offset]) & 0x1F

			// Litlen symbol and extra length bits
			r = r.addBits(full.codewords[length], uint(full.lens[length]))

			// Offset symbol
			r = r.addBits(offCodewords[offsetSlot], uint(lens[lensOffsetStart+offsetSlot]))

			// Extra offset bits
			r = r.addBits(offset-deflateOffsetSlotBase[offsetSlot], uint(deflateExtraOffsetBits[offsetSlot]))

			r = r.flushBits(out)
		}
		cur += length
	}
	w.setRegs(r)
}

// b2u converts a bool to 0 or 1.
func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}
