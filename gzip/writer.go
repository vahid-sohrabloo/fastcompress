package gzip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// HuffmanOnly is the level of a Writer that uses no matches, only Huffman
// coding (flate.HuffmanOnly, the standard library's gzip.HuffmanOnly).
const HuffmanOnly = flate.HuffmanOnly

// The header errors of the standard library's gzip.Writer (same texts).
var (
	errNonLatin1 = errors.New("gzip.Write: non-Latin-1 header string")
	errExtraLen  = errors.New("gzip.Write: Extra data is too large")
)

// gzip header constants (RFC 1952).
const (
	gzipID1, gzipID2 = 0x1f, 0x8b
	gzipCMDeflate    = 8
	flagExtra        = 1 << 2
	flagName         = 1 << 3
	flagComment      = 1 << 4
	osUnknown        = 255
)

// The Header of a gzip member: the standard library's gzip.Header. Strings
// must be UTF-8 and are written as ISO 8859-1 (Latin-1), the encoding RFC
// 1952 requires; a character outside Latin-1, or NUL, is an error.
type Header struct {
	Comment string    // comment (FCOMMENT)
	Extra   []byte    // "extra data" (FEXTRA, at most 65535 bytes); nil: none
	ModTime time.Time // modification time (MTIME); none (0) unless after 1970; 32 bits, so after 2106 it wraps
	Name    string    // file name (FNAME)
	OS      byte      // operating system type (255: unknown, the default)
}

// levelError is the error for an invalid Writer level: the standard
// library's text (with this module's level range), wrapping
// ErrInvalidLevel.
type levelError struct{ msg string }

// Error returns the message.
func (e *levelError) Error() string { return e.msg }

// Unwrap returns ErrInvalidLevel.
func (e *levelError) Unwrap() error { return ErrInvalidLevel }

// A Writer is an io.WriteCloser that writes a gzip member (RFC 1952): a
// drop-in replacement for the standard library's gzip.Writer, compressing
// with package flate's Writer (libdeflate's compressors; see flate.Writer
// for chunking, Flush, sticky errors and memory).
//
// The Header fields are written by the first Write, Flush or Close, as the
// standard library does; set them before. A Writer whose total input is at
// most 1 MiB, without Flush and with a zero Header except OS 255 (as
// NewWriter sets it), writes the bytes of Compress (libdeflate's
// libdeflate_gzip_compress) at the same level. XFL is libdeflate's (4 at
// levels 0 and 1, 2 from level 8, else 0; 0 for HuffmanOnly), which differs
// from the standard library's at level 0 (it writes 0) and level 8 (0).
//
// Close writes the CRC-32 and size trailer; it does not close the
// underlying writer. Several members written one after another (Close,
// then Reset on the same io.Writer) form a multi-member gzip file, which
// gzip readers decode as the concatenation.
type Writer struct {
	Header // written by the first Write, Flush or Close

	w           io.Writer
	level       int
	fw          *flate.Writer
	crc         uint32
	size        uint32
	wroteHeader bool
	closed      bool
	err         error
	hbuf        []byte // the header, kept for reuse
	tail        [8]byte
}

// NewWriter returns a Writer at DefaultCompression. Its Header has OS 255
// (unknown) and is otherwise empty.
func NewWriter(w io.Writer) *Writer {
	z, _ := NewWriterLevel(w, DefaultCompression)
	return z
}

// NewWriterLevel returns a Writer at level: DefaultCompression (6),
// NoCompression (0), BestSpeed (1) .. BestCompression (9), Level10 ..
// Level12 or HuffmanOnly (-2). Any other level returns an error wrapping
// ErrInvalidLevel.
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	fw, err := flate.NewWriter(w, level)
	if err != nil {
		return nil, &levelError{fmt.Sprintf("gzip: invalid compression level: %d", level)}
	}
	z := &Writer{level: level, fw: fw}
	z.Reset(w)
	return z, nil
}

// Reset discards the Writer's state and makes it equivalent to NewWriterLevel
// with w and the Writer's level: the Header is reset to OS 255 and nothing
// else, the buffers and compressor are kept.
func (z *Writer) Reset(w io.Writer) {
	z.Header = Header{OS: osUnknown}
	z.w = w
	z.fw.Reset(w)
	z.crc, z.size = 0, 0
	z.wroteHeader, z.closed = false, false
	z.err = nil
}

