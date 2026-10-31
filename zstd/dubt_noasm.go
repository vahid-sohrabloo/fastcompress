//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

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

func dubtFindBestMatchFast(ms *matchState, ip, iend int) (int, uint32, bool) {
	return 0, 0, false
}
