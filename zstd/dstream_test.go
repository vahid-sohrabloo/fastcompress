package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// dstreamRun decodes data with d through decompressStreamCode as `cref
// dstream` does: the input in chunks whose sizes chunk returns (each
// offered until it is consumed and the output buffer comes back not full),
// into an output buffer of outSize bytes. It returns every byte produced
// (on an error, the bytes of the calls before it), the error, and the hint
// of the last call that moved input or output: not 0 means the input ended
// inside a frame (cref's "incomplete"). It stops after more than limit
// bytes of output (0: no limit). A call that moves nothing while input is
// left fails t, as it makes cref fail.
func dstreamRun(t testing.TB, d *dctx, data []byte, chunk func() int, outSize, limit int) (out []byte, e dErr, last int) {
	t.Helper()
	buf := make([]byte, outSize)
	for pos := 0; pos < len(data); {
		in := inBuffer{src: data[pos : pos+min(chunk(), len(data)-pos)]}
		for {
			o := outBuffer{dst: buf}
			in0 := in.pos
			hint, e := d.decompressStreamCode(&o, &in)
			out = append(out, buf[:o.pos]...)
			if e != 0 {
				return out, e, last
			}
			if in.pos != in0 || o.pos > 0 {
				last = hint
			} else if in.pos < len(in.src) {
				t.Fatalf("no progress at input %d", pos+in.pos)
			}
			if limit > 0 && len(out) > limit {
				return out, 0, last
			}
			if in.pos == len(in.src) && o.pos < len(o.dst) {
				break
			}
		}
		pos += len(in.src)
	}
	return out, 0, last
}

func fixedChunks(n int) func() int { return func() int { return n } }

// TestDecompressStreamCref decodes every decode corpus entry through
// decompressStream with input chunks of 1 byte, 1000 bytes and random
// sizes 1..200000 (seed 1), each with output buffers of 1, 4096 and
// dStreamOutSize bytes: the output is Want (or C's dictionary error).
func TestDecompressStreamCref(t *testing.T) {
	chunkings := []struct {
		name string
		f    func() func() int
	}{
		{"in1", func() func() int { return fixedChunks(1) }},
		{"in1000", func() func() int { return fixedChunks(1000) }},
		{"inRand", func() func() int {
			r := rand.New(rand.NewPCG(1, 0))
			return func() int { return 1 + r.IntN(200000) }
		}},
	}
	for _, name := range creftest.FrameNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := creftest.LoadFrame(t, name)
			var dd *ddict
			if f.Dict != nil {
				dd = mustDDict(t, f.Dict, f.RawDict)
			}
			for _, c := range chunkings {
				for _, outSize := range []int{1, 4096, dStreamOutSize} {
					d := newDCtx()
					d.ddict = dd
					got, e, last := dstreamRun(t, d, f.Data, c.f(), outSize, 0)
					what := fmt.Sprintf("%s/out%d", c.name, outSize)
					if f.CErr != "" {
						if e != dErrDictionaryWrong {
							t.Errorf("%s: error %d, want dictionary_wrong (C %q)", what, e, f.CErr)
						}
						continue
					}
					if e != 0 || last != 0 || !bytes.Equal(got, f.Want) {
						t.Errorf("%s: err %d, hint %d, %d bytes (want %d), equal %v", what, e, last, len(got), len(f.Want), bytes.Equal(got, f.Want))
					}
				}
			}
		})
	}
}

// TestDecompressStreamDictSet decodes the decode corpus entries made with
// a dictionary through decompressStream with every test dictionary
// registered in a set (ddicts) and no default dictionary: each frame
// picks its own by ID (the raw dictionary has ID 0, as its frames).
func TestDecompressStreamDictSet(t *testing.T) {
	set := newDDictSet()
	for _, dc := range decDicts {
		set.add(mustDDict(t, readTestdata(t, dc.file), dc.raw))
	}
	n := 0
	for _, name := range creftest.FrameNames(t) {
		if !strings.HasPrefix(name, "dict/") {
			continue
		}
		f := creftest.LoadFrame(t, name)
		d := newDCtx()
		d.ddicts = set
		got, e, last := dstreamRun(t, d, f.Data, fixedChunks(1000), 4096, 0)
		if e != 0 || last != 0 || !bytes.Equal(got, f.Want) {
			t.Errorf("%s: err %d, hint %d, equal %v", name, e, last, bytes.Equal(got, f.Want))
		}
		n++
	}
	if n == 0 {
		t.Fatal("no dictionary entries")
	}
}

