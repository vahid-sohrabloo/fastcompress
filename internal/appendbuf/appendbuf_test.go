package appendbuf

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflate"
)

var errLimit = errors.New("test: output limit")

var formats = []Format{Raw, Gzip, Zlib}

// bound returns the CompressBound of f for n bytes.
func bound(f Format, n int) int {
	c := (*deflate.Compressor)(nil)
	switch f {
	case Gzip:
		return c.GzipCompressBound(n)
	case Zlib:
		return c.ZlibCompressBound(n)
	}
	return c.CompressBound(n)
}

// direct compresses src with the deflate method of f, not through Compress.
func direct(t *testing.T, c *deflate.Compressor, f Format, src []byte) []byte {
	t.Helper()
	dst := make([]byte, bound(f, len(src)))
	var n int
	switch f {
	case Raw:
		n = c.Compress(dst, src)
	case Gzip:
		n = c.CompressGzip(dst, src)
	case Zlib:
		n = c.CompressZlib(dst, src)
	}
	if n == 0 {
		t.Fatalf("format %d: compress failed", f)
	}
	return dst[:n]
}

func newCompressor(t *testing.T) *deflate.Compressor {
	t.Helper()
	c, err := deflate.NewCompressor(6)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// text returns n bytes of compressible pseudo-random text.
func text(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	words := []string{"alpha ", "beta ", "gamma ", "delta ", "epsilon ", "zeta\n"}
	var b []byte
	for len(b) < n {
		b = append(b, words[r.IntN(len(words))]...)
	}
	return b[:n]
}

var prefix = []byte("prefix")

func withPrefix() []byte { return append([]byte(nil), prefix...) }

func TestLimit(t *testing.T) {
	var l Limit
	if l.Get() != DefaultMaxOutput || DefaultMaxOutput != 1<<30 {
		t.Fatalf("zero Limit: %d", l.Get())
	}
	for _, c := range []struct{ set, want int }{
		{1, 1}, {12345, 12345}, {math.MaxInt, math.MaxInt},
		{0, DefaultMaxOutput}, {-1, DefaultMaxOutput}, {math.MinInt, DefaultMaxOutput},
	} {
		l.Set(77)
		l.Set(c.set)
		if l.Get() != c.want {
			t.Errorf("Set(%d): Get() = %d, want %d", c.set, l.Get(), c.want)
		}
	}
	if MaxExpansion != 1032 {
		t.Fatalf("MaxExpansion = %d", MaxExpansion)
	}
}

func TestCompress(t *testing.T) {
	c := newCompressor(t)
	data := text(5000)
	for _, f := range formats {
		want := direct(t, c, f, data)
		b := bound(f, len(data))
		check := func(name string, got []byte) {
			t.Helper()
			if !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], want) {
				t.Fatalf("format %d, %s: wrong output", f, name)
			}
		}
		if got := Compress(c, f, nil, data, b); !bytes.Equal(got, want) {
			t.Fatalf("format %d, nil dst: wrong output", f)
		}
		check("prefix", Compress(c, f, withPrefix(), data, b))
		// Exactly bound bytes of spare capacity: no reallocation.
		buf := make([]byte, len(prefix), len(prefix)+b)
		copy(buf, prefix)
		got := Compress(c, f, buf, data, b)
		check("capacity", got)
		if &got[0] != &buf[0] {
			t.Fatalf("format %d: dst with capacity was reallocated", f)
		}
		// One byte short: grows, the prefix is copied.
		buf = make([]byte, len(prefix), len(prefix)+b-1)
		copy(buf, prefix)
		got = Compress(c, f, buf, data, b)
		check("one short", got)
		if &got[0] == &buf[0] || cap(got)-len(prefix) < b {
			t.Fatalf("format %d: dst one short: not grown to the bound", f)
		}
		// Empty input; the prefix array is not written past its length.
		empty := direct(t, c, f, nil)
		p := withPrefix()
		p = p[:len(p):len(p)]
		got = Compress(c, f, p, nil, bound(f, 0))
		if !bytes.Equal(got[len(prefix):], empty) || !bytes.Equal(p, prefix) {
			t.Fatalf("format %d, empty input: %x, want %x", f, got[len(prefix):], empty)
		}
	}
}

