// Port of the dictionary trainers' entry points and shared parts of
// libzstd v1.5.7 lib/dictBuilder/zdict.c (ZDICT_trainFromBuffer) and
// lib/dictBuilder/cover.c (Copyright (c) Meta Platforms, Inc. and
// affiliates; BSD/GPLv2 dual license): the parameter search, the
// dictionary selection and the compressed-size check.

package zstd

import (
	"errors"
	"math"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
)

// CoverParams are the parameters of the COVER and fastCover dictionary
// trainers. Zero means C's default. The fields map 1:1 to C's
// ZDICT_cover_params_t and ZDICT_fastCover_params_t (nbThreads aside: the
// parameter search uses up to GOMAXPROCS goroutines).
type CoverParams struct {
	K     uint32 // segment size; 0 with D 0 or set: search 50..2000
	D     uint32 // dmer size; 0: search 6 and 8 (fastCover: 6 or 8 only)
	Steps uint32 // number of k values searched (0: 40)
	// F is fastCover's log2 of the frequency table size, 1..31 (0: 20).
	// Tables take 4 << F bytes, plus 6 << F per concurrently tried
	// candidate, all written: 12 GiB per candidate at F 31 (as in C).
	F     uint32
	Accel uint32 // fastCover: acceleration, 1..10 (0: 1)
	// SplitPoint is the share of samples trained on during the search, the
	// rest being used to test (0: 1.0 for COVER, 0.75 for fastCover).
	SplitPoint float64
	// ShrinkDict and ShrinkDictMaxRegression have no effect: C v1.5.7's
	// searches force shrinking off and its fixed-parameter trainers
	// ignore it.
	ShrinkDict              bool
	ShrinkDictMaxRegression uint32
	Level                   int    // C compression level the dictionary is tuned for (0: 3)
	DictID                  uint32 // 0: C's ID derived from the content
}

// TrainDict trains a dictionary of at most maxDictSize bytes on samples.
// It is C's ZDICT_trainFromBuffer (libzstd v1.5.7): a fastCover parameter
// search with d 8, 4 steps of k, f 20, accel 1 and level 3. The output is
// byte-identical to C's. The samples are first copied into one buffer, as
// C takes them.
func TrainDict(samples [][]byte, maxDictSize int) ([]byte, error) {
	dict, _, err := TrainDictFastCover(samples, maxDictSize, CoverParams{D: 8, Steps: 4, Level: clevelDefault})
	if err != nil {
		setTrainOp(err, "TrainDict")
	}
	return dict, err
}

// TrainDictCover trains a dictionary of at most maxDictSize bytes on
// samples with COVER: C's ZDICT_optimizeTrainFromBuffer_cover when p.K or
// p.D is 0, otherwise ZDICT_trainFromBuffer_cover (libzstd v1.5.7). It
// returns the parameters as C leaves them in its parameter struct: the
// chosen ones after a search, p unchanged otherwise. Output and parameters
// are byte-identical to C's (single-threaded: ties go to the first
// parameters C tries).
func TrainDictCover(samples [][]byte, maxDictSize int, p CoverParams) ([]byte, CoverParams, error) {
	s, err := newTrainSamples(samples, maxDictSize)
	if err != nil {
		return nil, p, err
	}
	var dict []byte
	out := p
	if p.K == 0 || p.D == 0 {
		dict, out, err = optimizeTrainCover(s, uint64(maxDictSize), p)
	} else {
		dict, err = trainCover(s, uint64(maxDictSize), p)
	}
	if err != nil {
		setTrainOp(err, "TrainDictCover")
		return nil, p, err
	}
	return dict, out, nil
}

// TrainDictFastCover is TrainDictCover with fastCover: C's
// ZDICT_optimizeTrainFromBuffer_fastCover when p.K or p.D is 0, otherwise
// ZDICT_trainFromBuffer_fastCover.
//
// The frequency tables take 4 << F bytes plus 6 << F per concurrently
// tried candidate, which copies them (C: the same per thread): 10 MiB for
// one candidate at the default F, 12 GiB per candidate at F 31.
func TrainDictFastCover(samples [][]byte, maxDictSize int, p CoverParams) ([]byte, CoverParams, error) {
	s, err := newTrainSamples(samples, maxDictSize)
	if err != nil {
		return nil, p, err
	}
	var dict []byte
	out := p
	if p.K == 0 || p.D == 0 {
		dict, out, err = optimizeTrainFastCover(s, uint64(maxDictSize), p)
	} else {
		dict, err = trainFastCover(s, uint64(maxDictSize), p)
	}
	if err != nil {
		setTrainOp(err, "TrainDictFastCover")
		return nil, p, err
	}
	return dict, out, nil
}

