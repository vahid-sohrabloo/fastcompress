package flate_test

// This file is shared: flate/reader_test.go, gzip/reader_test.go and
// zlib/reader_test.go are identical apart from the package clause and the
// import path of the package under test (pkg). The format-specific parts
// (newReader, stdNewReader, resetReader, stdResetReader, singleStream,
// wholeComparable, bodyStart, trailerSize, trailingIgnored) are in each package's reader_format_test.go.
// TestReaderSharedFileInSync checks the copies.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	pkg "github.com/vahid-sohrabloo/fastcompress/flate"

	"github.com/vahid-sohrabloo/fastcompress/flate"
	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// rdByteOnly is an io.ByteReader with no Peek or Seek.
type rdByteOnly struct{ r *bytes.Reader }

func (b *rdByteOnly) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *rdByteOnly) ReadByte() (byte, error)    { return b.r.ReadByte() }

// rdPlain hides every method but Read.
type rdPlain struct{ r io.Reader }

func (p *rdPlain) Read(b []byte) (int, error) { return p.r.Read(b) }

// rdRandom returns reads of random sizes.
type rdRandom struct {
	r   io.Reader
	rnd *rand.Rand
}

func (r *rdRandom) Read(p []byte) (int, error) { return r.r.Read(p[:min(len(p), 1+r.rnd.IntN(3000))]) }

// rdZero returns (0, nil) forever.
type rdZero struct{}

func (rdZero) Read([]byte) (int, error) { return 0, nil }

// rdSource is a kind of underlying reader. rest (for io.ByteReader kinds)
// returns what is left in it.
type rdSource struct {
	name  string
	exact bool
	make  func(b []byte, seed uint64) (r io.Reader, rest func() []byte)
}

func restOf(r io.Reader) func() []byte { return func() []byte { b, _ := io.ReadAll(r); return b } }

var rdSources = []rdSource{
	{"bytes.Reader", true, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		r := bytes.NewReader(b)
		return r, restOf(r)
	}},
	{"bufio", true, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		r := bufio.NewReader(bytes.NewReader(b))
		return r, restOf(r)
	}},
	{"bufio16", true, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		r := bufio.NewReaderSize(iotest.HalfReader(bytes.NewReader(b)), 16)
		return r, restOf(r)
	}},
	{"byte-only", true, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		r := &rdByteOnly{bytes.NewReader(b)}
		return r, restOf(r.r)
	}},
	{"bytes.Buffer", true, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		r := bytes.NewBuffer(append([]byte(nil), b...))
		return r, restOf(r)
	}},
	{"random-chunks", false, func(b []byte, seed uint64) (io.Reader, func() []byte) {
		return &rdRandom{bytes.NewReader(b), rand.New(rand.NewPCG(seed, 5))}, nil
	}},
	{"one-byte", false, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		return iotest.OneByteReader(bytes.NewReader(b)), nil
	}},
	{"half", false, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		return iotest.HalfReader(bytes.NewReader(b)), nil
	}},
	{"data-err", false, func(b []byte, _ uint64) (io.Reader, func() []byte) {
		return iotest.DataErrReader(&rdPlain{bytes.NewReader(b)}), nil
	}},
}

// rdClass is the error class compared with the standard library.
func rdClass(err error) string {
	var ce flate.CorruptInputError
	switch {
	case err == nil:
		return "nil"
	case err == io.EOF:
		return "EOF"
	case err == io.ErrUnexpectedEOF:
		return "UEOF"
	case errors.As(err, &ce), strings.HasPrefix(err.Error(), "flate: corrupt input before offset"):
		// Ours or the standard library's CorruptInputError (the offsets
		// are not compared).
		return "corrupt"
	}
	return err.Error()
}

