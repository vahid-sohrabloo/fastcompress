package deflate

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// compressAt compresses src with c into a CompressBound buffer.
func compressAt(t testing.TB, c *Compressor, src []byte) []byte {
	t.Helper()
	dst := make([]byte, c.CompressBound(len(src)))
	n := c.Compress(dst, src)
	if n == 0 {
		t.Fatalf("level %d: compress of %d bytes into the bound (%d) failed", c.Level(), len(src), len(dst))
	}
	return dst[:n]
}

func TestChooseMinMatchLenGolden(t *testing.T) {
	for _, tc := range []struct{ used, depth, want uint32 }{
		{0, 300, 9}, {5, 300, 9}, {6, 300, 8}, {8, 300, 7}, {10, 300, 6}, {16, 300, 5}, {44, 300, 5},
		{45, 300, 4}, {79, 300, 4}, {80, 300, 3}, {256, 300, 3}, {1 << 31, 300, 3},
		{0, 16, 9}, {0, 15, 7}, {0, 10, 7}, {0, 9, 5}, {0, 5, 5}, {0, 4, 4}, {0, 1, 4},
		{16, 12, 5}, {45, 6, 4}, {80, 4, 3}, {10, 6, 5}, {8, 12, 7},
	} {
		if got := chooseMinMatchLen(tc.used, tc.depth); got != tc.want {
			t.Errorf("chooseMinMatchLen(%d, %d) = %d, want %d", tc.used, tc.depth, got, tc.want)
		}
	}
	// calculate: < 512 bytes -> 3; only the first 4096 bytes count.
	six := bytes.Repeat([]byte("abcdef"), 2000)
	if got := calculateMinMatchLen(six[:511], 300); got != 3 {
		t.Errorf("511 bytes: %d", got)
	}
	if got := calculateMinMatchLen(six[:512], 300); got != 8 {
		t.Errorf("512 bytes of 6 literals: %d", got)
	}
	wide := append(bytes.Clone(six[:4096]), make([]byte, 0, 256)...)
	for i := range 256 {
		wide = append(wide, byte(i))
	}
	if got := calculateMinMatchLen(wide, 300); got != 8 {
		t.Errorf("wide data after 4096 bytes counted: %d", got)
	}
	if got := calculateMinMatchLen(wide[1:], 300); got != 8 { // first 4096: "abcdef" and one 0x00
		t.Errorf("shifted: %d", got)
	}
	five := bytes.Repeat([]byte("abcde"), 1000)
	if got := calculateMinMatchLen(append(bytes.Clone(five[:4095]), 'z'), 300); got != 8 {
		t.Errorf("6th literal at index 4095: %d", got) // 6 used -> 8 (5 -> 9)
	}
	if got := calculateMinMatchLen(append(bytes.Clone(five[:4096]), 'z'), 300); got != 9 {
		t.Errorf("6th literal at index 4096: %d", got)
	}
	// recalculate: literals with freq <= total >> 10 are ignored.
	var f deflateFreqs
	f.litlen['a'] = 2048
	f.litlen['b'] = 2 // cutoff = 2053 >> 10 = 2: not counted
	f.litlen['c'] = 3
	f.litlen[280] = 1 << 20                               // not a literal
	if got := recalculateMinMatchLen(&f, 300); got != 9 { // 2 used
		t.Errorf("recalculate: %d", got)
	}
}

func TestAdjustMaxAndNiceLen(t *testing.T) {
	for _, tc := range []struct {
		maxLen, niceLen uint32
		remaining       int
		wantMax, wantN  uint32
	}{
		{258, 30, 1000, 258, 30}, {258, 30, 258, 258, 30}, {258, 30, 257, 257, 30}, {258, 30, 20, 20, 20},
		{258, 258, 100, 100, 100}, {20, 10, 15, 15, 10}, {258, 10, 1, 1, 1},
	} {
		m, n := adjustMaxAndNiceLen(tc.maxLen, tc.niceLen, tc.remaining)
		if m != tc.wantMax || n != tc.wantN {
			t.Errorf("adjust(%d, %d, %d) = %d, %d", tc.maxLen, tc.niceLen, tc.remaining, m, n)
		}
	}
}

