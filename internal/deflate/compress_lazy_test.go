package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// lazyLevels are the levels deflate_compress_lazy (5..7) and
// deflate_compress_lazy2 (8, 9) serve.
var lazyLevels = []int{5, 6, 7, 8, 9}

// Hand-computed truth table of the lazy decision of
// deflate_compress_lazy_generic: next_len >= cur_len &&
// 4 * (next_len - cur_len) + (bsr32(cur_offset) - bsr32(next_offset)) >
// 2 (first look-ahead) or > 6 (lazy2's second).
func TestLazyNextIsBetter(t *testing.T) {
	for _, tc := range []struct {
		curLen, curOff, nextLen, nextOff uint32
		want2, want6                     bool
	}{
		{5, 100, 5, 100, false, false},     // 0 + (6 - 6) = 0
		{5, 1000, 5, 4, true, true},        // 0 + (9 - 2) = 7
		{5, 1000, 5, 8, true, false},       // 0 + (9 - 3) = 6
		{5, 8, 5, 2, false, false},         // 0 + (3 - 1) = 2
		{5, 16, 5, 2, true, false},         // 0 + (4 - 1) = 3
		{5, 8, 6, 8, true, false},          // 4 + 0 = 4
		{5, 8, 6, 64, false, false},        // 4 + (3 - 6) = 1
		{5, 8, 6, 32, false, false},        // 4 + (3 - 5) = 2
		{5, 1, 6, 32768, false, false},     // 4 - 15 = -11
		{10, 1024, 11, 4096, false, false}, // 4 + (10 - 12) = 2
		{10, 1024, 11, 2048, true, false},  // 4 + (10 - 11) = 3
		{5, 4, 7, 8, true, true},           // 8 + (2 - 3) = 7
		{5, 4, 7, 16, true, false},         // 8 + (2 - 4) = 6
		{3, 1, 258, 32767, true, true},     // 1020 - 14
		{6, 4, 5, 1, false, false},         // shorter: never, whatever the offsets
		{6, 32767, 5, 1, false, false},
		{5, 4, 4, 0, false, false}, // nothing found (cur_len - 1, offset 0)
	} {
		got2 := lazyNextIsBetter(tc.curLen, tc.curOff, tc.nextLen, tc.nextOff, 2)
		got6 := lazyNextIsBetter(tc.curLen, tc.curOff, tc.nextLen, tc.nextOff, 6)
		if got2 != tc.want2 || got6 != tc.want6 {
			t.Errorf("cur (%d, %d) next (%d, %d): got %v/%v, want %v/%v", tc.curLen, tc.curOff, tc.nextLen,
				tc.nextOff, got2, got6, tc.want2, tc.want6)
		}
	}
}

func TestNewCompressorLazyImpl(t *testing.T) {
	for l := 5; l <= 9; l++ {
		c := mustCompressor(t, l)
		if c.pg == nil || c.impl == nil {
			t.Fatalf("level %d: impl %v, pg %v", l, c.impl != nil, c.pg != nil)
		}
	}
}

// Levels 5..9 round trip through the T1.2 decoder and compress/flate, with
// the same block walker checks as the greedy levels.
func TestCompressLazyRoundTrip(t *testing.T) {
	nRand := 44
	if testing.Short() {
		nRand = 11
	}
	cases := greedyInputs(nRand)
	for _, level := range lazyLevels {
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

// The three block end conditions happen at the lazy levels too: the split
// heuristic, the sequence store and the soft maximum length.
func TestCompressLazyBlockEnds(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
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
	// A literal and a 3-byte copy at a distance of at most 8192 (lazy takes
	// length 3 matches up to 8192): about one sequence per 4 bytes.
	dense := make([]byte, 0, 300000)
	for range 8192 {
		dense = append(dense, byte(r.Uint32()))
	}
	for len(dense) < 300000 {
		dense = append(dense, byte(r.Uint32()))
		d := 4 + r.IntN(8188)
		for range 3 {
			dense = append(dense, dense[len(dense)-d])
		}
	}
	same := bytes.Repeat([]byte("all work and no play makes jack a dull boy. "), 15000)
	for _, level := range lazyLevels {
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
func TestCompressLazySmallDst(t *testing.T) {
	for _, level := range lazyLevels {
		c := mustCompressor(t, level)
		for i, n := range []int{40, 300, 5000, 70535, 310000} {
			src := structuredInput(uint64(3000+i), n)
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

func TestCompressLazyNoAlloc(t *testing.T) {
	src := structuredInput(9, 400000)
	for _, level := range lazyLevels {
		c := mustCompressor(t, level)
		dst := make([]byte, c.CompressBound(len(src)))
		for _, n := range []int{40, 1000, 70535, 400000} {
			allocs := testing.AllocsPerRun(5, func() {
				if c.Compress(dst, src[:n]) == 0 {
					t.Fatal("compress failed")
				}
			})
			if allocs != 0 {
				t.Errorf("level %d n=%d: %v allocs per Compress", level, n, allocs)
			}
		}
		allocs := testing.AllocsPerRun(5, func() {
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
// gives the same output as a fresh one.
func TestCompressLazyReuse(t *testing.T) {
	inputs := [][]byte{
		structuredInput(1, 100000), bytes.Repeat([]byte("abcd"), 30000), structuredInput(2, 70000),
		make([]byte, 50000), structuredInput(3, 333333), []byte("short input, but longer than fifty-five bytes......."),
		hcRecords(rand.New(rand.NewPCG(5, 5)), 40000, 40),
	}
	for _, level := range lazyLevels {
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

// The final hc_matchfinder state after a whole Compress at levels 5..9
// equals libdeflate's c->p.g.hc_mf (dump-hcmf 'c' with full tables): the
// look-ahead searches and the skip counts insert the same positions.
func TestHcMatchfinderStateLazyMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	inputs := [][]byte{structuredInput(1, 60), structuredInput(2, 100000), hcRecords(rand.New(rand.NewPCG(1, 2)), 70000, 40)}
	for _, n := range []int{32767, 32768, 32769, 65536 + 3} {
		inputs = append(inputs, structuredInput(uint64(n), n))
	}
	for _, in := range inputs {
		for _, level := range lazyLevels {
			var s []byte
			s = append(s, 'b')
			s = binary.LittleEndian.AppendUint32(s, uint32(len(in)))
			s = append(s, in...)
			s = append(s, 'c', byte(level), 1)
			want := creftest.LDRun(t, s, "dump-hcmf")
			got := runHcmfScript(t, s)
			if len(want) != 2*(len(hcMatchfinder{}.hash3Tab)+len(hcMatchfinder{}.hash4Tab)+matchfinderWindowSize) {
				t.Fatalf("dump-hcmf gave %d bytes", len(want))
			}
			if i := firstByteDiff(got, want); i != len(want) {
				t.Fatalf("level %d, %d bytes: table entry %d differs", level, len(in), i/2)
			}
		}
	}
}