// TestDecompressStreamHostage pins C's hostage byte: when a frame is
// decoded but its output not yet flushed, decompressStream keeps the
// input's last byte (in.pos one short) and returns 1; the call that
// flushes the rest releases it and returns 0, or returns 1 (stage
// zdssRead) when the input given no longer holds it.
func TestDecompressStreamHostage(t *testing.T) {
	text := malText(3000)
	frame := malEncode(t, text, false) // no checksum: the last block ends the frame
	for _, release := range []bool{true, false} {
		d := newDCtx()
		in := inBuffer{src: frame}
		buf := make([]byte, 1000)
		o := outBuffer{dst: buf}
		hint, e := d.decompressStreamCode(&o, &in)
		if e != 0 || hint != 1 || in.pos != len(frame)-1 || o.pos != len(buf) {
			t.Fatalf("first call: err %d, hint %d, in.pos %d/%d, out %d", e, hint, in.pos, len(frame), o.pos)
		}
		got := slices.Clone(buf)
		if !release {
			in.pos = len(frame) // the caller offers no input
		}
		for {
			o = outBuffer{dst: buf}
			hint, e = d.decompressStreamCode(&o, &in)
			got = append(got, buf[:o.pos]...)
			if e != 0 || hint == 0 || o.pos < len(buf) {
				break
			}
		}
		if release && (e != 0 || hint != 0 || in.pos != len(frame)) {
			t.Errorf("release: err %d, hint %d, in.pos %d", e, hint, in.pos)
		}
		if !release && (e != 0 || hint != 1 || d.streamStage != zdssRead) {
			t.Errorf("no release: err %d, hint %d, stage %d", e, hint, d.streamStage)
		}
		if !bytes.Equal(got, text) {
			t.Errorf("release %v: output differs", release)
		}
	}
}

// TestDecompressStreamNoProgress pins C's no-progress errors: the 16th
// call in a row that moves neither input nor output fails, with
// noForwardProgress_inputEmpty when there is no input and
// noForwardProgress_destFull when the output buffer is full.
func TestDecompressStreamNoProgress(t *testing.T) {
	frame := malEncode(t, malText(3000), false)
	for _, c := range []struct {
		name string
		in   inBuffer
		out  int
		want dErr
	}{
		{"input empty", inBuffer{src: frame[:20], pos: 20}, 100, dErrNoForwardProgressInputEmpty},
		{"dest full", inBuffer{src: frame}, 0, dErrNoForwardProgressDestFull},
	} {
		d := newDCtx()
		if c.out == 0 {
			// decode a block into outBuff, which then cannot be flushed
			in := inBuffer{src: frame}
			if _, e := d.decompressStreamCode(&outBuffer{}, &in); e != 0 {
				t.Fatal(e)
			}
			c.in = in
		} else {
			in := inBuffer{src: frame[:20]}
			if _, e := d.decompressStreamCode(&outBuffer{dst: make([]byte, c.out)}, &in); e != 0 {
				t.Fatal(e)
			}
		}
		for i := 1; i <= noForwardProgressMax; i++ {
			in := c.in
			_, e := d.decompressStreamCode(&outBuffer{dst: make([]byte, c.out)}, &in)
			if i < noForwardProgressMax && e != 0 || i == noForwardProgressMax && e != c.want {
				t.Fatalf("%s: call %d: err %d", c.name, i, e)
			}
		}
	}
}

// windowInput returns n bytes that repeat with period w (a few bytes
// changed every 1000), so a compressor with window w uses offsets up to w.
func windowInput(w, n int) []byte {
	r := rand.New(rand.NewPCG(uint64(w), uint64(n)))
	b := make([]byte, n)
	for i := range b {
		switch {
		case i < w:
			b[i] = 'a' + byte(r.IntN(16))
		case i%1000 == 0:
			b[i] = byte(r.Uint32())
		default:
			b[i] = b[i-w]
		}
	}
	return b
}

