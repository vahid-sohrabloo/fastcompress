//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the double-fast finder's assembly, shared by amd64 and arm64.

package zstd

// compressBlockDoubleFastNoDict is compressBlockDoubleFastNoDictGeneric
// with the main loop in assembly; the setup and the repcode bookkeeping
// around it are the same Go code.
func compressBlockDoubleFastNoDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if !useDoubleFastAsm {
		return compressBlockDoubleFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	cParams := &ms.cParams
	hBitsL := cParams.hashLog
	hBitsS := cParams.chainLog
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	istart := int(srcIdx - baseIdx)
	endIndex := srcIdx + uint32(len(src))
	prefixLowestIndex := getLowestPrefixIndex(ms, endIndex, cParams.windowLog)
	prefixLowest := int(prefixLowestIndex - baseIdx)
	iend := istart + len(src)
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	// prefixLowestIndex >= baseIdx is checked explicitly (the assembly
	// addresses b with b - baseIdx + index for indices >= prefixLowestIndex).
	if len(ms.hashTable) < 1<<hBitsL || len(ms.chainTable) < 1<<hBitsS || iend > len(b) ||
		prefixLowestIndex < baseIdx || prefixLowest > istart || cap(ss.literals)-nLits < len(src) || cap(ss.sequences)-nSeqs < len(src)/4 {
		return compressBlockDoubleFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	offset1, offset2 := rep[0], rep[1]
	var offsetSaved1, offsetSaved2 uint32
	ip := istart
	if ip == prefixLowest {
		ip++
	}
	{
		current := uint32(ip) + baseIdx
		windowLow := getLowestPrefixIndex(ms, current, cParams.windowLog)
		maxRep := current - windowLow
		if offset2 > maxRep {
			offsetSaved2, offset2 = offset2, 0
		}
		if offset1 > maxRep {
			offsetSaved1, offset1 = offset1, 0
		}
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
	asm := compressBlockDoubleFastAsm
	if istart-prefixLowest < staleTables<<hBitsL || ms.staleHint {
		asm = compressBlockDoubleFastAsmStale
	}
	anchor, nSeq, nLit, r1, r2, llType, llPos := asm(&ms.hashTable[0], &ms.chainTable[0], &b[0], seqPtr, litPtr,
		ip, istart, iend, prefixLowest, prefixLowestIndex, baseIdx, offset1, offset2,
		uint64(64-hBitsL), uint64(shiftS), uint64(64-hBitsS), primeS)
	ms.staleHint = nLit*staleLitsDouble < iend-istart
	if nSeq < 0 {
		panic("zstd: double-fast hash table holds an index at or after the current position")
	}
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	offset1, offset2 = r1, r2

	// C: _cleanup — see compressBlockDoubleFastNoDictGeneric.
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
