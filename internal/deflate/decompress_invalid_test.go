package deflate

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Malformed-stream suite (task T1.3). The C programs it ports are
// ref/libdeflate/programs/test_invalid_streams.c and test_incomplete_codes.c;
// the families below extend them to every malformed-header / code-shape
// combination libdeflate distinguishes.
//
// Every case asserts, for the Go decompressor:
//   - the exact result class (nil / BAD_DATA / SHORT_OUTPUT / INSUFFICIENT_SPACE),
//     hard-coded from the C test where the C test has an expectation and from
//     the C source (lib/deflate_decompress.c) otherwise;
//   - actual_in / actual_out / output bytes, which must be identical to
//     libdeflate's (ref/ldref decompress-batch) and, where the C test names
//     them, to the C test's expected values.

// portCase is one decompression with its expected outcome. Successes check
// nIn and nOut when they are >= 0 (the zero value checks for 0) and out when
// it is non-nil.
type portCase struct {
	name  string
	in    []byte
	dst   int  // len(out)
	exact bool // Decompress semantics (actual_out NULL)
	res   libdeflateResult
	// For successes: expected actual_in, actual_out (-1: not checked) and
	// output (nil: not checked).
	nIn, nOut int
	out       []byte
}

// haveLD reports whether the ldref binary exists. Without it the tests still
// assert every hard-coded expectation (the C tests' asserts and the results
// derived from the C source) and skip only the comparison with libdeflate;
// with CI set a missing binary is a failure, as in creftest.
func haveLD(t testing.TB) bool {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	_, err := os.Stat(filepath.Join(filepath.Dir(file), "..", "..", "ref", "ldref", "ldref"))
	if err != nil && os.Getenv("CI") != "" {
		creftest.LDBin(t) // fails with the how-to-build message
	}
	return err == nil
}

var noLDNote sync.Once

// diffCheckIfLD is diffCheck when ldref exists.
func diffCheckIfLD(t *testing.T, reqs []ldReq) {
	t.Helper()
	if haveLD(t) {
		diffCheck(t, reqs)
	} else {
		t.Logf("ldref not built: %d requests not compared with libdeflate", len(reqs))
	}
}

// compressRaw compresses data with libdeflate (ldref) at level, or, without
// ldref, with the standard library (level 6 stands in for any level); the C
// tests that compress only need some valid stream.
func compressRaw(t testing.TB, data []byte, level int) []byte {
	t.Helper()
	if haveLD(t) {
		return creftest.LDCompress(t, data, level, creftest.LDRaw)
	}
	return stdlibCompress(t, data, 6)
}

// checkPort runs every case through Go (internal call and public API, see
// goDecompress) and requires the result to equal the expectation in the case
// (always), and equal libdeflate's result, counts and bytes (when ldref
// exists).
func checkPort(t *testing.T, cases []portCase) {
	t.Helper()
	reqs := make([]ldReq, len(cases))
	for i, c := range cases {
		reqs[i] = ldReq{c.name, c.in, c.dst, c.exact}
	}
	var want []ldRes
	if haveLD(t) {
		want = ldBatch(t, reqs)
	} else {
		noLDNote.Do(func() {
			t.Log("ldref not built: ports are checked against their hard-coded expectations only")
		})
	}
	d := NewDecompressor()
	var buf []byte
	fails := 0
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf(format, args...)
		if fails++; fails > 30 {
			t.Fatal("too many failures")
		}
	}
	for i, c := range cases {
		g := goDecompress(t, d, reqs[i], &buf)
		if want != nil {
			l := want[i]
			if g.res != l.res || g.nIn != l.nIn || g.nOut != l.nOut || !bytes.Equal(g.out, l.out) {
				fail("%s: go result=%d in=%d out=%d, libdeflate result=%d in=%d out=%d (output equal %v)",
					c.name, g.res, g.nIn, g.nOut, l.res, l.nIn, l.nOut, bytes.Equal(g.out, l.out))
				continue
			}
		}
		if g.res != c.res {
			fail("%s: result %d, expected %d", c.name, g.res, c.res)
			continue
		}
		if c.res != libdeflateSuccess {
			if g.nIn != 0 || g.nOut != 0 {
				fail("%s: error result with counts in=%d out=%d", c.name, g.nIn, g.nOut)
			}
			continue
		}
		if c.nIn >= 0 && g.nIn != c.nIn {
			fail("%s: actual_in %d, expected %d", c.name, g.nIn, c.nIn)
		}
		if c.nOut >= 0 && g.nOut != c.nOut {
			fail("%s: actual_out %d, expected %d", c.name, g.nOut, c.nOut)
		}
		if c.out != nil && !bytes.Equal(g.out, c.out) {
			fail("%s: output %x, expected %x", c.name, g.out, c.out)
		}
	}
}