// TestDecompressStreamWindowCref decodes C frames (cref compress, window
// logs 10 and 17, content size in the header or not) of window input with
// sizes at the window size and at the stream buffer size (C's
// ZSTD_decodingBufferSize_min) -1, exact and +1, and 5 windows: the
// round buffer wraps (checkContinuity makes the previous segment the
// extDict) for the frames without a content size larger than the buffer.
func TestDecompressStreamWindowCref(t *testing.T) {
	for _, wlog := range []int{10, 17} {
		w := 1 << wlog
		bs := min(w, blockSizeMax)
		rb := w + 2*bs + 2*wildcopyOverlength
		for _, n := range []int{w - 1, w, w + 1, rb - 1, rb, rb + 1, 5 * w} {
			for _, pledged := range []string{"0", "1"} {
				name := fmt.Sprintf("W%d/n%d/p%s", wlog, n, pledged)
				t.Run(name, func(t *testing.T) {
					in := windowInput(w, n)
					frame := creftest.Run(t, in, "compress", "-level", "19", "-window-log", strconv.Itoa(wlog), "-pledged", pledged, "-checksum", "1")
					for _, outSize := range []int{1, 4096, dStreamOutSize} {
						for _, chunk := range []int{1, 1000, len(frame)} {
							d := newDCtx()
							got, e, last := dstreamRun(t, d, frame, fixedChunks(chunk), outSize, 0)
							if e != 0 || last != 0 || !bytes.Equal(got, in) {
								t.Fatalf("out %d chunk %d: err %d, hint %d, equal %v", outSize, chunk, e, last, bytes.Equal(got, in))
							}
							if wrap := len(d.extDict) > 0; pledged == "0" && n > rb && chunk < len(frame) && !wrap {
								t.Errorf("out %d chunk %d: the buffer did not wrap", outSize, chunk)
							}
						}
					}
				})
			}
		}
	}
}

// TestDecompressStreamWrapGrowCref pins the round buffer's growth against
// C's buffer, allocated whole: with a 1 KiB window (round buffer 3136
// bytes, FCS unknown), a compressed block grows outBuff to 2113 bytes,
// raw blocks fill it to 2113 and the buffer wraps (the previous segment,
// bytes 0..2113, becomes the extDict). Raw blocks write 0..2048; then a
// compressed block (which needs more room) writes 60 literals at
// 2048..2108, over the extDict's tail in C's buffer, and copies 8 bytes
// from that tail (offset 2121: beyond the window, within C's history
// check). C copies the literals just written there; so must Go, whose
// buffer therefore gets its full size before it wraps.
func TestDecompressStreamWrapGrowCref(t *testing.T) {
	raw := func(n int, seed byte) malBlock {
		b := make([]byte, n)
		for i := range b {
			b[i] = seed + byte(i*13)
		}
		return malBlock{typ: btRaw, size: n, body: b}
	}
	frame := malFrame{hdr: malFHdr{winDesc: 0}.bytes(), blocks: []malBlock{
		malCBlock(craftBlock(t, malText(1024), nil), false),
		raw(1024, 1), raw(65, 2), // outStart 2113: wrap
		raw(1024, 3), raw(1024, 4),
		malCBlock(craftBlock(t, raw(60, 5).body, []malSeq{{ll: 60, ml: 8, off: 2108 + 13}}), true),
	}}.bytes()
	for _, chunk := range []int{7, len(frame)} {
		if k, _ := streamCref(t, frame, nil, nil, chunk, 4096); k != "accept" {
			t.Errorf("chunk %d: %s", chunk, k)
		}
	}
}

