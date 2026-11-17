//go:build !amd64 || appengine || noasm || purego || !gc

package huf

// buildCTable: the assembly is amd64 only.
func buildCTable(ct CTable, count *[256]uint32, maxSymbolValue, maxNbBits uint32) (uint32, error) {
	return buildCTableGeneric(ct, count, maxSymbolValue, maxNbBits)
}
