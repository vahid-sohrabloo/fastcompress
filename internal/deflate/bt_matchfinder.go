// Port of libdeflate v1.26 lib/bt_matchfinder.h (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the binary trees matchfinder of the
// near-optimal compressor (struct lz_match, struct bt_matchfinder,
// BT_MATCHFINDER_HASH3_ORDER, BT_MATCHFINDER_HASH3_WAYS,
// BT_MATCHFINDER_HASH4_ORDER, BT_MATCHFINDER_TOTAL_HASH_SIZE,
// BT_MATCHFINDER_REQUIRED_NBYTES, bt_matchfinder_init,
// bt_matchfinder_slide_window, bt_left_child, bt_right_child,
// bt_matchfinder_advance_one_byte, bt_matchfinder_get_matches and
// bt_matchfinder_skip_byte).
//
// Each hash bucket of hash4_tab holds the root of a binary tree of the
// sequences whose first 4 bytes share the hash code, sorted so that a left
// child is lexicographically lesser and a right child greater than its
// parent. At each position the tree of the current sequence is searched for
// matches and re-rooted at the new node in a single traversal. Length 3
// matches come from a 2-way hash table (hash3_tab). Positions are mf_pos_t
// (s16) relative to in_base, which the compressor moves by
// MATCHFINDER_WINDOW_SIZE at every window slide (the matchfinder itself never
// slides here, unlike the hash chains one).
//
// C's bt_matchfinder_advance_one_byte has a compile-time record_matches
// parameter; it is written out twice here, as getMatches (true) and skipByte
// (false). The prefetchw calls have no Go counterpart.

package deflate

// C: BT_MATCHFINDER_HASH3_ORDER, BT_MATCHFINDER_HASH3_WAYS,
// BT_MATCHFINDER_HASH4_ORDER, BT_MATCHFINDER_REQUIRED_NBYTES
const (
	btMatchfinderHash3Order = 16
	btMatchfinderHash3Ways  = 2
	btMatchfinderHash4Order = 16

	// The minimum permissible value of max_len for getMatches and skipByte:
	// there must be enough bytes remaining to load a 32-bit integer from
	// the next position.
	btMatchfinderRequiredNbytes = 5
)

// The size in bytes of the hash tables (hash3_tab and hash4_tab, which
// bt_matchfinder_init initializes as one block; child_tab is not
// initialized). C: BT_MATCHFINDER_TOTAL_HASH_SIZE
const btMatchfinderTotalHashSize = ((1<<btMatchfinderHash3Order)*btMatchfinderHash3Ways +
	(1 << btMatchfinderHash4Order)) * 2

// C: STATIC_ASSERT(BT_MATCHFINDER_TOTAL_HASH_SIZE %
// MATCHFINDER_SIZE_ALIGNMENT == 0) in bt_matchfinder_init,
// STATIC_ASSERT(sizeof(*mf) % MATCHFINDER_SIZE_ALIGNMENT == 0) in
// bt_matchfinder_slide_window and STATIC_ASSERT(BT_MATCHFINDER_HASH3_WAYS
// >= 1 && BT_MATCHFINDER_HASH3_WAYS <= 2) in advance_one_byte (the port
// is written for exactly 2 ways).
const (
	_ = uint(0 - btMatchfinderTotalHashSize%matchfinderSizeAlignment)
	_ = uint(0 - (btMatchfinderTotalHashSize+2*2*matchfinderWindowSize)%matchfinderSizeAlignment)
	_ = uint(btMatchfinderHash3Ways-2) + uint(2-btMatchfinderHash3Ways)
)

// lzMatch represents a match found by the bt_matchfinder. C: struct lz_match
type lzMatch struct {
	// The number of bytes matched. C: length (u16)
	length uint16

	// The offset back from the current position that was matched.
	// C: offset (u16)
	offset uint16
}

// btMatchfinder is the binary trees matchfinder. C: struct bt_matchfinder
type btMatchfinder struct {
	// The hash table for finding length 3 matches, C's
	// hash3_tab[1 << 16][2] in its row-major layout: bucket h is entries
	// 2h (most recent) and 2h+1. C: hash3_tab
	hash3Tab [(1 << btMatchfinderHash3Order) * btMatchfinderHash3Ways]mfPos

	// The hash table which contains the roots of the binary trees for
	// finding length 4+ matches. C: hash4_tab
	hash4Tab [1 << btMatchfinderHash4Order]mfPos

	// The child node references for the binary trees: the left and right
	// children of the node for the sequence with position pos are
	// childTab[pos*2] and childTab[pos*2+1]. C: child_tab
	childTab [2 * matchfinderWindowSize]mfPos
}