// The min-match-length functions vs libdeflate's (ldref dump-minlen) on
// 20000 random records each (-short: 2000).
func TestMinMatchLenMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	r := rand.New(rand.NewPCG(24, 9))
	depths := []uint32{0, 1, 4, 5, 6, 9, 10, 12, 15, 16, 17, 35, 100, 300, 600}
	depth := func() uint32 {
		if r.IntN(10) == 0 {
			return r.Uint32()
		}
		return depths[r.IntN(len(depths))]
	}
	var in []byte
	var want []uint32
	u32 := func(v uint32) { in = binary.LittleEndian.AppendUint32(in, v) }
	for i := range 3 * n {
		d := depth()
		switch i % 3 {
		case 0:
			used := uint32(r.IntN(300))
			if r.IntN(20) == 0 {
				used = r.Uint32()
			}
			in = append(in, 'c')
			u32(used)
			u32(d)
			want = append(want, chooseMinMatchLen(used, d))
		case 1:
			sz := [4]int{r.IntN(600), 400 + r.IntN(300), 3900 + r.IntN(400), r.IntN(10000)}[r.IntN(4)]
			k := 1 + r.IntN([3]int{8, 90, 256}[r.IntN(3)])
			data := make([]byte, sz)
			for j := range data {
				data[j] = byte(r.IntN(k))
				if r.IntN(500) == 0 {
					data[j] = byte(r.Uint32()) // rare extra literals, also after 4096
				}
			}
			in = append(in, 'a')
			u32(d)
			u32(uint32(sz))
			in = append(in, data...)
			want = append(want, calculateMinMatchLen(data, d))
		default:
			var f deflateFreqs
			k := 1 + r.IntN(256)
			total := r.IntN(400000)
			for j := 0; j < total && j < 4000; j++ {
				f.litlen[r.IntN(k)] += uint32(1 + r.IntN(max(1, total/1000)))
			}
			for range 300 {
				if r.IntN(4) == 0 {
					f.litlen[r.IntN(deflateNumLiterals)] = uint32(r.IntN(8)) // around the cutoff
				}
			}
			in = append(in, 'r')
			u32(d)
			for j := range deflateNumLiterals {
				u32(f.litlen[j])
			}
			want = append(want, recalculateMinMatchLen(&f, d))
		}
	}
	out := creftest.LDRun(t, in, "dump-minlen")
	if len(out) != 4*len(want) {
		t.Fatalf("dump-minlen gave %d bytes for %d records", len(out), len(want))
	}
	hist := map[uint32]int{}
	for i, w := range want {
		if c := binary.LittleEndian.Uint32(out[4*i:]); c != w {
			t.Fatalf("record %d (kind %c): go %d, libdeflate %d", i, "car"[i%3], w, c)
		}
		hist[w]++
	}
	t.Logf("%d records, results %v", len(want), hist)
	for v := uint32(3); v <= 9; v++ {
		if hist[v] == 0 {
			t.Errorf("no record gave min_len %d", v)
		}
	}
}

func TestNewCompressorGreedyState(t *testing.T) {
	for l := 0; l <= 12; l++ {
		c := mustCompressor(t, l)
		if (c.pg != nil) != (l >= 2 && l <= 9) {
			t.Errorf("level %d: greedy state allocated %v", l, c.pg != nil)
		}
	}
}

// greedyInputs: the short corpus plus structured inputs of sizes around
// the block and window limits.
func greedyInputs(nRand int) []deflatetest.Case {
	cases := deflatetest.Cases(true)
	for i := range nRand {
		n := []int{40, 44, 48, 52, 512, 4096, 5000, 32769, 65536, 300000, 305000}[i%11] + i*997
		cases = append(cases, deflatetest.Case{Name: fmt.Sprintf("structured/%d", i), Data: structuredInput(uint64(i)+700, n)})
	}
	return cases
}

