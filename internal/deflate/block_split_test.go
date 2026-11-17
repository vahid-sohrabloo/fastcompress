package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// runSplitScript runs an `ldref dump-split` script (framing in ldref.c) on
// the Go block_split.go functions and returns the same output bytes, and
// how many of the 'r'/'s'/'d' queries returned true.
func runSplitScript(t testing.TB, script []byte) (out []byte, nTrue int) {
	t.Helper()
	var s blockSplitStats
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(script[i:]) }
	for p := 0; p < len(script); {
		op := script[p]
		p++
		switch op {
		case 'i':
			s.init()
		case 'l':
			s.observeLiteral(script[p])
			p++
		case 'L':
			for k := u32(p + 1); k > 0; k-- {
				s.observeLiteral(script[p])
			}
			p += 5
		case 'm':
			s.observeMatch(u32(p))
			p += 4
		case 'M':
			for k := u32(p + 4); k > 0; k-- {
				s.observeMatch(u32(p))
			}
			p += 8
		case 'g':
			s.mergeNewObservations()
		case 'r', 's':
			a, b, e := int(u32(p)), int(u32(p+4)), int(u32(p+8))
			p += 12
			var r bool
			if op == 'r' {
				r = s.readyToCheckBlock(a, b, e)
			} else {
				r = s.shouldEndBlock(a, b, e)
			}
			out = append(out, byte(b2u(r)))
			nTrue += btoi(r)
		case 'd':
			r := s.doEndBlockCheck(u32(p))
			out = append(out, byte(b2u(r)))
			nTrue += btoi(r)
			p += 4
		case 'p':
			for _, v := range s.newObservations {
				out = binary.LittleEndian.AppendUint32(out, v)
			}
			for _, v := range s.observations {
				out = binary.LittleEndian.AppendUint32(out, v)
			}
			out = binary.LittleEndian.AppendUint32(out, s.numNewObservations)
			out = binary.LittleEndian.AppendUint32(out, s.numObservations)
		default:
			t.Fatalf("bad script op %q at %d", op, p-1)
		}
	}
	return out, nTrue
}

// splitScript builds one random script: init, then observation bursts and
// checks the way a parser makes them (a block start, a growing position,
// should_end_block whenever the stats might be ready), mixed with direct
// do_end_block_check / ready_to_check_block calls, merges and stats dumps.
// Some scripts use huge observation counts and block lengths (u32 wrap of
// the products) and literal / match distributions that shift mid-block.
// It returns the script and the number of query ops.
func splitScript(r *rand.Rand) ([]byte, int) {
	var b []byte
	nq := 0
	u32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	b = append(b, 'i')
	// Two literal "regimes" (byte ranges) and a match length bias that the
	// script switches between.
	lo := [2]int{r.IntN(256), r.IntN(256)}
	span := [2]int{1 + r.IntN(256), 1 + r.IntN(64)}
	matchP := [2]int{r.IntN(100), r.IntN(100)}
	regime := 0
	huge := r.IntN(32) == 0 // up to 2^22 bulk observations: the u32 products wrap
	bulk := huge
	begin, pos := 0, 0
	end := pos + r.IntN(400000)
	for range 20 + r.IntN(60) {
		if r.IntN(10) == 0 {
			regime ^= 1
		}
		switch k := r.IntN(20); {
		case k < 12: // a burst of observations, then the parser's check
			n := 1 + r.IntN(1200)
			for range n {
				if r.IntN(100) < matchP[regime] {
					l := uint32(3 + r.IntN(8))
					if r.IntN(3) == 0 {
						l = uint32(r.IntN(300))
					}
					b = append(b, 'm')
					u32(l)
					pos += int(max(l, 1))
				} else {
					b = append(b, 'l', byte(lo[regime]+r.IntN(span[regime])))
					pos++
				}
			}
			if bulk || (huge && r.IntN(16) == 0) {
				bulk = false
				c := uint32(r.IntN(1 << 22))
				if r.IntN(2) == 0 {
					b = append(b, 'L', byte(r.Uint32()))
				} else {
					b = append(b, 'M')
					u32(uint32(r.IntN(20)))
				}
				u32(c)
			}
			if end < pos {
				end = pos + r.IntN(20000)
			}
			b = append(b, 's')
			u32(uint32(begin))
			u32(uint32(pos))
			u32(uint32(end))
			nq++
		case k < 14:
			b = append(b, 'r')
			u32(uint32(begin))
			u32(uint32(pos))
			u32(uint32(max(end, pos)))
			nq++
		case k < 16:
			bl := uint32(pos - begin)
			switch r.IntN(4) {
			case 0:
				bl = [8]uint32{4095, 4096, 8191, 8192, 9999, 10000, 10001, uint32(r.IntN(20000))}[r.IntN(8)]
			case 1:
				bl = r.Uint32()
			}
			b = append(b, 'd')
			u32(bl)
			nq++
		case k < 17:
			b = append(b, 'g')
		case k < 18:
			b = append(b, 'p')
			nq++
		default: // a new block, as the compressor starts one
			b = append(b, 'i')
			begin = pos
			end = pos + r.IntN(400000)
		}
		if pos > 8<<20 {
			break
		}
	}
	b = append(b, 'p')
	return b, nq + 1
}