// rdReadAll opens b with open and reads it all with Reads of random sizes.
func rdReadAll(b []byte, src rdSource, seed uint64, open func(io.Reader) (io.ReadCloser, error)) ([]byte, error,
	func() []byte) {
	r, rest := src.make(b, seed)
	z, err := open(r)
	if err != nil {
		// Wrapped, so that an io.EOF of gzip.NewReader (empty input) is
		// not taken for the end of the data.
		return nil, fmt.Errorf("open: %w", err), rest
	}
	rnd := rand.New(rand.NewPCG(seed, 6))
	var out []byte
	buf := make([]byte, 70000)
	for {
		n, err := z.Read(buf[:rnd.IntN(len(buf)+1)])
		out = append(out, buf[:n]...)
		if err != nil {
			return out, err, rest
		}
	}
}

// rdEncoders returns the encodings of data the reader must decode: our
// whole-buffer Compressor and streaming Writer at several levels,
// libdeflate (ldref) and the standard library.
func rdEncoders(t *testing.T, data []byte, seed uint64) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	r := rand.New(rand.NewPCG(seed, 3))
	for _, level := range []int{0, 1, 6, 12} {
		c, err := pkg.NewCompressor(level)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := c.AppendCompress(nil, data)
		m[fmt.Sprintf("ours-L%d", level)] = out
	}
	var b bytes.Buffer
	w, err := newWriter(&b, []int{pkg.HuffmanOnly, 1, 6, 9}[r.IntN(4)])
	if err != nil {
		t.Fatal(err)
	}
	writeAll(t, w, r, data, 1+r.IntN(100000), 0.2)
	m["writer"] = b.Bytes()
	m["libdeflate-L9"] = creftest.LDCompress(t, data, 9, ldFormat)
	m["stdlib-L5"] = stdEncode(t, 5, data)
	m["stdlib-huff"] = stdEncode(t, -2, data)
	return m
}

// The short corpus through every encoder and every kind of underlying
// reader decodes to the input.
func TestReaderRoundTrip(t *testing.T) {
	cases := deflatetest.Cases(true)
	k := 0
	for ci, c := range cases {
		if testing.Short() && ci%3 != 0 {
			continue
		}
		if raceEnabled && len(c.Data) > 100000 {
			continue
		}
		for name, enc := range rdEncoders(t, c.Data, uint64(ci)) {
			src := rdSources[k%len(rdSources)]
			k++
			got, err, _ := rdReadAll(enc, src, uint64(k), newReader)
			if err != io.EOF || !bytes.Equal(got, c.Data) {
				t.Fatalf("%s/%s/%s: %d of %d bytes, err %v", c.Name, name, src.name, len(got), len(c.Data), err)
			}
		}
	}
}

// Every level 0..12 of our Compressor and of libdeflate (ldref) on part of
// the short corpus, through the kinds of underlying reader in turn.
func TestReaderAllLevels(t *testing.T) {
	cases := deflatetest.Cases(true)
	k := 0
	for ci, c := range cases {
		if ci%9 != 0 || len(c.Data) > 300000 || (raceEnabled && len(c.Data) > 50000) {
			continue
		}
		for level := 0; level <= 12; level++ {
			cp, _ := pkg.NewCompressor(level)
			ours, _ := cp.AppendCompress(nil, c.Data)
			for _, enc := range [][]byte{ours, creftest.LDCompress(t, c.Data, level, ldFormat)} {
				src := rdSources[k%len(rdSources)]
				k++
				got, err, _ := rdReadAll(enc, src, uint64(k), newReader)
				if err != io.EOF || !bytes.Equal(got, c.Data) {
					t.Fatalf("%s level %d (%s): %d of %d bytes, %v", c.Name, level, src.name, len(got), len(c.Data), err)
				}
			}
		}
	}
}

