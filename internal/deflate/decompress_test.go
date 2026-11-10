package deflate

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// ---------------------------------------------------------------------------
// A small DEFLATE stream writer for hand-made test streams (RFC 1951 only; it
// is not derived from any compressor). Tokens are expanded by tokOutput, an
// independent oracle for the expected output.

// tok is a literal (dist == 0) or a match of length len at distance dist.
type tok struct {
	lit       byte
	len, dist int
}

func lit(b byte) tok     { return tok{lit: b} }
func match(l, d int) tok { return tok{len: l, dist: d} }
func lits(s string) []tok {
	t := make([]tok, len(s))
	for i := range s {
		t[i] = lit(s[i])
	}
	return t
}

// tokOutput expands toks after prefix (the output of earlier blocks).
func tokOutput(prefix []byte, toks []tok) []byte {
	out := append([]byte(nil), prefix...)
	for _, t := range toks {
		if t.dist == 0 {
			out = append(out, t.lit)
			continue
		}
		for k := 0; k < t.len; k++ {
			out = append(out, out[len(out)-t.dist])
		}
	}
	return out
}

// RFC 1951 3.2.5 length and distance tables.
var (
	tLenBase   = [29]int{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	tLenExtra  = [29]uint{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	tDistBase  = [30]int{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	tDistExtra = [30]uint{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
)

// lenSym returns the litlen symbol index (0..28 => 257..285) for a length;
// 258 uses symbol 285.
func lenSym(l int) int {
	for i := 28; i >= 0; i-- {
		if tLenBase[i] <= l {
			return i
		}
	}
	panic("bad length")
}

func distSym(d int) int {
	for i := 29; i >= 0; i-- {
		if tDistBase[i] <= d {
			return i
		}
	}
	panic("bad distance")
}

type bitWriter struct {
	out []byte
	acc uint64
	n   uint
}

func (w *bitWriter) bits(v uint64, n uint) {
	w.acc |= (v & (1<<n - 1)) << w.n
	w.n += n
	for w.n >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}

func (w *bitWriter) align() {
	if w.n%8 != 0 {
		w.bits(0, 8-w.n%8)
	}
}

func (w *bitWriter) bytes() []byte {
	w.align()
	return w.out
}

// canonCodes returns the RFC 1951 3.2.2 canonical codes of lens.
func canonCodes(lens []uint8) []uint16 {
	var count [16]int
	for _, l := range lens {
		if l != 0 {
			count[l]++
		}
	}
	var next [16]int
	code := 0
	for b := 1; b < 16; b++ {
		code = (code + count[b-1]) << 1
		next[b] = code
	}
	codes := make([]uint16, len(lens))
	for i, l := range lens {
		if l != 0 {
			codes[i] = uint16(next[l])
			next[l]++
		}
	}
	return codes
}

// huff writes a Huffman codeword (most significant code bit first).
func (w *bitWriter) huff(code uint16, ln uint8) {
	w.bits(uint64(bits.Reverse16(code)>>(16-ln)), uint(ln))
}

type tcode struct {
	lit, off   []uint8
	litC, offC []uint16
}

func newTcode(litLens, offLens []uint8) *tcode {
	return &tcode{litLens, offLens, canonCodes(litLens), canonCodes(offLens)}
}

func (c *tcode) sym(w *bitWriter, s int) {
	if c.lit[s] == 0 {
		panic(fmt.Sprintf("litlen symbol %d has no codeword", s))
	}
	w.huff(c.litC[s], c.lit[s])
}

func (c *tcode) toks(w *bitWriter, toks []tok) {
	for _, t := range toks {
		if t.dist == 0 {
			c.sym(w, int(t.lit))
			continue
		}
		ls := lenSym(t.len)
		c.sym(w, 257+ls)
		w.bits(uint64(t.len-tLenBase[ls]), tLenExtra[ls])
		ds := distSym(t.dist)
		if c.off[ds] == 0 {
			panic(fmt.Sprintf("offset symbol %d has no codeword", ds))
		}
		w.huff(c.offC[ds], c.off[ds])
		w.bits(uint64(t.dist-tDistBase[ds]), tDistExtra[ds])
	}
	c.sym(w, 256)
}

func staticTcode() *tcode {
	l := make([]uint8, 288)
	for i := range l {
		switch {
		case i < 144:
			l[i] = 8
		case i < 256:
			l[i] = 9
		case i < 280:
			l[i] = 7
		default:
			l[i] = 8
		}
	}
	o := make([]uint8, 32)
	for i := range o {
		o[i] = 5
	}
	return newTcode(l, o)
}

func (w *bitWriter) header(final bool, btype uint64) {
	f := uint64(0)
	if final {
		f = 1
	}
	w.bits(f, 1)
	w.bits(btype, 2)
}

func (w *bitWriter) stored(final bool, data []byte) {
	w.header(final, 0)
	w.align()
	n := len(data)
	w.bits(uint64(n), 16)
	w.bits(uint64(^n&0xffff), 16)
	w.out = append(w.out, data...)
}

func (w *bitWriter) static(final bool, toks []tok) {
	w.header(final, 1)
	staticTcode().toks(w, toks)
}

// preItem is one precode symbol with its repeat-count extra bits.
type preItem struct {
	sym   int
	extra uint64
}

// rleLens encodes codeword lengths as precode items (RFC 1951 3.2.7).
func rleLens(lens []uint8) []preItem {
	var items []preItem
	for i := 0; i < len(lens); {
		v := lens[i]
		run := 1
		for i+run < len(lens) && lens[i+run] == v {
			run++
		}
		switch {
		case v == 0 && run >= 11:
			r := min(run, 138)
			items = append(items, preItem{18, uint64(r - 11)})
			i += r
		case v == 0 && run >= 3:
			r := min(run, 10)
			items = append(items, preItem{17, uint64(r - 3)})
			i += r
		case v != 0 && run >= 4:
			items = append(items, preItem{int(v), 0})
			r := min(run-1, 6)
			items = append(items, preItem{16, uint64(r - 3)})
			i += 1 + r
		default:
			items = append(items, preItem{int(v), 0})
			i++
		}
	}
	return items
}

// defaultPrecode is a complete precode: 13 lengths of 4 and 6 of 5.
func defaultPrecode() [19]uint8 {
	var p [19]uint8
	for i := range p {
		if i < 13 {
			p[i] = 4
		} else {
			p[i] = 5
		}
	}
	return p
}

// dynamicHeader writes a dynamic block header: hlit, hdist (counts),
// hclen (4..19 precode lens sent), the precode lens (by symbol) and items.
func (w *bitWriter) dynamicHeader(final bool, hlit, hdist, hclen int, pre [19]uint8, items []preItem) {
	w.header(final, 2)
	w.bits(uint64(hlit-257), 5)
	w.bits(uint64(hdist-1), 5)
	w.bits(uint64(hclen-4), 4)
	for i := 0; i < hclen; i++ {
		w.bits(uint64(pre[deflatePrecodeLensPermutation[i]]), 3)
	}
	pc := canonCodes(pre[:])
	for _, it := range items {
		if pre[it.sym] == 0 {
			panic("precode symbol without codeword")
		}
		w.huff(pc[it.sym], pre[it.sym])
		switch it.sym {
		case 16:
			w.bits(it.extra, 2)
		case 17:
			w.bits(it.extra, 3)
		case 18:
			w.bits(it.extra, 7)
		}
	}
}

// dynamic writes a dynamic Huffman block with the given code lengths.
func (w *bitWriter) dynamic(final bool, litLens, offLens []uint8, toks []tok) {
	all := append(append([]uint8(nil), litLens...), offLens...)
	w.dynamicHeader(final, len(litLens), len(offLens), 19, defaultPrecode(), rleLens(all))
	newTcode(litLens, offLens).toks(w, toks)
}

// kraftLens gives the symbols of used a complete code (lengths L-1 and L)
// among n symbols; the others get 0.
func kraftLens(n int, used []int) []uint8 {
	l := make([]uint8, n)
	m := len(used)
	if m == 1 {
		l[used[0]] = 1
		return l
	}
	L := bits.Len(uint(m - 1))
	short := 1<<L - m
	for i, s := range used {
		if i < short {
			l[s] = uint8(L - 1)
		} else {
			l[s] = uint8(L)
		}
	}
	return l
}

func seq(a, b int) []int {
	var s []int
	for i := a; i < b; i++ {
		s = append(s, i)
	}
	return s
}

// chainLens gives used[k] length k+1 and the last one the same length as
// the one before it (a complete code with codewords up to len(used)-1 bits).
func chainLens(n int, used []int) []uint8 {
	l := make([]uint8, n)
	for k, s := range used {
		l[s] = uint8(min(k+1, len(used)-1))
	}
	return l
}

// ---------------------------------------------------------------------------
// Golden streams.

type golden struct {
	name string
	in   []byte
	// res is the DecompressEx result with a dst of len(out)+64 bytes when
	// res is ok, else of goldenErrDst bytes; out and nIn are checked on
	// success.
	res libdeflateResult
	out []byte
	nIn int
}

const goldenErrDst = 1 << 16

func rndBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 7))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint64())
	}
	return b
}

