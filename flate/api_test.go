package flate_test

// This file is shared: flate/api_test.go, gzip/api_test.go and
// zlib/api_test.go are identical apart from the package clause and the
// import path of the package under test (pkg). The format-specific parts
// (ldFormat, overhead, wrapRaw, stdEncode, stdDecode, stdLevels) are in each
// package's format_test.go. TestSharedFileInSync checks the copies.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"

	pkg "github.com/vahid-sohrabloo/fastcompress/flate"

	"github.com/vahid-sohrabloo/fastcompress/flate"
	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflate"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// allLevels are the accepted levels, DefaultCompression included.
var allLevels = []int{-1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

// cLevel is the libdeflate level of a public level (-1 is 6).
func cLevel(level int) int {
	if level == pkg.DefaultCompression {
		return 6
	}
	return level
}

func newCompressor(t testing.TB, level int) *pkg.Compressor {
	t.Helper()
	c, err := pkg.NewCompressor(level)
	if err != nil {
		t.Fatalf("NewCompressor(%d): %v", level, err)
	}
	return c
}

// compress compresses data with c into a CompressBound buffer.
func compress(t testing.TB, c *pkg.Compressor, data []byte) []byte {
	t.Helper()
	dst := make([]byte, c.CompressBound(len(data)))
	n, err := c.Compress(dst, data)
	if err != nil {
		t.Fatalf("Compress(%d bytes) at level %d: %v", len(data), c.Level(), err)
	}
	return dst[:n]
}

// resultCode maps a public error to the libdeflate result code (creftest.LD*).
func resultCode(err error) int {
	switch {
	case err == nil:
		return creftest.LDOK
	case errors.Is(err, pkg.ErrBadData):
		return creftest.LDBadData
	case errors.Is(err, pkg.ErrShortOutput):
		return creftest.LDShortOutput
	case errors.Is(err, pkg.ErrInsufficientSpace):
		return creftest.LDInsufficientSpace
	}
	return -1
}

// ---- ldref batch helpers --------------------------------------------------

// ldRec is one `ldref compress-batch` record (raw DEFLATE): data at level
// into a capBytes-byte buffer (-1: libdeflate_deflate_compress_bound).
type ldRec struct {
	level    int
	capBytes int
	data     []byte
}

// ldCompressRaw returns libdeflate_deflate_compress's output per record (nil
// where it returned 0), in batches of bounded size.
func ldCompressRaw(t testing.TB, recs []ldRec) [][]byte {
	t.Helper()
	const chunk = 48 << 20
	res := make([][]byte, 0, len(recs))
	for start := 0; start < len(recs); {
		var in []byte
		end := start
		for end < len(recs) && (end == start || len(in)+len(recs[end].data) < chunk) {
			r := recs[end]
			c := uint32(0xFFFFFFFF)
			if r.capBytes >= 0 {
				c = uint32(r.capBytes)
			}
			in = append(in, byte(r.level))
			in = binary.LittleEndian.AppendUint32(in, c)
			in = binary.LittleEndian.AppendUint32(in, uint32(len(r.data)))
			in = append(in, r.data...)
			end++
		}
		out := creftest.LDRun(t, in, "compress-batch")
		for i := start; i < end; i++ {
			if len(out) < 4 {
				t.Fatalf("compress-batch: output truncated at record %d", i)
			}
			n := int(binary.LittleEndian.Uint32(out))
			if len(out)-4 < n {
				t.Fatalf("compress-batch: record %d: %d bytes announced, %d left", i, n, len(out)-4)
			}
			var b []byte
			if n > 0 {
				b = out[4 : 4+n : 4+n]
			}
			res = append(res, b)
			out = out[4+n:]
		}
		if len(out) != 0 {
			t.Fatalf("compress-batch: %d trailing output bytes", len(out))
		}
		start = end
	}
	return res
}

// ldReq is one decompression: in decoded into an outSize-byte buffer; exact
// means libdeflate's actual_out_nbytes_ret == NULL (our Decompress), else
// the _ex form (our DecompressEx).
type ldReq struct {
	name    string
	in      []byte
	outSize int
	exact   bool
}

type ldRes struct {
	code, nIn, nOut int
	out             []byte
}

func ldCost(r ldReq) int { return len(r.in) + min(r.outSize, 1032*len(r.in)+64) }

// ldDecompress runs reqs through `ldref decompress-batch -format ldFormat`.
func ldDecompress(t testing.TB, reqs []ldReq) []ldRes {
	t.Helper()
	const chunk = 32 << 20
	var res []ldRes
	for start := 0; start < len(reqs); {
		var payload []byte
		size, end := 0, start
		for end < len(reqs) && (end == start || size+ldCost(reqs[end]) < chunk) {
			r := reqs[end]
			e := byte(0)
			if r.exact {
				e = 1
			}
			payload = append(payload, e)
			payload = binary.LittleEndian.AppendUint64(payload, uint64(r.outSize))
			payload = binary.LittleEndian.AppendUint64(payload, uint64(len(r.in)))
			payload = append(payload, r.in...)
			size += ldCost(r)
			end++
		}
		out := creftest.LDRun(t, payload, "decompress-batch", "-format", ldFormat)
		for i := start; i < end; i++ {
			nl := bytes.IndexByte(out, '\n')
			if nl < 0 {
				t.Fatalf("decompress-batch: missing header for record %d", i)
			}
			var r ldRes
			if _, err := fmt.Sscanf(string(out[:nl]), "result=%d in=%d out=%d", &r.code, &r.nIn, &r.nOut); err != nil {
				t.Fatalf("decompress-batch: bad header %q: %v", out[:nl], err)
			}
			out = out[nl+1:]
			if len(out) < r.nOut {
				t.Fatalf("decompress-batch: record %d: short output", i)
			}
			r.out = out[:r.nOut:r.nOut]
			out = out[r.nOut:]
			res = append(res, r)
		}
		if len(out) != 0 {
			t.Fatalf("decompress-batch: %d trailing bytes", len(out))
		}
		start = end
	}
	return res
}

// ---- inputs ----------------------------------------------------------------

type input struct {
	name string
	data []byte
}

var (
	casesOnce  sync.Once
	casesShort []deflatetest.Case
)

func shortCases() []deflatetest.Case {
	casesOnce.Do(func() { casesShort = deflatetest.Cases(true) })
	return casesShort
}

// caseData returns the short case called name.
func caseData(t testing.TB, name string) []byte {
	t.Helper()
	for _, cs := range shortCases() {
		if cs.Name == name {
			return cs.Data
		}
	}
	t.Fatalf("no case %q", name)
	return nil
}

// compressInputs: Cases(true) (and, without -short, the rest of
// Cases(false)), plus Silesia samples: -short: the first 1 MiB of xml and
// x-ray (256 KiB at levels 10..12); else the whole xml, x-ray and dickens.
func compressInputs(t *testing.T, level int) []input {
	var in []input
	seen := map[string]bool{}
	for _, cs := range shortCases() {
		in = append(in, input{cs.Name, cs.Data})
		seen[cs.Name] = true
	}
	if !testing.Short() {
		for _, cs := range deflatetest.Cases(false) {
			if !seen[cs.Name] {
				in = append(in, input{cs.Name, cs.Data})
			}
		}
	}
	want := map[string]bool{"xml": true, "x-ray": true}
	if !testing.Short() {
		want["dickens"] = true
	}
	for _, f := range deflatetest.Silesia(t) {
		if !want[f.Name] {
			continue
		}
		data := f.Load(t)
		if testing.Short() {
			n := 1 << 20
			if level >= 10 {
				n = 256 << 10
			}
			data = data[:min(n, len(data))]
		}
		in = append(in, input{"silesia/" + f.Name, data})
	}
	return in
}

// ---- (1) compressor byte identity ------------------------------------------

// TestCompressMatchesLibdeflate: per level -1..12, every input is compressed
// by libdeflate (raw, ldref compress-batch, bound-sized buffer) and by the
// public Compress into a CompressBound buffer; Compress must equal
// wrapRaw(C raw), and AppendCompress the same. Every 8th input and the
// Silesia samples are also compressed by `ldref compress -format F` (the real
// libdeflate_{deflate,gzip,zlib}_compress). For inputs <= 64 KiB, an
// exact-size dst gives the same bytes and one byte less gives ErrShortBuffer,
// exactly when libdeflate into the same sizes returns the data and 0.
func TestCompressMatchesLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	for _, level := range allLevels {
		t.Run(strconv.Itoa(level), func(t *testing.T) {
			cl := cLevel(level)
			c := newCompressor(t, level)
			if c.Level() != cl {
				t.Fatalf("Level() = %d, want %d", c.Level(), cl)
			}
			ins := compressInputs(t, cl)
			recs := make([]ldRec, len(ins))
			for i, in := range ins {
				recs[i] = ldRec{cl, -1, in.data}
			}
			craw := ldCompressRaw(t, recs)

			var capRecs []ldRec
			var capIdx []int
			direct := 0
			var app []byte
			for i, in := range ins {
				want := wrapRaw(cl, craw[i], in.data)
				got := compress(t, c, in.data)
				if !bytes.Equal(got, want) {
					t.Fatalf("%s: Compress differs from libdeflate (%d vs %d bytes)", in.name, len(got), len(want))
				}
				app, _ = c.AppendCompress(app[:0], in.data)
				if !bytes.Equal(app, want) {
					t.Fatalf("%s: AppendCompress differs from libdeflate", in.name)
				}
				if i%8 == 0 || strings.HasPrefix(in.name, "silesia/") {
					if d := creftest.LDCompress(t, in.data, cl, ldFormat); !bytes.Equal(d, got) {
						t.Fatalf("%s: Compress differs from ldref compress -format %s", in.name, ldFormat)
					}
					direct++
				}
				if len(in.data) > 64<<10 {
					continue
				}
				// Exact size and one byte less.
				dst := make([]byte, len(want))
				if n, err := c.Compress(dst, in.data); err != nil || !bytes.Equal(dst[:n], want) {
					t.Fatalf("%s: exact-size dst: n=%d err=%v", in.name, n, err)
				}
				if n, err := c.Compress(dst[:len(want)-1], in.data); n != 0 || err != pkg.ErrShortBuffer {
					t.Fatalf("%s: dst of size-1: n=%d err=%v, want 0, ErrShortBuffer", in.name, n, err)
				}
				rawLen := len(craw[i])
				capRecs = append(capRecs, ldRec{cl, rawLen, in.data}, ldRec{cl, rawLen - 1, in.data})
				capIdx = append(capIdx, i)
			}
			// libdeflate into the same (container-adjusted) sizes.
			cc := ldCompressRaw(t, capRecs)
			for k, i := range capIdx {
				if !bytes.Equal(cc[2*k], craw[i]) || cc[2*k+1] != nil {
					t.Fatalf("%s: libdeflate at exact/exact-1 size: %d/%d bytes, want %d/0",
						ins[i].name, len(cc[2*k]), len(cc[2*k+1]), len(craw[i]))
				}
			}
			t.Logf("level %d: %d inputs identical (%d also vs ldref compress -format %s), %d exact/short-dst checks",
				level, len(ins), direct, ldFormat, len(capIdx))
		})
	}
}

