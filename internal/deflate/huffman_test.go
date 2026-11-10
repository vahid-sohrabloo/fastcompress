package deflate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"slices"
	"testing"
)

// Huffman code kinds of ldref dump-codes (kind byte) and their limits.
const (
	hkLitlen  = 0 // deflate_make_huffman_code(n <= 288, MAX_LITLEN_CODEWORD_LEN = 14)
	hkOffset  = 1 // n <= 32, MAX_OFFSET_CODEWORD_LEN = 15
	hkPrecode = 2 // n <= 19, MAX_PRE_CODEWORD_LEN = 7
	hkPair    = 3 // deflate_make_huffman_codes + deflate_compute_full_len_codewords
)

var hkMaxN = [3]int{deflateNumLitlenSyms, deflateNumOffsetSyms, deflateNumPrecodeSyms}
var hkMaxLen = [3]uint32{maxLitlenCodewordLen, maxOffsetCodewordLen, maxPreCodewordLen}
var hkName = [3]string{"litlen", "offset", "precode"}

// goMakeCode runs deflateMakeHuffmanCode on zeroed output arrays (the state
// ldref dump-codes uses).
func goMakeCode(kind int, freqs []uint32) (lens []uint8, cw []uint32) {
	n := len(freqs)
	lens = make([]uint8, n)
	cw = make([]uint32, n)
	deflateMakeHuffmanCode(uint32(n), hkMaxLen[kind], freqs, lens, cw)
	return lens, cw
}

// ---------------------------------------------------------------------------
// Independent oracles (not C's algorithm).

// canonicalCodewords is RFC 1951 section 3.2.2 (bl_count / next_code, codes
// assigned in symbol order), with each codeword bit-reversed as DEFLATE
// sends it; zero-length symbols get 0.
func canonicalCodewords(lens []uint8) []uint32 {
	var blCount, nextCode [17]uint32
	for _, l := range lens {
		if l != 0 {
			blCount[l]++
		}
	}
	code := uint32(0)
	for b := 1; b <= 16; b++ {
		code = (code + blCount[b-1]) << 1
		nextCode[b] = code
	}
	out := make([]uint32, len(lens))
	for s, l := range lens {
		if l == 0 {
			continue
		}
		out[s] = bits.Reverse32(nextCode[l]) >> (32 - uint(l))
		nextCode[l]++
	}
	return out
}

// optimalCost is the cost (sum freq*len) of an unrestricted Huffman code
// (two-queue algorithm with 64-bit sums).
func optimalCost(freqs []uint32) uint64 {
	var leaves []uint64
	for _, f := range freqs {
		if f != 0 {
			leaves = append(leaves, uint64(f))
		}
	}
	if len(leaves) < 2 {
		return 0
	}
	slices.Sort(leaves)
	var nodes []uint64
	cost := uint64(0)
	i, j := 0, 0
	pop := func() uint64 {
		if i < len(leaves) && (j >= len(nodes) || leaves[i] <= nodes[j]) {
			i++
			return leaves[i-1]
		}
		j++
		return nodes[j-1]
	}
	for k := 0; k < len(leaves)-1; k++ {
		s := pop() + pop()
		cost += s
		nodes = append(nodes, s)
	}
	return cost
}

