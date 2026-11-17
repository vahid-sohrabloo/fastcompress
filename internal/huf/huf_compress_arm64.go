//go:build arm64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useHufAsm selects the assembly Huffman stream writer (baseline arm64).
var useHufAsm bool

func init() {
	cpu.RegisterAsm("huf", &useHufAsm, func() bool { return true })
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

// compress1XAsmX5 is compress1XAsm with 5 codes per flush (C: kUnroll 5),
// for tableLog <= 11, with the same preconditions.
//
//go:noescape
func compress1XAsmX5(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int

// compress1XAsmX10 is compress1XAsmX5 with 10 codes per loop iteration and
// unclamped flushes, for tableLog <= 11 and dstLen >= srcLen*tableLog/8 +
// 8 (C: HUF_tightCompressBound, below which C clamps).
//
//go:noescape
func compress1XAsmX10(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int

// compress1XAsmVariant runs compress1XAsmX10, compress1XAsmX5 or
// compress1XAsm, by tableLog and dstLen.
func compress1XAsmVariant(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt, tableLog uint32) int {
	if tableLog <= 11 {
		if dstLen >= srcLen*int(tableLog)>>3+8 {
			return compress1XAsmX10(dst, dstLen, src, srcLen, ct)
		}
		return compress1XAsmX5(dst, dstLen, src, srcLen, ct)
	}
	return compress1XAsm(dst, dstLen, src, srcLen, ct)
}
