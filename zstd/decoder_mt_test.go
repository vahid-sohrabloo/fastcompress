package zstd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// The multi-threaded stream (WithDecoderConcurrency > 1, decoder_mt.go)
// must give the n == 1 stream's output and error for every input, read in
// every way. Three differences are documented exemptions
// (checkStreamResult, FuzzDecompressStream), all from the n == 1 stream's
// own dependence on how its input is cut, which the pipeline does not
// share (it cuts its input at blocks and never takes the single-pass
// shortcut):
//   - a block that regenerates too much: ErrFrameSizeExceeded on one side,
//     ErrUnexpectedBlockSize on the other (the shortcut's output space is
//     the frame's, the stream's its buffer), the same output;
//   - the error class of an invalid sequence with a dictionary registered
//     (which of C's two sequence decoders runs depends on whether the
//     dictionary is cold, which the shortcut changes);
//   - a match offset beyond a non-single-segment window (the history
//     beyond it depends on where the round buffer wrapped).

// streamOut is one way of reading a stream.
type streamOut struct {
	name string
	run  func(t testing.TB, d *Decoder, data []byte) ([]byte, error)
	// small: only for inputs of at most smallInput bytes
	small bool
}

// equivLimit bounds the output read from mutations (RLE bombs): both sides
// stop after it.
const (
	equivLimit = 1 << 24
	smallInput = 4 << 10
)

// streamOuts are the chunk patterns of the Decoder tests (Task 11/12):
// Read of the whole input with 4096-byte reads, of 1-byte input with
// 1-byte reads (small inputs), of random input chunks with random read
// sizes; WriteTo of the whole input and of 7-byte chunks.
var streamOuts = []streamOut{
	{name: "read-whole", run: func(t testing.TB, d *Decoder, data []byte) ([]byte, error) {
		return readAll(t, d, wholeReader(data), fixedSize(4096), equivLimit)
	}},
	{name: "read-1", small: true, run: func(t testing.TB, d *Decoder, data []byte) ([]byte, error) {
		return readAll(t, d, &chunkReader{b: data, next: fixedSize(1)}, fixedSize(1), 64<<10)
	}},
	{name: "read-random", run: func(t testing.TB, d *Decoder, data []byte) ([]byte, error) {
		// (two sources: S1 reads in its own goroutine)
		rc, rp := rand.New(rand.NewPCG(7, uint64(len(data)))), rand.New(rand.NewPCG(8, uint64(len(data))))
		return readAll(t, d, &chunkReader{b: data, next: func() int { return 1 + rc.IntN(200000) }},
			func() int { return 1 + rp.IntN(2*dStreamOutSize) }, equivLimit)
	}},
	{name: "writeto-whole", run: func(t testing.TB, d *Decoder, data []byte) ([]byte, error) {
		return writeToAll(t, d, wholeReader(data))
	}},
	{name: "writeto-7", run: func(t testing.TB, d *Decoder, data []byte) ([]byte, error) {
		return writeToAll(t, d, &chunkReader{b: data, next: fixedSize(7)})
	}},
}

// equivConcurrencies are the n values compared with n == 1.
func equivConcurrencies() []int {
	ns := []int{2, 4}
	if p := runtime.GOMAXPROCS(0); p > 4 {
		ns = append(ns, p)
	}
	return ns
}

// checkSameStream fails t unless a stream of concurrency n gave the n == 1
// stream's result (got, gerr vs want, werr), up to the documented
// exemptions. limited: the read stopped at an output limit, so only the
// common prefix is compared.
//
// An exemption covers a difference that comes from the n == 1 stream's
// own dependence on how its input is cut, which the pipeline does not
// share, so it is accepted only in its documented class and only when ref
// (the n == 1 stream reading the whole input with 1-byte reads: whole
// blocks, never the single-pass shortcut, as the pipeline) gives exactly
// the n > 1 result.
func checkSameStream(t testing.TB, what string, singleSegment, dicts, limited bool, got []byte, gerr error, want []byte, werr error,
	ref func() ([]byte, error, bool)) (exempt string) {
	t.Helper()
	if limited {
		m := min(len(got), len(want))
		if !bytes.Equal(got[:m], want[:m]) {
			t.Fatalf("%s: outputs differ within %d bytes", what, m)
		}
		return ""
	}
	sameErr := func(a, b error) bool {
		return a == nil && b == nil || a != nil && b != nil && errClass(a) == errClass(b)
	}
	if sameErr(gerr, werr) && bytes.Equal(got, want) {
		return ""
	}
	seqCorrupt := func(err error) bool {
		var ce *corruptError
		return errors.Is(err, ErrWindowSizeTooSmall) || errors.As(err, &ce) && ce.code.cCode() == int(dErrCorruptionDetected)
	}
	switch {
	case !singleSegment && (errors.Is(gerr, ErrWindowSizeTooSmall) || errors.Is(werr, ErrWindowSizeTooSmall)):
		exempt = "window"
	case errors.Is(gerr, ErrUnexpectedBlockSize) && errors.Is(werr, ErrFrameSizeExceeded) && bytes.Equal(got, want):
		exempt = "size" // the shortcut names it ErrFrameSizeExceeded; never the pipeline
	case dicts && seqCorrupt(gerr) && seqCorrupt(werr) && bytes.Equal(got, want):
		exempt = "dict-cold"
	default:
		t.Fatalf("%s: n>1 %d bytes, %v; n=1 %d bytes, %v", what, len(got), gerr, len(want), werr)
	}
	rout, rerr, rlimited := ref()
	if rlimited {
		m := min(len(got), len(rout))
		if !bytes.Equal(got[:m], rout[:m]) {
			t.Fatalf("%s (%s): n>1 differs from n=1 without the shortcut within %d bytes", what, exempt, m)
		}
		return exempt
	}
	if !sameErr(gerr, rerr) || !bytes.Equal(got, rout) {
		t.Fatalf("%s (%s): n>1 %d bytes, %v; n=1 without the shortcut %d bytes, %v", what, exempt, len(got), gerr, len(rout), rerr)
	}
	return exempt
}

