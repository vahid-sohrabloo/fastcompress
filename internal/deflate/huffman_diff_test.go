package deflate

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// cwExact reports whether codewords of zero-length (unused) symbols can be
// compared with ldref: C leaves them "undefined" and computes them with
// bitreverse_tab on x86 (0, as this port does) but with rbit32 on arm, so
// on other architectures only the codewords of used symbols are compared.
var cwExact = runtime.GOARCH == "amd64" || runtime.GOARCH == "386"

// hRec is one ldref dump-codes record.
type hRec struct {
	kind  int // hkLitlen, hkOffset, hkPrecode or hkPair
	freqs []uint32
}

// hRes is the result of one record.
type hRes struct {
	lens   []uint8
	cw     []uint32
	fullCW []uint32 // hkPair: c->o.length.codewords[0..258]
	fullL  []uint8  // hkPair: c->o.length.lens[0..258]
}

type binReader struct {
	t   testing.TB
	out []byte
}

func (b *binReader) bytes(n int) []byte {
	b.t.Helper()
	if len(b.out) < n {
		b.t.Fatalf("ldref: short output (want %d more bytes, have %d)", n, len(b.out))
	}
	v := b.out[:n]
	b.out = b.out[n:]
	return v
}

func (b *binReader) u32s(n int) []uint32 {
	raw := b.bytes(4 * n)
	v := make([]uint32, n)
	for i := range v {
		v[i] = binary.LittleEndian.Uint32(raw[4*i:])
	}
	return v
}

func (b *binReader) line() string {
	b.t.Helper()
	i := bytes.IndexByte(b.out, '\n')
	if i < 0 {
		b.t.Fatalf("ldref: no header line")
	}
	s := string(b.out[:i])
	b.out = b.out[i+1:]
	return s
}

func (b *binReader) done() {
	b.t.Helper()
	if len(b.out) != 0 {
		b.t.Fatalf("ldref: %d trailing bytes", len(b.out))
	}
}

// ldCodes runs ldref dump-codes on recs.
func ldCodes(t testing.TB, recs []hRec) []hRes {
	t.Helper()
	var in []byte
	for _, r := range recs {
		in = append(in, byte(r.kind))
		in = binary.LittleEndian.AppendUint16(in, uint16(len(r.freqs)))
		for _, f := range r.freqs {
			in = binary.LittleEndian.AppendUint32(in, f)
		}
	}
	b := &binReader{t: t, out: creftest.LDRun(t, in, "dump-codes")}
	res := make([]hRes, len(recs))
	for i, r := range recs {
		n := len(r.freqs)
		res[i].lens = slices.Clone(b.bytes(n))
		res[i].cw = b.u32s(n)
		if r.kind == hkPair {
			res[i].fullCW = b.u32s(deflateMaxMatchLen + 1)
			res[i].fullL = slices.Clone(b.bytes(deflateMaxMatchLen + 1))
		}
	}
	b.done()
	return res
}

// goCodes is the Go side of one dump-codes record (zeroed state).
func goCodes(r hRec) hRes {
	if r.kind != hkPair {
		lens, cw := goMakeCode(r.kind, r.freqs)
		return hRes{lens: lens, cw: cw}
	}
	c := new(Compressor)
	copy(c.freqs.litlen[:], r.freqs[:deflateNumLitlenSyms])
	copy(c.freqs.offset[:], r.freqs[deflateNumLitlenSyms:])
	deflateMakeHuffmanCodes(&c.freqs, &c.codes)
	c.computeFullLenCodewords(&c.codes)
	return hRes{
		lens:   slices.Clone(c.codes.lens[:]),
		cw:     append(slices.Clone(c.codes.codewords.litlen[:]), c.codes.codewords.offset[:]...),
		fullCW: slices.Clone(c.o.length.codewords[:]),
		fullL:  slices.Clone(c.o.length.lens[:]),
	}
}

