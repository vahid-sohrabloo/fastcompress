package lz4

import "sync"

// The multi-threaded frame compression of lz4frame.c (lz4 v1.11.1,
// LZ4F_preferences_t.nbWorkers): the content is cut into jobs at fixed
// positions (a whole number of blocks up to 1 MB, or one block when larger,
// restarting after each flush point), compressed concurrently and emitted
// in order. With linked blocks each job restarts the stream with the
// previous 64 KB of content as dictionary (the first job of the frame with
// the frame's dictionary), so the frame is C's for the same content and
// flush points, whatever the number of workers or the scheduling; with
// independent blocks it equals the single-threaded frame.

// mtJobSizeMax is C's LZ4F_MT_JOBSIZE; the job boundaries are part of the
// output of linked blocks, so only tests change it.
var mtJobSizeMax = 1 << 20

const (
	mtPrefixSize  = 64 * kb // C: LZ4F_MT_PREFIX_SIZE
	nbWorkersMax  = 1024    // C: LZ4F_NBWORKERS_MAX (64-bit)
	mtSlotsPerJob = 2       // C: LZ4F_MT_SLOTS_PER_THREAD
	mtMaxInFlight = 1 << 12 // a cap on job slots, above C's 2*1024+1
)

// C: LZ4F_mtJobSize
func mtJobSize(blockSize int) int {
	if blockSize >= mtJobSizeMax {
		return blockSize
	}
	return mtJobSizeMax / blockSize * blockSize
}

// C: LZ4F_mtEligible (with multi-threading support)
func mtEligible(p *Preferences) bool { return p.NbWorkers > 0 }

// mtSlots is the number of jobs in flight for nbWorkers workers (C:
// LZ4F_MT_NBSLOTS): one being filled, two per worker.
func mtSlots(nbWorkers int) int {
	return min(nbWorkers, nbWorkersMax)*mtSlotsPerJob + 1
}

// C: LZ4F_blocksBound
func blocksBound(srcSize, blockSize int, crc bool) int {
	nbBlocks := (srcSize + blockSize - 1) / blockSize
	return srcSize + nbBlocks*(bhSize+b2i(crc)*bfSize)
}

// mtCompressBound is CompressBoundFrame for a frame cut into jobs without
// autoFlush: the jobs started by srcSize bytes, plus every job in flight
// (Update, Flush and End emit them whole), plus the frame's end.
func mtCompressBound(srcSize int, p *Preferences) int {
	blockSize, err := BlockSize(p.BlockSizeID)
	if err != nil {
		blockSize = 4 << 20
	}
	jobSize := mtJobSize(blockSize)
	nbJobs := srcSize/jobSize + b2i(srcSize%jobSize != 0) + mtSlots(p.NbWorkers)
	frameEnd := bhSize + b2i(p.ContentChecksum)*bfSize
	return nbJobs*blocksBound(jobSize, blockSize, p.BlockChecksum) + frameEnd
}

// mtJob is C's LZ4F_mtJob_t: data is the prefix bytes of history then the
// content, in one slice so that linked blocks reference the history in
// prefix mode, as in C's job buffer: in (copied input, from mtPrefixSize -
// prefix) or the caller's stable input (read in place).
type mtJob struct {
	in     []byte // mtPrefixSize + jobSize, when the input is copied
	data   []byte
	prefix int // history before the content (linked blocks)
	mode   blockCompressMode
	out    []byte
	cSize  int
	err    error
	done   chan struct{}
}

func (j *mtJob) content() []byte { return j.data[j.prefix:] }

// frameMT is C's LZ4F_mtCtx_t, kept by a frameCCtx across frames.
type frameMT struct {
	prefs     Preferences // the frame's (the context's copy)
	cdict     *CDict
	blockSize int
	jobSize   int
	slots     int
	ring      []*mtJob // jobs submitted, oldest first
	filling   *mtJob   // the job being filled
	history   []byte   // the last mtPrefixSize bytes of content
	sem       chan struct{}
	mu        sync.Mutex
	free      []*mtJob // jobs with their buffers, for reuse
	workers   []*frameCCtx
	fault     error // a failed job (frame invalid)
	// stablePrev is the tail of the last stable input (up to mtPrefixSize
	// bytes, its capacity unchanged), until the next input or flush point
	stablePrev []byte
}

