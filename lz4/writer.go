package lz4

import "io"

// Writer compresses a stream into one frame (C: LZ4F_compressBegin,
// LZ4F_compressUpdate, LZ4F_flush and LZ4F_compressEnd). Each Write is one
// LZ4F_compressUpdate of the data given, as unstable input: for the same
// sequence of Write, Flush and Close calls the frame is byte-identical to
// C's. A Writer is not safe for concurrent use.
type Writer struct {
	w     io.Writer
	prefs Preferences
	dict  []byte
	cdict *CDict
	c     frameCCtx
	buf   []byte
	begun bool
	err   error
}

// WriterOption configures a Writer.
type WriterOption func(*Writer)

// WithPreferences sets the frame parameters (C: LZ4F_preferences_t). The
// default is C's: fast level, 64 KB linked blocks, no checksums.
func WithPreferences(p Preferences) WriterOption { return func(z *Writer) { z.prefs = p } }

// WithLevel sets the compression level (see Preferences.CompressionLevel).
func WithLevel(level int) WriterOption { return func(z *Writer) { z.prefs.CompressionLevel = level } }

// WithDict compresses with dict as history (C:
// LZ4F_compressBegin_usingDict). dict must not change while the Writer uses it.
func WithDict(dict []byte) WriterOption { return func(z *Writer) { z.dict, z.cdict = dict, nil } }

// WithCDict compresses with a digested dictionary (C:
// LZ4F_compressBegin_usingCDict).
func WithCDict(d *CDict) WriterOption { return func(z *Writer) { z.cdict, z.dict = d, nil } }

// NewWriter returns a Writer writing one frame to w.
func NewWriter(w io.Writer, opts ...WriterOption) *Writer {
	z := &Writer{w: w}
	for _, o := range opts {
		o(z)
	}
	return z
}

// Reset discards the Writer's state and makes it write a new frame to w,
// with the same options.
func (z *Writer) Reset(w io.Writer) {
	z.w = w
	z.begun = false
	z.err = nil
}

func (z *Writer) grow(n int) []byte {
	if cap(z.buf) < n {
		z.buf = make([]byte, n)
	}
	return z.buf[:n]
}

func (z *Writer) begin() error {
	if z.begun {
		return nil
	}
	dst := z.grow(maxFHSize)
	var dict []byte
	if z.dict != nil {
		dict = z.dict
	}
	n, err := z.c.begin(dst, dict, z.cdict, &z.prefs)
	if err != nil {
		return err
	}
	z.begun = true
	_, err = z.w.Write(dst[:n])
	return err
}

func (z *Writer) emit(n int, err error) error {
	if err != nil {
		return err
	}
	if n > 0 {
		_, err = z.w.Write(z.buf[:n])
	}
	return err
}

// Write compresses p (C: LZ4F_compressUpdate).
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.err = z.begin(); z.err != nil {
		return 0, z.err
	}
	dst := z.grow(CompressBoundFrame(len(p), &z.prefs))
	n, err := z.c.update(dst, p, false, compressedMode)
	if z.err = z.emit(n, err); z.err != nil {
		return 0, z.err
	}
	return len(p), nil
}

// WriteUncompressed stores p as uncompressed blocks (C: LZ4F_compressUpdate
// with the uncompressed option).
func (z *Writer) WriteUncompressed(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.err = z.begin(); z.err != nil {
		return 0, z.err
	}
	dst := z.grow(CompressBoundFrame(len(p), &z.prefs))
	n, err := z.c.update(dst, p, false, uncompressedMode)
	if z.err = z.emit(n, err); z.err != nil {
		return 0, z.err
	}
	return len(p), nil
}

// Flush compresses the buffered input into a block and writes it (C:
// LZ4F_flush).
func (z *Writer) Flush() error {
	if z.err != nil {
		return z.err
	}
	if z.err = z.begin(); z.err != nil {
		return z.err
	}
	dst := z.grow(CompressBoundFrame(0, &z.prefs))
	z.err = z.emit(z.c.flush(dst))
	return z.err
}

// Close ends the frame: buffered input, end mark and content checksum (C:
// LZ4F_compressEnd). It does not close the underlying writer.
func (z *Writer) Close() error {
	if z.err != nil {
		return z.err
	}
	if z.err = z.begin(); z.err != nil {
		return z.err
	}
	dst := z.grow(CompressBoundFrame(0, &z.prefs))
	if z.err = z.emit(z.c.end(dst)); z.err != nil {
		return z.err
	}
	z.err = errWriterClosed
	return nil
}

var errWriterClosed = errorString("lz4: writer closed")

type errorString string

func (e errorString) Error() string { return string(e) }
