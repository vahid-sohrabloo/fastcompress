// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the compressor's build-time parameters, the
// maximum block length, the packed (frequency | symbol) representation used
// by the Huffman code builder, the output bitstream capacity and the
// length/offset slot tables.

package deflate

// Build-time parameters of the compression algorithms. C: the
// "#define"s at the top of deflate_compress.c (SUPPORT_NEAR_OPTIMAL_PARSING
// is 1 in C and in this port).
const (
	// Minimum block length the compressor uses (except for the final
	// block), in uncompressed bytes. C: MIN_BLOCK_LENGTH
	minBlockLength = 5000

	// Soft maximum block length of the greedy, lazy, lazy2 and
	// near-optimal compressors. C: SOFT_MAX_BLOCK_LENGTH
	softMaxBlockLength = 300000

	// Length of the sequence store of the greedy and lazy compressors.
	// C: SEQ_STORE_LENGTH
	seqStoreLength = 50000

	// Soft maximum block length and sequence store length of
	// deflate_compress_fastest. C: FAST_SOFT_MAX_BLOCK_LENGTH,
	// FAST_SEQ_STORE_LENGTH
	fastSoftMaxBlockLength = 65535
	fastSeqStoreLength     = 8192

	// Maximum codeword lengths the compressor uses for each Huffman code.
	// Litlen codewords are limited to 14 bits (not the format's 15) so
	// that 4 literals can be written at once. C: MAX_LITLEN_CODEWORD_LEN,
	// MAX_OFFSET_CODEWORD_LEN, MAX_PRE_CODEWORD_LEN
	maxLitlenCodewordLen = 14
	maxOffsetCodewordLen = deflateMaxOffsetCodewordLen
	maxPreCodewordLen    = deflateMaxPreCodewordLen

	// Near-optimal parsing: fractional bit cost scale and the costs of
	// symbols unused in the previous pass. C: BIT_COST,
	// LITERAL_NOSTAT_BITS, LENGTH_NOSTAT_BITS, OFFSET_NOSTAT_BITS
	bitCost           = 16
	literalNostatBits = 13
	lengthNostatBits  = 13
	offsetNostatBits  = 10

	// (Slightly less than) the maximum number of matches the near-optimal
	// compressor caches per block. C: MATCH_CACHE_LENGTH
	matchCacheLength = softMaxBlockLength * 5

	// Maximum number of matches the binary trees matchfinder can find at
	// one position. C: MAX_MATCHES_PER_POS
	maxMatchesPerPos = deflateMaxMatchLen - deflateMinMatchLen + 1

	// The largest block length ever used: the final block of length
	// SOFT_MAX_BLOCK_LENGTH + MIN_BLOCK_LENGTH - 1, or a lazy2 block of
	// SOFT_MAX_BLOCK_LENGTH + 1 + DEFLATE_MAX_MATCH_LEN. C: MAX_BLOCK_LENGTH
	maxBlockLength = max(softMaxBlockLength+minBlockLength-1,
		softMaxBlockLength+1+deflateMaxMatchLen)
)

// The Huffman code builder sorts (frequency, symbol) pairs packed into one
// uint32: the low numSymbolBits bits hold the symbol, the high numFreqBits
// bits the frequency. C: NUM_SYMBOL_BITS, NUM_FREQ_BITS, SYMBOL_MASK,
// FREQ_MASK
const (
	numSymbolBits = 10
	numFreqBits   = 32 - numSymbolBits
	symbolMask    = (1 << numSymbolBits) - 1
	freqMask      = ^uint32(symbolMask)
)

// Output bitstream capacity. bitbuf_t is machine_word_t; the port always
// uses the 64-bit configuration (as on amd64 and arm64), on every GOARCH,
// like the decompressor. The capacity is 1 less than the real size to avoid
// undefined behaviour when doing bitbuf >>= bitcount & ~7.
// C: deflate_compress.c BITBUF_NBITS (8 * sizeof(bitbuf_t) - 1)
const outBitbufNbits = 8*8 - 1

// canBuffer reports whether n bits can always be added to the bit buffer
// after any pending bytes have been flushed (up to 7 bits remain after a
// flush). Callers use it with constant arguments, so it folds away.
// C: CAN_BUFFER
func canBuffer(n int) bool { return 7+n <= outBitbufNbits }

