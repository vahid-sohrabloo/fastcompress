package zstd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// The tests of the multithreaded compressor (zstdmt_compress.go,
// WithConcurrentBlocks) against `cref -workers N`, C's zstdmt. C's output
// does not depend on nbWorkers >= 1 (checked here too, see
// TestCrefMTWorkersIndependent), so the Go side runs with several worker
// counts against C with 2 workers.

// zmtSilesia returns the first n bytes of silesia.tar, or skips.
func zmtSilesia(t testing.TB, n int) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia.tar"))
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal(err)
		}
		t.Skip("bench/corpus/silesia.tar missing; run ref/fetch.sh")
	}
	return b[:min(n, len(b))]
}

// zmtOpts returns the Encoder options of C level level, window log wlog (0
// = default), checksum and n workers (WithConcurrentBlocks).
func zmtOpts(level int, wlog uint32, checksum bool, n int, extra ...EOption) []EOption {
	opts := []EOption{WithEncoderZstdLevel(level), WithEncoderCRC(checksum),
		WithConcurrentBlocks(true), WithEncoderConcurrency(n)}
	if wlog != 0 {
		opts = append(opts, WithWindowSize(1<<wlog))
	}
	return append(opts, extra...)
}

// zmtCrefArgs returns cref's common options for the same parameters and 2
// workers.
func zmtCrefArgs(level int, wlog uint32, checksum bool) []string {
	args := []string{"-level", strconv.Itoa(level), "-checksum", "0", "-workers", "2"}
	if checksum {
		args[3] = "1"
	}
	if wlog != 0 {
		args = append(args, "-window-log", strconv.Itoa(int(wlog)))
	}
	return args
}

// zmtJobs returns the job size and overlap C uses for a frame of level
// level and window log wlog (0 = default) of size n (< 0 = unknown).
func zmtJobs(level int, wlog uint32, n int64) (job, overlap int) {
	c := newCCtx()
	c.setLevel(level)
	_ = c.setWindowLog(wlog)
	c.requestedParams.nbWorkers = 2
	if n >= 0 {
		c.setPledgedSrcSize(uint64(n))
	}
	if err := c.initCompressStream2(eContinue, 0, true); err != nil {
		panic(err)
	}
	defer c.mtAbandon()
	if c.mt == nil {
		return 0, 0
	}
	return c.mt.targetSectionSize, c.mt.targetPrefixSize
}

// zmtEncodeCase is one EncodeAll comparison.
type zmtEncodeCase struct {
	level    int
	wlog     uint32
	checksum bool
	size     int // input size (from zmtInput)
	workers  int
	long     bool // skipped under -short
}

func (c zmtEncodeCase) String() string {
	return fmt.Sprintf("L%d/w%d/c%v/%dB/n%d", c.level, c.wlog, c.checksum, c.size, c.workers)
}

// zmtInput returns n bytes: Silesia when present (falling back to mixed
// data under -short only), so that the inputs have long matches across
// job boundaries.
func zmtInput(t testing.TB, n int) []byte {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia.tar")); err == nil && len(b) >= n {
		return b[:n]
	}
	if !testing.Short() {
		return zmtSilesia(t, n)
	}
	return asyncTestData(n, uint64(n))
}

