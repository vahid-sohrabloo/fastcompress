// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): struct deflate_freqs, struct deflate_codes, the
// Huffman-code part of struct libdeflate_compressor, the length-limited
// canonical Huffman code builder (heapify_subtree, heapify_array, heap_sort,
// sort_symbols, build_tree, compute_length_counts, reverse_codeword,
// gen_codewords, deflate_make_huffman_code), deflate_reset_symbol_frequencies,
// deflate_make_huffman_codes, deflate_init_static_codes,
// deflate_get_offset_slot, deflate_compute_precode_items,
// deflate_precompute_huffman_header, deflate_compute_full_len_codewords and
// deflate_init_offset_slot_full; plus bsr32/bsf32/bsrw/bsfw of
// common_defs.h.
//
// The builders keep C's algorithm step for step (same sort order, same tie
// breaking, same length limiting heuristic), so lens and codewords are
// bit-identical to libdeflate's; huffman_diff_test.go compares them with
// ref/ldref dump-codes, dump-header and dump-static.

package deflate

import "math/bits"

// Symbol frequency counters for the DEFLATE Huffman codes.
// C: struct deflate_freqs
type deflateFreqs struct {
	litlen [deflateNumLitlenSyms]uint32 // C: litlen
	offset [deflateNumOffsetSyms]uint32 // C: offset
}

// Codewords for the DEFLATE Huffman codes. C: struct deflate_codewords
type deflateCodewords struct {
	litlen [deflateNumLitlenSyms]uint32 // C: litlen
	offset [deflateNumOffsetSyms]uint32 // C: offset
}

// lensOffsetStart is the index of the first offset codeword length in
// deflateCodes.lens. C: offsetof(struct deflate_lens, offset)
const lensOffsetStart = deflateNumLitlenSyms

// Codewords and lengths for the DEFLATE Huffman codes. C: struct deflate_codes
type deflateCodes struct {
	codewords deflateCodewords // C: codewords

	// Codeword lengths in bits; 0 means the symbol had zero frequency.
	// C: lens, a struct deflate_lens {u8 litlen[288]; u8 offset[32];}.
	// It is one array here because deflate_precompute_huffman_header treats
	// the struct as contiguous bytes (it moves the offset lengths down to
	// follow the used litlen lengths and back): lens[:lensOffsetStart] is
	// C's lens.litlen and lens[lensOffsetStart:] is C's lens.offset.
	lens [deflateNumLitlenSyms + deflateNumOffsetSyms]uint8
}

// Information about the precode of the current dynamic block.
// C: the 'precode' member of the union libdeflate_compressor.o
type deflatePrecode struct {
	freqs     [deflateNumPrecodeSyms]uint32                       // C: freqs
	codewords [deflateNumPrecodeSyms]uint32                       // C: codewords
	lens      [deflateNumPrecodeSyms]uint8                        // C: lens
	items     [deflateNumLitlenSyms + deflateNumOffsetSyms]uint32 // C: items (unsigned)
	// C: num_litlen_syms, num_offset_syms, num_explicit_lens, num_items
	numLitlenSyms   uint32
	numOffsetSyms   uint32
	numExplicitLens uint32
	numItems        uint32
}

// The "full" length codewords: the litlen codeword of each match length
// concatenated with its extra length bits.
// C: the 'length' member of the union libdeflate_compressor.o
type deflateLengthCodes struct {
	codewords [deflateMaxMatchLen + 1]uint32 // C: codewords
	lens      [deflateMaxMatchLen + 1]uint8  // C: lens
}