func compareCodes(r hRec, g, c hRes) error {
	if !slices.Equal(g.lens, c.lens) {
		return fmt.Errorf("lens differ:\n go %v\n  c %v", g.lens, c.lens)
	}
	for s := range g.cw {
		if g.cw[s] != c.cw[s] && (cwExact || g.lens[s] != 0) {
			return fmt.Errorf("codeword %d: go %#x, c %#x (len %d)", s, g.cw[s], c.cw[s], g.lens[s])
		}
	}
	if r.kind == hkPair {
		for ln := range g.fullCW {
			if g.fullL[ln] != c.fullL[ln] {
				return fmt.Errorf("full len %d: go len %d, c %d", ln, g.fullL[ln], c.fullL[ln])
			}
			// Length symbols with zero length have undefined codewords.
			sym := deflateFirstLenSym + int(deflateLengthSlot[ln])
			if g.fullCW[ln] != c.fullCW[ln] && (cwExact || ln < deflateMinMatchLen || g.lens[sym] != 0) {
				return fmt.Errorf("full codeword %d: go %#x, c %#x", ln, g.fullCW[ln], c.fullCW[ln])
			}
		}
	}
	return nil
}

func genPair(r *rand.Rand) hRec {
	f := append(genFreqs(r, deflateNumLitlenSyms), genFreqs(r, deflateNumOffsetSyms)...)
	if r.IntN(4) != 0 {
		f[deflateEndOfBlock] = max(f[deflateEndOfBlock], 1) // as deflate_finish_block
	}
	return hRec{kind: hkPair, freqs: f}
}

func diffCodes(t *testing.T, recs []hRec) {
	t.Helper()
	const batch = 5000
	for lo := 0; lo < len(recs); lo += batch {
		part := recs[lo:min(lo+batch, len(recs))]
		cres := ldCodes(t, part)
		for i, r := range part {
			g := goCodes(r)
			if err := compareCodes(r, g, cres[i]); err != nil {
				t.Fatalf("record %d kind %d n %d freqs %v: %v", lo+i, r.kind, len(r.freqs), r.freqs, err)
			}
			if r.kind != hkPair {
				if err := checkCodeProps(r.freqs, g.lens, g.cw, hkMaxLen[r.kind]); err != nil {
					t.Fatalf("record %d: %v", lo+i, err)
				}
			}
		}
	}
}

// TestMakeHuffmanCodeMatchLibdeflate: >= 50000 random frequency vectors per
// kind (litlen max 14, offset max 15, precode max 7) and 20000 litlen+offset
// pairs (with the full length codewords) compared with ldref dump-codes:
// lens and codewords exactly.
func TestMakeHuffmanCodeMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	per, pairs := 50000, 20000
	if testing.Short() {
		per, pairs = 3000, 1000
	}
	for kind := range 3 {
		t.Run(hkName[kind], func(t *testing.T) {
			r := rand.New(rand.NewPCG(2101, uint64(kind)))
			recs := make([]hRec, per)
			for i := range recs {
				recs[i] = hRec{kind: kind, freqs: genFreqs(r, genN(r, kind))}
			}
			diffCodes(t, recs)
		})
	}
	t.Run("pair", func(t *testing.T) {
		r := rand.New(rand.NewPCG(2101, 3))
		recs := make([]hRec, pairs)
		for i := range recs {
			recs[i] = genPair(r)
		}
		diffCodes(t, recs)
	})
}

// TestMakeHuffmanCodeUnitMatchLibdeflate: the golden / limit cases of
// huffman_test.go against ldref too.
func TestMakeHuffmanCodeUnitMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	recs := []hRec{
		{hkOffset, []uint32{1, 1, 1}}, {hkOffset, []uint32{1, 2, 4, 8}}, {hkOffset, []uint32{5, 5, 5, 5}},
		{hkOffset, []uint32{7, 1, 7}}, {hkOffset, make([]uint32, 32)}, {hkOffset, []uint32{0, 1}},
		{hkPrecode, []uint32{3, 0}}, {hkLitlen, []uint32{3, 9}}, {hkPrecode, fibFreqs(19)},
		{hkOffset, append(fibFreqs(30), 0, 0)}, {hkLitlen, append(fibFreqs(25), ones(263)...)},
		{hkOffset, fillU32(32, 0x80000000)}, {hkLitlen, ones(288)}, {hkLitlen, mod5(257)},
		{hkLitlen, fillU32(288, 0xffffffff)}, {hkPrecode, fillU32(19, 1<<22)}, {hkOffset, fillU32(32, 1<<22-1)},
	}
	for kind, ns := range map[int][]int{hkLitlen: {2, 3, 256, 257, 287, 288}, hkOffset: {2, 3, 31, 32}, hkPrecode: {2, 18, 19}} {
		for _, n := range ns {
			recs = append(recs, hRec{kind, append(fibFreqs(min(n, 40)), ones(n-min(n, 40))...)})
			one := make([]uint32, n)
			one[n-1] = 1
			recs = append(recs, hRec{kind, one})
		}
	}
	diffCodes(t, recs)
}

