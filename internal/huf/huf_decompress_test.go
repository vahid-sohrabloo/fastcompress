package huf

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"sort"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/bitstream"
	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/fse"
)

// hufStream is one Huffman-compressed input: the table description
// followed by one or four streams, as Compress1XRepeat/Compress4XRepeat
// write it.
type hufStream struct {
	name string
	src  []byte // original input
	comp []byte // table description || stream(s)
	four bool
}

// hufStreams compresses every creftest input of at most BlockSizeMax bytes
// at tableLog 6 and 11, single and four streams. Incompressible and RLE
// inputs are skipped (the encoder emits no Huffman stream for them).
func hufStreams(t testing.TB) []hufStream {
	t.Helper()
	inputs := creftest.Inputs(t)
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	wksp := make([]byte, WorkspaceSize)
	var out []hufStream
	for _, name := range names {
		src := inputs[name]
		if len(src) > BlockSizeMax {
			continue
		}
		for _, tl := range []uint{6, 11} {
			for _, four := range []bool{false, true} {
				dst := make([]byte, CompressBound(len(src)))
				rep := RepeatNone
				var n int
				var err error
				if four {
					n, err = Compress4XRepeat(dst, src, 255, tl, wksp, nil, &rep, 0)
				} else {
					n, err = Compress1XRepeat(dst, src, 255, tl, wksp, nil, &rep, 0)
				}
				if err != nil {
					t.Fatalf("%s tl=%d four=%v: compress: %v", name, tl, four, err)
				}
				if n <= 1 {
					continue
				}
				out = append(out, hufStream{
					name: name + map[uint]string{6: "/tl6", 11: "/tl11"}[tl] + map[bool]string{false: "/1x", true: "/4x"}[four],
					src:  src, comp: dst[:n], four: four,
				})
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no Huffman streams")
	}
	return out
}

// decodeX1 reads the table and decodes the rest of comp into dstSize bytes.
func decodeX1(comp []byte, dstSize int, four bool, maxTableLog uint, flags int) ([]byte, error) {
	var dt DTable
	dt.Init(maxTableLog)
	hSize, err := ReadDTableX1(&dt, comp, flags)
	if err != nil {
		return nil, err
	}
	dst := make([]byte, dstSize)
	if four {
		err = Decompress4X1UsingDTable(dst, comp[hSize:], &dt, flags)
	} else {
		err = Decompress1X1UsingDTable(dst, comp[hSize:], &dt, flags)
	}
	return dst, err
}

func TestHUFDecompressX1RoundTrip(t *testing.T) {
	for _, s := range hufStreams(t) {
		// maxTableLog 12 (the zstd DTable): tables are rescaled to
		// HUF_DECODER_FAST_TABLELOG; 4X takes the fast loop.
		// maxTableLog = the stream's own table log - 1: no rescale beyond
		// it, so streams below 11 bits decode at their own log through the
		// body.
		var w [SymbolValueMax + 1]byte
		var rank [TableLogMax + 1]uint32
		_, tableLog, _, err := ReadStats(w[:], rank[:], s.comp)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		for _, mtl := range []uint{TableLogMax, uint(tableLog) - 1} {
			for _, flags := range []int{0, FlagDisableFast} {
				got, err := decodeX1(s.comp, len(s.src), s.four, mtl, flags)
				if err != nil {
					t.Fatalf("%s mtl=%d flags=%d: %v", s.name, mtl, flags, err)
				}
				if !bytes.Equal(got, s.src) {
					t.Fatalf("%s mtl=%d flags=%d: output differs", s.name, mtl, flags)
				}
			}
		}
	}
}

func TestHUFReadDTableX1(t *testing.T) {
	// One stored weight (1) plus the implied last one (1): two symbols of 1
	// bit, tableLog 1, rescaled to 11.
	desc := []byte{128, 0x10}
	var dt DTable
	dt.Init(TableLogMax)
	n, err := ReadDTableX1(&dt, desc, 0)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if dt.desc.tableLog != decoderFastTableLog || dt.desc.tableType != 0 || dt.desc.maxTableLog != TableLogMax {
		t.Fatalf("desc=%+v", dt.desc)
	}
	half := 1 << (decoderFastTableLog - 1)
	for i := 0; i < 1<<decoderFastTableLog; i++ {
		// Both weights rescale to 11, i.e. 1 bit; symbol 0 fills the low
		// half (symbols are ordered by weight, then value).
		want := uint16(1)
		if i >= half {
			want |= 1 << 8
		}
		if dt.x1[i] != want {
			t.Fatalf("x1[%d]=%#x want %#x", i, dt.x1[i], want)
		}
	}
	// A DTable too small for the tree: maxTableLog 1 allows tableLog 2 only.
	desc2 := []byte{128 + 3, 0x12, 0x30} // weights 1,2,3,0 (+1) -> tableLog 3
	dt.Init(1)
	if _, err := ReadDTableX1(&dt, desc2, 0); err != ErrTableLogTooLarge {
		t.Fatalf("err=%v want ErrTableLogTooLarge", err)
	}
}

// TestHUFReadDTableX1NoAlloc checks that reading a table allocates
// nothing: the weight reader's FSE table lives on the stack (C keeps it in
// the caller's workspace).
func TestHUFReadDTableX1NoAlloc(t *testing.T) {
	var dt DTable
	check := func(name string, comp []byte) uint {
		var w [SymbolValueMax + 1]byte
		var rank [TableLogMax + 1]uint32
		if _, _, _, err := ReadStats(w[:], rank[:], comp); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fseLog := fseTableLog(t, comp)
		if n := testing.AllocsPerRun(10, func() {
			dt.Init(TableLogMax - 1)
			if _, err := ReadDTableX1(&dt, comp, 0); err != nil {
				t.Fatal(err)
			}
		}); n != 0 {
			t.Fatalf("%s: %v allocations per ReadDTableX1", name, n)
		}
		return fseLog
	}
	sawLog6 := false
	for _, s := range hufStreams(t) {
		if s.comp[0] >= 128 { // not FSE-compressed weights
			continue
		}
		if check(s.name, s.comp) == fse.MaxTableLogForHuffHeader {
			sawLog6 = true
		}
		// The encoder picks FSE tableLog 5 for these weights; re-encode
		// the same weights at tableLog 6, the largest the reader accepts.
		if comp := reencodeWeightsLog6(t, s.comp); comp != nil {
			if check(s.name+"/log6", comp) == fse.MaxTableLogForHuffHeader {
				sawLog6 = true
			}
		}
	}
	if !sawLog6 {
		t.Fatal("no stream whose FSE weight table has tableLog 6")
	}
}

// reencodeWeightsLog6 rewrites the FSE weight header of the Huffman stream
// comp (compressWeights, C: HUF_compressWeights, with tableLog forced to
// 6) and returns it followed by comp's payload; nil if the weights do not
// suit FSE.
func reencodeWeightsLog6(t *testing.T, comp []byte) []byte {
	t.Helper()
	var w [SymbolValueMax + 1]byte
	var rank [TableLogMax + 1]uint32
	nbSymbols, _, hSize, err := ReadStats(w[:], rank[:], comp)
	if err != nil {
		t.Fatal(err)
	}
	weights := w[:nbSymbols-1] // the last weight is implied
	var count [256]uint32
	maxSV := uint(0)
	for _, v := range weights {
		count[v]++
		if uint(v) > maxSV {
			maxSV = uint(v)
		}
	}
	if maxSV > TableLogMax-1 || len(weights) <= 1 {
		return nil
	}
	const tl = fse.MaxTableLogForHuffHeader
	var norm [TableLogMax + 1]int16
	if _, err := fse.NormalizeCount(norm[:], tl, count[:], len(weights), maxSV, false); err != nil {
		return nil
	}
	dst := make([]byte, 1+2*len(weights)+64)
	n, err := fse.WriteNCount(dst[1:], norm[:], maxSV, tl)
	if err != nil {
		t.Fatal(err)
	}
	ct := make(fse.CTable, fse.CTableSizeU32(tl, TableLogMax))
	if err := fse.BuildCTable(ct, norm[:], maxSV, tl, make([]byte, fse.BuildCTableWorkspaceSize(TableLogMax, tl))); err != nil {
		t.Fatal(err)
	}
	c, err := fse.CompressWeightsUsingCTable(dst[1+n:], weights, ct)
	if err != nil || c == 0 || n+c >= 128 {
		return nil
	}
	dst[0] = byte(n + c)
	return append(dst[:1+n+c], comp[hSize:]...)
}

// fseTableLog returns the tableLog of the FSE-compressed weight header of a
// Huffman stream (first byte < 128 is the compressed size).
func fseTableLog(t *testing.T, comp []byte) uint {
	t.Helper()
	var ncount [256]int16
	maxSV := uint(255)
	tl, _, err := fse.ReadNCount(ncount[:], &maxSV, comp[1:1+int(comp[0])])
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// decode4X1Both runs the 4X decoder with the fast loop allowed and with it
// disabled, on copies of the same table.
func decode4X1Both(dt *DTable, src []byte, dstSize int) (fast, body []byte, errFast, errBody error) {
	fast = make([]byte, dstSize)
	body = make([]byte, dstSize)
	dt2 := *dt
	errFast = Decompress4X1UsingDTable(fast, src, dt, 0)
	errBody = Decompress4X1UsingDTable(body, src, &dt2, FlagDisableFast)
	return
}

func TestHUF4X1FastVsBody(t *testing.T) {
	fastUsed := 0
	for _, s := range hufStreams(t) {
		if !s.four {
			continue
		}
		var dt DTable
		dt.Init(TableLogMax)
		hSize, err := ReadDTableX1(&dt, s.comp, 0)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		data := s.comp[hSize:]
		var args decompressFastArgs
		if r, _ := decompressFastArgsInit(&args, make([]byte, len(s.src)), data, &dt); r == 1 {
			fastUsed++
		}
		// The whole stream, then truncated and bit-flipped variants.
		variants := [][]byte{data, data[:len(data)-1]}
		for _, off := range []int{0, 6, len(data) / 2, len(data) - 1} {
			v := bytes.Clone(data)
			v[off] ^= 0x5a
			variants = append(variants, v)
		}
		for i, v := range variants {
			for _, dstSize := range []int{len(s.src), len(s.src) - 1, len(s.src) + 1} {
				fast, body, ef, eb := decode4X1Both(&dt, v, dstSize)
				if eb == nil && (ef != nil || !bytes.Equal(fast, body)) {
					t.Fatalf("%s variant %d dstSize %d: body ok, fast err=%v equal=%v", s.name, i, dstSize, ef, bytes.Equal(fast, body))
				}
				if i == 0 && dstSize == len(s.src) && (ef != nil || !bytes.Equal(fast, s.src)) {
					t.Fatalf("%s: fast err=%v", s.name, ef)
				}
			}
		}
	}
	if fastUsed == 0 {
		t.Fatal("fast loop never taken")
	}
}

func TestHUF4X1Errors(t *testing.T) {
	var dt DTable
	dt.Init(TableLogMax)
	if _, err := ReadDTableX1(&dt, []byte{128 + 1, 0x10}, 0); err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, 100)
	for _, flags := range []int{0, FlagDisableFast} {
		if err := Decompress4X1UsingDTable(dst, make([]byte, 9), &dt, flags); err != ErrCorruptionDetected {
			t.Fatalf("srcSize 9: %v", err)
		}
		if err := Decompress4X1UsingDTable(dst[:5], make([]byte, 40), &dt, flags); err != ErrCorruptionDetected {
			t.Fatalf("dstSize 5: %v", err)
		}
		// Jump table lengths beyond srcSize.
		src := make([]byte, 40)
		binary.LittleEndian.PutUint16(src, 30)
		binary.LittleEndian.PutUint16(src[2:], 30)
		if err := Decompress4X1UsingDTable(dst, src, &dt, flags); err != ErrCorruptionDetected {
			t.Fatalf("overflow: %v", err)
		}
	}
}

// FuzzHUFDecompress4X1 feeds an arbitrary table description and stream data
// to the X1 decoder: no panic, and the fast and body paths agree whenever
// the body accepts.
//
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast — the fast
// path does not check that each stream ends exactly (it only checks the
// output lengths and that no stream read below its start), so it accepts
// some inputs the body rejects. Agreement is therefore one-way: body ok
// implies fast ok with identical output; the reverse is not C's behavior.
func FuzzHUFDecompress4X1(f *testing.F) {
	wksp := make([]byte, WorkspaceSize)
	for _, src := range [][]byte{
		bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123"), 40),
		[]byte("the quick brown fox jumps over the lazy dog, again and again and again"),
	} {
		dst := make([]byte, CompressBound(len(src)))
		rep := RepeatNone
		n, err := Compress4XRepeat(dst, src, 255, 11, wksp, nil, &rep, 0)
		if err != nil || n <= 1 {
			f.Fatalf("seed: n=%d err=%v", n, err)
		}
		var dt DTable
		dt.Init(TableLogMax)
		h, err := ReadDTableX1(&dt, dst[:n], 0)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(dst[:h], dst[h:n], uint32(len(src)))
	}
	f.Add([]byte{128 + 1, 0x10}, make([]byte, 40), uint32(64))
	f.Fuzz(func(t *testing.T, table, data []byte, dstSize uint32) {
		var dt DTable
		dt.Init(TableLogMax)
		if _, err := ReadDTableX1(&dt, table, 0); err != nil {
			return
		}
		size := int(dstSize % (2 * BlockSizeMax))
		fast, body, ef, eb := decode4X1Both(&dt, data, size)
		if eb == nil && (ef != nil || !bytes.Equal(fast, body)) {
			t.Fatalf("body ok, fast err=%v equal=%v", ef, bytes.Equal(fast, body))
		}
		// 1X on the same data must not panic either.
		_ = Decompress1X1UsingDTable(body, data, &dt, 0)
	})
}

// craftedX1Table reads the table with weights 10,9,...,2,1,1,0 plus an
// implied 11: tableLog 11, so the fast loop applies; an all-ones stream
// decodes 1-bit symbols and an all-zero stream 11-bit ones.
func craftedX1Table(t *testing.T) *DTable {
	t.Helper()
	var dt DTable
	dt.Init(TableLogMax)
	if _, err := ReadDTableX1(&dt, []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}, 0); err != nil {
		t.Fatal(err)
	}
	if dt.desc.tableLog != decoderFastTableLog {
		t.Fatalf("tableLog %d", dt.desc.tableLog)
	}
	return &dt
}

// jumpStreams joins four streams behind a 6-byte jump table.
func jumpStreams(s [4][]byte) []byte {
	out := make([]byte, 6)
	for i := 0; i < 3; i++ {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(len(s[i])))
	}
	for _, b := range s {
		out = append(out, b...)
	}
	return out
}

