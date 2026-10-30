//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useDoubleFastAsm selects the assembly main loop of the no-dictionary
// double-fast match finder (baseline amd64 instructions only).
var useDoubleFastAsm bool

func init() {
	cpu.RegisterAsm("doublefast", &useDoubleFastAsm, func() bool { return true })
}

// compressBlockDoubleFastAsm runs the outer loop of
// compressBlockDoubleFastNoDictGeneric from ip until _cleanup, appending
// sequences at seqs and literals at lits. Results as for
// compressBlockFastAsm. The short hash is ((le64 << shiftS) * primeS) >>
// hShiftS, the long one (le64 * prime8bytes) >> hShiftL.
//
// Preconditions (checked by compressBlockDoubleFastNoDict):
// prefixLowest <= istart <= ip, ip > prefixLowest, iend <= len(b); the long
// and short tables have 1<<(64-hShiftL) and 1<<(64-hShiftS) entries; lits has
// room for iend - istart bytes and seqs for (iend - istart) / 4 sequences;
// rep1 and rep2 do not reach before prefixLowest from ip (the Go caller's
// maxRep check). A table entry at or after the position being searched
// (impossible) returns nSeq = -1 instead of being read.
//
//go:noescape
func compressBlockDoubleFastAsm(hashLong, hashSmall *uint32, b *byte, seqs *seqDef, lits *byte,
	ip, istart, iend, prefixLowest int, prefixLowestIndex, baseIdx, rep1, rep2 uint32,
	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

// compressBlockDoubleFastAsmStale is compressBlockDoubleFastAsm without a
// branch on the candidates' indices: faster while the tables hold many
// entries from before the window. Same arguments, results and output.
//
//go:noescape
func compressBlockDoubleFastAsmStale(hashLong, hashSmall *uint32, b *byte, seqs *seqDef, lits *byte,
	ip, istart, iend, prefixLowest int, prefixLowestIndex, baseIdx, rep1, rep2 uint32,
	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
