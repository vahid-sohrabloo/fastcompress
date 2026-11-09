// Port of libdeflate v1.26 lib/hc_matchfinder.h (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the hash chains matchfinder of the greedy and
// lazy compressors (struct hc_matchfinder, HC_MATCHFINDER_HASH3_ORDER,
// HC_MATCHFINDER_HASH4_ORDER, HC_MATCHFINDER_TOTAL_HASH_SIZE,
// hc_matchfinder_init, hc_matchfinder_slide_window,
// hc_matchfinder_longest_match and hc_matchfinder_skip_bytes).
//
// Length 3 matches are found through a single-entry hash table (hash3_tab,
// the most recent position of each 3-byte hash); length 4+ matches through
// hash chains: hash4_tab holds the most recent position of each 4-byte hash
// and next_tab[pos] the previous position with the same 4-byte hash, so a
// chain is walked most recent first, at most max_search_depth nodes.
// Positions are mf_pos_t (s16) relative to in_base, which moves by
// MATCHFINDER_WINDOW_SIZE at every window slide; positions a full window or
// more behind the current one are out of range (<= cutoff).
//
// The port follows C's UNALIGNED_ACCESS_IS_FAST branch (the one built on
// x86 and arm64): the match candidates of the length 5+ search are first
// checked with 4-byte loads of their last and first 4 bytes. The prefetchw
// calls have no Go counterpart.

package deflate

import "encoding/binary"

// C: HC_MATCHFINDER_HASH3_ORDER, HC_MATCHFINDER_HASH4_ORDER
const (
	hcMatchfinderHash3Order = 15
	hcMatchfinderHash4Order = 16
)

// The size in bytes of the hash tables (hash3_tab and hash4_tab, which C's
// hc_matchfinder_init initializes as one block; next_tab is not
// initialized). C: HC_MATCHFINDER_TOTAL_HASH_SIZE
const hcMatchfinderTotalHashSize = ((1 << hcMatchfinderHash3Order) + (1 << hcMatchfinderHash4Order)) * 2

// C: STATIC_ASSERT(HC_MATCHFINDER_TOTAL_HASH_SIZE %
// MATCHFINDER_SIZE_ALIGNMENT == 0) in hc_matchfinder_init and
// STATIC_ASSERT(sizeof(*mf) % MATCHFINDER_SIZE_ALIGNMENT == 0) in
// hc_matchfinder_slide_window.
const (
	_ = uint(0 - hcMatchfinderTotalHashSize%matchfinderSizeAlignment)
	_ = uint(0 - (hcMatchfinderTotalHashSize+2*matchfinderWindowSize)%matchfinderSizeAlignment)
)

// hcMatchfinder is the hash chains matchfinder. C: struct hc_matchfinder
type hcMatchfinder struct {
	// The hash table for finding length 3 matches. C: hash3_tab
	hash3Tab [1 << hcMatchfinderHash3Order]mfPos

	// The hash table which contains the first nodes of the linked lists
	// for finding length 4+ matches. C: hash4_tab
	hash4Tab [1 << hcMatchfinderHash4Order]mfPos

	// The "next node" references for the linked lists: the "next node" of
	// the node for the sequence with position pos is nextTab[pos].
	// C: next_tab
	nextTab [matchfinderWindowSize]mfPos
}

// Index masks (the hashes and window positions are always in range; the
// masks only let the compiler drop the bounds checks).
const (
	hcHash3Mask = 1<<hcMatchfinderHash3Order - 1
	hcHash4Mask = 1<<hcMatchfinderHash4Order - 1
	hcPosMask   = matchfinderWindowSize - 1
)

// init prepares the matchfinder for a new input buffer: the hash tables
// are emptied (next_tab is left as is, like C: a chain only reaches nodes
// whose next_tab entry was written for this buffer).
// port of hc_matchfinder_init
func (mf *hcMatchfinder) init() {
	matchfinderInit(mf.hash3Tab[:])
	matchfinderInit(mf.hash4Tab[:])
}

