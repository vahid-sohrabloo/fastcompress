package deflate

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// Assembly equivalence of the decoder (decompress_fastloop_amd64.s): every
// decode is run with the assembly off (the Go fast loop, on ordinary heap
// buffers) and on, with src and dst flush against PROT_NONE guard pages
// (both after the end and before the start), so that an access past either
// end faults. The result code, actual in/out counts and the whole dst
// buffer, including the bytes past the output that the match-copy overrun
// writes, must be identical. In builds or on CPUs without the assembly the
// comparisons run the Go loop twice.

// withDeflateAsm runs fn with every registered assembly routine switched on
// (when available, per cpu.X86 now) or off, then restores the switches.
func withDeflateAsm(on bool, fn func()) {
	routines := cpu.AsmRoutines()
	saved := make([]bool, len(routines))
	for i, r := range routines {
		saved[i] = *r.Use
		*r.Use = on && r.Available()
	}
	defer func() {
		for i, r := range routines {
			*r.Use = saved[i]
		}
	}()
	fn()
}

// asmEquivBufs holds guard-page buffers reused across decodes: a decode of
// n bytes uses the last n bytes of an "after" buffer or the first n of a
// "before" one, so only four mappings exist per test.
type asmEquivBufs struct {
	inAfter, inBefore, outAfter, outBefore []byte
	heapIn, heapOut                        []byte
}

func newAsmEquivBufs(t testing.TB, maxIn, maxOut int) *asmEquivBufs {
	return &asmEquivBufs{
		inAfter:   guardmem.After(t, maxIn),
		inBefore:  guardmem.Before(t, maxIn),
		outAfter:  guardmem.After(t, maxOut),
		outBefore: guardmem.Before(t, maxOut),
		heapIn:    make([]byte, maxIn),
		heapOut:   make([]byte, maxOut),
	}
}

// asmEquivResult is a decode's result with the whole dst buffer.
type asmEquivResult struct {
	res       libdeflateResult
	nIn, nOut int
	dst       []byte
}

// decodeInto copies src to in (len(in) == len(src)), fills dst, decodes with
// a fresh Decompressor (actual_in always requested; actual_out unless
// exact) and returns the result with a copy of dst.
func decodeInto(in, dst, src []byte, exact bool) asmEquivResult {
	copy(in, src)
	fillDst(dst)
	res, nIn, nOut := NewDecompressor().decompressImpl(in, dst, true, !exact)
	return asmEquivResult{res, nIn, nOut, append([]byte(nil), dst...)}
}

// check decodes src into a dstSize-byte buffer with the assembly off and on
// (src and dst after, then before, guard pages) and fails on any difference.
// It returns the assembly-off result code.
func (b *asmEquivBufs) check(t testing.TB, name string, src []byte, dstSize int, exact bool) libdeflateResult {
	t.Helper()
	if len(src) > len(b.heapIn) || dstSize > len(b.heapOut) {
		t.Fatalf("%s: %d/%d bytes exceed the buffers (%d/%d)", name, len(src), dstSize, len(b.heapIn), len(b.heapOut))
	}
	var want asmEquivResult
	withDeflateAsm(false, func() {
		want = decodeInto(b.heapIn[:len(src):len(src)], b.heapOut[:dstSize:dstSize], src, exact)
	})
	withDeflateAsm(true, func() {
		for _, l := range []struct {
			layout  string
			in, dst []byte
		}{
			{"after", b.inAfter[len(b.inAfter)-len(src):], b.outAfter[len(b.outAfter)-dstSize:]},
			{"before", b.inBefore[:len(src):len(src)], b.outBefore[:dstSize:dstSize]},
		} {
			got := decodeInto(l.in, l.dst, src, exact)
			if got.res != want.res || got.nIn != want.nIn || got.nOut != want.nOut || !bytes.Equal(got.dst, want.dst) {
				t.Fatalf("%s (%d bytes, dst %d, exact %v, guard %s): asm result=%d in=%d out=%d, Go result=%d in=%d out=%d, dst equal %v",
					name, len(src), dstSize, exact, l.layout, got.res, got.nIn, got.nOut, want.res, want.nIn, want.nOut,
					bytes.Equal(got.dst, want.dst))
			}
		}
	})
	return want.res
}

