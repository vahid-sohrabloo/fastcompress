package deflate

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// Overread suite (task T1.3), porting programs/test_overread.c and the
// intent of the guarded-buffer tests of the C programs.
//
// libdeflate's decompressor may look at (but not use) a few bytes past the end
// of its input in C, which is why the C tests treat "input followed by
// unmapped memory" carefully. The Go port never reads past len(src): Go
// bounds-checks every access, so an overread would panic here (an index
// out of range) instead of faulting on a guard page, and there is no unsafe or
// assembly code in the decoder yet that could skip the checks. These tests
// therefore assert three things:
//
//  1. no panic, for every stream and every truncation of it (each decode runs
//     under recover, so a bounds failure is reported with the stream name
//     instead of killing the test binary);
//  2. the result (class, actual_in, actual_out, output bytes) depends on the
//     bytes of src only, never on what follows them in memory: the same
//     src, as an exact copy, as the tail of an mmap'ed region followed by a
//     PROT_NONE page (guardmem.After), as the head of a region preceded by
//     one (guardmem.Before), and as a prefix of a larger buffer (cap > len)
//     filled behind it with 0x00, 0xFF, random bytes or the stream's real
//     continuation, always gives the same outcome; and the outcome equals
//     libdeflate's (ldref);
//  3. when the input ends in the middle of a symbol, the decoder treats the
//     missing bytes as zeros but stops after more than 8 implicit bytes
//     (BAD_DATA), so a stream whose zero bits decode to literals cannot
//     produce unbounded output (test_overread.c).
//
// The assembly decoders of later tasks must satisfy the same tests with real
// guard pages, which is why the guard-memory layouts are already here.

// outcome is the observable result of one decompression.
type outcome struct {
	res       libdeflateResult
	nIn, nOut int
	out       []byte
}

func (o outcome) equal(p outcome) bool {
	return o.res == p.res && o.nIn == p.nIn && o.nOut == p.nOut && bytes.Equal(o.out, p.out)
}

func (o outcome) String() string {
	return fmt.Sprintf("result=%d in=%d out=%d (%d output bytes)", o.res, o.nIn, o.nOut, len(o.out))
}

// decodeOutcome decompresses src into a dstSize-byte buffer (reused through
// buf), failing the test if the decoder panics. The output is only kept for
// a successful decompression.
func decodeOutcome(t testing.TB, d *Decompressor, name string, src []byte, dstSize int, exact bool, buf *[]byte) (o outcome) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: decoder panicked on %d bytes of input (dst %d, exact %v): %v", name, len(src), dstSize, exact, r)
		}
	}()
	if cap(*buf) < dstSize {
		*buf = make([]byte, dstSize)
	}
	dst := (*buf)[:dstSize]
	o.res, o.nIn, o.nOut = d.decompressImpl(src, dst, true, !exact)
	if o.res != libdeflateSuccess {
		return outcome{res: o.res}
	}
	if exact {
		o.nOut = dstSize
	}
	if o.nIn > len(src) || o.nOut > dstSize {
		t.Fatalf("%s: counts out of range: in=%d (input %d), out=%d (dst %d)", name, o.nIn, len(src), o.nOut, dstSize)
	}
	o.out = append([]byte(nil), dst[:o.nOut]...)
	return o
}

// layouts returns the memory layouts under test for src: each is a slice
// with the bytes of src and different memory behind (and before) it. The
// guard layouts are only produced when guard is true (an mmap each).
func layouts(t *testing.T, src []byte, guard bool, r *rand.Rand) map[string][]byte {
	ls := map[string][]byte{}
	exact := append([]byte(nil), src...)
	ls["exact"] = exact[:len(src):len(src)]
	for _, fill := range []struct {
		name string
		f    func(i int) byte
	}{
		{"cap-0x00", func(int) byte { return 0 }},
		{"cap-0xff", func(int) byte { return 0xff }},
		{"cap-random", func(int) byte { return byte(r.Uint64()) }},
	} {
		b := make([]byte, len(src)+64)
		for i := range b {
			b[i] = fill.f(i)
		}
		copy(b, src)
		ls[fill.name] = b[:len(src)] // cap > len, garbage behind
	}
	if guard {
		a := guardmem.After(t, len(src))
		copy(a, src)
		ls["guard-after"] = a
		b := guardmem.Before(t, len(src))
		copy(b, src)
		ls["guard-before"] = b
	}
	return ls
}