// begin is C's LZ4F_mt_begin for the frame c begins.
func (m *frameMT) begin(c *frameCCtx) {
	m.prefs = c.prefs
	m.cdict = c.cdict
	m.blockSize = c.maxBlockSize
	m.jobSize = mtJobSize(c.maxBlockSize)
	m.slots = min(mtSlots(c.prefs.NbWorkers), mtMaxInFlight)
	if cap(m.sem) != min(c.prefs.NbWorkers, nbWorkersMax) {
		m.sem = make(chan struct{}, min(c.prefs.NbWorkers, nbWorkersMax))
	}
	m.ring = m.ring[:0]
	m.filling = nil
	m.history = m.history[:0]
	m.fault = nil
	m.stablePrev = nil
	if cap(m.history) < mtPrefixSize {
		m.history = make([]byte, 0, mtPrefixSize)
	}
}

// abort waits for the jobs in flight of an abandoned frame.
func (m *frameMT) abort() {
	for _, j := range m.ring {
		<-j.done
		m.release(j)
	}
	m.ring = m.ring[:0]
	if m.filling != nil {
		m.release(m.filling)
		m.filling = nil
	}
}

func (m *frameMT) acquire() *mtJob {
	m.mu.Lock()
	var j *mtJob
	if n := len(m.free); n > 0 {
		j = m.free[n-1]
		m.free = m.free[:n-1]
	}
	m.mu.Unlock()
	if j == nil {
		j = new(mtJob)
	}
	outCap := blocksBound(m.jobSize, m.blockSize, true)
	if cap(j.out) < outCap {
		j.out = make([]byte, outCap)
	}
	j.out = j.out[:outCap]
	j.data, j.cSize, j.err = nil, 0, nil
	j.done = make(chan struct{})
	return j
}

func (m *frameMT) release(j *mtJob) {
	j.data = nil // drops a reference to the caller's input
	m.mu.Lock()
	m.free = append(m.free, j)
	m.mu.Unlock()
}

// worker returns a compression context for one job (C: the worker's
// state), set up for the frame's preferences.
func (m *frameMT) worker() *frameCCtx {
	m.mu.Lock()
	var w *frameCCtx
	if n := len(m.workers); n > 0 {
		w = m.workers[n-1]
		m.workers = m.workers[:n-1]
	}
	m.mu.Unlock()
	if w == nil {
		w = new(frameCCtx)
	}
	w.prefs = m.prefs
	w.maxBlockSize = m.blockSize
	w.cdict = nil
	if m.prefs.CompressionLevel < LevelHCMin {
		if w.fast == nil {
			w.fast = new(Stream)
		}
	} else if w.hc == nil {
		w.hc = NewStreamHC(m.prefs.CompressionLevel)
	} else {
		w.hc.Reset(m.prefs.CompressionLevel)
	}
	return w
}

func (m *frameMT) putWorker(w *frameCCtx) {
	m.mu.Lock()
	m.workers = append(m.workers, w)
	m.mu.Unlock()
}

// run is C's LZ4F_compressJob.
func (m *frameMT) run(j *mtJob) {
	w := m.worker()
	w.mode = j.mode
	if j.mode == uncompressedMode {
		j.cSize, j.err = w.compressBlocks(j.out, j.content())
	} else {
		if j.prefix > 0 {
			w.startStream(nil, j.data[:j.prefix])
		} else {
			w.startStream(m.cdict, nil)
		}
		w.cdict = m.cdict
		// the content continues the history within one slice: prefix mode
		j.cSize, j.err = w.compressBlocks(j.out, j.data[j.prefix:len(j.data):len(j.data)])
	}
	m.putWorker(w)
}

// submit is C's LZ4F_mt_submit: the filling job starts.
func (m *frameMT) submit() {
	j := m.filling
	m.filling = nil
	m.ring = append(m.ring, j)
	if !m.prefs.BlockIndependent {
		m.addHistory(j.content())
	}
	go func() {
		m.sem <- struct{}{} // at most NbWorkers jobs compress at once
		m.run(j)
		<-m.sem
		close(j.done)
	}()
}

// C: LZ4F_mt_addHistory
func (m *frameMT) addHistory(content []byte) {
	if len(content) >= mtPrefixSize {
		m.history = append(m.history[:0], content[len(content)-mtPrefixSize:]...)
		return
	}
	kept := min(len(m.history), mtPrefixSize-len(content))
	copy(m.history, m.history[len(m.history)-kept:])
	m.history = append(m.history[:kept], content...)
}

