package deflate

import (
	"bytes"
	"compress/flate"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// Stress suite (task T1.3): trailing bytes, slow-decompression streams,
// literal-run overflow and the systematic truncation / mutation generators.
// Ports of programs/test_trailing_bytes.c, test_slow_decompression.c and
// test_litrunlen_overflow.c (decode side). Everything is compared with
// libdeflate (ref/ldref).

// ---------------------------------------------------------------------------
// test_trailing_bytes.c

// port of programs/test_trailing_bytes.c:tmain, for the DEFLATE codec. The
// zlib and gzip codecs of the C test belong to the container layers
// (later tasks of the plan), which do not exist yet.
func TestPortTrailingBytes(t *testing.T) {
	const originalNbytes = 32768
	const compressedNbytesTotal = 32768
	const compressedNbytesAvail = 30000
	original := make([]byte, originalNbytes)
	for i := range original {
		original[i] = byte(i%123 + i%1023)
	}
	comp := compressRaw(t, original, 6)
	if len(comp) == 0 || len(comp) > compressedNbytesAvail {
		t.Fatalf("compressed size %d, the C test needs 1..%d", len(comp), compressedNbytesAvail)
	}
	// "compressed" in C: the stream, then zeros up to compressed_nbytes_total.
	padded := make([]byte, compressedNbytesTotal)
	copy(padded, comp)
	n := len(comp)
	checkPort(t, []portCase{
		// decompress() of the stream that fills the whole buffer
		{name: "decompress", in: comp, dst: originalNbytes, exact: true, res: libdeflateSuccess, nIn: n, nOut: originalNbytes, out: original},
		// decompress_ex()
		{name: "decompress_ex", in: comp, dst: originalNbytes, res: libdeflateSuccess, nIn: n, nOut: originalNbytes, out: original},
		// decompress() of the stream with trailing bytes
		{name: "decompress/trailing", in: padded, dst: originalNbytes, exact: true, res: libdeflateSuccess, nIn: n, nOut: originalNbytes, out: original},
		// decompress_ex() of the stream with trailing bytes
		{name: "decompress_ex/trailing", in: padded, dst: originalNbytes, res: libdeflateSuccess, nIn: n, nOut: originalNbytes, out: original},
	})
}

// TestTrailingBytesAll: streams of every block kind followed by 0..32 bytes
// of every kind of garbage: actual_in is the end of the stream, exactly as
// libdeflate reports it, whatever follows.
func TestTrailingBytesAll(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	type stream struct {
		name string
		in   []byte
		nIn  int
		out  []byte
	}
	var ss []stream
	for _, g := range goldenCases() {
		if g.res == libdeflateSuccess {
			ss = append(ss, stream{"golden/" + g.name, g.in[:g.nIn], g.nIn, g.out})
		}
	}
	pick := map[string]bool{"text": true, "random": true, "zero": true, "records": true, "sweep-text/37": true,
		"size-text/513": true, "size-rand/65536": true, "litrun-stride251": true, "dense-matches": true, "sweep-rand/3": true}
	for _, c := range deflatetest.Cases(true) {
		if !pick[c.Name] {
			continue
		}
		for _, l := range []int{0, 1, 6, 12} {
			comp := compressRaw(t, c.Data, l)
			ss = append(ss, stream{fmt.Sprintf("%s/ld%d", c.Name, l), comp, len(comp), c.Data})
		}
		ss = append(ss, stream{c.Name + "/std6", stdlibCompress(t, c.Data, 6), -1, c.Data})
	}
	r := rand.New(rand.NewPCG(21, 22))
	patterns := []struct {
		name string
		gen  func(n int) []byte
	}{
		{"zeros", func(n int) []byte { return make([]byte, n) }},
		{"0xff", func(n int) []byte { return bytes.Repeat([]byte{0xff}, n) }},
		{"random", func(n int) []byte {
			b := make([]byte, n)
			for i := range b {
				b[i] = byte(r.Uint64())
			}
			return b
		}},
		// Looks like the start of a further block: a non-final stored header
		// with a huge LEN, a final static block header, a final dynamic one.
		{"stored-hdr", func(n int) []byte { return append([]byte{0x00, 0xff, 0xff, 0x00, 0x00}, make([]byte, n)...)[:n] }},
		{"static-hdr", func(n int) []byte { return append([]byte{0x03}, make([]byte, n)...)[:n] }},
		{"dynamic-hdr", func(n int) []byte { return append([]byte{0x05, 0xff, 0xff}, make([]byte, n)...)[:n] }},
	}
	// Full runs use all 6 patterns and every length 0..32; -short uses 3
	// patterns (zeros, random, dynamic-hdr) and nine lengths.
	lengths := make([]int, 33)
	for k := range lengths {
		lengths[k] = k
	}
	if testing.Short() {
		lengths = []int{0, 1, 2, 7, 8, 9, 16, 17, 32}
		patterns = []struct {
			name string
			gen  func(n int) []byte
		}{patterns[0], patterns[2], patterns[5]}
	}
	for _, s := range ss {
		var cases []portCase
		nIn := s.nIn
		if nIn < 0 {
			nIn = len(s.in) // the stdlib stream ends with its final block
		}
		for _, p := range patterns {
			for _, k := range lengths {
				in := append(append([]byte(nil), s.in...), p.gen(k)...)
				name := fmt.Sprintf("%s/%s+%d", s.name, p.name, k)
				cases = append(cases,
					portCase{name: name, in: in, dst: len(s.out) + 7, res: libdeflateSuccess, nIn: nIn, nOut: len(s.out), out: s.out},
					portCase{name: name + "/exact", in: in, dst: len(s.out), exact: true, res: libdeflateSuccess, nIn: nIn, nOut: len(s.out), out: s.out})
			}
		}
		checkPort(t, cases)
	}
	t.Logf("%d streams x %d patterns x %d lengths", len(ss), len(patterns), len(lengths))
}

// ---------------------------------------------------------------------------
// test_slow_decompression.c

// outBits is the C test's struct output_bitstream with a bounded buffer.
type outBits struct {
	bitbuf   uint64
	bitcount int
	next     []byte
	capacity int
}

// port of programs/test_util.c:put_bits.
func (os *outBits) put(bits uint64, n int) bool {
	os.bitbuf |= bits << os.bitcount
	os.bitcount += n
	for os.bitcount >= 8 {
		if len(os.next) == os.capacity {
			return false
		}
		os.next = append(os.next, byte(os.bitbuf))
		os.bitcount -= 8
		os.bitbuf >>= 8
	}
	return true
}

// port of programs/test_slow_decompression.c:generate_empty_static_huffman_blocks.
func slowEmptyStaticBlocks(n int) []byte {
	os := &outBits{capacity: n}
	for os.put(0, 1) && // BFINAL: 0
		os.put(1, 2) && // BTYPE: STATIC_HUFFMAN
		os.put(0, 7) { // litlensym_256 (end-of-block)
	}
	return os.next
}

// port of programs/test_slow_decompression.c:generate_empty_dynamic_huffman_block.
func slowEmptyDynamicBlock(os *outBits) bool {
	if !os.put(0, 1) || !os.put(2, 2) { // BFINAL: 0; BTYPE: DYNAMIC_HUFFMAN
		return false
	}
	// Litlen code: litlensym_256 len=1 codeword=0. Offset code:
	// offsetsym_0 (unused) len=1 codeword=0. Precode: presym_1 len=1
	// codeword=0, presym_18 len=1 codeword=1.
	if !os.put(0, 5) || !os.put(0, 5) || !os.put(14, 4) {
		return false
	}
	for i := 0; i < 2; i++ { // presym_{16,17}: len=0
		if !os.put(0, 3) {
			return false
		}
	}
	if !os.put(1, 3) { // presym_18: len=1
		return false
	}
	for i := 0; i < 14; i++ { // presym_{0,...,14}: len=0
		if !os.put(0, 3) {
			return false
		}
	}
	if !os.put(1, 3) { // presym_1: len=1
		return false
	}
	for i := 0; i < 2; i++ { // presym_18, 128 zeroes
		if !os.put(1, 1) || !os.put(117, 7) {
			return false
		}
	}
	if !os.put(0, 1) || !os.put(0, 1) { // presym_1, presym_1
		return false
	}
	return os.put(0, 1) // litlensym_256 (end-of-block)
}

// port of programs/test_slow_decompression.c:generate_empty_dynamic_huffman_blocks.
func slowEmptyDynamicBlocks(n int) []byte {
	os := &outBits{capacity: n}
	for slowEmptyDynamicBlock(os) {
	}
	return os.next
}

// slowRepro is orig_repro of test_slow_decompression.c.
func slowRepro(t testing.TB) []byte {
	b, err := hex.DecodeString(slowReproHex)
	if err != nil || len(b) != 3962 {
		t.Fatalf("slowReproHex: %d bytes, %v", len(b), err)
	}
	return b
}

// slowCase is one pathological stream.
type slowCase struct {
	name string
	in   []byte
	dst  int
	// res is the expected result with a dst of dst bytes.
	res libdeflateResult
}

func slowCases(t *testing.T) []slowCase {
	r := rand.New(rand.NewPCG(31, 32))
	cs := []slowCase{
		// The three streams of the C test, decoded into out[10000]; it
		// asserts BAD_DATA or INSUFFICIENT_SPACE for them. The static and
		// dynamic ones are 4096 bytes of non-final empty blocks and end in
		// the middle of input: BAD_DATA; the original repro too.
		{"C/static-empty-blocks", slowEmptyStaticBlocks(4096), 10000, libdeflateBadData},
		{"C/dynamic-empty-blocks", slowEmptyDynamicBlocks(4096), 10000, libdeflateBadData},
		{"C/original-repro", slowRepro(t), 10000, -1},
		// Bigger variants of the same, to separate decoder time from
		// call overhead.
		{"static-empty-blocks/256K", slowEmptyStaticBlocks(256 << 10), 10000, libdeflateBadData},
		{"dynamic-empty-blocks/256K", slowEmptyDynamicBlocks(256 << 10), 10000, libdeflateBadData},
		{"stored-empty-blocks/256K", bytes.Repeat([]byte{0, 0, 0, 0xff, 0xff}, 50000), 10000, libdeflateBadData},
	}
	// Alternating static / dynamic / stored empty blocks.
	{
		var w bitWriter
		for i := 0; i < 20000; i++ {
			switch i % 3 {
			case 0:
				w.static(false, nil)
			case 1:
				w.dynamic(false, kraftLens(257, []int{256}), make([]uint8, 1), nil)
			default:
				w.stored(false, nil)
			}
		}
		cs = append(cs, slowCase{"alternating-empty-blocks", w.bytes(), 10000, libdeflateBadData})
	}
	// A valid stream of many small dynamic blocks, each with its own codes
	// (a table build per block, some with subtables), ending in a final block.
	{
		var w bitWriter
		var want []byte
		const nb = 600
		for i := 0; i < nb; i++ {
			nsym := 3 + int(r.Uint64()%283)
			used := []int{256}
			for len(used) < nsym {
				used = append(used, int(r.Uint64()%256))
			}
			seen := map[int]bool{}
			var u []int
			for _, s := range used {
				if !seen[s] {
					seen[s] = true
					u = append(u, s)
				}
			}
			var ll []uint8
			if i%2 == 0 {
				ll = kraftLens(286, u)
			} else if len(u) > 16 {
				ll = chainLens(286, u[:16])
				u = u[:16]
			} else {
				ll = chainLens(286, u)
			}
			ol := kraftLens(30, seq(0, 1+int(r.Uint64()%29)))
			var lt []tok
			for j := 0; j < 5; j++ {
				s := u[int(r.Uint64()%uint64(len(u)))]
				if s < 256 {
					lt = append(lt, lit(byte(s)))
				}
			}
			w.dynamic(i == nb-1, ll, ol, lt)
			want = tokOutput(want, lt)
		}
		in := w.bytes()
		cs = append(cs, slowCase{fmt.Sprintf("600-dynamic-blocks/%dB", len(in)), in, len(want), libdeflateSuccess})
	}
	// Long literal / match mixes: one static block, about 100k tokens, three
	// literals then a match of random length at a random distance (a quarter
	// of them at distance 1..8), a few MB of output.
	{
		var toks []tok
		size := 0
		for len(toks) < 100000 {
			for k := 0; k < 3; k++ {
				toks = append(toks, lit(byte(r.Uint64())))
				size++
			}
			if size < 8 {
				continue
			}
			l := 3 + int(r.Uint64()%256)
			d := 1 + int(r.Uint64()%uint64(min(size, 32768)))
			if r.Uint64()%4 == 0 {
				d = min(1+int(r.Uint64()%8), size)
			}
			toks = append(toks, match(l, d))
			size += l
		}
		var w bitWriter
		w.static(true, toks)
		cs = append(cs, slowCase{"literal-match-mix/static", w.bytes(), size, libdeflateSuccess})
	}
	// One long run: a dynamic block of 4 literals then 60000 matches of length
	// 258 at distances 1..4 (about 15 MB of output).
	{
		toks := lits("abcd")
		size := 4
		for i := 0; i < 60000; i++ {
			toks = append(toks, match(258, 1+i%4))
			size += 258
		}
		var w bitWriter
		w.dynamic(true, kraftLens(286, seq(0, 286)), kraftLens(30, seq(0, 30)), toks)
		cs = append(cs, slowCase{"long-matches/dynamic", w.bytes(), size, libdeflateSuccess})
	}
	return cs
}

// TestSlowStreamsCorrect: Go == libdeflate (result, counts, bytes) on every
// pathological stream, with the C test's out[10000], an exact-size and an
// oversized buffer, in both API modes; the results the C test asserts are
// asserted here.
func TestSlowStreamsCorrect(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	var reqs []ldReq
	for _, c := range slowCases(t) {
		for _, dst := range []int{c.dst, c.dst + 1, 1 << 16, 1 << 20} {
			if dst > 32<<20 {
				continue
			}
			for _, exact := range []bool{false, true} {
				reqs = append(reqs, ldReq{fmt.Sprintf("%s/dst%d/exact%v", c.name, dst, exact), c.in, dst, exact})
			}
		}
	}
	diffCheckIfLD(t, reqs)
	// The C test's assertions (decompress with out[10000], actual_out NULL).
	d := NewDecompressor()
	var buf []byte
	for _, c := range slowCases(t) {
		o := decodeOutcome(t, d, c.name, c.in, c.dst, true, &buf)
		switch {
		case c.name[:2] == "C/":
			if o.res != libdeflateBadData && o.res != libdeflateInsufficientSpace {
				t.Errorf("%s: result %d, the C test asserts BAD_DATA or INSUFFICIENT_SPACE", c.name, o.res)
			}
			if c.res >= 0 && o.res != c.res {
				t.Errorf("%s: result %d, expected %d", c.name, o.res, c.res)
			}
		case c.res >= 0:
			// (success cases use exact dst: Decompress succeeds only if the
			// stream fills the buffer exactly)
			if o.res != c.res {
				t.Errorf("%s: result %d, expected %d", c.name, o.res, c.res)
			}
		}
	}
}

// bestOf runs f (which performs n decompressions) a few times and returns
// the best time per decompression.
func bestOf(n int, f func()) time.Duration {
	best := time.Duration(1 << 62)
	for i := 0; i < 5; i++ {
		start := time.Now()
		f()
		if e := time.Since(start); e < best {
			best = e
		}
	}
	return best / time.Duration(n)
}

// TestSlowStreamsTiming: wall-clock sanity bound for the degenerate
// streams, skipped under -short and -race. Go may be at most 20x slower than
// libdeflate per decompression (ldref time measured through
// decompress-batch of the same records, process start included, so the bound
// is lenient), with an absolute cap of 2 s per decompression. This is not a
// benchmark (T1.4); the observed ratios are logged. The relative asserts of
// the C test are ported with compress/flate (black box) as the "zlib" stand-in:
// the static-block stream and orig_repro must be faster than it and the
// dynamic-block stream at most 4x slower (observed ratios: about 0.25, 0.28 and
// 1.3-1.6). These need no ldref.
func TestSlowStreamsTiming(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing bound: skipped under -short and -race")
	}
	ld := haveLD(t)
	d := NewDecompressor()
	for _, c := range slowCases(t) {
		dstSize := c.dst
		iters := 100 // NUM_ITERATIONS in the C test
		if len(c.in) > 64<<10 || dstSize > 1<<20 {
			iters = 5
		}
		dst := make([]byte, dstSize)
		goPer := bestOf(iters, func() {
			for i := 0; i < iters; i++ {
				d.decompressImpl(c.in, dst, true, true)
			}
		})
		var ldPer time.Duration
		if ld {
			reqs := make([]ldReq, iters)
			for i := range reqs {
				reqs[i] = ldReq{c.name, c.in, dstSize, false}
			}
			ldBatch(t, reqs[:1]) // warm the binary
			ldPer = bestOf(iters, func() { ldBatch(t, reqs) })
		}
		stdPer := bestOf(iters, func() {
			for i := 0; i < iters; i++ {
				r := flate.NewReader(bytes.NewReader(c.in))
				buf := make([]byte, 4096)
				for {
					if _, err := r.Read(buf); err != nil {
						break
					}
				}
			}
		})
		t.Logf("%-34s in %7d B dst %8d: go %10v  libdeflate(ldref) %10v  stdlib %10v  go/ld %5.2f  go/std %5.2f  go %8.0f KB/s",
			c.name, len(c.in), dstSize, goPer, ldPer, stdPer, float64(goPer)/float64(ldPer), float64(goPer)/float64(stdPer),
			float64(len(c.in))/1024/goPer.Seconds())
		switch c.name {
		case "C/static-empty-blocks", "C/original-repro": // C: ASSERT(t < tz)
			if goPer >= stdPer {
				t.Errorf("%s: go %v per decompression, not faster than compress/flate %v", c.name, goPer, stdPer)
			}
		case "C/dynamic-empty-blocks": // C: ASSERT(t < 4 * tz)
			if goPer >= 4*stdPer {
				t.Errorf("%s: go %v per decompression, 4x or more slower than compress/flate %v", c.name, goPer, stdPer)
			}
		}
		if ld && goPer > 20*ldPer+time.Millisecond {
			t.Errorf("%s: go %v per decompression, libdeflate %v: more than 20x", c.name, goPer, ldPer)
		}
		if goPer > 2*time.Second {
			t.Errorf("%s: go %v per decompression, over the 2 s cap", c.name, goPer)
		}
	}
}

