// Port of libzstd v1.5.7 lib/compress/zstd_preSplit.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license).

package zstd

const (
	preSplitThresholdPenaltyRate = 16
	preSplitThresholdBase        = preSplitThresholdPenaltyRate - 2
	preSplitThresholdPenalty     = 3

	preSplitHashLength   = 2
	preSplitHashLogMax   = 10
	preSplitHashTableLen = 1 << preSplitHashLogMax
	preSplitKnuth        = 0x9e3779b9

	preSplitChunkSize   = 8 << 10
	preSplitSegmentSize = 512
)

// fingerprint is a hashed-event histogram. C: zstd_preSplit.c:Fingerprint
type fingerprint struct {
	events   [preSplitHashTableLen]uint32
	nbEvents uint64 // C: size_t
}

// fpStats is the splitter's workspace; the cctx keeps one so that splitting
// does not allocate. C: zstd_preSplit.c:FPStats. middleEvents is carved out
// of the same workspace in C (ZSTD_splitBlock_fromBorders), where it aliases
// only entries that the hashLog-8 comparisons never read; a separate field
// cannot change output.
type fpStats struct {
	pastEvents   fingerprint
	newEvents    fingerprint
	middleEvents fingerprint
}

// hash2 hashes 2 bytes. For hashLog > 8; for hashLog == 8 it takes the byte
// itself. C: zstd_preSplit.c:hash2
func hash2(p []byte, hashLog uint) uint32 {
	if hashLog == 8 {
		return uint32(p[0])
	}
	return (uint32(p[0]) | uint32(p[1])<<8) * preSplitKnuth >> (32 - hashLog)
}

// C: zstd_preSplit.c:addEvents_generic
func addEventsGeneric(fp *fingerprint, src []byte, samplingRate int, hashLog uint) {
	limit := len(src) - preSplitHashLength + 1
	for n := 0; n < limit; n += samplingRate {
		fp.events[hash2(src[n:], hashLog)]++
	}
	fp.nbEvents += uint64(limit / samplingRate)
}

// C: zstd_preSplit.c:recordFingerprint_generic
func recordFingerprintGeneric(fp *fingerprint, src []byte, samplingRate int, hashLog uint) {
	clear(fp.events[:1<<hashLog])
	fp.nbEvents = 0
	addEventsGeneric(fp, src, samplingRate, hashLog)
}

// C: zstd_preSplit.c:abs64
func abs64(s64 int64) uint64 {
	if s64 < 0 {
		return uint64(-s64)
	}
	return uint64(s64)
}

// C: zstd_preSplit.c:fpDistance
func fpDistance(fp1, fp2 *fingerprint, hashLog uint) uint64 {
	var distance uint64
	for n := 0; n < 1<<hashLog; n++ {
		distance += abs64(int64(fp1.events[n])*int64(fp2.nbEvents) - int64(fp2.events[n])*int64(fp1.nbEvents))
	}
	return distance
}

// compareFingerprints compares newfp with ref; it returns true when
// considered "too different". C: zstd_preSplit.c:compareFingerprints
func compareFingerprints(ref, newfp *fingerprint, penalty int, hashLog uint) bool {
	p50 := ref.nbEvents * newfp.nbEvents
	deviation := fpDistance(ref, newfp, hashLog)
	threshold := p50 * uint64(preSplitThresholdBase+penalty) / preSplitThresholdPenaltyRate
	return deviation >= threshold
}

// C: zstd_preSplit.c:mergeEvents
func mergeEvents(acc, newfp *fingerprint) {
	for n := range acc.events {
		acc.events[n] += newfp.events[n]
	}
	acc.nbEvents += newfp.nbEvents
}

// splitBlockByChunks is the level >= 1 splitter: 8 KiB chunks are compared
// with the fingerprint of everything before them.
// C: zstd_preSplit.c:ZSTD_splitBlock_byChunks
func splitBlockByChunks(blockStart []byte, blockSize int, level int, fpstats *fpStats) int {
	// records_fs: sampling rates 43, 11, 5, 1 with hashLogs 8, 9, 10, 10.
	samplingRates := [...]int{43, 11, 5, 1}
	hashParams := [...]uint{8, 9, 10, 10}
	rate, hashLog := samplingRates[level], hashParams[level]
	penalty := preSplitThresholdPenalty

	*fpstats = fpStats{} // initStats
	recordFingerprintGeneric(&fpstats.pastEvents, blockStart[:preSplitChunkSize], rate, hashLog)
	for pos := preSplitChunkSize; pos <= blockSize-preSplitChunkSize; pos += preSplitChunkSize {
		recordFingerprintGeneric(&fpstats.newEvents, blockStart[pos:pos+preSplitChunkSize], rate, hashLog)
		if compareFingerprints(&fpstats.pastEvents, &fpstats.newEvents, penalty, hashLog) {
			return pos
		}
		mergeEvents(&fpstats.pastEvents, &fpstats.newEvents)
		if penalty > 0 {
			penalty--
		}
	}
	return blockSize
}

// histAdd adds the byte histogram of src to count. C: hist.c:HIST_add
func histAdd(count []uint32, src []byte) {
	for _, b := range src {
		count[b]++
	}
}

// splitBlockFromBorders is the level 0 splitter: it compares fingerprints
// from the beginning and end of the block and derives from their difference
// whether it is preferable to split in the middle.
// C: zstd_preSplit.c:ZSTD_splitBlock_fromBorders
func splitBlockFromBorders(blockStart []byte, blockSize int, fpstats *fpStats) int {
	*fpstats = fpStats{} // initStats (also zeroes middleEvents, see fpStats)
	middleEvents := &fpstats.middleEvents
	histAdd(fpstats.pastEvents.events[:], blockStart[:preSplitSegmentSize])
	histAdd(fpstats.newEvents.events[:], blockStart[blockSize-preSplitSegmentSize:blockSize])
	fpstats.pastEvents.nbEvents = preSplitSegmentSize
	fpstats.newEvents.nbEvents = preSplitSegmentSize
	if !compareFingerprints(&fpstats.pastEvents, &fpstats.newEvents, 0, 8) {
		return blockSize
	}

	histAdd(middleEvents.events[:], blockStart[blockSize/2-preSplitSegmentSize/2:blockSize/2+preSplitSegmentSize/2])
	middleEvents.nbEvents = preSplitSegmentSize
	distFromBegin := fpDistance(&fpstats.pastEvents, middleEvents, 8)
	distFromEnd := fpDistance(&fpstats.newEvents, middleEvents, 8)
	const minDistance = preSplitSegmentSize * preSplitSegmentSize / 3
	if abs64(int64(distFromBegin)-int64(distFromEnd)) < minDistance {
		return 64 << 10
	}
	if distFromBegin > distFromEnd {
		return 32 << 10
	}
	return 96 << 10
}

// splitBlock returns the position at which to cut the (128 KiB) block at
// the start of src, for split levels 0..4. wksp is the reusable workspace
// (C: a byte workspace of ZSTD_SLIPBLOCK_WORKSPACESIZE; typed here).
// C: zstd_preSplit.c:ZSTD_splitBlock
func splitBlock(src []byte, blockSize int, level int, wksp *fpStats) int {
	if level == 0 {
		return splitBlockFromBorders(src, blockSize, wksp)
	}
	return splitBlockByChunks(src, blockSize, level-1, wksp)
}
