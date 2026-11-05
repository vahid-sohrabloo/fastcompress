//go:build arm64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useSeqDecAsm selects the assembly sequence fast loop (baseline arm64
// instructions only). C has no assembly here: the loop is
// ZSTD_decompressSequences_body's, with ZSTD_decodeSequence and
// ZSTD_execSequence's fast path inlined.
var useSeqDecAsm bool

func init() {
	cpu.RegisterAsm("seq.decompressSequencesFast", &useSeqDecAsm, decAsmAvailable)
}

// decAsmAvailable reports whether the CPU runs the decoder's sequence
// assembly.
func decAsmAvailable() bool { return true }

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

// useFSETableAsm stays false: buildFSETable has no arm64 assembly yet, so
// buildFSETableFast never calls buildFSETableAsm.
var useFSETableAsm bool

func buildFSETableAsm(_ *seqSymbol, _ *int16, _ uint, _ *uint32, _ *uint8, _ uint, _ *buildFSETableWksp) bool {
	return false
}
