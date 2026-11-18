// Port of libzstd v1.5.7 lib/compress/zstd_lazy.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the binary-tree,
// hash-chain and row-based match finders and the lazy parser
// (ZSTD_compressBlock_lazy_generic) with searchMethod search_binaryTree,
// search_hashChain or search_rowHash and dictMode ZSTD_noDict.

package zstd

import (
	"encoding/binary"
	"math/bits"
)

// Constants for the row-based hash. C: zstd_lazy.c, zstd_compress_internal.h
const (
	kLazySkippingStep = 8                     // C: kLazySkippingStep
	rowHashTagMask    = 1<<rowHashTagBits - 1 // C: ZSTD_ROW_HASH_TAG_MASK
	rowHashMaxEntries = 64                    // C: ZSTD_ROW_HASH_MAX_ENTRIES: absolute maximum number of entries per row, for all configurations
	rowHashCacheSize  = 8                     // C: ZSTD_ROW_HASH_CACHE_SIZE: size of prefetching hash cache for row-based matchfinder
	rowHashCacheMask  = rowHashCacheSize - 1  // C: ZSTD_ROW_HASH_CACHE_MASK
	offBaseNotFound   = 999999999             // the C callers' initial offBase of a search
)

// hashPtrSalted hashes the first mls bytes at b[i:] to hBits bits, salted.
// C: zstd_compress_internal.h:ZSTD_hashPtrSalted
//
// The salt is XORed in before the final shift, so the result is the
// unsalted hash XOR (salt >> shift): a constant relabelling of rows and
// tags. That is why the salt (which C changes on every context reset) does
// not change output (see TestRowHashSaltNeutral).
//
// Branch-free (so it inlines into the row match finder's loops): every mls
// is the 64-bit form ((le64 << shift) * prime ^ salt) >> (64 - hBits). For
// mls 4, C's 32-bit (u32 * prime4 ^ uint32(salt)) >> (32 - hBits) is the
// same value with shift 32 and the salt's low 32 bits moved to the top: the
// low 32 bits of (u32 << 32) * prime4 are zero and its top 32 bits are
// u32 * prime4 mod 2^32. Reads 8 bytes, as C does for mls >= 5.
func hashPtrSalted(b []byte, i int, hBits, mls uint32, salt uint64) uint32 {
	m := mls & 15
	return uint32((((binary.LittleEndian.Uint64(b[i:]) << saltedHashShift[m]) * saltedHashPrime[m]) ^ (salt << saltedHashSaltShift[m])) >> (64 - hBits))
}

// Parameters of hashPtrSalted per mls (3..8; other entries unused). mls 3
// hashes 4 bytes, as C's switch default does: rowUpdate passes minMatch
// capped at 6 but not raised to 4 (C: ZSTD_row_update), which a dictionary
// load with minMatch 3 reaches.
var (
	saltedHashShift     = [16]uint8{3: 32, 4: 32, 5: 64 - 40, 6: 64 - 48, 7: 64 - 56, 8: 0}
	saltedHashPrime     = [16]uint64{3: prime4bytes, 4: prime4bytes, 5: prime5bytes, 6: prime6bytes, 7: prime7bytes, 8: prime8bytes}
	saltedHashSaltShift = [16]uint8{3: 32, 4: 32}
)

// bitmix mixes the bits of a 64-bit value, based on XXH3_rrmxmx.
// C: zstd_compress.c:ZSTD_bitmix
func bitmix(val, len uint64) uint64 {
	val ^= bits.RotateLeft64(val, -49) ^ bits.RotateLeft64(val, -24)
	val *= 0x9FB21C651E98DF25
	val ^= (val >> 35) + len
	val *= 0x9FB21C651E98DF25
	return val ^ (val >> 28)
}

// advanceHashSalt mixes the hash salt and the collected salt entropy into a
// new hash salt. C: zstd_compress.c:ZSTD_advanceHashSalt
func advanceHashSalt(ms *matchState) {
	ms.hashSalt = bitmix(ms.hashSalt, 8) ^ bitmix(uint64(ms.hashSaltEntropy), 4)
}

// resizeTagTable returns t with n entries, reusing its storage when possible.
// C: zstd_cwksp.h:ZSTD_cwksp_reserve_aligned_init_once — memory is zeroed
// the first time it is handed out, then reused as left behind.
//
// C's leftover bytes depend on where the workspace places the table, which
// this port cannot reproduce. That cannot change output: a row's entries
// are visited newest first from its head, wherever the head starts, and the
// first entry not written in this frame holds an index below the window
// (the hash table is zeroed or holds earlier frames' indices), which ends
// the search whatever its tag. TestRowHashSaltNeutral fills the tag table
// with garbage to check this.
func resizeTagTable(t []byte, n int) []byte {
	if cap(t) < n {
		return make([]byte, n)
	}
	return t[:n]
}

// rowVecMaskNext returns the index of the lowest set bit of val.
// C: zstd_lazy.c:ZSTD_VecMask_next
func rowVecMaskNext(val uint64) uint32 { return uint32(bits.TrailingZeros64(val)) }

// rowNextIndex returns the next position to insert at within a tag row, and
// updates the row's head (tagRow[0]). It cycles backwards through [1,
// rowEntries). C: zstd_lazy.c:ZSTD_row_nextIndex
func rowNextIndex(tagRow []byte, rowMask uint32) uint32 {
	next := (uint32(tagRow[0]) - 1) & rowMask
	if next == 0 {
		next += rowMask // skip first position
	}
	tagRow[0] = byte(next)
	return next
}

// rowFillHashCache fills the hash cache starting at index idx, up to
// rowHashCacheSize entries but not beyond position iLimit of ms.window.base.
// C: zstd_lazy.c:ZSTD_row_fillHashCache — C also calls ZSTD_row_prefetch for
// each hash; it is a cache hint only, so it is omitted.
func rowFillHashCache(ms *matchState, b []byte, baseIdx, mls, idx uint32, iLimit int) {
	hashLog := ms.rowHashLog
	var maxElemsToPrefetch uint32
	if p := int(idx - baseIdx); p <= iLimit {
		maxElemsToPrefetch = uint32(iLimit - p + 1)
	}
	lim := idx + min(rowHashCacheSize, maxElemsToPrefetch)
	for ; idx < lim; idx++ {
		hash := hashPtrSalted(b, int(idx-baseIdx), hashLog+rowHashTagBits, mls, ms.hashSalt)
		ms.hashCache[idx&rowHashCacheMask] = hash
	}
}

// rowNextCachedHash returns the hash of index idx from the cache, and
// replaces it with the hash of idx + rowHashCacheSize.
// C: zstd_lazy.c:ZSTD_row_nextCachedHash — C also prefetches the row of the
// new hash; it is a cache hint only, so it is omitted.
func rowNextCachedHash(cache *[rowHashCacheSize]uint32, b []byte, baseIdx, idx, hashLog, mls uint32, hashSalt uint64) uint32 {
	newHash := hashPtrSalted(b, int(idx-baseIdx)+rowHashCacheSize, hashLog+rowHashTagBits, mls, hashSalt)
	hash := cache[idx&rowHashCacheMask]
	cache[idx&rowHashCacheMask] = newHash
	return hash
}

// rowUpdateInternalImpl inserts the indices [updateStartIdx, updateEndIdx)
// into the hash and tag tables.
// C: zstd_lazy.c:ZSTD_row_update_internalImpl
func rowUpdateInternalImpl(ms *matchState, updateStartIdx, updateEndIdx, mls, rowLog, rowMask uint32, useCache bool) {
	if useCache {
		rowUpdateCached(ms, updateStartIdx, updateEndIdx, mls, rowLog)
		return
	}
	hashTable := ms.hashTable
	tagTable := ms.tagTable
	hashLog := ms.rowHashLog
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	for ; updateStartIdx < updateEndIdx; updateStartIdx++ {
		hash := hashPtrSalted(b, int(updateStartIdx-baseIdx), hashLog+rowHashTagBits, mls, ms.hashSalt)
		relRow := (hash >> rowHashTagBits) << rowLog
		tagRow := tagTable[relRow : relRow+rowMask+1]
		pos := rowNextIndex(tagRow, rowMask)
		tagRow[pos] = byte(hash & rowHashTagMask)
		hashTable[relRow+pos] = updateStartIdx
	}
}

// rowUpdateInternal inserts the indices from ms.nextToUpdate up to position
// ip into the tables and updates ms.nextToUpdate, skipping the middle of
// long matches when using the hash cache.
// C: zstd_lazy.c:ZSTD_row_update_internal
func rowUpdateInternal(ms *matchState, ip int, mls, rowLog, rowMask uint32, useCache bool) {
	idx := ms.nextToUpdate
	baseIdx := ms.window.baseIdx
	target := uint32(ip) + baseIdx
	const (
		kSkipThreshold                  = 384
		kMaxMatchStartPositionsToUpdate = 96
		kMaxMatchEndPositionsToUpdate   = 32
	)

	if useCache {
		// Only skip positions when using hash cache, i.e. if we are loading
		// a dict, don't skip anything. If we decide to skip, then we only
		// update a set number of positions at the beginning and end of the
		// match.
		if target-idx > kSkipThreshold {
			bound := idx + kMaxMatchStartPositionsToUpdate
			rowUpdateInternalImpl(ms, idx, bound, mls, rowLog, rowMask, useCache)
			idx = target - kMaxMatchEndPositionsToUpdate
			rowFillHashCache(ms, ms.window.base, baseIdx, mls, idx, ip+1)
		}
	}
	rowUpdateInternalImpl(ms, idx, target, mls, rowLog, rowMask, useCache)
	ms.nextToUpdate = target
}

// rowUpdate inserts the positions up to ip (in ms.window.base) into the
// tables without the hash cache; used to fill the tables from a
// dictionary. C: zstd_lazy.c:ZSTD_row_update
func rowUpdate(ms *matchState, ip int) {
	rowLog := min(max(ms.cParams.searchLog, 4), 6)
	rowMask := uint32(1)<<rowLog - 1
	mls := min(ms.cParams.minMatch, 6) // mls caps out at 6
	rowUpdateInternal(ms, ip, mls, rowLog, rowMask, false)
}

// rowGetMatchMask returns a mask with bit n set when the tag of the nth
// entry of the row, counted from the row's head, equals tag. Each row is a
// circular buffer beginning at headGrouped, so the matches are rotated to
// line up with the layout of the entries. Group width is 1 (the scalar
// layout; NEON's wider groups are not used).
// C: zstd_lazy.c:ZSTD_row_getMatchMask, its SWAR (non-SIMD, 64-bit little
// endian) path. The SSE2 path C takes on x86-64 computes the same mask.
func rowGetMatchMask(tagRow []byte, tag byte, headGrouped uint32, rowEntries uint32) uint64 {
	const (
		chunkSize    = 8
		shiftAmount  = chunkSize*8 - chunkSize
		xFF          = ^uint64(0)
		x01          = xFF / 0xFF
		x80          = x01 << 7
		extractMagic = (xFF / 0x7F) >> chunkSize
	)
	splatChar := uint64(tag) * x01
	var matches uint64
	src := tagRow[:rowEntries]
	for i := int(rowEntries) - chunkSize; i >= 0; i -= chunkSize {
		chunk := binary.LittleEndian.Uint64(src[i:])
		chunk ^= splatChar
		chunk = (((chunk | x80) - x01) | chunk) & x80
		matches <<= chunkSize
		matches |= (chunk * extractMagic) >> shiftAmount
	}
	matches = ^matches
	switch rowEntries {
	case 16:
		return uint64(bits.RotateLeft16(uint16(matches), -int(headGrouped&15)))
	case 32:
		return uint64(bits.RotateLeft32(uint32(matches), -int(headGrouped&31)))
	default: // 64
		return bits.RotateLeft64(matches, -int(headGrouped&63))
	}
}

