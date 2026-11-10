package deflate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// ---------------------------------------------------------------------------
// ldref dump-tables plumbing (framing documented in ref/ldref/ldref.c above
// cmd_dump_tables).

// precodeResult is one precode table build.
type precodeResult struct {
	ok    bool
	table [precodeEnough]uint32
}

// litlenRec is one litlen+offset lens record; litlenResult the two builds.
type litlenRec struct {
	lit, off []uint8
}

type litlenResult struct {
	okLit     bool
	tablebits uint32
	lit       [litlenEnough]uint32
	okOff     bool
	off       [offsetEnough]uint32
}

// ldParser walks ldref dump-tables output.
type ldParser struct {
	t   testing.TB
	out []byte
}

func (p *ldParser) table(kind string, dst []uint32) (ok bool, tablebits uint32) {
	p.t.Helper()
	nl := bytes.IndexByte(p.out, '\n')
	if nl < 0 {
		p.t.Fatalf("ldref dump-tables: missing header for %s", kind)
	}
	line := string(p.out[:nl])
	p.out = p.out[nl+1:]
	var okInt int
	var k string
	var err error
	if kind == "litlen" {
		_, err = fmt.Sscanf(line, "ok=%d kind=%s tablebits=%d", &okInt, &k, &tablebits)
	} else {
		_, err = fmt.Sscanf(line, "ok=%d kind=%s", &okInt, &k)
	}
	if err != nil || k != kind || okInt > 1 {
		p.t.Fatalf("ldref dump-tables: bad header %q (want kind %s): %v", line, kind, err)
	}
	if len(p.out) < 4*len(dst) {
		p.t.Fatalf("ldref dump-tables: short table for %s", kind)
	}
	for i := range dst {
		dst[i] = binary.LittleEndian.Uint32(p.out[4*i:])
	}
	p.out = p.out[4*len(dst):]
	return okInt == 1, tablebits
}

func (p *ldParser) litlen() litlenResult {
	p.t.Helper()
	var r litlenResult
	r.okLit, r.tablebits = p.table("litlen", r.lit[:])
	r.okOff, _ = p.table("offset", r.off[:])
	return r
}

func (p *ldParser) done() {
	p.t.Helper()
	if len(p.out) != 0 {
		p.t.Fatalf("ldref dump-tables: %d trailing bytes", len(p.out))
	}
}

func ldPrecode(t testing.TB, recs [][deflateNumPrecodeSyms]uint8) []precodeResult {
	t.Helper()
	in := make([]byte, 0, len(recs)*deflateNumPrecodeSyms)
	for i := range recs {
		in = append(in, recs[i][:]...)
	}
	p := &ldParser{t: t, out: creftest.LDRun(t, in, "dump-tables", "-kind", "precode")}
	res := make([]precodeResult, len(recs))
	for i := range res {
		res[i].ok, _ = p.table("precode", res[i].table[:])
	}
	p.done()
	return res
}

func ldLitlen(t testing.TB, recs []litlenRec) []litlenResult {
	t.Helper()
	var in []byte
	for _, r := range recs {
		in = binary.LittleEndian.AppendUint16(in, uint16(len(r.lit)))
		in = append(in, r.lit...)
		in = binary.LittleEndian.AppendUint16(in, uint16(len(r.off)))
		in = append(in, r.off...)
	}
	p := &ldParser{t: t, out: creftest.LDRun(t, in, "dump-tables", "-kind", "litlen")}
	res := make([]litlenResult, len(recs))
	for i := range res {
		res[i] = p.litlen()
	}
	p.done()
	return res
}

func ldStatic(t testing.TB) litlenResult {
	t.Helper()
	p := &ldParser{t: t, out: creftest.LDRun(t, nil, "dump-tables", "-kind", "static")}
	r := p.litlen()
	p.done()
	return r
}

// ---------------------------------------------------------------------------
// Go side, set up like ldref does.

// unionImage is C's litlen_decode_table before a build: in C it overlays
// u.l.lens (then zero padding and the zeroed precode table), so the entries
// the build leaves alone show the lens bytes. Go keeps lens separately (see
// Decompressor), so the test preloads this image to compare whole arrays,
// which also checks that Go writes exactly the entries C writes.
func unionImage(d *Decompressor) {
	clear(d.litlenDecodeTable[:])
	for i, b := range d.lens {
		d.litlenDecodeTable[i/4] |= uint32(b) << (8 * (i % 4))
	}
}