// port of programs/test_overread.c:generate_test_input.
func streamOverread() []byte {
	var w bitWriter
	w.bits(0, 1) // BFINAL: 0
	w.bits(2, 2) // BTYPE: DYNAMIC_HUFFMAN
	// Litlen code: litlensym_0 len=1 codeword=0, litlensym_256 len=1
	// codeword=1. Offset code: offsetsym_0 (unused) len=1 codeword=0.
	w.bits(0, 5)  // num_litlen_syms: 0 + 257
	w.bits(0, 5)  // num_offset_syms: 0 + 1
	w.bits(14, 4) // num_explicit_precode_lens: 14 + 4
	w.bits(0, 3)  // presym_16: len=0
	w.bits(0, 3)  // presym_17: len=0
	w.bits(1, 3)  // presym_18: len=1
	for i := 0; i < 14; i++ {
		w.bits(0, 3) // presym_{0,...,14}: len=0
	}
	w.bits(1, 3) // presym_1: len=1
	// Litlen and offset codeword lengths.
	w.bits(0, 1)   // presym_1
	w.bits(1, 1)   // presym_18 ...
	w.bits(117, 7) // ... 11 + 117 zeroes
	w.bits(1, 1)   // presym_18 ...
	w.bits(116, 7) // ... 11 + 116 zeroes
	w.bits(0, 1)   // presym_1
	w.bits(0, 1)   // presym_1
	// Implicit zeroes would generate endless literals from here.
	return w.bytes()
}

// port of programs/test_overread.c:tmain. The C test decodes the 16-byte
// buffer into udata[256] and requires LIBDEFLATE_BAD_DATA ("before the fix,
// the result was LIBDEFLATE_INSUFFICIENT_SPACE here"). The input ends in
// the middle of the block, the zero bits decode as literal 0 forever, and
// the decoder must give up once it has overread more than 8 bytes.
func TestPortOverread(t *testing.T) {
	in := streamOverread()
	if len(in) > 16 {
		t.Fatalf("stream is %d bytes, the C buffer holds 16", len(in))
	}
	cases := []portCase{{name: "test_overread/udata256", in: in, dst: 256, res: libdeflateBadData}}
	for _, dst := range []int{256, 300, 1000, 1 << 16, 1 << 20} {
		for _, exact := range []bool{false, true} {
			cases = append(cases, portCase{name: fmt.Sprintf("dst%d/exact%v", dst, exact),
				in: in, dst: dst, exact: exact, res: libdeflateBadData})
		}
	}
	// Smaller buffers fill up with the literals before the overread limit is
	// reached: INSUFFICIENT_SPACE. The boundary is where the number of
	// literals the decoder will produce before giving up is exhausted. It is
	// found with the Go decoder, but checkPort then pins every dst to the
	// expectation (and to libdeflate's result when ldref exists).
	d := NewDecompressor()
	var buf []byte
	limit := -1
	for dst := 0; dst < 300; dst++ {
		o := decodeOutcome(t, d, "boundary", in, dst, false, &buf)
		if o.res == libdeflateBadData {
			limit = dst
			break
		}
		cases = append(cases, portCase{name: fmt.Sprintf("dst%d", dst), in: in, dst: dst, res: libdeflateInsufficientSpace})
	}
	if limit < 0 || limit > 256 {
		t.Fatalf("BAD_DATA only for dst >= %d, the C test needs it at 256", limit)
	}
	t.Logf("test_overread stream: %d bytes; BAD_DATA from dst = %d bytes (literals before giving up)", len(in), limit)
	for dst := limit; dst < limit+20; dst++ {
		cases = append(cases, portCase{name: fmt.Sprintf("dst%d", dst), in: in, dst: dst, res: libdeflateBadData})
	}
	checkPort(t, cases)
	// Every prefix of the stream, against libdeflate.
	var reqs []ldReq
	for k := 0; k <= len(in); k++ {
		reqs = append(reqs, ldReq{fmt.Sprintf("prefix%d", k), in[:k], 256, false})
	}
	diffCheckIfLD(t, reqs)
}

// overreadStreams are the streams of the layout / prefix tests: goldens
// (valid and invalid) and libdeflate / stdlib compressions of corpus cases.
func overreadStreams(t *testing.T) []diffStream {
	ss := diffStreams(t)
	for i, s := range [][]byte{streamTooManyLens(), streamEmptyOffsetCode(), streamSingletonLitlen(),
		streamSingletonOffset(), streamSingletonOffsetNotSym0(), streamOverread()} {
		ss = append(ss, diffStream{fmt.Sprintf("port%d", i), s, 200})
	}
	return ss
}

// TestOverreadLayouts: every stream, whole, in every memory layout, with
// four output sizes and both APIs: identical outcomes, equal to libdeflate's.
func TestOverreadLayouts(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	ss := overreadStreams(t)
	r := rand.New(rand.NewPCG(11, 12))
	d := NewDecompressor()
	var buf []byte
	var reqs []ldReq
	for _, s := range ss {
		t.Run(s.name, func(t *testing.T) {
			ls := layouts(t, s.in, true, r)
			for _, dst := range []int{s.size, s.size + 100, 0, max(s.size-1, 0)} {
				for _, exact := range []bool{false, true} {
					name := fmt.Sprintf("%s/dst%d/exact%v", s.name, dst, exact)
					want := decodeOutcome(t, d, name+"/exact-copy", ls["exact"], dst, exact, &buf)
					reqs = append(reqs, ldReq{name, s.in, dst, exact})
					for ln, src := range ls {
						got := decodeOutcome(t, d, name+"/"+ln, src, dst, exact, &buf)
						if !got.equal(want) {
							t.Errorf("%s: layout %s gives %v, exact copy gives %v", name, ln, got, want)
						}
					}
				}
			}
		})
	}
	// The reference outcome (exact copy) is libdeflate's.
	diffCheck(t, reqs)
}

