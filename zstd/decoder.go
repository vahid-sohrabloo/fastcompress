package zstd

import (
	"errors"
	"io"
	"maps"
	"math"
	"sync"
	"sync/atomic"
)

// Decoder provides decoding of zstandard streams.
// The decoder has been designed to operate without allocations after a warmup.
// This means that you should store the decoder for best performance.
// To re-use a stream decoder, use the Reset(r io.Reader) error to switch to another stream.
// A decoder can safely be re-used even if the previous stream failed.
// To release the resources, you must call the Close() function on a decoder.
//
// DecodeAll is C's ZSTD_decompressDCtx (ZSTD_decompressMultiFrame) on one
// of the Decoder's decompression contexts; Read and WriteTo drive C's
// ZSTD_decompressStream on the stream's own context, with input read from
// the io.Reader in ZSTD_DStreamInSize() chunks (with an explicit
// WithDecoderConcurrency(n > 1), through a pipeline that reads ahead and
// decodes the blocks' entropy on two goroutines, see decoder_mt.go). Both
// apply the options' C parameters (see DOption) and the deviations from C:
// in particular, a stream gives
// DecodeAll's output and error for the same input, however it is cut, and
// delivers the output decoded before an error, then the error.
//
// Each frame's dictionary is chosen by the frame's dictionary ID among the
// registered ones (WithDecoderDicts, WithDecoderDictRaw): a frame with an ID
// that is not registered fails with ErrUnknownDictionary, and a frame
// without one uses the dictionary registered with ID 0, if any.
//
// DecodeAll may run concurrently with every other method; it uses the
// options of the last NewReader or ResetWithOptions that completed. The
// stream methods (Read, WriteTo, Reset, ResetWithOptions) and Close must
// not run concurrently with each other, as for any io.Reader. The zero
// value is not usable: its methods return an error.
//
// A pipelined stream's goroutines (WithDecoderConcurrency) end with the
// stream, at Reset and at Close; as the doc says, Close must be called to
// release the resources. A panic of the io.Reader's Read is raised in the
// Read or WriteTo call that needs its input, as without a pipeline.
//
// On 32-bit platforms the DecodeAll calls of a Decoder share one memory
// budget of 1 GiB (maxMemoryCap), charged for every buffer they allocate
// and given back when each call returns; a call that would exceed it fails
// with ErrDecoderSizeExceeded. Several Decoders in one 32-bit process, and
// a stream's window buffers, are not covered by it.
type Decoder struct {
	o      decoderOptions // the stream's options (Reset, Read, WriteTo)
	dctxs  chan *dctx     // one slot per concurrent DecodeAll; nil slots get a context on first use
	dicts  *ddictSet      // o's dictionaries by ID; shared, read only, by every context
	s      decStream
	closed atomic.Bool
	// cfg is DecodeAll's copy of o and dicts, replaced (never changed) by
	// ResetWithOptions, so that DecodeAll can run during it.
	cfg atomic.Pointer[decoderConfig]
	// budget is the 32-bit DecodeAll memory budget; nil on 64-bit
	// platforms.
	budget *memBudget
}

// decoderConfig is an immutable snapshot of a Decoder's options.
type decoderConfig struct {
	o     decoderOptions
	dicts *ddictSet
}

// decStream is the state of the stream (Read, WriteTo).
type decStream struct {
	dc *dctx     // C's ZSTD_DStream; created by the first Reset with a reader
	r  io.Reader // nil: no stream (ErrDecoderNilInput)
	// in[inPos:inEnd] is input read from r and not yet consumed.
	in           []byte
	inPos, inEnd int
	// last is the hint of the last decompressStream call that consumed
	// input or produced output: 0 at a frame boundary, where the input
	// may end (else it ends inside a frame).
	last   int
	needIn bool  // the last call made no progress: read input before the next
	eof    bool  // r returned io.EOF
	err    error // sticky: io.EOF at the end, or the error; cleared by Reset
	out    []byte
	// WithDecodeBuffersBelow: buf[bufPos:] is output decoded at Reset,
	// bufErr the error after it.
	buffered bool
	buf      []byte
	bufPos   int
	bufErr   error
	// An explicit WithDecoderConcurrency(n) with n > 1: the pipeline
	// (decoder_mt.go) while it runs, with in holding its units' input; S2's
	// context and scratch, kept for the next pipeline.
	pipe     *pipeline
	edc      *dctx
	escratch []byte
	// Stopped pipelines' S1s that may still be in a Read (waitOldReader).
	lingering []lingeringS1
	// The generation of the last pipeline that ran to its end, reused by
	// the next (see stopPipeline).
	spare *pipeline
}

