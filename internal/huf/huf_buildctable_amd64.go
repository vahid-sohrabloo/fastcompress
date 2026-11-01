//go:build amd64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useHufBuildAsm selects the assembly Huffman table builder (baseline amd64).
var useHufBuildAsm bool

func init() {
	cpu.RegisterAsm("huf-build", &useHufBuildAsm, func() bool { return true })
}

// buildCTableAsm is gcc's HUF_buildCTable_wksp (v1.5.7, ref/zstd/programs/zstd),
// transliterated: it builds ct from count and returns C's result, maxNbBits or
// an error code (-46 maxSymbolValue too large, -1 generic), -2 when every
// count is zero, or -3 when maxNbBits is infeasible (more than 2^maxNbBits
// nonzero counts); C's behaviour is undefined for both. Preconditions
// (checked by buildCTable): len(ct) > maxSymbolValue+1 when maxSymbolValue
// <= 255, and maxNbBits <= 12.
//
//go:noescape
func buildCTableAsm(ct *CElt, count *[256]uint32, maxSymbolValue, maxNbBits uint32) uint64

// buildCTable builds ct from count, in assembly when it is on. C:
// huf_compress.c:HUF_buildCTable_wksp
func buildCTable(ct CTable, count *[256]uint32, maxSymbolValue, maxNbBits uint32) (uint32, error) {
	if !useHufBuildAsm || maxNbBits > TableLogMax || maxSymbolValue > SymbolValueMax {
		return buildCTableGeneric(ct, count, maxSymbolValue, maxNbBits)
	}
	_ = ct[maxSymbolValue+1]
	switch r := int64(buildCTableAsm(&ct[0], count, maxSymbolValue, maxNbBits)); r {
	case -1:
		return 0, ErrGeneric
	case -2:
		panic("huf: BuildCTable with every count zero")
	case -3:
		// Infeasible maxNbBits: the Go body raises its own panic (index out
		// of range in setMaxHeight), as before the assembly.
		return buildCTableGeneric(ct, count, maxSymbolValue, maxNbBits)
	default:
		return uint32(r), nil
	}
}
