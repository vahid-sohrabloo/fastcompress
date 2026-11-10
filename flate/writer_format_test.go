package flate_test

import (
	stdflate "compress/flate"
	"io"

	pkg "github.com/vahid-sohrabloo/fastzstd/flate"
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
	z, err := pkg.NewWriter(w, level)
	if err != nil {
		return nil, err
	}
	return z, nil
}

// stdReader returns the standard library's reader of this format.
func stdReader(r io.Reader) (io.Reader, error) { return stdflate.NewReader(r), nil }

// stdWriter returns the standard library's writer of this format.
func stdWriter(w io.Writer, level int) (streamWriter, error) {
	z, err := stdflate.NewWriter(w, level)
	if err != nil {
		return nil, err
	}
	return z, nil
}
