//go:build !(amd64 || arm64) || appengine || noasm || purego || !gc

package deflate

// useFastloopAsm is false: there is no assembly fast loop in this build, so
// the calls in decompressDefault and decompressStream are removed by the
// compiler.
const useFastloopAsm = false

// decompressFastloopAsm is never called (useFastloopAsm is false); it runs
// no iteration and returns the state unchanged.
func decompressFastloopAsm(_, _ []byte, _ *[litlenEnough]uint32, _ *[offsetEnough]uint32,
	_ uint32, _, _ int, bitbuf uint64, bitsleft uint32, inNext, outNext int) (uint64, uint32, int, int, int) {
	return bitbuf, bitsleft, inNext, outNext, fastloopContinue
}
