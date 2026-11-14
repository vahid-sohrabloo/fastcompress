// Port of libzstd v1.5.7 lib/compress/zstd_ldm.c and zstd_ldm.h (Copyright
// (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the
// long-distance matcher (LDM). C enables it for strategies btopt and above
// with a window log of 27 or more (ZSTD_resolveEnableLdm); it then runs
// before the block compressor over every block, through its own window
// and hash table, and hands its long matches to the optimal parser as
// extra candidates (ZSTD_ldm_blockCompress).
//
// The LDM window (ldmState.window) is a window like the match state's,
// with its own indices; the positions handled here are positions in its
// base slice (C's pointers), as elsewhere in this port.

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/xxhash"

const (
	ldmBucketSizeLog  = 4  // C: zstd_ldm.c:LDM_BUCKET_SIZE_LOG
	ldmMinMatchLength = 64 // C: zstd_ldm.c:LDM_MIN_MATCH_LENGTH
	// ldmBatchSize is the most split points gathered before they are
	// processed. C: zstd_compress_internal.h:LDM_BATCH_SIZE
	ldmBatchSize = 64
	// ldmDefaultWindowLog. C: zstd_ldm.h:ZSTD_LDM_DEFAULT_WINDOW_LOG
	// (ZSTD_WINDOWLOG_LIMIT_DEFAULT)
	ldmDefaultWindowLog = 27
	ldmBucketSizeLogMax = 8  // C: zstd.h:ZSTD_LDM_BUCKETSIZELOG_MAX
	hashLogMax          = 30 // C: zstd.h:ZSTD_HASHLOG_MAX (64-bit)
)

// rawSeq is a sequence found by the long-distance matcher.
// C: zstd_compress_internal.h:rawSeq
type rawSeq struct {
	offset      uint32 // offset of sequence
	litLength   uint32 // length of literals prior to match
	matchLength uint32 // raw length of match
}

// rawSeqStore. C: zstd_compress_internal.h:RawSeqStore_t — seq holds
// capacity entries (C: the start of the sequences, seq to seq + capacity).
type rawSeqStore struct {
	seq           []rawSeq
	pos           int // the index in seq where reading stopped. pos <= size.
	posInSequence int // the position within the sequence at seq[pos] where reading stopped. posInSequence <= seq[pos].litLength + seq[pos].matchLength
	size          int // the number of sequences. <= capacity.
	capacity      int
}

// ldmEntry. C: zstd_compress_internal.h:ldmEntry_t
type ldmEntry struct {
	offset   uint32
	checksum uint32
}

// ldmMatchCandidate. C: zstd_compress_internal.h:ldmMatchCandidate_t —
// split is a position in the LDM window's base, bucket the index in
// hashTable of the bucket's first entry.
type ldmMatchCandidate struct {
	split    int
	hash     uint32
	checksum uint32
	bucket   int
}

// ldmState. C: zstd_compress_internal.h:ldmState_t
type ldmState struct {
	window          window // state for the window round buffer management
	hashTable       []ldmEntry
	loadedDictEnd   uint32
	bucketOffsets   []byte // next position in bucket to insert entry
	splitIndices    [ldmBatchSize]int
	matchCandidates [ldmBatchSize]ldmMatchCandidate
}

// ldmParams. C: zstd_compress_internal.h:ldmParams_t
type ldmParams struct {
	enableLdm      paramSwitch // psEnable to enable LDM. psAuto by default
	hashLog        uint32      // log size of hashTable
	bucketSizeLog  uint32      // log bucket size for collision resolution, at most 8
	minMatchLength uint32      // minimum match length
	hashRateLog    uint32      // log number of entries to skip
	windowLog      uint32      // window log for the LDM
}

// resolveEnableLdm returns psEnable if the compression parameters are such
// that long distance matching should be enabled (windowLog >= 27, strategy
// >= btopt), psDisable otherwise. C: zstd_compress.c:ZSTD_resolveEnableLdm
func resolveEnableLdm(mode paramSwitch, cParams *compressionParameters) paramSwitch {
	if mode != psAuto {
		return mode
	}
	if cParams.strategy >= zstdBtopt && cParams.windowLog >= 27 {
		return psEnable
	}
	return psDisable
}

// ldmRollingHashState. C: zstd_ldm.c:ldmRollingHashState_t
type ldmRollingHashState struct {
	rolling  uint64
	stopMask uint64
}

