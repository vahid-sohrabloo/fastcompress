//go:build !(amd64 || arm64) || appengine || noasm || purego || !gc

package lz4

const useHCAsm = false

func hcChainAsm(a *hcChainArgs) int { panic("lz4: no assembly chain search") }

const useMidAsm = false

const midDictAsm = false

func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int) {
	panic("lz4: no assembly LZ4MID")
}

const useHCParseAsm = false

const hcParseFull = false

func hcHashChainAsm(a *hcParseArgs) int { panic("lz4: no assembly hash chain parser") }

func hcOptimalAsm(a *hcParseArgs) int { panic("lz4: no assembly optimal parser") }

func midCompressDictAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int) {
	panic("lz4: no assembly LZ4MID")
}

func hcOptimalFavAsm(a *hcParseArgs) int { panic("lz4: no assembly optimal parser") }