// stdlibInflate decodes in with compress/flate, used (as zlib is in the C
// tests) as a control. Only the first stream is decoded, like libdeflate.
func stdlibInflate(in []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(in))
	defer r.Close()
	return io.ReadAll(r)
}

// ---------------------------------------------------------------------------
// Ports of the C test streams. bitWriter.bits is put_bits, bitWriter.bytes
// is flush_bits. The precode length order is
// [16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15].

// streamTooManyLens is the stream of test_invalid_streams.c:
// test_too_many_codeword_lengths.
func streamTooManyLens() []byte {
	var w bitWriter
	w.bits(1, 1)  // BFINAL: 1
	w.bits(2, 2)  // BTYPE: DYNAMIC_HUFFMAN
	w.bits(0, 5)  // num_litlen_syms: 0 + 257
	w.bits(0, 5)  // num_offset_syms: 0 + 1
	w.bits(14, 4) // num_explicit_precode_lens: 14 + 4
	for i := 0; i < 2; i++ {
		w.bits(0, 3) // presym_{16,17}: len=0
	}
	w.bits(1, 3) // presym_18: len=1
	w.bits(0, 3) // presym_0: len=0
	for i := 0; i < 13; i++ {
		w.bits(0, 3) // presym_{8,...,14}: len=0
	}
	w.bits(1, 3) // presym_1: len=1
	// Litlen and offset codeword lengths.
	w.bits(0x1, 1) // presym_18, 128 zeroes
	w.bits(117, 7)
	w.bits(0x1, 1) // presym_18, 127 zeroes
	w.bits(116, 7)
	w.bits(0x0, 1) // presym_1
	w.bits(0x0, 1) // presym_1
	w.bits(0x1, 1) // presym_18, 128 zeroes [TOO MANY]
	w.bits(117, 7)
	w.bits(0x0, 0) // litlensym_255
	w.bits(0x1, 1) // litlensym_256 (end-of-block)
	return w.bytes()
}

// streamEmptyOffsetCode is test_incomplete_codes.c:test_empty_offset_code;
// it decodes to "ABAA".
func streamEmptyOffsetCode() []byte {
	var w bitWriter
	w.bits(1, 1)
	w.bits(2, 2)
	w.bits(0, 5)
	w.bits(0, 5)
	w.bits(14, 4)
	for i := 0; i < 2; i++ {
		w.bits(0, 3) // presym_{16,17}: len=0
	}
	w.bits(1, 3) // presym_18: len=1
	w.bits(3, 3) // presym_0: len=3
	for i := 0; i < 11; i++ {
		w.bits(0, 3) // presym_{8,...,13}: len=0
	}
	w.bits(2, 3) // presym_2: len=2
	w.bits(0, 3) // presym_14: len=0
	w.bits(3, 3) // presym_1: len=3
	w.bits(0x0, 1)
	w.bits(54, 7)  // presym_18, 65 zeroes
	w.bits(0x7, 3) // presym_1
	w.bits(0x1, 2) // presym_2
	w.bits(0x0, 1)
	w.bits(89, 7) // presym_18, 100 zeroes
	w.bits(0x0, 1)
	w.bits(78, 7)  // presym_18, 89 zeroes
	w.bits(0x1, 2) // presym_2
	w.bits(0x3, 3) // presym_0
	w.bits(0x0, 1) // litlensym_A
	w.bits(0x1, 2) // litlensym_B
	w.bits(0x0, 1) // litlensym_A
	w.bits(0x0, 1) // litlensym_A
	w.bits(0x3, 2) // litlensym_256 (end-of-block)
	return w.bytes()
}

// streamSingletonLitlen is test_incomplete_codes.c:
// test_singleton_litrunlen_code; it decodes to nothing.
func streamSingletonLitlen() []byte {
	var w bitWriter
	w.bits(1, 1)
	w.bits(2, 2)
	w.bits(0, 5)
	w.bits(0, 5)
	w.bits(14, 4)
	for i := 0; i < 2; i++ {
		w.bits(0, 3)
	}
	w.bits(1, 3) // presym_18: len=1
	w.bits(2, 3) // presym_0: len=2
	for i := 0; i < 13; i++ {
		w.bits(0, 3)
	}
	w.bits(2, 3) // presym_1: len=2
	for i := 0; i < 2; i++ {
		w.bits(0, 1) // presym_18, 128 zeroes
		w.bits(117, 7)
	}
	w.bits(0x3, 2) // presym_1
	w.bits(0x1, 2) // presym_0
	w.bits(0x0, 1) // litlensym_256 (end-of-block)
	return w.bytes()
}

