// Port of libzstd v1.5.7 lib/dictBuilder/cover.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the COVER
// dictionary trainer, from Liao, Petri, Moffat and Wirth, "Effective
// Construction of Relative Lempel-Ziv Dictionaries" (WWW 2016).

package zstd

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"slices"
	"sort"
)

const coverDefaultSplitPoint = 1.0 // C: COVER_DEFAULT_SPLITPOINT

// coverMapEmptyValue marks a free slot. C: cover.c:MAP_EMPTY_VALUE
const coverMapEmptyValue = ^uint32(0)

// coverMap is a small hash map for the active dmers of a segment: linear
// probing, never resized, load below 0.5. C: cover.c:COVER_map_t
type coverMap struct {
	data     []coverMapPair
	sizeLog  uint32
	sizeMask uint32
}

type coverMapPair struct{ key, value uint32 }

// newCoverMap returns a map for at most size live keys.
// C: cover.c:COVER_map_init — C sizes it for k - d + 1 keys; callers here
// bound size by the number of distinct dmers too (a segment cannot hold
// more), so the map does not grow with k beyond the samples. Its size has
// no effect on the results.
func newCoverMap(size uint32) *coverMap {
	m := &coverMap{sizeLog: min(highbit32(size)+2, 31)}
	m.data = make([]coverMapPair, 1<<m.sizeLog)
	m.sizeMask = 1<<m.sizeLog - 1
	m.clear()
	return m
}

// clear. C: cover.c:COVER_map_clear
func (m *coverMap) clear() {
	for i := range m.data {
		m.data[i] = coverMapPair{coverMapEmptyValue, coverMapEmptyValue}
	}
}

// hash. C: cover.c:COVER_map_hash
func (m *coverMap) hash(key uint32) uint32 {
	return (key * prime4bytes) >> (32 - m.sizeLog)
}

// index returns the slot key is in or should be placed into.
// C: cover.c:COVER_map_index
func (m *coverMap) index(key uint32) uint32 {
	for i := m.hash(key); ; i = (i + 1) & m.sizeMask {
		pos := &m.data[i]
		if pos.value == coverMapEmptyValue || pos.key == key {
			return i
		}
	}
}

// at returns the value of key, inserting it with value 0 if missing.
// C: cover.c:COVER_map_at
func (m *coverMap) at(key uint32) *uint32 {
	pos := &m.data[m.index(key)]
	if pos.value == coverMapEmptyValue {
		pos.key = key
		pos.value = 0
	}
	return &pos.value
}

// remove deletes key if present. C: cover.c:COVER_map_remove
func (m *coverMap) remove(key uint32) {
	i := m.index(key)
	del := &m.data[i]
	shift := uint32(1)
	if del.value == coverMapEmptyValue {
		return
	}
	for i = (i + 1) & m.sizeMask; ; i = (i + 1) & m.sizeMask {
		pos := &m.data[i]
		// If the position is empty we are done
		if pos.value == coverMapEmptyValue {
			del.value = coverMapEmptyValue
			return
		}
		// If pos can be moved to del do so
		if ((i - m.hash(pos.key)) & m.sizeMask) >= shift {
			del.key = pos.key
			del.value = pos.value
			del = pos
			shift = 1
		} else {
			shift++
		}
	}
}

// coverCtx is the state shared by every k for one d. C: cover.c:COVER_ctx_t
type coverCtx struct {
	s          *trainSamples
	sp         trainSplit
	suffixSize uint64
	freqs      []uint32 // by dmerID: the number of samples the dmer is in
	dmerAt     []uint32 // the dmerID at each position
	d          uint32
}

// coverCheckParameters. C: cover.c:COVER_checkParameters
func coverCheckParameters(p *CoverParams, maxDictSize uint64) bool {
	// k and d are required parameters
	if p.D == 0 || p.K == 0 {
		return false
	}
	// k <= maxDictSize
	if uint64(p.K) > maxDictSize {
		return false
	}
	// d <= k
	if p.D > p.K {
		return false
	}
	// 0 < splitPoint <= 1
	if p.SplitPoint <= 0 || p.SplitPoint > 1 {
		return false
	}
	return true
}