// checkCodeProps checks a deflateMakeHuffmanCode result (on zeroed output
// arrays) without using C's algorithm: used symbols get lengths, unused ones
// 0 (except C's two-codeword special case), lengths <= maxLen, the code is
// complete (Kraft sum exactly 1, so prefix-free and full), codewords are the
// canonical code, and when no length limiting happened (max length <
// maxLen, see compute_length_counts: a clamp always leaves codewords of
// length maxLen) and the frequencies don't wrap, the cost is optimal.
func checkCodeProps(freqs []uint32, lens []uint8, cw []uint32, maxLen uint32) error {
	used, sum := 0, uint64(0)
	for _, f := range freqs {
		if f != 0 {
			used++
		}
		sum += uint64(f)
	}
	if used < 2 {
		nz := 1
		for s, f := range freqs {
			if f != 0 && s != 0 {
				nz = s
			}
		}
		for s := range lens {
			want := uint8(0)
			if s == 0 || s == nz {
				want = 1
			}
			if lens[s] != want {
				return fmt.Errorf("special case: lens[%d] = %d, want %d", s, lens[s], want)
			}
		}
	} else {
		for s, f := range freqs {
			if (f != 0) != (lens[s] != 0) {
				return fmt.Errorf("sym %d freq %d has len %d", s, f, lens[s])
			}
		}
	}
	kraft, maxSeen := uint64(0), uint32(0)
	for s, l := range lens {
		if uint32(l) > maxLen {
			return fmt.Errorf("sym %d len %d > max %d", s, l, maxLen)
		}
		if l != 0 {
			kraft += 1 << (deflateMaxCodewordLen - l)
			maxSeen = max(maxSeen, uint32(l))
		}
	}
	if kraft != 1<<deflateMaxCodewordLen {
		return fmt.Errorf("Kraft sum %d/%d, want complete code", kraft, 1<<deflateMaxCodewordLen)
	}
	if want := canonicalCodewords(lens); !slices.Equal(cw, want) {
		return fmt.Errorf("codewords not canonical:\n got %v\nwant %v", cw, want)
	}
	if used >= 2 && sum < 1<<numFreqBits {
		cost := uint64(0)
		for s, f := range freqs {
			cost += uint64(f) * uint64(lens[s])
		}
		opt := optimalCost(freqs)
		if cost < opt || (maxSeen < maxLen && cost != opt) {
			return fmt.Errorf("cost %d, optimal %d (max len %d, limit %d)", cost, opt, maxSeen, maxLen)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Frequency generators (integer only), shared with the differential tests.

func fibFreqs(k int) []uint32 {
	out := make([]uint32, k)
	a, b := uint32(1), uint32(1)
	for i := range out {
		out[i] = a
		a, b = b, a+b
	}
	return out
}

// genN picks an alphabet size for kind: mostly the sizes C uses (288 / 32 /
// 19) or near them, sometimes anything in [2, max].
func genN(r *rand.Rand, kind int) int {
	maxN := hkMaxN[kind]
	switch r.IntN(4) {
	case 0:
		return maxN
	case 1:
		if kind == hkLitlen {
			return 257 + r.IntN(32)
		}
		return max(2, maxN-r.IntN(4))
	default:
		if kind == hkLitlen && r.IntN(2) == 0 {
			return 257 + r.IntN(32)
		}
		return 2 + r.IntN(maxN-1)
	}
}

// genFreqs fills n frequencies from one of several distributions: uniform
// small, uniform up to 2^31 / 2^32, Zipf-like (K/(rank+1)^s), geometric,
// sparse (0..4 nonzero), Fibonacci (adversarial for length limiting),
// near-equal (tie breaking) and powers of two. For n == 2 at least one
// frequency is nonzero (C's precondition, see sortSymbols).
func genFreqs(r *rand.Rand, n int) []uint32 {
	f := make([]uint32, n)
	perm := r.Perm(n)
	switch d := r.IntN(9); d {
	case 0: // uniform small, with zeros
		k := []uint32{2, 3, 5, 17, 1000, 1 << 16}[r.IntN(6)]
		pz := r.IntN(4)
		for i := range f {
			if r.IntN(4) >= pz {
				f[i] = r.Uint32N(k)
			}
		}
	case 1: // uniform huge (packed values wrap)
		for i := range f {
			if r.IntN(2) == 0 {
				f[i] = r.Uint32N(1 << 31)
			} else {
				f[i] = r.Uint32()
			}
		}
	case 2: // Zipf-like: K / (rank+1)^s on a random symbol order
		k := uint32(1) << (8 + r.IntN(14))
		s := 1 + r.IntN(3)
		for rank, sym := range perm {
			den := uint64(1)
			for range s {
				den *= uint64(rank + 1)
			}
			f[sym] = uint32(uint64(k) / den)
		}
	case 3: // geometric: f *= num/den per rank
		v := uint64(1) << (10 + r.IntN(12))
		num, den := uint64(1+r.IntN(4)), uint64(5)
		for _, sym := range perm {
			f[sym] = uint32(v)
			v = v * num / den
		}
	case 4: // sparse: 0..4 nonzero
		for range r.IntN(5) {
			f[r.IntN(n)] = 1 + r.Uint32N([]uint32{1, 9, 1 << 20, 1 << 31}[r.IntN(4)])
		}
	case 5: // Fibonacci (length limiting), optionally scaled, plus 1s / 0s
		fib := fibFreqs(min(n, 1+r.IntN(46)))
		scale := uint32(1 + r.IntN(3))
		rest := uint32(r.IntN(2))
		for i, sym := range perm {
			if i < len(fib) {
				f[sym] = fib[i] * scale
			} else {
				f[sym] = rest
			}
		}
	case 6: // near-equal (ties)
		base := r.Uint32N(1 << 12)
		for i := range f {
			f[i] = base + r.Uint32N(3)
		}
	case 7: // powers of two
		for i := range f {
			if r.IntN(3) != 0 {
				f[i] = 1 << r.IntN(32)
			}
		}
	default: // a geometric halving chain (deep tree) on symbol order
		v := uint32(1) << 31
		for i := range f {
			f[i] = v
			v >>= 1
		}
	}
	if n == 2 && f[0] == 0 && f[1] == 0 {
		f[r.IntN(2)] = 1
	}
	return f
}

// ---------------------------------------------------------------------------
// Golden unit tests (no ldref needed).

func TestMakeHuffmanCodeGolden(t *testing.T) {
	type tc struct {
		name  string
		kind  int
		freqs []uint32
		lens  []uint8
		cw    []uint32
	}
	single5 := make([]uint32, 32)
	single5[5] = 7
	single0 := make([]uint32, 32)
	single0[0] = 3
	wantSingle := func(nz int) ([]uint8, []uint32) {
		l, c := make([]uint8, 32), make([]uint32, 32)
		l[0], l[nz], c[nz] = 1, 1, 1
		return l, c
	}
	l5, c5 := wantSingle(5)
	l1, c1 := wantSingle(1)
	cases := []tc{
		// Hand-derived (see the C walk-through in the T2.1 ledger).
		{"3 equal", hkOffset, []uint32{1, 1, 1}, []uint8{2, 2, 1}, []uint32{1, 3, 0}},
		{"powers", hkOffset, []uint32{1, 2, 4, 8}, []uint8{3, 3, 2, 1}, []uint32{3, 7, 1, 0}},
		{"4 equal", hkOffset, []uint32{5, 5, 5, 5}, []uint8{2, 2, 2, 2}, []uint32{0, 2, 1, 3}},
		{"bucket order", hkOffset, []uint32{7, 1, 7}, []uint8{2, 2, 1}, []uint32{1, 3, 0}},
		{"single sym 5", hkOffset, single5, l5, c5},
		{"single sym 0", hkOffset, single0, l1, c1},
		{"no syms", hkOffset, make([]uint32, 32), l1, c1},
		{"n=2 sym 1", hkOffset, []uint32{0, 1}, []uint8{1, 1}, []uint32{0, 1}},
		{"n=2 sym 0", hkPrecode, []uint32{3, 0}, []uint8{1, 1}, []uint32{0, 1}},
		{"n=2 both", hkLitlen, []uint32{3, 9}, []uint8{1, 1}, []uint32{0, 1}},
		{"precode single", hkPrecode, append(make([]uint32, 18), 4), append(append([]uint8{1}, make([]uint8, 17)...), 1),
			append(append([]uint32{0}, make([]uint32, 17)...), 1)},
	}
	for _, c := range cases {
		lens, cw := goMakeCode(c.kind, c.freqs)
		if !slices.Equal(lens, c.lens) || !slices.Equal(cw, c.cw) {
			t.Errorf("%s: got lens %v cw %v, want %v %v", c.name, lens, cw, c.lens, c.cw)
		}
		if err := checkCodeProps(c.freqs, lens, cw, hkMaxLen[c.kind]); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}

	// Pinned from ref/ldref dump-codes (libdeflate v1.26, x86-64): lens as
	// one hex digit per symbol, sha256 of the codewords as u32 LE.
	pinned := []struct {
		name, lens, cwSHA string
		kind              int
		freqs             []uint32
		maxLen            uint8 // expected maximum length (limiting)
	}{
		{"fib30-offset", "ffffffffffffffffeedca98765432100",
			"971e24721ebacb4ada82ee87dd93c0d8204fe4be042b63124b933ba01def71d5",
			hkOffset, append(fibFreqs(30), 0, 0), 15},
		{"fib19-precode", "7777777777777777321",
			"cb3371ae09d79c53184c446903a1a3704621827f37122ecd2f9674ec8dcc31df",
			hkPrecode, fibFreqs(19), 7},
		{"fib25-litlen", "eeeeeeeeeccb9876655443322" + repeatHex('e', 288-25),
			"b5738252caf2ffd7e0f26904cb0c93f8744f15604699e112b58f0b9fd287e348",
			hkLitlen, append(fibFreqs(25), ones(288-25)...), 14},
		{"huge-offset", repeatHex('5', 32),
			"411da6cb8a44dfdfd94547b5824a04816d39d03958db7d1cba43a60c611a1ea4",
			hkOffset, fillU32(32, 0x80000000), 5},
		{"huge-mixed-offset", "27617777777777777777777777777776",
			"c5c06b4acb12992916b35e72a0150dc6c537fc1432a470f92bdc6ef944fed063",
			hkOffset, append([]uint32{0xffffffff, 1, 0x400000, 0x3fffff}, fillU32(28, 7)...), 7},
		{"equal-litlen", repeatHex('9', 64) + repeatHex('8', 224),
			"ba0029decf2fec6219cdc80ca38a4008c02b46cb4477c1a4fc61025580581e61",
			hkLitlen, ones(288), 9},
		{"litlen257", "",
			"b22078719c886368d1d6c3985c217a757aa5e9ffb9d5da5fe0c616ad411f23e5",
			hkLitlen, mod5(257), 9},
	}
	for _, p := range pinned {
		lens, cw := goMakeCode(p.kind, p.freqs)
		var hx []byte
		for _, l := range lens {
			hx = append(hx, "0123456789abcdef"[l])
		}
		if p.lens != "" && string(hx) != p.lens {
			t.Errorf("%s: lens\n got %s\nwant %s", p.name, hx, p.lens)
		}
		var raw []byte
		for _, c := range cw {
			raw = binary.LittleEndian.AppendUint32(raw, c)
		}
		if s := sha256.Sum256(raw); hex.EncodeToString(s[:]) != p.cwSHA {
			t.Errorf("%s: codewords sha256 %x, want %s", p.name, s, p.cwSHA)
		}
		if m := slices.Max(lens); m != p.maxLen {
			t.Errorf("%s: max len %d, want %d", p.name, m, p.maxLen)
		}
		if err := checkCodeProps(p.freqs, lens, cw, hkMaxLen[p.kind]); err != nil {
			t.Errorf("%s: %v", p.name, err)
		}
	}
}

func repeatHex(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func fillU32(n int, v uint32) []uint32 {
	f := make([]uint32, n)
	for i := range f {
		f[i] = v
	}
	return f
}

func ones(n int) []uint32 { return fillU32(n, 1) }

func mod5(n int) []uint32 {
	f := make([]uint32, n)
	for i := range f {
		f[i] = uint32(i % 5)
	}
	return f
}

// TestMakeHuffmanCodeLimits covers the alphabet sizes at their limits and
// both length-limit thresholds with the independent property checks.
func TestMakeHuffmanCodeLimits(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 1))
	sizes := map[int][]int{
		hkLitlen:  {2, 3, 256, 257, 258, 286, 287, 288},
		hkOffset:  {2, 3, 4, 30, 31, 32},
		hkPrecode: {2, 3, 18, 19},
	}
	for kind, ns := range sizes {
		for _, n := range ns {
			for range 200 {
				f := genFreqs(r, n)
				lens, cw := goMakeCode(kind, f)
				if err := checkCodeProps(f, lens, cw, hkMaxLen[kind]); err != nil {
					t.Fatalf("%s n=%d freqs %v: %v", hkName[kind], n, f, err)
				}
			}
			// All symbols used with a Fibonacci tail: limiting must
			// reach exactly the maximum length when the tree is deep.
			f := append(fibFreqs(min(n, 40)), ones(n-min(n, 40))...)
			lens, cw := goMakeCode(kind, f)
			if err := checkCodeProps(f, lens, cw, hkMaxLen[kind]); err != nil {
				t.Fatalf("%s n=%d fib: %v", hkName[kind], n, err)
			}
			if n >= 20 && uint32(slices.Max(lens)) != hkMaxLen[kind] {
				t.Errorf("%s n=%d fib: max len %d, want the limit %d", hkName[kind], n, slices.Max(lens), hkMaxLen[kind])
			}
		}
	}
}

// TestMakeHuffmanCodeProps runs the property checks on many random
// frequency vectors of every kind, and checks that reusing (dirty) output
// arrays gives the same lens and the same codewords of used symbols, as a
// pooled Compressor does.
func TestMakeHuffmanCodeProps(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 2))
	iters := 20000
	if testing.Short() {
		iters = 2000
	}
	var dirtyLens [deflateMaxNumSyms]uint8
	var dirtyCW [deflateMaxNumSyms]uint32
	for i := range iters {
		kind := i % 3
		f := genFreqs(r, genN(r, kind))
		lens, cw := goMakeCode(kind, f)
		if err := checkCodeProps(f, lens, cw, hkMaxLen[kind]); err != nil {
			t.Fatalf("%s n=%d freqs %v: %v", hkName[kind], len(f), f, err)
		}
		n := len(f)
		deflateMakeHuffmanCode(uint32(n), hkMaxLen[kind], f, dirtyLens[:n], dirtyCW[:n])
		for s := range n {
			if dirtyLens[s] != lens[s] || (lens[s] != 0 && dirtyCW[s] != cw[s]) {
				t.Fatalf("%s: reused arrays differ at sym %d", hkName[kind], s)
			}
		}
	}
}

