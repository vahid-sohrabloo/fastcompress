//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

func compressBlockFastNoDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	return compressBlockFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
}
