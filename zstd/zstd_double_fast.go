// Port of libzstd v1.5.7 lib/compress/zstd_double_fast.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license).

package zstd

import "encoding/binary"

// dictTableLoadMethod. C: zstd_compress_internal.h:ZSTD_dictTableLoadMethod_e
type dictTableLoadMethod int

const (
	dtlmFast dictTableLoadMethod = iota // C: ZSTD_dtlm_fast
	dtlmFull                            // C: ZSTD_dtlm_full
)

// fillDoubleHashTable inserts the positions of the data up to end (an index
// into ms.window.base) into the long (hashTable) and short (chainTable) hash
// tables, from ms.nextToUpdate: every third position into both, and with
// dtlmFull the others into the long table if their entry is empty. A
// CDict's entries are tagged (see writeTaggedIndex).
// C: zstd_double_fast.c:ZSTD_fillDoubleHashTable
// (ZSTD_fillDoubleHashTableForCDict and ZSTD_fillDoubleHashTableForCCtx)
func fillDoubleHashTable(ms *matchState, end uint32, dtlm dictTableLoadMethod, tfp tableFillPurpose) {
	cParams := &ms.cParams
	hashLarge := ms.hashTable
	hBitsL := cParams.hashLog
	mls := cParams.minMatch
	hashSmall := ms.chainTable
	hBitsS := cParams.chainLog
	if tfp == tfpForCDict {
		hBitsL += shortCacheTagBits
		hBitsS += shortCacheTagBits
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	const fastHashFillStep = 3

	ip := int(ms.nextToUpdate - baseIdx)
	iend := int(end-baseIdx) - hashReadSize

	// Always insert every fastHashFillStep position into the hash tables.
	// Insert the other positions into the large hash table if their entry is
	// empty.
	for ; ip+fastHashFillStep-1 <= iend; ip += fastHashFillStep {
		curr := uint32(ip) + baseIdx
		for i := 0; i < fastHashFillStep; i++ {
			smHash := hashPtr(b, ip+i, hBitsS, mls)
			lgHash := hashPtr(b, ip+i, hBitsL, 8)
			if tfp == tfpForCDict {
				if i == 0 {
					writeTaggedIndex(hashSmall, smHash, curr+uint32(i))
				}
				if i == 0 || hashLarge[lgHash>>shortCacheTagBits] == 0 {
					writeTaggedIndex(hashLarge, lgHash, curr+uint32(i))
				}
			} else {
				if i == 0 {
					hashSmall[smHash] = curr + uint32(i)
				}
				if i == 0 || hashLarge[lgHash] == 0 {
					hashLarge[lgHash] = curr + uint32(i)
				}
			}
			// Only load extra positions for dtlmFull
			if dtlm == dtlmFast {
				break
			}
		}
	}
}

// compressBlockDoubleFast is the ZSTD_dfast match finder (no dictionary).
// C: zstd_double_fast.c:ZSTD_compressBlock_doubleFast
//
// C instantiates ZSTD_compressBlock_doubleFast_noDict_generic for mls 4..7
// (any other minMatch uses 4); here mls is a value used only by the short
// hash, which hashPtr's switch resolves.
func compressBlockDoubleFast(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	mls := ms.cParams.minMatch
	if mls < 4 || mls > 7 {
		mls = 4 // includes case 3
	}
	return compressBlockDoubleFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockDoubleFastNoDictGeneric.
// C: zstd_double_fast.c:ZSTD_compressBlock_doubleFast_noDict_generic
//
// Pointers become positions in ms.window.base (C's base + idx is
// b[idx-baseIdx]). C's ZSTD_selectAddr dummy loads (a branch-free guard so
// that stale indices are not dereferenced) become the equivalent index
// comparisons; only speed differs.
func compressBlockDoubleFastNoDictGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	cParams := &ms.cParams
	hashLong := ms.hashTable
	hBitsL := cParams.hashLog
	hashSmall := ms.chainTable
	hBitsS := cParams.chainLog
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	istart := int(srcIdx - baseIdx)
	anchor := istart
	endIndex := srcIdx + uint32(len(src))
	// presumes that, if there is a dictionary, it must be using Attach mode
	prefixLowestIndex := getLowestPrefixIndex(ms, endIndex, cParams.windowLog)
	prefixLowest := int(prefixLowestIndex - baseIdx)
	iend := istart + len(src)
	ilimit := iend - hashReadSize
	offset1, offset2 := rep[0], rep[1]
	var offsetSaved1, offsetSaved2 uint32

	var mLength int
	var offset uint32
	var curr uint32

	// how many positions to search before increasing step size
	const kStepIncr = 1 << kSearchStrength

	// Closures on b/baseIdx that the compiler inlines.
	hashL := func(p int) uint32 { return hashPtr(b, p, hBitsL, 8) }
	hashS := func(p int) uint32 { return hashPtr(b, p, hBitsS, mls) }
	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	read32 := func(p int) uint32 { return binary.LittleEndian.Uint32(b[p:]) }
	read64 := func(p int) uint64 { return binary.LittleEndian.Uint64(b[p:]) }

	ip := istart // the current position
	// init
	if ip == prefixLowest {
		ip++
	}
	{
		current := idx(ip)
		windowLow := getLowestPrefixIndex(ms, current, cParams.windowLog)
		maxRep := current - windowLow
		if offset2 > maxRep {
			offsetSaved2, offset2 = offset2, 0
		}
		if offset1 > maxRep {
			offsetSaved1, offset1 = offset1, 0
		}
	}

	// C: _cleanup
	cleanup := func() int {
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

	// Outer Loop: one iteration per match found and stored
	for {
		step := 1 // the current step size
		nextStep := ip + kStepIncr
		ip1 := ip + step // the next position

		if ip1 > ilimit {
			return cleanup()
		}

		hl0 := hashL(ip)       // the long hash at ip
		idxl0 := hashLong[hl0] // the long match index for ip
		var hl1 uint32         // the long hash at ip1
		var idxl1 uint32       // the long match index for ip1
		var matchs0 int        // the short match for ip

		const (
			notFound   = iota // C: loop exit to _cleanup
			repStored         // C: goto _match_stored
			searchLong        // C: goto _search_next_long
			matchFound        // C: goto _match_found
		)
		state := notFound

		// Inner Loop: one iteration per search / position
		for {
			hs0 := hashS(ip)
			idxs0 := hashSmall[hs0]
			curr = idx(ip)

			hashLong[hl0] = curr // update hash tables
			hashSmall[hs0] = curr

			// check noDict repcode
			if offset1 > 0 && read32(ip+1-int(offset1)) == read32(ip+1) {
				mLength = count(b, ip+1+4, ip+1+4-int(offset1), iend) + 4
				ip++
				storeSeq(ss, ip-anchor, b[anchor:], repcode1ToOffBase, mLength)
				state = repStored
				break
			}

			hl1 = hashL(ip1)

			// check prefix long match
			if idxl0 >= prefixLowestIndex {
				matchl0 := int(idxl0 - baseIdx)
				if read64(matchl0) == read64(ip) {
					mLength = count(b, ip+8, matchl0+8, iend) + 8
					offset = uint32(ip - matchl0)
					for ip > anchor && matchl0 > prefixLowest && b[ip-1] == b[matchl0-1] { // catch up
						ip--
						matchl0--
						mLength++
					}
					state = matchFound
					break
				}
			}

			idxl1 = hashLong[hl1]

			// check prefix short match
			if idxs0 >= prefixLowestIndex {
				matchs0 = int(idxs0 - baseIdx)
				if read32(matchs0) == read32(ip) {
					state = searchLong
					break
				}
			}

			if ip1 >= nextStep {
				step++
				nextStep += kStepIncr
			}
			ip = ip1
			ip1 += step

			hl0 = hl1
			idxl0 = idxl1
			if ip1 > ilimit {
				break // state == notFound
			}
		}

		switch state {
		case notFound:
			return cleanup()

		case searchLong:
			// C: _search_next_long
			// short match found: let's check for a longer one
			mLength = count(b, ip+4, matchs0+4, iend) + 4
			offset = uint32(ip - matchs0)

			// check long match at +1 position
			if idxl1 > prefixLowestIndex {
				matchl1 := int(idxl1 - baseIdx)
				if read64(matchl1) == read64(ip1) {
					l1len := count(b, ip1+8, matchl1+8, iend) + 8
					if l1len > mLength {
						// use the long match instead
						ip = ip1
						mLength = l1len
						offset = uint32(ip - matchl1)
						matchs0 = matchl1
					}
				}
			}

			for ip > anchor && matchs0 > prefixLowest && b[ip-1] == b[matchs0-1] { // complete backward
				ip--
				matchs0--
				mLength++
			}
			fallthrough

		case matchFound:
			// C: _match_found (requires ip, offset, mLength)
			offset2 = offset1
			offset1 = offset

			if step < 4 {
				// It is unsafe to write this value back to the hashtable when
				// ip1 is greater than or equal to the new ip we will have
				// after we're done processing this match. Rather than perform
				// that test directly (ip1 >= ip + mLength), which costs speed
				// in practice, we do a simpler more predictable test. The
				// minmatch even if we take a short match is 4 bytes, so as
				// long as step, the distance between ip and ip1 (initially) is
				// less than 4, we know ip1 < new ip.
				hashLong[hl1] = idx(ip1)
			}

			storeSeq(ss, ip-anchor, b[anchor:], offsetToOffBase(offset), mLength)
		}

		// C: _match_stored
		// match found
		ip += mLength
		anchor = ip

		if ip <= ilimit {
			// Complementary insertion
			// done after iLimit test, as candidates could be > iend-8
			{
				indexToInsert := curr + 2
				hashLong[hashL(int(indexToInsert-baseIdx))] = indexToInsert
				hashLong[hashL(ip-2)] = idx(ip - 2)
				hashSmall[hashS(int(indexToInsert-baseIdx))] = indexToInsert
				hashSmall[hashS(ip-1)] = idx(ip - 1)
			}

			// check immediate repcode
			for ip <= ilimit && offset2 > 0 && read32(ip) == read32(ip-int(offset2)) {
				// store sequence
				rLength := count(b, ip+4, ip+4-int(offset2), iend) + 4
				offset2, offset1 = offset1, offset2 // swap offset2 <=> offset1
				hashSmall[hashS(ip)] = idx(ip)
				hashLong[hashL(ip)] = idx(ip)
				storeSeq(ss, 0, nil, repcode1ToOffBase, rLength)
				ip += rLength
				anchor = ip
			}
		}
	}
}

// compressBlockDoubleFastExtDict is the ZSTD_dfast match finder for a
// window with an extDict segment.
// C: zstd_double_fast.c:ZSTD_compressBlock_doubleFast_extDict (mls 4..7,
// any other minMatch uses 4; mls is a value here)
func compressBlockDoubleFastExtDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	mls := ms.cParams.minMatch
	if mls < 4 || mls > 7 {
		mls = 4 // includes case 3
	}
	return compressBlockDoubleFastExtDictGeneric(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockDoubleFastExtDictGeneric.
// C: zstd_double_fast.c:ZSTD_compressBlock_doubleFast_extDict_generic
//
// Pointers become positions: in the prefix ms.window.base (C's base + idx
// is b[idx-baseIdx]) and in the extDict ms.window.dictBase (C's dictBase +
// idx is d[idx-dictBaseIdx]).
func compressBlockDoubleFastExtDictGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	cParams := &ms.cParams
	hashLong := ms.hashTable
	hBitsL := cParams.hashLog
	hashSmall := ms.chainTable
	hBitsS := cParams.chainLog
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	istart := int(srcIdx - baseIdx)
	ip := istart
	anchor := istart
	iend := istart + len(src)
	ilimit := iend - 8
	endIndex := srcIdx + uint32(len(src))
	lowLimit := getLowestMatchIndex(ms, endIndex, cParams.windowLog)
	dictStartIndex := lowLimit
	dictLimit := w.dictLimit
	prefixStartIndex := lowLimit
	if dictLimit > lowLimit {
		prefixStartIndex = dictLimit
	}
	offset1, offset2 := rep[0], rep[1]

	// if extDict is invalidated due to maxDistance, switch to "regular" variant
	if prefixStartIndex == dictStartIndex {
		return compressBlockDoubleFast(ms, ss, rep, src, srcIdx)
	}
	if extDictHook != nil {
		extDictHook("dfast")
	}
	prefixStart := int(prefixStartIndex - baseIdx)
	dictStart := int(dictStartIndex - dictBaseIdx)
	dictEnd := int(prefixStartIndex - dictBaseIdx)

	hashL := func(p int) uint32 { return hashPtr(b, p, hBitsL, 8) }
	hashS := func(p int) uint32 { return hashPtr(b, p, hBitsS, mls) }
	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	// seg returns the segment holding index i and i's position in it.
	seg := func(i uint32) ([]byte, int) {
		if i < prefixStartIndex {
			return d, int(i - dictBaseIdx)
		}
		return b, int(i - baseIdx)
	}
	// segEnd and segLow return the end and the lowest position of the
	// segment holding index i (C: dictEnd or iend, dictStart or prefixStart).
	segEnd := func(i uint32) int {
		if i < prefixStartIndex {
			return dictEnd
		}
		return iend
	}
	segLow := func(i uint32) int {
		if i < prefixStartIndex {
			return dictStart
		}
		return prefixStart
	}
	read32 := func(s []byte, p int) uint32 { return binary.LittleEndian.Uint32(s[p:]) }
	read64 := func(s []byte, p int) uint64 { return binary.LittleEndian.Uint64(s[p:]) }

	// Search Loop
	for ip < ilimit { // < instead of <=, because (ip+1)
		hSmall := hashS(ip)
		matchIndex := hashSmall[hSmall]
		match, mp := seg(matchIndex)

		hLong := hashL(ip)
		matchLongIndex := hashLong[hLong]
		matchLong, mlp := seg(matchLongIndex)

		curr := idx(ip)
		repIndex := curr + 1 - offset1 // offset_1 expected <= curr +1
		repMatch, rp := seg(repIndex)
		var mLength int
		hashSmall[hSmall] = curr // update hash table
		hashLong[hLong] = curr

		if indexOverlapCheck(prefixStartIndex, repIndex) && offset1 <= curr+1-dictStartIndex && // note: we are searching at curr+1
			read32(repMatch, rp) == read32(b, ip+1) {
			repMatchEnd := segEnd(repIndex)
			mLength = count2Segments(b, ip+1+4, repMatch, rp+4, iend, repMatchEnd, prefixStart) + 4
			ip++
			storeSeq(ss, ip-anchor, b[anchor:], repcode1ToOffBase, mLength)
		} else {
			if matchLongIndex > dictStartIndex && read64(matchLong, mlp) == read64(b, ip) {
				matchEnd := segEnd(matchLongIndex)
				lowMatchPtr := segLow(matchLongIndex)
				mLength = count2Segments(b, ip+8, matchLong, mlp+8, iend, matchEnd, prefixStart) + 8
				offset := curr - matchLongIndex
				for ip > anchor && mlp > lowMatchPtr && b[ip-1] == matchLong[mlp-1] { // catch up
					ip--
					mlp--
					mLength++
				}
				offset2 = offset1
				offset1 = offset
				storeSeq(ss, ip-anchor, b[anchor:], offsetToOffBase(offset), mLength)
			} else if matchIndex > dictStartIndex && read32(match, mp) == read32(b, ip) {
				h3 := hashL(ip + 1)
				matchIndex3 := hashLong[h3]
				match3, m3p := seg(matchIndex3)
				var offset uint32
				hashLong[h3] = curr + 1
				if matchIndex3 > dictStartIndex && read64(match3, m3p) == read64(b, ip+1) {
					matchEnd := segEnd(matchIndex3)
					lowMatchPtr := segLow(matchIndex3)
					mLength = count2Segments(b, ip+9, match3, m3p+8, iend, matchEnd, prefixStart) + 8
					ip++
					offset = curr + 1 - matchIndex3
					for ip > anchor && m3p > lowMatchPtr && b[ip-1] == match3[m3p-1] { // catch up
						ip--
						m3p--
						mLength++
					}
				} else {
					matchEnd := segEnd(matchIndex)
					lowMatchPtr := segLow(matchIndex)
					mLength = count2Segments(b, ip+4, match, mp+4, iend, matchEnd, prefixStart) + 4
					offset = curr - matchIndex
					for ip > anchor && mp > lowMatchPtr && b[ip-1] == match[mp-1] { // catch up
						ip--
						mp--
						mLength++
					}
				}
				offset2 = offset1
				offset1 = offset
				storeSeq(ss, ip-anchor, b[anchor:], offsetToOffBase(offset), mLength)
			} else {
				ip += (ip-anchor)>>kSearchStrength + 1
				continue
			}
		}

		// move to next sequence start
		ip += mLength
		anchor = ip

		if ip <= ilimit {
			// Complementary insertion
			// done after iLimit test, as candidates could be > iend-8
			{
				indexToInsert := curr + 2
				hashLong[hashL(int(indexToInsert-baseIdx))] = indexToInsert
				hashLong[hashL(ip-2)] = idx(ip - 2)
				hashSmall[hashS(int(indexToInsert-baseIdx))] = indexToInsert
				hashSmall[hashS(ip-1)] = idx(ip - 1)
			}

			// check immediate repcode
			for ip <= ilimit {
				current2 := idx(ip)
				repIndex2 := current2 - offset2
				repMatch2, rp2 := seg(repIndex2)
				if indexOverlapCheck(prefixStartIndex, repIndex2) && offset2 <= current2-dictStartIndex &&
					read32(repMatch2, rp2) == read32(b, ip) {
					repEnd2 := segEnd(repIndex2)
					repLength2 := count2Segments(b, ip+4, repMatch2, rp2+4, iend, repEnd2, prefixStart) + 4
					offset2, offset1 = offset1, offset2 // swap offset_2 <=> offset_1
					storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, repLength2)
					hashSmall[hashS(ip)] = current2
					hashLong[hashL(ip)] = current2
					ip += repLength2
					anchor = ip
					continue
				}
				break
			}
		}
	}

	// save reps for next block
	rep[0] = offset1
	rep[1] = offset2

	// Return the last literals size
	return iend - anchor
}

// compressBlockDoubleFastDictMatchState is the ZSTD_dfast match finder
// searching an attached CDict too.
// C: zstd_double_fast.c:ZSTD_compressBlock_doubleFast_dictMatchState (mls
// as in compressBlockDoubleFast)
func compressBlockDoubleFastDictMatchState(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	mls := ms.cParams.minMatch
	if mls < 4 || mls > 7 {
		mls = 4 // includes case 3
	}
	return compressBlockDoubleFastDictMatchStateGeneric(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockDoubleFastDictMatchStateGeneric.
// C: zstd_double_fast.c:ZSTD_compressBlock_doubleFast_dictMatchState_generic
//
// Pointers become positions in the prefix ms.window.base (b) or the
// dictionary dms.window.base (d); see compressBlockFastDictMatchStateGeneric.
// C's gotos are the found switch (_search_next_long, _match_found,
// _match_stored). C's prefetch of the dictionary's tables is a cache hint
// only.
func compressBlockDoubleFastDictMatchStateGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if dictMatchStateHook != nil {
		dictMatchStateHook("dfast")
	}
	cParams := &ms.cParams
	hashLong := ms.hashTable
	hBitsL := cParams.hashLog
	hashSmall := ms.chainTable
	hBitsS := cParams.chainLog
	b, baseIdx := ms.window.base, ms.window.baseIdx
	istart := int(srcIdx - baseIdx)
	ip := istart
	anchor := istart
	endIndex := srcIdx + uint32(len(src))
	// presumes that, if there is a dictionary, it must be using Attach mode
	prefixLowestIndex := getLowestPrefixIndex(ms, endIndex, cParams.windowLog)
	prefixLowest := int(prefixLowestIndex - baseIdx)
	iend := istart + len(src)
	ilimit := iend - hashReadSize
	offset1, offset2 := rep[0], rep[1]

	dms := ms.dictMatchState
	dictCParams := &dms.cParams
	dictHashLong := dms.hashTable
	dictHashSmall := dms.chainTable
	dictStartIndex := dms.window.dictLimit
	d, dictBaseIdx := dms.window.base, dms.window.baseIdx
	dictStart := int(dictStartIndex - dictBaseIdx)
	dictEndIdx := dms.window.nextSrc // C: dictEnd - dictBase
	dictEnd := int(dictEndIdx - dictBaseIdx)
	dictIndexDelta := prefixLowestIndex - dictEndIdx
	dictHBitsL := dictCParams.hashLog + shortCacheTagBits
	dictHBitsS := dictCParams.chainLog + shortCacheTagBits
	dictAndPrefixLength := uint32(ip-prefixLowest) + uint32(dictEnd-dictStart)

	read32 := func(s []byte, p int) uint32 { return binary.LittleEndian.Uint32(s[p:]) }
	read64 := func(s []byte, p int) uint64 { return binary.LittleEndian.Uint64(s[p:]) }
	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	// repAt returns the segment, position and segment end of the repcode
	// match at index repIndex (C: repMatch and repMatchEnd).
	repAt := func(repIndex uint32) ([]byte, int, int) {
		if repIndex < prefixLowestIndex {
			return d, int(repIndex - dictIndexDelta - dictBaseIdx), dictEnd
		}
		return b, int(repIndex - baseIdx), iend
	}

	// init
	if dictAndPrefixLength == 0 {
		ip++
	}

	// Main Search Loop
	for ip < ilimit { // < instead of <=, because repcode check at (ip+1)
		var mLength int
		var offset uint32
		h2 := hashPtr(b, ip, hBitsL, 8)
		h := hashPtr(b, ip, hBitsS, mls)
		dictHashAndTagL := hashPtr(b, ip, dictHBitsL, 8)
		dictHashAndTagS := hashPtr(b, ip, dictHBitsS, mls)
		dictMatchIndexAndTagL := dictHashLong[dictHashAndTagL>>shortCacheTagBits]
		dictMatchIndexAndTagS := dictHashSmall[dictHashAndTagS>>shortCacheTagBits]
		dictTagsMatchL := comparePackedTags(dictMatchIndexAndTagL, dictHashAndTagL)
		dictTagsMatchS := comparePackedTags(dictMatchIndexAndTagS, dictHashAndTagS)
		curr := idx(ip)
		matchIndexL := hashLong[h2]
		matchIndexS := hashSmall[h]
		matchLong := int(matchIndexL - baseIdx)
		matchSeg, match := b, int(matchIndexS-baseIdx)
		repIndex := curr + 1 - offset1
		repBase, repPos, repMatchEnd := repAt(repIndex)
		hashLong[h2] = curr // update hash tables
		hashSmall[h] = curr

		const (
			searchNextLong = iota + 1 // C: _search_next_long
			matchFound                // C: _match_found
			matchStored               // C: _match_stored
		)
		next := 0 // 0: no match, C's `continue`

		// check repcode
		if indexOverlapCheck(prefixLowestIndex, repIndex) && read32(repBase, repPos) == read32(b, ip+1) {
			mLength = count2Segments(b, ip+1+4, repBase, repPos+4, iend, repMatchEnd, prefixLowest) + 4
			ip++
			storeSeq(ss, ip-anchor, b[anchor:], repcode1ToOffBase, mLength)
			next = matchStored
		}

		if next == 0 {
			if matchIndexL >= prefixLowestIndex && read64(b, matchLong) == read64(b, ip) {
				// check prefix long match
				mLength = count(b, ip+8, matchLong+8, iend) + 8
				offset = uint32(ip - matchLong)
				for ip > anchor && matchLong > prefixLowest && b[ip-1] == b[matchLong-1] {
					ip--
					matchLong--
					mLength++
				} // catch up
				next = matchFound
			} else if dictTagsMatchL {
				// check dictMatchState long match
				dictMatchIndexL := dictMatchIndexAndTagL >> shortCacheTagBits
				dictMatchL := int(dictMatchIndexL - dictBaseIdx)
				if dictMatchIndexL > dictStartIndex && read64(d, dictMatchL) == read64(b, ip) { // C: dictMatchL > dictStart
					mLength = count2Segments(b, ip+8, d, dictMatchL+8, iend, dictEnd, prefixLowest) + 8
					offset = curr - dictMatchIndexL - dictIndexDelta
					for ip > anchor && dictMatchL > dictStart && b[ip-1] == d[dictMatchL-1] {
						ip--
						dictMatchL--
						mLength++
					} // catch up
					next = matchFound
				}
			}
		}

		if next == 0 {
			if matchIndexS > prefixLowestIndex {
				// short match candidate
				if read32(b, match) == read32(b, ip) {
					next = searchNextLong
				}
			} else if dictTagsMatchS {
				// check dictMatchState short match
				dictMatchIndexS := dictMatchIndexAndTagS >> shortCacheTagBits
				matchSeg, match = d, int(dictMatchIndexS-dictBaseIdx)
				matchIndexS = dictMatchIndexS + dictIndexDelta

				if dictMatchIndexS > dictStartIndex && read32(d, match) == read32(b, ip) { // C: match > dictStart
					next = searchNextLong
				}
			}
		}

		if next == 0 {
			ip += (ip-anchor)>>kSearchStrength + 1
			continue
		}

		if next == searchNextLong { // C: _search_next_long
			hl3 := hashPtr(b, ip+1, hBitsL, 8)
			dictHashAndTagL3 := hashPtr(b, ip+1, dictHBitsL, 8)
			matchIndexL3 := hashLong[hl3]
			dictMatchIndexAndTagL3 := dictHashLong[dictHashAndTagL3>>shortCacheTagBits]
			dictTagsMatchL3 := comparePackedTags(dictMatchIndexAndTagL3, dictHashAndTagL3)
			matchL3 := int(matchIndexL3 - baseIdx)
			hashLong[hl3] = curr + 1

			// check prefix long +1 match
			if matchIndexL3 >= prefixLowestIndex && read64(b, matchL3) == read64(b, ip+1) {
				mLength = count(b, ip+9, matchL3+8, iend) + 8
				ip++
				offset = uint32(ip - matchL3)
				for ip > anchor && matchL3 > prefixLowest && b[ip-1] == b[matchL3-1] {
					ip--
					matchL3--
					mLength++
				} // catch up
				next = matchFound
			} else if dictTagsMatchL3 {
				// check dict long +1 match
				dictMatchIndexL3 := dictMatchIndexAndTagL3 >> shortCacheTagBits
				dictMatchL3 := int(dictMatchIndexL3 - dictBaseIdx)
				if dictMatchIndexL3 > dictStartIndex && read64(d, dictMatchL3) == read64(b, ip+1) { // C: dictMatchL3 > dictStart
					mLength = count2Segments(b, ip+1+8, d, dictMatchL3+8, iend, dictEnd, prefixLowest) + 8
					ip++
					offset = curr + 1 - dictMatchIndexL3 - dictIndexDelta
					for ip > anchor && dictMatchL3 > dictStart && b[ip-1] == d[dictMatchL3-1] {
						ip--
						dictMatchL3--
						mLength++
					} // catch up
					next = matchFound
				}
			}
		}

		if next == searchNextLong {
			// if no long +1 match, explore the short match we found
			if matchIndexS < prefixLowestIndex {
				mLength = count2Segments(b, ip+4, matchSeg, match+4, iend, dictEnd, prefixLowest) + 4
				offset = curr - matchIndexS
				for ip > anchor && match > dictStart && b[ip-1] == matchSeg[match-1] {
					ip--
					match--
					mLength++
				} // catch up
			} else {
				mLength = count(b, ip+4, match+4, iend) + 4
				offset = uint32(ip - match)
				for ip > anchor && match > prefixLowest && b[ip-1] == b[match-1] {
					ip--
					match--
					mLength++
				} // catch up
			}
			next = matchFound
		}

		if next == matchFound { // C: _match_found
			offset2 = offset1
			offset1 = offset
			storeSeq(ss, ip-anchor, b[anchor:], offsetToOffBase(offset), mLength)
		}

		// C: _match_stored
		// match found
		ip += mLength
		anchor = ip

		if ip <= ilimit {
			// Complementary insertion
			// done after iLimit test, as candidates could be > iend-8
			{
				indexToInsert := curr + 2
				hashLong[hashPtr(b, int(indexToInsert-baseIdx), hBitsL, 8)] = indexToInsert
				hashLong[hashPtr(b, ip-2, hBitsL, 8)] = idx(ip - 2)
				hashSmall[hashPtr(b, int(indexToInsert-baseIdx), hBitsS, mls)] = indexToInsert
				hashSmall[hashPtr(b, ip-1, hBitsS, mls)] = idx(ip - 1)
			}

			// check immediate repcode
			for ip <= ilimit {
				current2 := idx(ip)
				repIndex2 := current2 - offset2
				repBase2, repPos2, repEnd2 := repAt(repIndex2)
				if indexOverlapCheck(prefixLowestIndex, repIndex2) && read32(repBase2, repPos2) == read32(b, ip) {
					repLength2 := count2Segments(b, ip+4, repBase2, repPos2+4, iend, repEnd2, prefixLowest) + 4
					offset2, offset1 = offset1, offset2 // swap offset_2 <=> offset_1
					storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, repLength2)
					hashSmall[hashPtr(b, ip, hBitsS, mls)] = current2
					hashLong[hashPtr(b, ip, hBitsL, 8)] = current2
					ip += repLength2
					anchor = ip
					continue
				}
				break
			}
		}
	} // while (ip < ilimit)

	// save reps for next block
	rep[0] = offset1
	rep[1] = offset2

	// Return the last literals size
	return iend - anchor
}
