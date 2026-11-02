package huf

import (
	"bytes"
	"encoding/binary"
	"sort"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
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
