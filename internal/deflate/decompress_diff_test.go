package deflate

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// Differential tests against libdeflate (ref/ldref decompress-batch, which
// runs libdeflate_deflate_decompress_ex per record): the result code, the
// actual in/out counts and the output bytes must be identical.

// ldReq is one decompression: in decoded into an outSize-byte buffer, with
// exact true meaning actual_out_nbytes_ret == NULL (Decompress).
type ldReq struct {
	name    string
	in      []byte
	outSize int
	exact   bool
}

type ldRes struct {
	res       libdeflateResult
	nIn, nOut int
	out       []byte
}

// ldBatch runs reqs through `ldref decompress-batch`, in chunks of bounded
// size (input plus worst-case output).
func ldBatch(t testing.TB, reqs []ldReq) []ldRes {
	t.Helper()
	return ldBatchFormat(t, creftest.LDRaw, reqs)
}

// ldBatchFormat is ldBatch for a container format (creftest.LDRaw, LDGzip
// or LDZlib: ldref runs libdeflate_{deflate,gzip,zlib}_decompress_ex).
func ldBatchFormat(t testing.TB, format string, reqs []ldReq) []ldRes {
	t.Helper()
	const chunk = diffBatchBytes
	var res []ldRes
	for start := 0; start < len(reqs); {
		var payload []byte
		size := 0
		end := start
		// The output of a record is at most outSize bytes, and at most
		// 1032 per input byte (the DEFLATE maximum ratio, plus slack).
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
		out := creftest.LDRun(t, payload, "decompress-batch", "-format", format)
		for i := start; i < end; i++ {
			nl := bytes.IndexByte(out, '\n')
			if nl < 0 {
				t.Fatalf("ldref decompress-batch: missing header for record %d", i)
			}
			var r ldRes
			var code int
			if _, err := fmt.Sscanf(string(out[:nl]), "result=%d in=%d out=%d", &code, &r.nIn, &r.nOut); err != nil {
				t.Fatalf("ldref decompress-batch: bad header %q: %v", out[:nl], err)
			}
			r.res = libdeflateResult(code)
			out = out[nl+1:]
			if len(out) < r.nOut {
				t.Fatalf("ldref decompress-batch: record %d: short output", i)
			}
			r.out = out[:r.nOut:r.nOut]
			out = out[r.nOut:]
			res = append(res, r)
		}
		if len(out) != 0 {
			t.Fatalf("ldref decompress-batch: %d trailing bytes", len(out))
		}
		start = end
	}
	return res
}