// rowFindBestMatch returns the length of the longest match at position ip
// (in ms.window.base) found in its row, and its offBase; a length below 4
// means none (offBase is then offBaseNotFound). iLimit is the end of the
// input. It also inserts ip (and the positions before it, from
// ms.nextToUpdate) into the tables.
// C: zstd_lazy.c:ZSTD_RowFindBestMatch with dictMode ZSTD_noDict.
// groupWidth is 1 (see rowGetMatchMask). C: zstd_lazy.c:ZSTD_RowFindBestMatch
// — C's PREFETCH_L1 of the row and of the matches are cache hints only, so
// they are omitted.
func rowFindBestMatch(ms *matchState, ip, iLimit int, mls, rowLog uint32) (int, uint32) {
	hashLog := ms.rowHashLog
	cParams := &ms.cParams
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	curr := uint32(ip) + baseIdx
	maxDistance := uint32(1) << cParams.windowLog
	lowestValid := ms.window.lowLimit
	withinMaxDistance := lowestValid
	if curr-lowestValid > maxDistance {
		withinMaxDistance = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	lowLimit := withinMaxDistance
	if isDictionary {
		lowLimit = lowestValid
	}
	rowEntries := uint32(1) << rowLog
	rowMask := rowEntries - 1
	cappedSearchLog := min(cParams.searchLog, rowLog) // nb of searches is capped at nb entries per row
	hashSalt := ms.hashSalt
	nbAttempts := uint32(1) << cappedSearchLog
	offBase := uint32(offBaseNotFound)
	var hash uint32

	// Update the hashTable and tagTable up to (but not including) ip
	if !ms.lazySkipping {
		rowUpdateInternal(ms, ip, mls, rowLog, rowMask, true)
		hash = rowNextCachedHash(&ms.hashCache, b, baseIdx, curr, hashLog, mls, hashSalt)
	} else {
		// Stop inserting every position when in the lazy skipping mode.
		// The hash cache is also not kept up to date in this mode.
		hash = hashPtrSalted(b, ip, hashLog+rowHashTagBits, mls, hashSalt)
		ms.nextToUpdate = curr
	}
	ms.hashSaltEntropy += hash // collect salt entropy

	// Get the hash for ip, compute the appropriate row
	relRow := (hash >> rowHashTagBits) << rowLog
	tag := hash & rowHashTagMask
	ml, matchIndex := rowSearchGeneric(ms, ip, iLimit, relRow, tag, rowLog, nbAttempts, lowLimit)
	ms.nextToUpdate++ // rowSearchGeneric inserted it
	if ml > 4-1 {
		offBase = offsetToOffBase(curr - matchIndex)
	}
	return ml, offBase
}

/*-*************************************
*  Binary Tree search
***************************************/

// The binary-tree match finder (btlazy2) keeps, in ms.chainTable, two
// entries per position of the last 1<<(chainLog-1) positions: the indices of
// its smaller and larger subtrees. Positions are first inserted unsorted,
// like a hash chain (next candidate, dubtUnsortedMark), and sorted into the
// tree lazily when a search reaches them ("DUBT": Delayed Update Binary
// Tree).
//
// Only dictMode ZSTD_noDict is ported, so every candidate lies in the
// prefix (base): C's extDict branches (dictBase, ZSTD_count_2segments) and
// ZSTD_DUBT_findBetterDictMatch are omitted.

// updateDUBT inserts the positions from ms.nextToUpdate up to ip (excluded)
// into the binary tree as unsorted candidates.
// C: zstd_lazy.c:ZSTD_updateDUBT — no iend parameter: C uses it only in an
// assert.
func updateDUBT(ms *matchState, ip int, mls uint32) {
	hashTable := ms.hashTable
	hashLog := ms.cParams.hashLog

	bt := ms.chainTable
	btLog := ms.cParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1

	b := ms.window.base
	baseIdx := ms.window.baseIdx
	target := uint32(ip) + baseIdx
	idx := ms.nextToUpdate

	for ; idx < target; idx++ {
		h := hashPtr(b, int(idx-baseIdx), hashLog, mls) // assumption : ip + 8 <= iend
		matchIndex := hashTable[h]

		nextCandidate := 2 * (idx & btMask) // C: nextCandidatePtr; sortMarkPtr is the next entry

		hashTable[h] = idx             // Update Hash Table
		bt[nextCandidate] = matchIndex // update BT like a chain
		bt[nextCandidate+1] = dubtUnsortedMark
	}
	ms.nextToUpdate = target
}

// dubtFindBestMatch sorts the unsorted candidates of ip's hash, then
// searches the tree for the longest match at ip, inserting ip into it. It
// returns the match length (0 if none) and its offBase (offBaseNotFound if
// none). iend is the end of the input.
// C: zstd_lazy.c:ZSTD_DUBT_findBestMatch with dictMode ZSTD_noDict; *offBasePtr
// starts at offBaseNotFound, the value every C caller passes.
func dubtFindBestMatch(ms *matchState, ip, iend int, mls uint32) (int, uint32) {
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hashLog := cParams.hashLog
	b := ms.window.base
	h := hashPtr(b, ip, hashLog, mls)
	matchIndex := hashTable[h]

	baseIdx := ms.window.baseIdx
	curr := uint32(ip) + baseIdx
	windowLow := getLowestMatchIndex(ms, curr, cParams.windowLog)

	bt := ms.chainTable
	btLog := cParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1
	var btLow uint32
	if btMask < curr {
		btLow = curr - btMask
	}
	unsortLimit := max(btLow, windowLow)

	nextCandidate := 2 * (matchIndex & btMask) // C: nextCandidate; unsortedMark is the next entry
	nbCompares := uint32(1) << cParams.searchLog
	nbCandidates := nbCompares
	var previousCandidate uint32

	// reach end of unsorted candidates list
	for matchIndex > unsortLimit && bt[nextCandidate+1] == dubtUnsortedMark && nbCandidates > 1 {
		bt[nextCandidate+1] = previousCandidate // the unsortedMark becomes a reversed chain, to move up back to original position
		previousCandidate = matchIndex
		matchIndex = bt[nextCandidate]
		nextCandidate = 2 * (matchIndex & btMask)
		nbCandidates--
	}

	// nullify last candidate if it's still unsorted
	// simplification, detrimental to compression ratio, beneficial for speed
	if matchIndex > unsortLimit && bt[nextCandidate+1] == dubtUnsortedMark {
		bt[nextCandidate] = 0
		bt[nextCandidate+1] = 0
	}

	// batch sort stacked candidates
	matchIndex = previousCandidate
	for matchIndex != 0 { // will end on matchIndex == 0
		nextCandidateIdx := bt[2*(matchIndex&btMask)+1]
		insertDUBT1(ms, matchIndex, iend, nbCandidates, unsortLimit)
		matchIndex = nextCandidateIdx
		nbCandidates++
	}

	// find longest match
	matchIndex = hashTable[h]
	hashTable[h] = curr // Update Hash Table
	bestLength, offBase, matchEndIdx := dubtSearch(ms, ip, iend, curr, matchIndex, nbCompares, btLow, windowLow)

	ms.nextToUpdate = matchEndIdx - 8 // skip repetitive patterns
	return bestLength, offBase
}

// btFindBestMatch is the tree updater, providing the best match at ip.
// C: zstd_lazy.c:ZSTD_BtFindBestMatch with dictMode ZSTD_noDict.
func btFindBestMatch(ms *matchState, ip, iLimit int, mls uint32) (int, uint32) {
	if uint32(ip)+ms.window.baseIdx < ms.nextToUpdate {
		return 0, offBaseNotFound // skipped area
	}
	if bestLength, offBase, ok := dubtFindBestMatchFast(ms, ip, iLimit); ok {
		return bestLength, offBase // the same, in assembly (set up by compressBlockLazyGeneric)
	}
	updateDUBT(ms, ip, mls)
	return dubtFindBestMatch(ms, ip, iLimit, mls)
}

// searchMethod selects the match finder of the lazy parser.
// C: zstd_lazy.c:searchMethod_e
type searchMethod uint8

const (
	searchHashChain  searchMethod = 0 // C: search_hashChain
	searchBinaryTree searchMethod = 1 // C: search_binaryTree
	searchRowHash    searchMethod = 2 // C: search_rowHash
)

// insertAndFindFirstIndexInternal inserts the positions from ms.nextToUpdate
// up to ip (excluded) into the hash chain and returns the most recent index
// with the same hash as ip. In lazy skipping mode only one position is
// inserted. C: zstd_lazy.c:ZSTD_insertAndFindFirstIndex_internal
func insertAndFindFirstIndexInternal(ms *matchState, ip int, mls uint32, lazySkipping bool) uint32 {
	hashTable := ms.hashTable
	hashLog := ms.cParams.hashLog
	chainTable := ms.chainTable
	chainMask := uint32(1)<<ms.cParams.chainLog - 1
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	target := uint32(ip) + baseIdx
	idx := ms.nextToUpdate

	for idx < target { // catch up
		h := hashPtr(b, int(idx-baseIdx), hashLog, mls)
		chainTable[idx&chainMask] = hashTable[h] // C: NEXT_IN_CHAIN(idx, chainMask)
		hashTable[h] = idx
		idx++
		// Stop inserting every position when in the lazy skipping mode.
		if lazySkipping {
			break
		}
	}

	ms.nextToUpdate = target
	return hashTable[hashPtr(b, ip, hashLog, mls)]
}

// hcFindBestMatch returns the length of the longest match at position ip
// (in ms.window.base) found by walking its hash chain, and its offBase; a
// length below 4 means none (offBase is then offBaseNotFound). iLimit is the
// end of the input. It also inserts the positions before ip into the chain.
// C: zstd_lazy.c:ZSTD_HcFindBestMatch with dictMode ZSTD_noDict.
func hcFindBestMatch(ms *matchState, ip, iLimit int, mls uint32) (int, uint32) {
	cParams := &ms.cParams
	chainTable := ms.chainTable
	chainSize := uint32(1) << cParams.chainLog
	chainMask := chainSize - 1
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	curr := uint32(ip) + baseIdx
	maxDistance := uint32(1) << cParams.windowLog
	lowestValid := ms.window.lowLimit
	withinMaxDistance := lowestValid
	if curr-lowestValid > maxDistance {
		withinMaxDistance = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	lowLimit := withinMaxDistance
	if isDictionary {
		lowLimit = lowestValid
	}
	var minChain uint32
	if curr > chainSize {
		minChain = curr - chainSize
	}
	nbAttempts := uint32(1) << cParams.searchLog
	ml := 4 - 1
	offBase := uint32(offBaseNotFound)

	// HC4 match finder
	matchIndex := insertAndFindFirstIndexInternal(ms, ip, mls, ms.lazySkipping)

	// C: `&` of the two conditions; && is equivalent (no side effects).
	ipVal := le32(b, ip+ml-3) // C reads it every attempt; it changes only with ml
	for ; matchIndex >= lowLimit && nbAttempts > 0; nbAttempts-- {
		currentMl := 0
		match := int(matchIndex - baseIdx)
		// read 4B starting from (match + ml + 1 - sizeof(U32))
		if le32(b, match+ml-3) == ipVal { // potentially better
			currentMl = count(b, ip, match, iLimit)
		}

		// save best solution
		if currentMl > ml {
			ml = currentMl
			offBase = offsetToOffBase(curr - matchIndex)
			if ip+currentMl == iLimit {
				break // best possible, avoids read overflow on next attempt
			}
			ipVal = le32(b, ip+ml-3)
		}

		if matchIndex <= minChain {
			break
		}
		matchIndex = chainTable[matchIndex&chainMask] // C: NEXT_IN_CHAIN(matchIndex, chainMask)
	}
	return ml, offBase
}

// searchMax runs the match finder method at ip.
// C: zstd_lazy.c:ZSTD_searchMax — C picks a search function per (dictMode,
// mls, rowLog) through a table of specialised copies; this port passes mls
// and rowLog as arguments.
func searchMax(ms *matchState, ip, iLimit int, mls, rowLog uint32, method searchMethod) (int, uint32) {
	if method == searchRowHash {
		return rowFindBestMatch(ms, ip, iLimit, mls, rowLog)
	}
	if method == searchBinaryTree {
		return btFindBestMatch(ms, ip, iLimit, mls)
	}
	return hcFindBestMatch(ms, ip, iLimit, mls)
}

// compressBlockGreedy. C: zstd_lazy.c:ZSTD_compressBlock_greedy
func compressBlockGreedy(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 0)
}

// compressBlockLazy. C: zstd_lazy.c:ZSTD_compressBlock_lazy
func compressBlockLazy(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 1)
}

