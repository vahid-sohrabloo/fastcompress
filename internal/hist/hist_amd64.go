//go:build amd64 && !appengine && !noasm && !purego && gc

package hist

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useHistAsm selects the assembly counting loop (baseline amd64).
var useHistAsm bool

func init() {
	cpu.RegisterAsm("hist", &useHistAsm, func() bool { return true })
}

// histTables is the number of 256-entry tables countParallel spreads the
// counts over. C's HIST_count_parallel_wksp uses 4, byte i of each word
// going to table i. The amd64 loop counts byte i of each 8 into table i:
// a counter is then incremented at most once per 8 bytes from the same
// stripe, which halves the store-to-load chains on data with a repeating
// byte every 2 or 4 bytes (16-bit samples such as x-ray: L1 +9% on an
// i9-13900H), and costs nothing on other data. The sums are the same.
const histTables = 8

// countStripes8Asm adds the counts of the n bytes at src to ws, byte i of
// each 8 going to table i. Preconditions: n > 0 and src holds n bytes.
//
//go:noescape
func countStripes8Asm(ws *[8 * 256]uint32, src *byte, n int)

// countMerge8Asm adds tables 1-7 of ws into table 0, 4 lanes at a time,
// and returns the largest sum. Precondition: every sum is below 2^31.
//
//go:noescape
func countMerge8Asm(ws *[8 * 256]uint32) uint32

// countTables adds the counts of src (non-empty) to the zeroed ws, leaves
// their sum in ws[0:256] and returns its largest entry.
func countTables(ws *[histTables * 256]uint32, src []byte) uint32 {
	if !useHistAsm || len(src) >= 1<<31 {
		w4 := (*[4 * 256]uint32)(ws[:4*256])
		countStripesGeneric(w4, src)
		return countMergeGeneric(w4)
	}
	countStripes8Asm(ws, &src[0], len(src))
	return countMerge8Asm(ws)
}
