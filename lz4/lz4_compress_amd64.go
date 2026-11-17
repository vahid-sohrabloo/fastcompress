//go:build amd64 && !appengine && !noasm && !purego && gc

package lz4

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// fastExtAsm reports whether the extDict and dictCtx main loops
// (compressFastExtAsm, compressFastDictCtxAsm) exist in assembly.
const fastExtAsm = true

// useFastAsm selects the assembly main loop of the fast compressor for
// noDict blocks (baseline amd64 instructions only).
var useFastAsm bool

func init() {
	cpu.RegisterAsm("compressFast", &useFastAsm, func() bool { return true })
}

//go:noescape
func compressFast32Asm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast32NoDictAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast32FillAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast16FillAsm(src, dst []byte, table *uint16, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast16Asm(src, dst []byte, table *uint16, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFastExtAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx int, dict []byte, seqLimit int) (anchor, op, status int)

//go:noescape
func compressFastDictCtxAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx int, dict []byte, seqLimit int, dctx *uint32, dictDelta int) (anchor, op, status int)
