//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the binary-tree assembly, shared by amd64 and arm64.

package zstd

func dubtAsmOK(ms *matchState, ip, iend int, windowLow uint32) bool {
	btMask := uint32(1)<<(ms.cParams.chainLog-1) - 1
	return useDubtAsm && len(ms.chainTable) >= 2*int(btMask+1) && ip >= 0 && ip < iend &&
		iend <= len(ms.window.base) && windowLow >= ms.window.baseIdx
}

func insertDUBT1(ms *matchState, curr uint32, inputEnd int, nbCompares, btLow uint32) {
	baseIdx := ms.window.baseIdx
	ip := int(curr - baseIdx)
	windowValid := ms.window.lowLimit
	maxDistance := uint32(1) << ms.cParams.windowLog
	windowLow := windowValid
	if curr-windowValid > maxDistance {
		windowLow = curr - maxDistance
	}
	if !dubtAsmOK(ms, ip, inputEnd, windowLow) {
		insertDUBT1Generic(ms, curr, inputEnd, nbCompares, btLow)
		return
	}
	btMask := uint32(1)<<(ms.cParams.chainLog-1) - 1
	if !insertDUBT1Asm(&ms.chainTable[0], &ms.window.base[0], ip, inputEnd, curr, baseIdx, btMask, nbCompares, btLow, windowLow) {
		panic("zstd: binary tree holds an index at or after the current position")
	}
}

func dubtSearch(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, uint32) {
	if !dubtAsmOK(ms, ip, iend, windowLow) {
		return dubtSearchGeneric(ms, ip, iend, curr, matchIndex, nbCompares, btLow, windowLow)
	}
	btMask := uint32(1)<<(ms.cParams.chainLog-1) - 1
	best, offBase, matchEndIdx := dubtSearchAsm(&ms.chainTable[0], &ms.window.base[0], ip, iend, curr, ms.window.baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex)
	if matchEndIdx == 0 {
		panic("zstd: binary tree holds an index at or after the current position")
	}
	return best, offBase, matchEndIdx
}

// insertBt1Loop runs insertBt1's tree walk in assembly from matchIndex
// (hashTable already updated), returning bestLength and matchEndIdx, or
// ok = false (nothing done) when the assembly is off or a precondition
// fails (see dubtAsmOK; candidates are >= windowLow >= baseIdx).
func insertBt1Loop(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, bool) {
	if !dubtAsmOK(ms, ip, iend, windowLow) {
		return 0, 0, false
	}
	btMask := uint32(1)<<(ms.cParams.chainLog-1) - 1
	bestLength, matchEndIdx := insertBt1Asm(&ms.chainTable[0], &ms.window.base[0], ip, iend, curr, ms.window.baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex)
	if matchEndIdx == 0 {
		panic("zstd: binary tree holds an index at or after the current position")
	}
	return bestLength, matchEndIdx, true
}

// btAsmSetup prepares ms.btAsm for compressBlockOptGeneric's noDict
// searches (with their match table) or btlazy2's (matches nil) over a
// block ending at iend (a position in ms.window.base), for mls 4 to 6, and
// on amd64 (btAsmMls3) for the optimal parser's mls 3, which adds its hash3
// table: then 0 < hashLog3 (so a hash3 is below 1<<hashLog3 <=
// len(hashTable3)) and baseIdx <= ms.nextToUpdate3, which compressBlockOptGeneric
// has just set to nextToUpdate. Every later value of nextToUpdate3 is the
// index of a searched position (> baseIdx), so the positions hashed into
// hashTable3 always lie in the window's buffer.
func btAsmSetup(ms *matchState, matches *[optSize]optMatch, mls uint32, iend int) {
	p := &ms.btAsm
	p.ok = false
	cParams := &ms.cParams
	b := ms.window.base
	btMask := uint32(1)<<(cParams.chainLog-1) - 1
	mls3 := mls == 3 && btAsmMls3 && matches != nil && ms.hashLog3 > 0 && len(ms.hashTable3) >= 1<<ms.hashLog3 &&
		ms.nextToUpdate3 >= ms.window.baseIdx
	if !useDubtAsm || (mls < 4 && !mls3) || mls > 6 || len(ms.hashTable) < 1<<cParams.hashLog || len(ms.chainTable) < 2*int(btMask+1) ||
		iend > len(b) || len(b) == 0 || ms.window.lowLimit < ms.window.baseIdx {
		return
	}
	*p = btAsmParams{
		bt:            &ms.chainTable[0],
		b:             &b[0],
		hashTable:     &ms.hashTable[0],
		nextToUpdate:  &ms.nextToUpdate,
		prime:         saltedHashPrime[mls] << saltedHashShift[mls],
		hShift:        uint64(64 - cParams.hashLog),
		baseIdx:       ms.window.baseIdx,
		btMask:        btMask,
		nbCompares:    1 << cParams.searchLog,
		lowestValid:   ms.window.lowLimit,
		maxDistance:   1 << cParams.windowLog,
		dictLimit:     ms.window.dictLimit,
		sufficientLen: min(cParams.targetLength, optNum-1),
		iLimit:        iend,
		ok:            true,
	}
	if matches != nil {
		p.matches = &matches[0]
	}
	if mls3 {
		p.hashTable3 = &ms.hashTable3[0]
		p.nextToUpdate3 = &ms.nextToUpdate3
		p.hash3Shift = 32 - ms.hashLog3
		p.mls3 = 1
	}
	if ms.loadedDictEnd != 0 {
		p.isDictionary = 1
	}
}