// compressBlockLazy2. C: zstd_lazy.c:ZSTD_compressBlock_lazy2
func compressBlockLazy2(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 2)
}

// compressBlockGreedyRow. C: zstd_lazy.c:ZSTD_compressBlock_greedy_row
func compressBlockGreedyRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 0)
}

// compressBlockLazyRow. C: zstd_lazy.c:ZSTD_compressBlock_lazy_row
func compressBlockLazyRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 1)
}

// compressBlockLazy2Row. C: zstd_lazy.c:ZSTD_compressBlock_lazy2_row
func compressBlockLazy2Row(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 2)
}

// compressBlockBtlazy2. C: zstd_lazy.c:ZSTD_compressBlock_btlazy2
func compressBlockBtlazy2(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyGeneric(ms, ss, rep, src, srcIdx, searchBinaryTree, 2)
}

// compressBlockLazyGeneric is the lazy parser: at each position it takes
// the repcode or best match, then (depth 1 and 2) looks 1 (and 2) positions
// ahead for a better one.
// C: zstd_lazy.c:ZSTD_compressBlock_lazy_generic with dictMode ZSTD_noDict;
// C's `goto _storeSequence` is the store flag.
func compressBlockLazyGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, method searchMethod, depth int) int {
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	istart := int(srcIdx - baseIdx)
	ip := istart
	anchor := istart
	iend := istart + len(src)
	ilimit := iend - 8
	if method == searchRowHash {
		ilimit -= rowHashCacheSize
	}
	prefixLowestIndex := ms.window.dictLimit
	prefixLowest := int(prefixLowestIndex - baseIdx)
	mls := min(max(ms.cParams.minMatch, 4), 6)
	rowLog := min(max(ms.cParams.searchLog, 4), 6)

	offset1, offset2 := rep[0], rep[1]
	var offsetSaved1, offsetSaved2 uint32

	read32 := func(p int) uint32 { return binary.LittleEndian.Uint32(b[p:]) }

	dictAndPrefixLength := ip - prefixLowest
	if dictAndPrefixLength == 0 {
		ip++
	}
	{
		curr := uint32(ip) + baseIdx
		windowLow := getLowestPrefixIndex(ms, curr, ms.cParams.windowLog)
		maxRep := curr - windowLow
		if offset2 > maxRep {
			offsetSaved2, offset2 = offset2, 0
		}
		if offset1 > maxRep {
			offsetSaved1, offset1 = offset1, 0
		}
	}

	// Reset the lazy skipping state
	ms.lazySkipping = false

	if method == searchRowHash {
		rowFillHashCache(ms, b, baseIdx, mls, ms.nextToUpdate, ilimit)
		rowAsmSetup(ms, mls, rowLog, iend)
		// Do not keep the window and tables reachable through ms.rowAsm
		// once the block is done.
		defer func() { ms.rowAsm = rowAsmParams{} }()
		if ms.rowAsm.ok {
			// The whole match loop in assembly; the Go loop below then
			// has nothing left to do (ip >= ilimit).
			ip, anchor, offset1, offset2, _ = lazyRowRun(ms, ss, ip, anchor, iend, ilimit, prefixLowest, offset1, offset2, depth, mls, rowLog)
		}
	} else if method == searchBinaryTree {
		btAsmSetup(ms, nil, mls, iend)
		defer func() { ms.btAsm, ms.rowAsm = btAsmParams{}, rowAsmParams{} }()
		if ms.btAsm.ok {
			// As above, with the binary-tree finder.
			ip, anchor, offset1, offset2, _ = lazyBtRun(ms, ss, ip, anchor, iend, ilimit, prefixLowest, offset1, offset2, depth)
		}
	} else if method == searchHashChain {
		hcAsmSetup(ms, iend)
		defer func() { ms.rowAsm = rowAsmParams{} }()
		if ms.rowAsm.ok {
			// As above, with the hash-chain finder.
			ip, anchor, offset1, offset2, _ = lazyHcRun(ms, ss, ip, anchor, iend, ilimit, prefixLowest, offset1, offset2, depth, mls)
		}
	}

	// Match Loop
	for ip < ilimit {
		matchLength := 0
		offBase := uint32(repcode1ToOffBase)
		start := ip + 1
		store := false // C: goto _storeSequence

		// check repCode
		// C: zstd_lazy.c:ZSTD_compressBlock_lazy_generic — C combines the
		// conditions with `&` (both operands always evaluated); `&&` is the
		// same result, as the read is in bounds even for offset1 == 0.
		if offset1 > 0 && read32(ip+1-int(offset1)) == read32(ip+1) {
			matchLength = count(b, ip+1+4, ip+1+4-int(offset1), iend) + 4
			store = depth == 0
		}

		if !store {
			// first search (depth 0)
			if ml2, offbaseFound := searchMax(ms, ip, iend, mls, rowLog, method); ml2 > matchLength {
				matchLength, start, offBase = ml2, ip, offbaseFound
			}

			if matchLength < 4 {
				step := (ip-anchor)>>kSearchStrength + 1 // jump faster over incompressible sections
				ip += step
				// Enter the lazy skipping mode once we are skipping more
				// than 8 bytes at a time. In this mode we stop inserting
				// every position into our tables, and only insert positions
				// that we search, which is one in step positions. The exact
				// cutoff is flexible, I've just chosen a number that is
				// reasonably high, so we minimize the compression ratio loss
				// in "normal" scenarios. This mode gets triggered once we've
				// gone 2KB without finding any matches.
				ms.lazySkipping = step > kLazySkippingStep
				continue
			}

			// let's try to find a better solution
			if depth >= 1 {
				for ip < ilimit {
					ip++
					// C: zstd_lazy.c:ZSTD_compressBlock_lazy_generic — `&` in C, `&&` here (same result).
					if offBase != 0 && offset1 > 0 && read32(ip) == read32(ip-int(offset1)) {
						mlRep := count(b, ip+4, ip+4-int(offset1), iend) + 4
						gain2 := mlRep * 3
						gain1 := matchLength*3 - int(highbit32(offBase)) + 1
						if mlRep >= 4 && gain2 > gain1 {
							matchLength, offBase, start = mlRep, repcode1ToOffBase, ip
						}
					}
					{
						ml2, ofbCandidate := searchMax(ms, ip, iend, mls, rowLog, method)
						gain2 := ml2*4 - int(highbit32(ofbCandidate)) // raw approx
						gain1 := matchLength*4 - int(highbit32(offBase)) + 4
						if ml2 >= 4 && gain2 > gain1 {
							matchLength, offBase, start = ml2, ofbCandidate, ip
							continue // search a better one
						}
					}

					// let's find an even better one
					if depth == 2 && ip < ilimit {
						ip++
						// C: zstd_lazy.c:ZSTD_compressBlock_lazy_generic — `&` in C, `&&` here (same result).
						if offBase != 0 && offset1 > 0 && read32(ip) == read32(ip-int(offset1)) {
							mlRep := count(b, ip+4, ip+4-int(offset1), iend) + 4
							gain2 := mlRep * 4
							gain1 := matchLength*4 - int(highbit32(offBase)) + 1
							if mlRep >= 4 && gain2 > gain1 {
								matchLength, offBase, start = mlRep, repcode1ToOffBase, ip
							}
						}
						{
							ml2, ofbCandidate := searchMax(ms, ip, iend, mls, rowLog, method)
							gain2 := ml2*4 - int(highbit32(ofbCandidate)) // raw approx
							gain1 := matchLength*4 - int(highbit32(offBase)) + 7
							if ml2 >= 4 && gain2 > gain1 {
								matchLength, offBase, start = ml2, ofbCandidate, ip
								continue
							}
						}
					}
					break // nothing found : store previous solution
				}
			}

			// catch up
			if offBase > repNum { // C: OFFBASE_IS_OFFSET
				off := int(offBase - repNum)
				for start > anchor && start-off > prefixLowest && b[start-1] == b[start-off-1] { // only search for offset within prefix
					start--
					matchLength++
				}
				offset2 = offset1
				offset1 = offBase - repNum
			}
		}

		// C: _storeSequence
		storeSeq(ss, start-anchor, b[anchor:], offBase, matchLength)
		ip = start + matchLength
		anchor = ip
		if ms.lazySkipping {
			// We've found a match, disable lazy skipping mode, and refill
			// the hash cache.
			if method == searchRowHash {
				rowFillHashCache(ms, b, baseIdx, mls, ms.nextToUpdate, ilimit)
			}
			ms.lazySkipping = false
		}

		// check immediate repcode
		// C: zstd_lazy.c:ZSTD_compressBlock_lazy_generic — `&` in C, `&&` here (same result).
		for ip <= ilimit && offset2 > 0 && read32(ip) == read32(ip-int(offset2)) {
			// store sequence
			matchLength = count(b, ip+4, ip+4-int(offset2), iend) + 4
			offset2, offset1 = offset1, offset2 // swap repcodes
			storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, matchLength)
			ip += matchLength
			anchor = ip
		}
	}

	// If offset1 started invalid (offsetSaved1 != 0) and became valid
	// (offset1 != 0), rotate saved offsets. See the comment in
	// compressBlockFastNoDictGeneric for more context.
	if offsetSaved1 != 0 && offset1 != 0 {
		offsetSaved2 = offsetSaved1
	}

	// save reps for next block
	rep[0] = offset1
	if offset1 == 0 {
		rep[0] = offsetSaved1
	}
	rep[1] = offset2
	if offset2 == 0 {
		rep[1] = offsetSaved2
	}

	// Return the last literals size
	return iend - anchor
}