// Index masks (the hashes are always in range; the masks only let the
// compiler drop the bounds checks).
const (
	btHash3Mask = 1<<btMatchfinderHash3Order - 1
	btHash4Mask = 1<<btMatchfinderHash4Order - 1
	btChildMask = 2*matchfinderWindowSize - 1
)

// init prepares the matchfinder for a new input buffer: the hash tables
// are emptied (child_tab is left as is, like C: a tree only reaches nodes
// whose children were written for this buffer).
// port of bt_matchfinder_init
func (mf *btMatchfinder) init() {
	matchfinderInit(mf.hash3Tab[:])
	matchfinderInit(mf.hash4Tab[:])
}

// slideWindow rebases every position (the hash tables and child_tab) by
// MATCHFINDER_WINDOW_SIZE. port of bt_matchfinder_slide_window
func (mf *btMatchfinder) slideWindow() {
	matchfinderRebase(mf.hash3Tab[:])
	matchfinderRebase(mf.hash4Tab[:])
	matchfinderRebase(mf.childTab[:])
}

// btLeftChild and btRightChild return the child_tab index of the left /
// right child of node. port of bt_left_child, bt_right_child
func btLeftChild(node int32) int  { return int(2*(node&(matchfinderWindowSize-1))+0) & btChildMask }
func btRightChild(node int32) int { return int(2*(node&(matchfinderWindowSize-1))+1) & btChildMask }

// getMatches records the matches of the bytes at in[inBase+curPos:] in
// matches (sorted by strictly increasing length and non-strictly
// increasing offset; at most niceLen - 2 of them, and never more than
// MAX_MATCHES_PER_POS) and returns how many were recorded; the position is
// inserted into the matchfinder (its tree re-rooted at it).
//
// inBase is the position, in in, of the window base (C: in_base, the next
// input byte at the last init or slide_window); 0 <= curPos <
// MATCHFINDER_WINDOW_SIZE. BT_MATCHFINDER_REQUIRED_NBYTES <= maxLen <=
// len(in) - (inBase+curPos), niceLen <= maxLen, maxSearchDepth >= 1.
// nextHashes holds the 3- and 4-byte hashes of the bytes at the position on
// entry and those of the next position on return.
// port of bt_matchfinder_get_matches (bt_matchfinder_advance_one_byte with
// record_matches = true)
func (mf *btMatchfinder) getMatches(in []byte, inBase, curPos int, maxLen, niceLen, maxSearchDepth uint32,
	nextHashes *[2]uint32, matches *[maxMatchesPerPos]lzMatch) int {
	inNext := inBase + curPos
	depthRemaining := maxSearchDepth
	cutoff := int32(curPos) - matchfinderWindowSize
	bestLen := uint32(3)
	n := 0

	nextHashseq := loadLE32(in, inNext+1)

	hash3 := nextHashes[0] & btHash3Mask
	hash4 := nextHashes[1] & btHash4Mask

	nextHashes[0] = lzHash(nextHashseq&0xFFFFFF, btMatchfinderHash3Order)
	nextHashes[1] = lzHash(nextHashseq, btMatchfinderHash4Order)

	bucket3 := (*[btMatchfinderHash3Ways]mfPos)(mf.hash3Tab[2*hash3:])
	curNode := int32(bucket3[0])
	bucket3[0] = mfPos(curPos)
	curNode2 := int32(bucket3[1])
	bucket3[1] = mfPos(curNode)
	if curNode > cutoff {
		seq3 := loadLE32(in, inNext) & 0xFFFFFF // C: load_u24_unaligned
		if seq3 == loadLE32(in, inBase+int(curNode))&0xFFFFFF {
			matches[n&(maxMatchesPerPos-1)] = lzMatch{3, uint16(curPos - int(curNode))}
			n++
		} else if curNode2 > cutoff && seq3 == loadLE32(in, inBase+int(curNode2))&0xFFFFFF {
			matches[n&(maxMatchesPerPos-1)] = lzMatch{3, uint16(curPos - int(curNode2))}
			n++
		}
	}

	curNode = int32(mf.hash4Tab[hash4])
	mf.hash4Tab[hash4] = mfPos(curPos)

	child := &mf.childTab
	pendingLt := btLeftChild(int32(curPos))
	pendingGt := btRightChild(int32(curPos))

	if curNode <= cutoff {
		child[pendingLt&btChildMask] = matchfinderInitval
		child[pendingGt&btChildMask] = matchfinderInitval
		return n
	}

	bestLtLen := uint32(0)
	bestGtLen := uint32(0)
	ln := uint32(0)

	for {
		matchptr := inBase + int(curNode)

		if in[matchptr+int(ln)] == in[inNext+int(ln)] {
			ln = lzExtend(in, inNext, matchptr, ln+1, maxLen)
			if ln > bestLen {
				bestLen = ln
				matches[n&(maxMatchesPerPos-1)] = lzMatch{uint16(ln), uint16(inNext - matchptr)}
				n++
				if ln >= niceLen {
					child[pendingLt&btChildMask] = child[btLeftChild(curNode)]
					child[pendingGt&btChildMask] = child[btRightChild(curNode)]
					return n
				}
			}
		}

		if in[matchptr+int(ln)] < in[inNext+int(ln)] {
			child[pendingLt&btChildMask] = mfPos(curNode)
			pendingLt = btRightChild(curNode)
			curNode = int32(child[pendingLt&btChildMask])
			bestLtLen = ln
			if bestGtLen < ln {
				ln = bestGtLen
			}
		} else {
			child[pendingGt&btChildMask] = mfPos(curNode)
			pendingGt = btLeftChild(curNode)
			curNode = int32(child[pendingGt&btChildMask])
			bestGtLen = ln
			if bestLtLen < ln {
				ln = bestLtLen
			}
		}

		if curNode <= cutoff {
			break
		}
		depthRemaining--
		if depthRemaining == 0 {
			break
		}
	}
	child[pendingLt&btChildMask] = matchfinderInitval
	child[pendingGt&btChildMask] = matchfinderInitval
	return n
}

