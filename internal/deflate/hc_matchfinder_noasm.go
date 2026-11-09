//go:build !amd64 || appengine || noasm || purego || !gc

package deflate

// useGreedyLoopAsm is false: no assembly greedy loop in this build.
const useGreedyLoopAsm = false

// useLazyLoopAsm is false: no assembly lazy loop in this build.
const useLazyLoopAsm = false

// hcLongestMatchAsmForTest is the Go method (the assembly equivalence tests
// call it; in this build they compare the Go method with itself).
func hcLongestMatchAsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, maxLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32) {
	return mf.longestMatch(in, inBase, inNext, bestLen, maxLen, niceLen, maxSearchDepth, nextHashes)
}

// hcLongestMatch258AsmForTest is the Go method with maxLen 258.
func hcLongestMatch258AsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
	bestLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32) {
	return mf.longestMatch(in, inBase, inNext, bestLen, 258, niceLen, maxSearchDepth, nextHashes)
}

// greedyLoopAsm is never called (useGreedyLoopAsm is false).
func greedyLoopAsm(*greedyState, *deflateFreqs, *blockSplitStats, []byte, *greedyLoopState) {}

// lazyLoopAsm is never called (useLazyLoopAsm is false).
func lazyLoopAsm(*greedyState, *deflateFreqs, *blockSplitStats, []byte, *lazyLoopState) {}