// TestHUF4X1FastReadBelowStart pins HUF_initRemainingDStream's check that a
// stream did not read below its start: streams 1-3 decode 11-bit symbols
// from 700 bytes, far fewer than their segments need, so they run into the
// stream before them. C (fast and body) rejects this.
func TestHUF4X1FastReadBelowStart(t *testing.T) {
	dt := craftedX1Table(t)
	var s [4][]byte
	s[0] = bytes.Repeat([]byte{0xFF}, 700)
	for i := 1; i < 4; i++ {
		s[i] = make([]byte, 700)
		s[i][699] = 0x01
	}
	src := jumpStreams(s)
	for _, flags := range []int{0, FlagDisableFast} {
		dt2 := *dt
		if err := Decompress4X1UsingDTable(make([]byte, 40000), src, &dt2, flags); err != ErrCorruptionDetected {
			t.Fatalf("flags=%d: err=%v, want ErrCorruptionDetected", flags, err)
		}
	}
}

// TestHUF4X1SevenByteStream pins the fast init's 8-byte minimum per stream:
// with one stream of 7 bytes the fast path must decline, so both paths
// run the body and agree exactly, error or not.
func TestHUF4X1SevenByteStream(t *testing.T) {
	dt := craftedX1Table(t)
	for short := 0; short < 4; short++ {
		for _, fill := range []byte{0xFF, 0x00, 0x5a} {
			var s [4][]byte
			for i := range s {
				n := 64
				if i == short {
					n = 7
				}
				s[i] = bytes.Repeat([]byte{fill}, n)
				s[i][n-1] |= 0x01
			}
			src := jumpStreams(s)
			for _, dstSize := range []int{40, 300, 1000} {
				fast, body, ef, eb := decode4X1Both(dt, src, dstSize)
				if ef != eb || !bytes.Equal(fast, body) {
					t.Fatalf("short=%d fill=%#x dst=%d: fast err=%v body err=%v equal=%v",
						short, fill, dstSize, ef, eb, bytes.Equal(fast, body))
				}
			}
		}
	}
}