// Levels 2..4 round trip through the T1.2 decoder and compress/flate, and
// the block walker's view: block lengths add up, and every non-final block
// is at least MIN_BLOCK_LENGTH and at most MAX_BLOCK_LENGTH bytes long and
// was ended by the soft maximum length, the sequence store or the split
// heuristic (which needs MIN_BLOCK_LENGTH bytes left after it).
func TestCompressGreedyRoundTrip(t *testing.T) {
	nRand := 44
	if testing.Short() {
		nRand = 11
	}
	cases := greedyInputs(nRand)
	for _, level := range []int{2, 3, 4} {
		c := mustCompressor(t, level)
		for _, cs := range cases {
			// The race detector slows the parsers ~10x; they run no
			// goroutines, so small inputs suffice there.
			if raceEnabled && len(cs.Data) > 20000 {
				continue
			}
			out := compressAt(t, c, cs.Data)
			if err := checkDecode(t, out, cs.Data); err != nil {
				t.Fatalf("level %d %s: %v", level, cs.Name, err)
			}
			raw, err := walkBlocks(out)
			if err != nil {
				t.Fatalf("level %d %s: walk: %v", level, cs.Name, err)
			}
			blocks := mergeStored(raw)
			total := 0
			for i, b := range blocks {
				total += b.ulen
				if len(cs.Data) > c.maxPassthroughSize && i < len(blocks)-1 &&
					(b.ulen < minBlockLength || b.ulen > maxBlockLength || len(cs.Data)-total < minBlockLength) {
					t.Errorf("level %d %s: non-final block %d %v breaks the block limits", level, cs.Name, i, b)
				}
			}
			if total != len(cs.Data) || !blocks[len(blocks)-1].final {
				t.Fatalf("level %d %s: blocks %v cover %d of %d bytes", level, cs.Name, blocks, total, len(cs.Data))
			}
		}
	}
}

// splitEnded counts the non-final blocks of a stream that the split
// heuristic ended: shorter than SOFT_MAX_BLOCK_LENGTH and fewer than
// SEQ_STORE_LENGTH matches.
func splitEnded(blocks []blockInfo) int {
	n := 0
	for _, b := range blocks[:len(blocks)-1] {
		if b.ulen < softMaxBlockLength && b.nmatch < seqStoreLength {
			n++
		}
	}
	return n
}

// Each of the three block end conditions really happens: the split
// heuristic on data whose statistics shift, the sequence store
// (SEQ_STORE_LENGTH matches in a block) on dense 3-byte matches at small
// offsets (after a random prefix, which the heuristic splits off), and the soft maximum length on a
// repeated sentence (only 258-byte matches: uniform statistics).
func TestCompressGreedyBlockEnds(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	shift := make([]byte, 0, 200000)
	for len(shift) < 200000 {
		switch len(shift) / 20000 % 3 {
		case 0:
			shift = append(shift, bytes.Repeat([]byte("the quick brown fox "), 1000)...)
		case 1:
			for range 20000 {
				shift = append(shift, byte(r.Uint32()))
			}
		default:
			shift = append(shift, make([]byte, 20000)...)
		}
	}
	dense := make([]byte, 0, 300000)
	for range 4096 {
		dense = append(dense, byte(r.Uint32()))
	}
	for len(dense) < 300000 {
		dense = append(dense, byte(r.Uint32()))
		d := 4 + r.IntN(4092)
		for range 3 {
			dense = append(dense, dense[len(dense)-d])
		}
	}
	same := bytes.Repeat([]byte("all work and no play makes jack a dull boy. "), 15000)
	for _, level := range greedyLevels {
		c := mustCompressor(t, level)
		bs, err := walkBlocks(compressAt(t, c, shift))
		if bs = mergeStored(bs); err != nil || splitEnded(bs) < 3 {
			t.Errorf("level %d shift: %d split-ended blocks (%v): %v", level, splitEnded(bs), err, bs)
		}
		bs, err = walkBlocks(compressAt(t, c, dense))
		full := 0
		for _, b := range bs[:len(bs)-1] {
			if b.nmatch == seqStoreLength && b.ulen < softMaxBlockLength {
				full++
			}
		}
		if err != nil || full == 0 {
			t.Errorf("level %d dense: blocks %v (%v), want one ended by %d matches", level, bs, err, seqStoreLength)
		}
		bs, err = walkBlocks(compressAt(t, c, same))
		if err != nil || len(bs) != 3 || bs[0].ulen < softMaxBlockLength || bs[0].ulen >= softMaxBlockLength+258 {
			t.Errorf("level %d same: blocks %v (%v), want the first at the soft maximum", level, bs, err)
		}
	}
}

