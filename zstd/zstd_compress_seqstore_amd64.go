//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useSeqCodesAsm selects the assembly seqToCodes loop (baseline amd64).
var useSeqCodesAsm bool

func init() {
	cpu.RegisterAsm("seqcodes", &useSeqCodesAsm, func() bool { return true })
}

// seqCodesAsm is seqCodesGeneric (see it) over the n > 0 sequences at
// seqs, writing n codes to each of llCodes, ofCodes and mlCodes. Every
// offBase is at least 1 (an offset + repNum or a repcode), so its BSR is
// defined.
//
//go:noescape
func seqCodesAsm(seqs *seqDef, n int, llCodes, ofCodes, mlCodes *byte)