// TestDecompressStreamBlockRoomCref pins the error of a compressed block
// regenerating more than its output space, against C: with a 64 KiB
// window, a block regenerating 195,012 bytes where 186,672 are left in the
// round buffer runs out of it in C and in Go, which makes all of it room
// (dstSize_tooSmall); with a 1 MiB
// window, a block regenerating 325,004 bytes fits C's buffer (C then
// rejects it as over blockSizeMax, corruption_detected) but not Go's room,
// and Go names it so (dErrDecodedBlockTooLarge).
func TestDecompressStreamBlockRoomCref(t *testing.T) {
	lits := []byte("abcd")
	for _, c := range []struct {
		name    string
		winDesc byte
		before  []malBlock
		seqs    []malSeq
		want    dErr
	}{
		{"window-64KiB", 6 << 3, []malBlock{malCBlock(craftBlock(t, []byte("x"), nil), false), {typ: btRLE, size: 9999, body: []byte{'r'}}},
			slices.Repeat([]malSeq{{ll: 4, ml: 65000, off: 1}}, 3), dErrBlockDstSizeTooSmall},
		{"window-1MiB", malWindow1M, nil, slices.Repeat([]malSeq{{ll: 4, ml: 65000, off: 1}}, 5), dErrDecodedBlockTooLarge},
	} {
		blocks := append(c.before, malCBlock(craftBlock(t, slices.Repeat(lits, len(c.seqs)), c.seqs), true))
		frame := malFrame{hdr: malFHdr{winDesc: c.winDesc}.bytes(), blocks: blocks}.bytes()
		if k, _ := streamCref(t, frame, nil, nil, 7, 4096); k != "reject" {
			t.Errorf("%s: %s", c.name, k)
		}
		if _, e, _ := dstreamRun(t, newDCtx(), frame, fixedChunks(7), 4096, 0); e != c.want {
			t.Errorf("%s: err %d, want %d", c.name, e, c.want)
		}
	}
}

// TestDecompressStreamResetDicts: reset(false) drops the dictionaries,
// the default one (C: ZSTD_clearDict) and the set (C: refMultipleDDicts
// back to ZSTD_rmd_refSingleDDict); reset(true) keeps them.
func TestDecompressStreamResetDicts(t *testing.T) {
	dd := mustDDict(t, readTestdata(t, "train.dict"), false)
	set := newDDictSet()
	set.add(dd)
	d := newDCtx()
	d.ddict, d.ddicts = dd, set
	d.reset(true)
	if d.ddict != dd || d.ddicts != set {
		t.Fatal("reset(true) dropped a dictionary")
	}
	d.reset(false)
	if d.ddict != nil || d.ddicts != nil {
		t.Fatal("reset(false) kept a dictionary")
	}
}

// TestDecompressStreamOversized pins C's buffer shrinking: after a frame
// with a 1 MiB window, frames with a 1 KiB window keep its buffer sizes
// until the 128th (ZSTD_WORKSPACETOOLARGE_MAXDURATION) in a row for which
// they are 3 times too large, which reallocates them at the small sizes.
func TestDecompressStreamOversized(t *testing.T) {
	big := malFrame{hdr: malFHdr{winDesc: malWindow1M}.bytes(), blocks: []malBlock{{typ: btRaw, last: true, size: 1, body: []byte{'x'}}}}.bytes()
	small := malFrame{hdr: malFHdr{winDesc: 0}.bytes(), blocks: []malBlock{{typ: btRaw, last: true, size: 1, body: []byte{'y'}}}}.bytes()
	d := newDCtx()
	buf := make([]byte, 16)
	decode := func(f []byte) {
		t.Helper()
		in := inBuffer{src: f}
		if hint, e := d.decompressStreamCode(&outBuffer{dst: buf}, &in); e != 0 || hint != 0 || in.pos != len(f) {
			t.Fatalf("err %d, hint %d", e, hint)
		}
	}
	decode(big)
	bigSize := d.outBuffSize
	for i := 1; i <= workspaceTooLargeMaxDuration; i++ {
		decode(small)
		if shrunk := d.outBuffSize < bigSize; shrunk != (i == workspaceTooLargeMaxDuration) {
			t.Fatalf("small frame %d: outBuffSize %d (after the big frame %d)", i, d.outBuffSize, bigSize)
		}
	}
}