// errDecoderZero is returned when a Decoder was not created by NewReader.
var errDecoderZero = errors.New("zstd: Decoder not initialized (use NewReader to create one)")

// lowMemBuffers is the size above which WithDecoderLowmem frees buffers.
const lowMemBuffers = 1 << 20

// NewReader creates a new decoder.
// A nil Reader can be provided in which case Reset can be used to start a decode.
//
// A Decoder can be used in two modes:
//
// 1) As a stream, or
// 2) For stateless decoding using DecodeAll.
//
// Only a single stream can be decoded concurrently, but the same decoder
// can run multiple concurrent stateless decodes. It is even possible to
// use stateless decodes while a stream is being decoded.
//
// The Reset function can be used to initiate a new stream, which will considerably
// reduce the allocations normally caused by NewReader.
func NewReader(r io.Reader, opts ...DOption) (*Decoder, error) {
	d := &Decoder{}
	d.o.defaults()
	for _, o := range opts {
		if err := o(&d.o); err != nil {
			return nil, err
		}
	}
	d.dctxs = make(chan *dctx, d.o.concurrent)
	for range d.o.concurrent {
		d.dctxs <- nil
	}
	d.dicts = d.o.ddictSet()
	d.cfg.Store(&decoderConfig{o: d.o, dicts: d.dicts})
	if maxMemoryCap < 64<<30 { // 32-bit platforms
		d.budget = newMemBudget(maxMemoryCap)
	}
	if err := d.Reset(r); err != nil {
		return nil, err
	}
	return d, nil
}

// memLimit returns the memory limit in effect: WithDecoderMaxMemory, at
// most maxMemoryCap.
func (o *decoderOptions) memLimit() uint64 {
	return min(o.maxMemory, maxMemoryCap)
}

// configure sets dc's C parameters and dictionaries from o and dicts, its
// output limit and its memory budget (no C counterparts).
func configure(dc *dctx, o *decoderOptions, dicts *ddictSet, outLimit uint64, budget *memBudget) {
	// C: ZSTD_d_windowLogMax. WithDecoderMaxWindow, or a lower memory
	// limit (doc text).
	dc.setMaxWindow(min(o.maxWindow, o.memLimit()))
	dc.setIgnoreChecksum(o.ignoreChecksum)
	dc.maxMemory = outLimit
	dc.budget = budget
	// C: ZSTD_d_refMultipleDDicts: every frame picks its dictionary in the
	// set by its ID (decompressFrames, decompressStream); there is no
	// single default DDict.
	dc.ddict = nil
	dc.ddicts = dicts
}

