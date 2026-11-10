package gzip

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/flate"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflate"
)

// The errors of the standard library's gzip.Reader (same texts).
var (
	// ErrChecksum is returned when reading gzip data that has an invalid
	// checksum (CRC-32 or size).
	ErrChecksum = errors.New("gzip: invalid checksum")
	// ErrHeader is returned when reading gzip data that has an invalid
	// header.
	ErrHeader = errors.New("gzip: invalid header")
)

// Header flag (FLG) bits read by the Reader, besides those of writer.go.
const flagHdrCrc = 1 << 1

// A Reader is an io.Reader that can be read to retrieve uncompressed data
// from a gzip file (RFC 1952): a drop-in replacement for the standard
// library's gzip.Reader, decoding with libdeflate's decoder (see package
// flate, "Streaming Reader").
//
// A gzip file can be a concatenation of gzip files ("members"), each with
// its own header. Reads return the concatenation of their uncompressed
// data; only the first header is recorded in the Reader's fields (as the
// standard library does). Each member's CRC-32 and size are verified when
// its end is reached: on a mismatch Read returns ErrChecksum after the
// member's data. Clients should treat data returned before an error as
// tentative.
//
// Differences from libdeflate's whole-buffer DecompressGzip (this Reader
// follows the standard library): the header CRC (FHCRC) is verified,
// reserved FLG bits are ignored, and further members are read.
type Reader struct {
	Header // valid after NewReader or Reader.Reset

	sr          *deflate.StreamReader
	digest      uint32 // CRC-32 of the member's data so far
	size        uint32 // its size mod 2^32
	err         error
	multistream bool
	buf         [512]byte // header fields and the trailer
}

// NewReader creates a new Reader reading the given reader and reads the
// gzip header: io.EOF if r is empty, io.ErrUnexpectedEOF if it ends inside
// the header, ErrHeader if the header is invalid. If r does not also
// implement io.ByteReader, the decompressor may read more data than
// necessary from r. If it does, r is left just after the last member read
// once Read has returned io.EOF or ErrChecksum (a bytes.Reader or
// strings.Reader is read ahead and moved back then, so it is ahead before
// that and after other errors). It is the caller's responsibility to call
// Close when done.
//
// The Reader holds about 33 KiB, growing to about 173 KiB once a member
// produces more than 16 KiB of output (see package flate, "Streaming
// Reader"). As the standard library's flate reader (but not its gzip
// reader, which then loops forever), Read returns io.ErrNoProgress if r
// keeps returning no data and no error.
func NewReader(r io.Reader) (*Reader, error) {
	z := new(Reader)
	if err := z.Reset(r); err != nil {
		return nil, err
	}
	return z, nil
}

// Reset discards the Reader z's state and makes it equivalent to the result
// of its original state from NewReader, but reading from r instead (its
// buffers are kept). It re-enables multistream mode. It returns the error
// of reading the header, which also becomes the sticky error of Read.
func (z *Reader) Reset(r io.Reader) error {
	if z.sr == nil {
		z.sr = new(deflate.StreamReader)
	}
	z.sr.ResetSource(r)
	z.multistream = true
	z.Header, z.err = z.readHeader(true)
	return z.err
}

// Multistream controls whether the reader supports multistream files.
//
// If enabled (the default), the Reader expects the input to be a sequence
// of individually gzipped data streams, each with its own header and
// trailer, ending at EOF. The effect is that the concatenation of a
// sequence of gzipped files is treated as equivalent to the gzip of the
// concatenation of the sequence. This is standard behavior for gzip
// readers.
//
// Calling Multistream(false) disables this behavior; disabling the
// behavior can be useful when reading file formats that distinguish
// individual gzip data streams or mix gzip data streams with other data
// streams. In this mode, when the Reader reaches the end of the data
// stream, Read returns io.EOF. The underlying reader must implement
// io.ByteReader in order to be left positioned just after the gzip stream.
// To start the next stream, call z.Reset(r) followed by
// z.Multistream(false). If there is no next stream, z.Reset(r) will return
// io.EOF.
func (z *Reader) Multistream(ok bool) { z.multistream = ok }