func TestMakeHuffmanCodesPair(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 3))
	for range 500 {
		var freqs deflateFreqs
		copy(freqs.litlen[:], genFreqs(r, deflateNumLitlenSyms))
		copy(freqs.offset[:], genFreqs(r, deflateNumOffsetSyms))
		var codes deflateCodes
		deflateMakeHuffmanCodes(&freqs, &codes)
		ll, lc := goMakeCode(hkLitlen, freqs.litlen[:])
		ol, oc := goMakeCode(hkOffset, freqs.offset[:])
		if !slices.Equal(codes.lens[:lensOffsetStart], ll) || !slices.Equal(codes.codewords.litlen[:], lc) ||
			!slices.Equal(codes.lens[lensOffsetStart:], ol) || !slices.Equal(codes.codewords.offset[:], oc) {
			t.Fatal("deflateMakeHuffmanCodes differs from the two deflateMakeHuffmanCode calls")
		}
		var c Compressor
		c.computeFullLenCodewords(&codes)
		checkFullLen(t, &c, &codes)
	}
}

// checkFullLen checks c.o.length against its definition (independently of
// the C loop): for each length, the litlen codeword of its length symbol
// followed by the extra bits (length - base, in the slot's extra bit count).
func checkFullLen(t *testing.T, c *Compressor, codes *deflateCodes) {
	t.Helper()
	for ln := deflateMinMatchLen; ln <= deflateMaxMatchLen; ln++ {
		slot := 0
		for slot+1 < len(deflateLengthSlotBase) && int(deflateLengthSlotBase[slot+1]) <= ln {
			slot++
		}
		sym := deflateFirstLenSym + slot
		extra := uint32(ln) - deflateLengthSlotBase[slot]
		if extra >= 1<<deflateExtraLengthBits[slot] {
			t.Fatalf("len %d: extra %d does not fit %d bits", ln, extra, deflateExtraLengthBits[slot])
		}
		wantCW := codes.codewords.litlen[sym] | extra<<codes.lens[sym]
		wantLen := codes.lens[sym] + deflateExtraLengthBits[slot]
		if c.o.length.codewords[ln] != wantCW || c.o.length.lens[ln] != wantLen {
			t.Fatalf("len %d: full codeword %#x/%d, want %#x/%d", ln,
				c.o.length.codewords[ln], c.o.length.lens[ln], wantCW, wantLen)
		}
	}
	for ln := range deflateMinMatchLen {
		if c.o.length.codewords[ln] != 0 || c.o.length.lens[ln] != 0 {
			t.Fatalf("len %d written", ln)
		}
	}
}

