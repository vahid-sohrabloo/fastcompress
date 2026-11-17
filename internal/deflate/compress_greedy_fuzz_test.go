package deflate

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// FuzzCompressGreedy compares Compress at levels 2..4 with libdeflate,
// including the exact and one-byte-short output buffers, and round-trips
// the output. The input is expanded by fuzzLevel1Input (data[0] bits 0-2:
// as is or repeated to 70000.. bytes, buffer mode); data[0] bits 3-4 pick
// the level (0, 3: 2; 1: 3; 2: 4). It skips without ldref locally and
// fails under CI (creftest.LDBin).
func FuzzCompressGreedy(f *testing.F) {
	f.Add([]byte{0, 0})
	f.Add(append([]byte{8, 0}, bytes.Repeat([]byte("abcdabcxabcd"), 20)...))
	f.Add(append([]byte{17, 0}, []byte("hello, world")...))
	f.Add(append([]byte{3, 9}, structuredInput(1, 3000)...))
	f.Add(append([]byte{12, 0}, structuredInput(2, 70535)...))
	f.Add(append([]byte{22, 0}, structuredInput(3, 1000)...))
	f.Add(append([]byte{15, 200}, []byte("ab")...))
	f.Add(append([]byte{17, 70}, hcRecords(rand.New(rand.NewPCG(1, 1)), 6000, 20)...))
	cs := [3]*Compressor{mustCompressor(f, 2), mustCompressor(f, 3), mustCompressor(f, 4)}
	f.Fuzz(func(t *testing.T, data []byte) {
		creftest.LDBin(t)
		in, capMode := fuzzLevel1Input(data)
		li := 0
		if len(data) > 0 {
			li = [4]int{0, 1, 2, 0}[data[0]>>3&3]
		}
		level, c := 2+li, cs[li]
		want := ldCompressBatch(t, []ldBatchRec{{level, -1, in}})[0]
		got := compressAt(t, c, in)
		if !bytes.Equal(got, want) {
			t.Fatalf("level %d, %d bytes: go %d, libdeflate %d; %s; first differing byte %d", level, len(in),
				len(got), len(want), firstBlockDiff(got, want), firstByteDiff(got, want))
		}
		switch capMode {
		case 2:
			ld := ldCompressBatch(t, []ldBatchRec{{level, len(want), in}})[0]
			dst := make([]byte, len(want))
			if n := c.Compress(dst, in); n != len(want) || !bytes.Equal(dst, want) || !bytes.Equal(ld, want) {
				t.Fatalf("exact buffer: go %d, libdeflate %d, want %d", n, len(ld), len(want))
			}
		case 3:
			ld := ldCompressBatch(t, []ldBatchRec{{level, len(want) - 1, in}})[0]
			if n := c.Compress(make([]byte, len(want)-1), in); n != 0 || ld != nil {
				t.Fatalf("one byte short: go %d, libdeflate %d", n, len(ld))
			}
		}
		if err := checkDecode(t, got, in); err != nil {
			t.Fatal(err)
		}
	})
}