// TestCompressSmallDst: every dst size 0..len+2 for small inputs: Compress
// succeeds exactly from the full size up, with libdeflate's bytes.
func TestCompressSmallDst(t *testing.T) {
	inputs := [][]byte{nil, []byte("a"), []byte("hello, hello, hello, hello, hello, hello, hello!"),
		bytes.Repeat([]byte("abcdefgh"), 40)}
	for _, level := range []int{0, 1, 6, 12} {
		c := newCompressor(t, level)
		for _, data := range inputs {
			want := compress(t, c, data)
			if d := creftest.LDCompress(t, data, level, ldFormat); !bytes.Equal(d, want) {
				t.Fatalf("level %d, %d bytes: differs from ldref", level, len(data))
			}
			for size := 0; size <= len(want)+2; size++ {
				dst := make([]byte, size)
				n, err := c.Compress(dst, data)
				if size < len(want) {
					if n != 0 || err != pkg.ErrShortBuffer {
						t.Fatalf("level %d, %d bytes, dst %d: n=%d err=%v", level, len(data), size, n, err)
					}
					continue
				}
				if err != nil || !bytes.Equal(dst[:n], want) {
					t.Fatalf("level %d, %d bytes, dst %d: n=%d err=%v", level, len(data), size, n, err)
				}
			}
		}
	}
}