// newCoverCtx prepares the context for d: the partial suffix array (sorted
// by the first d bytes, positions ascending within a dmer), dmerAt and the
// frequency of each dmer. C: cover.c:COVER_ctx_init
//
// C sorts with qsort_r and its own comparator (memcmp, or a masked
// little-endian load for d <= 8), ties broken by position. Only the
// grouping of equal dmers, positions ascending, reaches the output (a
// dmerID merely names a group), and any sort by dmer then position gives
// it: the output is byte-identical to C on glibc's qsort_r mergesort path,
// which is stable. Where C's qsort is not (glibc's quicksort fallback above
// a quarter of physical memory, macOS, MSVC, musl), C's own output depends
// on the platform.
func newCoverCtx(s *trainSamples, d uint32, splitPoint float64) (*coverCtx, error) {
	sp, err := s.split(d, splitPoint)
	if err != nil {
		return nil, err
	}
	ctx := &coverCtx{s: s, sp: sp, d: d}
	// Partial suffix array. C's size_t arithmetic wraps below
	// max(d, 8) - 1 training bytes, and its malloc fails.
	readLen := uint64(max(d, 8))
	if sp.trainSize+1 < readLen {
		return nil, trainError(cErrMemoryAllocation, "training samples smaller than a dmer")
	}
	ctx.suffixSize = sp.trainSize + 1 - readLen
	// Go guard (32-bit): suffix and dmerAt, and a worker's frequency copy,
	// 12 bytes per dmer.
	if !s.fits32(12 * ctx.suffixSize) {
		return nil, trainError(cErrMemoryAllocation, "tables too large for a 32-bit platform")
	}
	s.concat()
	suffix := make([]uint32, ctx.suffixSize)
	ctx.dmerAt = make([]uint32, ctx.suffixSize)
	for i := range suffix {
		suffix[i] = uint32(i)
	}
	samples := s.buf
	var eq func(a, b uint32) bool
	if d <= 8 {
		mask := ^uint64(0)
		if d < 8 {
			mask = 1<<(8*d) - 1
		}
		key := func(i uint32) uint64 { return binary.LittleEndian.Uint64(samples[i:]) & mask }
		slices.SortFunc(suffix, func(a, b uint32) int {
			if c := cmp.Compare(key(a), key(b)); c != 0 {
				return c
			}
			return cmp.Compare(a, b)
		})
		eq = func(a, b uint32) bool { return key(a) == key(b) }
	} else {
		dmer := func(i uint32) []byte { return samples[i : uint64(i)+uint64(d)] }
		slices.SortFunc(suffix, func(a, b uint32) int {
			if c := bytes.Compare(dmer(a), dmer(b)); c != 0 {
				return c
			}
			return cmp.Compare(a, b)
		})
		eq = func(a, b uint32) bool { return bytes.Equal(dmer(a), dmer(b)) }
	}
	// Computing frequencies (C: COVER_groupBy with COVER_group)
	for i := 0; i < len(suffix); {
		j := i + 1
		for j < len(suffix) && eq(suffix[i], suffix[j]) {
			j++
		}
		ctx.group(suffix, i, j)
		i = j
	}
	ctx.freqs = suffix
	return ctx, nil
}

// group counts the samples the dmer of suffix[begin:end] occurs in, stores
// it in suffix[begin] (its dmerID) and fills dmerAt. C: cover.c:COVER_group
func (ctx *coverCtx) group(suffix []uint32, begin, end int) {
	dmerID := uint32(begin)
	var freq uint32
	offsets := ctx.s.offsets
	nbSamples := len(ctx.s.list)
	curOffset := 0
	// Once pos >= curSampleEnd this occurrence of the dmer is in a
	// different sample than the last.
	curSampleEnd := offsets[0]
	for i := begin; i < end; i++ {
		pos := suffix[i]
		ctx.dmerAt[pos] = dmerID
		// Dictionaries only help for the first reference to the dmer.
		if uint64(pos) < curSampleEnd {
			continue
		}
		freq++
		// Binary search to find the end of the sample pos is in
		// (C: COVER_lower_bound, which stops at a sample starting at
		// pos itself).
		if i+1 != end {
			n := sort.Search(nbSamples-curOffset, func(x int) bool { return offsets[curOffset+x] >= uint64(pos) })
			curSampleEnd = offsets[curOffset+n]
			curOffset += n + 1
		}
	}
	suffix[dmerID] = freq
}

