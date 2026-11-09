package zlib_test

import (
	"bytes"
	stdzlib "compress/zlib"
	"encoding/binary"
	"hash/adler32"
	"io"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Format-specific parts of the shared tests in api_test.go (which is the same
// file in packages flate, gzip and zlib apart from the package clause and the
// import of the package under test).

// ldFormat is the ldref -format of this package.
const ldFormat = creftest.LDZlib

// overhead is the container overhead added to the raw DEFLATE bound.
const overhead = 6

// wrapRaw turns libdeflate's raw DEFLATE output for data at level (0..12)
// into what this package's Compress must return: the RFC 1950 header
// (CM 8, CINFO 7, FLEVEL 0 below level 2, 1 below 6, 2 below 8, else 3,
// FCHECK made by libdeflate_zlib_compress's `hdr |= 31 - hdr % 31`), raw,
// then the big-endian Adler-32.
func wrapRaw(level int, raw, data []byte) []byte {
	hint := uint16(3)
	switch {
	case level < 2:
		hint = 0
	case level < 6:
		hint = 1
	case level < 8:
		hint = 2
	}
	hdr := uint16(8<<8|7<<12) | hint<<6
	hdr |= 31 - hdr%31
	out := binary.BigEndian.AppendUint16(nil, hdr)
	out = append(out, raw...)
	return binary.BigEndian.AppendUint32(out, adler32.Checksum(data))
}

// stdLevels are the standard library writer's levels.
var stdLevels = []int{stdzlib.HuffmanOnly, stdzlib.DefaultCompression, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

// stdEncode compresses data with the standard library at level.
func stdEncode(t testing.TB, level int, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := stdzlib.NewWriterLevel(&b, level)
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
	r, err := stdzlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
