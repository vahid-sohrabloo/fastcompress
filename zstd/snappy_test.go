package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// countWriter counts the bytes written through it.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// snappyConvert converts in with r and checks the returned count against
// the bytes written.
func snappyConvert(t testing.TB, r *SnappyConverter, in []byte) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	cw := &countWriter{w: &buf}
	n, err := r.Convert(&chunkReader{b: in, next: fixedSize(1000)}, cw)
	if n != cw.n || n != int64(buf.Len()) {
		t.Fatalf("Convert returned %d, wrote %d", n, cw.n)
	}
	return buf.Bytes(), err
}

// checkSnappyOutput requires frame to decode to want with C and with our
// Decoder.
func checkSnappyOutput(t *testing.T, d *Decoder, frame, want []byte) {
	t.Helper()
	got, err := d.DecodeAll(frame, nil)
	if err != nil {
		t.Fatalf("Decoder: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Decoder: %d bytes, want %d", len(got), len(want))
	}
	if got := creftest.DecodeC(t, frame); !bytes.Equal(got, want) {
		t.Fatalf("C: %d bytes, want %d", len(got), len(want))
	}
}

// TestSnappyTestCodec checks the test-only Snappy encoder against the
// test-only decoder, so that neither can hide a converter bug.
func TestSnappyTestCodec(t *testing.T) {
	names, inputs := sortedInputs(t)
	for i, name := range names {
		in := inputs[name]
		for _, extra := range []bool{false, true} {
			s := tsEncode(in, tsEncodeOpts{seed: uint64(i), extra: extra})
			got, err := tsDecode(s)
			if err != nil || !bytes.Equal(got, in) {
				t.Fatalf("%s extra=%v: %v, %d bytes, want %d", name, extra, err, len(got), len(in))
			}
		}
	}
}