// ---------------------------------------------------------------------------
// Primitives.

func TestHeapSort(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 4))
	for n := range 300 {
		a := make([]uint32, n+2)
		for i := range a {
			a[i] = r.Uint32N(uint32(1 + r.IntN(1<<20)))
		}
		want := slices.Clone(a[1 : n+1])
		slices.Sort(want)
		heapSort(a[1:], uint32(n))
		if !slices.Equal(a[1:n+1], want) {
			t.Fatalf("n=%d: not sorted", n)
		}
	}
}

func TestBitPrimitives(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 5))
	for range 10000 {
		v := r.Uint64() >> r.IntN(64)
		if v == 0 {
			v = 1
		}
		hi, lo := -1, -1
		for b := range 64 {
			if v>>b&1 != 0 {
				hi = b
				if lo < 0 {
					lo = b
				}
			}
		}
		if bsrw(v) != uint32(hi) || bsfw(v) != uint32(lo) {
			t.Fatalf("bsrw/bsfw(%#x) = %d/%d, want %d/%d", v, bsrw(v), bsfw(v), hi, lo)
		}
		if v32 := uint32(v); v32 != 0 {
			h, l := 31-bits.LeadingZeros32(v32), bits.TrailingZeros32(v32)
			if bsr32(v32) != uint32(h) || bsf32(v32) != uint32(l) {
				t.Fatalf("bsr32/bsf32(%#x)", v32)
			}
		}
	}
	for ln := uint8(0); ln <= 16; ln++ {
		for cw := uint32(0); cw < 1<<ln && cw < 4096; cw++ {
			want := uint32(0)
			for b := range ln {
				want |= (cw >> b & 1) << (ln - 1 - b)
			}
			if got := reverseCodeword(cw, ln); got != want {
				t.Fatalf("reverseCodeword(%#x, %d) = %#x, want %#x", cw, ln, got, want)
			}
		}
	}
	// Zero-length codewords (unused symbols) are 0, as with C's x86 table.
	for cw := uint32(0); cw < 300; cw++ {
		if reverseCodeword(cw, 0) != 0 {
			t.Fatalf("reverseCodeword(%d, 0) != 0", cw)
		}
	}
	if !canBuffer(56) || canBuffer(57) {
		t.Fatal("canBuffer limits: want 56 ok, 57 not")
	}
}

