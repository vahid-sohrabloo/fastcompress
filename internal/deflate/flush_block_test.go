package deflate

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// fbTok is one token of a block: length 0 is a literal (the next data
// byte), else a match (length 3..258, offset 1..32768). This is the input
// of ldref dump-flush.
type fbTok struct{ length, offset uint16 }

type fbBlock struct {
	final bool
	data  []byte
	toks  []fbTok
}

// fbRec is one dump-flush record: a compressor level, an output buffer of
// cap bytes starting at start with bitcount pending bits bitbuf, and the
// blocks flushed in order.
type fbRec struct {
	level    int
	cap      int
	start    int
	bitcount uint
	bitbuf   byte
	blocks   []fbBlock
}

type fbRes struct {
	buf      []byte
	next     int
	bitcount uint
	bitbuf   uint64
	overflow bool
	types    []int // Go only: block type chosen for each flushed block
}

func (r *fbRec) encode(dst []byte) []byte {
	dst = append(dst, byte(r.level))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(r.cap))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(r.start))
	dst = append(dst, byte(r.bitcount), r.bitbuf)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(r.blocks)))
	for _, b := range r.blocks {
		dst = append(dst, byte(b2u(b.final)))
		dst = binary.LittleEndian.AppendUint32(dst, uint32(len(b.data)))
		dst = append(dst, b.data...)
		dst = binary.LittleEndian.AppendUint32(dst, uint32(len(b.toks)))
		for _, t := range b.toks {
			dst = binary.LittleEndian.AppendUint16(dst, t.length)
			dst = binary.LittleEndian.AppendUint16(dst, t.offset)
		}
	}
	return dst
}

// fbGo runs a record through the Go port, with compressors reused per
// level (as ldref reuses its compressor), so state left by earlier records
// is exercised too.
type fbGo struct {
	cs   [13]*Compressor
	seqs []deflateSequence
}

func (g *fbGo) compressor(t testing.TB, level int) *Compressor {
	if g.cs[level] == nil {
		c, err := NewCompressor(level)
		if err != nil {
			t.Fatal(err)
		}
		g.cs[level] = c
	}
	return g.cs[level]
}

// bitAt returns bit p of the stream written so far (the output bytes, then
// the pending bits of os).
func bitAt(os *outputBitstream, p int) uint32 {
	if p < 8*os.next {
		return uint32(os.out[p/8]>>(p%8)) & 1
	}
	return uint32(os.bitbuf>>(p-8*os.next)) & 1
}

func (g *fbGo) run(t testing.TB, r *fbRec) fbRes {
	c := g.compressor(t, r.level)
	buf := bytes.Repeat([]byte{0xA5}, r.cap)
	os := outputBitstream{bitbuf: uint64(r.bitbuf), bitcount: r.bitcount, next: r.start, out: buf}
	var types []int
	for _, b := range r.blocks {
		if os.overflow {
			continue
		}
		if len(g.seqs) < len(b.toks)+1 {
			g.seqs = make([]deflateSequence, len(b.toks)+1)
		}
		seqs := g.seqs
		c.beginSequences(&seqs[0])
		i, pos := 0, 0
		for _, tk := range b.toks {
			if tk.length == 0 {
				c.chooseLiteral(uint32(b.data[pos]), &seqs[i])
				pos++
			} else {
				i = c.chooseMatch(uint32(tk.length), uint32(tk.offset), seqs, i)
				pos += int(tk.length)
			}
		}
		if pos != len(b.data) {
			t.Fatalf("tokens cover %d of %d bytes", pos, len(b.data))
		}
		p0 := 8*os.next + int(os.bitcount)
		c.finishBlock(&os, b.data, seqs, b.final)
		if !os.overflow {
			types = append(types, int(bitAt(&os, p0+1)|bitAt(&os, p0+2)<<1))
		}
	}
	return fbRes{buf, os.next, os.bitcount, os.bitbuf, os.overflow, types}
}