// decodeX2 reads the table with ReadDTableX2 and decodes the rest of comp
// into dstSize bytes.
func decodeX2(comp []byte, dstSize int, four bool, maxTableLog uint, flags int) ([]byte, error) {
	var dt DTable
	dt.Init(maxTableLog)
	hSize, err := ReadDTableX2(&dt, comp, flags)
	if err != nil {
		return nil, err
	}
	dst := make([]byte, dstSize)
	if four {
		err = Decompress4X2UsingDTable(dst, comp[hSize:], &dt, flags)
	} else {
		err = Decompress1X2UsingDTable(dst, comp[hSize:], &dt, flags)
	}
	return dst, err
}

func TestHUFDecompressX2RoundTrip(t *testing.T) {
	for _, s := range hufStreams(t) {
		// maxTableLog 12 (the zstd DTable): tables up to 11 bits are built
		// at HUF_DECODER_FAST_TABLELOG; 4X takes the fast loop.
		// maxTableLog = the stream's own table log: built at that log, so
		// streams below 11 bits decode through the body.
		var w [SymbolValueMax + 1]byte
		var rank [TableLogMax + 1]uint32
		_, tableLog, _, err := ReadStats(w[:], rank[:], s.comp)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		for _, mtl := range []uint{TableLogMax, uint(tableLog)} {
			for _, flags := range []int{0, FlagDisableFast} {
				got, err := decodeX2(s.comp, len(s.src), s.four, mtl, flags)
				if err != nil {
					t.Fatalf("%s mtl=%d flags=%d: %v", s.name, mtl, flags, err)
				}
				if !bytes.Equal(got, s.src) {
					t.Fatalf("%s mtl=%d flags=%d: output differs", s.name, mtl, flags)
				}
			}
		}
	}
}