// asmEquivCompress compresses data at level with the Go compressor
// (byte-identical to libdeflate's; levels 0..12).
func asmEquivCompress(t testing.TB, data []byte, level int) []byte {
	t.Helper()
	c, err := NewCompressor(level)
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, c.CompressBound(len(data)))
	n := c.Compress(dst, data)
	if n == 0 {
		t.Fatalf("level %d: Compress returned 0", level)
	}
	return dst[:n]
}

// asmEquivCorpus returns corpus cases of at most maxLen bytes compressed at
// every level 0..12 (levels 10..12 only for cases up to 64 KiB in -short)
// and by the stdlib at a few levels, with their sizes.
func asmEquivCorpus(t testing.TB, maxLen int) []diffStream {
	var ss []diffStream
	for _, c := range deflatetest.Cases(true) {
		if len(c.Data) > maxLen {
			continue
		}
		for l := 0; l <= 12; l++ {
			if testing.Short() && l >= 10 && len(c.Data) > 64<<10 {
				continue
			}
			ss = append(ss, diffStream{fmt.Sprintf("%s/L%d", c.Name, l), asmEquivCompress(t, c.Data, l), len(c.Data)})
		}
		for _, l := range []int{-2, 1, 9} {
			ss = append(ss, diffStream{fmt.Sprintf("%s/std%d", c.Name, l), stdlibCompress(t, c.Data, l), len(c.Data)})
		}
	}
	return ss
}

// outputMarginStreams are TestFastloopOutputMargin's streams: every fast
// loop iteration writes lit, lit and a 257/258-byte match at offset 1, 2
// or 8 (the worst case written), plus offsets 16 and 31 (16-byte copies);
// 64 trailing bytes keep the input margin out of the way.
func outputMarginStreams() []diffStream {
	var ss []diffStream
	r := rand.New(rand.NewPCG(11, 12))
	for _, L := range []int{257, 258} {
		for _, o := range []int{1, 2, 7, 8, 15, 16, 31} {
			toks := litsOf(rndBytes(uint64(L*o), 32))
			for k := 0; k < 8; k++ {
				toks = append(toks, lit(byte(r.Uint64())), lit(byte(r.Uint64())), match(L, o))
			}
			var w bitWriter
			w.static(true, toks)
			ss = append(ss, diffStream{fmt.Sprintf("outmargin/L%d/o%d", L, o), append(w.bytes(), make([]byte, 64)...),
				len(tokOutput(nil, toks))})
		}
	}
	return ss
}

// offsetStreams returns matches at every offset 1..300 with random lengths
// 3..258 mixed with literals, in one static block, and the same followed by
// a dynamic block with subtables (long litlen and offset codewords).
func offsetStreams() []diffStream {
	var ss []diffStream
	r := rand.New(rand.NewPCG(5, 6))
	for _, final := range []bool{true, false} {
		toks := litsOf(rndBytes(77, 300))
		for o := 1; o <= 300; o++ {
			toks = append(toks, match(3+r.IntN(256), o))
			if o%3 == 0 {
				toks = append(toks, lit(byte(r.Uint64())))
			}
		}
		var w bitWriter
		w.static(final, toks)
		if !final {
			dyn := append(lits("jxqz"), match(258, 1), match(8, 4), lit('e'), match(24, 7), match(258, 30),
				match(24, 200), match(3, 500))
			w.dynamic(true, chainLens(286, []int{'e', 't', 256, 'a', 257, 'o', 258, 'i', 262, 'n', 'z', 285, 'q', 270, 'x', 'j'}),
				chainLens(30, []int{0, 1, 3, 5, 9, 12, 15, 17, 20, 22, 24, 25, 26, 27, 28, 29}), dyn)
			toks = append(toks, dyn...)
		}
		ss = append(ss, diffStream{fmt.Sprintf("offsets/final=%v", final), w.bytes(), len(tokOutput(nil, toks))})
	}
	return ss
}