// DecodeAll allows stateless decoding of a blob of bytes.
// Output will be appended to dst, so if the destination size is known
// you can pre-allocate the destination slice to avoid allocations.
// DecodeAll can be used concurrently.
// The Decoder concurrency limits will be respected.
//
// C: ZSTD_decompressDCtx (ZSTD_decompressMultiFrame), with the allocation
// rule and deviations below: the
// output is at most WithDecoderMaxMemory bytes (also cap(dst)-len(dst)
// under WithDecodeAllCapLimit), else ErrDecoderSizeExceeded. On an error
// the result holds dst and the output decoded before the error.
// dst[:len(dst)] is never modified. Empty input gives dst and no error.
func (d *Decoder) DecodeAll(input, dst []byte) ([]byte, error) {
	if d.dctxs == nil {
		return dst, errDecoderZero
	}
	if d.closed.Load() {
		return dst, ErrDecoderClosed
	}
	dc := <-d.dctxs
	defer func() { d.dctxs <- dc }()
	if dc == nil {
		dc = newDCtx()
	}
	c := d.cfg.Load()
	limit := c.o.memLimit()
	if c.o.capLimit {
		limit = min(limit, uint64(cap(dst)-len(dst)))
	}
	configure(dc, &c.o, c.dicts, limit, d.budget)
	defer dc.releaseBudget()
	// Go: a result in a new array gets exactly the output's size (a
	// 32-bit Decoder's budget and WithDecoderLowmem keep the plain rule).
	if cap(dst) == 0 && d.budget == nil && !c.o.lowMem && outputSizeUnsure(input) {
		return dc.decompressNew(input)
	}
	return dc.decompressMultiFrame(dst, input, nil)
}

// Reset will reset the decoder the supplied stream after the current has finished processing.
// Note that this functionality cannot be used after Close has been called.
// Reset can be called with a nil reader to release references to the previous reader.
// After being called with a nil reader, no other operations than Reset or DecodeAll or Close
// should be used.
//
// A pipelined stream (WithDecoderConcurrency) is stopped without waiting
// for the previous reader: a Read of it in progress (or starting) when the
// stream stops finishes in the background, its bytes are dropped, and no
// further Read is made. Only when r is that same reader (equal interface
// values) does Reset wait for that Read to return, so that r.Read is never
// called by two goroutines at once.
//
// C: ZSTD_DCtx_reset(ZSTD_reset_session_only), as a stream can be reused
// after an error. A reader with Bytes and Len (see WithDecodeBuffersBelow)
// may be decoded here; its error, if any, is returned by Read and WriteTo
// after the output, never by Reset. Reset(nil) also drops that output.
func (d *Decoder) Reset(r io.Reader) error {
	if d.dctxs == nil {
		return errDecoderZero
	}
	if d.closed.Load() {
		return ErrDecoderClosed
	}
	d.stopPipeline()
	s := &d.s
	s.waitOldReader(r)
	*s = decStream{dc: s.dc, r: r, in: s.in, out: s.out, buf: s.buf[:0], edc: s.edc, escratch: s.escratch, lingering: s.lingering, spare: s.spare}
	if r == nil {
		s.buf = nil // nor the output decoded from it (WithDecodeBuffersBelow)
		return nil
	}
	if s.dc == nil {
		s.dc = newDCtx()
	}
	s.dc.reset(true)
	if d.o.lowMem {
		s.release()
	}
	if b, ok := r.(interface {
		Bytes() []byte
		Len() int
	}); ok && !d.o.capLimit && b.Len() < d.o.buffersBelow {
		configure(s.dc, &d.o, d.dicts, d.o.memLimit(), d.budget)
		out, err := s.dc.decompressMultiFrame(s.buf, b.Bytes(), nil)
		s.dc.releaseBudget() // (a panic is not recovered: the stream is lost anyway)
		if !errors.Is(err, ErrDecoderSizeExceeded) {
			s.buffered, s.buf, s.bufErr = true, out, err
			return nil
		}
		// Go: over DecodeAll's limit, the reader is streamed (a stream's
		// limit is the window).
		s.dc.reset(true)
	}
	// Go: the output of a stream is not limited (doc text: for streams,
	// WithDecoderMaxMemory limits the window). The single-pass shortcut
	// decodes into the caller's buffer only, so it reserves no memory.
	configure(s.dc, &d.o, d.dicts, math.MaxUint64, nil)
	return nil
}

