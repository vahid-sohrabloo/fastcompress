//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

func compressBlockDoubleFastNoDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	return compressBlockDoubleFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
}
