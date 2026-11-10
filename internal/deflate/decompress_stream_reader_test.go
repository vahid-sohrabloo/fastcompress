package deflate

import (
	"bufio"
	"bytes"
	stdflate "compress/flate"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// byteOnly is an io.ByteReader without Peek or Seek (byte mode).
type byteOnly struct{ r *bytes.Reader }

func (b *byteOnly) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *byteOnly) ReadByte() (byte, error)    { return b.r.ReadByte() }

// byteHalf is a byte mode source whose Read returns half of what is
// asked for (rounded up).
type byteHalf struct{ r *bytes.Reader }

func (b *byteHalf) Read(p []byte) (int, error) { return b.r.Read(p[:(len(p)+1)/2]) }
func (b *byteHalf) ReadByte() (byte, error)    { return b.r.ReadByte() }

// plainReader hides every method but Read (bulk mode).
type plainReader struct{ r io.Reader }

func (p *plainReader) Read(b []byte) (int, error) { return p.r.Read(b) }

// zeroReader returns (0, nil) forever.
type zeroReader struct{ n int }

func (z *zeroReader) Read([]byte) (int, error) { z.n++; return 0, nil }

// badSeeker is a seekable io.ByteReader whose Seek fails.
type badSeeker struct{ *bytes.Reader }

var errSeek = errors.New("seek failed")

func (b badSeeker) Seek(int64, int) (int64, error) { return 0, errSeek }

// errPeeker has bufio.Reader's methods, but a Peek that fails: not a
// *bufio.Reader, so read in byte mode (Peek is never called).
type errPeeker struct {
	*bufio.Reader
	err error
}

func (e errPeeker) Peek(int) ([]byte, error) { return nil, e.err }

// fakeSeekPeeker has bufio.Reader's methods and a Seek: seek mode.
type fakeSeekPeeker struct{ *bytes.Reader }

func (fakeSeekPeeker) Peek(int) ([]byte, error) { return nil, errSeek }
func (fakeSeekPeeker) Discard(int) (int, error) { return 0, errSeek }
func (fakeSeekPeeker) Buffered() int            { return 1 << 20 }

// streamSource is a source kind for the StreamReader tests: it wraps b and
// reports the bytes left in it afterwards (-1: unknown, the source may
// read ahead).
type streamSource struct {
	name  string
	exact bool // a ByteReader: nothing past the data may be consumed
	make  func(b []byte) (io.Reader, func() []byte)
}

var streamSources = []streamSource{
	{"bytes.Reader(seek)", true, func(b []byte) (io.Reader, func() []byte) {
		r := bytes.NewReader(b)
		return r, func() []byte { x, _ := io.ReadAll(r); return x }
	}},
	{"bufio(peek)", true, func(b []byte) (io.Reader, func() []byte) {
		r := bufio.NewReader(bytes.NewReader(b))
		return r, func() []byte { x, _ := io.ReadAll(r); return x }
	}},
	{"bufio16(peek+byte)", true, func(b []byte) (io.Reader, func() []byte) {
		r := bufio.NewReaderSize(iotest.HalfReader(bytes.NewReader(b)), 16)
		return r, func() []byte { x, _ := io.ReadAll(r); return x }
	}},
	{"byte-only", true, func(b []byte) (io.Reader, func() []byte) {
		r := &byteOnly{bytes.NewReader(b)}
		return r, func() []byte { x, _ := io.ReadAll(r.r); return x }
	}},
	{"byte-half", true, func(b []byte) (io.Reader, func() []byte) {
		r := &byteHalf{bytes.NewReader(b)}
		return r, func() []byte { x, _ := io.ReadAll(r.r); return x }
	}},
	{"bytes.Buffer", true, func(b []byte) (io.Reader, func() []byte) {
		r := bytes.NewBuffer(append([]byte(nil), b...))
		return r, func() []byte { x, _ := io.ReadAll(r); return x }
	}},
	{"plain", false, func(b []byte) (io.Reader, func() []byte) { return &plainReader{bytes.NewReader(b)}, nil }},
	{"one-byte", false, func(b []byte) (io.Reader, func() []byte) { return iotest.OneByteReader(bytes.NewReader(b)), nil }},
	{"half", false, func(b []byte) (io.Reader, func() []byte) { return iotest.HalfReader(bytes.NewReader(b)), nil }},
	{"data-err", false, func(b []byte) (io.Reader, func() []byte) { return iotest.DataErrReader(bytes.NewReader(b)), nil }},
}

