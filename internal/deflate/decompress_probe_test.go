package deflate

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Targeted streams for the refill thresholds and buffer margins of
// decompress_generic.go. Each test names the condition it protects; the T1.2
// ledger lists the mutants each one kills.

// lenRange / distRange give the values coded by length symbol index ls
// (0..28 => 257..285) and distance symbol ds. Length 258 is only produced
// through symbol 285 (lenSym), so it is excluded from symbol 284's range.
func lenRange(ls int) (lo, hi int) {
	lo = tLenBase[ls]
	hi = lo + 1<<tLenExtra[ls] - 1
	if ls == 27 {
		hi = 257
	}
	return lo, hi
}

func distRange(ds int) (lo, hi int) {
	lo = tDistBase[ds]
	hi = min(lo+1<<tDistExtra[ds]-1, 32768)
	return lo, hi
}

// randToks appends n random tokens using only codes present in litLens and
// offLens (literals with probability 1/3 when available); outLen is the
// output length before them. It returns the tokens and the new length.
func randToks(r *rand.Rand, toks []tok, n, outLen int, litLens, offLens []uint8) ([]tok, int) {
	var litSyms, lenSyms, distSyms []int
	for s, l := range litLens {
		switch {
		case l == 0 || s == 256:
		case s < 256:
			litSyms = append(litSyms, s)
		default:
			lenSyms = append(lenSyms, s-257)
		}
	}
	for s, l := range offLens {
		if l != 0 && s < 30 {
			distSyms = append(distSyms, s)
		}
	}
	for k := 0; k < n; k++ {
		var d int
		ok := false
		if len(lenSyms) > 0 && len(distSyms) > 0 && outLen > 0 && (len(litSyms) == 0 || r.IntN(3) != 0) {
			for try := 0; try < 8 && !ok; try++ {
				lo, hi := distRange(distSyms[r.IntN(len(distSyms))])
				if lo <= outLen {
					d = lo + r.IntN(min(hi, outLen)-lo+1)
					ok = true
				}
			}
		}
		if ok {
			lo, hi := lenRange(lenSyms[r.IntN(len(lenSyms))])
			l := lo + r.IntN(hi-lo+1)
			toks = append(toks, match(l, d))
			outLen += l
		} else if len(litSyms) > 0 {
			toks = append(toks, lit(byte(litSyms[r.IntN(len(litSyms))])))
			outLen++
		}
	}
	return toks, outLen
}

// longLitlenLens: a complete litlen code over all 286 symbols with 229
// 8-bit, 53 9-bit and 4 11-bit codewords, the 4 longest being literals. So
// litlen_tablebits is 11, literals are "fast" (no subtable; up to 3 per fast
// loop iteration, 33 bits), and a litlen preload reads all 11 index bits
// exactly when the next symbol is one of the 4 long literals. It returns the
// lengths and the 4 long literals.
func longLitlenLens(r *rand.Rand) ([]uint8, []int) {
	l := make([]uint8, 286)
	var long []int
	perm := r.Perm(286)
	for _, s := range perm {
		if s < 256 && len(long) < 4 {
			l[s] = 11
			long = append(long, s)
		}
	}
	n := 0
	for _, s := range perm {
		if l[s] != 0 {
			continue
		}
		if n < 229 {
			l[s] = 8
		} else {
			l[s] = 9
		}
		n++
	}
	return l, long
}

