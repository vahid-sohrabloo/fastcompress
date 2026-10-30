//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useDubtAsm selects the assembly binary-tree loops (baseline amd64).
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