// TestOffsetSlots checks deflateGetOffsetSlot and the full map against the
// slot base / extra bits tables, and the length slot table against the
// length base table.
func TestOffsetSlots(t *testing.T) {
	var c Compressor
	c.initOffsetSlotFull()
	slot := 0
	for off := uint32(1); off <= deflateMaxMatchOffset; off++ {
		for slot+1 < len(deflateOffsetSlotBase) && deflateOffsetSlotBase[slot+1] <= off {
			slot++
		}
		if off-deflateOffsetSlotBase[slot] >= 1<<deflateExtraOffsetBits[slot] {
			t.Fatalf("offset %d beyond slot %d", off, slot)
		}
		if got := deflateGetOffsetSlot(off); got != uint32(slot) {
			t.Fatalf("deflateGetOffsetSlot(%d) = %d, want %d", off, got, slot)
		}
		if c.offsetSlotFull[off] != uint8(slot) {
			t.Fatalf("offsetSlotFull[%d] = %d, want %d", off, c.offsetSlotFull[off], slot)
		}
	}
	if slot != 29 || c.offsetSlotFull[0] != 0 {
		t.Fatalf("last slot %d, entry 0 %d", slot, c.offsetSlotFull[0])
	}
	ls := 0
	for ln := deflateMinMatchLen; ln <= deflateMaxMatchLen; ln++ {
		for ls+1 < len(deflateLengthSlotBase) && int(deflateLengthSlotBase[ls+1]) <= ln {
			ls++
		}
		if deflateLengthSlot[ln] != uint8(ls) {
			t.Fatalf("deflateLengthSlot[%d] = %d, want %d", ln, deflateLengthSlot[ln], ls)
		}
	}
}