// TestCompressBound: the package function and the method agree with
// overhead + libdeflate_deflate_compress_bound (ldref compress-bound), and
// saturate at math.MaxInt; a negative size panics.
func TestCompressBound(t *testing.T) {
	c := newCompressor(t, 6)
	sizes := []int{0, 1, 4999, 5000, 5001, 65535, 65536, 1 << 20, 1<<20 + 1, 1 << 30}
	var in []byte
	for _, n := range sizes {
		in = binary.LittleEndian.AppendUint64(in, uint64(n))
	}
	out := creftest.LDRun(t, in, "compress-bound", "-l", "6")
	if len(out) != 8*len(sizes) {
		t.Fatalf("ldref compress-bound: %d bytes", len(out))
	}
	for i, n := range sizes {
		want := overhead + int(binary.LittleEndian.Uint64(out[8*i:]))
		if got := pkg.CompressBound(n); got != want {
			t.Errorf("CompressBound(%d) = %d, want %d", n, got, want)
		}
		if got := c.CompressBound(n); got != want {
			t.Errorf("(*Compressor).CompressBound(%d) = %d, want %d", n, got, want)
		}
	}
	if got := pkg.CompressBound(math.MaxInt); got != math.MaxInt {
		t.Errorf("CompressBound(MaxInt) = %d, want saturation at MaxInt", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("CompressBound(-1) did not panic")
		}
	}()
	pkg.CompressBound(-1)
}

// ---- (2) decompressor differential -----------------------------------------

// decompressStreams returns valid streams with their decompressed data: our
// output (= libdeflate's, see TestCompressMatchesLibdeflate) at levels 0, 1,
// 6, 9, 12 for every 7th short case of at most 128 KiB, and standard
// library output at every stdlib level for a few inputs.
func decompressStreams(t *testing.T) (streams, datas [][]byte) {
	cs := shortCases()
	for _, level := range []int{0, 1, 6, 9, 12} {
		c := newCompressor(t, level)
		for i := 0; i < len(cs); i += 7 {
			if len(cs[i].Data) > 128<<10 {
				continue
			}
			streams = append(streams, compress(t, c, cs[i].Data))
			datas = append(datas, cs[i].Data)
		}
	}
	for _, level := range stdLevels {
		for _, i := range []int{0, 3, 60, 141, len(cs) / 2} {
			streams = append(streams, stdEncode(t, level, cs[i].Data))
			datas = append(datas, cs[i].Data)
		}
	}
	return streams, datas
}

// goDecompress runs r through the public API: Decompress if exact, else
// DecompressEx (whose nIn is compared; Decompress has none, so nIn is
// taken from the reference on success).
func goDecompress(d *pkg.Decompressor, r ldReq, ref ldRes) ldRes {
	dst := make([]byte, r.outSize)
	for i := range dst {
		dst[i] = 0xA5
	}
	if r.exact {
		n, err := d.Decompress(dst, r.in)
		res := ldRes{code: resultCode(err), nOut: n, out: dst[:n]}
		if err == nil {
			res.nIn = ref.nIn
		}
		return res
	}
	nIn, nOut, err := d.DecompressEx(dst, r.in)
	return ldRes{code: resultCode(err), nIn: nIn, nOut: nOut, out: dst[:nOut]}
}

// runDiff runs reqs through ldref and the public API and requires the same
// result code, counts and output. It returns the count per result code.
func runDiff(t *testing.T, d *pkg.Decompressor, reqs []ldReq) map[int]int {
	t.Helper()
	refs := ldDecompress(t, reqs)
	counts := map[int]int{}
	for i, r := range reqs {
		ref := refs[i]
		got := goDecompress(d, r, ref)
		if got.code != ref.code || got.nIn != ref.nIn || got.nOut != ref.nOut || !bytes.Equal(got.out, ref.out) {
			t.Fatalf("%s (in %d bytes, out %d, exact %v): got code=%d in=%d out=%d, libdeflate code=%d in=%d out=%d (bytes equal %v)",
				r.name, len(r.in), r.outSize, r.exact, got.code, got.nIn, got.nOut, ref.code, ref.nIn, ref.nOut,
				bytes.Equal(got.out, ref.out))
		}
		counts[ref.code]++
	}
	return counts
}

