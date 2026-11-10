package flate

import (
	"errors"
	"fmt"
	"io"

	"github.com/vahid-sohrabloo/fastzstd/internal/deflate"
)

// HuffmanOnly is the level of a Writer that uses no matches, only Huffman
// coding (the standard library's compress/flate.HuffmanOnly). It has no
// libdeflate counterpart, so its output is not compared with libdeflate's;
// the whole-buffer Compressor does not accept it.
const HuffmanOnly = deflate.HuffmanOnly

// The streaming Writer is new design on top of the libdeflate port (C has
// no streaming compressor): input is buffered in chunks of chunkSize bytes,
// each compressed by deflate.Compressor.CompressChunk with the previous
// windowSize bytes as history, so matches cross chunk boundaries.
const (
	chunkSize  = 1 << 20 // input bytes compressed per CompressChunk call
	windowSize = 1 << 15 // DEFLATE's window: the history carried over
)

// errClosed is returned by Write and Flush after Close (the standard
// library's text).
var errClosed = errors.New("flate: closed writer")

// levelError is the error for an invalid Writer level: the standard
// library's text (with this module's level range), wrapping
// ErrInvalidLevel.
type levelError struct{ msg string }

// Error returns the message.
func (e *levelError) Error() string { return e.msg }

// Unwrap returns ErrInvalidLevel.
func (e *levelError) Unwrap() error { return ErrInvalidLevel }

// A Writer compresses the data written to it to raw DEFLATE (RFC 1951) and
// writes it to an underlying io.Writer. It is a drop-in replacement for the
// standard library's compress/flate.Writer, with libdeflate's compressors:
//
//   - Written data is buffered and compressed in 1 MiB chunks, each with
//     the 32 KiB before it as history (matches reach across chunks). A
//     chunk is compressed when it is full and more data arrives, or by
//     Flush or Close; Write itself compresses (and writes to the
//     underlying writer) only when a chunk fills up.
//   - When at most 1 MiB is written in total and Flush is never called,
//     the stream is byte-identical to Compress (libdeflate) at the same
//     level. Larger or flushed streams differ from libdeflate's
//     (libdeflate has no streaming mode) but always decode with any
//     DEFLATE decoder. Without Flush the output does not depend on how
//     the data is split into Write calls.
//   - Every chunk that is not the last ends with an empty stored block (a
//     sync flush, 5 bytes per MiB), as Flush does.
//   - Flush compresses what is buffered and writes a sync flush (an empty
//     stored block, 00 00 FF FF), so that a reader can decode everything
//     written so far; frequent Flush calls cost compression ratio.
//   - Errors from the underlying writer are sticky: every later Write
//     (even Write(nil)), Flush and Close returns the same error until
//     Reset. A short write
//     without an error is reported as io.ErrShortWrite (the standard
//     library ignores short writes).
//   - Write and Flush after Close return an error; Close after Close
//     returns the error of the first Close (nil if it succeeded) and writes
//     nothing.
//
// Memory: about 2 MiB of buffers (grown on demand, so small streams use
// less) plus the Compressor of the level (see the package documentation),
// kept across Reset. A Writer is not safe for concurrent use.
type Writer struct {
	w     io.Writer
	level int
	c     *deflate.Compressor
	dict  []byte // the preset dictionary's last 32 KiB (NewWriterDict)

	// buf holds the history (buf[:hist], the last <= 32 KiB of the data
	// before the pending chunk) followed by the pending input
	// (buf[hist:n], <= chunkSize bytes): adjacent, so CompressChunk needs
	// no copy.
	buf     []byte
	hist, n int
	out     []byte // compressed output of one chunk

	err    error
	closed bool
}

// NewWriter returns a Writer compressing at level: DefaultCompression
// (level 6), NoCompression (0), BestSpeed (1) .. BestCompression (9), the
// extensions Level10 .. Level12, or HuffmanOnly (-2). Any other level
// returns an error wrapping ErrInvalidLevel. The Writer allocates the
// level's Compressor; its buffers are allocated as data arrives.
func NewWriter(w io.Writer, level int) (*Writer, error) {
	return NewWriterDict(w, level, nil)
}

