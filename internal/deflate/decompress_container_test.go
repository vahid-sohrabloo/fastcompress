package deflate

import (
	"bytes"
	stdgzip "compress/gzip"
	stdzlib "compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/internal/adler32"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Differential tests of DecompressGzip{,Ex} and DecompressZlib{,Ex} against
// libdeflate_{gzip,zlib}_decompress_ex (ldref decompress-batch -format):
// result code, actual in/out and output bytes must be identical.

// goContainer runs r through the Go container decompressor the way ldref
// runs it (actual_in always requested; actual_out unless exact), then through
// the public API (DecompressGzip/Zlib if exact, else the Ex variant), which
// must agree in result, counts and output (dst refilled in between).
func goContainer(t testing.TB, d *Decompressor, format string, r ldReq, buf *[]byte) ldRes {
	t.Helper()
	if cap(*buf) < r.outSize {
		*buf = make([]byte, r.outSize)
	}
	dst := (*buf)[:r.outSize]
	fillDst(dst)
	var res libdeflateResult
	var nIn, nOut int
	gz := format == creftest.LDGzip
	if gz {
		res, nIn, nOut = d.gzipDecompress(r.in, dst, !r.exact)
	} else {
		res, nIn, nOut = d.zlibDecompress(r.in, dst, !r.exact)
	}
	g := ldRes{res, nIn, nOut, append([]byte(nil), dst[:nOut]...)}
	fillDst(dst)
	var err error
	pubOut := 0
	if r.exact {
		var n int
		if gz {
			n, err = d.DecompressGzip(dst, r.in)
		} else {
			n, err = d.DecompressZlib(dst, r.in)
		}
		if (err == nil && n != r.outSize) || (err != nil && n != 0) {
			t.Fatalf("%s: Decompress%s returned %d, %v (dst %d)", r.name, format, n, err, r.outSize)
		}
		pubOut = n
	} else {
		var a, b int
		if gz {
			a, b, err = d.DecompressGzipEx(dst, r.in)
		} else {
			a, b, err = d.DecompressZlibEx(dst, r.in)
		}
		if (err == nil && (a != nIn || b != nOut)) || (err != nil && (a != 0 || b != 0)) {
			t.Fatalf("%s: Ex (%d, %d, %v), internal (%d, %d)", r.name, a, b, err, nIn, nOut)
		}
		pubOut = b
	}
	if err != res.err() {
		t.Fatalf("%s: public API error %v, internal result %d", r.name, err, res)
	}
	if err == nil && !bytes.Equal(dst[:pubOut], g.out) {
		t.Fatalf("%s: public API output differs from the internal call's", r.name)
	}
	return g
}

// containerDiff compares Go with libdeflate on reqs (skips without ldref,
// fails in CI). One Decompressor is reused for all of them, as ldref does.
func containerDiff(t *testing.T, format string, reqs []ldReq) {
	t.Helper()
	creftest.LDBin(t)
	want := ldBatchFormat(t, format, reqs)
	d := NewDecompressor()
	var buf []byte
	for i, r := range reqs {
		g := goContainer(t, d, format, r, &buf)
		c := want[i]
		if g.res != c.res || g.nIn != c.nIn || g.nOut != c.nOut || !bytes.Equal(g.out, c.out) {
			t.Fatalf("%s %s (in %d bytes, dst %d, exact %v): go result=%d in=%d out=%d, libdeflate result=%d in=%d out=%d",
				format, r.name, len(r.in), r.outSize, r.exact, g.res, g.nIn, g.nOut, c.res, c.nIn, c.nOut)
		}
	}
}

// cstream is a container stream and the size of the data it holds.
type cstream struct {
	name string
	in   []byte
	n    int
}

// containerTestData returns the plain inputs the valid streams are made of.
func containerTestData() [][]byte {
	r := rand.New(rand.NewPCG(21, 22))
	text := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = "the quick brown fox jumps over the lazy dog "[(i*7+i/13)%44]
		}
		return b
	}
	return [][]byte{{}, {'x'}, text(100), text(3000), randBytes(r, 500), text(70000)}
}

// gzipWith builds a gzip member from the header bytes hdr, the raw DEFLATE
// stream raw and the CRC-32 / ISIZE trailer of data.
func gzipWith(hdr, raw, data []byte) []byte {
	out := append([]byte(nil), hdr...)
	out = append(out, raw...)
	out = binary.LittleEndian.AppendUint32(out, crc32.ChecksumIEEE(data))
	return binary.LittleEndian.AppendUint32(out, uint32(len(data)))
}