// ldmGearInit initializes the rolling hash state such that it will honor
// the settings in params. C: zstd_ldm.c:ZSTD_ldm_gear_init
func ldmGearInit(state *ldmRollingHashState, params *ldmParams) {
	maxBitsInMask := min(params.minMatchLength, 64)
	hashRateLog := params.hashRateLog

	state.rolling = uint64(^uint32(0))

	// The choice of the splitting criterion is subject to two conditions:
	//   1. it has to trigger on average every 2^(hashRateLog) bytes;
	//   2. ideally, it has to depend on a window of minMatchLength bytes.
	//
	// In the gear hash algorithm, bit n depends on the last n bytes; so in
	// order to obtain a good quality splitting criterion it is preferable
	// to use bits with high weight.
	//
	// To match condition 1 we use a mask with hashRateLog bits set and,
	// because of the previous remark, we make sure these bits have the
	// highest possible weight while still respecting condition 2.
	if hashRateLog > 0 && hashRateLog <= maxBitsInMask {
		state.stopMask = (uint64(1)<<hashRateLog - 1) << (maxBitsInMask - hashRateLog)
	} else {
		// In this degenerate case we simply honor the hash rate.
		state.stopMask = uint64(1)<<hashRateLog - 1
	}
}

// ldmGearReset is meant to feed minMatchLength bytes of data into the hash
// without registering any splits, to reset the hash state when skipping
// over data. C: zstd_ldm.c:ZSTD_ldm_gear_reset — C feeds the bytes into a
// local copy of state->rolling and never stores it back, so the state is
// left unchanged; this port keeps that (it is a no-op).
func ldmGearReset(*ldmRollingHashState, []byte, uint32) {}

// ldmGearFeed registers in splits all the split points found in data (as
// counts of bytes from its start). It stops when either all the data has
// been processed or ldmBatchSize splits are present in splits, which must
// not be full, and returns the number of bytes processed.
// C: zstd_ldm.c:ZSTD_ldm_gear_feed (C unrolls the loop by 4)
func ldmGearFeed(state *ldmRollingHashState, data []byte, splits *[ldmBatchSize]int, numSplits *int) int {
	hash := state.rolling
	mask := state.stopMask
	ns := *numSplits
	n := 0
	for n < len(data) {
		hash = hash<<1 + ldmGearTab[data[n]]
		n++
		if hash&mask == 0 {
			splits[ns] = n
			ns++
			if ns == ldmBatchSize {
				break
			}
		}
	}
	*numSplits = ns
	state.rolling = hash
	return n
}

// ldmAdjustParameters. If the params.hashRateLog is not set, set it to its
// default value based on windowLog and params.hashLog. Ensures that
// params.bucketSizeLog is <= params.hashLog (setting it to params.hashLog
// if it is not). Ensures that the minMatchLength >= targetLength during
// optimal parsing. C: zstd_ldm.c:ZSTD_ldm_adjustParameters
func ldmAdjustParameters(params *ldmParams, cParams *compressionParameters) {
	params.windowLog = cParams.windowLog
	if params.hashRateLog == 0 {
		if params.hashLog > 0 {
			// if params.hashLog is set, derive hashRateLog from it
			if params.windowLog > params.hashLog {
				params.hashRateLog = params.windowLog - params.hashLog
			}
		} else {
			// mapping from [fast, rate7] to [btultra2, rate4]
			params.hashRateLog = 7 - uint32(cParams.strategy)/3
		}
	}
	if params.hashLog == 0 {
		params.hashLog = max(hashLogMin, min(params.windowLog-params.hashRateLog, hashLogMax))
	}
	if params.minMatchLength == 0 {
		params.minMatchLength = ldmMinMatchLength
		if cParams.strategy >= zstdBtultra {
			params.minMatchLength /= 2
		}
	}
	if params.bucketSizeLog == 0 {
		params.bucketSizeLog = max(ldmBucketSizeLog, min(uint32(cParams.strategy), ldmBucketSizeLogMax))
	}
	params.bucketSizeLog = min(params.bucketSizeLog, params.hashLog)
}

// ldmGetTableSize estimates the space needed for the LDM tables, 0 when
// LDM is disabled. C: zstd_ldm.c:ZSTD_ldm_getTableSize (C rounds each
// table up to its workspace allocation size; this port's tables are plain
// slices).
func ldmGetTableSize(params ldmParams) uint64 {
	if params.enableLdm != psEnable {
		return 0
	}
	ldmHSize := uint64(1) << params.hashLog
	ldmBucketSizeLog := min(params.bucketSizeLog, params.hashLog)
	ldmBucketSize := uint64(1) << (params.hashLog - ldmBucketSizeLog)
	return ldmBucketSize + ldmHSize*8 // sizeof(ldmEntry_t)
}

