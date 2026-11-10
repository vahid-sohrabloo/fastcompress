//go:build amd64 && !appengine && !noasm && !purego && gc

package hist

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useHistAsm selects the assembly counting loop (baseline amd64).
var useHistAsm bool

func init() {
	cpu.RegisterAsm("hist", &useHistAsm, func() bool { return true })
}

// countStripesAsm adds the counts of the n bytes at src to ws, byte i of
// each 8 going to table i%4. Preconditions: n > 0 and src holds n bytes.
//
//go:noescape
func countStripesAsm(ws *[4 * 256]uint32, src *byte, n int)

// countMergeAsm adds tables 1-3 of ws into table 0, 4 lanes at a time, and
// returns the largest sum. Precondition: every sum is below 2^31.
//
//go:noescape
func countMergeAsm(ws *[4 * 256]uint32) uint32

// countMerge is countMergeGeneric, in assembly when it is on.
func countMerge(ws *[4 * 256]uint32, n int) uint32 {
	if !useHistAsm || n >= 1<<31 {
		return countMergeGeneric(ws)
	}
	return countMergeAsm(ws)
}