// Compressor is the compressor state, allocated once and reused for every
// call. C: struct libdeflate_compressor. This file holds the fields the
// Huffman code builders need; the level parameters, the parsers' state
// (matchfinders, sequence stores, the near-optimal parser's data) and the
// block split statistics are in compressorState (compress.go).
//
// Layout difference: C keeps 'o.precode' and 'o.length' in a union (the
// length codewords overwrite the precode information once it is no longer
// needed in deflate_flush_block). Go has no unions without unsafe, so they
// are separate fields.
// The difference is not observable: the only entries a union write would
// change are ones C never reads again before rewriting them (precode
// codewords the builder leaves unset are those of zero-length symbols,
// which are never output).
type Compressor struct {
	// Frequency counters for the current block. C: freqs
	freqs deflateFreqs

	// Dynamic Huffman codes for the current block. C: codes
	codes deflateCodes

	// The static Huffman codes defined by the DEFLATE format.
	// C: static_codes
	staticCodes deflateCodes

	// Temporary space for block flushing. C: o (a union in C)
	o struct {
		precode deflatePrecode     // C: o.precode
		length  deflateLengthCodes // C: o.length
	}

	// Offset => offset slot, the full map for offsets 1..32768 (used by the
	// near-optimal compressor only, so only allocated for levels 10..12,
	// where it points into the near-optimal parser's state like C's
	// p.n.offset_slot_full; nil otherwise). C: p.n.offset_slot_full
	offsetSlotFull *[deflateMaxMatchOffset + 1]uint8

	// Level parameters and the rest of C's fields (compress.go).
	compressorState
}

// bsr32 returns the index of the most significant set bit of v (v != 0).
// C: common_defs.h bsr32
func bsr32(v uint32) uint32 { return uint32(bits.Len32(v)) - 1 }

// bsf32 returns the index of the least significant set bit of v (v != 0).
// C: common_defs.h bsf32
func bsf32(v uint32) uint32 { return uint32(bits.TrailingZeros32(v)) }

// bsrw is bsr32 for a machine word (64 bits in this port). C: bsrw
func bsrw(v uint64) uint32 { return uint32(bits.Len64(v)) - 1 }

// bsfw is bsf32 for a machine word (64 bits in this port). C: bsfw
func bsfw(v uint64) uint32 { return uint32(bits.TrailingZeros64(v)) }

// heapifySubtree: given the binary tree node A[subtreeIdx] whose children
// already satisfy the maxheap property, swap the node with its greater
// child until it is greater than or equal to both of its children. A uses
// 1-based indices: C's A[i] is a[i-1] (C passes A-1, which Go cannot form).
// port of heapify_subtree
func heapifySubtree(a []uint32, length, subtreeIdx uint32) {
	v := a[subtreeIdx-1]
	parentIdx := subtreeIdx
	for {
		childIdx := parentIdx * 2
		if childIdx > length {
			break
		}
		if childIdx < length && a[childIdx] > a[childIdx-1] {
			childIdx++
		}
		if v >= a[childIdx-1] {
			break
		}
		a[parentIdx-1] = a[childIdx-1]
		parentIdx = childIdx
	}
	a[parentIdx-1] = v
}

// heapifyArray rearranges a[0:length] (1-based A[1..length]) so that it
// satisfies the maxheap property. port of heapify_array
func heapifyArray(a []uint32, length uint32) {
	for subtreeIdx := length / 2; subtreeIdx >= 1; subtreeIdx-- {
		heapifySubtree(a, length, subtreeIdx)
	}
}

// heapSort sorts a[0:length] in increasing order. port of heap_sort
func heapSort(a []uint32, length uint32) {
	// C: A-- (1-based indices); heapifySubtree/heapifyArray subtract 1.
	heapifyArray(a, length)
	for length >= 2 {
		tmp := a[length-1]
		a[length-1] = a[0]
		a[0] = tmp
		length--
		heapifySubtree(a, length, 1)
	}
}