// zlibWith builds a zlib stream from the 2-byte header hdr, the raw DEFLATE
// stream raw and the Adler-32 of data.
func zlibWith(hdr uint16, raw, data []byte) []byte {
	out := []byte{byte(hdr >> 8), byte(hdr)}
	out = append(out, raw...)
	return binary.BigEndian.AppendUint32(out, adler32.Checksum(data))
}

// goRaw compresses data at level with Go's (libdeflate-identical) Compress.
func goRaw(t testing.TB, data []byte, level int) []byte {
	t.Helper()
	c, err := NewCompressor(level)
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, c.CompressBound(len(data)))
	return dst[:c.Compress(dst, data)]
}

// validStreams: libdeflate's output at levels 0..12 (ldref), the standard
// library writers at every level (gzip with and without FNAME, FCOMMENT,
// FEXTRA and a modification time; zlib also with a preset dictionary, which
// libdeflate rejects), and gzip members with an FHCRC field (no stdlib
// writer emits one).
func validStreams(t *testing.T, format string) []cstream {
	var ss []cstream
	data := containerTestData()
	for i, d := range data {
		for level := 0; level <= 12; level++ {
			if i == len(data)-1 && level%3 != 0 {
				continue
			}
			ss = append(ss, cstream{fmt.Sprintf("ldref/l%d/%d", level, len(d)), creftest.LDCompress(t, d, level, format), len(d)})
		}
		for level := -2; level <= 9; level++ {
			var b bytes.Buffer
			if format == creftest.LDGzip {
				hdrs := []stdgzip.Header{{}, {Name: "file.txt"}, {Comment: "a comment"}, {Extra: []byte{'A', 'B', 2, 0, 1, 2}},
					{Name: "n", Comment: "c", Extra: []byte{1, 2, 3, 4}, ModTime: time.Unix(1700000000, 0), OS: 3}}
				for k, h := range hdrs {
					b.Reset()
					w, err := stdgzip.NewWriterLevel(&b, level)
					if err != nil {
						t.Fatal(err)
					}
					w.Header = h
					w.Write(d)
					w.Close()
					ss = append(ss, cstream{fmt.Sprintf("stdgzip/l%d/h%d/%d", level, k, len(d)), append([]byte(nil), b.Bytes()...), len(d)})
				}
			} else {
				for k, dict := range [][]byte{nil, []byte("quick brown")} {
					b.Reset()
					var w *stdzlib.Writer
					var err error
					if dict == nil {
						w, err = stdzlib.NewWriterLevel(&b, level)
					} else {
						w, err = stdzlib.NewWriterLevelDict(&b, level, dict)
					}
					if err != nil {
						t.Fatal(err)
					}
					w.Write(d)
					w.Close()
					ss = append(ss, cstream{fmt.Sprintf("stdzlib/l%d/dict%d/%d", level, k, len(d)), append([]byte(nil), b.Bytes()...), len(d)})
				}
			}
		}
		if format == creftest.LDGzip && len(d) <= 3000 {
			raw := goRaw(t, d, 6)
			hdr := []byte{0x1F, 0x8B, 8, gzipFHCRC | gzipFNAME, 0, 0, 0, 0, 0, 255, 'n', 0}
			good := crc32.ChecksumIEEE(hdr)
			for _, h16 := range []uint32{good, good ^ 1} {
				h := binary.LittleEndian.AppendUint16(append([]byte(nil), hdr...), uint16(h16))
				ss = append(ss, cstream{fmt.Sprintf("fhcrc/%x/%d", h16&0xFFFF, len(d)), gzipWith(h, raw, d), len(d)})
			}
		}
	}
	return ss
}

// dstReqs: the output-size variants of one stream of n data bytes: exact
// (exact semantics) with n, n-1, n+1 and 0, and actual-out semantics with n,
// n-1, n+1, 0 and 2n+100.
func dstReqs(name string, in []byte, n int) []ldReq {
	var reqs []ldReq
	for _, sz := range []int{n, n - 1, n + 1, 0} {
		if sz >= 0 {
			reqs = append(reqs, ldReq{fmt.Sprintf("%s/exact%d", name, sz), in, sz, true})
		}
	}
	for _, sz := range []int{n, n - 1, n + 1, 0, 2*n + 100} {
		if sz >= 0 {
			reqs = append(reqs, ldReq{fmt.Sprintf("%s/dst%d", name, sz), in, sz, false})
		}
	}
	return reqs
}

