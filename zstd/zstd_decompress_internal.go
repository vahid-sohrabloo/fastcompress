package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/huf"

// Objects and definitions shared within the decoder.
// C: lib/decompress/zstd_decompress_internal.h (v1.5.7)

// C: zstd_decompress_internal.h:LL_base
var llBase = [maxLL + 1]uint32{
	0, 1, 2, 3, 4, 5, 6, 7,
	8, 9, 10, 11, 12, 13, 14, 15,
	16, 18, 20, 22, 24, 28, 32, 40,
	48, 64, 0x80, 0x100, 0x200, 0x400, 0x800, 0x1000,
	0x2000, 0x4000, 0x8000, 0x10000}

// C: zstd_decompress_internal.h:OF_base
var ofBase = [maxOff + 1]uint32{
	0, 1, 1, 5, 0xD, 0x1D, 0x3D, 0x7D,
	0xFD, 0x1FD, 0x3FD, 0x7FD, 0xFFD, 0x1FFD, 0x3FFD, 0x7FFD,
	0xFFFD, 0x1FFFD, 0x3FFFD, 0x7FFFD, 0xFFFFD, 0x1FFFFD, 0x3FFFFD, 0x7FFFFD,
	0xFFFFFD, 0x1FFFFFD, 0x3FFFFFD, 0x7FFFFFD, 0xFFFFFFD, 0x1FFFFFFD, 0x3FFFFFFD, 0x7FFFFFFD}

// C: zstd_decompress_internal.h:OF_bits
var ofBits = [maxOff + 1]uint8{
	0, 1, 2, 3, 4, 5, 6, 7,
	8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23,
	24, 25, 26, 27, 28, 29, 30, 31}

// C: zstd_decompress_internal.h:ML_base
var mlBase = [maxML + 1]uint32{
	3, 4, 5, 6, 7, 8, 9, 10,
	11, 12, 13, 14, 15, 16, 17, 18,
	19, 20, 21, 22, 23, 24, 25, 26,
	27, 28, 29, 30, 31, 32, 33, 34,
	35, 37, 39, 41, 43, 47, 51, 59,
	67, 83, 99, 0x83, 0x103, 0x203, 0x403, 0x803,
	0x1003, 0x2003, 0x4003, 0x8003, 0x10003}

// seqSymbol is one FSE decoding cell of a sequence table.
// C: zstd_decompress_internal.h:ZSTD_seqSymbol
//
// Entry 0 of every table is the header, C's ZSTD_seqSymbol_header
// {U32 fastMode; U32 tableLog} stored over the cell as on a little-endian
// machine: fastMode in nextState (the higher bytes, nbAdditionalBits and
// nbBits, are fastMode's upper bytes) and tableLog in baseValue.
type seqSymbol struct {
	nextState        uint16
	nbAdditionalBits uint8
	nbBits           uint8
	baseValue        uint32
}

// seqSymbolTableSize. C: SEQSYMBOL_TABLE_SIZE
func seqSymbolTableSize(log int) int { return 1 + 1<<log }

// hufDTableCapacityLog. C: ZSTD_HUFFDTABLE_CAPACITY_LOG
const hufDTableCapacityLog = 12

// buildFSETableWksp is the workspace of buildFSETable.
// C: ZSTD_BUILD_FSE_TABLE_WKSP_SIZE bytes, carved by ZSTD_buildFSETable_body
// into U16 symbolNext[MaxSeq+1] then BYTE spread[(1<<MaxFSELog) + 8]; Go
// cannot type-pun a U32 array, so the two parts are typed fields.
type buildFSETableWksp struct {
	symbolNext [maxSeq + 1]uint16
	spread     [1<<maxFSELog + 8]byte
}

// entropyDTables. C: zstd_decompress_internal.h:ZSTD_entropyDTables_t
type entropyDTables struct {
	llTable   [1 + 1<<llFSELog]seqSymbol  // C: LLTable
	ofTable   [1 + 1<<offFSELog]seqSymbol // C: OFTable
	mlTable   [1 + 1<<mlFSELog]seqSymbol  // C: MLTable
	hufTable  huf.DTable                  // can accommodate HUF_decompress4X
	rep       [repNum]uint32
	workspace buildFSETableWksp
}

// Literal buffer sizes. C: zstd_decompress_internal.h
const (
	decoderInternalBuffer = 1 << 16   // C: ZSTD_DECODER_INTERNAL_BUFFER
	lbMin                 = 64        // C: ZSTD_LBMIN
	lbMax                 = 128 << 10 // C: ZSTD_LBMAX
	// litBufferExtraSize is the extra buffer that compensates when dst is
	// not large enough to store litBuffer.
	// C: ZSTD_LITBUFFEREXTRASIZE = BOUNDED(ZSTD_LBMIN, ZSTD_DECODER_INTERNAL_BUFFER, ZSTD_LBMAX)
	litBufferExtraSize = max(lbMin, min(decoderInternalBuffer, lbMax))
)

// litLocation. C: zstd_decompress_internal.h:ZSTD_litLocation_e
type litLocation uint8

const (
	notInDst litLocation = 0 // C: ZSTD_not_in_dst — stored entirely within litExtraBuffer
	inDst    litLocation = 1 // C: ZSTD_in_dst — stored entirely within dst (in memory after current output write)
	split    litLocation = 2 // C: ZSTD_split — split between litExtraBuffer and dst
)

// streamingOperation. C: zstd_decompress_block.h:streaming_operation
type streamingOperation uint8

const (
	notStreaming streamingOperation = 0 // C: not_streaming
	isStreaming  streamingOperation = 1 // C: is_streaming
)

// dctx is the decompression context. C: zstd_decompress_internal.h:ZSTD_DCtx_s
//
// C's pointers into the output become int offsets into the current output
// buffer prevDst (prefixStart, virtualStart, previousDstEnd), with the
// previous segment in extDict, as Phase 1 does with its window: C's dictEnd
// is the end of extDict, and virtualStart is prefixStart - len(extDict)
// (negative when extDict is longer than the prefix before it). The table
// pointers become slices of the table they point into (entry 0 is the
// header). The literal pointers litBuffer, litBufferEnd and litPtr are
// offsets into litBuf, the array C's pointers point into: the dst given to
// decodeLiteralsBlock, litExtraBuffer, or (literals referenced in place)
// the block's src. litBuffer is an offset into dst whenever
// litBufferLocation is inDst or split.
type dctx struct {
	llTptr  []seqSymbol // C: LLTptr
	mlTptr  []seqSymbol // C: MLTptr
	ofTptr  []seqSymbol // C: OFTptr
	hufPtr  *huf.DTable // C: HUFptr
	entropy entropyDTables

	previousDstEnd int    // detect continuity
	prefixStart    int    // start of current segment
	virtualStart   int    // virtual start of previous segment if it was just before current one
	extDict        []byte // the previous segment (C: virtualStart..dictEnd)
	// prevDst is the buffer previousDstEnd and prefixStart index, up to its
	// capacity; nil before the first output. C compares pointers to detect
	// continuity; Go compares the buffer's start and the offset.
	prevDst []byte

	fParams              frameHeader
	litEntropy           bool // C: U32 litEntropy
	fseEntropy           bool // C: U32 fseEntropy
	isFrameDecompression bool
	ddictIsCold          bool // C: int ddictIsCold; the dictionary task sets it

	litBuf            []byte
	litPtr            int
	litSize           int
	litBuffer         int
	litBufferEnd      int
	litBufferLocation litLocation
	litExtraBuffer    [litBufferExtraSize + wildcopyOverlength]byte // literal buffer can be split between storage within dst and within this scratch buffer
}
