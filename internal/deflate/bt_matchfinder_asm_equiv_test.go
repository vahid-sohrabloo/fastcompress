package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// Assembly equivalence of btMatchLoopAsm (levels 10..12) with the Go code,
// as for the greedy and lazy loops (compress_greedy_asm_equiv_test.go): a
// CompressChunk of history+src, assembly off (heap state and input) and on,
// the latter with the compressor's nearOptimalState (matchfinder, match
// cache, new match length frequencies) flush against a guard page (after its
// end, then before its start) and the input flush against one (after the end
// of src, then before the start of history). The assembly run starts from the
// state the Go run started from, so an entry the assembly fails to write
// keeps its old value and differs. The output, the whole nearOptimalState,
// the frequencies and the block split statistics must be identical. In builds
// without the assembly both runs are the Go code.

// btAsmEnv holds the guarded memory and the compressors of the near-optimal
// levels.
type btAsmEnv struct {
	nsAfter, nsBefore *nearOptimalState
	pre               *nearOptimalState // the Go run's starting state
	inAfter, inBefore []byte
	maxIn             int
	refC, asmC        []*Compressor // index level-10
	refDst, asmDst    []byte
}

func newBtAsmEnv(t testing.TB, maxIn int) *btAsmEnv {
	size := int(unsafe.Sizeof(nearOptimalState{}))
	a := guardmem.After(t, size)
	b := guardmem.Before(t, size)
	e := &btAsmEnv{
		nsAfter:  (*nearOptimalState)(unsafe.Pointer(&a[0])),
		nsBefore: (*nearOptimalState)(unsafe.Pointer(&b[0])),
		pre:      new(nearOptimalState),
		inAfter:  guardmem.After(t, maxIn),
		inBefore: guardmem.Before(t, maxIn),
		maxIn:    maxIn,
	}
	for level := 10; level <= 12; level++ {
		e.refC = append(e.refC, mustCompressor(t, level))
		e.asmC = append(e.asmC, mustCompressor(t, level))
	}
	e.refDst = make([]byte, e.refC[0].CompressChunkBound(maxIn))
	e.asmDst = make([]byte, len(e.refDst))
	return e
}

// checkBtAsm compresses history+data (history <= 32 KiB) at level 10..12
// with the assembly off and on, fails on any difference and returns the
// output.
func checkBtAsm(t testing.TB, e *btAsmEnv, level int, history, data []byte, final bool) []byte {
	t.Helper()
	n := len(history) + len(data)
	if n > e.maxIn {
		t.Fatalf("input of %d bytes > %d", n, e.maxIn)
	}
	h := len(history)
	refC, asmC := e.refC[level-10], e.asmC[level-10]
	ref := append(bytes.Clone(history), data...)
	*e.pre = *refC.pnState
	var want int
	withDeflateAsm(false, func() { want = refC.CompressChunk(e.refDst, ref[:h], ref[h:], final) })
	heapNs := asmC.pnState
	for i, l := range []struct {
		ns *nearOptimalState
		in []byte
	}{
		{e.nsAfter, e.inAfter[e.maxIn-n:]},
		{e.nsBefore, e.inBefore[:n:n]},
	} {
		copy(l.in, ref)
		*l.ns = *e.pre
		asmC.pnState = l.ns
		var got int
		withDeflateAsm(true, func() { got = asmC.CompressChunk(e.asmDst, l.in[:h], l.in[h:], final) })
		asmC.pnState = heapNs
		switch {
		case got != want || !bytes.Equal(e.asmDst[:got], e.refDst[:want]):
			t.Fatalf("level %d, layout %d, history %d, %d bytes, final %v: output differs (%d vs %d bytes)", level,
				i, h, len(data), final, got, want)
		case l.ns.btMf != refC.pnState.btMf:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: matchfinder tables differ", level, i, h, len(data))
		case l.ns.matchCache != refC.pnState.matchCache:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: match caches differ", level, i, h, len(data))
		case *l.ns != *refC.pnState:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: near-optimal states differ", level, i, h, len(data))
		case asmC.freqs != refC.freqs || asmC.splitStats != refC.splitStats:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: frequencies or split statistics differ", level, i,
				h, len(data))
		}
	}
	return e.refDst[:want]
}

