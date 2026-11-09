package deflate

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// diffCompress compresses src at level with a CompressBound-sized buffer
// and compares with ldref compress (which uses libdeflate's bound too),
// then checks the exact-size and one-byte-short buffers.
func diffCompress(t *testing.T, c *Compressor, name string, src []byte) {
	t.Helper()
	want := creftest.LDCompress(t, src, c.Level(), creftest.LDRaw)
	dst := make([]byte, c.CompressBound(len(src)))
	n := c.Compress(dst, src)
	if !bytes.Equal(dst[:n], want) {
		t.Fatalf("%s level %d: go %d bytes, libdeflate %d bytes, equal=%v", name, c.Level(), n, len(want),
			bytes.Equal(dst[:n], want))
	}
	if m := c.Compress(dst[:n], src); m != n {
		t.Fatalf("%s level %d: exact-size buffer gave %d, want %d", name, c.Level(), m, n)
	}
	if m := c.Compress(dst[:n-1], src); m != 0 {
		t.Fatalf("%s level %d: one byte short gave %d, want 0", name, c.Level(), m)
	}
}

// Level 0 vs libdeflate on the whole generated corpus and Silesia.
func TestCompressLevel0MatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	c := mustCompressor(t, 0)
	files, total := 0, 0
	for _, cs := range deflatetest.Cases(true) {
		diffCompress(t, c, cs.Name, cs.Data)
		files, total = files+1, total+len(cs.Data)
	}
	for _, f := range deflatetest.Silesia(t) {
		if testing.Short() && f.Name != "xml" {
			continue
		}
		data := f.Load(t)
		diffCompress(t, c, f.Name, data)
		files, total = files+1, total+len(data)
	}
	t.Logf("%d inputs, %d bytes", files, total)
}

// Every level on every passthrough-sized input: the corpus' small cases
// (the 0..70 text and random sweeps) and all sizes 0..55-4*level of a few
// more patterns.
func TestCompressPassthroughMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	cases := deflatetest.Cases(true)
	for n := 0; n <= 55; n++ {
		cases = append(cases,
			deflatetest.Case{Name: fmt.Sprintf("zero/%d", n), Data: make([]byte, n)},
			deflatetest.Case{Name: fmt.Sprintf("ramp/%d", n), Data: randData(uint64(n)+1000, n)})
	}
	for l := 1; l <= 12; l++ {
		c := mustCompressor(t, l)
		count := 0
		for _, cs := range cases {
			if len(cs.Data) > 55-4*l {
				continue
			}
			diffCompress(t, c, cs.Name, cs.Data)
			count++
		}
		if count < 2*(56-4*l) {
			t.Fatalf("level %d: only %d passthrough inputs", l, count)
		}
	}
}

func TestCompressBoundMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	var sizes []uint64
	for n := range uint64(20001) {
		sizes = append(sizes, n)
	}
	for k := uint64(1); k < 1000; k++ {
		sizes = append(sizes, 5000*k-1, 5000*k, 5000*k+1, 65535*k, 65535*k+1)
	}
	for s := 20; s < 63; s++ {
		sizes = append(sizes, 1<<s-1, 1<<s, 1<<s+1)
	}
	sizes = append(sizes, math.MaxInt64-6000, math.MaxInt64/1001*1000)
	var in []byte
	for _, n := range sizes {
		in = binary.LittleEndian.AppendUint64(in, n)
	}
	compared := 0
	for l := 0; l <= 12; l++ {
		c := mustCompressor(t, l)
		out := creftest.LDRun(t, in, "compress-bound", "-l", strconv.Itoa(l))
		if len(out) != 8*len(sizes) {
			t.Fatalf("ldref compress-bound: %d bytes", len(out))
		}
		for i, n := range sizes {
			want := binary.LittleEndian.Uint64(out[8*i:])
			if n > math.MaxInt || want > math.MaxInt {
				continue // beyond Go's int (386, or saturated)
			}
			if got := c.CompressBound(int(n)); uint64(got) != want {
				t.Fatalf("level %d: CompressBound(%d) = %d, libdeflate %d", l, n, got, want)
			}
			compared++
		}
	}
	t.Logf("%d bounds compared", compared)
}

// The level table against what libdeflate_alloc_compressor sets.
func TestLevelParamsMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	impls := []string{"none", "fastest", "greedy", "greedy", "greedy", "lazy", "lazy", "lazy", "lazy2", "lazy2",
		"near_optimal", "near_optimal", "near_optimal"}
	for l := 0; l <= 12; l++ {
		c := mustCompressor(t, l)
		pt := strconv.Itoa(c.maxPassthroughSize)
		if l == 0 {
			pt = strconv.FormatUint(math.MaxUint64, 10) // C: SIZE_MAX (64-bit ldref)
		}
		s := fmt.Sprintf("level=%d passthrough=%s impl=%s", c.Level(), pt, impls[l])
		if l >= 2 {
			s += fmt.Sprintf(" depth=%d", c.maxSearchDepth)
		}
		if l >= 1 {
			s += fmt.Sprintf(" nice=%d", c.niceMatchLength)
		}
		if l >= 10 {
			var sum uint64
			for off := 1; off <= deflateMaxMatchOffset; off++ {
				sum += uint64(c.offsetSlotFull[off]) * uint64(off)
			}
			s += fmt.Sprintf(" passes=%d minimp=%d minbits=%d maxstatic=%d slotfull=%d", c.pn.maxOptimPasses,
				c.pn.minImprovementToContinue, c.pn.minBitsToUseNonfinalPath, c.pn.maxLenToOptimizeStaticBlock, sum)
		}
		want := strings.TrimSuffix(string(creftest.LDRun(t, nil, "dump-params", "-l", strconv.Itoa(l))), "\n")
		if s != want {
			t.Fatalf("level %d:\n go %s\n  C %s", l, s, want)
		}
	}
}
