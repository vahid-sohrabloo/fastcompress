package deflate

import (
	"encoding/binary"
	"math/rand/v2"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// Assembly equivalence of minCostPathAsm (levels 10..12) with the Go loop
// findMinCostPathGo: synthetic blocks (match caches with random literals,
// strictly increasing match lengths 3..258 and offsets 1..32768, random cost
// models, the nodes past the block end set as optimizeAndFlushBlock does)
// run through the Go loop on a heap state and through the assembly, in
// minCostPathMaxPositions chunks as findMinCostPath calls it, on a
// nearOptimalState flush against a guard page (after its end, then before its
// start). The whole state and the returned cache index must match. The
// compressor-level tests (TestBtMatchLoopAsmEquiv, the near-optimal
// differential tests) cover it inside real compressions. In builds without
// the assembly the test is skipped.

// mcpEnv holds the guarded states, the reference state and a level-12
// compressor (its offsetSlotFull and the Go loop).
type mcpEnv struct {
	nsAfter, nsBefore *nearOptimalState
	ref, pre          *nearOptimalState
	c                 *Compressor
}

func newMcpEnv(t testing.TB) *mcpEnv {
	size := int(unsafe.Sizeof(nearOptimalState{}))
	a := guardmem.After(t, size)
	b := guardmem.Before(t, size)
	e := &mcpEnv{
		nsAfter:  (*nearOptimalState)(unsafe.Pointer(&a[0])),
		nsBefore: (*nearOptimalState)(unsafe.Pointer(&b[0])),
		ref:      new(nearOptimalState),
		pre:      new(nearOptimalState),
		c:        mustCompressor(t, 12),
	}
	e.pre.offsetSlotFull = *e.c.offsetSlotFull
	return e
}

// mcpGen fills e.pre with a block of blockLength positions whose match cache
// starts at index cb, and returns the cache index after its last header.
// maxMatches bounds the matches per position, longRuns makes the lengths of
// a position consecutive up to 258 (long-match data).
func mcpGen(e *mcpEnv, r *rand.Rand, blockLength, cb, maxMatches int, longRuns bool) int {
	ns := e.pre
	for i := range ns.costs.literal {
		ns.costs.literal[i] = r.Uint32() >> uint(r.IntN(32))
	}
	for i := range ns.costs.length {
		ns.costs.length[i] = r.Uint32() >> uint(r.IntN(32))
	}
	for i := range ns.costs.offsetSlot {
		ns.costs.offsetSlot[i] = r.Uint32() >> uint(r.IntN(32))
	}
	for i := range ns.optimumNodes {
		ns.optimumNodes[i] = deflateOptimumNode{costToEnd: r.Uint32(), item: r.Uint32()}
	}
	for i := blockLength; i <= min(blockLength-1+deflateMaxMatchLen, len(ns.optimumNodes)-1); i++ {
		ns.optimumNodes[i].costToEnd = 0x80000000
	}
	cp := cb
	for i := range blockLength {
		maxLen := min(deflateMaxMatchLen, maxBlockLength-i)
		k := 0
		if maxLen >= deflateMinMatchLen && r.IntN(4) != 0 {
			k = 1 + r.IntN(maxMatches)
		}
		ln, n := deflateMinMatchLen-1, 0
		for ; n < k && ln < maxLen; n++ {
			if longRuns {
				ln = min(ln+1+r.IntN(3), maxLen)
				if r.IntN(8) == 0 {
					ln = maxLen
				}
			} else {
				ln += 1 + r.IntN(max(1, (maxLen-ln)/2))
				ln = min(ln, maxLen)
			}
			off := 1 + r.IntN(deflateMaxMatchOffset)
			if r.IntN(16) == 0 {
				off = deflateMaxMatchOffset
			}
			ns.matchCache[cp] = lzMatch{length: uint16(ln), offset: uint16(off)}
			cp++
		}
		lit := uint16(r.IntN(256))
		if r.IntN(16) == 0 {
			lit = uint16(r.Uint32()) // only its low byte selects the cost
		}
		ns.matchCache[cp] = lzMatch{length: uint16(n), offset: lit}
		cp++
	}
	return cp
}

// mcpAsm runs the assembly over the block as findMinCostPath does.
func mcpAsm(ns *nearOptimalState, slotFull *[deflateMaxMatchOffset + 1]uint8, blockLength, cacheEnd int) int {
	cur, cp := blockLength, cacheEnd
	ns.optimumNodes[cur].costToEnd = 0
	for cur > 0 {
		stop := max(cur-minCostPathMaxPositions, 0)
		cp = minCostPathAsm(ns, slotFull, cur, cp, stop)
		cur = stop
	}
	return cp
}

// checkMcp runs e.pre's block (blockLength positions, cache from cb to
// cacheEnd) through the Go loop and the assembly and fails on any difference.
func checkMcp(t testing.TB, e *mcpEnv, blockLength, cb, cacheEnd int) {
	t.Helper()
	*e.ref = *e.pre
	heap := e.c.pnState
	e.c.pnState = e.ref
	e.ref.optimumNodes[blockLength].costToEnd = 0
	e.c.findMinCostPathGo(blockLength, cacheEnd)
	e.c.pnState = heap
	for i, ns := range []*nearOptimalState{e.nsAfter, e.nsBefore} {
		*ns = *e.pre
		got := mcpAsm(ns, e.c.offsetSlotFull, blockLength, cacheEnd)
		if got != cb {
			t.Fatalf("block %d, cache %d..%d, layout %d: returned cache index %d, want %d", blockLength, cb,
				cacheEnd, i, got, cb)
		}
		if ns.optimumNodes != e.ref.optimumNodes {
			for k := range ns.optimumNodes {
				if ns.optimumNodes[k] != e.ref.optimumNodes[k] {
					t.Fatalf("block %d, cache %d..%d, layout %d: node %d = %+v, want %+v", blockLength, cb,
						cacheEnd, i, k, ns.optimumNodes[k], e.ref.optimumNodes[k])
				}
			}
		}
		if *ns != *e.ref {
			t.Fatalf("block %d, cache %d..%d, layout %d: near-optimal states differ outside the nodes",
				blockLength, cb, cacheEnd, i)
		}
	}
}

// TestMinCostPathAsmEquiv: block lengths 1..20 and around the chunk size
// (1023..1025, 2048, 2049), random lengths, MAX_BLOCK_LENGTH (matches ending
// at the last node), caches starting at index 0 and further in, few or many
// matches per position, long consecutive lengths; then findMinCostPath itself
// (the dispatch, the tally and the codes) with the assembly off and on.
func TestMinCostPathAsmEquiv(t *testing.T) {
	if !useMinCostPathAsm {
		t.Skip("no minCostPathAsm in this build")
	}
	e := newMcpEnv(t)
	r := rand.New(rand.NewPCG(17, 71))
	lengths := []int{1, 2, 3, 4, 5, 7, 10, 20, 257, 258, 259, 1023, 1024, 1025, 2048, 2049, 4097, 8193}
	for range 6 {
		lengths = append(lengths, 1+r.IntN(30000))
	}
	if !testing.Short() {
		lengths = append(lengths, maxBlockLength-1, 100000+r.IntN(100000))
	}
	lengths = append(lengths, maxBlockLength)
	for n, bl := range lengths {
		cb := 0
		if n%2 == 1 {
			cb = r.IntN(1000)
		}
		maxMatches := []int{1, 3, 8, 64}[n%4]
		if bl >= 100000 {
			maxMatches = 3
		}
		end := mcpGen(e, r, bl, cb, maxMatches, n%3 == 2)
		checkMcp(t, e, bl, cb, end)
	}

	// The last positions of a MAX_BLOCK_LENGTH block with matches ending
	// exactly at node MAX_BLOCK_LENGTH.
	end := mcpGen(e, r, maxBlockLength, 0, 2, false)
	cp := end - 1
	for i := maxBlockLength - 1; i >= maxBlockLength-deflateMaxMatchLen; i-- {
		n := int(e.pre.matchCache[cp].length)
		if n > 0 {
			e.pre.matchCache[cp-1].length = uint16(maxBlockLength - i) // the longest: to the last node
		}
		cp -= n + 1
	}
	checkMcp(t, e, maxBlockLength, 0, end)

	// An offset slot table with entries >= 32 (never produced by
	// initOffsetSlotFull): both mask the slot with 0x1F (Go's
	// slotFull[offset]&0x1F), so the results must still match.
	var bad [deflateMaxMatchOffset + 1]uint8
	for i := range bad {
		bad[i] = uint8(r.Uint32())
	}
	good := e.c.offsetSlotFull
	e.c.offsetSlotFull = &bad
	end = mcpGen(e, r, 3000, 5, 8, false)
	checkMcp(t, e, 3000, 5, end)
	e.c.offsetSlotFull = good

	// findMinCostPath: dispatch, tally and codes, asm off vs on.
	for _, bl := range []int{1, 300, 1024, 5000, 20000} {
		end := mcpGen(e, r, bl, 0, 4, bl == 5000)
		var wantFreqs deflateFreqs
		var wantCodes deflateCodes
		for k, on := range []bool{false, true} {
			c := mustCompressor(t, 12)
			*c.pnState = *e.pre
			withDeflateAsm(on, func() { c.findMinCostPath(uint32(bl), end) })
			if k == 0 {
				wantFreqs, wantCodes = c.freqs, c.codes
				*e.ref = *c.pnState
			} else if *c.pnState != *e.ref || c.freqs != wantFreqs || c.codes != wantCodes {
				t.Fatalf("findMinCostPath block %d: state, frequencies or codes differ with the assembly", bl)
			}
		}
	}
}

// TestMinCostPathAsmOverrun: caches that break minCostPathAsm's per-position
// and per-match checks (a header or a match below match_cache[0], an offset
// above 32768, a length above 258 or below the running length, a match past
// node MAX_BLOCK_LENGTH) must make it store to address 0 (recovered as a nil
// pointer fault) instead of reading outside its tables; each of them would
// read memory of the state, not fault, without the check.
func TestMinCostPathAsmOverrun(t *testing.T) {
	if !useMinCostPathAsm {
		t.Skip("no minCostPathAsm in this build")
	}
	// SetPanicOnFault(true): a broken check that lets the assembly read or
	// write past the guard page of nsBefore then fails this test with a
	// recovered fault (wrong address) instead of crashing the test binary;
	// the expected store to address 0 is a nil pointer dereference, which
	// the runtime recovers either way (checked below with it false).
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	e := newMcpEnv(t)
	ns := e.nsBefore
	*ns = nearOptimalState{}
	slotFull := e.c.offsetSlotFull
	// The 4 bytes before match_cache (the end of the matchfinder's child
	// table) read as an entry: a valid-looking match unless a case needs a
	// header of no matches there.
	before := (*lzMatch)(unsafe.Add(unsafe.Pointer(&ns.matchCache[0]), -4))
	cases := []struct {
		name      string
		cache     []lzMatch // at match_cache[0:]
		cur, cp   int       // the block length and the cache index after its last header
		positions int       // positions to run (stop = cur - positions)
		before    lzMatch
	}{
		{"header below the cache", nil, 1, 0, 1, lzMatch{0, 'z'}},
		{"matches below the cache", []lzMatch{{1, 'a'}}, 1, 1, 1, lzMatch{3, 1}},
		{"second header below the cache", []lzMatch{{0, 'a'}}, 2, 1, 2, lzMatch{0, 'z'}},
		{"offset 32769", []lzMatch{{3, 32769}, {1, 'a'}}, 1, 2, 1, lzMatch{}},
		{"offset 65535", []lzMatch{{3, 65535}, {1, 'a'}}, 1, 2, 1, lzMatch{}},
		{"length 259", []lzMatch{{3, 1}, {259, 1}, {2, 'a'}}, 1, 3, 1, lzMatch{}},
		{"length below the running length", []lzMatch{{5, 1}, {4, 2}, {2, 'a'}}, 1, 3, 1, lzMatch{}},
		{"first length 2", []lzMatch{{2, 1}, {1, 'a'}}, 1, 2, 1, lzMatch{}},
		{"match past the last node", []lzMatch{{3, 1}, {1, 'a'}}, maxBlockLength, 2, 1, lzMatch{}},
		{"long match past the last node", []lzMatch{{258, 1}, {1, 'a'}, {0, 'b'}}, maxBlockLength - 200, 3, 2,
			lzMatch{}},
		{"stop == cur", []lzMatch{{0, 'a'}}, 1, 1, 0, lzMatch{}},
		{"negative stop", []lzMatch{{0, 'a'}, {0, 'b'}}, 1, 2, 2, lzMatch{}},
		{"cur past the last node", []lzMatch{{0, 'a'}}, maxBlockLength + 1, 1, 1, lzMatch{}},
	}
	run := func(name string, cache []lzMatch, cur, cp, positions int, prev lzMatch) {
		copy(ns.matchCache[:], cache)
		*before = prev
		defer func() {
			e, ok := recover().(runtime.Error)
			if !ok || !strings.Contains(e.Error(), "nil pointer") {
				t.Fatalf("%s: recovered %v, want a nil pointer dereference", name, e)
			}
			if a, ok := e.(interface{ Addr() uintptr }); ok && a.Addr() != 0 {
				t.Fatalf("%s: fault at %#x, want address 0", name, a.Addr())
			}
		}()
		minCostPathAsm(ns, slotFull, cur, cp, cur-positions)
	}
	for _, tc := range cases {
		run(tc.name, tc.cache, tc.cur, tc.cp, tc.positions, tc.before)
	}
	// The overrun store is a recoverable runtime.Error without
	// SetPanicOnFault too.
	debug.SetPanicOnFault(false)
	tc := cases[0]
	run(tc.name+" (SetPanicOnFault false)", tc.cache, tc.cur, tc.cp, tc.positions, tc.before)
}

// FuzzDeflateMinCostPathAsmEquiv (minCostPathAsm): the fuzz input's first 8
// bytes seed the generator; the next 2 (little-endian) give the block length
// (1..65536), the next one the matches per position and the long-run flag.
func FuzzDeflateMinCostPathAsmEquiv(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 0, 16, 3})
	f.Add([]byte{9, 9, 9, 9, 9, 9, 9, 9, 0xFF, 0xFF, 0x87})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x40})
	if !useMinCostPathAsm {
		f.Skip("no minCostPathAsm in this build")
	}
	e := newMcpEnv(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var b [11]byte
		copy(b[:], data)
		r := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(b[:]), 3))
		bl := 1 + int(binary.LittleEndian.Uint16(b[8:]))
		maxMatches := 1 + int(b[10]&63)
		cb := int(b[0]) * 7
		end := mcpGen(e, r, bl, cb, maxMatches, b[10]&0x80 != 0)
		checkMcp(t, e, bl, cb, end)
	})
}