/*-*******************************
*  extDict variants
*********************************/

// The extDict variants below follow C's dictMode == ZSTD_extDict branches.
// Candidates below window.dictLimit lie in the extDict segment
// (window.dictBase: C's dictBase + idx is d[idx-dictBaseIdx]); the rest in
// the prefix (window.base: C's base + idx is b[idx-baseIdx]). Where C
// forms base + idx for an idx below dictLimit (a pointer before the prefix,
// only used at offsets that reach into it), positions are taken at the
// offset index directly (idx + k - baseIdx).

// hcFindBestMatchExtDict. C: zstd_lazy.c:ZSTD_HcFindBestMatch with dictMode
// ZSTD_extDict; see hcFindBestMatch.
func hcFindBestMatchExtDict(ms *matchState, ip, iLimit int, mls uint32) (int, uint32) {
	cParams := &ms.cParams
	chainTable := ms.chainTable
	chainSize := uint32(1) << cParams.chainLog
	chainMask := chainSize - 1
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	dictLimit := w.dictLimit
	prefixStart := int(dictLimit - baseIdx)
	dictEnd := int(dictLimit - dictBaseIdx)
	curr := uint32(ip) + baseIdx
	maxDistance := uint32(1) << cParams.windowLog
	lowestValid := w.lowLimit
	withinMaxDistance := lowestValid
	if curr-lowestValid > maxDistance {
		withinMaxDistance = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	lowLimit := withinMaxDistance
	if isDictionary {
		lowLimit = lowestValid
	}
	var minChain uint32
	if curr > chainSize {
		minChain = curr - chainSize
	}
	nbAttempts := uint32(1) << cParams.searchLog
	ml := 4 - 1
	offBase := uint32(offBaseNotFound)

	// HC4 match finder
	matchIndex := insertAndFindFirstIndexInternal(ms, ip, mls, ms.lazySkipping)

	// C: `&` of the two conditions; && is equivalent (no side effects).
	for ; matchIndex >= lowLimit && nbAttempts > 0; nbAttempts-- {
		currentMl := 0
		if matchIndex >= dictLimit {
			match := int(matchIndex - baseIdx)
			// read 4B starting from (match + ml + 1 - sizeof(U32))
			if binary.LittleEndian.Uint32(b[match+ml-3:]) == binary.LittleEndian.Uint32(b[ip+ml-3:]) { // potentially better
				currentMl = count(b, ip, match, iLimit)
			}
		} else {
			match := int(matchIndex - dictBaseIdx)
			if binary.LittleEndian.Uint32(d[match:]) == binary.LittleEndian.Uint32(b[ip:]) { // assumption : matchIndex <= dictLimit-4 (by table construction)
				currentMl = count2Segments(b, ip+4, d, match+4, iLimit, dictEnd, prefixStart) + 4
			}
		}

		// save best solution
		if currentMl > ml {
			ml = currentMl
			offBase = offsetToOffBase(curr - matchIndex)
			if ip+currentMl == iLimit {
				break // best possible, avoids read overflow on next attempt
			}
		}

		if matchIndex <= minChain {
			break
		}
		matchIndex = chainTable[matchIndex&chainMask] // C: NEXT_IN_CHAIN(matchIndex, chainMask)
	}
	return ml, offBase
}

// rowFindBestMatchExtDict. C: zstd_lazy.c:ZSTD_RowFindBestMatch with
// dictMode ZSTD_extDict; see rowFindBestMatch (C's PREFETCH_L1 of each
// match, from base or dictBase, is a cache hint only and is omitted).
func rowFindBestMatchExtDict(ms *matchState, ip, iLimit int, mls, rowLog uint32) (int, uint32) {
	hashTable := ms.hashTable
	tagTable := ms.tagTable
	hashLog := ms.rowHashLog
	cParams := &ms.cParams
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	dictLimit := w.dictLimit
	prefixStart := int(dictLimit - baseIdx)
	dictEnd := int(dictLimit - dictBaseIdx)
	curr := uint32(ip) + baseIdx
	maxDistance := uint32(1) << cParams.windowLog
	lowestValid := w.lowLimit
	withinMaxDistance := lowestValid
	if curr-lowestValid > maxDistance {
		withinMaxDistance = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	lowLimit := withinMaxDistance
	if isDictionary {
		lowLimit = lowestValid
	}
	rowEntries := uint32(1) << rowLog
	rowMask := rowEntries - 1
	cappedSearchLog := min(cParams.searchLog, rowLog) // nb of searches is capped at nb entries per row
	hashSalt := ms.hashSalt
	nbAttempts := uint32(1) << cappedSearchLog
	ml := 4 - 1
	offBase := uint32(offBaseNotFound)
	var hash uint32

	// Update the hashTable and tagTable up to (but not including) ip
	if !ms.lazySkipping {
		rowUpdateInternal(ms, ip, mls, rowLog, rowMask, true)
		hash = rowNextCachedHash(&ms.hashCache, b, baseIdx, curr, hashLog, mls, hashSalt)
	} else {
		// Stop inserting every position when in the lazy skipping mode.
		// The hash cache is also not kept up to date in this mode.
		hash = hashPtrSalted(b, ip, hashLog+rowHashTagBits, mls, hashSalt)
		ms.nextToUpdate = curr
	}
	ms.hashSaltEntropy += hash // collect salt entropy

	// Get the hash for ip, compute the appropriate row
	relRow := (hash >> rowHashTagBits) << rowLog
	tag := hash & rowHashTagMask
	row := hashTable[relRow : relRow+rowEntries]
	tagRow := tagTable[relRow : relRow+rowEntries]
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
		ms.nextToUpdate++
	}

	// Return the longest match
	for _, matchIndex := range matchBuffer[:numMatches] {
		currentMl := 0
		if matchIndex >= dictLimit {
			match := int(matchIndex - baseIdx)
			// read 4B starting from (match + ml + 1 - sizeof(U32))
			if binary.LittleEndian.Uint32(b[match+ml-3:]) == binary.LittleEndian.Uint32(b[ip+ml-3:]) { // potentially better
				currentMl = count(b, ip, match, iLimit)
			}
		} else {
			match := int(matchIndex - dictBaseIdx)
			if binary.LittleEndian.Uint32(d[match:]) == binary.LittleEndian.Uint32(b[ip:]) { // assumption : matchIndex <= dictLimit-4 (by table construction)
				currentMl = count2Segments(b, ip+4, d, match+4, iLimit, dictEnd, prefixStart) + 4
			}
		}
		// Save best solution
		if currentMl > ml {
			ml = currentMl
			offBase = offsetToOffBase(curr - matchIndex)
			if ip+currentMl == iLimit {
				break // best possible, avoids read overflow on next attempt
			}
		}
	}
	return ml, offBase
}

// insertDUBT1ExtDictGeneric. C: zstd_lazy.c:ZSTD_insertDUBT1 with dictMode
// ZSTD_extDict; see insertDUBT1. curr (an unsorted candidate) may lie in
// the extDict: it is then compared within the extDict only.
// insertDUBT1ExtDict (zstd_lazy_dubt_*.go) runs it, or the same loop in
// assembly.
func insertDUBT1ExtDictGeneric(ms *matchState, curr uint32, inputEnd int, nbCompares, btLow uint32) {
	bt := ms.chainTable
	btLog := ms.cParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1
	var commonLengthSmaller, commonLengthLarger int
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	dictLimit := w.dictLimit
	ipSeg, ip, iend := b, int(curr-baseIdx), inputEnd
	if curr < dictLimit {
		ipSeg, ip, iend = d, int(curr-dictBaseIdx), int(dictLimit-dictBaseIdx)
	}
	dictEnd := int(dictLimit - dictBaseIdx)
	prefixStart := int(dictLimit - baseIdx)
	smallerPtr := &bt[2*(curr&btMask)]
	largerPtr := &bt[2*(curr&btMask)+1]
	// this candidate is unsorted : next sorted candidate is reached through
	// *smallerPtr, while *largerPtr contains previous unsorted candidate
	// (which is already saved and can be overwritten)
	matchIndex := *smallerPtr
	var dummy32 uint32 // to be nullified at the end
	windowValid := w.lowLimit
	maxDistance := uint32(1) << ms.cParams.windowLog
	windowLow := windowValid
	if curr-windowValid > maxDistance {
		windowLow = curr - maxDistance
	}

	for ; nbCompares > 0 && matchIndex > windowLow; nbCompares-- {
		nextPtr := bt[2*(matchIndex&btMask):][:2]
		matchLength := min(commonLengthSmaller, commonLengthLarger) // guaranteed minimum nb of common bytes
		// note : all candidates are now supposed sorted, but it's still
		// possible to have nextPtr[1] == dubtUnsortedMark when a real index
		// has the same value as dubtUnsortedMark

		switch {
		case matchIndex+uint32(matchLength) >= dictLimit: // both in current segment
			matchLength += count(b, ip+matchLength, int(matchIndex+uint32(matchLength)-baseIdx), iend)
		case curr < dictLimit: // both in extDict
			matchLength += count(d, ip+matchLength, int(matchIndex-dictBaseIdx)+matchLength, iend)
		default:
			matchLength += count2Segments(b, ip+matchLength, d, int(matchIndex-dictBaseIdx)+matchLength, iend, dictEnd, prefixStart)
		}

		if ip+matchLength == iend { // equal : no way to know if inf or sup
			break // drop , to guarantee consistency ; miss a bit of compression, but other solutions can corrupt tree
		}

		// C: match[matchLength], with match moved to base once the match
		// reaches the prefix.
		var mByte byte
		if mi := matchIndex + uint32(matchLength); mi >= dictLimit {
			mByte = b[mi-baseIdx]
		} else {
			mByte = d[mi-dictBaseIdx]
		}
		if mByte < ipSeg[ip+matchLength] { // necessarily within buffer
			// match is smaller than current
			*smallerPtr = matchIndex          // update smaller idx
			commonLengthSmaller = matchLength // all smaller will now have at least this guaranteed common length
			if matchIndex <= btLow {          // beyond tree size, stop searching
				smallerPtr = &dummy32
				break
			}
			smallerPtr = &nextPtr[1] // new "candidate" => larger than match, which was smaller than target
			matchIndex = nextPtr[1]  // new matchIndex, larger than previous and closer to current
		} else {
			// match is larger than current
			*largerPtr = matchIndex
			commonLengthLarger = matchLength
			if matchIndex <= btLow { // beyond tree size, stop searching
				largerPtr = &dummy32
				break
			}
			largerPtr = &nextPtr[0]
			matchIndex = nextPtr[0]
		}
	}

	*largerPtr = 0
	*smallerPtr = 0
}

// dubtFindBestMatchExtDict. C: zstd_lazy.c:ZSTD_DUBT_findBestMatch with
// dictMode ZSTD_extDict; see dubtFindBestMatch.
func dubtFindBestMatchExtDict(ms *matchState, ip, iend int, mls uint32) (int, uint32) {
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hashLog := cParams.hashLog
	w := &ms.window
	b := w.base
	h := hashPtr(b, ip, hashLog, mls)
	matchIndex := hashTable[h]

	baseIdx := w.baseIdx
	curr := uint32(ip) + baseIdx
	windowLow := getLowestMatchIndex(ms, curr, cParams.windowLog)

	bt := ms.chainTable
	btLog := cParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1
	var btLow uint32
	if btMask < curr {
		btLow = curr - btMask
	}
	unsortLimit := max(btLow, windowLow)

	nextCandidate := 2 * (matchIndex & btMask) // C: nextCandidate; unsortedMark is the next entry
	nbCompares := uint32(1) << cParams.searchLog
	nbCandidates := nbCompares
	var previousCandidate uint32

	// reach end of unsorted candidates list
	for matchIndex > unsortLimit && bt[nextCandidate+1] == dubtUnsortedMark && nbCandidates > 1 {
		bt[nextCandidate+1] = previousCandidate // the unsortedMark becomes a reversed chain, to move up back to original position
		previousCandidate = matchIndex
		matchIndex = bt[nextCandidate]
		nextCandidate = 2 * (matchIndex & btMask)
		nbCandidates--
	}

	// nullify last candidate if it's still unsorted
	// simplification, detrimental to compression ratio, beneficial for speed
	if matchIndex > unsortLimit && bt[nextCandidate+1] == dubtUnsortedMark {
		bt[nextCandidate] = 0
		bt[nextCandidate+1] = 0
	}

	// batch sort stacked candidates
	matchIndex = previousCandidate
	for matchIndex != 0 { // will end on matchIndex == 0
		nextCandidateIdx := bt[2*(matchIndex&btMask)+1]
		insertDUBT1ExtDict(ms, matchIndex, iend, nbCandidates, unsortLimit)
		matchIndex = nextCandidateIdx
		nbCandidates++
	}

	// find longest match
	matchIndex = hashTable[h]
	hashTable[h] = curr // Update Hash Table
	bestLength, offBase, matchEndIdx := dubtSearchExtDict(ms, ip, iend, curr, matchIndex, nbCompares, btLow, windowLow)

	ms.nextToUpdate = matchEndIdx - 8 // skip repetitive patterns
	return bestLength, offBase
}

// dubtSearchExtDictGeneric is dubtSearchGeneric for a window with an
// extDict (C: zstd_lazy.c:ZSTD_DUBT_findBestMatch, "find longest match",
// dictMode ZSTD_extDict). dubtSearchExtDict (zstd_lazy_dubt_*.go) runs it,
// or the same loop in assembly.
func dubtSearchExtDictGeneric(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, uint32) {
	bt := ms.chainTable
	btMask := uint32(1)<<(ms.cParams.chainLog-1) - 1
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	dictLimit := w.dictLimit
	dictEnd := int(dictLimit - dictBaseIdx)
	prefixStart := int(dictLimit - baseIdx)
	offBase := uint32(offBaseNotFound)
	var commonLengthSmaller, commonLengthLarger int
	smallerPtr := &bt[2*(curr&btMask)]
	largerPtr := &bt[2*(curr&btMask)+1]
	matchEndIdx := curr + 8 + 1
	var dummy32 uint32 // to be nullified at the end
	bestLength := 0

	for ; nbCompares > 0 && matchIndex > windowLow; nbCompares-- {
		nextPtr := bt[2*(matchIndex&btMask):][:2]
		matchLength := min(commonLengthSmaller, commonLengthLarger) // guaranteed minimum nb of common bytes

		if matchIndex+uint32(matchLength) >= dictLimit {
			matchLength += count(b, ip+matchLength, int(matchIndex+uint32(matchLength)-baseIdx), iend)
		} else {
			matchLength += count2Segments(b, ip+matchLength, d, int(matchIndex-dictBaseIdx)+matchLength, iend, dictEnd, prefixStart)
		}

		if matchLength > bestLength {
			if uint32(matchLength) > matchEndIdx-matchIndex {
				matchEndIdx = matchIndex + uint32(matchLength)
			}
			if 4*(matchLength-bestLength) > int(highbit32(curr-matchIndex+1))-int(highbit32(offBase)) {
				bestLength, offBase = matchLength, offsetToOffBase(curr-matchIndex)
			}
			if ip+matchLength == iend { // equal : no way to know if inf or sup
				break // drop, to guarantee consistency (miss a little bit of compression)
			}
		}

		// C: match[matchLength], with match moved to base once the match
		// reaches the prefix.
		var mByte byte
		if mi := matchIndex + uint32(matchLength); mi >= dictLimit {
			mByte = b[mi-baseIdx]
		} else {
			mByte = d[mi-dictBaseIdx]
		}
		if mByte < b[ip+matchLength] {
			// match is smaller than current
			*smallerPtr = matchIndex          // update smaller idx
			commonLengthSmaller = matchLength // all smaller will now have at least this guaranteed common length
			if matchIndex <= btLow {          // beyond tree size, stop the search
				smallerPtr = &dummy32
				break
			}
			smallerPtr = &nextPtr[1] // new "smaller" => larger of match
			matchIndex = nextPtr[1]  // new matchIndex larger than previous (closer to current)
		} else {
			// match is larger than current
			*largerPtr = matchIndex
			commonLengthLarger = matchLength
			if matchIndex <= btLow { // beyond tree size, stop the search
				largerPtr = &dummy32
				break
			}
			largerPtr = &nextPtr[0]
			matchIndex = nextPtr[0]
		}
	}

	*largerPtr = 0
	*smallerPtr = 0

	return bestLength, offBase, matchEndIdx
}

// btFindBestMatchExtDict. C: zstd_lazy.c:ZSTD_BtFindBestMatch with dictMode
// ZSTD_extDict.
func btFindBestMatchExtDict(ms *matchState, ip, iLimit int, mls uint32) (int, uint32) {
	if uint32(ip)+ms.window.baseIdx < ms.nextToUpdate {
		return 0, offBaseNotFound // skipped area
	}
	updateDUBT(ms, ip, mls)
	return dubtFindBestMatchExtDict(ms, ip, iLimit, mls)
}

// searchMaxExtDict. C: zstd_lazy.c:ZSTD_searchMax with dictMode ZSTD_extDict.
func searchMaxExtDict(ms *matchState, ip, iLimit int, mls, rowLog uint32, method searchMethod) (int, uint32) {
	if method == searchRowHash {
		return rowFindBestMatchExtDict(ms, ip, iLimit, mls, rowLog)
	}
	if method == searchBinaryTree {
		return btFindBestMatchExtDict(ms, ip, iLimit, mls)
	}
	return hcFindBestMatchExtDict(ms, ip, iLimit, mls)
}

// compressBlockGreedyExtDict. C: zstd_lazy.c:ZSTD_compressBlock_greedy_extDict
func compressBlockGreedyExtDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 0)
}