// selectSegment selects the best segment in an epoch: the k-byte segment
// with the highest sum of the frequencies of its distinct dmers, trimmed of
// zero-frequency dmers at both ends; the frequencies of its dmers are then
// zeroed. C: cover.c:COVER_selectSegment
func (ctx *coverCtx) selectSegment(freqs []uint32, activeDmers *coverMap, begin, end uint32, k, d uint32) coverSegment {
	dmersInK := k - d + 1
	// Try each segment (activeSegment) and save the best (bestSegment)
	var bestSegment coverSegment
	activeDmers.clear()
	activeSegment := coverSegment{begin: begin, end: begin}
	for activeSegment.end < end {
		// The dmerId for the dmer at the next position
		newDmer := ctx.dmerAt[activeSegment.end]
		newDmerOcc := activeDmers.at(newDmer)
		// If the dmer isn't already present in the segment add its score.
		if *newDmerOcc == 0 {
			activeSegment.score += freqs[newDmer]
		}
		// Add the dmer to the segment
		activeSegment.end++
		*newDmerOcc++

		// If the window is now too large, drop the first position
		if activeSegment.end-activeSegment.begin == dmersInK+1 {
			delDmer := ctx.dmerAt[activeSegment.begin]
			delDmerOcc := activeDmers.at(delDmer)
			activeSegment.begin++
			*delDmerOcc--
			// If this is the last occurrence of the dmer, subtract its score
			if *delDmerOcc == 0 {
				activeDmers.remove(delDmer)
				activeSegment.score -= freqs[delDmer]
			}
		}

		// If this segment is the best so far save it
		if activeSegment.score > bestSegment.score {
			bestSegment = activeSegment
		}
	}
	{
		// Trim off the zero frequency head and tail from the segment.
		newBegin := bestSegment.end
		newEnd := bestSegment.begin
		for pos := bestSegment.begin; pos != bestSegment.end; pos++ {
			if freqs[ctx.dmerAt[pos]] != 0 {
				newBegin = min(newBegin, pos)
				newEnd = pos + 1
			}
		}
		bestSegment.begin = newBegin
		bestSegment.end = newEnd
	}
	// Zero out the frequency of each dmer covered by the chosen segment.
	for pos := bestSegment.begin; pos != bestSegment.end; pos++ {
		freqs[ctx.dmerAt[pos]] = 0
	}
	return bestSegment
}

// newActiveDmers is the map for a segment of k bytes.
func (ctx *coverCtx) newActiveDmers(k uint32) *coverMap {
	return newCoverMap(uint32(min(uint64(k-ctx.d+1), max(ctx.suffixSize, 1))))
}

// buildDictionary selects one segment per epoch, round robin, until the
// dictionary is full or the content runs out, and returns the content
// (C fills it from the back of the buffer, so the best segments are
// referenced with the smallest offsets). C: cover.c:COVER_buildDictionary
func (ctx *coverCtx) buildDictionary(freqs []uint32, activeDmers *coverMap, capacity uint64, k, d uint32) ([]byte, error) {
	tail := capacity
	// Divide the data into epochs. We will select one segment from each epoch.
	epochs, err := computeEpochs(uint32(capacity), uint32(ctx.suffixSize), k, 4)
	if err != nil {
		return nil, err
	}
	maxZeroScoreRun := uint64(max(10, min(100, epochs.num>>3)))
	var zeroScoreRun uint64
	var segs []segmentCopy
	// Loop through the epochs until there are no more segments or the
	// dictionary is full.
	for epoch := uint64(0); tail > 0; epoch = (epoch + 1) % uint64(epochs.num) {
		epochBegin := uint32(epoch * uint64(epochs.size))
		epochEnd := epochBegin + epochs.size
		// Select a segment
		segment := ctx.selectSegment(freqs, activeDmers, epochBegin, epochEnd, k, d)
		// If the segment covers no dmers, then we are out of content.
		// There may be new content in other epochs, for continue for some time.
		if segment.score == 0 {
			zeroScoreRun++
			if zeroScoreRun >= maxZeroScoreRun {
				break
			}
			continue
		}
		zeroScoreRun = 0
		// Trim the segment if necessary and if it is too small then we are done
		segmentSize := min(uint64(segment.end-segment.begin+d-1), tail)
		if segmentSize < uint64(d) {
			break
		}
		tail -= segmentSize
		segs = append(segs, segmentCopy{segment.begin, segmentSize})
	}
	return assembleContent(ctx.s.buf, segs), nil
}