// ldFlush runs records through ldref dump-flush in batches.
func ldFlush(t testing.TB, recs []*fbRec) []fbRes {
	t.Helper()
	var res []fbRes
	for len(recs) > 0 {
		var in []byte
		n, outSize := 0, 0
		for n < len(recs) && (n == 0 || len(in)+outSize < 16<<20) {
			in = recs[n].encode(in)
			outSize += recs[n].cap
			n++
		}
		out := creftest.LDRun(t, in, "dump-flush")
		br := &binReader{t: t, out: out}
		for _, r := range recs[:n] {
			buf := bytes.Clone(br.bytes(r.cap))
			var g fbRes
			var ov int
			line := br.line()
			if _, err := fmt.Sscanf(line, "next=%d bitcount=%d bitbuf=%x overflow=%d",
				&g.next, &g.bitcount, &g.bitbuf, &ov); err != nil {
				t.Fatalf("ldref dump-flush: bad line %q: %v", line, err)
			}
			g.buf, g.overflow = buf, ov != 0
			res = append(res, g)
		}
		br.done()
		recs = recs[n:]
	}
	return res
}

func compareFlush(g, c fbRes) error {
	if g.next != c.next || g.bitcount != c.bitcount || g.bitbuf != c.bitbuf || g.overflow != c.overflow {
		return fmt.Errorf("state: go next=%d bitcount=%d bitbuf=%x overflow=%v, C next=%d bitcount=%d bitbuf=%x overflow=%v",
			g.next, g.bitcount, g.bitbuf, g.overflow, c.next, c.bitcount, c.bitbuf, c.overflow)
	}
	if !bytes.Equal(g.buf, c.buf) {
		i := 0
		for g.buf[i] == c.buf[i] {
			i++
		}
		return fmt.Errorf("output differs at byte %d of %d (go %#x, C %#x)", i, len(g.buf), g.buf[i], c.buf[i])
	}
	return nil
}

// Match lengths and offsets at slot boundaries (first and last value of
// every slot).
var (
	fbLengths = func() []uint16 {
		var v []uint16
		for s, base := range deflateLengthSlotBase {
			v = append(v, uint16(base), uint16(base+(1<<deflateExtraLengthBits[s])-1))
		}
		return v
	}()
	fbOffsets = func() []uint16 {
		var v []uint16
		for s, base := range deflateOffsetSlotBase {
			v = append(v, uint16(base), uint16(base+(1<<deflateExtraOffsetBits[s])-1))
		}
		return v
	}()
)

// fbGen generates records whose matches are real (data copied from the
// offset), so flushed streams also decode.
type fbGen struct{ r *rand.Rand }

func (g fbGen) blockLen() int {
	r := g.r
	switch x := r.IntN(100); {
	case x < 30:
		return 1 + r.IntN(16)
	case x < 60:
		return 17 + r.IntN(300)
	case x < 85:
		return 300 + r.IntN(4700)
	case x < 97:
		return 5000 + r.IntN(70000)
	default:
		return maxBlockLength - r.IntN(70000)
	}
}

// block appends a block of length L to all (the record's stream so far)
// and returns it.
func (g fbGen) block(all []byte, L int, kind int) ([]byte, fbBlock) {
	r := g.r
	start := len(all)
	var toks []fbTok
	pMatch := []int{0, 0, 40, 97, 60, 20}[kind]
	alpha := []int{256, 3, 20, 256, 256, 64}[kind]
	for len(all)-start < L {
		avail := L - (len(all) - start)
		hist := len(all)
		if hist > 0 && avail >= 3 && r.IntN(100) < pMatch {
			var ln, off int
			if kind == 4 || r.IntN(4) == 0 {
				ln = int(fbLengths[r.IntN(len(fbLengths))])
				off = int(fbOffsets[r.IntN(len(fbOffsets))])
			} else {
				ln = 3 + r.IntN(256)
				off = 1 + r.IntN(min(hist, 1+r.IntN(deflateMaxMatchOffset)))
			}
			ln = min(ln, avail)
			if off > hist {
				off = 1 + r.IntN(min(hist, deflateMaxMatchOffset))
			}
			if ln >= 3 {
				for range ln {
					all = append(all, all[len(all)-off])
				}
				toks = append(toks, fbTok{uint16(ln), uint16(off)})
				continue
			}
		}
		var b byte
		if kind == 1 && r.IntN(50) == 0 {
			b = byte(r.IntN(256))
		} else {
			b = byte(r.IntN(alpha)) + byte(kind*31)
		}
		all = append(all, b)
		toks = append(toks, fbTok{})
	}
	return all, fbBlock{data: all[start:], toks: toks}
}