// ---------------------------------------------------------------------------
// test_litrunlen_overflow.c

// litrunData is the pattern of programs/test_litrunlen_overflow.c repeated
// reps times (reps = 2 is its data_size = 2 * 250 * 251): no 2-byte sequence
// repeats within 32768 bytes, only 251 symbols are used.
func litrunData(reps int) []byte {
	b := make([]byte, 0, reps*250*251)
	for i := 0; i < reps; i++ {
		for stride := 1; stride < 251; stride++ {
			for multiple := 0; multiple < 251; multiple++ {
				b = append(b, byte(stride*multiple%251))
			}
		}
	}
	return b
}

// port of programs/test_litrunlen_overflow.c:tmain, decode side. The C test
// compresses the data at levels 3, 6 and 12 (csize > 0 && csize < data_size)
// and decompresses it with libdeflate_deflate_decompress(d, .., data_size,
// NULL), requiring SUCCESS and identical bytes. The compressed streams come
// from ldref (libdeflate's compressor); the Go decoder decodes them.
func TestPortLitrunlenOverflow(t *testing.T) {
	for _, reps := range []int{1, 2, 3} {
		data := litrunData(reps)
		if reps == 2 && len(data) != 2*250*251 {
			t.Fatalf("data_size %d", len(data))
		}
		var cases []portCase
		for _, level := range []int{3, 6, 12} {
			comp := compressRaw(t, data, level)
			// csize < data_size is a property of libdeflate's compressor (the
			// standard-library fallback does not compress this data).
			if len(comp) == 0 || (haveLD(t) && len(comp) >= len(data)) {
				t.Fatalf("reps %d level %d: csize %d, data_size %d", reps, level, len(comp), len(data))
			}
			n := len(comp)
			name := fmt.Sprintf("reps%d/level%d", reps, level)
			cases = append(cases,
				portCase{name: name, in: comp, dst: len(data), exact: true, res: libdeflateSuccess, nIn: n, nOut: len(data), out: data},
				portCase{name: name + "/ex", in: comp, dst: len(data) + 1, res: libdeflateSuccess, nIn: n, nOut: len(data), out: data},
				portCase{name: name + "/trailing", in: append(append([]byte(nil), comp...), 1, 2, 3), dst: len(data), exact: true, res: libdeflateSuccess, nIn: n, nOut: len(data), out: data},
				portCase{name: name + "/short", in: comp, dst: len(data) - 1, res: libdeflateInsufficientSpace},
				portCase{name: name + "/exact-larger", in: comp, dst: len(data) + 1, exact: true, res: libdeflateShortOutput})
			// And the same in the corpus' litrun-stride251 case.
		}
		checkPort(t, cases)
	}
	// The corpus case is the same pattern.
	for _, c := range deflatetest.Cases(true) {
		if c.Name == "litrun-stride251" && !bytes.Equal(c.Data, litrunData(2)) {
			t.Error("litrun-stride251 differs from the C test's pattern")
		}
	}
}

