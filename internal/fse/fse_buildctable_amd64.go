//go:build amd64 && !appengine && !noasm && !purego && gc

package fse

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useFSEBuildAsm selects the assembly table builder, useFSENormAsm the
// assembly normalizer and useFSECompressAsm the assembly encoder used for
// huf's weights (baseline amd64).
var useFSEBuildAsm, useFSENormAsm, useFSECompressAsm bool

func init() {
	cpu.RegisterAsm("fse-build", &useFSEBuildAsm, func() bool { return true })
	cpu.RegisterAsm("fse-norm", &useFSENormAsm, func() bool { return true })
	cpu.RegisterAsm("fse-compress", &useFSECompressAsm, func() bool { return true })
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

// compressUsingCTableAsm is gcc's FSE_compress_usingCTable_generic (v1.5.7,
// ref/zstd/programs/zstd), transliterated. It returns C's result: the
// stream size, or 0 when it does not fit (or srcSize <= 2, dstSize <= 8).
// fast 1 skips the end-of-buffer clamp: it needs dstSize >=
// FSE_BLOCKBOUND(srcSize) and a stream that fits in it. It reads ct's symbol
// table at every src byte unchecked: each must be at most ct's maxSymbol.
//
//go:noescape
func compressUsingCTableAsm(dst *byte, dstSize uint, src *byte, srcSize uint, ct *uint32, fast uint) uint64

// CompressWeightsUsingCTable is CompressUsingCTable for a src whose bytes
// are all at most ct's maxSymbol, as huf's weights are (compressWeights
// builds ct from their count): the assembly encoder relies on it.
func CompressWeightsUsingCTable(dst, src []byte, ct CTable) (int, error) {
	tableLog, maxSymbol := uint(ct[0]&0xFFFF), uint(ct[0]>>16)
	if !useFSECompressAsm || len(dst) == 0 || len(src) == 0 || tableLog-1 > MaxTableLog-1 ||
		len(ct) < CTableSizeU32(tableLog, maxSymbol) {
		return CompressUsingCTable(dst, src, ct)
	}
	// C's fast flush (no clamp) when dst holds FSE_BLOCKBOUND, as
	// CompressUsingCTable, but only up to tableLog 7: a symbol then costs at
	// most 8 bits, so any src fits the bound. (huf's weights use 6.) The
	// clamped flush writes the same bytes whenever the stream fits.
	var fast uint
	if len(dst) >= len(src)+len(src)>>7+4+8 && tableLog <= 7 {
		fast = 1
	}
	return int(compressUsingCTableAsm(&dst[0], uint(len(dst)), &src[0], uint(len(src)), &ct[0], fast)), nil
}
