//go:build !(amd64 || arm64) || appengine || noasm || purego || !gc

package deflate

// useMinCostPathAsm is false: no assembly minimum-cost path pass in this
// build.
const useMinCostPathAsm = false

// minCostPathAsm is never called (useMinCostPathAsm is false).
func minCostPathAsm(*nearOptimalState, *[deflateMaxMatchOffset + 1]uint8, int, int, int) int {
	return 0
}