// rec generates a record; decodable ones start at bit 0 of the buffer and
// only their last block is final. The cap is chosen by the caller.
func (g fbGen) rec() *fbRec {
	r := g.r
	rec := &fbRec{level: r.IntN(13)}
	if r.IntN(3) == 0 {
		rec.start = r.IntN(20)
		rec.bitcount = uint(r.IntN(8))
		rec.bitbuf = byte(r.IntN(256)) & byte(1<<rec.bitcount-1)
	}
	nb := 1
	if r.IntN(10) < 3 {
		nb = 2 + r.IntN(3)
	}
	var all []byte
	finalLast := r.IntN(10) < 7
	for i := range nb {
		L := g.blockLen()
		if nb > 1 {
			L = min(L, 1+r.IntN(80000))
		}
		var b fbBlock
		all, b = g.block(all, L, r.IntN(6))
		b.final = finalLast && i == nb-1
		rec.blocks = append(rec.blocks, b)
	}
	return rec
}

// fbBigCap is an output size that always fits (every block fits as
// uncompressed blocks, and the cheapest choice is never larger).
func fbBigCap(r *fbRec) int {
	n := r.start + 1
	for _, b := range r.blocks {
		n += len(b.data) + 5*((len(b.data)+65534)/65535) + 1
	}
	return n
}

// fbNeeded is the number of bytes from the buffer start a result occupies,
// counting the pending partial byte.
func fbNeeded(g fbRes) int { return g.next + int((g.bitcount+7)/8) }

// chooseCap sets r.cap: big, exactly the needed size, one byte short, or
// something in between (measured with a big-cap Go run).
func chooseCap(t testing.TB, g *fbGo, r *fbRec, mode int) {
	r.cap = fbBigCap(r)
	if mode == 0 {
		return
	}
	need := fbNeeded(g.run(t, r))
	switch mode {
	case 1:
		r.cap = need
	case 2:
		r.cap = max(r.start, need-1)
	default:
		r.cap = r.start + int(uint(need-r.start)*uint(mode&0xff)/256)
	}
}

func (r *fbRec) stream() []byte {
	var all []byte
	for _, b := range r.blocks {
		all = append(all, b.data...)
	}
	return all
}

// decodable reports whether the record's output is a complete DEFLATE
// stream (bit 0 of the buffer, only the last block final).
func (r *fbRec) decodable() bool {
	for i, b := range r.blocks {
		if b.final != (i == len(r.blocks)-1) {
			return false
		}
	}
	return r.start == 0 && r.bitcount == 0
}

// checkDecode decodes a complete stream with the T1.2 decoder and with
// compress/flate and compares with want.
func checkDecode(t testing.TB, stream, want []byte) error {
	d := NewDecompressor()
	got := make([]byte, len(want))
	n, err := d.Decompress(got, stream)
	if err != nil || n != len(want) || !bytes.Equal(got, want) {
		return fmt.Errorf("T1.2 decoder: n=%d err=%v equal=%v", n, err, bytes.Equal(got[:n], want))
	}
	std, err := io.ReadAll(flate.NewReader(bytes.NewReader(stream)))
	if err != nil || !bytes.Equal(std, want) {
		return fmt.Errorf("compress/flate: %d bytes, err=%v, equal=%v", len(std), err, bytes.Equal(std, want))
	}
	return nil
}

func fbStream(g fbRes) []byte {
	s := bytes.Clone(g.buf[:g.next])
	if g.bitcount != 0 {
		s = append(s, byte(g.bitbuf))
	}
	return s
}

