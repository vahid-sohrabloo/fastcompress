package zstd

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// asyncTestData returns n bytes alternating compressible text and random
// runs, so that blocks of every kind (compressed, raw, RLE-ish) occur.
func asyncTestData(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, 1))
	words := []string{"stream ", "async ", "zstd ", "block ", "window ", "the ", "of ", "frame ", "\n"}
	b := make([]byte, 0, n)
	for len(b) < n {
		k := 1 + r.IntN(50000)
		switch r.IntN(4) {
		case 0: // random
			for range k {
				b = append(b, byte(r.Uint32()))
			}
		case 1: // a run
			c := byte(r.Uint32())
			for range k {
				b = append(b, c)
			}
		default: // text
			for i := 0; i < k; {
				w := words[r.IntN(len(words))]
				b = append(b, w...)
				i += len(w)
			}
		}
	}
	return b[:n]
}

// asyncInputs are the inputs of TestStreamAsyncIdentical: edge sizes,
// block- and asyncChunk-boundary sizes, multi-MB data and, without -short, Silesia files.
func asyncInputs(t *testing.T) ([]string, map[string][]byte) {
	t.Helper()
	names := []string{}
	inputs := map[string][]byte{}
	for _, n := range []int{0, 1, 7, 4095, blockSizeMax - 1, blockSizeMax, blockSizeMax + 1, 3*blockSizeMax + 17, asyncChunk - 1, asyncChunk, asyncChunk + 1, 2 * asyncChunk, 1000003, 5<<20 + 3} {
		name := fmt.Sprintf("gen%d", n)
		names = append(names, name)
		inputs[name] = asyncTestData(n, uint64(n))
	}
	if !testing.Short() {
		for _, f := range []string{"dickens", "mr", "xml", "x-ray"} {
			b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia", f))
			if err != nil {
				continue
			}
			names = append(names, "silesia/"+f)
			inputs["silesia/"+f] = b
		}
	}
	return names, inputs
}

// asyncPattern drives a stream: it writes src through e (Reset to w
// first) and closes it, returning the first error.
type asyncPattern struct {
	name string
	max  int // largest input the pattern runs on; 0 = any
	run  func(e *Encoder, src []byte) error
}

// randomWrites writes src in random sizes in 1..maxChunk, flushing after
// each write with probability 1/flushEvery (never if 0).
func randomWrites(e *Encoder, src []byte, maxChunk, flushEvery int, seed uint64) error {
	r := rand.New(rand.NewPCG(seed, 2))
	for len(src) > 0 {
		n := min(len(src), 1+r.IntN(maxChunk))
		if k, err := e.Write(src[:n]); err != nil || k != n {
			return errors.Join(err, fmt.Errorf("wrote %d of %d", k, n))
		}
		src = src[n:]
		if flushEvery > 0 && r.IntN(flushEvery) == 0 {
			if err := e.Flush(); err != nil {
				return err
			}
		}
	}
	return e.Close()
}

// randReader returns its data in random sizes in 1..max.
type randReader struct {
	b   []byte
	r   *rand.Rand
	max int
}

func (r *randReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), 1+r.r.IntN(r.max))], r.b)
	r.b = r.b[n:]
	return n, nil
}

var asyncPatterns = []asyncPattern{
	{"single", 0, func(e *Encoder, src []byte) error {
		if _, err := e.Write(src); err != nil {
			return err
		}
		return e.Close()
	}},
	{"1byte", 300 << 10, func(e *Encoder, src []byte) error { return randomWrites(e, src, 1, 0, 0) }},
	{"rand", 0, func(e *Encoder, src []byte) error { return randomWrites(e, src, 300000, 0, 3) }},
	{"rand-small-flush", 0, func(e *Encoder, src []byte) error { return randomWrites(e, src, 5000, 40, 4) }},
	{"rand-flush-each", 0, func(e *Encoder, src []byte) error { return randomWrites(e, src, 200000, 1, 5) }},
	{"single-flush", 0, func(e *Encoder, src []byte) error {
		if _, err := e.Write(src); err != nil {
			return err
		}
		if err := e.Flush(); err != nil {
			return err
		}
		if err := e.Flush(); err != nil {
			return err
		}
		return e.Close()
	}},
	{"readfrom", 0, func(e *Encoder, src []byte) error {
		n, err := e.ReadFrom(&randReader{b: src, r: rand.New(rand.NewPCG(6, 0)), max: 100000})
		if err != nil || n != int64(len(src)) {
			return errors.Join(err, fmt.Errorf("ReadFrom read %d of %d", n, len(src)))
		}
		return e.Close()
	}},
	{"write-readfrom-flush", 0, func(e *Encoder, src []byte) error {
		h := len(src) / 3
		if _, err := e.Write(src[:h]); err != nil {
			return err
		}
		if _, err := e.ReadFrom(&randReader{b: src[h : 2*h], r: rand.New(rand.NewPCG(7, 0)), max: 70000}); err != nil {
			return err
		}
		if err := e.Flush(); err != nil {
			return err
		}
		return randomWrites(e, src[2*h:], 90000, 3, 8)
	}},
}