// slideWindow rebases every position (the hash tables and next_tab) by
// MATCHFINDER_WINDOW_SIZE. port of hc_matchfinder_slide_window
func (mf *hcMatchfinder) slideWindow() {
	matchfinderRebase(mf.hash3Tab[:])
	matchfinderRebase(mf.hash4Tab[:])
	matchfinderRebase(mf.nextTab[:])
}

// longestMatch finds the longest match longer than bestLen bytes (at most
// maxLen; the search stops once niceLen is reached; at most maxSearchDepth
// >= 1 length 4+ candidates are considered) of the bytes at in[inNext:],
// and inserts inNext into the matchfinder. It returns the match length, or
// bestLen if no longer match was found, and the match offset (0 if none was
// found). niceLen must be <= maxLen and maxLen <= len(in) - inNext.
//
// *inBase is the window base (C: *in_base_p, a pointer into in, here an
// index): when inNext is a full window past it the matchfinder is slid and
// the base advanced. nextHashes holds the 3- and 4-byte hashes of the bytes
// at inNext on entry and those at inNext+1 on return. If maxLen < 5 (so the
// 4 bytes at inNext+1 cannot be read) nothing is searched, inserted or
// hashed. The lazy compressors call it; deflateCompressGreedy has it
// written out.
// port of hc_matchfinder_longest_match
func (mf *hcMatchfinder) longestMatch(in []byte, inBase *int, inNext int, bestLen, maxLen, niceLen,
	maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32) {
	depthRemaining := maxSearchDepth
	bestMatchptr := inNext
	curPos := uint32(inNext - *inBase)

	if curPos == matchfinderWindowSize {
		mf.slideWindow()
		*inBase += matchfinderWindowSize
		curPos = 0
	}
	base := *inBase
	// C: mf_pos_t cutoff = cur_pos - MATCHFINDER_WINDOW_SIZE (truncated to
	// 16 bits).
	cutoff := mfPos(curPos - matchfinderWindowSize)

	if maxLen < 5 { // can we read 4 bytes from 'in_next + 1'?
		return bestLen, 0
	}

	// Get the precomputed hash codes.
	hash3 := nextHashes[0] & hcHash3Mask
	hash4 := nextHashes[1] & hcHash4Mask

	// From the hash buckets, get the first node of each linked list.
	curNode3 := mf.hash3Tab[hash3]
	curNode4 := mf.hash4Tab[hash4]

	// Update for length 3 matches. This replaces the singleton node in the
	// 'hash3' bucket with the node for the current sequence.
	mf.hash3Tab[hash3] = mfPos(curPos)

	// Update for length 4 matches. This prepends the node for the current
	// sequence to the linked list in the 'hash4' bucket.
	mf.hash4Tab[hash4] = mfPos(curPos)
	mf.nextTab[curPos&hcPosMask] = curNode4

	// Compute the next hash codes.
	nextHashseq := loadLE32(in, inNext+1)
	nextHashes[0] = lzHash(nextHashseq&0xFFFFFF, hcMatchfinderHash3Order)
	nextHashes[1] = lzHash(nextHashseq, hcMatchfinderHash4Order)

	var matchptr int
	if bestLen < 4 { // No match of length >= 4 found yet?
		// Check for a length 3 match if needed.
		if curNode3 <= cutoff {
			return bestLen, uint32(inNext - bestMatchptr)
		}

		seq4 := loadLE32(in, inNext)

		if bestLen < 3 {
			matchptr = base + int(curNode3)
			if loadLE32(in, matchptr)&0xFFFFFF == seq4&0xFFFFFF {
				bestLen = 3
				bestMatchptr = matchptr
			}
		}

		// Check for a length 4 match.
		if curNode4 <= cutoff {
			return bestLen, uint32(inNext - bestMatchptr)
		}

		for {
			// No length 4 match found yet. Check the first 4 bytes.
			matchptr = base + int(curNode4)

			if loadLE32(in, matchptr) == seq4 {
				break
			}

			// The first 4 bytes did not match. Keep trying.
			curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
			depthRemaining--
			if curNode4 <= cutoff || depthRemaining == 0 {
				return bestLen, uint32(inNext - bestMatchptr)
			}
		}

		// Found a match of length >= 4. Extend it to its full length.
		bestMatchptr = matchptr
		bestLen = lzExtend(in, inNext, bestMatchptr, 4, maxLen)
		if bestLen >= niceLen {
			return bestLen, uint32(inNext - bestMatchptr)
		}
		curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
		depthRemaining--
		if curNode4 <= cutoff || depthRemaining == 0 {
			return bestLen, uint32(inNext - bestMatchptr)
		}
	} else if curNode4 <= cutoff || bestLen >= niceLen {
		return bestLen, uint32(inNext - bestMatchptr)
	}

	// Check for matches of length >= 5. The 4-byte values at inNext and at
	// inNext+bestLen-3 do not change while bestLen does not; they are loaded
	// once per bestLen (C's compiler hoists them out of the loop).
	seqFirst := loadLE32(in, inNext)
	for {
		seqLast := loadLE32(in, inNext+int(bestLen)-3)
		for {
			matchptr = base + int(curNode4)

			// Already found a length 4 match. Try for a longer match;
			// start by checking the last 4 bytes and the first 4 bytes.
			// (The last byte, the one which would extend the match length
			// by 1, is the most important.)
			if loadLE32(in, matchptr+int(bestLen)-3) == seqLast &&
				loadLE32(in, matchptr) == seqFirst {
				break
			}

			// Continue to the next node in the list.
			curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
			depthRemaining--
			if curNode4 <= cutoff || depthRemaining == 0 {
				return bestLen, uint32(inNext - bestMatchptr)
			}
		}

		if l := lzExtend(in, inNext, matchptr, 4, maxLen); l > bestLen {
			// This is the new longest match.
			bestLen = l
			bestMatchptr = matchptr
			if bestLen >= niceLen {
				return bestLen, uint32(inNext - bestMatchptr)
			}
		}

		// Continue to the next node in the list.
		curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
		depthRemaining--
		if curNode4 <= cutoff || depthRemaining == 0 {
			return bestLen, uint32(inNext - bestMatchptr)
		}
	}
}