// longCodeStream is a dynamic block built to reach the two fast loop offset
// refill conditions at every value of uint8(bitsleft) near their
// thresholds, with a litlen preload right after that needs all 11 bits:
//   - order 0: the offset code is a chain over {0..13, 28, 29} with the two
//     15-bit codewords on 28/29 (28 bits per offset with 13 extra bits):
//     the offset-subtable refill, threshold OFFSET_MAXBITS +
//     LITLEN_TABLEBITS - PRELOAD_SLACK = 38;
//   - order 1: 28 gets the 8-bit codeword (21 bits, no subtable): the other
//     refill, threshold OFFSET_MAXFASTBITS + LITLEN_TABLEBITS -
//     PRELOAD_SLACK = 31 (29 gets 9 bits: a subtable);
//   - order 2: shuffled.
//
// Tokens: half the literals are the 4 11-bit ones, half the distances use
// 28/29, the rest random; lengths 3..258. A 32 KiB literal prefix makes
// every distance valid.
func longCodeStream(seed uint64, order int) (in, want []byte) {
	r := rand.New(rand.NewPCG(seed, 31))
	litLens, long := longLitlenLens(r)
	ou := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 28, 29}
	switch order {
	case 1:
		ou = []int{0, 1, 2, 3, 4, 5, 6, 28, 29, 7, 8, 9, 10, 11, 12, 13}
	case 2:
		r.Shuffle(len(ou), func(i, j int) { ou[i], ou[j] = ou[j], ou[i] })
	}
	offLens := chainLens(30, ou)
	toks := litsOf(rndBytes(seed, 32768))
	for k := 0; k < 6000; k++ {
		switch r.IntN(5) {
		case 0, 1:
			if r.IntN(2) == 0 {
				toks = append(toks, lit(byte(long[r.IntN(4)])))
			} else {
				toks = append(toks, lit(byte(r.Uint64())))
			}
		default:
			ds := 28 + r.IntN(2)
			if r.IntN(2) == 0 {
				ds = ou[r.IntN(len(ou))]
			}
			lo, hi := distRange(ds)
			toks = append(toks, match(3+r.IntN(256), lo+r.IntN(hi-lo+1)))
		}
	}
	var w bitWriter
	w.dynamic(true, litLens, offLens, toks)
	return w.bytes(), tokOutput(nil, toks)
}

// refillProbeStream places the two fast loop offset refill conditions at
// every critical bitsleft value with the smallest slack, deterministically
// often. It is built from units
//
//	A, B, match(len, far), X, Y, Z
//
// so that each unit is exactly two fast loop iterations: A (primary), B
// (1st extra literal), the length and the offset; then X (primary), Y and Z
// (extra literals; the next entry, A, is only preloaded, and the refill
// after it sets bitsleft to some B0 in 56..63, with 64 - B0 bits of preload
// slack). A, B and the length
// symbol (with its extra bits) are chosen to consume c = 26..33 bits, so the
// offset refill test sees uint8(bitsleft) = B0 - c. The offset is either
// symbol 29 with a 15-bit codeword (28 bits: threshold 38) or symbol 28 with
// an 8-bit codeword (21 bits: threshold 31). X is an 11-bit literal whose
// last codeword bit is 1, so a preload of X with fewer than 11 valid bits
// (a refill wrongly skipped) reads a 0 there and decodes another symbol.
// The Y and Z lengths vary, so B0 takes every value.
func refillProbeStream(seed uint64) (in, want []byte) {
	r := rand.New(rand.NewPCG(seed, 57))
	litLens, long := longLitlenLens(r)
	codes := canonCodes(litLens)
	ou := []int{0, 1, 2, 3, 4, 5, 6, 28, 7, 8, 9, 10, 11, 12, 29, 13}
	offLens := chainLens(30, ou) // 28: 8 bits, 29: 15 bits
	var lit8, lit9, xs []int
	for s := 0; s < 256; s++ {
		switch litLens[s] {
		case 8:
			lit8 = append(lit8, s)
		case 9:
			lit9 = append(lit9, s)
		}
	}
	for _, s := range long {
		if codes[s]&1 == 1 {
			xs = append(xs, s)
		}
	}
	litBy := map[int][]int{8: lit8, 9: lit9, 11: long}
	// Length choices: (litlen code length + extra bits, length value).
	type lenChoice struct{ bits, length int }
	var lens []lenChoice
	for ls := 0; ls < 29; ls++ {
		lo, hi := lenRange(ls)
		for v := lo; v <= hi; v += max(1, (hi-lo)/3) {
			lens = append(lens, lenChoice{int(litLens[257+ls]) + int(tLenExtra[ls]), v})
		}
	}
	pick := func(xs []int) int { return xs[r.IntN(len(xs))] }
	toks := litsOf(rndBytes(seed, 32768))
	for u := 0; u < 3000; u++ {
		c := 26 + r.IntN(8)
		for {
			la := []int{8, 9, 11}[r.IntN(3)]
			lb := []int{8, 9, 11}[r.IntN(3)]
			lc := lens[r.IntN(len(lens))]
			if la+lb+lc.bits != c {
				continue
			}
			ds := 28 + r.IntN(2)
			lo, hi := distRange(ds)
			toks = append(toks, lit(byte(pick(litBy[la]))), lit(byte(pick(litBy[lb]))),
				match(lc.length, lo+r.IntN(hi-lo+1)))
			break
		}
		toks = append(toks, lit(byte(pick(xs))), lit(byte(r.Uint64())), lit(byte(r.Uint64())))
	}
	var w bitWriter
	w.dynamic(true, litLens, offLens, toks)
	return w.bytes(), tokOutput(nil, toks)
}