// readAllSR reads z to its end with reads of random sizes (0 included).
func readAllSR(z *StreamReader, r *rand.Rand) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1<<17)
	for {
		n, err := z.Read(buf[:r.IntN(len(buf)+1)])
		out = append(out, buf[:n]...)
		if err != nil {
			return out, err
		}
	}
}

// bigStream returns about 3 MiB of corpus data (many window slides and
// matches across them) and its level-6 stream.
func bigStream(t testing.TB) (data, stream []byte) {
	for _, c := range deflatetest.Cases(true) {
		if len(data) > 3<<20 {
			break
		}
		if len(c.Data) > 10000 {
			data = append(data, c.Data[:min(len(c.Data), 600000)]...)
		}
	}
	c, _ := NewCompressor(6)
	dst := make([]byte, c.CompressBound(len(data)))
	return data, dst[:c.Compress(dst, data)]
}

// Container-like framing through every source kind: raw header bytes
// (ReadFull, ReadByte), the stream, a trailer, then bytes that must stay
// in a ByteReader source after Unread.
func TestStreamReaderSources(t *testing.T) {
	data, stream := bigStream(t)
	if testing.Short() || raceEnabled {
		data = data[:400000]
		c, _ := NewCompressor(1)
		dst := make([]byte, c.CompressBound(len(data)))
		stream = dst[:c.Compress(dst, data)]
	}
	input := append(append([]byte("HDR!"), stream...), "TRL!REST"...)
	for i, src := range streamSources {
		r := rand.New(rand.NewPCG(uint64(i), 1))
		rd, rest := src.make(input)
		var z StreamReader
		z.ResetSource(rd)
		h := make([]byte, 3)
		if err := z.ReadFull(h); err != nil || string(h) != "HDR" {
			t.Fatalf("%s: header %q %v", src.name, h, err)
		}
		if c, err := z.ReadByte(); err != nil || c != '!' {
			t.Fatalf("%s: ReadByte %q %v", src.name, c, err)
		}
		z.ResetStream(nil)
		got, err := readAllSR(&z, r)
		if err != io.EOF || !bytes.Equal(got, data) {
			t.Fatalf("%s: %d of %d bytes, err %v", src.name, len(got), len(data), err)
		}
		if err := z.ReadFull(h[:3]); err != nil || string(h) != "TRL" {
			t.Fatalf("%s: trailer %q %v", src.name, h, err)
		}
		if c, err := z.ReadByte(); err != nil || c != '!' {
			t.Fatalf("%s: ReadByte %q %v", src.name, c, err)
		}
		if err := z.Unread(); err != nil {
			t.Fatalf("%s: Unread: %v", src.name, err)
		}
		if src.exact {
			if left := rest(); string(left) != "REST" {
				t.Fatalf("%s: left in the source %q, want REST", src.name, left)
			}
		}
		// Read after the end: io.EOF again.
		if n, err := z.Read(h); n != 0 || err != io.EOF {
			t.Fatalf("%s: Read after EOF = %d, %v", src.name, n, err)
		}
	}
}

// ReadFull and ReadByte at the end of the input: io.EOF if nothing,
// io.ErrUnexpectedEOF if part of it.
func TestStreamReaderRawEOF(t *testing.T) {
	for _, src := range streamSources {
		rd, _ := src.make([]byte("ab"))
		var z StreamReader
		z.ResetSource(rd)
		if err := z.ReadFull(make([]byte, 3)); err != io.ErrUnexpectedEOF {
			t.Fatalf("%s: ReadFull past the end: %v", src.name, err)
		}
		rd, _ = src.make(nil)
		z.ResetSource(rd)
		if err := z.ReadFull(make([]byte, 3)); err != io.EOF {
			t.Fatalf("%s: ReadFull at the end: %v", src.name, err)
		}
		if _, err := z.ReadByte(); err != io.EOF {
			t.Fatalf("%s: ReadByte at the end: %v", src.name, err)
		}
		if err := z.ReadFull(nil); err != nil {
			t.Fatalf("%s: ReadFull(nil): %v", src.name, err)
		}
	}
}