// compressBlockLazyExtDict. C: zstd_lazy.c:ZSTD_compressBlock_lazy_extDict
func compressBlockLazyExtDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 1)
}

// compressBlockLazy2ExtDict. C: zstd_lazy.c:ZSTD_compressBlock_lazy2_extDict
func compressBlockLazy2ExtDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 2)
}

// compressBlockGreedyExtDictRow. C: zstd_lazy.c:ZSTD_compressBlock_greedy_extDict_row
func compressBlockGreedyExtDictRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 0)
}

// compressBlockLazyExtDictRow. C: zstd_lazy.c:ZSTD_compressBlock_lazy_extDict_row
func compressBlockLazyExtDictRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 1)
}

// compressBlockLazy2ExtDictRow. C: zstd_lazy.c:ZSTD_compressBlock_lazy2_extDict_row
func compressBlockLazy2ExtDictRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 2)
}

// compressBlockBtlazy2ExtDict. C: zstd_lazy.c:ZSTD_compressBlock_btlazy2_extDict
func compressBlockBtlazy2ExtDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyExtDictGeneric(ms, ss, rep, src, srcIdx, searchBinaryTree, 2)
}

// lazyExtDictNames are the extDictHook names, by search method and depth.
var lazyExtDictNames = [3][3]string{
	searchHashChain:  {"greedy-hc", "lazy-hc", "lazy2-hc"},
	searchBinaryTree: {"", "", "btlazy2"},
	searchRowHash:    {"greedy-row", "lazy-row", "lazy2-row"},
}

