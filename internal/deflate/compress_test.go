package deflate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

func mustCompressor(t testing.TB, level int) *Compressor {
	t.Helper()
	c, err := NewCompressor(level)
	if err != nil {
		t.Fatalf("NewCompressor(%d): %v", level, err)
	}
	return c
}

func randData(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 7))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// levelParams is the level table of libdeflate_alloc_compressor_ex as read
// from the C source (TestLevelParamsMatchLibdeflate checks it against C).
var levelParams = [13]struct {
	passthrough int
	depth, nice uint32
	pn          nearOptimalParams
}{
	{math.MaxInt, 0, 0, nearOptimalParams{}},
	{51, 0, 32, nearOptimalParams{}},
	{47, 6, 10, nearOptimalParams{}},
	{43, 12, 14, nearOptimalParams{}},
	{39, 16, 30, nearOptimalParams{}},
	{35, 16, 30, nearOptimalParams{}},
	{31, 35, 65, nearOptimalParams{}},
	{27, 100, 130, nearOptimalParams{}},
	{23, 300, 258, nearOptimalParams{}},
	{19, 600, 258, nearOptimalParams{}},
	{15, 35, 75, nearOptimalParams{2, 32, 32, 0}},
	{11, 100, 150, nearOptimalParams{4, 16, 16, 1000}},
	{7, 300, 258, nearOptimalParams{10, 1, 1, 10000}},
}

func TestNewCompressorLevels(t *testing.T) {
	for _, l := range []int{-2, -3, 13, 14, 100, math.MinInt, math.MaxInt} {
		c, err := NewCompressor(l)
		if c != nil || !errors.Is(err, ErrInvalidLevel) {
			t.Errorf("NewCompressor(%d) = %v, %v; want nil, ErrInvalidLevel", l, c, err)
		}
	}
	var static, slotFull Compressor
	static.initStaticCodes()
	slotFull.initOffsetSlotFull()
	for l := -1; l <= 12; l++ {
		c := mustCompressor(t, l)
		want := max(l, 0)
		if l == -1 {
			want = 6
		}
		p := levelParams[want]
		if c.Level() != want || c.maxPassthroughSize != p.passthrough || c.maxSearchDepth != p.depth ||
			c.niceMatchLength != p.nice || c.pn != p.pn || (c.impl != nil) != (want >= 1) {
			t.Errorf("level %d: got level %d passthrough %d depth %d nice %d pn %+v impl set %v",
				l, c.Level(), c.maxPassthroughSize, c.maxSearchDepth, c.niceMatchLength, c.pn, c.impl != nil)
		}
		if c.staticCodes != static.staticCodes {
			t.Errorf("level %d: static codes not initialized", l)
		}
		if want >= 10 {
			if c.offsetSlotFull == nil || *c.offsetSlotFull != *slotFull.offsetSlotFull {
				t.Errorf("level %d: offsetSlotFull not initialized", l)
			}
		} else if c.offsetSlotFull != nil {
			t.Errorf("level %d: offsetSlotFull allocated", l)
		}
	}
}

// storedOracle is an independent model of level 0 output: blocks of
// 65535 bytes, the last (or the only, empty one) final.
func storedOracle(in []byte) []byte {
	var out []byte
	for {
		n := min(len(in), 65535)
		final := byte(0)
		if n == len(in) {
			final = 1
		}
		out = append(out, final)
		out = binary.LittleEndian.AppendUint16(out, uint16(n))
		out = binary.LittleEndian.AppendUint16(out, ^uint16(n))
		out = append(out, in[:n]...)
		in = in[n:]
		if final == 1 {
			return out
		}
	}
}