// TestOverreadTruncations: every prefix of each stream of at most 512 bytes (300
// random prefixes of each bigger one; -short: 128, 20 and every 4th stream), as an exact copy, with every kind of memory
// behind it (including, for the sampled ones, a guard page), and as a
// prefix of the real stream (cap > len, real continuation behind it).
// Truncated input is the case in which the decoder substitutes zeros for
// missing bytes; the outcome must not depend on the bytes behind src, and
// must equal libdeflate's.
func TestOverreadTruncations(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	ss := overreadStreams(t)
	r := rand.New(rand.NewPCG(13, 14))
	d := NewDecompressor()
	var buf []byte
	dr := newDiffRunner(t)
	nOK, nTotal := 0, 0
	// Full runs enumerate every prefix of streams of at most 512 bytes and
	// put a guard page behind about 1 prefix in 97; -short shrinks these to
	// 128 bytes and 1 in 400, 20 random prefixes of the bigger streams, and
	// every 4th stream (fixed, deterministic subsets).
	nBig, fullMax, guardEvery, streamStep := 300, 512, 97, 1
	if testing.Short() {
		nBig, fullMax, guardEvery, streamStep = 20, 128, 400, 4
	}
	for si, s := range ss {
		if si%streamStep != 0 {
			continue
		}
		var ks []int
		if len(s.in) <= fullMax {
			for k := 0; k < len(s.in); k++ {
				ks = append(ks, k)
			}
		} else {
			for i := 0; i < nBig; i++ {
				ks = append(ks, int(r.Uint64()%uint64(len(s.in))))
			}
			ks = append(ks, len(s.in)-1, len(s.in)-2, len(s.in)-8, len(s.in)-9)
		}
		t.Run(s.name, func(t *testing.T) {
			for i, k := range ks {
				pre := s.in[:k] // cap > len: the real continuation follows
				guard := i%guardEvery == 0 || k == len(s.in)-1
				ls := layouts(t, pre, guard, r)
				ls["real-continuation"] = pre
				for _, pol := range []struct {
					dst   int
					exact bool
				}{{s.size + 50, false}, {s.size, true}} {
					name := fmt.Sprintf("%s/prefix%d/dst%d/exact%v", s.name, k, pol.dst, pol.exact)
					want := decodeOutcome(t, d, name, ls["exact"], pol.dst, pol.exact, &buf)
					nTotal++
					if want.res == libdeflateSuccess {
						nOK++
					}
					for ln, src := range ls {
						if ln == "exact" {
							continue
						}
						got := decodeOutcome(t, d, name+"/"+ln, src, pol.dst, pol.exact, &buf)
						if !got.equal(want) {
							t.Errorf("%s: layout %s gives %v, exact copy gives %v", name, ln, got, want)
						}
					}
					dr.add(ldReq{name, s.in[:k:k], pol.dst, pol.exact})
				}
			}
		})
	}
	dr.done()
	t.Logf("%d truncated decodes, %d of them successful (input ended in padding / implicit zeros)", nTotal, nOK)
}

// TestOverreadNoPanicGarbage: random strings and mutated streams in a
// guard-page layout, any result is fine as long as nothing panics and the
// counts are in range (decodeOutcome checks both).
func TestOverreadNoPanicGarbage(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16))
	d := NewDecompressor()
	var buf []byte
	n := 3000
	if testing.Short() {
		n = 600
	}
	ss := overreadStreams(t)
	t.Run("garbage", func(t *testing.T) {
		for i := 0; i < n; i++ {
			var src []byte
			if i%2 == 0 {
				src = make([]byte, int(r.Uint64()%300))
				for j := range src {
					src[j] = byte(r.Uint64())
				}
				if len(src) > 0 {
					src[0] = src[0]&^6 | byte(1+i/2%2)<<1
				}
			} else {
				s := ss[int(r.Uint64()%uint64(len(ss)))]
				src = append([]byte(nil), s.in[:int(r.Uint64()%uint64(len(s.in)+1))]...)
				if len(src) > 0 {
					src[int(r.Uint64()%uint64(len(src)))] ^= byte(1 << (r.Uint64() % 8))
				}
			}
			var g []byte
			if i%25 == 0 {
				g = guardmem.After(t, len(src))
				copy(g, src)
			} else {
				g = src
			}
			for _, dst := range []int{0, 17, 4096, 1 << 18} {
				decodeOutcome(t, d, fmt.Sprintf("garbage%d", i), g, dst, i%3 == 0, &buf)
			}
		}
	})
}
