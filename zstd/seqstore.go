package zstd

import "math/bits"

// Format constants. C: lib/common/zstd_internal.h
const (
	repNum = 3 // C: ZSTD_REP_NUM

	minMatch = 3 // C: MINMATCH

	litHufLog     = 11 // C: LitHufLog
	maxML         = 52 // C: MaxML
	maxLL         = 35 // C: MaxLL
	defaultMaxOff = 28 // C: DefaultMaxOff
	maxOff        = 31 // C: MaxOff
	maxSeq        = max(maxLL, maxML)
	mlFSELog      = 9 // C: MLFSELog
	llFSELog      = 9 // C: LLFSELog
	offFSELog     = 8 // C: OffFSELog
	maxFSELog     = max(mlFSELog, llFSELog, offFSELog)

	longNbSeq = 0x7F00 // C: LONGNBSEQ

	minLiteralsFor4Streams = 6 // C: MIN_LITERALS_FOR_4_STREAMS

	// streamAccumulatorMin is C's STREAM_ACCUMULATOR_MIN on 64-bit targets
	// (bitstream.h: STREAM_ACCUMULATOR_MIN_64).
	streamAccumulatorMin = 57

	llDefaultNormLog = 6 // C: LL_DEFAULTNORMLOG
	mlDefaultNormLog = 6 // C: ML_DEFAULTNORMLOG
	ofDefaultNormLog = 5 // C: OF_DEFAULTNORMLOG
)

// symbolEncodingType is the compression mode of a literals or sequences
// section. C: zstd_internal.h:SymbolEncodingType_e
type symbolEncodingType uint32

const (
	setBasic symbolEncodingType = iota
	setRLE
	setCompressed
	setRepeat
)

// C: zstd_internal.h:LL_bits
var llBits = [maxLL + 1]uint8{
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 1, 2, 2, 3, 3,
	4, 6, 7, 8, 9, 10, 11, 12,
	13, 14, 15, 16,
}

// C: zstd_internal.h:LL_defaultNorm
var llDefaultNorm = [maxLL + 1]int16{
	4, 3, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 1, 1, 1,
	2, 2, 2, 2, 2, 2, 2, 2,
	2, 3, 2, 1, 1, 1, 1, 1,
	-1, -1, -1, -1,
}

// C: zstd_internal.h:ML_bits
var mlBits = [maxML + 1]uint8{
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 1, 2, 2, 3, 3,
	4, 4, 5, 7, 8, 9, 10, 11,
	12, 13, 14, 15, 16,
}

// C: zstd_internal.h:ML_defaultNorm
var mlDefaultNorm = [maxML + 1]int16{
	1, 4, 3, 2, 2, 2, 2, 2,
	2, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, -1, -1,
	-1, -1, -1, -1, -1,
}

// C: zstd_internal.h:OF_defaultNorm
var ofDefaultNorm = [defaultMaxOff + 1]int16{
	1, 1, 1, 1, 1, 1, 2, 2,
	2, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	-1, -1, -1, -1, -1,
}

// seqDef is one stored sequence. C: zstd_compress_internal.h:SeqDef
type seqDef struct {
	offBase   uint32 // offset + repNum, or repcode 1..3
	litLength uint16
	mlBase    uint16 // matchLength - minMatch
}

// Long-length markers. C: zstd_compress_internal.h:ZSTD_longLengthType_e
const (
	lltNone          = 0 // no longLengthType
	lltLiteralLength = 1 // represents a long literal
	lltMatchLength   = 2 // represents a long match
)

// seqStore holds the sequences and literals of one block.
// C: zstd_compress_internal.h:SeqStore_t. C's start/end pointer pairs are Go
// slices: sequences is sequencesStart..sequences (len = nbSeq), literals is
// litStart..lit (len = litSize); appending replaces advancing the end
// pointer. llCode, mlCode and ofCode must have len >= len(sequences) (C: the
// maxNbSeq-sized code tables); only their first len(sequences) entries are
// written. C's maxNbSeq/maxNbLit are the slices' capacities.
//
// At most one sequence may hold a litLength or matchLength above 0xFFFF:
// the one at longLengthPos, whose field named by longLengthType is stored
// minus 0x10000.
type seqStore struct {
	sequences              []seqDef
	literals               []byte
	llCode, mlCode, ofCode []byte
	longLengthType         int
	longLengthPos          uint32
}

// resetSeqStore. C: zstd_compress.c:ZSTD_resetSeqStore
func resetSeqStore(ss *seqStore) {
	ss.literals = ss.literals[:0]
	ss.sequences = ss.sequences[:0]
	ss.longLengthType = lltNone
}

// highbit32. C: bits.h:ZSTD_highbit32 (v != 0)
func highbit32(v uint32) uint32 { return uint32(bits.Len32(v)) - 1 }

// C: zstd_compress_internal.h:ZSTD_LLcode
var llCodeTable = [64]uint8{
	0, 1, 2, 3, 4, 5, 6, 7,
	8, 9, 10, 11, 12, 13, 14, 15,
	16, 16, 17, 17, 18, 18, 19, 19,
	20, 20, 20, 20, 21, 21, 21, 21,
	22, 22, 22, 22, 22, 22, 22, 22,
	23, 23, 23, 23, 23, 23, 23, 23,
	24, 24, 24, 24, 24, 24, 24, 24,
	24, 24, 24, 24, 24, 24, 24, 24,
}

// llCode. C: zstd_compress_internal.h:ZSTD_LLcode
func llCode(litLength uint32) uint32 {
	const llDeltaCode = 19
	if litLength > 63 {
		return highbit32(litLength) + llDeltaCode
	}
	return uint32(llCodeTable[litLength])
}

// C: zstd_compress_internal.h:ZSTD_MLcode
var mlCodeTable = [128]uint8{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
	32, 32, 33, 33, 34, 34, 35, 35, 36, 36, 36, 36, 37, 37, 37, 37,
	38, 38, 38, 38, 38, 38, 38, 38, 39, 39, 39, 39, 39, 39, 39, 39,
	40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40,
	41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41,
	42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42,
	42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42,
}

// mlCode. mlBase = matchLength - minMatch.
// C: zstd_compress_internal.h:ZSTD_MLcode
func mlCode(mlBase uint32) uint32 {
	const mlDeltaCode = 36
	if mlBase > 127 {
		return highbit32(mlBase) + mlDeltaCode
	}
	return uint32(mlCodeTable[mlBase])
}

// seqToCodes fills the code tables from the sequences and reports whether
// any offset code needs the long-offset path.
// C: zstd_compress.c:ZSTD_seqToCodes. longOffsets can only be set on 32-bit
// targets (ofCode >= STREAM_ACCUMULATOR_MIN_32); this port has a 64-bit bit
// container, so it is always false, as in C on 64-bit.
func seqToCodes(ss *seqStore) bool {
	nbSeq := len(ss.sequences)
	llCodes := ss.llCode[:nbSeq]
	ofCodes := ss.ofCode[:nbSeq]
	mlCodes := ss.mlCode[:nbSeq]
	for u, s := range ss.sequences {
		llCodes[u] = byte(llCode(uint32(s.litLength)))
		ofCodes[u] = byte(highbit32(s.offBase))
		mlCodes[u] = byte(mlCode(uint32(s.mlBase)))
	}
	if ss.longLengthType == lltLiteralLength {
		llCodes[ss.longLengthPos] = maxLL
	}
	if ss.longLengthType == lltMatchLength {
		mlCodes[ss.longLengthPos] = maxML
	}
	return false
}