// readHeader reads a member header (from its first byte) and starts its
// DEFLATE stream. With keep false (later members) the strings and Extra
// are not returned.
func (z *Reader) readHeader(keep bool) (hdr Header, err error) {
	b := z.buf[:10]
	if err = z.sr.ReadFull(b); err != nil {
		// io.EOF here means no (further) member; it is returned as it
		// is (the end of a multistream file).
		return hdr, err
	}
	if b[0] != gzipID1 || b[1] != gzipID2 || b[2] != gzipCMDeflate {
		return hdr, ErrHeader
	}
	flg := b[3]
	if t := int64(binary.LittleEndian.Uint32(b[4:8])); t > 0 {
		// MTIME is a uint32; zero means no time (the zero Time).
		hdr.ModTime = time.Unix(t, 0)
	}
	// b[8] is XFL (ignored).
	hdr.OS = b[9]
	digest := crc32.ChecksumIEEE(b)

	if flg&flagExtra != 0 {
		b = z.buf[:2]
		if err = z.readFull(b); err != nil {
			return hdr, err
		}
		digest = crc32.Update(digest, crc32.IEEETable, b)
		xlen := int(binary.LittleEndian.Uint16(b))
		if keep {
			hdr.Extra = make([]byte, xlen)
			if err = z.readFull(hdr.Extra); err != nil {
				return hdr, err
			}
			digest = crc32.Update(digest, crc32.IEEETable, hdr.Extra)
		} else {
			// A later member's Extra is not kept: read it in pieces.
			for xlen > 0 {
				c := z.buf[:min(xlen, len(z.buf))]
				if err = z.readFull(c); err != nil {
					return hdr, err
				}
				digest = crc32.Update(digest, crc32.IEEETable, c)
				xlen -= len(c)
			}
		}
	}

	var s string
	if flg&flagName != 0 {
		if s, digest, err = z.readString(digest, keep); err != nil {
			return hdr, err
		}
		hdr.Name = s
	}
	if flg&flagComment != 0 {
		if s, digest, err = z.readString(digest, keep); err != nil {
			return hdr, err
		}
		hdr.Comment = s
	}

	if flg&flagHdrCrc != 0 {
		b = z.buf[:2]
		if err = z.readFull(b); err != nil {
			return hdr, err
		}
		if binary.LittleEndian.Uint16(b) != uint16(digest) {
			return hdr, ErrHeader
		}
	}

	z.digest, z.size = 0, 0
	z.sr.ResetStream(nil)
	return hdr, nil
}

// readFull reads len(b) header bytes; the end of the input is
// io.ErrUnexpectedEOF.
func (z *Reader) readFull(b []byte) error {
	err := z.sr.ReadFull(b)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// readString reads a NUL-terminated ISO 8859-1 (Latin-1) header string and
// returns it as UTF-8 (only if keep), updating the header CRC. A string of
// more than 511 bytes is ErrHeader, as with the standard library.
func (z *Reader) readString(digest uint32, keep bool) (string, uint32, error) {
	needConv := false
	for i := 0; ; i++ {
		if i >= len(z.buf) {
			return "", digest, ErrHeader
		}
		c, err := z.sr.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", digest, err
		}
		z.buf[i] = c
		if c > 0x7f {
			needConv = true
		}
		if c == 0 {
			digest = crc32.Update(digest, crc32.IEEETable, z.buf[:i+1])
			if !keep {
				return "", digest, nil
			}
			if needConv {
				r := make([]rune, 0, i)
				for _, v := range z.buf[:i] {
					r = append(r, rune(v))
				}
				return string(r), digest, nil
			}
			return string(z.buf[:i]), digest, nil
		}
	}
}

// Read implements io.Reader, reading uncompressed bytes from its
// underlying reader: io.EOF after the last member (with Multistream(false):
// after the first), io.ErrUnexpectedEOF if the input ends inside a member,
// ErrChecksum or ErrHeader for a bad trailer or later header,
// flate.CorruptInputError for invalid compressed data, or the error of the
// underlying reader. Errors are sticky.
func (z *Reader) Read(p []byte) (n int, err error) {
	if z.err != nil {
		return 0, z.err
	}
	for {
		n, z.err = z.sr.Read(p)
		z.digest = crc32.Update(z.digest, crc32.IEEETable, p[:n])
		z.size += uint32(n)
		if z.err != io.EOF {
			if z.err == deflate.ErrBadData {
				z.err = flate.CorruptInputError(z.sr.CorruptOffset())
			}
			return n, z.err
		}

		// End of the member's DEFLATE stream: the trailer.
		t := z.buf[:8]
		if z.err = z.readFull(t); z.err != nil {
			return n, z.err
		}
		if binary.LittleEndian.Uint32(t) != z.digest || binary.LittleEndian.Uint32(t[4:]) != z.size {
			z.err = ErrChecksum
			// The source is still left just after the trailer (a Seek
			// error is not reported over ErrChecksum).
			z.sr.Unread()
			return n, z.err
		}

		// File is ok; check if there is another.
		if !z.multistream {
			z.err = io.EOF
		} else {
			_, z.err = z.readHeader(false)
		}
		if z.err == io.EOF {
			if uerr := z.sr.Unread(); uerr != nil {
				z.err = uerr
			}
			return n, z.err
		}
		if z.err != nil || n > 0 {
			return n, z.err
		}
	}
}

// Close closes the Reader. It does not close the underlying io.Reader. As
// with the standard library, it returns the error of the compressed data
// of the current member if there is one (truncated or invalid DEFLATE
// data, or an error of the underlying reader inside it), else nil, and does
// not stop the Reader.
func (z *Reader) Close() error {
	if z.sr == nil || z.err == ErrChecksum {
		// After ErrChecksum the DEFLATE data were valid (a failed Seek
		// back over the bytes read ahead is not reported here either).
		return nil
	}
	err := z.sr.Err()
	switch err {
	case io.EOF:
		return nil
	case deflate.ErrBadData:
		return flate.CorruptInputError(z.sr.CorruptOffset())
	}
	return err
}
