//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the counting loop's assembly, shared by amd64 and arm64.

package hist

func countStripes(ws *[4 * 256]uint32, src []byte) {
	if !useHistAsm || len(src) == 0 {
		countStripesGeneric(ws, src)
		return
	}
	countStripesAsm(ws, &src[0], len(src))
}
