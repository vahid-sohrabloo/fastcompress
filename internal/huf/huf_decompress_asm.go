//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the fast 4X loops' assembly, shared by amd64 and arm64.

package huf

// fastArgs has the layout of C's HUF_DecompressFastArgs (checked by
// TestHUFFastArgsLayout), which huf_decompress_{amd64,arm64}.s read at fixed
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

// decompress4X1FastLoop runs the assembly fast loop when it is on and flags
// lacks FlagDisableAsm, else decompress4X1FastCLoop.
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal (loopFn)
func decompress4X1FastLoop(args *decompressFastArgs, dt *DTable, flags int) {
	if !useHuf4X1Asm || flags&FlagDisableAsm != 0 {
		decompress4X1FastCLoop(args)
		return
	}
	a := newFastArgs(args, dt)
	decompress4X1FastAsmLoop(&a)
	args.ip, args.op, args.bits = a.ip, a.op, a.bits
}

// decompress4X2FastLoop is decompress4X1FastLoop for X2 tables.
// C: huf_decompress.c:HUF_decompress4X2_usingDTable_internal (loopFn)
func decompress4X2FastLoop(args *decompressFastArgs, dt *DTable, flags int) {
	if !useHuf4X2Asm || flags&FlagDisableAsm != 0 {
		decompress4X2FastCLoop(args)
		return
	}
	a := newFastArgs(args, dt)
	decompress4X2FastAsmLoop(&a)
	args.ip, args.op, args.bits = a.ip, a.op, a.bits
}

// newFastArgs copies the loop state of args, initialized by
// decompressFastArgsInit (so len(args.src) >= 10 and op[3] < oend =
// len(args.dst)), into the assembly's argument block.
func newFastArgs(args *decompressFastArgs, dt *DTable) fastArgs {
	return fastArgs{
		ip:      args.ip,
		op:      args.op,
		bits:    args.bits,
		dt:      dt,
		ilowest: &args.src[0],
		oend:    args.oend,
		dst:     &args.dst[0],
	}
}