// TestDecompressContinueCref decodes every decode corpus entry with the
// bufferless API (nextSrcSizeToDecompress, decompressContinue) into one
// flat buffer, beginning each frame with the entry's dictionary.
func TestDecompressContinueCref(t *testing.T) {
	for _, name := range creftest.FrameNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := creftest.LoadFrame(t, name)
			if f.CErr != "" {
				t.Skip("C error entry")
			}
			var dd *ddict
			if f.Dict != nil {
				dd = mustDDict(t, f.Dict, f.RawDict)
			}
			d := newDCtx()
			out := make([]byte, 0, len(f.Want)+2*blockSizeMax+2*wildcopyOverlength)
			d.decompressBeginUsingDDict(dd)
			for pos := 0; pos < len(f.Data); {
				n := d.nextSrcSizeToDecompress()
				if n == 0 { // end of frame
					d.decompressBeginUsingDDict(dd)
					continue
				}
				typ := d.nextInputType()
				if typ == nitSkippableFrame && d.stage == dsSkipFrame {
					// C does not read the skippable content.
					if _, e := d.decompressContinue(out, cap(out)-len(out), nil, n); e != 0 {
						t.Fatalf("skip: %d", e)
					}
					pos += int(n)
					continue
				}
				r, e := d.decompressContinue(out, cap(out)-len(out), f.Data[pos:pos+int(n)], n)
				if e != 0 {
					t.Fatalf("at %d (input type %d): %d", pos, typ, e)
				}
				out = out[:len(out)+r]
				pos += int(n)
			}
			if d.nextSrcSizeToDecompress() != 0 || !bytes.Equal(out, f.Want) {
				t.Fatalf("expected %d, output equal %v", d.nextSrcSizeToDecompress(), bytes.Equal(out, f.Want))
			}
		})
	}
}

// streamCref compares decompressStream with `cref dstream` on data, with
// input chunks of chunk bytes and outSize-byte output buffers, the
// dictionary dd (cargs loads it into cref): the same output bytes, and
// both accept, both end incomplete or both fail with the same C error
// code. It returns the outcome's name and the Go error (streamClass).
// When a whole frame is in one call
// and the output buffer holds its content size, C takes
// ZSTD_decompressStream's single-pass shortcut (one-shot decoding) and so
// does Go, which applies the streaming checks there (the deviations of
// docs/superpowers/phase2-api-decisions.md, counted by name):
//
//   - window-limit: a window above the maximum, which C one-shot does not
//     check.
//   - block-over-max: a block regenerating more than blockSizeMax, which
//     C one-shot accepts when its output space has room.
//
// And one that needs no shortcut:
//
//   - block-room: a compressed block regenerating more than the room Go
//     makes for it in the round buffer (2 × blockSizeMax and more), less
//     than C's whole buffer: Go fails it with dErrDecodedBlockTooLarge
//     (corruption_detected), C with dstSize_tooSmall when it also runs out
//     of its buffer (both ErrUnexpectedBlockSize).
func streamCref(t *testing.T, data []byte, dd *ddict, cargs []string, chunk, outSize int) (string, error) {
	t.Helper()
	d := newDCtx()
	d.ddict = dd
	got, e, last := dstreamRun(t, d, data, fixedChunks(chunk), outSize, 0)
	args := append(slices.Clone(cargs), "-max-window-log", "29", "-chunk-size", strconv.Itoa(chunk), "-out-size", strconv.Itoa(outSize))
	want, cerr := creftest.DStreamCRef(t, data, args...)
	outcome := ""
	switch {
	case cerr == nil && e == 0 && last == 0:
		outcome = "accept"
	case cerr != nil && cerr.Code == -1 && e == 0 && last != 0:
		outcome = "incomplete"
	case cerr != nil && e != 0 && e.cCode() == cerr.Code:
		outcome = "reject"
	case (cerr == nil || cerr.Code == -1) && e == dErrFrameParameterWindowTooLarge:
		outcome = "window-limit"
	case (cerr == nil || cerr.Code == -1) && e == dErrDecodedBlockTooLarge:
		outcome = "block-over-max"
	case cerr != nil && cerr.Code == int(dErrDstSizeTooSmall) && e == dErrDecodedBlockTooLarge:
		outcome = "block-room"
	default:
		t.Errorf("C %v, Go err %d hint %d", cerr, e, last)
		return "", nil
	}
	if outcome == "window-limit" || outcome == "block-over-max" {
		if !bytes.HasPrefix(want, got) {
			t.Errorf("%s: Go output is not a prefix of C's", outcome)
		}
	} else if !bytes.Equal(got, want) {
		t.Errorf("%s: output differs (%d bytes, C %d)", outcome, len(got), len(want))
	}
	return outcome, streamClass(e, last)
}