func TestHUFReadDTableX2(t *testing.T) {
	// Weights 1,2,3,0 plus the implied 1 (tableLog 3): symbols 2, 1, 4, 0
	// have codes 1, 01, 001, 000. Built at 11 bits with maxTableLog 12.
	desc := []byte{128 + 3, 0x12, 0x30}
	var dt DTable
	dt.Init(TableLogMax)
	n, err := ReadDTableX2(&dt, desc, 0)
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if dt.desc.tableLog != decoderFastTableLog || dt.desc.tableType != 1 || dt.desc.maxTableLog != TableLogMax {
		t.Fatalf("desc=%+v", dt.desc)
	}
	// HUF_DEltX2 {sequence, nbBits, length} as sequence | nbBits<<16 | length<<24.
	for _, c := range []struct {
		index int
		want  uint32
	}{
		{0b11 << 9, 2 | 2<<8 | 2<<16 | 2<<24},      // "1" "1": symbols 2, 2
		{0b000_000 << 5, 0 | 0<<8 | 6<<16 | 2<<24}, // "000" "000": symbols 0, 0
		{0b001_01 << 6, 4 | 1<<8 | 5<<16 | 2<<24},  // "001" "01": symbols 4, 1
		{0b01_000 << 6, 1 | 0<<8 | 5<<16 | 2<<24},  // "01" "000": symbols 1, 0
	} {
		if e := dt.x2[c.index]; e != c.want {
			t.Fatalf("x2[%#b]=%#x want %#x", c.index, e, c.want)
		}
	}
	// Level-2 boundary of HUF_fillDTableX2 (targetLog-nbBits >= minBits):
	// in the 11-bit table of weights 10,9,...,2,1,1,0 plus an implied 11,
	// symbol 8 (weight 2) has the 10-bit code 0000000001 (DTable rank
	// [2,4)) and symbol 12 (weight 11) the 1-bit code 1. 11-10 == minBits
	// (1), so C builds the 2-symbol entry {8, 12}, 11 bits, at index 3.
	dt.Init(TableLogMax)
	if _, err := ReadDTableX2(&dt, []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}, 0); err != nil {
		t.Fatal(err)
	}
	if e, want := dt.x2[0b0000000001_1], uint32(8|12<<8|11<<16|2<<24); e != want {
		t.Fatalf("x2[3]=%#x want %#x", e, want)
	}

	// maxTableLog below the tree's log.
	dt.Init(2)
	if _, err := ReadDTableX2(&dt, desc, 0); err != ErrTableLogTooLarge {
		t.Fatalf("err=%v want ErrTableLogTooLarge", err)
	}
	// maxTableLog beyond HUF_TABLELOG_MAX.
	dt.Init(TableLogMax + 1)
	if _, err := ReadDTableX2(&dt, desc, 0); err != ErrTableLogTooLarge {
		t.Fatalf("err=%v want ErrTableLogTooLarge", err)
	}
}

// TestHUFSelectDecoder pins C's algoTime table verbatim and checks the
// decision on a {dstSize, cSrcSize} grid that walks every quantisation
// step Q = cSrcSize*16/dstSize (0..15, and cSrcSize >= dstSize). The
// expected strings were printed by C's HUF_selectDecoder (v1.5.7
// huf_decompress.c compiled with gcc), one digit per cSrcSize =
// dstSize*q/16 for q = 0..16.
func TestHUFSelectDecoder(t *testing.T) {
	want := [16][2]algoTimeT{
		{{0, 0}, {1, 1}},
		{{0, 0}, {1, 1}},
		{{150, 216}, {381, 119}},
		{{170, 205}, {514, 112}},
		{{177, 199}, {539, 110}},
		{{197, 194}, {644, 107}},
		{{221, 192}, {735, 107}},
		{{256, 189}, {881, 106}},
		{{359, 188}, {1167, 109}},
		{{582, 187}, {1570, 114}},
		{{688, 187}, {1712, 122}},
		{{825, 186}, {1965, 136}},
		{{976, 185}, {2131, 150}},
		{{1180, 186}, {2070, 175}},
		{{1377, 185}, {1731, 202}},
		{{1412, 185}, {1695, 202}},
	}
	if algoTime != want {
		t.Fatalf("algoTime differs from C")
	}
	grid := []struct {
		dstSize int
		want    string
	}{
		{1, "00000000000000000"},
		{16, "00000000000000000"},
		{255, "00000000000000000"},
		{256, "00000000000000000"},
		{1000, "00110000000000000"},
		{4096, "00111111110000000"},
		{10000, "00111111111100000"},
		{32768, "00111111111110000"},
		{65536, "00111111111111000"},
		{100000, "00111111111111000"},
		{131072, "00111111111111000"},
	}
	for _, g := range grid {
		for q := 0; q <= 16; q++ {
			c := g.dstSize * q / 16
			if got := SelectDecoder(g.dstSize, c); got != uint32(g.want[q]-'0') {
				t.Errorf("SelectDecoder(%d, %d) = %d, want %c", g.dstSize, c, got, g.want[q])
			}
		}
	}
}

// decode4X2Both runs the 4X X2 decoder with the fast loop allowed and with
// it disabled, on copies of the same table.
func decode4X2Both(dt *DTable, src []byte, dstSize int) (fast, body []byte, errFast, errBody error) {
	fast = make([]byte, dstSize)
	body = make([]byte, dstSize)
	dt2 := *dt
	errFast = Decompress4X2UsingDTable(fast, src, dt, 0)
	errBody = Decompress4X2UsingDTable(body, src, &dt2, FlagDisableFast)
	return
}

