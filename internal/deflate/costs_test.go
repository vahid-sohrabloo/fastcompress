package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// noptNStats is the number of u32 of dump-nopt's 'S' / 'T' ops.
const noptNStats = 3*(numObservationTypes+1) + 2*(deflateMaxMatchLen+1)

func appendCosts(b []byte, k *deflateCosts) []byte {
	for _, v := range k.literal {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	for _, v := range k.length {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	for _, v := range k.offsetSlot {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// noptStatFields lists the dump-nopt 'S' / 'T' fields of c in their order.
func noptStatFields(c *Compressor) [][]uint32 {
	s, ns := &c.splitStats, c.pnState
	return [][]uint32{s.observations[:], {s.numObservations}, s.newObservations[:], {s.numNewObservations},
		ns.prevObservations[:], {ns.prevNumObservations}, ns.matchLenFreqs[:], ns.newMatchLenFreqs[:]}
}

// noptSetStats is the inverse of noptStatFields.
func noptSetStats(c *Compressor, v []uint32) {
	s, ns := &c.splitStats, c.pnState
	k := 0
	take := func(dst []uint32) {
		k += copy(dst, v[k:k+len(dst)])
	}
	take(s.observations[:])
	s.numObservations = v[k]
	k++
	take(s.newObservations[:])
	s.numNewObservations = v[k]
	k++
	take(ns.prevObservations[:])
	ns.prevNumObservations = v[k]
	k++
	take(ns.matchLenFreqs[:])
	take(ns.newMatchLenFreqs[:])
}

// noptOutcomes counts the outcomes of runNoptScript's 'o' ops (non-vacuity
// of the differential): only literals chosen, the static block's path
// chosen (the costs left behind are the static codes'), output overflow,
// and the rest.
var noptOutcomes struct{ literals, static, overflow, other int }

// runNoptScript runs an `ldref dump-nopt` script (framing in ldref.c) on
// costs.go / compress_near_optimal.go and returns the same output bytes.
// Like ldref it allocates a new compressor whenever the level changes.
func runNoptScript(t testing.TB, script []byte) []byte {
	t.Helper()
	var c *Compressor
	var out []byte
	var buf []byte
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(script[i:]) }
	readCache := func(p int, bl uint32) (int, int) {
		ncache := int(u32(p))
		p += 4
		mc := c.pnState.matchCache[:]
		for i := range ncache {
			mc[i] = lzMatch{binary.LittleEndian.Uint16(script[p+4*i:]), binary.LittleEndian.Uint16(script[p+4*i+2:])}
		}
		return p + 4*ncache, ncache
	}
	initBeyond := func(bl uint32) {
		for i := bl; i <= min(bl-1+deflateMaxMatchLen, uint32(len(c.pnState.optimumNodes)-1)); i++ {
			c.pnState.optimumNodes[i].costToEnd = 0x80000000
		}
	}
	for p := 0; p < len(script); {
		op := script[p]
		p++
		switch op {
		case 'L':
			if c == nil || c.Level() != int(script[p]) {
				c = mustCompressor(t, int(script[p]))
			}
			p++
		case 'C':
			k := &c.pnState.costs
			for i := range k.literal {
				k.literal[i] = u32(p)
				p += 4
			}
			for i := range k.length {
				k.length[i] = u32(p)
				p += 4
			}
			for i := range k.offsetSlot {
				k.offsetSlot[i] = u32(p)
				p += 4
			}
		case 'P':
			out = appendCosts(out, &c.pnState.costs)
		case 'f':
			var lens [deflateNumLitlenSyms + deflateNumOffsetSyms]uint8
			p += copy(lens[:], script[p:])
			c.setCostsFromCodes(&lens)
		case 'd':
			c.setDefaultCosts(u32(p), u32(p+4))
			p += 8
		case 'a':
			c.adjustCosts(u32(p), u32(p+4))
			p += 8
		case 'S':
			v := make([]uint32, noptNStats)
			for i := range v {
				v[i] = u32(p + 4*i)
			}
			p += 4 * noptNStats
			noptSetStats(c, v)
		case 'T':
			for _, f := range noptStatFields(c) {
				for _, v := range f {
					out = binary.LittleEndian.AppendUint32(out, v)
				}
			}
		case 'i':
			c.nearOptimalInitStats()
		case 'm':
			c.nearOptimalMergeStats()
		case 's':
			c.nearOptimalSaveStats()
		case 'x':
			c.nearOptimalClearOldStats()
		case 'l':
			n := int(u32(p))
			lit, ls := c.chooseDefaultLitlenCosts(script[p+4 : p+4+n])
			p += 4 + n
			out = binary.LittleEndian.AppendUint32(out, lit)
			out = binary.LittleEndian.AppendUint32(out, ls)
		case 'I':
			first := script[p] == 1
			n := int(u32(p + 1))
			c.setInitialCosts(script[p+5:p+5+n], first)
			p += 5 + n
		case 't':
			for i := range c.freqs.litlen {
				c.freqs.litlen[i] = u32(p)
				p += 4
			}
			for i := range c.freqs.offset {
				c.freqs.offset[i] = u32(p)
				p += 4
			}
			deflateMakeHuffmanCodes(&c.freqs, &c.codes)
			out = binary.LittleEndian.AppendUint32(out, c.computeTrueCost())
			out = append(out, c.codes.lens[:]...)
		case 'p':
			bl := u32(p)
			var ncache int
			p, ncache = readCache(p+4, bl)
			initBeyond(bl)
			c.findMinCostPath(bl, ncache)
			for _, nd := range c.pnState.optimumNodes[:bl] {
				out = binary.LittleEndian.AppendUint32(out, nd.costToEnd)
				out = binary.LittleEndian.AppendUint32(out, nd.item)
			}
			for _, v := range c.freqs.litlen {
				out = binary.LittleEndian.AppendUint32(out, v)
			}
			for _, v := range c.freqs.offset {
				out = binary.LittleEndian.AppendUint32(out, v)
			}
			out = append(out, c.codes.lens[:]...)
		case 'o':
			first, final := script[p] == 1, script[p+1] == 1
			capBytes := int(u32(p + 2))
			bl := u32(p + 6)
			p += 10
			data := script[p : p+int(bl)]
			p += int(bl)
			var ncache int
			p, ncache = readCache(p, bl)
			if len(buf) < capBytes {
				buf = make([]byte, capBytes)
			}
			for i := range buf[:capBytes] {
				buf[i] = 0xA5
			}
			var os outputBitstream
			os.init(buf[:capBytes])
			used := c.optimizeAndFlushBlock(&os, data, ncache, first, final)
			after := c.pnState.costs
			c.setCostsFromCodes(&c.staticCodes.lens)
			switch {
			case os.overflow:
				noptOutcomes.overflow++
			case used:
				noptOutcomes.literals++
			case after == c.pnState.costs:
				noptOutcomes.static++
			default:
				noptOutcomes.other++
			}
			c.pnState.costs = after
			out = append(out, byte(btoi(used)))
			out = binary.LittleEndian.AppendUint32(out, uint32(os.next))
			out = append(out, byte(os.bitcount))
			out = binary.LittleEndian.AppendUint64(out, os.bitbuf)
			out = append(out, byte(btoi(os.overflow)))
			out = append(out, buf[:os.next]...)
			out = appendCosts(out, &c.pnState.costs)
		default:
			t.Fatalf("bad script op %q at %d", op, p-1)
		}
	}
	return out
}

// noptCacheEnd builds a match cache for block as deflate_compress_near_optimal
// does (a fresh bt matchfinder over block with the level's depth and nice
// length, long matches skipping the positions they cover), and then, with
// mutate, randomly perturbs it while keeping it valid (matches dropped,
// synthetic matches added, lengths running beyond the block, offsets
// changed). It returns the cache entries and, for every step of the
// compressor's matchfinding loop (a position and the ones its long match
// skips), the position after the step and the cache length there.
func noptCacheEnd(r *rand.Rand, block []byte, depth, nice uint32, mutate bool) ([]lzMatch, [][2]int) {
	var steps [][2]int
	var mf btMatchfinder
	mf.init()
	var cache []lzMatch
	var m [maxMatchesPerPos]lzMatch
	var nh [2]uint32
	base, slideAt := 0, min(len(block), matchfinderWindowSize)
	maxLen, niceLen := uint32(deflateMaxMatchLen), nice
	for p := 0; p < len(block); {
		k := 0
		maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, len(block)-p)
		if p == slideAt {
			mf.slideWindow()
			base, slideAt = p, p+min(len(block)-p, matchfinderWindowSize)
		}
		if maxLen >= btMatchfinderRequiredNbytes {
			k = mf.getMatches(block, base, p-base, maxLen, niceLen, depth, &nh, &m)
		}
		ms := append([]lzMatch(nil), m[:k]...)
		if mutate && r.IntN(4) == 0 {
			ms = noptMutate(r, ms, p)
		}
		cache = append(cache, ms...)
		cache = append(cache, lzMatch{uint16(len(ms)), uint16(block[p])})
		best := uint32(0)
		if k > 0 {
			best = uint32(m[k-1].length)
		}
		p++
		if best >= deflateMinMatchLen && best >= niceLen {
			for best--; best > 0; best-- {
				maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, len(block)-p)
				if p == slideAt {
					mf.slideWindow()
					base, slideAt = p, p+min(len(block)-p, matchfinderWindowSize)
				}
				if maxLen >= btMatchfinderRequiredNbytes {
					mf.skipByte(block, base, p-base, niceLen, depth, &nh)
				}
				cache = append(cache, lzMatch{0, uint16(block[p])})
				p++
			}
		}
		steps = append(steps, [2]int{p, len(cache)})
	}
	return cache, steps
}

// noptMutate returns a perturbed but valid match list for position p:
// strictly increasing lengths 3..258 (possibly beyond the block), offsets
// 1..32768.
func noptMutate(r *rand.Rand, ms []lzMatch, p int) []lzMatch {
	switch r.IntN(4) {
	case 0:
		return nil
	case 1:
		if len(ms) > 0 {
			i := r.IntN(len(ms))
			return append(ms[:i:i], ms[i+1:]...)
		}
		fallthrough
	default:
		var out []lzMatch
		ln := uint16(2)
		for range 1 + r.IntN(6) {
			ln += uint16(1 + r.IntN([]int{1, 3, 40}[r.IntN(3)]))
			if ln > deflateMaxMatchLen || p+int(ln) > maxBlockLength {
				break
			}
			off := []uint16{1, 2, 4, 5, 8, 256, 257, 4097, 24577, 32768}[r.IntN(10)]
			if r.IntN(2) == 0 {
				off = uint16(1 + r.IntN(deflateMaxMatchOffset))
			}
			out = append(out, lzMatch{ln, off})
		}
		return out
	}
}

// noptScript builds one dump-nopt script: a level, a starting cost model
// and statistics, then a random sequence of the cost-model, statistics and
// optimizer operations on data of a random kind.
func noptScript(r *rand.Rand, short bool) []byte {
	var b []byte
	u32 := func(vs ...uint32) {
		for _, v := range vs {
			b = binary.LittleEndian.AppendUint32(b, v)
		}
	}
	level := 10 + r.IntN(3)
	depth := []uint32{35, 100, 300}[level-10]
	nice := []uint32{75, 150, 258}[level-10]
	b = append(b, 'L', byte(level))
	randCosts := func() {
		b = append(b, 'C')
		for range deflateNumLiterals + deflateMaxMatchLen + 1 + deflateNumOffsetSyms {
			u32(uint32(r.IntN([]int{2, 300, 4000}[r.IntN(3)])))
		}
	}
	randStats := func() {
		b = append(b, 'S')
		scale := []int{1, 100, 1 << 16, 1 << 24}[r.IntN(4)]
		for range 3 {
			sum := uint32(0)
			for range numObservationTypes {
				v := uint32(r.IntN(scale))
				if r.IntN(4) == 0 {
					v = 0
				}
				sum += v
				u32(v)
			}
			if r.IntN(10) == 0 {
				sum = uint32(r.IntN(scale))
			}
			u32(sum)
		}
		for range 2 * (deflateMaxMatchLen + 1) {
			v := uint32(0)
			if r.IntN(3) == 0 {
				v = uint32(r.IntN([]int{3, 50, 3000}[r.IntN(3)]))
			}
			u32(v)
		}
	}
	randData := func() []byte {
		var n int
		switch k := r.IntN(100); {
		case k < 70:
			n = 1 + r.IntN(1200)
		case k < 97:
			n = 1200 + r.IntN(12000)
		default:
			n = 13000 + r.IntN(maxBlockLength-13000+1)
		}
		if short && n > 40000 {
			n = 40000
		}
		switch r.IntN(4) {
		case 0:
			return structuredInput(r.Uint64(), n)
		case 1:
			return skewData(r, n)
		default:
			return btData(r, n)
		}
	}
	// blockCache returns the cache of d (cut, with d, where the
	// compressor would end the block because the cache is full).
	blockCache := func(d []byte, mutate bool) ([]byte, []lzMatch) {
		cache, steps := noptCacheEnd(r, d, depth, nice, mutate)
		for _, st := range steps {
			if st[1] >= matchCacheLength {
				return d[:st[0]], cache[:st[1]]
			}
		}
		return d, cache
	}
	appendCache := func(cache []lzMatch) {
		u32(uint32(len(cache)))
		for _, m := range cache {
			b = binary.LittleEndian.AppendUint16(b, m.length)
			b = binary.LittleEndian.AppendUint16(b, m.offset)
		}
	}
	randCosts()
	randStats()
	for range 1 + r.IntN(8) {
		switch k := r.IntN(20); {
		case k < 2:
			randCosts()
		case k < 3:
			randStats()
		case k < 5:
			b = append(b, 'f')
			for range deflateNumLitlenSyms + deflateNumOffsetSyms {
				v := byte(0)
				if r.IntN(3) != 0 {
					v = byte(1 + r.IntN(15))
				}
				b = append(b, v)
			}
		case k < 6:
			b = append(b, 'd')
			u32(uint32(r.IntN(256)), uint32(r.IntN(256)))
		case k < 8:
			b = append(b, 'a')
			u32(uint32(r.IntN(256)), uint32(r.IntN(256)))
		case k < 9:
			b = append(b, "imsx"[r.IntN(4)])
		case k < 11:
			d := randData()
			if r.IntN(10) == 0 {
				d = nil // no literals at all: num_used_literals 0 -> 1
			}
			if r.IntN(2) == 0 {
				b = append(b, 'l')
			} else {
				b = append(b, 'I', byte(r.IntN(2)))
			}
			u32(uint32(len(d)))
			b = append(b, d...)
		case k < 12:
			b = append(b, 't')
			for range deflateNumLitlenSyms + deflateNumOffsetSyms {
				v := uint32(0)
				if r.IntN(2) == 0 {
					v = uint32(r.IntN([]int{2, 100, 100000}[r.IntN(3)]))
				}
				u32(v)
			}
		case k < 14:
			d, cache := blockCache(randData(), r.IntN(2) == 0)
			b = append(b, 'p')
			u32(uint32(len(d)))
			appendCache(cache)
		default:
			d, cache := blockCache(randData(), r.IntN(3) == 0)
			b = append(b, 'o', byte(r.IntN(2)), byte(r.IntN(2)))
			capBytes := len(d) + 5*(len(d)/65535+1) + 8
			switch r.IntN(6) {
			case 0:
				capBytes = r.IntN(capBytes)
			case 1:
				capBytes = r.IntN(len(d)/4 + 16)
			}
			u32(uint32(capBytes), uint32(len(d)))
			b = append(b, d...)
			appendCache(cache)
		}
		if r.IntN(3) == 0 {
			b = append(b, 'P')
		}
		if r.IntN(4) == 0 {
			b = append(b, 'T')
		}
	}
	return append(b, 'P', 'T')
}

// skewData is data with a skewed literal distribution over a few to all
// 256 byte values and occasional copies (the default litlen costs' used
// literal count and match probability buckets).
func skewData(r *rand.Rand, n int) []byte {
	k := 1 + r.IntN(256)
	b := make([]byte, 0, n)
	for len(b) < n {
		if len(b) > 300 && r.IntN(20) == 0 {
			d := 1 + r.IntN(300)
			for j := 3 + r.IntN(30); j > 0 && len(b) < n; j-- {
				b = append(b, b[len(b)-d])
			}
			continue
		}
		v := r.IntN(k)
		b = append(b, byte(v*v/k))
	}
	return b
}

// costs.go and the near-optimal optimizer (deflate_set_costs_from_codes,
// deflate_set_default_costs, deflate_adjust_costs (+ _impl, the 5 change
// levels), deflate_choose_default_litlen_costs, deflate_set_initial_costs,
// deflate_compute_true_cost, deflate_find_min_cost_path with
// deflate_tally_item_list, deflate_optimize_and_flush_block (all passes,
// the static and only-literals alternatives, the non-final path, the block
// output from the optimum nodes) and the stats functions) vs the real C
// functions (ldref dump-nopt) on 20000 random scripts (-short: 2000).
func TestNearOptimalStagesMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	r := rand.New(rand.NewPCG(26, 2))
	var all []byte
	var starts []int
	flush := func() {
		want := creftest.LDRun(t, all, "dump-nopt")
		if got := runNoptScript(t, all); !bytes.Equal(got, want) {
			for i, s := range starts {
				e := len(all)
				if i+1 < len(starts) {
					e = starts[i+1]
				}
				w := creftest.LDRun(t, all[s:e], "dump-nopt")
				if g := runNoptScript(t, all[s:e]); !bytes.Equal(g, w) {
					d := firstByteDiff(g, w)
					t.Fatalf("script %d (%d bytes) differs at output byte %d of %d/%d: go % x, libdeflate % x", i,
						e-s, d, len(g), len(w), g[d:min(len(g), d+8)], w[d:min(len(w), d+8)])
				}
			}
			t.Fatal("outputs differ but no single script does")
		}
		all, starts = all[:0], starts[:0]
	}
	noptOutcomes = struct{ literals, static, overflow, other int }{}
	for range n {
		starts = append(starts, len(all))
		all = append(all, noptScript(r, testing.Short())...)
		if len(all) > 32<<20 {
			flush()
		}
	}
	flush()
	o := noptOutcomes
	t.Logf("optimize_and_flush outcomes: only literals %d, static path %d, overflow %d, other %d", o.literals,
		o.static, o.overflow, o.other)
	if min(o.literals, o.static, o.overflow) < n/400 || o.other < n/10 {
		t.Errorf("an optimize_and_flush outcome is (nearly) never exercised: %+v", o)
	}
}