// TestDecompressRefillProbe protects the fast loop offset refill
// thresholds 38 and 31 down to the last bit (see refillProbeStream).
func TestDecompressRefillProbe(t *testing.T) {
	d := NewDecompressor()
	for seed := uint64(0); seed < 4; seed++ {
		in, want := refillProbeStream(seed)
		dst := make([]byte, len(want))
		if n, err := d.Decompress(dst, in); err != nil || n != len(want) || !bytes.Equal(dst, want) {
			t.Fatalf("seed %d: n %d err %v, equal %v", seed, n, err, bytes.Equal(dst, want))
		}
	}
}

func TestDiffRefillProbe(t *testing.T) {
	creftest.LDBin(t)
	dr := newDiffRunner(t)
	for seed := uint64(0); seed < 4; seed++ {
		in, want := refillProbeStream(seed)
		dr.add(ldReq{fmt.Sprintf("refill-probe/%d", seed), in, len(want), true})
		dr.add(ldReq{fmt.Sprintf("refill-probe/%d/+1", seed), in, len(want) + 1, false})
	}
	dr.done()
}

const longCodeSeeds = 16

// TestDecompressLongCodes protects the two fast-loop offset refill
// thresholds (38 and 31, see longCodeStream) and the 25-byte input margin
// (the streams are cut in each of their last 40 bytes: the fast loop runs up
// to in_fastloop_end with maximal per-iteration input use). Oracle: tokOutput.
func TestDecompressLongCodes(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	d := NewDecompressor()
	for seed := uint64(0); seed < longCodeSeeds; seed++ {
		for order := 0; order < 3; order++ {
			in, want := longCodeStream(seed, order)
			name := fmt.Sprintf("seed %d order %d", seed, order)
			dst := make([]byte, len(want)+300)
			if n, err := d.Decompress(dst[:len(want)], in); err != nil || n != len(want) || !bytes.Equal(dst[:n], want) {
				t.Fatalf("%s: Decompress: n %d err %v, equal %v", name, n, err, bytes.Equal(dst[:len(want)], want))
			}
			nIn, nOut, err := d.DecompressEx(dst, in)
			if err != nil || nIn != len(in) || nOut != len(want) || !bytes.Equal(dst[:nOut], want) {
				t.Fatalf("%s: DecompressEx: nIn %d nOut %d err %v", name, nIn, nOut, err)
			}
			for k := 1; k <= 40; k++ {
				// A cut stream must not decode completely (and must not
				// panic: the input margin). The final block's last bits are
				// in the cut bytes.
				_, nOut, err := d.DecompressEx(dst, in[:len(in)-k])
				if err == nil && nOut == len(want) && k > 8 {
					t.Fatalf("%s cut %d: decoded completely", name, k)
				}
			}
		}
	}
}

// TestDiffLongCodes: the same streams (and their cuts) vs libdeflate.
func TestDiffLongCodes(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	dr := newDiffRunner(t)
	for seed := uint64(0); seed < longCodeSeeds; seed++ {
		for order := 0; order < 3; order++ {
			in, want := longCodeStream(seed, order)
			name := fmt.Sprintf("long/%d/%d", seed, order)
			dr.add(ldReq{name + "/exact", in, len(want), true})
			dr.add(ldReq{name + "/+300", in, len(want) + 300, false})
			for k := 1; k <= 40; k += 3 {
				dr.add(ldReq{fmt.Sprintf("%s/cut%d", name, k), in[:len(in)-k], len(want), k%2 == 0})
			}
		}
	}
	dr.done()
}

// deepPrecode: a complete precode in which presyms 16, 17 and 18 and one
// random length symbol have 7-bit codewords (one length gets 3 bits, one 5
// bits, 13 get 4 bits), so a presym 18 item uses 7 + 7 = 14 bits: the
// threshold DEFLATE_MAX_PRE_CODEWORD_LEN + 7 of the lens loop refill.
func deepPrecode(r *rand.Rand) [19]uint8 {
	var p [19]uint8
	p[16], p[17], p[18] = 7, 7, 7
	for i, s := range r.Perm(16) {
		switch i {
		case 0:
			p[s] = 3
		case 1:
			p[s] = 5
		case 2:
			p[s] = 7
		default:
			p[s] = 4
		}
	}
	return p
}