// hdrRes is the result of one dump-header record.
type hdrRes struct {
	nl, no, nexp, nitems uint32
	freqs, cw, items     []uint32
	lens                 []uint8
	codesLens            []uint8
}

func ldHeader(t testing.TB, ins [][deflateNumLitlenSyms + deflateNumOffsetSyms]uint8) []hdrRes {
	t.Helper()
	var in []byte
	for i := range ins {
		in = binary.LittleEndian.AppendUint16(in, deflateNumLitlenSyms)
		in = append(in, ins[i][:lensOffsetStart]...)
		in = binary.LittleEndian.AppendUint16(in, deflateNumOffsetSyms)
		in = append(in, ins[i][lensOffsetStart:]...)
	}
	b := &binReader{t: t, out: creftest.LDRun(t, in, "dump-header")}
	res := make([]hdrRes, len(ins))
	for i := range res {
		h := &res[i]
		line := b.line()
		if _, err := fmt.Sscanf(line, "nl=%d no=%d nexp=%d nitems=%d", &h.nl, &h.no, &h.nexp, &h.nitems); err != nil {
			t.Fatalf("ldref dump-header: bad line %q: %v", line, err)
		}
		h.freqs = b.u32s(deflateNumPrecodeSyms)
		h.lens = slices.Clone(b.bytes(deflateNumPrecodeSyms))
		h.cw = b.u32s(deflateNumPrecodeSyms)
		h.items = b.u32s(int(h.nitems))
		h.codesLens = slices.Clone(b.bytes(deflateNumLitlenSyms + deflateNumOffsetSyms))
	}
	b.done()
	return res
}

func compareHeader(c *Compressor, h *hdrRes) error {
	p := &c.o.precode
	if p.numLitlenSyms != h.nl || p.numOffsetSyms != h.no || p.numExplicitLens != h.nexp || p.numItems != h.nitems {
		return fmt.Errorf("go nl=%d no=%d nexp=%d nitems=%d, c nl=%d no=%d nexp=%d nitems=%d",
			p.numLitlenSyms, p.numOffsetSyms, p.numExplicitLens, p.numItems, h.nl, h.no, h.nexp, h.nitems)
	}
	if !slices.Equal(p.freqs[:], h.freqs) || !slices.Equal(p.lens[:], h.lens) {
		return fmt.Errorf("precode freqs/lens: go %v %v, c %v %v", p.freqs, p.lens, h.freqs, h.lens)
	}
	for s := range p.codewords {
		if p.codewords[s] != h.cw[s] && (cwExact || p.lens[s] != 0) {
			return fmt.Errorf("precode codeword %d: go %#x, c %#x", s, p.codewords[s], h.cw[s])
		}
	}
	if !slices.Equal(p.items[:p.numItems], h.items) {
		return fmt.Errorf("items: go %v, c %v", p.items[:p.numItems], h.items)
	}
	if !slices.Equal(c.codes.lens[:], h.codesLens) {
		return fmt.Errorf("codes.lens after the call: go %v, c %v", c.codes.lens, h.codesLens)
	}
	return nil
}

func diffHeader(t *testing.T, ins [][deflateNumLitlenSyms + deflateNumOffsetSyms]uint8) {
	t.Helper()
	const batch = 5000
	c := new(Compressor)
	for lo := 0; lo < len(ins); lo += batch {
		part := ins[lo:min(lo+batch, len(ins))]
		hres := ldHeader(t, part)
		for i := range part {
			c.codes = deflateCodes{}
			c.o.precode = deflatePrecode{}
			c.codes.lens = part[i]
			c.precomputeHuffmanHeader()
			if err := compareHeader(c, &hres[i]); err != nil {
				t.Fatalf("record %d lens %v: %v", lo+i, part[i], err)
			}
			if err := checkHeaderProps(&part[i], c); err != nil {
				t.Fatalf("record %d: %v", lo+i, err)
			}
		}
	}
}