func goPrecode(lens *[deflateNumPrecodeSyms]uint8) precodeResult {
	d := new(Decompressor)
	copy(d.lens[:], lens[:])
	ok := d.buildPrecodeDecodeTable()
	return precodeResult{ok: ok, table: d.precodeDecodeTable}
}

// goBuildLitlenOffset builds the offset table then the litlen table (the
// decoder's order) from the lens already in d.
func goBuildLitlenOffset(d *Decompressor, nl, no uint32) litlenResult {
	unionImage(d)
	okOff := d.buildOffsetDecodeTable(nl, no)
	okLit := d.buildLitlenDecodeTable(nl, no)
	return litlenResult{okLit: okLit, tablebits: d.litlenTablebits, lit: d.litlenDecodeTable,
		okOff: okOff, off: d.offsetDecodeTable}
}

func goLitlen(r litlenRec) litlenResult {
	d := new(Decompressor)
	copy(d.lens[:], r.lit)
	copy(d.lens[len(r.lit):], r.off)
	return goBuildLitlenOffset(d, uint32(len(r.lit)), uint32(len(r.off)))
}

func goStatic() (litlenResult, *Decompressor) {
	d := new(Decompressor)
	loaded, nl, no := d.loadStaticCodes()
	if loaded {
		panic("fresh decompressor reports static codes loaded")
	}
	return goBuildLitlenOffset(d, nl, no), d
}

// ---------------------------------------------------------------------------
// Independent oracle: RFC 1951 canonical codes, decoded through the table.

// expectOK says whether libdeflate accepts lens: a complete code, an empty
// code, or a single codeword of length 1.
func expectOK(lens []uint8) bool {
	var kraft uint32 // in units of 2^-15
	n, maxLen := 0, uint8(0)
	for _, l := range lens {
		if l != 0 {
			kraft += 1 << (15 - l)
			n++
			maxLen = max(maxLen, l)
		}
	}
	return kraft == 1<<15 || n == 0 || (n == 1 && maxLen == 1)
}

// usedMax is the longest codeword length, at least 1.
func usedMax(lens []uint8) uint32 {
	m := uint32(1)
	for _, l := range lens {
		m = max(m, uint32(l))
	}
	return m
}

// checkTable verifies a table that the builder accepted against lens, without
// using the builder's algorithm: every codeword decodes (through a subtable
// pointer where needed) to an entry equal to results[sym] plus the remaining
// length, for every value of the bits beyond the codeword; for the accepted
// incomplete codes every main entry is the 1-bit entry of the one symbol
// (or of symbol 0 when the code is empty).
func checkTable(t testing.TB, name string, table []uint32, tb uint32, lens []uint8, results []uint32) {
	t.Helper()
	var blCount [16]uint32
	n, used := 0, 0
	var kraft uint32
	for s, l := range lens {
		if l != 0 {
			blCount[l]++
			n++
			used = s
			kraft += 1 << (15 - l)
		}
	}
	if kraft != 1<<15 {
		sym := 0
		if n == 1 {
			sym = used
		}
		want := results[sym] + 1<<8 + 1
		for i := uint32(0); i < 1<<tb; i++ {
			if table[i] != want {
				t.Fatalf("%s: incomplete code entry %d = %#x, want %#x", name, i, table[i], want)
			}
		}
		return
	}
	var next [16]uint32
	code := uint32(0)
	for b := 1; b <= 15; b++ {
		code = (code + blCount[b-1]) << 1
		next[b] = code
	}
	for s, l8 := range lens {
		if l8 == 0 {
			continue
		}
		l := uint32(l8)
		c := next[l]
		next[l]++
		r := bits.Reverse32(c) >> (32 - l)
		if l <= tb {
			want := results[s] + l<<8 + l
			for x := uint32(0); x < 1<<(tb-l); x++ {
				if got := table[r|x<<l]; got != want {
					t.Fatalf("%s: sym %d len %d index %d = %#x, want %#x", name, s, l, r|x<<l, got, want)
				}
			}
			continue
		}
		p := table[r&(1<<tb-1)]
		if p&(huffdecExceptional|huffdecSubtablePointer) != huffdecExceptional|huffdecSubtablePointer ||
			p&0xF != tb || p&huffdecLiteral != 0 {
			t.Fatalf("%s: sym %d len %d: main entry %#x is not a subtable pointer", name, s, l, p)
		}
		start, sb, rem := p>>16, (p>>8)&0x3F, l-tb
		if rem > sb {
			t.Fatalf("%s: sym %d len %d: subtable bits %d < %d", name, s, l, sb, rem)
		}
		want := results[s] + rem<<8 + rem
		for x := uint32(0); x < 1<<(sb-rem); x++ {
			idx := start + (r>>tb | x<<rem)
			if int(idx) >= len(table) || table[idx] != want {
				t.Fatalf("%s: sym %d len %d subtable index %d: want %#x", name, s, l, idx, want)
			}
		}
	}
}

