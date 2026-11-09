package deflate

import (
	"bytes"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// fuzzLevel1Input expands fuzz bytes into a level 1 input:
//
//	data[0] bit 0: 0 = the rest as is; 1 = the rest repeated (and each
//	        copy's first byte bumped) up to 70000 + 4096*data[1] bytes, so
//	        that the matchfinder window slides and blocks end
//	data[0] bits 1-2: output buffer: 0-1 bound, 2 exact, 3 one byte short
func fuzzLevel1Input(data []byte) (in []byte, capMode int) {
	if len(data) < 2 {
		return data, 0
	}
	capMode = int(data[0]>>1) & 3
	rest := data[2:]
	if data[0]&1 == 0 || len(rest) == 0 {
		return rest, capMode
	}
	n := 70000 + 4096*int(data[1])
	in = make([]byte, 0, n+len(rest))
	for k := 0; len(in) < n; k++ {
		start := len(in)
		in = append(in, rest...)
		in[start] += byte(k)
	}
	return in[:n], capMode
}

// FuzzCompressLevel1 compares Compress(level 1) with libdeflate, including
// the exact and one-byte-short output buffers, and round-trips the output.
// It skips without ldref locally and fails under CI (creftest.LDBin).
func FuzzCompressLevel1(f *testing.F) {
	f.Add([]byte{0, 0})
	f.Add(append([]byte{0, 0}, bytes.Repeat([]byte("abcdabcxabcd"), 20)...))
	f.Add(append([]byte{1, 0}, []byte("hello, world")...))
	f.Add(append([]byte{3, 9}, structuredInput(1, 3000)...))
	f.Add(append([]byte{4, 0}, structuredInput(2, 70535)...))
	f.Add(append([]byte{6, 0}, structuredInput(3, 1000)...))
	f.Add(append([]byte{7, 200}, []byte("ab")...))
	c := mustCompressor(f, 1)
	f.Fuzz(func(t *testing.T, data []byte) {
		creftest.LDBin(t)
		in, capMode := fuzzLevel1Input(data)
		want := ldCompressBatch(t, []ldBatchRec{{1, -1, in}})[0]
		got := compressLevel1(t, c, in)
		if !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: go %d, libdeflate %d; %s; first differing byte %d", len(in), len(got), len(want),
				firstBlockDiff(got, want), firstByteDiff(got, want))
		}
		switch capMode {
		case 2:
			ld := ldCompressBatch(t, []ldBatchRec{{1, len(want), in}})[0]
			dst := make([]byte, len(want))
			if n := c.Compress(dst, in); n != len(want) || !bytes.Equal(dst, want) || !bytes.Equal(ld, want) {
				t.Fatalf("exact buffer: go %d, libdeflate %d, want %d", n, len(ld), len(want))
			}
		case 3:
			ld := ldCompressBatch(t, []ldBatchRec{{1, len(want) - 1, in}})[0]
			if n := c.Compress(make([]byte, len(want)-1), in); n != 0 || ld != nil {
				t.Fatalf("one byte short: go %d, libdeflate %d", n, len(ld))
			}
		}
		if err := checkDecode(t, got, in); err != nil {
			t.Fatal(err)
		}
	})
}