func TestSnappyConvert(t *testing.T) {
	names, inputs := sortedInputs(t)
	d := newTestDecoder(t, WithDecoderConcurrency(1))
	var r SnappyConverter
	for i, name := range names {
		in := inputs[name]
		for _, extra := range []bool{false, true} {
			s := tsEncode(in, tsEncodeOpts{seed: uint64(i) + 100, extra: extra})
			out, err := snappyConvert(t, &r, s)
			if err != nil {
				t.Fatalf("%s extra=%v: %v", name, extra, err)
			}
			checkSnappyOutput(t, d, out, in)
		}
	}
	t.Run("concatenated", func(t *testing.T) {
		a, b := inputs[names[len(names)-1]], inputs[names[len(names)/2]]
		s := append(tsEncode(a, tsEncodeOpts{seed: 1, extra: true}), tsEncode(b, tsEncodeOpts{seed: 2})...)
		out, err := snappyConvert(t, &r, s)
		if err != nil {
			t.Fatal(err)
		}
		checkSnappyOutput(t, d, out, append(append([]byte(nil), a...), b...))
	})
	t.Run("empty", func(t *testing.T) {
		out, err := snappyConvert(t, &r, nil)
		if err != nil || len(out) != 0 {
			t.Fatalf("%d bytes, %v", len(out), err)
		}
		out, err = snappyConvert(t, &r, tsStreamID(nil))
		if err != nil {
			t.Fatal(err)
		}
		// Magic, FHD 0 (no FCS, checksum or dictID), the 64 KiB window
		// descriptor, then the empty last raw block.
		if want := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, windowDescriptor(1 << 16), 1, 0, 0}; !bytes.Equal(out, want) || want[5] != 0x30 {
			t.Fatalf("frame % x, want % x", out, want)
		}
		checkSnappyOutput(t, d, out, nil)
	})
	t.Run("handmade", func(t *testing.T) {
		// One block per element kind: literal forms 60..63, copies with
		// 1-, 2- and 4-byte offsets, overlapping copies, 1- and 2-byte
		// copies, an empty compressed chunk, and full 65536-byte chunks.
		want := []byte("xab" + "ababa" + "x" + "abab" + "q")
		block := []byte{byte(len(want))}
		block = append(block, 60<<2, 2, 'x', 'a', 'b') // literal, 1-byte length
		block = append(block, 1|1<<2, 2)               // copy-1 len 5 off 2
		block = append(block, 61<<2, 0, 0, 'x')        // literal, 2-byte length
		block = append(block, 2|1<<2, 4, 0)            // copy-2 len 2 off 4
		block = append(block, 3|1<<2, 2, 0, 0, 0)      // copy-4 len 2 off 2
		block = append(block, 63<<2, 0, 0, 0, 0, 'q')  // literal, 4-byte length
		big := make([]byte, tsMaxBlock)
		for i := range big {
			big[i] = byte(i * i >> 7)
		}
		s := tsStreamID(nil)
		s = tsDataChunk(s, tsChunkCompressed, want, block)
		s = tsDataChunk(s, tsChunkCompressed, nil, []byte{0})
		s = tsDataChunk(s, tsChunkUncompressed, nil, nil)
		s = tsDataChunk(s, tsChunkUncompressed, big, big)
		s = tsDataChunk(s, tsChunkCompressed, big, tsEncodeBlock(nil, big, rand.New(rand.NewPCG(3, 3))))
		s = tsDataChunk(s, tsChunkCompressed, want, block)
		out, err := snappyConvert(t, &r, s)
		if err != nil {
			t.Fatal(err)
		}
		checkSnappyOutput(t, d, out, bytes.Join([][]byte{want, big, big, want}, nil))
	})
	t.Run("entropy coded", func(t *testing.T) {
		in := bytes.Repeat([]byte("reuse me, reuse me twice; "), 2520)
		s := tsDataChunk(tsStreamID(nil), tsChunkCompressed, in, tsEncodeBlock(nil, in, rand.New(rand.NewPCG(5, 5))))
		out, err := snappyConvert(t, &r, s)
		if err != nil {
			t.Fatal(err)
		}
		checkSnappyOutput(t, d, out, in)
		if len(out) > len(in)/20 {
			t.Fatalf("%d bytes from %d: block not entropy-coded", len(out), len(in))
		}
		// A second identical chunk reuses the first block's Huffman table
		// (strategy fast prefers repeating it up to 1024 literals).
		rng := rand.New(rand.NewPCG(8, 8))
		in = make([]byte, 1000)
		for i := range in {
			in[i] = "eeeeeettttaaoinshrdlu"[rng.IntN(21)]
		}
		s = tsDataChunk(tsStreamID(nil), tsChunkCompressed, in, tsEncodeBlock(nil, in, rand.New(rand.NewPCG(6, 6))))
		out, err = snappyConvert(t, &r, s)
		if err != nil {
			t.Fatal(err)
		}
		out2, err := snappyConvert(t, &r, append(s, s[10:]...))
		if err != nil {
			t.Fatal(err)
		}
		checkSnappyOutput(t, d, out2, append(append([]byte(nil), in...), in...))
		if second := len(out2) - len(out); second >= len(out)-6-3 {
			t.Fatalf("second block %d bytes, first %d: tables not reused", second, len(out)-6-3)
		}
	})
}

