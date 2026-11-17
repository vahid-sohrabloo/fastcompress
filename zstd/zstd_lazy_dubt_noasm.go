//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

// btAsmMls3: no assembly here (see zstd_lazy_dubt_amd64.go).
const btAsmMls3 = false

func insertDUBT1(ms *matchState, curr uint32, inputEnd int, nbCompares, btLow uint32) {
	insertDUBT1Generic(ms, curr, inputEnd, nbCompares, btLow)
}

func dubtSearch(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, uint32) {
	return dubtSearchGeneric(ms, ip, iend, curr, matchIndex, nbCompares, btLow, windowLow)
}

func insertBt1Loop(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, bool) {
	return 0, 0, false
}

func btAsmSetup(ms *matchState, matches *[optSize]optMatch, mls uint32, iend int) {}

func btGetAllNoDict(matches *[optSize]optMatch, ms *matchState, ip, iLimit int, rep *[repNum]uint32, ll0 bool, lengthToBeat uint32) (uint32, bool) {
	return 0, false
}

func btGetAllMatchesFast(matches *[optSize]optMatch, ms *matchState, ip, iLimit int, rep *[repNum]uint32, ll0 bool, lengthToBeat uint32) (uint32, bool) {
	return 0, false
}

func lazyBtRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int) (int, int, uint32, uint32, bool) {
	return ip, anchor, offset1, offset2, false
}

func dubtFindBestMatchFast(ms *matchState, ip, iend int) (int, uint32, bool) {
	return 0, 0, false
}