// equalButSegmentEnds reports whether a and b are equal except possibly
// at the last byte of each of the four 4X segments.
//
// C: huf_decompress.c:HUF_decodeLastSymbolX2 clamps bitsConsumed to 64, so
// the X2 body accepts a stream whose last symbol's code runs past the
// stream's start; its lookahead there sees zeros. The fast path's
// remaining DStream starts at ilowest (HUF_initRemainingDStream), so the
// same lookahead sees the previous stream's bytes and can decode another
// last symbol. Checked against C's fast and body paths (built from
// ref/zstd with ZSTD_DISABLE_ASM): Go matches C on such inputs, which
// differ the same way in C.
func equalButSegmentEnds(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	seg := (len(a) + 3) / 4
	for i := range a {
		if a[i] != b[i] && i != len(a)-1 && (i+1)%seg != 0 {
			return false
		}
	}
	return true
}

func TestHUF4X2FastVsBody(t *testing.T) {
	fastUsed := 0
	for _, s := range hufStreams(t) {
		if !s.four {
			continue
		}
		var dt DTable
		dt.Init(TableLogMax)
		hSize, err := ReadDTableX2(&dt, s.comp, 0)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		data := s.comp[hSize:]
		var args decompressFastArgs
		if r, _ := decompressFastArgsInit(&args, make([]byte, len(s.src)), data, &dt); r == 1 {
			fastUsed++
		}
		// The whole stream, then truncated and bit-flipped variants.
		variants := [][]byte{data, data[:len(data)-1]}
		for _, off := range []int{0, 6, len(data) / 2, len(data) - 1} {
			v := bytes.Clone(data)
			v[off] ^= 0x5a
			variants = append(variants, v)
		}
		for i, v := range variants {
			for _, dstSize := range []int{len(s.src), len(s.src) - 1, len(s.src) + 1} {
				fast, body, ef, eb := decode4X2Both(&dt, v, dstSize)
				if eb == nil && (ef != nil || !equalButSegmentEnds(fast, body)) {
					t.Fatalf("%s variant %d dstSize %d: body ok, fast err=%v equal=%v", s.name, i, dstSize, ef, equalButSegmentEnds(fast, body))
				}
				if i == 0 && dstSize == len(s.src) && (ef != nil || !bytes.Equal(fast, s.src)) {
					t.Fatalf("%s: fast err=%v", s.name, ef)
				}
			}
		}
	}
	if fastUsed == 0 {
		t.Fatal("fast loop never taken")
	}
}

// TestHUF4X2FastReadBelowStart is the X2 form of
// TestHUF4X1FastReadBelowStart: streams 1-3 run below their start.
func TestHUF4X2FastReadBelowStart(t *testing.T) {
	var dt DTable
	dt.Init(TableLogMax)
	if _, err := ReadDTableX2(&dt, []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}, 0); err != nil {
		t.Fatal(err)
	}
	var s [4][]byte
	s[0] = bytes.Repeat([]byte{0xFF}, 700)
	for i := 1; i < 4; i++ {
		s[i] = make([]byte, 700)
		s[i][699] = 0x01
	}
	src := jumpStreams(s)
	for _, flags := range []int{0, FlagDisableFast} {
		dt2 := dt
		if err := Decompress4X2UsingDTable(make([]byte, 40000), src, &dt2, flags); err != ErrCorruptionDetected {
			t.Fatalf("flags=%d: err=%v, want ErrCorruptionDetected", flags, err)
		}
	}
}

// TestHUF4XFastInputBound pins the fast loops' input-side iteration bound
// (C: iters = (ip[0] - ilowest) / 7): four 700-byte all-zero streams decode
// 11-bit symbols, about 6.9 bytes per iteration, into a large dst, so the
// iteration count is limited by ip[0], not by the output. A weaker bound
// runs ip[0] below 0. The streams run out of input: C's body rejects;
// C's fast path, which never checks the stream end, accepts with output
// FNV-1a 0xd10ad426093d7171 for X1 and X2 alike (C built from ref/zstd).
func TestHUF4XFastInputBound(t *testing.T) {
	desc := []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}
	var s [4][]byte
	for i := range s {
		s[i] = make([]byte, 700)
		s[i][699] = 0x01
	}
	src := jumpStreams(s)
	for _, x2 := range []bool{false, true} {
		var dt DTable
		dt.Init(TableLogMax)
		var err error
		if x2 {
			_, err = ReadDTableX2(&dt, desc, 0)
		} else {
			_, err = ReadDTableX1(&dt, desc, 0)
		}
		if err != nil {
			t.Fatal(err)
		}
		var args decompressFastArgs
		if r, _ := decompressFastArgsInit(&args, make([]byte, 40000), src, &dt); r != 1 {
			t.Fatal("fast path not taken")
		}
		dst := make([]byte, 40000)
		dt2 := dt
		if err := Decompress4XUsingDTable(dst, src, &dt2, 0); err != nil {
			t.Fatalf("x2=%v fast: err=%v", x2, err)
		}
		h := fnv.New64a()
		h.Write(dst)
		if got := h.Sum64(); got != 0xd10ad426093d7171 {
			t.Fatalf("x2=%v fast: output hash %#x, C 0xd10ad426093d7171", x2, got)
		}
		if err := Decompress4XUsingDTable(dst, src, &dt2, FlagDisableFast); err != ErrCorruptionDetected {
			t.Fatalf("x2=%v body: err=%v, want ErrCorruptionDetected", x2, err)
		}
	}
}