// TestLitrunlenRuns: decode-side literal runs of 65535 / 65536 / 65537 and
// more literals in a single block (static and dynamic), the run lengths a
// 16-bit literal-run counter would overflow on, alone, followed by a match,
// and several blocks in a row.
func TestLitrunlenRuns(t *testing.T) {
	var cases []portCase
	for _, n := range []int{255, 256, 257, 65534, 65535, 65536, 65537, 70000, 131071, 131072, 200000, 300000} {
		data := rndBytes(uint64(n), n)
		toks := litsOf(data)
		withMatch := append(append([]tok(nil), toks...), match(258, 1), match(3, min(n, 32768)), lit('x'))
		for _, kind := range []string{"static", "dynamic", "dynamic-skew"} {
			for _, tt := range []struct {
				name string
				toks []tok
			}{{"lits", toks}, {"lits+match", withMatch}} {
				var w bitWriter
				switch kind {
				case "static":
					w.static(true, tt.toks)
				case "dynamic":
					w.dynamic(true, kraftLens(286, seq(0, 286)), kraftLens(30, seq(0, 30)), tt.toks)
				default:
					// Skewed code: 12 literals, the end of block and the
					// symbols for lengths 3 and 258, with codewords of 1 to
					// 14 bits.
					w.dynamic(true, chainLens(286, append(seq(0, 12), 256, 257, 285)),
						kraftLens(30, seq(0, 30)), tokenFilter(tt.toks, 12))
				}
				in := w.bytes()
				want := tokOutput(nil, tt.toks)
				if kind == "dynamic-skew" {
					want = tokOutput(nil, tokenFilter(tt.toks, 12))
				}
				name := fmt.Sprintf("%s/%s/%d", kind, tt.name, n)
				cases = append(cases,
					portCase{name: name, in: in, dst: len(want), exact: true, res: libdeflateSuccess, nIn: len(in), nOut: len(want), out: want},
					portCase{name: name + "/ex", in: in, dst: len(want) + 1000, res: libdeflateSuccess, nIn: len(in), nOut: len(want), out: want})
			}
		}
	}
	// Several blocks, each a 65536-literal run.
	{
		var w bitWriter
		var want []byte
		for i := 0; i < 4; i++ {
			toks := litsOf(rndBytes(uint64(100+i), 65536))
			w.dynamic(i == 3, kraftLens(286, seq(0, 286)), kraftLens(30, seq(0, 30)), toks)
			want = tokOutput(want, toks)
		}
		in := w.bytes()
		cases = append(cases, portCase{name: "4-blocks-of-65536-literals", in: in, dst: len(want), exact: true, res: libdeflateSuccess, nIn: len(in), nOut: len(want), out: want})
	}
	checkPort(t, cases)
}

