//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useMinCostPathAsm selects minCostPathAsm for the backward pass of
// findMinCostPath. It needs only amd64 (CMOV), so it is always available; the
// switch exists for the assembly equivalence tests.
var useMinCostPathAsm bool

func init() {
	cpu.RegisterAsm("deflate.minCostPath", &useMinCostPathAsm, func() bool { return true })
}

// The byte offsets compress_near_optimal_amd64.s uses: nearOptimalState
// (CACHE_OFF, NODES_OFF, NODES_LAST = &optimumNodes[maxBlockLength],
// NODES_COUNT_M1 = maxBlockLength, the
// costs COST_LIT, COST_LEN, COST_OFFSLOT), deflateOptimumNode (8 bytes:
// costToEnd, item), lzMatch (length at 0, offset at 2) and the constants
// written in.
const (
	_ = uint(unsafe.Offsetof(nearOptimalState{}.matchCache) - 524288)
	_ = uint(524288 - unsafe.Offsetof(nearOptimalState{}.matchCache))
	_ = uint(unsafe.Offsetof(nearOptimalState{}.optimumNodes) - 6526340)
	_ = uint(6526340 - unsafe.Offsetof(nearOptimalState{}.optimumNodes))
	_ = uint(unsafe.Offsetof(nearOptimalState{}.optimumNodes) + maxBlockLength*8 - 8966332)
	_ = uint(8966332 - unsafe.Offsetof(nearOptimalState{}.optimumNodes) - maxBlockLength*8)
	_ = uint(maxBlockLength-304999) + uint(304999-maxBlockLength)
	_ = uint(len(nearOptimalState{}.optimumNodes) - maxBlockLength - 1)
	_ = uint(maxBlockLength + 1 - len(nearOptimalState{}.optimumNodes))
	_ = uint(unsafe.Offsetof(nearOptimalState{}.costs) - 8966340)
	_ = uint(8966340 - unsafe.Offsetof(nearOptimalState{}.costs))
	_ = uint(unsafe.Offsetof(deflateCosts{}.literal) - 0)
	_ = uint(0 - unsafe.Offsetof(deflateCosts{}.literal))
	_ = uint(unsafe.Offsetof(deflateCosts{}.length) - 1024)
	_ = uint(1024 - unsafe.Offsetof(deflateCosts{}.length))
	_ = uint(unsafe.Offsetof(deflateCosts{}.offsetSlot) - 2060)
	_ = uint(2060 - unsafe.Offsetof(deflateCosts{}.offsetSlot))
	_ = uint(len(deflateCosts{}.offsetSlot) - 32)
	_ = uint(32 - len(deflateCosts{}.offsetSlot))
	_ = uint(unsafe.Sizeof(deflateOptimumNode{}) - 8)
	_ = uint(8 - unsafe.Sizeof(deflateOptimumNode{}))
	_ = uint(unsafe.Offsetof(deflateOptimumNode{}.item) - 4)
	_ = uint(4 - unsafe.Offsetof(deflateOptimumNode{}.item))
	_ = uint(unsafe.Sizeof(lzMatch{}) - 4)
	_ = uint(4 - unsafe.Sizeof(lzMatch{}))
	_ = uint(unsafe.Offsetof(lzMatch{}.offset) - 2)
	_ = uint(2 - unsafe.Offsetof(lzMatch{}.offset))
	_ = uint(optimumOffsetShift-9) + uint(9-optimumOffsetShift)
	_ = uint(deflateMaxMatchOffset-32768) + uint(32768-deflateMaxMatchOffset)
	_ = uint(deflateMaxMatchLen-258) + uint(258-deflateMaxMatchLen)
	_ = uint(deflateMinMatchLen-3) + uint(3-deflateMinMatchLen)
)

// minCostPathAsm runs the backward pass of findMinCostPath from optimum node
// cur (its costToEnd already set) down to node stop with the match cache
// entries before cp, and returns the cache index of position stop's first
// entry (its first match, else its header): the cp to continue from.
// Its preconditions are listed in the BOUNDS block.
// C: deflate_find_min_cost_path
//
//go:noescape
func minCostPathAsm(ns *nearOptimalState, slotFull *[deflateMaxMatchOffset + 1]uint8, cur, cp, stop int) int
