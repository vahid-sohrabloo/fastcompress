package zlib_test

import (
	"bytes"
	stdzlib "compress/zlib"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/zlib"
)

// zlib-specific behaviour (the shared tests are in api_test.go).

// TestPresetDictionaryRejected: a stream that needs a preset dictionary
// (FDICT) is ErrBadData in every decompression function, as in libdeflate.
func TestPresetDictionaryRejected(t *testing.T) {
	data := bytes.Repeat([]byte("dictionary words "), 20)
	var b bytes.Buffer
	w, err := stdzlib.NewWriterLevelDict(&b, 6, []byte("dictionary words"))
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	w.Close()
	s := b.Bytes()
	if s[1]&0x20 == 0 {
		t.Fatal("standard library stream without FDICT")
	}
	d := zlib.NewDecompressor()
	if _, err := d.Decompress(make([]byte, len(data)), s); err != zlib.ErrBadData {
		t.Fatalf("Decompress: %v", err)
	}
	if _, _, err := d.DecompressEx(make([]byte, 2*len(data)), s); err != zlib.ErrBadData {
		t.Fatalf("DecompressEx: %v", err)
	}
	if out, err := d.AppendDecompress(nil, s, 0); err != zlib.ErrBadData || len(out) != 0 {
		t.Fatalf("AppendDecompress: %v", err)
	}
	counts := runDiff(t, d, []ldReq{{"fdict", s, len(data), true}, {"fdict", s, 2 * len(data), false}})
	if counts[creftest.LDBadData] != 2 {
		t.Fatalf("libdeflate result codes %v", counts)
	}
}

// TestOurHeader: the header bytes per level (libdeflate's FLEVEL hints).
func TestOurHeader(t *testing.T) {
	want := map[int]uint16{-1: 0x789C, 0: 0x7801, 1: 0x7801, 2: 0x785E, 5: 0x785E, 6: 0x789C, 7: 0x789C,
		8: 0x78DA, 9: 0x78DA, 12: 0x78DA}
	for level, h := range want {
		s := compress(t, newCompressor(t, level), []byte("header"))
		if got := binary.BigEndian.Uint16(s); got != h {
			t.Errorf("level %d: header %#04x, want %#04x", level, got, h)
		}
	}
}

// TestAdler32Checked: any change of the trailer is ErrBadData; libdeflate
// agrees.
func TestAdler32Checked(t *testing.T) {
	data := []byte("adler-32 trailer check, adler-32 trailer check")
	s := compress(t, newCompressor(t, 6), data)
	d := zlib.NewDecompressor()
	var reqs []ldReq
	for off := len(s) - 4; off < len(s); off++ {
		m := append([]byte(nil), s...)
		m[off] ^= 0x80
		if _, err := d.Decompress(make([]byte, len(data)), m); !errors.Is(err, zlib.ErrBadData) {
			t.Fatalf("trailer byte %d flipped: %v", off, err)
		}
		reqs = append(reqs, ldReq{"adler", m, len(data), true})
	}
	if counts := runDiff(t, d, reqs); counts[creftest.LDBadData] != 4 {
		t.Fatalf("libdeflate result codes %v", counts)
	}
}
