//go:build !amd64 || appengine || noasm || purego || !gc

package zstd

func insertDUBT1(ms *matchState, curr uint32, inputEnd int, nbCompares, btLow uint32) {
	insertDUBT1Generic(ms, curr, inputEnd, nbCompares, btLow)
}

func dubtSearch(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, uint32) {
	return dubtSearchGeneric(ms, ip, iend, curr, matchIndex, nbCompares, btLow, windowLow)
}