// TestDecompressStreamErrorsCref compares decompressStream with cref
// dstream (see streamCref) on the malicious corpus (7-byte input chunks,
// 4096-byte output: C never takes the single-pass shortcut, so only
// block-room is allowed; the error is the sentinel MANIFEST.txt records
// for DecodeAll, incomplete input io.ErrUnexpectedEOF) and on 2,000 seeded mutations of the decoder seeds
// plus splitSeed (whose literals take the split placement in streaming),
// both with 7-byte chunks and with the whole input in one chunk and a
// 1 MiB output buffer (the shortcut).
func TestDecompressStreamErrorsCref(t *testing.T) {
	creftest.Bin(t)
	lines, _ := readManifest(t)
	byFile := map[string]malCase{}
	for _, c := range maliciousCases(t) {
		byFile[c.file()] = c
	}
	t.Run("malicious", func(t *testing.T) {
		for _, l := range lines {
			file, rest, _ := strings.Cut(l, " ")
			expect, _, _ := strings.Cut(rest, " ")
			want, ok := sentinelByName[expect]
			if !ok && expect != "corrupt" {
				t.Fatalf("unknown expectation %q", expect)
			}
			c, ok := byFile[file]
			if !ok {
				t.Fatalf("%s: not built by maliciousCases", file)
			}
			data := c.data
			if !c.gen() {
				var err error
				if data, err = os.ReadFile(filepath.Join(malDir, file)); err != nil {
					t.Fatal(err)
				}
			}
			var dd *ddict
			var cargs []string
			if c.dict != "" {
				dc := decDict{c.dict, c.rawDict}
				dd = mustDDict(t, readTestdata(t, c.dict), c.rawDict)
				cargs = dc.crefArgs(t)
			}
			k, err := streamCref(t, data, dd, cargs, 7, 4096)
			if k != "accept" && k != "incomplete" && k != "reject" && k != "block-room" {
				t.Errorf("%s: %s", c.name, k)
			}
			if c.name == "skip-size-ffffffff" {
				// One-shot C rejects the skippable size that overflows with
				// its header; streaming C waits for the content.
				want = io.ErrUnexpectedEOF
			}
			checkSentinel(t, c.name, err, want)
			if c.control != nil {
				if k, _ := streamCref(t, c.control, dd, cargs, 7, 4096); k != "accept" {
					t.Errorf("%s control: %s", c.name, k)
				}
			}
		}
	})

	var seeds [][]byte
	ents, err := os.ReadDir(seedsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(seedsDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, b)
	}
	splitFrame := splitSeed(t)
	{
		// Streaming places the block's literals (more than
		// litBufferExtraSize) split between the end of the block's output
		// space and litExtraBuffer.
		d := newDCtx()
		d.decompressBegin()
		hs, _ := frameHeaderSizeInternal(splitFrame)
		if e := d.decodeFrameHeader(splitFrame[:hs]); e != 0 {
			t.Fatal(e)
		}
		size, e := decodingBufferSizeInternal(d.fParams.windowSize, d.fParams.frameContentSize, int(d.fParams.blockSizeMax))
		if e != 0 {
			t.Fatal(e)
		}
		if _, e := d.decodeLiteralsBlock(splitFrame[hs+blockHeaderSize:], make([]byte, size), size, isStreaming); e != 0 || d.litBufferLocation != split {
			t.Fatalf("split seed: err %d, literals placed %d", e, d.litBufferLocation)
		}
	}
	seeds = append(seeds, splitFrame)

	const n = 2000
	workers := runtime.GOMAXPROCS(0)
	var mu sync.Mutex
	stats := map[string]int{}
	for w := range workers {
		t.Run(fmt.Sprint("mutations", w), func(t *testing.T) {
			t.Parallel()
			for i := w; i < n; i += workers {
				r := rand.New(rand.NewPCG(uint64(i), 0x5D1F))
				data := mutate(r, seeds)
				k1, _ := streamCref(t, data, nil, nil, 7, 4096)
				k2, _ := streamCref(t, data, nil, nil, max(len(data), 1), 1<<20)
				if k1 != "accept" && k1 != "incomplete" && k1 != "reject" && k1 != "block-room" {
					t.Errorf("7-byte chunks: %s", k1)
				}
				mu.Lock()
				stats["chunk7/"+k1]++
				stats["whole/"+k2]++
				mu.Unlock()
				if t.Failed() {
					t.Fatalf("mutation %d: %x", i, data)
				}
			}
		})
	}
	t.Cleanup(func() { t.Logf("outcomes: %v", stats) })
}