// A ByteReader source is left just after the stream (with gzip:
// Multistream(false)); every source kind decodes the stream.
func TestReaderTrailingData(t *testing.T) {
	data := streamData(4, 300000)
	c, _ := pkg.NewCompressor(6)
	enc, _ := c.AppendCompress(nil, data)
	in := append(append([]byte(nil), enc...), "TRAILING DATA"...)
	for i, src := range rdSources {
		got, err, rest := rdReadAll(in, src, uint64(i), func(r io.Reader) (io.ReadCloser, error) {
			z, err := newReader(r)
			if err == nil {
				singleStream(z)
			}
			return z, err
		})
		if err != io.EOF || !bytes.Equal(got, data) {
			t.Fatalf("%s: %d bytes, err %v", src.name, len(got), err)
		}
		if src.exact {
			if left := rest(); string(left) != "TRAILING DATA" {
				t.Fatalf("%s: left %q", src.name, left)
			}
		}
	}
}

// rdStreams returns small valid encodings for the error tests (outputs
// below 32 KiB, so read boundaries match the standard library's).
func rdStreams(t *testing.T) [][]byte {
	var out [][]byte
	for i, data := range [][]byte{nil, []byte("a"), []byte(strings.Repeat("error classes ", 300)), streamData(9, 20000)} {
		for _, level := range []int{0, 1, 9} {
			c, _ := pkg.NewCompressor(level)
			enc, _ := c.AppendCompress(nil, data)
			out = append(out, enc)
		}
		out = append(out, stdEncode(t, 6, data))
		if i == 3 {
			out = append(out, stdEncode(t, -2, data))
		}
	}
	return out
}

// rdOutcome reads b with open through a bytes.Reader and returns the data
// and the error class ("nil": read to the end, io.ReadAll's nil).
func rdOutcome(b []byte, open func(io.Reader) (io.ReadCloser, error)) ([]byte, string) {
	z, err := open(bytes.NewReader(b))
	if err != nil {
		return nil, "open:" + rdClass(err)
	}
	got, err := io.ReadAll(z)
	return got, rdClass(err)
}

// Truncated streams and streams with a corrupt header or trailer byte give
// the standard library's error class (and, for checksum errors and
// success, its data). Corrupt bytes inside the DEFLATE data may give
// another class (both fail, but the decoders detect different things
// first: libdeflate decodes offset symbols 30 and 31, the standard library
// rejects them at once); those only have to fail or succeed alike with the
// same data.
func TestReaderErrorsMatchStdlib(t *testing.T) {
	for si, s := range rdStreams(t) {
		var muts [][]byte
		for cut := 0; cut < len(s); cut += 1 + cut/50 {
			muts = append(muts, s[:cut])
		}
		nStrict := len(muts)
		for _, inBody := range []bool{false, true} {
			step := 1
			if inBody {
				step = 1 + len(s)/150 // sampled: the body is checked loosely
			}
			for p := 0; p < len(s); p += step {
				if (p >= bodyStart && p < len(s)-trailerSize) != inBody {
					continue
				}
				for _, x := range []byte{1, 2, 4, 0x80, 0xff} {
					m := append([]byte(nil), s...)
					m[p] ^= x
					muts = append(muts, m)
				}
			}
			if !inBody {
				nStrict = len(muts)
			}
		}
		for mi, m := range muts {
			gotOurs, cOurs := rdOutcome(m, newReader)
			gotStd, cStd := rdOutcome(m, stdNewReader)
			if mi < nStrict && cOurs != cStd {
				t.Fatalf("stream %d mutation %d (len %d of %d): ours %q, stdlib %q", si, mi, len(m), len(s), cOurs, cStd)
			}
			if (cOurs == "nil") != (cStd == "nil") && mi < nStrict {
				t.Fatalf("stream %d mutation %d: ours %q, stdlib %q", si, mi, cOurs, cStd)
			}
			if (cOurs == "nil" && cStd == "nil" || strings.Contains(cOurs, "checksum")) && !bytes.Equal(gotOurs, gotStd) {
				t.Fatalf("stream %d mutation %d: %s with %d bytes, stdlib %d bytes", si, mi, cOurs, len(gotOurs), len(gotStd))
			}
		}
	}
}

