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
// without external or attached dictionary.
var useMidAsm bool

func init() {
	cpu.RegisterAsm("midCompress", &useMidAsm, func() bool { return true })
}

//go:noescape
func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int) (nip, anchor, op, status int)

// useHCParseAsm selects the assembly parsers of the hash chain levels (3
// to 9) and the optimal levels (10 to 12) for a prefix without external or
// attached dictionary.
var useHCParseAsm bool

func init() {
	cpu.RegisterAsm("hcParse", &useHCParseAsm, func() bool { return true })
}

//go:noescape
func hcHashChainAsm(a *hcParseArgs) int

//go:noescape
func hcOptimalAsm(a *hcParseArgs) int
