//go:build !amd64 || appengine || noasm || purego || !gc

package hist

// histTables is C's 4 (see hist_amd64.go for the amd64 loop's 8).
const histTables = 4

// countTables adds the counts of src (non-empty) to the zeroed ws, leaves
// their sum in ws[0:256] and returns its largest entry.
func countTables(ws *[histTables * 256]uint32, src []byte) uint32 {
	countStripes(ws, src)
	return countMergeGeneric(ws)
}