// refillStreams pin the two refill thresholds of the offset decode. The
// thresholds are on bitsleft, but bitbuf holds 64 - c valid bits after c
// bits consumed since the last refill, so a missing refill only shows when
// those run out. Each match is preceded by 0..2 random 11-bit literals and
// its offset is distance symbol 29 (13 extra bits), coded in 15 bits (a
// subtable: OFFSET_SUB_REFILL, 28 bits then 11 preloaded) or in 1 or 8 bits
// (main table: OFFSET_FAST_REFILL). With 15-bit lengths with 5 extra bits
// (c = 20..42) a missing refill shows; with lengths 9 (15-bit codeword, no
// extra bits: c = 26 after one literal) and 10 (11-bit codeword: c = 33
// after two) the thresholds are pinned exactly: one less than C's leaves
// the last preloaded bit short when bitsleft ends at 63 after the refill.
func refillStreams() []diffStream {
	var ss []diffStream
	for _, v := range []struct {
		name    string
		lenSyms []int // the litlen chain after 256 (1 bit) and 257 (2 bits): 4, 5, ... 15, 15 bits
		offUsed []int
		lens    []int // lenSym indexes of the match lengths
	}{
		{"sub", []int{258, 259, 260, 261, 262, 263, 264, 265, 266, 267, 268, 283, 284}, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 29}, []int{26, 27}},
		{"fast", []int{258, 259, 260, 261, 262, 263, 264, 265, 266, 267, 268, 283, 284}, []int{29, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}, []int{26, 27}},
		{"sub-exact", []int{258, 259, 260, 261, 262, 265, 266, 264, 267, 268, 283, 284, 263}, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 29}, []int{6}},
		{"fast-exact", []int{258, 259, 260, 261, 262, 265, 266, 264, 267, 268, 283, 284, 263}, []int{0, 1, 2, 3, 4, 5, 6, 29, 7, 8, 9, 10, 11, 12, 13, 14}, []int{7}},
	} {
		litLens := make([]uint8, 286)
		for s := 0; s < 256; s++ {
			litLens[s] = 11
		}
		litLens[256], litLens[257] = 1, 2
		for i, s := range v.lenSyms {
			litLens[s] = uint8(min(4+i, 15))
		}
		r := rand.New(rand.NewPCG(13, 17))
		toks := litsOf(rndBytes(19, 32768))
		dlo, dhi := distRange(29)
		for u := 0; u < 600; u++ {
			for k := r.IntN(3); k > 0; k-- {
				toks = append(toks, lit(byte(r.Uint64())))
			}
			lo, hi := lenRange(v.lens[r.IntN(len(v.lens))])
			toks = append(toks, match(lo+r.IntN(hi-lo+1), dlo+r.IntN(dhi-dlo+1)))
		}
		var w bitWriter
		w.dynamic(true, litLens, chainLens(30, v.offUsed), toks)
		ss = append(ss, diffStream{"refill/" + v.name, w.bytes(), len(tokOutput(nil, toks))})
	}
	return ss
}

