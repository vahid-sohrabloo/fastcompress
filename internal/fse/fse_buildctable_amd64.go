//go:build amd64 && !appengine && !noasm && !purego && gc

package fse

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useFSEBuildAsm selects the assembly table builder and useFSENormAsm the
// assembly normalizer (baseline amd64).
var useFSEBuildAsm, useFSENormAsm bool

func init() {
	cpu.RegisterAsm("fse-build", &useFSEBuildAsm, func() bool { return true })
	cpu.RegisterAsm("fse-norm", &useFSENormAsm, func() bool { return true })
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

// normalizeCountAsm is gcc's FSE_normalizeCount (v1.5.7,
// ref/zstd/programs/zstd), FSE_normalizeM2 inlined, transliterated. It
// returns C's result: the table log, 0 for RLE, or an error code (-1
// generic, -44 table log too large). Preconditions (checked by
// normalizeCount): norm and count have maxSymbol+1 entries, 1 <= maxSymbol,
// and 1 <= total < 2^32 (C divides by (U32)total and takes its highbit32).
//
//go:noescape
func normalizeCountAsm(norm *int16, tableLog uint, count *uint32, total, maxSymbol, useLowProbCount uint) int64

// normalizeCount is normalizeCountGeneric, in assembly when it is on.
func normalizeCount(norm []int16, tableLog uint, count []uint32, total int, maxSymbol uint, useLowProbCount bool) (uint, error) {
	if !useFSENormAsm || maxSymbol == 0 || uint(len(norm)) <= maxSymbol || uint(len(count)) <= maxSymbol ||
		total <= 0 || total >= 1<<32 {
		return normalizeCountGeneric(norm, tableLog, count, total, maxSymbol, useLowProbCount)
	}
	var lowProb uint
	if useLowProbCount {
		lowProb = 1
	}
	switch r := normalizeCountAsm(&norm[0], tableLog, &count[0], uint(total), maxSymbol, lowProb); r {
	case -1:
		return 0, ErrGeneric
	case -44:
		return 0, ErrTableLogTooLarge
	default:
		return uint(r), nil
	}
}