// emitOldest waits for the oldest job and passes its blocks to emit.
func (m *frameMT) emitOldest(emit func([]byte) error) error {
	j := m.ring[0]
	<-j.done
	copy(m.ring, m.ring[1:])
	m.ring = m.ring[:len(m.ring)-1]
	err := j.err
	if err == nil {
		err = emit(j.out[:j.cSize])
	}
	m.release(j)
	if err != nil && m.fault == nil {
		m.fault = err
	}
	return err
}

// emitDone emits the completed jobs at the head of the queue, in order;
// with wait, every job.
func (m *frameMT) emitDone(emit func([]byte) error, wait bool) error {
	for len(m.ring) > 0 {
		if !wait {
			select {
			case <-m.ring[0].done:
			default:
				return nil
			}
		}
		if err := m.emitOldest(emit); err != nil {
			return err
		}
	}
	return nil
}

// update is C's LZ4F_mt_update. The content is copied into jobs, or, with
// stable input (C: LZ4F_srcStableUntilFlush; src stays unchanged until the
// next flush point) or autoFlush, read in place when a whole job and its
// history are in src, or in the previous stable input when both are parts
// of one slice (its capacity reaches the end of src; C compares pointers).
func (m *frameMT) update(src []byte, stable bool, mode blockCompressMode, emit func([]byte) error) error {
	if m.fault != nil {
		return m.fault
	}
	if m.filling != nil && m.filling.mode != mode {
		m.submit() // mode switch
	}
	inPlace := stable || m.prefs.AutoFlush
	// base holds the previous stable input's tail (prevRun bytes) then src,
	// when both are in one slice
	base, prevRun := src, 0
	if p := m.stablePrev; stable && len(p) > 0 && len(src) > 0 && addr(p)+uintptr(len(p)) == addr(src) && cap(p) >= len(p)+len(src) {
		base, prevRun = p[:len(p)+len(src)], len(p)
	}
	for ip := 0; ip < len(src); {
		j := m.filling
		if j == nil {
			if len(m.ring) >= m.slots-1 {
				// all slots busy: emit the oldest job, to free one
				if err := m.emitOldest(emit); err != nil {
					return err
				}
			}
			j = m.acquire()
			j.mode = mode
			j.prefix = 0
			if !m.prefs.BlockIndependent {
				j.prefix = len(m.history)
			}
			m.filling = j
			if remaining := len(src) - ip; inPlace && (remaining >= m.jobSize || m.prefs.AutoFlush) && prevRun+ip >= j.prefix {
				// the job and its history are in place
				n := min(remaining, m.jobSize)
				j.data = base[prevRun+ip-j.prefix : prevRun+ip+n]
				m.submit()
				ip += n
				continue
			}
			if cap(j.in) < mtPrefixSize+m.jobSize {
				j.in = make([]byte, mtPrefixSize+m.jobSize)
			}
			j.in = j.in[:mtPrefixSize+m.jobSize]
			copy(j.in[mtPrefixSize-j.prefix:], m.history)
			j.data = j.in[mtPrefixSize-j.prefix : mtPrefixSize]
		}
		n := copy(j.in[mtPrefixSize+len(j.data)-j.prefix:mtPrefixSize+m.jobSize], src[ip:])
		j.data = j.in[mtPrefixSize-j.prefix : mtPrefixSize+len(j.data)-j.prefix+n]
		ip += n
		if len(j.data)-j.prefix == m.jobSize {
			m.submit()
		}
	}

	if len(src) > 0 {
		m.stablePrev = nil
		if stable {
			m.stablePrev = base[max(0, len(base)-mtPrefixSize):]
		}
	}
	if m.prefs.AutoFlush {
		if m.filling != nil {
			m.submit()
		}
		return m.emitDone(emit, true)
	}
	return m.emitDone(emit, false)
}

// flush is C's LZ4F_mt_flushPoint and the emission of every job.
func (m *frameMT) flush(emit func([]byte) error) error {
	if m.fault != nil {
		return m.fault
	}
	if m.filling != nil {
		m.submit()
	}
	m.stablePrev = nil // stable input is only kept until a flush point
	return m.emitDone(emit, true)
}