// checkPrecode / checkLitlen apply the oracle to a Go result.
func checkPrecode(t testing.TB, name string, lens []uint8, r precodeResult) {
	t.Helper()
	if r.ok != expectOK(lens) {
		t.Fatalf("%s: precode ok=%v, oracle says %v (lens %v)", name, r.ok, expectOK(lens), lens)
	}
	if r.ok {
		checkTable(t, name+"/precode", r.table[:], precodeTablebits, lens, precodeDecodeResults[:])
	}
}

func checkLitlen(t testing.TB, name string, rec litlenRec, r litlenResult) {
	t.Helper()
	if r.okLit != expectOK(rec.lit) {
		t.Fatalf("%s: litlen ok=%v, oracle says %v (lens %v)", name, r.okLit, expectOK(rec.lit), rec.lit)
	}
	if want := min(litlenTablebits, usedMax(rec.lit)); r.tablebits != want {
		t.Fatalf("%s: litlen tablebits %d, want %d", name, r.tablebits, want)
	}
	if r.okLit {
		checkTable(t, name+"/litlen", r.lit[:], r.tablebits, rec.lit, litlenDecodeResults[:])
	}
	if r.okOff != expectOK(rec.off) {
		t.Fatalf("%s: offset ok=%v, oracle says %v (lens %v)", name, r.okOff, expectOK(rec.off), rec.off)
	}
	if r.okOff {
		checkTable(t, name+"/offset", r.off[:], offsetTablebits, rec.off, offsetDecodeResults[:])
	}
}

func diffTables(t testing.TB, name string, got, want []uint32) {
	t.Helper()
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: entry %d: go %#08x, libdeflate %#08x", name, i, got[i], want[i])
		}
	}
}

func compareLitlen(t testing.TB, name string, g, c litlenResult) {
	t.Helper()
	if g.okLit != c.okLit || g.tablebits != c.tablebits || g.okOff != c.okOff {
		t.Fatalf("%s: go ok=%v/%v tablebits=%d, libdeflate ok=%v/%v tablebits=%d",
			name, g.okLit, g.okOff, g.tablebits, c.okLit, c.okOff, c.tablebits)
	}
	if g.lit != c.lit {
		diffTables(t, name+"/litlen", g.lit[:], c.lit[:])
	}
	if g.off != c.off {
		diffTables(t, name+"/offset", g.off[:], c.off[:])
	}
}

func comparePrecode(t testing.TB, name string, g, c precodeResult) {
	t.Helper()
	if g.ok != c.ok {
		t.Fatalf("%s: go ok=%v, libdeflate ok=%v", name, g.ok, c.ok)
	}
	if g.table != c.table {
		diffTables(t, name, g.table[:], c.table[:])
	}
}

// ---------------------------------------------------------------------------
// Static codes.

// staticHash is sha256 over the litlen then offset table (LE u32) built for
// the static codes, as dumped by ldref dump-tables -kind static (libdeflate
// v1.26). It pins the static tables when ldref is not built.
const staticHash = "6ba93ab468363b51a0051f191366e342a3cc238e41ffe4aa56a6ae987d05032f"

func tablesHash(r litlenResult) string {
	h := sha256.New()
	_ = binary.Write(h, binary.LittleEndian, r.lit[:])
	_ = binary.Write(h, binary.LittleEndian, r.off[:])
	return hex.EncodeToString(h.Sum(nil))
}

