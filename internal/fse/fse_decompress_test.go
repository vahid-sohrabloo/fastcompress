package fse

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// randomNorm returns a normalized distribution over maxSymbol+1 symbols at
// tableLog, built by the encoder from random counts (some zero).
func randomNorm(t *testing.T, r *rand.Rand, maxSymbol uint, tableLog *uint) []int16 {
	t.Helper()
	count := make([]uint32, maxSymbol+1)
	total := 0
	for i := range count {
		if r.IntN(4) != 0 {
			count[i] = uint32(r.IntN(1000))
		}
		total += int(count[i])
	}
	count[maxSymbol] += 1 // keep maxSymbol present
	count[0] += 1
	total += 2
	*tableLog = OptimalTableLog(*tableLog, total, maxSymbol)
	norm := make([]int16, maxSymbol+1)
	if _, err := NormalizeCount(norm, *tableLog, count, total, maxSymbol, total >= 2048); err != nil {
		t.Fatal(err)
	}
	return norm
}

// TestReadNCountRoundTrip checks ReadNCount against WriteNCount: the
// normalized counts, maxSymbol, tableLog and the header size, also for
// headers shorter than 8 bytes and with trailing data.
func TestReadNCountRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 2000; i++ {
		maxSymbol := uint(r.IntN(255) + 1)
		tableLog := uint(MinTableLog + r.IntN(MaxTableLog-MinTableLog+1))
		norm := randomNorm(t, r, maxSymbol, &tableLog)
		buf := make([]byte, NCountWriteBound(maxSymbol, tableLog))
		n, err := WriteNCount(buf, norm, maxSymbol, tableLog)
		if err != nil {
			t.Fatal(err)
		}
		hdr := append(buf[:n:n], bytes.Repeat([]byte{0xA5}, r.IntN(3)*7)...)
		var got [256]int16
		gotMax := uint(255)
		gotLog, size, err := ReadNCount(got[:], &gotMax, hdr)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if size != n || gotMax != maxSymbol || gotLog != tableLog {
			t.Fatalf("case %d: size %d max %d log %d, want %d %d %d", i, size, gotMax, gotLog, n, maxSymbol, tableLog)
		}
		for s := range got {
			want := int16(0)
			if uint(s) <= maxSymbol {
				want = norm[s]
			}
			if got[s] != want {
				t.Fatalf("case %d: norm[%d] = %d, want %d", i, s, got[s], want)
			}
		}
		// A too small maxSymbolValue is an error.
		small := maxSymbol - 1
		if _, _, err := ReadNCount(got[:], &small, hdr); err == nil && norm[maxSymbol] != 0 {
			t.Fatalf("case %d: maxSymbol %d accepted", i, maxSymbol-1)
		}
	}
	// Truncated headers fail.
	var got [256]int16
	m := uint(255)
	if _, _, err := ReadNCount(got[:], &m, nil); err == nil {
		t.Fatal("empty header accepted")
	}
}

// TestDecompressWkspRoundTrip decodes what Compress2 encodes.
func TestDecompressWkspRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 500; i++ {
		n := 2 + r.IntN(300)
		alpha := 2 + r.IntN(16)
		src := make([]byte, n)
		for j := range src {
			// skewed distribution so FSE compresses
			src[j] = byte(min(r.IntN(alpha), r.IntN(alpha)))
		}
		dst := make([]byte, n+512)
		c, err := Compress2(dst, src, 255, 6)
		if err != nil {
			t.Fatal(err)
		}
		if c <= 1 {
			continue // not compressible or RLE
		}
		out := make([]byte, n+16)
		d, err := DecompressWksp(out, dst[:c], 6)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !bytes.Equal(out[:d], src) {
			t.Fatalf("case %d: round trip mismatch", i)
		}
	}
}