// ldmGetMaxNbSeq returns an upper bound on the number of LDM sequences
// for a chunk of maxChunkSize bytes, 0 when LDM is disabled.
// C: zstd_ldm.c:ZSTD_ldm_getMaxNbSeq
func ldmGetMaxNbSeq(params ldmParams, maxChunkSize int) int {
	if params.enableLdm != psEnable {
		return 0
	}
	return maxChunkSize / int(params.minMatchLength)
}

// ldmInsertEntry inserts the entry with corresponding hash into the hash
// table. C: zstd_ldm.c:ZSTD_ldm_insertEntry (and ZSTD_ldm_getBucket)
func ldmInsertEntry(ls *ldmState, hash uint32, entry ldmEntry, bucketSizeLog uint32) {
	offset := uint32(ls.bucketOffsets[hash])
	ls.hashTable[int(hash)<<bucketSizeLog+int(offset)] = entry
	ls.bucketOffsets[hash] = byte((offset + 1) & (1<<bucketSizeLog - 1))
}

// ldmCountBackwardsMatch returns the number of bytes that match backwards
// before in[pIn] and m[pMatch], counting only bytes where pMatch >=
// pMatchBase and pIn >= pAnchor. C: zstd_ldm.c:ZSTD_ldm_countBackwardsMatch
func ldmCountBackwardsMatch(in []byte, pIn, pAnchor int, m []byte, pMatch, pMatchBase int) int {
	matchLength := 0
	for pIn > pAnchor && pMatch > pMatchBase && in[pIn-1] == m[pMatch-1] {
		pIn--
		pMatch--
		matchLength++
	}
	return matchLength
}

// ldmCountBackwardsMatch2Segments returns the number of bytes that match
// backwards from m[pMatch], even with the backwards match spanning 2
// different segments: on reaching pMatchBase in the prefix, it continues
// from the end of the extDict d[pExtDictStart:pExtDictEnd]. inExtDict
// tells whether m is the extDict (C: pMatchBase == pExtDictStart).
// C: zstd_ldm.c:ZSTD_ldm_countBackwardsMatch_2segments
func ldmCountBackwardsMatch2Segments(in []byte, pIn, pAnchor int, m []byte, pMatch, pMatchBase int,
	inExtDict bool, d []byte, pExtDictStart, pExtDictEnd int) int {
	matchLength := ldmCountBackwardsMatch(in, pIn, pAnchor, m, pMatch, pMatchBase)
	if pMatch-matchLength != pMatchBase || inExtDict {
		// If backwards match is entirely in the extDict or prefix, immediately return
		return matchLength
	}
	matchLength += ldmCountBackwardsMatch(in, pIn-matchLength, pAnchor, d, pExtDictEnd, pExtDictStart)
	return matchLength
}

// ldmFillFastTables fills the relevant tables for the fast and dfast
// strategies, up to index end. This is similar to loadDictionaryContent.
// The tables for the other strategies are filled within their block
// compressors. C: zstd_ldm.c:ZSTD_ldm_fillFastTables
func ldmFillFastTables(ms *matchState, end uint32) {
	switch ms.cParams.strategy {
	case zstdFast:
		fillHashTable(ms, end, dtlmFast, tfpForCCtx)
	case zstdDfast:
		fillDoubleHashTable(ms, end, dtlmFast, tfpForCCtx)
	}
}

// ldmFillHashTable fills the LDM hash table with the split points of
// ls.window.base[ip:iend] (dictionary content).
// C: zstd_ldm.c:ZSTD_ldm_fillHashTable
func ldmFillHashTable(ls *ldmState, ip, iend int, params *ldmParams) {
	minMatchLength := int(params.minMatchLength)
	bucketSizeLog := params.bucketSizeLog
	hBits := params.hashLog - bucketSizeLog
	b := ls.window.base
	baseIdx := ls.window.baseIdx
	istart := ip
	var hashState ldmRollingHashState
	splits := &ls.splitIndices

	ldmGearInit(&hashState, params)
	for ip < iend {
		numSplits := 0
		hashed := ldmGearFeed(&hashState, b[ip:iend], splits, &numSplits)

		for n := range numSplits {
			if ip+splits[n] >= istart+minMatchLength {
				split := ip + splits[n] - minMatchLength
				xxh := xxhash.Sum64(b[split:split+minMatchLength], 0)
				hash := uint32(xxh) & (uint32(1)<<hBits - 1)
				entry := ldmEntry{offset: uint32(split) + baseIdx, checksum: uint32(xxh >> 32)}
				ldmInsertEntry(ls, hash, entry, params.bucketSizeLog)
			}
		}

		ip += hashed
	}
}

