//go:build !amd64 || appengine || noasm || purego || !gc

package hist

// countMerge: the assembly is amd64 only.
func countMerge(ws *[4 * 256]uint32, _ int) uint32 { return countMergeGeneric(ws) }