func TestDecompress(t *testing.T) {
	c := newCompressor(t)
	data := text(5000)
	n := len(data)
	for _, f := range formats {
		s := direct(t, c, f, data)
		var d deflate.Decompressor
		check := func(name string, out []byte, err error) {
			t.Helper()
			if err != nil || len(out) != len(prefix)+n || !bytes.Equal(out[:len(prefix)], prefix) || !bytes.Equal(out[len(prefix):], data) {
				t.Fatalf("format %d, %s: err=%v len=%d", f, name, err, len(out))
			}
		}
		for _, hint := range []int{math.MinInt, -5, 0, 1, n - 1, n, n + 1, 10 * n, math.MaxInt / 1024, math.MaxInt / 2, math.MaxInt} {
			out, err := Decompress(&d, f, withPrefix(), s, hint, DefaultMaxOutput, errLimit)
			check(fmt.Sprintf("hint %d", hint), out, err)
		}
		// nil dst.
		out, err := Decompress(&d, f, nil, s, 0, DefaultMaxOutput, errLimit)
		if err != nil || !bytes.Equal(out, data) {
			t.Fatalf("format %d, nil dst: err=%v", f, err)
		}
		// Spare capacity is used as is, even past a smaller hint.
		buf := make([]byte, len(prefix), len(prefix)+n)
		copy(buf, prefix)
		out, err = Decompress(&d, f, buf, s, 1, DefaultMaxOutput, errLimit)
		check("spare capacity", out, err)
		if &out[0] != &buf[0] {
			t.Fatalf("format %d: spare capacity: dst was reallocated", f)
		}
		// src stored in dst[:len(dst)] (before the spare capacity): allowed.
		buf = make([]byte, 0, len(prefix)+len(s)+n)
		buf = append(append(buf, prefix...), s...)
		out, err = Decompress(&d, f, buf, buf[len(prefix):], 0, DefaultMaxOutput, errLimit)
		if err != nil || &out[0] != &buf[0] || !bytes.Equal(out[:len(prefix)], prefix) ||
			!bytes.Equal(out[len(prefix):len(prefix)+len(s)], s) || !bytes.Equal(out[len(prefix)+len(s):], data) {
			t.Fatalf("format %d: src in dst[:len(dst)]: err=%v", f, err)
		}

		// Output limit: exactly n bytes fit, n-1 do not; the limit error is
		// the one passed in; capacity never passes the limit.
		for _, hint := range []int{0, 1, n, 10 * n, math.MaxInt} {
			out, err = Decompress(&d, f, withPrefix(), s, hint, n, errLimit)
			check(fmt.Sprintf("limit n, hint %d", hint), out, err)
			out, err = Decompress(&d, f, withPrefix(), s, hint, n-1, errLimit)
			if err != errLimit || !bytes.Equal(out, prefix) || cap(out) > len(prefix)+n-1 {
				t.Fatalf("format %d, limit n-1, hint %d: err=%v out=%q cap=%d", f, hint, err, out, cap(out))
			}
		}
		// A spare capacity above the limit is not used beyond the limit.
		big := make([]byte, len(prefix), len(prefix)+2*n)
		copy(big, prefix)
		out, err = Decompress(&d, f, big, s, 0, n-1, errLimit)
		if err != errLimit || &out[:1][0] != &big[0] || len(out) != len(prefix) {
			t.Fatalf("format %d, big spare over limit: err=%v", f, err)
		}

		// Bad and empty input: the decoder's error, dst length unchanged.
		for _, bad := range [][]byte{nil, {}, {0xff}, s[:len(s)/2]} {
			for _, hint := range []int{0, 1, math.MaxInt} {
				out, err = Decompress(&d, f, withPrefix(), bad, hint, math.MaxInt, errLimit)
				if err != deflate.ErrBadData || !bytes.Equal(out, prefix) || cap(out) > len(prefix)+MaxExpansion*len(bad)+1024+(MaxExpansion*len(bad)+1024)/8 {
					t.Fatalf("format %d, %d bad bytes, hint %d: err=%v out=%q cap=%d", f, len(bad), hint, err, out, cap(out))
				}
			}
			out, err = Decompress(&d, f, nil, bad, 0, DefaultMaxOutput, errLimit)
			if err != deflate.ErrBadData || len(out) != 0 {
				t.Fatalf("format %d, nil dst, %d bad bytes: err=%v len=%d", f, len(bad), err, len(out))
			}
		}
		// Empty output.
		es := direct(t, c, f, nil)
		out, err = Decompress(&d, f, nil, es, 0, DefaultMaxOutput, errLimit)
		if err != nil || len(out) != 0 {
			t.Fatalf("format %d, empty stream: err=%v len=%d", f, err, len(out))
		}
		out, err = Decompress(&d, f, withPrefix(), es, 0, 1, errLimit)
		if err != nil || !bytes.Equal(out, prefix) {
			t.Fatalf("format %d, empty stream, limit 1: err=%v out=%q", f, err, out)
		}
	}
	// A format mismatch is bad data: the dispatch picks the right decoder.
	raw := direct(t, c, Raw, data)
	for _, f := range []Format{Gzip, Zlib} {
		var d deflate.Decompressor
		if _, err := Decompress(&d, f, nil, raw, 0, DefaultMaxOutput, errLimit); err != deflate.ErrBadData {
			t.Fatalf("raw data as format %d: err=%v", f, err)
		}
		if _, err := Decompress(&d, Raw, nil, direct(t, c, f, data), 0, DefaultMaxOutput, errLimit); err == nil {
			t.Fatalf("format %d data as raw: decoded", f)
		}
	}
}

