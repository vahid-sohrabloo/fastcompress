// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the per-level fields of struct
// libdeflate_compressor, libdeflate_alloc_compressor_ex /
// libdeflate_alloc_compressor (the level table), libdeflate_deflate_compress,
// libdeflate_get_compression_level and libdeflate_deflate_compress_bound.

package deflate

import (
	"errors"
	"math"
)

// ErrInvalidLevel is returned by NewCompressor for a compression level
// outside -1..12 (C: libdeflate_alloc_compressor returns NULL).
var ErrInvalidLevel = errors.New("deflate: invalid compression level")

// compressImpl is the type of the compress() implementation of a level.
// It compresses in into os, setting os.overflow if the output does not fit.
// C: libdeflate_compressor.impl
type compressImpl func(c *Compressor, in []byte, os *outputBitstream)

// nearOptimalParams holds the near-optimal parser parameters of levels
// 10..12 (the rest of C's 'p.n' is nearOptimalState,
// compress_near_optimal.go).
// C: struct libdeflate_compressor p.n.{max_optim_passes,
// min_improvement_to_continue, min_bits_to_use_nonfinal_path,
// max_len_to_optimize_static_block}
type nearOptimalParams struct {
	// Maximum number of optimization passes per block.
	// C: max_optim_passes (unsigned)
	maxOptimPasses uint32

	// Stop optimizing once a pass improves the cost by fewer bits than
	// this. C: min_improvement_to_continue
	minImprovementToContinue uint32

	// Minimum number of bits a previous pass's path must save to be
	// regenerated and used. C: min_bits_to_use_nonfinal_path
	minBitsToUseNonfinalPath uint32

	// Maximum block length at which the optimal path for the static codes
	// is searched. C: max_len_to_optimize_static_block
	maxLenToOptimizeStaticBlock uint32
}

// compressorState holds the fields of struct libdeflate_compressor that
// are not part of the Huffman code state of huffman.go. It is embedded in
// Compressor (Go cannot add fields to a struct from another file).
//
// C's parser union 'p' is one pointer per parser (pf, pg, pnState), of
// which NewCompressor allocates only the one its level uses, as C sizes the
// allocation by level. p.n.offset_slot_full is reached through
// Compressor.offsetSlotFull (huffman.go). C's free_func has no Go
// counterpart.
type compressorState struct {
	// The compress() implementation of the level; nil for level 0 (not
	// used: every input is output as stored blocks).
	// C: impl
	impl compressImpl

	// The compression level with which this compressor was created
	// (-1 is stored as 6). C: compression_level (unsigned)
	compressionLevel uint32

	// Inputs of this size or less are output as uncompressed blocks.
	// C: max_passthrough_size (size_t; SIZE_MAX for level 0, here MaxInt)
	maxPassthroughSize int

	// Consider at most this many potential matches at each position.
	// C: max_search_depth (unused, so left 0, for levels 0 and 1)
	maxSearchDepth uint32

	// A match of this length is chosen immediately. C: nice_match_length
	// (unused, so left 0, for level 0)
	niceMatchLength uint32

	// Near-optimal parameters, levels 10..12 only. C: p.n.*
	pn nearOptimalParams

	// Data for near-optimal parsing, levels 10..12 only
	// (compress_near_optimal.go). C: p.n
	pnState *nearOptimalState

	// Data for fastest parsing, level 1 only (compress_fastest.go). C: p.f
	pf *fastestState

	// Data for greedy or lazy parsing, levels 2..9 (compress_greedy.go,
	// compress_lazy.go). C: p.g
	pg *greedyState

	// Block split statistics for the current block (block_split.go).
	// C: split_stats
	splitStats blockSplitStats

	// The output bitstream of the running Compress call (C: the local
	// 'struct deflate_output_bitstream os' of libdeflate_deflate_compress).
	os outputBitstream

	// Not in C (CompressChunk, compress_chunk.go): during a CompressChunk
	// call, the number of history bytes at the start of the impl's input
	// (primed into the matchfinder, not output) and whether the output
	// must not end with a final block. Both are zero outside
	// CompressChunk, so Compress runs exactly C's code.
	histLen  int
	nonFinal bool

	// Not in C: CompressChunk's copy of history+src when the caller's are
	// not adjacent (allocated on first use).
	chunkBuf []byte

	// Not in C: set by NewHuffmanOnlyCompressor (Level reports -2), and
	// the one sequence of its all-literal blocks.
	huffmanOnly bool
	huffOnlySeq [1]deflateSequence
}