// sortSymbols sorts the symbols primarily by frequency and secondarily by
// symbol value, discarding symbols with zero frequency (their lens are set
// to 0). symout[i] = sym | freq<<numSymbolBits for the used symbols; the
// result is their number.
//
// Preconditions as in C: numSyms <= 1<<numSymbolBits and numSyms >= 2; for
// numSyms == 2 at least one frequency must be nonzero (C's counter
// arithmetic then wraps and heap_sort runs off the array; Go panics).
// Frequencies whose packed value overflows 32 bits wrap exactly as C's u32
// arithmetic does. port of sort_symbols
func sortSymbols(numSyms uint32, freqs []uint32, lens []uint8, symout []uint32) uint32 {
	// C: GET_NUM_COUNTERS(num_syms) == num_syms counters, memset to 0.
	var counters [deflateMaxNumSyms]uint32
	numCounters := numSyms

	// Count the frequencies.
	for sym := uint32(0); sym < numSyms; sym++ {
		counters[min(freqs[sym], numCounters-1)]++
	}

	// Make the counters cumulative, ignoring the zero-th, which counted
	// symbols with zero frequency. As a side effect, this calculates the
	// number of symbols with nonzero frequency.
	numUsedSyms := uint32(0)
	for i := uint32(1); i < numCounters; i++ {
		count := counters[i]
		counters[i] = numUsedSyms
		numUsedSyms += count
	}

	// Sort nonzero-frequency symbols using the counters. At the same time,
	// set the codeword lengths of zero-frequency symbols to 0.
	for sym := uint32(0); sym < numSyms; sym++ {
		freq := freqs[sym]
		if freq != 0 {
			ci := min(freq, numCounters-1)
			symout[counters[ci]] = sym | freq<<numSymbolBits
			counters[ci]++
		} else {
			lens[sym] = 0
		}
	}

	// Sort the symbols counted in the last counter.
	heapSort(symout[counters[numCounters-2]:],
		counters[numCounters-1]-counters[numCounters-2])

	return numUsedSyms
}

// buildTree builds the stripped-down Huffman tree (non-leaf nodes only,
// parent pointers only) in place: on input a[0:symCount] holds the packed
// (freq, symbol) entries sorted by increasing frequency; on output
// a[0:symCount-1] are the non-leaf nodes, a[symCount-2] the root, each other
// node holding its parent's index in the frequency bits. The symbol bits are
// kept as-is. symCount must be at least 2. port of build_tree
func buildTree(a []uint32, symCount uint32) {
	lastIdx := symCount - 1

	// Index of the next lowest frequency leaf that still needs a parent.
	i := uint32(0)

	// Index of the next lowest frequency non-leaf that still needs a
	// parent, or 'e' if there is currently no such node.
	b := uint32(0)

	// Index of the next spot for a non-leaf (will overwrite a leaf).
	e := uint32(0)

	for {
		var newFreq uint32

		// Select the next two lowest frequency nodes among the leaves
		// a[i] and non-leaves a[b], and create a new node a[e] to be
		// their parent, with the sum of their frequencies. Usually the
		// next two lowest frequency nodes are of the same type (leaf or
		// non-leaf), so check those cases first.
		if i+1 <= lastIdx &&
			(b == e || (a[i+1]&freqMask) <= (a[b]&freqMask)) {
			// Two leaves
			newFreq = (a[i] & freqMask) + (a[i+1] & freqMask)
			i += 2
		} else if b+2 <= e &&
			(i > lastIdx ||
				(a[b+1]&freqMask) < (a[i]&freqMask)) {
			// Two non-leaves
			newFreq = (a[b] & freqMask) + (a[b+1] & freqMask)
			a[b] = (e << numSymbolBits) | (a[b] & symbolMask)
			a[b+1] = (e << numSymbolBits) | (a[b+1] & symbolMask)
			b += 2
		} else {
			// One leaf and one non-leaf
			newFreq = (a[i] & freqMask) + (a[b] & freqMask)
			a[b] = (e << numSymbolBits) | (a[b] & symbolMask)
			i++
			b++
		}
		a[e] = newFreq | (a[e] & symbolMask)

		// A binary tree with 'n' leaves has 'n - 1' non-leaves, so the
		// tree is complete once we've created 'n - 1' non-leaves.
		e++
		if e >= lastIdx {
			break
		}
	}
}

