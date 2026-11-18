package zstd

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// streamCase is one streaming run: the same chunks and flushes go to the
// Go stream and to `cref stream` (random chunks) or `cref stream-pipe`
// (uniform chunks).
type streamCase struct {
	level      int    // C level; 1/3/7/11 can go through the Writer, any level through cctxStream
	windowLog  uint32 // 0 = level default
	checksum   bool
	chunk      int         // uniform chunk size; 0 = random sizes in 1..200000 (seed 1)
	flushEvery int         // flush after every flushEvery-th chunk; 0 = never
	pledged    int64       // pledged content size; <= 0 = unknown
	ldm        paramSwitch // long-distance matching (C: ZSTD_c_enableLongDistanceMatching); psAuto = C's choice; cctxStream only
}

func (sc streamCase) String() string {
	s := "L" + strconv.Itoa(sc.level)
	if sc.windowLog != 0 {
		s += "/w" + strconv.Itoa(int(sc.windowLog))
	}
	if sc.checksum {
		s += "/c1"
	}
	if sc.chunk > 0 {
		s += "/chunk" + strconv.Itoa(sc.chunk)
	} else {
		s += "/chunkRand"
	}
	if sc.flushEvery > 0 {
		s += "/flush" + strconv.Itoa(sc.flushEvery)
	}
	if sc.pledged > 0 {
		s += "/pledged" + strconv.FormatInt(sc.pledged, 10)
	}
	if sc.ldm != psAuto {
		s += "/ldm" + strconv.Itoa(int(sc.ldm))
	}
	return s
}

// chunks splits n bytes into sc's chunk sizes.
func (sc streamCase) chunks(n int) []int {
	var cs []int
	r := rand.New(rand.NewPCG(1, 0))
	for n > 0 {
		c := sc.chunk
		if c == 0 {
			c = 1 + r.IntN(200000)
		}
		c = min(c, n)
		cs = append(cs, c)
		n -= c
	}
	return cs
}

// flushAfter reports whether a flush follows chunk i (0-based).
func (sc streamCase) flushAfter(i int) bool {
	return sc.flushEvery > 0 && (i+1)%sc.flushEvery == 0
}

// crefArgs returns the cref arguments that stream n bytes as sc describes.
func (sc streamCase) crefArgs(n int) []string {
	var args []string
	if sc.chunk > 0 {
		args = []string{"stream-pipe", "-chunk-size", strconv.Itoa(sc.chunk)}
		if sc.flushEvery > 0 {
			args = append(args, "-flush-every", strconv.Itoa(sc.flushEvery))
		}
	} else {
		var cs, fl []string
		for i, c := range sc.chunks(n) {
			cs = append(cs, strconv.Itoa(c))
			if sc.flushAfter(i) {
				fl = append(fl, strconv.Itoa(i))
			}
		}
		if len(cs) == 0 {
			cs = []string{"0"} // cref needs a chunk list; Go then makes no Write
		}
		args = []string{"stream", "-chunks", strings.Join(cs, ",")}
		if len(fl) > 0 {
			args = append(args, "-flush-after", strings.Join(fl, ","))
		}
	}
	args = append(args, "-level", strconv.Itoa(sc.level), "-checksum", "0")
	if sc.checksum {
		args[len(args)-1] = "1"
	}
	if sc.windowLog != 0 {
		args = append(args, "-window-log", strconv.Itoa(int(sc.windowLog)))
	}
	if sc.pledged > 0 {
		args = append(args, "-pledged-size", strconv.FormatInt(sc.pledged, 10))
	}
	if sc.ldm != psAuto {
		args = append(args, "-ldm", strconv.Itoa(int(sc.ldm)))
	}
	return args
}