// trainCover. C: cover.c:ZDICT_trainFromBuffer_cover
func trainCover(s *trainSamples, maxDictSize uint64, p CoverParams) ([]byte, error) {
	p.SplitPoint = 1.0
	// Checks
	if !coverCheckParameters(&p, maxDictSize) {
		return nil, trainError(cErrParameterOutOfBound, "cover parameters incorrect")
	}
	if len(s.list) == 0 {
		return nil, trainError(int(dErrSrcSizeWrong), "cover must have at least one input file")
	}
	if maxDictSize < zdictDictSizeMin {
		return nil, trainError(int(dErrDstSizeTooSmall), "dictionary capacity must be at least 256")
	}
	// Initialize context and activeDmers
	ctx, err := newCoverCtx(s, p.D, p.SplitPoint)
	if err != nil {
		return nil, err
	}
	content, err := ctx.buildDictionary(ctx.freqs, ctx.newActiveDmers(p.K), maxDictSize, p.K, p.D)
	if err != nil {
		return nil, err
	}
	return finalizeDictionaryCap(content, s.list, p.Level, p.DictID, repStartValue, maxDictSize, nil)
}

// optimizeTrainCover searches k and d. C: cover.c:ZDICT_optimizeTrainFromBuffer_cover
// (single-threaded C's choice: see trainSearch)
func optimizeTrainCover(s *trainSamples, maxDictSize uint64, p CoverParams) ([]byte, CoverParams, error) {
	// constants
	splitPoint := p.SplitPoint
	if splitPoint <= 0.0 {
		splitPoint = coverDefaultSplitPoint
	}
	kMinD, kMaxD := p.D, p.D
	if p.D == 0 {
		kMinD, kMaxD = 6, 8
	}
	kMinK, kMaxK := p.K, p.K
	if p.K == 0 {
		kMinK, kMaxK = 50, 2000
	}
	kSteps := p.Steps
	if kSteps == 0 {
		kSteps = 40
	}
	kStepSize := max((kMaxK-kMinK)/kSteps, 1)

	// Checks
	if splitPoint <= 0 || splitPoint > 1 {
		return nil, p, trainError(cErrParameterOutOfBound, "incorrect parameters")
	}
	if kMinK < kMaxD || kMaxK < kMinK {
		return nil, p, trainError(cErrParameterOutOfBound, "incorrect parameters")
	}
	if len(s.list) == 0 {
		return nil, p, trainError(int(dErrSrcSizeWrong), "cover must have at least one input file")
	}
	if maxDictSize < zdictDictSizeMin {
		return nil, p, trainError(int(dErrDstSizeTooSmall), "dictionary capacity must be at least 256")
	}
	var search trainSearch
	compressMem := compressMemoryEstimate(p.Level, s.maxSampleSize(), min(maxDictSize, s.total))
	// Loop through d first because each new value needs a new context
	// (C's loops are unsigned; these stop where C's would wrap)
	for d := uint64(kMinD); d <= uint64(kMaxD); d += 2 {
		ctx, err := newCoverCtx(s, uint32(d), splitPoint)
		if err != nil {
			return nil, p, err
		}
		// Loop through k reusing the same context
		var cands []CoverParams
		for k := uint64(kMinK); k <= uint64(kMaxK); k += uint64(kStepSize) {
			cp := p
			cp.K, cp.D = uint32(k), uint32(d)
			cp.SplitPoint = splitPoint
			cp.Steps = kSteps
			cp.ShrinkDict = false
			// Check the parameters
			if !coverCheckParameters(&cp, maxDictSize) {
				continue
			}
			cands = append(cands, cp)
		}
		search.run(cands, 4*ctx.suffixSize+compressMem, func() func(CoverParams) coverSelection {
			freqs := make([]uint32, len(ctx.freqs))
			return func(cp CoverParams) coverSelection { return ctx.tryParameters(freqs, maxDictSize, &cp) }
		})
	}
	return search.result()
}

// tryParameters builds and measures the dictionary of one parameter set
// (freqs is the worker's scratch copy of the frequencies).
// C: cover.c:COVER_tryParameters
func (ctx *coverCtx) tryParameters(freqs []uint32, maxDictSize uint64, p *CoverParams) coverSelection {
	// Copy the frequencies because we need to modify them
	copy(freqs, ctx.freqs)
	// Build the dictionary
	content, err := ctx.buildDictionary(freqs, ctx.newActiveDmers(p.K), maxDictSize, p.K, p.D)
	if err != nil {
		return coverSelection{err: err}
	}
	return selectDict(content, maxDictSize, ctx.s, ctx.sp.nbTrain, ctx.sp.nbTrain, p)
}