// setTrainOp names the public function in a C error.
func setTrainOp(err error, op string) {
	var ce *buildDictError
	if errors.As(err, &ce) {
		ce.op = op
	}
}

// C error codes of the trainers besides cErrorNames' and zdict.go's.
const cErrParameterOutOfBound = 42 // C: ZSTD_error_parameter_outOfBound

func trainError(code int, msg string) error { return &buildDictError{code: code, err: msg} }

// trainMaxSamplesSize is the largest total sample size (exclusive): 32-bit
// indices reference the samples. C: cover.c:COVER_MAX_SAMPLES_SIZE (and
// fastcover.c's), 4 GiB on 64-bit builds and 1 GiB on 32-bit ones.
var trainMaxSamplesSize uint64 = func() uint64 {
	if strconv.IntSize == 64 {
		return math.MaxUint32
	}
	return 1 << 30
}()

// trainSamples are C's samplesBuffer, samplesSizes and nbSamples.
type trainSamples struct {
	buf     []byte   // the samples, concatenated (nil when too large to train on)
	list    [][]byte // each sample, a sub-slice of buf
	offsets []uint64 // the start of each sample in buf, then its length
	total   uint64   // C: COVER_sum of all samples
}

// newTrainSamples sizes the samples; concat copies them later, once C's
// checks before reading them have passed.
func newTrainSamples(samples [][]byte, maxDictSize int) (*trainSamples, error) {
	if maxDictSize < 0 {
		return nil, errors.New("zstd: negative dictionary size")
	}
	s := &trainSamples{list: samples, offsets: make([]uint64, len(samples)+1)}
	for i, b := range samples {
		s.offsets[i+1] = s.offsets[i] + uint64(len(b))
	}
	s.total = s.offsets[len(samples)]
	return s, nil
}

// concat copies the samples into one buffer, as C takes them (once).
func (s *trainSamples) concat() {
	if s.buf != nil || s.total == 0 {
		return
	}
	samples := s.list
	s.buf = make([]byte, 0, s.total)
	s.list = make([][]byte, len(samples))
	for i, b := range samples {
		s.buf = append(s.buf, b...)
		s.list[i] = s.buf[s.offsets[i]:s.offsets[i+1]:s.offsets[i+1]]
	}
}

// fits32 is the Go guard of 32-bit platforms, where C's mallocs fail long
// before Go can allocate (and a failed Go allocation is fatal): the sample
// copy plus tables of tableBytes must fit 1 GiB. The search runs one
// candidate at a time there (trainWorkers), so tableBytes counts one
// worker's copies.
func (s *trainSamples) fits32(tableBytes uint64) bool {
	return strconv.IntSize == 64 || s.total+tableBytes <= 1<<30
}

// sum is the total size of the first n samples. C: cover.c:COVER_sum
func (s *trainSamples) sum(from, n int) uint64 { return s.offsets[from+n] - s.offsets[from] }

// trainSplit is C's split of the samples into training and testing sets
// (COVER_ctx_init and FASTCOVER_ctx_init), with its checks.
type trainSplit struct {
	nbTrain, nbTest int
	trainSize       uint64
}

func (s *trainSamples) split(d uint32, splitPoint float64) (trainSplit, error) {
	nbSamples := len(s.list)
	var sp trainSplit
	if splitPoint < 1.0 {
		sp.nbTrain = int(uint32(float64(nbSamples) * splitPoint))
		sp.nbTest = nbSamples - sp.nbTrain
		sp.trainSize = s.sum(0, sp.nbTrain)
	} else {
		sp.nbTrain, sp.nbTest, sp.trainSize = nbSamples, nbSamples, s.total
	}
	// Checks
	if s.total < uint64(max(d, 8)) || s.total >= trainMaxSamplesSize {
		return sp, trainError(int(dErrSrcSizeWrong), "total samples size is too small or too large")
	}
	// Check if there are at least 5 training samples
	if sp.nbTrain < 5 {
		return sp, trainError(int(dErrSrcSizeWrong), "fewer than 5 training samples")
	}
	// Check if there's testing sample
	if sp.nbTest < 1 {
		return sp, trainError(int(dErrSrcSizeWrong), "no testing sample")
	}
	return sp, nil
}