// deepPrecodeStream: 40 dynamic blocks with random codes (random symbol
// subsets with Kraft-complete lengths, or 16-symbol chains with lengths
// 1..15; random HLIT/HDIST), each with a deepPrecode and 20 tokens.
func deepPrecodeStream(seed uint64) (in, want []byte) {
	r := rand.New(rand.NewPCG(seed, 77))
	var w bitWriter
	var toks []tok
	outLen := 0
	for b := 0; b < 40; b++ {
		nlit := 257 + r.IntN(30)
		ndist := 1 + r.IntN(30)
		used := []int{'a', 256}
		if nlit > 257 {
			used = append(used, 257+r.IntN(nlit-257))
		}
		var litLens []uint8
		if r.IntN(3) == 0 {
			for len(used) < 16 {
				s := r.IntN(nlit)
				dup := false
				for _, u := range used {
					dup = dup || u == s
				}
				if !dup {
					used = append(used, s)
				}
			}
			r.Shuffle(len(used), func(i, j int) { used[i], used[j] = used[j], used[i] })
			litLens = chainLens(nlit, used)
		} else {
			p := 1 + r.IntN(9)
			in := map[int]bool{}
			for _, u := range used {
				in[u] = true
			}
			var all []int
			for s := 0; s < nlit; s++ {
				if in[s] || r.IntN(10) < p {
					all = append(all, s)
				}
			}
			litLens = kraftLens(nlit, all)
		}
		var offUsed []int
		for s := 0; s < ndist; s++ {
			if r.IntN(3) != 0 {
				offUsed = append(offUsed, s)
			}
		}
		offLens := make([]uint8, ndist)
		if len(offUsed) > 0 {
			offLens = kraftLens(ndist, offUsed)
		}
		pre := deepPrecode(r)
		all := append(append([]uint8(nil), litLens...), offLens...)
		w.dynamicHeader(b == 39, nlit, ndist, 19, pre, rleLens(all))
		start := len(toks)
		toks, outLen = randToks(r, toks, 20, outLen, litLens, offLens)
		newTcode(litLens, offLens).toks(&w, toks[start:])
	}
	return w.bytes(), tokOutput(nil, toks)
}

// TestDecompressDeepPrecode protects the lens loop refill threshold
// (DEFLATE_MAX_PRE_CODEWORD_LEN + 7 = 14) and the dynamic header parsing.
func TestDecompressDeepPrecode(t *testing.T) {
	d := NewDecompressor()
	for seed := uint64(0); seed < 30; seed++ {
		in, want := deepPrecodeStream(seed)
		dst := make([]byte, len(want)+64)
		nIn, nOut, err := d.DecompressEx(dst, in)
		if err != nil || nIn != len(in) || nOut != len(want) || !bytes.Equal(dst[:nOut], want) {
			t.Fatalf("seed %d: nIn %d/%d nOut %d/%d err %v", seed, nIn, len(in), nOut, len(want), err)
		}
	}
}

func TestDiffDeepPrecode(t *testing.T) {
	creftest.LDBin(t)
	dr := newDiffRunner(t)
	for seed := uint64(0); seed < 30; seed++ {
		in, want := deepPrecodeStream(seed)
		dr.add(ldReq{fmt.Sprintf("deep-precode/%d", seed), in, len(want), true})
		for k := 1; k <= 12; k++ {
			dr.add(ldReq{fmt.Sprintf("deep-precode/%d/cut%d", seed, k), in[:len(in)-k], len(want), false})
		}
	}
	dr.done()
}

// noEOBStream: a dynamic block whose litlen code is 'a' and 'b' with 1-bit
// codewords and no end-of-block, followed by pad zero bits. Decoding goes on
// in the generic loop over implicit zero bytes ('a' each bit) until the
// bytewise REFILL_BITS has overread more than 8 bytes (BAD_DATA); with a
// smaller dst, INSUFFICIENT_SPACE comes first. Where that boundary lies
// depends on the REFILL_BITS refill target (CONSUMABLE_NBITS, the bytewise
// loop's threshold) and on the overread limit (sizeof(bitbuf_t)).
func noEOBStream(pad uint) []byte {
	ll := make([]uint8, 257)
	ll['a'], ll['b'] = 1, 1
	var w bitWriter
	w.dynamicHeader(true, 257, 1, 19, defaultPrecode(), rleLens(append(ll, 0)))
	w.bits(0, pad)
	return w.bytes()
}

