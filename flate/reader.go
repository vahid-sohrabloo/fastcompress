package flate

import (
	"io"
	"strconv"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflate"
)

// A CorruptInputError reports the presence of corrupt input at a given
// offset: the standard library's compress/flate.CorruptInputError, returned
// by the streaming Reader for invalid data. The offset is an upper bound
// of the position of the invalid data (the input up to the end of the
// invalid block header or match, never past the end of the DEFLATE data),
// counted from the start of the DEFLATE data (for gzip and zlib: after the
// header of the member), not necessarily the standard library's value.
type CorruptInputError int64

// Error returns "flate: corrupt input before offset N".
func (e CorruptInputError) Error() string {
	return "flate: corrupt input before offset " + strconv.FormatInt(int64(e), 10)
}

// An InternalError reports an error in the flate code itself (the standard
// library's compress/flate.InternalError). This package never returns one;
// the type is there for API compatibility.
type InternalError string

// Error returns "flate: internal error: " followed by the message.
func (e InternalError) Error() string { return "flate: internal error: " + string(e) }

// A ReadError reports an error encountered while reading input (the
// standard library's compress/flate.ReadError).
//
// Deprecated: No longer returned (errors of the underlying reader are
// returned as they are, as by the standard library).
type ReadError struct {
	Offset int64 // byte offset where error occurred
	Err    error // error returned by underlying Read
}

// Error returns "flate: read error at offset N: " followed by Err's text.
func (e *ReadError) Error() string {
	return "flate: read error at offset " + strconv.FormatInt(e.Offset, 10) + ": " + e.Err.Error()
}

// A WriteError reports an error encountered while writing output (the
// standard library's compress/flate.WriteError).
//
// Deprecated: No longer returned.
type WriteError struct {
	Offset int64 // byte offset where error occurred
	Err    error // error returned by underlying Write
}

// Error returns "flate: write error at offset N: " followed by Err's text.
func (e *WriteError) Error() string {
	return "flate: write error at offset " + strconv.FormatInt(e.Offset, 10) + ": " + e.Err.Error()
}

// The actual read interface needed by NewReader to read exactly the stream
// and nothing after it (the standard library's compress/flate.Reader). If
// the passed in io.Reader does not also have ReadByte, the reader may read
// more data than necessary.
type Reader interface {
	io.Reader
	io.ByteReader
}

// Resetter resets a ReadCloser returned by NewReader or NewReaderDict to
// switch to a new underlying Reader, discarding the reader's state (the
// standard library's compress/flate.Resetter). This permits reusing a
// ReadCloser instead of allocating a new one.
type Resetter interface {
	// Reset discards any buffered data and resets the Resetter as if it
	// was newly initialized with the given reader.
	Reset(r io.Reader, dict []byte) error
}

// NewReader returns a new ReadCloser that decompresses the raw DEFLATE
// stream read from r: a drop-in replacement for the standard library's
// compress/flate.NewReader, decoding with libdeflate's decoder (see the
// package documentation, "Streaming Reader"). The ReadCloser also
// implements Resetter. It is the caller's responsibility to call Close
// when done (Close releases nothing; it reports the stream's error).
//
// If r also implements io.ByteReader, no byte after the end of the stream
// is consumed from r once Read has returned io.EOF (a bytes.Reader or
// strings.Reader is read ahead and moved back then, so it is ahead before
// that and after an error). Otherwise r may be read past the end of the
// stream. The reader holds about 33 KiB, growing to about 173 KiB once the
// stream produces more than 16 KiB of output. If r keeps returning no data
// and no error, Read returns io.ErrNoProgress (as the standard library's).
func NewReader(r io.Reader) io.ReadCloser {
	return NewReaderDict(r, nil)
}

// NewReaderDict is like NewReader but initializes the reader with a preset
// dictionary (only its last 32 KiB matter): the stream must have been
// written with the same dictionary (NewWriterDict). dict is copied.
func NewReaderDict(r io.Reader, dict []byte) io.ReadCloser {
	z := new(decompressor)
	z.Reset(r, dict)
	return z
}

// decompressor is the ReadCloser of NewReader.
type decompressor struct {
	sr    deflate.StreamReader
	ended bool // the end of the stream was reached (and the source repositioned)
}

// Reset makes z read a new stream from r with the preset dictionary dict,
// keeping its buffers. It always returns nil.
func (z *decompressor) Reset(r io.Reader, dict []byte) error {
	z.sr.Reset(r, dict)
	z.ended = false
	return nil
}

// Read reads decompressed data: io.EOF at the end of the stream,
// io.ErrUnexpectedEOF if r ends first, CorruptInputError for invalid data,
// or the error of r. Errors are sticky.
func (z *decompressor) Read(p []byte) (int, error) {
	n, err := z.sr.Read(p)
	if err == io.EOF && !z.ended {
		z.ended = true
		if uerr := z.sr.Unread(); uerr != nil {
			return n, uerr
		}
	}
	return n, z.mapErr(err)
}

// Close returns the error of the stream (nil if it was read to its end or
// is not finished; as the standard library's, it does not stop the reader).
func (z *decompressor) Close() error {
	if err := z.sr.Err(); err != io.EOF {
		return z.mapErr(err)
	}
	return nil
}

// mapErr turns deflate.ErrBadData into a CorruptInputError.
func (z *decompressor) mapErr(err error) error {
	if err == deflate.ErrBadData {
		return CorruptInputError(z.sr.CorruptOffset())
	}
	return err
}