func TestDecompressMatchesLibdeflate(t *testing.T) {
	streams, datas := decompressStreams(t)
	r := rand.New(rand.NewPCG(1, 2))
	var reqs []ldReq
	for k, s := range streams {
		n := len(datas[k])
		name := fmt.Sprintf("stream%d", k)
		for _, sz := range []int{n, n - 1, n + 1, 0} {
			if sz >= 0 {
				reqs = append(reqs, ldReq{name, s, sz, true})
			}
		}
		for _, sz := range []int{n, n - 1, n + 1, 0, 2*n + 100} {
			if sz >= 0 {
				reqs = append(reqs, ldReq{name, s, sz, false})
			}
		}
		// Trailing garbage / zeros.
		g := make([]byte, r.IntN(33))
		if k%2 == 0 {
			for i := range g {
				g[i] = byte(r.Uint32())
			}
		}
		tg := append(append([]byte(nil), s...), g...)
		reqs = append(reqs, ldReq{name + "+trailing", tg, n, true}, ldReq{name + "+trailing", tg, 2*n + 100, false})
	}
	d := pkg.NewDecompressor()
	counts := runDiff(t, d, reqs)
	t.Logf("%d valid-stream requests, result codes %v", len(reqs), counts)
	if counts[creftest.LDOK] == 0 || counts[creftest.LDShortOutput] == 0 || counts[creftest.LDInsufficientSpace] == 0 {
		t.Fatalf("result classes not all reached: %v", counts)
	}
}

func TestDecompressMutationsMatchLibdeflate(t *testing.T) {
	streams, datas := decompressStreams(t)
	r := rand.New(rand.NewPCG(3, 4))
	var reqs []ldReq
	truncated, flipped := 0, 0
	largest := 0
	for k, s := range streams {
		if len(s) > len(streams[largest]) {
			largest = k
		}
		n := len(datas[k])
		if len(s) <= 300 && truncated < 40 {
			truncated++
			for cut := 0; cut < len(s); cut++ {
				reqs = append(reqs, ldReq{fmt.Sprintf("stream%d[:%d]", k, cut), s[:cut], n, true},
					ldReq{fmt.Sprintf("stream%d[:%d]", k, cut), s[:cut], 2*n + 100, false})
			}
		}
		if len(s) <= 120 && flipped < 20 {
			flipped++
			for bit := 0; bit < 8*len(s); bit++ {
				m := append([]byte(nil), s...)
				m[bit/8] ^= 1 << (bit % 8)
				reqs = append(reqs, ldReq{fmt.Sprintf("stream%d^bit%d", k, bit), m, n, true},
					ldReq{fmt.Sprintf("stream%d^bit%d", k, bit), m, 2*n + 100, false})
			}
		}
	}
	s := streams[largest]
	n := len(datas[largest])
	for i := 0; i < 1000; i++ {
		m := append([]byte(nil), s...)
		m[r.IntN(len(m))] ^= byte(1 << r.IntN(8))
		reqs = append(reqs, ldReq{fmt.Sprintf("largest flip %d", i), m, n, i%2 == 0})
	}
	for _, in := range [][]byte{nil, {0}, {1}, {3, 0}, {0x1f, 0x8b}, {0x78, 0x9c}, {0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255, 3, 0}} {
		reqs = append(reqs, ldReq{fmt.Sprintf("short %x", in), in, 0, true}, ldReq{fmt.Sprintf("short %x", in), in, 64, false})
	}
	d := pkg.NewDecompressor()
	counts := runDiff(t, d, reqs)
	t.Logf("%d mutated-stream requests (%d streams truncated, %d bit-flipped), result codes %v",
		len(reqs), truncated, flipped, counts)
	if counts[creftest.LDBadData] == 0 {
		t.Fatalf("no bad-data result: %v", counts)
	}
}

// ---- (3) standard library round trips --------------------------------------

func TestStdlibDecodesOurOutput(t *testing.T) {
	cs := shortCases()
	for _, level := range allLevels {
		c := newCompressor(t, level)
		for i := 0; i < len(cs); i += 5 {
			// No goroutines here: under -race a small-input smoke suffices.
			if raceEnabled && len(cs[i].Data) > 20000 {
				continue
			}
			got, err := stdDecode(compress(t, c, cs[i].Data))
			if err != nil || !bytes.Equal(got, cs[i].Data) {
				t.Fatalf("level %d, %s: standard library decode: err=%v, equal=%v", level, cs[i].Name, err, bytes.Equal(got, cs[i].Data))
			}
		}
	}
}

func TestWeDecodeStdlibOutput(t *testing.T) {
	cs := shortCases()
	d := pkg.NewDecompressor()
	for _, level := range stdLevels {
		for i := 0; i < len(cs); i += 9 {
			data := cs[i].Data
			s := stdEncode(t, level, data)
			dst := make([]byte, len(data))
			if n, err := d.Decompress(dst, s); err != nil || n != len(data) || !bytes.Equal(dst, data) {
				t.Fatalf("std level %d, %s: Decompress n=%d err=%v", level, cs[i].Name, n, err)
			}
			big := make([]byte, len(data)+100)
			nIn, nOut, err := d.DecompressEx(big, s)
			if err != nil || nIn != len(s) || nOut != len(data) || !bytes.Equal(big[:nOut], data) {
				t.Fatalf("std level %d, %s: DecompressEx nIn=%d/%d nOut=%d err=%v", level, cs[i].Name, nIn, len(s), nOut, err)
			}
			out, err := d.AppendDecompress([]byte("x"), s, 0)
			if err != nil || string(out[:1]) != "x" || !bytes.Equal(out[1:], data) {
				t.Fatalf("std level %d, %s: AppendDecompress err=%v", level, cs[i].Name, err)
			}
		}
	}
}

// ---- (4) Append behaviour ----------------------------------------------------