// skipByte advances the matchfinder by one position (inBase+curPos)
// without recording matches: the position is hashed and inserted (its tree
// re-rooted at it, searching with max_len = niceLen) exactly as getMatches
// would. Preconditions as for getMatches with maxLen = niceLen.
// port of bt_matchfinder_skip_byte (bt_matchfinder_advance_one_byte with
// record_matches = false and max_len = nice_len)
func (mf *btMatchfinder) skipByte(in []byte, inBase, curPos int, niceLen, maxSearchDepth uint32,
	nextHashes *[2]uint32) {
	inNext := inBase + curPos
	depthRemaining := maxSearchDepth
	cutoff := int32(curPos) - matchfinderWindowSize

	nextHashseq := loadLE32(in, inNext+1)

	hash3 := nextHashes[0] & btHash3Mask
	hash4 := nextHashes[1] & btHash4Mask

	nextHashes[0] = lzHash(nextHashseq&0xFFFFFF, btMatchfinderHash3Order)
	nextHashes[1] = lzHash(nextHashseq, btMatchfinderHash4Order)

	bucket3 := (*[btMatchfinderHash3Ways]mfPos)(mf.hash3Tab[2*hash3:])
	bucket3[1] = bucket3[0]
	bucket3[0] = mfPos(curPos)

	curNode := int32(mf.hash4Tab[hash4])
	mf.hash4Tab[hash4] = mfPos(curPos)

	child := &mf.childTab
	pendingLt := btLeftChild(int32(curPos))
	pendingGt := btRightChild(int32(curPos))

	if curNode <= cutoff {
		child[pendingLt&btChildMask] = matchfinderInitval
		child[pendingGt&btChildMask] = matchfinderInitval
		return
	}

	bestLtLen := uint32(0)
	bestGtLen := uint32(0)
	ln := uint32(0)

	for {
		matchptr := inBase + int(curNode)

		if in[matchptr+int(ln)] == in[inNext+int(ln)] {
			ln = lzExtend(in, inNext, matchptr, ln+1, niceLen)
			if ln >= niceLen {
				child[pendingLt&btChildMask] = child[btLeftChild(curNode)]
				child[pendingGt&btChildMask] = child[btRightChild(curNode)]
				return
			}
		}

		if in[matchptr+int(ln)] < in[inNext+int(ln)] {
			child[pendingLt&btChildMask] = mfPos(curNode)
			pendingLt = btRightChild(curNode)
			curNode = int32(child[pendingLt&btChildMask])
			bestLtLen = ln
			if bestGtLen < ln {
				ln = bestGtLen
			}
		} else {
			child[pendingGt&btChildMask] = mfPos(curNode)
			pendingGt = btLeftChild(curNode)
			curNode = int32(child[pendingGt&btChildMask])
			bestGtLen = ln
			if bestLtLen < ln {
				ln = bestLtLen
			}
		}

		if curNode <= cutoff {
			break
		}
		depthRemaining--
		if depthRemaining == 0 {
			break
		}
	}
	child[pendingLt&btChildMask] = matchfinderInitval
	child[pendingGt&btChildMask] = matchfinderInitval
}