// TestCrefMTEncodeAll checks EncodeAll with WithConcurrentBlocks against
// C's ZSTD_compress2 with nbWorkers: levels 1 to 22, small windows (1 MiB
// jobs, so that a few MiB make many jobs), inputs at or below 512 KiB
// (single-threaded in C too), of less than one job, of exactly k jobs and
// of many jobs, the checksum on and off.
func TestCrefMTEncodeAll(t *testing.T) {
	cases := []zmtEncodeCase{
		{level: 3, wlog: 17, checksum: true, size: 512 << 10, workers: 4},     // single-threaded in C (<= ZSTDMT_JOBSIZE_MIN)
		{level: 3, wlog: 17, checksum: true, size: 512<<10 + 1, workers: 4},   // one job
		{level: 1, wlog: 17, checksum: true, size: 3 << 20, workers: 2},       // exactly 3 jobs
		{level: 1, wlog: 17, checksum: false, size: 3<<20 + 1, workers: 8},    // 3 jobs and 1 byte
		{level: 3, wlog: 17, checksum: true, size: 7<<20 + 12345, workers: 3}, // many jobs
		{level: 7, wlog: 18, checksum: false, size: 2<<20 + 77, workers: 4},
		{level: 12, wlog: 17, checksum: true, size: 2<<20 + 5, workers: 2},
		{level: 19, wlog: 17, checksum: true, size: 1<<20 + 999, workers: 3},
		{level: 22, wlog: 17, checksum: false, size: 1<<20 + 3, workers: 2},
		{level: 3, checksum: true, size: 3 << 20, workers: 4}, // default window: one job
		{level: 1, checksum: true, size: 30 << 20, workers: 8, long: true},
		{level: 3, checksum: true, size: 40 << 20, workers: 4, long: true},
		{level: 9, checksum: false, size: 40 << 20, workers: 3, long: true},
		{level: 19, checksum: true, size: 40 << 20, workers: 8, long: true},
		{level: 16, wlog: 27, checksum: true, size: 40 << 20, workers: 8, long: true}, // long-distance matching, 3 jobs
	}
	for _, c := range cases {
		t.Run(c.String(), func(t *testing.T) {
			if c.long && testing.Short() {
				t.Skip("-short")
			}
			src := zmtInput(t, c.size)
			e := newTestEncoder(t, zmtOpts(c.level, c.wlog, c.checksum, c.workers)...)
			got := e.EncodeAll(src, nil)
			want := creftest.Run(t, src, append([]string{"compress"}, zmtCrefArgs(c.level, c.wlog, c.checksum)...)...)
			if !bytes.Equal(got, want) {
				job, ov := zmtJobs(c.level, c.wlog, int64(len(src)))
				t.Fatalf("EncodeAll differs from C (Go %d bytes, C %d bytes; jobs of %d, overlap %d)%s", len(got), len(want), job, ov, firstDiff(got, want))
			}
			// The same Encoder again, with data in dst: same frame.
			if got2 := e.EncodeAll(src, []byte("x")); !bytes.Equal(got2[1:], want) {
				t.Fatal("second EncodeAll differs")
			}
		})
	}
}

// zmtStreamCase is one Writer comparison.
type zmtStreamCase struct {
	sc      streamCase // chunks, flushes, pledged size, level, window, checksum
	size    int
	workers int
	long    bool
}

// TestCrefMTStream checks the Writer with WithConcurrentBlocks against
// `cref stream -workers 2` (unknown or pledged size, random chunks and
// flushes), for inputs smaller than one job (unknown size: still zstdmt in
// C), exactly k jobs and many jobs. One Encoder per case is reused for
// two streams.
func TestCrefMTStream(t *testing.T) {
	cases := []zmtStreamCase{
		{sc: streamCase{level: 3, windowLog: 17, checksum: true}, size: 100 << 10, workers: 4},                                  // unknown size, one small job
		{sc: streamCase{level: 3, windowLog: 17, checksum: true, pledged: 400 << 10}, size: 400 << 10, workers: 4},              // pledged <= 512 KiB: single-threaded
		{sc: streamCase{level: 1, windowLog: 17, checksum: true}, size: 3 << 20, workers: 2},                                    // exactly 3 jobs
		{sc: streamCase{level: 1, windowLog: 17, checksum: false, chunk: 1000}, size: 2<<20 + 1, workers: 8},                    // stream-pipe
		{sc: streamCase{level: 3, windowLog: 17, checksum: true, flushEvery: 7}, size: 6 << 20, workers: 3},                     // flushes cut jobs short
		{sc: streamCase{level: 3, windowLog: 17, checksum: true, flushEvery: 1}, size: 2 << 20, workers: 2},                     // flush after every chunk
		{sc: streamCase{level: 7, windowLog: 18, checksum: true, pledged: 3<<20 + 9}, size: 3<<20 + 9, workers: 4},              // pledged
		{sc: streamCase{level: 12, windowLog: 17, checksum: false, flushEvery: 3, pledged: 2 << 20}, size: 2 << 20, workers: 3}, // pledged and flushes
		{sc: streamCase{level: 19, windowLog: 17, checksum: true, flushEvery: 5}, size: 1<<20 + 500, workers: 2},
		{sc: streamCase{level: 22, windowLog: 17, checksum: true}, size: 1 << 20, workers: 3},
		{sc: streamCase{level: 3, checksum: true, flushEvery: 40}, size: 40 << 20, workers: 8, long: true},
		{sc: streamCase{level: 19, checksum: true}, size: 40 << 20, workers: 4, long: true},
		{sc: streamCase{level: 16, windowLog: 27, checksum: true, flushEvery: 60}, size: 40 << 20, workers: 8, long: true}, // long-distance matching
		{sc: streamCase{level: 22, checksum: true}, size: 12 << 20, workers: 2, long: true},                                // LDM, window 128 MiB, one 512 MiB job
	}
	for _, c := range cases {
		sc := c.sc
		name := fmt.Sprintf("%v/%dB/n%d", sc, c.size, c.workers)
		t.Run(name, func(t *testing.T) {
			if c.long && testing.Short() {
				t.Skip("-short")
			}
			src := zmtInput(t, c.size)
			e := newTestEncoder(t, zmtOpts(sc.level, sc.windowLog, sc.checksum, c.workers)...)
			if e.async() {
				t.Fatal("the zstdmt stream must not be async")
			}
			args := sc.crefArgs(len(src))
			args = append(args, "-workers", "2")
			want := creftest.Run(t, src, args...)
			for i := range 2 {
				got, err := writerStream(e, sc, src)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					job, ov := zmtJobs(sc.level, sc.windowLog, sc.pledged)
					t.Fatalf("stream %d differs from C (Go %d bytes, C %d bytes; jobs of %d, overlap %d)%s", i, len(got), len(want), job, ov, firstDiff(got, want))
				}
			}
		})
	}
}

