package flate_test

import (
	"bytes"
	stdflate "compress/flate"
	"io"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Format-specific parts of the shared tests in api_test.go (which is the same
// file in packages flate, gzip and zlib apart from the package clause and the
// import of the package under test).

// ldFormat is the ldref -format of this package.
const ldFormat = creftest.LDRaw

// overhead is the container overhead added to the raw DEFLATE bound.
const overhead = 0

// wrapRaw turns libdeflate's raw DEFLATE output for data at level (0..12)
// into what this package's Compress must return.
func wrapRaw(level int, raw, data []byte) []byte { return raw }

// stdLevels are the standard library writer's levels.
var stdLevels = []int{stdflate.HuffmanOnly, stdflate.DefaultCompression, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

// stdEncode compresses data with the standard library at level.
func stdEncode(t testing.TB, level int, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := stdflate.NewWriter(&b, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// stdDecode decompresses b with the standard library reader.
func stdDecode(b []byte) ([]byte, error) {
	r := stdflate.NewReader(bytes.NewReader(b))
	defer r.Close()
	return io.ReadAll(r)
}