// TestPrecomputeHuffmanHeaderMatchLibdeflate: 20000 litlen+offset length sets
// (half real codes from the Go builder, half raw 0..15 runs) plus edge cases,
// compared with ldref dump-header (counts, precode freqs/lens/codewords,
// items, and the lens array left behind).
func TestPrecomputeHuffmanHeaderMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	r := rand.New(rand.NewPCG(2101, 10))
	var ins [][deflateNumLitlenSyms + deflateNumOffsetSyms]uint8
	for _, v := range []uint8{0, 1, 7, 15} {
		var l [deflateNumLitlenSyms + deflateNumOffsetSyms]uint8
		for i := range l {
			l[i] = v
		}
		ins = append(ins, l)
	}
	for _, k := range []int{1, 2, 3, 4, 5, 6, 7, 10, 11, 12, 137, 138, 139, 140, 148, 149} {
		// A run of k equal nonzero / zero lengths inside the litlen set.
		var a, z [deflateNumLitlenSyms + deflateNumOffsetSyms]uint8
		for i := range a {
			a[i], z[i] = 3, 3
		}
		for i := 10; i < 10+k; i++ {
			a[i], z[i] = 9, 0
		}
		ins = append(ins, a, z)
	}
	for len(ins) < n {
		ins = append(ins, genHeaderLens(r))
	}
	diffHeader(t, ins)
}

// TestStaticCodesMatchLibdeflate compares the static codes, the frequencies
// deflate_init_static_codes leaves behind, every slot table, the full
// offset slot map, deflate_get_offset_slot for every offset and the static
// full length codewords with ldref dump-static.
func TestStaticCodesMatchLibdeflate(t *testing.T) {
	b := &binReader{t: t, out: creftest.LDRun(t, nil, "dump-static")}
	var c Compressor
	c.initStaticCodes()
	c.initOffsetSlotFull()
	eq := func(name string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("%s differs from libdeflate", name)
		}
	}
	eq("static lens", bytes.Equal(b.bytes(320), c.staticCodes.lens[:]))
	eq("static litlen codewords", slices.Equal(b.u32s(288), c.staticCodes.codewords.litlen[:]))
	eq("static offset codewords", slices.Equal(b.u32s(32), c.staticCodes.codewords.offset[:]))
	eq("freqs.litlen", slices.Equal(b.u32s(288), c.freqs.litlen[:]))
	eq("freqs.offset", slices.Equal(b.u32s(32), c.freqs.offset[:]))
	eq("deflate_length_slot_base", slices.Equal(b.u32s(len(deflateLengthSlotBase)), deflateLengthSlotBase[:]))
	eq("deflate_extra_length_bits", bytes.Equal(b.bytes(len(deflateExtraLengthBits)), deflateExtraLengthBits[:]))
	eq("deflate_offset_slot_base", slices.Equal(b.u32s(len(deflateOffsetSlotBase)), deflateOffsetSlotBase[:]))
	eq("deflate_extra_offset_bits", bytes.Equal(b.bytes(len(deflateExtraOffsetBits)), deflateExtraOffsetBits[:]))
	eq("deflate_length_slot", bytes.Equal(b.bytes(len(deflateLengthSlot)), deflateLengthSlot[:]))
	eq("deflate_offset_slot", bytes.Equal(b.bytes(len(deflateOffsetSlot)), deflateOffsetSlot[:]))
	eq("deflate_extra_precode_bits", bytes.Equal(b.bytes(19), deflateExtraPrecodeBits[:]))
	eq("deflate_precode_lens_permutation", bytes.Equal(b.bytes(19), deflatePrecodeLensPermutation[:]))
	eq("offset_slot_full", bytes.Equal(b.bytes(deflateMaxMatchOffset), c.offsetSlotFull[1:]))
	slots := b.bytes(deflateMaxMatchOffset)
	for off := uint32(1); off <= deflateMaxMatchOffset; off++ {
		if uint32(slots[off-1]) != deflateGetOffsetSlot(off) {
			t.Fatalf("deflate_get_offset_slot(%d): c %d, go %d", off, slots[off-1], deflateGetOffsetSlot(off))
		}
	}
	c.computeFullLenCodewords(&c.staticCodes)
	nl := deflateMaxMatchLen + 1 - deflateMinMatchLen
	eq("static full length codewords", slices.Equal(b.u32s(nl), c.o.length.codewords[deflateMinMatchLen:]))
	eq("static full length lens", bytes.Equal(b.bytes(nl), c.o.length.lens[deflateMinMatchLen:]))
	b.done()
}
