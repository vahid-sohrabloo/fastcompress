package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// Every level 1..12 has a compress() implementation; level 0 has none (it
// always outputs stored blocks). Each level allocates exactly its own
// parser state, and offsetSlotFull (levels 10..12) lives in the
// near-optimal state. (That every level compresses and round-trips is
// TestEveryLevelImplemented, compress_ports_test.go.)
func TestEveryLevelHasImpl(t *testing.T) {
	for l := -1; l <= 12; l++ {
		c := mustCompressor(t, l)
		level := c.Level()
		if (c.impl != nil) != (level >= 1) {
			t.Errorf("level %d: impl set %v", level, c.impl != nil)
		}
		if (c.pf != nil) != (level == 1) || (c.pg != nil) != (level >= 2 && level <= 9) ||
			(c.pnState != nil) != (level >= 10) {
			t.Errorf("level %d: parser state pf %v pg %v pn %v", level, c.pf != nil, c.pg != nil, c.pnState != nil)
		}
		if level >= 10 && c.offsetSlotFull != &c.pnState.offsetSlotFull {
			t.Errorf("level %d: offsetSlotFull is not the near-optimal state's", level)
		}
	}
}

func TestCompressNearOptimalRoundTrip(t *testing.T) {
	nRand := 30
	if testing.Short() {
		nRand = 6
	}
	cases := greedyInputs(nRand)
	for _, level := range nearOptimalLevels {
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
					(b.ulen < minBlockLength || b.ulen > maxBlockLength) {
					t.Errorf("level %d %s: non-final block %d %v breaks the block limits", level, cs.Name, i, b)
				}
			}
			if total != len(cs.Data) || !blocks[len(blocks)-1].final {
				t.Fatalf("level %d %s: blocks %v cover %d of %d bytes", level, cs.Name, blocks, total, len(cs.Data))
			}
		}
	}
}

// The block end conditions of the near-optimal compressor happen: the
// split heuristic (with the rewind to the previous check), the match cache
// overflow (MATCH_CACHE_LENGTH entries: a block of the 2-letter alphabet
// case ends exactly at the first matchfinding step at which the cache,
// counted from the block's first position, holds MATCH_CACHE_LENGTH
// entries; noptCacheEnd recomputes the per-step counts, which do not
// depend on the block boundaries) and the soft maximum length.
func TestCompressNearOptimalBlockEnds(t *testing.T) {
	// Single-goroutine block-boundary property check on large inputs: nothing for -race to find.
	if raceEnabled {
		t.Skip("block-boundary property check: no goroutines, runs in the normal lane")
	}
	r := rand.New(rand.NewPCG(3, 6))
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
	var overflow []byte
	for _, cs := range deflatetest.Cases(true) {
		if cs.Name == "nopt-overflow/2" {
			overflow = cs.Data
		}
	}
	same := bytes.Repeat([]byte("all work and no play makes jack a dull boy. "), 15000)
	for _, level := range nearOptimalLevels {
		c := mustCompressor(t, level)
		bs, err := walkBlocks(compressAt(t, c, shift))
		if bs = mergeStored(bs); err != nil || splitEnded(bs) < 3 {
			t.Errorf("level %d shift: %d split-ended blocks (%v): %v", level, splitEnded(bs), err, bs)
		}
		// cacheExactInput additionally has a step of the overflow-ended
		// block that lands on exactly MATCH_CACHE_LENGTH-1 entries, so a
		// limit one lower would end that block one step earlier.
		for _, in := range []struct {
			name  string
			data  []byte
			exact bool
		}{{"overflow", overflow, false}, {"cache-exact", cacheExactInput(), true}} {
			_, steps := noptCacheEnd(nil, in.data, c.maxSearchDepth, c.niceMatchLength, false)
			bs, err = walkBlocks(compressAt(t, c, in.data))
			bs = mergeStored(bs)
			overflowEnded, exactEnded, begin, si := 0, 0, 0, 0
			for _, b := range bs[:max(len(bs), 1)-1] {
				cacheBegin := 0 // cache length before the block's first position
				for si < len(steps) && steps[si][0] <= begin {
					cacheBegin = steps[si][1]
					si++
				}
				exact := false
				for k := si; k < len(steps); k++ {
					exact = exact || steps[k][1]-cacheBegin == matchCacheLength-1
					if steps[k][1]-cacheBegin >= matchCacheLength {
						if steps[k][0] == begin+b.ulen {
							overflowEnded++
							if exact {
								exactEnded++
							}
						}
						break
					}
				}
				begin += b.ulen
			}
			if err != nil || overflowEnded == 0 || (in.exact && exactEnded == 0) {
				t.Errorf("level %d %s: blocks %v (%v), want one ended by the match cache (exact landing: %v)",
					level, in.name, bs, err, in.exact)
			}
		}
		bs, err = walkBlocks(compressAt(t, c, same))
		if bs = mergeStored(bs); err != nil || len(bs) != 3 || bs[0].ulen < softMaxBlockLength ||
			bs[0].ulen >= softMaxBlockLength+258 {
			t.Errorf("level %d same: blocks %v (%v), want the first at the soft maximum", level, bs, err)
		}
	}
}