// snappyErrorCases are crafted streams and the sentinel each must give.
func snappyErrorCases() []struct {
	name string
	in   []byte
	want error
} {
	sid := tsStreamID(nil)
	good := []byte("hello hello hello")
	gblock := tsEncodeBlock(nil, good, rand.New(rand.NewPCG(1, 1)))
	cat := func(b ...[]byte) []byte { return bytes.Join(b, nil) }
	comp := func(raw, body []byte) []byte { return tsDataChunk(nil, tsChunkCompressed, raw, body) }
	badCRC := comp(good, gblock)
	badCRC[4] ^= 1
	goodStream := cat(sid, comp(good, gblock))
	return []struct {
		name string
		in   []byte
		want error
	}{
		{"bad CRC", cat(sid, badCRC), ErrSnappyCorrupt},
		{"bad CRC uncompressed", cat(sid, tsChunk(nil, tsChunkUncompressed, []byte{0, 0, 0, 0, 'a'})), ErrSnappyCorrupt},
		{"offset 0", cat(sid, comp([]byte("aaaaa"), []byte{5, 0, 'a', 2 | 3<<2, 0, 0})), ErrSnappyCorrupt},
		{"offset past start", cat(sid, comp([]byte("aaaaa"), []byte{5, 0, 'a', 2 | 3<<2, 2, 0})), ErrSnappyCorrupt},
		{"copy first", cat(sid, comp([]byte("aaaa"), []byte{4, 1, 1})), ErrSnappyCorrupt},
		{"bad varint", cat(sid, comp(nil, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01})), ErrSnappyCorrupt},
		{"varint above 32 bits", cat(sid, comp(nil, []byte{0xff, 0xff, 0xff, 0xff, 0x1f})), ErrSnappyCorrupt},
		{"varint 6 bytes", cat(sid, comp(nil, []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00})), ErrSnappyCorrupt},
		{"varint cut", cat(sid, comp(nil, []byte{0x80})), ErrSnappyCorrupt},
		{"no varint", cat(sid, comp(nil, nil)), ErrSnappyCorrupt},
		{"short decode", cat(sid, comp([]byte("ab"), []byte{3, 1 << 2, 'a', 'b'})), ErrSnappyCorrupt},
		{"long decode", cat(sid, comp([]byte("a"), []byte{1, 1 << 2, 'a', 'b'})), ErrSnappyCorrupt},
		{"literal cut", cat(sid, comp([]byte("abc"), []byte{3, 2 << 2, 'a', 'b'})), ErrSnappyCorrupt},
		{"literal length cut", cat(sid, comp([]byte("abc"), []byte{3, 62 << 2, 2})), ErrSnappyCorrupt},
		{"copy cut", cat(sid, comp([]byte("aaaa"), []byte{4, 0, 'a', 3 | 2<<2, 1, 0})), ErrSnappyCorrupt},
		{"truncated header", goodStream[:len(sid)+2], ErrSnappyCorrupt},
		{"truncated data", goodStream[:len(goodStream)-1], ErrSnappyCorrupt},
		{"truncated stream id", sid[:7], ErrSnappyCorrupt},
		{"truncated padding", cat(sid, []byte{0xfe, 10, 0, 0, 0}), ErrSnappyCorrupt},
		{"no stream id", comp(good, gblock), ErrSnappyCorrupt},
		{"bad stream id", cat(sid, tsChunk(nil, tsChunkStreamID, []byte("sNaPpX"))), ErrSnappyCorrupt},
		{"stream id length", tsChunk(nil, tsChunkStreamID, []byte("sNaPpY!")), ErrSnappyCorrupt},
		{"short uncompressed", cat(sid, tsChunk(nil, tsChunkUncompressed, []byte{1, 2, 3})), ErrSnappyCorrupt},
		{"short compressed", cat(sid, tsChunk(nil, tsChunkCompressed, []byte{1, 2, 3})), ErrSnappyCorrupt},
		{"too large compressed", cat(sid, comp(nil, []byte{0x81, 0x80, 0x04})), ErrSnappyTooLarge},
		{"too large uncompressed", cat(sid, tsDataChunk(nil, tsChunkUncompressed, nil, make([]byte, tsMaxBlock+1))), ErrSnappyTooLarge},
		{"chunk 0x02", cat(sid, tsChunk(nil, 0x02, nil)), ErrSnappyUnsupported},
		{"chunk 0x7f", cat(goodStream, tsChunk(nil, 0x7f, []byte{1})), ErrSnappyUnsupported},
	}
}