// streamSingletonOffset is test_incomplete_codes.c:
// test_singleton_offset_code; it decodes to FF FF FF FF.
func streamSingletonOffset() []byte {
	var w bitWriter
	w.bits(1, 1)
	w.bits(2, 2)
	w.bits(1, 5) // num_litlen_syms: 1 + 257
	w.bits(0, 5) // num_offset_syms: 0 + 1
	w.bits(14, 4)
	for i := 0; i < 2; i++ {
		w.bits(0, 3)
	}
	w.bits(1, 3) // presym_18: len=1
	for i := 0; i < 12; i++ {
		w.bits(0, 3)
	}
	w.bits(2, 3) // presym_2: len=2
	w.bits(0, 3) // presym_14: len=0
	w.bits(2, 3) // presym_1: len=2
	w.bits(0x0, 1)
	w.bits(117, 7) // presym_18, 128 zeroes
	w.bits(0x0, 1)
	w.bits(116, 7) // presym_18, 127 zeroes
	w.bits(0x1, 2) // presym_1
	w.bits(0x3, 2) // presym_2
	w.bits(0x3, 2) // presym_2
	w.bits(0x1, 2) // presym_1
	w.bits(0x0, 1) // litlensym_255
	w.bits(0x3, 2) // litlensym_257
	w.bits(0x0, 1) // offsetsym_0
	w.bits(0x1, 2) // litlensym_256 (end-of-block)
	return w.bytes()
}

// streamSingletonOffsetNotSym0 is test_incomplete_codes.c:
// test_singleton_offset_code_notsymzero; it decodes to FE FF FE FF FE.
func streamSingletonOffsetNotSym0() []byte {
	var w bitWriter
	w.bits(1, 1)
	w.bits(2, 2)
	w.bits(1, 5) // num_litlen_syms: 1 + 257
	w.bits(1, 5) // num_offset_syms: 1 + 1
	w.bits(14, 4)
	for i := 0; i < 2; i++ {
		w.bits(0, 3)
	}
	w.bits(2, 3) // presym_18: len=2
	w.bits(2, 3) // presym_0: len=2
	for i := 0; i < 11; i++ {
		w.bits(0, 3)
	}
	w.bits(2, 3) // presym_2: len=2
	w.bits(0, 3) // presym_14: len=0
	w.bits(2, 3) // presym_1: len=2
	w.bits(0x3, 2)
	w.bits(117, 7) // presym_18, 128 zeroes
	w.bits(0x3, 2)
	w.bits(115, 7) // presym_18, 126 zeroes
	w.bits(0x1, 2) // presym_2
	w.bits(0x1, 2) // presym_2
	w.bits(0x1, 2) // presym_2
	w.bits(0x1, 2) // presym_2
	w.bits(0x0, 2) // presym_0
	w.bits(0x2, 2) // presym_1
	w.bits(0x0, 2) // litlensym_254
	w.bits(0x2, 2) // litlensym_255
	w.bits(0x3, 2) // litlensym_257
	w.bits(0x0, 1) // offsetsym_1
	w.bits(0x1, 2) // litlensym_256 (end-of-block)
	return w.bytes()
}

// port of programs/test_invalid_streams.c:assert_decompression_error and
// test_too_many_codeword_lengths. The C test decodes into out[128] and
// expects LIBDEFLATE_BAD_DATA (and zlib Z_DATA_ERROR as the control).
func TestPortInvalidStreams(t *testing.T) {
	in := streamTooManyLens()
	cases := []portCase{{name: "too-many-codeword-lengths", in: in, dst: 128, res: libdeflateBadData}}
	// The error is detected in the header, before any output: independent of
	// the output buffer size and of the API variant.
	for _, dst := range []int{0, 1, 5, 127, 129, 1 << 16} {
		for _, exact := range []bool{false, true} {
			cases = append(cases, portCase{name: fmt.Sprintf("too-many-codeword-lengths/dst%d/exact%v", dst, exact),
				in: in, dst: dst, exact: exact, res: libdeflateBadData})
		}
	}
	checkPort(t, cases)
	if _, err := stdlibInflate(in); err == nil {
		t.Error("compress/flate (control) accepted the too-many-lengths stream")
	}
	// Control: the stream is only invalid because of the third presym_18; with
	// that item replaced by the one remaining length (258 in total) the
	// block is valid and decodes the literal 255.
	var w bitWriter
	w.bits(1, 1)
	w.bits(2, 2)
	w.bits(0, 5)
	w.bits(0, 5)
	w.bits(14, 4)
	for i := 0; i < 2; i++ {
		w.bits(0, 3)
	}
	w.bits(1, 3)
	w.bits(0, 3)
	for i := 0; i < 13; i++ {
		w.bits(0, 3)
	}
	w.bits(1, 3)
	w.bits(0x1, 1)
	w.bits(117, 7)
	w.bits(0x1, 1)
	w.bits(116, 7)
	w.bits(0x0, 1) // presym_1: litlensym_255, len 1
	w.bits(0x0, 1) // presym_1: litlensym_256, len 1
	w.bits(0x0, 1) // presym_1: offsetsym_0, len 1 (the missing 258th length)
	w.bits(0x0, 1) // litlensym_255 (the C stream writes zero bits here)
	w.bits(0x1, 1) // litlensym_256
	checkPort(t, []portCase{{name: "control-complete", in: w.bytes(), dst: 128, res: libdeflateSuccess,
		nIn: len(w.bytes()), nOut: 1, out: []byte{255}}})
}

