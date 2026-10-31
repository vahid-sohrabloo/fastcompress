package zstd

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

// Encoder provides encoding to Zstandard.
// An Encoder can be used for either compressing a stream via the
// io.WriteCloser interface supported by the Encoder or as multiple independent
// tasks via the EncodeAll function.
// Smaller encodes are encouraged to use the EncodeAll function.
// Use NewWriter to create a new instance.
//
// The streaming Writer (Write, ReadFrom, Flush, Close) drives C's
// ZSTD_compressStream2 on one compression context: Write is
// ZSTD_e_continue, Flush is ZSTD_e_flush and Close is ZSTD_e_end, each
// repeated, as C's documentation prescribes, with an output buffer of
// ZSTD_CStreamOutSize() bytes that is written to the io.Writer after every
// call. The output is therefore C's for the same calls (C's bytes do not
// depend on how input is split between ZSTD_e_continue calls; flushes,
// and where Close falls, do matter). A Write of no bytes makes no call; a
// stream with nothing written produces no output unless WithZeroFrames is
// set. An Encoder must not be used for streaming from several goroutines
// at once, and ResetWithOptions must not run while EncodeAll calls are
// running.
type Encoder struct {
	o     encoderOptions
	cd    *cdict    // the dictionary of o, digested; shared by all contexts
	cctxs sync.Pool // *cctx configured from o; one per concurrent EncodeAll
	s     encStream
}

// encStream is the state of the streaming Writer.
type encStream struct {
	c        *cctx     // C's ZSTD_CStream; created on first use
	w        io.Writer // destination; nil until NewWriter/Reset gives one
	out      []byte    // output buffer, ZSTD_CStreamOutSize() bytes
	readBuf  []byte    // ReadFrom's input buffer
	err      error     // sticky error, cleared by Reset
	started  bool      // a frame is open: data was written since Reset
	closed   bool      // Close succeeded; cleared by Reset
	pledged  int64     // content size from ResetContentSize; < 0 unknown
	nWritten int64     // bytes written to w since Reset, for padding
}

// errNilWriter is returned by the streaming methods when output must be
// written but the Encoder has no io.Writer (NewWriter(nil) without Reset).
var errNilWriter = errors.New("zstd: Encoder has no io.Writer (use Reset to set one)")

// NewWriter will create a new Zstandard encoder.
// If the encoder will be used for encoding blocks a nil writer can be used.
func NewWriter(w io.Writer, opts ...EOption) (*Encoder, error) {
	e := &Encoder{}
	e.o.setDefault()
	for _, o := range opts {
		if err := o(&e.o); err != nil {
			return nil, err
		}
	}
	e.cctxs.New = func() any { return e.newCCtx() }
	e.s.w = w
	e.s.pledged = -1
	cd, err := makeCDict(&e.o)
	if err != nil {
		return nil, err
	}
	e.cd = cd
	return e, nil
}

// makeCDict digests the dictionary of o (nil if none), as C's
// ZSTD_initLocalDict does at a context's first frame: from the context's
// requested parameters. The CDict's stored state depends only on the
// dictionary, the level and the window size, and ResetWithOptions can
// change neither of the last two.
func makeCDict(o *encoderOptions) (*cdict, error) {
	if o.dict == nil {
		return nil, nil
	}
	c := newCCtx()
	configureParams(c, o)
	cd, err := createCDictAdvanced2(o.dict, o.dictContentType(), &c.requestedParams)
	if err != nil {
		return nil, err
	}
	if o.dictRaw {
		// C: zstd_compress.c:ZSTD_compress_insertDictionary — raw content
		// has dictID 0 in C; WithEncoderDictRaw's ID goes into the frame
		// header instead (it is used nowhere else).
		cd.dictID = o.dictID
	}
	return cd, nil
}

// dictContentType is the C content type of o.dict.
func (o *encoderOptions) dictContentType() dictContentType {
	if o.dictRaw {
		return dctRawContent
	}
	return dctFullDict
}