// coverEpochs is the number of epochs and the size of each epoch.
// C: cover.h:COVER_epoch_info_t
type coverEpochs struct{ num, size uint32 }

// computeEpochs divides nbDmers dmers into epochs of at least 10 * k
// dmers. C: cover.c:COVER_computeEpochs (U32 arithmetic). Where C divides
// by zero (no dmers) it returns an error. (When 10 * k wraps to 0, C returns
// before dividing, and so does this.)
func computeEpochs(maxDictSize, nbDmers, k, passes uint32) (coverEpochs, error) {
	minEpochSize := k * 10
	var e coverEpochs
	e.num = max(1, maxDictSize/k/passes)
	e.size = nbDmers / e.num
	if e.size >= minEpochSize {
		return e, nil
	}
	e.size = min(minEpochSize, nbDmers)
	if e.size == 0 {
		return e, &buildDictError{code: int(dErrSrcSizeWrong), err: "no dmers to train on (C divides by zero)", undefined: true}
	}
	e.num = nbDmers / e.size
	return e, nil
}

// coverSegment is a range of dmer positions and its score.
// C: cover.h:COVER_segment_t
type coverSegment struct{ begin, end, score uint32 }

// segmentCopy is one segment a trainer put into the dictionary content.
type segmentCopy struct {
	begin uint32
	size  uint64
}

// assembleContent builds the dictionary content (C: dict + tail, filled
// from the back) from the segments in the order they were chosen.
func assembleContent(samples []byte, segs []segmentCopy) []byte {
	var n uint64
	for _, sg := range segs {
		n += sg.size
	}
	content := make([]byte, n)
	pos := n
	for _, sg := range segs {
		pos -= sg.size
		copy(content[pos:], samples[sg.begin:uint64(sg.begin)+sg.size])
	}
	return content
}

// coverSelection is a candidate dictionary and its score.
// C: cover.h:COVER_dictSelection_t
type coverSelection struct {
	dict                []byte
	totalCompressedSize uint64
	err                 error
}

// selectDict finalizes content with the first nbFinalize samples and
// measures it on the test samples. C: cover.c:COVER_selectDict — shrinking
// (params.shrinkDict) is not ported: C's searches, its only callers, force
// it off.
func selectDict(content []byte, capacity uint64, s *trainSamples, nbFinalize, nbCheck int, p *CoverParams) coverSelection {
	dict, err := finalizeDictionaryCap(content, s.list[:nbFinalize], p.Level, p.DictID, repStartValue, capacity, nil)
	if err != nil {
		return coverSelection{err: err}
	}
	total, err := checkTotalCompressedSize(p, s, nbCheck, dict)
	if err != nil {
		return coverSelection{err: err}
	}
	return coverSelection{dict: dict, totalCompressedSize: total}
}

// checkTotalCompressedSize returns the dictionary size plus the size of
// every test sample compressed with it (samples from nbTrain on when
// splitting, else all). C: cover.c:COVER_checkTotalCompressedSize
func checkTotalCompressedSize(p *CoverParams, s *trainSamples, nbTrain int, dict []byte) (uint64, error) {
	// Allocate dst with enough space to compress the maximum sized sample
	first := 0
	if p.SplitPoint < 1.0 {
		first = nbTrain
	}
	maxSampleSize := 0
	for _, b := range s.list[first:] {
		maxSampleSize = max(maxSampleSize, len(b))
	}
	dst := make([]byte, compressBound(maxSampleSize))
	// Create the cctx and cdict
	cd, err := createCDictLevel(dict, p.Level)
	if err != nil {
		return 0, trainError(int(dErrGeneric), err.Error())
	}
	c := newCCtx()
	// Compress each sample and sum their sizes (or error)
	total := uint64(len(dict))
	for _, b := range s.list[first:] {
		n, err := c.compressUsingCDict(dst, b, cd)
		if err != nil {
			return 0, err
		}
		total += uint64(n)
	}
	return total, nil
}