func litsOf(b []byte) []tok {
	t := make([]tok, len(b))
	for i, c := range b {
		t[i] = lit(c)
	}
	return t
}

// overlapToks: a few literals, then many matches at distance d (lengths
// varying 3..258), enough to run in the fast loop and end in the generic one.
func overlapToks(d int, seed uint64) []tok {
	toks := litsOf(rndBytes(seed, d))
	r := rand.New(rand.NewPCG(seed, 9))
	for k := 0; k < 120; k++ {
		l := 3 + int(r.Uint64()%256)
		if k%7 == 0 {
			l = 258
		}
		toks = append(toks, match(l, d))
		if k%5 == 0 {
			toks = append(toks, lit(byte(k)))
			toks = append(toks, match(3, d+1))
		}
	}
	return toks
}

func goldenCases() []golden {
	var gs []golden
	ok := func(name string, in, out []byte) {
		gs = append(gs, golden{name, in, libdeflateSuccess, out, len(in)})
	}
	bad := func(name string, in []byte) {
		gs = append(gs, golden{name: name, in: in, res: libdeflateBadData})
	}
	var w *bitWriter
	nw := func() *bitWriter { w = &bitWriter{}; return w }

	// Stored blocks.
	nw().stored(true, nil)
	ok("stored-empty", w.bytes(), nil)
	nw().stored(true, []byte("hello"))
	ok("stored-hello", w.bytes(), []byte("hello"))
	nw().stored(true, rndBytes(1, 65535))
	ok("stored-65535", w.bytes(), rndBytes(1, 65535))
	bad("stored-len-nlen-mismatch", []byte{0x01, 0x05, 0x00, 0xfb, 0xff, 'h', 'e', 'l', 'l', 'o'})
	bad("stored-truncated-header", []byte{0x01, 0x05, 0x00, 0xfa})
	bad("stored-truncated-data", []byte{0x01, 0x05, 0x00, 0xfa, 0xff, 'h', 'e', 'l'})
	bad("empty-input", nil)
	bad("btype-3", []byte{0x07, 0x00})
	bad("btype-3-nonfinal", []byte{0x06, 0x00})

	// Static blocks.
	nw().static(true, nil)
	ok("static-empty", w.bytes(), nil)
	st := append(lits("abcabcabcX"), match(9, 10), match(258, 3), lit('!'))
	nw().static(true, st)
	ok("static-literals-match", w.bytes(), tokOutput(nil, st))
	nw().static(true, append(lits("ab"), match(258, 1)))
	ok("static-max-len-258", w.bytes(), tokOutput(nil, append(lits("ab"), match(258, 1))))
	{
		// Length 258 written as symbol 284 + 31 extra bits (227+31): not
		// produced by encoders, accepted by libdeflate.
		c := staticTcode()
		w := &bitWriter{}
		w.header(true, 1)
		c.sym(w, 'q')
		c.sym(w, 257+27)
		w.bits(31, 5)
		w.huff(c.offC[0], c.off[0]) // distance 1
		c.sym(w, 256)
		ok("static-len-284-plus-31", w.bytes(), bytes.Repeat([]byte{'q'}, 259))
	}

	// Max offset 32768: 32768 random literals, then far matches.
	far := litsOf(rndBytes(2, 32768))
	far = append(far, match(258, 32768), match(3, 32768), match(100, 32767), match(258, 32768))
	nw().static(true, far)
	ok("static-max-offset-32768", w.bytes(), tokOutput(nil, far))

	// Overlap cases in the fast loop and the generic loop.
	for _, d := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 15, 16, 17, 39, 40, 41} {
		t := overlapToks(d, uint64(d))
		nw().static(true, t)
		ok(fmt.Sprintf("static-overlap-offset-%d", d), w.bytes(), tokOutput(nil, t))
	}

	// Offset beyond the start of the output (generic loop and fast loop).
	nw().static(true, append(lits("a"), match(3, 2)))
	bad("offset-beyond-start-generic", w.bytes())
	nw().static(true, append(litsOf(rndBytes(3, 400)), match(10, 401), lit('x')))
	bad("offset-beyond-start-fastloop", w.bytes())

	// Dynamic blocks.
	dynLit := kraftLens(286, seq(0, 286))
	dynOff := kraftLens(30, seq(0, 30))
	dt := append(lits("dynamic huffman "), match(16, 16), match(4, 1), lit(0xff), match(30, 33))
	nw().dynamic(true, dynLit, dynOff, dt)
	ok("dynamic-basic", w.bytes(), tokOutput(nil, dt))

	// Deep codes: litlen and offset codewords up to 15 bits (subtables).
	deepUsed := []int{'e', 't', 256, 'a', 257, 'o', 258, 'i', 262, 'n', 'z', 285, 'q', 270, 'x', 'j'}
	deepLit := chainLens(286, deepUsed) // 'j' and 'x' get 15 bits
	deepOff := chainLens(30, []int{0, 1, 3, 5, 9, 12, 15, 17, 20, 22, 24, 25, 26, 27, 28, 29})
	var deepToks []tok
	// Lengths with codewords: 3, 4, 8, 23..26, 258; distances: 1, 2, 4,
	// 7..8, 25..32, 65..96, 193..256, 385..512, 1025..1536, 2049..3072,
	// 4097..8192, 8193..32768 (each iteration writes 307 bytes).
	for k := 0; k < 300; k++ {
		deepToks = append(deepToks, lits("jxqzetaoin")...)
		deepToks = append(deepToks, match(3, 1), match(258, 8), match(4, 4), match(8, 30), match(24, 2))
		if k > 20 {
			deepToks = append(deepToks, match(4, 1500), match(8, 5000), match(3, 70), match(258, 200), match(3, 400), match(4, 2500))
		}
		if k > 100 {
			deepToks = append(deepToks, match(3, 24577+k), match(258, 9000+k), match(24, 13000), match(3, 20000))
		}
	}
	nw().dynamic(true, deepLit, deepOff, deepToks)
	ok("dynamic-deep-subtables", w.bytes(), tokOutput(nil, deepToks))
	// HLIT = 288 and HDIST = 32 (more than RFC 1951's 286 / 30; libdeflate
	// accepts them).
	nw().dynamic(true, append(deepLit, 0, 0), append(deepOff, 0, 0), deepToks[:500])
	ok("dynamic-hlit-288-hdist-32", w.bytes(), tokOutput(nil, deepToks[:500]))

	// Multi-block streams, final flag in a later block; EOB in the fast
	// loop (large non-final blocks).
	big := litsOf(bytes.Repeat([]byte("the quick brown fox "), 100))
	big = append(big, match(258, 20), match(258, 40))
	{
		w := &bitWriter{}
		w.stored(false, []byte("stored "))
		w.static(false, big)
		w.dynamic(false, dynLit, dynOff, dt)
		w.static(false, nil)
		w.stored(false, nil)
		w.dynamic(true, deepLit, deepOff, deepToks[:200])
		want := []byte("stored ")
		want = tokOutput(want, big)
		want = tokOutput(want, dt)
		want = tokOutput(want, deepToks[:200])
		ok("multi-block-final-later", w.bytes(), want)
	}

	// Trailing bytes after the final block are not consumed.
	{
		w := &bitWriter{}
		w.static(true, st)
		s := w.bytes()
		gs = append(gs, golden{"trailing-bytes", append(append([]byte(nil), s...), "TRAILING-GARBAGE"...), libdeflateSuccess, tokOutput(nil, st), len(s)})
		w = &bitWriter{}
		w.stored(true, []byte("xyz"))
		s = w.bytes()
		gs = append(gs, golden{"trailing-bytes-stored", append(append([]byte(nil), s...), 0xff, 0xff), libdeflateSuccess, []byte("xyz"), len(s)})
		w = &bitWriter{}
		w.dynamic(true, deepLit, deepOff, deepToks)
		s = w.bytes()
		gs = append(gs, golden{"trailing-bytes-big", append(append([]byte(nil), s...), rndBytes(4, 100)...), libdeflateSuccess, tokOutput(nil, deepToks), len(s)})
	}

	// Truncated input.
	{
		w := &bitWriter{}
		w.dynamic(true, dynLit, dynOff, dt)
		s := w.bytes()
		bad("truncated-dynamic-half", s[:len(s)/2])
		bad("truncated-dynamic-header", s[:3])
		w = &bitWriter{}
		w.static(true, far)
		s = w.bytes()
		bad("truncated-static-big", s[:len(s)-12])
	}

	// Dynamic header edge cases.
	pre := defaultPrecode()
	{
		w := &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, pre, []preItem{{16, 0}})
		bad("presym16-at-i0", w.bytes())
	}
	{
		// 257 + 1 lens, but the last repeat overshoots the total.
		items := rleLens(kraftLens(257, seq(0, 257)))
		items = append(items, preItem{18, 127}) // 138 zeros for 1 remaining
		w := &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, pre, items)
		bad("too-many-lens", w.bytes())
	}
	{
		// Precode oversubscribed / incomplete.
		p := pre
		p[0] = 1
		p[1] = 1
		p[2] = 1
		w := &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, p, nil)
		bad("precode-oversubscribed", w.bytes())
		var q [19]uint8
		q[0], q[8] = 2, 2
		w = &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, q, []preItem{{8, 0}})
		bad("precode-incomplete", w.bytes())
	}
	{
		// Accepted incomplete codes: litlen code with only EOB (one 1-bit
		// codeword), empty offset code; litlen with literal 'A' and EOB
		// and a single 1-bit offset codeword used by a match.
		ll := make([]uint8, 257)
		ll[256] = 1
		ol := make([]uint8, 1)
		w := &bitWriter{}
		w.dynamic(true, ll, ol, nil)
		ok("litlen-single-eob-offset-empty", w.bytes(), nil)

		ll = make([]uint8, 258)
		ll['A'], ll[256], ll[257] = 1, 2, 2
		ol = make([]uint8, 2)
		ol[1] = 1 // only distance 2
		mt := append(lits("AA"), match(3, 2), lit('A'))
		w = &bitWriter{}
		w.dynamic(true, ll, ol, mt)
		ok("offset-single-codeword", w.bytes(), tokOutput(nil, mt))

		// Empty offset code with literals only.
		ll = kraftLens(257, seq(0, 257))
		w = &bitWriter{}
		w.dynamic(true, ll, make([]uint8, 1), lits("only literals"))
		ok("offset-empty-literals-only", w.bytes(), []byte("only literals"))

		// Rejected: litlen oversubscribed, litlen incomplete (2 codewords
		// of 2 bits), offset incomplete (one 2-bit codeword).
		ll = kraftLens(257, seq(0, 257))
		ll[0] = 1
		w = &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, pre, rleLens(append(ll, 1)))
		bad("litlen-oversubscribed", w.bytes())
		ll = make([]uint8, 257)
		ll['a'], ll[256] = 2, 2
		w = &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, pre, rleLens(append(ll, 1)))
		bad("litlen-incomplete", w.bytes())
		ll = kraftLens(257, seq(0, 257))
		w = &bitWriter{}
		w.dynamicHeader(true, 257, 2, 19, pre, rleLens(append(ll, 0, 2)))
		bad("offset-incomplete", w.bytes())
		// Litlen code without EOB is accepted as a code (decoding then
		// never ends: bad data once the input runs out).
		ll = make([]uint8, 257)
		ll['a'], ll['b'] = 1, 1
		w = &bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, pre, rleLens(append(ll, 0)))
		w.bits(0, 30)
		// The 1-bit codes decode zero bits as 'a' until more than 8
		// implicit zero bytes have been overread.
		bad("litlen-no-eob", w.bytes())
	}
	{
		// Offset symbols 30 and 31 (invalid per RFC) are decodable by
		// libdeflate (they have base 24577, 13 extra bits).
		ll := kraftLens(257+29, seq(0, 286))
		ol := kraftLens(32, seq(0, 32))
		t := append(litsOf(rndBytes(5, 30000)), lit('z'))
		w := &bitWriter{}
		w.dynamicHeader(true, 286, 32, 19, pre, rleLens(append(append([]uint8(nil), ll...), ol...)))
		c := newTcode(ll, ol)
		for _, x := range t {
			c.sym(w, int(x.lit))
		}
		c.sym(w, 257) // length 3
		w.huff(c.offC[30], c.off[30])
		w.bits(5, 13) // distance 24577 + 5
		c.sym(w, 256)
		want := tokOutput(nil, append(t, match(3, 24582)))
		ok("offset-symbol-30", w.bytes(), want)
	}
	return gs
}