// TestCrefMTDict checks dictionaries with WithConcurrentBlocks: C's
// zstdmt starts its first job from the CDict (later jobs start from the
// previous job's end, without the dictionary).
func TestCrefMTDict(t *testing.T) {
	src := zmtInput(t, 2<<20+300)
	for _, d := range testDicts(t) {
		if d.name == "train12k" || d.name == "raw8k" {
			continue
		}
		dargs := d.crefArgs(t)
		for _, level := range []int{1, 3, 12, 19} {
			t.Run(d.name+"/L"+strconv.Itoa(level), func(t *testing.T) {
				if level == 19 && testing.Short() {
					t.Skip("-short")
				}
				e := newTestEncoder(t, zmtOpts(level, 17, true, 3, d.option())...)
				got := e.EncodeAll(src, nil)
				want := creftest.Run(t, src, append(append([]string{"compress"}, zmtCrefArgs(level, 17, true)...), dargs...)...)
				sameFrames(t, d, got, want)
				for _, sc := range []streamCase{
					{level: level, windowLog: 17, checksum: true, flushEvery: 4},
					{level: level, windowLog: 17, checksum: true, pledged: int64(len(src))},
				} {
					got, err := writerStream(e, sc, src)
					if err != nil {
						t.Fatal(err)
					}
					want := creftest.Run(t, src, append(append(sc.crefArgs(len(src)), "-workers", "2"), dargs...)...)
					sameFrames(t, d, got, want)
				}
			})
		}
	}
}

// TestCrefMTWorkersIndependent checks, at a few levels, that C's zstdmt
// output is the same for 1, 2, 3 and 8 workers (the premise of the other
// tests) and equals Go's for those counts.
func TestCrefMTWorkersIndependent(t *testing.T) {
	src := zmtInput(t, 3<<20+17)
	levels := []int{1, 7, 19}
	if testing.Short() {
		levels = levels[:2]
	}
	for _, level := range levels {
		var ref []byte
		for _, n := range []int{1, 2, 3, 8} {
			args := []string{"compress", "-level", strconv.Itoa(level), "-checksum", "1", "-window-log", "17", "-workers", strconv.Itoa(n)}
			out := creftest.Run(t, src, args...)
			if ref == nil {
				ref = out
			} else if !bytes.Equal(out, ref) {
				t.Fatalf("L%d: C with %d workers differs from 1 worker", level, n)
			}
			if n == 1 {
				continue // n = 1 is single-threaded in the Encoder
			}
			got := newTestEncoder(t, zmtOpts(level, 17, true, n)...).EncodeAll(src, nil)
			if !bytes.Equal(got, ref) {
				t.Fatalf("L%d: Go with %d workers differs from C%s", level, n, firstDiff(got, ref))
			}
		}
	}
}

