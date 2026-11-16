//go:build !(amd64 || arm64) || appengine || noasm || purego || !gc

package deflate

// useFastestLoopAsm is false: there is no assembly level 1 loop in this
// build, so deflateCompressFastest's call of fastestLoopAsm is removed by the
// compiler.
const useFastestLoopAsm = false

// fastestLoopAsm is never called (useFastestLoopAsm is false).
func fastestLoopAsm(*fastestState, *deflateFreqs, []byte, *fastestLoopState) {}
