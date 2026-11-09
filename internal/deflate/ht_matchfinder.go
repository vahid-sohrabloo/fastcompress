// Port of libdeflate v1.26 lib/ht_matchfinder.h (Copyright 2022 Eric
// Biggers, MIT; see NOTICE): the hash table matchfinder of
// deflate_compress_fastest (struct ht_matchfinder, ht_matchfinder_init,
// ht_matchfinder_slide_window, ht_matchfinder_longest_match and
// ht_matchfinder_skip_bytes).
//
// This is a variant of the hash chains matchfinder optimized for very fast
// compression: the hash chains are stored inline in the hash table, as
// buckets of HT_MATCHFINDER_BUCKET_SIZE positions. It finds no length 3
// matches, and its search depth is the build-time bucket size.

package deflate

// C: HT_MATCHFINDER_HASH_ORDER, HT_MATCHFINDER_BUCKET_SIZE,
// HT_MATCHFINDER_MIN_MATCH_LEN, HT_MATCHFINDER_REQUIRED_NBYTES (the minimum
// value of max_len for ht_matchfinder_longest_match)
const (
	htMatchfinderHashOrder      = 15
	htMatchfinderBucketSize     = 2
	htMatchfinderMinMatchLen    = 4
	htMatchfinderRequiredNbytes = 5
)

// The functions below are C's hand-unrolled HT_MATCHFINDER_BUCKET_SIZE == 2
// versions, which assume HT_MATCHFINDER_MIN_MATCH_LEN == 4 and
// HT_MATCHFINDER_REQUIRED_NBYTES == 5 (STATIC_ASSERTs in C); and the table
// size is a multiple of MATCHFINDER_SIZE_ALIGNMENT (C: STATIC_ASSERT in
// ht_matchfinder_init).
const (
	_ = uint(htMatchfinderBucketSize-2) + uint(2-htMatchfinderBucketSize)
	_ = uint(htMatchfinderMinMatchLen-4) + uint(4-htMatchfinderMinMatchLen)
	_ = uint(htMatchfinderRequiredNbytes-5) + uint(5-htMatchfinderRequiredNbytes)
	_ = uint(0 - (len(htMatchfinder{}.hashTab)*2)%matchfinderSizeAlignment)
)

// htMatchfinder is the hash table. C declares it as
// hash_tab[1 << HT_MATCHFINDER_HASH_ORDER][HT_MATCHFINDER_BUCKET_SIZE] and
// processes it as one flat mf_pos_t array in init/rebase; the port keeps it
// flat in that (row-major) layout: bucket h is
// hashTab[h*BUCKET_SIZE : (h+1)*BUCKET_SIZE], most recent position first.
// C: struct ht_matchfinder
type htMatchfinder struct {
	hashTab [(1 << htMatchfinderHashOrder) * htMatchfinderBucketSize]mfPos
}

// bucket returns hash_tab[hash] (hash < 1<<HT_MATCHFINDER_HASH_ORDER).
func (mf *htMatchfinder) bucket(hash uint32) *[htMatchfinderBucketSize]mfPos {
	i := int(hash*htMatchfinderBucketSize) & (len(mf.hashTab) - htMatchfinderBucketSize)
	return (*[htMatchfinderBucketSize]mfPos)(mf.hashTab[i : i+htMatchfinderBucketSize])
}

// init empties the hash table. port of ht_matchfinder_init
func (mf *htMatchfinder) init() {
	matchfinderInit(mf.hashTab[:])
}

// slideWindow rebases every position by MATCHFINDER_WINDOW_SIZE.
// port of ht_matchfinder_slide_window
func (mf *htMatchfinder) slideWindow() {
	matchfinderRebase(mf.hashTab[:])
}