// TestStreamAllocBound feeds frames whose header claims a 2^29 window (and
// FCS 2^60, or a 4 GiB skippable frame) followed by 50 bytes of a block
// that is cut short: decompressStream waits for more input and allocates
// less than 64 KiB, with 1-byte and whole-input chunks.
func TestStreamAllocBound(t *testing.T) {
	body := make([]byte, 50)
	for i := range body {
		body[i] = byte(i*7 + 1)
	}
	win := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, (29 - windowLogAbsoluteMin) << 3}
	winFCS := []byte{0x28, 0xB5, 0x2F, 0xFD, 0xC0, (29 - windowLogAbsoluteMin) << 3}
	winFCS = binary.LittleEndian.AppendUint64(winFCS, 1<<60)
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"compressed", cat(win, malBlockHeader(btCompressed, 100000, true), body)},
		{"raw", cat(win, malBlockHeader(btRaw, 100000, true), body)},
		{"fcs-2^60", cat(winFCS, malBlockHeader(btCompressed, 100000, true), body)},
		{"skippable-4GiB", cat([]byte{0x50, 0x2A, 0x4D, 0x18, 0xFF, 0xFF, 0xFF, 0xFF}, body)},
	} {
		for _, chunk := range []int{1, len(c.in)} {
			d := newDCtx()
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			out, e, last := dstreamRun(t, d, c.in, fixedChunks(chunk), 4096, 0)
			runtime.ReadMemStats(&m1)
			if e != 0 || last == 0 {
				t.Errorf("%s/%d: err %d, hint %d: want incomplete", c.name, chunk, e, last)
			}
			if c.name == "raw" && !bytes.Equal(out, body) {
				t.Errorf("%s/%d: output %d bytes", c.name, chunk, len(out))
			}
			if a := m1.TotalAlloc - m0.TotalAlloc; a >= 64<<10 {
				t.Errorf("%s/%d: allocated %d bytes", c.name, chunk, a)
			}
		}
	}
}

// streamClass returns the error class of a stream decode for
// FuzzDecompressStream: nil for success, io.ErrUnexpectedEOF for an
// incomplete input, the mapped error otherwise.
func streamClass(e dErr, last int) error {
	switch {
	case e != 0:
		return e.toError("stream")
	case last != 0:
		return io.ErrUnexpectedEOF
	}
	return nil
}

// errClass names err's class: the sentinel, or the C code of a
// *corruptError (whose message also names the operation).
func errClass(err error) string {
	var ce *corruptError
	if errors.As(err, &ce) {
		return "corrupt " + strconv.Itoa(ce.code.cCode())
	}
	return err.Error()
}

// sizeClass reports whether err is an output-size error, on which the
// one-shot allocation rule and the stream's buffer may disagree (see
// FuzzDecompressStream).
func sizeClass(err error) bool {
	return errors.Is(err, ErrFrameSizeExceeded) || errors.Is(err, ErrUnexpectedBlockSize) ||
		errors.Is(err, ErrFrameSizeMismatch) || errors.Is(err, ErrDecoderSizeExceeded)
}

