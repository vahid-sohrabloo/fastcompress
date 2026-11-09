package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	stdflate "compress/flate"
	stdgzip "compress/gzip"
	kflate "github.com/klauspost/compress/flate"
	kgzip "github.com/klauspost/compress/gzip"
)

// Adapter is the black-box surface the benchmark needs from a library. The
// contract mirrors libdeflate's whole-buffer API: compress into a caller
// buffer, decompress into a caller buffer of exactly the decoded size.
type Adapter interface {
	// NewCompressor returns a reusable compressor for the level, or an error
	// wrapping ErrUnsupportedLevel. format is "raw" or "gzip".
	NewCompressor(level int, format string) (Compressor, error)
	NewDecompressor(format string) (Decompressor, error)
}

type Compressor interface {
	// Compress writes the complete stream for src into dst and returns its
	// length. It errors if dst is too small.
	Compress(dst, src []byte) (int, error)
}

type Decompressor interface {
	// Decompress decodes src into dst, which must be exactly the decoded size
	// (a longer or shorter stream is an error), and returns the length.
	Decompress(dst, src []byte) (int, error)
}

var (
	// ErrNotPorted: the library under test does not exist yet (exit code 3).
	ErrNotPorted = errors.New("not ported yet")
	// ErrUnsupportedLevel: the library has no such level (exit code 4).
	ErrUnsupportedLevel = errors.New("unsupported level")
)

// sliceWriter appends into a fixed buffer, failing on overflow, so the
// stdlib-style writers can be driven with a whole-buffer API without a copy.
type sliceWriter struct {
	buf []byte
	n   int
}

func (w *sliceWriter) Write(p []byte) (int, error) {
	if len(p) > len(w.buf)-w.n {
		return 0, io.ErrShortBuffer
	}
	w.n += copy(w.buf[w.n:], p)
	return len(p), nil
}

type resetWriter interface {
	io.WriteCloser
	Reset(io.Writer)
}

type streamCompressor struct {
	w  resetWriter
	sw sliceWriter
}

func (c *streamCompressor) Compress(dst, src []byte) (int, error) {
	c.sw = sliceWriter{buf: dst}
	c.w.Reset(&c.sw)
	if _, err := c.w.Write(src); err != nil {
		return 0, err
	}
	if err := c.w.Close(); err != nil {
		return 0, err
	}
	return c.sw.n, nil
}

type streamDecompressor struct {
	r   io.Reader
	rs  func(io.Reader) error
	src bytes.Reader
	one [1]byte // trailing-byte probe; a field so Decompress does not allocate
}

func (d *streamDecompressor) Decompress(dst, src []byte) (int, error) {
	d.src.Reset(src)
	if err := d.rs(&d.src); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(d.r, dst)
	if err != nil {
		return n, err
	}
	// The stream must end exactly here.
	if m, err := d.r.Read(d.one[:]); m != 0 || err != io.EOF {
		return n, fmt.Errorf("decoded stream longer than %d bytes (err=%v)", len(dst), err)
	}
	return n, nil
}

// stdAdapter drives compress/flate + compress/gzip; kpAdapter the klauspost
// equivalents. Levels 1..9 map to the same numeric level; 0 (libdeflate
// stored) and 10..12 have no counterpart, so they are reported unsupported.
type stdAdapter struct{}
type kpAdapter struct{}

func checkLevel(level int) error {
	if level < 1 || level > 9 {
		return fmt.Errorf("level %d: %w", level, ErrUnsupportedLevel)
	}
	return nil
}

func (stdAdapter) NewCompressor(level int, format string) (Compressor, error) {
	if err := checkLevel(level); err != nil {
		return nil, err
	}
	var w resetWriter
	var err error
	if format == "gzip" {
		w, err = stdgzip.NewWriterLevel(io.Discard, level)
	} else {
		w, err = stdflate.NewWriter(io.Discard, level)
	}
	if err != nil {
		return nil, err
	}
	return &streamCompressor{w: w}, nil
}

func (stdAdapter) NewDecompressor(format string) (Decompressor, error) {
	d := &streamDecompressor{}
	d.src.Reset(nil)
	if format == "gzip" {
		// gzip.NewReader needs a valid header up front; Reset is used instead.
		r := new(stdgzip.Reader)
		d.r, d.rs = r, r.Reset
		return d, nil
	}
	r := stdflate.NewReader(&d.src)
	fr := r.(stdflate.Resetter)
	d.r, d.rs = r, func(in io.Reader) error { return fr.Reset(in, nil) }
	return d, nil
}

func (kpAdapter) NewCompressor(level int, format string) (Compressor, error) {
	if err := checkLevel(level); err != nil {
		return nil, err
	}
	var w resetWriter
	var err error
	if format == "gzip" {
		w, err = kgzip.NewWriterLevel(io.Discard, level)
	} else {
		w, err = kflate.NewWriter(io.Discard, level)
	}
	if err != nil {
		return nil, err
	}
	return &streamCompressor{w: w}, nil
}

func (kpAdapter) NewDecompressor(format string) (Decompressor, error) {
	d := &streamDecompressor{}
	if format == "gzip" {
		r := new(kgzip.Reader)
		d.r, d.rs = r, r.Reset
		return d, nil
	}
	r := kflate.NewReader(&d.src)
	fr := r.(kflate.Resetter)
	d.r, d.rs = r, func(in io.Reader) error { return fr.Reset(in, nil) }
	return d, nil
}
