//go:build !amd64 || appengine || noasm || purego || !gc

package deflate

// useBtMatchLoopAsm is false: no assembly matchfinding loop in this build.
const useBtMatchLoopAsm = false

// btMatchLoopAsm is never called (useBtMatchLoopAsm is false).
func btMatchLoopAsm(*nearOptimalState, *blockSplitStats, []byte, *btMatchLoopState) {}