// asyncOptionSet is one encoder configuration of TestStreamAsyncIdentical.
type asyncOptionSet struct {
	name string
	opts []EOption
	dopt []DOption // to decode the output
}

func asyncOptionSets(t *testing.T) []asyncOptionSet {
	dicts := testDicts(t)
	train, raw := dicts[0], dicts[2]
	return []asyncOptionSet{
		{name: "fastest", opts: []EOption{WithEncoderLevel(SpeedFastest)}},
		{name: "default", opts: []EOption{WithEncoderLevel(SpeedDefault)}},
		{name: "better/nocrc", opts: []EOption{WithEncoderLevel(SpeedBetterCompression), WithEncoderCRC(false)}},
		{name: "best", opts: []EOption{WithEncoderLevel(SpeedBestCompression)}},
		{name: "fastest/w64k", opts: []EOption{WithEncoderLevel(SpeedFastest), WithWindowSize(1 << 16)}},
		{name: "default/w1k/nocrc", opts: []EOption{WithEncoderLevel(SpeedDefault), WithWindowSize(1 << 10), WithEncoderCRC(false)}},
		{name: "zlevel5", opts: []EOption{WithEncoderZstdLevel(5)}},
		{name: "default/dict", opts: []EOption{WithEncoderLevel(SpeedDefault), train.option()},
			dopt: []DOption{WithDecoderDicts(train.dict)}},
		{name: "better/rawdict", opts: []EOption{WithEncoderLevel(SpeedBetterCompression), raw.option()},
			dopt: []DOption{WithDecoderDictRaw(raw.id, raw.dict)}},
	}
}

// TestStreamAsyncIdentical checks that the async stream
// (WithEncoderConcurrency(4)) writes the same bytes as the synchronous one
// (WithEncoderConcurrency(1)) for every write pattern, input and option
// set, and that the output decodes to the input. One Encoder of each kind
// per option set is reused through Reset.
func TestStreamAsyncIdentical(t *testing.T) {
	names, inputs := asyncInputs(t)
	for _, set := range asyncOptionSets(t) {
		t.Run(set.name, func(t *testing.T) {
			sync := newTestEncoder(t, append([]EOption{WithEncoderConcurrency(1)}, set.opts...)...)
			async := newTestEncoder(t, append([]EOption{WithEncoderConcurrency(4)}, set.opts...)...)
			if sync.async() || !async.async() {
				t.Fatal("WithEncoderConcurrency does not select the stream mode")
			}
			d, err := NewReader(nil, set.dopt...)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var want, got bytes.Buffer
			for _, name := range names {
				src := inputs[name]
				for _, p := range asyncPatterns {
					if p.max > 0 && len(src) > p.max {
						continue
					}
					if testing.Short() && len(src) > 1<<20 && p.name != "rand" && p.name != "readfrom" {
						continue
					}
					want.Reset()
					got.Reset()
					sync.Reset(&want)
					if err := p.run(sync, src); err != nil {
						t.Fatalf("%s %s sync: %v", name, p.name, err)
					}
					async.Reset(&got)
					if err := p.run(async, src); err != nil {
						t.Fatalf("%s %s async: %v", name, p.name, err)
					}
					if !bytes.Equal(got.Bytes(), want.Bytes()) {
						t.Fatalf("%s %s: async output differs from sync (%d vs %d bytes)%s", name, p.name, got.Len(), want.Len(), firstDiff(got.Bytes(), want.Bytes()))
					}
					dec, err := d.DecodeAll(got.Bytes(), nil)
					if err != nil || !bytes.Equal(dec, src) {
						t.Fatalf("%s %s: decoding gives %d bytes, %v; want %d bytes", name, p.name, len(dec), err, len(src))
					}
				}
			}
		})
	}
}