// Truncated and corrupt streams through every source: the same error
// class, sticky; data before the error is a prefix of the output.
func TestStreamReaderErrors(t *testing.T) {
	data := []byte(strings.Repeat("streaming errors, ", 5000))
	c, _ := NewCompressor(6)
	dst := make([]byte, c.CompressBound(len(data)))
	stream := dst[:c.Compress(dst, data)]
	bad := append([]byte(nil), stream...)
	bad[0] |= 6 // BTYPE 3
	for _, src := range streamSources {
		for _, tc := range []struct {
			name string
			in   []byte
			want error
		}{{"empty", nil, io.ErrUnexpectedEOF}, {"cut", stream[:len(stream)/2], io.ErrUnexpectedEOF},
			{"cut1", stream[:len(stream)-1], io.ErrUnexpectedEOF}, {"btype3", bad, ErrBadData}} {
			rd, _ := src.make(tc.in)
			var z StreamReader
			z.Reset(rd, nil)
			got, err := readAllSR(&z, rand.New(rand.NewPCG(1, 2)))
			if err != tc.want || !bytes.HasPrefix(data, got) {
				t.Fatalf("%s/%s: err %v (want %v), %d bytes, prefix %v", src.name, tc.name, err, tc.want, len(got),
					bytes.HasPrefix(data, got))
			}
			if n, err2 := z.Read(make([]byte, 10)); n != 0 || err2 != err || z.Err() != err {
				t.Fatalf("%s/%s: not sticky: %d %v", src.name, tc.name, n, err2)
			}
			if err == ErrBadData && (z.CorruptOffset() <= 0 || z.CorruptOffset() > int64(len(tc.in))) {
				t.Fatalf("%s/%s: CorruptOffset %d of %d", src.name, tc.name, z.CorruptOffset(), len(tc.in))
			}
		}
	}
}

// A source error is returned as it is (after the data before it) and
// sticks; (0, nil) reads end in io.ErrNoProgress; a failing Seek in Unread
// becomes the sticky error; a failing Peek is a source error.
func TestStreamReaderSourceErrors(t *testing.T) {
	data := []byte(strings.Repeat("source errors ", 10000))
	c, _ := NewCompressor(1)
	dst := make([]byte, c.CompressBound(len(data)))
	stream := dst[:c.Compress(dst, data)]
	boom := errors.New("boom")
	var z StreamReader
	z.Reset(io.MultiReader(bytes.NewReader(stream[:len(stream)/2]), iotest.ErrReader(boom)), nil)
	got, err := io.ReadAll(&z)
	if err != boom || len(got) == 0 || !bytes.HasPrefix(data, got) {
		t.Fatalf("source error: %v, %d bytes", err, len(got))
	}
	if _, err := z.Read(nil); err != boom {
		t.Fatalf("not sticky: %v", err)
	}

	zr := &zeroReader{}
	z.Reset(zr, nil)
	if _, err := z.Read(make([]byte, 10)); err != io.ErrNoProgress || zr.n != maxEmptyReads {
		t.Fatalf("zero reader: %v after %d reads", err, zr.n)
	}

	z.Reset(badSeeker{bytes.NewReader(append(append([]byte(nil), stream...), 1, 2, 3))}, nil)
	if _, err := io.ReadAll(&z); err != nil {
		t.Fatalf("badSeeker read: %v", err)
	}
	if err := z.Unread(); err != errSeek {
		t.Fatalf("Unread: %v", err)
	}
	if _, err := z.Read(nil); err != errSeek {
		t.Fatalf("after failed Unread: %v", err)
	}

	// Types with bufio.Reader's methods are not peeked: byte mode, or
	// seek mode with a Seek.
	src := append(append([]byte(nil), stream...), "REST"...)
	ep := errPeeker{bufio.NewReader(bytes.NewReader(src)), boom}
	z.Reset(ep, nil)
	if got, err := io.ReadAll(&z); err != nil || !bytes.Equal(got, data) || z.mode != srcByte {
		t.Fatalf("peeker look-alike: %v, mode %d", err, z.mode)
	}
	if left, _ := io.ReadAll(ep); string(left) != "REST" {
		t.Fatalf("peeker look-alike: left %q", left)
	}
	sp := fakeSeekPeeker{bytes.NewReader(src)}
	z.Reset(sp, nil)
	if got, err := io.ReadAll(&z); err != nil || !bytes.Equal(got, data) || z.mode != srcSeek {
		t.Fatalf("seeking peeker look-alike: %v, mode %d", err, z.mode)
	}
	if err := z.Unread(); err != nil || sp.Len() != 4 {
		t.Fatalf("seeking peeker look-alike: Unread %v, %d left", err, sp.Len())
	}
}

