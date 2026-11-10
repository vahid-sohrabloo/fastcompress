//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
)

// useBtMatchLoopAsm selects btMatchLoopAsm for the matchfinding loop of
// deflateCompressNearOptimal. It needs only amd64 (its TZCNTs have nonzero
// operands, where TZCNT equals BSF), so it is always available; the switch
// exists for the assembly equivalence tests.
var useBtMatchLoopAsm bool

func init() {
	cpu.RegisterAsm("deflate.btMatchLoop", &useBtMatchLoopAsm, func() bool { return true })
}

// The byte offsets bt_matchfinder_amd64.s uses: nearOptimalState (the
// matchfinder tables HASH4_OFF and CHILD_OFF, the match cache CACHE_OFF and
// its end CACHE_END = match_cache[MATCH_CACHE_LENGTH], new_match_len_freqs
// NMLF_OFF), lzMatch (length at 0, offset at 2), blockSplitStats (OBS_MATCH,
// OBS_NUM, as in hc_matchfinder_amd64.go), btMatchLoopState (its field
// offsets) and the constants written in.
const (
	_ = uint(unsafe.Offsetof(nearOptimalState{}.btMf) - 0)
	_ = uint(0 - unsafe.Offsetof(nearOptimalState{}.btMf))
	_ = uint(unsafe.Offsetof(btMatchfinder{}.hash3Tab) - 0)
	_ = uint(0 - unsafe.Offsetof(btMatchfinder{}.hash3Tab))
	_ = uint(unsafe.Offsetof(btMatchfinder{}.hash4Tab) - 262144)
	_ = uint(262144 - unsafe.Offsetof(btMatchfinder{}.hash4Tab))
	_ = uint(unsafe.Offsetof(btMatchfinder{}.childTab) - 393216)
	_ = uint(393216 - unsafe.Offsetof(btMatchfinder{}.childTab))
	_ = uint(unsafe.Offsetof(nearOptimalState{}.matchCache) - 524288)
	_ = uint(524288 - unsafe.Offsetof(nearOptimalState{}.matchCache))
	_ = uint(matchCacheLength-1500000) + uint(1500000-matchCacheLength)
	_ = uint(unsafe.Offsetof(nearOptimalState{}.newMatchLenFreqs) - 9003532)
	_ = uint(9003532 - unsafe.Offsetof(nearOptimalState{}.newMatchLenFreqs))
	_ = uint(unsafe.Sizeof(lzMatch{}) - 4)
	_ = uint(4 - unsafe.Sizeof(lzMatch{}))
	_ = uint(unsafe.Offsetof(lzMatch{}.offset) - 2)
	_ = uint(2 - unsafe.Offsetof(lzMatch{}.offset))
	_ = uint(btMatchfinderHash3Order-16) + uint(16-btMatchfinderHash3Order)
	_ = uint(btMatchfinderHash4Order-16) + uint(16-btMatchfinderHash4Order)
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.inCurBase) - 8)
	_ = uint(8 - unsafe.Offsetof(btMatchLoopState{}.inCurBase))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.limit) - 16)
	_ = uint(16 - unsafe.Offsetof(btMatchLoopState{}.limit))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.inBlockBegin) - 24)
	_ = uint(24 - unsafe.Offsetof(btMatchLoopState{}.inBlockBegin))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.inMaxBlockEnd) - 32)
	_ = uint(32 - unsafe.Offsetof(btMatchLoopState{}.inMaxBlockEnd))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.nextObservation) - 40)
	_ = uint(40 - unsafe.Offsetof(btMatchLoopState{}.nextObservation))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.cachePtr) - 48)
	_ = uint(48 - unsafe.Offsetof(btMatchLoopState{}.cachePtr))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.nextHash3) - 56)
	_ = uint(56 - unsafe.Offsetof(btMatchLoopState{}.nextHash3))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.nextHash4) - 60)
	_ = uint(60 - unsafe.Offsetof(btMatchLoopState{}.nextHash4))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.minLen) - 64)
	_ = uint(64 - unsafe.Offsetof(btMatchLoopState{}.minLen))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.niceLen) - 68)
	_ = uint(68 - unsafe.Offsetof(btMatchLoopState{}.niceLen))
	_ = uint(unsafe.Offsetof(btMatchLoopState{}.maxSearchDepth) - 72)
	_ = uint(72 - unsafe.Offsetof(btMatchLoopState{}.maxSearchDepth))
)

// btMatchLoopAsm runs the matchfinding loop of deflateCompressNearOptimal
// (getMatches with max_len 258, the block split observations, the match
// cache entries and the skipByte calls after a match of at least nice_len)
// from st.inNext, starting positions only while st.inNext < st.limit, and
// returns after the first position at which the block may end (st.inNext >=
// st.inMaxBlockEnd, st.cachePtr >= MATCH_CACHE_LENGTH or
// stats.readyToCheckBlock) or at st.limit, with st updated and the
// matchfinder, the match cache, ns.newMatchLenFreqs and stats filled like
// the Go loop would. Its preconditions are listed in the BOUNDS block.
// C: deflate_compress_near_optimal (the matchfinding loop of a block)
//
//go:noescape
func btMatchLoopAsm(ns *nearOptimalState, stats *blockSplitStats, in []byte, st *btMatchLoopState)