// createCDictLevel digests a dictionary for compressing at level.
// C: zstd_compress.c:ZSTD_createCDict (content type auto)
func createCDictLevel(dict []byte, level int) (*cdict, error) {
	var params cctxParams
	cctxParamsInit(&params, 0)
	params.cParams = getCParamsInternal(level, contentSizeUnknown, uint64(len(dict)), cpmCreateCDict)
	cd, err := createCDictAdvanced2(dict, dctAuto, &params)
	if err != nil {
		return nil, err
	}
	if level == 0 {
		level = clevelDefault
	}
	cd.compressionLevel = level
	return cd, nil
}

// cdictParamsSrcSizeCutoff and cdictParamsDictSizeMultiplier: below either
// bound a frame uses the CDict's parameters (and tables).
// C: zstd_compress.c:ZSTD_USE_CDICT_PARAMS_SRCSIZE_CUTOFF and
// ZSTD_USE_CDICT_PARAMS_DICTSIZE_MULTIPLIER
const (
	cdictParamsSrcSizeCutoff      = 128 << 10
	cdictParamsDictSizeMultiplier = 6
)

// compressUsingCDict compresses src into one frame (content size, no
// checksum, dictID) in dst with cd and returns its size.
// C: zstd_compress.c:ZSTD_compress_usingCDict, through
// ZSTD_compressBegin_usingCDict_internal, ZSTD_compressBegin_internal and
// ZSTD_compressEnd_public.
func (c *cctx) compressUsingCDict(dst, src []byte, cd *cdict) (int, error) {
	pledgedSrcSize := uint64(len(src))
	useCDict := pledgedSrcSize < cdictParamsSrcSizeCutoff ||
		pledgedSrcSize < uint64(cd.dictContentSize)*cdictParamsDictSizeMultiplier ||
		cd.compressionLevel == 0

	// Initialize the cctxParams from the cdict
	// (C: ZSTD_CCtxParams_init_internal)
	var params cctxParams
	if useCDict {
		params.cParams = cd.matchState.cParams
	} else {
		params.cParams = getCParamsInternal(cd.compressionLevel, pledgedSrcSize, uint64(cd.dictContentSize), cpmUnknown)
	}
	params.fParams = frameParameters{contentSizeFlag: true}
	params.compressionLevel = cd.compressionLevel
	params.useRowMatchFinder = resolveRowMatchFinderMode(psAuto, &params.cParams)
	params.postBlockSplitter = resolveBlockSplitterMode(psAuto, &params.cParams)
	params.ldmParams.enableLdm = resolveEnableLdm(psAuto, &params.cParams)
	params.maxBlockSize = resolveMaxBlockSize(0)

	// Increase window log to fit the entire dictionary and source if the
	// source size is known. Limit the increase to 19, which is the window
	// log for compression level 1 with the largest source size.
	limitedSrcSize := uint32(min(pledgedSrcSize, 1<<19))
	limitedSrcLog := uint32(1)
	if limitedSrcSize > 1 {
		limitedSrcLog = highbit32(limitedSrcSize-1) + 1
	}
	params.cParams.windowLog = max(params.cParams.windowLog, limitedSrcLog)

	// C: ZSTD_compressBegin_internal (attachDictPref is the default)
	if cd.dictContentSize > 0 && useCDict {
		c.resetCCtxUsingCDict(cd, &params, pledgedSrcSize, false)
	} else {
		c.resetCCtxInternal(&params, pledgedSrcSize, cd.dictContentSize, false)
		dictID, err := compressInsertDictionary(c.blockState.prevCBlock, &c.blockState.matchState, &c.ldmState, &c.appliedParams,
			cd.dictContent, cd.dictContentType, dtlmFast, tfpForCCtx)
		if err != nil {
			return 0, trainError(int(dErrDictionaryCorrupted), err.Error())
		}
		c.dictID = dictID
	}
	n, err := c.compressEnd(dst, src)
	if err != nil {
		code := int(dErrGeneric)
		if errors.Is(err, errDstSizeTooSmall) {
			code = int(dErrDstSizeTooSmall)
		}
		return 0, trainError(code, err.Error())
	}
	return n, nil
}

