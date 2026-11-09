package gzip_test

import (
	stdgzip "compress/gzip"
	"io"

	pkg "github.com/vahid-sohrabloo/fastzstd/gzip"
)

// Format-specific parts of the shared Reader tests in reader_test.go (the
// same file in packages flate, gzip and zlib apart from the package clause
// and the import of the package under test).

// bodyStart is the size of the fixed header before the DEFLATE data in
// Compressor output (mutations start after it).
const bodyStart = 10

// trailerSize is the size of the trailer after the DEFLATE data.
const trailerSize = 8

// trailingIgnored: bytes after a stream are not read (no further members).
// A gzip Reader reads them as the next member.
const trailingIgnored = false

// newReader returns this package's streaming reader.
func newReader(r io.Reader) (io.ReadCloser, error) {
	z, err := pkg.NewReader(r)
	if err != nil {
		return nil, err
	}
	return z, nil
}

// stdNewReader returns the standard library's reader of this format.
func stdNewReader(r io.Reader) (io.ReadCloser, error) {
	z, err := stdgzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	return z, nil
}

// resetReader resets a reader of newReader to read r.
func resetReader(z io.ReadCloser, r io.Reader) error { return z.(*pkg.Reader).Reset(r) }

// stdResetReader resets a reader of stdNewReader to read r.
func stdResetReader(z io.ReadCloser, r io.Reader) error { return z.(*stdgzip.Reader).Reset(r) }

// singleStream makes z stop after one stream (gzip: Multistream(false)).
func singleStream(z io.ReadCloser) { z.(*pkg.Reader).Multistream(false) }

// wholeComparable reports whether the whole-buffer decoder (libdeflate)
// and the Reader (the standard library's semantics) treat m's header alike:
// libdeflate rejects reserved FLG bits, which the Reader ignores, and skips
// FHCRC, which the Reader checks.
func wholeComparable(m []byte) bool { return len(m) < 4 || m[3]&0xe2 == 0 }