// longestMatch finds the longest match (of length >= 4, at most maxLen,
// the search stopping early once niceLen is reached) of the bytes at
// in[inNext:] among the positions of its bucket, and inserts inNext into the
// bucket. It returns the match length (0 if none) and offset.
//
// *inBase is the window base (C: *in_base_p, a pointer into in, here an
// index): positions are relative to it, and when inNext is a full window
// past it the table is slid and the base advanced. *nextHash holds the hash
// of the 4 bytes at inNext on entry (computed by the previous call or by
// skipBytes) and the hash of the 4 bytes at inNext+1 on return, which is
// why maxLen must be >= HT_MATCHFINDER_REQUIRED_NBYTES.
//
// C's BUCKET_SIZE == 2 version also copies the first entry to the second
// even when nice_len is reached on the first. The prefetchw has no Go
// counterpart.
//
// It is the test reference only: deflateCompressFastest carries a
// written-out copy of this method and of skipBytes' loop (see there), and
// TestCompressFastestHtEquiv checks that copy against these methods
// (skipBytes itself also primes CompressChunk's history).
// port of ht_matchfinder_longest_match
func (mf *htMatchfinder) longestMatch(in []byte, inBase *int, inNext int, maxLen, niceLen uint32,
	nextHash *uint32) (length, offset uint32) {
	bestLen := uint32(0)
	bestMatchptr := inNext
	curPos := uint32(inNext - *inBase)

	if curPos == matchfinderWindowSize {
		mf.slideWindow()
		*inBase += matchfinderWindowSize
		curPos = 0
	}
	base := *inBase
	// C: mf_pos_t cutoff = cur_pos - MATCHFINDER_WINDOW_SIZE (truncated to
	// 16 bits: -32768..-1).
	cutoff := mfPos(curPos - matchfinderWindowSize)

	hash := *nextHash
	*nextHash = lzHash(loadLE32(in, inNext+1), htMatchfinderHashOrder)
	seq := loadLE32(in, inNext)
	b := mf.bucket(hash)

	curNode := b[0]
	b[0] = mfPos(curPos)
	if curNode > cutoff {
		matchptr := base + int(curNode)

		toInsert := curNode
		curNode = b[1]
		b[1] = toInsert

		if loadLE32(in, matchptr) == seq {
			bestLen = lzExtend(in, inNext, matchptr, 4, maxLen)
			bestMatchptr = matchptr
			if curNode > cutoff && bestLen < niceLen {
				matchptr = base + int(curNode)
				if loadLE32(in, matchptr) == seq &&
					loadLE32(in, matchptr+int(bestLen)-3) == loadLE32(in, inNext+int(bestLen)-3) {
					if l := lzExtend(in, inNext, matchptr, 4, maxLen); l > bestLen {
						bestLen = l
						bestMatchptr = matchptr
					}
				}
			}
		} else if curNode > cutoff {
			matchptr = base + int(curNode)
			if loadLE32(in, matchptr) == seq {
				bestLen = lzExtend(in, inNext, matchptr, 4, maxLen)
				bestMatchptr = matchptr
			}
		}
	}
	// C: out:
	return bestLen, uint32(inNext - bestMatchptr)
}

// skipBytes inserts the count (>= 1) positions inNext..inNext+count-1 into
// the table without searching (the positions covered by a chosen match),
// sliding the window first if the last of them is past it, and leaves the
// hash of the 4 bytes at inNext+count in *nextHash. It does nothing (not
// even update *nextHash) when fewer than count + REQUIRED_NBYTES bytes
// remain before inEnd: the caller then has fewer than REQUIRED_NBYTES bytes
// left and searches no more.
// port of ht_matchfinder_skip_bytes
func (mf *htMatchfinder) skipBytes(in []byte, inBase *int, inNext, inEnd int, count uint32,
	nextHash *uint32) {
	curPos := int32(inNext - *inBase)

	if int(count)+htMatchfinderRequiredNbytes > inEnd-inNext {
		return
	}

	// C: cur_pos + count - 1 >= MATCHFINDER_WINDOW_SIZE (cur_pos >= 1 in
	// C; 0 when CompressChunk primes a history of <= 32768 bytes, which
	// never slides here).
	if curPos+int32(count)-1 >= matchfinderWindowSize {
		mf.slideWindow()
		*inBase += matchfinderWindowSize
		curPos -= matchfinderWindowSize
	}

	hash := *nextHash
	for remaining := count; remaining != 0; remaining-- {
		b := mf.bucket(hash)
		b[1] = b[0] // C: for (i = BUCKET_SIZE - 1; i > 0; i--)
		b[0] = mfPos(curPos)

		inNext++
		hash = lzHash(loadLE32(in, inNext), htMatchfinderHashOrder)
		curPos++
	}
	*nextHash = hash
}
