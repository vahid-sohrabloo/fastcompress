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