// port of programs/test_incomplete_codes.c:verify_decompression and the four
// test_* functions: libdeflate must accept the incomplete codes and produce
// the expected bytes with actual_in == whole stream; zlib (here
// compress/flate) is the control.
func TestPortIncompleteCodes(t *testing.T) {
	tests := []struct {
		name string // C function
		in   []byte
		out  []byte
	}{
		{"test_empty_offset_code", streamEmptyOffsetCode(), []byte{'A', 'B', 'A', 'A'}},
		{"test_singleton_litrunlen_code", streamSingletonLitlen(), []byte{}},
		{"test_singleton_offset_code", streamSingletonOffset(), []byte{255, 255, 255, 255}},
		{"test_singleton_offset_code_notsymzero", streamSingletonOffsetNotSym0(), []byte{254, 255, 254, 255, 254}},
	}
	var cases []portCase
	for _, tc := range tests {
		n := len(tc.out)
		cases = append(cases,
			// C: out[128], libdeflate_deflate_decompress(.., &actual_out).
			portCase{name: tc.name + "/dst128", in: tc.in, dst: 128, res: libdeflateSuccess, nIn: len(tc.in), nOut: n, out: tc.out},
			// Decompress (actual_out NULL): dst must be filled exactly.
			portCase{name: tc.name + "/exact", in: tc.in, dst: n, exact: true, res: libdeflateSuccess, nIn: len(tc.in), nOut: n, out: tc.out},
			portCase{name: tc.name + "/exact-larger", in: tc.in, dst: n + 1, exact: true, res: libdeflateShortOutput},
			// Trailing bytes after the stream are not consumed.
			portCase{name: tc.name + "/trailing", in: append(append([]byte(nil), tc.in...), 0xff, 0xff, 0x00), dst: 128, res: libdeflateSuccess, nIn: len(tc.in), nOut: n, out: tc.out})
		if n > 0 {
			cases = append(cases, portCase{name: tc.name + "/dst-1", in: tc.in, dst: n - 1, res: libdeflateInsufficientSpace})
		}
	}
	checkPort(t, cases)
	for _, tc := range tests {
		got, err := stdlibInflate(tc.in)
		if err != nil || !bytes.Equal(got, tc.out) {
			t.Errorf("%s: compress/flate (control) = %x, %v; want %x", tc.name, got, err, tc.out)
		}
	}
}

// ---------------------------------------------------------------------------
// Code-shape matrix: every combination of litlen-code shape x offset-code
// shape in a dynamic block header. libdeflate (build_decode_table) rejects
// over-subscribed codes and all incomplete codes except the empty code and a
// code with a single codeword of length 1; and it does not require an
// end-of-block codeword, in which case decoding runs into the implicit zero
// bytes until more than 8 bytes were overread (BAD_DATA).

type codeShape struct {
	name     string
	lens     func() []uint8
	accepted bool // build_decode_table succeeds
}

func lensWith(n int, kv ...int) func() []uint8 {
	return func() []uint8 {
		l := make([]uint8, n)
		for i := 0; i < len(kv); i += 2 {
			l[kv[i]] = uint8(kv[i+1])
		}
		return l
	}
}

