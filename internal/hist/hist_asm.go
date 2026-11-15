//go:build arm64 && !appengine && !noasm && !purego && gc

// The Go side of the arm64 counting loop's assembly (amd64 has its own
// 8-table loop, see hist_amd64.go).

package hist

func countStripes(ws *[4 * 256]uint32, src []byte) {
	if !useHistAsm || len(src) == 0 {
		countStripesGeneric(ws, src)
		return
	}
	countStripesAsm(ws, &src[0], len(src))
}