// tokenFilter maps literals to the alphabet 0..m-1 and leaves matches of
// length 3 or 258 (the skewed code only has codewords for those).
func tokenFilter(toks []tok, m int) []tok {
	out := make([]tok, len(toks))
	for i, t := range toks {
		if t.dist == 0 {
			t.lit %= byte(m)
		} else if t.len != 258 {
			t.len = 3
		}
		out[i] = t
	}
	return out
}

// ---------------------------------------------------------------------------
// Systematic generators.

// stressStreams: every corpus case x libdeflate levels 1, 6, 12 and
// standard-library levels (-2, 1, 6, 9, 0), plus the golden streams.
func stressStreams(t *testing.T) []diffStream {
	var ss []diffStream
	for _, g := range goldenCases() {
		size := len(g.out)
		if g.res != libdeflateSuccess {
			size = 1000
		}
		ss = append(ss, diffStream{"golden/" + g.name, g.in, size})
	}
	ldLevels := []int{1, 6, 12}
	stdLevels := []int{-2, 0, 1, 6, 9}
	step := 1
	if testing.Short() {
		ldLevels, stdLevels, step = []int{6}, []int{1}, 5
	}
	for i, c := range deflatetest.Cases(true) {
		if i%step != 0 {
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

// TestStressTruncations: for every stream, every truncation point when the
// stream is at most 2048 bytes (300 random ones otherwise, -short: 60), and
// for the streams of at most 256 bytes also every single-byte deletion.
// Go == libdeflate on result, counts and bytes; nothing panics.
func TestStressTruncations(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	r := rand.New(rand.NewPCG(41, 42))
	nBig := 300
	if testing.Short() {
		nBig = 60
	}
	dr := newDiffRunner(t)
	d := NewDecompressor()
	var buf []byte
	nTrunc, nDel := 0, 0
	for si, s := range stressStreams(t) {
		var ks []int
		if len(s.in) <= 2048 {
			for k := 0; k < len(s.in); k++ {
				ks = append(ks, k)
			}
		} else {
			for i := 0; i < nBig; i++ {
				ks = append(ks, int(r.Uint64()%uint64(len(s.in))))
			}
			ks = append(ks, len(s.in)-1, len(s.in)-2, len(s.in)-8, len(s.in)-9)
		}
		add := func(name string, in []byte, j int) {
			exact := (si+j)%3 == 0
			dst := s.size + 64
			if exact {
				dst = s.size
			}
			decodeOutcome(t, d, name, in, dst, exact, &buf) // no panic, counts in range
			dr.add(ldReq{name, in, dst, exact})
		}
		for j, k := range ks {
			add(fmt.Sprintf("%s/trunc%d", s.name, k), s.in[:k:k], j)
			nTrunc++
		}
		if len(s.in) <= 256 {
			for j := range s.in {
				in := append(append([]byte(nil), s.in[:j]...), s.in[j+1:]...)
				add(fmt.Sprintf("%s/del%d", s.name, j), in, j)
				nDel++
			}
		}
	}
	dr.done()
	t.Logf("%d truncations, %d single-byte deletions", nTrunc, nDel)
}

// mutate applies one random structured mutation to a copy of s (other is a
// second stream for splices).
func mutate(r *rand.Rand, s, other []byte) []byte {
	b := append([]byte(nil), s...)
	if len(b) == 0 {
		return append(b, byte(r.Uint64()))
	}
	pos := func() int { return int(r.Uint64() % uint64(len(b))) }
	switch r.Uint64() % 13 {
	case 0: // bit flip anywhere
		b[pos()] ^= 1 << (r.Uint64() % 8)
	case 1: // bit flip in the block header region
		b[int(r.Uint64()%uint64(min(len(b), 8)))] ^= 1 << (r.Uint64() % 8)
	case 2: // random byte
		b[pos()] = byte(r.Uint64())
	case 3: // 0x00 / 0xff
		b[pos()] = byte(0 - r.Uint64()%2)
	case 4: // delete a byte
		p := pos()
		b = append(b[:p], b[p+1:]...)
	case 5: // insert a byte
		p := pos()
		b = append(b[:p], append([]byte{byte(r.Uint64())}, b[p:]...)...)
	case 6: // duplicate a chunk
		p := pos()
		n := min(1+int(r.Uint64()%16), len(b)-p)
		b = append(b[:p+n], append(append([]byte(nil), b[p:p+n]...), b[p+n:]...)...)
	case 7: // swap two bytes
		i, j := pos(), pos()
		b[i], b[j] = b[j], b[i]
	case 8: // splice the tail of another stream
		p := pos()
		if len(other) > 0 {
			b = append(b[:p], other[int(r.Uint64()%uint64(len(other))):]...)
		}
	case 9: // truncate and append garbage
		b = b[:pos()]
		for n := int(r.Uint64() % 24); n > 0; n-- {
			b = append(b, byte(r.Uint64()))
		}
	case 10: // zero / ones run
		p := pos()
		v := byte(0 - r.Uint64()%2)
		for i := p; i < min(len(b), p+1+int(r.Uint64()%12)); i++ {
			b[i] = v
		}
	case 11: // dynamic header counts (HLIT, HDIST, HCLEN) to extremes
		if len(b) >= 3 {
			b[0] = b[0]&0x07 | byte(r.Uint64()&0xf8)
			b[1] = byte(r.Uint64())
			b[2] ^= byte(r.Uint64() & 0x1f)
		}
	default: // 2..4 stacked bit flips
		for n := 2 + int(r.Uint64()%3); n > 0; n-- {
			b[pos()] ^= 1 << (r.Uint64() % 8)
		}
	}
	return b
}

// TestStressMutations: 100000 random structured mutations of valid and
// invalid streams (-short: 15000). The decoder must not panic and must keep
// its counts in range (decodeOutcome), and Go == libdeflate on every one.
func TestStressMutations(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	n := 100000
	if testing.Short() {
		n = 15000
	}
	r := rand.New(rand.NewPCG(51, 52))
	var seeds []diffStream
	for _, s := range overreadStreams(t) {
		if len(s.in) <= 8192 {
			seeds = append(seeds, s)
		}
	}
	dr := newDiffRunner(t)
	d := NewDecompressor()
	var buf []byte
	for i := 0; i < n; i++ {
		s := seeds[int(r.Uint64()%uint64(len(seeds)))]
		o := seeds[int(r.Uint64()%uint64(len(seeds)))]
		b := mutate(r, s.in, o.in)
		for k := r.Uint64() % 3; k > 0; k-- { // stacked mutations
			b = mutate(r, b, o.in)
		}
		sizes := [...]int{s.size, s.size + 100, 0, 1 << 14, max(s.size-1, 0)}
		dst := sizes[i%len(sizes)]
		exact := i%2 == 0
		name := fmt.Sprintf("%s/mut%d", s.name, i)
		decodeOutcome(t, d, name, b, dst, exact, &buf)
		dr.add(ldReq{name, b, dst, exact})
	}
	dr.done()
}