// TestDecompressSizes checks the first size tried and the doubling sequence
// through the number of allocations and the final capacity (the decoder
// itself allocates nothing).
func TestDecompressSizes(t *testing.T) {
	c := newCompressor(t)
	for _, f := range formats {
		var d deflate.Decompressor
		run := func(name string, data []byte, hint, limit, wantAllocs, wantCap int, wantErr error) {
			t.Helper()
			s := direct(t, c, f, data)
			var out []byte
			var err error
			allocs := testing.AllocsPerRun(3, func() {
				out, err = Decompress(&d, f, nil, s, hint, limit, errLimit)
			})
			if err != wantErr || int(allocs) != wantAllocs || cap(out) != wantCap {
				t.Fatalf("format %d, %s: err=%v allocs=%v cap=%d, want %v %d %d",
					f, name, err, allocs, cap(out), wantErr, wantAllocs, wantCap)
			}
			if err == nil && !bytes.Equal(out, data) {
				t.Fatalf("format %d, %s: wrong output", f, name)
			}
		}
		data := text(1000)
		// hint 1: 1, 2, 4, ..., 1024.
		run("double", data, 1, DefaultMaxOutput, 11, 1024, nil)
		// hint 3: 3, 6, ..., 768, 1536.
		run("double from 3", data, 3, DefaultMaxOutput, 10, 1536, nil)
		// The exact hint: one allocation of exactly that size.
		run("exact", data, 1000, DefaultMaxOutput, 1, 1000, nil)
		// hint n-1: n-1 then 2(n-1).
		run("n-1", data, 999, DefaultMaxOutput, 2, 1998, nil)
		// Doubling stops at the limit: ..., 512, then the limit 1000.
		run("to limit", data, 1, 1000, 11, 1000, nil)
		// ... 512, 1023 (512 > 1023/2 = 511).
		run("to odd limit", data, 1, 1023, 11, 1023, nil)
		// ... 512, 1024: 512 is not > 1024/2, so it doubles to exactly the limit.
		run("to even limit", data, 1, 1024, 11, 1024, nil)
		// ... 256, 512, 1025: 512 is not > 1025/2 = 512 -> 1024, then 1025.
		run("past even half", data, 1, 1025, 11, 1024, nil)
		// One byte too few: ..., 512, 999, then the limit error (the slice
		// keeps the last attempt's capacity).
		run("limit n-1", data, 1, 999, 11, 999, errLimit)
		// The limit is the first size: one attempt.
		run("limit is first", data, 5000, 999, 1, 999, errLimit)
		// Unknown size: max(4*len(src), 1024) is tried first.
		s := direct(t, c, f, data)
		first := max(4*len(s), 1024)
		wantCap, allocs := first, 1
		for wantCap < len(data) {
			wantCap *= 2
			allocs++
		}
		run("unknown", data, 0, DefaultMaxOutput, allocs, wantCap, nil)
		// Unknown size with a small limit: the limit/4 term keeps 4*len(src)
		// from overflowing and the first try is the limit itself.
		run("unknown small limit", data, 0, 100, 1, 100, errLimit)
		run("unknown limit 4", data, -1, 4, 1, 4, errLimit)
		// Unknown size, larger output: 4*len(src) then doubling.
		big := text(200000)
		bs := direct(t, c, f, big)
		wantCap, allocs = max(4*len(bs), 1024), 1
		for wantCap < len(big) {
			wantCap *= 2
			allocs++
		}
		run("unknown big", big, 0, DefaultMaxOutput, allocs, wantCap, nil)
		// A hint past 1032*len(src)+1024 is clamped to that (no valid
		// src decompresses to more, so one attempt).
		clamp := MaxExpansion*len(s) + 1024
		run("clamped hint", data, math.MaxInt, math.MaxInt, 1, clamp, nil)
		run("clamped hint, default limit", data, math.MaxInt, DefaultMaxOutput, 1, clamp, nil)
		run("hint at the clamp", data, clamp, math.MaxInt, 1, clamp, nil)
		run("hint just above the clamp", data, clamp+1, math.MaxInt, 1, clamp, nil)
		run("hint just below the clamp", data, clamp-1, math.MaxInt, 1, clamp-1, nil)
		// The limit below the clamp wins.
		run("clamped hint, lower limit", data, math.MaxInt, clamp-1, 1, clamp-1, nil)
	}
}