// FuzzDecompressStream decodes arbitrary input through decompressStream
// with chunks and output buffers chosen by chunkSeed: no panic. When
// one-shot decoding (max window and memory 64 MiB) succeeds, the stream
// gives the same output and ends at a frame end; when it fails (other than
// on the memory limit), the stream fails too, with the same error class,
// and one output is a prefix of the other. Exceptions: the output-size
// errors (one-shot's output space and the stream's buffer differ, so one
// may fail a block on its size where the other, with more room, decodes
// further and fails it on another check, or stops at the input's end),
// and an RLE block in a frame with
// blockSizeMax 0 (streaming C rejects its 1-byte size, one-shot C accepts
// it when it regenerates nothing), a skippable frame whose size
// overflows 32 bits with its header (one-shot C rejects it,
// frameParameter_unsupported; streaming C waits for its content), and
// input cut short: one-shot decoding stops where a frame or block runs
// past the input's end (io.ErrUnexpectedEOF) without checking what the
// stream checks as the data arrives (a block size above blockSizeMax, a
// frame header field), so the stream may fail with any error there. And
// streaming C does not check the frame content size when a frame ends
// with an empty last block (ZSTD_decompressContinue checks it only after
// a non-empty one): the stream accepts what one-shot decoding rejects with
// ErrFrameSizeMismatch. Likewise it skips a compressed block of size 0
// as empty, which one-shot decoding rejects (ErrBlockTooSmall). After
// either, the stream goes on and may end any way.
func FuzzDecompressStream(f *testing.F) {
	for _, dir := range []string{seedsDir, malDir} {
		ents, _ := os.ReadDir(dir)
		for i, e := range ents {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.HasSuffix(e.Name(), ".zst") {
				f.Add(b, uint64(i))
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, chunkSeed uint64) {
		const limit = 1 << 26
		r := rand.New(rand.NewPCG(chunkSeed, 0))
		outSize := 1 + r.IntN(1<<17)
		maxChunk := 1 + r.IntN(len(data)+1)
		d := newDCtx()
		d.setMaxWindow(limit)
		got, e, last := dstreamRun(t, d, data, func() int { return 1 + r.IntN(maxChunk) }, outSize, limit)
		one := newDCtx()
		one.setMaxWindow(limit)
		one.maxMemory = limit
		want, oe := one.decompressFrames(nil, data, nil)
		if oe == dErrDecoderSizeExceeded {
			return
		}
		if len(got) > limit {
			t.Fatalf("stream output %d bytes, one-shot %d (err %d)", len(got), len(want), oe)
		}
		sc := streamClass(e, last)
		if oe == 0 {
			if sc != nil && !(e == dErrDecodedBlockTooLarge && d.fParams.blockSizeMax == 0) {
				t.Fatalf("one-shot accepts, stream: %v (err %d)", sc, e)
			}
			if sc == nil && !bytes.Equal(got, want) {
				t.Fatalf("outputs differ: stream %d bytes, one-shot %d", len(got), len(want))
			}
			return
		}
		oc := oe.toError("decompress")
		if oe == dErrFrameParameterUnsupported && sc == io.ErrUnexpectedEOF ||
			oe == dErrFrameSizeMismatch || oe == dErrBlockTooSmall {
			if !bytes.HasPrefix(got, want) && !bytes.HasPrefix(want, got) {
				t.Fatalf("outputs diverge: stream %d bytes, one-shot %d", len(got), len(want))
			}
			return
		}
		if sc == nil {
			t.Fatalf("one-shot %v (err %d), stream accepts", oc, oe)
		}
		if !bytes.HasPrefix(got, want) && !bytes.HasPrefix(want, got) {
			t.Fatalf("outputs diverge: stream %d bytes, one-shot %d", len(got), len(want))
		}
		if errClass(sc) != errClass(oc) && oc != io.ErrUnexpectedEOF && !sizeClass(oc) && !sizeClass(sc) {
			t.Fatalf("one-shot %v (err %d), stream %v (err %d)", oc, oe, sc, e)
		}
	})
}