// trainCandidate is one parameter set of a search, in C's order.
type trainCandidate struct {
	idx int
	p   CoverParams
}

// trainSearch runs the candidates of one context (one d) and keeps the
// best dictionary: the smallest total compressed size, the first candidate
// in C's order on ties (C's single-threaded COVER_best_finish). Failed
// candidates are skipped, as in C, except those refused as not ported and
// those where C crashes (undefined behaviour), which fail the search (the
// first in C's order).
type trainSearch struct {
	mu        sync.Mutex
	dict      []byte
	size      uint64
	params    CoverParams
	idx       int
	found     bool
	abort     error
	abortIdx  int
	nextIndex int
}

// run tries cands on up to GOMAXPROCS goroutines, fewer when
// workerMemory bytes each would not fit the memory budget. newWorker
// returns a worker's try function (with its own scratch buffers).
func (t *trainSearch) run(cands []CoverParams, workerMemory uint64, newWorker func() func(CoverParams) coverSelection) {
	base := t.nextIndex
	t.nextIndex += len(cands)
	if len(cands) == 0 {
		return
	}
	n := min(trainWorkers(workerMemory), len(cands))
	work := make(chan trainCandidate)
	var wg sync.WaitGroup
	for range n {
		try := newWorker()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				t.finish(c, try(c.p))
			}
		}()
	}
	for i, p := range cands {
		work <- trainCandidate{idx: base + i, p: p}
	}
	close(work)
	wg.Wait()
}

// finish records a candidate's result. C: cover.c:COVER_best_finish
func (t *trainSearch) finish(c trainCandidate, sel coverSelection) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sel.err != nil {
		var ce *buildDictError
		if errors.As(sel.err, &ce) && (ce.notPorted || ce.undefined) && (t.abort == nil || c.idx < t.abortIdx) {
			t.abort, t.abortIdx = sel.err, c.idx
		}
		return
	}
	if !t.found || sel.totalCompressedSize < t.size || (sel.totalCompressedSize == t.size && c.idx < t.idx) {
		t.dict, t.size, t.params, t.idx, t.found = sel.dict, sel.totalCompressedSize, c.p, c.idx, true
	}
}

// result is the best dictionary, or C's error when every candidate failed.
func (t *trainSearch) result() ([]byte, CoverParams, error) {
	if t.abort != nil {
		return nil, CoverParams{}, t.abort
	}
	if !t.found {
		return nil, CoverParams{}, trainError(int(dErrGeneric), "no parameters gave a dictionary")
	}
	return t.dict, t.params, nil
}

// trainWorkers is the number of goroutines a search may use when each
// needs workerMemory bytes: GOMAXPROCS, within a budget of a quarter of
// the Go memory limit (1 GiB without one), at least 1; 1 on 32-bit
// platforms.
func trainWorkers(workerMemory uint64) int {
	budget := uint64(1 << 30)
	if limit := debug.SetMemoryLimit(-1); limit != math.MaxInt64 {
		budget = uint64(limit) / 4
	}
	n := runtime.GOMAXPROCS(0)
	if strconv.IntSize == 32 {
		return 1 // see trainSamples.fits32
	}
	if workerMemory > 0 {
		n = min(n, int(min(budget/workerMemory, uint64(n))))
	}
	return max(n, 1)
}

// compressMemoryEstimate estimates what checking one candidate takes: a
// CDict and a context at level (their tables, and the context's LDM table
// where C enables long-distance matching), dst, and the dictionary copies.
func compressMemoryEstimate(level int, maxSampleSize int, maxDictSize uint64) uint64 {
	cp := getCParamsInternal(level, uint64(maxSampleSize), maxDictSize, cpmUnknown)
	tables := uint64(5)<<cp.hashLog + uint64(4)<<cp.chainLog
	ldm := ldmParams{enableLdm: resolveEnableLdm(psAuto, &cp)}
	if ldm.enableLdm == psEnable {
		ldmAdjustParameters(&ldm, &cp)
	}
	return 2*tables + ldmGetTableSize(ldm) + 4*blockSizeMax + uint64(compressBound(maxSampleSize)) + 3*maxDictSize
}

// maxSampleSize is the size of the largest sample.
func (s *trainSamples) maxSampleSize() int {
	m := 0
	for _, b := range s.list {
		m = max(m, len(b))
	}
	return m
}