// Random mutations of the compressed data: the Reader accepts exactly what
// the whole-buffer decoder accepts (libdeflate's decisions), with the same
// output; where both it and the standard library accept, they agree.
func TestReaderMutations(t *testing.T) {
	var ins [][]byte
	for i, c := range deflatetest.Cases(true) {
		if len(c.Data) < 20000 && i%4 == 0 {
			for _, level := range []int{1, 6} {
				cp, _ := pkg.NewCompressor(level)
				enc, _ := cp.AppendCompress(nil, c.Data)
				if len(enc) > bodyStart+2 {
					ins = append(ins, enc)
				}
			}
		}
	}
	n := 6000
	if testing.Short() || raceEnabled {
		n = 1500
	}
	r := rand.New(rand.NewPCG(11, 11))
	for k := range n {
		in := ins[r.IntN(len(ins))]
		m := append([]byte(nil), in...)
		for e := 1 + r.IntN(3); e > 0; e-- {
			p := bodyStart + r.IntN(len(m)-bodyStart)
			switch r.IntN(4) {
			case 0:
				m = m[:p]
			case 1:
				m[p] = byte(r.Uint32())
			default:
				m[p] ^= 1 << r.IntN(8)
			}
			if len(m) <= bodyStart {
				break
			}
		}
		rdCheckVsWhole(t, fmt.Sprintf("mutant %d", k), m, rdSources[k%len(rdSources)], uint64(k))
	}
}

// rdCheckVsWhole: reading m through src agrees with the whole-buffer
// decoder's DecompressEx (and with the standard library where both
// succeed).
func rdCheckVsWhole(t *testing.T, name string, m []byte, src rdSource, seed uint64) {
	t.Helper()
	limit := max(1032*len(m)+1024, 1<<16)
	dst := make([]byte, limit)
	nIn, nOut, wErr := pkg.NewDecompressor().DecompressEx(dst, m)
	if errors.Is(wErr, pkg.ErrInsufficientSpace) {
		return
	}
	got, err, _ := rdReadAll(m, src, seed, newReader)
	switch {
	case !wholeComparable(m):
		// The container semantics differ (see wholeComparable).
	case wErr != nil:
		if err == io.EOF {
			t.Fatalf("%s (%s): Reader accepted, whole-buffer: %v", name, src.name, wErr)
		}
	case nIn == len(m) || trailingIgnored:
		if err != io.EOF || !bytes.Equal(got, dst[:nOut]) {
			t.Fatalf("%s (%s): Reader err %v (%d bytes), whole-buffer ok (%d bytes)", name, src.name, err, len(got), nOut)
		}
	default:
		// Bytes after the stream, read as further members: the stream's
		// data come first.
		if !bytes.HasPrefix(got, dst[:nOut]) {
			t.Fatalf("%s (%s): Reader err %v (%d bytes) does not start with the whole-buffer output (%d bytes)", name,
				src.name, err, len(got), nOut)
		}
	}
	if err == io.EOF {
		if sGot, sClass := rdOutcome(m, stdNewReader); sClass == "nil" && !bytes.Equal(sGot, got) {
			t.Fatalf("%s: stdlib accepts with other data", name)
		}
	}
}

// rdOp is one step of a reader lifecycle.
type rdOp int

const (
	opRead10 rdOp = iota
	opReadNil
	opReadBig
	opClose
	opReset
	numOps
)

// rdRun applies ops to a reader of b (from open and reset) and returns one
// line per op: the count (if withN) and the error class.
func rdRun(b []byte, ops []rdOp, withN bool, open func(io.Reader) (io.ReadCloser, error),
	reset func(io.ReadCloser, io.Reader) error) []string {
	z, err := open(bytes.NewReader(b))
	if err != nil {
		return []string{"open " + rdClass(err)}
	}
	var res []string
	buf := make([]byte, 1<<16)
	for _, op := range ops {
		var n int
		var err error
		switch op {
		case opRead10:
			n, err = z.Read(buf[:10])
		case opReadNil:
			n, err = z.Read(nil)
		case opReadBig:
			n, err = z.Read(buf)
		case opClose:
			err = z.Close()
		case opReset:
			err = reset(z, bytes.NewReader(b))
		}
		if withN {
			res = append(res, fmt.Sprintf("op%d n=%d %s", op, n, rdClass(err)))
		} else {
			res = append(res, fmt.Sprintf("op%d %s", op, rdClass(err)))
		}
	}
	return res
}