// The block_split.go functions vs libdeflate's (ldref dump-split) on
// 20000 random scripts (-short: 2000).
func TestBlockSplitMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	r := rand.New(rand.NewPCG(24, 5))
	var all []byte
	var starts []int // script start offsets, for locating a mismatch
	queries, nTrue := 0, 0
	flush := func() {
		want := creftest.LDRun(t, all, "dump-split")
		got, nt := runSplitScript(t, all)
		nTrue += nt
		if !bytes.Equal(got, want) {
			for i, s := range starts {
				e := len(all)
				if i+1 < len(starts) {
					e = starts[i+1]
				}
				w := creftest.LDRun(t, all[s:e], "dump-split")
				if g, _ := runSplitScript(t, all[s:e]); !bytes.Equal(g, w) {
					t.Fatalf("script %d (%d bytes) differs at output byte %d: go %v, libdeflate %v", i, e-s,
						firstByteDiff(g, w), g[firstByteDiff(g, w):min(len(g), firstByteDiff(g, w)+8)],
						w[firstByteDiff(g, w):min(len(w), firstByteDiff(g, w)+8)])
				}
			}
			t.Fatal("outputs differ but no single script does")
		}
		all, starts = all[:0], starts[:0]
	}
	for range n {
		s, q := splitScript(r)
		starts = append(starts, len(all))
		all = append(all, s...)
		queries += q
		if len(all) > 16<<20 {
			flush()
		}
	}
	flush()
	t.Logf("%d scripts, %d queries, %d of them true", n, queries, nTrue)
	if nTrue < queries/20 || nTrue > queries/2 {
		t.Errorf("unbalanced scripts: %d of %d queries true", nTrue, queries)
	}
}

func TestObserve(t *testing.T) {
	var s blockSplitStats
	for lit := range 256 {
		s.init()
		s.observeLiteral(uint8(lit))
		// top 2 bits -> bits 2..1, low bit -> bit 0
		want := (lit>>6)<<1 | lit&1
		if s.newObservations[want] != 1 || s.numNewObservations != 1 {
			t.Fatalf("literal %#x: %v", lit, s.newObservations)
		}
	}
	for _, tc := range []struct{ length, typ uint32 }{{3, 8}, {8, 8}, {9, 9}, {258, 9}} {
		s.init()
		s.observeMatch(tc.length)
		if s.newObservations[tc.typ] != 1 || s.numNewObservations != 1 {
			t.Fatalf("match %d: %v", tc.length, s.newObservations)
		}
	}
	s.observeLiteral(0)
	s.mergeNewObservations()
	if s.observations[9] != 1 || s.observations[0] != 1 || s.numObservations != 2 || s.numNewObservations != 0 ||
		s.newObservations != [numObservationTypes]uint32{} {
		t.Fatalf("merge: %+v", s)
	}
}

