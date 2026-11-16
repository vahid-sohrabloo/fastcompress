//go:build !(amd64 || arm64) || appengine || noasm || purego || !gc

package deflate

// useMatchfinderSSE2 is false: there is no assembly matchfinder init or
// rebase in this build, so the calls in matchfinderInit and
// matchfinderRebase are removed by the compiler.
const useMatchfinderSSE2 = false

// matchfinderInitSSE2 is never called (useMatchfinderSSE2 is false).
func matchfinderInitSSE2([]mfPos) {}

// matchfinderRebaseSSE2 is never called (useMatchfinderSSE2 is false).
func matchfinderRebaseSSE2([]mfPos) {}