// TestDeflateAsmEquiv runs the decoder tests (several against libdeflate)
// with the assembly off, on, and on a BMI2 host on without BMI2, then
// compares the assembly with the Go fast loop directly (asmEquivBufs.check)
// on golden, corpus (all levels), margin-boundary, offset-sweep, mutated and
// random streams.
func TestDeflateAsmEquiv(t *testing.T) {
	if !useFastloopBMI2 {
		t.Log("no assembly fast loop in this build or CPU: the comparisons run the Go loop twice")
	}
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"DecompressGolden", TestDecompressGolden},
		{"DecompressGoldenStdlib", TestDecompressGoldenStdlib},
		{"DecompressDstSizes", TestDecompressDstSizes},
		{"RoundTripLibdeflate", TestRoundTripLibdeflate},
		{"RoundTripStdlib", TestRoundTripStdlib},
		{"FastloopOutputMargin", TestFastloopOutputMargin},
		{"FastloopInputMargin", TestFastloopInputMargin},
		{"DecompressLongCodes", TestDecompressLongCodes},
		{"DecompressNoEOBBoundary", TestDecompressNoEOBBoundary},
		{"PortInvalidStreams", TestPortInvalidStreams},
		{"InvalidOffsets", TestInvalidOffsets},
		{"PortOverread", TestPortOverread},
		{"OverreadLayouts", TestOverreadLayouts},
		{"FuzzSeedsReuse", TestFuzzSeedsReuse},
		{"DiffValidStreams", TestDiffValidStreams},
		{"DiffTruncations", TestDiffTruncations},
		{"DiffBitFlips", TestDiffBitFlips},
		{"DiffLongCodes", TestDiffLongCodes},
		{"DiffNoEOBBoundary", TestDiffNoEOBBoundary},
		{"ContainerDecompressValid", TestContainerDecompressValid},
	}
	// The tests of one mode run as parallel cases: t.Run(name) returns once
	// they all ended, so the mode does not change under them.
	run := func(name string, on bool) {
		withDeflateAsm(on, func() {
			t.Run(name, func(t *testing.T) {
				for _, c := range tests {
					t.Run(c.name, func(t *testing.T) {
						parallelCase(t)
						c.fn(t)
					})
				}
			})
		})
	}
	run("asm=off", false)
	run("asm=on", true)
	if cpu.X86.HasBMI2 {
		restore := cpu.DisableBMI2()
		run("asm=on,bmi2=off", true)
		restore()
	}

	maxLen := 1 << 20
	if testing.Short() {
		maxLen = 1 << 17
	}
	b := newAsmEquivBufs(t, 2*maxLen, 2*maxLen+1024)
	t.Run("golden", func(t *testing.T) {
		for _, g := range goldenCases() {
			size := len(g.out)
			if g.res != libdeflateSuccess {
				size = 1000
			}
			if len(g.in) > maxLen {
				continue
			}
			for _, n := range dstSizes(size) {
				if n > len(b.heapOut) {
					continue
				}
				for _, exact := range []bool{false, true} {
					b.check(t, g.name, g.in, n, exact)
				}
			}
		}
	})
	corpus := asmEquivCorpus(t, maxLen)
	t.Run("corpus", func(t *testing.T) {
		for _, s := range corpus {
			for _, n := range []int{s.size, s.size + 1, s.size - 1, s.size + 1000, 0} {
				if n < 0 {
					continue
				}
				if r := b.check(t, s.name, s.in, n, n == s.size); n == s.size && r != libdeflateSuccess {
					t.Fatalf("%s: does not decode: %d", s.name, r)
				}
			}
			// Trailing bytes after the stream.
			tr := append(append([]byte(nil), s.in...), 0x00, 0xff, 0x13, 0x37, 0, 0, 0, 0, 0, 0, 0, 0)
			b.check(t, s.name+"/trailing", tr, s.size+10, false)
		}
	})
	// The output end swept across the fast loop margin, so the assembly
	// stops at every position relative to out_fastloop_end; the input
	// cut and extended near its end, for in_fastloop_end.
	t.Run("margins", func(t *testing.T) {
		ss := outputMarginStreams()
		ss = append(ss, offsetStreams()...)
		for seed := uint64(0); seed < 2; seed++ {
			in, want := inputMarginStream(seed)
			ss = append(ss, diffStream{fmt.Sprintf("inmargin/%d", seed), in, len(want)})
		}
		for _, c := range corpus {
			if c.size >= 2000 && c.size <= 40000 && len(ss) < 60 {
				ss = append(ss, c)
			}
		}
		for _, s := range ss {
			for x := -320; x <= 320; x++ {
				if n := s.size + x; n >= 0 {
					b.check(t, fmt.Sprintf("%s/dst%+d", s.name, x), s.in, n, x == 0)
				}
			}
			for k := 0; k <= 64 && k <= len(s.in); k++ {
				b.check(t, fmt.Sprintf("%s/cut%d", s.name, k), s.in[:len(s.in)-k], s.size, false)
				b.check(t, fmt.Sprintf("%s/cut%d/exact", s.name, k), s.in[:len(s.in)-k], s.size, true)
			}
			for k := 1; k <= 32; k++ {
				ext := append(append([]byte(nil), s.in...), make([]byte, k)...)
				b.check(t, fmt.Sprintf("%s/ext%d", s.name, k), ext, s.size+300, false)
			}
		}
	})
	t.Run("refill", func(t *testing.T) {
		for _, s := range refillStreams() {
			for _, n := range []int{s.size, s.size + 1, s.size - 1, s.size + 300} {
				if r := b.check(t, s.name, s.in, n, n == s.size); n == s.size && r != libdeflateSuccess {
					t.Fatalf("%s: does not decode: %d", s.name, r)
				}
			}
		}
	})
	t.Run("mutations", func(t *testing.T) {
		r := rand.New(rand.NewPCG(31, 41))
		n := 0
		for i, s := range corpus {
			if len(s.in) < 8 || i%3 != 0 {
				continue
			}
			for k := 0; k < 12; k++ {
				m := append([]byte(nil), s.in...)
				for f := 0; f <= k%3; f++ {
					p := r.IntN(len(m))
					m[p] ^= 1 << r.IntN(8)
				}
				b.check(t, fmt.Sprintf("%s/flip%d", s.name, k), m, s.size+r.IntN(400), k%2 == 0)
				n++
			}
			cut := r.IntN(len(s.in))
			b.check(t, fmt.Sprintf("%s/trunc%d", s.name, cut), s.in[:cut], s.size, false)
		}
		if n == 0 {
			t.Fatal("no mutations ran")
		}
	})
	t.Run("random", func(t *testing.T) {
		for seed := uint64(0); seed < 200; seed++ {
			src := rndBytes(seed, 1+int(seed*37%5000))
			b.check(t, fmt.Sprintf("random/%d", seed), src, 1<<16, false)
		}
	})
	// The per-call output bound (fastloopChunk) lowered so that the
	// assembly returns and is re-entered every few iterations (every one
	// with 1): the margin, offset, refill and input-margin streams, a
	// corpus sample and mutants, each against the Go loop (guard pages)
	// and against the unbounded assembly.
	t.Run("chunked", func(t *testing.T) {
		ss := append(outputMarginStreams(), offsetStreams()...)
		ss = append(ss, refillStreams()...)
		for seed := uint64(0); seed < 2; seed++ {
			in, want := inputMarginStream(seed)
			ss = append(ss, diffStream{fmt.Sprintf("inmargin/%d", seed), in, len(want)})
		}
		for i, c := range corpus {
			if i%7 == 0 && c.size <= 200000 {
				ss = append(ss, c)
			}
		}
		r := rand.New(rand.NewPCG(7, 8))
		for _, s := range ss {
			flip := append([]byte(nil), s.in...)
			flip[r.IntN(len(flip))] ^= 1 << r.IntN(8)
			for _, v := range []struct {
				name  string
				in    []byte
				n     int
				exact bool
			}{
				{s.name, s.in, s.size, true},
				{s.name + "/dst-1", s.in, max(0, s.size-1), false},
				{s.name + "/dst+300", s.in, s.size + 300, false},
				{s.name + "/cut", s.in[:len(s.in)-min(len(s.in), 20)], s.size, false},
				{s.name + "/flip", flip, s.size + 50, false},
			} {
				b.checkChunked(t, v.name, v.in, v.n, v.exact, []int{1, 64, 300, 1000})
			}
		}
	})
}