func TestAppendCompress(t *testing.T) {
	c := newCompressor(t, 6)
	data := bytes.Repeat([]byte("append compress "), 1000)
	want := compress(t, c, data)
	prefix := []byte("prefix")

	// nil dst, short dst (grows), dst with enough capacity (no growth).
	got, err := c.AppendCompress(nil, data)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("nil dst: err=%v equal=%v", err, bytes.Equal(got, want))
	}
	got, err = c.AppendCompress(append([]byte(nil), prefix...), data)
	if err != nil || !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], want) {
		t.Fatalf("prefix dst: err=%v", err)
	}
	buf := make([]byte, len(prefix), len(prefix)+c.CompressBound(len(data)))
	copy(buf, prefix)
	got, err = c.AppendCompress(buf, data)
	if err != nil || &got[0] != &buf[0] || !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], want) {
		t.Fatalf("dst with capacity: err=%v, same array %v", err, &got[0] == &buf[0])
	}
	// One byte of capacity too few: grows, still correct.
	buf = make([]byte, len(prefix), len(prefix)+c.CompressBound(len(data))-1)
	copy(buf, prefix)
	got, err = c.AppendCompress(buf, data)
	if err != nil || !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], want) {
		t.Fatalf("dst one short: err=%v", err)
	}
	// Empty input.
	empty := compress(t, c, nil)
	got, _ = c.AppendCompress(prefix[:len(prefix):len(prefix)], nil)
	if !bytes.Equal(got[len(prefix):], empty) || string(prefix) != "prefix" {
		t.Fatalf("empty input: %x, want %x", got[len(prefix):], empty)
	}
}

func TestAppendDecompress(t *testing.T) {
	c := newCompressor(t, 6)
	data := caseData(t, "text")
	s := compress(t, c, data)
	n := len(data)
	prefix := []byte("prefix")
	check := func(name string, out []byte, err error) {
		t.Helper()
		if err != nil || !bytes.Equal(out[:len(prefix)], prefix) || !bytes.Equal(out[len(prefix):], data) {
			t.Fatalf("%s: err=%v len=%d want %d", name, err, len(out), len(prefix)+n)
		}
	}
	d := pkg.NewDecompressor()
	for _, hint := range []int{-5, 0, 1, n - 1, n, n + 1, 10 * n} {
		out, err := d.AppendDecompress(append([]byte(nil), prefix...), s, hint)
		check(fmt.Sprintf("hint %d", hint), out, err)
	}
	// Spare capacity is used as is.
	buf := make([]byte, len(prefix), len(prefix)+n)
	copy(buf, prefix)
	out, err := d.AppendDecompress(buf, s, 1)
	check("spare capacity", out, err)
	if &out[0] != &buf[0] {
		t.Fatal("spare capacity: dst was reallocated")
	}

	// The output limit: exactly n bytes fit, n-1 do not.
	d.SetMaxOutput(n)
	if d.MaxOutput() != n {
		t.Fatalf("MaxOutput() = %d, want %d", d.MaxOutput(), n)
	}
	out, err = d.AppendDecompress(append([]byte(nil), prefix...), s, 0)
	check("limit n", out, err)
	d.SetMaxOutput(n - 1)
	for _, hint := range []int{0, 1, n, 10 * n} {
		out, err = d.AppendDecompress(append([]byte(nil), prefix...), s, hint)
		if err != pkg.ErrOutputLimit || !errors.Is(err, pkg.ErrInsufficientSpace) || !bytes.Equal(out, prefix) {
			t.Fatalf("limit n-1, hint %d: err=%v out=%q", hint, err, out)
		}
		// Retries allocate exactly: the capacity never passes the limit.
		if cap(out) > len(prefix)+n-1 {
			t.Fatalf("limit n-1, hint %d: capacity %d grew past the limit %d", hint, cap(out), n-1)
		}
	}
	// SetMaxOutput(<= 0) restores the default; the zero value has it too.
	d.SetMaxOutput(0)
	if d.MaxOutput() != pkg.DefaultMaxOutput {
		t.Fatalf("SetMaxOutput(0): MaxOutput() = %d", d.MaxOutput())
	}
	d.SetMaxOutput(-1)
	if d.MaxOutput() != pkg.DefaultMaxOutput {
		t.Fatalf("SetMaxOutput(-1): MaxOutput() = %d", d.MaxOutput())
	}
	var zero pkg.Decompressor
	if zero.MaxOutput() != pkg.DefaultMaxOutput {
		t.Fatal("zero Decompressor: wrong default limit")
	}
	out, err = zero.AppendDecompress(append([]byte(nil), prefix...), s, 0)
	check("zero value", out, err)

	// Bad data: ErrBadData, dst length unchanged.
	bad := append([]byte(nil), s...)
	bad = bad[:len(bad)/2]
	out, err = d.AppendDecompress(append([]byte(nil), prefix...), bad, 0)
	if !errors.Is(err, pkg.ErrBadData) || !bytes.Equal(out, prefix) {
		t.Fatalf("truncated stream: err=%v out=%q", err, out)
	}
	// Empty output.
	es := compress(t, c, nil)
	out, err = d.AppendDecompress(nil, es, 0)
	if err != nil || len(out) != 0 {
		t.Fatalf("empty stream: err=%v len=%d", err, len(out))
	}
}

// zeroBomb returns a raw DEFLATE stream of n zero bytes (n >= 1, n-1 a
// multiple of 258): one static-Huffman block with a literal 0 then
// (n-1)/258 matches of length 258 at distance 1, about 13 bits per 258
// output bytes. It never allocates the n bytes.
func zeroBomb(n int) []byte {
	var out []byte
	var bitbuf uint64
	var bits uint
	put := func(v uint64, nb uint) {
		bitbuf |= v << bits
		bits += nb
		for bits >= 8 {
			out = append(out, byte(bitbuf))
			bitbuf >>= 8
			bits -= 8
		}
	}
	// Static Huffman codes are sent MSB first: reverse them.
	code := func(c uint64, nb uint) {
		var r uint64
		for i := uint(0); i < nb; i++ {
			r |= (c >> i & 1) << (nb - 1 - i)
		}
		put(r, nb)
	}
	put(1, 1)     // BFINAL
	put(1, 2)     // BTYPE 01: static
	code(0x30, 8) // literal 0: 00110000
	for k := 0; k < (n-1)/258; k++ {
		code(0xC5, 8) // length 258: symbol 285 = 11000101
		code(0, 5)    // distance 1: code 0
	}
	code(0, 7) // end of block: symbol 256 = 0000000
	if bits > 0 {
		out = append(out, byte(bitbuf))
	}
	return out
}