// ResetWithOptions will reset the decoder and apply the given options
// for the next stream or DecodeAll operation.
// Options are applied on top of the existing options.
// Some options cannot be changed on reset and will return an error.
//
// WithDecoderConcurrency, WithDecodeBuffersBelow and WithDecoderLowmem
// cannot change (passing their current value is allowed); on an error
// nothing is reset.
func (d *Decoder) ResetWithOptions(r io.Reader, opts ...DOption) error {
	if d.dctxs == nil {
		return errDecoderZero
	}
	if d.closed.Load() {
		return ErrDecoderClosed
	}
	o := d.o
	o.dicts = maps.Clone(d.o.dicts)
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return err
		}
	}
	// WithDecoderConcurrency(n) given again is no change, even when n = 0
	// now means another GOMAXPROCS.
	if o.concurrentReq == d.o.concurrentReq {
		o.concurrent = d.o.concurrent
	}
	if o.concurrent != d.o.concurrent || o.buffersBelow != d.o.buffersBelow || o.lowMem != d.o.lowMem {
		return errors.New("zstd: WithDecoderConcurrency, WithDecodeBuffersBelow and WithDecoderLowmem cannot be changed with ResetWithOptions")
	}
	d.o = o
	d.dicts = o.ddictSet() // a new set: one held by a context is not changed
	d.cfg.Store(&decoderConfig{o: o, dicts: d.dicts})
	return d.Reset(r)
}

// Read bytes from the decompressed stream into p.
// Returns the number of bytes read and any error that occurred.
// When the stream is done, io.EOF will be returned.
//
// Read returns once p is full, or when more input is needed and some
// output is ready. Errors, including the io.Reader's, are sticky until
// Reset; the output decoded before an error is returned before it. Input
// that ends inside a frame gives io.ErrUnexpectedEOF.
func (d *Decoder) Read(p []byte) (int, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	n, err := d.fill(p)
	if n > 0 {
		return n, nil // the error, if any, is sticky
	}
	return 0, err
}

// WriteTo writes data to w until there's no more data to write or when an error occurs.
// The return value n is the number of bytes written.
// Any error encountered during the write is also returned.
//
// The output is written in chunks of up to ZSTD_DStreamOutSize() bytes;
// output decoded before an error is written before the error is returned.
// An error of w is sticky, as the decoder's are.
//
// Go: without a pipeline, each chunk is written to w straight from the
// stream context's window buffer (or from the output decoded at Reset),
// not copied into an output buffer first; the chunks and the output are
// the same.
func (d *Decoder) WriteTo(w io.Writer) (int64, error) {
	if err := d.check(); err != nil {
		return 0, err
	}
	s := &d.s
	if s.pipe == nil && !d.o.pipelined() {
		return d.writeDirect(w)
	}
	if s.out == nil {
		s.out = make([]byte, dStreamOutSize)
	}
	var written int64
	for {
		n, err := d.fill(s.out)
		if n > 0 {
			nw, werr := w.Write(s.out[:n])
			written += int64(nw)
			if werr == nil && nw < n {
				werr = io.ErrShortWrite
			}
			if werr != nil {
				s.err = werr
				d.stopPipeline()
				return written, werr
			}
		}
		if err == io.EOF {
			return written, nil
		}
		if err != nil {
			return written, err
		}
	}
}

// writeDirect is WriteTo without a pipeline. The output decoded at Reset
// (WithDecodeBuffersBelow) is written from s.buf. A stream runs fill's
// loop with an output buffer of no room: each decompressStream call then
// stops at its flush stage with the block it decoded in the context's
// buffer (C's zdssFlush, see pendingOutput), which is written to w from
// there and marked flushed, and the next call goes on from there.
func (d *Decoder) writeDirect(w io.Writer) (int64, error) {
	s := &d.s
	var written int64
	if s.buffered {
		for s.err == nil {
			p := s.buf[s.bufPos:min(len(s.buf), s.bufPos+dStreamOutSize)]
			s.bufPos += len(p)
			if s.bufPos == len(s.buf) {
				s.endBuffered(d.o.lowMem)
			}
			if len(p) > 0 {
				nw, err := writeChunk(w, p)
				written += int64(nw)
				if err != nil {
					s.err = err
					return written, err
				}
			}
		}
		if s.err == io.EOF {
			return written, nil
		}
		return written, s.err
	}
	for s.err == nil {
		if p := s.dc.pendingOutput(); len(p) > 0 {
			s.dc.outStart = s.dc.outEnd // flushed, whatever w does
			nw, err := writeChunk(w, p)
			written += int64(nw)
			if err != nil {
				s.err = err
				return written, err
			}
		}
		if s.needIn {
			if s.eof {
				// C: as in fill.
				s.err = io.EOF
				if s.last != 0 || s.inPos < s.inEnd {
					s.err = io.ErrUnexpectedEOF
				}
				break
			}
			if err := s.readInput(); err != nil {
				s.err = err
				break
			}
			s.needIn = false
		}
		in := inBuffer{src: s.in[:s.inEnd], pos: s.inPos}
		var out outBuffer
		hint, e := s.dc.decompressStreamCode(&out, &in)
		moved := in.pos != s.inPos || len(s.dc.pendingOutput()) > 0
		s.inPos = in.pos
		if e != 0 {
			s.err = e.toError("stream")
			break
		}
		if !moved {
			s.needIn = true
			continue
		}
		s.last = hint
		if hint == 0 && d.o.lowMem {
			s.release()
		}
	}
	if s.err == io.EOF {
		return written, nil
	}
	return written, s.err
}

