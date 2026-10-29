//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useRowAsm selects the assembly versions of the row match finder's inner
// loops (SSE2 and PREFETCHT0, which every amd64 CPU has).
var useRowAsm bool

func init() {
	cpu.RegisterAsm("row", &useRowAsm, func() bool { return true })
}

// rowUpdateCachedAsm is rowUpdateCachedGeneric (see it for the meaning) with
// the hash parameters precomputed: hash(p) = ((le64(b[p:]) << shift) *
// prime ^ saltAdj) >> hShift, as in hashPtrSalted. It also prefetches the
// rows of the new hashes (C: ZSTD_row_prefetch).
//
// Preconditions (checked by rowUpdateCached): idx < end; b holds the 8
// bytes at end-1-baseIdx+rowHashCacheSize; hashTable and tagTable hold
// every row (1<<(hBits-rowHashTagBits+rowLog) entries); rowMask is
// 1<<rowLog - 1 with rowLog in 4..6.
//
//go:noescape
func rowUpdateCachedAsm(hashTable *uint32, tagTable *byte, cache *[rowHashCacheSize]uint32, b *byte,
	idx, end, baseIdx, rowLog, rowMask uint32, shift, hShift, prime, saltAdj uint64)

// rowSearchAsm is rowSearchGeneric (see it for the meaning), computing the
// match mask with SSE2 like C's ZSTD_row_getSSEMask and prefetching each
// candidate (C: PREFETCH_L1(base + matchIndex)).
//
// Preconditions (checked by rowSearch): the row at relRow (1<<rowLog
// entries, rowLog in 4..6) lies within hashTable and tagTable; 0 < ip <
// iLimit <= len(b); lowLimit >= baseIdx. A candidate index at or after
// ip's makes it return ml = -1 instead of reading past ip (it cannot happen:
// the tables only hold earlier positions).
//
//go:noescape
func rowSearchAsm(hashTable *uint32, tagTable *byte, b *byte, ip, iLimit int,
	baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, nextToUpdate uint32) (ml int, best uint32)

func rowUpdateCached(ms *matchState, idx, end, mls, rowLog uint32) {
	if !useRowAsm || idx >= end {
		rowUpdateCachedGeneric(ms, idx, end, mls, rowLog)
		return
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	hBits := ms.rowHashLog + rowHashTagBits
	rows := 1 << (ms.rowHashLog + rowLog)
	if rowLog < 4 || rowLog > 6 || len(ms.hashTable) < rows || len(ms.tagTable) < rows ||
		int(end-1-baseIdx)+rowHashCacheSize+8 > len(b) {
		panic("zstd: rowUpdateCached preconditions")
	}
	m := mls & 15
	rowUpdateCachedAsm(&ms.hashTable[0], &ms.tagTable[0], &ms.hashCache, &b[0],
		idx, end, baseIdx, rowLog, 1<<rowLog-1,
		uint64(saltedHashShift[m]), uint64(64-hBits), saltedHashPrime[m], ms.hashSalt<<saltedHashSaltShift[m])
}

func rowSearch(ms *matchState, ip, iLimit int, relRow, tag, rowLog, nbAttempts, lowLimit uint32) (int, uint32) {
	if !useRowAsm {
		return rowSearchGeneric(ms, ip, iLimit, relRow, tag, rowLog, nbAttempts, lowLimit)
	}
	b := ms.window.base
	end := int(relRow) + 1<<rowLog
	if rowLog < 4 || rowLog > 6 || end > len(ms.hashTable) || end > len(ms.tagTable) ||
		ip <= 0 || ip >= iLimit || iLimit > len(b) {
		panic("zstd: rowSearch preconditions")
	}
	baseIdx := ms.window.baseIdx
	if lowLimit < baseIdx {
		panic("zstd: rowSearch lowLimit below the window base")
	}
	ml, best := rowSearchAsm(&ms.hashTable[0], &ms.tagTable[0], &b[0], ip, iLimit,
		baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, ms.nextToUpdate)
	if ml < 0 {
		panic("zstd: row table holds an index at or after the current position")
	}
	return ml, best
}