// skipBytes advances the matchfinder by count (>= 1) positions, inNext..
// inNext+count-1, without searching for matches (the positions covered by
// a chosen match), and leaves the hashes of the bytes at inNext+count in
// nextHashes (on entry they are those of the bytes at inNext). It does
// nothing (not even update nextHashes) when fewer than count + 5 bytes
// remain before inEnd: the caller then has fewer than 5 bytes left, for
// which longestMatch searches no more.
// port of hc_matchfinder_skip_bytes
func (mf *hcMatchfinder) skipBytes(in []byte, inBase *int, inNext, inEnd int, count uint32,
	nextHashes *[2]uint32) {
	if int(count)+5 > inEnd-inNext {
		// Invariant: this early return can leave the next position more
		// than a full window past *inBase without a slide (cur_pos >
		// 32768). It is only taken when fewer than 5 bytes remain after
		// the skipped ones, and longestMatch then returns (max_len < 5)
		// before touching the tables; any new caller must keep that.
		return
	}

	curPos := uint32(inNext - *inBase)
	hash3 := nextHashes[0]
	hash4 := nextHashes[1]
	// next holds the bytes at inNext+1 .. inNext+count+3: each step hashes
	// its first 4 bytes and then drops its first byte (C advances in_next),
	// so that the loads need no bounds checks.
	next := in[inNext+1 : inNext+int(count)+4]
	for len(next) >= 4 {
		if curPos == matchfinderWindowSize {
			mf.slideWindow()
			*inBase += matchfinderWindowSize
			curPos = 0
		}
		mf.hash3Tab[hash3&hcHash3Mask] = mfPos(curPos)
		mf.nextTab[curPos&hcPosMask] = mf.hash4Tab[hash4&hcHash4Mask]
		mf.hash4Tab[hash4&hcHash4Mask] = mfPos(curPos)

		nextHashseq := binary.LittleEndian.Uint32(next)
		next = next[1:]
		hash3 = lzHash(nextHashseq&0xFFFFFF, hcMatchfinderHash3Order)
		hash4 = lzHash(nextHashseq, hcMatchfinderHash4Order)
		curPos++
	}
	nextHashes[0] = hash3
	nextHashes[1] = hash4
}