// goDecompress runs r through the Go decompressor the way ldref runs it
// (actual_in always requested; actual_out unless exact), and then through
// the public API (Decompress if exact, else DecompressEx), checking that both
// agree in result, counts and output. The output of the internal call is
// snapshotted and the buffer refilled (0xA5) before the public call, so neither call
// can supply bytes the other failed to write.
func goDecompress(t testing.TB, d *Decompressor, r ldReq, buf *[]byte) ldRes {
	t.Helper()
	if cap(*buf) < r.outSize {
		*buf = make([]byte, r.outSize)
	}
	dst := (*buf)[:r.outSize]
	fillDst(dst)
	res, nIn, nOut := d.decompressImpl(r.in, dst, true, !r.exact)
	if res == libdeflateSuccess && r.exact {
		nOut = r.outSize
	}
	g := ldRes{res, nIn, nOut, append([]byte(nil), dst[:nOut]...)}
	fillDst(dst)
	var err error
	pubOut := 0
	if r.exact {
		var n int
		n, err = d.Decompress(dst, r.in)
		if err == nil && n != r.outSize {
			t.Fatalf("%s: Decompress returned %d, want %d", r.name, n, r.outSize)
		}
		pubOut = n
	} else {
		var a, b int
		a, b, err = d.DecompressEx(dst, r.in)
		if err == nil && (a != nIn || b != nOut) {
			t.Fatalf("%s: DecompressEx (%d, %d), internal (%d, %d)", r.name, a, b, nIn, nOut)
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

// fillDst fills dst with a non-zero pattern, so that a skipped write of a byte
// whose true value is 0 is detected too.
func fillDst(dst []byte) {
	for i := range dst {
		dst[i] = 0xA5
	}
}

// diffRunner compares Go with libdeflate on requests added one at a time,
// running them in batches of bounded size so that memory stays small (only
// one batch of inputs and outputs is alive at a time).
type diffRunner struct {
	t       *testing.T
	d       *Decompressor
	buf     []byte
	pending []ldReq
	size    int
	fails   int
	total   int
	counts  map[libdeflateResult]int
}

const diffBatchBytes = 32 << 20

func newDiffRunner(t *testing.T) *diffRunner {
	return &diffRunner{t: t, d: NewDecompressor(), counts: map[libdeflateResult]int{}}
}

func ldCost(r ldReq) int { return len(r.in) + min(r.outSize, 1032*len(r.in)+64) }

func (dr *diffRunner) add(r ldReq) {
	if c := ldCost(r); dr.size+c > diffBatchBytes && len(dr.pending) > 0 {
		dr.flush()
	}
	dr.pending = append(dr.pending, r)
	dr.size += ldCost(r)
}

func (dr *diffRunner) flush() {
	t := dr.t
	t.Helper()
	want := ldBatch(t, dr.pending)
	for i, r := range dr.pending {
		g := goDecompress(t, dr.d, r, &dr.buf)
		c := want[i]
		dr.counts[c.res]++
		if g.res != c.res || g.nIn != c.nIn || g.nOut != c.nOut || !bytes.Equal(g.out, c.out) {
			t.Errorf("%s (in %d bytes, dst %d, exact %v): go result=%d in=%d out=%d, libdeflate result=%d in=%d out=%d, output equal %v",
				r.name, len(r.in), r.outSize, r.exact, g.res, g.nIn, g.nOut, c.res, c.nIn, c.nOut, bytes.Equal(g.out, c.out))
			if dr.fails++; dr.fails > 20 {
				t.Fatal("too many mismatches")
			}
		}
	}
	dr.total += len(dr.pending)
	clear(dr.pending)
	dr.pending = dr.pending[:0]
	dr.size = 0
}

func (dr *diffRunner) done() {
	if len(dr.pending) > 0 {
		dr.flush()
	}
	dr.t.Logf("%d requests: ok %d, bad_data %d, short_output %d, insufficient_space %d", dr.total,
		dr.counts[libdeflateSuccess], dr.counts[libdeflateBadData], dr.counts[libdeflateShortOutput], dr.counts[libdeflateInsufficientSpace])
}

// diffCheck compares Go with libdeflate on every request.
func diffCheck(t *testing.T, reqs []ldReq) {
	t.Helper()
	dr := newDiffRunner(t)
	for _, r := range reqs {
		dr.add(r)
	}
	dr.done()
}

// diffStream is a valid stream with its decompressed size.
type diffStream struct {
	name string
	in   []byte
	size int
}

// diffStreams returns the golden streams (valid and invalid; size is a
// guess for invalid ones) and libdeflate / stdlib compressions of a set of
// corpus cases.
func diffStreams(t *testing.T) []diffStream {
	var ss []diffStream
	for _, g := range goldenCases() {
		size := len(g.out)
		if g.res != libdeflateSuccess {
			size = 1000
		}
		ss = append(ss, diffStream{"golden/" + g.name, g.in, size})
	}
	pick := map[string]bool{}
	for _, n := range []string{
		"sweep-text/0", "sweep-text/1", "sweep-text/37", "sweep-text/70", "sweep-rand/5", "sweep-rand/70",
		"size-text/513", "size-text/4096", "size-text/32769", "size-text/65536", "size-text/300001",
		"size-rand/4097", "size-rand/65536", "zero", "same-0x41", "period/2", "period/258", "runs",
		"random", "text", "alphabet/4", "alphabet-skew/20", "records", "many-short-matches",
		"edge-matches", "segments", "litrun-stride251", "dense-matches", "eof-same/258",
		"eof-period3/515", "tail-copy/258+1", "sparse-mutations/3", "thue-morse/24000",
	} {
		pick[n] = true
	}
	ldLevels := []int{0, 1, 2, 4, 6, 8, 9, 10, 12}
	stdLevels := []int{-2, 0, 1, 6, 9}
	if testing.Short() {
		ldLevels = []int{0, 1, 6, 12}
		stdLevels = []int{-2, 6}
	}
	for _, c := range deflatetest.Cases(true) {
		if !pick[c.Name] {
			continue
		}
		for _, l := range ldLevels {
			ss = append(ss, diffStream{fmt.Sprintf("%s/ld%d", c.Name, l), creftest.LDCompress(t, c.Data, l, creftest.LDRaw), len(c.Data)})
		}
		for _, l := range stdLevels {
			ss = append(ss, diffStream{fmt.Sprintf("%s/std%d", c.Name, l), stdlibCompress(t, c.Data, l), len(c.Data)})
		}
	}
	return ss
}

// dstSizes are the output buffer sizes tried for a stream of size bytes:
// exact, +1, +100, -1, 0 and a huge one.
func dstSizes(size int) []int {
	s := []int{size, size + 1, size + 100, 0, size + 4<<20}
	if size > 0 {
		s = append(s, size-1)
	}
	return s
}

func TestDiffValidStreams(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	var reqs []ldReq
	for _, s := range diffStreams(t) {
		for _, n := range dstSizes(s.size) {
			for _, exact := range []bool{false, true} {
				reqs = append(reqs, ldReq{s.name + "/dst" + strconv.Itoa(n), s.in, n, exact})
			}
		}
		// Trailing bytes after the stream.
		tr := append(append([]byte(nil), s.in...), 0x00, 0xff, 0x13, 0x37, 0, 0, 0, 0, 0, 0, 0, 0)
		reqs = append(reqs, ldReq{s.name + "/trailing", tr, s.size + 10, false})
	}
	diffCheck(t, reqs)
}

func TestDiffTruncations(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	r := rand.New(rand.NewPCG(1, 2))
	var reqs []ldReq
	for i, s := range diffStreams(t) {
		var offs []int
		if len(s.in) <= 2048 {
			for k := 0; k < len(s.in); k++ {
				offs = append(offs, k)
			}
		} else {
			n := 200
			if testing.Short() {
				n = 40
			}
			for k := 0; k < n; k++ {
				offs = append(offs, int(r.Uint64()%uint64(len(s.in))))
			}
			offs = append(offs, len(s.in)-1, len(s.in)-2, len(s.in)-8, len(s.in)-9)
		}
		for j, k := range offs {
			reqs = append(reqs, ldReq{fmt.Sprintf("%s/trunc%d", s.name, k), s.in[:k], s.size + 50, (i+j)%3 == 0})
		}
	}
	diffCheck(t, reqs)
}

// bigFlipStreams get 2000 random single-bit flips (-short: 300); the other
// streams longer than 512 bytes get 20 (-short: 4), the shorter ones every
// bit (-short: every 4th).
var bigFlipStreams = map[string]bool{
	"golden/dynamic-deep-subtables": true, "golden/static-max-offset-32768": true,
	"text/ld6": true, "records/std6": true, "size-text/65536/ld12": true, "dense-matches/ld12": true,
	"segments/ld1": true, "random/ld6": true,
}

func TestDiffBitFlips(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	r := rand.New(rand.NewPCG(3, 4))
	dr := newDiffRunner(t)
	nbig := 0
	for i, s := range diffStreams(t) {
		nbits := len(s.in) * 8
		var flips []int
		randomFlips := func(n int) {
			for k := 0; k < n; k++ {
				flips = append(flips, int(r.Uint64()%uint64(nbits)))
			}
		}
		switch {
		case len(s.in) <= 512 && !testing.Short():
			for b := 0; b < nbits; b++ {
				flips = append(flips, b)
			}
		case len(s.in) <= 512:
			for b := i % 4; b < nbits; b += 4 {
				flips = append(flips, b)
			}
		case bigFlipStreams[s.name] && testing.Short():
			nbig++
			randomFlips(300)
		case bigFlipStreams[s.name]:
			nbig++
			randomFlips(2000)
		case testing.Short():
			randomFlips(4)
		default:
			randomFlips(20)
		}
		for j, b := range flips {
			in := append([]byte(nil), s.in...)
			in[b/8] ^= 1 << (b % 8)
			sizes := [...]int{s.size, s.size + 100, max(s.size-1, 0)}
			dr.add(ldReq{fmt.Sprintf("%s/flip%d", s.name, b), in, sizes[j%3], j%2 == 0})
		}
	}
	if nbig != len(bigFlipStreams) {
		t.Fatalf("found %d of the %d big flip streams", nbig, len(bigFlipStreams))
	}
	dr.done()
}

func TestDiffGarbage(t *testing.T) {
	creftest.LDBin(t)
	r := rand.New(rand.NewPCG(5, 6))
	n := 4000
	if testing.Short() {
		n = 800
	}
	rnd := func(k int) []byte {
		b := make([]byte, k)
		for i := range b {
			b[i] = byte(r.Uint64())
		}
		return b
	}
	sizes := []int{0, 1, 10, 300, 5000, 1 << 16}
	var reqs []ldReq
	for i := 0; i < n; i++ {
		in := rnd(int(r.Uint64() % 600))
		// Bias some toward block types: BTYPE 01 / 10 headers.
		if len(in) > 0 && i%3 != 0 {
			in[0] = in[0]&^6 | byte(1+i%2)<<1
		}
		reqs = append(reqs, ldReq{fmt.Sprintf("garbage%d", i), in, sizes[i%len(sizes)], i%2 == 0})
	}
	// Valid-prefix garbage: a prefix of a valid stream, then random bytes.
	ss := diffStreams(t)
	for i := 0; i < n; i++ {
		s := ss[int(r.Uint64()%uint64(len(ss)))]
		k := int(r.Uint64() % uint64(len(s.in)+1))
		in := append(append([]byte(nil), s.in[:k]...), rnd(int(r.Uint64()%200))...)
		reqs = append(reqs, ldReq{fmt.Sprintf("%s/prefix%d+garbage", s.name, k), in, s.size + int(r.Uint64()%300), i%2 == 1})
	}
	diffCheck(t, reqs)
}

// Silesia files compressed by libdeflate at a few levels, in both modes
// (skipped without the corpus; -short uses the smallest file only).
func TestDiffSilesia(t *testing.T) {
	files := deflatetest.Silesia(t)
	creftest.LDBin(t)
	for _, f := range files {
		if f.Size > 64<<20 || (testing.Short() && f.Name != "xml") {
			continue
		}
		data := f.Load(t)
		var reqs []ldReq
		for _, l := range []int{1, 6, 12} {
			if testing.Short() && l == 12 {
				continue
			}
			comp := creftest.LDCompress(t, data, l, creftest.LDRaw)
			reqs = append(reqs,
				ldReq{fmt.Sprintf("%s/ld%d/exact", f.Name, l), comp, len(data), true},
				ldReq{fmt.Sprintf("%s/ld%d/ex+1", f.Name, l), comp, len(data) + 1, false},
				ldReq{fmt.Sprintf("%s/ld%d/small", f.Name, l), comp, len(data) - 1, false})
		}
		diffCheck(t, reqs)
	}
}