// noEOBBoundary[pad] is the smallest dst size for which libdeflate v1.26
// returns BAD_DATA instead of INSUFFICIENT_SPACE on noEOBStream(pad)
// (measured with ldref decompress-ex; TestDiffNoEOBBoundary re-checks it).
var noEOBBoundary = [...]int{
	11, 11, 11, 19, 19, 19, 19, 19, 19, 19, 19, 27, 27, 27, 27, 27,
	27, 27, 27, 35, 35, 35, 35, 35, 35, 35, 35, 43, 43, 43, 43, 43,
}

// TestDecompressNoEOBBoundary protects the bytewise REFILL_BITS threshold
// (CONSUMABLE_NBITS) and the overread limit: both move the boundary.
func TestDecompressNoEOBBoundary(t *testing.T) {
	d := NewDecompressor()
	for pad, b := range noEOBBoundary {
		in := noEOBStream(uint(pad))
		for n := max(b-20, 0); n <= b+20; n++ {
			_, _, err := d.DecompressEx(make([]byte, n), in)
			want := ErrInsufficientSpace
			if n >= b {
				want = ErrBadData
			}
			if err != want {
				t.Fatalf("pad %d dst %d: %v, want %v (boundary %d)", pad, n, err, want, b)
			}
		}
	}
}

func TestDiffNoEOBBoundary(t *testing.T) {
	creftest.LDBin(t)
	dr := newDiffRunner(t)
	for pad, b := range noEOBBoundary {
		in := noEOBStream(uint(pad))
		for n := max(b-20, 0); n <= b+20; n++ {
			dr.add(ldReq{fmt.Sprintf("noeob/pad%d/dst%d", pad, n), in, n, false})
			dr.add(ldReq{fmt.Sprintf("noeob/pad%d/dst%d/exact", pad, n), in, n, true})
		}
	}
	dr.done()
}

// TestFastloopOutputMargin protects FASTLOOP_MAX_BYTES_WRITTEN (299): every
// fast loop iteration of these streams writes the worst case, lit, lit and a
// 257/258-byte match (offset 1: 2 + 288 bytes with the word overrun), and
// the dst sizes sweep the start of an iteration across every position
// relative to out_fastloop_end. 64 trailing input bytes keep the input
// margin out of the way. With the margin 11 or more bytes smaller the copy
// writes past len(dst) (panic).
func TestFastloopOutputMargin(t *testing.T) {
	d := NewDecompressor()
	r := rand.New(rand.NewPCG(11, 12))
	for _, L := range []int{257, 258} {
		for _, o := range []int{1, 2, 8} {
			toks := litsOf(rndBytes(uint64(L*o), 16))
			for k := 0; k < 8; k++ {
				toks = append(toks, lit(byte(r.Uint64())), lit(byte(r.Uint64())), match(L, o))
			}
			var w bitWriter
			w.static(true, toks)
			in := append(w.bytes(), make([]byte, 64)...)
			want := tokOutput(nil, toks)
			dst := make([]byte, len(want))
			if n, err := d.Decompress(dst, in); err != nil || n != len(want) || !bytes.Equal(dst, want) {
				t.Fatalf("L %d o %d: exact: %v", L, o, err)
			}
			for x := 1; x <= 600 && x <= len(want); x++ {
				if _, _, err := d.DecompressEx(dst[:len(want)-x], in); err != ErrInsufficientSpace {
					t.Fatalf("L %d o %d: dst %d bytes short: %v", L, o, x, err)
				}
			}
		}
	}
}

// refillCopies returns the normalised text of every hand-expanded copy of
// macro (REFILL_BITS or REFILL_BITS_IN_FASTLOOP) in decompress_generic.go,
// i.e. the lines between "// C: <macro>()" and "// C: end of <macro>()".
func refillCopies(t *testing.T, src, macro string) []string {
	begin, end := "// C: "+macro+"()", "// C: end of "+macro+"()"
	var copies []string
	var cur []string
	in := false
	for _, line := range strings.Split(src, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case s == begin:
			if in {
				t.Fatalf("%s: nested begin marker", macro)
			}
			in, cur = true, nil
		case s == end:
			if !in {
				t.Fatalf("%s: end marker without begin", macro)
			}
			copies = append(copies, strings.Join(cur, "\n"))
			in = false
		case in:
			cur = append(cur, s)
		}
	}
	if in {
		t.Fatalf("%s: unterminated copy", macro)
	}
	return copies
}

