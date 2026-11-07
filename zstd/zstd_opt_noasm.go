//go:build !amd64 || appengine || noasm || purego || !gc

package zstd

// optParseFast: the assembly is amd64 only.
func optParseFast(*matchState, *seqStore, *[repNum]uint32, int, int, int, uint32, uint32) (int, bool) {
	return 0, false
}
