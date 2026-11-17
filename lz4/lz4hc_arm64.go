//go:build arm64 && !appengine && !noasm && !purego && gc

package lz4

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useHCAsm selects the assembly candidate loop of the hash chain search
// (levels 3 to 12; baseline arm64 instructions only).
var useHCAsm bool

func init() {
	cpu.RegisterAsm("hcChain", &useHCAsm, func() bool { return true })
}

//go:noescape
func hcChainAsm(a *hcChainArgs) int

// useMidAsm selects the assembly main loop of LZ4MID (level 2) for blocks
// without dictionary (external or attached; midDictAsm is false: those run
// the Go loop).
var useMidAsm bool

func init() {
	cpu.RegisterAsm("midCompress", &useMidAsm, func() bool { return true })
}

const midDictAsm = false

//go:noescape
func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int)

func midCompressDictAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int) {
	panic("lz4: no assembly LZ4MID")
}

// useHCParseAsm selects the assembly parsers of the hash chain levels (3
// to 9) and the optimal levels (10 to 12, without favorDecSpeed) for a
// prefix without dictionary (hcParseFull is false: the other modes run the
// Go parsers, with hcChainAsm).
var useHCParseAsm bool

func init() {
	cpu.RegisterAsm("hcParse", &useHCParseAsm, func() bool { return true })
}

const hcParseFull = false

//go:noescape
func hcHashChainAsm(a *hcParseArgs) int

//go:noescape
func hcOptimalAsm(a *hcParseArgs) int

func hcOptimalFavAsm(a *hcParseArgs) int { panic("lz4: no assembly optimal parser with favorDecSpeed") }