// The C macros are expanded by hand (no calls in the loops); all copies of
// each must stay textually identical (modulo indentation).
//
// The test reads decompress_generic.go from disk, so mutation tooling that
// substitutes the file with `go test -overlay` never reaches it (a mutant of a
// single copy then passes this test vacuously); mutate a real copy of the
// file, or run the test binary in a directory holding the mutant, to check it.
func TestRefillCopiesIdentical(t *testing.T) {
	b, err := os.ReadFile("decompress_generic.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, c := range []struct {
		macro string
		n     int
	}{{"REFILL_BITS", 4}, {"REFILL_BITS_IN_FASTLOOP", 6}} {
		copies := refillCopies(t, src, c.macro)
		if len(copies) != c.n {
			t.Fatalf("%s: %d copies, want %d", c.macro, len(copies), c.n)
		}
		for i, cp := range copies {
			if cp != copies[0] {
				t.Errorf("%s copy %d differs from copy 0:\n%s\n--- vs ---\n%s", c.macro, i, cp, copies[0])
			}
		}
		// Every mention of the macro is a marked copy.
		if n := strings.Count(src, "// C: "+c.macro+"()\n"); n != c.n {
			t.Errorf("%s: %d begin markers, want %d", c.macro, n, c.n)
		}
	}
	// An unmarked hand expansion would escape the check above: every
	// branchless refill word load is inside a marked copy, so the number of
	// them equals the number of copies (4 REFILL_BITS + 6 in the fast loop).
	if n := strings.Count(src, "binary.LittleEndian.Uint64(in[inNext:]) << (bitsleft & 63)"); n != 10 {
		t.Errorf("%d raw refill expressions, want 10 (4 + 6 marked copies)", n)
	}
}

// inputMarginStream: every fast loop iteration reads the most input it can:
// two 11-bit literals, a 15-bit length codeword with 5 extra bits and a
// 15-bit offset codeword with 13 extra bits (70 bits). The litlen code is
// EOB 1 bit, length symbol 257 2 bits, the 256 literals 11 bits, and a chain
// 258..268 (4..14 bits), 283, 284 (15 bits); the offset code is a chain with
// symbol 29 at 15 bits.
func inputMarginStream(seed uint64) (in, want []byte) {
	r := rand.New(rand.NewPCG(seed, 91))
	litLens := make([]uint8, 286)
	for s := 0; s < 256; s++ {
		litLens[s] = 11
	}
	litLens[256], litLens[257] = 1, 2
	for i, s := range []int{258, 259, 260, 261, 262, 263, 264, 265, 266, 267, 268, 283, 284} {
		litLens[s] = uint8(min(4+i, 15))
	}
	offLens := chainLens(30, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 29})
	toks := litsOf(rndBytes(seed, 32768))
	for u := 0; u < 400; u++ {
		ls := 26 + r.IntN(2)
		lo, hi := lenRange(ls)
		dlo, dhi := distRange(29)
		toks = append(toks, lit(byte(r.Uint64())), lit(byte(r.Uint64())), match(lo+r.IntN(hi-lo+1), dlo+r.IntN(dhi-dlo+1)))
	}
	var w bitWriter
	w.dynamic(true, litLens, offLens, toks)
	return w.bytes(), tokOutput(nil, toks)
}

// TestFastloopInputMargin protects FASTLOOP_MAX_BYTES_READ (25): the input
// end is swept across the positions of maximal-input iterations (the
// stream cut by 0..64 bytes, and with 1..16 trailing bytes), so a smaller
// margin makes REFILL_BITS_IN_FASTLOOP read past len(src) (panic). Cut
// streams must fail; whole ones must decode exactly.
func TestFastloopInputMargin(t *testing.T) {
	d := NewDecompressor()
	for seed := uint64(0); seed < 4; seed++ {
		in, want := inputMarginStream(seed)
		dst := make([]byte, len(want))
		for k := 0; k <= 64; k++ {
			n, err := d.Decompress(dst, in[:len(in)-k])
			if k == 0 && (err != nil || n != len(want) || !bytes.Equal(dst, want)) {
				t.Fatalf("seed %d: whole stream: %v", seed, err)
			}
			if k > 0 && err == nil {
				t.Fatalf("seed %d cut %d: decoded", seed, k)
			}
		}
		for k := 1; k <= 16; k++ {
			ext := append(append([]byte(nil), in...), make([]byte, k)...)
			nIn, nOut, err := d.DecompressEx(dst, ext)
			if err != nil || nIn != len(in) || nOut != len(want) || !bytes.Equal(dst, want) {
				t.Fatalf("seed %d +%d trailing: %v", seed, k, err)
			}
		}
	}
}