// TestStreamAsyncContentSize checks ResetContentSize on the async stream:
// the right size gives the synchronous frame, a wrong one an error from a
// later call.
func TestStreamAsyncContentSize(t *testing.T) {
	src := asyncTestData(1<<20+5, 9)
	sync := newTestEncoder(t, WithEncoderConcurrency(1))
	async := newTestEncoder(t, WithEncoderConcurrency(4))
	var want, got bytes.Buffer
	sync.ResetContentSize(&want, int64(len(src)))
	async.ResetContentSize(&got, int64(len(src)))
	if err := randomWrites(sync, src, 70000, 5, 1); err != nil {
		t.Fatal(err)
	}
	if err := randomWrites(async, src, 70000, 5, 1); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("pledged size: async output differs from sync%s", firstDiff(got.Bytes(), want.Bytes()))
	}
	for _, pledged := range []int64{int64(len(src)) - 1, int64(len(src)) + 1, 1000} {
		async.ResetContentSize(io.Discard, pledged)
		if err := randomWrites(async, src, 70000, 0, 1); !errors.Is(err, errSrcSizeWrong) {
			t.Errorf("pledged %d: err = %v, want %v", pledged, err, errSrcSizeWrong)
		}
	}
}

// TestStreamAsyncWriterError checks that an error of the io.Writer, which
// the async stream meets on its worker, is returned by a later Write,
// ReadFrom, Flush or Close (at the latest by Close), and then by every
// call until Reset; no goroutine is left behind.
func TestStreamAsyncWriterError(t *testing.T) {
	src := make([]byte, 4<<20) // incompressible: the output is as large
	r := rand.New(rand.NewPCG(11, 0))
	for i := range src {
		src[i] = byte(r.Uint32())
	}
	boom := errors.New("boom")
	base := runtime.NumGoroutine()
	for _, after := range []int{0, 1, 5000, 300000, 1 << 20, 3 << 20} {
		for _, p := range []string{"write", "write-flush", "readfrom", "single"} {
			what := fmt.Sprintf("fail after %d, %s", after, p)
			e := newTestEncoder(t, WithEncoderConcurrency(4), WithEncoderLevel(SpeedFastest))
			e.Reset(&failWriter{n: after, err: boom})
			var err error
			switch p {
			case "write", "write-flush":
				flush := 0
				if p == "write-flush" {
					flush = 4
				}
				err = randomWrites(e, src, 40000, flush, uint64(after))
			case "readfrom":
				if _, err = e.ReadFrom(&randReader{b: src, r: rand.New(rand.NewPCG(1, 0)), max: 50000}); err == nil {
					err = e.Close()
				}
			case "single":
				if _, err = e.Write(src); err == nil {
					err = e.Close()
				}
			}
			if !errors.Is(err, boom) {
				t.Fatalf("%s: first error = %v, want boom", what, err)
			}
			if _, err := e.Write([]byte("x")); err != boom {
				t.Fatalf("%s: Write after the error = %v", what, err)
			}
			if _, err := e.ReadFrom(bytes.NewReader(src[:10])); err != boom {
				t.Fatalf("%s: ReadFrom after the error = %v", what, err)
			}
			if err := e.Flush(); err != boom {
				t.Fatalf("%s: Flush after the error = %v", what, err)
			}
			if err := e.Close(); err != boom {
				t.Fatalf("%s: Close after the error = %v", what, err)
			}
			waitGoroutines(t, what, base)
			// Reset clears the error; the next stream is the synchronous one.
			var got, want bytes.Buffer
			e.Reset(&got)
			if err := randomWrites(e, src[:1<<20], 30000, 0, 2); err != nil {
				t.Fatalf("%s: stream after Reset: %v", what, err)
			}
			s := newTestEncoder(t, WithEncoderConcurrency(1), WithEncoderLevel(SpeedFastest))
			s.Reset(&want)
			if err := randomWrites(s, src[:1<<20], 30000, 0, 2); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("%s: stream after Reset differs from sync%s", what, firstDiff(got.Bytes(), want.Bytes()))
			}
		}
	}
}