var litlenShapes = []codeShape{
	{"complete-257", func() []uint8 { return kraftLens(257, seq(0, 257)) }, true},
	{"eob-only-len1", lensWith(257, 256, 1), true},
	{"two-len1", lensWith(257, 'a', 1, 256, 1), true},
	{"a-only-len1-noeob", lensWith(257, 'a', 1), true},
	{"empty-noeob", lensWith(257), true},
	{"eob-only-len2", lensWith(257, 256, 2), false},
	{"eob-only-len15", lensWith(257, 256, 15), false},
	{"oversubscribed-3x-len1", lensWith(257, 'a', 1, 'b', 1, 256, 1), false},
	{"incomplete-3x-len2", lensWith(257, 'a', 2, 'b', 2, 256, 2), false},
	{"incomplete-len1-len2", lensWith(257, 'a', 1, 256, 2), false},
	{"incomplete-len2-len2", lensWith(257, 'a', 2, 256, 2), false},
	{"oversubscribed-len1-len1-len2", lensWith(257, 'a', 1, 'b', 1, 256, 2), false},
}

var offsetShapes = []codeShape{
	{"empty", lensWith(1), true},
	{"empty-30", lensWith(30), true},
	{"single-len1-sym0", lensWith(1, 0, 1), true},
	{"single-len1-sym29", lensWith(30, 29, 1), true},
	{"complete-2x-len1", lensWith(2, 0, 1, 1, 1), true},
	{"complete-30", func() []uint8 { return kraftLens(30, seq(0, 30)) }, true},
	{"single-len2", lensWith(30, 3, 2), false},
	{"single-len15", lensWith(30, 3, 15), false},
	{"oversubscribed-3x-len1", lensWith(3, 0, 1, 1, 1, 2, 1), false},
	{"incomplete-2x-len2", lensWith(30, 0, 2, 1, 2), false},
	{"incomplete-len1-len2", lensWith(30, 0, 1, 1, 2), false},
}

// shapeStream is a final dynamic block with the given code lengths followed
// by the end-of-block codeword (or, when the litlen code has none, 40 zero
// bits).
func shapeStream(ll, ol []uint8) []byte {
	var w bitWriter
	all := append(append([]uint8(nil), ll...), ol...)
	w.dynamicHeader(true, len(ll), len(ol), 19, defaultPrecode(), rleLens(all))
	if ll[256] != 0 {
		w.huff(canonCodes(ll)[256], ll[256])
	} else {
		w.bits(0, 40)
	}
	return w.bytes()
}

func TestInvalidCodeShapes(t *testing.T) {
	var cases []portCase
	nOK := 0
	for _, ls := range litlenShapes {
		for _, os := range offsetShapes {
			ll, ol := ls.lens(), os.lens()
			in := shapeStream(ll, ol)
			res := libdeflateBadData
			nIn, nOut := 0, 0
			if ls.accepted && os.accepted && ll[256] != 0 {
				res, nIn, nOut = libdeflateSuccess, len(in), 0
				nOK++
			}
			name := ls.name + "+" + os.name
			// With dst empty, a stream that decodes literals before it hits
			// the overread limit reports INSUFFICIENT_SPACE instead.
			res0 := res
			if res == libdeflateBadData && ls.accepted && os.accepted {
				res0 = libdeflateInsufficientSpace
			}
			cases = append(cases,
				portCase{name: name, in: in, dst: 1 << 16, res: res, nIn: nIn, nOut: nOut},
				portCase{name: name + "/exact0", in: in, dst: 0, exact: true, res: res0, nIn: nIn, nOut: nOut})
		}
	}
	checkPort(t, cases)
	if nOK != 3*6 {
		t.Errorf("%d accepted combinations, expected 18", nOK)
	}
}

// ---------------------------------------------------------------------------
// Block types, stored blocks, header fields.