func TestContainerDecompressValid(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	for _, format := range containerFormats {
		t.Run(format, func(t *testing.T) {
			creftest.LDBin(t)
			var reqs []ldReq
			r := rand.New(rand.NewPCG(23, 24))
			for _, s := range validStreams(t, format) {
				reqs = append(reqs, dstReqs(s.name, s.in, s.n)...)
				// Trailing garbage 0..32 bytes: zeros and random.
				for k := 0; k <= 32; k++ {
					z := append(append([]byte(nil), s.in...), make([]byte, k)...)
					g := append(append([]byte(nil), s.in...), randBytes(r, k)...)
					reqs = append(reqs, ldReq{fmt.Sprintf("%s/zeros%d", s.name, k), z, s.n, true},
						ldReq{fmt.Sprintf("%s/garbage%d", s.name, k), g, s.n + 7, false})
				}
			}
			containerDiff(t, format, reqs)
			t.Logf("%d requests", len(reqs))
		})
	}
}

// TestContainerDecompressMutations: every truncation of the small valid
// streams; every bit flip of streams of at most 120 bytes; 2000 random bit
// flips of a large stream; every trailer byte changed (CRC-32 / ISIZE /
// Adler-32) and the sizes off by one; empty and short inputs.
func TestContainerDecompressMutations(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	for _, format := range containerFormats {
		t.Run(format, func(t *testing.T) {
			creftest.LDBin(t)
			r := rand.New(rand.NewPCG(25, 26))
			var reqs []ldReq
			var big cstream
			nflip, ncut := 0, 0
			for _, s := range validStreams(t, format) {
				if len(s.in) > len(big.in) && s.n >= 3000 {
					big = s
				}
				if len(s.in) <= 400 && ncut < 60 {
					ncut++
					for cut := 0; cut < len(s.in); cut++ {
						reqs = append(reqs, ldReq{fmt.Sprintf("%s/cut%d", s.name, cut), s.in[:cut], s.n, true},
							ldReq{fmt.Sprintf("%s/cut%d/ex", s.name, cut), s.in[:cut], s.n + 8, false})
					}
				}
				if len(s.in) <= 120 && nflip < 40 {
					nflip++
					for bit := 0; bit < 8*len(s.in); bit++ {
						m := append([]byte(nil), s.in...)
						m[bit/8] ^= 1 << (bit % 8)
						reqs = append(reqs, ldReq{fmt.Sprintf("%s/flip%d", s.name, bit), m, s.n, bit%2 == 0},
							ldReq{fmt.Sprintf("%s/flip%d/ex", s.name, bit), m, s.n + 8, false})
					}
				}
				// Trailer bytes.
				tl := 8
				if format == creftest.LDZlib {
					tl = 4
				}
				for k := 1; k <= tl && k <= len(s.in); k++ {
					for _, x := range []byte{1, 0x80, 0xFF} {
						m := append([]byte(nil), s.in...)
						m[len(m)-k] ^= x
						reqs = append(reqs, ldReq{fmt.Sprintf("%s/trailer%d^%x", s.name, k, x), m, s.n, false},
							ldReq{fmt.Sprintf("%s/trailer%d^%x/exact", s.name, k, x), m, s.n, true})
					}
				}
			}
			if big.in == nil {
				t.Fatal("no large stream")
			}
			for i := 0; i < 2000; i++ {
				m := append([]byte(nil), big.in...)
				bit := r.IntN(8 * len(m))
				m[bit/8] ^= 1 << (bit % 8)
				reqs = append(reqs, ldReq{fmt.Sprintf("%s/rflip%d", big.name, bit), m, big.n + 64, i%3 == 0})
			}
			// Empty and short inputs.
			for n := 0; n <= 24; n++ {
				for _, b := range []byte{0, 0xFF, 0x1F} {
					in := bytes.Repeat([]byte{b}, n)
					reqs = append(reqs, ldReq{fmt.Sprintf("short%d/%x", n, b), in, 16, false},
						ldReq{fmt.Sprintf("short%d/%x/exact", n, b), in, 0, true})
				}
			}
			containerDiff(t, format, reqs)
			t.Logf("%d requests", len(reqs))
		})
	}
}

