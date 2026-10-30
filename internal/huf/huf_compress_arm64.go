//go:build arm64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

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

func compress1XBody(dst, src []byte, ct CTable) int {
	if !useHufAsm || len(dst) <= 8 || ReadCTableHeader(ct).TableLog > TableLogMax {
		return compress1XBodyGeneric(dst, src, ct)
	}
	var srcPtr *byte
	if len(src) > 0 {
		srcPtr = &src[0]
	}
	elts := (*[SymbolValueMax + 1]CElt)(ct[1:])
	return compress1XAsm(&dst[0], len(dst), srcPtr, len(src), &elts[0])
}