// NewWriterDict is NewWriter with a preset dictionary: the compressed data
// can reference dict (only its last 32 KiB), and must be decompressed with
// the same dictionary (compress/flate.NewReaderDict). dict is copied.
func NewWriterDict(w io.Writer, level int, dict []byte) (*Writer, error) {
	var c *deflate.Compressor
	if level == HuffmanOnly {
		c = deflate.NewHuffmanOnlyCompressor()
	} else {
		var err error
		if c, err = deflate.NewCompressor(level); err != nil {
			return nil, &levelError{fmt.Sprintf("flate: invalid compression level %d: want value in range [%d, %d]",
				level, HuffmanOnly, MaxLevel)}
		}
	}
	z := &Writer{w: w, level: level, c: c}
	if len(dict) > 0 {
		z.dict = append([]byte(nil), dict[max(0, len(dict)-windowSize):]...)
	}
	z.Reset(w)
	return z, nil
}

// Reset discards the Writer's state (buffered data, sticky error, closed
// state) and makes it equivalent to the result of NewWriter or
// NewWriterDict with w and the Writer's level and dictionary, keeping its
// buffers and Compressor.
func (z *Writer) Reset(w io.Writer) {
	z.w = w
	z.err = nil
	z.closed = false
	z.hist, z.n = 0, 0
	if len(z.dict) > 0 {
		z.grow(len(z.dict))
		z.hist = copy(z.buf, z.dict)
		z.n = z.hist
	}
}

// grow makes buf hold at least need bytes (at most windowSize+chunkSize),
// doubling it so the copies stay amortised.
func (z *Writer) grow(need int) {
	if need <= len(z.buf) {
		return
	}
	nb := make([]byte, min(max(need, 2*len(z.buf), 4096), windowSize+chunkSize))
	copy(nb, z.buf[:z.n])
	z.buf = nb
}

// Write buffers p for compression and returns len(p), nil, or 0 and the
// error of the underlying writer (or of a closed Writer). It writes to the
// underlying writer only when a full chunk is compressed.
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.closed {
		return 0, errClosed
	}
	total := len(p)
	for len(p) > 0 {
		if z.n-z.hist == chunkSize {
			if err := z.compress(false); err != nil {
				return 0, err
			}
		}
		z.grow(min(z.n+len(p), z.hist+chunkSize))
		k := copy(z.buf[z.n:min(len(z.buf), z.hist+chunkSize)], p)
		z.n += k
		p = p[k:]
	}
	return total, nil
}

// Flush compresses the buffered data and writes it followed by a sync
// flush, so that everything written so far can be decompressed. It is
// meant for network protocols; it does not close the stream.
func (z *Writer) Flush() error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return errClosed
	}
	return z.compress(false)
}

// Close compresses and writes the buffered data as the final block. It
// does not close the underlying writer.
func (z *Writer) Close() error {
	if z.closed {
		return z.err
	}
	z.closed = true
	if z.err != nil {
		return z.err
	}
	return z.compress(true)
}

// compress compresses the pending chunk (non-final: ending with a sync
// flush), writes it and keeps its last 32 KiB as the next history.
func (z *Writer) compress(final bool) error {
	src := z.buf[z.hist:z.n]
	if bound := z.c.CompressChunkBound(len(src)); len(z.out) < bound {
		z.out = make([]byte, max(bound, min(2*len(z.out), z.c.CompressChunkBound(chunkSize))))
	}
	m := z.c.CompressChunk(z.out, z.buf[:z.hist], src, final)
	if m == 0 {
		// Cannot happen: z.out holds CompressChunkBound(len(src)) bytes.
		panic("flate: chunk bound violated")
	}
	if n, err := z.w.Write(z.out[:m]); err != nil {
		z.err = err
	} else if n != m {
		z.err = io.ErrShortWrite
	}
	if !final {
		h := min(z.n, windowSize)
		copy(z.buf, z.buf[z.n-h:z.n])
		z.hist, z.n = h, h
	}
	return z.err
}