// noShortcutRef returns checkSameStream's ref for data, decoded by a
// Decoder of concurrency 1 with opts, output limited to limit.
func noShortcutRef(t testing.TB, data []byte, opts []DOption, limit int) func() ([]byte, error, bool) {
	return func() ([]byte, error, bool) {
		d, err := NewReader(nil, append(slices.Clone(opts), WithDecoderConcurrency(1))...)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		out, err := readAll(t, d, wholeReader(data), fixedSize(1), limit)
		return out, err, len(out) > limit
	}
}

// equivSem bounds the equivStream calls running at once (each holds the
// windows of five Decoders).
var equivSem = make(chan struct{}, 4)

// equivStream decodes data with concurrency 1 and every n of
// equivConcurrencies, in every way of streamOuts, and checks that the
// results are the same. It returns the exemptions used.
func equivStream(t testing.TB, data []byte, opts []DOption) map[string]int {
	t.Helper()
	equivSem <- struct{}{}
	defer func() { <-equivSem }()
	dicts := len(opts) > 0
	newDecoder := func(n int) *Decoder {
		d, err := NewReader(nil, append(slices.Clone(opts), WithDecoderConcurrency(n))...)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d1 := newDecoder(1)
	defer d1.Close()
	var dn []*Decoder
	for _, n := range equivConcurrencies() {
		d := newDecoder(n)
		defer d.Close()
		dn = append(dn, d)
	}
	ex := map[string]int{}
	for _, so := range streamOuts {
		if so.small && len(data) > smallInput {
			continue
		}
		want, werr := so.run(t, d1, data)
		ss := d1.s.dc != nil && d1.s.dc.fParams.singleSegment
		limit := equivLimit
		if so.small {
			limit = 64 << 10
		}
		for i, d := range dn {
			got, gerr := so.run(t, d, data)
			what := fmt.Sprintf("%s n=%d", so.name, equivConcurrencies()[i])
			limited := so.name[:4] == "read" && (len(got) > limit || len(want) > limit)
			if e := checkSameStream(t, what, ss || d.s.dc != nil && d.s.dc.fParams.singleSegment, dicts, limited, got, gerr, want, werr,
				noShortcutRef(t, data, opts, equivLimit)); e != "" {
				ex[e]++
			}
		}
	}
	return ex
}

// TestStreamConcurrencyEquiv compares the n > 1 stream with the n == 1
// stream on the decode corpus, the malicious corpus and 2,000 mutations
// of the decoder seeds (plus splitSeed, whose literals take the split
// placement), each read in every way of streamOuts.
func TestStreamConcurrencyEquiv(t *testing.T) {
	var mu sync.Mutex
	exempt := map[string]int{}
	add := func(ex map[string]int) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range ex {
			exempt[k] += v
		}
	}
	t.Cleanup(func() { t.Logf("exemptions used: %v", exempt) })

	t.Run("corpus", func(t *testing.T) {
		for _, name := range creftest.FrameNames(t) {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := creftest.LoadFrame(t, name)
				ex := equivStream(t, f.Data, frameDictOpts(f))
				if f.CErr == "" && len(ex) > 0 {
					t.Fatalf("valid entry needed exemptions %v", ex)
				}
				add(ex)
			})
		}
	})
	t.Run("malicious", func(t *testing.T) {
		for _, c := range maliciousCases(t) {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				var opts []DOption
				if c.dict != "" {
					if c.rawDict {
						opts = []DOption{WithDecoderDictRaw(0, readTestdata(t, c.dict))}
					} else {
						opts = []DOption{WithDecoderDicts(readTestdata(t, c.dict))}
					}
				}
				add(equivStream(t, c.data, opts))
			})
		}
	})
	t.Run("multiframe", func(t *testing.T) {
		for _, m := range multiFrameStreams(t, 1) {
			t.Run(m.name, func(t *testing.T) {
				t.Parallel()
				if ex := equivStream(t, m.data, m.opts); len(ex) > 0 {
					t.Fatalf("valid stream needed exemptions %v", ex)
				}
			})
		}
	})
	t.Run("multiframe-mutations", func(t *testing.T) {
		var seeds [][]byte
		for _, m := range multiFrameStreams(t, 16) {
			if m.opts == nil {
				seeds = append(seeds, m.data)
			}
		}
		const n = 400
		workers := runtime.GOMAXPROCS(0)
		for w := range workers {
			t.Run(fmt.Sprint(w), func(t *testing.T) {
				t.Parallel()
				for i := w; i < n; i += workers {
					r := rand.New(rand.NewPCG(uint64(i), 0x17))
					data := mutate(r, seeds)
					add(equivStream(t, data, nil))
					if t.Failed() {
						t.Fatalf("mutation %d: %x", i, data)
					}
				}
			})
		}
	})
	t.Run("mutations", func(t *testing.T) {
		seeds := decodeSeeds(t)
		const n = 2000
		workers := runtime.GOMAXPROCS(0)
		for w := range workers {
			t.Run(fmt.Sprint(w), func(t *testing.T) {
				t.Parallel()
				for i := w; i < n; i += workers {
					r := rand.New(rand.NewPCG(uint64(i), 0x13))
					data := mutate(r, seeds)
					add(equivStream(t, data, nil))
					if t.Failed() {
						t.Fatalf("mutation %d: %x", i, data)
					}
				}
			})
		}
	})
}