// computeLengthCounts determines from the tree of buildTree the number of
// codewords of each length 1..maxCodewordLen, applying C's (non-optimal)
// length limiting: a non-leaf at depth >= maxCodewordLen is moved up to the
// deepest length below maxCodewordLen that still has a codeword. It
// overwrites the parent indices of a with depths; the symbol bits are kept.
// rootIdx is the root's index (number of used symbols - 2).
// port of compute_length_counts
func computeLengthCounts(a []uint32, rootIdx uint32, lenCounts *[deflateMaxCodewordLen + 1]uint32,
	maxCodewordLen uint32) {
	for ln := uint32(0); ln <= maxCodewordLen; ln++ {
		lenCounts[ln] = 0
	}
	lenCounts[1] = 2

	// Set the root node's depth to 0.
	a[rootIdx] &= symbolMask

	for node := int(rootIdx) - 1; node >= 0; node-- {
		// Calculate the depth of this node.
		parent := a[node] >> numSymbolBits
		parentDepth := a[parent] >> numSymbolBits
		depth := parentDepth + 1

		// Set the depth of this node so that it is available when its
		// children (if any) are processed.
		a[node] = (a[node] & symbolMask) | (depth << numSymbolBits)

		// If needed, decrease the length to meet the length-limited
		// constraint.
		if depth >= maxCodewordLen {
			depth = maxCodewordLen
			for {
				depth--
				if lenCounts[depth] != 0 {
					break
				}
			}
		}

		// Account for the fact that we have a non-leaf node at the
		// current depth.
		lenCounts[depth]--
		lenCounts[depth+1] += 2
	}
}

// reverseCodeword bit-reverses the low ln bits of codeword (DEFLATE sends
// codewords bit-reversed). This is the bitreverse_tab variant that C uses
// on x86 (no rbit32): it reverses 16 bits and shifts right by 16 - ln, so
// for ln == 0 (zero-frequency symbols, whose codewords C calls undefined)
// the result is 0, as in libdeflate's x86 build. (C's arm builds use rbit32
// and give a different, equally unused, value there.) codeword < 1<<16.
// port of reverse_codeword
func reverseCodeword(codeword uint32, ln uint8) uint32 {
	return uint32(bits.Reverse16(uint16(codeword))) >> (16 - ln)
}

// genCodewords assigns the codeword lengths (decreasing lengths to the
// symbols in a's sort order, i.e. by increasing frequency then symbol) and
// generates the canonical codewords, bit-reversed, into a[0:numSyms].
// port of gen_codewords
func genCodewords(a []uint32, lens []uint8, lenCounts *[deflateMaxCodewordLen + 1]uint32,
	maxCodewordLen, numSyms uint32) {
	var nextCodewords [deflateMaxCodewordLen + 1]uint32

	// Assign codeword lengths to the symbols.
	i := 0
	for ln := maxCodewordLen; ln >= 1; ln-- {
		for count := lenCounts[ln]; count != 0; count-- {
			lens[a[i]&symbolMask] = uint8(ln)
			i++
		}
	}

	// Generate the codewords themselves: the lexicographically first
	// codeword of each length, then assign codewords in symbol order. This
	// produces a canonical code.
	nextCodewords[0] = 0
	nextCodewords[1] = 0
	for ln := uint32(2); ln <= maxCodewordLen; ln++ {
		nextCodewords[ln] = (nextCodewords[ln-1] + lenCounts[ln-1]) << 1
	}

	for sym := uint32(0); sym < numSyms; sym++ {
		// DEFLATE requires bit-reversed codewords.
		l := lens[sym]
		a[sym] = reverseCodeword(nextCodewords[l], l)
		nextCodewords[l]++
	}
}