func TestSnappyConvertErrors(t *testing.T) {
	var r SnappyConverter
	for _, c := range snappyErrorCases() {
		t.Run(c.name, func(t *testing.T) {
			_, err := snappyConvert(t, &r, c.in)
			checkSentinel(t, "Convert", err, c.want)
			// The test decoder agrees, so the fuzz differential holds.
			_, terr := tsDecode(c.in)
			if want := map[error]error{ErrSnappyCorrupt: errTSCorrupt, ErrSnappyTooLarge: errTSTooLarge,
				ErrSnappyUnsupported: errTSUnsupported}[c.want]; terr != want {
				t.Fatalf("tsDecode: %v, want %v", terr, want)
			}
		})
	}
	t.Run("too long compressed", func(t *testing.T) {
		// Longer than any valid chunk: rejected once its start is read.
		body := make([]byte, tsMaxChunk)
		body[0] = 1 // varint 1, then zeros: 1-byte literals
		in := tsChunk(tsStreamID(nil), tsChunkCompressed, append(make([]byte, 4), body...))
		_, err := snappyConvert(t, &r, in)
		checkSentinel(t, "Convert", err, ErrSnappyCorrupt)
		if _, terr := tsDecode(in); terr != errTSCorrupt {
			t.Fatalf("tsDecode: %v", terr)
		}
		// A chunk 4 bytes too long whose first snappyMaxChunk bytes are a
		// valid chunk and whose rest is a padding chunk header.
		raw := make([]byte, tsMaxBlock)
		blk := []byte{0x80, 0x80, 0x84, 0x80, 0x00} // varint 65536, 5 bytes
		for range raw {
			blk = append(blk, 63<<2, 0, 0, 0, 0, 0) // 1-byte literal, 4-byte length
		}
		// The largest valid chunk itself converts.
		in = tsDataChunk(tsStreamID(nil), tsChunkCompressed, raw, blk)
		out, err := snappyConvert(t, &r, in)
		if err != nil {
			t.Fatalf("largest chunk: %v", err)
		}
		checkSnappyOutput(t, newTestDecoder(t), out, raw)
		in = tsDataChunk(tsStreamID(nil), tsChunkCompressed, raw, append(blk, tsChunkPadding, 0, 0, 0))
		if len(in) != 10+4+tsMaxChunk+4 {
			t.Fatalf("chunk is %d bytes", len(in)-10)
		}
		_, err = snappyConvert(t, &r, in)
		checkSentinel(t, "Convert", err, ErrSnappyCorrupt)
		if _, terr := tsDecode(in); terr != errTSCorrupt {
			t.Fatalf("tsDecode: %v", terr)
		}
		body[0], body[1], body[2] = 0x81, 0x80, 0x04 // varint 65537
		in = tsChunk(tsStreamID(nil), tsChunkCompressed, append(make([]byte, 4), body...))
		_, err = snappyConvert(t, &r, in[:len(in)-1]) // truncated, but too large wins
		checkSentinel(t, "Convert", err, ErrSnappyTooLarge)
		if _, terr := tsDecode(in[:len(in)-1]); terr != errTSTooLarge {
			t.Fatalf("tsDecode: %v", terr)
		}
	})
	t.Run("short write", func(t *testing.T) {
		// shortWriter (stream_test.go) takes half of each Write, no error.
		cw := &countWriter{w: shortWriter{}}
		n, err := r.Convert(bytes.NewReader(tsEncode([]byte("data"), tsEncodeOpts{})), cw)
		if err != io.ErrShortWrite || n != cw.n || n != 3 {
			t.Fatalf("got %d (wrote %d), %v, want 3, io.ErrShortWrite", n, cw.n, err)
		}
	})
	t.Run("reader error", func(t *testing.T) {
		errRead := errors.New("read failed")
		_, err := r.Convert(io.MultiReader(bytes.NewReader(tsStreamID(nil)), errReader{errRead}), io.Discard)
		if err != errRead {
			t.Fatalf("got %v, want %v", err, errRead)
		}
	})
	t.Run("writer error", func(t *testing.T) {
		n, err := r.Convert(bytes.NewReader(tsEncode([]byte("data"), tsEncodeOpts{})), errWriter{})
		if err == nil || err.Error() != "write failed" || n != 0 {
			t.Fatalf("got %d, %v, want write failed", n, err)
		}
	})
}

// testSnappyErrorsIs is TestErrorsIs' Snappy part: one stream per Snappy
// sentinel of the mapping table.
func testSnappyErrorsIs(t *testing.T) {
	var r SnappyConverter
	for _, name := range []string{"bad CRC", "too large compressed", "chunk 0x02"} {
		for _, c := range snappyErrorCases() {
			if c.name == name {
				_, err := r.Convert(bytes.NewReader(c.in), io.Discard)
				checkSentinel(t, name, err, c.want)
			}
		}
	}
}