// Hand-computed do_end_block_check / should_end_block cases.
func TestDoEndBlockCheckGolden(t *testing.T) {
	mk := func(old, nw [numObservationTypes]uint32) blockSplitStats {
		s := blockSplitStats{newObservations: nw, observations: old}
		for i := range numObservationTypes {
			s.numObservations += old[i]
			s.numNewObservations += nw[i]
		}
		return s
	}
	// No observations yet: never ends, merges.
	s := mk([10]uint32{}, [10]uint32{512})
	if s.doEndBlockCheck(100000) || s.numObservations != 512 || s.numNewObservations != 0 {
		t.Fatalf("first check: %+v", s)
	}
	// Same distribution: total_delta 0. cutoff = 512*200/512*1024 =
	// 204800; block 20000 (no short-block penalty): 20000/4096 = 4,
	// 4*1024 = 4096 < 204800 -> no end.
	same := mk([10]uint32{1024}, [10]uint32{512})
	if same.doEndBlockCheck(20000) {
		t.Fatal("same distribution ended the block")
	}
	// Disjoint: total_delta = 1024*512 + 512*1024 = 1048576 >= 204800.
	dis := mk([10]uint32{1024}, [10]uint32{0, 512})
	if !dis.doEndBlockCheck(20000) || dis.numObservations != 1024 {
		t.Fatal("disjoint distributions did not end the block (or merged)")
	}
	// Short-block penalty: 5000 bytes, 1536 items: cutoff 204800 +
	// 204800*(8192-1536)/8192 = 204800 + 166400 = 371200. A delta of
	// 300000-ish: old {1024, 0}, new {200, 312}: |1024*512-200*1024| +
	// |0-312*1024| = 319488 + 319488 = 638976 >= 371200 -> end; with
	// new {400, 112}: |524288-409600| + 114688 = 229376: below 371200,
	// but above 204800 at block length >= 10000.
	pen := mk([10]uint32{1024}, [10]uint32{400, 112})
	if pen.doEndBlockCheck(5000) {
		t.Fatal("short-block penalty not applied")
	}
	pen = mk([10]uint32{1024}, [10]uint32{400, 112})
	if !pen.doEndBlockCheck(10000) {
		t.Fatal("block of 10000 bytes not ended")
	}
	pen = mk([10]uint32{1024}, [10]uint32{200, 312})
	if !pen.doEndBlockCheck(5000) {
		t.Fatal("clear difference not ended despite the penalty")
	}
	// Block length term: (block_length / 4096) * num_observations.
	// same distribution, 1024 old: need 204800 / 1024 = 200 -> block
	// length >= 200*4096 = 819200.
	same = mk([10]uint32{1024}, [10]uint32{512})
	if same.doEndBlockCheck(819199) {
		t.Fatal("ended at 819199")
	}
	same = mk([10]uint32{1024}, [10]uint32{512})
	if !same.doEndBlockCheck(819200) {
		t.Fatal("not ended at 819200")
	}

	// ready_to_check_block thresholds.
	var r blockSplitStats
	r.numNewObservations = 511
	if r.readyToCheckBlock(0, 5000, 10000) {
		t.Fatal("ready with 511 new observations")
	}
	r.numNewObservations = 512
	for _, tc := range []struct {
		b, n, e int
		want    bool
	}{{0, 5000, 10000, true}, {0, 4999, 10000, false}, {0, 5000, 9999, false}, {7, 5007, 10007, true}} {
		if got := r.readyToCheckBlock(tc.b, tc.n, tc.e); got != tc.want {
			t.Errorf("ready(%d, %d, %d) = %v", tc.b, tc.n, tc.e, got)
		}
	}
	// should_end_block: not ready -> false without merging.
	s = mk([10]uint32{1024}, [10]uint32{0, 512})
	if s.shouldEndBlock(0, 4999, 100000) || s.numNewObservations != 512 {
		t.Fatal("should_end_block checked before MIN_BLOCK_LENGTH")
	}
	if !s.shouldEndBlock(0, 5000, 100000) {
		t.Fatal("should_end_block did not end")
	}
}