// rdSame reports whether two lifecycle traces are equal. When counts are not
// compared (truncated or corrupt data), an op that succeeds on our side and
// returns the stream's final error on the standard library's also matches:
// the standard library's reader hits the truncation one op earlier in some Go
// releases (1.25) than in others, depending on how its buffering flushes, and
// from then on Read and Close return that error.
func rdSame(ours, std []string, withN bool) bool {
	if len(ours) != len(std) {
		return false
	}
	for i := range ours {
		if ours[i] == std[i] {
			continue
		}
		if withN || !strings.HasSuffix(std[i], " UEOF") ||
			ours[i] != strings.TrimSuffix(std[i], " UEOF")+" nil" {
			return false
		}
	}
	return true
}

// Sequences of Read (10 bytes, nil, 64 KiB), Close and Reset on valid,
// truncated, corrupt and bad-checksum streams return what the standard
// library's reader returns (counts too, except for truncated or corrupt
// data, where the data delivered before the error depends on buffering).
func TestReaderLifecycleMatchesStdlib(t *testing.T) {
	var inputs [][]byte
	var withN []bool
	for _, s := range rdStreams(t) {
		// Truncated: counts not compared (the standard library holds
		// back the last symbols whose codes it cannot look ahead past).
		inputs = append(inputs, s, s[:len(s)*2/3])
		withN = append(withN, true, false)
		if len(s) > bodyStart+3 {
			m := append([]byte(nil), s...)
			m[len(m)-1] ^= 1 // trailer (gzip, zlib) or last data byte (flate)
			bad := append([]byte(nil), s...)
			bad[bodyStart] |= 6 // BTYPE 3
			inputs = append(inputs, m, bad)
			withN = append(withN, bodyStart > 0, false)
		}
	}
	fixed := [][]rdOp{
		{opReadBig, opReadBig, opReadBig},
		{opReadNil, opReadBig, opReadNil, opClose, opReadBig, opClose},
		{opRead10, opClose, opRead10, opReadBig, opReadBig, opClose, opReadBig},
		{opReadBig, opReadBig, opClose, opReset, opReadBig, opReadBig, opClose},
		{opClose, opClose, opReadBig, opReadBig},
	}
	r := rand.New(rand.NewPCG(1, 1))
	for range 300 {
		ops := make([]rdOp, 1+r.IntN(12))
		for i := range ops {
			ops[i] = rdOp(r.IntN(int(numOps)))
		}
		fixed = append(fixed, ops)
	}
	for ii, in := range inputs {
		for oi, ops := range fixed {
			ours := rdRun(in, ops, withN[ii], newReader, resetReader)
			std := rdRun(in, ops, withN[ii], stdNewReader, stdResetReader)
			if !rdSame(ours, std, withN[ii]) {
				t.Fatalf("input %d, ops %d %v:\nours   %v\nstdlib %v", ii, oi, ops, ours, std)
			}
		}
	}
}