// Every output size below the needed one gives 0; the exact size gives the
// same bytes.
func TestCompressGreedySmallDst(t *testing.T) {
	for _, level := range []int{2, 3, 4} {
		c := mustCompressor(t, level)
		for i, n := range []int{48, 300, 5000, 70535, 310000} {
			src := structuredInput(uint64(2000+i), n)
			want := compressAt(t, c, src)
			dst := make([]byte, len(want))
			if m := c.Compress(dst, src); m != len(want) || !bytes.Equal(dst, want) {
				t.Fatalf("level %d n=%d: exact buffer gave %d", level, n, m)
			}
			for _, short := range []int{1, 2, 7, len(want) / 2, len(want) - 1, len(want)} {
				if m := c.Compress(dst[:len(want)-short], src); m != 0 {
					t.Fatalf("level %d n=%d: buffer %d bytes short gave %d", level, n, short, m)
				}
			}
			if c.os.out != nil {
				t.Fatal("Compress retained dst")
			}
		}
	}
}

func TestCompressGreedyNoAlloc(t *testing.T) {
	src := structuredInput(8, 400000)
	for _, level := range []int{2, 3, 4} {
		c := mustCompressor(t, level)
		dst := make([]byte, c.CompressBound(len(src)))
		for _, n := range []int{48, 1000, 70535, 400000} {
			allocs := testing.AllocsPerRun(10, func() {
				if c.Compress(dst, src[:n]) == 0 {
					t.Fatal("compress failed")
				}
			})
			if allocs != 0 {
				t.Errorf("level %d n=%d: %v allocs per Compress", level, n, allocs)
			}
		}
		allocs := testing.AllocsPerRun(10, func() {
			if c.Compress(dst[:10], src) != 0 {
				t.Fatal("overflow not reported")
			}
		})
		if allocs != 0 {
			t.Errorf("level %d overflow: %v allocs per Compress", level, allocs)
		}
	}
}

// A Compressor reused across different inputs (and after an overflow)
// gives the same output as a fresh one: no state leaks from one call to
// the next (matchfinder tables, next_tab, split stats, frequencies).
func TestCompressGreedyReuse(t *testing.T) {
	inputs := [][]byte{
		structuredInput(1, 100000), bytes.Repeat([]byte("abcd"), 30000), structuredInput(2, 70000),
		make([]byte, 50000), structuredInput(3, 333333), []byte("short input, but longer than fifty-five bytes......."),
		structuredInput(4, 40000),
	}
	for _, level := range []int{2, 3, 4} {
		reused := mustCompressor(t, level)
		for round := range 2 {
			for i, in := range inputs {
				want := compressAt(t, mustCompressor(t, level), in)
				if got := compressAt(t, reused, in); !bytes.Equal(got, want) {
					t.Fatalf("level %d round %d input %d: reused compressor differs (%s)", level, round, i,
						firstBlockDiff(got, want))
				}
				reused.Compress(make([]byte, 20), inputs[(i+3)%len(inputs)]) // an overflow in between
			}
		}
	}
}