// Read(nil) decodes until output is pending (0, nil) or fails (0, err);
// the last data come with io.EOF in one Read when the stream ended in it.
func TestStreamReaderReadNil(t *testing.T) {
	var w bitWriter
	w.stored(true, []byte("abcde"))
	var z StreamReader
	z.Reset(bytes.NewReader(w.bytes()), nil)
	if n, err := z.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = %d, %v", n, err)
	}
	buf := make([]byte, 100)
	if n, err := z.Read(buf); n != 5 || err != io.EOF || string(buf[:5]) != "abcde" {
		t.Fatalf("Read = %d %q, %v", n, buf[:n], err)
	}
	z.Reset(bytes.NewReader([]byte{0x07, 0, 0}), nil)
	if n, err := z.Read(nil); n != 0 || err != ErrBadData {
		t.Fatalf("Read(nil) of a bad stream = %d, %v", n, err)
	}
}

// A preset dictionary: offsets reach into it, only its last 32 KiB count,
// and Reset with another dictionary or none works.
func TestStreamReaderDict(t *testing.T) {
	dict := rndBytes(9, 50000)
	src := append(append([]byte(nil), dict[len(dict)-32768:len(dict)-32000]...), dict[len(dict)-100:]...)
	c, _ := NewCompressor(6)
	dst := make([]byte, c.CompressChunkBound(len(src)))
	s := dst[:c.CompressChunk(dst, dict, src, true)]
	var z StreamReader
	for range 2 {
		z.Reset(bufio.NewReader(bytes.NewReader(s)), dict)
		got, err := io.ReadAll(&z)
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("with dict: %v", err)
		}
		z.Reset(bytes.NewReader(s), nil)
		if _, err := io.ReadAll(&z); err != ErrBadData {
			t.Fatalf("without dict: %v", err)
		}
	}
}

// Every chunking of the source through the StreamReader (random read
// sizes of a plain reader) equals the whole-buffer decoder, also for
// mutated streams (accept/reject decisions and output).
func TestStreamReaderVsWhole(t *testing.T) {
	_, streams, origs := streamCorpus(t, []int{1, 9})
	r := rand.New(rand.NewPCG(4, 4))
	var z StreamReader
	n := 3000
	if testing.Short() || raceEnabled {
		n = 600
	}
	for k := range n {
		i := r.IntN(len(streams))
		in := streams[i]
		if k%2 == 1 {
			in = streamMutate(r, in)
		}
		limit := max(1032*len(in)+64, len(origs[i])+64)
		if len(in) > 200000 {
			continue
		}
		wOut, _, wErr := wholeResult(NewDecompressor(), in, limit)
		z.Reset(iotest.OneByteReader(&randReader{bytes.NewReader(in), r}), nil)
		got, err := io.ReadAll(&z)
		if wErr == ErrInsufficientSpace {
			continue
		}
		if (err == nil) != (wErr == nil) || (err == nil && !bytes.Equal(got, wOut)) {
			t.Fatalf("case %d: stream err %v (%d bytes), whole-buffer err %v (%d bytes)", k, err, len(got), wErr, len(wOut))
		}
		if err != nil && err != ErrBadData && err != io.ErrUnexpectedEOF {
			t.Fatalf("case %d: error %v", k, err)
		}
		if err == ErrBadData {
			// The data before the invalid item are delivered: what one
			// call of the generic loop over the whole input decodes.
			ref := slowBadData(t, in, nil, limit)
			if !bytes.Equal(got, ref) {
				t.Fatalf("case %d: %d bytes before ErrBadData, want %d", k, len(got), len(ref))
			}
		}
	}
}