// btGetAllNoDict is insertBtAndGetAllMatches (noDict, mls 3 to 6) in
// assembly, writing into matches, with ms.btAsm set up for this block. ok
// is false (nothing done) when it is not set up or a precondition fails:
// matches is the table btAsmSetup was given (the assembly writes there),
// 0 < ip, ip + 8 <= iLimit (the hash reads 8 bytes), lengthToBeat >= 1, and
// the window's lowLimit <= ip's index (so the candidates' lower bound is at
// most curr).
func btGetAllNoDict(matches *[optSize]optMatch, ms *matchState, ip, iLimit int, rep *[repNum]uint32, ll0 bool, lengthToBeat uint32) (uint32, bool) {
	p := &ms.btAsm
	if !p.ok || p.matches != &matches[0] || iLimit != p.iLimit || ip <= 0 || ip+8 > iLimit || lengthToBeat < 1 || p.lowestValid > uint32(ip)+p.baseIdx {
		return 0, false
	}
	var l uint32
	if ll0 {
		l = 1
	}
	mnum, bad := btGetAllNoDictAsm(p, ip, rep, l, lengthToBeat)
	if bad {
		panic("zstd: binary tree holds an index at or after the current position")
	}
	ms.asmCalls++
	return mnum, true
}

// btGetAllMatchesFast is btGetAllMatches for dictMode noDict in assembly
// (btGetAllNoDictAsm also updates the tree), with ms.btAsm set up for this
// block. ok is false (nothing done) when it is not set up or a
// precondition fails: btGetAllNoDict's, and baseIdx <= ms.nextToUpdate (the
// positions inserted lie in the window's buffer).
func btGetAllMatchesFast(matches *[optSize]optMatch, ms *matchState, ip, iLimit int, rep *[repNum]uint32, ll0 bool, lengthToBeat uint32) (uint32, bool) {
	p := &ms.btAsm
	if !p.ok || p.matches != &matches[0] || iLimit != p.iLimit || ip <= 0 || ip+8 > iLimit || lengthToBeat < 1 ||
		p.lowestValid > uint32(ip)+p.baseIdx || ms.nextToUpdate < p.baseIdx {
		return 0, false
	}
	var l uint32
	if ll0 {
		l = 1
	}
	mnum, bad := btGetAllNoDictAsm(p, ip, rep, l, lengthToBeat)
	if bad {
		panic("zstd: binary tree holds an index at or after the current position")
	}
	ms.asmCalls++
	return mnum, true
}

// lazyBtRun runs compressBlockLazyGeneric's match loop (binary-tree
// finder, noDict) in assembly from ip until ilimit, storing into ss, and
// returns the new ip, anchor and repcodes. It returns ok = false, doing
// nothing, when ms.btAsm is not set up for this block (btAsmSetup: the
// assembly is on, mls 4 to 6, the tables' sizes) or a precondition fails:
// 0 < ip, anchor <= ip, ilimit + 8 <= iend == the block's end (every
// searched position has 8 bytes to hash), room in ss for every literal (and
// 16 bytes of slack for the 16-byte literal copy) and
// a sequence per 4 input bytes (every match is >= 4), baseIdx <=
// ms.nextToUpdate, and the window's lowLimit <= ip's index: then every
// search the loop makes (at increasing positions up to ilimit) meets
// dubtFindBestMatchFast's preconditions.
func lazyBtRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int) (int, int, uint32, uint32, bool) {
	bp := &ms.btAsm
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	if !bp.ok || ip <= 0 || anchor > ip || ilimit+8 > iend || iend != bp.iLimit ||
		ms.nextToUpdate < bp.baseIdx || bp.lowestValid > uint32(ip)+bp.baseIdx ||
		prefixLowest < 0 || depth < 0 || depth > 2 ||
		cap(ss.literals)-nLits < iend-anchor+16 || cap(ss.sequences)-nSeqs < (iend-anchor)/4 {
		return ip, anchor, offset1, offset2, false
	}
	p := &ms.rowAsm
	*p = rowAsmParams{
		b:            bp.b,
		nextToUpdate: &ms.nextToUpdate,
		baseIdx:      bp.baseIdx,
		bt:           bp,
	}
	if ms.lazySkipping {
		p.skip = 1
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
	ipOut, anchorOut, nSeq, nLit, r1, r2, llType, llPos := lazyBt(p, seqPtr, litPtr, ip, anchor, iend, ilimit, prefixLowest, offset1, offset2, depth)
	if nSeq < 0 {
		panic("zstd: binary tree holds an index at or after the current position")
	}
	ms.asmLoopRuns++
	ms.lazySkipping = p.skip != 0
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	ms.asmCalls++
	return ipOut, anchorOut, r1, r2, true
}

// dubtFindBestMatchFast is updateDUBT + dubtFindBestMatch in assembly, with
// ms.btAsm set up for this block. ok is false (nothing done) when it is not
// set up or a precondition fails: 0 < ip, ip + 8 <= iend (the hash reads 8
// bytes), baseIdx <= ms.nextToUpdate, and the window's lowLimit <= ip's
// index.
func dubtFindBestMatchFast(ms *matchState, ip, iend int) (int, uint32, bool) {
	p := &ms.btAsm
	if !p.ok || iend != p.iLimit || ip <= 0 || ip+8 > iend || ms.nextToUpdate < p.baseIdx || p.lowestValid > uint32(ip)+p.baseIdx {
		return 0, 0, false
	}
	bestLength, offBase, bad := dubtFindBestMatchAsm(p, ip)
	if bad {
		panic("zstd: binary tree holds an index at or after the current position")
	}
	ms.asmCalls++
	return bestLength, offBase, true
}