// TestSnappyConverterReuse converts after every kind of error with the same
// converter and requires the same output as a fresh one.
func TestSnappyConverterReuse(t *testing.T) {
	in := bytes.Repeat([]byte("reuse me, reuse me twice; "), 5000)
	s := tsEncode(in, tsEncodeOpts{seed: 9, extra: true})
	var fresh SnappyConverter
	want, err := snappyConvert(t, &fresh, s)
	if err != nil {
		t.Fatal(err)
	}
	d := newTestDecoder(t, WithDecoderConcurrency(1))
	checkSnappyOutput(t, d, want, in)
	var r SnappyConverter
	for _, c := range snappyErrorCases() {
		// A good prefix first, so the error leaves entropy state behind.
		bad := append(tsEncode(in[:30000], tsEncodeOpts{seed: 4}), c.in...)
		if _, err := tsDecode(bad); err == nil {
			continue // "no stream id" is fine after a stream
		}
		if _, err := snappyConvert(t, &r, bad); err == nil {
			t.Fatalf("%s: no error", c.name)
		}
		got, err := snappyConvert(t, &r, s)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("after %s: %v, output differs", c.name, err)
		}
	}
}

// TestSnappyConvertAllocs bounds memory on hostile chunk lengths: a
// converter allocates its fixed buffers once, whatever lengths the chunks
// claim, and skips skippable chunks without buffering them.
func TestSnappyConvertAllocs(t *testing.T) {
	var r SnappyConverter
	if _, err := r.Convert(bytes.NewReader(tsStreamID(nil)), io.Discard); err != nil {
		t.Fatal(err)
	}
	huge := func(typ byte) []byte { return append(tsStreamID(nil), typ, 0xff, 0xff, 0xff) }
	cases := []struct {
		name string
		in   io.Reader
	}{
		{"compressed 16MiB claim", bytes.NewReader(huge(tsChunkCompressed))},
		{"compressed 16MiB", io.MultiReader(bytes.NewReader(huge(tsChunkCompressed)), &zeroReader{n: 1<<24 - 1})},
		{"uncompressed 16MiB claim", bytes.NewReader(huge(tsChunkUncompressed))},
		{"skippable 16MiB", io.MultiReader(bytes.NewReader(huge(0x80)), &zeroReader{n: 1<<24 - 1})},
		{"padding 16MiB x4", io.MultiReader(bytes.NewReader(huge(0xfe)), &zeroReader{n: 1<<24 - 1},
			bytes.NewReader([]byte{0xfe, 0xff, 0xff, 0xff}), &zeroReader{n: 1<<24 - 1},
			bytes.NewReader([]byte{0xfe, 0xff, 0xff, 0xff}), &zeroReader{n: 1<<24 - 1},
			bytes.NewReader([]byte{0xfe, 0xff, 0xff, 0xff}), &zeroReader{n: 1<<24 - 1})},
	}
	for _, c := range cases {
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		_, _ = r.Convert(c.in, io.Discard)
		runtime.ReadMemStats(&m1)
		if d := m1.TotalAlloc - m0.TotalAlloc; d > 64<<10 {
			t.Errorf("%s: allocated %d bytes", c.name, d)
		}
	}
	// A fresh converter's buffers are fixed: about 0.85 MiB.
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	var fresh SnappyConverter
	_, _ = fresh.Convert(bytes.NewReader(huge(tsChunkCompressed)), io.Discard)
	runtime.ReadMemStats(&m1)
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 1<<20 {
		t.Errorf("fresh converter allocated %d bytes", d)
	}
}

// zeroReader reads n zero bytes.
type zeroReader struct{ n int }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.n == 0 {
		return 0, io.EOF
	}
	k := min(len(p), z.n)
	clear(p[:k])
	z.n -= k
	return k, nil
}

