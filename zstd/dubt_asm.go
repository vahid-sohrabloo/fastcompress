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
// searches over a block ending at iend (a position in ms.window.base), for
// mls 4 to 6 (mls 3 adds the hash3 table, left to the Go code).
func btAsmSetup(ms *matchState, matches *[optSize]optMatch, mls uint32, iend int) {
	p := &ms.btAsm
	p.ok = false
	cParams := &ms.cParams
	b := ms.window.base
	btMask := uint32(1)<<(cParams.chainLog-1) - 1
	if !useDubtAsm || mls < 4 || mls > 6 || len(ms.hashTable) < 1<<cParams.hashLog || len(ms.chainTable) < 2*int(btMask+1) ||
		iend > len(b) || len(b) == 0 || ms.window.lowLimit < ms.window.baseIdx {
		return
	}
	*p = btAsmParams{
		bt:            &ms.chainTable[0],
		b:             &b[0],
		hashTable:     &ms.hashTable[0],
		matches:       &matches[0],
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
	if ms.loadedDictEnd != 0 {
		p.isDictionary = 1
	}
}

// btGetAllNoDict is insertBtAndGetAllMatches (noDict, mls 4 to 6) in
// assembly, with ms.btAsm set up for this block and matches the table it
// names. ok is false (nothing done) when it is not set up or a
// precondition fails: 0 < ip, ip + 8 <= iLimit (the hash reads 8 bytes),
// lengthToBeat >= 1, and the window's lowLimit <= ip's index (so the
// candidates' lower bound is at most curr).
func btGetAllNoDict(ms *matchState, ip, iLimit int, rep *[repNum]uint32, ll0 bool, lengthToBeat uint32) (uint32, bool) {
	p := &ms.btAsm
	if !p.ok || iLimit != p.iLimit || ip <= 0 || ip+8 > iLimit || lengthToBeat < 1 || p.lowestValid > uint32(ip)+p.baseIdx {
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
	return mnum, true
}
