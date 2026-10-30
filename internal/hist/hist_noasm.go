//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package hist

func countStripes(ws *[4 * 256]uint32, src []byte) { countStripesGeneric(ws, src) }
