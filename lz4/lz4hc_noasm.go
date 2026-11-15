//go:build !amd64 || appengine || noasm || purego || !gc

package lz4

const useHCAsm = false

func hcChainAsm(a *hcChainArgs) int { panic("lz4: no assembly chain search") }

const useMidAsm = false

func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int) (nip, anchor, op, status int) {
	panic("lz4: no assembly LZ4MID")
}