// cacheExactInput is 200000 random bytes of a 2-letter alphabet (seed found
// by search) whose second block (after a 5000-byte rewound one) ends by the
// match cache overflow at levels 10..12 after a step that lands on exactly
// MATCH_CACHE_LENGTH-1 entries:
// it pins the `cache_ptr >= &c->p.n.match_cache[MATCH_CACHE_LENGTH]` limit
// (TestCompressNearOptimalBlockEnds, and against libdeflate in
// TestCompressNearOptimalRandomMatchLibdeflate).
func cacheExactInput() []byte {
	r := rand.New(rand.NewPCG(2, 26))
	b := make([]byte, 200000)
	for i := range b {
		b[i] = byte('a' + r.IntN(2))
	}
	return b
}

// Every output size below the needed one gives 0; the exact size gives the
// same bytes.
func TestCompressNearOptimalSmallDst(t *testing.T) {
	// Single-goroutine block-boundary property check on large inputs: nothing for -race to find.
	if raceEnabled {
		t.Skip("block-boundary property check: no goroutines, runs in the normal lane")
	}
	for _, level := range nearOptimalLevels {
		c := mustCompressor(t, level)
		for i, n := range []int{40, 300, 5000, 70535, 310000} {
			src := structuredInput(uint64(4000+i), n)
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

// AllocsPerRun == 0 through the public Compress at levels 10..12 (the
// near-optimal state is allocated by NewCompressor), on overflow too.
func TestCompressNearOptimalNoAlloc(t *testing.T) {
	// Allocation counts on large inputs, single goroutine: nothing for -race to find.
	if raceEnabled {
		t.Skip("allocation counts: no goroutines, runs in the normal lane")
	}
	src := structuredInput(9, 400000)
	for _, level := range nearOptimalLevels {
		c := mustCompressor(t, level)
		dst := make([]byte, c.CompressBound(len(src)))
		for _, n := range []int{40, 1000, 70535, 400000} {
			allocs := testing.AllocsPerRun(3, func() {
				if c.Compress(dst, src[:n]) == 0 {
					t.Fatal("compress failed")
				}
			})
			if allocs != 0 {
				t.Errorf("level %d n=%d: %v allocs per Compress", level, n, allocs)
			}
		}
		allocs := testing.AllocsPerRun(3, func() {
			if c.Compress(dst[:10], src) != 0 {
				t.Fatal("overflow not reported")
			}
		})
		if allocs != 0 {
			t.Errorf("level %d overflow: %v allocs per Compress", level, allocs)
		}
	}
}

// A Compressor reused across different inputs and sizes (and after an
// overflow) gives the same output as a fresh one: the costs, statistics
// and previous-block observations carried in c.pnState between blocks are
// reset (or fully rewritten before use) at the start of every call, as in C.
func TestCompressNearOptimalReuse(t *testing.T) {
	var overflow []byte
	for _, cs := range deflatetest.Cases(true) {
		if cs.Name == "nopt-overflow/2" {
			overflow = cs.Data
		}
	}
	inputs := [][]byte{
		structuredInput(1, 100000), bytes.Repeat([]byte("abcd"), 30000), structuredInput(2, 70000),
		make([]byte, 50000), structuredInput(3, 333333), []byte("short input, but longer than fifty-five bytes......."),
		hcRecords(rand.New(rand.NewPCG(5, 5)), 40000, 40), overflow, structuredInput(4, 999),
	}
	for _, level := range nearOptimalLevels {
		reused := mustCompressor(t, level)
		for round := range 2 {
			for i, in := range inputs {
				// No goroutines here: under -race the smaller inputs suffice.
				if raceEnabled && len(in) > 70000 {
					continue
				}
				want := compressAt(t, mustCompressor(t, level), in)
				if got := compressAt(t, reused, in); !bytes.Equal(got, want) {
					t.Fatalf("level %d round %d input %d: reused compressor differs (%s)", level, round, i,
						firstBlockDiff(got, want))
				}
				// An overflow in between (the second input stops after its
				// first block, mid-way through the cost and stats state).
				reused.Compress(make([]byte, 20+i*1000), inputs[(i+3)%len(inputs)])
			}
		}
	}
}

// The final bt_matchfinder state after a whole Compress at levels 10..12
// equals libdeflate's c->p.n.bt_mf (dump-btmf 'c' with full tables): the
// skipped positions of long matches and the window slides insert the same
// nodes.
func TestBtMatchfinderStateMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	inputs := [][]byte{structuredInput(1, 60), structuredInput(2, 100000), hcRecords(rand.New(rand.NewPCG(1, 2)), 70000, 40),
		bytes.Repeat([]byte("abcdefgh"), 20000)}
	for _, n := range []int{32767, 32768, 32769, 65536 + 3} {
		inputs = append(inputs, structuredInput(uint64(n), n))
	}
	for _, in := range inputs {
		for _, level := range nearOptimalLevels {
			var s []byte
			s = append(s, 'b')
			s = binary.LittleEndian.AppendUint32(s, uint32(len(in)))
			s = append(s, in...)
			s = append(s, 'c', byte(level), 1)
			want := creftest.LDRun(t, s, "dump-btmf")
			got := runBtmfScript(t, s)
			if len(want) != 2*(len(btMatchfinder{}.hash3Tab)+len(btMatchfinder{}.hash4Tab)+len(btMatchfinder{}.childTab)) {
				t.Fatalf("dump-btmf gave %d bytes", len(want))
			}
			if i := firstByteDiff(got, want); i != len(want) {
				t.Fatalf("level %d, %d bytes: table entry %d differs", level, len(in), i/2)
			}
		}
	}
}
