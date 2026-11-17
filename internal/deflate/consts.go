// Port of libdeflate v1.26 lib/deflate_constants.h and the decode-table
// parameters of lib/deflate_decompress.c (Copyright 2016 Eric Biggers, MIT;
// see NOTICE).

package deflate

// DEFLATE format constants. C: deflate_constants.h
const (
	// Valid block types.
	deflateBlocktypeUncompressed   = 0 // C: DEFLATE_BLOCKTYPE_UNCOMPRESSED
	deflateBlocktypeStaticHuffman  = 1 // C: DEFLATE_BLOCKTYPE_STATIC_HUFFMAN
	deflateBlocktypeDynamicHuffman = 2 // C: DEFLATE_BLOCKTYPE_DYNAMIC_HUFFMAN

	// Minimum and maximum supported match lengths (in bytes).
	deflateMinMatchLen = 3   // C: DEFLATE_MIN_MATCH_LEN
	deflateMaxMatchLen = 258 // C: DEFLATE_MAX_MATCH_LEN

	// Maximum supported match offset (in bytes).
	deflateMaxMatchOffset = 32768 // C: DEFLATE_MAX_MATCH_OFFSET

	// log2 of deflateMaxMatchOffset.
	deflateWindowOrder = 15 // C: DEFLATE_WINDOW_ORDER

	// Number of symbols in each Huffman code. For the literal/length and
	// offset codes these are maximum values; a block may use fewer.
	deflateNumPrecodeSyms = 19  // C: DEFLATE_NUM_PRECODE_SYMS
	deflateNumLitlenSyms  = 288 // C: DEFLATE_NUM_LITLEN_SYMS
	deflateNumOffsetSyms  = 32  // C: DEFLATE_NUM_OFFSET_SYMS

	// The maximum number of symbols across all codes.
	deflateMaxNumSyms = 288 // C: DEFLATE_MAX_NUM_SYMS

	// Division of symbols in the literal/length code.
	deflateNumLiterals = 256 // C: DEFLATE_NUM_LITERALS
	deflateEndOfBlock  = 256 // C: DEFLATE_END_OF_BLOCK
	deflateFirstLenSym = 257 // C: DEFLATE_FIRST_LEN_SYM

	// Maximum codeword length, in bits, within each Huffman code.
	deflateMaxPreCodewordLen    = 7  // C: DEFLATE_MAX_PRE_CODEWORD_LEN
	deflateMaxLitlenCodewordLen = 15 // C: DEFLATE_MAX_LITLEN_CODEWORD_LEN
	deflateMaxOffsetCodewordLen = 15 // C: DEFLATE_MAX_OFFSET_CODEWORD_LEN

	// The maximum codeword length across all codes.
	deflateMaxCodewordLen = 15 // C: DEFLATE_MAX_CODEWORD_LEN

	// Maximum possible overrun when decoding codeword lengths.
	deflateMaxLensOverrun = 137 // C: DEFLATE_MAX_LENS_OVERRUN

	// Maximum number of extra bits that may be required to represent a
	// match length or offset.
	deflateMaxExtraLengthBits = 5  // C: DEFLATE_MAX_EXTRA_LENGTH_BITS
	deflateMaxExtraOffsetBits = 13 // C: DEFLATE_MAX_EXTRA_OFFSET_BITS
)

// Decode table sizes. TABLEBITS is log2 of the main table size; ENOUGH is
// the worst-case number of entries of the main table plus all subtables
// (computed by zlib's 'enough' utility: "enough <syms> <tablebits> <maxlen>").
// When TABLEBITS changes, ENOUGH must change too. C: deflate_decompress.c
const (
	precodeTablebits = 7    // C: PRECODE_TABLEBITS
	precodeEnough    = 128  // C: PRECODE_ENOUGH (enough 19 7 7)
	litlenTablebits  = 11   // C: LITLEN_TABLEBITS
	litlenEnough     = 2342 // C: LITLEN_ENOUGH (enough 288 11 15)
	offsetTablebits  = 8    // C: OFFSET_TABLEBITS
	offsetEnough     = 402  // C: OFFSET_ENOUGH (enough 32 8 15)
)

// Litlen and offset decode table entry flags. C: deflate_decompress.c
const (
	// A literal entry in the litlen decode table.
	huffdecLiteral = 0x80000000 // C: HUFFDEC_LITERAL
	// huffdecSubtablePointer or huffdecEndOfBlock is set.
	huffdecExceptional = 0x00008000 // C: HUFFDEC_EXCEPTIONAL
	// A subtable pointer entry in the litlen or offset decode table.
	huffdecSubtablePointer = 0x00004000 // C: HUFFDEC_SUBTABLE_POINTER
	// An end-of-block entry in the litlen decode table.
	huffdecEndOfBlock = 0x00002000 // C: HUFFDEC_END_OF_BLOCK
)

// Maximum number of bits consumed by decoding a match length / offset, and
// the same without a subtable. C: deflate_decompress.c
const (
	lengthMaxbits     = deflateMaxLitlenCodewordLen + deflateMaxExtraLengthBits // C: LENGTH_MAXBITS
	lengthMaxfastbits = litlenTablebits + deflateMaxExtraLengthBits             // C: LENGTH_MAXFASTBITS
	offsetMaxbits     = deflateMaxOffsetCodewordLen + deflateMaxExtraOffsetBits // C: OFFSET_MAXBITS
	offsetMaxfastbits = offsetTablebits + deflateMaxExtraOffsetBits             // C: OFFSET_MAXFASTBITS
)

// The order in which precode lengths are stored.
// C: decompress_template.h:deflate_precode_lens_permutation
var deflatePrecodeLensPermutation = [deflateNumPrecodeSyms]uint8{
	16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15,
}