// TestFlushBlockMatchLibdeflate compares the Go block writer with C's
// deflate_flush_block (ldref dump-flush) on random token lists, byte for
// byte over the whole output buffer (junk of word stores included) and the
// final bitstream state; it counts the block types and overflows chosen.
func TestFlushBlockMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	r := rand.New(rand.NewPCG(22, 1))
	gen := fbGen{r}
	var g fbGo
	var types [3]int
	total, overflows, decoded := 0, 0, 0
	check := func(recs []*fbRec) {
		cres := ldFlush(t, recs)
		for i, rec := range recs {
			gr := g.run(t, rec)
			if err := compareFlush(gr, cres[i]); err != nil {
				t.Fatalf("record %d (level %d, %d blocks, cap %d): %v", total, rec.level, len(rec.blocks), rec.cap, err)
			}
			total++
			for _, ty := range gr.types {
				types[ty]++
			}
			if gr.overflow {
				overflows++
			} else if rec.decodable() {
				if err := checkDecode(t, fbStream(gr), rec.stream()); err != nil {
					t.Fatalf("record %d: %v", total-1, err)
				}
				decoded++
			}
		}
	}
	// In chunks, to bound memory.
	for done := 0; done < n; {
		var recs []*fbRec
		for ; done < n && len(recs) < 400; done++ {
			rec := gen.rec()
			chooseCap(t, &g, rec, []int{0, 0, 1, 2, 3 + r.IntN(250)}[done%5])
			recs = append(recs, rec)
		}
		check(recs)
	}
	check(fbEdgeRecs(t, &g))
	t.Logf("%d records: blocks stored %d, static %d, dynamic %d; %d overflows; %d decoded",
		total, types[0], types[1], types[2], overflows, decoded)
	need := n / 20
	if types[0] < need || types[1] < need || types[2] < need || overflows < need || decoded < need {
		t.Fatalf("generator does not hit every outcome often enough (want >= %d each)", need)
	}
}

// fbEdgeRecs are hand-made records: every alignment before a stored block
// (bitcount 0..7, so 0 or 1 padding bytes), stored blocks of exactly
// k*65535 and k*65535+1 bytes, MAX_BLOCK_LENGTH of literals and of
// matches, every length and every offset slot boundary, single-symbol
// blocks, and their exact / short caps.
func fbEdgeRecs(t testing.TB, g *fbGo) []*fbRec {
	r := rand.New(rand.NewPCG(22, 2))
	gen := fbGen{r}
	var recs []*fbRec
	add := func(rec *fbRec) {
		for mode := range 3 {
			cp := *rec
			chooseCap(t, g, &cp, mode)
			recs = append(recs, &cp)
		}
	}
	lits := func(data []byte, final bool) fbBlock {
		return fbBlock{final: final, data: data, toks: make([]fbTok, len(data))}
	}
	rnd := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		return b
	}
	for bc := range uint(8) {
		for _, L := range []int{1, 2, 65535, 65536, 2 * 65535, 2*65535 + 1, maxBlockLength} {
			add(&fbRec{level: int(bc), start: int(bc), bitcount: bc, bitbuf: byte(0x5A & (1<<bc - 1)),
				blocks: []fbBlock{lits(rnd(L), bc%2 == 0)}})
		}
	}
	for _, L := range []int{1, 2, 3, 100, maxBlockLength} {
		add(&fbRec{level: 6, blocks: []fbBlock{lits(bytes.Repeat([]byte{'a'}, L), true)}})
	}
	// One literal then MAX_BLOCK_LENGTH-1 bytes of matches of every length
	// and offset slot boundary (offsets clamped to the history).
	for _, final := range []bool{true, false} {
		all := []byte{'x'}
		toks := []fbTok{{}}
		i := 0
		for len(all) < maxBlockLength {
			ln := min(int(fbLengths[i%len(fbLengths)]), maxBlockLength-len(all))
			off := min(int(fbOffsets[(i/7)%len(fbOffsets)]), len(all))
			i++
			if ln < 3 {
				all = append(all, 'y')
				toks = append(toks, fbTok{})
				continue
			}
			for range ln {
				all = append(all, all[len(all)-off])
			}
			toks = append(toks, fbTok{uint16(ln), uint16(off)})
		}
		add(&fbRec{level: 12, blocks: []fbBlock{{final: final, data: all, toks: toks}}})
	}
	// Long blocks of each kind, two blocks in a row.
	for kind := range 6 {
		all, b1 := gen.block(nil, 40000, kind)
		_, b2 := gen.block(all, maxBlockLength, kind)
		b2.final = true
		add(&fbRec{level: kind, blocks: []fbBlock{b1, b2}})
	}
	return recs
}