// TestBtMatchLoopAsmEquiv: levels 10..12; every length 480..1100 (the
// assembly runs from 515 bytes on and stops 514 bytes before the end) and
// shorter ones, random inputs up to 300 KiB (window slides, block splits by
// the split statistics) with history 0..32 KiB, final and not final,
// periodic inputs (long matches, the nice-length exits and the skipped
// bytes, 16-bit offsets), 2-letter random data (blocks ended by a full match
// cache) and 400 KiB of stationary data (a block ended by
// SOFT_MAX_BLOCK_LENGTH).
func TestBtMatchLoopAsmEquiv(t *testing.T) {
	const maxIn = 400<<10 + matchfinderWindowSize
	e := newBtAsmEnv(t, maxIn)
	r := rand.New(rand.NewPCG(161, 1))
	step := 1
	if testing.Short() {
		step = 3
	}
	// The level cycles with the step count (not with n, which is a multiple
	// of step under -short), so every level is swept.
	for n := 0; n < 480; n += 7 * step {
		checkBtAsm(t, e, 10+n/(7*step)%3, nil, fastestAsmData(r, n), true)
	}
	for n := 480; n <= 1100; n += step {
		checkBtAsm(t, e, 10+(n-480)/step%3, nil, fastestAsmData(r, n), true)
	}
	rounds := 60
	if testing.Short() {
		rounds = 15
	}
	for k := range rounds {
		var n int
		switch k := r.IntN(10); {
		case k < 5:
			n = r.IntN(5000)
		case k < 9:
			n = 5000 + r.IntN(100000)
		default:
			n = 100000 + r.IntN(200000)
		}
		data := fastestAsmData(r, n)
		var hist []byte
		if r.IntN(3) == 0 {
			hist = fastestAsmData(r, r.IntN(matchfinderWindowSize+1))
		}
		checkBtAsm(t, e, 10+k%3, hist, data, r.IntN(4) != 0)
	}
	// Periodic data: matches of every length up to 258 at offsets 1..8 and
	// at offsets near 32768 (the skip loop over the bytes of a match of
	// nice_len or more, the nice-length exits of both walks).
	for p := 1; p <= 8; p++ {
		b := make([]byte, 70<<10)
		for i := range b {
			b[i] = byte(i % p)
		}
		checkBtAsm(t, e, 10+p%3, nil, b, true)
	}
	for _, off := range []int{32767, 32768, 32769} {
		b := make([]byte, 0, 100000)
		for len(b) < off {
			b = append(b, byte(r.Uint32()))
		}
		for len(b) < 100000 {
			b = append(b, b[len(b)-off])
			if r.IntN(500) == 0 {
				b[len(b)-1]++
			}
		}
		for level := 10; level <= 12; level++ {
			checkBtAsm(t, e, level, nil, b, true)
		}
	}
	// 2-letter random data: many matches per position, so blocks end with
	// a full match cache (MATCH_CACHE_LENGTH).
	for level := 10; level <= 12; level++ {
		checkBtAsm(t, e, level, nil, cacheExactInput(), true)
	}
	// This one (seed found by search with noptCacheEnd and the block
	// boundaries) fills the cache of its second block (after a 5000-byte
	// rewound one) to exactly MATCH_CACHE_LENGTH entries at the position
	// 190395, 26555 into its window (an assembly position), at levels
	// 10..12: the block ends there by the cache check's equality.
	cr := rand.New(rand.NewPCG(19, 26))
	exact := make([]byte, 200000)
	for i := range exact {
		exact[i] = byte('a' + cr.IntN(2))
	}
	for level := 10; level <= 12; level++ {
		checkBtAsm(t, e, level, nil, exact, true)
	}
	// 400 KiB of stationary data (a random letter or a copy of 3..32 bytes
	// from up to 32768 back): a block ends at in_max_block_end with more
	// than 514 bytes left, i.e. at the assembly's in_max_block_end exit.
	for level := 10; level <= 12; level++ {
		sr := rand.New(rand.NewPCG(162, uint64(level)))
		b := make([]byte, 0, 400<<10+32)
		for len(b) < 400<<10 {
			if len(b) < 8 || sr.IntN(3) == 0 {
				b = append(b, byte('a'+sr.IntN(26)))
				continue
			}
			d := 1 + sr.IntN(min(len(b), matchfinderWindowSize))
			for range 3 + sr.IntN(30) {
				b = append(b, b[len(b)-d])
			}
		}
		checkBtAsm(t, e, level, nil, b[:400<<10], true)
	}
}