func TestAppendDecompressBomb(t *testing.T) {
	// Check the generator on a small size against the standard library.
	small := 1 + 258*1000
	zeros := make([]byte, small)
	s := wrapRaw(6, zeroBomb(small), zeros)
	if got, err := stdDecode(s); err != nil || !bytes.Equal(got, zeros) {
		t.Fatalf("zeroBomb(%d) is not a valid stream: %v", small, err)
	}
	d := pkg.NewDecompressor()
	d.SetMaxOutput(small)
	if out, err := d.AppendDecompress(nil, s, 0); err != nil || !bytes.Equal(out, zeros) {
		t.Fatalf("zeroBomb(%d) at limit %d: %v", small, small, err)
	}

	// 1 GiB of zeros (+1 byte) in about 6.5 MB; the trailer's checksum is
	// irrelevant (decoding stops at the limit, before it is read). With a
	// 1 MiB limit the call must fail without allocating more than that.
	n := 1 + 258*4161790 // 1073741821 bytes
	bomb := wrapRaw(6, zeroBomb(n), nil)
	t.Logf("bomb: %d bytes decompress to %d", len(bomb), n)
	d.SetMaxOutput(1 << 20)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, err := d.AppendDecompress(nil, bomb, 0)
	runtime.ReadMemStats(&after)
	if err != pkg.ErrOutputLimit || len(out) != 0 {
		t.Fatalf("bomb with a 1 MiB limit: err=%v len=%d", err, len(out))
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 4<<20 {
		t.Fatalf("bomb with a 1 MiB limit allocated %d bytes", alloc)
	}
	if err.Error() == "" || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("unclear error %q", err)
	}
}

