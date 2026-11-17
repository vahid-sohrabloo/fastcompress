package lz4

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"
)

func randText(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 3))
	words := []string{"lz4 ", "block ", "frame ", "match ", "literal ", "\n", "offset ", "the "}
	b := make([]byte, 0, n+10)
	for len(b) < n {
		if r.IntN(20) == 0 {
			b = append(b, byte(r.Uint32()))
			continue
		}
		b = append(b, words[r.IntN(len(words))]...)
	}
	return b[:n]
}

func TestRoundTripBlocks(t *testing.T) {
	for _, n := range []int{0, 1, 12, 13, 100, 65547, 70000, 300000} {
		in := randText(uint64(n), n)
		for _, a := range []int{-1, 1, 2, 20, 1 << 20} {
			dst := make([]byte, CompressBound(n))
			c := dst[:CompressFast(dst, in, a)]
			out := make([]byte, n)
			k, err := DecompressSafe(out, c)
			if err != nil || k != n || !bytes.Equal(out, in) {
				t.Fatalf("fast n=%d a=%d: %v", n, a, err)
			}
		}
		for level := 0; level <= 13; level++ {
			dst := make([]byte, CompressBound(n))
			c := dst[:CompressHC(dst, in, level)]
			out := make([]byte, n)
			k, err := DecompressSafe(out, c)
			if err != nil || k != n || !bytes.Equal(out, in) {
				t.Fatalf("hc n=%d l=%d: %v", n, level, err)
			}
		}
	}
}

func TestRoundTripFrames(t *testing.T) {
	in := randText(9, 1<<20+333)
	dict := randText(10, 50000)
	for _, level := range []int{-3, 1, 2, 6, 10} {
		for _, p := range []Preferences{
			{CompressionLevel: level},
			{CompressionLevel: level, FrameInfo: FrameInfo{BlockIndependent: true, BlockSizeID: Block256KB, ContentChecksum: true}},
			{CompressionLevel: level, AutoFlush: true, FrameInfo: FrameInfo{BlockChecksum: true, BlockSizeID: Block4MB}},
		} {
			fr, err := AppendFrame(nil, in, &p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := AppendDecompressFrame(nil, fr, nil)
			if err != nil || !bytes.Equal(got, in) {
				t.Fatalf("frame l%d: %v", level, err)
			}
			var buf bytes.Buffer
			w := NewWriter(&buf, WithPreferences(p), WithDict(dict))
			for i := 0; i < len(in); i += 77777 {
				w.Write(in[i:min(i+77777, len(in))])
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r := NewReaderDict(&buf, dict)
			got, err = io.ReadAll(r)
			if err != nil || !bytes.Equal(got, in) {
				t.Fatalf("writer/reader l%d: %v", level, err)
			}
		}
	}
}