// TestHUF4XBodySegmentOverrun runs the 4X bodies on an input where stream
// 1 decodes 2-symbol X2 entries (8 bytes per main-loop iteration) while
// stream 4 decodes 11-bit single symbols (4 bytes), so X2's op1 passes
// opStart2 in the main loop. C rejects it (op1 > opStart2 for X2; the end
// check for X1, where every stream advances 4 bytes per iteration).
//
// With 64-bit semantics the op1/op2/op3 overrun checks cannot change the
// result, so dropping them is an equivalent mutant: a stream that passed
// its segment end is finished by decodeStream{X1,X2} with one Reload
// only, and BIT_endOfDStream then needs ptr == start and bitsConsumed ==
// 64 at the main-loop exit, which needs >= 57 bits in one iteration (at
// most 4*12 are decoded). In X1 the check is unreachable: op1 - 0 ==
// op4 - opStart4 <= oend - opStart4 <= segmentSize.
func TestHUF4XBodySegmentOverrun(t *testing.T) {
	desc := []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}
	var s [4][]byte
	for i := range s {
		s[i] = bytes.Repeat([]byte{0xFF}, 64) // symbol 12, code "1"
	}
	s[3] = make([]byte, 64) // symbol 9, code 00000000000
	s[3][63] = 0x01
	src := jumpStreams(s)
	for _, x2 := range []bool{false, true} {
		var dt DTable
		dt.Init(TableLogMax)
		var err error
		if x2 {
			_, err = ReadDTableX2(&dt, desc, 0)
		} else {
			_, err = ReadDTableX1(&dt, desc, 0)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := Decompress4XUsingDTable(make([]byte, 48), src, &dt, FlagDisableFast); err != ErrCorruptionDetected {
			t.Fatalf("x2=%v: err=%v, want ErrCorruptionDetected", x2, err)
		}
	}
}

// TestHUFDecompress4XDispatchStaleX1 reads a 12-bit X1 table and then an
// 11-bit X2 table into the same DTable: x1 keeps stale 12-bit entries
// while desc.tableLog is 11. Decompress4XUsingDTable must dispatch on
// desc.tableType (C: HUF_decompress4X_usingDTable) and never run the X1
// fast loop on them (which would consume 8 bytes per iteration and run ip
// below 0).
func TestHUFDecompress4XDispatchStaleX1(t *testing.T) {
	var dt DTable
	dt.Init(TableLogMax)
	// Weights 11,10,...,2,1,1 plus an implied 12: tableLog 12.
	if _, err := ReadDTableX1(&dt, []byte{128 + 11, 0xBA, 0x98, 0x76, 0x54, 0x32, 0x11}, 0); err != nil {
		t.Fatal(err)
	}
	if dt.desc.tableLog != 12 {
		t.Fatalf("X1 tableLog %d", dt.desc.tableLog)
	}
	if _, err := ReadDTableX2(&dt, []byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}, 0); err != nil {
		t.Fatal(err)
	}
	if dt.desc.tableLog != decoderFastTableLog || dt.desc.tableType != 1 {
		t.Fatalf("desc=%+v", dt.desc)
	}
	var s [4][]byte
	for i := range s {
		s[i] = make([]byte, 200)
		s[i][199] = 0x01
	}
	src := jumpStreams(s)
	for _, flags := range []int{0, FlagDisableFast} {
		dt2 := dt
		_ = Decompress4XUsingDTable(make([]byte, 20000), src, &dt2, flags)
		_ = Decompress1XUsingDTable(make([]byte, 20000), src, &dt2, flags)
	}
}

func TestHUFDecompressEntryPoints(t *testing.T) {
	for _, s := range hufStreams(t) {
		var dt DTable
		dt.Init(TableLogMax)
		dst := make([]byte, len(s.src))
		var err error
		if s.four {
			err = Decompress4XHufOnlyWksp(&dt, dst, s.comp, 0)
		} else {
			err = Decompress1XDCtxWksp(&dt, dst, s.comp, 0)
		}
		if err != nil || !bytes.Equal(dst, s.src) {
			t.Fatalf("%s: err=%v equal=%v", s.name, err, bytes.Equal(dst, s.src))
		}
		if want := SelectDecoder(len(s.src), len(s.comp)); uint32(dt.desc.tableType) != want {
			t.Fatalf("%s: tableType %d, SelectDecoder %d", s.name, dt.desc.tableType, want)
		}
		// Treeless repeat: the stream again with the table just read.
		var w [SymbolValueMax + 1]byte
		var rank [TableLogMax + 1]uint32
		_, _, hSize, err := ReadStats(w[:], rank[:], s.comp)
		if err != nil {
			t.Fatal(err)
		}
		clear(dst)
		if s.four {
			err = Decompress4XUsingDTable(dst, s.comp[hSize:], &dt, 0)
		} else {
			err = Decompress1XUsingDTable(dst, s.comp[hSize:], &dt, 0)
		}
		if err != nil || !bytes.Equal(dst, s.src) {
			t.Fatalf("%s repeat: err=%v equal=%v", s.name, err, bytes.Equal(dst, s.src))
		}
	}
	// C's validation checks.
	var dt DTable
	dt.Init(TableLogMax)
	if err := Decompress1XDCtxWksp(&dt, nil, []byte{1}, 0); err != ErrDstSizeTooSmall {
		t.Fatalf("1X dstSize 0: %v", err)
	}
	if err := Decompress4XHufOnlyWksp(&dt, nil, []byte{1}, 0); err != ErrDstSizeTooSmall {
		t.Fatalf("4X dstSize 0: %v", err)
	}
	if err := Decompress4XHufOnlyWksp(&dt, make([]byte, 4), nil, 0); err != ErrCorruptionDetected {
		t.Fatalf("4X cSrcSize 0: %v", err)
	}
	if err := Decompress1XDCtxWksp(&dt, make([]byte, 2), []byte{1, 2, 3}, 0); err != ErrCorruptionDetected {
		t.Fatalf("1X cSrcSize > dstSize: %v", err)
	}
	dst := make([]byte, 3)
	if err := Decompress1XDCtxWksp(&dt, dst, []byte{7, 8, 9}, 0); err != nil || !bytes.Equal(dst, []byte{7, 8, 9}) {
		t.Fatalf("1X raw: %v %v", err, dst)
	}
	if err := Decompress1XDCtxWksp(&dt, dst, []byte{5}, 0); err != nil || !bytes.Equal(dst, []byte{5, 5, 5}) {
		t.Fatalf("1X rle: %v %v", err, dst)
	}
	// A table description with nothing after it: srcSize_wrong
	// (dstSize 1000 with cSrcSize 3 selects X1).
	desc := []byte{128 + 3, 0x12, 0x30}
	if err := Decompress1XDCtxWksp(&dt, make([]byte, 1000), desc, 0); err != ErrSrcSizeWrong {
		t.Fatalf("1X X1 header only: %v", err)
	}
	if err := Decompress4XHufOnlyWksp(&dt, make([]byte, 1000), desc, 0); err != ErrSrcSizeWrong {
		t.Fatalf("4X X1 header only: %v", err)
	}
	// SelectDecoder picks X2 only for cSrcSize >= dstSize/8 with dstSize
	// >= 768, which a bare description (at most ~130 bytes) cannot reach;
	// check the X2 helpers directly.
	if err := decompress1X2DCtxWksp(&dt, make([]byte, 1000), desc, 0); err != ErrSrcSizeWrong {
		t.Fatalf("1X X2 header only: %v", err)
	}
	if err := decompress4X2DCtxWksp(&dt, make([]byte, 1000), desc, 0); err != ErrSrcSizeWrong {
		t.Fatalf("4X X2 header only: %v", err)
	}
	if SelectDecoder(4096, 3) != 0 || SelectDecoder(4096, 1024) != 1 {
		t.Fatal("selector grid assumption")
	}
	// X2 selected (Q=4 at 4096): description then garbage of the right size.
	src := append(bytes.Clone(desc), make([]byte, 1021)...)
	if err := Decompress4XHufOnlyWksp(&dt, make([]byte, 4096), src, 0); err == nil || dt.desc.tableType != 1 {
		t.Fatalf("4X X2 garbage: err=%v type=%d", err, dt.desc.tableType)
	}
	if err := Decompress1XDCtxWksp(&dt, make([]byte, 4096), src, 0); err == nil || dt.desc.tableType != 1 {
		t.Fatalf("1X X2 garbage: err=%v type=%d", err, dt.desc.tableType)
	}
}

