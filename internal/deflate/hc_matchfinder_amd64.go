//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useGreedyLoopAsm selects greedyLoopAsm for the per-position loop of
// deflateCompressGreedy while at least DEFLATE_MAX_MATCH_LEN bytes remain.
// It needs only amd64 (the TZCNTs of its search have nonzero operands, where
// TZCNT equals BSF), so it is always available; the switch exists for the
// assembly equivalence tests.
var useGreedyLoopAsm bool

// useLazyLoopAsm selects lazyLoopAsm for the per-position loop of
// deflateCompressLazyGeneric (always available, like useGreedyLoopAsm).
var useLazyLoopAsm bool

func init() {
	cpu.RegisterAsm("deflate.greedyLoop", &useGreedyLoopAsm, func() bool { return true })
	cpu.RegisterAsm("deflate.lazyLoop", &useLazyLoopAsm, func() bool { return true })
}

// The byte offsets of the tables that hc_matchfinder_amd64.s uses
// (HASH4_OFF, NEXT_OFF) and the struct size its window slide rebases.
const (
	_ = uint(unsafe.Offsetof(hcMatchfinder{}.hash3Tab) - 0)
	_ = uint(0 - unsafe.Offsetof(hcMatchfinder{}.hash3Tab))
	_ = uint(unsafe.Offsetof(hcMatchfinder{}.hash4Tab) - 65536)
	_ = uint(65536 - unsafe.Offsetof(hcMatchfinder{}.hash4Tab))
	_ = uint(unsafe.Offsetof(hcMatchfinder{}.nextTab) - 196608)
	_ = uint(196608 - unsafe.Offsetof(hcMatchfinder{}.nextTab))
	_ = uint(unsafe.Sizeof(hcMatchfinder{}) - 262144)
	_ = uint(262144 - unsafe.Sizeof(hcMatchfinder{}))
)

// The layouts greedyLoopAsm uses: greedyState (GSEQ_OFF, GSEQ_END),
// deflateSequence (8 bytes: litrunlenAndLength, offset, offsetSlot),
// deflateFreqs (FREQ_LEN, FREQ_OFF), blockSplitStats (OBS_MATCH, OBS_NUM),
// greedyLoopState (its field offsets) and the constants it has written in.
const (
	_ = uint(unsafe.Offsetof(greedyState{}.sequences) - 262144)
	_ = uint(262144 - unsafe.Offsetof(greedyState{}.sequences))
	_ = uint(seqStoreLength-50000) + uint(50000-seqStoreLength)
	_ = uint(unsafe.Sizeof(deflateSequence{}) - 8)
	_ = uint(8 - unsafe.Sizeof(deflateSequence{}))
	_ = uint(unsafe.Offsetof(deflateSequence{}.offset) - 4)
	_ = uint(4 - unsafe.Offsetof(deflateSequence{}.offset))
	_ = uint(unsafe.Offsetof(deflateSequence{}.offsetSlot) - 6)
	_ = uint(6 - unsafe.Offsetof(deflateSequence{}.offsetSlot))
	_ = uint(seqLengthShift-23) + uint(23-seqLengthShift)
	_ = uint(deflateFirstLenSym*4-1028) + uint(1028-deflateFirstLenSym*4)
	_ = uint(unsafe.Offsetof(deflateFreqs{}.offset) - 1152)
	_ = uint(1152 - unsafe.Offsetof(deflateFreqs{}.offset))
	_ = uint(numLiteralObservationTypes*4-32) + uint(32-numLiteralObservationTypes*4)
	_ = uint(numObservationTypes-10) + uint(10-numObservationTypes)
	_ = uint(unsafe.Offsetof(blockSplitStats{}.newObservations) - 0)
	_ = uint(0 - unsafe.Offsetof(blockSplitStats{}.newObservations))
	_ = uint(unsafe.Offsetof(blockSplitStats{}.numNewObservations) - 80)
	_ = uint(80 - unsafe.Offsetof(blockSplitStats{}.numNewObservations))
	_ = uint(numObservationsPerBlockCheck-512) + uint(512-numObservationsPerBlockCheck)
	_ = uint(minBlockLength-5000) + uint(5000-minBlockLength)
	_ = uint(deflateMaxMatchLen-258) + uint(258-deflateMaxMatchLen)
	_ = uint(deflateMinMatchLen-3) + uint(3-deflateMinMatchLen)
	_ = uint(unsafe.Offsetof(greedyLoopState{}.inCurBase) - 8)
	_ = uint(8 - unsafe.Offsetof(greedyLoopState{}.inCurBase))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.inBlockBegin) - 16)
	_ = uint(16 - unsafe.Offsetof(greedyLoopState{}.inBlockBegin))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.inMaxBlockEnd) - 24)
	_ = uint(24 - unsafe.Offsetof(greedyLoopState{}.inMaxBlockEnd))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.seq) - 32)
	_ = uint(32 - unsafe.Offsetof(greedyLoopState{}.seq))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.nextHash3) - 40)
	_ = uint(40 - unsafe.Offsetof(greedyLoopState{}.nextHash3))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.nextHash4) - 44)
	_ = uint(44 - unsafe.Offsetof(greedyLoopState{}.nextHash4))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.minLen) - 48)
	_ = uint(48 - unsafe.Offsetof(greedyLoopState{}.minLen))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.niceLen) - 52)
	_ = uint(52 - unsafe.Offsetof(greedyLoopState{}.niceLen))
	_ = uint(unsafe.Offsetof(greedyLoopState{}.maxSearchDepth) - 56)
	_ = uint(56 - unsafe.Offsetof(greedyLoopState{}.maxSearchDepth))
)