// panicWriter panics on every Write.
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("panicWriter") }

// TestStreamAsyncWriterPanic checks that a panic of the io.Writer on the
// worker is raised again on the caller's goroutine by a later call.
func TestStreamAsyncWriterPanic(t *testing.T) {
	src := asyncTestData(1<<20, 12)
	e := newTestEncoder(t, WithEncoderConcurrency(4), WithEncoderLevel(SpeedFastest))
	e.Reset(panicWriter{})
	got := func() (r any) {
		defer func() { r = recover() }()
		_ = randomWrites(e, src, 10000, 0, 1)
		return nil
	}()
	if got != "panicWriter" {
		t.Fatalf("recovered %v, want panicWriter", got)
	}
	var buf bytes.Buffer
	e.Reset(&buf)
	if err := randomWrites(e, src, 10000, 0, 1); err != nil {
		t.Fatal(err)
	}
	if dec, err := DecodeTo(nil, buf.Bytes()); err != nil || !bytes.Equal(dec, src) {
		t.Fatalf("stream after the panic does not decode: %v", err)
	}
}

// slowWriter sleeps on every Write, so the worker is still running when
// the caller ends the stream.
type slowWriter struct {
	w io.Writer
	d time.Duration
}

func (w slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.d)
	return w.w.Write(p)
}

// TestStreamAsyncNoGoroutineLeak ends async streams with a worker still
// running in every way (Close, Reset, Reset(nil), ResetWithOptions,
// ResetContentSize, Flush then dropping the Encoder): afterwards no
// goroutine remains. A stream following an abandoned one equals the
// synchronous stream.
func TestStreamAsyncNoGoroutineLeak(t *testing.T) {
	src := asyncTestData(2<<20, 13)
	base := runtime.NumGoroutine()
	var want bytes.Buffer
	s := newTestEncoder(t, WithEncoderConcurrency(1))
	s.Reset(&want)
	if err := randomWrites(s, src, 50000, 0, 1); err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"close", "reset", "reset-nil", "reset-options", "reset-size", "flush-drop", "drop"} {
		e := newTestEncoder(t, WithEncoderConcurrency(4))
		var junk bytes.Buffer
		e.Reset(slowWriter{&junk, 2 * time.Millisecond})
		if _, err := e.Write(src[:len(src)/2]); err != nil {
			t.Fatalf("%s: %v", end, err)
		}
		if !e.s.busy {
			t.Fatalf("%s: no worker after a %d-byte Write", end, len(src)/2)
		}
		switch end {
		case "close":
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			if dec, err := DecodeTo(nil, junk.Bytes()); err != nil || !bytes.Equal(dec, src[:len(src)/2]) {
				t.Fatalf("close: output does not decode: %v", err)
			}
		case "reset":
			e.Reset(io.Discard)
		case "reset-nil":
			e.Reset(nil)
		case "reset-options":
			if err := e.ResetWithOptions(io.Discard, WithEncoderCRC(true)); err != nil {
				t.Fatal(err)
			}
		case "reset-size":
			e.ResetContentSize(io.Discard, 5)
		case "flush-drop":
			if err := e.Flush(); err != nil {
				t.Fatal(err)
			}
		case "drop": // the worker ends with its buffer
		}
		waitGoroutines(t, end, base)
		if end == "drop" || end == "flush-drop" {
			continue
		}
		var got bytes.Buffer
		e.Reset(&got)
		if err := randomWrites(e, src, 50000, 0, 1); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%s: stream after the abandoned one differs from sync%s", end, firstDiff(got.Bytes(), want.Bytes()))
		}
		waitGoroutines(t, end+" (second stream)", base)
	}
}

