//go:build amd64 && !appengine && !noasm && !purego && gc

package lz4

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useHCAsm selects the assembly candidate loop of the hash chain search
// (levels 3 to 12; baseline amd64 instructions only).
var useHCAsm bool

func init() {
	cpu.RegisterAsm("hcChain", &useHCAsm, func() bool { return true })
}

//go:noescape
func hcChainAsm(a *hcChainArgs) int

// useMidAsm selects the assembly main loop of LZ4MID (level 2) for blocks
// without attached dictionary.
var useMidAsm bool

func init() {
	cpu.RegisterAsm("midCompress", &useMidAsm, func() bool { return true })
}

//go:noescape
func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int)

//go:noescape
func midCompressDictAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int)

// useHCParseAsm selects the assembly parsers of the hash chain levels (3
// to 9) and the optimal levels (10 to 12) for a prefix with or without an
// external dictionary (no attached one).
var useHCParseAsm bool

func init() {
	cpu.RegisterAsm("hcParse", &useHCParseAsm, func() bool { return true })
}

//go:noescape
func hcHashChainAsm(a *hcParseArgs) int

//go:noescape
func hcOptimalAsm(a *hcParseArgs) int

//go:noescape
func hcOptimalFavAsm(a *hcParseArgs) int
