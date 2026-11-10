package flate_test

// This file is shared: flate/writer_test.go, gzip/writer_test.go and
// zlib/writer_test.go are identical apart from the package clause and the
// import path of the package under test (pkg). The format-specific parts
// (newWriter, stdReader, stdWriter, streamWriter) are in each package's
// writer_format_test.go, ldFormat and stdDecode in format_test.go.
// TestWriterSharedFileInSync checks the copies.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkg "github.com/vahid-sohrabloo/fastcompress/flate"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// writerChunk is the Writer's documented chunk size.
const writerChunk = 1 << 20

// writerLevels are every level a Writer accepts.
var writerLevels = []int{pkg.HuffmanOnly, pkg.DefaultCompression, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

// streamData returns n bytes of text-like data with repeats at all
// distances up to 32 KiB and beyond, plus incompressible and zero runs.
func streamData(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 99))
	b := make([]byte, 0, n+400)
	words := []string{"stream ", "chunk ", "deflate ", "history ", "window ", "flush ", "the ", "a ", "\n"}
	for len(b) < n {
		switch r.IntN(10) {
		case 0:
			for k := r.IntN(2000); k > 0; k-- {
				b = append(b, byte(r.Uint32()))
			}
		case 1:
			b = append(b, make([]byte, r.IntN(3000))...)
		case 2, 3:
			if len(b) > 0 {
				d := min(len(b), 1+r.IntN(40000))
				for k := 3 + r.IntN(300); k > 0; k-- {
					b = append(b, b[len(b)-d])
				}
			}
		default:
			for k := r.IntN(30); k > 0; k-- {
				b = append(b, words[r.IntN(len(words))]...)
			}
		}
	}
	return b[:n]
}

// mustWriter returns newWriter(w, level) or fails.
func mustWriter(t testing.TB, w io.Writer, level int) streamWriter {
	t.Helper()
	z, err := newWriter(w, level)
	if err != nil {
		t.Fatalf("level %d: %v", level, err)
	}
	return z
}