// TestStaticCodesGolden checks the static codes against RFC 1951 3.2.6
// (lengths 8/9/7/8 and 5, canonical codewords) and the frequencies
// deflate_init_static_codes leaves in c->freqs.
func TestStaticCodesGolden(t *testing.T) {
	var c Compressor
	c.initStaticCodes()
	for s := range deflateNumLitlenSyms + deflateNumOffsetSyms {
		want := uint8(5)
		switch {
		case s < 144:
			want = 8
		case s < 256:
			want = 9
		case s < 280:
			want = 7
		case s < 288:
			want = 8
		}
		if c.staticCodes.lens[s] != want {
			t.Fatalf("static len[%d] = %d, want %d", s, c.staticCodes.lens[s], want)
		}
	}
	if !slices.Equal(c.staticCodes.codewords.litlen[:], canonicalCodewords(c.staticCodes.lens[:lensOffsetStart])) ||
		!slices.Equal(c.staticCodes.codewords.offset[:], canonicalCodewords(c.staticCodes.lens[lensOffsetStart:])) {
		t.Fatal("static codewords are not the RFC 1951 canonical code")
	}
	// RFC 1951: literal 0 is 00110000 (8 bits), 144 is 110010000, 256 is
	// 0000000, 280 is 11000000 (shown MSB first; stored bit-reversed).
	for _, e := range []struct{ sym, code, ln int }{{0, 0x30, 8}, {143, 0xbf, 8}, {144, 0x190, 9}, {255, 0x1ff, 9},
		{256, 0, 7}, {279, 0x17, 7}, {280, 0xc0, 8}, {287, 0xc7, 8}} {
		if got := c.staticCodes.codewords.litlen[e.sym]; got != bits.Reverse32(uint32(e.code))>>(32-e.ln) {
			t.Fatalf("static codeword %d = %#x", e.sym, got)
		}
	}
	for s := range deflateNumLitlenSyms {
		if c.freqs.litlen[s] != 1<<(9-c.staticCodes.lens[s]) {
			t.Fatalf("freqs.litlen[%d] = %d", s, c.freqs.litlen[s])
		}
	}
	for s := range deflateNumOffsetSyms {
		if c.freqs.offset[s] != 1 {
			t.Fatalf("freqs.offset[%d] = %d", s, c.freqs.offset[s])
		}
	}
	c.resetSymbolFrequencies()
	if c.freqs != (deflateFreqs{}) {
		t.Fatal("resetSymbolFrequencies left nonzero counters")
	}
	c.computeFullLenCodewords(&c.staticCodes)
	checkFullLen(t, &c, &c.staticCodes)
}

// ---------------------------------------------------------------------------
// Precode items and the dynamic header.