// multiFrame is a stream of several frames (multiFrameStreams).
type multiFrame struct {
	name       string
	data, want []byte
	opts       []DOption
}

// multiFrameStreams returns streams of several frames whose decoding needs
// each frame to begin afresh (review I2): frames of text and of one-byte
// runs, whose first match is repcode 1 from the frame's start values,
// after frames that end with other repcodes; streaming-Encoder frames (no
// content size) and EncodeAll ones; a frame with a 1 KiB window (another
// blockSizeMax); skippable frames between; dictionary frames after plain
// ones (with opts registering the dictionary). Sizes are divided by div.
func multiFrameStreams(t testing.TB, div int) []multiFrame {
	t.Helper()
	part := func(i int) []byte {
		if i%2 == 1 {
			return bytes.Repeat([]byte{byte('a' + i)}, (150000+i)/div)
		}
		return malText((200<<10 + i*777) / div)
	}
	streamed := func(src []byte, opts ...EOption) []byte {
		var buf bytes.Buffer
		w, err := NewWriter(&buf, opts...)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(src)
		w.Close()
		return buf.Bytes()
	}
	var ms []multiFrame
	add := func(name string, f func(i int, src []byte) []byte, opts []DOption) {
		m := multiFrame{name: name, opts: opts}
		for i := range 6 {
			src := part(i)
			m.data = f(i, src)
			m.want = append(m.want, src...)
		}
		ms = append(ms, m)
	}
	var data []byte
	add("streamed", func(i int, src []byte) []byte {
		data = append(data[:len(data):len(data)], streamed(src, WithEncoderLevel(SpeedDefault), WithWindowSize(1<<20))...)
		return data
	}, nil)
	data = nil
	enc := newTestEncoder(t, WithEncoderLevel(SpeedDefault))
	small := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithWindowSize(1<<10))
	add("encodeall-windows", func(i int, src []byte) []byte {
		if i == 3 {
			data = small.EncodeAll(src, data)
		} else {
			data = enc.EncodeAll(src, data)
		}
		return data
	}, nil)
	data = nil
	add("skippable", func(i int, src []byte) []byte {
		data = append(data, 0x5A, 0x2A, 0x4D, 0x18, 5, 0, 0, 0, 1, 2, 3, 4, 5)
		data = append(data, streamed(src, WithEncoderLevel(SpeedFastest), WithEncoderCRC(true))...)
		return data
	}, nil)
	dict := readTestdata(t, "train.dict")
	denc := newTestEncoder(t, WithEncoderDict(dict))
	data = nil
	add("dict", func(i int, src []byte) []byte {
		if i%2 == 0 {
			data = denc.EncodeAll(src, data)
		} else {
			data = enc.EncodeAll(src, data)
		}
		return data
	}, []DOption{WithDecoderDicts(dict)})
	return ms
}

// TestStreamMultiFrame decodes multiFrameStreams with concurrency 1, 2 and
// 4, read whole with 4096-byte reads and with WriteTo of 7-byte chunks:
// exactly the input, no error.
func TestStreamMultiFrame(t *testing.T) {
	for _, m := range multiFrameStreams(t, 1) {
		for _, n := range []int{1, 2, 4} {
			d := newTestDecoder(t, append(slices.Clone(m.opts), WithDecoderConcurrency(n))...)
			got, err := readAll(t, d, wholeReader(m.data), fixedSize(4096), 0)
			if err != nil || !bytes.Equal(got, m.want) {
				t.Fatalf("%s n=%d read: %d bytes (want %d), %v", m.name, n, len(got), len(m.want), err)
			}
			got, err = writeToAll(t, d, &chunkReader{b: m.data, next: fixedSize(7)})
			if err != nil || !bytes.Equal(got, m.want) {
				t.Fatalf("%s n=%d writeto: %d bytes (want %d), %v", m.name, n, len(got), len(m.want), err)
			}
		}
	}
}

// scriptReader returns its data in reads of up to step bytes, and after
// failAt bytes the read that reaches it returns err (with the bytes it
// read when withData), or nothing forever (err nil), or an invalid count.
type scriptReader struct {
	b             []byte
	step, failAt  int
	err           error
	withData, bad bool
	pos           int
}

