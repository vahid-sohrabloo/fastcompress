package creftest

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

func ldInputs() map[string][]byte {
	r := rand.New(rand.NewPCG(1, 2))
	rnd := make([]byte, 100*kib)
	for i := range rnd {
		rnd[i] = byte(r.Uint32())
	}
	return map[string][]byte{
		"empty":      {},
		"1byte":      {'x'},
		"text100":    bytes.Repeat([]byte("hello deflate "), 8)[:100],
		"rep300k":    bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 300*kib/45+1)[:300*kib],
		"random100k": rnd,
	}
}

func ldStd(format string, z []byte) ([]byte, error) {
	var r io.Reader
	switch format {
	case LDRaw:
		r = flate.NewReader(bytes.NewReader(z))
	case LDGzip:
		g, err := gzip.NewReader(bytes.NewReader(z))
		if err != nil {
			return nil, err
		}
		r = g
	default:
		g, err := zlib.NewReader(bytes.NewReader(z))
		if err != nil {
			return nil, err
		}
		r = g
	}
	return io.ReadAll(r)
}

func TestLDRoundTrip(t *testing.T) {
	LDBin(t)
	for name, in := range ldInputs() {
		for _, format := range []string{LDRaw, LDGzip, LDZlib} {
			for level := 0; level <= 12; level++ {
				t.Run(fmt.Sprintf("%s/%s/l%d", name, format, level), func(t *testing.T) {
					z := LDCompress(t, in, level, format)
					got, err := ldStd(format, z)
					if err != nil || !bytes.Equal(got, in) {
						t.Fatalf("stdlib decode: err=%v equal=%v", err, bytes.Equal(got, in))
					}
					res, nIn, nOut, out := LDDecompressEx(t, z, len(in), format)
					if res != LDOK || nIn != len(z) || nOut != len(in) || !bytes.Equal(out, in) {
						t.Fatalf("ldref decode: result=%d in=%d/%d out=%d/%d", res, nIn, len(z), nOut, len(in))
					}
				})
			}
		}
	}
}

func TestLDDeterministic(t *testing.T) {
	LDBin(t)
	for name, in := range ldInputs() {
		for _, level := range []int{1, 6, 12} {
			a := LDCompress(t, in, level, LDRaw)
			b := LDCompress(t, in, level, LDRaw)
			if !bytes.Equal(a, b) {
				t.Errorf("%s level %d: output differs between runs", name, level)
			}
		}
	}
}

func TestLDCorrupt(t *testing.T) {
	LDBin(t)
	in := ldInputs()["rep300k"]
	for _, format := range []string{LDRaw, LDGzip, LDZlib} {
		z := LDCompress(t, in, 6, format)
		// Truncated: never ok.
		for _, cut := range []int{0, 1, len(z) / 2, len(z) - 1} {
			if res, _, _, _ := LDDecompressEx(t, z[:cut], len(in), format); res == LDOK {
				t.Errorf("%s truncated to %d: result ok", format, cut)
			}
		}
		// Output buffer too small.
		if res, _, _, _ := LDDecompressEx(t, z, len(in)-1, format); res != LDInsufficientSpace {
			t.Errorf("%s small buffer: result %d, want %d", format, res, LDInsufficientSpace)
		}
		// Flipped bytes: any result is fine as long as ldref does not crash
		// (run fails the test on a non-zero exit).
		for _, pos := range []int{0, 2, len(z) / 3, len(z) - 5, len(z) - 1} {
			bad := bytes.Clone(z)
			bad[pos] ^= 0xff
			LDDecompressEx(t, bad, len(in), format)
		}
	}
	// Reserved block type 3 is bad data.
	if res, _, _, _ := LDDecompressEx(t, []byte{0x07, 0, 0, 0}, 16, LDRaw); res != LDBadData {
		t.Errorf("block type 3: result %d, want %d", res, LDBadData)
	}
}

func TestLDVersion(t *testing.T) {
	if v := LDVersion(t); !strings.HasPrefix(v, "libdeflate v1.26") {
		t.Fatalf("version output %q", v)
	}
}
