//go:build !amd64 || appengine || noasm || purego || !gc

package fse

// buildCTable and normalizeCount: the assembly is amd64 only.
func buildCTable(ct CTable, norm []int16, maxSymbol, tableLog uint, wksp []byte) error {
	return buildCTableGeneric(ct, norm, maxSymbol, tableLog, wksp)
}

func normalizeCount(norm []int16, tableLog uint, count []uint32, total int, maxSymbol uint, useLowProbCount bool) (uint, error) {
	return normalizeCountGeneric(norm, tableLog, count, total, maxSymbol, useLowProbCount)
}

// CompressWeightsUsingCTable is CompressUsingCTable for a src whose bytes
// are all at most ct's maxSymbol (huf's weights).
func CompressWeightsUsingCTable(dst, src []byte, ct CTable) (int, error) {
	return CompressUsingCTable(dst, src, ct)
}
