// Port of libzstd v1.5.7 lib/compress/zstdmt_compress.c and zstdmt_compress.h
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual
// license): the multithreaded compressor C runs for ZSTD_c_nbWorkers >= 1.
//
// The input is cut into jobs of targetSectionSize bytes (and at every flush
// and the end of the frame), each compressed as an independent piece of
// the frame on its own context, starting from the last targetPrefixSize
// bytes of the previous job as a raw-content prefix. The first job writes
// the frame header, the last one ends the frame; the content checksum and
// the long-distance matcher run serially over the jobs, in job order. Output
// is flushed in job order.
//
// Where the jobs are cut depends only on the input and on where flushes and
// the end of the frame fall, never on how many workers there are or on how
// they are scheduled: a job ends when its input buffer is full, or at
// ZSTD_e_flush / ZSTD_e_end once all input given so far is loaded; a job
// that cannot be posted yet (no idle worker, full job table, input buffer
// still in use) only delays the next one. C's output is therefore the same
// for every nbWorkers >= 1, and so is this port's (checked against C for
// several worker counts).
//
// C's threads are goroutines here: a job is started on a new goroutine when
// one of nbWorkers slots is free (C: POOL_tryAdd on a pool of nbWorkers
// threads and no queue), and the goroutine ends with the job. The job
// table, the round input buffer, and the output, context and sequence pools
// are C's, so memory is bounded as in C. Not ported: rsyncable mode
// (ZSTD_c_rsyncable, no Encoder option), raw-content prefix dictionaries
// (ZSTD_CCtx_refPrefix, not exposed; dictionaries come as a CDict, as in
// C's MT path for a loaded dictionary), the parameter updates of
// ZSTDMT_updateCParams_whileCompressing (parameters cannot change within a
// frame here) and the progression queries.

package zstd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"sync/atomic"

	"github.com/vahid-sohrabloo/fastcompress/internal/xxhash"
)

// C: zstdmt_compress.h constants (64-bit values; 32-bit in parentheses).
const (
	mtNbWorkersMax = 64 << (2 * (bits.UintSize / 64))  // ZSTDMT_NBWORKERS_MAX: 256 (64)
	mtJobSizeMin   = 512 << 10                         // ZSTDMT_JOBSIZE_MIN
	mtJobLogMax    = 29 + bits.UintSize/64             // ZSTDMT_JOBLOG_MAX: 30 (29)
	mtJobSizeMax   = 512 << 20 << (bits.UintSize / 64) // ZSTDMT_JOBSIZE_MAX: 1 GiB (512 MiB)
	overlapLogMax  = 9                                 // C: zstd.h:ZSTD_OVERLAPLOG_MAX
)

var (
	errStageWrong = errors.New("zstd: frame ended, only flush or end allowed") // ERROR(stage_wrong)
	errMTAborted  = errors.New("zstd: multithreaded frame abandoned")          // Go only: see mtCCtx.abandon
	// errMemoryAllocation: zstdmt's round buffer would not fit in an int
	// (32-bit platforms). C: ERROR(memory_allocation)
	errMemoryAllocation = errors.New("zstd: multithreaded compression buffers too large")
)

const maxInt = int(^uint(0) >> 1)

// mtRange is a part of the round buffer: an offset and a size. C: Range
// and Buffer (pointer and size); C's NULL ranges are size 0 here, and a
// range of size 0 never overlaps anything, as in C.
type mtRange struct {
	start, size int
}

// mtIsOverlapped. C: ZSTDMT_isOverlapped
func mtIsOverlapped(buffer, r mtRange) bool {
	if buffer.size == 0 || r.size == 0 {
		return false // empty ranges cannot overlap
	}
	return buffer.start < r.start+r.size && r.start < buffer.start+buffer.size
}

// mtBufferPool hands out the jobs' output buffers.
// C: ZSTDMT_bufferPool (ZSTDMT_getBuffer, ZSTDMT_releaseBuffer)
type mtBufferPool struct {
	bufferSize int
	maxBuffers int
	buffers    [][]byte
}

// get returns a pooled buffer large enough but not more than 8 times
// bufferSize, or a new one. Only the compressing goroutine calls it.
func (p *mtBufferPool) get() []byte {
	if n := len(p.buffers); n > 0 {
		buf := p.buffers[n-1]
		p.buffers = p.buffers[:n-1]
		if cap(buf) >= p.bufferSize && cap(buf)>>3 <= p.bufferSize {
			return buf[:cap(buf)]
		}
	}
	return make([]byte, p.bufferSize)
}

func (p *mtBufferPool) release(buf []byte) {
	if buf != nil && len(p.buffers) < p.maxBuffers {
		p.buffers = append(p.buffers, buf)
	}
}

// mtJob is one job. C: ZSTDMT_jobDescription. The fields after the
// comment are guarded by mtCCtx.mu while the job runs.
type mtJob struct {
	prefix, src mtRange
	prefixBuf   []byte // the round buffer at prefix, cut with full capacity
	srcBuf      []byte // the round buffer at src, cut with full capacity
	jobID       uint
	firstJob    bool
	lastJob     bool
	params      cctxParams
	cdict       *cdict
	// fullFrameSize is the frame's pledged size (contentSizeUnknown if
	// unknown).
	fullFrameSize uint64
	dstBuff       []byte
	// dstFlushed and frameChecksumNeeded are used only by the
	// compressing goroutine.
	dstFlushed          int
	frameChecksumNeeded bool

	// Shared with the worker (C: SHARED).
	consumed int   // input compressed so far
	cSize    int   // output produced so far
	err      error // C: an error code in cSize
	panicVal any   // the worker's panic, re-raised by the compressing goroutine
	// done is set when the worker has finished with the job (C: consumed
	// == src.size, which an empty job meets before it runs).
	done bool
}