func staticLens() litlenRec {
	d := new(Decompressor)
	d.loadStaticCodes()
	return litlenRec{lit: d.lens[:288:288], off: d.lens[288:320:320]}
}

func TestStaticTablesGolden(t *testing.T) {
	g, _ := goStatic()
	if !g.okLit || !g.okOff || g.tablebits != 9 {
		t.Fatalf("static: ok=%v/%v tablebits=%d", g.okLit, g.okOff, g.tablebits)
	}
	checkLitlen(t, "static", staticLens(), g)
	// Spot checks from RFC 1951 3.2.6: literal 0 is 00110000 (8 bits), bit
	// reversed index 0b00001100; end of block is 0000000 (7 bits); length
	// symbol 285 (258) is 11000101 (8 bits); offset 0 is 00000 (5 bits).
	if got, want := g.lit[0b00001100], uint32(huffdecLiteral|8<<8|8); got != want {
		t.Errorf("static literal 0 entry %#x, want %#x", got, want)
	}
	if got, want := g.lit[0], uint32(huffdecExceptional|huffdecEndOfBlock|7<<8|7); got != want {
		t.Errorf("static end of block entry %#x, want %#x", got, want)
	}
	if got, want := g.lit[0b10100011], uint32(258<<16|8<<8|8); got != want {
		t.Errorf("static length 258 entry %#x, want %#x", got, want)
	}
	if got, want := g.off[0], uint32(1<<16|5<<8|5); got != want {
		t.Errorf("static offset 0 entry %#x, want %#x", got, want)
	}
	if h := tablesHash(g); h != staticHash {
		t.Errorf("static tables sha256 %s, want %s", h, staticHash)
	}
}

func TestStaticTablesMatchLibdeflate(t *testing.T) {
	g, _ := goStatic()
	compareLitlen(t, "static", g, ldStatic(t))
}

func TestLoadStaticCodesFlag(t *testing.T) {
	_, d := goStatic()
	before := *d
	loaded, nl, no := d.loadStaticCodes()
	if !loaded || nl != 0 || no != 0 {
		t.Fatalf("second loadStaticCodes = %v, %d, %d; want true, 0, 0", loaded, nl, no)
	}
	if *d != before {
		t.Fatal("loadStaticCodes changed the decompressor when the codes were loaded")
	}
	d.staticCodesLoaded = false // what a dynamic block does
	if loaded, nl, no = d.loadStaticCodes(); loaded || nl != 288 || no != 32 || !d.staticCodesLoaded {
		t.Fatalf("loadStaticCodes after reset = %v, %d, %d (flag %v)", loaded, nl, no, d.staticCodesLoaded)
	}
}

// ---------------------------------------------------------------------------
// Hand-made codes.

type unitCase struct {
	name    string
	precode bool
	pre     [deflateNumPrecodeSyms]uint8
	rec     litlenRec
	okLit   bool // expected result (litlen or precode)
	okOff   bool
}

func lensOf(n int, set map[int]uint8) []uint8 {
	l := make([]uint8, n)
	for s, v := range set {
		l[s] = v
	}
	return l
}

func fill(n int, v uint8) []uint8 { return bytes.Repeat([]byte{v}, n) }

// deep is the complete code 1, 2, ..., max-1, max, max on the symbols
// first, first+1, ...: it reaches the maximum length and, for max >
// TABLEBITS, needs subtables.
func deep(n, first, maxLen int) []uint8 {
	l := make([]uint8, n)
	for i := 1; i <= maxLen; i++ {
		l[first+i-1] = uint8(i)
	}
	l[first+maxLen] = uint8(maxLen)
	return l
}

