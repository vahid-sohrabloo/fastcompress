//go:build amd64 && !appengine && !noasm && !purego && gc

package lz4

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useDecAsm selects the assembly fast loop of the block decoder (baseline
// amd64 instructions only).
var useDecAsm bool

func init() {
	cpu.RegisterAsm("decodeFast", &useDecAsm, func() bool { return true })
}

// decodeFastAsm runs decompressGeneric's fast loop for a block without
// external dictionary (lz4_decompress_amd64.s has the statuses and the
// bounds proof).
//
//go:noescape
func decodeFastAsm(src, out []byte, ip, op, oend, lowPrefix int) (nip, nop, offset, ml, status int)
