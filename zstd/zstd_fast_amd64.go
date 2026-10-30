//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useFastAsm selects the assembly main loop of the no-dictionary fast match
// finder (baseline amd64 instructions only).
var useFastAsm bool

// useAVX2 selects the 32-byte AVX2 compare in fastCount and in the row
// search's inline count (row_lazy_amd64.s), taken once the first 8 bytes
// match. The scalar 8-byte loop runs otherwise.
var useAVX2 bool

func init() {
	cpu.RegisterAsm("fast", &useFastAsm, func() bool { return true })
	cpu.RegisterAsm("avx2count", &useAVX2, func() bool { return cpu.X86.HasAVX2 })
}

// compressBlockFastAsm runs the search loop of
// compressBlockFastNoDictGeneric from ip0 (C: from _start to _cleanup),
// appending sequences at seqs and literals at lits. It returns the final
// anchor, the number of sequences and literal bytes written, the repcodes,
// and the long-length marker (llType 0 when none; llPos counts from seqs).
// nSeq is -1 if a hash table entry pointed at or after the position being
// searched (impossible: the table only holds earlier positions).
//
// Preconditions (checked by compressBlockFastNoDict): prefixStart <=
// istart <= ip0, ip0 > prefixStart and iend <= len(b); the hash table has
// 1<<hlog entries (hShift = 64 - hlog); lits has room for iend - istart
// bytes and seqs for (iend - istart) / 4 sequences (every match is at least
// 4 bytes); rep1 and rep2 do not reach before prefixStart from ip0 (the Go
// caller's maxRep check).
//
//go:noescape
func compressBlockFastAsm(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

// compressBlockFastAsmStale is compressBlockFastAsm without a branch on the
// candidate's index: faster while the table holds many entries from before
// the window. Same arguments, results and output.
//
//go:noescape
func compressBlockFastAsmStale(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

// Register-convention helpers of compressBlockFastAsm (never called from Go).
func fastCopyLits()
func fastCount()
