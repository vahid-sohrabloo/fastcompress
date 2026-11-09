package zlib

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/vahid-sohrabloo/fastzstd/flate"
	"github.com/vahid-sohrabloo/fastzstd/internal/adler32"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflate"
)

// The errors of the standard library's zlib reader (same texts).
var (
	// ErrChecksum is returned when reading ZLIB data that has an invalid
	// checksum.
	ErrChecksum = errors.New("zlib: invalid checksum")
	// ErrDictionary is returned when reading ZLIB data that has an invalid
	// dictionary.
	ErrDictionary = errors.New("zlib: invalid dictionary")
	// ErrHeader is returned when reading ZLIB data that has an invalid
	// header.
	ErrHeader = errors.New("zlib: invalid header")
)

// zlib header fields (RFC 1950) checked by the reader.
const (
	zlibCMDeflate      = 8      // CM: DEFLATE
	zlibCINFO32KWindow = 7      // largest CINFO: a 32 KiB window
	zlibFDICT          = 1 << 5 // FLG.FDICT: a preset dictionary is needed
)

// Resetter resets a ReadCloser returned by NewReader or NewReaderDict to
// switch to a new underlying Reader (the standard library's
// zlib.Resetter). This permits reusing a ReadCloser instead of allocating
// a new one.
type Resetter interface {
	// Reset discards any buffered data and resets the Resetter as if it
	// was newly initialized with the given reader.
	Reset(r io.Reader, dict []byte) error
}

// reader is the ReadCloser of NewReader.
type reader struct {
	sr      deflate.StreamReader
	adler   uint32
	err     error
	scratch [4]byte
}

// NewReader creates a new ReadCloser decompressing the zlib stream (RFC
// 1950) read from r: a drop-in replacement for the standard library's
// zlib.NewReader, decoding with libdeflate's decoder (see package flate,
// "Streaming Reader"). It reads the header: io.ErrUnexpectedEOF if r ends
// inside it, ErrHeader if it is invalid, ErrDictionary if it needs a
// preset dictionary. If r does not also implement io.ByteReader, the
// decompressor may read more data than necessary from r. If it does, r is
// left just after the stream once Read has returned io.EOF or ErrChecksum
// (a bytes.Reader or strings.Reader is read ahead and moved back then, so
// it is ahead before that and after other errors). It is the caller's
// responsibility to call Close on the ReadCloser when done. The ReadCloser
// also implements Resetter.
//
// The reader holds about 33 KiB, growing to about 173 KiB once a stream
// produces more than 16 KiB of output (see package flate, "Streaming
// Reader"). As the standard library's flate reader (but not its zlib
// reader, which then loops forever), Read returns io.ErrNoProgress if r
// keeps returning no data and no error.
func NewReader(r io.Reader) (io.ReadCloser, error) {
	return NewReaderDict(r, nil)
}

// NewReaderDict is like NewReader but uses a preset dictionary. It is
// ignored if the header does not ask for one (no FDICT); if it does, dict
// must be the one it was compressed with (its Adler-32 is compared, else
// ErrDictionary).
func NewReaderDict(r io.Reader, dict []byte) (io.ReadCloser, error) {
	z := new(reader)
	if err := z.Reset(r, dict); err != nil {
		return nil, err
	}
	return z, nil
}

// Reset makes z read a new zlib stream from r (with the preset dictionary
// dict, used only if the stream asks for one), keeping its buffers. It
// returns the error of reading the header, which also becomes the sticky
// error of Read.
func (z *reader) Reset(r io.Reader, dict []byte) error {
	z.sr.ResetSource(r)
	z.err = z.readHeader(dict)
	return z.err
}

// readHeader reads CMF, FLG and, with FDICT, the dictionary's Adler-32,
// and starts the DEFLATE stream.
func (z *reader) readHeader(dict []byte) error {
	b := z.scratch[:2]
	if err := z.readFull(b); err != nil {
		return err
	}
	h := binary.BigEndian.Uint16(b)
	if b[0]&0x0f != zlibCMDeflate || b[0]>>4 > zlibCINFO32KWindow || h%31 != 0 {
		return ErrHeader
	}
	if b[1]&zlibFDICT == 0 {
		z.sr.ResetStream(nil)
	} else {
		b = z.scratch[:4]
		if err := z.readFull(b); err != nil {
			return err
		}
		// As the standard library: only the Adler-32 is compared (a
		// stream made with an empty dictionary also reads without one).
		if binary.BigEndian.Uint32(b) != adler32.Checksum(dict) {
			return ErrDictionary
		}
		z.sr.ResetStream(dict)
	}
	z.adler = adler32.Init
	return nil
}

// readFull reads len(b) raw bytes; the end of the input is
// io.ErrUnexpectedEOF.
func (z *reader) readFull(b []byte) error {
	err := z.sr.ReadFull(b)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// Read reads decompressed data: io.EOF at the end of the stream (after its
// Adler-32 was verified), io.ErrUnexpectedEOF if the input ends first,
// ErrChecksum for a bad Adler-32, flate.CorruptInputError for invalid
// compressed data, or the error of the underlying reader. Errors are
// sticky.
func (z *reader) Read(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	var n int
	n, z.err = z.sr.Read(p)
	z.adler = adler32.Update(z.adler, p[:n])
	if z.err != io.EOF {
		if z.err == deflate.ErrBadData {
			z.err = flate.CorruptInputError(z.sr.CorruptOffset())
		}
		return n, z.err
	}

	// Finished the DEFLATE stream: the Adler-32 trailer.
	t := z.scratch[:4]
	if z.err = z.readFull(t); z.err != nil {
		return n, z.err
	}
	if binary.BigEndian.Uint32(t) != z.adler {
		z.err = ErrChecksum
		// The source is still left just after the trailer (a Seek
		// error is not reported over ErrChecksum).
		z.sr.Unread()
		return n, z.err
	}
	if z.err = z.sr.Unread(); z.err == nil {
		z.err = io.EOF
	}
	return n, z.err
}

// Close does not close the underlying io.Reader. As with the standard
// library, it returns the stream's error if there is one (other than
// io.EOF), and after the end of the stream it returns nil and clears the
// end state: a later Read reads a trailer again (io.ErrUnexpectedEOF at
// the end of the input).
func (z *reader) Close() error {
	if z.err != nil && z.err != io.EOF {
		return z.err
	}
	// The DEFLATE stream's error may be pending behind data not read yet.
	switch z.err = z.sr.Err(); z.err {
	case io.EOF:
		z.err = nil
	case deflate.ErrBadData:
		z.err = flate.CorruptInputError(z.sr.CorruptOffset())
	}
	return z.err
}