// NewCompressor allocates a compressor for compression level -1..12: 0 is
// no compression (stored blocks only), 1 is the fastest, 12 the slowest
// with the best ratio; -1 is an alias for the default level 6. Any other
// level returns ErrInvalidLevel. The compressor may be reused for any
// number of Compress calls but not concurrently.
// port of libdeflate_alloc_compressor (libdeflate_alloc_compressor_ex with
// default options)
func NewCompressor(level int) (*Compressor, error) {
	// Note: for similarity with zlib's API, -1 is accepted as an alias for
	// the default compression level.
	if level == -1 {
		level = 6
	}
	if level < 0 || level > 12 {
		return nil, ErrInvalidLevel
	}

	c := new(Compressor)
	c.compressionLevel = uint32(level)

	// The higher the compression level, the more we should bother trying
	// to compress very small inputs.
	c.maxPassthroughSize = 55 - (level * 4)

	switch level {
	case 0:
		c.maxPassthroughSize = math.MaxInt // C: SIZE_MAX
		// impl is not used.
	case 1:
		c.impl = deflateCompressFastest
		c.pf = new(fastestState)
		// max_search_depth is unused.
		c.niceMatchLength = 32
	case 2:
		c.impl = deflateCompressGreedy
		c.pg = new(greedyState)
		c.maxSearchDepth = 6
		c.niceMatchLength = 10
	case 3:
		c.impl = deflateCompressGreedy
		c.pg = new(greedyState)
		c.maxSearchDepth = 12
		c.niceMatchLength = 14
	case 4:
		c.impl = deflateCompressGreedy
		c.pg = new(greedyState)
		c.maxSearchDepth = 16
		c.niceMatchLength = 30
	case 5:
		c.impl = deflateCompressLazy
		c.pg = new(greedyState)
		c.maxSearchDepth = 16
		c.niceMatchLength = 30
	case 6:
		c.impl = deflateCompressLazy
		c.pg = new(greedyState)
		c.maxSearchDepth = 35
		c.niceMatchLength = 65
	case 7:
		c.impl = deflateCompressLazy
		c.pg = new(greedyState)
		c.maxSearchDepth = 100
		c.niceMatchLength = 130
	case 8:
		c.impl = deflateCompressLazy2
		c.pg = new(greedyState)
		c.maxSearchDepth = 300
		c.niceMatchLength = deflateMaxMatchLen
	case 9:
		c.impl = deflateCompressLazy2
		c.pg = new(greedyState)
		c.maxSearchDepth = 600
		c.niceMatchLength = deflateMaxMatchLen
	case 10:
		c.impl = deflateCompressNearOptimal
		c.pnState = new(nearOptimalState)
		c.maxSearchDepth = 35
		c.niceMatchLength = 75
		c.pn = nearOptimalParams{maxOptimPasses: 2, minImprovementToContinue: 32, minBitsToUseNonfinalPath: 32,
			maxLenToOptimizeStaticBlock: 0}
		c.offsetSlotFull = &c.pnState.offsetSlotFull
		c.initOffsetSlotFull()
	case 11:
		c.impl = deflateCompressNearOptimal
		c.pnState = new(nearOptimalState)
		c.maxSearchDepth = 100
		c.niceMatchLength = 150
		c.pn = nearOptimalParams{maxOptimPasses: 4, minImprovementToContinue: 16, minBitsToUseNonfinalPath: 16,
			maxLenToOptimizeStaticBlock: 1000}
		c.offsetSlotFull = &c.pnState.offsetSlotFull
		c.initOffsetSlotFull()
	default: // 12
		c.impl = deflateCompressNearOptimal
		c.pnState = new(nearOptimalState)
		c.maxSearchDepth = 300
		c.niceMatchLength = deflateMaxMatchLen
		c.pn = nearOptimalParams{maxOptimPasses: 10, minImprovementToContinue: 1, minBitsToUseNonfinalPath: 1,
			maxLenToOptimizeStaticBlock: 10000}
		c.offsetSlotFull = &c.pnState.offsetSlotFull
		c.initOffsetSlotFull()
	}

	c.initStaticCodes()
	return c, nil
}

