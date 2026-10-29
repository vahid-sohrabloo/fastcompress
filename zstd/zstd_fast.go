// Port of libzstd v1.5.7 lib/compress/zstd_fast.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license).

package zstd

import "encoding/binary"

// fastHashParams returns the shift and prime with which ZSTD_hashPtr(p,
// hBits, mls) equals ((le64(p) << shift) * prime) >> (64 - hBits) for mls
// 4..7. For mls 4 C hashes a 32-bit read: (u32 * prime4) mod 2^32 >> (32 -
// hBits) is the same value, since (u << 32) * prime4 mod 2^64 = ((u * prime4)
// mod 2^32) << 32. The single branch-free form lets one function serve every
// mls where C instantiates one per mls.
func fastHashParams(mls uint32) (shift uint32, prime uint64) {
	switch mls {
	case 5:
		return 64 - 40, prime5bytes
	case 6:
		return 64 - 48, prime6bytes
	case 7:
		return 64 - 56, prime7bytes
	default:
		return 32, prime4bytes
	}
}

// fillHashTable inserts the positions of the data from ms.nextToUpdate up
// to end (an index) into the hash table: every third position, and with
// dtlmFull the others whose entry is empty. A CDict's entries are tagged
// (see writeTaggedIndex). C: zstd_fast.c:ZSTD_fillHashTable
// (ZSTD_fillHashTableForCDict and ZSTD_fillHashTableForCCtx; CDicts always
// use dtlmFull, contexts dtlmFast)
func fillHashTable(ms *matchState, end uint32, dtlm dictTableLoadMethod, tfp tableFillPurpose) {
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hBits := cParams.hashLog
	if tfp == tfpForCDict {
		hBits += shortCacheTagBits
	}
	mls := cParams.minMatch
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	ip := int(ms.nextToUpdate - baseIdx)
	iend := int(end-baseIdx) - hashReadSize
	const fastHashFillStep = 3

	// Always insert every fastHashFillStep position into the hash table.
	// Insert the other positions if their hash entry is empty.
	for ; ip+fastHashFillStep < iend+2; ip += fastHashFillStep {
		curr := uint32(ip) + baseIdx
		if tfp == tfpForCDict {
			writeTaggedIndex(hashTable, hashPtr(b, ip, hBits, mls), curr)
		} else {
			hashTable[hashPtr(b, ip, hBits, mls)] = curr
		}

		if dtlm == dtlmFast {
			continue
		}
		// Only load extra positions for dtlmFull
		for p := 1; p < fastHashFillStep; p++ {
			hash := hashPtr(b, ip+p, hBits, mls)
			if tfp == tfpForCDict {
				if hashTable[hash>>shortCacheTagBits] == 0 { // not yet filled
					writeTaggedIndex(hashTable, hash, curr+uint32(p))
				}
			} else if hashTable[hash] == 0 { // not yet filled
				hashTable[hash] = curr + uint32(p)
			}
		}
	}
}