func TestInvalidBlockHeaders(t *testing.T) {
	var cases []portCase
	add := func(name string, in []byte, dst int, exact bool, res libdeflateResult, nIn, nOut int, out []byte) {
		cases = append(cases, portCase{name: name, in: in, dst: dst, exact: exact, res: res, nIn: nIn, nOut: nOut, out: out})
	}
	bad := func(name string, in []byte) {
		add(name, in, 1<<16, false, libdeflateBadData, 0, 0, nil)
	}
	var w bitWriter

	// BTYPE 3 (reserved), final or not, first or after valid blocks.
	for _, final := range []uint64{0, 1} {
		w = bitWriter{}
		w.bits(final, 1)
		w.bits(3, 2)
		w.bits(0, 29)
		bad(fmt.Sprintf("btype3/final%d", final), w.bytes())
		w = bitWriter{}
		w.static(false, lits("ab"))
		w.bits(final, 1)
		w.bits(3, 2)
		w.bits(0, 29)
		bad(fmt.Sprintf("btype3-after-static/final%d", final), w.bytes())
		w = bitWriter{}
		w.stored(false, []byte("xyz"))
		w.bits(final, 1)
		w.bits(3, 2)
		w.bits(0, 29)
		bad(fmt.Sprintf("btype3-after-stored/final%d", final), w.bytes())
	}

	// Empty and truncated headers.
	bad("empty", nil)
	for _, b := range []byte{0x00, 0x01, 0x02, 0x04, 0x05, 0x03} {
		bad(fmt.Sprintf("one-byte/%#02x", b), []byte{b})
	}
	// Non-final static block without end of the stream: input ends.
	w = bitWriter{}
	w.static(false, lits("abc"))
	bad("static-nonfinal-then-eof", w.bytes())
	w = bitWriter{}
	w.stored(false, []byte("abc"))
	bad("stored-nonfinal-then-eof", w.bytes())

	// Stored blocks: LEN / NLEN, every single-bit corruption of NLEN and of
	// LEN, for several lengths.
	for _, n := range []int{0, 1, 5, 255, 256, 65535} {
		data := rndBytes(uint64(n)+7, n)
		w = bitWriter{}
		w.stored(true, data)
		good := w.bytes()
		add(fmt.Sprintf("stored/%d/ok", n), good, n, true, libdeflateSuccess, len(good), n, data)
		for bit := 0; bit < 32; bit++ {
			s := append([]byte(nil), good...)
			s[1+bit/8] ^= 1 << (bit % 8)
			bad(fmt.Sprintf("stored/%d/len-nlen-bit%d", n, bit), s)
		}
		// Truncated: header only, one byte short, a quarter.
		for _, k := range []int{1, 2, 3, 4, 5, len(good) - 1, len(good) / 4} {
			if k >= 0 && k < len(good) {
				bad(fmt.Sprintf("stored/%d/trunc%d", n, k), good[:k])
			}
		}
		// dst too small: INSUFFICIENT_SPACE is reported before the check of the
		// remaining input length.
		if n > 0 {
			add(fmt.Sprintf("stored/%d/dst-1", n), good, n-1, false, libdeflateInsufficientSpace, 0, 0, nil)
			add(fmt.Sprintf("stored/%d/dst-1-truncated", n), good[:len(good)-1], n-1, false, libdeflateInsufficientSpace, 0, 0, nil)
		}
		add(fmt.Sprintf("stored/%d/exact-larger", n), good, n+1, true, libdeflateShortOutput, 0, 0, nil)
	}
	// The padding bits before LEN are ignored, whatever they hold.
	for _, pad := range []byte{0x00, 0x08, 0x10, 0x80, 0xf8} {
		in := []byte{0x01 | pad, 0x03, 0x00, 0xfc, 0xff, 'a', 'b', 'c'}
		add(fmt.Sprintf("stored/padding%#02x", pad), in, 10, false, libdeflateSuccess, 8, 3, []byte("abc"))
	}
	// Stored blocks in a row, the last non-final.
	w = bitWriter{}
	w.stored(false, []byte("one"))
	w.stored(false, nil)
	w.stored(true, []byte("three"))
	s := w.bytes()
	add("stored/chain", s, 100, false, libdeflateSuccess, len(s), 8, []byte("onethree"))
	bad("stored/chain-truncated", s[:len(s)-1])

	checkPort(t, cases)
}

// ---------------------------------------------------------------------------
// Dynamic block header fields.

