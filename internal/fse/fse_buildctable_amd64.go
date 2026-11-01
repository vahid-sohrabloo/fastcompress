//go:build amd64 && !appengine && !noasm && !purego && gc

package fse

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useFSEBuildAsm selects the assembly table builder (baseline amd64).
var useFSEBuildAsm bool

func init() {
	cpu.RegisterAsm("fse-build", &useFSEBuildAsm, func() bool { return true })
}

// buildCTableAsm is gcc's FSE_buildCTable_wksp (v1.5.7,
// ref/zstd/programs/zstd), transliterated. It returns C's result (0, or -44
// for a workspace below BuildCTableWorkspaceSize), or -1 when norm does not
// sum to 1<<tableLog (undefined in C). It uses wksp as C does: cumul, then
// tableSymbol and the spread buffer. Preconditions (checked by buildCTable):
// 1 <= tableLog <= 12, maxSymbol < 1<<tableLog, maxSymbol <= 255, norm has
// maxSymbol+1 entries, ct has CTableSizeU32(tableLog, maxSymbol) words and
// wksp has BuildCTableWorkspaceSize(maxSymbol, tableLog)+8 bytes.
//
//go:noescape
func buildCTableAsm(ct *uint32, norm *int16, maxSymbol, tableLog uint, wksp *byte, wkspSize uint) int64

// buildCTable is buildCTableGeneric, in assembly when it is on.
func buildCTable(ct CTable, norm []int16, maxSymbol, tableLog uint, wksp []byte) error {
	// The +8: C's spread buffer can end 2 bytes past its workspace size (when
	// maxSymbol is odd); C's callers pass more.
	if !useFSEBuildAsm || tableLog-1 > 11 || maxSymbol > 255 || maxSymbol >= 1<<tableLog ||
		uint(len(norm)) <= maxSymbol || len(ct) < CTableSizeU32(tableLog, maxSymbol) ||
		len(wksp) < BuildCTableWorkspaceSize(maxSymbol, tableLog)+8 {
		return buildCTableGeneric(ct, norm, maxSymbol, tableLog, wksp)
	}
	if buildCTableAsm(&ct[0], &norm[0], maxSymbol, tableLog, unsafe.SliceData(wksp), uint(len(wksp))) != 0 {
		return buildCTableGeneric(ct, norm, maxSymbol, tableLog, wksp)
	}
	return nil
}