func unitCases() []unitCase {
	var pre1, pre2, preAll7, preFlat, preDeep, preInc, preOver, preLen2 [deflateNumPrecodeSyms]uint8
	pre1[5] = 1
	pre2[0], pre2[18] = 1, 1
	for i := range preAll7 {
		preAll7[i] = 7
	}
	for i := 0; i < 16; i++ { // 16 codewords of 4 bits
		preFlat[i] = 4
	}
	copy(preDeep[:], deep(19, 2, 7))
	preInc[3], preInc[4] = 1, 2 // 3/4 of the codespace
	preOver[0], preOver[1], preOver[2] = 1, 1, 1
	preLen2[7] = 2 // a single codeword of length 2: rejected

	// A complete litlen code needing subtables: literals 0..255 get 9 bits
	// (half the codespace), symbols 256..270 get 2, 3, ..., 15, 15 bits (the
	// other half), so codewords of 12..15 bits share 11-bit prefixes.
	sub := make([]uint8, 288)
	for i := 0; i < 256; i++ {
		sub[i] = 9
	}
	for i := 2; i <= 15; i++ {
		sub[256+i-2] = uint8(i)
	}
	sub[256+14] = 15

	// Offset codes: deep 1..15,15 (subtables for 9..15 bits) and flat 5.
	offDeep := deep(32, 0, 15)
	offFlat := fill(32, 5)
	offSingle1 := lensOf(30, map[int]uint8{29: 1})
	offSingle2 := lensOf(30, map[int]uint8{3: 2})
	offTwo := lensOf(2, map[int]uint8{0: 1, 1: 1})

	return []unitCase{
		{name: "precode/single-len1", precode: true, pre: pre1, okLit: true},
		{name: "precode/two-codewords", precode: true, pre: pre2, okLit: true},
		{name: "precode/empty", precode: true, okLit: true},
		{name: "precode/single-len2", precode: true, pre: preLen2},
		{name: "precode/all-7", precode: true, pre: preAll7}, // 19 * 2^-7: incomplete
		{name: "precode/flat-4", precode: true, pre: preFlat, okLit: true},
		{name: "precode/deep-7", precode: true, pre: preDeep, okLit: true},
		{name: "precode/incomplete", precode: true, pre: preInc},
		{name: "precode/oversubscribed", precode: true, pre: preOver},

		{name: "litlen/single-len1+offset-single-len1", rec: litlenRec{lensOf(257, map[int]uint8{65: 1}), offSingle1}, okLit: true, okOff: true},
		{name: "litlen/single-len2+offset-single-len2", rec: litlenRec{lensOf(257, map[int]uint8{256: 2}), offSingle2}},
		{name: "litlen/two-codewords+offset-two", rec: litlenRec{lensOf(288, map[int]uint8{0: 1, 287: 1}), offTwo}, okLit: true, okOff: true},
		{name: "litlen/empty+offset-empty-1", rec: litlenRec{fill(257, 0), fill(1, 0)}, okLit: true, okOff: true},
		{name: "litlen/empty+offset-empty-32", rec: litlenRec{fill(288, 0), fill(32, 0)}, okLit: true, okOff: true},
		{name: "litlen/no-syms+offset-no-syms", rec: litlenRec{nil, nil}, okLit: true, okOff: true},
		{name: "litlen/all-15+offset-all-15", rec: litlenRec{fill(288, 15), fill(32, 15)}}, // incomplete
		{name: "litlen/all-9+offset-flat-5", rec: litlenRec{append(fill(256, 9), fill(32, 0)...), offFlat}, okLit: false, okOff: true},
		{name: "litlen/deep-15+offset-deep-15", rec: litlenRec{deep(288, 100, 15), offDeep}, okLit: true, okOff: true},
		{name: "litlen/subtables+offset-flat", rec: litlenRec{sub, offFlat}, okLit: true, okOff: true},
		{name: "litlen/deep-11+offset-deep-8", rec: litlenRec{deep(257, 0, 11), deep(32, 3, 8)}, okLit: true, okOff: true},
		{name: "litlen/deep-12+offset-deep-9", rec: litlenRec{deep(257, 0, 12), deep(32, 3, 9)}, okLit: true, okOff: true},
		{name: "litlen/incomplete+offset-incomplete", rec: litlenRec{lensOf(257, map[int]uint8{0: 1, 1: 2}), lensOf(32, map[int]uint8{0: 2, 1: 2, 2: 2})}},
		{name: "litlen/oversubscribed+offset-oversubscribed", rec: litlenRec{lensOf(257, map[int]uint8{0: 1, 1: 1, 2: 1}), lensOf(32, map[int]uint8{0: 1, 1: 2, 2: 2, 3: 2})}},
		{name: "litlen/oversubscribed-15+offset-oversubscribed-15", rec: litlenRec{append(deep(288, 0, 15)[:287:287], 15), append(deep(32, 0, 15)[:31:31], 15)}},
	}
}