// ldmLimitTableUpdate sets ms.nextToUpdate to a position corresponding
// closer to anchor (an index) if it is far away (after a long match, only
// update tables a limited amount). C: zstd_ldm.c:ZSTD_ldm_limitTableUpdate
func ldmLimitTableUpdate(ms *matchState, anchor uint32) {
	curr := anchor
	if curr > ms.nextToUpdate+1024 {
		ms.nextToUpdate = curr - min(512, curr-ms.nextToUpdate-1024)
	}
}

// ldmGenerateSequencesInternal generates the sequences of
// ls.window.base[istart:istart+srcSize] into rawSeqStore and returns the
// number of trailing literals. C: zstd_ldm.c:ZSTD_ldm_generateSequences_internal
func ldmGenerateSequencesInternal(ls *ldmState, rs *rawSeqStore, params *ldmParams, istart, srcSize int) (int, error) {
	w := &ls.window
	// LDM parameters
	extDict := windowHasExtDict(w)
	minMatchLength := int(params.minMatchLength)
	entsPerBucket := 1 << params.bucketSizeLog
	hBits := params.hashLog - params.bucketSizeLog
	// Prefix and extDict parameters
	dictLimit := w.dictLimit
	lowestIndex := dictLimit
	if extDict {
		lowestIndex = w.lowLimit
	}
	b := w.base
	baseIdx := w.baseIdx
	var dictBase []byte
	var dictStart, dictEnd int
	if extDict {
		dictBase = w.dictBase
		dictStart = int(lowestIndex - w.dictBaseIdx)
		dictEnd = int(dictLimit - w.dictBaseIdx)
	}
	lowPrefixPtr := int(dictLimit - baseIdx)
	// Input bounds
	iend := istart + srcSize
	ilimit := iend - hashReadSize
	// Input positions
	anchor := istart
	ip := istart
	// Rolling hash state
	var hashState ldmRollingHashState
	// Arrays for staged-processing
	splits := &ls.splitIndices
	candidates := &ls.matchCandidates

	if srcSize < minMatchLength {
		return iend - anchor, nil
	}

	// Initialize the rolling hash state with the first minMatchLength bytes
	ldmGearInit(&hashState, params)
	ldmGearReset(&hashState, b[ip:], params.minMatchLength)
	ip += minMatchLength

	for ip < ilimit {
		numSplits := 0
		hashed := ldmGearFeed(&hashState, b[ip:ilimit], splits, &numSplits)

		for n := range numSplits {
			split := ip + splits[n] - minMatchLength
			xxh := xxhash.Sum64(b[split:split+minMatchLength], 0)
			hash := uint32(xxh) & (uint32(1)<<hBits - 1)

			candidates[n] = ldmMatchCandidate{
				split:    split,
				hash:     hash,
				checksum: uint32(xxh >> 32),
				bucket:   int(hash) << params.bucketSizeLog,
			}
		}

		for n := 0; n < numSplits; n++ {
			var forwardMatchLength, backwardMatchLength, bestMatchLength int
			split := candidates[n].split
			checksum := candidates[n].checksum
			hash := candidates[n].hash
			bucket := ls.hashTable[candidates[n].bucket : candidates[n].bucket+entsPerBucket]
			bestEntry := -1
			newEntry := ldmEntry{offset: uint32(split) + baseIdx, checksum: checksum}

			// If a split point would generate a sequence overlapping with
			// the previous one, we merely register it in the hash table
			// and move on
			if split < anchor {
				ldmInsertEntry(ls, hash, newEntry, params.bucketSizeLog)
				continue
			}

			for i := range bucket {
				cur := bucket[i]
				if cur.checksum != checksum || cur.offset <= lowestIndex {
					continue
				}
				var curForwardMatchLength, curBackwardMatchLength int
				if extDict {
					inDict := cur.offset < dictLimit
					curMatchBase, pMatch, matchEnd, lowMatchPtr := b, int(cur.offset-baseIdx), iend, lowPrefixPtr
					if inDict {
						curMatchBase, pMatch, matchEnd, lowMatchPtr = dictBase, int(cur.offset-w.dictBaseIdx), dictEnd, dictStart
					}
					curForwardMatchLength = count2Segments(b, split, curMatchBase, pMatch, iend, matchEnd, lowPrefixPtr)
					if curForwardMatchLength < minMatchLength {
						continue
					}
					curBackwardMatchLength = ldmCountBackwardsMatch2Segments(b, split, anchor,
						curMatchBase, pMatch, lowMatchPtr, inDict, dictBase, dictStart, dictEnd)
				} else { // !extDict
					pMatch := int(cur.offset - baseIdx)
					curForwardMatchLength = count(b, split, pMatch, iend)
					if curForwardMatchLength < minMatchLength {
						continue
					}
					curBackwardMatchLength = ldmCountBackwardsMatch(b, split, anchor, b, pMatch, lowPrefixPtr)
				}
				curTotalMatchLength := curForwardMatchLength + curBackwardMatchLength

				if curTotalMatchLength > bestMatchLength {
					bestMatchLength = curTotalMatchLength
					forwardMatchLength = curForwardMatchLength
					backwardMatchLength = curBackwardMatchLength
					bestEntry = i
				}
			}

			// No match found -- insert an entry into the hash table and
			// process the next candidate match
			if bestEntry < 0 {
				ldmInsertEntry(ls, hash, newEntry, params.bucketSizeLog)
				continue
			}

			// Match found
			offset := uint32(split) + baseIdx - bucket[bestEntry].offset
			mLength := forwardMatchLength + backwardMatchLength
			{
				// Out of sequence storage
				if rs.size == rs.capacity {
					return 0, errDstSizeTooSmall
				}
				rs.seq[rs.size] = rawSeq{
					litLength:   uint32(split - backwardMatchLength - anchor),
					matchLength: uint32(mLength),
					offset:      offset,
				}
				rs.size++
			}

			// Insert the current entry into the hash table --- it must be
			// done after the previous block to avoid clobbering bestEntry
			ldmInsertEntry(ls, hash, newEntry, params.bucketSizeLog)

			anchor = split + forwardMatchLength

			// If we find a match that ends after the data that we've hashed
			// then we have a repeating, overlapping, pattern. E.g. all
			// zeros. If one repetition of the pattern matches our stopMask
			// then all repetitions will. We don't need to insert them all
			// into out table, only the first one. So skip over overlapping
			// matches. This is a major speed boost (20x) for compressing a
			// single byte repeated, when that byte ends up in the table.
			if anchor > ip+hashed {
				ldmGearReset(&hashState, b[anchor-minMatchLength:], params.minMatchLength)
				// Continue the outer loop at anchor (ip + hashed == anchor).
				ip = anchor - hashed
				break
			}
		}

		ip += hashed
	}

	return iend - anchor, nil
}