// compressBlockFast is the ZSTD_fast match finder (no dictionary).
// C: zstd_fast.c:ZSTD_compressBlock_fast
//
// C selects one of eight instantiations of
// ZSTD_compressBlock_fast_noDict_generic: mls 4..7 (any other minMatch uses
// 4), times ZSTD_match4Found_cmov (windowLog < 19) or _branch. The two
// match4Found variants return the same result (the cmov one compares
// against a dummy when the index is out of range, then also requires it in
// range), so they differ only in speed. Here one function takes mls as a
// value; its only use is the hash, computed branch-free (fastHashParams).
func compressBlockFast(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	mls := ms.cParams.minMatch
	if mls < 4 || mls > 7 {
		mls = 4 // includes case 3
	}
	return compressBlockFastNoDict(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockFastExtDict is the ZSTD_fast match finder for a window with
// an extDict segment. C: zstd_fast.c:ZSTD_compressBlock_fast_extDict (C
// instantiates the generic function for mls 4..7, any other minMatch uses
// 4; here mls is a value, as in compressBlockFast).
func compressBlockFastExtDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	mls := ms.cParams.minMatch
	if mls < 4 || mls > 7 {
		mls = 4 // includes case 3
	}
	return compressBlockFastExtDictGeneric(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockFastExtDictGeneric.
// C: zstd_fast.c:ZSTD_compressBlock_fast_extDict_generic
//
// Pointers become positions: in the prefix ms.window.base (C's base + idx
// is b[idx-baseIdx]) and in the extDict ms.window.dictBase (C's dictBase +
// idx is d[idx-dictBaseIdx]). C's gotos are the found switch (_match,
// _offset) and loop exits (_cleanup); `goto _start` is the outer loop.
func compressBlockFastExtDictGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hlog := cParams.hashLog
	stepSize := int(cParams.targetLength) + 1 // support stepSize of 0
	if cParams.targetLength == 0 {
		stepSize++
	}
	w := &ms.window
	b, baseIdx := w.base, w.baseIdx
	d, dictBaseIdx := w.dictBase, w.dictBaseIdx
	istart := int(srcIdx - baseIdx)
	anchor := istart
	endIndex := srcIdx + uint32(len(src))
	lowLimit := getLowestMatchIndex(ms, endIndex, cParams.windowLog)
	dictStartIndex := lowLimit
	dictLimit := w.dictLimit
	prefixStartIndex := dictLimit
	if dictLimit < lowLimit {
		prefixStartIndex = lowLimit
	}
	iend := istart + len(src)
	ilimit := iend - 8
	offset1, offset2 := rep[0], rep[1]
	var offsetSaved1, offsetSaved2 uint32

	// switch to "regular" variant if extDict is invalidated due to maxDistance
	if prefixStartIndex == dictStartIndex {
		return compressBlockFast(ms, ss, rep, src, srcIdx)
	}
	if extDictHook != nil {
		extDictHook("fast")
	}
	dictStart := int(dictStartIndex - dictBaseIdx)
	prefixStart := int(prefixStartIndex - baseIdx)
	dictEnd := int(prefixStartIndex - dictBaseIdx)

	hShift, prime := fastHashParams(mls)
	hBits := 64 - hlog
	hash := func(p int) uint32 {
		return uint32(((binary.LittleEndian.Uint64(b[p:]) << hShift) * prime) >> hBits)
	}
	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	read32 := func(p int) uint32 { return binary.LittleEndian.Uint32(b[p:]) }
	// seg returns the segment holding index i and i's position in it.
	seg := func(i uint32) ([]byte, int) {
		if i < prefixStartIndex {
			return d, int(i - dictBaseIdx)
		}
		return b, int(i - baseIdx)
	}
	// segEnd returns the end of the segment holding index i (C: dictEnd or iend).
	segEnd := func(i uint32) int {
		if i < prefixStartIndex {
			return dictEnd
		}
		return iend
	}

	ip0 := istart
	{
		curr := idx(ip0)
		maxRep := curr - dictStartIndex
		if offset2 >= maxRep {
			offsetSaved2, offset2 = offset2, 0
		}
		if offset1 >= maxRep {
			offsetSaved1, offset1 = offset1, 0
		}
	}

	const kStepIncr = 1 << (kSearchStrength - 1)
	for { // C: _start (requires ip0)
		step := stepSize
		nextStep := ip0 + kStepIncr

		// calculate positions, ip0 - anchor == 0, so we skip step calc
		ip1 := ip0 + 1
		ip2 := ip0 + step
		ip3 := ip2 + 1

		if ip3 >= ilimit {
			break // C: goto _cleanup
		}

		hash0 := hash(ip0)
		hash1 := hash(ip1)
		matchIdx := hashTable[hash0] // C: idx; idxBase is seg(matchIdx)

		const (
			found0 = iota // C: _cleanup
			foundRep
			foundOffset
		)
		found := found0
		var current0 uint32
		var match0 []byte
		var m0, mLength, matchEnd int
		var offcode uint32
		// loadMatch is C's "load match for ip[0]": the 4 bytes at matchIdx,
		// or a value guaranteed not to match ip0's.
		loadMatch := func() uint32 {
			if matchIdx >= dictStartIndex {
				s, p := seg(matchIdx)
				return binary.LittleEndian.Uint32(s[p:])
			}
			return read32(ip0) ^ 1 // guaranteed not to match
		}
		for {
			{ // load repcode match for ip[2]
				current2 := idx(ip2)
				repIndex := current2 - offset1
				repBase, repPos := seg(repIndex)
				var rval uint32
				if prefixStartIndex-repIndex >= 4 && offset1 > 0 { // intentional underflow
					rval = binary.LittleEndian.Uint32(repBase[repPos:])
				} else {
					rval = read32(ip2) ^ 1 // guaranteed to not match.
				}

				// write back hash table entry
				current0 = idx(ip0)
				hashTable[hash0] = current0

				// check repcode at ip[2]
				if read32(ip2) == rval {
					ip0 = ip2
					match0, m0 = repBase, repPos
					matchEnd = segEnd(repIndex)
					mLength = 0
					if b[ip0-1] == match0[m0-1] {
						mLength = 1
					}
					ip0 -= mLength
					m0 -= mLength
					offcode = repcode1ToOffBase
					mLength += 4
					found = foundRep
					break // C: goto _match
				}
			}

			// check match at ip[0]
			if read32(ip0) == loadMatch() {
				found = foundOffset
				break // found a match! C: goto _offset
			}

			// lookup ip[1]
			matchIdx = hashTable[hash1]

			// hash ip[2]
			hash0 = hash1
			hash1 = hash(ip2)

			// advance to next positions
			ip0 = ip1
			ip1 = ip2
			ip2 = ip3

			// write back hash table entry
			current0 = idx(ip0)
			hashTable[hash0] = current0

			// check match at ip[0]
			if read32(ip0) == loadMatch() {
				found = foundOffset
				break // found a match! C: goto _offset
			}

			// lookup ip[1]
			matchIdx = hashTable[hash1]

			// hash ip[2]
			hash0 = hash1
			hash1 = hash(ip2)

			// advance to next positions
			ip0 = ip1
			ip1 = ip2
			ip2 = ip0 + step
			ip3 = ip1 + step

			// calculate step
			if ip2 >= nextStep {
				step++
				nextStep += kStepIncr
			}
			if ip3 >= ilimit {
				break
			}
		}
		if found == found0 {
			break // C: _cleanup
		}

		if found == foundOffset { // C: _offset (requires ip0, idx, idxBase)
			// Compute the offset code.
			offset := current0 - matchIdx
			lowMatchPtr := prefixStart
			if matchIdx < prefixStartIndex {
				lowMatchPtr = dictStart
			}
			matchEnd = segEnd(matchIdx)
			match0, m0 = seg(matchIdx)
			offset2 = offset1
			offset1 = offset
			offcode = offsetToOffBase(offset)
			mLength = 4

			// Count the backwards match length.
			for ip0 > anchor && m0 > lowMatchPtr && b[ip0-1] == match0[m0-1] {
				ip0--
				m0--
				mLength++
			}
		}

		// C: _match (requires ip0, match0, offcode, matchEnd)

		// Count the forward length.
		mLength += count2Segments(b, ip0+mLength, match0, m0+mLength, iend, matchEnd, prefixStart)

		storeSeq(ss, ip0-anchor, b[anchor:], offcode, mLength)

		ip0 += mLength
		anchor = ip0

		// write next hash table entry
		if ip1 < ip0 {
			hashTable[hash1] = idx(ip1)
		}

		// Fill table and check for immediate repcode.
		if ip0 <= ilimit {
			// Fill Table
			hashTable[hash(int(current0+2-baseIdx))] = current0 + 2 // here because current+2 could be > iend-8
			hashTable[hash(ip0-2)] = idx(ip0 - 2)

			for ip0 <= ilimit {
				repIndex2 := idx(ip0) - offset2
				repMatch2, rp := seg(repIndex2)
				if indexOverlapCheck(prefixStartIndex, repIndex2) && offset2 > 0 &&
					binary.LittleEndian.Uint32(repMatch2[rp:]) == read32(ip0) {
					repEnd2 := segEnd(repIndex2)
					repLength2 := count2Segments(b, ip0+4, repMatch2, rp+4, iend, repEnd2, prefixStart) + 4
					offset2, offset1 = offset1, offset2 // swap offset_2 <=> offset_1
					storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, repLength2)
					hashTable[hash(ip0)] = idx(ip0)
					ip0 += repLength2
					anchor = ip0
					continue
				}
				break
			}
		}
	}

	// C: _cleanup
	// Note that there are probably still a couple positions we could
	// search. However, it seems to be a meaningful performance hit to try
	// to search them. So let's not.

	// If offset_1 started invalid (offsetSaved1 != 0) and became valid
	// (offset_1 != 0), rotate saved offsets. See comment in
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

// compressBlockFastDictMatchState is the ZSTD_fast match finder searching
// an attached CDict too. C: zstd_fast.c:ZSTD_compressBlock_fast_dictMatchState
// (instantiated for mls 4..7, any other minMatch uses 4; here mls is a
// value, as in compressBlockFast).
func compressBlockFastDictMatchState(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) int {
	mls := ms.cParams.minMatch
	if mls < 4 || mls > 7 {
		mls = 4 // includes case 3
	}
	return compressBlockFastDictMatchStateGeneric(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockFastDictMatchStateGeneric.
// C: zstd_fast.c:ZSTD_compressBlock_fast_dictMatchState_generic
//
// Pointers become positions: in the prefix ms.window.base (C's base + idx
// is b[idx-baseIdx]) and in the dictionary dms.window.base (C's dictBase +
// idx is d[idx-dictBaseIdx]). Indices below prefixStartIndex are the
// dictionary's, translated by dictIndexDelta. C's prefetch of the
// dictionary's hash table (prefetchCDictTables, off by default) is a cache
// hint only.
func compressBlockFastDictMatchStateGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if dictMatchStateHook != nil {
		dictMatchStateHook("fast")
	}
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hlog := cParams.hashLog
	// support stepSize of 0
	stepSize := int(cParams.targetLength)
	if cParams.targetLength == 0 {
		stepSize = 1
	}
	b, baseIdx := ms.window.base, ms.window.baseIdx
	istart := int(srcIdx - baseIdx)
	ip0 := istart
	ip1 := ip0 + stepSize // we assert below that stepSize >= 1
	anchor := istart
	prefixStartIndex := ms.window.dictLimit
	prefixStart := int(prefixStartIndex - baseIdx)
	iend := istart + len(src)
	ilimit := iend - hashReadSize
	offset1, offset2 := rep[0], rep[1]

	dms := ms.dictMatchState
	dictCParams := &dms.cParams
	dictHashTable := dms.hashTable
	dictStartIndex := dms.window.dictLimit
	d, dictBaseIdx := dms.window.base, dms.window.baseIdx
	dictStart := int(dictStartIndex - dictBaseIdx)
	dictEndIdx := dms.window.nextSrc // C: dictEnd - dictBase
	dictEnd := int(dictEndIdx - dictBaseIdx)
	dictIndexDelta := prefixStartIndex - dictEndIdx
	dictAndPrefixLength := uint32(ip0-prefixStart) + uint32(dictEnd-dictStart)
	dictHBits := dictCParams.hashLog + shortCacheTagBits

	read32 := func(s []byte, p int) uint32 { return binary.LittleEndian.Uint32(s[p:]) }
	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	// rep returns the segment and position of the repcode match at index
	// repIndex, and its segment's end (C: repMatch, repMatchEnd).
	repAt := func(repIndex uint32) ([]byte, int, int) {
		if repIndex < prefixStartIndex {
			return d, int(repIndex - dictIndexDelta - dictBaseIdx), dictEnd
		}
		return b, int(repIndex - baseIdx), iend
	}

	// init
	if dictAndPrefixLength == 0 {
		ip0++
	}

	// Outer search loop
	const kStepIncr = 1 << kSearchStrength
outer:
	for ip1 <= ilimit { // repcode check at (ip0 + 1) is safe because ip0 < ip1
		var mLength int
		hash0 := hashPtr(b, ip0, hlog, mls)

		dictHashAndTag0 := hashPtr(b, ip0, dictHBits, mls)
		dictMatchIndexAndTag := dictHashTable[dictHashAndTag0>>shortCacheTagBits]
		dictTagsMatch := comparePackedTags(dictMatchIndexAndTag, dictHashAndTag0)

		matchIndex := hashTable[hash0]
		curr := idx(ip0)
		step := stepSize
		nextStep := ip0 + kStepIncr

		// Inner search loop
		for {
			match := int(matchIndex - baseIdx)
			repIndex := curr + 1 - offset1
			repBase, repPos, repMatchEnd := repAt(repIndex)
			hash1 := hashPtr(b, ip1, hlog, mls)
			dictHashAndTag1 := hashPtr(b, ip1, dictHBits, mls)
			hashTable[hash0] = curr // update hash table

			if indexOverlapCheck(prefixStartIndex, repIndex) && read32(repBase, repPos) == read32(b, ip0+1) {
				mLength = count2Segments(b, ip0+1+4, repBase, repPos+4, iend, repMatchEnd, prefixStart) + 4
				ip0++
				storeSeq(ss, ip0-anchor, b[anchor:], repcode1ToOffBase, mLength)
				break
			}

			if dictTagsMatch {
				// Found a possible dict match
				dictMatchIndex := dictMatchIndexAndTag >> shortCacheTagBits
				dictMatch := int(dictMatchIndex - dictBaseIdx)
				if dictMatchIndex > dictStartIndex && read32(d, dictMatch) == read32(b, ip0) {
					// To replicate extDict parse behavior, we only use dict
					// matches when the normal matchIndex is invalid
					if matchIndex <= prefixStartIndex {
						offset := curr - dictMatchIndex - dictIndexDelta
						mLength = count2Segments(b, ip0+4, d, dictMatch+4, iend, dictEnd, prefixStart) + 4
						for ip0 > anchor && dictMatch > dictStart && b[ip0-1] == d[dictMatch-1] {
							ip0--
							dictMatch--
							mLength++
						} // catch up
						offset2 = offset1
						offset1 = offset
						storeSeq(ss, ip0-anchor, b[anchor:], offsetToOffBase(offset), mLength)
						break
					}
				}
			}

			// C: ZSTD_match4Found_cmov(ip0, match, matchIndex, prefixStartIndex)
			if matchIndex >= prefixStartIndex && read32(b, ip0) == read32(b, match) {
				// found a regular match of size >= 4
				offset := uint32(ip0 - match)
				mLength = count(b, ip0+4, match+4, iend) + 4
				for ip0 > anchor && match > prefixStart && b[ip0-1] == b[match-1] {
					ip0--
					match--
					mLength++
				} // catch up
				offset2 = offset1
				offset1 = offset
				storeSeq(ss, ip0-anchor, b[anchor:], offsetToOffBase(offset), mLength)
				break
			}

			// Prepare for next iteration
			dictMatchIndexAndTag = dictHashTable[dictHashAndTag1>>shortCacheTagBits]
			dictTagsMatch = comparePackedTags(dictMatchIndexAndTag, dictHashAndTag1)
			matchIndex = hashTable[hash1]

			if ip1 >= nextStep {
				step++
				nextStep += kStepIncr
			}
			ip0 = ip1
			ip1 = ip1 + step
			if ip1 > ilimit {
				break outer // C: goto _cleanup
			}

			curr = idx(ip0)
			hash0 = hash1
		} // end inner search loop

		// match found
		ip0 += mLength
		anchor = ip0

		if ip0 <= ilimit {
			// Fill Table
			hashTable[hashPtr(b, int(curr+2-baseIdx), hlog, mls)] = curr + 2 // here because curr+2 could be > iend-8
			hashTable[hashPtr(b, ip0-2, hlog, mls)] = idx(ip0 - 2)

			// check immediate repcode
			for ip0 <= ilimit {
				current2 := idx(ip0)
				repIndex2 := current2 - offset2
				repBase2, repPos2, repEnd2 := repAt(repIndex2)
				if indexOverlapCheck(prefixStartIndex, repIndex2) && read32(repBase2, repPos2) == read32(b, ip0) {
					repLength2 := count2Segments(b, ip0+4, repBase2, repPos2+4, iend, repEnd2, prefixStart) + 4
					offset2, offset1 = offset1, offset2 // swap offset_2 <=> offset_1
					storeSeq(ss, 0, b[anchor:], repcode1ToOffBase, repLength2)
					hashTable[hashPtr(b, ip0, hlog, mls)] = current2
					ip0 += repLength2
					anchor = ip0
					continue
				}
				break
			}
		}

		// Prepare for next iteration
		ip1 = ip0 + stepSize
	}

	// C: _cleanup
	// save reps for next block
	rep[0] = offset1
	rep[1] = offset2

	// Return the last literals size
	return iend - anchor
}