// FuzzHUFDecompress4X2 feeds an arbitrary table description, stream data
// and DTable size to the X2 decoder: no panic, and the fast and body paths
// agree whenever the body accepts. As for X1 this is one-way (the fast
// path's finish, HUF_decompress4X2_usingDTable_internal_fast, never checks
// BIT_endOfDStream), and the last byte of each segment may differ (see
// equalButSegmentEnds).
func FuzzHUFDecompress4X2(f *testing.F) {
	wksp := make([]byte, WorkspaceSize)
	for _, src := range [][]byte{
		bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123"), 40),
		[]byte("the quick brown fox jumps over the lazy dog, again and again and again"),
	} {
		dst := make([]byte, CompressBound(len(src)))
		rep := RepeatNone
		n, err := Compress4XRepeat(dst, src, 255, 11, wksp, nil, &rep, 0)
		if err != nil || n <= 1 {
			f.Fatalf("seed: n=%d err=%v", n, err)
		}
		var dt DTable
		dt.Init(TableLogMax)
		h, err := ReadDTableX2(&dt, dst[:n], 0)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(dst[:h], dst[h:n], uint32(len(src)), uint8(TableLogMax))
	}
	f.Add([]byte{128 + 1, 0x10}, make([]byte, 40), uint32(64), uint8(TableLogMax))
	f.Fuzz(func(t *testing.T, table, data []byte, dstSize uint32, mtl uint8) {
		var dt DTable
		dt.Init(uint(mtl % (TableLogMax + 2)))
		if _, err := ReadDTableX2(&dt, table, 0); err != nil {
			return
		}
		size := int(dstSize % (2 * BlockSizeMax))
		fast, body, ef, eb := decode4X2Both(&dt, data, size)
		if eb == nil && (ef != nil || !equalButSegmentEnds(fast, body)) {
			t.Fatalf("body ok, fast err=%v equal=%v", ef, equalButSegmentEnds(fast, body))
		}
		// 1X on the same data must not panic either.
		_ = Decompress1X2UsingDTable(body, data, &dt, 0)
	})
}

// precheck4X returns the error C's 4X bodies (X1 and X2 alike) return
// before decoding any symbol, or nil. C:
// huf_decompress.c:HUF_decompress4X{1,2}_usingDTable_internal_body, the
// checks up to the four BIT_initDStream calls. structural reports a size
// check failing; the fast path rejects those inputs with
// corruption_detected too (it either errors the same way or falls back to
// the body), except at dstSize 4: HUF_DecompressFastArgs_init has no
// dstSize < 6 check and only falls back when op[3] >= oend, which holds
// for every dstSize < 6 but 4 (op[3] = 3). C's fast path then decodes
// dstSize 4 (checked against C), so that case is not structural for it.
func precheck4X(src []byte, dstSize int) (err error, structural bool) {
	if len(src) < 10 || dstSize < 6 {
		return ErrCorruptionDetected, true
	}
	l1 := int(binary.LittleEndian.Uint16(src))
	l2 := int(binary.LittleEndian.Uint16(src[2:]))
	l3 := int(binary.LittleEndian.Uint16(src[4:]))
	l4 := len(src) - (l1 + l2 + l3 + 6)
	if l4 < 0 || 3*((dstSize+3)/4) > dstSize {
		return ErrCorruptionDetected, true
	}
	var d bitstream.DStream
	for _, s := range [][]byte{src[6 : 6+l1], src[6+l1 : 6+l1+l2], src[6+l1+l2 : 6+l1+l2+l3], src[6+l1+l2+l3:]} {
		if _, err := d.Init(s); err != nil {
			return err, false
		}
	}
	return nil, false
}