func TestCompressLevel0Golden(t *testing.T) {
	c := mustCompressor(t, 0)
	if got := c.Compress(make([]byte, 5), nil); got != 5 {
		t.Fatalf("empty input: %d", got)
	}
	dst := make([]byte, 5)
	c.Compress(dst, []byte{})
	if !bytes.Equal(dst, []byte{0x01, 0x00, 0x00, 0xff, 0xff}) {
		t.Fatalf("empty input: % x", dst)
	}
	for _, n := range []int{0, 1, 2, 65534, 65535, 65536, 65537, 131070, 131071, 200000} {
		src := randData(uint64(n), n)
		want := storedOracle(src)
		if len(want) != 5*max(1, (n+65534)/65535)+n {
			t.Fatalf("oracle size %d for %d", len(want), n)
		}
		if b := c.CompressBound(n); b < len(want) {
			t.Fatalf("n=%d: bound %d < %d", n, b, len(want))
		}
		for _, extra := range []int{0, 1, 100} {
			dst := make([]byte, len(want)+extra)
			if got := c.Compress(dst, src); got != len(want) || !bytes.Equal(dst[:got], want) {
				t.Fatalf("n=%d cap+%d: got %d bytes, want %d (equal=%v)", n, extra, got, len(want), bytes.Equal(dst[:got], want))
			}
			if err := checkDecode(t, dst[:len(want)], src); err != nil {
				t.Fatalf("n=%d: %v", n, err)
			}
		}
		// Too small by one byte at the exact boundary, and much too small.
		for _, short := range []int{1, 2, 5, len(want)} {
			if got := c.Compress(make([]byte, len(want)-short), src); got != 0 {
				t.Fatalf("n=%d cap-%d: got %d, want 0", n, short, got)
			}
		}
	}
}

// Passthrough boundary of every level: max_passthrough_size bytes go out
// stored, one more runs the level's compressor.
func TestCompressPassthroughBoundaries(t *testing.T) {
	for l := 1; l <= 12; l++ {
		c := mustCompressor(t, l)
		maxPT := 55 - 4*l
		for n := 0; n <= maxPT; n++ {
			src := randData(uint64(100*l+n), n)
			want := storedOracle(src)
			dst := make([]byte, len(want))
			if got := c.Compress(dst, src); got != len(want) || !bytes.Equal(dst, want) {
				t.Fatalf("level %d n=%d: got %d", l, n, got)
			}
			if got := c.Compress(dst[:len(want)-1], src); got != 0 {
				t.Fatalf("level %d n=%d: one byte short gave %d", l, n, got)
			}
		}
		src := randData(uint64(l), maxPT+1)
		dst := make([]byte, c.CompressBound(len(src)))
		n := c.Compress(dst, src)
		if n == 0 || c.impl == nil {
			t.Fatalf("level %d n=%d: Compress = %d bytes (impl set: %v)", l, maxPT+1, n, c.impl != nil)
		}
		if err := checkDecode(t, dst[:n], src); err != nil {
			t.Fatalf("level %d n=%d: %v", l, maxPT+1, err)
		}
	}
	// Level 0 has no limit.
	if n := mustCompressor(t, 0).Compress(make([]byte, 1000), make([]byte, 900)); n != 905 {
		t.Fatalf("level 0: %d", n)
	}
}

func TestCompressBound(t *testing.T) {
	c := mustCompressor(t, 0)
	for _, tc := range [][2]int{
		{0, 5}, {1, 6}, {4999, 5004}, {5000, 5005}, {5001, 5011}, {10000, 10010}, {10001, 10016},
		{1 << 20, 1<<20 + 5*210},
	} {
		if got := c.CompressBound(tc[0]); got != tc[1] {
			t.Errorf("CompressBound(%d) = %d, want %d", tc[0], got, tc[1])
		}
	}
	// Same for every level (C ignores the compressor).
	for l := 1; l <= 12; l++ {
		if mustCompressor(t, l).CompressBound(123456) != c.CompressBound(123456) {
			t.Fatalf("level %d bound differs", l)
		}
	}
	if got := c.CompressBound(math.MaxInt); got != math.MaxInt {
		t.Errorf("CompressBound(MaxInt) = %d, want saturation", got)
	}
	if got, want := c.CompressBound(math.MaxInt/2), math.MaxInt/2+5*((math.MaxInt/2+4999)/5000); got != want {
		t.Errorf("CompressBound(MaxInt/2) = %d, want %d", got, want)
	}
	defer func() {
		if recover() == nil {
			t.Errorf("CompressBound(-1) did not panic")
		}
	}()
	c.CompressBound(-1)
}

