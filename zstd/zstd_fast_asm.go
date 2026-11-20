//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the fast finder's assembly, shared by amd64 and arm64.

package zstd

// compressBlockFastNoDict is compressBlockFastNoDictGeneric with the main
// loop in assembly; the setup and the repcode bookkeeping around it are the
// same Go code.
func compressBlockFastNoDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if !useFastAsm {
		return compressBlockFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	cParams := &ms.cParams
	hlog := cParams.hashLog
	stepSize := int(cParams.targetLength) + 1 // min 2
	if cParams.targetLength == 0 {
		stepSize++
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	endIndex := srcIdx + uint32(len(src))
	prefixStartIndex := getLowestPrefixIndex(ms, endIndex, cParams.windowLog)
	prefixStart := int(prefixStartIndex - baseIdx)
	istart := int(srcIdx - baseIdx)
	iend := istart + len(src)
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	// prefixStartIndex >= baseIdx is checked explicitly (the assembly
	// addresses b with b - baseIdx + index for indices >= prefixStartIndex).
	if len(ms.hashTable) < 1<<hlog || iend > len(b) || prefixStartIndex < baseIdx || prefixStart > istart ||
		cap(ss.literals)-nLits < len(src)+16 || cap(ss.sequences)-nSeqs < len(src)/4 {
		return compressBlockFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	hShift, prime := fastHashParams(mls)

	ip0 := istart
	repOffset1 := rep[0]
	repOffset2 := rep[1]
	var offsetSaved1, offsetSaved2 uint32
	if ip0 == prefixStart {
		ip0++
	}
	{
		curr := uint32(ip0) + baseIdx
		windowLow := getLowestPrefixIndex(ms, curr, cParams.windowLog)
		maxRep := curr - windowLow
		if repOffset2 > maxRep {
			offsetSaved2, repOffset2 = repOffset2, 0
		}
		if repOffset1 > maxRep {
			offsetSaved1, repOffset1 = repOffset1, 0
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
	asm := compressBlockFastAsm
	if int(srcIdx-ms.frameStart) < staleTables<<hlog || ms.staleHint {
		asm = compressBlockFastAsmStale
	}
	anchor, nSeq, nLit, r1, r2, llType, llPos := asm(&ms.hashTable[0], &b[0], seqPtr, litPtr,
		ip0, istart, iend, prefixStart, prefixStartIndex, baseIdx, repOffset1, repOffset2,
		stepSize, uint64(hShift), uint64(64-hlog), prime)
	ms.staleHint = nLit*staleLitsFast < iend-istart // sequences' literals only (see staleTables)
	if nSeq < 0 {
		panic("zstd: fast hash table holds an index at or after the current position")
	}
	ms.asmLoopRuns++
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	repOffset1, repOffset2 = r1, r2

	// C: _cleanup — see compressBlockFastNoDictGeneric.
	if offsetSaved1 != 0 && repOffset1 != 0 {
		offsetSaved2 = offsetSaved1
	}
	rep[0] = repOffset1
	if repOffset1 == 0 {
		rep[0] = offsetSaved1
	}
	rep[1] = repOffset2
	if repOffset2 == 0 {
		rep[1] = offsetSaved2
	}
	return iend - anchor
}

// The fast and double-fast finders have two assembly loops with the same
// output: one branches on whether a candidate's index is inside the window,
// the other (...Stale) does not. The branch predicts well once the frame has
// refreshed most of the tables; while they still hold many entries from
// before the window (a context reused for a new frame), it mispredicts, and
// the branchless loop is faster. The Stale loop runs while the frame has
// covered fewer than staleTables table sizes (counted from the frame's
// start, ms.frameStart, not from the window's low limit: with a small
// window that distance stays below the threshold for the whole stream,
// while the tables are fresh once one window has passed), or after a block whose
// sequences' literals were under 1/staleLits* of it. That count leaves out
// the block's trailing literals on purpose: it is low both when matches
// cover the block (xml, nci) and when the finder found few matches, i.e.
// skipped through incompressible data with a growing step; in both cases
// few positions are inserted and old entries survive. Counting the
// trailing literals too picked the branchy loop on incompressible input,
// which measured 9-19% slower there (4 MiB of random bytes, zstd -3
// output of mozilla; L1 and L3). Measured on silesia: the branchless loop
// alone made whole files 1-4% slower at L1 and up to 4% at L3; these
// choices are at or below the branchy loop on every file.
// The double-fast finder also runs the Stale loop when its long table has
// at least 1/2^staleWindowLog as many entries as the window has positions:
// entries then often outlive the window (level 4 on large inputs, a 2^18
// table for a 2^21 window: mozilla +6.8%, samba +3.4%, silesia.tar in 4 MiB
// blocks +2.4% on an i9-13900H), while at level 3 (2^17) the branchy loop
// stays faster (x-ray and ooffice -3% with the branchless one).
const (
	staleTables     = 4
	staleLitsFast   = 16
	staleLitsDouble = 8
	staleWindowLog  = 3
)

// compressBlockFastExtDictAsm runs the search loop of
// compressBlockFastExtDictGeneric from ip0 (C: zstd_fast.c:
// ZSTD_compressBlock_fast_extDict_generic, _start to _cleanup) over the
// prefix b and the extDict d, with the results of compressBlockFastAsm.
// nSeq is -1 if a hash table entry pointed at or after the position being
// searched, or into the extDict's last 3 bytes (both impossible; the Go
// code would read outside d for the latter). The preconditions are the
// BOUNDS in zstd_fast_extdict_amd64.s (and _arm64.s), checked by
// compressBlockFastExtDictMls.
//
//go:noescape
func compressBlockFastExtDictAsm(hashTable *uint32, b, d *byte, seqs *seqDef, lits *byte,
	ip0, istart, iend, prefixStart, dictStart, dictEnd int,
	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

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
