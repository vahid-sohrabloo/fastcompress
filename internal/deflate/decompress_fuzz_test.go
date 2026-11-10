package deflate

import (
	"bytes"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// fuzzDstSizes are the dst size policies selected by the fuzz mode byte;
// a negative value means "the input length times -value".
var fuzzDstSizes = [...]int{0, 1, 7, 64, 300, 4096, 1 << 16, -4, -40, 1 << 20}

// fuzzReq turns fuzz bytes into a request: data[0] is the mode byte (bit 0:
// exact, i.e. Decompress semantics; bits 1+: dst size policy), the rest is
// the stream.
func fuzzReq(data []byte) ldReq {
	var mode byte
	if len(data) > 0 {
		mode, data = data[0], data[1:]
	}
	n := fuzzDstSizes[int(mode>>1)%len(fuzzDstSizes)]
	if n < 0 {
		n = -n * len(data)
	}
	return ldReq{"fuzz", data, n, mode&1 != 0}
}

// fuzzSeeds are the FuzzDecompress seeds: the golden streams in 4 modes,
// plus a bit-flip and a truncation mutation of each.
func fuzzSeeds() [][]byte {
	var seeds [][]byte
	for i, g := range goldenCases() {
		if len(g.in) > 1<<16 {
			continue
		}
		for _, mode := range []byte{0, 1, byte(i%8) << 1, 6<<1 | 1} {
			seeds = append(seeds, append([]byte{mode}, g.in...))
		}
		if len(g.in) > 2 {
			m := append([]byte{byte(i) << 1}, g.in...)
			m[1+(i*13)%len(g.in)] ^= 1 << (i % 8)
			seeds = append(seeds, m)
			seeds = append(seeds, append([]byte{byte(i)}, g.in[:len(g.in)*2/3]...))
		}
	}
	return append(seeds, []byte{}, []byte{0})
}

// reusePrimer is decoded by a reused Decompressor before the input under
// test: a static block (static_codes_loaded set, static tables built) and a
// dynamic block with subtables, so stale state would show.
func reusePrimer() []byte {
	var w bitWriter
	w.static(false, lits("primer"))
	w.dynamic(true, chainLens(286, []int{'e', 't', 256, 'a', 257, 'o', 258, 'i', 262, 'n', 'z', 285, 'q', 270, 'x', 'j'}),
		chainLens(30, []int{0, 1, 3, 5, 9, 12, 15, 17, 20, 22, 24, 25, 26, 27, 28, 29}), lits("jxqz"))
	return w.bytes()
}

// checkReuse decodes r with a Decompressor that first decoded the primer and
// then r itself, and compares both results with want (from a fresh
// Decompressor). Deterministic: depends on r only.
func checkReuse(t *testing.T, r ldReq, want ldRes, primer []byte, buf *[]byte) {
	t.Helper()
	d := NewDecompressor()
	if _, _, err := d.DecompressEx(make([]byte, 1<<10), primer); err != nil {
		t.Fatalf("primer: %v", err)
	}
	for pass := 0; pass < 2; pass++ {
		g := goDecompress(t, d, r, buf)
		if g.res != want.res || g.nIn != want.nIn || g.nOut != want.nOut || !bytes.Equal(g.out, want.out) {
			t.Fatalf("reused decompressor (pass %d): result=%d in=%d out=%d, fresh result=%d in=%d out=%d",
				pass, g.res, g.nIn, g.nOut, want.res, want.nIn, want.nOut)
		}
	}
}

// FuzzDecompress: arbitrary bytes, decoded by a fresh Go Decompressor and by
// libdeflate (ldref; C also gets a fresh decompressor per input); result
// code, actual in/out and output must be identical, and Go must not panic.
// Then the input is decoded again by a reused Decompressor (primed with a
// static and a dynamic block, then the input twice) and must give the same
// result. Everything depends on the input alone, so a failure reproduces
// from its corpus entry. Skips without ldref, fails in CI (creftest.LDBin).
func FuzzDecompress(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	primer := reusePrimer()
	f.Fuzz(func(t *testing.T, data []byte) {
		creftest.LDBin(t)
		var buf []byte
		r := fuzzReq(data)
		g := goDecompress(t, NewDecompressor(), r, &buf)
		c := ldBatch(t, []ldReq{r})[0]
		if g.res != c.res || g.nIn != c.nIn || g.nOut != c.nOut || !bytes.Equal(g.out, c.out) {
			t.Fatalf("in %d bytes, dst %d, exact %v: go result=%d in=%d out=%d, libdeflate result=%d in=%d out=%d",
				len(r.in), r.outSize, r.exact, g.res, g.nIn, g.nOut, c.res, c.nIn, c.nOut)
		}
		checkReuse(t, r, g, primer, &buf)
	})
}

// The second pass over the seed corpus: one Decompressor reused across all
// seeds, in order, gives the same results as a fresh one per seed (no ldref
// needed).
func TestFuzzSeedsReuse(t *testing.T) {
	shared := NewDecompressor()
	var buf1, buf2 []byte
	for i, s := range fuzzSeeds() {
		r := fuzzReq(s)
		want := goDecompress(t, NewDecompressor(), r, &buf1)
		got := goDecompress(t, shared, r, &buf2)
		if got.res != want.res || got.nIn != want.nIn || got.nOut != want.nOut || !bytes.Equal(got.out, want.out) {
			t.Fatalf("seed %d: shared result=%d in=%d out=%d, fresh result=%d in=%d out=%d",
				i, got.res, got.nIn, got.nOut, want.res, want.nIn, want.nOut)
		}
	}
}
