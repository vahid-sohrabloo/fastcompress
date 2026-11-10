//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useFastestLoopAsm selects fastestLoopAsm for the per-position loop of
// deflateCompressFastest while at least DEFLATE_MAX_MATCH_LEN bytes remain.
// It needs only amd64 (its TZCNTs have nonzero operands, where TZCNT equals
// BSF), so it is always available; the switch exists for the assembly
// equivalence tests.
var useFastestLoopAsm bool

func init() {
	cpu.RegisterAsm("deflate.fastestLoop", &useFastestLoopAsm, func() bool { return true })
}

// The layouts compress_fastest_amd64.s uses: fastestState (SEQ_OFF, SEQ_END),
// deflateSequence (8 bytes: litrunlenAndLength, offset, offsetSlot),
// deflateFreqs (FREQ_LEN, FREQ_OFF), fastestLoopState (its field offsets) and
// the constants it has written in.
const (
	_ = uint(unsafe.Offsetof(fastestState{}.sequences) - 131072)
	_ = uint(131072 - unsafe.Offsetof(fastestState{}.sequences))
	_ = uint(unsafe.Sizeof(htMatchfinder{}) - 131072)
	_ = uint(131072 - unsafe.Sizeof(htMatchfinder{}))
	_ = uint(unsafe.Sizeof(deflateSequence{}) - 8)
	_ = uint(8 - unsafe.Sizeof(deflateSequence{}))
	_ = uint(unsafe.Offsetof(deflateSequence{}.offset) - 4)
	_ = uint(4 - unsafe.Offsetof(deflateSequence{}.offset))
	_ = uint(unsafe.Offsetof(deflateSequence{}.offsetSlot) - 6)
	_ = uint(6 - unsafe.Offsetof(deflateSequence{}.offsetSlot))
	_ = uint(fastSeqStoreLength - 8192)
	_ = uint(8192 - fastSeqStoreLength)
	_ = uint(seqLengthShift-23) + uint(23-seqLengthShift)
	_ = uint(deflateFirstLenSym*4-1028) + uint(1028-deflateFirstLenSym*4)
	_ = uint(unsafe.Offsetof(deflateFreqs{}.offset) - 1152)
	_ = uint(1152 - unsafe.Offsetof(deflateFreqs{}.offset))
	_ = uint(deflateMaxMatchLen-258) + uint(258-deflateMaxMatchLen)
	_ = uint(htMatchfinderHashOrder-15) + uint(15-htMatchfinderHashOrder)
	_ = uint(unsafe.Offsetof(fastestLoopState{}.inCurBase) - 8)
	_ = uint(8 - unsafe.Offsetof(fastestLoopState{}.inCurBase))
	_ = uint(unsafe.Offsetof(fastestLoopState{}.inMaxBlockEnd) - 16)
	_ = uint(16 - unsafe.Offsetof(fastestLoopState{}.inMaxBlockEnd))
	_ = uint(unsafe.Offsetof(fastestLoopState{}.seq) - 24)
	_ = uint(24 - unsafe.Offsetof(fastestLoopState{}.seq))
	_ = uint(unsafe.Offsetof(fastestLoopState{}.nextHash) - 32)
	_ = uint(32 - unsafe.Offsetof(fastestLoopState{}.nextHash))
	_ = uint(unsafe.Offsetof(fastestLoopState{}.niceLen) - 36)
	_ = uint(36 - unsafe.Offsetof(fastestLoopState{}.niceLen))
)

// fastestLoopAsm runs the per-position loop of deflateCompressFastest
// (search, choose literal or match, skip) from st.inNext while at least
// DEFLATE_MAX_MATCH_LEN bytes remain (max_len 258, nice_len st.niceLen), and
// returns at the end of the block (st.inNext >= st.inMaxBlockEnd or st.seq >=
// FAST_SEQ_STORE_LENGTH) or when fewer bytes remain, with st updated, the
// sequences and c.freqs filled like the Go loop would. Its preconditions are
// listed in the BOUNDS block. C: deflate_compress_fastest
//
//go:noescape
func fastestLoopAsm(pf *fastestState, freqs *deflateFreqs, in []byte, st *fastestLoopState)