// TestMTDeterministic checks that the output does not depend on the
// number of workers or on scheduling: 2, 3 and 8 workers, the Writer (a
// slow io.Writer and random writes and flushes, so that jobs complete in
// varying orders) and EncodeAll, repeated; and that the frames decode.
// Small enough for the race detector.
func TestMTDeterministic(t *testing.T) {
	n := 3 << 20
	if raceEnabled {
		n = 1<<20 + 7
	}
	src := asyncTestData(n, 21)
	var wantAll, wantStream []byte
	for rep := range 2 {
		for _, w := range []int{2, 3, 8} {
			e := newTestEncoder(t, zmtOpts(3, 17, true, w)...)
			all := e.EncodeAll(src, nil)
			var buf bytes.Buffer
			e.Reset(slowWriter{&buf, time.Duration(rep) * 50 * time.Microsecond})
			if err := randomWrites(e, src, 70000, 6, 5); err != nil {
				t.Fatal(err)
			}
			if wantAll == nil {
				wantAll, wantStream = all, bytes.Clone(buf.Bytes())
				for _, f := range [][]byte{all, wantStream} {
					if dec, err := DecodeTo(nil, f); err != nil || !bytes.Equal(dec, src) {
						t.Fatalf("frame does not decode: %v", err)
					}
				}
				continue
			}
			if !bytes.Equal(all, wantAll) {
				t.Fatalf("EncodeAll with %d workers (run %d) differs", w, rep)
			}
			if !bytes.Equal(buf.Bytes(), wantStream) {
				t.Fatalf("stream with %d workers (run %d) differs%s", w, rep, firstDiff(buf.Bytes(), wantStream))
			}
		}
	}
}

