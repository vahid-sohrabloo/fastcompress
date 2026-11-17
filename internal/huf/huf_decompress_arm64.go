//go:build arm64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useHuf4X1Asm and useHuf4X2Asm select the assembly fast loops (baseline
// arm64 instructions only).
var useHuf4X1Asm, useHuf4X2Asm bool

func init() {
	cpu.RegisterAsm("huf.decompress4X1Fast", &useHuf4X1Asm, hufFastAsmAvailable)
	cpu.RegisterAsm("huf.decompress4X2Fast", &useHuf4X2Asm, hufFastAsmAvailable)
}

// hufFastAsmAvailable reports whether the CPU runs the fast-loop assembly.
func hufFastAsmAvailable() bool { return true }

// decompress4X1FastAsmLoop is decompress4X1FastCLoop.
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast_c_loop
//
//go:noescape
func decompress4X1FastAsmLoop(args *fastArgs)

// decompress4X2FastAsmLoop is decompress4X2FastCLoop.
// C: huf_decompress.c:HUF_decompress4X2_usingDTable_internal_fast_c_loop
//
//go:noescape
func decompress4X2FastAsmLoop(args *fastArgs)