// xfl is libdeflate's XFL for the level (libdeflate_gzip_compress), and 0
// for HuffmanOnly (as the standard library writes).
func xfl(level int) byte {
	switch {
	case level == HuffmanOnly || level == DefaultCompression:
		return 0
	case level < 2:
		return 4 // fastest
	case level >= 8:
		return 2 // slowest
	}
	return 0
}

// writeHeader writes the member header. On a Header error it writes the
// header up to the bad field (as the standard library does) and returns
// the error.
func (z *Writer) writeHeader() error {
	z.wroteHeader = true
	h := append(z.hbuf[:0], gzipID1, gzipID2, gzipCMDeflate, 0, 0, 0, 0, 0, xfl(z.level), z.OS)
	if z.Extra != nil {
		h[3] |= flagExtra
	}
	if z.Name != "" {
		h[3] |= flagName
	}
	if z.Comment != "" {
		h[3] |= flagComment
	}
	// As the standard library: any time after the epoch, truncated to 32
	// bits (times after 2106 wrap).
	if t := z.ModTime; t.After(time.Unix(0, 0)) {
		binary.LittleEndian.PutUint32(h[4:], uint32(t.Unix()))
	}
	var herr error
	if z.Extra != nil {
		if len(z.Extra) > 0xffff {
			herr = errExtraLen
		} else {
			h = binary.LittleEndian.AppendUint16(h, uint16(len(z.Extra)))
			h = append(h, z.Extra...)
		}
	}
	if herr == nil && z.Name != "" {
		h, herr = appendLatin1(h, z.Name)
	}
	if herr == nil && z.Comment != "" {
		h, herr = appendLatin1(h, z.Comment)
	}
	z.hbuf = h
	if n, err := z.w.Write(h); err != nil {
		return err
	} else if n != len(h) {
		return io.ErrShortWrite
	}
	return herr
}

// appendLatin1 appends s, converted from UTF-8 to Latin-1, and a NUL; on
// error h is returned unchanged.
func appendLatin1(h []byte, s string) ([]byte, error) {
	n := len(h)
	for _, r := range s {
		if r == 0 || r > 0xff {
			return h[:n], errNonLatin1
		}
		h = append(h, byte(r))
	}
	return append(h, 0), nil
}

// Write compresses p. It returns len(p), nil, or 0 and the error of the
// header or of the underlying writer (sticky, as are header errors; even
// Write(nil) returns a sticky error). After Close it returns an error,
// which then also sticks (later Flush and Close return it), as the
// standard library's.
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if !z.wroteHeader && !z.closed {
		if z.err = z.writeHeader(); z.err != nil {
			return 0, z.err
		}
	}
	n, err := z.fw.Write(p)
	if err != nil {
		// Sticky also for a Write after Close, as the standard library's.
		z.err = err
		return 0, err
	}
	z.crc = crc32.Update(z.crc, crc32.IEEETable, p)
	z.size += uint32(len(p))
	return n, nil
}

// Flush compresses the buffered data and writes it followed by a sync
// flush (flate.Writer.Flush), writing the header first if needed. After
// Close it does nothing and returns nil (or the sticky error), as the
// standard library's.
func (z *Writer) Flush() error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return nil // as the standard library's gzip.Writer
	}
	if !z.wroteHeader {
		if z.err = z.writeHeader(); z.err != nil {
			return z.err
		}
	}
	z.err = z.fw.Flush()
	return z.err
}

// Close finishes the member: the header if not written yet, the final
// compressed data and the trailer. A second Close returns the first one's
// error (nil if it succeeded) and writes nothing.
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
	// In the Writer: a local array would escape to the heap.
	t := z.tail[:]
	binary.LittleEndian.PutUint32(t[:4], z.crc)
	binary.LittleEndian.PutUint32(t[4:], z.size)
	if n, err := z.w.Write(t); err != nil {
		z.err = err
	} else if n != len(t) {
		z.err = io.ErrShortWrite
	}
	return z.err
}