// newCCtx returns a compression context with the Encoder's C parameters.
func (e *Encoder) newCCtx() *cctx {
	c := newCCtx()
	e.configure(c)
	return c
}

// configure sets c's C parameters and dictionary from the Encoder's options.
func (e *Encoder) configure(c *cctx) {
	configureParams(c, &e.o)
	if e.cd != nil {
		c.setLocalDict(e.o.dict, e.o.dictContentType(), e.cd)
	} else {
		c.clearAllDicts()
	}
}

// configureParams sets c's C parameters (not the dictionary) from o.
func configureParams(c *cctx, o *encoderOptions) {
	c.setLevel(zstdLevels[o.level])
	c.setChecksum(o.crc)
	_ = c.setWindowLog(o.windowLog) // in range: validated by WithWindowSize
	c.requestedParams.literalCompressionMode = o.literalCompressionMode()
	c.hugePages = o.hugePages
}

// EncodeAll will encode all input in src and append it to dst.
// This function can be called concurrently, but each call will only run on a single goroutine.
// If empty input is given, nothing is returned, unless WithZeroFrames is specified.
// Encoded blocks can be concatenated and the result will be the combined input stream.
// Data compressed with EncodeAll can be decoded with the Decoder,
// using either a stream or DecodeAll.
//
// The frame is C's ZSTD_compress2 output for the mapped parameters (see
// EOption). Calls share a pool of compression contexts; as in C, reusing a
// context does not change the output (at SpeedBetterCompression and
// SpeedBestCompression C's row match finder re-salts its hash on every
// reset, which this port mirrors, but the salt only relabels hash rows), so
// the frame does not depend on which pooled context serves the call.
func (e *Encoder) EncodeAll(src, dst []byte) []byte {
	if len(src) == 0 && !e.o.zeroFrames {
		return dst
	}
	c := e.cctxs.Get().(*cctx)
	e.configure(c) // ResetWithOptions may have changed the options since c was pooled
	dst, err := c.compress2(dst, src)
	if err != nil {
		panic(err) // unreachable with validated options
	}
	e.cctxs.Put(c)
	if e.o.pad > 1 {
		dst = addPadding(dst, e.o.pad)
	}
	return dst
}

// MaxEncodedSize returns the expected maximum size of an encoded block or stream.
func (e *Encoder) MaxEncodedSize(size int) int {
	n := compressBound(size)
	if e.o.pad > 1 {
		n += e.o.pad + skippableHeaderSize
	}
	return n
}

// Skippable frame format. C: zstd.h ZSTD_MAGIC_SKIPPABLE_START,
// ZSTD_SKIPPABLEHEADERSIZE.
const (
	magicSkippableStart = 0x184D2A50
	skippableHeaderSize = 8
)

// addPadding appends a skippable frame of random content so that len(b)
// becomes a multiple of n. A skippable frame is at least its 8-byte header,
// so a shorter gap is widened by multiples of n.
//
// C: no equivalent (klauspost's WithEncoderPadding).
func addPadding(b []byte, n int) []byte {
	return appendPadding(b, int64(len(b)), n)
}

// appendPadding appends to b the skippable frame that makes total (the
// size of the output so far) a multiple of n; see addPadding.
func appendPadding(b []byte, total int64, n int) []byte {
	add := int((int64(n) - total%int64(n)) % int64(n))
	if add == 0 {
		return b
	}
	for add < skippableHeaderSize {
		add += n
	}
	b = binary.LittleEndian.AppendUint32(b, magicSkippableStart)
	b = binary.LittleEndian.AppendUint32(b, uint32(add-skippableHeaderSize))
	n0 := len(b)
	b = append(b, make([]byte, add-skippableHeaderSize)...)
	rand.Read(b[n0:])
	return b
}

