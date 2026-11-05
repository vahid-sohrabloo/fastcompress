//go:build amd64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useHuf4X1Asm and useHuf4X2Asm select the assembly fast loops, which need
// BMI2 (SHRX/SHLX). Without BMI2 the Go fast loops
// (HUF_decompress4X{1,2}_usingDTable_internal_fast_c_loop) run, as in a C
// build without DYNAMIC_BMI2. C's DYNAMIC_BMI2 dispatch differs: without
// HUF_flags_bmi2 it skips the fast loop and runs the fallback body. The
// output is identical either way.
var useHuf4X1Asm, useHuf4X2Asm bool

func init() {
	cpu.RegisterAsm("huf.decompress4X1Fast", &useHuf4X1Asm, hufFastAsmAvailable)
	cpu.RegisterAsm("huf.decompress4X2Fast", &useHuf4X2Asm, hufFastAsmAvailable)
}

// hufFastAsmAvailable reports whether the CPU runs the fast-loop assembly.
func hufFastAsmAvailable() bool { return cpu.X86.HasBMI2 }

// decompress4X1FastAsmLoop is decompress4X1FastCLoop.
// C: huf_decompress_amd64.S:HUF_decompress4X1_usingDTable_internal_fast_asm_loop
//
//go:noescape
func decompress4X1FastAsmLoop(args *fastArgs)

// decompress4X2FastAsmLoop is decompress4X2FastCLoop.
// C: huf_decompress_amd64.S:HUF_decompress4X2_usingDTable_internal_fast_asm_loop
//
//go:noescape
func decompress4X2FastAsmLoop(args *fastArgs)