// writeAll writes data in pieces of random sizes (0 included), flushing
// with probability flushProb after each piece, then closes.
func writeAll(t testing.TB, z streamWriter, r *rand.Rand, data []byte, maxPiece int, flushProb float64) {
	t.Helper()
	for len(data) > 0 {
		k := min(len(data), r.IntN(maxPiece+1))
		n, err := z.Write(data[:k])
		if n != k || err != nil {
			t.Fatalf("Write(%d bytes) = %d, %v", k, n, err)
		}
		data = data[k:]
		if r.Float64() < flushProb {
			if err := z.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
		}
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// checkStream decodes stream with the standard library and our
// whole-buffer Decompressor (and, if withLD, libdeflate).
func checkStream(t testing.TB, stream, want []byte, withLD bool) {
	t.Helper()
	got, err := stdDecode(stream)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("stdlib reader: %d bytes, err %v, equal %v (want %d bytes)", len(got), err, bytes.Equal(got, want),
			len(want))
	}
	ours, err := pkg.NewDecompressor().AppendDecompress(nil, stream, len(want))
	if err != nil || !bytes.Equal(ours, want) {
		t.Fatalf("our Decompressor: %d bytes, err %v", len(ours), err)
	}
	if withLD {
		res, nIn, _, out := creftest.LDDecompressEx(t, stream, len(want), ldFormat)
		if res != creftest.LDOK || nIn != len(stream) || !bytes.Equal(out, want) {
			t.Fatalf("libdeflate: result %d, in %d of %d, equal %v", res, nIn, len(stream), bytes.Equal(out, want))
		}
	}
}

// Round trips at every level, with random Write sizes and Flush at random
// points, decoded by the standard library, our Decompressor and libdeflate.
func TestWriterRoundTrip(t *testing.T) {
	sizes := []int{0, 1, 100, 70000, writerChunk, writerChunk + 1, 2*writerChunk + 12345}
	for _, level := range writerLevels {
		for i, n := range sizes {
			if (testing.Short() || raceEnabled) && n > writerChunk && level >= 10 {
				continue
			}
			if raceEnabled && n > 70000 {
				continue
			}
			data := streamData(uint64(i), n)
			for _, mode := range []struct {
				maxPiece  int
				flushProb float64
			}{{n + 1, 0}, {5000, 0.02}, {300000, 0.3}} {
				var b bytes.Buffer
				z := mustWriter(t, &b, level)
				r := rand.New(rand.NewPCG(uint64(level+10), uint64(n)))
				writeAll(t, z, r, data, mode.maxPiece, mode.flushProb)
				checkStream(t, b.Bytes(), data, i%2 == 0)
			}
		}
	}
}

// Without Flush, at most one chunk in total is byte-identical to
// libdeflate's whole-buffer compression at levels 0..12 (and -1 = 6), however
// the input is split into Write calls.
func TestWriterMatchesLibdeflate(t *testing.T) {
	sizes := []int{0, 1, 2, 54, 55, 5000, 70000, 300001, writerChunk}
	for level := -1; level <= 12; level++ {
		for i, n := range sizes {
			if (testing.Short() || raceEnabled) && n > 70000 && level >= 10 {
				continue
			}
			data := streamData(uint64(100+i), n)
			ldLevel := level
			if level == pkg.DefaultCompression {
				ldLevel = 6
			}
			want := creftest.LDCompress(t, data, ldLevel, ldFormat)
			for _, maxPiece := range []int{n + 1, 1000, 77777} {
				var b bytes.Buffer
				z := mustWriter(t, &b, level)
				writeAll(t, z, rand.New(rand.NewPCG(uint64(n), 1)), data, maxPiece, 0)
				if !bytes.Equal(b.Bytes(), want) {
					t.Fatalf("level %d, %d bytes, pieces <= %d: %d bytes, libdeflate %d", level, n, maxPiece,
						b.Len(), len(want))
				}
			}
			// The chunk is compressed only once it is full and more data
			// arrives: n-1 bytes, then 1, is still one chunk.
			if n > 1 {
				var b bytes.Buffer
				z := mustWriter(t, &b, level)
				z.Write(data[:n-1])
				z.Write(data[n-1:])
				z.Close()
				if !bytes.Equal(b.Bytes(), want) {
					t.Fatalf("level %d, %d bytes as n-1 and 1: differs from libdeflate", level, n)
				}
			}
		}
	}
}

// Without Flush the output does not depend on how the input is split into
// Write calls, also across chunk boundaries.
func TestWriterSplitInvariance(t *testing.T) {
	n := 3*writerChunk + 999
	if raceEnabled {
		n = writerChunk + 999
	}
	data := streamData(7, n)
	levels := []int{pkg.HuffmanOnly, 0, 1, 6}
	if !testing.Short() {
		levels = append(levels, 9, 12)
	}
	for _, level := range levels {
		var want []byte
		for i, maxPiece := range []int{n, writerChunk, writerChunk - 1, 65536, 4097} {
			var b bytes.Buffer
			z := mustWriter(t, &b, level)
			writeAll(t, z, rand.New(rand.NewPCG(uint64(i), 2)), data, maxPiece, 0)
			if i == 0 {
				want = bytes.Clone(b.Bytes())
				checkStream(t, want, data, false)
			} else if !bytes.Equal(b.Bytes(), want) {
				t.Fatalf("level %d, pieces <= %d: output differs from one Write", level, maxPiece)
			}
		}
		// Exact chunk-sized writes.
		var b bytes.Buffer
		z := mustWriter(t, &b, level)
		for p := data; len(p) > 0; p = p[min(len(p), writerChunk):] {
			if _, err := z.Write(p[:min(len(p), writerChunk)]); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil || !bytes.Equal(b.Bytes(), want) {
			t.Fatalf("level %d chunk-sized writes: err %v, equal %v", level, err, bytes.Equal(b.Bytes(), want))
		}
	}
}

// After each Flush everything written so far decodes (the stdlib reader
// returns it before reporting the unexpected end), and a Flush with nothing
// buffered still writes a sync flush.
func TestWriterFlush(t *testing.T) {
	data := streamData(8, 400000)
	for _, level := range []int{pkg.HuffmanOnly, 0, 1, 6, 12} {
		var b bytes.Buffer
		z := mustWriter(t, &b, level)
		r := rand.New(rand.NewPCG(uint64(level+3), 3))
		written := 0
		for written < len(data) {
			k := min(len(data)-written, r.IntN(60000))
			if _, err := z.Write(data[written : written+k]); err != nil {
				t.Fatal(err)
			}
			written += k
			if err := z.Flush(); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasSuffix(b.Bytes(), []byte{0, 0, 0xff, 0xff}) {
				t.Fatalf("level %d: Flush did not end with a sync flush", level)
			}
			sr, err := stdReader(bytes.NewReader(b.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, written)
			if _, err := io.ReadFull(sr, got); err != nil || !bytes.Equal(got, data[:written]) {
				t.Fatalf("level %d after %d bytes: flushed data does not decode: %v", level, written, err)
			}
			before := b.Len()
			if err := z.Flush(); err != nil || b.Len() != before+5 {
				t.Fatalf("level %d: empty Flush wrote %d bytes, err %v", level, b.Len()-before, err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		checkStream(t, b.Bytes(), data, false)
	}
}

// Zero-byte writes write nothing and change nothing.
func TestWriterZeroWrites(t *testing.T) {
	data := streamData(9, 5000)
	var want bytes.Buffer
	z := mustWriter(t, &want, 6)
	if _, err := z.Write(data); err != nil {
		t.Fatal(err)
	}
	z.Close()
	var b bytes.Buffer
	z = mustWriter(t, &b, 6)
	z.Write(nil)
	header := b.Len() // gzip and zlib write their header on the first Write
	for _, p := range [][]byte{nil, {}, data[:10], nil, data[10:], {}} {
		if n, err := z.Write(p); n != len(p) || err != nil {
			t.Fatalf("Write(%d) = %d, %v", len(p), n, err)
		}
	}
	if b.Len() != header {
		t.Fatalf("%d bytes written before Close", b.Len()-header)
	}
	z.Close()
	if !bytes.Equal(b.Bytes(), want.Bytes()) {
		t.Fatal("zero-byte writes changed the output")
	}
}

// Reset makes a used Writer (also one in the middle of a stream, closed or
// failed) produce exactly what a new one does.
func TestWriterReset(t *testing.T) {
	a := streamData(10, writerChunk+50000)
	bdata := streamData(11, 300000)
	for _, level := range []int{pkg.HuffmanOnly, 0, 1, 6, 10} {
		var want bytes.Buffer
		writeAll(t, mustWriter(t, &want, level), rand.New(rand.NewPCG(1, 1)), bdata, 10000, 0.01)

		var sink bytes.Buffer
		z := mustWriter(t, &sink, level)
		z.Write(a)
		z.Flush()
		z.Write(a[:1000]) // pending data, discarded by Reset
		for round := range 3 {
			var got bytes.Buffer
			z.Reset(&got)
			writeAll(t, z, rand.New(rand.NewPCG(1, 1)), bdata, 10000, 0.01)
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("level %d round %d: Reset Writer differs from a new one", level, round)
			}
		}
		// After a sticky error.
		z.Reset(&failWriter{})
		if _, err := z.Write(a); err == nil {
			t.Fatal("no error from a failing writer")
		}
		var got bytes.Buffer
		z.Reset(&got)
		writeAll(t, z, rand.New(rand.NewPCG(1, 1)), bdata, 10000, 0.01)
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("level %d: Reset after an error differs", level)
		}
	}
}

var errFail = errors.New("test: write failed")

// failWriter accepts ok writes, then fails every write.
type failWriter struct {
	ok, calls int
	b         bytes.Buffer
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.ok <= 0 {
		return 0, errFail
	}
	f.ok--
	return f.b.Write(p)
}

// shortWriter writes half of each buffer and reports no error.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

// An error of the underlying writer is returned by the call that hit it
// and by every later Write, Flush and Close; nothing more is written.
func TestWriterStickyError(t *testing.T) {
	data := streamData(12, 3*writerChunk)
	for ok := range 6 {
		f := &failWriter{ok: ok}
		z := mustWriter(t, f, 1)
		var first error
		for i := 0; i < 8 && first == nil; i++ {
			if n, err := z.Write(data[:writerChunk]); err != nil {
				if n != 0 {
					t.Fatalf("ok=%d: failed Write returned n=%d", ok, n)
				}
				first = err
			} else if i%2 == 1 {
				first = z.Flush()
			}
		}
		if first == nil {
			first = z.Close()
		}
		if !errors.Is(first, errFail) {
			t.Fatalf("ok=%d: first error %v", ok, first)
		}
		calls := f.calls
		if n, err := z.Write(data[:10]); n != 0 || err != first {
			t.Fatalf("ok=%d: Write after error = %d, %v", ok, n, err)
		}
		if err := z.Flush(); err != first {
			t.Fatalf("ok=%d: Flush after error = %v", ok, err)
		}
		if err := z.Close(); err != first {
			t.Fatalf("ok=%d: Close after error = %v", ok, err)
		}
		if err := z.Close(); err != first {
			t.Fatalf("ok=%d: second Close after error = %v", ok, err)
		}
		if f.calls != calls {
			t.Fatalf("ok=%d: %d writes after the error", ok, f.calls-calls)
		}
	}
	// A short write without an error is io.ErrShortWrite, also sticky.
	z := mustWriter(t, shortWriter{}, 6)
	z.Write(data[:1000])
	if err := z.Flush(); err != io.ErrShortWrite {
		t.Fatalf("short write: Flush = %v", err)
	}
	if err := z.Close(); err != io.ErrShortWrite {
		t.Fatalf("short write: Close = %v", err)
	}
	z = mustWriter(t, shortWriter{}, 6)
	if err := z.Close(); err != io.ErrShortWrite {
		t.Fatalf("short write: Close of an empty stream = %v", err)
	}
}

// The results of Write, Write(nil), Flush, Close and Reset in any order,
// before and after Close, are the standard library's (errors compared by
// text); nothing is written after a Close until the next Reset.
func TestWriterLifecycleMatchesStdlib(t *testing.T) {
	seqs := []string{"C", "CC", "CFF", "CFC", "CF0", "C0", "C0C", "C0F", "CW", "CWC", "CWF", "CW0", "WCWCF",
		"WCWFC", "WC0F", "CWF0C", "CFCW", "WFC0", "0F0C", "0", "0C", "F", "FC", "CR", "CRW", "CRC", "CWRWC", "WCRF0C",
		"R", "RC", "WRW"}
	r := rand.New(rand.NewPCG(15, 15))
	for range 2000 {
		var b strings.Builder
		for k := 1 + r.IntN(7); k > 0; k-- {
			b.WriteByte("WW0FCR"[r.IntN(6)])
		}
		seqs = append(seqs, b.String())
	}
	run := func(z streamWriter, sink *bytes.Buffer, seq string) (res []string, wroteAfterClose bool) {
		closed := false
		for _, op := range seq {
			before := sink.Len()
			var err error
			n := -1
			switch op {
			case 'W':
				n, err = z.Write([]byte("hello"))
			case '0':
				n, err = z.Write(nil)
			case 'F':
				err = z.Flush()
			case 'C':
				err = z.Close()
			case 'R':
				z.Reset(sink)
			}
			if closed && sink.Len() != before {
				wroteAfterClose = true
			}
			closed = (closed || op == 'C') && op != 'R'
			res = append(res, fmt.Sprintf("%c:%d:%v", op, n, err))
		}
		return res, wroteAfterClose
	}
	for _, seq := range seqs {
		var ob, sb bytes.Buffer
		ours, wrote := run(mustWriter(t, &ob, 6), &ob, seq)
		sz, err := stdWriter(&sb, 6)
		if err != nil {
			t.Fatal(err)
		}
		std, _ := run(sz, &sb, seq)
		if strings.Join(ours, " ") != strings.Join(std, " ") {
			t.Fatalf("%s: ours %v, stdlib %v", seq, ours, std)
		}
		if wrote {
			t.Fatalf("%s: wrote after Close", seq)
		}
	}
	// A closed stream decodes.
	var b bytes.Buffer
	z := mustWriter(t, &b, 6)
	z.Write([]byte("hello"))
	z.Close()
	z.Write([]byte("more"))
	z.Flush()
	z.Close()
	checkStream(t, b.Bytes(), []byte("hello"), false)
}

// Invalid levels are rejected with an error wrapping ErrInvalidLevel whose
// text is the standard library's (with MaxLevel in flate's range).
func TestWriterLevels(t *testing.T) {
	for _, level := range []int{-3, 13, 100} {
		_, err := newWriter(io.Discard, level)
		if !errors.Is(err, pkg.ErrInvalidLevel) {
			t.Errorf("level %d: err %v", level, err)
			continue
		}
		_, stdErr := stdWriter(io.Discard, level)
		if got := strings.Replace(err.Error(), fmt.Sprint(pkg.MaxLevel)+"]", "9]", 1); got != stdErr.Error() {
			t.Errorf("level %d: %q, stdlib %q", level, err, stdErr)
		}
	}
	for _, level := range writerLevels {
		if _, err := newWriter(io.Discard, level); err != nil {
			t.Errorf("level %d: %v", level, err)
		}
	}
}

// After a Flush the history still holds the older data: with many small
// Flushes, data repeated from up to 32 KiB back still compresses to
// almost nothing.
func TestWriterHistoryAcrossFlush(t *testing.T) {
	r := rand.New(rand.NewPCG(16, 16))
	x := make([]byte, 20000)
	for i := range x {
		x[i] = byte(r.Uint32())
	}
	for _, level := range []int{1, 6, 12} {
		var b bytes.Buffer
		z := mustWriter(t, &b, level)
		for p := x; len(p) > 0; p = p[min(len(p), 2000):] { // 10 small Flushes
			z.Write(p[:min(len(p), 2000)])
			z.Flush()
		}
		z.Write([]byte("0123456789"))
		z.Flush()
		mark := b.Len()
		z.Write(x) // all of it is in the last 32 KiB
		z.Flush()
		if n := b.Len() - mark; n > 400 {
			t.Fatalf("level %d: repeating 20000 bytes after small Flushes cost %d bytes: history lost", level, n)
		}
		z.Close()
		checkStream(t, b.Bytes(), append(append(bytes.Clone(x), "0123456789"...), x...), false)
	}
}

// HuffmanOnly uses no matches: all-equal input still costs >= 1 bit/byte.
func TestWriterHuffmanOnly(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 200000)
	var b bytes.Buffer
	writeAll(t, mustWriter(t, &b, pkg.HuffmanOnly), rand.New(rand.NewPCG(1, 2)), data, 70000, 0)
	if b.Len() < len(data)/8 {
		t.Fatalf("HuffmanOnly: %d bytes for %d equal bytes", b.Len(), len(data))
	}
	checkStream(t, b.Bytes(), data, true)
}

// Write, Flush, Close and Reset do not allocate once the Writer's buffers
// have grown.
func TestWriterAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	data := streamData(13, 2*writerChunk+5000)
	for _, level := range []int{pkg.HuffmanOnly, 0, 1, 6, 12} {
		z := mustWriter(t, io.Discard, level)
		run := func() {
			z.Reset(io.Discard)
			for p := data; len(p) > 0; p = p[min(len(p), 100000):] {
				z.Write(p[:min(len(p), 100000)])
				if len(p) < 300000 {
					z.Flush()
				}
			}
			z.Flush()
			z.Close()
		}
		run()
		if a := testing.AllocsPerRun(2, run); a != 0 {
			t.Errorf("level %d: %v allocs per stream", level, a)
		}
	}
}

// FuzzWriterRoundTrip: data[0] picks the level, data[1:9] seeds the Write
// sizes and Flush points; the stream must decode to the input.
func FuzzWriterRoundTrip(f *testing.F) {
	f.Add([]byte{0})
	f.Add(append([]byte{7, 1, 2, 3, 4, 5, 6, 7, 8}, streamData(1, 5000)...))
	f.Add(append([]byte{0, 9, 9, 9, 9, 9, 9, 9, 9}, streamData(2, 70000)...))
	f.Add(append([]byte{14, 0, 0, 0, 0, 0, 0, 0, 1}, bytes.Repeat([]byte("ab"), 1000)...))
	ws := make([]streamWriter, len(writerLevels))
	for i, l := range writerLevels {
		ws[i] = mustWriter(f, io.Discard, l)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 9 {
			return
		}
		var seed uint64
		for _, c := range data[1:9] {
			seed = seed<<8 | uint64(c)
		}
		z := ws[int(data[0])%len(ws)]
		var b bytes.Buffer
		z.Reset(&b)
		in := data[9:]
		writeAll(t, z, rand.New(rand.NewPCG(seed, 3)), in, 1+int(seed%5000), float64(seed%7)/10)
		checkStream(t, b.Bytes(), in, false)
	})
}

// The 32 KiB before a chunk are its history: a second chunk that repeats
// the end of the first compresses to almost nothing.
func TestWriterHistoryAcrossChunks(t *testing.T) {
	r := rand.New(rand.NewPCG(14, 14))
	data := make([]byte, writerChunk+30000)
	for i := range writerChunk {
		data[i] = byte(r.Uint32())
	}
	copy(data[writerChunk:], data[writerChunk-30000:writerChunk])
	levels := []int{1, 6, 12}
	if raceEnabled {
		levels = levels[:1]
	}
	for _, level := range levels {
		var b bytes.Buffer
		writeAll(t, mustWriter(t, &b, level), r, data, writerChunk/3, 0)
		if b.Len() > writerChunk+2000 {
			t.Fatalf("level %d: %d bytes: the second chunk did not use the first as history", level, b.Len())
		}
		checkStream(t, b.Bytes(), data, false)
	}
}

// TestWriterSharedFileInSync: the three copies of this file differ only in
// the package clause and the import of the package under test.
func TestWriterSharedFileInSync(t *testing.T) {
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
	me := norm("writer_test.go")
	for _, other := range []string{"flate", "gzip", "zlib"} {
		if norm(filepath.Join("..", other, "writer_test.go")) != me {
			t.Errorf("../%s/writer_test.go is out of sync with this copy", other)
		}
	}
}
