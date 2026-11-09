package gzip_test

import (
	"bytes"
	stdgzip "compress/gzip"
	"encoding/binary"
	"hash/crc32"
	"io"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Format-specific parts of the shared tests in api_test.go (which is the same
// file in packages flate, gzip and zlib apart from the package clause and the
// import of the package under test).

// ldFormat is the ldref -format of this package.
const ldFormat = creftest.LDGzip

// overhead is the container overhead added to the raw DEFLATE bound.
const overhead = 18

// wrapRaw turns libdeflate's raw DEFLATE output for data at level (0..12)
// into what this package's Compress must return: RFC 1952 header (FLG 0,
// MTIME 0, XFL 4 below level 2 and 2 from level 8, OS 255), raw, CRC-32 and
// ISIZE, both little-endian (libdeflate_gzip_compress).
func wrapRaw(level int, raw, data []byte) []byte {
	xfl := byte(0)
	if level < 2 {
		xfl = 4
	} else if level >= 8 {
		xfl = 2
	}
	out := []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, xfl, 255}
	out = append(out, raw...)
	out = binary.LittleEndian.AppendUint32(out, crc32.ChecksumIEEE(data))
	return binary.LittleEndian.AppendUint32(out, uint32(len(data)))
}

// stdLevels are the standard library writer's levels.
var stdLevels = []int{stdgzip.HuffmanOnly, stdgzip.DefaultCompression, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

// stdEncode compresses data with the standard library at level.
func stdEncode(t testing.TB, level int, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := stdgzip.NewWriterLevel(&b, level)
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

// stdDecode decompresses b with the standard library reader (single member).
func stdDecode(b []byte) ([]byte, error) {
	r, err := stdgzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r.Multistream(false)
	defer r.Close()
	return io.ReadAll(r)
}