// Write data to the encoder.
// Input data will be buffered and as the buffer fills up
// content will be compressed and written to the output.
// When done writing, use Close to flush the remaining output
// and write CRC if requested.
//
// C: ZSTD_compressStream2(ZSTD_e_continue), repeated until p is consumed.
// Errors, including those of the io.Writer, are sticky until Reset.
func (e *Encoder) Write(p []byte) (n int, err error) {
	s := &e.s
	if err := s.check(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if s.w == nil {
		return 0, errNilWriter
	}
	c := e.startFrame()
	for {
		out, consumed, _, err := c.compressStream2(s.out[:0], p[n:], eContinue)
		n += consumed
		if err == nil {
			err = s.write(out)
		}
		if err != nil {
			s.err = err
			return n, err
		}
		if n == len(p) {
			return n, nil
		}
	}
}

// ReadFrom reads data from r until EOF or error.
// The return value n is the number of bytes read.
// Any error except io.EOF encountered during the read is also returned.
//
// The Copy function uses ReaderFrom if available.
//
// Every chunk read is passed to Write; how the input is split does not
// change the output.
func (e *Encoder) ReadFrom(r io.Reader) (n int64, err error) {
	s := &e.s
	if err := s.check(); err != nil {
		return 0, err
	}
	if s.readBuf == nil {
		s.readBuf = make([]byte, blockSizeMax)
	}
	for {
		nr, rerr := r.Read(s.readBuf)
		n += int64(nr)
		if nr > 0 {
			if _, err := e.Write(s.readBuf[:nr]); err != nil {
				return n, err
			}
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			return n, rerr
		}
	}
}

// Flush will send the currently written data to output
// and block until everything has been written.
// This should only be used on rare occasions where pushing the currently queued data is critical.
//
// C: ZSTD_compressStream2(ZSTD_e_flush), repeated until it returns 0. With
// nothing written since Reset (no frame started) it does nothing.
func (e *Encoder) Flush() error {
	s := &e.s
	if err := s.check(); err != nil {
		return err
	}
	if !s.started {
		return nil
	}
	return e.drain(eFlush)
}

// Close will flush the final output and close the stream.
// The function will block until everything has been written.
// The Encoder can still be re-used after calling this.
//
// C: ZSTD_compressStream2(ZSTD_e_end), repeated until it returns 0, then
// the WithEncoderPadding skippable frame. With nothing written since Reset,
// it writes nothing, unless WithZeroFrames is set: then C's frame for empty
// input (whose first ZSTD_e_end call pledges a content size of 0). After
// ResetContentSize, C returns an error when the written size differs from
// the pledged one; Close returns that error too when nothing was written
// (where C would pledge 0 instead). Closing a closed Encoder returns nil.
func (e *Encoder) Close() error {
	s := &e.s
	if s.err != nil {
		return s.err
	}
	if s.closed {
		return nil
	}
	if !s.started {
		if s.pledged > 0 {
			s.err = errSrcSizeWrong
			return s.err
		}
		if !e.o.zeroFrames {
			s.closed = true
			return nil
		}
		if s.w == nil {
			return errNilWriter
		}
		e.startFrame()
	}
	if err := e.drain(eEnd); err != nil {
		return err
	}
	if e.o.pad > 1 {
		if err := s.write(appendPadding(nil, s.nWritten, e.o.pad)); err != nil {
			s.err = err
			return err
		}
	}
	s.started = false
	s.closed = true
	return nil
}

// Reset will re-initialize the writer and new writes will encode to the supplied writer
// as a new, independent stream.
//
// C: ZSTD_CCtx_reset(ZSTD_reset_session_only): the content size is unknown.
func (e *Encoder) Reset(w io.Writer) {
	e.ResetContentSize(w, -1)
}

// ResetContentSize will reset and set a content size for the next stream.
// If the bytes written does not match the size given an error will be returned
// when calling Close().
// This is removed when Reset is called.
// Sizes <= 0 results in no content size set.
//
// C: ZSTD_CCtx_reset(ZSTD_reset_session_only), then
// ZSTD_CCtx_setPledgedSrcSize(size). The frame header then carries the
// size and C's parameters are chosen for it. Writing more than size fails
// once C compresses the excess (Write, Flush or Close returns C's error),
// writing less fails at Close.
func (e *Encoder) ResetContentSize(w io.Writer, size int64) {
	s := &e.s
	*s = encStream{c: s.c, w: w, out: s.out, readBuf: s.readBuf, pledged: -1}
	if size > 0 {
		s.pledged = size
	}
}

// ResetWithOptions will re-initialize the writer and apply the given options
// as a new, independent stream.
// Options are applied on top of the existing options.
// Some options cannot be changed on reset and will return an error.
//
// WithEncoderLevel, WithWindowSize, WithEncoderConcurrency,
// WithConcurrentBlocks and WithLowerEncoderMem cannot change (passing their current value is
// allowed); on error nothing is reset. The new options apply to the next
// stream and to EncodeAll. A new dictionary is digested here.
func (e *Encoder) ResetWithOptions(w io.Writer, opts ...EOption) error {
	o := e.o
	for _, opt := range opts {
		if err := opt(&o); err != nil {
			return err
		}
	}
	if o.level != e.o.level || o.windowLog != e.o.windowLog || o.concurrent != e.o.concurrent || o.concBlocks != e.o.concBlocks || o.lowerMem != e.o.lowerMem || o.hugePages != e.o.hugePages {
		return errors.New("zstd: WithEncoderLevel, WithWindowSize, WithEncoderConcurrency, WithConcurrentBlocks, WithLowerEncoderMem and WithHugePages cannot be changed with ResetWithOptions")
	}
	newDict := len(o.dict) != len(e.o.dict) || len(o.dict) > 0 && &o.dict[0] != &e.o.dict[0] ||
		o.dictRaw != e.o.dictRaw || o.dictID != e.o.dictID
	cd := e.cd
	if newDict {
		var err error
		if cd, err = makeCDict(&o); err != nil {
			return err // nothing is reset
		}
	}
	e.o, e.cd = o, cd
	e.Reset(w)
	return nil
}

// check returns the error a streaming call must fail with, if any.
func (s *encStream) check() error {
	if s.err != nil {
		return s.err
	}
	if s.closed {
		return ErrEncoderClosed
	}
	return nil
}

// startFrame returns the stream's context, making it ready for a new
// frame if none is open (C: the ZSTD_CCtx_reset and
// ZSTD_CCtx_setPledgedSrcSize calls of Reset and ResetContentSize, made
// when the stream's first data arrives).
func (e *Encoder) startFrame() *cctx {
	s := &e.s
	if s.c == nil {
		s.c = e.newCCtx()
		s.out = make([]byte, 0, cStreamOutSize)
	}
	if !s.started {
		s.started = true
		s.c.reset()
		e.configure(s.c)
		if s.pledged > 0 {
			s.c.setPledgedSrcSize(uint64(s.pledged))
		}
	}
	return s.c
}

// drain repeats ZSTD_compressStream2 with op (eFlush or eEnd) and no input
// until nothing is left to flush, writing the output.
func (e *Encoder) drain(op endDirective) error {
	s := &e.s
	for {
		out, _, remaining, err := s.c.compressStream2(s.out[:0], nil, op)
		if err == nil {
			err = s.write(out)
		}
		if err != nil {
			s.err = err
			return err
		}
		if remaining == 0 {
			return nil
		}
	}
}

// write writes b to the destination.
func (s *encStream) write(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	n, err := s.w.Write(b)
	s.nWritten += int64(n)
	if err == nil && n < len(b) {
		err = io.ErrShortWrite
	}
	return err
}

var (
	defaultEncoderOnce sync.Once
	defaultEncoder     *Encoder
)

// EncodeTo appends the encoded data from src to dst.
//
// It is EncodeAll(src, dst) of an Encoder with the default options
// (SpeedDefault, content checksum on), shared by all callers.
func EncodeTo(dst []byte, src []byte) []byte {
	defaultEncoderOnce.Do(func() {
		var err error
		if defaultEncoder, err = NewWriter(nil); err != nil {
			panic(err) // unreachable: no options
		}
	})
	return defaultEncoder.EncodeAll(src, dst)
}