// noptTieBlocks searches small random blocks (text, few-letter and random
// data, 20..400 bytes) on which the only-literals cost and the optimal
// static block's cost tie, selected from the costs alone (the outcome of
// the Go block flush is not consulted): deflate_optimize_and_flush_block
// prefers the static block on such a tie (only_lits_cost < static_cost is
// false) unless a dynamic pass is cheaper still. It returns up to want 'o'
// scripts, each from a fresh level 11 state.
func noptTieBlocks(t *testing.T, want int) [][]byte {
	r := rand.New(rand.NewPCG(26, 3))
	c := mustCompressor(t, 11)
	var scripts [][]byte
	for tries := 0; tries < 400000 && len(scripts) < want; tries++ {
		n := 20 + r.IntN(381)
		var d []byte
		switch r.IntN(3) {
		case 0:
			d = structuredInput(r.Uint64(), n)
		case 1:
			d = skewData(r, n)
		default:
			d = btData(r, n)
		}
		cache, _ := noptCacheEnd(r, d, c.maxSearchDepth, c.niceMatchLength, false)
		copy(c.pnState.matchCache[:], cache)
		c.chooseAllLiterals(d)
		lits := c.computeTrueCost()
		for i := uint32(n); i <= uint32(n)-1+deflateMaxMatchLen; i++ {
			c.pnState.optimumNodes[i].costToEnd = 0x80000000
		}
		c.setCostsFromCodes(&c.staticCodes.lens)
		c.findMinCostPath(uint32(n), len(cache))
		if c.pnState.optimumNodes[0].costToEnd/bitCost+7 != lits {
			continue
		}
		// A fresh compressor state: default costs (first block), zero stats.
		var b []byte
		b = append(b, 'L', 11, 'i')
		b = append(b, 'o', 1, 1)
		b = binary.LittleEndian.AppendUint32(b, uint32(n+64))
		b = binary.LittleEndian.AppendUint32(b, uint32(n))
		b = append(b, d...)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(cache)))
		for _, m := range cache {
			b = binary.LittleEndian.AppendUint16(b, m.length)
			b = binary.LittleEndian.AppendUint16(b, m.offset)
		}
		scripts = append(scripts, b)
	}
	return scripts
}

