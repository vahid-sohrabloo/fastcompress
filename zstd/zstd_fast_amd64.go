//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useFastAsm selects the assembly main loop of the no-dictionary fast match
// finder (baseline amd64 instructions only).
var useFastAsm bool

// useAVX2 selects the 32-byte AVX2 compare in fastCount and in the row
// search's inline count (zstd_lazy_row_lazy_amd64.s), taken once the first 8 bytes
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

// compressBlockFastExtDictAsm runs the search loop of
// compressBlockFastExtDictGeneric from ip0 (C: zstd_fast.c:
// ZSTD_compressBlock_fast_extDict_generic, _start to _cleanup) over the
// prefix b and the extDict d, with the results of compressBlockFastAsm.
// nSeq is -1 if a hash table entry pointed at or after the position being
// searched, or into the extDict's last 3 bytes (both impossible; the Go
// code would read outside d for the latter). The preconditions are the
// BOUNDS in zstd_fast_extdict_amd64.s, checked by
// compressBlockFastExtDictMls.
//
//go:noescape
func compressBlockFastExtDictAsm(hashTable *uint32, b, d *byte, seqs *seqDef, lits *byte,
	ip0, istart, iend, prefixStart, dictStart, dictEnd int,
	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

// Register-convention helpers of compressBlockFastAsm (never called from Go).
func fastCopyLits()
func fastCount()

// compressBlockFastExtDictMls is compressBlockFastExtDictGeneric with the
// main loop in assembly (compressBlockFastExtDictAsm); the setup and the
// repcode bookkeeping around it are the same Go code. Streams run it once
// their input buffer has wrapped.
func compressBlockFastExtDictMls(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if !useFastAsm {
		return compressBlockFastExtDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	cParams := &ms.cParams
	hlog := cParams.hashLog
	stepSize := int(cParams.targetLength) + 1
	if cParams.targetLength == 0 {
		stepSize++
	}
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	endIndex := srcIdx + uint32(len(src))
	dictStartIndex := getLowestMatchIndex(ms, endIndex, cParams.windowLog)
	prefixStartIndex := max(w.dictLimit, dictStartIndex)
	istart := int(srcIdx - baseIdx)
	iend := istart + len(src)
	prefixStart := int(prefixStartIndex - baseIdx)
	dictStart := int(dictStartIndex - dictBaseIdx)
	dictEnd := int(prefixStartIndex - dictBaseIdx)
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	// The Generic handles the noDict switch (prefixStartIndex ==
	// dictStartIndex) and anything the assembly's BOUNDS do not cover.
	if prefixStartIndex == dictStartIndex || len(ms.hashTable) < 1<<hlog || iend > len(b) ||
		prefixStartIndex < baseIdx || prefixStart > istart || dictStartIndex < dictBaseIdx || prefixStartIndex < dictBaseIdx ||
		dictStart > dictEnd || dictEnd > len(d) || dictEnd < 4 || len(d) == 0 ||
		cap(ss.literals)-nLits < len(src)+16 || cap(ss.sequences)-nSeqs < len(src)/4 {
		return compressBlockFastExtDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	if extDictHook != nil {
		extDictHook("fast")
	}
	hShift, prime := fastHashParams(mls)

	offset1, offset2 := rep[0], rep[1]
	var offsetSaved1, offsetSaved2 uint32
	{
		maxRep := uint32(istart) + baseIdx - dictStartIndex
		if offset2 >= maxRep {
			offsetSaved2, offset2 = offset2, 0
		}
		if offset1 >= maxRep {
			offsetSaved1, offset1 = offset1, 0
		}
	}

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
	anchor, nSeq, nLit, r1, r2, llType, llPos := compressBlockFastExtDictAsm(&ms.hashTable[0], &b[0], &d[0], seqPtr, litPtr,
		istart, istart, iend, prefixStart, dictStart, dictEnd,
		prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, offset1, offset2,
		stepSize, uint64(hShift), uint64(64-hlog), prime)
	if nSeq < 0 {
		panic("zstd: fast hash table holds an index at or after the current position, or at the extDict's last 3 bytes")
	}
	ms.asmLoopRuns++
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	offset1, offset2 = r1, r2

	// C: _cleanup — see compressBlockFastExtDictGeneric.
	if offsetSaved1 != 0 && offset1 != 0 {
		offsetSaved2 = offsetSaved1
	}
	rep[0] = offset1
	if offset1 == 0 {
		rep[0] = offsetSaved1
	}
	rep[1] = offset2
	if offset2 == 0 {
		rep[1] = offsetSaved2
	}
	return iend - anchor
}