// TestStreamAsyncAllocs checks that a reused async stream does not
// allocate in steady state, for small and large writes and ReadFrom.
func TestStreamAsyncAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	src := asyncTestData(2<<20, 14)
	e := newTestEncoder(t, WithEncoderConcurrency(4), WithEncoderLevel(SpeedFastest))
	writes := func(size, flushEvery int) error {
		for i, p := 0, src; len(p) > 0; i++ {
			n := min(len(p), size+i%7) // odd sizes, so buffers fill mid-write
			if _, err := e.Write(p[:n]); err != nil {
				return err
			}
			p = p[n:]
			if flushEvery > 0 && i%flushEvery == 0 {
				if err := e.Flush(); err != nil {
					return err
				}
			}
		}
		return e.Close()
	}
	rd := bytes.NewReader(nil)
	var r io.Reader = struct{ io.Reader }{rd} // hides WriteTo
	for _, c := range []struct {
		name string
		run  func() error
	}{
		{"write4k", func() error { return writes(4096, 0) }},
		{"write1m-flush", func() error { return writes(1<<20, 2) }},
		{"readfrom", func() error {
			rd.Reset(src)
			if _, err := e.ReadFrom(r); err != nil {
				return err
			}
			return e.Close()
		}},
	} {
		run := func() {
			e.Reset(io.Discard)
			if err := c.run(); err != nil {
				t.Fatal(err)
			}
		}
		run() // allocate the context and buffers
		if n := testing.AllocsPerRun(5, run); n != 0 {
			t.Errorf("%s: %v allocations per stream, want 0", c.name, n)
		}
	}
}

// asyncSink keeps BenchmarkStreamAsync's hashes live.
var asyncSink byte

// BenchmarkStreamAsync streams 16 MiB through the Writer in 64 KiB chunks
// whose producer does CPU work on each chunk (it hashes the chunk with
// SHA-256 four times before writing it), synchronously
// (WithEncoderConcurrency(1)) and async: async overlaps the producer's
// work with compression. Run it on 2 CPUs (taskset -c 0,1).
func BenchmarkStreamAsync(b *testing.B) {
	src := asyncTestData(16<<20, 15)
	if f, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia", "mozilla")); err == nil && len(f) >= len(src) {
		src = f[:len(src)]
	}
	const chunk = 64 << 10
	for _, level := range []EncoderLevel{SpeedFastest, SpeedDefault} {
		for _, conc := range []int{1, 4} {
			mode := "sync"
			if conc > 1 {
				mode = "async"
			}
			b.Run(level.String()+"/"+mode, func(b *testing.B) {
				e, err := NewWriter(io.Discard, WithEncoderLevel(level), WithEncoderConcurrency(conc))
				if err != nil {
					b.Fatal(err)
				}
				var sum [32]byte
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				for b.Loop() {
					e.Reset(io.Discard)
					for p := src; len(p) > 0; {
						n := min(len(p), chunk)
						for range 4 {
							sum = sha256.Sum256(p[:n])
						}
						asyncSink ^= sum[0]
						if _, err := e.Write(p[:n]); err != nil {
							b.Fatal(err)
						}
						p = p[n:]
					}
					if err := e.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkStreamAsyncCopy is io.Copy of each Silesia file through the
// Writer (its ReadFrom), synchronously and async.
func BenchmarkStreamAsyncCopy(b *testing.B) {
	files, _ := filepath.Glob(filepath.Join("..", "bench", "corpus", "silesia", "*"))
	if len(files) == 0 {
		b.Skip("bench/corpus/silesia missing; run ref/fetch.sh")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		for _, conc := range []int{1, 4} {
			mode := "sync"
			if conc > 1 {
				mode = "async"
			}
			b.Run(filepath.Base(f)+"/"+mode, func(b *testing.B) {
				e, err := NewWriter(io.Discard, WithEncoderConcurrency(conc))
				if err != nil {
					b.Fatal(err)
				}
				rd := bytes.NewReader(nil)
				b.SetBytes(int64(len(src)))
				b.ReportAllocs()
				for b.Loop() {
					rd.Reset(src)
					e.Reset(io.Discard)
					if _, err := io.Copy(e, struct{ io.Reader }{rd}); err != nil {
						b.Fatal(err)
					}
					if err := e.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
