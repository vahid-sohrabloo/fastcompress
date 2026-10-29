// Port of libzstd v1.5.7 lib/compress/zstd_double_fast.c (Copyright (c)
// Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license).
//
// The Go body of the no-dictionary double-fast match finder, always
// compiled; on amd64 zstd_double_fast_amd64.s replaces its main loop.

package zstd

import "encoding/binary"

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