// TestDecompressLenDst: the limit is lowered so that len(dst)+limit cannot
// overflow, and spare capacity counts from len(dst).
func TestDecompressLenDst(t *testing.T) {
	c := newCompressor(t)
	data := text(3000)
	for _, f := range formats {
		s := direct(t, c, f, data)
		var d deflate.Decompressor
		// The limit counts output bytes, not len(dst): a long dst does not
		// eat into it.
		long := make([]byte, 100000, 100000+len(data))
		out, err := Decompress(&d, f, long, s, 0, len(data), errLimit)
		if err != nil || len(out) != len(long)+len(data) || &out[0] != &long[0] || !bytes.Equal(out[len(long):], data) {
			t.Fatalf("format %d, long dst: err=%v len=%d", f, err, len(out))
		}
		// Spare capacity smaller than the hint: grows to len(dst)+hint
		// exactly (not cap(dst)+hint).
		short := make([]byte, 100, 200)
		out, err = Decompress(&d, f, short, s, len(data), DefaultMaxOutput, errLimit)
		if err != nil || cap(out) != 100+len(data) || !bytes.Equal(out[100:], data) {
			t.Fatalf("format %d, short spare: err=%v cap=%d", f, err, cap(out))
		}
		// The prefix is copied on growth, and the original dst is not written.
		p := withPrefix()
		p = p[:len(p):len(p)]
		out, err = Decompress(&d, f, p, s, 1, DefaultMaxOutput, errLimit)
		if err != nil || !bytes.Equal(out[:len(prefix)], prefix) || !bytes.Equal(p, prefix) {
			t.Fatalf("format %d, grown prefix: err=%v", f, err)
		}
	}
}

// TestDecompressHugeSrc: with a src longer than (MaxInt-1024)/MaxExpansion
// bytes the clamp would overflow and is skipped. That length can only be
// allocated where int is 32 bits.
func TestDecompressHugeSrc(t *testing.T) {
	n := (math.MaxInt-1024)/MaxExpansion + 1
	if n > 8<<20 {
		t.Skipf("needs a %d-byte src", n)
	}
	c := newCompressor(t)
	data := text(1000)
	for _, f := range formats {
		s := direct(t, c, f, data)
		src := make([]byte, n) // the stream, then ignored trailing bytes
		copy(src, s)
		var d deflate.Decompressor
		for _, hint := range []int{0, 1, len(data), math.MaxInt} {
			out, err := Decompress(&d, f, withPrefix(), src, hint, 1<<20, errLimit)
			if err != nil || !bytes.Equal(out[len(prefix):], data) {
				t.Fatalf("format %d, hint %d: err=%v", f, hint, err)
			}
			if hint == math.MaxInt && cap(out) != len(prefix)+1<<20 {
				t.Fatalf("format %d, hint %d: cap %d, want the limit", f, hint, cap(out))
			}
		}
	}
}