func TestInvalidDynamicHeaders(t *testing.T) {
	var cases []portCase
	add := func(name string, in []byte, res libdeflateResult, out []byte) {
		n := -1
		if res == libdeflateSuccess {
			n = len(in)
		}
		cases = append(cases, portCase{name: name, in: in, dst: 1 << 16, res: res, nIn: n, nOut: len(out), out: out})
	}
	pre := defaultPrecode()
	body := func(w *bitWriter, ll []uint8, toks []tok) {
		// body for a block whose offset code is irrelevant (no matches)
		newTcode(ll, nil).toks(w, toks)
	}

	// HLIT 257..288 and HDIST 1..32 are all accepted (RFC: 286 / 30).
	for _, hlit := range []int{257, 258, 285, 286, 287, 288} {
		for _, hdist := range []int{1, 2, 29, 30, 31, 32} {
			ll := kraftLens(hlit, seq(0, hlit))
			ol := kraftLens(hdist, seq(0, hdist))
			var w bitWriter
			w.dynamicHeader(true, hlit, hdist, 19, pre, rleLens(append(append([]uint8(nil), ll...), ol...)))
			body(&w, ll, lits("hlit"))
			add(fmt.Sprintf("hlit%d/hdist%d", hlit, hdist), w.bytes(), libdeflateSuccess, []byte("hlit"))
		}
	}

	// Precode shapes (19 lengths sent): all zero (an empty code: accepted,
	// decoding then yields only presym 0, i.e. empty litlen and offset codes,
	// i.e. literal 0 forever: BAD_DATA by overread), over-subscribed,
	// incomplete, a lone 1-bit codeword (accepted as a code, every length
	// then equal: over-subscribed -> BAD_DATA).
	for _, tc := range []struct {
		name string
		p    func(p *[19]uint8)
	}{
		{"all-zero", func(p *[19]uint8) {}},
		{"oversubscribed-3x-len1", func(p *[19]uint8) { p[0], p[1], p[2] = 1, 1, 1 }},
		{"incomplete-2x-len2", func(p *[19]uint8) { p[0], p[8] = 2, 2 }},
		{"incomplete-len7", func(p *[19]uint8) { p[0] = 7 }},
		{"single-len1-sym0", func(p *[19]uint8) { p[0] = 1 }},
		{"single-len1-sym18", func(p *[19]uint8) { p[18] = 1 }},
		{"single-len1-sym8", func(p *[19]uint8) { p[8] = 1 }},
		{"single-len2-sym8", func(p *[19]uint8) { p[8] = 2 }},
	} {
		var p [19]uint8
		tc.p(&p)
		var w bitWriter
		w.header(true, 2)
		w.bits(0, 5) // 257
		w.bits(0, 5) // 1
		w.bits(15, 4)
		for i := 0; i < 19; i++ {
			w.bits(uint64(p[deflatePrecodeLensPermutation[i]]), 3)
		}
		w.bits(0, 200)
		add("precode/"+tc.name, w.bytes(), libdeflateBadData, nil)
	}

	// Repeat codes. presym 16 at the first length is invalid; 16 repeating the
	// last litlen length into the offset lengths (the lengths are one array)
	// is valid; a run that ends exactly on the last length is valid and one
	// that overshoots by one is invalid.
	{
		// litlen: 'a'=1, 'b'=2, EOB=2; offset lens = EOB's len x4, via one
		// presym 16 (repeat previous 3 + 1 = 4 times).
		ll := make([]uint8, 257)
		ll['a'], ll['b'], ll[256] = 1, 2, 2
		var w bitWriter
		items := []preItem{{18, 97 - 11}, {1, 0}, {2, 0}, {18, 127}, {18, 19 - 11}, {2, 0}, {16, 1}}
		w.dynamicHeader(true, 257, 4, 19, pre, items)
		ol := []uint8{2, 2, 2, 2}
		newTcode(ll, ol).toks(&w, lits("aba"))
		add("repeat16-across-litlen-offset", w.bytes(), libdeflateSuccess, []byte("aba"))

		// Run of zeros ending exactly on the last length (offset lens
		// 1,1 then 28 zeros via presym 18 with 28 zeroes), and one more.
		for _, extra := range []int{0, 1} {
			ll := kraftLens(257, seq(0, 257))
			items := rleLens(ll)
			items = append(items, preItem{1, 0}, preItem{1, 0}, preItem{18, uint64(28 + extra - 11)})
			var w bitWriter
			w.dynamicHeader(true, 257, 30, 19, pre, items)
			newTcode(ll, []uint8{1, 1}).toks(&w, lits("z"))
			if extra == 0 {
				add("zero-run-ends-exactly", w.bytes(), libdeflateSuccess, []byte("z"))
			} else {
				add("zero-run-overshoots-by-1", w.bytes(), libdeflateBadData, nil)
			}
		}
		// presym 17 / 16 overshooting.
		ll = kraftLens(257, seq(0, 257))
		for _, it := range []preItem{{17, 7}, {16, 3}} {
			items := rleLens(append(ll, 1, 1))
			items = append(items, it)
			var w bitWriter
			w.dynamicHeader(true, 257, 2, 19, pre, items)
			add(fmt.Sprintf("repeat-presym%d-overshoots", it.sym), w.bytes(), libdeflateBadData, nil)
		}
		// presym 16 first, in the litlen part.
		w = bitWriter{}
		w.dynamicHeader(true, 257, 1, 19, pre, []preItem{{16, 3}})
		add("repeat16-first", w.bytes(), libdeflateBadData, nil)
	}
	// Input ends inside the header / the precode / the lengths.
	{
		ll := kraftLens(286, seq(0, 286))
		ol := kraftLens(30, seq(0, 30))
		var w bitWriter
		w.dynamic(true, ll, ol, lits("trunc"))
		s := w.bytes()
		for k := 0; k < len(s); k++ {
			add(fmt.Sprintf("truncated-header/%d", k), s[:k], libdeflateBadData, nil)
		}
	}
	checkPort(t, cases)
}

// ---------------------------------------------------------------------------
// Offsets beyond the start of the output.

