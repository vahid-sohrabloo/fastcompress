// Port of libzstd v1.5.7 lib/dictBuilder/fastcover.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the fastCover
// dictionary trainer, COVER with dmer frequencies counted by hash.

package zstd

const (
	fastCoverMaxF               = 31   // C: FASTCOVER_MAX_F
	fastCoverMaxAccel           = 10   // C: FASTCOVER_MAX_ACCEL
	fastCoverDefaultSplitPoint  = 0.75 // C: FASTCOVER_DEFAULT_SPLITPOINT
	fastCoverDefaultF           = 20   // C: DEFAULT_F
	fastCoverDefaultAccel       = 1    // C: DEFAULT_ACCEL
	fastCoverMaxZeroScoreRun    = 10   // C: FASTCOVER_buildDictionary's maxZeroScoreRun
	fastCoverPassesOverTheDmers = 1    // C: FASTCOVER_buildDictionary's passes
)

// fastCoverAccel: finalize is the percentage of training samples
// finalizeDictionary uses, skip the number of dmers skipped between each
// dmer counted. C: fastcover.c:FASTCOVER_accel_t
type fastCoverAccel struct{ finalize, skip uint32 }

// fastCoverDefaultAccelParameters, by accel.
// C: fastcover.c:FASTCOVER_defaultAccelParameters
var fastCoverDefaultAccelParameters = [fastCoverMaxAccel + 1]fastCoverAccel{
	{100, 0}, // accel = 0, should not happen because accel = 0 defaults to accel = 1
	{100, 0}, // accel = 1
	{50, 1},  // accel = 2
	{34, 2},  // accel = 3
	{25, 3},  // accel = 4
	{20, 4},  // accel = 5
	{17, 5},  // accel = 6
	{14, 6},  // accel = 7
	{13, 7},  // accel = 8
	{11, 8},  // accel = 9
	{10, 9},  // accel = 10
}

// fastCoverCtx is the state shared by every k for one d.
// C: fastcover.c:FASTCOVER_ctx_t
type fastCoverCtx struct {
	s           *trainSamples
	sp          trainSplit
	freqs       []uint32 // by hash: the number of dmers with it
	d, f        uint32
	accelParams fastCoverAccel
}

// hashPtrToIndex hashes the d-byte value at position i into f bits (8
// bytes are read). C: fastcover.c:FASTCOVER_hashPtrToIndex
func (ctx *fastCoverCtx) hashPtrToIndex(i uint32) uint32 {
	if ctx.d == 6 {
		return hashPtr(ctx.s.buf, int(i), ctx.f, 6)
	}
	return hashPtr(ctx.s.buf, int(i), ctx.f, 8)
}