// deflateMakeHuffmanCode builds a length-limited canonical Huffman code
// for the alphabet [0, numSyms) with the given frequencies: lens[sym] is
// the codeword length (0 for zero-frequency symbols, never more than
// maxCodewordLen) and codewords[sym] the bit-reversed codeword (undefined
// in C, and here 0, for zero-length symbols). codewords is also the sort
// and tree scratch space ('A'), as in C.
//
// Fewer than 2 used symbols give C's special case: two codewords of
// length 1, '0' for symbol 0 and '1' for the used symbol if it exists and
// is not 0, otherwise symbol 1; no other entry is written (lens of the
// zero-frequency symbols are 0, codewords of the others keep the sort
// scratch / previous values, as in C).
//
// Preconditions as in C: 2 <= numSyms <= deflateMaxNumSyms, len(freqs),
// len(lens), len(codewords) >= numSyms, numSyms < 1<<maxCodewordLen,
// maxCodewordLen <= deflateMaxCodewordLen; see sortSymbols for numSyms == 2.
// The frequency sum should be < 1<<numFreqBits; larger values wrap as in
// C's u32 arithmetic (the code is still complete, not optimal).
// port of deflate_make_huffman_code
func deflateMakeHuffmanCode(numSyms, maxCodewordLen uint32, freqs []uint32, lens []uint8, codewords []uint32) {
	a := codewords

	// Sort the symbols primarily by frequency and secondarily by symbol
	// value; 'a' shares storage with the codewords.
	numUsedSyms := sortSymbols(numSyms, freqs, lens, a)

	// A complete Huffman code must contain at least 2 codewords; see the
	// long comment in C for why libdeflate always sends 2.
	if numUsedSyms < 2 {
		sym := uint32(0)
		if numUsedSyms != 0 {
			sym = a[0] & symbolMask
		}
		nonzeroIdx := sym
		if sym == 0 {
			nonzeroIdx = 1
		}
		codewords[0] = 0
		lens[0] = 1
		codewords[nonzeroIdx] = 1
		lens[nonzeroIdx] = 1
		return
	}

	// Build the stripped-down Huffman tree in 'a', then extract length
	// counts from it and generate the final codewords.
	buildTree(a, numUsedSyms)

	var lenCounts [deflateMaxCodewordLen + 1]uint32
	computeLengthCounts(a, numUsedSyms-2, &lenCounts, maxCodewordLen)
	genCodewords(a, lens, &lenCounts, maxCodewordLen, numSyms)
}

// resetSymbolFrequencies clears the Huffman symbol frequency counters; it
// must be called when starting a new DEFLATE block.
// port of deflate_reset_symbol_frequencies
func (c *Compressor) resetSymbolFrequencies() {
	c.freqs = deflateFreqs{}
}

// deflateMakeHuffmanCodes builds the literal/length (max 14 bits) and offset
// (max 15 bits) Huffman codes of a block. port of deflate_make_huffman_codes
func deflateMakeHuffmanCodes(freqs *deflateFreqs, codes *deflateCodes) {
	deflateMakeHuffmanCode(deflateNumLitlenSyms, maxLitlenCodewordLen,
		freqs.litlen[:], codes.lens[:lensOffsetStart], codes.codewords.litlen[:])

	deflateMakeHuffmanCode(deflateNumOffsetSyms, maxOffsetCodewordLen,
		freqs.offset[:], codes.lens[lensOffsetStart:], codes.codewords.offset[:])
}

// initStaticCodes initializes c.staticCodes from frequencies proportional
// to the static code's lengths (it leaves those frequencies in c.freqs, as
// C does). port of deflate_init_static_codes
func (c *Compressor) initStaticCodes() {
	i := 0
	for ; i < 144; i++ {
		c.freqs.litlen[i] = 1 << (9 - 8)
	}
	for ; i < 256; i++ {
		c.freqs.litlen[i] = 1 << (9 - 9)
	}
	for ; i < 280; i++ {
		c.freqs.litlen[i] = 1 << (9 - 7)
	}
	for ; i < 288; i++ {
		c.freqs.litlen[i] = 1 << (9 - 8)
	}

	for i = 0; i < 32; i++ {
		c.freqs.offset[i] = 1 << (5 - 5)
	}

	deflateMakeHuffmanCodes(&c.freqs, &c.staticCodes)
}