// compressBlockLazyExtDictGeneric is the lazy parser for a window with an
// extDict segment; see compressBlockLazyGeneric.
// C: zstd_lazy.c:ZSTD_compressBlock_lazy_extDict_generic; C's `goto
// _storeSequence` is the store flag, its repcode checks are repMatchLength.
func compressBlockLazyExtDictGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, method searchMethod, depth int) int {
	if extDictHook != nil {
		extDictHook(lazyExtDictNames[method][depth])
	}
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	istart := int(srcIdx - baseIdx)
	ip := istart
	anchor := istart
	iend := istart + len(src)
	ilimit := iend - 8
	if method == searchRowHash {
		ilimit -= rowHashCacheSize
	}
	dictLimit := w.dictLimit
	prefixStart := int(dictLimit - baseIdx)
	dictEnd := int(dictLimit - dictBaseIdx)
	dictStart := int(w.lowLimit - dictBaseIdx)
	windowLog := ms.cParams.windowLog
	mls := min(max(ms.cParams.minMatch, 4), 6)
	rowLog := min(max(ms.cParams.searchLog, 4), 6)

	offset1, offset2 := rep[0], rep[1]

	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	// seg returns the segment holding index i and i's position in it.
	seg := func(i uint32) ([]byte, int) {
		if i < dictLimit {
			return d, int(i - dictBaseIdx)
		}
		return b, int(i - baseIdx)
	}
	// repMatchLength returns the length of the repcode match at position p
	// (index cur) with offset off, or 0 if there is none (C: the "check
	// repCode" blocks: `&` of the validity conditions in C, `&&` here, the
	// same result).
	repMatchLength := func(p int, cur, off uint32) int {
		windowLow := getLowestMatchIndex(ms, cur, windowLog)
		repIndex := cur - off
		if indexOverlapCheck(dictLimit, repIndex) && off <= cur-windowLow { // equivalent to `curr > repIndex >= windowLow`
			repBase, rp := seg(repIndex)
			if binary.LittleEndian.Uint32(b[p:]) == binary.LittleEndian.Uint32(repBase[rp:]) {
				repEnd := iend
				if repIndex < dictLimit {
					repEnd = dictEnd
				}
				return count2Segments(b, p+4, repBase, rp+4, iend, repEnd, prefixStart) + 4
			}
		}
		return 0
	}

	// Reset the lazy skipping state
	ms.lazySkipping = false

	// init
	if ip == prefixStart {
		ip++
	}
	if method == searchRowHash {
		rowFillHashCache(ms, b, baseIdx, mls, ms.nextToUpdate, ilimit)
		// The whole match loop in assembly when it can run; the Go loop
		// below then has nothing left to do (ip >= ilimit).
		ip, anchor, offset1, offset2, _ = lazyRowExtRun(ms, ss, ip, anchor, iend, ilimit, offset1, offset2, depth, mls, rowLog)
	}

	// Match Loop
	for ip < ilimit {
		matchLength := 0
		offBase := uint32(repcode1ToOffBase)
		start := ip + 1
		curr := idx(ip)
		store := false // C: goto _storeSequence

		// check repCode
		if rl := repMatchLength(ip+1, curr+1, offset1); rl > 0 { // note: we are searching at curr+1
			// repcode detected we should take it
			matchLength = rl
			store = depth == 0
		}

		if !store {
			// first search (depth 0)
			if ml2, ofbCandidate := searchMaxExtDict(ms, ip, iend, mls, rowLog, method); ml2 > matchLength {
				matchLength, start, offBase = ml2, ip, ofbCandidate
			}

			if matchLength < 4 {
				step := (ip - anchor) >> kSearchStrength
				ip += step + 1 // jump faster over incompressible sections
				// Enter the lazy skipping mode once we are skipping more
				// than 8 bytes at a time. In this mode we stop inserting
				// every position into our tables, and only insert positions
				// that we search, which is one in step positions. The exact
				// cutoff is flexible, I've just chosen a number that is
				// reasonably high, so we minimize the compression ratio loss
				// in "normal" scenarios. This mode gets triggered once we've
				// gone 2KB without finding any matches.
				ms.lazySkipping = step > kLazySkippingStep
				continue
			}

			// let's try to find a better solution
			if depth >= 1 {
				for ip < ilimit {
					ip++
					curr++
					// check repCode
					if offBase != 0 {
						if repLength := repMatchLength(ip, curr, offset1); repLength > 0 {
							// repcode detected
							gain2 := repLength * 3
							gain1 := matchLength*3 - int(highbit32(offBase)) + 1
							if repLength >= 4 && gain2 > gain1 {
								matchLength, offBase, start = repLength, repcode1ToOffBase, ip
							}
						}
					}

					// search match, depth 1
					{
						ml2, ofbCandidate := searchMaxExtDict(ms, ip, iend, mls, rowLog, method)
						gain2 := ml2*4 - int(highbit32(ofbCandidate)) // raw approx
						gain1 := matchLength*4 - int(highbit32(offBase)) + 4
						if ml2 >= 4 && gain2 > gain1 {
							matchLength, offBase, start = ml2, ofbCandidate, ip
							continue // search a better one
						}
					}

					// let's find an even better one
					if depth == 2 && ip < ilimit {
						ip++
						curr++
						// check repCode
						if offBase != 0 {
							if repLength := repMatchLength(ip, curr, offset1); repLength > 0 {
								// repcode detected
								gain2 := repLength * 4
								gain1 := matchLength*4 - int(highbit32(offBase)) + 1
								if repLength >= 4 && gain2 > gain1 {
									matchLength, offBase, start = repLength, repcode1ToOffBase, ip
								}
							}
						}

						// search match, depth 2
						{
							ml2, ofbCandidate := searchMaxExtDict(ms, ip, iend, mls, rowLog, method)
							gain2 := ml2*4 - int(highbit32(ofbCandidate)) // raw approx
							gain1 := matchLength*4 - int(highbit32(offBase)) + 7
							if ml2 >= 4 && gain2 > gain1 {
								matchLength, offBase, start = ml2, ofbCandidate, ip
								continue
							}
						}
					}
					break // nothing found : store previous solution
				}
			}

			// catch up
			if offBase > repNum { // C: OFFBASE_IS_OFFSET
				matchIndex := idx(start) - (offBase - repNum)
				match, mp := seg(matchIndex)
				mStart := prefixStart
				if matchIndex < dictLimit {
					mStart = dictStart
				}
				for start > anchor && mp > mStart && b[start-1] == match[mp-1] { // catch up
					start--
					mp--
					matchLength++
				}
				offset2 = offset1
				offset1 = offBase - repNum
			}
		}

		// C: _storeSequence
		storeSeq(ss, start-anchor, b[anchor:], offBase, matchLength)
		ip = start + matchLength
		anchor = ip
		if ms.lazySkipping {
			// We've found a match, disable lazy skipping mode, and refill
			// the hash cache.
			if method == searchRowHash {
				rowFillHashCache(ms, b, baseIdx, mls, ms.nextToUpdate, ilimit)
			}
			ms.lazySkipping = false
		}

		// check immediate repcode
		for ip <= ilimit {
			ml := repMatchLength(ip, idx(ip), offset2)
			if ml == 0 {
				break
			}
			// repcode detected we should take it
			matchLength = ml
			offset2, offset1 = offset1, offset2 // swap offset history
			storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, matchLength)
			ip += matchLength
			anchor = ip
		}
	}

	// Save reps for next block
	rep[0] = offset1
	rep[1] = offset2

	// Return the last literals size
	return iend - anchor
}

/*-*******************************
*  dictMatchState variants
*********************************/

// The dictMatchState variants below follow C's dictMode ==
// ZSTD_dictMatchState branches: the context's own tables cover the prefix
// (window.base, as in noDict), then the attached CDict's tables
// (ms.dictMatchState, read-only) are searched. A CDict index i is the
// position i - dms.window.baseIdx of the dictionary content (C: dictBase +
// i) and stands for the context index i + dictIndexDelta, just below the
// prefix.

// insertAndFindFirstIndex inserts the positions up to ip into the hash
// chain; used to fill a CDict's tables (with the CDict's unbounded
// minMatch, as C). C: zstd_lazy.c:ZSTD_insertAndFindFirstIndex
func insertAndFindFirstIndex(ms *matchState, ip int) uint32 {
	return insertAndFindFirstIndexInternal(ms, ip, ms.cParams.minMatch, false)
}

// hcFindBestMatchDictMatchState. C: zstd_lazy.c:ZSTD_HcFindBestMatch with
// dictMode ZSTD_dictMatchState; see hcFindBestMatch.
func hcFindBestMatchDictMatchState(ms *matchState, ip, iLimit int, mls uint32) (int, uint32) {
	cParams := &ms.cParams
	chainTable := ms.chainTable
	chainSize := uint32(1) << cParams.chainLog
	chainMask := chainSize - 1
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	dictLimit := ms.window.dictLimit
	prefixStart := int(dictLimit - baseIdx)
	curr := uint32(ip) + baseIdx
	maxDistance := uint32(1) << cParams.windowLog
	lowestValid := ms.window.lowLimit
	withinMaxDistance := lowestValid
	if curr-lowestValid > maxDistance {
		withinMaxDistance = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	lowLimit := withinMaxDistance
	if isDictionary {
		lowLimit = lowestValid
	}
	var minChain uint32
	if curr > chainSize {
		minChain = curr - chainSize
	}
	nbAttempts := uint32(1) << cParams.searchLog
	ml := 4 - 1
	offBase := uint32(offBaseNotFound)
	dms := ms.dictMatchState

	// HC4 match finder
	matchIndex := insertAndFindFirstIndexInternal(ms, ip, mls, ms.lazySkipping)

	// C: `&` of the two conditions; && is equivalent (no side effects).
	for ; matchIndex >= lowLimit && nbAttempts > 0; nbAttempts-- {
		currentMl := 0
		match := int(matchIndex - baseIdx)
		// read 4B starting from (match + ml + 1 - sizeof(U32))
		if binary.LittleEndian.Uint32(b[match+ml-3:]) == binary.LittleEndian.Uint32(b[ip+ml-3:]) { // potentially better
			currentMl = count(b, ip, match, iLimit)
		}

		// save best solution
		if currentMl > ml {
			ml = currentMl
			offBase = offsetToOffBase(curr - matchIndex)
			if ip+currentMl == iLimit {
				break // best possible, avoids read overflow on next attempt
			}
		}

		if matchIndex <= minChain {
			break
		}
		matchIndex = chainTable[matchIndex&chainMask] // C: NEXT_IN_CHAIN(matchIndex, chainMask)
	}

	{
		dmsChainTable := dms.chainTable
		dmsChainSize := uint32(1) << dms.cParams.chainLog
		dmsChainMask := dmsChainSize - 1
		dmsLowestIndex := dms.window.dictLimit
		d, dmsBaseIdx := dms.window.base, dms.window.baseIdx
		dmsSize := dms.window.nextSrc // C: dmsEnd - dmsBase
		dmsEnd := int(dmsSize - dmsBaseIdx)
		dmsIndexDelta := dictLimit - dmsSize
		var dmsMinChain uint32
		if dmsSize > dmsChainSize {
			dmsMinChain = dmsSize - dmsChainSize
		}

		matchIndex = dms.hashTable[hashPtr(b, ip, dms.cParams.hashLog, mls)]

		for ; matchIndex >= dmsLowestIndex && nbAttempts > 0; nbAttempts-- {
			currentMl := 0
			match := int(matchIndex - dmsBaseIdx)
			if binary.LittleEndian.Uint32(d[match:]) == binary.LittleEndian.Uint32(b[ip:]) { // assumption : matchIndex <= dictLimit-4 (by table construction)
				currentMl = count2Segments(b, ip+4, d, match+4, iLimit, dmsEnd, prefixStart) + 4
			}

			// save best solution
			if currentMl > ml {
				ml = currentMl
				offBase = offsetToOffBase(curr - (matchIndex + dmsIndexDelta))
				if ip+currentMl == iLimit {
					break // best possible, avoids read overflow on next attempt
				}
			}

			if matchIndex <= dmsMinChain {
				break
			}

			matchIndex = dmsChainTable[matchIndex&dmsChainMask]
		}
	}
	return ml, offBase
}

// rowFindBestMatchDictMatchState. C: zstd_lazy.c:ZSTD_RowFindBestMatch with
// dictMode ZSTD_dictMatchState; see rowFindBestMatch. The CDict's rows are
// hashed without salt (its salt is 0), with the context's rowLog.
func rowFindBestMatchDictMatchState(ms *matchState, ip, iLimit int, mls, rowLog uint32) (int, uint32) {
	hashTable := ms.hashTable
	tagTable := ms.tagTable
	hashLog := ms.rowHashLog
	cParams := &ms.cParams
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	dictLimit := ms.window.dictLimit
	prefixStart := int(dictLimit - baseIdx)
	curr := uint32(ip) + baseIdx
	maxDistance := uint32(1) << cParams.windowLog
	lowestValid := ms.window.lowLimit
	withinMaxDistance := lowestValid
	if curr-lowestValid > maxDistance {
		withinMaxDistance = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	lowLimit := withinMaxDistance
	if isDictionary {
		lowLimit = lowestValid
	}
	rowEntries := uint32(1) << rowLog
	rowMask := rowEntries - 1
	cappedSearchLog := min(cParams.searchLog, rowLog) // nb of searches is capped at nb entries per row
	hashSalt := ms.hashSalt
	nbAttempts := uint32(1) << cappedSearchLog
	ml := 4 - 1
	offBase := uint32(offBaseNotFound)
	var hash uint32

	// DMS rows (C prefetches them here)
	dms := ms.dictMatchState
	dmsHash := hashPtr(b, ip, dms.rowHashLog+rowHashTagBits, mls)
	dmsRelRow := (dmsHash >> rowHashTagBits) << rowLog
	dmsTag := dmsHash & rowHashTagMask
	dmsTagRow := dms.tagTable[dmsRelRow : dmsRelRow+rowEntries]
	dmsRow := dms.hashTable[dmsRelRow : dmsRelRow+rowEntries]

	// Update the hashTable and tagTable up to (but not including) ip
	if !ms.lazySkipping {
		rowUpdateInternal(ms, ip, mls, rowLog, rowMask, true)
		hash = rowNextCachedHash(&ms.hashCache, b, baseIdx, curr, hashLog, mls, hashSalt)
	} else {
		// Stop inserting every position when in the lazy skipping mode.
		// The hash cache is also not kept up to date in this mode.
		hash = hashPtrSalted(b, ip, hashLog+rowHashTagBits, mls, hashSalt)
		ms.nextToUpdate = curr
	}
	ms.hashSaltEntropy += hash // collect salt entropy

	{
		// Get the hash for ip, compute the appropriate row
		relRow := (hash >> rowHashTagBits) << rowLog
		tag := hash & rowHashTagMask
		row := hashTable[relRow : relRow+rowEntries]
		tagRow := tagTable[relRow : relRow+rowEntries]
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

		// Speed opt: insert current byte into hashtable too. This allows
		// us to avoid one iteration of the loop in rowUpdateInternal() at
		// the next search.
		{
			pos := rowNextIndex(tagRow, rowMask)
			tagRow[pos] = byte(tag)
			row[pos] = ms.nextToUpdate
			ms.nextToUpdate++
		}

		// Return the longest match
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
				offBase = offsetToOffBase(curr - matchIndex)
				if ip+currentMl == iLimit {
					break // best possible, avoids read overflow on next attempt
				}
			}
		}
	}

	{
		dmsLowestIndex := dms.window.dictLimit
		d, dmsBaseIdx := dms.window.base, dms.window.baseIdx
		dmsSize := dms.window.nextSrc // C: dmsEnd - dmsBase
		dmsEnd := int(dmsSize - dmsBaseIdx)
		dmsIndexDelta := dictLimit - dmsSize

		headGrouped := uint32(dmsTagRow[0]) & rowMask
		var matchBuffer [rowHashMaxEntries]uint32
		numMatches := 0
		matches := rowGetMatchMask(dmsTagRow, byte(dmsTag), headGrouped, rowEntries)

		for ; matches > 0 && nbAttempts > 0; matches &= matches - 1 {
			matchPos := (headGrouped + rowVecMaskNext(matches)) & rowMask
			matchIndex := dmsRow[matchPos]
			if matchPos == 0 {
				continue
			}
			if matchIndex < dmsLowestIndex {
				break
			}
			matchBuffer[numMatches] = matchIndex
			numMatches++
			nbAttempts--
		}

		// Return the longest match
		for _, matchIndex := range matchBuffer[:numMatches] {
			currentMl := 0
			match := int(matchIndex - dmsBaseIdx)
			if binary.LittleEndian.Uint32(d[match:]) == binary.LittleEndian.Uint32(b[ip:]) {
				currentMl = count2Segments(b, ip+4, d, match+4, iLimit, dmsEnd, prefixStart) + 4
			}

			if currentMl > ml {
				ml = currentMl
				offBase = offsetToOffBase(curr - (matchIndex + dmsIndexDelta))
				if ip+currentMl == iLimit {
					break
				}
			}
		}
	}
	return ml, offBase
}