// Underlying read errors are returned as they are and stick; an underlying
// reader that returns (0, nil) forever gives io.ErrNoProgress.
func TestReaderSourceErrors(t *testing.T) {
	data := streamData(2, 100000)
	c, _ := pkg.NewCompressor(1)
	enc, _ := c.AppendCompress(nil, data)
	boom := errors.New("boom")
	for _, cut := range []int{0, 1, bodyStart + 1, len(enc) / 2, len(enc) - 1} {
		r := io.MultiReader(bytes.NewReader(enc[:cut]), iotest.ErrReader(boom))
		z, err := newReader(r)
		if err == nil {
			var got []byte
			got, err = io.ReadAll(z)
			if !bytes.HasPrefix(data, got) {
				t.Fatalf("cut %d: wrong data", cut)
			}
			if _, err2 := z.Read(make([]byte, 1)); err2 != err {
				t.Fatalf("cut %d: not sticky: %v then %v", cut, err, err2)
			}
		}
		if err != boom {
			t.Fatalf("cut %d: %v, want the source's error", cut, err)
		}
	}
	z, err := newReader(rdZero{})
	if err == nil {
		_, err = z.Read(make([]byte, 10))
	}
	if err != io.ErrNoProgress {
		t.Fatalf("(0, nil) reader: %v", err)
	}
}

// Reset reuses the reader: the same output as a new one, and in steady
// state reading a stream allocates nothing.
func TestReaderResetAllocs(t *testing.T) {
	data := streamData(3, 200000)
	c, _ := pkg.NewCompressor(6)
	enc, _ := c.AppendCompress(nil, data)
	br := bytes.NewReader(enc)
	z, err := newReader(br)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5000)
	var got []byte
	read := func() {
		got = got[:0]
		for {
			n, err := z.Read(buf)
			got = append(got, buf[:n]...)
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	got = make([]byte, 0, len(data))
	allocs := testing.AllocsPerRun(5, func() {
		br.Reset(enc)
		if err := resetReader(z, br); err != nil {
			t.Fatal(err)
		}
		read()
	})
	if !bytes.Equal(got, data) {
		t.Fatal("wrong data after Reset")
	}
	if allocs != 0 {
		t.Fatalf("%v allocations per Reset + read", allocs)
	}
}

// rdBadSeeker is a seekable io.ByteReader whose Seek fails.
type rdBadSeeker struct{ *bytes.Reader }

var errRdSeek = errors.New("seek failed")

func (rdBadSeeker) Seek(int64, int) (int64, error) { return 0, errRdSeek }

// At the end of the data a seekable ByteReader is moved back over what was
// read ahead; a failing Seek is returned by Read and sticks.
func TestReaderUnreadError(t *testing.T) {
	c, _ := pkg.NewCompressor(6)
	enc, _ := c.AppendCompress(nil, []byte(strings.Repeat("seek back ", 100)))
	z, err := newReader(rdBadSeeker{bytes.NewReader(append(enc, "more"...))})
	if err != nil {
		t.Fatal(err)
	}
	singleStream(z)
	if _, err := io.ReadAll(z); err != errRdSeek {
		t.Fatalf("got %v, want the Seek error", err)
	}
	if _, err := z.Read(nil); err != errRdSeek {
		t.Fatalf("not sticky: %v", err)
	}
}

// FuzzReader: arbitrary (seeded: compressed corpus) input read through a
// kind of underlying reader chosen by the first byte agrees with the
// whole-buffer decoder and never panics.
func FuzzReader(f *testing.F) {
	for i, c := range deflatetest.Cases(true) {
		if len(c.Data) > 5000 || i%3 != 0 {
			continue
		}
		cp, _ := pkg.NewCompressor([]int{0, 1, 6, 9}[i%4])
		enc, _ := cp.AppendCompress(nil, c.Data)
		f.Add(append([]byte{byte(i)}, enc...))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 || len(data) > 1<<16 {
			return
		}
		rdCheckVsWhole(t, "fuzz", data[1:], rdSources[int(data[0])%len(rdSources)], uint64(data[0]))
	})
}

// TestReaderSharedFileInSync: the three copies of this file differ only in
// the package clause and the import of the package under test.
func TestReaderSharedFileInSync(t *testing.T) {
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
	me := norm("reader_test.go")
	for _, other := range []string{"../flate/reader_test.go", "../gzip/reader_test.go", "../zlib/reader_test.go"} {
		if norm(other) != me {
			t.Errorf("%s differs from this package's reader_test.go", other)
		}
	}
}