// Compile-time checks. C: check_buildtime_parameters and the STATIC_ASSERTs
// of deflate_make_huffman_code / deflate_compute_full_len_codewords.
const (
	_ = uint(softMaxBlockLength - minBlockLength)
	_ = uint(fastSoftMaxBlockLength - minBlockLength)
	_ = uint(seqStoreLength*deflateMinMatchLen - minBlockLength)
	_ = uint(fastSeqStoreLength*4 - minBlockLength) // HT_MATCHFINDER_MIN_MATCH_LEN == 4
	_ = uint(matchCacheLength - minBlockLength*maxMatchesPerPos)
	_ = uint(softMaxBlockLength - fastSoftMaxBlockLength)
	_ = uint(softMaxBlockLength + minBlockLength - seqStoreLength*deflateMinMatchLen)
	_ = uint(fastSoftMaxBlockLength + minBlockLength - fastSeqStoreLength*4)
	_ = uint(deflateMaxLitlenCodewordLen - maxLitlenCodewordLen)
	_ = uint(deflateMaxOffsetCodewordLen - maxOffsetCodewordLen)
	_ = uint(deflateMaxPreCodewordLen - maxPreCodewordLen)
	_ = uint(1<<maxLitlenCodewordLen - deflateNumLitlenSyms)
	_ = uint(1<<maxOffsetCodewordLen - deflateNumOffsetSyms)
	_ = uint(1<<maxPreCodewordLen - deflateNumPrecodeSyms)
	_ = uint(1<<numSymbolBits - deflateMaxNumSyms)
	_ = uint((1<<numFreqBits - 1) - maxBlockLength)
	_ = uint(32 - (maxLitlenCodewordLen + deflateMaxExtraLengthBits))
	_ = uint(16 - deflateMaxCodewordLen) // reverseCodeword works on 16 bits
)

// Length slot => length slot base value. C: deflate_length_slot_base
var deflateLengthSlotBase = [...]uint32{
	3, 4, 5, 6, 7, 8, 9, 10,
	11, 13, 15, 17, 19, 23, 27, 31,
	35, 43, 51, 59, 67, 83, 99, 115,
	131, 163, 195, 227, 258,
}

// Length slot => number of extra length bits. C: deflate_extra_length_bits
var deflateExtraLengthBits = [...]uint8{
	0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 1, 2, 2, 2, 2,
	3, 3, 3, 3, 4, 4, 4, 4,
	5, 5, 5, 5, 0,
}

// Offset slot => offset slot base value. C: deflate_offset_slot_base
var deflateOffsetSlotBase = [...]uint32{
	1, 2, 3, 4, 5, 7, 9, 13,
	17, 25, 33, 49, 65, 97, 129, 193,
	257, 385, 513, 769, 1025, 1537, 2049, 3073,
	4097, 6145, 8193, 12289, 16385, 24577,
}

// Offset slot => number of extra offset bits. C: deflate_extra_offset_bits
var deflateExtraOffsetBits = [...]uint8{
	0, 0, 0, 0, 1, 1, 2, 2,
	3, 3, 4, 4, 5, 5, 6, 6,
	7, 7, 8, 8, 9, 9, 10, 10,
	11, 11, 12, 12, 13, 13,
}

// Length => length slot. C: deflate_length_slot
var deflateLengthSlot = [deflateMaxMatchLen + 1]uint8{
	0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 12,
	12, 13, 13, 13, 13, 14, 14, 14, 14, 15, 15, 15, 15, 16, 16, 16, 16, 16,
	16, 16, 16, 17, 17, 17, 17, 17, 17, 17, 17, 18, 18, 18, 18, 18, 18, 18,
	18, 19, 19, 19, 19, 19, 19, 19, 19, 20, 20, 20, 20, 20, 20, 20, 20, 20,
	20, 20, 20, 20, 20, 20, 20, 21, 21, 21, 21, 21, 21, 21, 21, 21, 21, 21,
	21, 21, 21, 21, 21, 22, 22, 22, 22, 22, 22, 22, 22, 22, 22, 22, 22, 22,
	22, 22, 22, 23, 23, 23, 23, 23, 23, 23, 23, 23, 23, 23, 23, 23, 23, 23,
	23, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24,
	24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 24, 25, 25, 25,
	25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25,
	25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 25, 26, 26, 26, 26, 26, 26, 26,
	26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26, 26,
	26, 26, 26, 26, 26, 26, 26, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27,
	27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27, 27,
	27, 27, 28,
}

// 'offset - 1 => offset_slot' for offset <= 256 (generated in C by
// scripts/gen_offset_slot_map.py). C: deflate_offset_slot
var deflateOffsetSlot = [256]uint8{
	0, 1, 2, 3, 4, 4, 5, 5, 6, 6, 6, 6, 7, 7, 7, 7,
	8, 8, 8, 8, 8, 8, 8, 8, 9, 9, 9, 9, 9, 9, 9, 9,
	10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
	11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11, 11,
	12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12,
	12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12,
	13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13,
	13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13,
	14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14,
	14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14,
	14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14,
	14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14,
	15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15,
	15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15,
	15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15,
	15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15, 15,
}

// Precode symbol => number of extra bits. (The precode length permutation,
// deflate_precode_lens_permutation, is shared with the decompressor:
// deflatePrecodeLensPermutation in consts.go.) C: deflate_extra_precode_bits
var deflateExtraPrecodeBits = [deflateNumPrecodeSyms]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 3, 7,
}