// TestMTConcurrentEncodeAll runs EncodeAll with WithConcurrentBlocks from
// several goroutines at once (each call has its own workers): every frame
// is the same.
func TestMTConcurrentEncodeAll(t *testing.T) {
	src := asyncTestData(1<<20+1000, 22)
	e := newTestEncoder(t, zmtOpts(1, 17, true, 3)...)
	want := e.EncodeAll(src, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for g := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				if got := e.EncodeAll(src, nil); !bytes.Equal(got, want) {
					errs <- fmt.Errorf("goroutine %d: frame differs", g)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestMTConcurrencyOne checks that WithConcurrentBlocks has no effect with
// WithEncoderConcurrency(1): the output is single-threaded.
func TestMTConcurrencyOne(t *testing.T) {
	src := asyncTestData(2<<20, 23)
	st := newTestEncoder(t, WithEncoderZstdLevel(3), WithWindowSize(1<<17))
	one := newTestEncoder(t, zmtOpts(3, 17, true, 1)...)
	if one.o.nbWorkers() != 0 {
		t.Fatal("nbWorkers with concurrency 1")
	}
	if !bytes.Equal(one.EncodeAll(src, nil), st.EncodeAll(src, nil)) {
		t.Fatal("EncodeAll with concurrency 1 differs from single-threaded")
	}
	mt := newTestEncoder(t, zmtOpts(3, 17, true, 2)...)
	if bytes.Equal(mt.EncodeAll(src, nil), st.EncodeAll(src, nil)) {
		t.Fatal("zstdmt output equals the single-threaded output: zstdmt not used")
	}
}

// TestMTContentSize checks ResetContentSize with zstdmt: the right size
// gives C's frame (TestCrefMTStream), a wrong one an error. C fails a
// one-job frame, but writes a frame with the wrong content size for more
// jobs; this port fails both.
func TestMTContentSize(t *testing.T) {
	src := asyncTestData(3<<20, 24)
	for _, pledged := range []int64{int64(len(src)) - 1, int64(len(src)) + 1, 600 << 10} {
		for _, flush := range []bool{false, true} {
			e := newTestEncoder(t, zmtOpts(1, 17, true, 3)...)
			e.ResetContentSize(io.Discard, pledged)
			_, err := e.Write(src)
			if err == nil && flush {
				err = e.Flush()
			}
			if err == nil {
				err = e.Close()
			}
			if !errors.Is(err, errSrcSizeWrong) {
				t.Fatalf("pledged %d of %d, flush %v: err = %v, want errSrcSizeWrong", pledged, len(src), flush, err)
			}
			// Reset clears it.
			var buf bytes.Buffer
			e.ResetContentSize(&buf, int64(len(src)))
			if _, err := e.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			if dec, err := DecodeTo(nil, buf.Bytes()); err != nil || !bytes.Equal(dec, src) {
				t.Fatalf("stream after Reset does not decode: %v", err)
			}
		}
	}
}

// TestMTWriterError checks that an error of the io.Writer is returned and
// sticky until Reset, that no goroutine (job) is left after Reset, and
// that the next stream is right.
func TestMTWriterError(t *testing.T) {
	src := make([]byte, 5<<20) // incompressible: the output is as large
	r := rand.New(rand.NewPCG(25, 0))
	for i := range src {
		src[i] = byte(r.Uint32())
	}
	boom := errors.New("boom")
	base := runtime.NumGoroutine()
	e := newTestEncoder(t, zmtOpts(1, 17, true, 4)...)
	var want bytes.Buffer
	e.Reset(&want)
	if err := randomWrites(e, src[:2<<20], 30000, 0, 2); err != nil {
		t.Fatal(err)
	}
	for _, after := range []int{0, 1, 5000, 1 << 20, 4 << 20} {
		for _, p := range []string{"write", "write-flush", "single", "readfrom"} {
			what := fmt.Sprintf("fail after %d, %s", after, p)
			e.Reset(&failWriter{n: after, err: boom})
			var err error
			switch p {
			case "write", "write-flush":
				flush := 0
				if p == "write-flush" {
					flush = 9
				}
				err = randomWrites(e, src, 40000, flush, uint64(after))
			case "single":
				if _, err = e.Write(src); err == nil {
					err = e.Close()
				}
			case "readfrom":
				if _, err = e.ReadFrom(bytes.NewReader(src)); err == nil {
					err = e.Close()
				}
			}
			if !errors.Is(err, boom) {
				t.Fatalf("%s: first error = %v, want boom", what, err)
			}
			if _, err := e.Write([]byte("x")); err != boom {
				t.Fatalf("%s: Write after the error = %v", what, err)
			}
			if err := e.Flush(); err != boom {
				t.Fatalf("%s: Flush after the error = %v", what, err)
			}
			if err := e.Close(); err != boom {
				t.Fatalf("%s: Close after the error = %v", what, err)
			}
			var got bytes.Buffer
			e.Reset(&got)
			waitGoroutines(t, what, base)
			if err := randomWrites(e, src[:2<<20], 30000, 0, 2); err != nil {
				t.Fatalf("%s: stream after Reset: %v", what, err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("%s: stream after Reset differs%s", what, firstDiff(got.Bytes(), want.Bytes()))
			}
		}
	}
}

// TestMTNoGoroutineLeak ends zstdmt streams with jobs in flight in every
// way (Close, Reset, ResetWithOptions, ResetContentSize, dropping the
// Encoder): afterwards no goroutine remains, and a stream following an
// abandoned one is right.
func TestMTNoGoroutineLeak(t *testing.T) {
	src := asyncTestData(4<<20, 26)
	base := runtime.NumGoroutine()
	var want bytes.Buffer
	s := newTestEncoder(t, zmtOpts(19, 17, true, 2)...)
	s.Reset(&want)
	if err := randomWrites(s, src, 50000, 0, 1); err != nil {
		t.Fatal(err)
	}
	for _, end := range []string{"close", "reset", "reset-nil", "reset-options", "reset-size", "drop"} {
		e := newTestEncoder(t, zmtOpts(19, 17, true, 4)...) // slow: jobs are running when Write returns
		var junk bytes.Buffer
		e.Reset(&junk)
		if _, err := e.Write(src[:len(src)/2]); err != nil {
			t.Fatalf("%s: %v", end, err)
		}
		if runtime.NumGoroutine() <= base {
			t.Fatalf("%s: no job running after a %d-byte Write", end, len(src)/2)
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
		case "drop": // the jobs end by themselves
			waitGoroutinesFor(t, end, base, 30*time.Second)
			continue
		}
		waitGoroutines(t, end, base)
		var got bytes.Buffer
		e.Reset(&got)
		if err := randomWrites(e, src, 50000, 0, 1); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%s: stream after the abandoned one differs%s", end, firstDiff(got.Bytes(), want.Bytes()))
		}
		waitGoroutines(t, end+" (second stream)", base)
	}
}

// waitGoroutinesFor is waitGoroutines with a deadline of d.
func waitGoroutinesFor(t *testing.T, what string, base int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	waitGoroutines(t, what, base)
}

// TestMTEmptyAndSmall checks zstdmt streams around the edges: nothing
// written (no frame, or C's empty frame with WithZeroFrames, which is
// single-threaded: pledged 0), one byte (unknown size: zstdmt with one
// job), a flush with nothing new, and an empty last job after a flush.
func TestMTEmptyAndSmall(t *testing.T) {
	e := newTestEncoder(t, zmtOpts(3, 17, true, 2)...)
	var buf bytes.Buffer
	e.Reset(&buf)
	if err := e.Close(); err != nil || buf.Len() != 0 {
		t.Fatalf("empty stream: %v, %d bytes", err, buf.Len())
	}
	z := newTestEncoder(t, zmtOpts(3, 17, true, 2, WithZeroFrames(true))...)
	z.Reset(&buf)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if want := creftest.Run(t, nil, "compress", "-level", "3", "-checksum", "1", "-workers", "2", "-window-log", "17"); !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("WithZeroFrames empty frame differs from C")
	}
	src := asyncTestData(2<<20, 27)
	for _, sc := range []streamCase{
		{level: 3, windowLog: 17, checksum: true, chunk: 1}, // with 1 byte of input below
		{level: 3, windowLog: 17, checksum: true, chunk: 1 << 20, flushEvery: 1},
		{level: 3, windowLog: 17, checksum: true, chunk: 1<<20 + 3, flushEvery: 2},
	} {
		in := src
		if sc.chunk == 1 {
			in = src[:1]
		}
		buf.Reset()
		got, err := writerStream(e, sc, in)
		if err != nil {
			t.Fatal(err)
		}
		// A flush with nothing new since the last one.
		want := creftest.Run(t, in, append(sc.crefArgs(len(in)), "-workers", "2")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%v: differs from C%s", sc, firstDiff(got, want))
		}
	}
	e.Reset(&buf)
	buf.Reset()
	if _, err := e.Write(src[:1000]); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := e.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	want := creftest.Run(t, src[:1000], "stream", "-chunks", "1000", "-flush-after", "0", "-level", "3", "-checksum", "1", "-window-log", "17", "-workers", "2")
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("repeated flushes: differs from C%s", firstDiff(buf.Bytes(), want))
	}
}

// BenchmarkEncoderConcurrentBlocks streams the first 64 MiB of silesia.tar
// (MTBENCH_MB overrides the size) of unknown size through the Writer, as
// `zstd -T<n> < file` does, with zstdmt and GOMAXPROCS workers ("mt", at
// least 2; -cpu sets it, run under taskset to bind the cores) and single-threaded
// ("st"), at levels 1, 3, 9 and 19. With MTBENCH_OUT set to a directory,
// the last frame of each is written to it (L<level>-<mode>.zst), to compare
// with the zstd CLI's.
func BenchmarkEncoderConcurrentBlocks(b *testing.B) {
	n := 64 << 20
	if v, err := strconv.Atoi(os.Getenv("MTBENCH_MB")); err == nil {
		n = v << 20
	}
	src := zmtSilesia(b, n)
	for _, level := range []int{1, 3, 9, 19} {
		for _, mode := range []string{"mt", "st"} {
			b.Run(fmt.Sprintf("L%d/%s", level, mode), func(b *testing.B) {
				opts := []EOption{WithEncoderZstdLevel(level), WithEncoderConcurrency(runtime.GOMAXPROCS(0))}
				if mode == "mt" {
					// At least 2 workers: with 1, WithConcurrentBlocks is off.
					opts = []EOption{WithEncoderZstdLevel(level), WithEncoderConcurrency(max(2, runtime.GOMAXPROCS(0))), WithConcurrentBlocks(true)}
				}
				e := newTestEncoder(b, opts...)
				var out bytes.Buffer
				w := &countWriter{w: io.Discard}
				dir := os.Getenv("MTBENCH_OUT")
				b.SetBytes(int64(len(src)))
				b.ResetTimer()
				for i := range b.N {
					if dir != "" && i == b.N-1 {
						w.w = &out
					}
					e.Reset(w)
					if _, err := e.Write(src); err != nil {
						b.Fatal(err)
					}
					if err := e.Close(); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(w.n)/float64(b.N)/float64(len(src)), "ratio")
				if dir != "" {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("L%d-%s.zst", level, mode)), out.Bytes(), 0o644); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