// On a tie between the only-literals cost and the static block's cost (both
// below the best dynamic pass), C chooses the static block; byte identity
// with ldref dump-nopt on every tie block, and libdeflate's own output
// (not Go's) must show the static block chosen on enough of them.
func TestNearOptimalLitsStaticTie(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	want := 40
	if testing.Short() {
		want = 10
	}
	scripts := noptTieBlocks(t, want)
	if len(scripts) < want {
		t.Fatalf("only %d of %d only-literals / static ties found", len(scripts), want)
	}
	static := 0
	for i, s := range scripts {
		got, cw := runNoptScript(t, s), creftest.LDRun(t, s, "dump-nopt")
		if !bytes.Equal(got, cw) {
			t.Fatalf("tie block %d differs from libdeflate at output byte %d", i, firstByteDiff(got, cw))
		}
		// 'o' record: used_only_literals, out_next (4), bitcount, bitbuf
		// (8), overflow, the output bytes; the block header is in the
		// first output byte, or in bitbuf when nothing was flushed.
		hdr := cw[6]
		if binary.LittleEndian.Uint32(cw[1:]) > 0 {
			hdr = cw[15]
		}
		if cw[0] == 0 && hdr>>1&3 == 1 {
			static++
		}
	}
	t.Logf("%d tie blocks, libdeflate chose the static block on %d", len(scripts), static)
	if static < want/4 {
		t.Errorf("libdeflate chose the static block on only %d of %d tie blocks", static, len(scripts))
	}
}