// decodeItems expands precode items back into codeword lengths (RFC 1951
// 3.2.7 semantics), the independent check of deflateComputePrecodeItems.
func decodeItems(items []uint32) ([]uint8, error) {
	var out []uint8
	for _, it := range items {
		sym, extra := it&31, it>>5
		switch {
		case sym <= 15:
			if extra != 0 {
				return nil, fmt.Errorf("literal item %#x has extra bits", it)
			}
			out = append(out, uint8(sym))
		case sym == 16:
			if len(out) == 0 || extra > 3 {
				return nil, fmt.Errorf("bad item %#x", it)
			}
			for range 3 + extra {
				out = append(out, out[len(out)-1])
			}
		case sym == 17:
			if extra > 7 {
				return nil, fmt.Errorf("bad item %#x", it)
			}
			out = append(out, make([]uint8, 3+extra)...)
		case sym == 18:
			if extra > 127 {
				return nil, fmt.Errorf("bad item %#x", it)
			}
			out = append(out, make([]uint8, 11+extra)...)
		default:
			return nil, fmt.Errorf("bad item %#x", it)
		}
	}
	return out, nil
}

// checkHeaderProps checks precomputeHuffmanHeader's result for the input
// lens (litlen 288 + offset 32) without C's algorithm.
func checkHeaderProps(in *[deflateNumLitlenSyms + deflateNumOffsetSyms]uint8, c *Compressor) error {
	p := &c.o.precode
	nl, no := 288, 32
	for nl > 257 && in[nl-1] == 0 {
		nl--
	}
	for no > 1 && in[288+no-1] == 0 {
		no--
	}
	if p.numLitlenSyms != uint32(nl) || p.numOffsetSyms != uint32(no) {
		return fmt.Errorf("nl/no %d/%d, want %d/%d", p.numLitlenSyms, p.numOffsetSyms, nl, no)
	}
	want := append(slices.Clone(in[:nl]), in[288:288+no]...)
	got, err := decodeItems(p.items[:p.numItems])
	if err != nil {
		return err
	}
	if !slices.Equal(got, want) {
		return fmt.Errorf("items decode to %v, want %v", got, want)
	}
	var freqs [deflateNumPrecodeSyms]uint32
	for _, it := range p.items[:p.numItems] {
		freqs[it&31]++
	}
	if freqs != p.freqs {
		return fmt.Errorf("precode freqs %v, items give %v", p.freqs, freqs)
	}
	if err := checkCodeProps(p.freqs[:], p.lens[:], p.codewords[:], maxPreCodewordLen); err != nil {
		return fmt.Errorf("precode: %v", err)
	}
	ne := 19
	for ne > 4 && p.lens[deflatePrecodeLensPermutation[ne-1]] == 0 {
		ne--
	}
	if p.numExplicitLens != uint32(ne) {
		return fmt.Errorf("numExplicitLens %d, want %d", p.numExplicitLens, ne)
	}
	// Lens after the call: as on input, except C's leftover copy of the
	// offset lengths at [nl, nl+no) when nl < 288.
	// (When nl+no > 288 the two ranges overlap and the restore rewrites the
	// overlap with the real offset lengths.)
	exp := *in
	if nl != 288 {
		copy(exp[nl:nl+no], in[288:288+no])
		copy(exp[288:288+no], in[288:288+no])
	}
	if exp != c.codes.lens {
		return fmt.Errorf("codes.lens after the call:\n got %v\nwant %v", c.codes.lens, exp)
	}
	return nil
}

// genHeaderLens makes a litlen+offset lens set: either a real code from the Go
// builder (litlen with an end-of-block symbol) or raw lengths 0..15 with
// runs, with optional zero tails.
func genHeaderLens(r *rand.Rand) (l [deflateNumLitlenSyms + deflateNumOffsetSyms]uint8) {
	if r.IntN(2) == 0 {
		var freqs deflateFreqs
		copy(freqs.litlen[:], genFreqs(r, deflateNumLitlenSyms))
		copy(freqs.offset[:], genFreqs(r, deflateNumOffsetSyms))
		freqs.litlen[deflateEndOfBlock]++
		if r.IntN(3) == 0 {
			for s := 257 + r.IntN(32); s < 288; s++ {
				freqs.litlen[s] = 0
			}
		}
		var codes deflateCodes
		deflateMakeHuffmanCodes(&freqs, &codes)
		return codes.lens
	}
	i := 0
	for i < len(l) {
		run := 1 + r.IntN([]int{3, 8, 40, 160}[r.IntN(4)])
		v := uint8(0)
		if r.IntN(3) != 0 {
			v = uint8(r.IntN(16))
		}
		for ; run > 0 && i < len(l); run-- {
			l[i] = v
			i++
		}
	}
	if r.IntN(2) == 0 {
		for s := 257 + r.IntN(32); s < 288; s++ {
			l[s] = 0
		}
	}
	if r.IntN(2) == 0 {
		for s := 288 + r.IntN(33); s < len(l); s++ {
			l[s] = 0
		}
	}
	return l
}