// deflateGetOffsetSlot returns the offset slot of a match offset
// (1 <= offset <= 32768) using the small map: deflateOffsetSlot[offset-1]
// for offset <= 256, else deflateOffsetSlot[(offset-1)>>7] + 14, selected
// branchlessly with n = (256 - offset) >> 29 (0 or 7).
// port of deflate_get_offset_slot
func deflateGetOffsetSlot(offset uint32) uint32 {
	n := (256 - offset) >> 29
	// (offset-1)>>n < 256 for offsets 1..32768: the uint8 conversion only
	// drops the bounds check.
	return uint32(deflateOffsetSlot[uint8((offset-1)>>n)]) + (n << 1)
}

// deflateComputePrecodeItems computes the precode items (RLE / literal
// tokens: precode symbol in bits 0-4, extra bits value from bit 5) with
// which the codeword lengths lens[0:numLens] are output, and the precode
// symbol frequencies. Returns the number of items. numLens >= 1, every
// length <= 15. port of deflate_compute_precode_items
func deflateComputePrecodeItems(lens []uint8, numLens uint32, precodeFreqs *[deflateNumPrecodeSyms]uint32,
	precodeItems []uint32) uint32 {
	*precodeFreqs = [deflateNumPrecodeSyms]uint32{}

	itemptr := uint32(0)
	runStart := uint32(0)
	for {
		// Find the next run of codeword lengths.

		// ln = the length being repeated
		ln := lens[runStart]

		// Extend the run.
		runEnd := runStart
		for {
			runEnd++
			if runEnd == numLens || ln != lens[runEnd] {
				break
			}
		}

		if ln == 0 {
			// Run of zeroes.

			// Symbol 18: RLE 11 to 138 zeroes at a time.
			for (runEnd - runStart) >= 11 {
				extraBits := min((runEnd-runStart)-11, 0x7F)
				precodeFreqs[18]++
				precodeItems[itemptr] = 18 | (extraBits << 5)
				itemptr++
				runStart += 11 + extraBits
			}

			// Symbol 17: RLE 3 to 10 zeroes at a time.
			if (runEnd - runStart) >= 3 {
				extraBits := min((runEnd-runStart)-3, 0x7)
				precodeFreqs[17]++
				precodeItems[itemptr] = 17 | (extraBits << 5)
				itemptr++
				runStart += 3 + extraBits
			}
		} else {
			// A run of nonzero lengths.

			// Symbol 16: RLE 3 to 6 of the previous length.
			if (runEnd - runStart) >= 4 {
				precodeFreqs[ln]++
				precodeItems[itemptr] = uint32(ln)
				itemptr++
				runStart++
				for {
					extraBits := min((runEnd-runStart)-3, 0x3)
					precodeFreqs[16]++
					precodeItems[itemptr] = 16 | (extraBits << 5)
					itemptr++
					runStart += 3 + extraBits
					if (runEnd - runStart) < 3 {
						break
					}
				}
			}
		}

		// Output any remaining lengths without RLE.
		for runStart != runEnd {
			precodeFreqs[ln]++
			precodeItems[itemptr] = uint32(ln)
			itemptr++
			runStart++
		}
		if runStart == numLens {
			break
		}
	}

	return itemptr
}