// mtSerialState is the part of the compression that runs over the jobs in
// order: the content checksum and the long-distance matcher.
// C: zstdmt_compress.c:SerialState
type mtSerialState struct {
	mu        sync.Mutex // guards all fields but the ldmWindow ones
	cond      sync.Cond
	params    cctxParams
	ldmState  ldmState
	xxhState  *xxhash.Digest
	nextJobID uint
	// ldmWindowMu protects ldmWindow. Acquired after mu when both are.
	ldmWindowMu   sync.Mutex
	ldmWindowCond sync.Cond // signaled when ldmWindow is updated
	ldmWindow     window    // a thread-safe copy of ldmState.window
}

// reset prepares the serial state for a frame of jobs of jobSize bytes
// (no prefix dictionary: see the file comment). It returns the number of
// LDM sequences a job's sequence store must hold (0 without LDM).
// C: ZSTDMT_serialState_reset
func (s *mtSerialState) reset(params cctxParams, jobSize int) int {
	nbSeq := 0
	// Adjust parameters
	if params.ldmParams.enableLdm == psEnable {
		ldmAdjustParameters(&params.ldmParams, &params.cParams)
	} else {
		params.ldmParams = ldmParams{}
	}
	s.nextJobID = 0
	if params.fParams.checksumFlag {
		s.xxhState.Reset()
	}
	if params.ldmParams.enableLdm == psEnable {
		ls := &s.ldmState
		hashSize := 1 << params.ldmParams.hashLog
		numBuckets := 1 << (params.ldmParams.hashLog - params.ldmParams.bucketSizeLog)
		// Size the seq pool tables
		nbSeq = ldmGetMaxNbSeq(params.ldmParams, jobSize)
		// Reset the window
		windowInit(&ls.window)
		// Resize tables if necessary, and zero them.
		if cap(ls.hashTable) < hashSize {
			ls.hashTable = make([]ldmEntry, hashSize)
		}
		ls.hashTable = ls.hashTable[:hashSize]
		clear(ls.hashTable)
		if cap(ls.bucketOffsets) < numBuckets {
			ls.bucketOffsets = make([]byte, numBuckets)
		}
		ls.bucketOffsets = ls.bucketOffsets[:numBuckets]
		clear(ls.bucketOffsets)
		// Update window state (no dictionary to load).
		ls.loadedDictEnd = 0
		// Initialize the serial state's copy of ldmWindow.
		s.ldmWindow = ls.window
	}
	s.params = params
	s.params.jobSize = jobSize
	return nbSeq
}

