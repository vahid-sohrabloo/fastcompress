//go:build !amd64 || appengine || noasm || purego || !gc

package fse

// buildCTable: the assembly is amd64 only.
func buildCTable(ct CTable, norm []int16, maxSymbol, tableLog uint, wksp []byte) error {
	return buildCTableGeneric(ct, norm, maxSymbol, tableLog, wksp)
}