func TestBuildDecodeTablesUnit(t *testing.T) {
	for _, c := range unitCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.precode {
				g := goPrecode(&c.pre)
				if g.ok != c.okLit {
					t.Fatalf("ok=%v, want %v", g.ok, c.okLit)
				}
				checkPrecode(t, c.name, c.pre[:], g)
				return
			}
			g := goLitlen(c.rec)
			if g.okLit != c.okLit || g.okOff != c.okOff {
				t.Fatalf("ok=%v/%v, want %v/%v", g.okLit, g.okOff, c.okLit, c.okOff)
			}
			checkLitlen(t, c.name, c.rec, g)
		})
	}
}

// TestBuildDecodeTablesUnitSpecials pins the special cases of
// build_decode_table / build_offset_decode_table entry by entry.
func TestBuildDecodeTablesUnitSpecials(t *testing.T) {
	// Single litlen codeword of length 1: tablebits 1, both entries map to it.
	g := goLitlen(litlenRec{lensOf(257, map[int]uint8{65: 1}), fill(1, 0)})
	want := uint32(huffdecLiteral | 65<<16 | 1<<8 | 1)
	if g.tablebits != 1 || g.lit[0] != want || g.lit[1] != want {
		t.Fatalf("single codeword: tablebits %d entries %#x %#x, want 1 %#x", g.tablebits, g.lit[0], g.lit[1], want)
	}
	// Empty offset code: all 256 main entries are symbol 0 with length 1.
	wantOff := offsetDecodeResults[0] + 1<<8 + 1
	for i := 0; i < 1<<offsetTablebits; i++ {
		if g.off[i] != wantOff {
			t.Fatalf("empty offset code: entry %d = %#x, want %#x", i, g.off[i], wantOff)
		}
	}
	for i := 1 << offsetTablebits; i < offsetEnough; i++ {
		if g.off[i] != 0 {
			t.Fatalf("empty offset code: entry %d beyond the main table written", i)
		}
	}
	// A rejected code writes nothing.
	d := new(Decompressor)
	for i := range d.offsetDecodeTable {
		d.offsetDecodeTable[i] = 0xdeadbeef
	}
	d.lens[0], d.lens[1], d.lens[2] = 1, 1, 1
	if d.buildOffsetDecodeTable(0, 3) {
		t.Fatal("over-subscribed offset code accepted")
	}
	d.lens[0], d.lens[1], d.lens[2] = 1, 2, 0
	if d.buildOffsetDecodeTable(0, 3) {
		t.Fatal("incomplete offset code accepted")
	}
	for i, v := range d.offsetDecodeTable {
		if v != 0xdeadbeef {
			t.Fatalf("rejected code wrote entry %d", i)
		}
	}
}

// TestBuildDecodeTableLen16Panics pins the invariant the sort loop's mask
// (l & deflateMaxCodewordLen) relies on: a length above
// deflateMaxCodewordLen panics in the count loop, before any masked use.
func TestBuildDecodeTableLen16Panics(t *testing.T) {
	d := new(Decompressor)
	lens := make([]uint8, 288)
	lens[0], lens[1] = 1, deflateMaxCodewordLen+1
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("length 16 accepted, want a panic")
		}
		if e, ok := r.(runtime.Error); !ok || !strings.Contains(e.Error(), "index out of range") {
			t.Fatalf("panic %v, want a runtime index out of range error", r)
		}
	}()
	buildDecodeTable(d.litlenDecodeTable[:], lens, 288, litlenDecodeResults[:],
		litlenTablebits, deflateMaxLitlenCodewordLen, d.sortedSyms[:], nil)
}

func TestBuildDecodeTablesUnitMatchLibdeflate(t *testing.T) {
	cases := unitCases()
	var pres [][deflateNumPrecodeSyms]uint8
	var recs []litlenRec
	for _, c := range cases {
		if c.precode {
			pres = append(pres, c.pre)
		} else {
			recs = append(recs, c.rec)
		}
	}
	cp := ldPrecode(t, pres)
	cl := ldLitlen(t, recs)
	ip, il := 0, 0
	for _, c := range cases {
		if c.precode {
			comparePrecode(t, c.name, goPrecode(&c.pre), cp[ip])
			ip++
		} else {
			compareLitlen(t, c.name, goLitlen(c.rec), cl[il])
			il++
		}
	}
}