// dubtFindBetterDictMatch searches the attached CDict's binary tree (built
// fully sorted by updateTree) for a better match than bestLength at ip,
// with the remaining nbCompares. C: zstd_lazy.c:ZSTD_DUBT_findBetterDictMatch
func dubtFindBetterDictMatch(ms *matchState, ip, iend int, offBase *uint32, bestLength int, nbCompares, mls uint32) int {
	dms := ms.dictMatchState
	dmsCParams := &dms.cParams
	dictHashTable := dms.hashTable
	hashLog := dmsCParams.hashLog
	b, baseIdx := ms.window.base, ms.window.baseIdx
	h := hashPtr(b, ip, hashLog, mls)
	dictMatchIndex := dictHashTable[h]

	prefixStart := int(ms.window.dictLimit - baseIdx)
	curr := uint32(ip) + baseIdx
	d, dictBaseIdx := dms.window.base, dms.window.baseIdx
	dictHighLimit := dms.window.nextSrc // C: dms->window.nextSrc - dms->window.base
	dictEnd := int(dictHighLimit - dictBaseIdx)
	dictLowLimit := dms.window.lowLimit
	dictIndexDelta := ms.window.lowLimit - dictHighLimit

	dictBt := dms.chainTable
	btLog := dmsCParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1
	btLow := dictLowLimit
	if btMask < dictHighLimit-dictLowLimit {
		btLow = dictHighLimit - btMask
	}

	var commonLengthSmaller, commonLengthLarger int

	for ; nbCompares > 0 && dictMatchIndex > dictLowLimit; nbCompares-- {
		nextPtr := dictBt[2*(dictMatchIndex&btMask):][:2]
		matchLength := min(commonLengthSmaller, commonLengthLarger) // guaranteed minimum nb of common bytes
		match := int(dictMatchIndex - dictBaseIdx)
		matchLength += count2Segments(b, ip+matchLength, d, match+matchLength, iend, dictEnd, prefixStart)
		// C: match = base + dictMatchIndex + dictIndexDelta when the match
		// crossed into the prefix, to prepare for next usage of
		// match[matchLength]
		mByte := func() byte { // C: match[matchLength]
			if dictMatchIndex+uint32(matchLength) >= dictHighLimit {
				return b[int(dictMatchIndex+dictIndexDelta+uint32(matchLength)-baseIdx)]
			}
			return d[match+matchLength]
		}

		if matchLength > bestLength {
			matchIndex := dictMatchIndex + dictIndexDelta
			if 4*(matchLength-bestLength) > int(highbit32(curr-matchIndex+1))-int(highbit32(*offBase+1)) {
				bestLength, *offBase = matchLength, offsetToOffBase(curr-matchIndex)
			}
			if ip+matchLength == iend { // reached end of input : ip[matchLength] is not valid, no way to know if it's larger or smaller than match
				break // drop, to guarantee consistency (miss a little bit of compression)
			}
		}

		if mByte() < b[ip+matchLength] {
			if dictMatchIndex <= btLow {
				break // beyond tree size, stop the search
			}
			commonLengthSmaller = matchLength // all smaller will now have at least this guaranteed common length
			dictMatchIndex = nextPtr[1]       // new matchIndex larger than previous (closer to current)
		} else {
			// match is larger than current
			if dictMatchIndex <= btLow {
				break // beyond tree size, stop the search
			}
			commonLengthLarger = matchLength
			dictMatchIndex = nextPtr[0]
		}
	}
	return bestLength
}

// dubtFindBestMatchDictMatchState. C: zstd_lazy.c:ZSTD_DUBT_findBestMatch
// with dictMode ZSTD_dictMatchState; see dubtFindBestMatch. The context's
// candidates are all in the prefix (insertDUBT1 is the noDict one, as in
// C), then the CDict's tree is searched with the comparisons left.
func dubtFindBestMatchDictMatchState(ms *matchState, ip, iend int, mls uint32) (int, uint32) {
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hashLog := cParams.hashLog
	b := ms.window.base
	h := hashPtr(b, ip, hashLog, mls)
	matchIndex := hashTable[h]

	baseIdx := ms.window.baseIdx
	curr := uint32(ip) + baseIdx
	windowLow := getLowestMatchIndex(ms, curr, cParams.windowLog)

	bt := ms.chainTable
	btLog := cParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1
	var btLow uint32
	if btMask < curr {
		btLow = curr - btMask
	}
	unsortLimit := max(btLow, windowLow)

	nextCandidate := 2 * (matchIndex & btMask) // C: nextCandidate; unsortedMark is the next entry
	nbCompares := uint32(1) << cParams.searchLog
	nbCandidates := nbCompares
	var previousCandidate uint32
	offBase := uint32(offBaseNotFound)

	// reach end of unsorted candidates list
	for matchIndex > unsortLimit && bt[nextCandidate+1] == dubtUnsortedMark && nbCandidates > 1 {
		bt[nextCandidate+1] = previousCandidate // the unsortedMark becomes a reversed chain, to move up back to original position
		previousCandidate = matchIndex
		matchIndex = bt[nextCandidate]
		nextCandidate = 2 * (matchIndex & btMask)
		nbCandidates--
	}

	// nullify last candidate if it's still unsorted
	// simplification, detrimental to compression ratio, beneficial for speed
	if matchIndex > unsortLimit && bt[nextCandidate+1] == dubtUnsortedMark {
		bt[nextCandidate] = 0
		bt[nextCandidate+1] = 0
	}

	// batch sort stacked candidates
	matchIndex = previousCandidate
	for matchIndex != 0 { // will end on matchIndex == 0
		nextCandidateIdx := bt[2*(matchIndex&btMask)+1]
		insertDUBT1(ms, matchIndex, iend, nbCandidates, unsortLimit)
		matchIndex = nextCandidateIdx
		nbCandidates++
	}

	// find longest match
	var commonLengthSmaller, commonLengthLarger int
	smallerPtr := &bt[2*(curr&btMask)]
	largerPtr := &bt[2*(curr&btMask)+1]
	matchEndIdx := curr + 8 + 1
	var dummy32 uint32 // to be nullified at the end
	bestLength := 0

	matchIndex = hashTable[h]
	hashTable[h] = curr // Update Hash Table

	for ; nbCompares > 0 && matchIndex > windowLow; nbCompares-- {
		nextPtr := bt[2*(matchIndex&btMask):][:2]
		matchLength := min(commonLengthSmaller, commonLengthLarger) // guaranteed minimum nb of common bytes
		match := int(matchIndex - baseIdx)
		matchLength += count(b, ip+matchLength, match+matchLength, iend)

		if matchLength > bestLength {
			if uint32(matchLength) > matchEndIdx-matchIndex {
				matchEndIdx = matchIndex + uint32(matchLength)
			}
			if 4*(matchLength-bestLength) > int(highbit32(curr-matchIndex+1))-int(highbit32(offBase)) {
				bestLength, offBase = matchLength, offsetToOffBase(curr-matchIndex)
			}
			if ip+matchLength == iend { // equal : no way to know if inf or sup
				// in addition to avoiding checking any further in this
				// loop, make sure we skip checking in the dictionary.
				nbCompares = 0
				break // drop, to guarantee consistency (miss a little bit of compression)
			}
		}

		if b[match+matchLength] < b[ip+matchLength] {
			// match is smaller than current
			*smallerPtr = matchIndex          // update smaller idx
			commonLengthSmaller = matchLength // all smaller will now have at least this guaranteed common length
			if matchIndex <= btLow {          // beyond tree size, stop the search
				smallerPtr = &dummy32
				break
			}
			smallerPtr = &nextPtr[1] // new "smaller" => larger of match
			matchIndex = nextPtr[1]  // new matchIndex larger than previous (closer to current)
		} else {
			// match is larger than current
			*largerPtr = matchIndex
			commonLengthLarger = matchLength
			if matchIndex <= btLow { // beyond tree size, stop the search
				largerPtr = &dummy32
				break
			}
			largerPtr = &nextPtr[0]
			matchIndex = nextPtr[0]
		}
	}

	*largerPtr = 0
	*smallerPtr = 0

	if nbCompares > 0 {
		bestLength = dubtFindBetterDictMatch(ms, ip, iend, &offBase, bestLength, nbCompares, mls)
	}

	ms.nextToUpdate = matchEndIdx - 8 // skip repetitive patterns
	return bestLength, offBase
}