// slowBadData decodes the invalid stream in in one call of the generic
// loop only and returns the output before the invalid data.
func slowBadData(t *testing.T, in, dict []byte, limit int) []byte {
	t.Helper()
	out := make([]byte, len(dict)+limit)
	copy(out, dict)
	s := streamState{slow: true}
	res, _, nOut := NewDecompressor().decompressStream(&s, in, out, len(dict))
	if res != libdeflateBadData {
		t.Fatalf("slow decode of an invalid stream: %v", res)
	}
	return out[len(dict):nOut]
}

// randReader returns reads of random sizes.
type randReader struct {
	r   io.Reader
	rnd *rand.Rand
}

func (r *randReader) Read(p []byte) (int, error) {
	return r.r.Read(p[:min(len(p), 1+r.rnd.IntN(5000))])
}

// Steady state: Reset and reading a stream to its end allocate nothing.
func TestStreamReaderNoAlloc(t *testing.T) {
	data := []byte(strings.Repeat("no allocations here, ", 20000))
	c, _ := NewCompressor(6)
	dst := make([]byte, c.CompressBound(len(data)))
	stream := dst[:c.Compress(dst, data)]
	br := bytes.NewReader(stream)
	bu := bufio.NewReader(br)
	var z StreamReader
	z.Reset(br, nil)
	buf := make([]byte, 4096)
	for _, src := range []io.Reader{br, bu} {
		allocs := testing.AllocsPerRun(5, func() {
			br.Reset(stream)
			if src == bu {
				bu.Reset(br)
			}
			z.Reset(src, nil)
			for {
				_, err := z.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		})
		if allocs != 0 {
			t.Fatalf("%T: %v allocations per stream", src, allocs)
		}
	}
}

// BenchmarkStreamVsWhole compares the StreamReader (through several source
// kinds) with the whole-buffer DecompressEx and the standard library's
// reader (a black box) on about 3 MiB of corpus data.
func BenchmarkStreamVsWhole(b *testing.B) {
	data, stream := bigStream(b)
	dst := make([]byte, len(data)+64)
	buf := make([]byte, 32<<10)
	b.Run("whole", func(b *testing.B) {
		d := NewDecompressor()
		b.SetBytes(int64(len(data)))
		for range b.N {
			if _, _, err := d.DecompressEx(dst, stream); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stdlib", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for range b.N {
			r := stdflate.NewReader(bytes.NewReader(stream))
			for {
				_, err := r.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	for _, src := range []struct {
		name string
		r    func() io.Reader
	}{
		{"plain", func() io.Reader { return &plainReader{bytes.NewReader(stream)} }},
		{"bytes.Reader", func() io.Reader { return bytes.NewReader(stream) }},
		{"bufio", func() io.Reader { return bufio.NewReader(bytes.NewReader(stream)) }},
	} {
		b.Run("stream-"+src.name, func(b *testing.B) {
			var z StreamReader
			b.SetBytes(int64(len(data)))
			for range b.N {
				z.Reset(src.r(), nil)
				for {
					_, err := z.Read(buf)
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// Matches at the largest offset (32768) right after every window slide
// need the whole 32 KiB history to be kept (libdeflate's compressor never
// emits offset 32768, so the stream is crafted: random literals, then
// max-length matches at offset 32768 and literals mixed).
func TestStreamReaderMaxOffsetAcrossSlides(t *testing.T) {
	r := rand.New(rand.NewPCG(8, 8))
	toks := litsOf(rndBytes(8, 32768))
	for range 3000 {
		toks = append(toks, match(258, 32768))
		if r.IntN(3) == 0 {
			toks = append(toks, lit(byte(r.Uint32())), match(3+r.IntN(256), 32768))
		}
	}
	var w bitWriter
	w.static(true, toks)
	in, want := w.bytes(), tokOutput(nil, toks)
	for _, src := range streamSources {
		rd, _ := src.make(in)
		var z StreamReader
		z.Reset(rd, nil)
		got, err := readAllSR(&z, rand.New(rand.NewPCG(1, 1)))
		if err != io.EOF || !bytes.Equal(got, want) {
			t.Fatalf("%s: %d of %d bytes, %v", src.name, len(got), len(want), err)
		}
	}
	if _, nOut, err := NewDecompressor().DecompressEx(make([]byte, len(want)), in); err != nil || nOut != len(want) {
		t.Fatalf("whole-buffer: %v", err)
	}
}

// badStreams returns crafted invalid streams with the data before the
// invalid item: bad match offsets (in the first block, in a later block,
// past a dictionary), BTYPE 3 and a bad stored NLEN after a valid block.
func badStreams() (names []string, ins, dicts, wants [][]byte) {
	add := func(name string, w *bitWriter, dict, want []byte) {
		names, ins, dicts, wants = append(names, name), append(ins, w.bytes()), append(dicts, dict), append(wants, want)
	}
	tail := litsOf(rndBytes(20, 100)) // input after the bad match: the fast loop finds it
	a := rndBytes(21, 20000)
	var w1 bitWriter
	w1.static(true, append(append(litsOf(a), match(10, 25000)), tail...))
	add("offset-first-block", &w1, nil, a)

	b, c := rndBytes(22, 5000), rndBytes(23, 3000)
	var w2 bitWriter
	w2.static(false, litsOf(b))
	w2.static(true, append(append(litsOf(c), match(5, 9000)), tail...))
	add("offset-later-block", &w2, nil, append(append([]byte(nil), b...), c...))

	dict := rndBytes(24, 1000)
	var w3 bitWriter
	w3.static(true, append(append(litsOf(a[:2000]), match(4, 3500)), tail...))
	add("offset-past-dict", &w3, dict, a[:2000])

	var w4 bitWriter
	w4.static(false, litsOf(b))
	w4.header(true, 3)
	w4.bits(0, 61)
	add("btype3-after-block", &w4, nil, b)

	var w5 bitWriter
	w5.stored(false, c)
	w5.header(true, 0)
	w5.align()
	w5.bits(10, 16)
	w5.bits(10, 16)
	w5.bits(0, 64)
	add("stored-nlen-after-block", &w5, nil, c)

	// A dynamic block longer than the first input buffer, then a static
	// block with a bad offset: the failing call starts inside the dynamic
	// block and replaces its decode tables before failing.
	litLens := make([]uint8, 286)
	for i := range 256 {
		litLens[i] = 9
	}
	litLens[256], litLens[257], litLens[258] = 2, 3, 3
	offLens := chainLens(30, []int{0, 1, 3, 5, 9, 12, 15, 17, 20, 22, 24, 25, 26, 27, 28, 29})
	d := rndBytes(25, 6000)
	var w6 bitWriter
	w6.dynamic(false, litLens, offLens, litsOf(d))
	w6.static(true, append(append(litsOf(c[:100]), match(5, 9000)), tail...))
	add("offset-after-dynamic-block", &w6, nil, append(append([]byte(nil), d...), c[:100]...))
	return
}

// MIN-1: invalid data after valid data: the data before the invalid item
// are delivered with ErrBadData (as by the standard library), through
// every source and read pattern; the error sticks.
func TestStreamReaderDataBeforeError(t *testing.T) {
	names, ins, dicts, wants := badStreams()
	for i, in := range ins {
		if _, _, err := wholeResult(NewDecompressor(), in, 1<<20); dicts[i] == nil && err != ErrBadData {
			t.Fatalf("%s: whole-buffer: %v", names[i], err)
		}
		sgot, serr := io.ReadAll(stdflate.NewReaderDict(bytes.NewReader(in), dicts[i]))
		var ce stdflate.CorruptInputError
		if !errors.As(serr, &ce) || !bytes.Equal(sgot, wants[i]) {
			t.Fatalf("%s: stdlib: %v, %d bytes", names[i], serr, len(sgot))
		}
		for j, src := range streamSources {
			rd, _ := src.make(in)
			var z StreamReader
			z.Reset(rd, dicts[i])
			got, err := readAllSR(&z, rand.New(rand.NewPCG(uint64(i), uint64(j))))
			if err != ErrBadData || !bytes.Equal(got, wants[i]) {
				t.Fatalf("%s/%s: %v, %d bytes, want %d", names[i], src.name, err, len(got), len(wants[i]))
			}
			if n, err := z.Read(make([]byte, 10)); n != 0 || err != ErrBadData {
				t.Fatalf("%s/%s: not sticky: %d %v", names[i], src.name, n, err)
			}
		}
	}
}

// MAJ-1: a *bytes.Buffer source is read in bulk (the number of decoder
// calls does not grow with the input bytes), and so is the body of a
// stored block in byte mode; nothing after the stream is consumed.
func TestStreamReaderDecoderCalls(t *testing.T) {
	data := []byte(strings.Repeat("bytes.Buffer is a common source of compressed data. ", 20000))
	for _, level := range []int{0, 6} {
		c, _ := NewCompressor(level)
		dst := make([]byte, c.CompressBound(len(data)))
		stream := dst[:c.Compress(dst, data)]
		src := append(append([]byte(nil), stream...), "REST"...)
		for _, mk := range []func() (io.Reader, func() int){
			func() (io.Reader, func() int) { b := bytes.NewBuffer(append([]byte(nil), src...)); return b, b.Len },
			func() (io.Reader, func() int) { b := &byteOnly{bytes.NewReader(src)}; return b, b.r.Len },
			func() (io.Reader, func() int) {
				br := bytes.NewReader(src)
				b := bufio.NewReader(br)
				return b, func() int { return br.Len() + b.Buffered() }
			},
		} {
			r, left := mk()
			if _, ok := r.(*byteOnly); ok && level != 0 {
				continue // byte mode is per byte for Huffman blocks
			}
			var z StreamReader
			z.Reset(r, nil)
			got, err := io.ReadAll(&z)
			if err != nil || !bytes.Equal(got, data) || left() != 4 {
				t.Fatalf("level %d %T: %v, %d bytes left", level, r, err, left())
			}
			if z.calls > len(stream)/100 {
				t.Fatalf("level %d %T: %d decoder calls for %d input bytes", level, r, z.calls, len(stream))
			}
		}
	}
}

// MIN-2: the buffers start small and grow to their full size only when
// the first window is full (or a large dictionary needs it).
func TestStreamReaderBufferGrowth(t *testing.T) {
	small := []byte(strings.Repeat("small ", 1000))
	big := rndBytes(30, 100000)
	c, _ := NewCompressor(6)
	enc := func(b []byte) []byte {
		dst := make([]byte, c.CompressBound(len(b)))
		return dst[:c.Compress(dst, b)]
	}
	var z StreamReader
	for _, tc := range []struct {
		data      []byte
		dict      []byte
		win, inSz int
	}{{small, nil, streamWinStart, streamInStart}, {big, nil, streamWinSize, streamInSize}} {
		z = StreamReader{}
		z.Reset(bytes.NewReader(enc(tc.data)), tc.dict)
		got, err := io.ReadAll(&z)
		if err != nil || !bytes.Equal(got, tc.data) || len(z.win) != tc.win || len(z.in) != tc.inSz {
			t.Fatalf("%d bytes: %v, window %d, input %d", len(tc.data), err, len(z.win), len(z.in))
		}
	}
	// A block boundary with less than streamMinRoom bytes of room left in
	// the first window: the next Read grows it before decoding.
	var w bitWriter
	first := rndBytes(32, streamWinStart-100)
	w.stored(false, first)
	w.stored(true, []byte("abc"))
	z = StreamReader{}
	z.Reset(iotest.OneByteReader(bytes.NewReader(w.bytes())), nil)
	buf := make([]byte, streamWinStart)
	if n, err := io.ReadFull(&z, buf[:len(first)]); err != nil || n != len(first) || len(z.win) != streamWinStart {
		t.Fatalf("first block: %d, %v, window %d", n, err, len(z.win))
	}
	if got, err := io.ReadAll(&z); err != nil || string(got) != "abc" || len(z.win) != streamWinSize {
		t.Fatalf("after the boundary: %q, %v, window %d", got, err, len(z.win))
	}
	// A dictionary that does not fit the first window: the full one.
	z = StreamReader{}
	z.Reset(bytes.NewReader(nil), rndBytes(31, streamWinStart))
	if len(z.win) != streamWinSize || len(z.in) != streamInSize {
		t.Fatalf("large dictionary: window %d, input %d", len(z.win), len(z.in))
	}
	z = StreamReader{}
	z.Reset(bytes.NewReader(nil), rndBytes(31, streamWinStart-streamMinRoom))
	if len(z.win) != streamWinStart {
		t.Fatalf("dictionary that fits: window %d", len(z.win))
	}
}

// ReadFull in byte mode reads only what is still missing: with short
// Reads of the source nothing after the requested bytes is consumed.
func TestStreamReaderReadFullExact(t *testing.T) {
	r := &byteHalf{bytes.NewReader([]byte("ABCDEFGH"))}
	var z StreamReader
	z.ResetSource(r)
	p := make([]byte, 5)
	if err := z.ReadFull(p); err != nil || string(p) != "ABCDE" || r.r.Len() != 3 {
		t.Fatalf("ReadFull %q, %v, %d left", p, err, r.r.Len())
	}
}

// N-MIN-1: CorruptOffset is the input position just after the invalid
// item (the one-call slow decode's actualIn), whatever the source, the read
// pattern and the bytes after the stream (read ahead by most sources).
func TestStreamReaderCorruptOffset(t *testing.T) {
	names, ins, dicts, _ := badStreams()
	junk := rndBytes(40, 70000)
	for i, in := range ins {
		out := make([]byte, len(dicts[i])+1<<20)
		copy(out, dicts[i])
		s := streamState{slow: true}
		_, want, _ := NewDecompressor().decompressStream(&s, in, out, len(dicts[i]))
		if want <= 0 || want > len(in) {
			t.Fatalf("%s: slow actualIn %d of %d", names[i], want, len(in))
		}
		src := append(append([]byte(nil), in...), junk...)
		for j, ss := range streamSources {
			rd, _ := ss.make(src)
			var z StreamReader
			z.Reset(rd, dicts[i])
			if _, err := readAllSR(&z, rand.New(rand.NewPCG(uint64(i), uint64(j)))); err != ErrBadData || z.CorruptOffset() != int64(want) {
				t.Fatalf("%s/%s: %v, offset %d, want %d", names[i], ss.name, err, z.CorruptOffset(), want)
			}
		}
	}
}

// N-NIT-2: a rerun that does not fail again is reported as an internal
// error (and its output is not delivered).
func TestStreamReaderRerunDiverged(t *testing.T) {
	defer func(f func(*Decompressor, *streamState, []byte, []byte, int) (libdeflateResult, int, int)) {
		streamRerun = f
	}(streamRerun)
	streamRerun = func(*Decompressor, *streamState, []byte, []byte, int) (libdeflateResult, int, int) {
		return libdeflateSuccess, 0, 1 << 30
	}
	_, ins, _, wants := badStreams()
	var z StreamReader
	z.Reset(bytes.NewReader(ins[0]), nil)
	if got, err := io.ReadAll(&z); err != errStreamRerun || !bytes.HasPrefix(wants[0], got) {
		t.Fatalf("%v, %d bytes", err, len(got))
	}
}

// N-NIT-3: a *bytes.Buffer source emptied by the caller between Reads,
// while bytes copied from it are not consumed yet, gives
// io.ErrUnexpectedEOF (not a panic); bytes appended to it are read on.
func TestStreamReaderBufferDrained(t *testing.T) {
	var w bitWriter
	w.stored(false, []byte("abc"))
	w.stored(true, []byte("defg"))
	stream := w.bytes()
	for _, drain := range []bool{true, false} {
		bb := bytes.NewBuffer(append([]byte(nil), stream[:8+3]...)) // block 1, 3 bytes of block 2's header
		var z StreamReader
		z.Reset(bb, nil)
		p := make([]byte, 10)
		if n, err := z.Read(p); n != 3 || err != nil || string(p[:3]) != "abc" {
			t.Fatalf("first block: %q, %v", p[:n], err)
		}
		if z.iend-z.ipos != 3 || bb.Len() != 3 {
			t.Fatalf("unconsumed %d, buffered %d", z.iend-z.ipos, bb.Len())
		}
		if drain {
			bb.Reset()
			if n, err := z.Read(p); n != 0 || err != io.ErrUnexpectedEOF {
				t.Fatalf("drained: %d, %v", n, err)
			}
			continue
		}
		bb.Write(stream[8+3:])
		if got, err := io.ReadAll(&z); err != nil || string(got) != "defg" || bb.Len() != 0 {
			t.Fatalf("appended: %q, %v, %d left", got, err, bb.Len())
		}
	}
}
