package flate_test

import (
	stdflate "compress/flate"
	"io"

	pkg "github.com/vahid-sohrabloo/fastcompress/flate"
)

// Format-specific parts of the shared Reader tests in reader_test.go (the
// same file in packages flate, gzip and zlib apart from the package clause
// and the import of the package under test).

// bodyStart is the size of the fixed header before the DEFLATE data in
// Compressor output (mutations start after it).
const bodyStart = 0

// trailerSize is the size of the trailer after the DEFLATE data.
const trailerSize = 0

// trailingIgnored: bytes after a stream are not read (no further members).
const trailingIgnored = true

// newReader returns this package's streaming reader.
func newReader(r io.Reader) (io.ReadCloser, error) { return pkg.NewReader(r), nil }

// stdNewReader returns the standard library's reader of this format.
func stdNewReader(r io.Reader) (io.ReadCloser, error) { return stdflate.NewReader(r), nil }

// resetReader resets a reader of newReader to read r.
func resetReader(z io.ReadCloser, r io.Reader) error { return z.(pkg.Resetter).Reset(r, nil) }

// stdResetReader resets a reader of stdNewReader to read r.
func stdResetReader(z io.ReadCloser, r io.Reader) error { return z.(stdflate.Resetter).Reset(r, nil) }

// singleStream makes z stop after one stream (gzip: Multistream(false)).
func singleStream(io.ReadCloser) {}

// wholeComparable reports whether the whole-buffer decoder and the Reader
// treat m's container alike: always, for this format.
func wholeComparable([]byte) bool { return true }
