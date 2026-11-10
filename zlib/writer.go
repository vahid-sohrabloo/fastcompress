package zlib

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/vahid-sohrabloo/fastcompress/flate"
	"github.com/vahid-sohrabloo/fastcompress/internal/adler32"
)

// HuffmanOnly is the level of a Writer that uses no matches, only Huffman
// coding (flate.HuffmanOnly, the standard library's zlib.HuffmanOnly).
const HuffmanOnly = flate.HuffmanOnly

// levelError is the error for an invalid Writer level: the standard
// library's text (with this module's level range), wrapping
// ErrInvalidLevel.
type levelError struct{ msg string }

// Error returns the message.
func (e *levelError) Error() string { return e.msg }

// Unwrap returns ErrInvalidLevel.
func (e *levelError) Unwrap() error { return ErrInvalidLevel }

// A Writer is an io.WriteCloser that writes a zlib stream (RFC 1950): a
// drop-in replacement for the standard library's zlib.Writer, compressing
// with package flate's Writer (libdeflate's compressors; see flate.Writer
// for chunking, Flush, sticky errors and memory).
//
// The 2-byte header (and, with a dictionary, its Adler-32) is written by
// the first Write, Flush or Close. FLEVEL is libdeflate's (0 below level 2,
// 1 below 6, 2 below 8, else 3; 0 for HuffmanOnly), which differs from the
// standard library's at level 7 (it writes 3). A stream without a
// dictionary whose total input is at most 1 MiB, without Flush, is
// byte-identical to Compress (libdeflate's libdeflate_zlib_compress) at the
// same level. Close writes the Adler-32 trailer; it does not close the
// underlying writer.
type Writer struct {
	_           [0]func() // not comparable, as the standard library's Writer
	w           io.Writer
	level       int
	hasDict     bool   // FDICT: a dictionary was given (even an empty one)
	dictID      uint32 // its Adler-32, computed once
	fw          *flate.Writer
	adler       uint32
	wroteHeader bool
	closed      bool
	err         error
	scratch     [6]byte // header or trailer (a local array would escape)
}

// NewWriter returns a Writer at DefaultCompression.
func NewWriter(w io.Writer) *Writer {
	z, _ := NewWriterLevelDict(w, DefaultCompression, nil)
	return z
}

// NewWriterLevel returns a Writer at level: DefaultCompression (6),
// NoCompression (0), BestSpeed (1) .. BestCompression (9), Level10 ..
// Level12 or HuffmanOnly (-2). Any other level returns an error wrapping
// ErrInvalidLevel.
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	return NewWriterLevelDict(w, level, nil)
}

// NewWriterLevelDict is NewWriterLevel with a preset dictionary (FDICT):
// the stream records dict's Adler-32 and must be decompressed with the
// same dictionary (compress/zlib.NewReaderDict). dict is not retained: its
// last 32 KiB are copied (only those can be referenced) and its Adler-32
// (over all of it) is computed once.
func NewWriterLevelDict(w io.Writer, level int, dict []byte) (*Writer, error) {
	fw, err := flate.NewWriterDict(w, level, dict)
	if err != nil {
		return nil, &levelError{fmt.Sprintf("zlib: invalid compression level: %d", level)}
	}
	z := &Writer{level: level, fw: fw}
	if dict != nil {
		// flate.Writer keeps the dictionary's last 32 KiB; the header
		// only needs its Adler-32.
		z.hasDict, z.dictID = true, adler32.Checksum(dict)
	}
	z.Reset(w)
	return z, nil
}

// Reset discards the Writer's state and makes it equivalent to
// NewWriterLevelDict with w and the Writer's level and dictionary, keeping
// its buffers and compressor.
func (z *Writer) Reset(w io.Writer) {
	z.w = w
	z.fw.Reset(w)
	z.adler = adler32.Init
	z.wroteHeader, z.closed = false, false
	z.err = nil
}

// flevel is libdeflate's compression level hint (libdeflate_zlib_compress),
// and 0 for HuffmanOnly (as the standard library writes).
func flevel(level int) uint16 {
	switch {
	case level == DefaultCompression:
		return 2
	case level < 2:
		return 0 // fastest (incl. HuffmanOnly)
	case level < 6:
		return 1 // fast
	case level < 8:
		return 2 // default
	}
	return 3 // slowest
}

// writeHeader writes CMF, FLG (FCHECK as libdeflate computes it) and, with
// a dictionary, its Adler-32.
func (z *Writer) writeHeader() error {
	z.wroteHeader = true
	b := z.scratch[:]
	hdr := uint16(8<<8|7<<12) | flevel(z.level)<<6
	n := 2
	if z.hasDict {
		hdr |= 1 << 5 // FDICT
		binary.BigEndian.PutUint32(b[2:], z.dictID)
		n = 6
	}
	hdr |= 31 - hdr%31
	binary.BigEndian.PutUint16(b, hdr)
	if m, err := z.w.Write(b[:n]); err != nil {
		return err
	} else if m != n {
		return io.ErrShortWrite
	}
	return nil
}

// Write compresses p. It returns len(p), nil, or 0 and the error of the
// underlying writer (sticky; even Write(nil) returns a sticky error). As
// the standard library's: the first Write, even of nothing, writes the
// header; after Close, Write(nil) returns 0, nil and any other Write an
// error that then sticks (later Flush and Close return it).
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if !z.wroteHeader && !z.closed {
		if z.err = z.writeHeader(); z.err != nil {
			return 0, z.err
		}
	}
	if len(p) == 0 {
		return 0, nil // also after Close, as the standard library's
	}
	n, err := z.fw.Write(p)
	if err != nil {
		// Sticky also for a Write after Close, as the standard library's.
		z.err = err
		return 0, err
	}
	z.adler = adler32.Update(z.adler, p)
	return n, nil
}

// Flush compresses the buffered data and writes it followed by a sync
// flush (flate.Writer.Flush), writing the header first if needed. After
// Close it returns an error that then sticks, as the standard library's.
func (z *Writer) Flush() error {
	if z.err != nil {
		return z.err
	}
	if !z.wroteHeader && !z.closed {
		if z.err = z.writeHeader(); z.err != nil {
			return z.err
		}
	}
	// Sticky also for a Flush after Close, as the standard library's.
	z.err = z.fw.Flush()
	return z.err
}

// Close finishes the stream: the header if not written yet, the final
// compressed data and the Adler-32 trailer. A second Close returns the
// first one's error (nil if it succeeded) and writes nothing.
func (z *Writer) Close() error {
	if z.closed {
		return z.err
	}
	z.closed = true
	if z.err != nil {
		return z.err
	}
	if !z.wroteHeader {
		if z.err = z.writeHeader(); z.err != nil {
			return z.err
		}
	}
	if z.err = z.fw.Close(); z.err != nil {
		return z.err
	}
	t := z.scratch[:4]
	binary.BigEndian.PutUint32(t, z.adler)
	if n, err := z.w.Write(t); err != nil {
		z.err = err
	} else if n != len(t) {
		z.err = io.ErrShortWrite
	}
	return z.err
}
