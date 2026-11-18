//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

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

// compressBlockDoubleFastExtDictAsm runs the loop of
// compressBlockDoubleFastExtDictGeneric (C: zstd_double_fast.c:
// ZSTD_compressBlock_doubleFast_extDict_generic) over the prefix b and the
// extDict d, with the results of compressBlockDoubleFastAsm. nSeq is -1 if
// a table entry pointed at or after the position being searched, or into
// the extDict's last 7 bytes (both impossible; the Go code would read
// outside d for the latter). The preconditions are the BOUNDS in
// zstd_double_fast_extdict_amd64.s, checked by
// compressBlockDoubleFastExtDictMls.
//
//go:noescape
func compressBlockDoubleFastExtDictAsm(hashLong, hashSmall *uint32, b, d *byte, seqs *seqDef, lits *byte,
	istart, iend, prefixStart, dictStart, dictEnd int,
	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

// compressBlockDoubleFastExtDictMls is compressBlockDoubleFastExtDictGeneric
// with the loop in assembly (compressBlockDoubleFastExtDictAsm). Streams run
// it once their input buffer has wrapped.
func compressBlockDoubleFastExtDictMls(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if !useDoubleFastAsm {
		return compressBlockDoubleFastExtDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	cParams := &ms.cParams
	hBitsL := cParams.hashLog
	hBitsS := cParams.chainLog
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	istart := int(srcIdx - baseIdx)
	iend := istart + len(src)
	endIndex := srcIdx + uint32(len(src))
	dictStartIndex := getLowestMatchIndex(ms, endIndex, cParams.windowLog)
	prefixStartIndex := max(w.dictLimit, dictStartIndex)
	prefixStart := int(prefixStartIndex - baseIdx)
	dictStart := int(dictStartIndex - dictBaseIdx)
	dictEnd := int(prefixStartIndex - dictBaseIdx)
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	// The Generic handles the noDict switch (prefixStartIndex ==
	// dictStartIndex) and anything the assembly's BOUNDS do not cover.
	if prefixStartIndex == dictStartIndex || len(ms.hashTable) < 1<<hBitsL || len(ms.chainTable) < 1<<hBitsS ||
		iend > len(b) || prefixStartIndex < baseIdx || prefixStart > istart ||
		dictStartIndex < dictBaseIdx || prefixStartIndex < dictBaseIdx || dictStart > dictEnd || dictEnd > len(d) || dictEnd < 8 ||
		cap(ss.literals)-nLits < len(src)+16 || cap(ss.sequences)-nSeqs < len(src)/4 {
		return compressBlockDoubleFastExtDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	if extDictHook != nil {
		extDictHook("dfast")
	}
	shiftS, primeS := fastHashParams(mls)

	seqs := ss.sequences[nSeqs:cap(ss.sequences)]
	lits := ss.literals[nLits:cap(ss.literals)]
	var seqPtr *seqDef
	if len(seqs) > 0 {
		seqPtr = &seqs[0]
	}
	var litPtr *byte
	if len(lits) > 0 {
		litPtr = &lits[0]
	}
	anchor, nSeq, nLit, r1, r2, llType, llPos := compressBlockDoubleFastExtDictAsm(&ms.hashTable[0], &ms.chainTable[0], &b[0], &d[0],
		seqPtr, litPtr, istart, iend, prefixStart, dictStart, dictEnd,
		prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep[0], rep[1],
		uint64(64-hBitsL), uint64(shiftS), uint64(64-hBitsS), primeS)
	if nSeq < 0 {
		panic("zstd: double-fast hash table holds an index at or after the current position, or at the extDict's last 7 bytes")
	}
	ms.asmLoopRuns++
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	// save reps for next block
	rep[0], rep[1] = r1, r2
	return iend - anchor
}