// TestGzipHeaderFlags: every FLG value (a) with the fields its bits announce
// present (FEXTRA with XLEN and data, FNAME, FCOMMENT, FHCRC), and (b) with
// no fields (the bytes after the header are the DEFLATE stream); every value
// of ID1, ID2 and CM; and the field-length edge cases: XLEN reaching exactly
// to the footer and one byte past, unterminated FNAME / FCOMMENT, strings
// ending right before the footer, FHCRC as the last header bytes.
func TestGzipHeaderFlags(t *testing.T) {
	creftest.LDBin(t)
	data := []byte("gzip header flag test data, gzip header flag test data")
	raw := goRaw(t, data, 6)
	base := []byte{0x1F, 0x8B, 8, 0, 0, 0, 0, 0, 0, 255}
	var reqs []ldReq
	add := func(name string, in []byte) {
		reqs = append(reqs, dstReqs(name, in, len(data))...)
	}
	for v := 0; v < 256; v++ {
		hdr := append([]byte(nil), base...)
		hdr[3] = byte(v)
		add(fmt.Sprintf("flg%02x/nofields", v), gzipWith(hdr, raw, data))
		if v&gzipFEXTRA != 0 {
			hdr = append(hdr, 5, 0, 'a', 'b', 'c', 'd', 'e')
		}
		if v&gzipFNAME != 0 {
			hdr = append(hdr, 'n', 'a', 'm', 'e', 0)
		}
		if v&gzipFCOMMENT != 0 {
			hdr = append(hdr, 'c', 0)
		}
		if v&gzipFHCRC != 0 {
			hdr = binary.LittleEndian.AppendUint16(hdr, uint16(crc32.ChecksumIEEE(hdr)))
		}
		add(fmt.Sprintf("flg%02x/fields", v), gzipWith(hdr, raw, data))
	}
	for pos := 0; pos < 3; pos++ {
		for v := 0; v < 256; v++ {
			hdr := append([]byte(nil), base...)
			hdr[pos] = byte(v)
			add(fmt.Sprintf("byte%d=%02x", pos, v), gzipWith(hdr, raw, data))
		}
	}
	member := gzipWith(base, raw, data)
	body := member[10:] // DEFLATE stream + 8-byte trailer
	for _, flg := range []byte{gzipFEXTRA, gzipFEXTRA | gzipFNAME, gzipFEXTRA | gzipFHCRC} {
		// XLEN covering everything up to the footer, +-1, 0, and 0xFFFF.
		for _, delta := range []int{-1, 0, 1, 2} {
			in := append([]byte(nil), base...)
			in[3] = flg
			xlen := len(body) - 8 + delta
			in = binary.LittleEndian.AppendUint16(in, uint16(xlen))
			in = append(in, body...)
			add(fmt.Sprintf("xlen-to-footer%+d/flg%02x", delta, flg), in)
		}
		for _, xlen := range []int{0, 0xFFFF} {
			in := append([]byte(nil), base...)
			in[3] = flg
			in = binary.LittleEndian.AppendUint16(in, uint16(xlen))
			in = append(in, make([]byte, min(xlen, 70000))...)
			in = append(in, body...)
			add(fmt.Sprintf("xlen%d/flg%02x", xlen, flg), in)
		}
	}
	for _, flg := range []byte{gzipFNAME, gzipFCOMMENT, gzipFNAME | gzipFCOMMENT, gzipFNAME | gzipFHCRC} {
		// Unterminated string to the end of the input, and of various
		// lengths before a member body.
		in := append([]byte(nil), base...)
		in[3] = flg
		add(fmt.Sprintf("unterminated/flg%02x", flg), append(in, bytes.Repeat([]byte{'z'}, 30)...))
		for _, n := range []int{0, 1, 2, 7, 8, 9} {
			s := append(append([]byte(nil), base...), bytes.Repeat([]byte{'q'}, n)...)
			s[3] = flg
			s = append(s, 0)
			add(fmt.Sprintf("str%d/flg%02x", n, flg), append(s, body...))
			// The string is terminated by the last byte before an 8-byte
			// (or shorter) remainder.
			for rem := 6; rem <= 9; rem++ {
				add(fmt.Sprintf("str%d/rem%d/flg%02x", n, rem, flg), append(append([]byte(nil), s...), make([]byte, rem)...))
			}
		}
	}
	// FHCRC as the last bytes before the footer, and one byte too few.
	for _, rem := range []int{8, 9, 10, 11} {
		in := append([]byte(nil), base...)
		in[3] = gzipFHCRC
		add(fmt.Sprintf("fhcrc-rem%d", rem), append(in, make([]byte, rem)...))
	}
	containerDiff(t, creftest.LDGzip, reqs)
	t.Logf("%d requests", len(reqs))
}