func TestPrecomputeHuffmanHeaderGolden(t *testing.T) {
	// litlen: literal 0 and end-of-block (1 bit each); offset: symbol 0.
	var c Compressor
	c.codes.lens[0] = 1
	c.codes.lens[256] = 1
	c.codes.lens[288] = 1
	in := c.codes.lens
	c.precomputeHuffmanHeader()
	p := &c.o.precode
	wantItems := []uint32{1, 18 | 127<<5, 18 | 106<<5, 1, 1}
	if p.numLitlenSyms != 257 || p.numOffsetSyms != 1 || p.numExplicitLens != 18 ||
		!slices.Equal(p.items[:p.numItems], wantItems) {
		t.Fatalf("nl %d no %d nexp %d items %v", p.numLitlenSyms, p.numOffsetSyms, p.numExplicitLens, p.items[:p.numItems])
	}
	var wantFreqs [19]uint32
	wantFreqs[1], wantFreqs[18] = 3, 2
	var wantLens [19]uint8
	wantLens[1], wantLens[18] = 1, 1
	var wantCW [19]uint32
	wantCW[18] = 1
	if p.freqs != wantFreqs || p.lens != wantLens || p.codewords != wantCW {
		t.Fatalf("precode freqs %v lens %v cw %v", p.freqs, p.lens, p.codewords)
	}
	if c.codes.lens[257] != 1 {
		t.Fatal("C's leftover offset length copy at lens[257] is missing")
	}
	if err := checkHeaderProps(&in, &c); err != nil {
		t.Fatal(err)
	}

	// Full litlen (288) and offset (32) sets: nothing moved; all-zero
	// input: nl 257, no 1, one RLE-18 run sequence.
	for _, v := range []uint8{0, 5, 15} {
		var c Compressor
		for i := range c.codes.lens {
			c.codes.lens[i] = v
		}
		in := c.codes.lens
		c.precomputeHuffmanHeader()
		if err := checkHeaderProps(&in, &c); err != nil {
			t.Fatalf("all %d: %v", v, err)
		}
	}
}

func TestPrecomputeHuffmanHeaderProps(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 6))
	iters := 20000
	if testing.Short() {
		iters = 2000
	}
	var pooled Compressor // never reset, like a reused compressor
	for range iters {
		in := genHeaderLens(r)
		var c Compressor
		c.codes.lens = in
		c.precomputeHuffmanHeader()
		if err := checkHeaderProps(&in, &c); err != nil {
			t.Fatalf("lens %v: %v", in, err)
		}
		pooled.codes.lens = in
		pooled.precomputeHuffmanHeader()
		q, p := &pooled.o.precode, &c.o.precode
		if q.numLitlenSyms != p.numLitlenSyms || q.numOffsetSyms != p.numOffsetSyms ||
			q.numExplicitLens != p.numExplicitLens || q.numItems != p.numItems || q.lens != p.lens ||
			q.freqs != p.freqs || !slices.Equal(q.items[:q.numItems], p.items[:p.numItems]) ||
			pooled.codes.lens != c.codes.lens {
			t.Fatal("pooled compressor differs")
		}
		for s := range q.lens {
			if q.lens[s] != 0 && q.codewords[s] != p.codewords[s] {
				t.Fatalf("pooled precode codeword %d differs", s)
			}
		}
	}
}

// TestHuffmanNoAlloc: the builders allocate nothing (the Compressor is
// allocated once and reused).
func TestHuffmanNoAlloc(t *testing.T) {
	c := new(Compressor)
	r := rand.New(rand.NewPCG(21, 7))
	copy(c.freqs.litlen[:], genFreqs(r, deflateNumLitlenSyms))
	copy(c.freqs.offset[:], genFreqs(r, deflateNumOffsetSyms))
	c.freqs.litlen[deflateEndOfBlock] = 1
	saved := c.freqs
	n := testing.AllocsPerRun(100, func() {
		c.freqs = saved
		deflateMakeHuffmanCodes(&c.freqs, &c.codes)
		c.precomputeHuffmanHeader()
		c.computeFullLenCodewords(&c.codes)
		c.initStaticCodes()
		c.initOffsetSlotFull()
		c.resetSymbolFrequencies()
		_ = deflateGetOffsetSlot(32768)
	})
	if n != 0 {
		t.Fatalf("%v allocs per run, want 0", n)
	}
}
