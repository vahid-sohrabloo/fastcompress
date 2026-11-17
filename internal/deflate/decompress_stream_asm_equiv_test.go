package deflate

import (
	"bytes"
	stdflate "compress/flate"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// Assembly equivalence of the stream decoder (decompressStream, which runs
// decompressFastloopAsm through the same hook as the whole-buffer
// decoder): a stream is decoded call by call as StreamReader does, with
// the assembly off (heap buffers) and on (input window and output window
// flush against PROT_NONE guard pages, after the end and before the
// start), and every call's result, counts, stream state and whole output
// window (with the bytes past the output that the match-copy overrun
// writes) must be identical, as must the reader's slow rerun after
// libdeflateBadData. Then the StreamReader itself, through every source
// kind, with the assembly off and on: output, error, corrupt offset,
// decoder calls and the input left must be identical, and agree with the
// whole-buffer decoder (and that with libdeflate). In builds or on CPUs
// without the assembly the comparisons run the Go loop twice.

// Layouts of the buffers of a stream trace.
const (
	layoutHeap = iota
	layoutAfter
	layoutBefore
)

// streamEquivBufs holds the guard-page buffers of stream traces: a call's
// input window of n bytes uses the last n bytes of inAfter or the first n
// of inBefore, and likewise its output window (history and room).
type streamEquivBufs struct {
	inAfter, inBefore, outAfter, outBefore []byte
	heapIn, heapOut                        []byte
}

func newStreamEquivBufs(t testing.TB, maxIn, maxOut int) *streamEquivBufs {
	return &streamEquivBufs{
		inAfter:   guardmem.After(t, maxIn),
		inBefore:  guardmem.Before(t, maxIn),
		outAfter:  guardmem.After(t, maxOut),
		outBefore: guardmem.Before(t, maxOut),
		heapIn:    make([]byte, maxIn),
		heapOut:   make([]byte, maxOut),
	}
}

// streamEquivOut is the output window size the buffers need: the history
// and the largest room of streamAsmPlans.
const streamEquivOut = streamWindow + 96<<10

// place returns an input window holding in and an n-byte output window
// (filled with fillDst) in the given layout.
func (b *streamEquivBufs) place(t testing.TB, in []byte, n, layout int) (inBuf, win []byte) {
	t.Helper()
	if len(in) > len(b.heapIn) || n > len(b.heapOut) {
		t.Fatalf("stream trace: %d/%d bytes exceed the buffers (%d/%d)", len(in), n, len(b.heapIn), len(b.heapOut))
	}
	switch layout {
	case layoutAfter:
		inBuf, win = b.inAfter[len(b.inAfter)-len(in):], b.outAfter[len(b.outAfter)-n:]
	case layoutBefore:
		inBuf, win = b.inBefore[:len(in):len(in)], b.outBefore[:n:n]
	default:
		inBuf, win = b.heapIn[:len(in):len(in)], b.heapOut[:n:n]
	}
	copy(inBuf, in)
	fillDst(win)
	return inBuf, win
}

// streamCall is one decompressStream call of a trace: its result and
// counts, the stream state after it and a copy of the whole output window.
type streamCall struct {
	res       libdeflateResult
	nIn, nOut int
	s         streamState
	win       []byte
}

type streamTraceResult struct {
	calls []streamCall
	out   []byte // the output (without dict); with ErrBadData the data before the invalid item
	nIn   int    // input consumed (with ErrBadData: up to the invalid item, as CorruptOffset)
	err   error  // nil, ErrBadData, io.ErrUnexpectedEOF or errStreamLimit
}

// trace decodes src (with the preset dictionary dict) as StreamReader
// does: each call gets the unconsumed input, step() more bytes whenever the
// decoder asks for more, and an output window of the last streamWindow
// bytes of output plus room() bytes; on libdeflateBadData the call's input
// is decoded again with the generic loop only (s.slow) from the block start
// record, in the same buffers (StreamReader.step). The window holds at
// most streamWindow (32 KiB) bytes of history where the reader keeps up to
// 128 KiB in front of wpos; for the decoder that is equivalent, since
// DEFLATE offsets are at most 32768 and both pass the window start as the
// base of the offset check (with less history both windows hold all of
// it), so every offset is accepted or rejected alike. The reader's slide,
// grow and stHeader-return policy is TestStreamReaderAsmEquiv's.
func (b *streamEquivBufs) trace(t testing.TB, src, dict []byte, step, room func() int, layout, limit int) streamTraceResult {
	t.Helper()
	d := NewDecompressor()
	var tr streamTraceResult
	var s streamState
	all := append([]byte(nil), dict...)
	ipos, iend := 0, 0
	for stalls := 0; ; {
		hist := all[max(0, len(all)-streamWindow):]
		in, win := b.place(t, src[ipos:iend], len(hist)+room(), layout)
		copy(win, hist)
		prev := s
		res, nIn, nOut := d.decompressStream(&s, in, win, len(hist))
		tr.calls = append(tr.calls, streamCall{res, nIn, nOut, s, append([]byte(nil), win...)})
		if res != libdeflateSuccess {
			rs, rin, rout := prev, 0, len(hist)
			if s.blkSet {
				rs, rin, rout = streamState{bitbuf: s.blkBitbuf, bitsleft: s.blkBitsleft}, s.blkIn, s.blkOut
			}
			rs.slow = true
			res, nIn, nOut = d.decompressStream(&rs, in[rin:], win, rout)
			tr.calls = append(tr.calls, streamCall{res, nIn, nOut, rs, append([]byte(nil), win...)})
			if res != libdeflateBadData || nOut < rout || nOut > len(win) {
				t.Fatalf("stream trace: the slow rerun gave %v, out %d (from %d, end %d)", res, nOut, rout, len(win))
			}
			tr.out = append(all, win[len(hist):nOut]...)[len(dict):]
			tr.nIn, tr.err = ipos+rin+nIn, ErrBadData
			return tr
		}
		if nIn < 0 || nIn > len(in) || nOut < len(hist) || nOut > len(win) {
			t.Fatalf("stream trace: counts out of range: in %d of %d, out %d (from %d, end %d)", nIn, len(in), nOut,
				len(hist), len(win))
		}
		progress := nIn > 0 || nOut > len(hist)
		ipos += nIn
		all = append(all, win[len(hist):nOut]...)
		tr.out, tr.nIn = all[len(dict):], ipos
		if len(tr.out) > limit {
			tr.err = errStreamLimit
			return tr
		}
		switch s.stop {
		case stopEnd:
			return tr
		case stopNeedInput:
			if iend == len(src) {
				tr.err = io.ErrUnexpectedEOF
				return tr
			}
			iend = min(len(src), iend+step())
			progress = true
		}
		if !progress {
			if stalls++; stalls > 2 {
				t.Fatalf("stream trace: no progress (stop %d, in %d/%d)", s.stop, ipos, iend)
			}
		} else {
			stalls = 0
		}
	}
}

// sameTrace fails unless got and want are identical call by call.
func sameTrace(t testing.TB, name string, got, want streamTraceResult) {
	t.Helper()
	for i := range min(len(got.calls), len(want.calls)) {
		g, w := got.calls[i], want.calls[i]
		if g.res != w.res || g.nIn != w.nIn || g.nOut != w.nOut || g.s != w.s || !bytes.Equal(g.win, w.win) {
			t.Fatalf("%s: call %d of %d: asm result=%d in=%d out=%d state %+v, Go result=%d in=%d out=%d state %+v, "+
				"window equal %v", name, i, len(want.calls), g.res, g.nIn, g.nOut, g.s, w.res, w.nIn, w.nOut, w.s,
				bytes.Equal(g.win, w.win))
		}
	}
	if len(got.calls) != len(want.calls) || got.err != want.err || got.nIn != want.nIn || !bytes.Equal(got.out, want.out) {
		t.Fatalf("%s: asm %d calls err %v in %d out %d, Go %d calls err %v in %d out %d", name, len(got.calls), got.err,
			got.nIn, len(got.out), len(want.calls), want.err, want.nIn, len(want.out))
	}
}

// streamPlan is a way of feeding a trace: input step and output room.
type streamPlan struct {
	name       string
	step, room func(r *rand.Rand) int
}

// streamAsmPlans: StreamReader's own sizes; input windows and rooms swept
// around FASTLOOP_MAX_BYTES_READ and FASTLOOP_MAX_BYTES_WRITTEN (so the
// assembly stops at every position relative to both margins); random; tiny.
var streamAsmPlans = []streamPlan{
	{"reader", func(*rand.Rand) int { return 64 << 10 }, func(*rand.Rand) int { return 96 << 10 }},
	{"margins", func(r *rand.Rand) int { return fastloopMaxBytesRead - 8 + r.IntN(48) },
		func(r *rand.Rand) int { return max(deflateMaxMatchLen, fastloopMaxBytesWritten-40+r.IntN(400)) }},
	{"random", func(r *rand.Rand) int { return 1 + r.IntN(4096) }, func(r *rand.Rand) int { return 258 + r.IntN(70000) }},
	{"tiny", func(r *rand.Rand) int { return 1 + r.IntN(64) }, func(r *rand.Rand) int { return 258 + r.IntN(40) }},
}

// checkStreamAsm traces src with each plan, the assembly off on heap
// buffers and on with guard pages after and before, with the assembly's
// per-call output bound at the default and at each of chunks, and fails on
// any difference. It returns the assembly-off trace of the first plan.
func checkStreamAsm(t testing.TB, b *streamEquivBufs, name string, src, dict []byte, limit int, seed uint64,
	plans []streamPlan, chunks []int) streamTraceResult {
	t.Helper()
	defer func(c int) { fastloopChunk = c }(fastloopChunk)
	var first streamTraceResult
	for pi, p := range plans {
		feed := func() (func() int, func() int) {
			r := rand.New(rand.NewPCG(seed, uint64(pi)))
			return func() int { return p.step(r) }, func() int { return p.room(r) }
		}
		var want streamTraceResult
		withDeflateAsm(false, func() {
			step, room := feed()
			want = b.trace(t, src, dict, step, room, layoutHeap, limit)
		})
		if pi == 0 {
			first = want
		}
		withDeflateAsm(true, func() {
			for _, c := range append([]int{fastloopChunkOut}, chunks...) {
				fastloopChunk = c
				for _, l := range []int{layoutAfter, layoutBefore} {
					step, room := feed()
					sameTrace(t, fmt.Sprintf("%s/%s/chunk%d/layout%d", name, p.name, c, l), b.trace(t, src, dict, step,
						room, l, limit), want)
				}
			}
		})
	}
	return first
}

// streamAsmCase is a stream of the stream equivalence tests.
type streamAsmCase struct {
	name     string
	in, dict []byte
	size     int // the decoded size if known (the output limit is derived from it)
}

// limit is the output limit: the decoded size (for invalid and mutated
// streams the size of the original) plus slack, or for golden streams
// without output the DEFLATE maximum.
func (c streamAsmCase) limit() int {
	if c.size > 0 {
		return max(c.size+1024, 1<<16)
	}
	return max(1032*len(c.in)+64, 1<<16)
}

// dictStreams returns streams decoded with preset dictionaries of 1, 32767
// and 32768 bytes, after 0, 1 or 300 bytes of output: valid ones whose
// first match is at distance exactly the history length (dictionary and
// output), followed by matches at random distances into the whole history
// and literals (so the fast loop reaches the matches), and, where DEFLATE
// can code the distance, the same first match one byte further back
// (ErrBadData; size is that of the output before it). This is the input to
// the assembly's offset check that only the stream decoder has: the
// history in front of outNext in the same window (base win[0]).
func dictStreams() (cases []streamAsmCase) {
	tail := litsOf(rndBytes(30, 100))
	for _, n := range []int{1, 32767, 32768} {
		dict := rndBytes(uint64(31+n), n)
		for _, lead := range []int{0, 1, 300} {
			hist := n + lead
			pre := litsOf(rndBytes(uint64(40+lead), lead))
			for _, d := range []int{hist, hist + 1} {
				if d > deflateMaxMatchOffset {
					continue
				}
				toks := append(append([]tok(nil), pre...), match(258, d))
				name := fmt.Sprintf("dict%d/lead%d/at-history", n, lead)
				if d == hist {
					r := rand.New(rand.NewPCG(uint64(n), uint64(lead)))
					outLen := lead + 258
					for range 64 {
						l := 3 + r.IntN(256)
						toks = append(toks, lit(byte(r.Uint64())), match(l, 1+r.IntN(min(deflateMaxMatchOffset, n+outLen+1))))
						outLen += 1 + l
					}
				} else {
					name = fmt.Sprintf("dict%d/lead%d/past-history", n, lead)
				}
				toks = append(toks, tail...)
				size := lead
				if d == hist {
					size = len(tokOutput(dict, toks)) - n
				}
				var w bitWriter
				w.static(true, toks)
				cases = append(cases, streamAsmCase{name, w.bytes(), dict, size})
			}
		}
	}
	return cases
}

// streamAsmCases returns crafted streams (output and input margins, every
// offset 1..300, the refill thresholds, golden, bad data after valid data
// incl. a dictionary, valid and invalid streams with dictionaries of up to
// 32 KiB), corpus streams at every level (with stdlib's) of at most maxLen
// bytes (every every-th one), and their mutants and truncations.
func streamAsmCases(t testing.TB, maxLen, every int) (crafted, all []streamAsmCase) {
	for _, s := range append(append(outputMarginStreams(), offsetStreams()...), refillStreams()...) {
		crafted = append(crafted, streamAsmCase{s.name, s.in, nil, s.size})
	}
	for seed := uint64(0); seed < 2; seed++ {
		in, want := inputMarginStream(seed)
		crafted = append(crafted, streamAsmCase{fmt.Sprintf("inmargin/%d", seed), in, nil, len(want)})
	}
	names, ins, dicts, wants := badStreams()
	for i := range ins {
		crafted = append(crafted, streamAsmCase{names[i], ins[i], dicts[i], len(wants[i])})
	}
	crafted = append(crafted, dictStreams()...)
	all = append(all, crafted...)
	for _, g := range goldenCases() {
		if len(g.in) <= maxLen {
			all = append(all, streamAsmCase{"golden/" + g.name, g.in, nil, len(g.out)})
		}
	}
	corpus := asmEquivCorpus(t, maxLen)
	r := rand.New(rand.NewPCG(42, 43))
	for i, s := range corpus {
		if i%every != 0 || (s.size < 1000 && i%(4*every) != 0) {
			continue
		}
		all = append(all, streamAsmCase{s.name, s.in, nil, s.size})
		if i%(2*every) == 0 && len(s.in) > 8 {
			all = append(all, streamAsmCase{s.name + "/mut", streamMutate(r, s.in), nil, s.size},
				streamAsmCase{s.name + "/cut", s.in[:r.IntN(len(s.in))], nil, s.size})
		}
	}
	return crafted, all
}

// TestStreamAsmEquiv: stream traces with the assembly off and on (guard
// pages) for every case and plan, the crafted cases also with the per-call
// output bound lowered (fastloopChunk 1, 64, 300); every trace without a
// dictionary also equals the whole-buffer decoder's result, every trace
// with one the standard library's.
func TestStreamAsmEquiv(t *testing.T) {
	if !useFastloopAsm {
		t.Log("no assembly fast loop in this build or CPU: the comparisons run the Go loop twice")
	}
	maxLen, every := 1<<18, 1
	if testing.Short() || raceEnabled {
		maxLen, every = 1<<16, 3
	}
	crafted, all := streamAsmCases(t, maxLen, every)
	maxIn := 0
	for _, c := range all {
		maxIn = max(maxIn, len(c.in))
	}
	b := newStreamEquivBufs(t, maxIn, streamEquivOut)
	calls := 0
	defer func() {
		t.Logf("%d cases, %d decompressStream calls (assembly-off traces of the first plan)", len(all), calls)
	}()
	for i, c := range all {
		var chunks []int
		if i < len(crafted) {
			chunks = []int{1, 64, 300}
		}
		tr := checkStreamAsm(t, b, c.name, c.in, c.dict, c.limit(), uint64(i), streamAsmPlans, chunks)
		calls += len(tr.calls)
		if c.dict != nil {
			// The standard library with the same dictionary: the same
			// data, and an error exactly where ours is ErrBadData.
			sOut, sErr := io.ReadAll(stdflate.NewReaderDict(bytes.NewReader(c.in), c.dict))
			var ce stdflate.CorruptInputError
			if (tr.err == nil) != (sErr == nil) || (tr.err == ErrBadData) != errors.As(sErr, &ce) ||
				len(tr.out) != c.size || !bytes.Equal(tr.out, sOut) {
				t.Fatalf("%s: err %v, %d bytes (want %d), stdlib err %v, %d bytes", c.name, tr.err, len(tr.out), c.size,
					sErr, len(sOut))
			}
			continue
		}
		wOut, wIn, wErr := wholeResult(NewDecompressor(), c.in, c.limit())
		switch {
		case wErr == ErrInsufficientSpace:
		case (tr.err == nil) != (wErr == nil):
			t.Fatalf("%s: stream err %v, whole-buffer err %v", c.name, tr.err, wErr)
		case tr.err == nil && (tr.nIn != wIn || !bytes.Equal(tr.out, wOut)):
			t.Fatalf("%s: stream in %d out %d, whole-buffer in %d out %d", c.name, tr.nIn, len(tr.out), wIn, len(wOut))
		case tr.err != nil && tr.err != ErrBadData && tr.err != io.ErrUnexpectedEOF:
			t.Fatalf("%s: stream err %v", c.name, tr.err)
		}
	}
}

// readerResult is what a StreamReader run shows: the data read, the error,
// CorruptOffset, the decoder calls and the bytes left in the source (exact
// sources, after reading the trailer when the stream is valid).
type readerResult struct {
	out     []byte
	err     error
	corrupt int64
	calls   int
	trailer string
	rest    []byte
}

var streamTrailer = []byte("TRAILER!")

// readerRun reads c.in followed by streamTrailer from src.
func readerRun(src streamSource, c streamAsmCase, seed uint64) readerResult {
	rd, rest := src.make(append(append([]byte(nil), c.in...), streamTrailer...))
	var z StreamReader
	z.Reset(rd, c.dict)
	var r readerResult
	r.out, r.err = readAllSR(&z, rand.New(rand.NewPCG(seed, 1)))
	r.corrupt, r.calls = z.CorruptOffset(), z.calls
	if r.err == io.EOF {
		h := make([]byte, len(streamTrailer))
		if err := z.ReadFull(h); err == nil {
			r.trailer = string(h)
		}
	}
	if rest != nil {
		r.rest = rest()
	}
	return r
}

// TestStreamReaderAsmEquiv: the StreamReader through every source kind
// (in -short a rotating subset per case) with the assembly off, on, and on
// with fastloopChunk 1 and 64 (every 4th case): identical results; they
// equal the whole-buffer decoder's (valid: the same data and the trailer
// read next; invalid: the same rejection, the data before ErrBadData that
// of one slow call), and the whole-buffer decoder equals libdeflate.
func TestStreamReaderAsmEquiv(t *testing.T) {
	maxLen, every := 1<<18, 2
	if testing.Short() || raceEnabled {
		maxLen, every = 1<<16, 4
	}
	_, all := streamAsmCases(t, maxLen, every)
	defer func(c int) { fastloopChunk = c }(fastloopChunk)
	var reqs []ldReq
	var reqCase []streamAsmCase
	runs := 0
	defer func() { t.Logf("%d cases, %d reader runs with the assembly off", len(all), runs) }()
	for i, c := range all {
		full := append(append([]byte(nil), c.in...), streamTrailer...) // what the reader reads
		srcs := streamSources
		if testing.Short() || raceEnabled {
			srcs = []streamSource{streamSources[i%len(streamSources)], streamSources[(i+3)%len(streamSources)],
				streamSources[(i+7)%len(streamSources)]}
		}
		for j, src := range srcs {
			name := fmt.Sprintf("%s/%s", c.name, src.name)
			seed := uint64(i*len(streamSources) + j)
			var want readerResult
			withDeflateAsm(false, func() { want = readerRun(src, c, seed) })
			runs++
			chunks := []int{fastloopChunkOut}
			if i%4 == 0 {
				chunks = append(chunks, 1, 64)
			}
			withDeflateAsm(true, func() {
				for _, ch := range chunks {
					fastloopChunk = ch
					got := readerRun(src, c, seed)
					if got.err != want.err || got.corrupt != want.corrupt || got.calls != want.calls ||
						got.trailer != want.trailer || !bytes.Equal(got.out, want.out) || !bytes.Equal(got.rest, want.rest) {
						t.Fatalf("%s (chunk %d): asm err %v out %d corrupt %d calls %d trailer %q rest %d, "+
							"Go err %v out %d corrupt %d calls %d trailer %q rest %d", name, ch, got.err, len(got.out),
							got.corrupt, got.calls, got.trailer, len(got.rest), want.err, len(want.out), want.corrupt,
							want.calls, want.trailer, len(want.rest))
					}
				}
				fastloopChunk = fastloopChunkOut
			})
			if c.dict != nil {
				// The standard library with the same dictionary: the same
				// data, and an error exactly where ours is ErrBadData.
				sOut, sErr := io.ReadAll(stdflate.NewReaderDict(bytes.NewReader(c.in), c.dict))
				var ce stdflate.CorruptInputError
				if (want.err == io.EOF) != (sErr == nil) || (want.err == ErrBadData) != errors.As(sErr, &ce) ||
					len(want.out) != c.size || !bytes.Equal(want.out, sOut) {
					t.Fatalf("%s: err %v, %d bytes (want %d), stdlib err %v, %d bytes", name, want.err, len(want.out),
						c.size, sErr, len(sOut))
				}
				continue
			}
			wOut, wIn, wErr := wholeResult(NewDecompressor(), full, c.limit())
			switch {
			case wErr == ErrInsufficientSpace:
			case (want.err == io.EOF) != (wErr == nil):
				t.Fatalf("%s: stream err %v, whole-buffer err %v", name, want.err, wErr)
			case want.err == io.EOF && !bytes.Equal(want.out, wOut):
				t.Fatalf("%s: stream %d bytes, whole-buffer %d", name, len(want.out), len(wOut))
			case want.err == io.EOF && wIn+len(streamTrailer) <= len(full) &&
				want.trailer != string(full[wIn:wIn+len(streamTrailer)]):
				// The reader consumed exactly the stream: the bytes after
				// it are read next.
				t.Fatalf("%s: input after the stream %q, whole-buffer in %d", name, want.trailer, wIn)
			case want.err == ErrBadData:
				if ref := slowBadData(t, full, nil, c.limit()); !bytes.Equal(want.out, ref) {
					t.Fatalf("%s: %d bytes before ErrBadData, want %d", name, len(want.out), len(ref))
				}
			case want.err != io.EOF && want.err != io.ErrUnexpectedEOF:
				t.Fatalf("%s: error %v", name, want.err)
			}
		}
		if c.dict == nil {
			reqs = append(reqs, ldReq{c.name, full, c.limit(), false})
			reqCase = append(reqCase, c)
		}
	}
	// The whole-buffer decoder (assembly as configured) == libdeflate.
	lds := ldBatch(t, reqs)
	for i, r := range reqs {
		dst := make([]byte, r.outSize)
		res, nIn, nOut := NewDecompressor().decompressImpl(r.in, dst, true, true)
		if l := lds[i]; res != l.res || nIn != l.nIn || nOut != l.nOut || !bytes.Equal(dst[:nOut], l.out) {
			t.Fatalf("%s: whole-buffer result=%d in=%d out=%d, libdeflate result=%d in=%d out=%d", reqCase[i].name, res,
				nIn, nOut, l.res, l.nIn, l.nOut)
		}
	}
}

// fuzzStreamAsm is FuzzDeflateAsmEquiv's stream part: data's stream traced
// with one plan derived from h (input steps 1..300, rooms 258..2257) and a
// preset dictionary derived from h (none; the first h%64 bytes of the
// input; or 1..32768 pseudo-random bytes, so that far offsets in the input
// reach into the history), the assembly off and on (guard pages), the
// per-call output bound at the default and at 1 + h%1000.
func fuzzStreamAsm(t *testing.T, in []byte, h int) {
	step, room := 1+h%300, 258+(h*7)%2000
	plan := streamPlan{"fuzz", func(*rand.Rand) int { return step }, func(*rand.Rand) int { return room }}
	limit := min(max(1032*len(in)+64, 1<<16), 4<<20)
	var dict []byte
	switch h % 4 {
	case 1:
		dict = in[:min(len(in), h%64)]
	case 3:
		dict = rndBytes(uint64(h), 1+(h*33)%deflateMaxMatchOffset)
	}
	b := newStreamEquivBufs(t, len(in), streamEquivOut)
	checkStreamAsm(t, b, "fuzz/stream", in, dict, limit, uint64(h), []streamPlan{plan}, []int{1 + h%1000})
}

// BenchmarkStreamReaderFastloop reads a text corpus case compressed at
// levels 1, 6 and 9 through a StreamReader (bytes.Reader source) with the
// assembly fast loop off and on.
func BenchmarkStreamReaderFastloop(b *testing.B) {
	var data []byte
	for _, c := range deflatetest.Cases(true) {
		if c.Name == "size-text/300001" {
			data = c.Data
		}
	}
	if data == nil {
		b.Fatal("corpus case size-text/300001 not found")
	}
	buf := make([]byte, 32<<10)
	for _, l := range []int{1, 6, 9} {
		comp := asmEquivCompress(b, data, l)
		for _, on := range []bool{false, true} {
			b.Run(fmt.Sprintf("L%d/asm=%v", l, on), func(b *testing.B) {
				var z StreamReader
				br := bytes.NewReader(comp)
				b.SetBytes(int64(len(data)))
				withDeflateAsm(on, func() {
					for b.Loop() {
						br.Reset(comp)
						z.Reset(br, nil)
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
			})
		}
	}
}
