package zlib_test

import (
	stdzlib "compress/zlib"
	"io"

	pkg "github.com/vahid-sohrabloo/fastcompress/zlib"
)

// Format-specific parts of the shared Writer tests in writer_test.go (the
// same file in packages flate, gzip and zlib apart from the package clause
// and the import of the package under test).

// streamWriter is the method set the shared tests use.
type streamWriter interface {
	io.WriteCloser
	Flush() error
	Reset(w io.Writer)
}

// newWriter returns this package's Writer at level.
func newWriter(w io.Writer, level int) (streamWriter, error) {
	z, err := pkg.NewWriterLevel(w, level)
	if err != nil {
		return nil, err
	}
	return z, nil
}

// stdReader returns the standard library's reader of this format.
func stdReader(r io.Reader) (io.Reader, error) { return stdzlib.NewReader(r) }

// stdWriter returns the standard library's writer of this format.
func stdWriter(w io.Writer, level int) (streamWriter, error) {
	z, err := stdzlib.NewWriterLevel(w, level)
	if err != nil {
		return nil, err
	}
	return z, nil
}
