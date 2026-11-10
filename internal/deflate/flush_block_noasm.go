//go:build !amd64 || appengine || noasm || purego || !gc

package deflate

// useWriteSequencesAsm is false: there is no assembly sequence writer in
// this build, so flushBlock's call of writeSequencesAsm is removed by the
// compiler.
const useWriteSequencesAsm = false

// writeSequencesAsm is never called (useWriteSequencesAsm is false).
func writeSequencesAsm(*bitRegs, []byte, *deflateCodes, *deflateLengthCodes, []byte, *deflateSequence) {
}