// btFindBestMatchDictMatchState. C: zstd_lazy.c:ZSTD_BtFindBestMatch with
// dictMode ZSTD_dictMatchState.
func btFindBestMatchDictMatchState(ms *matchState, ip, iLimit int, mls uint32) (int, uint32) {
	if uint32(ip)+ms.window.baseIdx < ms.nextToUpdate {
		return 0, offBaseNotFound // skipped area
	}
	updateDUBT(ms, ip, mls)
	return dubtFindBestMatchDictMatchState(ms, ip, iLimit, mls)
}

// searchMaxDictMatchState. C: zstd_lazy.c:ZSTD_searchMax with dictMode
// ZSTD_dictMatchState.
func searchMaxDictMatchState(ms *matchState, ip, iLimit int, mls, rowLog uint32, method searchMethod) (int, uint32) {
	if method == searchRowHash {
		return rowFindBestMatchDictMatchState(ms, ip, iLimit, mls, rowLog)
	}
	if method == searchBinaryTree {
		return btFindBestMatchDictMatchState(ms, ip, iLimit, mls)
	}
	return hcFindBestMatchDictMatchState(ms, ip, iLimit, mls)
}

// compressBlockGreedyDictMatchState. C: zstd_lazy.c:ZSTD_compressBlock_greedy_dictMatchState
func compressBlockGreedyDictMatchState(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 0)
}

// compressBlockLazyDictMatchState. C: zstd_lazy.c:ZSTD_compressBlock_lazy_dictMatchState
func compressBlockLazyDictMatchState(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 1)
}

// compressBlockLazy2DictMatchState. C: zstd_lazy.c:ZSTD_compressBlock_lazy2_dictMatchState
func compressBlockLazy2DictMatchState(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchHashChain, 2)
}

// compressBlockGreedyDictMatchStateRow. C: zstd_lazy.c:ZSTD_compressBlock_greedy_dictMatchState_row
func compressBlockGreedyDictMatchStateRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 0)
}

// compressBlockLazyDictMatchStateRow. C: zstd_lazy.c:ZSTD_compressBlock_lazy_dictMatchState_row
func compressBlockLazyDictMatchStateRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 1)
}

// compressBlockLazy2DictMatchStateRow. C: zstd_lazy.c:ZSTD_compressBlock_lazy2_dictMatchState_row
func compressBlockLazy2DictMatchStateRow(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchRowHash, 2)
}

// compressBlockBtlazy2DictMatchState. C: zstd_lazy.c:ZSTD_compressBlock_btlazy2_dictMatchState
func compressBlockBtlazy2DictMatchState(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	return compressBlockLazyDictMatchStateGeneric(ms, ss, rep, src, srcIdx, searchBinaryTree, 2)
}

// compressBlockLazyDictMatchStateGeneric is the lazy parser searching an
// attached CDict too; see compressBlockLazyGeneric.
// C: zstd_lazy.c:ZSTD_compressBlock_lazy_generic with dictMode
// ZSTD_dictMatchState; C's `goto _storeSequence` is the store flag, its
// repcode checks are repMatchLength.
func compressBlockLazyDictMatchStateGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, method searchMethod, depth int) int {
	if dictMatchStateHook != nil {
		dictMatchStateHook(lazyExtDictNames[method][depth])
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	istart := int(srcIdx - baseIdx)
	ip := istart
	anchor := istart
	iend := istart + len(src)
	ilimit := iend - 8
	if method == searchRowHash {
		ilimit -= rowHashCacheSize
	}
	prefixLowestIndex := ms.window.dictLimit
	prefixLowest := int(prefixLowestIndex - baseIdx)
	mls := min(max(ms.cParams.minMatch, 4), 6)
	rowLog := min(max(ms.cParams.searchLog, 4), 6)

	offset1, offset2 := rep[0], rep[1]

	dms := ms.dictMatchState
	dictLowestIndex := dms.window.dictLimit
	d, dictBaseIdx := dms.window.base, dms.window.baseIdx
	dictLowest := int(dictLowestIndex - dictBaseIdx)
	dictEndIdx := dms.window.nextSrc // C: dictEnd - dictBase
	dictEnd := int(dictEndIdx - dictBaseIdx)
	dictIndexDelta := prefixLowestIndex - dictEndIdx
	dictAndPrefixLength := uint32(ip-prefixLowest) + uint32(dictEnd-dictLowest)

	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	// seg returns the segment holding index i and i's position in it.
	seg := func(i uint32) ([]byte, int) {
		if i < prefixLowestIndex {
			return d, int(i - dictIndexDelta - dictBaseIdx)
		}
		return b, int(i - baseIdx)
	}
	// repMatchLength returns the length of the repcode match at position p
	// (index cur) with offset off, or 0 if there is none (C: the isDxS
	// "check repCode" blocks).
	repMatchLength := func(p int, cur, off uint32) int {
		repIndex := cur - off
		if indexOverlapCheck(prefixLowestIndex, repIndex) {
			repBase, rp := seg(repIndex)
			if binary.LittleEndian.Uint32(repBase[rp:]) == binary.LittleEndian.Uint32(b[p:]) {
				repMatchEnd := iend
				if repIndex < prefixLowestIndex {
					repMatchEnd = dictEnd
				}
				return count2Segments(b, p+4, repBase, rp+4, iend, repMatchEnd, prefixLowest) + 4
			}
		}
		return 0
	}

	if dictAndPrefixLength == 0 {
		ip++
	}

	// Reset the lazy skipping state
	ms.lazySkipping = false

	if method == searchRowHash {
		rowFillHashCache(ms, b, baseIdx, mls, ms.nextToUpdate, ilimit)
	}

	// Match Loop
	for ip < ilimit {
		matchLength := 0
		offBase := uint32(repcode1ToOffBase)
		start := ip + 1
		store := false // C: goto _storeSequence

		// check repCode
		if rl := repMatchLength(ip+1, idx(ip)+1, offset1); rl > 0 {
			matchLength = rl
			store = depth == 0
		}

		if !store {
			// first search (depth 0)
			if ml2, offbaseFound := searchMaxDictMatchState(ms, ip, iend, mls, rowLog, method); ml2 > matchLength {
				matchLength, start, offBase = ml2, ip, offbaseFound
			}

			if matchLength < 4 {
				step := (ip-anchor)>>kSearchStrength + 1 // jump faster over incompressible sections
				ip += step
				// Enter the lazy skipping mode once we are skipping more
				// than 8 bytes at a time (see compressBlockLazyGeneric).
				ms.lazySkipping = step > kLazySkippingStep
				continue
			}

			// let's try to find a better solution
			if depth >= 1 {
				for ip < ilimit {
					ip++
					if mlRep := repMatchLength(ip, idx(ip), offset1); mlRep > 0 {
						gain2 := mlRep * 3
						gain1 := matchLength*3 - int(highbit32(offBase)) + 1
						if mlRep >= 4 && gain2 > gain1 {
							matchLength, offBase, start = mlRep, repcode1ToOffBase, ip
						}
					}
					{
						ml2, ofbCandidate := searchMaxDictMatchState(ms, ip, iend, mls, rowLog, method)
						gain2 := ml2*4 - int(highbit32(ofbCandidate)) // raw approx
						gain1 := matchLength*4 - int(highbit32(offBase)) + 4
						if ml2 >= 4 && gain2 > gain1 {
							matchLength, offBase, start = ml2, ofbCandidate, ip
							continue // search a better one
						}
					}

					// let's find an even better one
					if depth == 2 && ip < ilimit {
						ip++
						if mlRep := repMatchLength(ip, idx(ip), offset1); mlRep > 0 {
							gain2 := mlRep * 4
							gain1 := matchLength*4 - int(highbit32(offBase)) + 1
							if mlRep >= 4 && gain2 > gain1 {
								matchLength, offBase, start = mlRep, repcode1ToOffBase, ip
							}
						}
						{
							ml2, ofbCandidate := searchMaxDictMatchState(ms, ip, iend, mls, rowLog, method)
							gain2 := ml2*4 - int(highbit32(ofbCandidate)) // raw approx
							gain1 := matchLength*4 - int(highbit32(offBase)) + 7
							if ml2 >= 4 && gain2 > gain1 {
								matchLength, offBase, start = ml2, ofbCandidate, ip
								continue
							}
						}
					}
					break // nothing found : store previous solution
				}
			}

			// catch up
			if offBase > repNum { // C: OFFBASE_IS_OFFSET
				matchIndex := idx(start) - (offBase - repNum)
				match, mp := seg(matchIndex)
				mStart := prefixLowest
				if matchIndex < prefixLowestIndex {
					mStart = dictLowest
				}
				for start > anchor && mp > mStart && b[start-1] == match[mp-1] { // catch up
					start--
					mp--
					matchLength++
				}
				offset2 = offset1
				offset1 = offBase - repNum
			}
		}

		// C: _storeSequence
		storeSeq(ss, start-anchor, b[anchor:], offBase, matchLength)
		ip = start + matchLength
		anchor = ip
		if ms.lazySkipping {
			// We've found a match, disable lazy skipping mode, and refill
			// the hash cache.
			if method == searchRowHash {
				rowFillHashCache(ms, b, baseIdx, mls, ms.nextToUpdate, ilimit)
			}
			ms.lazySkipping = false
		}

		// check immediate repcode
		for ip <= ilimit {
			ml := repMatchLength(ip, idx(ip), offset2)
			if ml == 0 {
				break
			}
			matchLength = ml
			offset2, offset1 = offset1, offset2 // swap offset_2 <=> offset_1
			storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, matchLength)
			ip += matchLength
			anchor = ip
		}
	}

	// save reps for next block (C's offsetSaved values stay 0 in this mode)
	rep[0] = offset1
	rep[1] = offset2

	// Return the last literals size
	return iend - anchor
}