// lazyLoopState's field offsets (lazyLoopAsm).
const (
	_ = uint(unsafe.Offsetof(lazyLoopState{}.inCurBase) - 8)
	_ = uint(8 - unsafe.Offsetof(lazyLoopState{}.inCurBase))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.inBlockBegin) - 16)
	_ = uint(16 - unsafe.Offsetof(lazyLoopState{}.inBlockBegin))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.inMaxBlockEnd) - 24)
	_ = uint(24 - unsafe.Offsetof(lazyLoopState{}.inMaxBlockEnd))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.nextRecalcMinLen) - 32)
	_ = uint(32 - unsafe.Offsetof(lazyLoopState{}.nextRecalcMinLen))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.seq) - 40)
	_ = uint(40 - unsafe.Offsetof(lazyLoopState{}.seq))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.nextHash3) - 48)
	_ = uint(48 - unsafe.Offsetof(lazyLoopState{}.nextHash3))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.nextHash4) - 52)
	_ = uint(52 - unsafe.Offsetof(lazyLoopState{}.nextHash4))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.minLen) - 56)
	_ = uint(56 - unsafe.Offsetof(lazyLoopState{}.minLen))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.maxLen) - 60)
	_ = uint(60 - unsafe.Offsetof(lazyLoopState{}.maxLen))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.niceLen) - 64)
	_ = uint(64 - unsafe.Offsetof(lazyLoopState{}.niceLen))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.maxSearchDepth) - 68)
	_ = uint(68 - unsafe.Offsetof(lazyLoopState{}.maxSearchDepth))
	_ = uint(unsafe.Offsetof(lazyLoopState{}.lazy2) - 72)
	_ = uint(72 - unsafe.Offsetof(lazyLoopState{}.lazy2))
)

// hcLongestMatchAsmForTest is mf.longestMatch(in, inBase, inNext, bestLen,
// maxLen, niceLen, maxSearchDepth, nextHashes) in assembly (same results, same
// matchfinder, *inBase and nextHashes updates): a Go-callable entry to the
// search of greedyLoopAsm and lazyLoopAsm (hcsearch<>, with lzExtend and the
// window slide written in), used by the assembly equivalence tests to check
// that search call by call against the Go method. The compressors do not call
// it (their loops expand the same HCSEARCH macro in place; greedyLoopAsm
// expands its max_len 258 variant HCSEARCH258, whose entry is
// hcLongestMatch258AsmForTest). Its preconditions are listed in the BOUNDS
// block. C: hc_matchfinder_longest_match
//
//go:noescape
func hcLongestMatchAsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, maxLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32)

// hcLongestMatch258AsmForTest is hcLongestMatchAsmForTest with maxLen 258
// through the HCSEARCH258 expansion of greedyLoopAsm (hcsearch258<>), the
// tests' entry to that variant. Precondition: 258 <= len(in) - inNext.
// C: hc_matchfinder_longest_match
//
//go:noescape
func hcLongestMatch258AsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32)

// greedyLoopAsm runs the per-position loop of deflateCompressGreedy (search,
// choose literal or match with the block split statistics, skip) from
// st.inNext while at least DEFLATE_MAX_MATCH_LEN bytes remain (max_len 258,
// nice_len st.niceLen), and returns after the first position at which the
// block may end (st.inNext >= st.inMaxBlockEnd, st.seq >= SEQ_STORE_LENGTH or
// stats.readyToCheckBlock) or when fewer bytes remain, with st updated, the
// sequences, c.freqs and stats filled like the Go loop would. Its
// preconditions are listed in the BOUNDS block. C: deflate_compress_greedy
//
//go:noescape
func greedyLoopAsm(pg *greedyState, freqs *deflateFreqs, stats *blockSplitStats, in []byte, st *greedyLoopState)

// lazyLoopAsm runs the per-position loop of deflateCompressLazyGeneric
// (search, the lazy / lazy2 look-ahead, literals and matches with the block
// split statistics, skips) from st.inNext, and returns at the start of a
// position with st.inNext >= st.nextRecalcMinLen or after the first position
// at which the block may end (as greedyLoopAsm), with st (including maxLen
// and niceLen) updated, the sequences, c.freqs and stats filled like the Go
// loop would. Its preconditions are listed in the BOUNDS block.
// C: deflate_compress_lazy_generic
//
//go:noescape
func lazyLoopAsm(pg *greedyState, freqs *deflateFreqs, stats *blockSplitStats, in []byte, st *lazyLoopState)