func (r *scriptReader) Read(p []byte) (int, error) {
	n := copy(p[:min(len(p), r.step)], r.b[r.pos:])
	if r.pos+n < r.failAt || r.pos >= len(r.b) && r.failAt > len(r.b) {
		r.pos += n
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
	switch {
	case r.bad:
		return len(p) + 1, nil
	case r.err == nil:
		return 0, nil
	case r.withData:
		r.pos += n
		return n, r.err
	}
	return 0, r.err
}

// TestStreamReaderErrors reads streams whose io.Reader fails (an error
// with or without the bytes of that read, no progress, an invalid count)
// at several points, with concurrency 1 and 4: the same output, then the
// same error.
func TestStreamReaderErrors(t *testing.T) {
	frame, _ := mtTestStream(t, 1<<20, 1<<20)
	errX := errors.New("reader failed")
	for _, failAt := range []int{0, 1, 5, 100, len(frame) / 3, len(frame) - 1, len(frame) + 1} {
		for _, step := range []int{7, 1000, 1 << 20} {
			for _, mode := range []string{"err", "err-data", "no-progress", "bad-count"} {
				var res []string
				var outs [][]byte
				for _, n := range []int{1, 4} {
					r := &scriptReader{b: frame, step: step, failAt: failAt, err: errX,
						withData: mode == "err-data", bad: mode == "bad-count"}
					if mode == "no-progress" {
						r.err = nil
					}
					d := newTestDecoder(t, WithDecoderConcurrency(n))
					got, err := readAll(t, d, r, fixedSize(4096), 0)
					res = append(res, fmt.Sprint(err))
					outs = append(outs, got)
				}
				if res[0] != res[1] || !bytes.Equal(outs[0], outs[1]) {
					t.Fatalf("fail at %d, step %d, %s: n=1 %d bytes %s; n=4 %d bytes %s",
						failAt, step, mode, len(outs[0]), res[0], len(outs[1]), res[1])
				}
			}
		}
	}
}

// TestStreamPipelineOptIn checks when streams are pipelined (controller
// ruling, Task 13 fix round 1): only with an explicit
// WithDecoderConcurrency that resolves to more than 1. The default
// (min(4, GOMAXPROCS) DecodeAll contexts) streams synchronously, as does
// WithDecoderConcurrency(1); WithDecoderConcurrency(0) is GOMAXPROCS.
func TestStreamPipelineOptIn(t *testing.T) {
	frame, _ := mtTestStream(t, 2<<20, 1<<20)
	base := runtime.NumGoroutine()
	for _, c := range []struct {
		name string
		opts []DOption
		pipe bool
	}{
		{"default", nil, false},
		{"n=1", []DOption{WithDecoderConcurrency(1)}, false},
		{"n=2", []DOption{WithDecoderConcurrency(2)}, true},
		{"n=0", []DOption{WithDecoderConcurrency(0)}, runtime.GOMAXPROCS(0) > 1},
	} {
		d := newTestDecoder(t, c.opts...)
		if err := d.Reset(bytes.NewReader(frame)); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Read(make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		if got := d.s.pipe != nil; got != c.pipe {
			t.Fatalf("%s: pipelined %v, want %v", c.name, got, c.pipe)
		}
		if c.pipe && cap(d.s.pipe.units) != min(d.o.concurrent, maxReadAhead) {
			t.Fatalf("%s: read-ahead of %d units", c.name, cap(d.s.pipe.units))
		}
		d.Close()
		waitGoroutines(t, c.name, base)
	}
}

// blockingReader returns first bytes of its data, then blocks in Read
// until release is closed, then returns the rest.
type blockingReader struct {
	b       []byte
	first   int
	pos     int
	release chan struct{}
	blocked chan struct{} // closed when Read first blocks
	once    sync.Once
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.pos >= r.first {
		r.once.Do(func() { close(r.blocked) })
		<-r.release
	}
	end := len(r.b)
	if r.pos < r.first {
		end = r.first
	}
	n := copy(p, r.b[r.pos:end])
	r.pos += n
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// TestStreamStopBlockedReader pins review I4: a pipelined stream whose
// reader blocks in a read-ahead (S1 in Read) is closed, or reset to
// another stream (which then decodes), promptly; S1 exits once the reader
// is released, and the goroutines return to the baseline.
func TestStreamStopBlockedReader(t *testing.T) {
	frame, text := mtTestStream(t, 1<<20, 1<<20)
	base := runtime.NumGoroutine()
	for _, end := range []string{"close", "reset", "reset-nil"} {
		r := &blockingReader{b: frame, first: len(frame) / 2, release: make(chan struct{}), blocked: make(chan struct{})}
		d, err := NewReader(r, WithDecoderConcurrency(4))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Read(make([]byte, 100)); err != nil {
			t.Fatal(err)
		}
		<-r.blocked
		done := make(chan struct{})
		go func() {
			defer close(done)
			switch end {
			case "close":
				d.Close()
			case "reset":
				got, err := readAll(t, d, bytes.NewReader(frame), fixedSize(1<<16), 0)
				if err != nil || !bytes.Equal(got, text) {
					t.Errorf("reset: the new stream gave %d bytes, %v", len(got), err)
				}
			case "reset-nil":
				if err := d.Reset(nil); err != nil {
					t.Error(err)
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: blocked on the reader", end)
		}
		close(r.release)
		d.Close()
		waitGoroutines(t, end, base)
	}
}

// panicReader returns its data in reads of up to step bytes and panics
// with its value once at bytes have been read.
type panicReader struct {
	b        []byte
	step, at int
	pos      int
}

func (r *panicReader) Read(p []byte) (int, error) {
	if r.pos >= r.at {
		panic(fmt.Sprintf("reader panic at %d", r.at))
	}
	n := copy(p[:min(len(p), r.step, r.at-r.pos)], r.b[r.pos:])
	r.pos += n
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

// TestStreamReaderPanic pins review M1: a panic of the io.Reader's Read
// reaches the caller of Read, after the same output, with concurrency 1
// and 4; the pipelined stream has then failed (a further Read returns an
// error) and its goroutines end.
func TestStreamReaderPanic(t *testing.T) {
	frame, _ := mtTestStream(t, 1<<20, 1<<20)
	base := runtime.NumGoroutine()
	for _, at := range []int{0, 10, len(frame) / 2} {
		var outs [][]byte
		var vals []any
		for _, n := range []int{1, 4} {
			d := newTestDecoder(t, WithDecoderConcurrency(n))
			if err := d.Reset(&panicReader{b: frame, step: 5000, at: at}); err != nil {
				t.Fatal(err)
			}
			var out []byte
			p := make([]byte, 4096)
			func() {
				defer func() { vals = append(vals, recover()) }()
				for {
					m, err := d.Read(p)
					out = append(out, p[:m]...)
					if err != nil {
						t.Fatalf("at %d n=%d: %v", at, n, err)
					}
				}
			}()
			outs = append(outs, out)
			if n > 1 {
				// The stream has failed: a further Read returns an error
				// (it does not wait for a pipeline that has ended).
				res := make(chan error, 1)
				go func() { _, err := d.Read(p); res <- err }()
				select {
				case err := <-res:
					if err == nil {
						t.Fatalf("at %d: Read after the panic: no error", at)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("at %d: Read after the panic blocks", at)
				}
			}
			d.Close()
		}
		if vals[0] == nil || vals[0] != vals[1] || !bytes.Equal(outs[0], outs[1]) {
			t.Fatalf("at %d: n=1 %d bytes, panic %v; n=4 %d bytes, panic %v", at, len(outs[0]), vals[0], len(outs[1]), vals[1])
		}
		waitGoroutines(t, fmt.Sprint("panic at ", at), base)
	}
}

// TestStreamPipelineFast streams valid frames (several frames, raw and
// compressed blocks, checksums, a skippable frame, a dictionary) with
// concurrency 4: S2 decodes every compressed block (none is left to S3).
func TestStreamPipelineFast(t *testing.T) {
	text := malText(3 << 20)
	enc := newTestEncoder(t, WithEncoderLevel(SpeedDefault), WithWindowSize(1<<20))
	var in, want []byte
	for i, size := range []int{1 << 20, 100, 0, 3 << 20, 70000} {
		in = enc.EncodeAll(text[:size], in)
		want = append(want, text[:size]...)
		if i == 1 {
			in = append(in, 0x50, 0x2A, 0x4D, 0x18, 3, 0, 0, 0, 1, 2, 3) // skippable
		}
	}
	in = append(in, newTestEncoder(t, WithEncoderLevel(SpeedFastest)).EncodeAll(malRandom(1, 200000), nil)...)
	want = append(want, malRandom(1, 200000)...)
	frame, dict, _ := dictFrame(t, text[:50000])
	in = append(in, frame...)
	want = append(want, text[:50000]...)
	d := newTestDecoder(t, WithDecoderConcurrency(4), WithDecoderDicts(dict))
	fast, sync := pipeFastBlocks.Load(), pipeSyncBlocks.Load()
	got, err := readAll(t, d, wholeReader(in), fixedSize(1<<16), 0)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%v, %d bytes, want %d", err, len(got), len(want))
	}
	if n := pipeSyncBlocks.Load() - sync; n != 0 {
		t.Fatalf("%d blocks decoded by S3", n)
	}
	if n := pipeFastBlocks.Load() - fast; n < 30 {
		t.Fatalf("only %d blocks decoded by S2", n)
	}
}

// TestStreamDictColdState pins the stream context's ddictIsCold under a
// pipeline: a frame with a raw dictionary whose first block (valid, S2
// decodes it) makes the dictionary hot, and whose second block fails one
// way with C's prefetch sequence decoder (cold) and another with the
// regular one (FuzzDecodeAll seed b1ad5558968c6210's block). S3 decodes
// that block itself: the stream must give the n == 1 stream's error
// exactly, as the context's ddictIsCold is the n == 1 stream's.
func TestStreamDictColdState(t *testing.T) {
	seed := []byte("(\xb5/\xfd\xa000\x04\x00T\x00\x00%00\x01\x000001\x020")
	raw := readTestdata(t, "dickens-32k.raw")
	first := craftBlock(t, []byte("hello, world"), nil)
	in := append(slices.Clone(seed[:9]), malCBlock(first, false).bytes()...)
	in = append(in, seed[9:]...)
	var results []string
	for _, n := range []int{1, 4} {
		d := newTestDecoder(t, WithDecoderConcurrency(n), WithDecoderDictRaw(0, raw))
		got, err := readAll(t, d, wholeReader(in), fixedSize(4096), 0)
		if err == nil || string(got) != "hello, world" {
			t.Fatalf("n=%d: %q, %v", n, got, err)
		}
		results = append(results, errClass(err))
	}
	// The same block as the frame's first fails the other way (cold).
	d := newTestDecoder(t, WithDecoderConcurrency(1), WithDecoderDictRaw(0, raw))
	_, err := readAll(t, d, wholeReader(seed), fixedSize(4096), 0)
	t.Logf("second block: %v; first block: %v", results, errClass(err))
	if results[0] != results[1] {
		t.Fatalf("n=1 %s, n=4 %s", results[0], results[1])
	}
	if errClass(err) == results[0] {
		t.Fatal("the block fails the same way cold and hot: the test no longer pins ddictIsCold")
	}
}

// TestDecodeBlockSeqsTooMany checks that S2 leaves to S3 a block with more
// sequences than blockSizeMax/3 (an invalid block: 3 bytes per match at
// least) without decoding them, so the decoded sequences stay bounded.
func TestDecodeBlockSeqsTooMany(t *testing.T) {
	seqs := make([]malSeq, blockSizeMax/3+1)
	for i := range seqs {
		seqs[i] = malSeq{ml: 3, off: 1}
	}
	seqs[0].ll = 1
	body := craftBlock(t, []byte("a"), seqs)
	e := newDCtx()
	e.decompressBegin()
	e.fParams.blockSizeMax = blockSizeMax
	var b decodedBlock
	if e.decodeBlockSeqs(body, make([]byte, blockSizeMax), &b) || len(b.seqs) != 0 {
		t.Fatalf("decoded %d sequences", len(b.seqs))
	}
	seqs = seqs[:len(seqs)-1]
	body = craftBlock(t, []byte("a"), seqs)
	e.decompressBegin()
	e.fParams.blockSizeMax = blockSizeMax
	if !e.decodeBlockSeqs(body, make([]byte, blockSizeMax), &b) || len(b.seqs) != len(seqs) {
		t.Fatalf("decoded %d sequences, want %d", len(b.seqs), len(seqs))
	}
}

// craftBlockOffBase is craftBlock with the sequences' offBase given (so
// repcodes can be written).
func craftBlockOffBase(t testing.TB, literals []byte, seqs []seqDef) []byte {
	t.Helper()
	n := len(seqs)
	ss := seqStore{literals: literals, llCode: make([]byte, n), mlCode: make([]byte, n), ofCode: make([]byte, n), sequences: seqs}
	var prev, next compressedBlockState
	resetCompressedBlockState(&prev)
	resetCompressedBlockState(&next)
	var p cctxParams
	cctxParamsInit(&p, 1)
	p.cParams = getCParams(1, 0, 0)
	dst := make([]byte, len(literals)+16*n+1024)
	sz, err := entropyCompressSeqStoreInternal(dst, literals, &ss, &prev.entropy, &next.entropy, &p,
		make([]byte, entropyWorkspaceSize), false)
	if err != nil || sz == 0 {
		t.Fatalf("craftBlockOffBase: size %d, err %v", sz, err)
	}
	return dst[:sz]
}

// TestStreamS3Checks pins S3's part of a compressed block's checks with
// strict n == 1 vs n == 4 comparisons (no exemption):
//   - literals larger than the output space left (a content-size buffer
//     with 5 bytes left, 20 literals) fail before the sequences, whose
//     first match lies beyond the history;
//   - a block S2 leaves to S3 (too many sequences) starts from the
//     repcodes S2 saved: its first match uses repcode 3 (offset 4 after
//     the first block, 8 from the frame's start values, beyond the 6
//     bytes of history).
func TestStreamS3Checks(t *testing.T) {
	room := malFrame{hdr: malFHdr{fcsSize: 4, fcs: 15}.bytes(), blocks: []malBlock{
		{typ: btRaw, size: 10, body: []byte("0123456789")},
		malCBlock(craftBlock(t, bytes.Repeat([]byte("x"), 20), []malSeq{{ll: 1, ml: 3, off: 500}}), true),
	}}.bytes()
	seqs := []seqDef{{offBase: 3, litLength: 1}}
	for range blockSizeMax / 3 {
		seqs = append(seqs, seqDef{offBase: 4})
	}
	reps := malFrame{hdr: malFHdr{winDesc: malWindow1M}.bytes(), blocks: []malBlock{
		malCBlock(craftBlockOffBase(t, []byte("ab"), []seqDef{{offBase: 2 + 3, litLength: 2}}), false),
		malCBlock(craftBlockOffBase(t, []byte("c"), seqs), true),
	}}.bytes()
	for _, c := range []struct {
		name string
		in   []byte
		want error
		out  string
	}{
		{"literal-room", room, ErrFrameSizeExceeded, "0123456789"},
		{"fallback-reps", reps, ErrUnexpectedBlockSize, "ababa"},
	} {
		for _, n := range []int{1, 4} {
			d := newTestDecoder(t, WithDecoderConcurrency(n))
			got, err := readAll(t, d, wholeReader(c.in), fixedSize(4096), 0)
			if !errors.Is(err, c.want) || string(got) != c.out {
				t.Errorf("%s n=%d: %q, %v; want %q, %v", c.name, n, got, err, c.out, c.want)
			}
		}
	}
}

// TestSplitLitBuffer checks splitLitBuffer against decodeLiteralsBlock's
// placement of splitSeed's literals (streaming, split), for several
// output spaces.
func TestSplitLitBuffer(t *testing.T) {
	frame := splitSeed(t)
	hs, _ := frameHeaderSizeInternal(frame)
	for _, space := range []int{blockSizeMax, 2 * blockSizeMax, 95000, 90001} {
		d := newDCtx()
		d.decompressBegin()
		if e := d.decodeFrameHeader(frame[:hs]); e != 0 {
			t.Fatal(e)
		}
		if _, e := d.decodeLiteralsBlock(frame[hs+blockHeaderSize:], make([]byte, space), space, isStreaming); e != 0 || d.litBufferLocation != split {
			t.Fatalf("space %d: err %d, location %d", space, e, d.litBufferLocation)
		}
		if got := splitLitBuffer(min(d.blockSizeMax(), space), d.litSize); got != d.litBuffer {
			t.Fatalf("space %d: splitLitBuffer %d, decodeLiteralsBlock %d", space, got, d.litBuffer)
		}
	}
}

// decodeSeeds returns the decoder seeds plus splitSeed.
func decodeSeeds(t testing.TB) [][]byte {
	t.Helper()
	ents, err := os.ReadDir(seedsDir)
	if err != nil {
		t.Fatal(err)
	}
	var seeds [][]byte
	for _, e := range ents {
		b, err := os.ReadFile(seedsDir + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	return append(seeds, splitSeed(t))
}

// mtTestStream returns a frame of about size bytes of text at level 1
// (blocks of up to 128 KiB) with window w.
func mtTestStream(t testing.TB, size int, w int) (frame, text []byte) {
	t.Helper()
	text = malText(size)
	e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithWindowSize(w))
	return e.EncodeAll(text, nil), text
}

// waitGoroutines waits up to 1 s for the number of goroutines to fall to
// base.
func waitGoroutines(t *testing.T, what string, base int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("%s: %d goroutines, baseline %d\n%s", what, runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(time.Millisecond)
	}
}

// TestStreamNoGoroutineLeak closes or resets streams of concurrency n
// after 0, 1 and n+3 reads, after an error, at EOF and after a writer
// error in WriteTo: the pipeline's goroutines exit. While a stream is
// read, the pipeline runs (goroutines above the baseline).
func TestStreamNoGoroutineLeak(t *testing.T) {
	const n = 4
	frame, _ := mtTestStream(t, 2<<20, 1<<20)
	bad := slices.Clone(frame)
	bad[len(bad)/2] ^= 0x55
	base := runtime.NumGoroutine()
	for _, reads := range []int{0, 1, n + 3} {
		for _, end := range []string{"close", "reset", "reset-nil", "reset-options"} {
			what := fmt.Sprintf("%d reads, %s", reads, end)
			d, err := NewReader(bytes.NewReader(frame), WithDecoderConcurrency(n))
			if err != nil {
				t.Fatal(err)
			}
			p := make([]byte, 1)
			for i := range reads {
				if _, err := d.Read(p); err != nil {
					t.Fatalf("%s: read %d: %v", what, i, err)
				}
				if runtime.NumGoroutine() <= base {
					t.Fatalf("%s: no pipeline goroutines while reading", what)
				}
			}
			switch end {
			case "close":
				d.Close()
			case "reset":
				if err := d.Reset(bytes.NewReader(frame)); err != nil {
					t.Fatal(err)
				}
			case "reset-nil":
				if err := d.Reset(nil); err != nil {
					t.Fatal(err)
				}
			case "reset-options":
				if err := d.ResetWithOptions(bytes.NewReader(frame), IgnoreChecksum(true)); err != nil {
					t.Fatal(err)
				}
			}
			waitGoroutines(t, what, base)
			d.Close()
		}
	}
	// An error, EOF and a writer error end the pipeline without Close.
	for _, c := range []struct {
		name string
		data []byte
		w    io.Writer
	}{
		{"error", bad, io.Discard},
		{"eof", frame, io.Discard},
		{"writer-error", frame, errWriter{}},
	} {
		d, err := NewReader(bytes.NewReader(c.data), WithDecoderConcurrency(n))
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.WriteTo(c.w)
		if (err == nil) != (c.name == "eof") {
			t.Fatalf("%s: %v", c.name, err)
		}
		waitGoroutines(t, c.name, base)
		d.Close()
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// heapSampler returns a function sampling the in-use heap after a GC
// (the live heap, without the garbage a GC cycle leaves), and one
// returning the peak sampled above the heap at the call.
func heapSampler() (sample func(), peak func() uint64) {
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	base, top := ms.HeapInuse, ms.HeapInuse
	return func() {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			top = max(top, ms.HeapInuse)
		}, func() uint64 {
			return top - base
		}
}

// TestStreamMemoryBound reads streams of concurrency n with 64 KiB reads,
// sampling the live heap after every read: its peak, above the heap
// before, is at most the window (the stream's round buffer, as with
// n == 1) + (2n+4)·128 KiB + 1 MiB.
func TestStreamMemoryBound(t *testing.T) {
	const w = 1 << 20
	frame, text := mtTestStream(t, 24<<20, w)
	for _, n := range []int{2, 4, 8} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			d := newTestDecoder(t, WithDecoderConcurrency(n))
			if err := d.Reset(bytes.NewReader(frame)); err != nil {
				t.Fatal(err)
			}
			p := make([]byte, 64<<10)
			sample, peak := heapSampler()
			var got int
			for {
				m, err := d.Read(p)
				if !bytes.Equal(p[:m], text[got:got+m]) {
					t.Fatalf("output differs at %d", got)
				}
				got += m
				sample()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			outBuff, _ := decodingBufferSizeInternal(w, uint64(len(text)), blockSizeMax)
			bound := uint64(outBuff) + uint64(2*n+4)*blockSizeMax + 1<<20
			t.Logf("n=%d: peak %d KiB above the heap before, bound %d KiB", n, peak()>>10, bound>>10)
			if peak() > bound {
				t.Fatalf("peak %d > bound %d", peak(), bound)
			}
		})
	}
}

// manySeqsFrame is a frame (1 MiB window) of nb blocks of the most
// sequences a valid block can have: one literal, then 43,690 matches of 3
// bytes at offset 1 (blockSizeMax/3 sequences, 131,071 bytes).
func manySeqsFrame(t testing.TB, nb int) []byte {
	t.Helper()
	seqs := make([]malSeq, blockSizeMax/3)
	for i := range seqs {
		seqs[i] = malSeq{ml: 3, off: 1}
	}
	seqs[0].ll = 1
	body := craftBlock(t, []byte("a"), seqs)
	f := malFrame{hdr: malFHdr{winDesc: malWindow1M}.bytes()}
	for i := range nb {
		f.blocks = append(f.blocks, malCBlock(body, i == nb-1))
	}
	return f.bytes()
}

// TestStreamMemoryBoundHostile reads hostile streams with concurrency 8:
// blocks with the most sequences a block may have (their decoded
// sequences are the pipeline's largest buffers), an RLE bomb and the
// malicious corpus; the in-use heap stays within the pipeline's proven
// bound (pipelineMemBound) plus the window.
func TestStreamMemoryBoundHostile(t *testing.T) {
	const n = 8
	// A raw block claiming 2 MiB (over blockSizeMax): S1 must not wait
	// for it.
	big := append(malFHdr{winDesc: malWindow1M}.bytes(), malBlockHeader(btRaw, 1<<21-1, true)...)
	big = append(big, make([]byte, 1<<20)...)
	inputs := [][]byte{manySeqsFrame(t, 64), rleBombFrame(4096), big}
	for _, c := range maliciousCases(t) {
		if c.dict == "" {
			inputs = append(inputs, c.data)
		}
	}
	d := newTestDecoder(t, WithDecoderConcurrency(n), WithDecoderMaxWindow(1<<20))
	p := make([]byte, 1<<20)
	for i, in := range inputs {
		if err := d.Reset(bytes.NewReader(in)); err != nil {
			t.Fatal(err)
		}
		sample, peak := heapSampler()
		got := 0
		for got < 64<<20 {
			m, err := d.Read(p)
			if i == 0 && bytes.ContainsFunc(p[:m], func(r rune) bool { return r != 'a' }) {
				t.Fatal("many-sequences frame: output differs")
			}
			got += m
			sample()
			if err != nil {
				if i == 0 && (err != io.EOF || got != 64*131071) {
					t.Fatalf("many-sequences frame: %v after %d bytes", err, got)
				}
				break
			}
		}
		// + the stream's round buffer (window 1 MiB + 2 blocks)
		bound := uint64(pipelineMemBound(n)) + 2<<20
		if peak() > bound {
			t.Fatalf("input %d: peak %d KiB > bound %d KiB", i, peak()>>10, bound>>10)
		}
	}
}

// TestStreamConcurrent32 decodes hostile streams (the most sequences a
// block may have, an RLE bomb) with concurrency 64 from 4 goroutines at
// once, on 32-bit platforms (a fatal out-of-memory error fails it): the
// pipelines' buffers are bounded by pipelineMemBound whatever the input,
// and whatever n (the read-ahead is at most maxReadAhead units).
func TestStreamConcurrent32(t *testing.T) {
	if maxMemoryCap >= 64<<30 {
		t.Skip("32-bit platforms only")
	}
	inputs := [][]byte{manySeqsFrame(t, 32), rleBombFrame(2048)}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			d, err := NewReader(nil, WithDecoderConcurrency(64), WithDecoderMaxWindow(1<<20))
			if err != nil {
				t.Error(err)
				return
			}
			defer d.Close()
			for _, in := range inputs {
				if err := d.Reset(bytes.NewReader(in)); err != nil {
					t.Error(err)
					return
				}
				if _, err := d.WriteTo(io.Discard); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
}

// FuzzDecoderReadConcurrent decodes arbitrary input with Read (input
// chunks and read sizes chosen by seed) and WriteTo with concurrency 1 and
// with concurrency 2 to 9 (chosen by seed): the same output and error,
// up to the documented exemptions (checkSameStream).
func FuzzDecoderReadConcurrent(f *testing.F) {
	fuzzSeeds(f, func(i int) any { return uint64(i) })
	f.Fuzz(func(t *testing.T, data []byte, seed uint64) {
		n := 2 + int(seed%8)
		d1 := newTestDecoder(t, WithDecoderConcurrency(1))
		dn := newTestDecoder(t, WithDecoderConcurrency(n))
		run := func(d *Decoder, read bool) ([]byte, error, bool) {
			// (two sources: S1 reads in its own goroutine)
			r, rp := rand.New(rand.NewPCG(seed, 1)), rand.New(rand.NewPCG(seed, 2))
			maxChunk := 1 + r.IntN(len(data)+1)
			maxP := 1 + rp.IntN(1<<17)
			cr := &chunkReader{b: data, next: func() int { return 1 + r.IntN(maxChunk) }}
			if read {
				out, err := readAll(t, d, cr, func() int { return 1 + rp.IntN(maxP) }, fuzzLimit)
				return out, err, len(out) > fuzzLimit
			}
			out, err := writeToAll(t, d, cr)
			return out, err, false
		}
		for _, read := range []bool{true, false} {
			want, werr, l1 := run(d1, read)
			got, gerr, l2 := run(dn, read)
			ss := d1.s.dc.fParams.singleSegment || dn.s.dc.fParams.singleSegment
			if e := checkSameStream(t, fmt.Sprintf("read=%v n=%d", read, n), ss, false, l1 || l2, got, gerr, want, werr,
				noShortcutRef(t, data, nil, fuzzLimit)); e != "" {
				t.Logf("exemption %s", e)
			}
		}
	})
}

// BenchmarkDecoderRead streams silesia.tar (bench/corpus, see
// ref/fetch.sh), compressed at level 3, through WriteTo with concurrency
// n. For information only.
func BenchmarkDecoderRead(b *testing.B) {
	in, err := os.ReadFile("../bench/corpus/silesia.tar")
	if err != nil {
		b.Skip(err)
	}
	frame := newTestEncoder(b, WithEncoderLevel(SpeedDefault)).EncodeAll(in, nil)
	for _, n := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			d := newTestDecoder(b, WithDecoderConcurrency(n))
			b.SetBytes(int64(len(in)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := d.Reset(bytes.NewReader(frame)); err != nil {
					b.Fatal(err)
				}
				m, err := d.WriteTo(io.Discard)
				if err != nil || m != int64(len(in)) {
					b.Fatal(m, err)
				}
			}
		})
	}
}
