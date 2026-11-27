//go:build amd64 && !appengine && !noasm && !purego && gc

package fse

import (
	"github.com/vahid-sohrabloo/fastcompress/internal/bitstream"
	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
)

// useFSEDecompressAsm selects the assembly decoding loops of
// decompressUsingDTable, which need BMI2 (SHRX, BZHI).
var useFSEDecompressAsm bool

func init() {
	cpu.RegisterAsm("fse-decompress", &useFSEDecompressAsm, func() bool { return cpu.X86.HasBMI2 })
}

// decompressAsm is decompressUsingDTable's two loops (C:
// fse_decompress.c:FSE_decompress_usingDTable_generic, which has no
// assembly). ptr, container and consumed are the bitstream's state after the
// first states were read. It returns the number of bytes written to dst, or
// -1 for ErrDstSizeTooSmall. Preconditions (checked by decompressFast, ptr
// by DStream.Init): dstLen >= 1 bytes at dst, mask+1 cells at table, mask+1
// a power of two, src at least 8 bytes long and 0 <= ptr <= len(src)-8.
//
//go:noescape
func decompressAsm(dst *byte, dstLen int, table *decodeEntry, mask uint, src *byte, ptr int, container uint64, consumed, state1, state2 uint) int

// decompressFast runs the assembly loops from the first states, and reports
// whether it did. It does nothing when the assembly is off or the arguments
// break its preconditions: the Go loops then decode the stream.
func decompressFast(dst []byte, bitD *bitstream.DStream, state1, state2 uint64, table []decodeEntry) (int, bool, error) {
	src, ptr, container, consumed := bitD.Raw()
	if !useFSEDecompressAsm || len(src) < 8 || len(dst) == 0 || len(table) == 0 || len(table)&(len(table)-1) != 0 {
		return 0, false, nil
	}
	n := decompressAsm(&dst[0], len(dst), &table[0], uint(len(table)-1), &src[0], ptr, container,
		uint(consumed), uint(state1), uint(state2))
	if n < 0 {
		return 0, true, ErrDstSizeTooSmall
	}
	return n, true, nil
}