// TestFlushBlockGolden: hand-checked outputs (independent of ldref).
func TestFlushBlockGolden(t *testing.T) {
	var g fbGo
	cases := []struct {
		name string
		rec  fbRec
		want []byte
	}{
		// "a" final: static (3+8+7 = 18 bits) beats stored (3+5+32+8):
		// bits 1, 10 (BTYPE 1, LSB first), literal 0x61 = 8-bit codeword
		// 0x30+0x61 = 0x91 (MSB first), EOB 0000000.
		{"static-a", fbRec{blocks: []fbBlock{{true, []byte("a"), []fbTok{{}}}}},
			[]byte{0x4b, 0x04, 0x00}},
		// 0xff final: stored ties? static 3+9+7 = 19 < 48 -> static.
		{"static-ff", fbRec{blocks: []fbBlock{{true, []byte{0xff}, []fbTok{{}}}}},
			[]byte{0xfb, 0x0f, 0x00}},
	}
	for _, tc := range cases {
		tc.rec.cap = fbBigCap(&tc.rec)
		gr := g.run(t, &tc.rec)
		if got := fbStream(gr); !bytes.Equal(got, tc.want) {
			t.Errorf("%s: got % x, want % x", tc.name, got, tc.want)
		}
		if err := checkDecode(t, fbStream(gr), tc.rec.stream()); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// Random incompressible block, non-final, after 6 pending bits: a
	// stored block with an extra padding byte, no data changed.
	data := make([]byte, 70000)
	for i := range data {
		data[i] = byte(i*7919 + i>>8)
	}
	rec := fbRec{start: 3, bitcount: 6, bitbuf: 0x2d, blocks: []fbBlock{{false, data, make([]fbTok, len(data))}}}
	rec.cap = fbBigCap(&rec)
	gr := g.run(t, &rec)
	if gr.types[0] != deflateBlocktypeUncompressed {
		t.Fatalf("type %d, want stored", gr.types[0])
	}
	want := []byte{0x2d, 0x00, 0xff, 0xff, 0x00, 0x00}
	want = append(want, data[:65535]...)
	want = append(want, 0x00, 0x71, 0x11, 0x8e, 0xee) // second: BFINAL 0, LEN 4465
	want = append(want, data[65535:]...)
	if got := gr.buf[rec.start:gr.next]; !bytes.Equal(got, want) || gr.bitcount != 0 {
		t.Fatalf("stored split: got %d bytes (bitcount %d), mismatch", len(got), gr.bitcount)
	}
	if !bytes.Equal(gr.buf[:rec.start], []byte{0xa5, 0xa5, 0xa5}) {
		t.Fatalf("bytes before start written: % x", gr.buf[:rec.start])
	}
}

// TestFlushBlockOverflowState: on overflow nothing is written and the
// bitstream state is unchanged apart from the flag.
func TestFlushBlockOverflowState(t *testing.T) {
	var g fbGo
	r := rand.New(rand.NewPCG(22, 3))
	gen := fbGen{r}
	for range 200 {
		rec := gen.rec()
		rec.blocks = rec.blocks[:1]
		chooseCap(t, &g, rec, 2)
		gr := g.run(t, rec)
		if !gr.overflow {
			if rec.cap == rec.start && fbNeeded(gr) == rec.start {
				continue
			}
			t.Fatalf("one byte short did not overflow (cap %d)", rec.cap)
		}
		if gr.next != rec.start || gr.bitcount != rec.bitcount || gr.bitbuf != uint64(rec.bitbuf) ||
			!bytes.Equal(gr.buf, bytes.Repeat([]byte{0xA5}, rec.cap)) {
			t.Fatalf("overflow changed the state or the buffer")
		}
	}
}

func TestFlushBlockNoAlloc(t *testing.T) {
	c, err := NewCompressor(6)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(22, 4))
	_, b := fbGen{r}.block(nil, 20000, 2)
	seqs := make([]deflateSequence, len(b.toks)+1)
	out := make([]byte, 30000)
	allocs := testing.AllocsPerRun(20, func() {
		c.beginSequences(&seqs[0])
		i, pos := 0, 0
		for _, tk := range b.toks {
			if tk.length == 0 {
				c.chooseLiteral(uint32(b.data[pos]), &seqs[i])
				pos++
			} else {
				i = c.chooseMatch(uint32(tk.length), uint32(tk.offset), seqs, i)
				pos += int(tk.length)
			}
		}
		var os outputBitstream
		os.init(out)
		c.finishBlock(&os, b.data, seqs, true)
	})
	if allocs != 0 {
		t.Fatalf("%v allocs per flush", allocs)
	}
}
