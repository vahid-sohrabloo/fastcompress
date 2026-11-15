//go:build !amd64 || appengine || noasm || purego || !gc

package lz4

const useHCAsm = false

func hcChainAsm(a *hcChainArgs) int { panic("lz4: no assembly chain search") }

const useMidAsm = false

func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int) (nip, anchor, op, status int) {
	panic("lz4: no assembly LZ4MID")
}

const useHCParseAsm = false

func hcHashChainAsm(a *hcParseArgs) int { panic("lz4: no assembly hash chain parser") }

func hcOptimalAsm(a *hcParseArgs) int { panic("lz4: no assembly optimal parser") }