// literalsImpl is a stand-in compress() implementation (not one of the
// per-level parsers) that outputs in as all-literal blocks of blockLen bytes:
// it drives Compress's bitstream path (overflow, final partial byte)
// through the real block writer.
func literalsImpl(blockLen int) compressImpl {
	var seqs [1]deflateSequence
	return func(c *Compressor, in []byte, os *outputBitstream) {
		for len(in) > 0 && !os.overflow {
			n := min(len(in), blockLen)
			c.beginSequences(&seqs[0])
			for _, b := range in[:n] {
				c.chooseLiteral(uint32(b), &seqs[0])
			}
			c.finishBlock(os, in[:n], seqs[:], n == len(in))
			in = in[n:]
		}
	}
}

func TestCompressImplPath(t *testing.T) {
	c := mustCompressor(t, 6)
	var g fbGo
	sawPartial := false
	for i, n := range []int{32, 33, 100, 1000, 6000, 70000} {
		for _, blockLen := range []int{7, 5000, 65536} {
			src := make([]byte, n)
			r := rand.New(rand.NewPCG(uint64(i), uint64(blockLen)))
			for j := range src {
				src[j] = "aab"[r.IntN(3)]
				if i%2 == 1 {
					src[j] = byte(r.Uint32())
				}
			}
			c.impl = literalsImpl(blockLen)
			// Not CompressBound: blocks shorter than MIN_BLOCK_LENGTH
			// can exceed it (C's bound relies on that minimum).
			big := make([]byte, 2*n+64)
			got := c.Compress(big, src)
			if err := checkDecode(t, big[:got], src); err != nil {
				t.Fatalf("n=%d block %d: %v", n, blockLen, err)
			}
			// The same blocks through the test harness: Compress adds the
			// final partial byte.
			rec := fbRec{level: 6, cap: len(big)}
			for k := 0; k < n; k += blockLen {
				d := src[k:min(n, k+blockLen)]
				rec.blocks = append(rec.blocks, fbBlock{k+len(d) == n, d, make([]fbTok, len(d))})
			}
			gr := g.run(t, &rec)
			if !bytes.Equal(fbStream(gr), big[:got]) {
				t.Fatalf("n=%d block %d: Compress differs from the flushed blocks", n, blockLen)
			}
			sawPartial = sawPartial || gr.bitcount != 0
			// Exact size fits, one byte less returns 0.
			if g2 := c.Compress(make([]byte, got), src); g2 != got {
				t.Fatalf("n=%d block %d: exact cap gave %d, want %d", n, blockLen, g2, got)
			}
			if g2 := c.Compress(make([]byte, got-1), src); g2 != 0 {
				t.Fatalf("n=%d block %d: cap-1 gave %d, want 0", n, blockLen, g2)
			}
		}
	}
	if !sawPartial {
		t.Fatalf("no case ended with a partial byte")
	}
}

func TestCompressNoAlloc(t *testing.T) {
	src := randData(1, 200000)
	dst := make([]byte, 300000)
	for _, tc := range []struct{ level, n int }{{0, 0}, {0, 1}, {0, 200000}, {1, 51}, {6, 31}, {12, 7}, {12, 0}} {
		c := mustCompressor(t, tc.level)
		allocs := testing.AllocsPerRun(50, func() {
			if c.Compress(dst, src[:tc.n]) == 0 {
				t.Fatal("compress failed")
			}
		})
		if allocs != 0 {
			t.Errorf("level %d n=%d: %v allocs", tc.level, tc.n, allocs)
		}
	}
}
