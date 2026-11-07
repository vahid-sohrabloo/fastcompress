// Port of libzstd v1.5.7 lib/compress/zstd_lazy.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the inner loops of
// the row-based match finder, which have amd64 and arm64 assembly versions
// (zstd_lazy_row_amd64.s, zstd_lazy_row_arm64.s). These Go bodies are always compiled.

package zstd

import "encoding/binary"

// rowUpdateCachedGeneric inserts the indices [idx, end) into the hash and
// tag tables, taking each hash from the hash cache and refilling it with
// the hash rowHashCacheSize positions ahead. C: zstd_lazy.c:
// ZSTD_row_update_internalImpl with useCache = 1 (and
// ZSTD_row_nextCachedHash; its ZSTD_row_prefetch is a cache hint only).
func rowUpdateCachedGeneric(ms *matchState, idx, end, mls, rowLog uint32) {
	hashTable := ms.hashTable
	tagTable := ms.tagTable
	hBits := ms.rowHashLog + rowHashTagBits
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	salt := ms.hashSalt
	rowMask := uint32(1)<<rowLog - 1
	cache := &ms.hashCache
	for ; idx < end; idx++ {
		newHash := hashPtrSalted(b, int(idx-baseIdx)+rowHashCacheSize, hBits, mls, salt)
		hash := cache[idx&rowHashCacheMask]
		cache[idx&rowHashCacheMask] = newHash
		relRow := (hash >> rowHashTagBits) << rowLog
		tagRow := tagTable[relRow : relRow+rowMask+1]
		pos := rowNextIndex(tagRow, rowMask)
		tagRow[pos] = byte(hash & rowHashTagMask)
		hashTable[relRow+pos] = idx
	}
}

// rowSearchGeneric looks for the longest match at position ip (in
// ms.window.base) among the entries of the row at relRow whose tag equals
// tag, newest first, trying at most nbAttempts candidates and stopping at
// the first below lowLimit. It then inserts ms.nextToUpdate (ip's index)
// into the row; the caller increments ms.nextToUpdate. It returns the match
// length (below 4 when none was found) and the match's index.
// C: zstd_lazy.c:ZSTD_RowFindBestMatch (noDict), from the match mask on.
func rowSearchGeneric(ms *matchState, ip, iLimit int, relRow, tag, rowLog, nbAttempts, lowLimit uint32) (int, uint32) {
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	rowEntries := uint32(1) << rowLog
	rowMask := rowEntries - 1
	row := ms.hashTable[relRow : relRow+rowEntries]
	tagRow := ms.tagTable[relRow : relRow+rowEntries]
	headGrouped := uint32(tagRow[0]) & rowMask
	var matchBuffer [rowHashMaxEntries]uint32
	numMatches := 0
	matches := rowGetMatchMask(tagRow, byte(tag), headGrouped, rowEntries)

	// Cycle through the matches
	for ; matches > 0 && nbAttempts > 0; matches &= matches - 1 {
		matchPos := (headGrouped + rowVecMaskNext(matches)) & rowMask
		matchIndex := row[matchPos]
		if matchPos == 0 {
			continue
		}
		if matchIndex < lowLimit {
			break
		}
		matchBuffer[numMatches] = matchIndex
		numMatches++
		nbAttempts--
	}

	// Speed opt: insert current byte into hashtable too. This allows us to
	// avoid one iteration of the loop in rowUpdateInternal() at the next
	// search.
	{
		pos := rowNextIndex(tagRow, rowMask)
		tagRow[pos] = byte(tag)
		row[pos] = ms.nextToUpdate
	}

	// Return the longest match
	ml := 4 - 1
	var best uint32
	for _, matchIndex := range matchBuffer[:numMatches] {
		currentMl := 0
		match := int(matchIndex - baseIdx)
		// read 4B starting from (match + ml + 1 - sizeof(U32))
		if binary.LittleEndian.Uint32(b[match+ml-3:]) == binary.LittleEndian.Uint32(b[ip+ml-3:]) { // potentially better
			currentMl = count(b, ip, match, iLimit)
		}
		// Save best solution
		if currentMl > ml {
			ml = currentMl
			best = matchIndex
			if ip+currentMl == iLimit {
				break // best possible, avoids read overflow on next attempt
			}
		}
	}
	return ml, best
}

// rowAsmParams holds what the assembly lazyRow* (or, set up by hcAsmSetup,
// lazyHc*) reads for one block; ok is false when it is not set up (always,
// without assembly). The layout is fixed: row_lazy_*.s and hc_lazy_*.s read
// the fields by offset (checked by TestRowAsmParamsLayout).
type rowAsmParams struct {
	hashTable    *uint32                   // 0
	tagTable     *byte                     // 8
	b            *byte                     // 16
	cache        *[rowHashCacheSize]uint32 // 24
	nextToUpdate *uint32                   // 32
	entropy      *uint32                   // 40
	hShift       uint64                    // 48: hash = (le64 * (prime << shift) ^ saltAdj) >> hShift
	saltAdj      uint64                    // 56
	baseIdx      uint32                    // 64
	nbAttempts   uint32                    // 68
	lowestValid  uint32                    // 72
	maxDistance  uint32                    // 76
	isDictionary uint32                    // 80
	ok           bool                      // 84
	bLen         int                       // 88: positions up to bLen-16 may be searched
	iLimit       int                       // 96: the end of the input, for the searches' ZSTD_count
	skip         uint32                    // 104: lazySkipping, while lazyRow* or lazyHc* runs
	chainTable   *uint32                   // 112: lazyHc* only (tagTable, cache and entropy are then unused)
	chainMask    uint32                    // 120: lazyHc* only
	bt           *btAsmParams              // 128: lazyBt only (its finder's parameters)
}