func TestBuildDecodeTablesNoAlloc(t *testing.T) {
	d := new(Decompressor)
	rec := litlenRec{deep(288, 100, 15), deep(32, 0, 15)}
	copy(d.lens[:], rec.lit)
	copy(d.lens[288:], rec.off)
	// The precode build gets its own Decompressor with a real (complete,
	// non-empty) precode, since lens[:19] of d is all zero.
	dp := new(Decompressor)
	copy(dp.lens[:], deep(19, 2, 7))
	ok := true
	n := testing.AllocsPerRun(100, func() {
		d.staticCodesLoaded = false
		d.loadStaticCodes()
		ok = ok && d.buildOffsetDecodeTable(288, 32) && d.buildLitlenDecodeTable(288, 32)
		copy(d.lens[:], rec.lit) // back to the deep (subtable) code
		copy(d.lens[288:], rec.off)
		ok = ok && d.buildOffsetDecodeTable(288, 32)
		ok = ok && d.buildLitlenDecodeTable(288, 32)
		ok = ok && dp.buildPrecodeDecodeTable()
	})
	if !ok || d.litlenTablebits != litlenTablebits {
		t.Fatalf("no-alloc builds: ok=%v tablebits=%d, want true %d", ok, d.litlenTablebits, litlenTablebits)
	}
	if n != 0 {
		t.Fatalf("%v allocations per table build, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// Random length vectors.

// genLens returns random codeword lengths for n symbols with maximum length
// maxLen, drawn from a mix of shapes: complete codes grown from random binary
// trees (balanced or deep), incomplete and over-subscribed variants of them,
// sparse zero-heavy vectors, max-length-heavy vectors, single/empty codes
// and uniform noise.
func genLens(r *rand.Rand, n, maxLen int) []uint8 {
	l := make([]uint8, n)
	if n == 0 {
		return l
	}
	switch k := r.IntN(10); k {
	case 0, 1, 2, 3, 4, 5: // complete, then maybe broken
		complete(r, l, maxLen, k%3)
		switch r.IntN(4) {
		case 0: // incomplete: drop a codeword or lengthen one
			s := usedSym(r, l)
			if r.IntN(2) == 0 || int(l[s]) == maxLen {
				l[s] = 0
			} else {
				l[s]++
			}
		case 1: // over-subscribed: shorten a codeword or add one
			s := usedSym(r, l)
			if l[s] > 1 && r.IntN(2) == 0 {
				l[s]--
			} else {
				l[r.IntN(n)] = uint8(1 + r.IntN(maxLen))
			}
		}
	case 6: // sparse zero-heavy
		for i := range l {
			if r.IntN(10) == 0 {
				l[i] = uint8(1 + r.IntN(maxLen))
			}
		}
	case 7: // max-length heavy
		for i := range l {
			if r.IntN(10) < 7 {
				l[i] = uint8(maxLen)
			} else {
				l[i] = uint8(r.IntN(maxLen + 1))
			}
		}
	case 8: // single codeword or empty
		if r.IntN(3) != 0 {
			l[r.IntN(n)] = uint8(1 + r.IntN(2)*r.IntN(maxLen))
		}
	default: // uniform noise
		for i := range l {
			l[i] = uint8(r.IntN(maxLen + 1))
		}
	}
	return l
}

func usedSym(r *rand.Rand, l []uint8) int {
	for {
		if s := r.IntN(len(l)); l[s] != 0 {
			return s
		}
	}
}

// complete fills l with a complete code on a random subset of symbols: start
// with a root leaf and split leaves (shorter than maxLen) until the leaf
// count is reached. mode 0 splits uniformly random leaves, 1 mostly the
// newest leaf (long chains: max-length codewords, subtables), 2 mostly the
// shortest leaf (flat codes).
func complete(r *rand.Rand, l []uint8, maxLen, mode int) {
	n := len(l)
	if n < 2 {
		l[0] = 1 // single codeword (an accepted incomplete code)
		return
	}
	k := 2 + r.IntN(n-1)
	if r.IntN(2) == 0 {
		k = 2 + r.IntN(min(n-1, 16))
	}
	depth := make([]int, 1, k)
	for len(depth) < k {
		i := -1
		for try := 0; try < 64 && i < 0; try++ {
			j := r.IntN(len(depth))
			switch {
			case mode == 1 && r.IntN(5) != 0:
				j = len(depth) - 1 // the newest leaf
			case mode == 2:
				for s := 0; s < 3; s++ {
					if c := r.IntN(len(depth)); depth[c] < depth[j] {
						j = c
					}
				}
			}
			if depth[j] < maxLen {
				i = j
			}
		}
		if i < 0 {
			break
		}
		depth[i]++
		depth = append(depth, depth[i])
	}
	perm := r.Perm(n)
	for j, d := range depth {
		l[perm[j]] = uint8(d)
	}
}

func genLitlenRec(r *rand.Rand) litlenRec {
	nl := 257 + r.IntN(32)
	switch r.IntN(10) {
	case 0:
		nl = r.IntN(289)
	case 1:
		nl = []int{257, 288}[r.IntN(2)]
	}
	no := 1 + r.IntN(32)
	switch r.IntN(10) {
	case 0:
		no = r.IntN(33)
	case 1:
		no = []int{1, 32}[r.IntN(2)]
	}
	return litlenRec{genLens(r, nl, deflateMaxLitlenCodewordLen), genLens(r, no, deflateMaxOffsetCodewordLen)}
}

func diffCount(t *testing.T) int {
	if testing.Short() {
		return 2000
	}
	return 20000
}

func TestBuildDecodeTablesMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	n := diffCount(t)
	t.Run("precode", func(t *testing.T) {
		r := rand.New(rand.NewPCG(1, 19))
		recs := make([][deflateNumPrecodeSyms]uint8, n)
		for i := range recs {
			copy(recs[i][:], genLens(r, deflateNumPrecodeSyms, deflateMaxPreCodewordLen))
		}
		c := ldPrecode(t, recs)
		reused := new(Decompressor) // pooled reuse: no reset between builds
		var nok int
		for i := range recs {
			name := fmt.Sprintf("precode #%d %v", i, recs[i])
			g := goPrecode(&recs[i])
			comparePrecode(t, name, g, c[i])
			checkPrecode(t, name, recs[i][:], g)
			copy(reused.lens[:], recs[i][:])
			if reused.buildPrecodeDecodeTable() != g.ok {
				t.Fatalf("%s: reused decompressor gives a different result", name)
			}
			if g.ok {
				checkTable(t, name+" reused", reused.precodeDecodeTable[:], precodeTablebits, recs[i][:], precodeDecodeResults[:])
				nok++
			}
		}
		t.Logf("%d precode vectors, %d accepted", n, nok)
	})
	t.Run("litlen+offset", func(t *testing.T) {
		r := rand.New(rand.NewPCG(2, 288))
		const batch = 2500
		reused := new(Decompressor)
		var nokLit, nokOff, nsub int
		for done := 0; done < n; done += batch {
			recs := make([]litlenRec, min(batch, n-done))
			for i := range recs {
				recs[i] = genLitlenRec(r)
			}
			c := ldLitlen(t, recs)
			for i, rec := range recs {
				name := fmt.Sprintf("litlen #%d (nl=%d no=%d)", done+i, len(rec.lit), len(rec.off))
				g := goLitlen(rec)
				compareLitlen(t, name, g, c[i])
				checkLitlen(t, name, rec, g)
				copy(reused.lens[:], rec.lit)
				copy(reused.lens[len(rec.lit):], rec.off)
				nl, no := uint32(len(rec.lit)), uint32(len(rec.off))
				if reused.buildOffsetDecodeTable(nl, no) != g.okOff || reused.buildLitlenDecodeTable(nl, no) != g.okLit {
					t.Fatalf("%s: reused decompressor gives a different result", name)
				}
				if g.okLit {
					checkTable(t, name+" reused", reused.litlenDecodeTable[:], g.tablebits, rec.lit, litlenDecodeResults[:])
					nokLit++
					if usedMax(rec.lit) > litlenTablebits && expectOK(rec.lit) {
						nsub++
					}
				}
				if g.okOff {
					checkTable(t, name+" reused", reused.offsetDecodeTable[:], offsetTablebits, rec.off, offsetDecodeResults[:])
					nokOff++
				}
			}
		}
		t.Logf("%d litlen+offset vectors, %d litlen accepted (%d with subtables), %d offset accepted", n, nokLit, nsub, nokOff)
	})
}