// Level returns the compression level the compressor was created with
// (6 when it was created with -1). port of libdeflate_get_compression_level
// A NewHuffmanOnlyCompressor reports -2 (not in C).
func (c *Compressor) Level() int {
	if c.huffmanOnly {
		return HuffmanOnly
	}
	return int(c.compressionLevel)
}

// Compress compresses src into dst as raw DEFLATE and returns the
// compressed size, or 0 if dst is too small (bytes of dst beyond the
// returned size, and all of dst after a 0 return, may have been written).
// The output is byte-identical to libdeflate's. A buffer of
// CompressBound(len(src)) bytes is always large enough. Compress does not
// allocate.
// port of libdeflate_deflate_compress
func (c *Compressor) Compress(dst, src []byte) int {
	// For extremely short inputs, or for compression level 0, just output
	// uncompressed blocks.
	if len(src) <= c.maxPassthroughSize {
		return deflateCompressNone(src, dst)
	}

	// Initialize the output bitstream structure. It is kept in the
	// compressor (C: a local): passed by pointer through the c.impl
	// function value, a local would escape to the heap on every call. Its
	// out is cleared before returning so that dst is not retained.
	os := &c.os
	os.init(dst)

	// Call the actual compression function. The capacity is clamped so that
	// the matchfinders' bounded loads (loadLE32) cannot read past src.
	c.impl(c, src[:len(src):len(src)], os)

	// Return 0 if the output buffer is too small.
	if os.overflow {
		os.out = nil
		return 0
	}

	// Write the final byte if needed. This can't overflow the output
	// buffer because flushBlock would have set the overflow flag if there
	// wasn't enough space remaining for the full final block.
	if os.bitcount != 0 {
		os.out[os.next] = byte(os.bitbuf)
		os.next++
	}

	// Return the compressed size in bytes.
	os.out = nil
	return os.next
}

// CompressBound returns an upper bound on the compressed size of any input
// of at most n bytes, for every level: 5 bytes of stored-block overhead per
// MIN_BLOCK_LENGTH bytes (at least one block) plus n. The receiver is not
// used (like C with a NULL compressor), so it may be nil. It panics if
// n < 0. A bound above math.MaxInt (no Go buffer can be that large) is
// returned as math.MaxInt; that happens for n within about 0.1% of
// math.MaxInt (only a real buffer size where int is 32 bits).
// port of libdeflate_deflate_compress_bound
func (c *Compressor) CompressBound(n int) int {
	if n < 0 {
		panic("deflate: CompressBound of a negative size")
	}
	// Since the compressor never uses a compressed block when an
	// uncompressed block is cheaper, the worst case is the case where only
	// uncompressed blocks are used; the minimum length passed to
	// flushBlock is MIN_BLOCK_LENGTH (except the final block), and
	// 2 * MIN_BLOCK_LENGTH <= UINT16_MAX, so splitting at UINT16_MAX never
	// adds blocks. Empty inputs need 1 (empty) block. Computed in uint
	// (C: size_t), which cannot wrap for n <= MaxInt.
	un := uint(n)
	maxBlocks := max((un+minBlockLength-1)/minBlockLength, 1)
	bound := (5 * maxBlocks) + un
	if bound > math.MaxInt {
		return math.MaxInt
	}
	return int(bound)
}

// Compile-time check. C: STATIC_ASSERT(2 * MIN_BLOCK_LENGTH <= UINT16_MAX)
const _ = uint(math.MaxUint16 - 2*minBlockLength)