// TestBtMatchLoopAsmLimits pins the two zero-slack bounds of the assembly's
// domain (deflateCompressNearOptimal's limit = min(in_end - 514,
// in_next_slide - 257), checked before each position): in data whose every
// position after the first has a 258-byte match (all zeros, period 2 and 3),
// getMatches positions fall every 258 bytes, and the k sweeps put one at each
// residue, so a 258-byte match starts at exactly in_end - 515 (the last
// assembly position: its 257 skipped bytes end with 258 bytes left) and at
// in_end - 514 (the first Go one: its skipped bytes need max_len < 258), and
// one at exactly in_next_slide - 258 and in_next_slide - 257 (the last skipped
// byte just before the slide, or on it). The input ends at a guard page (After
// layout), so a read past in_end faults; a missing slide or a wrong max_len
// changes the matchfinder state.
func TestBtMatchLoopAsmLimits(t *testing.T) {
	const maxIn = 70 << 10
	e := newBtAsmEnv(t, maxIn)
	pattern := func(b []byte, period int) {
		for i := range b {
			b[i] = byte(i % period * 0x55)
		}
	}
	levels := func(k int) []int {
		if testing.Short() {
			return []int{10 + k%3}
		}
		return []int{10, 11, 12}
	}
	for _, period := range []int{1, 2, 3} {
		for k := range deflateMaxMatchLen {
			b := make([]byte, 1100+k)
			pattern(b, period)
			for _, level := range levels(k) {
				checkBtAsm(t, e, level, nil, b, true)
			}
		}
	}
	// The window side: a k-byte random prefix, then zeros up to 70 KiB (two
	// slides, at 32768 and 65536).
	r := rand.New(rand.NewPCG(163, 1))
	for k := range deflateMaxMatchLen {
		b := make([]byte, maxIn)
		for i := range k {
			b[i] = byte(r.Uint32()) | 1
		}
		for _, level := range levels(k) {
			checkBtAsm(t, e, level, nil, b, true)
		}
	}
}

// FuzzDeflateBtMatchLoopAsmEquiv (levels 10..12, btMatchLoopAsm): the fuzz
// input's first byte selects the level, the next 2 (little-endian) the
// history length (at most the rest); the rest is history+src.
func FuzzDeflateBtMatchLoopAsmEquiv(f *testing.F) {
	f.Add([]byte{})
	f.Add(append([]byte{0, 0, 0}, bytes.Repeat([]byte("abcdefgh"), 200)...))
	f.Add(append([]byte{2, 0x10, 0}, bytes.Repeat([]byte("WXYZhello, world WXYZhello, there "), 60)...))
	r := rand.New(rand.NewPCG(7, 9))
	f.Add(append([]byte{1, 0, 1}, fastestAsmData(r, 3000)...))
	f.Add(append([]byte{2, 0, 0}, hcRecords(r, 20000, 20)...))
	const maxIn = 1 << 17
	e := newBtAsmEnv(f, maxIn)
	f.Fuzz(func(t *testing.T, data []byte) {
		level, h := 10, 0
		if len(data) >= 3 {
			level = 10 + int(data[0])%3
			h = int(binary.LittleEndian.Uint16(data[1:]))
			data = data[3:]
		}
		data = data[:min(len(data), maxIn)]
		h = min(h, len(data))
		checkBtAsm(t, e, level, data[:h], data[h:], h%2 == 0)
	})
}
