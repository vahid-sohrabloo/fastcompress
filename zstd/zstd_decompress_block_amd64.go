//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useSeqDecAsm selects the assembly sequence fast loop, which needs BMI2
// (SHLX, SHRX, BZHI). Without it decompressSequencesBody's Go loop runs
// every sequence. C has no assembly here: the loop is
// ZSTD_decompressSequences_body's, with ZSTD_decodeSequence and
// ZSTD_execSequence's fast path inlined.
var useSeqDecAsm bool

func init() {
	cpu.RegisterAsm("seq.decompressSequencesFast", &useSeqDecAsm, func() bool { return cpu.X86.HasBMI2 })
}

// seqAsmArgs is the state decodeExecSeqsAsm reads and advances
// (zstd_decompress_block_amd64.s reads it at fixed offsets, checked by
// TestSeqAsmArgsLayout). ptr, op, oendW, litPtr, litEnd and prefixStart are
// offsets into src, out and lits, not pointers: the loop adds the bases on
// entry and subtracts them before returning, so the Go side never handles a
// pointer past the end of a buffer.
type seqAsmArgs struct {
	bitContainer uint64      // 0
	bitsConsumed uint64      // 8
	ptr          int         // 16: offset into src
	src          *byte       // 24
	stateLL      int         // 32
	stateML      int         // 40
	stateOf      int         // 48
	tableLL      *seqSymbol  // 56: C's dt + 1
	tableML      *seqSymbol  // 64
	tableOf      *seqSymbol  // 72
	rep          [repNum]int // 80: prevOffset
	out          *byte       // 104
	op           int         // 112
	oendW        int         // 120: oend - WILDCOPY_OVERLENGTH
	lits         *byte       // 128
	litPtr       int         // 136
	litEnd       int         // 144
	prefixStart  int         // 152
	nbSeq        int         // 160
	litLength    int         // 168: the pending sequence
	matchLength  int         // 176
	offset       int         // 184
	pending      int         // 192: 1 when the loop stopped on a sequence it decoded but did not execute
}

// decodeExecSeqsAsm is seqFast.runGo (zstd_decompress_block_amd64_test.go)
// in assembly.
// C: zstd_decompress_block.c:ZSTD_decompressSequences_body (the loop)
//
//go:noescape
func decodeExecSeqsAsm(a *seqAsmArgs)

// useFSETableAsm selects the assembly sequence-table builder, which needs
// BMI2 (SHLX). Without it buildFSETable's Go body builds every table. C has
// no assembly here either.
var useFSETableAsm bool

func init() {
	cpu.RegisterAsm("buildFSETable", &useFSETableAsm, func() bool { return cpu.X86.HasBMI2 })
}

// buildFSETableAsm is buildFSETable in assembly. It returns false, leaving
// the table to the Go body, when norm is not a valid distribution (see
// zstd_decompress_block_amd64.s). Preconditions: buildFSETableFast's.
// C: zstd_decompress_block.c:ZSTD_buildFSETable_body
//
//go:noescape
func buildFSETableAsm(dt *seqSymbol, norm *int16, maxSV1 uint, baseValue *uint32, nbAdd *uint8, tableLog uint, wksp *buildFSETableWksp) bool