// ldmReduceTable reduces table indexes by reducerValue.
// C: zstd_ldm.c:ZSTD_ldm_reduceTable
func ldmReduceTable(table []ldmEntry, reducerValue uint32) {
	for u := range table {
		if table[u].offset < reducerValue {
			table[u].offset = 0
		} else {
			table[u].offset -= reducerValue
		}
	}
}

// ldmSeqHook, when set (by tests), is called after each block's LDM
// sequence generation with the number of sequences found and whether the
// LDM window has an extDict segment. C has no equivalent.
var ldmSeqHook func(n int, extDict bool)

// ldmGenerateSequences generates the long-distance sequences of the
// srcSize bytes at index srcIdx of ls.window into sequences, which must be
// empty-positioned (pos <= size <= capacity). ls.window must already
// contain them (windowUpdate). Large inputs are broken up into chunks to
// enforce the maximum distance and handle overflow correction.
// C: zstd_ldm.c:ZSTD_ldm_generateSequences
func ldmGenerateSequences(ls *ldmState, sequences *rawSeqStore, params *ldmParams, srcIdx uint32, srcSize int) error {
	maxDist := uint32(1) << params.windowLog
	istart := int(srcIdx - ls.window.baseIdx)
	iend := istart + srcSize
	const kMaxChunkSize = 1 << 20
	nbChunks := srcSize / kMaxChunkSize
	if srcSize%kMaxChunkSize != 0 {
		nbChunks++
	}
	leftoverSize := 0

	// The input could be very large (in zstdmt), so it must be broken up
	// into chunks to enforce the maximum distance and handle overflow
	// correction.
	for chunk := 0; chunk < nbChunks && sequences.size < sequences.capacity; chunk++ {
		w := &ls.window
		chunkStart := istart + chunk*kMaxChunkSize
		remaining := iend - chunkStart
		chunkEnd := chunkStart + kMaxChunkSize
		if remaining < kMaxChunkSize {
			chunkEnd = iend
		}
		chunkSize := chunkEnd - chunkStart
		prevSize := sequences.size

		// 1. Perform overflow correction if necessary.
		if windowNeedOverflowCorrection(w, 0, maxDist, ls.loadedDictEnd, uint32(chunkStart)+w.baseIdx, uint32(chunkEnd)+w.baseIdx) {
			ldmHSize := 1 << params.hashLog
			correction := windowCorrectOverflow(w, 0 /* cycleLog */, maxDist, uint32(chunkStart)+w.baseIdx)
			ldmReduceTable(ls.hashTable[:ldmHSize], correction)
			// invalidate dictionaries on overflow correction
			ls.loadedDictEnd = 0
		}
		// 2. We enforce the maximum offset allowed.
		//
		// kMaxChunkSize should be small enough that we don't lose too much
		// of the window through early invalidation.
		//
		// NOTE: Because of dictionaries + sequence splitting we MUST make
		// sure that any offset used is valid at the END of the sequence,
		// since it may be split into two sequences. This condition holds
		// when using windowEnforceMaxDist(), but if we move to checking
		// offsets against maxDist directly, we'll have to carefully handle
		// that case.
		windowEnforceMaxDist(w, uint32(chunkEnd)+w.baseIdx, maxDist, &ls.loadedDictEnd, nil)
		// 3. Generate the sequences for the chunk, and get newLeftoverSize.
		newLeftoverSize, err := ldmGenerateSequencesInternal(ls, sequences, params, chunkStart, chunkSize)
		if err != nil {
			return err
		}
		// 4. We add the leftover literals from previous iterations to the
		// first newly generated sequence, or add the newLeftoverSize if
		// none are generated.
		// Prepend the leftover literals from the last call
		if prevSize < sequences.size {
			sequences.seq[prevSize].litLength += uint32(leftoverSize)
			leftoverSize = newLeftoverSize
		} else {
			leftoverSize += chunkSize
		}
	}
	if ldmSeqHook != nil {
		ldmSeqHook(sequences.size, windowHasExtDict(&ls.window))
	}
	return nil
}

