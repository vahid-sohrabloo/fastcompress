//go:build amd64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useHufAsm selects the assembly Huffman stream writer (baseline amd64);
// useHufBMI2 its BMI2 variant.
var useHufAsm, useHufBMI2 bool

func init() {
	cpu.RegisterAsm("huf", &useHufAsm, func() bool { return true })
	cpu.RegisterAsm("huf-bmi2", &useHufBMI2, func() bool { return cpu.X86.HasBMI2 })
}

// compress1XAsm encodes src backwards with the 256 codes at ct (CElt: code
// in the top bits, length in the low byte) into dst, as
// compress1XBodyGeneric, and returns the stream size or 0 if it did not
// fit. Its flushes differ from C's unrolled loops, which cannot change the
// bytes: the stream is the codes' bits in order, and whether it fits only
// depends on their total (HUF_flushBits clamps the write offset at
// len(dst)-8 and HUF_closeCStream fails once it is reached).
//
// Preconditions (checked by compress1XBody): len(dst) > 8; every code
// length is at most 12 (HUF_TABLELOG_MAX), so 4 codes fit after a flush.
//
//go:noescape
func compress1XAsm(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int

// compress1XAsmBMI2 is compress1XAsm using BMI2 (SHRX), with the same
// preconditions.
//
//go:noescape
func compress1XAsmBMI2(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int

// compress1XAsmVariant runs compress1XAsmBMI2 when it is on, else
// compress1XAsm.
func compress1XAsmVariant(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int {
	if useHufBMI2 {
		return compress1XAsmBMI2(dst, dstLen, src, srcLen, ct)
	}
	return compress1XAsm(dst, dstLen, src, srcLen, ct)
}
