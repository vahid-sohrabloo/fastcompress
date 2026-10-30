//go:build arm64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useDubtAsm selects the assembly binary-tree loops (baseline arm64).
var useDubtAsm bool

func init() {
	cpu.RegisterAsm("dubt", &useDubtAsm, func() bool { return true })
}

// insertDUBT1Asm is the loop of insertDUBT1Generic (see it) on the tree bt
// (pairs of entries, btMask+1 of them) over the input b, for position ip
// (index curr), with windowLow precomputed. It returns false, leaving the
// tree partially updated, if a candidate is not before ip (impossible: the
// tree only holds earlier positions).
//
// Preconditions (checked by insertDUBT1): len(bt) >= 2*(btMask+1);
// 0 <= ip < iend <= len(b); windowLow >= baseIdx, so every candidate
// (> windowLow) lies in b.
//
//go:noescape
func insertDUBT1Asm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow uint32) bool

// dubtSearchAsm is dubtSearchGeneric's loop (see it), with the same
// preconditions as insertDUBT1Asm. matchEndIdx is 0 if a candidate was not
// before ip (impossible).
//
//go:noescape
func dubtSearchAsm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, offBase, matchEndIdx uint32)

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
