//go:build amd64 && !appengine && !noasm && !purego && gc

package hist

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

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