// fastCoverCheckParameters. C: fastcover.c:FASTCOVER_checkParameters
func fastCoverCheckParameters(p *CoverParams, maxDictSize uint64, f, accel uint32) bool {
	// k, d, and f are required parameters
	if p.D == 0 || p.K == 0 {
		return false
	}
	// d has to be 6 or 8
	if p.D != 6 && p.D != 8 {
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
	// 0 < f <= FASTCOVER_MAX_F
	if f > fastCoverMaxF || f == 0 {
		return false
	}
	// 0 < splitPoint <= 1
	if p.SplitPoint <= 0 || p.SplitPoint > 1 {
		return false
	}
	// 0 < accel <= 10
	if accel > 10 || accel == 0 {
		return false
	}
	return true
}

// newFastCoverCtx prepares the context for d: the frequency of each dmer
// hash in the training samples. C: fastcover.c:FASTCOVER_ctx_init
// (with FASTCOVER_computeFrequency). C allocates the 4 << f bytes table
// even for an f its parameter check then rejects (in a search, for every
// candidate): from f 32 on, Go returns the search's result, GENERIC,
// without allocating. (C's calloc succeeds under overcommit up to about
// f 35, then fails with memory_allocation: machine-dependent.) On 32-bit
// platforms, tables (10 << f bytes with a worker's copies) and samples
// beyond 1 GiB give memory_allocation (see trainSamples.fits32).
func newFastCoverCtx(s *trainSamples, d uint32, splitPoint float64, f uint32, accelParams fastCoverAccel) (*fastCoverCtx, error) {
	sp, err := s.split(d, splitPoint)
	if err != nil {
		return nil, err
	}
	ctx := &fastCoverCtx{s: s, sp: sp, d: d, f: f, accelParams: accelParams}
	// Initialize frequency array of size 2^f
	if f > fastCoverMaxF {
		return nil, trainError(int(dErrGeneric), "f above 31: every candidate fails")
	}
	if !s.fits32(10 << f) {
		return nil, trainError(cErrMemoryAllocation, "tables too large for a 32-bit platform")
	}
	s.concat()
	ctx.freqs = make([]uint32, 1<<f)
	// Computing frequencies
	readLength := uint64(max(d, 8))
	skip := uint64(accelParams.skip)
	for i := range sp.nbTrain {
		start := s.offsets[i] // start of current dmer
		currSampleEnd := s.offsets[i+1]
		for start+readLength <= currSampleEnd {
			ctx.freqs[ctx.hashPtrToIndex(uint32(start))]++
			start = start + skip + 1
		}
	}
	return ctx, nil
}

// selectSegment selects the best segment in an epoch, scored by the
// frequencies of the distinct dmer hashes in it; those frequencies are
// then zeroed. segmentFreqs must be all zero, and is left so.
// C: fastcover.c:FASTCOVER_selectSegment
func (ctx *fastCoverCtx) selectSegment(freqs []uint32, begin, end uint32, k, d uint32, segmentFreqs []uint16) coverSegment {
	dmersInK := k - d + 1
	// Try each segment (activeSegment) and save the best (bestSegment)
	var bestSegment coverSegment
	activeSegment := coverSegment{begin: begin, end: begin}
	// Slide the activeSegment through the whole epoch.
	for activeSegment.end < end {
		// Get hash value of current dmer
		idx := ctx.hashPtrToIndex(activeSegment.end)
		// Add frequency of this index to score if this is the first
		// occurrence of index in active segment
		if segmentFreqs[idx] == 0 {
			activeSegment.score += freqs[idx]
		}
		// Increment end of segment and segmentFreqs
		activeSegment.end++
		segmentFreqs[idx]++
		// If the window is now too large, drop the first position
		if activeSegment.end-activeSegment.begin == dmersInK+1 {
			// Get hash value of the dmer to be eliminated from active segment
			delIndex := ctx.hashPtrToIndex(activeSegment.begin)
			segmentFreqs[delIndex]--
			// Subtract frequency of this index from score if this is the
			// last occurrence of this index in active segment
			if segmentFreqs[delIndex] == 0 {
				activeSegment.score -= freqs[delIndex]
			}
			// Increment start of segment
			activeSegment.begin++
		}
		// If this segment is the best so far save it
		if activeSegment.score > bestSegment.score {
			bestSegment = activeSegment
		}
	}
	// Zero out rest of segmentFreqs array
	for activeSegment.begin < end {
		segmentFreqs[ctx.hashPtrToIndex(activeSegment.begin)]--
		activeSegment.begin++
	}
	// Zero the frequency of hash value of each dmer covered by the chosen segment.
	for pos := bestSegment.begin; pos != bestSegment.end; pos++ {
		freqs[ctx.hashPtrToIndex(pos)] = 0
	}
	return bestSegment
}

// buildDictionary. C: fastcover.c:FASTCOVER_buildDictionary (see
// coverCtx.buildDictionary). Below 8 training bytes C's dmer count wraps
// (C then reads out of bounds or divides by zero); Go returns an error.
func (ctx *fastCoverCtx) buildDictionary(freqs []uint32, capacity uint64, k, d uint32, segmentFreqs []uint16) ([]byte, error) {
	if ctx.sp.trainSize < uint64(max(d, 8)) {
		return nil, &buildDictError{code: int(dErrSrcSizeWrong), err: "training samples smaller than a dmer (C reads out of bounds)", undefined: true}
	}
	nbDmers := ctx.sp.trainSize - uint64(max(d, 8)) + 1
	tail := capacity
	// Divide the data into epochs. We will select one segment from each epoch.
	epochs, err := computeEpochs(uint32(capacity), uint32(nbDmers), k, fastCoverPassesOverTheDmers)
	if err != nil {
		return nil, err
	}
	var zeroScoreRun uint64
	var segs []segmentCopy
	// Loop through the epochs until there are no more segments or the
	// dictionary is full.
	for epoch := uint64(0); tail > 0; epoch = (epoch + 1) % uint64(epochs.num) {
		epochBegin := uint32(epoch * uint64(epochs.size))
		epochEnd := epochBegin + epochs.size
		// Select a segment
		segment := ctx.selectSegment(freqs, epochBegin, epochEnd, k, d, segmentFreqs)
		// If the segment covers no dmers, then we are out of content.
		// There may be new content in other epochs, for continue for some time.
		if segment.score == 0 {
			zeroScoreRun++
			if zeroScoreRun >= fastCoverMaxZeroScoreRun {
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

// nbFinalizeSamples is the number of training samples finalizeDictionary
// uses at this acceleration.
func (ctx *fastCoverCtx) nbFinalizeSamples() int {
	return int(uint64(ctx.sp.nbTrain) * uint64(ctx.accelParams.finalize) / 100)
}

// trainFastCover. C: fastcover.c:ZDICT_trainFromBuffer_fastCover
func trainFastCover(s *trainSamples, maxDictSize uint64, p CoverParams) ([]byte, error) {
	// Assign splitPoint and f if not provided
	p.SplitPoint = 1.0
	if p.F == 0 {
		p.F = fastCoverDefaultF
	}
	if p.Accel == 0 {
		p.Accel = fastCoverDefaultAccel
	}
	// Checks
	if !fastCoverCheckParameters(&p, maxDictSize, p.F, p.Accel) {
		return nil, trainError(cErrParameterOutOfBound, "FASTCOVER parameters incorrect")
	}
	if len(s.list) == 0 {
		return nil, trainError(int(dErrSrcSizeWrong), "FASTCOVER must have at least one input file")
	}
	if maxDictSize < zdictDictSizeMin {
		return nil, trainError(int(dErrDstSizeTooSmall), "dictionary capacity must be at least 256")
	}
	// Assign corresponding FASTCOVER_accel_t to accelParams
	accelParams := fastCoverDefaultAccelParameters[p.Accel]
	// Initialize context
	ctx, err := newFastCoverCtx(s, p.D, p.SplitPoint, p.F, accelParams)
	if err != nil {
		return nil, err
	}
	// Build the dictionary
	segmentFreqs := make([]uint16, 1<<p.F)
	content, err := ctx.buildDictionary(ctx.freqs, maxDictSize, p.K, p.D, segmentFreqs)
	if err != nil {
		return nil, err
	}
	return finalizeDictionaryCap(content, s.list[:ctx.nbFinalizeSamples()], p.Level, p.DictID, repStartValue, maxDictSize, nil)
}

// optimizeTrainFastCover searches k and d.
// C: fastcover.c:ZDICT_optimizeTrainFromBuffer_fastCover (single-threaded
// C's choice: see trainSearch)
func optimizeTrainFastCover(s *trainSamples, maxDictSize uint64, p CoverParams) ([]byte, CoverParams, error) {
	// constants
	splitPoint := p.SplitPoint
	if splitPoint <= 0.0 {
		splitPoint = fastCoverDefaultSplitPoint
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
	f := p.F
	if f == 0 {
		f = fastCoverDefaultF
	}
	accel := p.Accel
	if accel == 0 {
		accel = fastCoverDefaultAccel
	}
	// Checks
	if splitPoint <= 0 || splitPoint > 1 {
		return nil, p, trainError(cErrParameterOutOfBound, "incorrect splitPoint")
	}
	if accel == 0 || accel > fastCoverMaxAccel {
		return nil, p, trainError(cErrParameterOutOfBound, "incorrect accel")
	}
	if kMinK < kMaxD || kMaxK < kMinK {
		return nil, p, trainError(cErrParameterOutOfBound, "incorrect k")
	}
	if len(s.list) == 0 {
		return nil, p, trainError(int(dErrSrcSizeWrong), "FASTCOVER must have at least one input file")
	}
	if maxDictSize < zdictDictSizeMin {
		return nil, p, trainError(int(dErrDstSizeTooSmall), "dictionary capacity must be at least 256")
	}
	// Initialization
	accelParams := fastCoverDefaultAccelParameters[accel]
	var search trainSearch
	compressMem := compressMemoryEstimate(p.Level, s.maxSampleSize(), min(maxDictSize, s.total))
	// Loop through d first because each new value needs a new context
	// (C's loops are unsigned; these stop where C's would wrap)
	for d := uint64(kMinD); d <= uint64(kMaxD); d += 2 {
		ctx, err := newFastCoverCtx(s, uint32(d), splitPoint, f, accelParams)
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
			cp.F, cp.Accel = f, accel // C: FASTCOVER_convertToFastCoverParams
			// Check the parameters
			if !fastCoverCheckParameters(&cp, maxDictSize, ctx.f, accel) {
				continue
			}
			cands = append(cands, cp)
		}
		search.run(cands, 6<<f+compressMem, func() func(CoverParams) coverSelection {
			freqs := make([]uint32, len(ctx.freqs))
			segmentFreqs := make([]uint16, len(ctx.freqs))
			return func(cp CoverParams) coverSelection { return ctx.tryParameters(freqs, segmentFreqs, maxDictSize, &cp) }
		})
	}
	return search.result()
}

// tryParameters builds and measures the dictionary of one parameter set
// (freqs and segmentFreqs are the worker's scratch tables).
// C: fastcover.c:FASTCOVER_tryParameters
func (ctx *fastCoverCtx) tryParameters(freqs []uint32, segmentFreqs []uint16, maxDictSize uint64, p *CoverParams) coverSelection {
	// Copy the frequencies because we need to modify them
	copy(freqs, ctx.freqs)
	// Build the dictionary
	content, err := ctx.buildDictionary(freqs, maxDictSize, p.K, p.D, segmentFreqs)
	if err != nil {
		return coverSelection{err: err}
	}
	return selectDict(content, maxDictSize, ctx.s, ctx.nbFinalizeSamples(), ctx.sp.nbTrain, p)
}