// ldmSkipSequences skips past srcSize bytes worth of sequences in
// rawSeqStore. Avoids emitting matches less than minMatch bytes. Must be
// called for data that is not passed to ldmBlockCompress.
// C: zstd_ldm.c:ZSTD_ldm_skipSequences
func ldmSkipSequences(rs *rawSeqStore, srcSize int, minMatch uint32) {
	for srcSize > 0 && rs.pos < rs.size {
		seq := &rs.seq[rs.pos]
		if srcSize <= int(seq.litLength) {
			// Skip past srcSize literals
			seq.litLength -= uint32(srcSize)
			return
		}
		srcSize -= int(seq.litLength)
		seq.litLength = 0
		if srcSize < int(seq.matchLength) {
			// Skip past the first srcSize of the match
			seq.matchLength -= uint32(srcSize)
			if seq.matchLength < minMatch {
				// The match is too short, omit it
				if rs.pos+1 < rs.size {
					rs.seq[rs.pos+1].litLength += seq.matchLength
				}
				rs.pos++
			}
			return
		}
		srcSize -= int(seq.matchLength)
		seq.matchLength = 0
		rs.pos++
	}
}

// maybeSplitSequence returns the current sequence to handle, or, if the
// rest of the block should be literals, a sequence with offset == 0. If
// the sequence length is longer than remaining then the sequence is split
// between this block and the next. C: zstd_ldm.c:maybeSplitSequence
func maybeSplitSequence(rs *rawSeqStore, remaining, minMatch uint32) rawSeq {
	sequence := rs.seq[rs.pos]
	// Likely: No partial sequence
	if remaining >= sequence.litLength+sequence.matchLength {
		rs.pos++
		return sequence
	}
	// Cut the sequence short (offset == 0 ==> rest is literals).
	if remaining <= sequence.litLength {
		sequence.offset = 0
	} else if remaining < sequence.litLength+sequence.matchLength {
		sequence.matchLength = remaining - sequence.litLength
		if sequence.matchLength < minMatch {
			sequence.offset = 0
		}
	}
	// Skip past remaining bytes for the future sequences.
	ldmSkipSequences(rs, int(remaining), minMatch)
	return sequence
}