// checkChunked runs check with fastloopChunk set to each of chunks, and
// requires the assembly-on result (heap buffers) with each to equal the
// one with the default bound (fastloopChunkOut).
func (b *asmEquivBufs) checkChunked(t testing.TB, name string, src []byte, dstSize int, exact bool, chunks []int) {
	t.Helper()
	defer func(c int) { fastloopChunk = c }(fastloopChunk)
	asmOn := func() (r asmEquivResult) {
		withDeflateAsm(true, func() {
			r = decodeInto(b.heapIn[:len(src):len(src)], b.heapOut[:dstSize:dstSize], src, exact)
		})
		return r
	}
	fastloopChunk = fastloopChunkOut
	want := asmOn()
	for _, c := range chunks {
		fastloopChunk = c
		b.check(t, fmt.Sprintf("%s/chunk%d", name, c), src, dstSize, exact)
		if got := asmOn(); got.res != want.res || got.nIn != want.nIn || got.nOut != want.nOut || !bytes.Equal(got.dst, want.dst) {
			t.Fatalf("%s (chunk %d): result=%d in=%d out=%d, unbounded result=%d in=%d out=%d, dst equal %v",
				name, c, got.res, got.nIn, got.nOut, want.res, want.nIn, want.nOut, bytes.Equal(got.dst, want.dst))
		}
	}
}

