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
	return compressBlockFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
}

// compressBlockFastNoDictGeneric. C: zstd_fast.c:ZSTD_compressBlock_fast_noDict_generic
//
// Pointers become positions in ms.window.base (C's base + idx is
// b[idx-baseIdx]); the hash table stores C's indices.
//
// The search interleaves hash, table lookup, match load and compare over the
// next few positions (see the C source for the pipeline diagram): ip0 and
// ip1 are always adjacent, and step is the gap between pairs of positions,
// from ip0 to ip2 or ip1 to ip3. The targetLength skipping and
// uncompressibility acceleration is applied to every other position.
func compressBlockFastNoDictGeneric(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	cParams := &ms.cParams
	hashTable := ms.hashTable
	hlog := cParams.hashLog
	stepSize := int(cParams.targetLength) + 1 // min 2
	if cParams.targetLength == 0 {
		stepSize++
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	endIndex := srcIdx + uint32(len(src))
	prefixStartIndex := getLowestPrefixIndex(ms, endIndex, cParams.windowLog)
	prefixStart := int(prefixStartIndex - baseIdx)
	istart := int(srcIdx - baseIdx)
	iend := istart + len(src)
	ilimit := iend - hashReadSize

	hShift, prime := fastHashParams(mls)
	hBits := 64 - hlog
	hash := func(p int) uint32 {
		return uint32(((binary.LittleEndian.Uint64(b[p:]) << hShift) * prime) >> hBits)
	}
	idx := func(p int) uint32 { return uint32(p) + baseIdx }
	read32 := func(p int) uint32 { return binary.LittleEndian.Uint32(b[p:]) }

	anchor := istart
	ip0 := istart

	repOffset1 := rep[0]
	repOffset2 := rep[1]
	var offsetSaved1, offsetSaved2 uint32

	const kStepIncr = 1 << (kSearchStrength - 1)

	if ip0 == prefixStart {
		ip0++
	}
	{
		curr := idx(ip0)
		windowLow := getLowestPrefixIndex(ms, curr, cParams.windowLog)
		maxRep := curr - windowLow
		if repOffset2 > maxRep {
			offsetSaved2, repOffset2 = repOffset2, 0
		}
		if repOffset1 > maxRep {
			offsetSaved1, repOffset1 = repOffset1, 0
		}
	}

	var current0 uint32
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
		matchIdx := hashTable[hash0]

		const (
			found0 = iota // no match: C's loop exit to _cleanup
			foundRep
			foundOffset
		)
		found := found0
		for {
			// load repcode match for ip[2]
			rval := read32(ip2 - int(repOffset1))

			// write back hash table entry
			current0 = idx(ip0)
			hashTable[hash0] = current0

			// check repcode at ip[2]
			if read32(ip2) == rval && repOffset1 > 0 {
				// Write next hash table entry: it's already calculated.
				// This write is known to be safe because ip1 is before the
				// repcode (ip2).
				hashTable[hash1] = idx(ip1)
				found = foundRep
				break // C: goto _match (setup below)
			}

			if matchIdx >= prefixStartIndex && read32(ip0) == read32(int(matchIdx-baseIdx)) {
				// Write next hash table entry (it's already calculated).
				// This write is known to be safe because the ip1 == ip0 + 1,
				// so searching will resume after ip1
				hashTable[hash1] = idx(ip1)
				found = foundOffset
				break // C: goto _offset
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

			if matchIdx >= prefixStartIndex && read32(ip0) == read32(int(matchIdx-baseIdx)) {
				// Write next hash table entry, since it's already calculated
				if step <= 4 {
					// Avoid writing an index if it's >= position where search
					// will resume. The minimum possible match has length 4,
					// so search can resume at ip0 + 4.
					hashTable[hash1] = idx(ip1)
				}
				found = foundOffset
				break // C: goto _offset
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

		var match0, mLength int
		var offcode uint32
		if found == foundRep {
			ip0 = ip2
			match0 = ip0 - int(repOffset1)
			if b[ip0-1] == b[match0-1] {
				mLength = 1
			}
			ip0 -= mLength
			match0 -= mLength
			offcode = repcode1ToOffBase
			mLength += 4
		} else { // C: _offset (requires ip0, matchIdx)
			// Compute the offset code.
			match0 = int(matchIdx - baseIdx)
			repOffset2 = repOffset1
			repOffset1 = uint32(ip0 - match0)
			offcode = offsetToOffBase(repOffset1)
			mLength = 4

			// Count the backwards match length.
			for ip0 > anchor && match0 > prefixStart && b[ip0-1] == b[match0-1] {
				ip0--
				match0--
				mLength++
			}
		}

		// C: _match (requires ip0, match0, offcode)

		// Count the forward length.
		mLength += count(b, ip0+mLength, match0+mLength, iend)

		storeSeq(ss, ip0-anchor, b[anchor:], offcode, mLength)

		ip0 += mLength
		anchor = ip0

		// Fill table and check for immediate repcode.
		if ip0 <= ilimit {
			// Fill Table
			hashTable[hash(int(current0+2-baseIdx))] = current0 + 2 // here because current+2 could be > iend-8
			hashTable[hash(ip0-2)] = idx(ip0 - 2)

			if repOffset2 > 0 { // repOffset2==0 means repOffset2 is invalidated
				for ip0 <= ilimit && read32(ip0) == read32(ip0-int(repOffset2)) {
					// store sequence
					rLength := count(b, ip0+4, ip0+4-int(repOffset2), iend) + 4
					repOffset2, repOffset1 = repOffset1, repOffset2 // swap repOffset2 <=> repOffset1
					hashTable[hash(ip0)] = idx(ip0)
					ip0 += rLength
					storeSeq(ss, 0, nil, repcode1ToOffBase, rLength)
					anchor = ip0
				}
			}
		}
	}

	// C: _cleanup
	// Note that there are probably still a couple positions one could
	// search. However, it seems to be a meaningful performance hit to try
	// to search them. So let's not.

	// When the repcodes are outside of the prefix, we set them to zero
	// before the loop. When the offsets are still zero, we need to restore
	// them after the block to have a correct repcode history. If only one
	// offset was invalid, it is easy. The tricky case is when both offsets
	// were invalid. We need to figure out which offset to refill with.
	//   - If both offsets are zero they are in the same order.
	//   - If both offsets are non-zero, we won't restore the offsets from
	//     offsetSaved[12].
	//   - If only one is zero, we need to decide which offset to restore.
	//     - If repOffset1 is non-zero, then repOffset2 must be offsetSaved1.
	//     - It is impossible for repOffset2 to be non-zero.
	//
	// So if repOffset1 started invalid (offsetSaved1 != 0) and became valid
	// (repOffset1 != 0), then set rep[0] = repOffset1 and rep[1] =
	// offsetSaved1.
	if offsetSaved1 != 0 && repOffset1 != 0 {
		offsetSaved2 = offsetSaved1
	}

	// save reps for next block
	rep[0] = repOffset1
	if repOffset1 == 0 {
		rep[0] = offsetSaved1
	}
	rep[1] = repOffset2
	if repOffset2 == 0 {
		rep[1] = offsetSaved2
	}

	// Return the last literals size
	return iend - anchor
}
