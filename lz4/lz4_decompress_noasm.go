//go:build !amd64 || appengine || noasm || purego || !gc

package lz4

const useDecAsm = false

func decodeFastAsm(src, out []byte, ip, op, oend, lowPrefix int) (nip, nop, offset, ml, status int) {
	panic("lz4: no assembly decoder")
}

func decodeFastPrefix64kAsm(src, out []byte, ip, op, oend, lowPrefix int) (nip, nop, offset, ml, status int) {
	panic("lz4: no assembly decoder")
}