// TestZlibHeaders: all 65536 values of the 2-byte header (FCHECK, CM, CINFO,
// FDICT, FLEVEL) before a valid DEFLATE stream and Adler-32.
func TestZlibHeaders(t *testing.T) {
	creftest.LDBin(t)
	data := []byte("zlib header test data")
	raw := goRaw(t, data, 1)
	var reqs []ldReq
	for h := 0; h < 1<<16; h++ {
		in := zlibWith(uint16(h), raw, data)
		reqs = append(reqs, ldReq{fmt.Sprintf("hdr%04x", h), in, len(data), h%2 == 0})
	}
	containerDiff(t, creftest.LDZlib, reqs)
}

// containerFuzzSeeds: small valid streams in several dst modes, plus a
// truncation and a bit flip of each, and empty inputs.
func containerFuzzSeeds(format string) [][]byte {
	var seeds [][]byte
	data := containerTestData()
	for i, d := range data[:5] {
		c, _ := NewCompressor(i * 2)
		dst := make([]byte, goContainerBound(c, format, len(d)))
		in := dst[:goContainerCompress(c, format, dst, d)]
		for _, mode := range []byte{0, 1, byte(i%8) << 1, 6<<1 | 1} {
			seeds = append(seeds, append([]byte{mode}, in...))
		}
		m := append([]byte{byte(i) << 1}, in...)
		m[1+(i*13)%len(in)] ^= 1 << (i % 8)
		seeds = append(seeds, m, append([]byte{byte(i)}, in[:len(in)*2/3]...))
	}
	return append(seeds, []byte{}, []byte{0})
}

// fuzzContainer is the body of FuzzDecompressGzip / FuzzDecompressZlib:
// arbitrary bytes (data[0] selects the dst size and exact semantics, see
// fuzzReq), decoded by a fresh Go Decompressor and by libdeflate; result,
// actual in/out and output must be identical. Then a Decompressor primed
// with a static and a dynamic raw block decodes the input twice and must
// give the same result.
func fuzzContainer(t *testing.T, format string, data, primer []byte) {
	creftest.LDBin(t)
	var buf []byte
	r := fuzzReq(data)
	g := goContainer(t, NewDecompressor(), format, r, &buf)
	c := ldBatchFormat(t, format, []ldReq{r})[0]
	if g.res != c.res || g.nIn != c.nIn || g.nOut != c.nOut || !bytes.Equal(g.out, c.out) {
		t.Fatalf("in %d bytes, dst %d, exact %v: go result=%d in=%d out=%d, libdeflate result=%d in=%d out=%d",
			len(r.in), r.outSize, r.exact, g.res, g.nIn, g.nOut, c.res, c.nIn, c.nOut)
	}
	d := NewDecompressor()
	if _, _, err := d.DecompressEx(make([]byte, 1<<10), primer); err != nil {
		t.Fatalf("primer: %v", err)
	}
	for pass := 0; pass < 2; pass++ {
		h := goContainer(t, d, format, r, &buf)
		if h.res != g.res || h.nIn != g.nIn || h.nOut != g.nOut || !bytes.Equal(h.out, g.out) {
			t.Fatalf("reused decompressor (pass %d) differs from a fresh one", pass)
		}
	}
}

// FuzzDecompressGzip: see fuzzContainer. Skips without ldref, fails in CI.
func FuzzDecompressGzip(f *testing.F) {
	for _, s := range containerFuzzSeeds(creftest.LDGzip) {
		f.Add(s)
	}
	primer := reusePrimer()
	f.Fuzz(func(t *testing.T, data []byte) { fuzzContainer(t, creftest.LDGzip, data, primer) })
}

// FuzzDecompressZlib: see fuzzContainer. Skips without ldref, fails in CI.
func FuzzDecompressZlib(f *testing.F) {
	for _, s := range containerFuzzSeeds(creftest.LDZlib) {
		f.Add(s)
	}
	primer := reusePrimer()
	f.Fuzz(func(t *testing.T, data []byte) { fuzzContainer(t, creftest.LDZlib, data, primer) })
}