// FuzzHUFDecompressX1VsX2 reads one table description as X1 and as X2
// (both built at the same log: X1 rescales up to 11, X2 builds at
// min(maxTableLog, 11) when tableLog <= 11, and both keep 12) and decodes
// the same data with both.
//
// What C guarantees, and what is checked:
//   - Both bodies succeed => equal output. A body succeeds only when every
//     stream ends exactly (BIT_endOfDStream), so the bits were read
//     without overflow and both decode the same prefix code; a level-2 X2
//     entry is the two X1 symbols it replaces. Each fast path matches its
//     body whenever the body succeeds (FuzzHUFDecompress4X1/4X2), so the
//     fast outputs are equal then too.
//   - Errors raised before any symbol is decoded (precheck4X) are equal:
//     the X1 and X2 bodies run the same checks in the same order.
//
// What C does not guarantee, and is not checked:
//   - Equal errors after decoding. HUF_decodeLastSymbolX2 skips a whole
//     2-symbol entry's bits for the last symbol and clamps bitsConsumed to
//     64, so X2 accepts trailing bits that make X1's BIT_endOfDStream fail
//     (and the converse when X2's last pair reads past the end).
//   - Equal output when only the fast paths succeed: they do not check
//     the stream end (HUF_decompress4X{1,2}_usingDTable_internal_fast), and
//     past the end X1 and X2 consume the garbage bits differently.
func FuzzHUFDecompressX1VsX2(f *testing.F) {
	wksp := make([]byte, WorkspaceSize)
	for _, src := range [][]byte{
		bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123"), 40),
		[]byte("the quick brown fox jumps over the lazy dog, again and again and again"),
	} {
		for _, four := range []bool{false, true} {
			dst := make([]byte, CompressBound(len(src)))
			rep := RepeatNone
			var n int
			var err error
			if four {
				n, err = Compress4XRepeat(dst, src, 255, 11, wksp, nil, &rep, 0)
			} else {
				n, err = Compress1XRepeat(dst, src, 255, 11, wksp, nil, &rep, 0)
			}
			if err != nil || n <= 1 {
				f.Fatalf("seed: n=%d err=%v", n, err)
			}
			var dt DTable
			dt.Init(TableLogMax)
			h, err := ReadDTableX1(&dt, dst[:n], 0)
			if err != nil {
				f.Fatal(err)
			}
			f.Add(dst[:h], dst[h:n], uint32(len(src)), four)
		}
	}
	// dstSize 4: C's fast path decodes it, the body rejects it (precheck4X).
	f.Add([]byte("\x150z0010210110000108101"), []byte("\v\x00\f\x00\t\x000000000000000000000000000000000000000000"), uint32(4), true)
	f.Fuzz(func(t *testing.T, table, data []byte, dstSize uint32, four bool) {
		var dt1, dt2 DTable
		dt1.Init(TableLogMax)
		dt2.Init(TableLogMax)
		if _, err := ReadDTableX1(&dt1, table, 0); err != nil {
			return
		}
		if _, err := ReadDTableX2(&dt2, table, 0); err != nil {
			return
		}
		if dt1.desc.tableLog != dt2.desc.tableLog {
			t.Fatalf("tableLog X1 %d X2 %d", dt1.desc.tableLog, dt2.desc.tableLog)
		}
		size := int(dstSize % (2 * BlockSizeMax))
		if !four {
			o1, o2 := make([]byte, size), make([]byte, size)
			e1 := Decompress1X1UsingDTable(o1, data, &dt1, 0)
			e2 := Decompress1X2UsingDTable(o2, data, &dt2, 0)
			if e1 == nil && e2 == nil && !bytes.Equal(o1, o2) {
				t.Fatal("1X: both ok, output differs")
			}
			var d bitstream.DStream
			if _, err := d.Init(data); err != nil && (e1 != err || e2 != err) {
				t.Fatalf("1X: init err %v, X1 %v, X2 %v", err, e1, e2)
			}
			return
		}
		pre, structural := precheck4X(data, size)
		var bodyOK [2][]byte
		for _, flags := range []int{FlagDisableFast, 0} {
			o1, o2 := make([]byte, size), make([]byte, size)
			e1 := Decompress4X1UsingDTable(o1, data, &dt1, flags)
			e2 := Decompress4X2UsingDTable(o2, data, &dt2, flags)
			fastDst4 := flags != FlagDisableFast && size == 4
			if ((structural && !fastDst4) || (pre != nil && flags == FlagDisableFast)) && (e1 != pre || e2 != pre) {
				t.Fatalf("flags=%d: precheck %v, X1 %v, X2 %v", flags, pre, e1, e2)
			}
			if flags == FlagDisableFast {
				if e1 == nil && e2 == nil {
					if !bytes.Equal(o1, o2) {
						t.Fatal("bodies ok, output differs")
					}
					bodyOK = [2][]byte{o1, o2}
				}
			} else if bodyOK[0] != nil && (e1 != nil || e2 != nil || !bytes.Equal(o1, bodyOK[0]) || !bytes.Equal(o2, bodyOK[1])) {
				t.Fatalf("bodies ok, fast X1 %v X2 %v", e1, e2)
			}
		}
	})
}

// TestHUFReadStatsFSEWorkspace pins C's FSE workspace budget in
// HUF_readStats: FSE_decompress_wksp_body (fse_decompress.c) returns
// tableLog_tooLarge when FSE_DECOMPRESS_WKSP_SIZE(tableLog, maxSymbolValue)
// exceeds HUF_READ_STATS_WORKSPACE_SIZE = FSE_DECOMPRESS_WKSP_SIZE(6, 11).
// This well-formed weight header has FSE tableLog 6 and maxSymbolValue 12
// (weights 12, 5x127, 4, 3, 2, 1, implied 1); C rejects it.
func TestHUFReadStatsFSEWorkspace(t *testing.T) {
	desc := []byte{0x0d, 0x11, 0x20, 0x08, 0x82, 0xe0, 0xb7, 0x03, 0xc0, 0x4a, 0x09, 0xc0, 0xce, 0xaa}
	var w [SymbolValueMax + 1]byte
	var rank [TableLogMax + 1]uint32
	if _, _, _, err := ReadStats(w[:], rank[:], desc); err != fse.ErrTableLogTooLarge {
		t.Fatalf("ReadStats err=%v, want fse.ErrTableLogTooLarge", err)
	}
	var dt DTable
	dt.Init(TableLogMax)
	if _, err := ReadDTableX1(&dt, desc, 0); err != fse.ErrTableLogTooLarge {
		t.Fatalf("ReadDTableX1 err=%v", err)
	}
	if _, err := ReadDTableX2(&dt, desc, 0); err != fse.ErrTableLogTooLarge {
		t.Fatalf("ReadDTableX2 err=%v", err)
	}
}
