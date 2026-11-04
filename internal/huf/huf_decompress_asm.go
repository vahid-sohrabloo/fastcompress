//go:build amd64 && !appengine && !noasm && !purego && gc

// The Go side of the fast 4X loops' assembly.

package huf

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