// ---------------------------------------------------------------------------

func TestBitstreamConfig(t *testing.T) {
	if fastloopMaxBytesWritten != 299 || fastloopMaxBytesRead != 25 || preloadSlack != 1 ||
		consumableNbits != 56 || maxBitsleft != 63 {
		t.Fatalf("written %d read %d slack %d consumable %d max %d", fastloopMaxBytesWritten,
			fastloopMaxBytesRead, preloadSlack, consumableNbits, maxBitsleft)
	}
	// Every compile-time branch decompress_generic.go selected.
	checks := []struct {
		name string
		got  bool
		want bool
	}{
		{"header", canConsume(1 + 2 + 5 + 5 + 4 + 3), true},
		{"precode merged", canConsume(3 * (deflateNumPrecodeSyms - 1)), true},
		{"2 extra literals a", canConsumeAndThenPreload(2*litlenTablebits+lengthMaxbits, offsetTablebits), true},
		{"2 extra literals b", canConsumeAndThenPreload(2*litlenTablebits+deflateMaxLitlenCodewordLen, litlenTablebits), true},
		{"one literal", canConsumeAndThenPreload(litlenTablebits, litlenTablebits), true},
		{"subtable literal", canConsumeAndThenPreload(deflateMaxLitlenCodewordLen, litlenTablebits), true},
		{"subtable length", canConsumeAndThenPreload(lengthMaxbits, offsetTablebits), true},
		{"length then offset preload", canConsumeAndThenPreload(lengthMaxfastbits, offsetTablebits), true},
		{"64-bit offset", canConsumeAndThenPreload(offsetMaxbits, litlenTablebits), true},
		{"preload before copy", canConsumeAndThenPreload(max(offsetMaxbits-offsetTablebits, offsetMaxfastbits), litlenTablebits), true},
		{"generic length+offset", canConsume(lengthMaxbits + offsetMaxbits), true},
		{"generic offset", canConsume(offsetMaxbits), true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestDecompressGolden(t *testing.T) {
	d := NewDecompressor()
	for _, g := range goldenCases() {
		extra := 64
		dst := make([]byte, len(g.out)+extra)
		if g.res != libdeflateSuccess {
			dst = make([]byte, goldenErrDst)
		}
		nIn, nOut, err := d.DecompressEx(dst, g.in)
		if err != g.res.err() {
			t.Errorf("%s: DecompressEx err %v, want %v", g.name, err, g.res.err())
			continue
		}
		if g.res != libdeflateSuccess {
			continue
		}
		if nIn != g.nIn || nOut != len(g.out) || !bytes.Equal(dst[:nOut], g.out) {
			t.Errorf("%s: nIn %d nOut %d (want %d %d), output equal %v", g.name, nIn, nOut, g.nIn, len(g.out), bytes.Equal(dst[:nOut], g.out))
		}
		// Exact size with Decompress, and a fresh Decompressor.
		exact := make([]byte, len(g.out))
		n, err := NewDecompressor().Decompress(exact, g.in)
		if err != nil || n != len(g.out) || !bytes.Equal(exact, g.out) {
			t.Errorf("%s: Decompress exact: n %d err %v", g.name, n, err)
		}
		// Larger dst: Decompress reports short output, DecompressEx is ok.
		if _, err := d.Decompress(dst, g.in); err != ErrShortOutput {
			t.Errorf("%s: Decompress with %d extra bytes: %v, want ErrShortOutput", g.name, extra, err)
		}
		// One byte too small.
		if len(g.out) > 0 {
			small := make([]byte, len(g.out)-1)
			if _, err := d.Decompress(small, g.in); err != ErrInsufficientSpace {
				t.Errorf("%s: Decompress one byte short: %v", g.name, err)
			}
			if _, _, err := d.DecompressEx(small, g.in); err != ErrInsufficientSpace {
				t.Errorf("%s: DecompressEx one byte short: %v", g.name, err)
			}
		}
	}
}

// Golden streams also decode identically with stdlib's decoder (a second,
// independent oracle; malformed streams are not compared, since the
// decoders legitimately differ there, e.g. offset symbols 30/31).
func TestDecompressGoldenStdlib(t *testing.T) {
	for _, g := range goldenCases() {
		if g.res != libdeflateSuccess || g.name == "offset-symbol-30" || g.name == "static-len-284-plus-31" ||
			g.name == "dynamic-hlit-288-hdist-32" {
			continue
		}
		r := flate.NewReader(bytes.NewReader(g.in[:g.nIn]))
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r); err != nil {
			t.Errorf("%s: stdlib: %v", g.name, err)
			continue
		}
		if !bytes.Equal(buf.Bytes(), g.out) {
			t.Errorf("%s: stdlib output differs from the test oracle", g.name)
		}
	}
}

func TestDecompressDstSizes(t *testing.T) {
	d := NewDecompressor()
	for _, g := range goldenCases() {
		if g.res != libdeflateSuccess {
			continue
		}
		for _, n := range []int{0, len(g.out) - 1, len(g.out), len(g.out) + 1, len(g.out) + 300, 3*len(g.out) + 1000} {
			if n < 0 {
				continue
			}
			dst := make([]byte, n)
			nIn, nOut, err := d.DecompressEx(dst, g.in)
			nE, errE := d.Decompress(dst, g.in)
			switch {
			case n < len(g.out):
				if err != ErrInsufficientSpace || errE != ErrInsufficientSpace || nIn != 0 || nOut != 0 || nE != 0 {
					t.Errorf("%s dst %d: Ex %v %d %d, exact %v", g.name, n, err, nIn, nOut, errE)
				}
			case n == len(g.out):
				if err != nil || errE != nil || nE != n || nOut != n || nIn != g.nIn {
					t.Errorf("%s dst %d: Ex %v, exact %v", g.name, n, err, errE)
				}
			default:
				if err != nil || nOut != len(g.out) || nIn != g.nIn || !bytes.Equal(dst[:nOut], g.out) || errE != ErrShortOutput {
					t.Errorf("%s dst %d: Ex %v %d %d, exact %v", g.name, n, err, nIn, nOut, errE)
				}
			}
		}
	}
}

// The internal result codes map 1:1 to the error values.
func TestResultErrors(t *testing.T) {
	want := map[libdeflateResult]error{
		libdeflateSuccess: nil, libdeflateBadData: ErrBadData,
		libdeflateShortOutput: ErrShortOutput, libdeflateInsufficientSpace: ErrInsufficientSpace,
	}
	for r, e := range want {
		if r.err() != e {
			t.Errorf("result %d: %v, want %v", r, r.err(), e)
		}
	}
	if errors.Is(ErrBadData, ErrShortOutput) || ErrBadData == ErrInsufficientSpace {
		t.Error("error values not distinct")
	}
}

// ---------------------------------------------------------------------------
// Round trips.

// roundTrip decodes comp with Decompress (exact) and DecompressEx (with
// trailing input bytes and a larger dst) and checks the result.
func roundTrip(t *testing.T, d *Decompressor, name string, orig, comp []byte, buf *[]byte) {
	t.Helper()
	need := len(orig) + 1000
	if cap(*buf) < need {
		*buf = make([]byte, need)
	}
	dst := (*buf)[:len(orig)]
	n, err := d.Decompress(dst, comp)
	if err != nil || n != len(orig) || !bytes.Equal(dst, orig) {
		t.Fatalf("%s: Decompress: n %d err %v, equal %v", name, n, err, bytes.Equal(dst[:n], orig))
	}
	dst = (*buf)[:need]
	nIn, nOut, err := d.DecompressEx(dst, comp)
	if err != nil || nIn != len(comp) || nOut != len(orig) || !bytes.Equal(dst[:nOut], orig) {
		t.Fatalf("%s: DecompressEx: nIn %d/%d nOut %d/%d err %v", name, nIn, len(comp), nOut, len(orig), err)
	}
}

func stdlibCompress(t testing.TB, data []byte, level int) []byte {
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var stdlibLevels = []int{-2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

func TestRoundTripLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	d := NewDecompressor()
	var buf []byte
	for _, c := range deflatetest.Cases(testing.Short()) {
		// No goroutines here: under -race a small-input smoke suffices.
		if raceEnabled && len(c.Data) > 20000 {
			continue
		}
		for _, lvl := range deflatetest.Levels() {
			comp := creftest.LDCompress(t, c.Data, lvl, creftest.LDRaw)
			roundTrip(t, d, fmt.Sprintf("%s/ld%d", c.Name, lvl), c.Data, comp, &buf)
		}
	}
}

func TestRoundTripStdlib(t *testing.T) {
	d := NewDecompressor()
	var buf []byte
	for _, c := range deflatetest.Cases(testing.Short()) {
		// No goroutines here: under -race a small-input smoke suffices.
		if raceEnabled && len(c.Data) > 20000 {
			continue
		}
		for _, lvl := range stdlibLevels {
			comp := stdlibCompress(t, c.Data, lvl)
			roundTrip(t, d, fmt.Sprintf("%s/std%d", c.Name, lvl), c.Data, comp, &buf)
		}
	}
}

// Silesia files (skipped when the corpus is missing). -short: files up to
// 10 MiB at a few levels, plus independent 128 KiB blocks of one file.
func TestRoundTripSilesia(t *testing.T) {
	// Single-goroutine corpus sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("corpus sweep: no goroutines, runs in the normal lane")
	}
	files := deflatetest.Silesia(t)
	creftest.LDBin(t)
	d := NewDecompressor()
	var buf []byte
	ldLevels := deflatetest.Levels()
	stdLevels := stdlibLevels
	if testing.Short() {
		ldLevels = []int{1, 6}
		stdLevels = []int{6}
	}
	for _, f := range files {
		if f.Size > 64<<20 || (testing.Short() && f.Size > 10<<20) {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			data := f.Load(t)
			for _, lvl := range ldLevels {
				roundTrip(t, d, fmt.Sprintf("%s/ld%d", f.Name, lvl), data, creftest.LDCompress(t, data, lvl, creftest.LDRaw), &buf)
			}
			for _, lvl := range stdLevels {
				roundTrip(t, d, fmt.Sprintf("%s/std%d", f.Name, lvl), data, stdlibCompress(t, data, lvl), &buf)
			}
		})
	}
	// Independent blocks.
	data := files[0].Load(t)
	if testing.Short() {
		data = data[:min(len(data), 2<<20)]
	}
	for _, size := range []int{1 << 20, 128 << 10, 16 << 10} {
		blocks := deflatetest.Blocks(data, size)
		for i, b := range blocks {
			if testing.Short() && i%4 != 0 {
				continue
			}
			for _, lvl := range []int{0, 1, 6, 9, 12} {
				roundTrip(t, d, fmt.Sprintf("%s/block%d-%d/ld%d", files[0].Name, size, i, lvl), b, creftest.LDCompress(t, b, lvl, creftest.LDRaw), &buf)
			}
			roundTrip(t, d, fmt.Sprintf("%s/block%d-%d/std", files[0].Name, size, i), b, stdlibCompress(t, b, 6), &buf)
		}
	}
}

// ---------------------------------------------------------------------------

func TestDecompressNoAlloc(t *testing.T) {
	var w bitWriter
	w.stored(false, []byte("stored"))
	w.static(false, overlapToks(3, 3))
	w.dynamic(true, kraftLens(286, seq(0, 286)), kraftLens(30, seq(0, 30)), overlapToks(9, 9))
	in := w.bytes()
	d := NewDecompressor()
	dst := make([]byte, 1<<20)
	_, nOut, err := d.DecompressEx(dst, in)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, nOut)
	allocs := testing.AllocsPerRun(100, func() {
		if _, _, err := d.DecompressEx(dst, in); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Decompress(out, in); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Decompress(out[:1], in); err != ErrInsufficientSpace {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocs per run, want 0", allocs)
	}
}

// A Decompressor reused across streams (as from a pool), including after
// errors and with static blocks between dynamic ones, gives the same results
// as a fresh one.
func TestDecompressorReuse(t *testing.T) {
	gs := goldenCases()
	shared := NewDecompressor()
	for round := 0; round < 3; round++ {
		for i := range gs {
			g := gs[(i*7+round)%len(gs)]
			dst1 := make([]byte, max(len(g.out)+64, goldenErrDst))
			dst2 := make([]byte, len(dst1))
			a1, b1, e1 := shared.DecompressEx(dst1, g.in)
			a2, b2, e2 := NewDecompressor().DecompressEx(dst2, g.in)
			if a1 != a2 || b1 != b2 || e1 != e2 || !bytes.Equal(dst1[:b1], dst2[:b2]) {
				t.Fatalf("%s: reused (%d %d %v) vs fresh (%d %d %v)", g.name, a1, b1, e1, a2, b2, e2)
			}
		}
	}
}
