//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

func hcAsmSetup(ms *matchState, iend int) {}

func lazyHcRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int, mls uint32) (int, int, uint32, uint32, bool) {
	return ip, anchor, offset1, offset2, false
}