// genSequences waits for the turn of job jobID, then runs the serial step
// over its input src: the long-distance matcher, which stores its
// sequences into seqStore, and the checksum. C: ZSTDMT_serialState_genSequences
func (s *mtSerialState) genSequences(seqStore *rawSeqStore, src []byte, jobID uint) error {
	var err error
	// Wait for our turn
	s.mu.Lock()
	defer func() {
		// Now it is the next jobs turn (also if the step panics, so that
		// the later jobs and ensureFinished do not wait forever).
		s.nextJobID++
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	for s.nextJobID < jobID {
		s.cond.Wait()
	}
	// A future job may error and skip our job
	if s.nextJobID == jobID {
		// It is now our turn, do any processing necessary
		if s.params.ldmParams.enableLdm == psEnable {
			ls := &s.ldmState
			windowUpdate(&ls.window, src, false)
			srcIdx := ls.window.nextSrc - uint32(len(src))
			// We provide a large enough buffer to never fail.
			err = ldmGenerateSequences(ls, seqStore, &s.params.ldmParams, srcIdx, len(src))
			// Update ldmWindow to match the ldmState.window and signal the
			// main thread if it is waiting for a buffer.
			s.ldmWindowMu.Lock()
			s.ldmWindow = ls.window
			s.ldmWindowCond.Signal()
			s.ldmWindowMu.Unlock()
		}
		if s.params.fParams.checksumFlag && len(src) > 0 {
			s.xxhState.Write(src)
		}
	}
	return err
}

// ensureFinished lets the jobs after jobID run their serial step if jobID
// failed before its own. C: ZSTDMT_serialState_ensureFinished
func (s *mtSerialState) ensureFinished(jobID uint) {
	s.mu.Lock()
	if s.nextJobID <= jobID {
		s.nextJobID = jobID + 1
		s.cond.Broadcast()

		s.ldmWindowMu.Lock()
		windowClear(&s.ldmWindow)
		s.ldmWindowCond.Signal()
		s.ldmWindowMu.Unlock()
	}
	s.mu.Unlock()
}

// mtCCtx is the multithreaded compressor of a cctx.
// C: zstdmt_compress.c:ZSTDMT_CCtx_s
//
// All fields but the job fields documented as shared, the pools' mutex
// guarded ones and the serial state belong to the compressing goroutine
// (the one calling compressStreamGeneric).
type mtCCtx struct {
	nbWorkers int
	slots     chan struct{} // one entry per busy worker (C: the pool's threads)
	hugePages bool          // the job contexts' WithHugePages

	mu   sync.Mutex // guards the shared fields of the jobs
	cond sync.Cond  // broadcast when a job progresses or ends

	jobs      []mtJob
	jobIDMask uint

	bufPool mtBufferPool

	poolMu   sync.Mutex
	cctxPool []*cctx    // C: ZSTDMT_CCtxPool
	seqPool  [][]rawSeq // C: ZSTDMT_seqPool
	nbSeq    int        // capacity of the sequence stores (0: no LDM)

	params            cctxParams
	targetSectionSize int
	targetPrefixSize  int
	// jobReady: one job is already prepared, but the pool has a shortage
	// of workers. Don't create a new job.
	jobReady bool
	inBuff   struct {
		prefix mtRange // read-only, non-owned prefix of the next job
		buffer mtRange // C: buffer (size 0: none)
		filled int
	}
	roundBuff []byte // C: RoundBuff_t's buffer; its capacity is len(roundBuff)
	roundPos  int    // C: RoundBuff_t's pos
	cCapacity uint64 // the capacity C's round buffer would have (the largest needed so far)
	serial    mtSerialState

	doneJobID        uint
	nextJobID        uint
	frameEnded       bool
	allJobsCompleted bool
	frameContentSize uint64
	consumed         uint64
	produced         uint64
	cdict            *cdict
	// abort makes running jobs fail at their next chunk: set while a frame
	// is abandoned (Go only; C lets abandoned jobs run to their end).
	abort atomic.Bool
}

// newMTCCtx. C: ZSTDMT_createCCtx_advanced_internal
func newMTCCtx(nbWorkers int) *mtCCtx {
	mt := &mtCCtx{allJobsCompleted: true}
	mt.cond.L = &mt.mu
	mt.serial.cond.L = &mt.serial.mu
	mt.serial.ldmWindowCond.L = &mt.serial.ldmWindowMu
	mt.serial.xxhState = xxhash.New(0)
	mt.resize(nbWorkers)
	return mt
}

// resize sets the number of workers; no job may be running.
// C: ZSTDMT_resize (and ZSTDMT_expandJobsTable)
func (mt *mtCCtx) resize(nbWorkers int) {
	nbWorkers = min(nbWorkers, mtNbWorkersMax)
	mt.nbWorkers = nbWorkers
	mt.slots = make(chan struct{}, nbWorkers)
	// The job table has a power of 2 entries, more than nbWorkers + 2.
	// C: ZSTDMT_createJobsTable
	if nbJobs := uint(nbWorkers + 2); nbJobs > mt.jobIDMask+1 {
		n := uint(1) << bits.Len(nbJobs)
		mt.jobs = make([]mtJob, n)
		mt.jobIDMask = n - 1
	}
	mt.bufPool.maxBuffers = 2*nbWorkers + 3 // C: BUF_POOL_MAX_NB_BUFFERS
	mt.params.nbWorkers = nbWorkers
}

// getCCtx. C: ZSTDMT_getCCtx
func (mt *mtCCtx) getCCtx() *cctx {
	mt.poolMu.Lock()
	if n := len(mt.cctxPool); n > 0 {
		c := mt.cctxPool[n-1]
		mt.cctxPool = mt.cctxPool[:n-1]
		mt.poolMu.Unlock()
		return c
	}
	mt.poolMu.Unlock()
	c := newCCtx()
	c.hugePages = mt.hugePages
	return c
}

// releaseCCtx. C: ZSTDMT_releaseCCtx (the pool holds nbWorkers contexts)
func (mt *mtCCtx) releaseCCtx(c *cctx) {
	mt.poolMu.Lock()
	if len(mt.cctxPool) < mt.nbWorkers {
		mt.cctxPool = append(mt.cctxPool, c)
	}
	mt.poolMu.Unlock()
}

// getSeq returns a sequence store for a job's LDM sequences, empty
// without LDM. C: ZSTDMT_getSeq
func (mt *mtCCtx) getSeq(nbSeq int) rawSeqStore {
	if nbSeq == 0 {
		return rawSeqStore{}
	}
	var seq []rawSeq
	mt.poolMu.Lock()
	if n := len(mt.seqPool); n > 0 {
		seq = mt.seqPool[n-1]
		mt.seqPool = mt.seqPool[:n-1]
	}
	mt.poolMu.Unlock()
	if cap(seq) < nbSeq || cap(seq)>>3 > nbSeq {
		seq = make([]rawSeq, nbSeq)
	}
	return rawSeqStore{seq: seq[:nbSeq], capacity: nbSeq}
}

// releaseSeq. C: ZSTDMT_releaseSeq
func (mt *mtCCtx) releaseSeq(rs rawSeqStore) {
	if rs.seq == nil {
		return
	}
	mt.poolMu.Lock()
	if len(mt.seqPool) < mt.nbWorkers {
		mt.seqPool = append(mt.seqPool, rs.seq)
	}
	mt.poolMu.Unlock()
}

// runJob is a worker: it compresses job, then reports its end.
// C: ZSTDMT_compressionJob (and its _endJob part)
func (mt *mtCCtx) runJob(job *mtJob, nbSeq int) {
	var c *cctx
	rawSeqStore := mt.getSeq(nbSeq)
	lastCBlockSize, err, panicVal := mt.compressJob(job, &c, &rawSeqStore)

	mt.serial.ensureFinished(job.jobID)
	// release resources
	mt.releaseSeq(rawSeqStore)
	if c != nil && panicVal == nil { // a context left by a panic may be inconsistent
		mt.releaseCCtx(c)
	}
	<-mt.slots // the worker is free
	// report
	mt.mu.Lock()
	switch {
	case panicVal != nil:
		job.panicVal = panicVal
		job.err = fmt.Errorf("zstd: compression job panicked: %v", panicVal)
	case err != nil:
		job.err = err
	default:
		job.cSize += lastCBlockSize
	}
	job.consumed = job.src.size // job->consumed == job->src.size: the job is completed
	job.done = true
	mt.cond.Broadcast()
	mt.mu.Unlock()
}

// compressJob compresses job into job.dstBuff and returns the size of its
// last chunk's output (the earlier chunks are added to job.cSize as they
// are compressed). *cp is set to the job's context once it has one.
// C: ZSTDMT_compressionJob
func (mt *mtCCtx) compressJob(job *mtJob, cp **cctx, seqStore *rawSeqStore) (lastCBlockSize int, err error, panicVal any) {
	defer func() {
		if r := recover(); r != nil {
			panicVal = r
		}
	}()
	jobParams := job.params // do not modify job.params ! copy it, modify the copy
	c := mt.getCCtx()
	*cp = c
	dst := job.dstBuff
	src := job.srcBuf[:job.src.size]

	// Don't compute the checksum for chunks, since we compute it
	// externally, but write it in the header.
	if job.jobID != 0 {
		jobParams.fParams.checksumFlag = false
	}
	// Don't run LDM for the chunks, since we handle it externally
	jobParams.ldmParams.enableLdm = psDisable
	// Correct nbWorkers to 0.
	jobParams.nbWorkers = 0

	// Perform serial step as early as possible
	if err := mt.serial.genSequences(seqStore, src, job.jobID); err != nil {
		return 0, err, nil
	}

	if job.cdict != nil {
		if err := c.compressBeginAdvancedInternal(nil, job.cdict, &jobParams, job.fullFrameSize); err != nil {
			return 0, err, nil
		}
	} else {
		pledgedSrcSize := uint64(len(src))
		if job.firstJob {
			pledgedSrcSize = job.fullFrameSize
		}
		jobParams.forceWindow = !job.firstJob
		// (ZSTD_c_deterministicRefPrefix is 0 already.)
		if err := c.compressBeginAdvancedInternal(job.prefixBuf[:job.prefix.size], nil, &jobParams, pledgedSrcSize); err != nil {
			return 0, err, nil
		}
	}

	// External Sequences can only be applied after CCtx initialization
	if seqStore.size > 0 {
		// C: ZSTDMT_serialState_applySequences, ZSTD_referenceExternalSequences
		c.externSeqStore = rawSeqStore{seq: seqStore.seq, size: seqStore.size, capacity: seqStore.size}
	}

	if !job.firstJob { // flush and overwrite frame header when it's not first job
		if _, err := c.compressContinueInternal(dst, src[:0], false); err != nil {
			return 0, err, nil
		}
		c.invalidateRepCodes()
	}

	// compress the entire job by smaller chunks, for better granularity
	const chunkSize = 4 * blockSizeMax
	nbChunks := (len(src) + chunkSize - 1) / chunkSize
	ip, op := 0, 0
	for chunkNb := 1; chunkNb < nbChunks; chunkNb++ {
		if mt.abort.Load() {
			return 0, errMTAborted, nil
		}
		cSize, err := c.compressContinueInternal(dst[op:], src[ip:ip+chunkSize], false)
		if err != nil {
			return 0, err, nil
		}
		ip += chunkSize
		op += cSize
		// stats
		mt.mu.Lock()
		job.cSize += cSize
		job.consumed = chunkSize * chunkNb
		mt.cond.Broadcast() // warns some more data is ready to be flushed
		mt.mu.Unlock()
	}
	// last block
	if nbChunks > 0 || job.lastJob { // must output a "last block" flag
		lastBlockSize := len(src) & (chunkSize - 1)
		if lastBlockSize == 0 && len(src) >= chunkSize {
			lastBlockSize = chunkSize
		}
		var cSize int
		if job.lastJob {
			cSize, err = c.compressEnd(dst[op:], src[ip:ip+lastBlockSize])
		} else {
			cSize, err = c.compressContinueInternal(dst[op:], src[ip:ip+lastBlockSize], false)
		}
		if err != nil {
			return 0, err, nil
		}
		lastCBlockSize = cSize
	}
	return lastCBlockSize, nil, nil
}

// compressBeginAdvancedInternal starts a frame from cd or, if cd is nil,
// from the raw-content prefix (which may be empty), without streaming
// buffers. C: zstd_compress.c:ZSTD_compressBegin_advanced_internal and
// ZSTD_compressBegin_internal with ZSTD_dtlm_fast and ZSTDb_not_buffered —
// C starts from the CDict when, as for every CDict here, its level is
// ZSTD_NO_CLEVEL (see compressBeginInternal).
func (c *cctx) compressBeginAdvancedInternal(prefix []byte, cd *cdict, params *cctxParams, pledgedSrcSize uint64) error {
	if cd != nil {
		c.resetCCtxUsingCDict(cd, params, pledgedSrcSize, false)
		return nil
	}
	c.resetCCtxInternal(params, pledgedSrcSize, len(prefix), false)
	dictID, err := compressInsertDictionary(c.blockState.prevCBlock, &c.blockState.matchState, &c.ldmState,
		&c.appliedParams, prefix, dctRawContent, dtlmFast, tfpForCCtx)
	if err != nil {
		return err
	}
	c.dictID = dictID
	return nil
}

// invalidateRepCodes ensures the next compression will not use repcodes
// from the previous block. C: zstd_compress.c:ZSTD_invalidateRepCodes
func (c *cctx) invalidateRepCodes() {
	c.blockState.prevCBlock.rep = [repNum]uint32{}
}

// mtComputeTargetJobLog. C: ZSTDMT_computeTargetJobLog
func mtComputeTargetJobLog(params *cctxParams) uint32 {
	var jobLog uint32
	if params.ldmParams.enableLdm == psEnable {
		// In Long Range Mode, the windowLog is typically oversized. In
		// which case, it's preferable to determine the jobSize based on
		// cycleLog instead.
		jobLog = max(21, cycleLog(params.cParams.chainLog, params.cParams.strategy)+3)
	} else {
		jobLog = max(20, params.cParams.windowLog+2)
	}
	return min(jobLog, mtJobLogMax)
}

// mtOverlapLog returns the overlap log, or the strategy's default for 0.
// C: ZSTDMT_overlapLog and ZSTDMT_overlapLog_default
func mtOverlapLog(ovlog int, strat strategy) int {
	if ovlog != 0 {
		return ovlog
	}
	switch strat {
	case zstdBtultra2:
		return 9
	case zstdBtultra, zstdBtopt:
		return 8
	case zstdBtlazy2, zstdLazy2:
		return 7
	}
	return 6
}

// mtComputeOverlapSize. C: ZSTDMT_computeOverlapSize
func mtComputeOverlapSize(params *cctxParams) int {
	overlapRLog := 9 - mtOverlapLog(params.overlapLog, params.cParams.strategy)
	ovLog := 0
	if overlapRLog < 8 {
		ovLog = int(params.cParams.windowLog) - overlapRLog
	}
	if params.ldmParams.enableLdm == psEnable {
		// In Long Range Mode, the windowLog is typically oversized. In
		// which case, it's preferable to determine the jobSize based on
		// chainLog instead. Then, ovLog becomes a fraction of the jobSize,
		// rather than windowSize
		ovLog = int(min(params.cParams.windowLog, mtComputeTargetJobLog(params)-2)) - overlapRLog
	}
	if ovLog == 0 {
		return 0
	}
	return 1 << ovLog
}

// initCStream starts a frame. params are fully resolved; cd is the
// context's CDict or nil. C: ZSTDMT_initCStream_internal (no prefix
// dictionary, no rsyncable)
func (mt *mtCCtx) initCStream(cd *cdict, params cctxParams, pledgedSrcSize uint64, hugePages bool) error {
	// previous compression not correctly finished
	if !mt.allJobsCompleted {
		mt.abandon()
	}
	// init (C resizes first; here no job may run then)
	params.nbWorkers = min(params.nbWorkers, mtNbWorkersMax)
	if params.nbWorkers != mt.nbWorkers {
		mt.resize(params.nbWorkers)
	}
	if mt.hugePages != hugePages {
		mt.hugePages = hugePages
		mt.poolMu.Lock()
		mt.cctxPool = nil
		mt.poolMu.Unlock()
	}

	if params.jobSize != 0 && params.jobSize < mtJobSizeMin {
		params.jobSize = mtJobSizeMin
	}
	params.jobSize = min(params.jobSize, mtJobSizeMax)

	mt.params = params
	mt.frameContentSize = pledgedSrcSize
	mt.cdict = cd

	mt.targetPrefixSize = mtComputeOverlapSize(&params)
	mt.targetSectionSize = params.jobSize
	if mt.targetSectionSize == 0 {
		mt.targetSectionSize = 1 << mtComputeTargetJobLog(&params)
	}
	if mt.targetSectionSize < mt.targetPrefixSize {
		mt.targetSectionSize = mt.targetPrefixSize // job size must be >= overlap size
	}
	mt.bufPool.bufferSize = compressBound(mt.targetSectionSize)
	{
		// If ldm is enabled we need windowSize space.
		windowSize := 0
		if params.ldmParams.enableLdm == psEnable {
			windowSize = 1 << params.cParams.windowLog
		}
		// Two buffers of slack, plus extra space for the overlap. This is
		// the minimum slack that LDM works with. One extra because flush
		// might waste up to targetSectionSize-1 bytes. Another extra for
		// the overlap (if > 0), then one to fill which doesn't overlap
		// with the LDM window.
		nbSlackBuffers := 2
		if mt.targetPrefixSize > 0 {
			nbSlackBuffers++
		}
		slackSize := uint64(mt.targetSectionSize) * uint64(nbSlackBuffers)
		// Compute the total size, and always have enough slack
		sectionsSize := uint64(mt.targetSectionSize) * uint64(max(params.nbWorkers, 1))
		capacity := max(uint64(windowSize), sectionsSize) + slackSize
		// Go only: with a known content size, a round buffer that holds all
		// of it never wraps, in C's capacity as in this one, so a smaller
		// buffer gives the same jobs (and the same LDM window segments).
		// One that also fits a prefix and a section can still wrap if more
		// input than pledged arrives (which fails the frame).
		// C keeps a larger buffer from an earlier frame, and its capacity:
		// cCapacity is the capacity C's buffer has now.
		mt.cCapacity = max(mt.cCapacity, capacity)
		capacity = mt.cCapacity
		if pledgedSrcSize != contentSizeUnknown {
			capacity = min(capacity, uint64(mt.targetPrefixSize)+uint64(mt.targetSectionSize)+pledgedSrcSize)
		}
		if capacity > uint64(maxInt) {
			return errMemoryAllocation // C: the allocation fails
		}
		if cap(mt.roundBuff) < int(capacity) {
			mt.roundBuff = nil // free it before allocating its replacement
			mt.roundBuff = make([]byte, int(capacity))
		}
		mt.roundBuff = mt.roundBuff[:capacity]
	}
	mt.roundPos = 0
	mt.inBuff.buffer = mtRange{}
	mt.inBuff.filled = 0
	mt.inBuff.prefix = mtRange{}
	mt.doneJobID = 0
	mt.nextJobID = 0
	mt.frameEnded = false
	mt.allJobsCompleted = false
	mt.consumed = 0
	mt.produced = 0
	mt.jobReady = false

	mt.nbSeq = mt.serial.reset(params, mt.targetSectionSize)
	return nil
}

// abandon stops the current frame: it makes its running jobs stop early,
// waits for them and releases their resources. C:
// ZSTDMT_waitForAllJobsCompleted and ZSTDMT_releaseAllJobResources (C
// lets the jobs finish).
func (mt *mtCCtx) abandon() {
	mt.abort.Store(true)
	mt.waitForAllJobsCompleted()
	mt.releaseAllJobResources()
	mt.abort.Store(false)
}

// waitForAllJobsCompleted. C: ZSTDMT_waitForAllJobsCompleted
func (mt *mtCCtx) waitForAllJobsCompleted() {
	mt.mu.Lock()
	for mt.doneJobID < mt.nextJobID {
		job := &mt.jobs[mt.doneJobID&mt.jobIDMask]
		for !job.done {
			mt.cond.Wait()
		}
		mt.doneJobID++
	}
	mt.mu.Unlock()
}

// releaseAllJobResources; all workers must have ended.
// C: ZSTDMT_releaseAllJobResources
func (mt *mtCCtx) releaseAllJobResources() {
	for i := range mt.jobs {
		mt.bufPool.release(mt.jobs[i].dstBuff)
		mt.jobs[i] = mtJob{}
	}
	mt.inBuff.buffer = mtRange{}
	mt.inBuff.filled = 0
	mt.jobReady = false
	mt.allJobsCompleted = true
}

// writeLastEmptyBlock makes job a single empty last block that ends the
// frame. C: ZSTDMT_writeLastEmptyBlock
func (mt *mtCCtx) writeLastEmptyBlock(job *mtJob) {
	job.dstBuff = mt.bufPool.get()
	job.src = mtRange{}
	writeLE24(job.dstBuff, 1+btRaw<<1) // C: ZSTD_writeLastEmptyBlock: lastBlock, 0 size
	job.cSize = blockHeaderSize
	job.done = true
}

// createCompressionJob prepares a job of srcSize bytes from the input
// buffer and posts it to a free worker, if any (else it stays ready).
// C: ZSTDMT_createCompressionJob
func (mt *mtCCtx) createCompressionJob(srcSize int, endOp endDirective) {
	jobID := mt.nextJobID & mt.jobIDMask
	endFrame := endOp == eEnd
	job := &mt.jobs[jobID]

	if mt.nextJobID > mt.doneJobID+mt.jobIDMask {
		return // will not create new job : table is full
	}

	if !mt.jobReady {
		src := mt.inBuff.buffer.start
		*job = mtJob{
			src:                 mtRange{src, srcSize},
			prefix:              mt.inBuff.prefix,
			params:              mt.params,
			fullFrameSize:       mt.frameContentSize,
			jobID:               mt.nextJobID,
			firstJob:            mt.nextJobID == 0,
			lastJob:             endFrame,
			frameChecksumNeeded: mt.params.fParams.checksumFlag && endFrame && mt.nextJobID > 0,
		}
		if mt.nextJobID == 0 {
			job.cdict = mt.cdict
		}
		job.srcBuf = mt.roundBuff[src:]
		job.prefixBuf = mt.roundBuff[job.prefix.start:]

		// Update the round buffer pos and clear the input buffer to be reset
		mt.roundPos += srcSize
		mt.inBuff.buffer = mtRange{}
		mt.inBuff.filled = 0
		// Set the prefix for next job
		if !endFrame {
			newPrefixSize := min(srcSize, mt.targetPrefixSize)
			mt.inBuff.prefix = mtRange{src + srcSize - newPrefixSize, newPrefixSize}
		} else { // endFrame => no need for another input buffer
			mt.inBuff.prefix = mtRange{}
			mt.frameEnded = true
			if mt.nextJobID == 0 {
				// single job exception : checksum is already calculated
				// directly within worker thread
				mt.params.fParams.checksumFlag = false
			}
		}

		if srcSize == 0 && mt.nextJobID > 0 { // single job must also write frame header
			// only possible case : need to end the frame with an empty last block
			mt.writeLastEmptyBlock(job)
			mt.nextJobID++
			return
		}
	}

	// C: POOL_tryAdd, which succeeds when a worker is idle.
	select {
	case mt.slots <- struct{}{}:
		job.dstBuff = mt.bufPool.get()
		go mt.runJob(job, mt.nbSeq)
		mt.nextJobID++
		mt.jobReady = false
	default: // no worker available
		mt.jobReady = true
	}
}

// flushProduced copies into dst[len(dst):cap(dst)] what the oldest job
// not yet fully flushed has produced, and moves to the next job when it is
// complete and flushed. With blockToFlush it first waits for the job to
// produce something, unless it is complete. It returns dst extended and
// the amount of data left in internal buffers: 0 if none, 1 if unknown
// but > 0. C: ZSTDMT_flushProduced
func (mt *mtCCtx) flushProduced(dst []byte, blockToFlush bool, end endDirective) ([]byte, int, error) {
	job := &mt.jobs[mt.doneJobID&mt.jobIDMask]

	mt.mu.Lock()
	if blockToFlush && mt.doneJobID < mt.nextJobID {
		for job.err == nil && job.dstFlushed == job.cSize { // nothing to flush
			if job.done {
				break // job is completely consumed: there will be no signal
			}
			mt.cond.Wait() // block when nothing to flush but some to come
		}
	}

	// try to flush something
	cSize := job.cSize
	srcConsumed := job.consumed
	srcSize := job.src.size
	done := job.done
	jobErr, panicVal := job.err, job.panicVal
	mt.mu.Unlock()
	if jobErr != nil {
		mt.waitForAllJobsCompleted()
		for i := range mt.jobs { // a panic of any job is raised, not only the oldest's
			if panicVal == nil {
				panicVal = mt.jobs[i].panicVal
			}
		}
		mt.releaseAllJobResources()
		if panicVal != nil {
			panic(panicVal)
		}
		return dst, 0, jobErr
	}
	// add frame checksum if necessary (can only happen once)
	if done && job.frameChecksumNeeded {
		checksum := uint32(mt.serial.xxhState.Sum64())
		binary.LittleEndian.PutUint32(job.dstBuff[job.cSize:], checksum)
		cSize += 4
		job.cSize += 4 // can write this shared value, as worker is no longer active
		job.frameChecksumNeeded = false
	}

	if cSize > 0 { // compression is ongoing or completed
		toFlush := copy(dst[len(dst):cap(dst)], job.dstBuff[job.dstFlushed:cSize])
		dst = dst[:len(dst)+toFlush]
		job.dstFlushed += toFlush // can write : this value is only used by mtctx

		if done && job.dstFlushed == cSize { // job is completed, output buffer fully flushed => free this job position
			mt.bufPool.release(job.dstBuff)
			job.dstBuff = nil
			job.cSize = 0 // ensure this job slot is considered "not started" in future check
			mt.consumed += uint64(srcSize)
			mt.produced += uint64(cSize)
			mt.doneJobID++
		}
	}

	// return value : how many bytes left in buffer ; fake it to 1 when unknown but >0
	if cSize > job.dstFlushed {
		return dst, cSize - job.dstFlushed, nil
	}
	if srcSize > srcConsumed {
		return dst, 1, nil // current job not completely compressed
	}
	if mt.doneJobID < mt.nextJobID {
		return dst, 1, nil // some more jobs ongoing
	}
	if mt.jobReady {
		return dst, 1, nil // one job is ready to push, just not yet in the list
	}
	if mt.inBuff.filled > 0 {
		return dst, 1, nil // input is not empty, and still needs to be converted into a job
	}
	mt.allJobsCompleted = mt.frameEnded // all jobs are entirely flushed => if this one is last one, frame is completed
	if end == eEnd && !mt.frameEnded {
		return dst, 1, nil // for eEnd, the question is whether the frame is completed
	}
	return dst, 0, nil // internal buffers fully flushed
}

// getInputDataInUse returns the range of data used by the earliest job
// that is not yet complete. C: ZSTDMT_getInputDataInUse
func (mt *mtCCtx) getInputDataInUse() mtRange {
	// no need to check during first round
	nbJobs1stRoundMin := uint(len(mt.roundBuff) / mt.targetSectionSize)
	if mt.nextJobID < nbJobs1stRoundMin {
		return mtRange{}
	}
	mt.mu.Lock()
	defer mt.mu.Unlock()
	for jobID := mt.doneJobID; jobID < mt.nextJobID; jobID++ {
		job := &mt.jobs[jobID&mt.jobIDMask]
		if !job.done {
			r := job.prefix
			if r.size == 0 { // Empty prefix
				r = job.src
			}
			return r
		}
	}
	return mtRange{}
}

// windowRange returns the part of the round buffer holding the indices
// [lo, hi) of a window segment whose first byte (index baseIdx) is b[0];
// an empty range if b is not part of the round buffer.
func (mt *mtCCtx) windowRange(b []byte, baseIdx, lo, hi uint32) mtRange {
	if hi <= lo || cap(b) == 0 || arrayEnd(b) != arrayEnd(mt.roundBuff) {
		return mtRange{}
	}
	return mtRange{len(mt.roundBuff) - cap(b) + int(lo-baseIdx), int(hi - lo)}
}

// doesOverlapWindow. C: ZSTDMT_doesOverlapWindow
func (mt *mtCCtx) doesOverlapWindow(buffer mtRange, w *window) bool {
	extDict := mt.windowRange(w.dictBase, w.dictBaseIdx, w.lowLimit, w.dictLimit)
	prefix := mt.windowRange(w.base, w.baseIdx, w.dictLimit, w.nextSrc)
	return mtIsOverlapped(buffer, extDict) || mtIsOverlapped(buffer, prefix)
}

// waitForLdmComplete waits until the LDM window no longer covers buffer.
// C: ZSTDMT_waitForLdmComplete
func (mt *mtCCtx) waitForLdmComplete(buffer mtRange) {
	if mt.params.ldmParams.enableLdm != psEnable {
		return
	}
	s := &mt.serial
	s.ldmWindowMu.Lock()
	for mt.doesOverlapWindow(buffer, &s.ldmWindow) {
		s.ldmWindowCond.Wait()
	}
	s.ldmWindowMu.Unlock()
}

// tryGetInputRange sets the input buffer to the next section of the round
// buffer to fill, unless part of it is still in use. It reports whether it
// did. C: ZSTDMT_tryGetInputRange
func (mt *mtCCtx) tryGetInputRange() bool {
	inUse := mt.getInputDataInUse()
	spaceLeft := len(mt.roundBuff) - mt.roundPos
	spaceNeeded := mt.targetSectionSize

	if spaceLeft < spaceNeeded {
		// ZSTD_invalidateRepCodes() doesn't work for extDict variants.
		// Simply copy the prefix to the beginning in that case.
		prefixSize := mt.inBuff.prefix.size
		buffer := mtRange{0, prefixSize}
		if mtIsOverlapped(buffer, inUse) {
			return false // Waiting for buffer...
		}
		mt.waitForLdmComplete(buffer)
		copy(mt.roundBuff[:prefixSize], mt.roundBuff[mt.inBuff.prefix.start:mt.inBuff.prefix.start+prefixSize])
		mt.inBuff.prefix.start = 0
		mt.roundPos = prefixSize
	}
	buffer := mtRange{mt.roundPos, spaceNeeded}
	if mtIsOverlapped(buffer, inUse) {
		return false // Waiting for buffer...
	}
	mt.waitForLdmComplete(buffer)
	mt.inBuff.buffer = buffer
	mt.inBuff.filled = 0
	return true
}

// compressStreamGeneric consumes input from in and appends output to dst
// within its capacity, as endOp directs. It returns dst extended, the
// number of bytes of in consumed, and the minimum amount of data left to
// flush (0 if none). C: ZSTDMT_compressStream_generic (no rsyncable)
func (mt *mtCCtx) compressStreamGeneric(dst []byte, in []byte, endOp endDirective) ([]byte, int, int, error) {
	forwardInputProgress := false
	pos := 0

	if mt.frameEnded && endOp == eContinue {
		// current frame being ended. Only flush/end are allowed
		return dst, 0, 0, errStageWrong
	}

	// fill input buffer
	if !mt.jobReady && len(in) > 0 {
		if mt.inBuff.buffer.size == 0 {
			// It is only possible for this to fail if there are still
			// compression jobs ongoing.
			mt.tryGetInputRange()
		}
		if mt.inBuff.buffer.size != 0 {
			// C: findSynchronizationPoint without rsyncable
			toLoad := min(len(in), mt.targetSectionSize-mt.inBuff.filled)
			at := mt.inBuff.buffer.start + mt.inBuff.filled
			copy(mt.roundBuff[at:at+toLoad], in[:toLoad])
			pos += toLoad
			mt.inBuff.filled += toLoad
			forwardInputProgress = toLoad > 0
		}
	}
	if pos < len(in) && endOp == eEnd {
		// Can't end yet because the input is not fully consumed: either
		// no input buffer could be had, or it is full; flush this job but
		// don't end the frame.
		endOp = eFlush
	}

	if mt.jobReady ||
		mt.inBuff.filled >= mt.targetSectionSize || // filled enough : let's compress
		endOp != eContinue && mt.inBuff.filled > 0 || // something to flush : let's go
		endOp == eEnd && !mt.frameEnded { // must finish the frame with a zero-size block
		mt.createCompressionJob(mt.inBuff.filled, endOp)
	}

	// check for potential compressed data ready to be flushed
	dst, remainingToFlush, err := mt.flushProduced(dst, !forwardInputProgress, endOp) // block if there was no forward input progress
	if err != nil {
		return dst, pos, 0, err
	}
	if pos < len(in) {
		return dst, pos, max(remainingToFlush, 1), nil // input not consumed : do not end flush yet
	}
	return dst, pos, remainingToFlush, nil
}

// compressStreamMT is ZSTD_compressStream2's multithreaded stage: it calls
// compressStreamGeneric until it makes progress (eContinue) or until
// everything is flushed or dst is full (eFlush, eEnd). A completed frame,
// or an error, ends the session (C: ZSTD_CCtx_reset(ZSTD_reset_session_only)).
// C: zstd_compress.c:ZSTD_compressStream2, the ZSTD_MULTITHREAD branch
//
// Deviation: with a pledged content size, input beyond it, or ending the
// frame short of it, returns errSrcSizeWrong. C checks the size only
// within the first job (and the last, if it is the first), so it fails a
// frame of one job, as its single-threaded compressor does, but for more
// jobs it writes a frame whose header has the wrong content size.
func (c *cctx) compressStreamMT(dst []byte, in []byte, op endDirective) ([]byte, int, int, error) {
	if fcs := c.mt.frameContentSize; fcs != contentSizeUnknown {
		if total := c.consumedSrcSize + uint64(len(in)); total > fcs || op == eEnd && total != fcs {
			c.reset()
			return dst, 0, 0, errSrcSizeWrong
		}
	}
	consumed := 0
	for {
		ipos, opos := consumed, len(dst)
		var n, flushMin int
		var err error
		dst, n, flushMin, err = c.mt.compressStreamGeneric(dst, in[consumed:], op)
		consumed += n
		c.consumedSrcSize += uint64(n)
		if err != nil || op == eEnd && flushMin == 0 { // compression completed
			c.reset()
		}
		if err != nil {
			return dst, consumed, 0, err
		}
		if op == eContinue {
			// We only require some progress with eContinue, not maximal
			// progress. We're done if we've consumed or produced any bytes,
			// or either buffer is full.
			if consumed != ipos || len(dst) != opos || consumed == len(in) || len(dst) == cap(dst) {
				return dst, consumed, flushMin, nil
			}
		} else if flushMin == 0 || len(dst) == cap(dst) {
			// We require maximal progress. We're done when the flush is
			// complete or the output buffer is full.
			return dst, consumed, flushMin, nil
		}
	}
}

// mtAbandon stops a multithreaded frame in progress, if any (Go only: the
// Encoder calls it when a stream is reset, so that no job outlives it).
func (c *cctx) mtAbandon() {
	if c.mt != nil && !c.mt.allJobsCompleted {
		c.mt.abandon()
	}
}
