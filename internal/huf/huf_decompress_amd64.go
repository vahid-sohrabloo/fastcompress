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
	cpu.RegisterAsm("huf.decompress4X1Fast", &useHuf4X1Asm, func() bool { return cpu.X86.HasBMI2 })
	cpu.RegisterAsm("huf.decompress4X2Fast", &useHuf4X2Asm, func() bool { return cpu.X86.HasBMI2 })
}

// fastArgs has the layout of C's HUF_DecompressFastArgs (checked by
// TestHUFFastArgsLayout), which huf_decompress_amd64.s reads at fixed
// offsets, plus dst. ip, op and oend hold offsets into src and dst, not
// pointers: the loops add ilowest (&src[0]) and dst on entry and subtract
// them again before returning, so the Go side never handles a pointer one
// past the end of dst. dt is the whole table; the loops add the offset of
// x1 or x2. iend is not read by the loops and stays zero.
type fastArgs struct {
	ip      [4]int    // 0
	op      [4]int    // 32
	bits    [4]uint64 // 64
	dt      *DTable   // 96
	ilowest *byte     // 104
	oend    int       // 112
	iend    [4]int    // 120
	dst     *byte     // 152 (not in C)
}

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