// FuzzDeflateAsmEquiv decodes arbitrary bytes (data[0] selects the dst size
// and exact mode, as in FuzzDecompress) with the assembly fast loop off and
// on, src and dst against guard pages, and requires identical results and
// dst bytes (asmEquivBufs.check), and the same for the stream decoder
// (fuzzStreamAsm). Seeds: FuzzDecompress's, corpus streams at
// every level, the margin and offset streams.
func FuzzDeflateAsmEquiv(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	for _, c := range deflatetest.Cases(true) {
		if len(c.Data) > 8<<10 || len(c.Data) < 64 {
			continue
		}
		for _, l := range []int{1, 4, 6, 9, 12} {
			f.Add(append([]byte{byte(l) << 1}, asmEquivCompress(f, c.Data, l)...))
		}
	}
	for _, s := range append(outputMarginStreams(), offsetStreams()...) {
		f.Add(append([]byte{0}, s.in...))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r := fuzzReq(data)
		r.outSize = min(r.outSize, 4<<20)
		b := newAsmEquivBufs(t, len(r.in), r.outSize)
		b.check(t, "fuzz", r.in, r.outSize, r.exact)
		// Again with the per-call output bound lowered to 1..1000 bytes
		// (a hash of the input), against the Go loop and the unbounded
		// assembly.
		h := len(data)
		for _, c := range data {
			h = (h*31 + int(c)) % 1000
		}
		b.checkChunked(t, "fuzz", r.in, r.outSize, r.exact, []int{1 + h})
		// The stream decoder on the same input (decompress_stream_asm_equiv_test.go).
		fuzzStreamAsm(t, r.in, h)
	})
}

// BenchmarkDecompressFastloop decodes a text corpus case compressed at
// levels 1, 6 and 9 with the assembly fast loop off and on.
func BenchmarkDecompressFastloop(b *testing.B) {
	var data []byte
	for _, c := range deflatetest.Cases(true) {
		if c.Name == "size-text/300001" {
			data = c.Data
		}
	}
	if data == nil {
		b.Fatal("corpus case size-text/300001 not found")
	}
	for _, l := range []int{1, 6, 9} {
		comp := asmEquivCompress(b, data, l)
		for _, on := range []bool{false, true} {
			b.Run(fmt.Sprintf("L%d/asm=%v", l, on), func(b *testing.B) {
				d := NewDecompressor()
				dst := make([]byte, len(data))
				b.SetBytes(int64(len(data)))
				withDeflateAsm(on, func() {
					for b.Loop() {
						if _, err := d.Decompress(dst, comp); err != nil {
							b.Fatal(err)
						}
					}
				})
			})
		}
	}
}