// TestAppendDecompressHugeHint: a sizeHint beyond what src can decompress
// to (at most 1032 bytes per input byte) is clamped before allocating, so
// math.MaxInt neither panics (growslice) nor allocates the output limit.
func TestAppendDecompressHugeHint(t *testing.T) {
	c := newCompressor(t, 6)
	data := caseData(t, "text")
	s := compress(t, c, data)
	prefix := []byte("prefix")
	// The largest allowed first attempt, plus the allocator's rounding.
	bound := 1032*len(s) + 1024
	bound += bound / 8
	for _, limit := range []int{0, math.MaxInt} {
		d := pkg.NewDecompressor()
		d.SetMaxOutput(limit)
		for _, hint := range []int{math.MaxInt, math.MaxInt - len(prefix), math.MaxInt / 2, math.MaxInt / 1024} {
			name := fmt.Sprintf("limit %d hint %d", limit, hint)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			out, err := d.AppendDecompress(append([]byte(nil), prefix...), s, hint)
			runtime.ReadMemStats(&after)
			if err != nil || !bytes.Equal(out[:len(prefix)], prefix) || !bytes.Equal(out[len(prefix):], data) {
				t.Fatalf("%s: err=%v len=%d", name, err, len(out))
			}
			if cap(out) > len(prefix)+bound {
				t.Fatalf("%s: capacity %d, want <= %d", name, cap(out), len(prefix)+bound)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > uint64(2*bound) {
				t.Fatalf("%s: allocated %d bytes", name, alloc)
			}
			// Invalid and empty input fail cleanly, with a small allocation.
			for _, bad := range [][]byte{nil, {0xff}, s[:len(s)/2]} {
				out, err := d.AppendDecompress(append([]byte(nil), prefix...), bad, hint)
				if !errors.Is(err, pkg.ErrBadData) || !bytes.Equal(out, prefix) {
					t.Fatalf("%s, %d bad bytes: err=%v out=%q", name, len(bad), err, out)
				}
				if cap(out) > len(prefix)+bound {
					t.Fatalf("%s, %d bad bytes: capacity %d", name, len(bad), cap(out))
				}
			}
		}
	}
}

// ---- (5) levels and errors ---------------------------------------------------

func TestNewCompressorLevels(t *testing.T) {
	for _, level := range []int{-2, 13, -100, 100, math.MinInt, math.MaxInt} {
		c, err := pkg.NewCompressor(level)
		if c != nil || !errors.Is(err, pkg.ErrInvalidLevel) || !strings.Contains(err.Error(), strconv.Itoa(level)) {
			t.Errorf("NewCompressor(%d) = %v, %v", level, c, err)
		}
	}
	for _, level := range allLevels {
		c := newCompressor(t, level)
		if c.Level() != cLevel(level) {
			t.Errorf("NewCompressor(%d).Level() = %d", level, c.Level())
		}
	}
	consts := map[string][2]int{
		"NoCompression":      {pkg.NoCompression, 0},
		"BestSpeed":          {pkg.BestSpeed, 1},
		"BestCompression":    {pkg.BestCompression, 9},
		"DefaultCompression": {pkg.DefaultCompression, -1},
		"Level10":            {pkg.Level10, 10},
		"Level11":            {pkg.Level11, 11},
		"Level12":            {pkg.Level12, 12},
		"MaxLevel":           {pkg.MaxLevel, 12},
		"DefaultMaxOutput":   {pkg.DefaultMaxOutput, 1 << 30},
	}
	for name, v := range consts {
		if v[0] != v[1] {
			t.Errorf("%s = %d, want %d", name, v[0], v[1])
		}
	}
}

func TestErrorIdentities(t *testing.T) {
	pairs := []struct {
		name          string
		ours, fl, int error
	}{
		{"ErrInvalidLevel", pkg.ErrInvalidLevel, flate.ErrInvalidLevel, deflate.ErrInvalidLevel},
		{"ErrBadData", pkg.ErrBadData, flate.ErrBadData, deflate.ErrBadData},
		{"ErrShortOutput", pkg.ErrShortOutput, flate.ErrShortOutput, deflate.ErrShortOutput},
		{"ErrInsufficientSpace", pkg.ErrInsufficientSpace, flate.ErrInsufficientSpace, deflate.ErrInsufficientSpace},
		{"ErrShortBuffer", pkg.ErrShortBuffer, flate.ErrShortBuffer, nil},
		{"ErrOutputLimit", pkg.ErrOutputLimit, flate.ErrOutputLimit, nil},
	}
	for _, p := range pairs {
		if p.ours == nil || p.ours != p.fl || !errors.Is(p.ours, p.fl) {
			t.Errorf("%s: not flate's value", p.name)
		}
		if p.int != nil && p.ours != p.int {
			t.Errorf("%s: not internal/deflate's value", p.name)
		}
	}
	if !errors.Is(pkg.ErrOutputLimit, pkg.ErrInsufficientSpace) {
		t.Error("ErrOutputLimit does not wrap ErrInsufficientSpace")
	}
	// The values returned by the API.
	d := pkg.NewDecompressor()
	if _, err := d.Decompress(make([]byte, 1), []byte{0xff, 0xff}); err != pkg.ErrBadData {
		t.Errorf("garbage: %v", err)
	}
	s := compress(t, newCompressor(t, 1), []byte("abc"))
	if _, err := d.Decompress(make([]byte, 4), s); err != pkg.ErrShortOutput {
		t.Errorf("larger dst: %v", err)
	}
	if _, err := d.Decompress(make([]byte, 2), s); err != pkg.ErrInsufficientSpace {
		t.Errorf("smaller dst: %v", err)
	}
	if _, _, err := d.DecompressEx(make([]byte, 2), s); err != pkg.ErrInsufficientSpace {
		t.Errorf("DecompressEx smaller dst: %v", err)
	}
}

// ---- (6) allocations -------------------------------------------------------

func TestAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under -race")
	}
	data := caseData(t, "text")
	d := pkg.NewDecompressor()
	for _, level := range []int{0, 1, 2, 6, 9, 10, 12} {
		c := newCompressor(t, level)
		dst := make([]byte, c.CompressBound(len(data)))
		s := compress(t, c, data)
		if a := testing.AllocsPerRun(5, func() { _, _ = c.Compress(dst, data) }); a != 0 {
			t.Errorf("level %d: Compress allocates %.1f", level, a)
		}
		app := make([]byte, 0, len(dst))
		if a := testing.AllocsPerRun(5, func() { _, _ = c.AppendCompress(app, data) }); a != 0 {
			t.Errorf("level %d: AppendCompress with capacity allocates %.1f", level, a)
		}
		out := make([]byte, len(data))
		if a := testing.AllocsPerRun(10, func() { _, _ = d.Decompress(out, s) }); a != 0 {
			t.Errorf("level %d: Decompress allocates %.1f", level, a)
		}
		big := make([]byte, len(data)+10)
		if a := testing.AllocsPerRun(10, func() { _, _, _ = d.DecompressEx(big, s) }); a != 0 {
			t.Errorf("level %d: DecompressEx allocates %.1f", level, a)
		}
		if a := testing.AllocsPerRun(10, func() { _, _ = d.AppendDecompress(out[:0], s, len(data)) }); a != 0 {
			t.Errorf("level %d: AppendDecompress with capacity allocates %.1f", level, a)
		}
		// Error paths do not allocate either.
		if a := testing.AllocsPerRun(10, func() { _, _ = c.Compress(dst[:3], data) }); a != 0 {
			t.Errorf("level %d: Compress (short dst) allocates %.1f", level, a)
		}
		if a := testing.AllocsPerRun(10, func() { _, _ = d.Decompress(out[:1], s) }); a != 0 {
			t.Errorf("level %d: Decompress (error) allocates %.1f", level, a)
		}
	}
}

// TestAppendDecompressExactResult: without a sizeHint, AppendDecompress
// into a dst without spare capacity (nil, or full) returns a new array of
// exactly the output, in one allocation of that size once the
// Decompressor's buffer has grown: not the max(4*len(src), 1024) (or
// doubled) space it decodes in.
func TestAppendDecompressExactResult(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under -race")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1)) // keep the pools
	c := newCompressor(t, 6)
	d := pkg.NewDecompressor()
	rnd := rand.New(rand.NewPCG(5, 6))
	text := caseData(t, "text")
	for _, n := range []int{0, 100, 1000, 64 << 10, 200 << 10} {
		data := make([]byte, 0, n)
		for len(data) < n {
			data = append(data, text[:min(len(text), 512)]...)
			data = append(data, byte(rnd.Uint32()), byte(rnd.Uint32()))
		}
		data = data[:n]
		s := compress(t, c, data)
		prefix := []byte("prefix")
		for _, dst := range [][]byte{nil, prefix[:len(prefix):len(prefix)]} {
			var out []byte
			var err error
			run := func() { out, err = d.AppendDecompress(dst, s, 0) }
			run()
			want := 1.0
			if n == 0 {
				want = 0 // dst itself
			}
			if a := testing.AllocsPerRun(5, run); a != want {
				t.Errorf("%d bytes, dst %q: %v allocations, want %v", n, dst, a, want)
			}
			if err != nil || !bytes.Equal(out[len(dst):], data) || !bytes.Equal(out[:len(dst)], dst) || cap(out) != len(out) {
				t.Fatalf("%d bytes, dst %q: err %v, len %d, cap %d", n, dst, err, len(out), cap(out))
			}
		}
	}
}

