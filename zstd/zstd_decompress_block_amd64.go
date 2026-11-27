//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useSeqDecAsm selects the assembly sequence fast loop, which needs BMI2
// (SHLX, SHRX, BZHI). Without it decompressSequencesBody's Go loop runs
// every sequence. C has no assembly here: the loop is
// ZSTD_decompressSequences_body's, with ZSTD_decodeSequence and
// ZSTD_execSequence's fast path inlined.
var useSeqDecAsm bool

func init() {
	cpu.RegisterAsm("seq.decompressSequencesFast", &useSeqDecAsm, decAsmAvailable)
}

// decAsmAvailable reports whether the CPU runs the decoder's sequence
// assembly (the fast loop and the table builder).
func decAsmAvailable() bool { return cpu.X86.HasBMI2 }

// seqAsmExtDict reports whether the sequence fast loop executes a match
// that starts in the extDict itself: decodeExecSeqsAsm and decodeExecSeqsSplitAsm execute it.
const seqAsmExtDict = true

// decodeExecSeqsAsm is seqFast.runGo (zstd_decompress_block_asm_test.go)
// in assembly.
// C: zstd_decompress_block.c:ZSTD_decompressSequences_body (the loop)
//
//go:noescape
func decodeExecSeqsAsm(a *seqAsmArgs)

// decodeExecSeqsSplitAsm is seqFast.runGo with split in assembly:
// decodeExecSeqsAsm for the first loop of
// decompressSequencesBodySplitLitBuffer (lits = out).
// C: zstd_decompress_block.c:ZSTD_decompressSequences_bodySplitLitBuffer
// (the first loop)
//
//go:noescape
func decodeExecSeqsSplitAsm(a *seqAsmArgs)

// decodeExecSeqsTailAsm is decodeExecSeqsAsm without its ptr >= 16
// condition: it takes the sequences up to the bitstream's start.
//
//go:noescape
func decodeExecSeqsTailAsm(a *seqAsmArgs)

// useFSETableAsm selects the assembly sequence-table builder, which needs
// BMI2 (SHLX). Without it buildFSETable's Go body builds every table. C has
// no assembly here either.
var useFSETableAsm bool

func init() {
	cpu.RegisterAsm("buildFSETable", &useFSETableAsm, decAsmAvailable)
}

// buildFSETableAsm is buildFSETable in assembly. It returns false, leaving
// the table to the Go body, when norm is not a valid distribution (see
// zstd_decompress_block_amd64.s). Preconditions: buildFSETableFast's.
// C: zstd_decompress_block.c:ZSTD_buildFSETable_body
//
//go:noescape
func buildFSETableAsm(dt *seqSymbol, norm *int16, maxSV1 uint, baseValue *uint32, nbAdd *uint8, tableLog uint, wksp *buildFSETableWksp) bool