func TestInvalidOffsets(t *testing.T) {
	var cases []portCase
	for _, p := range []int{0, 1, 2, 7, 8, 9, 31, 32, 33, 300, 2000} {
		data := rndBytes(uint64(p)+99, p)
		for _, d := range []int{p, p + 1} {
			if d == 0 {
				continue
			}
			toks := append(litsOf(data), match(5, d), lit('!'))
			var w bitWriter
			w.static(true, toks)
			in := w.bytes()
			name := fmt.Sprintf("static/after-%d-literals/offset%d", p, d)
			// Offsets that reach exactly the first byte are valid.
			if d <= p {
				want := tokOutput(nil, toks)
				cases = append(cases,
					portCase{name: name, in: in, dst: len(want) + 64, res: libdeflateSuccess, nIn: len(in), nOut: len(want), out: want},
					portCase{name: name + "/exact", in: in, dst: len(want), exact: true, res: libdeflateSuccess, nIn: len(in), nOut: len(want), out: want})
			} else {
				for _, dst := range []int{p + 6, 1 << 16} {
					cases = append(cases, portCase{name: fmt.Sprintf("%s/dst%d", name, dst), in: in, dst: dst, res: libdeflateBadData})
				}
			}
		}
	}
	// Dynamic block using offset symbols 30 and 31 (invalid per RFC,
	// decodable by libdeflate).
	for _, sym := range []int{30, 31} {
		ll := kraftLens(286, seq(0, 286))
		ol := kraftLens(32, seq(0, 32))
		for _, lits_ := range []int{100, 30000} {
			var w bitWriter
			w.dynamicHeader(true, 286, 32, 19, defaultPrecode(), rleLens(append(append([]uint8(nil), ll...), ol...)))
			c := newTcode(ll, ol)
			for i := 0; i < lits_; i++ {
				c.sym(&w, 'x')
			}
			c.sym(&w, 257)
			w.huff(c.offC[sym], c.off[sym])
			w.bits(0, 13)
			c.sym(&w, 256)
			in := w.bytes()
			name := fmt.Sprintf("dynamic/offset-sym%d/after-%d-literals", sym, lits_)
			// Symbols 30 and 31 both have base 24577 and 13 extra bits in
			// libdeflate's offset table; with extra bits 0 the distance
			// is 24577, within 30000 literals but not within 100.
			if lits_ == 30000 {
				want := bytes.Repeat([]byte{'x'}, lits_+3)
				cases = append(cases, portCase{name: name, in: in, dst: 1 << 16, res: libdeflateSuccess, nIn: len(in), nOut: lits_ + 3, out: want})
			} else {
				cases = append(cases, portCase{name: name, in: in, dst: 1 << 16, res: libdeflateBadData})
			}
		}
	}
	// A match in the second block reaching into the first (valid), and a
	// match reaching beyond both (invalid).
	for _, d := range []int{3, 9, 10} {
		var w bitWriter
		w.static(false, lits("abcde"))
		w.stored(false, []byte("fghi"))
		toks := []tok{match(3, d), lit('.')}
		w.static(true, toks)
		in := w.bytes()
		if d <= 9 {
			want := tokOutput([]byte("abcdefghi"), toks)
			cases = append(cases, portCase{name: fmt.Sprintf("across-blocks/offset%d", d), in: in, dst: 100, res: libdeflateSuccess, nIn: len(in), nOut: len(want), out: want})
		} else {
			cases = append(cases, portCase{name: fmt.Sprintf("across-blocks/offset%d", d), in: in, dst: 100, res: libdeflateBadData})
		}
	}
	checkPort(t, cases)
}

// ---------------------------------------------------------------------------
// Truncated everywhere: every prefix of each hand-made stream.

func TestInvalidTruncatedEverywhere(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	type full struct {
		name string
		in   []byte
		size int
	}
	var streams []full
	for _, g := range goldenCases() {
		if len(g.in) <= 3000 {
			streams = append(streams, full{"golden/" + g.name, g.in, len(g.out) + 50})
		}
	}
	for i, s := range [][]byte{streamTooManyLens(), streamEmptyOffsetCode(), streamSingletonLitlen(), streamSingletonOffset(), streamSingletonOffsetNotSym0()} {
		streams = append(streams, full{fmt.Sprintf("port%d", i), s, 200})
	}
	dr := newDiffRunner(t)
	for _, s := range streams {
		for k := 0; k < len(s.in); k++ {
			for _, exact := range []bool{false, true} {
				dr.add(ldReq{fmt.Sprintf("%s/prefix%d/exact%v", s.name, k, exact), s.in[:k], s.size, exact})
			}
		}
	}
	dr.done()
}