// ldmSkipRawSeqStoreBytes moves forward in rawSeqStore by nbBytes,
// updating pos and posInSequence. C: zstd_ldm.c:ZSTD_ldm_skipRawSeqStoreBytes
// (zstd_opt.c:ZSTD_optLdm_skipRawSeqStoreBytes is the same function)
func ldmSkipRawSeqStoreBytes(rs *rawSeqStore, nbBytes int) {
	currPos := uint32(rs.posInSequence + nbBytes)
	for currPos != 0 && rs.pos < rs.size {
		currSeq := rs.seq[rs.pos]
		if currPos >= currSeq.litLength+currSeq.matchLength {
			currPos -= currSeq.litLength + currSeq.matchLength
			rs.pos++
		} else {
			rs.posInSequence = int(currPos)
			break
		}
	}
	if currPos == 0 || rs.pos == rs.size {
		rs.posInSequence = 0
	}
}

// ldmBlockCompress compresses the block src (at index srcIdx of ms.window)
// with the long-distance sequences of rs. The optimal parser (btopt and
// above) takes them as candidates; the other strategies store each one and
// run the block compressor on the literals between them. It returns the
// number of trailing literals, like a block compressor.
// C: zstd_ldm.c:ZSTD_ldm_blockCompress
//
// Only the optimal parser's branch is reachable: LDM is only enabled
// automatically (ZSTD_resolveEnableLdm, btopt and above); the other
// strategies would need ZSTD_c_enableLongDistanceMatching, which this port
// does not expose. Their branch is kept as C has it.
func ldmBlockCompress(rs *rawSeqStore, ms *matchState, ss *seqStore, rep *[repNum]uint32,
	useRowMatchFinder paramSwitch, src []byte, srcIdx uint32) (int, error) {
	cParams := &ms.cParams
	minMatch := cParams.minMatch
	blockCompressor := selectBlockCompressor(cParams.strategy,
		rowMatchFinderUsed(cParams.strategy, useRowMatchFinder), matchStateDictMode(ms))
	if blockCompressor == nil {
		return 0, errStrategyUnsupported
	}
	// Input bounds
	iend := len(src)
	// Input positions
	ip := 0

	// If using opt parser, use LDMs only as candidates rather than always accepting them
	if cParams.strategy >= zstdBtopt {
		ms.ldmSeqStore = rs
		lastLLSize := blockCompressor(ms, ss, rep, src, srcIdx)
		ms.ldmSeqStore = nil // C leaves it dangling; every block sets it again
		ldmSkipRawSeqStoreBytes(rs, len(src))
		return lastLLSize, nil
	}

	// Loop through each sequence and apply the block compressor to the literals
	for rs.pos < rs.size && ip < iend {
		// maybeSplitSequence updates rs.pos
		sequence := maybeSplitSequence(rs, uint32(iend-ip), minMatch)
		// End signal
		if sequence.offset == 0 {
			break
		}

		// Fill tables for block compressor
		ldmLimitTableUpdate(ms, srcIdx+uint32(ip))
		ldmFillFastTables(ms, srcIdx+uint32(ip))
		// Run the block compressor
		{
			newLitLength := blockCompressor(ms, ss, rep, src[ip:ip+int(sequence.litLength)], srcIdx+uint32(ip))
			ip += int(sequence.litLength)
			// Update the repcodes
			for i := repNum - 1; i > 0; i-- {
				rep[i] = rep[i-1]
			}
			rep[0] = sequence.offset
			// Store the sequence
			storeSeq(ss, newLitLength, src[ip-newLitLength:], offsetToOffBase(sequence.offset), int(sequence.matchLength))
			ip += int(sequence.matchLength)
		}
	}
	// Fill the tables for the block compressor
	ldmLimitTableUpdate(ms, srcIdx+uint32(ip))
	ldmFillFastTables(ms, srcIdx+uint32(ip))
	// Compress the last literals
	return blockCompressor(ms, ss, rep, src[ip:], srcIdx+uint32(ip)), nil
}