// TestFootprint reports (with -v) the memory allocated by NewCompressor per
// level and by NewDecompressor (runtime.MemStats.TotalAlloc deltas).
func TestFootprint(t *testing.T) {
	measure := func(f func() any) uint64 {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		v := f()
		runtime.ReadMemStats(&b)
		runtime.KeepAlive(v)
		return b.TotalAlloc - a.TotalAlloc
	}
	for level := 0; level <= 12; level++ {
		n := measure(func() any { c, _ := pkg.NewCompressor(level); return c })
		t.Logf("footprint: NewCompressor(%d) = %d bytes (%.1f KiB)", level, n, float64(n)/1024)
	}
	n := measure(func() any { return pkg.NewDecompressor() })
	t.Logf("footprint: NewDecompressor() = %d bytes (%.1f KiB)", n, float64(n)/1024)
}

// ---- (7) concurrency ----------------------------------------------------------

// TestConcurrentInstances: 8 goroutines, each with its own Compressor and
// Decompressor, on shared read-only inputs; outputs must equal a serial run.
func TestConcurrentInstances(t *testing.T) {
	cs := shortCases()
	var ins [][]byte
	for i := 0; i < len(cs); i += 23 {
		if len(cs[i].Data) <= 256<<10 {
			ins = append(ins, cs[i].Data)
		}
	}
	levels := []int{-1, 0, 1, 3, 6, 9, 10, 12}
	want := make([][][]byte, len(levels))
	for g, level := range levels {
		c := newCompressor(t, level)
		for _, in := range ins {
			want[g] = append(want[g], compress(t, c, in))
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(levels))
	for g, level := range levels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pkg.NewCompressor(level)
			if err != nil {
				errs <- err
				return
			}
			d := pkg.NewDecompressor()
			for round := 0; round < 2; round++ {
				for i, in := range ins {
					got, _ := c.AppendCompress(nil, in)
					if !bytes.Equal(got, want[g][i]) {
						errs <- fmt.Errorf("level %d input %d: output differs from the serial run", level, i)
						return
					}
					out := make([]byte, len(in))
					if _, err := d.Decompress(out, got); err != nil || !bytes.Equal(out, in) {
						errs <- fmt.Errorf("level %d input %d: round trip: %v", level, i, err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// ---- (9) documentation ---------------------------------------------------------

// TestExportedDocs: every exported top-level identifier of the package (non
// test files) has a doc comment (its own, or its group's for const/var
// blocks).
func TestExportedDocs(t *testing.T) {
	fset := token.NewFileSet()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	checked := 0
	hasPkgDoc := false
	for _, f := range files {
		if f.Doc != nil {
			hasPkgDoc = true
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() {
					checked++
					if d.Doc == nil {
						t.Errorf("%s: no doc comment", d.Name.Name)
					}
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch s := sp.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							checked++
							if s.Doc == nil && d.Doc == nil {
								t.Errorf("type %s: no doc comment", s.Name.Name)
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								checked++
								if s.Doc == nil && s.Comment == nil && d.Doc == nil {
									t.Errorf("%s: no doc comment", n.Name)
								}
							}
						}
					}
				}
			}
		}
	}
	if !hasPkgDoc {
		t.Error("no package doc comment")
	}
	if checked < 20 {
		t.Fatalf("only %d exported identifiers found", checked)
	}
}

// FuzzBuffer: the whole-buffer API on arbitrary input (seeded with
// compressed data) never panics, and AppendDecompress and Decompress agree:
// a successful AppendDecompress gives the data Decompress decodes into a
// buffer of exactly that size. The input compressed at a level chosen by
// the first byte decodes back to it through both.
func FuzzBuffer(f *testing.F) {
	for i, cs := range shortCases() {
		if len(cs.Data) > 5000 || i%3 != 0 {
			continue
		}
		level := allLevels[i%len(allLevels)]
		f.Add(append([]byte{byte(i)}, compress(f, newCompressor(f, level), cs.Data)...))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 || len(data) > 1<<16 {
			return
		}
		in := data[1:]
		d := pkg.NewDecompressor()
		d.SetMaxOutput(1 << 22)
		if got, err := d.AppendDecompress(nil, in, 0); err == nil {
			dst := make([]byte, len(got))
			if n, err := d.Decompress(dst, in); err != nil || n != len(got) || !bytes.Equal(dst, got) {
				t.Fatalf("AppendDecompress gave %d bytes; Decompress: %d, %v", len(got), n, err)
			}
		} else if !errors.Is(err, pkg.ErrBadData) && !errors.Is(err, pkg.ErrOutputLimit) {
			t.Fatalf("AppendDecompress: %v", err)
		}
		z := compress(t, newCompressor(t, allLevels[int(data[0])%len(allLevels)]), in)
		back, err := d.AppendDecompress(nil, z, 0)
		dst := make([]byte, len(in))
		n, err2 := d.Decompress(dst, z)
		if err != nil || err2 != nil || n != len(in) || !bytes.Equal(back, in) || !bytes.Equal(dst, in) {
			t.Fatalf("round trip: AppendDecompress %v, Decompress %d, %v", err, n, err2)
		}
	})
}

// TestSharedFileInSync: the three copies of this file differ only in the
// package clause and the import of the package under test.
func TestSharedFileInSync(t *testing.T) {
	norm := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "package ") || strings.HasPrefix(l, "\tpkg \"") {
				continue
			}
			lines = append(lines, l)
		}
		return strings.Join(lines, "\n")
	}
	me := norm("api_test.go")
	for _, other := range []string{"flate", "gzip", "zlib"} {
		if norm(filepath.Join("..", other, "api_test.go")) != me {
			t.Errorf("../%s/api_test.go is out of sync with this copy", other)
		}
	}
}