// writeChunk writes p to w; a short write is io.ErrShortWrite.
func writeChunk(w io.Writer, p []byte) (int, error) {
	nw, err := w.Write(p)
	if err == nil && nw < len(p) {
		err = io.ErrShortWrite
	}
	return nw, err
}

// endBuffered sets the sticky error once the output decoded at Reset has
// been served: its error, else io.EOF.
func (s *decStream) endBuffered(lowMem bool) {
	s.err = s.bufErr
	if s.err == nil {
		s.err = io.EOF
	}
	if lowMem && cap(s.buf) > lowMemBuffers {
		s.buf = nil
	}
}

// check returns the error a stream call must fail with before decoding.
func (d *Decoder) check() error {
	switch {
	case d.dctxs == nil:
		return errDecoderZero
	case d.closed.Load():
		return ErrDecoderClosed
	case d.s.r == nil:
		return ErrDecoderNilInput
	}
	return nil
}

// fill decodes into p until it is full, or more input is needed and some
// output is in p, or the stream ends (io.EOF) or fails. It returns the
// bytes written to p and the stream's sticky error, if it has one now.
// With an explicit WithDecoderConcurrency(n > 1) the input comes from the
// pipeline (decoder_mt.go), which ends with the stream.
func (d *Decoder) fill(p []byte) (n int, err error) {
	s := &d.s
	if s.err != nil {
		return 0, s.err
	}
	if !s.buffered && s.pipe == nil && d.o.pipelined() {
		d.startPipeline()
	}
	if s.pipe != nil {
		defer func() {
			if s.err != nil {
				d.stopPipeline()
			}
		}()
	}
	if s.buffered {
		n = copy(p, s.buf[s.bufPos:])
		s.bufPos += n
		if s.bufPos == len(s.buf) {
			s.endBuffered(d.o.lowMem)
		}
		return n, s.err
	}
	for n < len(p) {
		if s.needIn {
			if n > 0 {
				return n, nil // do not wait for input with output ready
			}
			if s.eof {
				// C: ZSTD_decompressStream's hint is 0 at a frame
				// boundary; anywhere else (or with input it did not
				// take) the input was cut short.
				s.err = io.EOF
				if s.last != 0 || s.inPos < s.inEnd {
					s.err = io.ErrUnexpectedEOF
				}
				break
			}
			read := s.readInput
			if s.pipe != nil {
				read = s.readUnit
			}
			if err := read(); err != nil {
				s.err = err
				break
			}
			s.needIn = false
		}
		in := inBuffer{src: s.in[:s.inEnd], pos: s.inPos}
		out := outBuffer{dst: p, pos: n}
		hint, e := s.dc.decompressStreamCode(&out, &in)
		moved := in.pos != s.inPos || out.pos != n
		s.inPos, n = in.pos, out.pos
		if e != 0 {
			// Go: out.pos counts the output written before the error.
			s.err = e.toError("stream")
			break
		}
		if !moved {
			s.needIn = true
			continue
		}
		s.last = hint
		if hint == 0 && d.o.lowMem {
			s.release()
		}
	}
	return n, s.err
}