func TestAllocs(t *testing.T) {
	c := newCompressor(t)
	data := text(20000)
	for _, f := range formats {
		s := direct(t, c, f, data)
		var d deflate.Decompressor
		out := make([]byte, 0, len(data))
		if a := testing.AllocsPerRun(10, func() { _, _ = Decompress(&d, f, out, s, len(data), DefaultMaxOutput, errLimit) }); a != 0 {
			t.Errorf("format %d: Decompress with capacity allocates %v", f, a)
		}
		app := make([]byte, 0, bound(f, len(data)))
		if a := testing.AllocsPerRun(5, func() { _ = Compress(c, f, app, data, bound(f, len(data))) }); a != 0 {
			t.Errorf("format %d: Compress with capacity allocates %v", f, a)
		}
	}
}

// TestDecompressScratch: the output and the errors are Decompress's, the
// result is a new array of exactly dst and the output (dst itself for no
// output), the scratch grows to the size that worked but not past
// ScratchMax, and a larger output continues as Decompress.
func TestDecompressScratch(t *testing.T) {
	c := newCompressor(t)
	for _, f := range formats {
		var d deflate.Decompressor
		var scratch []byte
		for _, n := range []int{0, 1, 1000, 100000, ScratchMax, 3 * ScratchMax} {
			data := text(n)
			s := direct(t, c, f, data)
			for _, dst := range [][]byte{nil, prefix[:len(prefix):len(prefix)]} {
				out, err := DecompressScratch(&d, f, &scratch, dst, s, DefaultMaxOutput, errLimit)
				want, werr := Decompress(&d, f, append([]byte(nil), dst...), s, 0, DefaultMaxOutput, errLimit)
				if err != werr || !bytes.Equal(out, want) || !bytes.Equal(out[:len(dst)], dst) {
					t.Fatalf("format %d, %d bytes, dst %q: err %v, want %v", f, n, dst, err, werr)
				}
				if n == 0 && len(out) > 0 && &out[0] != &dst[0] {
					t.Fatalf("format %d, empty output: dst not returned", f)
				}
				if n > 0 && n <= ScratchMax/2 && cap(out) != len(out) {
					t.Fatalf("format %d, %d bytes: cap %d, len %d", f, n, cap(out), len(out))
				}
				if cap(scratch) > ScratchMax {
					t.Fatalf("format %d: scratch of %d bytes", f, cap(scratch))
				}
			}
		}
		// Incompressible input: a stored stream of 4*len(src) > ScratchMax
		// estimated bytes still decodes in the scratch (attempts are cut to
		// ScratchMax), and its result is exact.
		rnd := rand.New(rand.NewPCG(7, 8))
		for _, n := range []int{300 << 10, 3 * ScratchMax / 4, ScratchMax} {
			raw := make([]byte, n)
			for i := range raw {
				raw[i] = byte(rnd.Uint32())
			}
			st := direct(t, c, f, raw)
			var sc []byte
			out, err := DecompressScratch(&d, f, &sc, nil, st, DefaultMaxOutput, errLimit)
			if err != nil || !bytes.Equal(out, raw) || cap(out) != len(out) || cap(sc) > ScratchMax {
				t.Fatalf("format %d, %d random bytes: err %v, len %d, cap %d, scratch %d", f, n, err, len(out), cap(out), cap(sc))
			}
		}
		// The limit and bad data, as Decompress (dst returned).
		data := text(5000)
		s := direct(t, c, f, data)
		for _, limit := range []int{1, 1024, 4999, 5000} {
			var sc []byte
			out, err := DecompressScratch(&d, f, &sc, withPrefix()[:len(prefix):len(prefix)], s, limit, errLimit)
			_, werr := Decompress(&d, f, withPrefix(), s, 0, limit, errLimit)
			if err != werr || (err != nil && !bytes.Equal(out, prefix)) || cap(sc) > limit {
				t.Fatalf("format %d, limit %d: err %v, want %v, scratch %d", f, limit, err, werr, cap(sc))
			}
		}
		bad := append(append([]byte(nil), s[:len(s)/2]...), 0xff, 0xff, 0xff)
		if _, err := DecompressScratch(&d, f, &scratch, nil, bad, DefaultMaxOutput, errLimit); err == nil {
			t.Fatalf("format %d: bad data decoded", f)
		}
	}
}
