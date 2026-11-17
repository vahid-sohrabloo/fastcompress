//go:build arm64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
)

// useHCSearchAsm selects hcLongestMatchAsm for the searches of
// deflateCompressGreedy (levels 2..4) and of deflateCompressLazyGeneric
// where lazyLoopAsm does not run. It
// needs only baseline arm64 (NEON for the window slide), so it is always
// available; the switch exists for the assembly equivalence tests.
var useHCSearchAsm bool

func init() {
	cpu.RegisterAsm("deflate.hcSearch", &useHCSearchAsm, func() bool { return true })
}

// The byte offsets of the tables that hc_matchfinder_arm64.s uses
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

// hcLongestMatchAsm is mf.longestMatch(in, inBase, inNext, bestLen,
// maxLen, niceLen, maxSearchDepth, nextHashes) in assembly (same results,
// same matchfinder, *inBase and nextHashes updates). Its preconditions are
// listed in the BOUNDS block. C: hc_matchfinder_longest_match
//
//go:noescape
func hcLongestMatchAsm(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, maxLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32)

// hcLongestMatchAsmForTest is hcLongestMatchAsm (the assembly equivalence
// tests' entry, as on amd64).
func hcLongestMatchAsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, maxLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32) {
	return hcLongestMatchAsm(mf, in, inBase, inNext, bestLen, maxLen, niceLen, maxSearchDepth, nextHashes)
}

// hcLongestMatch258AsmForTest is hcLongestMatchAsm with maxLen 258.
func hcLongestMatch258AsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32) {
	return hcLongestMatchAsm(mf, in, inBase, inNext, bestLen, 258, niceLen, maxSearchDepth, nextHashes)
}

// useGreedyLoopAsm is false: arm64 has the search in assembly, not the
// whole greedy loop.
const useGreedyLoopAsm = false

// greedyLoopAsm is never called (useGreedyLoopAsm is false).
func greedyLoopAsm(*greedyState, *deflateFreqs, *blockSplitStats, []byte, *greedyLoopState) {}

// useLazyLoopAsm selects lazyLoopAsm for the per-position loop of
// deflateCompressLazyGeneric (always available, like useHCSearchAsm).
var useLazyLoopAsm bool

func init() {
	cpu.RegisterAsm("deflate.lazyLoop", &useLazyLoopAsm, func() bool { return true })
}

// The layouts lazyLoopAsm uses: greedyState (GSEQ_OFF, GSEQ_END),
// deflateSequence, deflateFreqs (FREQ_LEN, FREQ_OFF), blockSplitStats
// (OBS_MATCH, OBS_NUM), lazyLoopState (its field offsets) and the constants
// it has written in.
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

// lazyLoopAsm runs the per-position loop of deflateCompressLazyGeneric
// (search, the lazy / lazy2 look-ahead, literals and matches with the block
// split statistics, skips) from st.inNext, and returns at the start of a
// position with st.inNext >= st.nextRecalcMinLen or after the first position
// at which the block may end, with st (including maxLen and niceLen)
// updated, the sequences, c.freqs and stats filled like the Go loop would.
// Its preconditions are listed in the BOUNDS block.
// C: deflate_compress_lazy_generic
//
//go:noescape
func lazyLoopAsm(pg *greedyState, freqs *deflateFreqs, stats *blockSplitStats, in []byte, st *lazyLoopState)