// FuzzSnappyConvert requires Convert not to panic, to agree with the test
// decoder on accepting the stream (and on the sentinel when not), and the
// output of an accepted stream to decode with our Decoder to the stream's
// content.
func FuzzSnappyConvert(f *testing.F) {
	for i, s := range [][]byte{
		[]byte("hello hello hello hello"),
		bytes.Repeat([]byte("abcdefgh"), 3000),
		make([]byte, 70000),
	} {
		f.Add(tsEncode(s, tsEncodeOpts{seed: uint64(i), extra: i == 1}))
	}
	for _, c := range snappyErrorCases() {
		f.Add(c.in)
	}
	d := newTestDecoder(f, WithDecoderConcurrency(1))
	var r SnappyConverter
	sentinel := map[error]error{errTSCorrupt: ErrSnappyCorrupt, errTSTooLarge: ErrSnappyTooLarge,
		errTSUnsupported: ErrSnappyUnsupported}
	f.Fuzz(func(t *testing.T, data []byte) {
		want, werr := tsDecode(data)
		var buf bytes.Buffer
		n, err := r.Convert(bytes.NewReader(data), &buf)
		if n != int64(buf.Len()) {
			t.Fatalf("Convert returned %d, wrote %d", n, buf.Len())
		}
		if werr != nil {
			if err != sentinel[werr] {
				t.Fatalf("Convert: %v, test decoder: %v", err, werr)
			}
			return
		}
		if err != nil {
			t.Fatalf("Convert: %v, test decoder accepts", err)
		}
		if len(data) == 0 {
			if buf.Len() != 0 {
				t.Fatalf("empty input wrote %d bytes", buf.Len())
			}
			return
		}
		got, err := d.DecodeAll(buf.Bytes(), nil)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("Decoder: %v, %d bytes, want %d", err, len(got), len(want))
		}
	})
}

// tsLits appends b as literal elements of at most 60 bytes.
func tsLits(dst, b []byte) []byte {
	for len(b) > 0 {
		n := min(len(b), 60)
		dst = append(dst, byte((n-1)<<2))
		dst = append(dst, b[:n]...)
		b = b[n:]
	}
	return dst
}

// tsLitChunk appends a compressed chunk holding a as literals only.
func tsLitChunk(dst, a []byte) []byte {
	return tsDataChunk(dst, tsChunkCompressed, a, tsLits(binary.AppendUvarint(nil, uint64(len(a))), a))
}

// TestSnappyRawFallbackKeepsTables: a chunk whose literals Huffman-compress
// but whose block falls back to raw must not leave its tables for the next
// chunk to repeat (C: the swap happens only for an emitted compressed
// block). Chunk A is slightly compressible literals, chunk B 1000
// literals, which strategy fast would code with a repeated table.
func TestSnappyRawFallbackKeepsTables(t *testing.T) {
	d := newTestDecoder(t, WithDecoderConcurrency(1))
	var r SnappyConverter
	raw := 0
	for trial := range 600 {
		rng := rand.New(rand.NewPCG(uint64(trial), 77))
		q := 0.01 + 0.04*rng.Float64()
		alpha := 230 + rng.IntN(26)
		sym := func() byte {
			if rng.Float64() < q {
				return 'a'
			}
			return byte(rng.IntN(alpha))
		}
		a := make([]byte, 20000+rng.IntN(45537))
		for i := range a {
			a[i] = sym()
		}
		b := make([]byte, 1000)
		for i := range b {
			b[i] = 'a'
			if i%10 == 0 {
				b[i] = byte(rng.IntN(alpha))
			}
		}
		s := tsLitChunk(tsStreamID(nil), a)
		s = tsLitChunk(s, b)
		out, err := snappyConvert(t, &r, s)
		if err != nil {
			t.Fatal(err)
		}
		aRaw := (out[6]>>1)&3 == btRaw
		want := append(append([]byte(nil), a...), b...)
		got, err := d.DecodeAll(out, nil)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("trial %d (A raw %v): %v", trial, aRaw, err)
		}
		if aRaw {
			if raw < 3 {
				if got := creftest.DecodeC(t, out); !bytes.Equal(got, want) {
					t.Fatalf("trial %d: C decodes %d bytes, want %d", trial, len(got), len(want))
				}
			}
			raw++
		}
	}
	if raw == 0 {
		t.Fatal("chunk A was never stored raw")
	}
}
