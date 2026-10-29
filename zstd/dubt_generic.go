// Port of libzstd v1.5.7 lib/compress/zstd_lazy.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the binary-tree
// (DUBT) insertion and search loops, always compiled; on amd64
// dubt_amd64.s has equivalent ones.

package zstd

// insertDUBT1Generic sorts one already inserted but unsorted position curr into
// the tree. inputEnd is the end of the input. Assumption: curr >= btLow ==
// (curr - btMask). Doesn't fail.
// C: zstd_lazy.c:ZSTD_insertDUBT1 with dictMode ZSTD_noDict (curr and its
// candidates are always in the prefix, so ip is base + curr, iend is
// inputEnd and every match is read from base).
func insertDUBT1Generic(ms *matchState, curr uint32, inputEnd int, nbCompares, btLow uint32) {
	bt := ms.chainTable
	btLog := ms.cParams.chainLog - 1
	btMask := uint32(1)<<btLog - 1
	var commonLengthSmaller, commonLengthLarger int
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	ip := int(curr - baseIdx)
	iend := inputEnd
	smallerPtr := &bt[2*(curr&btMask)]
	largerPtr := &bt[2*(curr&btMask)+1]
	// this candidate is unsorted : next sorted candidate is reached through
	// *smallerPtr, while *largerPtr contains previous unsorted candidate
	// (which is already saved and can be overwritten)
	matchIndex := *smallerPtr
	var dummy32 uint32 // to be nullified at the end
	windowValid := ms.window.lowLimit
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
		match := int(matchIndex - baseIdx)
		matchLength += count(b, ip+matchLength, match+matchLength, iend)

		if ip+matchLength == iend { // equal : no way to know if inf or sup
			break // drop , to guarantee consistency ; miss a bit of compression, but other solutions can corrupt tree
		}

		if b[match+matchLength] < b[ip+matchLength] { // necessarily within buffer
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

// dubtSearchGeneric walks the tree from matchIndex (the previous head of
// ip's hash chain) for the longest match at ip, inserting ip (index curr)
// into the tree. It returns the best length (0 if none), its offBase
// (offBaseNotFound if none) and matchEndIdx (C: the skip of repetitive
// patterns). C: zstd_lazy.c:ZSTD_DUBT_findBestMatch, "find longest match".
func dubtSearchGeneric(ms *matchState, ip, iend int, curr, matchIndex, nbCompares, btLow, windowLow uint32) (int, uint32, uint32) {
	bt := ms.chainTable
	btMask := uint32(1)<<(ms.cParams.chainLog-1) - 1
	b := ms.window.base
	baseIdx := ms.window.baseIdx
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

	return bestLength, offBase, matchEndIdx
}
