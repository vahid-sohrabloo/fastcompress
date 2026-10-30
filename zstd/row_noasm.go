//go:build !amd64 || appengine || noasm || purego || !gc

package zstd

func rowUpdateCached(ms *matchState, idx, end, mls, rowLog uint32) {
	rowUpdateCachedGeneric(ms, idx, end, mls, rowLog)
}

func rowSearch(ms *matchState, ip, iLimit int, relRow, tag, rowLog, nbAttempts, lowLimit uint32) (int, uint32) {
	return rowSearchGeneric(ms, ip, iLimit, relRow, tag, rowLog, nbAttempts, lowLimit)
}

func rowAsmSetup(ms *matchState, mls, rowLog uint32, iend int) {}

func rowFindBestMatchAsmCall(ms *matchState, ip, iLimit int) (int, uint32) {
	panic("unreachable: rowAsm.ok is never set without assembly")
}

func lazyRowRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int, mls, rowLog uint32) (int, int, uint32, uint32, bool) {
	return ip, anchor, offset1, offset2, false
}