// readInput reads from r after the unconsumed input: at least one byte,
// or sets eof. Like bufio, 100 reads of nothing give io.ErrNoProgress.
func (s *decStream) readInput() error {
	if s.in == nil {
		s.in = make([]byte, dStreamInSize)
	}
	s.inEnd = copy(s.in, s.in[s.inPos:s.inEnd])
	s.inPos = 0
	for range 100 {
		nr, err := s.r.Read(s.in[s.inEnd:])
		if nr < 0 || nr > len(s.in)-s.inEnd {
			return errors.New("zstd: io.Reader returned an invalid count")
		}
		s.inEnd += nr
		if err == io.EOF {
			s.eof = true
			return nil
		}
		if err != nil {
			return err
		}
		if nr > 0 {
			return nil
		}
	}
	return io.ErrNoProgress
}

// release frees the stream context's buffers when they hold more than
// lowMemBuffers bytes (WithDecoderLowmem). It must run between frames: the
// next frame header sizes them again (C frees them there too when they are
// too large, see ZSTD_DCtx_isOversizedTooLong).
func (s *decStream) release() {
	dc := s.dc
	if dc != nil && dc.streamStage == zdssInit && cap(dc.inBuff)+cap(dc.outBuff) > lowMemBuffers {
		dc.inBuff, dc.outBuff = nil, nil
		dc.inBuffSize, dc.outBuffSize = 0, 0
	}
	if cap(s.buf) > lowMemBuffers {
		s.buf = nil
	}
}

// IOReadCloser returns the decoder as an io.ReadCloser for convenience.
// Any changes to the decoder will be reflected, so the returned ReadCloser
// can be reused along with the decoder.
// io.WriterTo is also supported by the returned ReadCloser.
//
// Its Close closes the Decoder.
func (d *Decoder) IOReadCloser() io.ReadCloser {
	return closeWrapper{d: d}
}

// closeWrapper wraps a Decoder as an io.ReadCloser (and io.WriterTo).
type closeWrapper struct {
	d *Decoder
}

func (c closeWrapper) Read(p []byte) (int, error)         { return c.d.Read(p) }
func (c closeWrapper) WriteTo(w io.Writer) (int64, error) { return c.d.WriteTo(w) }

// Close closes the Decoder and returns nil.
func (c closeWrapper) Close() error {
	c.d.Close()
	return nil
}

// Close will release all resources.
// It is NOT possible to reuse the decoder after this.
//
// Close must be called to release a pipelined stream's goroutines
// (WithDecoderConcurrency) when the stream is not read to its end or
// error. It does not wait for a Read of the reader in progress (see
// Reset).
//
// DecodeAll calls already running finish normally. The idle DecodeAll
// contexts are dropped; Close must not run concurrently with the stream
// methods (see Decoder). Closing twice is a no-op.
func (d *Decoder) Close() {
	if d.closed.Swap(true) {
		return
	}
	d.stopPipeline()
	d.s = decStream{}
	for n := len(d.dctxs); n > 0; n-- {
		select {
		case <-d.dctxs:
			d.dctxs <- nil
		default:
			return
		}
	}
}

var (
	defaultDecoderOnce sync.Once
	defaultDecoder     *Decoder
)

// DecodeTo appends the decoded data from src to dst.
// The maximum decoded size is 1GiB,
// not including what may already be in dst.
//
// It is DecodeAll(src, dst) of a Decoder with the default options and
// WithDecoderMaxMemory(1 GiB), shared by all callers.
func DecodeTo(dst []byte, src []byte) ([]byte, error) {
	defaultDecoderOnce.Do(func() {
		var err error
		if defaultDecoder, err = NewReader(nil, WithDecoderMaxMemory(1<<30)); err != nil {
			panic(err) // unreachable: valid options
		}
	})
	return defaultDecoder.DecodeAll(src, dst)
}