// newStreamEncoder returns an Encoder with sc's parameters.
func newStreamEncoder(t testing.TB, sc streamCase) *Encoder {
	t.Helper()
	level, ok := levelOK(sc.level)
	if !ok {
		t.Fatalf("level %d has no EncoderLevel", sc.level)
	}
	opts := []EOption{WithEncoderLevel(level), WithEncoderCRC(sc.checksum)}
	if sc.windowLog != 0 {
		opts = append(opts, WithWindowSize(1<<sc.windowLog))
	}
	e, err := NewWriter(nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// writerStream streams src through e as sc describes and returns the
// output and the first error.
func writerStream(e *Encoder, sc streamCase, src []byte) ([]byte, error) {
	var buf bytes.Buffer
	e.ResetContentSize(&buf, sc.pledged)
	pos := 0
	for i, c := range sc.chunks(len(src)) {
		if n, err := e.Write(src[pos : pos+c]); err != nil || n != c {
			return buf.Bytes(), errors.Join(err, errors.New("short write"))
		}
		pos += c
		if sc.flushAfter(i) {
			if err := e.Flush(); err != nil {
				return buf.Bytes(), err
			}
		}
	}
	err := e.Close()
	return buf.Bytes(), err
}

// cctxStream drives compressStream2 of a context at any level as cref's
// feed does: every call gets a fresh ZSTD_CStreamOutSize() output buffer.
// Empty input makes no ZSTD_e_continue call, like the Writer.
func cctxStream(t testing.TB, sc streamCase, src []byte) []byte {
	t.Helper()
	c := newCCtx()
	c.setLevel(sc.level)
	c.setChecksum(sc.checksum)
	if err := c.setWindowLog(sc.windowLog); err != nil {
		t.Fatal(err)
	}
	if sc.pledged > 0 {
		c.setPledgedSrcSize(uint64(sc.pledged))
	}
	c.requestedParams.ldmParams.enableLdm = sc.ldm
	var res []byte
	buf := make([]byte, 0, cStreamOutSize)
	feed := func(in []byte, op endDirective) {
		for {
			out, consumed, remaining, err := c.compressStream2(buf, in, op)
			if err != nil {
				t.Fatalf("%v: %v", sc, err)
			}
			res = append(res, out...)
			in = in[consumed:]
			if op == eContinue && len(in) == 0 || op != eContinue && remaining == 0 {
				return
			}
		}
	}
	pos := 0
	for i, n := range sc.chunks(len(src)) {
		feed(src[pos:pos+n], eContinue)
		pos += n
		if sc.flushAfter(i) {
			feed(nil, eFlush)
		}
	}
	feed(nil, eEnd)
	return res
}

// streamInputs returns the inputs for the streaming tests: creftest.Inputs
// without the silesia files (C and Go each compress every input up to 36
// times per level; the silesia corpus streams through
// TestCrefStreamOverflow instead).
func streamInputs(t *testing.T) ([]string, map[string][]byte) {
	t.Helper()
	names, inputs := sortedInputs(t)
	var keep []string
	for _, n := range names {
		if !strings.HasPrefix(n, "silesia/") {
			keep = append(keep, n)
		}
	}
	return keep, inputs
}

// TestCrefStreaming checks the Writer against `cref stream` at levels 1,
// 3, 7 and 11 with the level's own parameters (unknown content size), for
// chunks of 1 byte (inputs up to 128 KiB + 1), 1000 bytes and random sizes,
// flushing after every chunk, every 7th chunk or never. One Encoder per
// level is reused across all runs (Close then ResetContentSize).
func TestCrefStreaming(t *testing.T) {
	names, inputs := streamInputs(t)
	for _, level := range []int{1, 3, 7, 11} {
		e := newStreamEncoder(t, streamCase{level: level, checksum: true})
		for _, name := range names {
			src := inputs[name]
			for _, chunk := range []int{1, 1000, 0} {
				if chunk == 1 && len(src) > 128<<10+1 {
					continue
				}
				for _, flush := range []int{1, 7, 0} {
					sc := streamCase{level: level, checksum: true, chunk: chunk, flushEvery: flush}
					got, err := writerStream(e, sc, src)
					if err != nil {
						t.Fatalf("%v %s: %v", sc, name, err)
					}
					var want []byte // nothing written: no frame (see TestStreamZeroFramesAndPadding)
					if len(src) > 0 {
						want = creftest.Run(t, src, sc.crefArgs(len(src))...)
					}
					if !bytes.Equal(got, want) {
						t.Fatalf("%v %s: Writer output differs from C (Go %d bytes, C %d bytes)%s", sc, name, len(got), len(want), firstDiff(got, want))
					}
				}
			}
		}
	}
}

// TestCrefStreamingChecksumOff covers the content checksum flag off.
func TestCrefStreamingChecksumOff(t *testing.T) {
	_, inputs := streamInputs(t)
	src := inputs["win512KiB+1"]
	for _, level := range []int{1, 3, 7, 11} {
		sc := streamCase{level: level, chunk: 1000, flushEvery: 7}
		got, err := writerStream(newStreamEncoder(t, sc), sc, src)
		if err != nil {
			t.Fatal(err)
		}
		if want := creftest.Run(t, src, sc.crefArgs(len(src))...); !bytes.Equal(got, want) {
			t.Fatalf("%v: Writer output differs from C%s", sc, firstDiff(got, want))
		}
	}
}

// extDictRuns records, by strategy name, the extDict block compressors
// that ran (see extDictHook).
type extDictRuns struct {
	mu   sync.Mutex
	seen map[string]int
}

func (r *extDictRuns) install(t *testing.T) {
	r.seen = map[string]int{}
	extDictHook = func(name string) {
		r.mu.Lock()
		r.seen[name]++
		r.mu.Unlock()
	}
	t.Cleanup(func() { extDictHook = nil })
}

func (r *extDictRuns) take() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.seen
	r.seen = map[string]int{}
	return m
}

// TestCrefStreamExtDict streams inputs with windows smaller than the input,
// so that C's input buffer (window + block) wraps and the window gets an
// extDict segment, and checks the output against cref for each strategy's
// extDict block compressor, which each case must run (extDictHook). Levels
// 1, 3, 7 and 11 go through the Writer; greedy (level 5) and the unknown-
// size btlazy2, btopt, btultra and btultra2 levels (13, 16, 18, 19)
// through cctxStream, with windowLog 16 or less (no post-block splitter)
// and 17 (with it); btultra2 has no extDict compressor of its own, it runs
// btultra's. A pledged size steers level 11 to btlazy2 (<= 256 KiB) and
// btopt (<= 16 KiB).
func TestCrefStreamExtDict(t *testing.T) {
	names, inputs := streamInputs(t)
	var runs extDictRuns
	runs.install(t)
	for _, tc := range []struct {
		level     int
		windowLog uint32
		pledge    bool
		want      string // extDict compressor that must run ("": no check)
		minSize   int    // inputs below this size are skipped
	}{
		{1, 18, false, "fast", 384 << 10},
		{3, 18, false, "dfast", 384 << 10},
		// window == block: the fast and dfast extDict compressors fall back
		// to noDict for whole blocks (the extDict is beyond the window),
		// and run their own search only for blocks cut short by a flush.
		{1, 10, false, "", 4 << 10},
		{3, 10, false, "", 4 << 10},
		{7, 15, false, "lazy-row", 64 << 10},
		{7, 18, false, "lazy-row", 384 << 10},
		{11, 15, false, "lazy2-row", 64 << 10},
		{11, 17, false, "lazy2-row", 256 << 10},
		{7, 12, false, "lazy-hc", 8 << 10},
		{7, 14, false, "lazy-hc", 32 << 10},
		{11, 12, false, "lazy2-hc", 8 << 10},
		{5, 14, false, "greedy-hc", 32 << 10},
		{5, 16, false, "greedy-row", 128 << 10},
		{11, 12, true, "btlazy2", 16<<10 + 1},
		{11, 10, true, "btopt", 2 << 10},
		{13, 14, false, "btlazy2", 32 << 10},
		{16, 16, false, "btopt", 128 << 10},
		{18, 16, false, "btultra", 128 << 10},
		{19, 15, false, "btultra", 64 << 10},
		// window log 17: the post-block splitter runs too
		{16, 17, false, "btopt", 384 << 10},
		{19, 17, false, "btultra", 384 << 10},
	} {
		ran := false
		nInputs := 0
		for _, name := range names {
			src := inputs[name]
			if len(src) < tc.minSize {
				continue
			}
			// -short: the splitter cases (slow) take the first input only
			if nInputs++; testing.Short() && tc.windowLog == 17 && tc.level >= 16 && nInputs > 1 {
				break
			}
			for _, chunk := range []int{1000, 0} {
				for _, flush := range []int{7, 0} {
					sc := streamCase{level: tc.level, windowLog: tc.windowLog, checksum: chunk == 0, chunk: chunk, flushEvery: flush}
					if tc.pledge {
						sc.pledged = int64(len(src))
					}
					if tc.want != "" && extDictName(sc) != tc.want {
						continue // the pledged size selects another strategy
					}
					var got []byte
					if _, ok := levelOK(tc.level); ok {
						var err error
						if got, err = writerStream(newStreamEncoder(t, sc), sc, src); err != nil {
							t.Fatalf("%v %s: %v", sc, name, err)
						}
					} else {
						got = cctxStream(t, sc, src)
					}
					want := creftest.Run(t, src, sc.crefArgs(len(src))...)
					if !bytes.Equal(got, want) {
						t.Fatalf("%v %s: output differs from C (Go %d bytes, C %d bytes)%s", sc, name, len(got), len(want), firstDiff(got, want))
					}
					if runs.take()[tc.want] > 0 {
						ran = true
					}
				}
			}
		}
		if !ran && tc.want != "" {
			t.Errorf("L%d/w%d: the %s extDict compressor never ran", tc.level, tc.windowLog, tc.want)
		}
	}
}

// levelOK reports whether a C level has an EncoderLevel.
func levelOK(level int) (EncoderLevel, bool) {
	for l := SpeedFastest; l <= SpeedBestCompression; l++ {
		if zstdLevels[l] == level {
			return l, true
		}
	}
	return 0, false
}

// extDictName returns the extDictHook name of the block compressor sc's
// stream uses.
func extDictName(sc streamCase) string {
	var p cctxParams
	cctxParamsInit(&p, sc.level)
	p.cParams.windowLog = sc.windowLog
	size := contentSizeUnknown
	if sc.pledged > 0 {
		size = uint64(sc.pledged)
	}
	cp := getCParamsFromCCtxParams(&p, size, 0, cpmNoAttachDict)
	name := [...]string{zstdFast: "fast", zstdDfast: "dfast", zstdGreedy: "greedy", zstdLazy: "lazy", zstdLazy2: "lazy2",
		zstdBtlazy2: "btlazy2", zstdBtopt: "btopt", zstdBtultra: "btultra", zstdBtultra2: "btultra"}[cp.strategy] // C: no btultra2 extDict variant
	if rowMatchFinderSupported(cp.strategy) {
		if resolveRowMatchFinderMode(psAuto, &cp) == psEnable {
			return name + "-row"
		}
		return name + "-hc"
	}
	return name
}

// TestCrefStreamReset reuses one Encoder per level for three inputs through
// Reset, abandoning a half-written stream in between; every stream must
// equal a fresh C stream.
func TestCrefStreamReset(t *testing.T) {
	_, inputs := streamInputs(t)
	srcs := [][]byte{inputs["win512KiB+1"], inputs["text16KiB"], inputs["zstd-format.md"]}
	for _, level := range []int{1, 3, 7, 11} {
		sc := streamCase{level: level, checksum: true, chunk: 1000, flushEvery: 7}
		e := newStreamEncoder(t, sc)
		for i, src := range srcs {
			var junk bytes.Buffer
			e.Reset(&junk)
			if _, err := e.Write(src[:len(src)/2]); err != nil { // abandoned by the next Reset
				t.Fatal(err)
			}
			got, err := writerStream(e, sc, src)
			if err != nil {
				t.Fatal(err)
			}
			if want := creftest.Run(t, src, sc.crefArgs(len(src))...); !bytes.Equal(got, want) {
				t.Fatalf("%v input %d: output differs from C%s", sc, i, firstDiff(got, want))
			}
		}
	}
}

// TestCrefStreamReadFrom checks ReadFrom (with a reader returning odd-sized
// chunks) against C, and its byte count.
func TestCrefStreamReadFrom(t *testing.T) {
	_, inputs := streamInputs(t)
	src := inputs["win512KiB+1"]
	for _, level := range []int{1, 3, 7, 11} {
		sc := streamCase{level: level, checksum: true, chunk: 1000}
		e := newStreamEncoder(t, sc)
		var buf bytes.Buffer
		e.Reset(&buf)
		n, err := e.ReadFrom(io.MultiReader(bytes.NewReader(src[:777]), bytes.NewReader(src[777:])))
		if err != nil || n != int64(len(src)) {
			t.Fatalf("ReadFrom = %d, %v; want %d, nil", n, err, len(src))
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if want := creftest.Run(t, src, sc.crefArgs(len(src))...); !bytes.Equal(buf.Bytes(), want) {
			t.Fatalf("L%d: ReadFrom output differs from C%s", level, firstDiff(buf.Bytes(), want))
		}
	}
	// A read error is returned; the bytes read so far are counted.
	e := newStreamEncoder(t, streamCase{level: 1})
	e.Reset(io.Discard)
	boom := errors.New("boom")
	n, err := e.ReadFrom(io.MultiReader(bytes.NewReader(src[:10]), errReader{boom}))
	if n != 10 || err != boom {
		t.Fatalf("ReadFrom = %d, %v; want 10, boom", n, err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestCrefStreamContentSize checks ResetContentSize: with the right size
// the frame equals C's with that size pledged (content size in the header,
// parameters chosen for the size); with a wrong size both C and the Writer
// fail.
func TestCrefStreamContentSize(t *testing.T) {
	names, inputs := streamInputs(t)
	for _, level := range []int{1, 3, 7, 11} {
		e := newStreamEncoder(t, streamCase{level: level})
		for _, name := range names {
			src := inputs[name]
			if len(src) == 0 {
				continue
			}
			for _, chunk := range []int{1000, 0} {
				sc := streamCase{level: level, chunk: chunk, flushEvery: 7, pledged: int64(len(src))}
				got, err := writerStream(e, sc, src)
				if err != nil {
					t.Fatalf("%v %s: %v", sc, name, err)
				}
				if want := creftest.Run(t, src, sc.crefArgs(len(src))...); !bytes.Equal(got, want) {
					t.Fatalf("%v %s: output differs from C%s", sc, name, firstDiff(got, want))
				}
			}
		}
		src := inputs["text128KiB"]
		for _, pledged := range []int64{int64(len(src)) - 1, int64(len(src)) + 1, 1} {
			sc := streamCase{level: level, chunk: 1000, pledged: pledged}
			if _, err := writerStream(e, sc, src); !errors.Is(err, errSrcSizeWrong) {
				t.Errorf("%v: err = %v, want %v", sc, err, errSrcSizeWrong)
			}
			if out, err := creftest.RunErr(t, src, sc.crefArgs(len(src))...); err == nil {
				t.Errorf("%v: cref succeeded (%d bytes) with a wrong pledged size", sc, len(out))
			}
		}
	}
	// Nothing written with a pledged size: error at Close.
	e := newStreamEncoder(t, streamCase{level: 1})
	e.ResetContentSize(io.Discard, 5)
	if err := e.Close(); !errors.Is(err, errSrcSizeWrong) {
		t.Fatalf("Close with nothing written after ResetContentSize(5) = %v", err)
	}
	// Reset removes the pledge.
	var buf bytes.Buffer
	e.Reset(&buf)
	if _, err := e.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestStreamZeroFramesAndPadding checks the empty stream (no output, or
// C's empty frame with WithZeroFrames) and WithEncoderPadding on streams.
func TestStreamZeroFramesAndPadding(t *testing.T) {
	var buf bytes.Buffer
	e, err := NewWriter(&buf, WithEncoderLevel(SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil || buf.Len() != 0 {
		t.Fatalf("empty stream: Close = %v, %d bytes; want nil, 0", err, buf.Len())
	}
	if err := e.ResetWithOptions(&buf, WithZeroFrames(true)); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	want := creftest.Run(t, nil, "stream-pipe", "-chunk-size", "1", "-level", "1", "-checksum", "1")
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("WithZeroFrames empty stream = %x, want C's %x", buf.Bytes(), want)
	}
	if !bytes.Equal(buf.Bytes(), e.EncodeAll(nil, nil)) {
		t.Fatalf("WithZeroFrames empty stream differs from EncodeAll")
	}

	for _, pad := range []int{1, 7, 64, 1 << 16} {
		buf.Reset()
		if err := e.ResetWithOptions(&buf, WithEncoderPadding(pad), WithZeroFrames(false)); err != nil {
			t.Fatal(err)
		}
		src := bytes.Repeat([]byte("padding test "), 1000)
		if _, err := e.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if buf.Len()%pad != 0 {
			t.Fatalf("pad %d: output size %d is not a multiple", pad, buf.Len())
		}
		frame := creftest.Run(t, src, "stream-pipe", "-chunk-size", "100000", "-level", "1", "-checksum", "1")
		if !bytes.HasPrefix(buf.Bytes(), frame) {
			t.Fatalf("pad %d: output does not start with C's frame", pad)
		}
		if got := creftest.DecodeC(t, buf.Bytes()); !bytes.Equal(got, src) {
			t.Fatalf("pad %d: C decodes %d bytes, want %d", pad, len(got), len(src))
		}
	}
}

// failWriter fails every Write after the first n bytes.
type failWriter struct {
	n   int
	err error
}

func (w *failWriter) Write(p []byte) (int, error) {
	if len(p) <= w.n {
		w.n -= len(p)
		return len(p), nil
	}
	n := w.n
	w.n = 0
	return n, w.err
}

// TestStreamErrors checks the Writer's error semantics.
func TestStreamErrors(t *testing.T) {
	src := bytes.Repeat([]byte("error semantics "), 20000)

	// Writing after Close; closing twice; Reset reopens.
	var buf bytes.Buffer
	e, err := NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write(src); err != ErrEncoderClosed {
		t.Fatalf("Write after Close = %v, want ErrEncoderClosed", err)
	}
	if err := e.Flush(); err != ErrEncoderClosed {
		t.Fatalf("Flush after Close = %v, want ErrEncoderClosed", err)
	}
	if _, err := e.ReadFrom(bytes.NewReader(src)); err != ErrEncoderClosed {
		t.Fatalf("ReadFrom after Close = %v, want ErrEncoderClosed", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	e.Reset(&buf)
	if _, err := e.Write(src); err != nil {
		t.Fatalf("Write after Reset = %v", err)
	}
	// An async stream may write to buf until Close; buf is reused below.
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// A nil writer: EncodeAll-only use and Close without writes are fine.
	e, err = NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.EncodeAll(src, nil)) == 0 {
		t.Fatal("EncodeAll with a nil writer produced nothing")
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close with nil writer and no writes = %v", err)
	}
	e.Reset(nil)
	if _, err := e.Write(src); err != errNilWriter {
		t.Fatalf("Write with nil writer = %v, want errNilWriter", err)
	}

	// Errors of the io.Writer are returned and sticky until Reset.
	boom := errors.New("boom")
	fw := &failWriter{n: 100, err: boom}
	e, err = NewWriter(fw, WithEncoderLevel(SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	var werr error
	for i := 0; i < 10 && werr == nil; i++ {
		_, werr = e.Write(src)
	}
	if werr != boom {
		t.Fatalf("Write to a failing writer = %v, want boom", werr)
	}
	if _, err := e.Write([]byte("x")); err != boom {
		t.Fatalf("Write after a writer error = %v, want boom", err)
	}
	if err := e.Flush(); err != boom {
		t.Fatalf("Flush after a writer error = %v, want boom", err)
	}
	if err := e.Close(); err != boom {
		t.Fatalf("Close after a writer error = %v, want boom", err)
	}
	buf.Reset()
	e.Reset(&buf)
	if _, err := e.Write(src); err != nil {
		t.Fatalf("Write after Reset = %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if want := creftest.Run(t, src, "stream-pipe", "-chunk-size", strconv.Itoa(len(src)), "-level", "1", "-checksum", "1"); !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("stream after a failed one differs from C%s", firstDiff(buf.Bytes(), want))
	}

	// A short write without an error is io.ErrShortWrite (an async stream
	// may return it only from Close).
	e.Reset(shortWriter{})
	_, err = e.Write(src)
	if err == nil {
		err = e.Close()
	}
	if err != io.ErrShortWrite {
		t.Fatalf("Write and Close to a short writer = %v, want io.ErrShortWrite", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

// TestStreamResetWithOptions checks which options ResetWithOptions may
// change, and that changed ones apply to the next stream and to EncodeAll.
func TestStreamResetWithOptions(t *testing.T) {
	src := bytes.Repeat([]byte("reset with options "), 5000)
	e, err := NewWriter(nil, WithEncoderLevel(SpeedDefault), WithWindowSize(1<<20), WithEncoderConcurrency(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, opt := range []EOption{WithEncoderLevel(SpeedFastest), WithWindowSize(1 << 16), WithEncoderConcurrency(3)} {
		if err := e.ResetWithOptions(io.Discard, opt); err == nil {
			t.Fatal("ResetWithOptions changed an option that cannot change")
		}
	}
	if err := e.ResetWithOptions(io.Discard, WithEncoderLevel(SpeedDefault), WithWindowSize(1<<20), WithEncoderConcurrency(2)); err != nil {
		t.Fatalf("ResetWithOptions with unchanged values = %v", err)
	}
	if err := e.ResetWithOptions(io.Discard, WithEncoderPadding(0)); err == nil {
		t.Fatal("ResetWithOptions accepted an invalid option")
	}
	// Checksum off and literal compression disabled: stream and EncodeAll.
	var buf bytes.Buffer
	if err := e.ResetWithOptions(&buf, WithEncoderCRC(false), WithNoEntropyCompression(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"-level", "3", "-checksum", "0", "-window-log", "20", "-lit-mode", "2"}
	want := creftest.Run(t, src, append([]string{"stream-pipe", "-chunk-size", "1000"}, args...)...)
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("stream after ResetWithOptions differs from C%s", firstDiff(buf.Bytes(), want))
	}
	want = creftest.Run(t, src, append([]string{"compress", "-pledged", "1"}, args...)...)
	if got := e.EncodeAll(src, nil); !bytes.Equal(got, want) {
		t.Fatalf("EncodeAll after ResetWithOptions differs from C%s", firstDiff(got, want))
	}
}

// BenchmarkWriter writes 4 KiB chunks of a 4 MiB text through a reused
// Writer; it reports allocations per stream.
func BenchmarkWriter(b *testing.B) {
	src := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog, "), 4<<20/45)
	for _, level := range []EncoderLevel{SpeedFastest, SpeedDefault} {
		b.Run(level.String(), func(b *testing.B) {
			e, err := NewWriter(io.Discard, WithEncoderLevel(level))
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				e.Reset(io.Discard)
				for p := src; len(p) > 0; {
					n := min(len(p), 4096)
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