// precomputeHuffmanHeader precomputes the information needed to output the
// dynamic Huffman codes of c.codes: c.o.precode.{numLitlenSyms,
// numOffsetSyms, items, numItems, freqs, lens, codewords, numExplicitLens}.
//
// Like C, it temporarily moves the offset lengths down to follow the used
// litlen lengths and moves them back afterwards, but does not clear the
// copy: when numLitlenSyms < 288, c.codes.lens[numLitlenSyms :
// numLitlenSyms+numOffsetSyms] (litlen lengths of unused symbols, which
// were 0) are left holding the offset lengths, exactly as in C.
// port of deflate_precompute_huffman_header
func (c *Compressor) precomputeHuffmanHeader() {
	p := &c.o.precode

	// Compute how many litlen and offset symbols are needed.
	for p.numLitlenSyms = deflateNumLitlenSyms; p.numLitlenSyms > 257; p.numLitlenSyms-- {
		if c.codes.lens[p.numLitlenSyms-1] != 0 {
			break
		}
	}

	for p.numOffsetSyms = deflateNumOffsetSyms; p.numOffsetSyms > 1; p.numOffsetSyms-- {
		if c.codes.lens[lensOffsetStart+p.numOffsetSyms-1] != 0 {
			break
		}
	}

	// If we're not using the full set of literal/length codeword lengths,
	// then temporarily move the offset codeword lengths over so that the
	// literal/length and offset codeword lengths are contiguous.
	// (copy has memmove semantics.)
	nl, no := p.numLitlenSyms, p.numOffsetSyms
	if nl != deflateNumLitlenSyms {
		copy(c.codes.lens[nl:nl+no], c.codes.lens[lensOffsetStart:lensOffsetStart+no])
	}

	// Compute the "items" (RLE / literal tokens and extra bits) with which
	// the codeword lengths in the larger code will be output.
	p.numItems = deflateComputePrecodeItems(c.codes.lens[:], nl+no, &p.freqs, p.items[:])

	// Build the precode.
	deflateMakeHuffmanCode(deflateNumPrecodeSyms, maxPreCodewordLen,
		p.freqs[:], p.lens[:], p.codewords[:])

	// Count how many precode lengths we actually need to output.
	for p.numExplicitLens = deflateNumPrecodeSyms; p.numExplicitLens > 4; p.numExplicitLens-- {
		if p.lens[deflatePrecodeLensPermutation[p.numExplicitLens-1]] != 0 {
			break
		}
	}

	// Restore the offset codeword lengths if needed.
	if nl != deflateNumLitlenSyms {
		copy(c.codes.lens[lensOffsetStart:lensOffsetStart+no], c.codes.lens[nl:nl+no])
	}
}

// computeFullLenCodewords computes c.o.length: for each match length 3..258
// the litlen codeword of its length symbol in codes concatenated with the
// extra length bits, and the total length. Entries 0..2 are not written.
// port of deflate_compute_full_len_codewords
func (c *Compressor) computeFullLenCodewords(codes *deflateCodes) {
	for ln := uint32(deflateMinMatchLen); ln <= deflateMaxMatchLen; ln++ {
		slot := uint32(deflateLengthSlot[ln])
		litlenSym := deflateFirstLenSym + slot
		extraBits := ln - deflateLengthSlotBase[slot]

		c.o.length.codewords[ln] = codes.codewords.litlen[litlenSym] |
			(extraBits << codes.lens[litlenSym])
		c.o.length.lens[ln] = codes.lens[litlenSym] + deflateExtraLengthBits[slot]
	}
}

// initOffsetSlotFull initializes c.offsetSlotFull (entry 0 is not written),
// allocating it if it is nil. port of deflate_init_offset_slot_full
func (c *Compressor) initOffsetSlotFull() {
	if c.offsetSlotFull == nil {
		c.offsetSlotFull = new([deflateMaxMatchOffset + 1]uint8)
	}
	for offsetSlot := uint32(0); offsetSlot < uint32(len(deflateOffsetSlotBase)); offsetSlot++ {
		offset := deflateOffsetSlotBase[offsetSlot]
		offsetEnd := offset + (1 << deflateExtraOffsetBits[offsetSlot])
		for {
			c.offsetSlotFull[offset] = uint8(offsetSlot)
			offset++
			if offset == offsetEnd {
				break
			}
		}
	}
}
