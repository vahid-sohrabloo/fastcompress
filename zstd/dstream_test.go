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

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
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

// TestDecompressStreamHostageEndErr: when a short last block (frame
// content size not reached) is flushed over several calls, the first
// keeps the hostage byte; the call that finishes the flush returns
// ErrFrameSizeMismatch with the whole input reported consumed, the
// hostage byte included, and all of the block delivered.
func TestDecompressStreamHostageEndErr(t *testing.T) {
	raw := func(n int, last bool) malBlock {
		return malBlock{typ: btRaw, last: last, size: n, body: bytes.Repeat([]byte{'a'}, n)}
	}
	frame := malFrame{hdr: malFHdr{winDesc: 0, fcsSize: 2, fcs: 2000}.bytes(), blocks: []malBlock{raw(1024, false), raw(500, true)}}.bytes()
	d := newDCtx()
	in := inBuffer{src: frame}
	buf := make([]byte, 100)
	n := 0
	for range 100 {
		o := outBuffer{dst: buf}
		_, e := d.decompressStreamCode(&o, &in)
		n += o.pos
		if e != 0 {
			if e != dErrFrameSizeMismatch || in.pos != len(frame) || n != 1524 {
				t.Fatalf("err %d, in.pos %d/%d, %d bytes", e, in.pos, len(frame), n)
			}
			return
		}
	}
	t.Fatal("no error")
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

// TestDecompressStreamDecodeAll pins the deliberate deviations from
// streaming C, with the frames that showed C's result depends on how the
// input is cut (cref dstream accepts some at 1-byte chunks and rejects
// them at 1000, where its single-pass shortcut runs one-shot C): at every
// chunking and output size, the stream gives DecodeAll's output and error.
func TestDecompressStreamDecodeAll(t *testing.T) {
	raw := func(n int, last bool) malBlock {
		return malBlock{typ: btRaw, last: last, size: n, body: bytes.Repeat([]byte{'a'}, n)}
	}
	frame := func(h malFHdr, blocks ...malBlock) []byte { return malFrame{hdr: h.bytes(), blocks: blocks}.bytes() }
	single := func(fcs uint64) malFHdr { return malFHdr{single: true, fcsSize: 1, fcs: fcs} }
	fcs2000 := malFHdr{winDesc: 0, fcsSize: 2, fcs: 2000}  // 1 KiB window: buffer 2000 bytes, no wrap
	ring4000 := malFHdr{winDesc: 0, fcsSize: 2, fcs: 4000} // round buffer 3136 bytes
	rle := func(n int, last bool) malBlock { return malBlock{typ: btRLE, last: last, size: n, body: []byte{'r'}} }
	for _, c := range []struct {
		name string
		in   []byte
		want error // DecodeAll's (nil: *corruptError; io.EOF: none)
	}{
		// blockSizeMax 0: streaming C rejects the RLE block's 1-byte size
		{"fcs0-rle0", frame(single(0), rle(0, true)), io.EOF},
		{"fcs48-empty-last", frame(single(48), raw(0, true)), ErrFrameSizeMismatch},
		{"fcs48-raw10-empty-last", frame(single(48), raw(10, false), raw(0, true)), ErrFrameSizeMismatch},
		{"fcs3-compressed-size0", frame(single(3), malCBlock(nil, false), raw(3, true)), ErrBlockTooSmall},
		{"skippable-fffffff8", []byte{0x50, 0x2A, 0x4D, 0x18, 0xF8, 0xFF, 0xFF, 0xFF, 1, 2, 3}, nil},
		{"fcs-exceeded", frame(fcs2000, raw(1024, false), raw(1024, true)), ErrFrameSizeExceeded},
		{"fcs-short", frame(fcs2000, raw(1024, false), raw(500, true)), ErrFrameSizeMismatch},
		{"ring-fcs-exceeded", frame(ring4000, raw(1024, false), raw(1024, false), raw(1024, false), raw(1024, true)), ErrFrameSizeExceeded},
		{"ring-fcs-exceeded-rle", frame(ring4000, rle(1024, false), rle(1024, false), rle(1024, false), rle(1024, true)), ErrFrameSizeExceeded},
		{"ring-fcs-short", frame(ring4000, raw(1024, false), raw(1024, false), raw(1024, false), raw(500, true)), ErrFrameSizeMismatch},
	} {
		want, werr := dctxDecode(c.in)
		if c.want == io.EOF {
			if werr != nil {
				t.Fatalf("%s: DecodeAll: %v", c.name, werr)
			}
		} else {
			checkSentinel(t, c.name+" DecodeAll", werr, c.want)
		}
		for _, chunk := range []int{1, 7, 1000, len(c.in)} {
			for _, outSize := range []int{4096, 1 << 20} {
				got, e, last := dstreamRun(t, newDCtx(), c.in, fixedChunks(chunk), outSize, 0)
				what := fmt.Sprintf("%s chunk %d out %d", c.name, chunk, outSize)
				if sc := streamClass(e, last); (sc == nil) != (werr == nil) || sc != nil && errClass(sc) != errClass(werr) {
					t.Errorf("%s: %v (err %d), DecodeAll %v", what, sc, e, werr)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s: %d bytes, DecodeAll %d", what, len(got), len(want))
				}
			}
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

// crefHint returns the hint of C's last call that made progress when it
// decodes in (which must end inside a frame) with cref dstream: the
// value of its INCOMPLETE line.
func crefHint(t *testing.T, in []byte, chunk int) int {
	t.Helper()
	_, err := creftest.RunErr(t, in, "dstream", "-chunk-size", strconv.Itoa(chunk), "-out-size", "4096")
	if err == nil {
		t.Fatalf("cref accepted a cut frame")
	}
	_, after, ok := strings.Cut(err.Error(), "INCOMPLETE ")
	n, cerr := strconv.Atoi(strings.TrimSpace(after))
	if !ok || cerr != nil {
		t.Fatalf("cref: %v", err)
	}
	return n
}

// TestDecompressStreamHintCref compares the hint decompressStream returns
// with C's (cref dstream's INCOMPLETE value) for frames cut inside: the
// header, a block header, a block being loaded (the part loaded is
// subtracted), a raw block (streamed) and the checksum; after a block the
// next block's header is preloaded. Text (compressed blocks) and random
// (raw blocks) input, 1 KiB window, checksum; 7-byte chunks and the whole
// cut input in one call.
func TestDecompressStreamHintCref(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	random := make([]byte, 3000)
	for i := range random {
		random[i] = byte(r.Uint32())
	}
	for _, in := range [][]byte{malText(3000), random} {
		frame := creftest.Run(t, in, "compress", "-level", "3", "-window-log", "10", "-checksum", "1")
		for n := 1; n < len(frame); n++ {
			if n > 40 && n%13 != 0 && n < len(frame)-8 {
				continue
			}
			for _, chunk := range []int{7, n} {
				_, e, last := dstreamRun(t, newDCtx(), frame[:n], fixedChunks(chunk), 4096, 0)
				if want := crefHint(t, frame[:n], chunk); e != 0 || last != want {
					t.Fatalf("cut %d/%d chunk %d: err %d, hint %d, C %d", n, len(frame), chunk, e, last, want)
				}
			}
		}
	}
}

// TestDecompressStreamWrapEdgeCref pins the wrap condition (C:
// outStart + blockSizeMax > outBuffSize) at equality: with a 1 KiB window
// (round buffer 3136 bytes, FCS unknown), raw blocks fill it to 2112, and
// 2112 + 1024 = 3136 does not wrap, so the next block's literals go at
// 2112 and a match at offset 2162 copies the raw bytes at 50 (beyond the
// window: C's history check allows it). Had the buffer wrapped, the
// literals would have overwritten them.
func TestDecompressStreamWrapEdgeCref(t *testing.T) {
	raw := func(n int, seed byte) malBlock {
		b := make([]byte, n)
		for i := range b {
			b[i] = seed + byte(i*7)
		}
		return malBlock{typ: btRaw, size: n, body: b}
	}
	lits := bytes.Repeat([]byte{'L'}, 100)
	frame := malFrame{hdr: malFHdr{winDesc: 0}.bytes(), blocks: []malBlock{
		raw(1024, 1), raw(1024, 2), raw(64, 3), // outStart 2112: no wrap
		malCBlock(craftBlock(t, lits, []malSeq{{ll: 100, ml: 8, off: 2112 + 100 - 50}}), true),
	}}.bytes()
	for _, chunk := range []int{7, len(frame)} {
		if k, _ := streamCref(t, frame, nil, nil, chunk, 4096); k != "accept" {
			t.Errorf("chunk %d: %s", chunk, k)
		}
	}
}

// TestDecompressStreamShortcutNoAlloc: the single-pass shortcut decodes
// in place in out.dst, and DecodeAll into a dst with the capacity, without
// allocating, also for a frame compressing 8 MiB to under 1 KiB (whose
// content size the reservation rule does not trust).
func TestDecompressStreamShortcutNoAlloc(t *testing.T) {
	in := bytes.Repeat([]byte("abcdefgh"), 1<<20)
	frame := creftest.Run(t, in, "compress", "-level", "3", "-pledged", "1")
	d := newDCtx()
	out := make([]byte, len(in))
	stream := func() {
		o, i := outBuffer{dst: out}, inBuffer{src: frame}
		if hint, e := d.decompressStreamCode(&o, &i); e != 0 || hint != 0 || o.pos != len(in) {
			t.Fatalf("stream: err %d, hint %d, %d bytes", e, hint, o.pos)
		}
	}
	all := func() {
		if got, e := d.decompressFrames(out[:0], frame, nil); e != 0 || len(got) != len(in) {
			t.Fatalf("DecodeAll: err %d, %d bytes", e, len(got))
		}
	}
	for _, c := range []struct {
		name string
		f    func()
	}{{"shortcut", stream}, {"DecodeAll", all}} {
		var m0, m1 runtime.MemStats
		c.f()
		runtime.ReadMemStats(&m0)
		c.f()
		runtime.ReadMemStats(&m1)
		if n := testing.AllocsPerRun(3, c.f); n != 0 || m1.TotalAlloc != m0.TotalAlloc {
			t.Errorf("%s: %v allocations, %d bytes", c.name, n, m1.TotalAlloc-m0.TotalAlloc)
		}
	}
	if !bytes.Equal(out, in) {
		t.Fatal("output differs")
	}
}

// TestDecompressStreamOversized pins C's buffer shrinking: after a frame
// with a 1 MiB window, frames with a 1 KiB window keep its buffer sizes
// until the 128th (ZSTD_WORKSPACETOOLARGE_MAXDURATION) in a row for which
// they are 3 times too large or more, which reallocates them at the small
// sizes; also when they are exactly 3 times the small frames' sizes.
func TestDecompressStreamOversized(t *testing.T) {
	rle := func(n int, last bool) malBlock { return malBlock{typ: btRLE, last: last, size: n, body: []byte{'r'}} }
	single := func(fcs uint64, blocks ...malBlock) []byte {
		return malFrame{hdr: malFHdr{single: true, fcsSize: 4, fcs: fcs}.bytes(), blocks: blocks}.bytes()
	}
	for _, c := range []struct {
		name       string
		big, small []byte
	}{
		{"window-1MiB-vs-1KiB",
			malFrame{hdr: malFHdr{winDesc: malWindow1M}.bytes(), blocks: []malBlock{rle(1, true)}}.bytes(),
			malFrame{hdr: malFHdr{winDesc: 0}.bytes(), blocks: []malBlock{rle(1, true)}}.bytes()},
		// exactly 3 times: (131072 + 168928) = 3 × (50000 + 50000)
		{"exactly-3x", single(168928, rle(131072, false), rle(37856, true)), single(50000, rle(50000, true))},
	} {
		d := newDCtx()
		buf := make([]byte, 16) // smaller than the content: no single-pass shortcut
		decode := func(f []byte) {
			t.Helper()
			in := inBuffer{src: f}
			for {
				o := outBuffer{dst: buf}
				hint, e := d.decompressStreamCode(&o, &in)
				if e != 0 {
					t.Fatalf("%s: err %d", c.name, e)
				}
				if hint == 0 {
					return
				}
			}
		}
		decode(c.big)
		bigSize := d.outBuffSize
		for i := 1; i <= workspaceTooLargeMaxDuration; i++ {
			decode(c.small)
			if shrunk := d.outBuffSize < bigSize; shrunk != (i == workspaceTooLargeMaxDuration) {
				t.Fatalf("%s: small frame %d: outBuffSize %d (after the big frame %d)", c.name, i, d.outBuffSize, bigSize)
			}
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
// dictionary dd (cargs loads it into cref). It returns the outcome's name
// and the Go error (streamClass). Both accept or both end incomplete with
// the same output; or both fail with the same C error code, or Go fails
// where C does not or with another code as one of the deliberate
// deviations from streaming C says, named by Go's error:
//
//   - window-limit, block-over-max: C's single-pass shortcut runs one-shot
//     C, which checks neither; Go's runs DecodeAll's frame decoding.
//   - block-room: a compressed block regenerating more than the room Go
//     makes for it, less than C's whole buffer: Go dErrDecodedBlockTooLarge
//     (corruption_detected), C dstSize_tooSmall when it also runs out of
//     its buffer.
//   - fcs: Go checks the frame content size at every block and at an empty
//     last block (ErrFrameSizeExceeded, ErrFrameSizeMismatch); C only after
//     a non-empty last block, or one-shot C against its own output space.
//   - output-space: the shortcut's output space, DecodeAll's (Go) or the
//     output buffer (C), ran out at different points.
//   - zero-block: Go rejects a compressed block of size 0, which C skips.
//   - skip-overflow: Go rejects a skippable size that overflows with its
//     header, for which C waits.
//
// When an error ends either, the outputs need only be one a prefix of the
// other: Go reports the output written before an error (C drops what the
// failing call wrote) and rejects a block before writing any of it.
func streamCref(t *testing.T, data []byte, dd *ddict, cargs []string, chunk, outSize int) (string, error) {
	t.Helper()
	d := newDCtx()
	d.ddict = dd
	got, e, last := dstreamRun(t, d, data, fixedChunks(chunk), outSize, 0)
	args := append(slices.Clone(cargs), "-max-window-log", "29", "-chunk-size", strconv.Itoa(chunk), "-out-size", strconv.Itoa(outSize))
	want, cerr := creftest.DStreamCRef(t, data, args...)
	cOK := cerr == nil || cerr.Code == -1 // accepted or incomplete
	outcome := ""
	switch {
	case cerr == nil && e == 0 && last == 0:
		outcome = "accept"
	case cerr != nil && cerr.Code == -1 && e == 0 && last != 0:
		outcome = "incomplete"
	case cerr != nil && e != 0 && e.cCode() == cerr.Code:
		outcome = "reject"
	case e == 0:
		// Go accepts or waits where C fails: no deviation allows it
	case cOK && e == dErrFrameParameterWindowTooLarge:
		outcome = "window-limit"
	case cOK && e == dErrDecodedBlockTooLarge:
		outcome = "block-over-max"
	case cerr != nil && cerr.Code == int(dErrDstSizeTooSmall) && e == dErrDecodedBlockTooLarge:
		outcome = "block-room"
	case e == dErrFrameSizeExceeded || e == dErrFrameSizeMismatch:
		outcome = "fcs"
	case e.cCode() == int(dErrDstSizeTooSmall) || cerr != nil && cerr.Code == int(dErrDstSizeTooSmall):
		outcome = "output-space"
	case cOK && e == dErrBlockTooSmall:
		outcome = "zero-block"
	case cOK && e == dErrFrameParameterUnsupported:
		outcome = "skip-overflow"
	}
	if outcome == "" {
		t.Errorf("C %v, Go err %d hint %d", cerr, e, last)
		return "", nil
	}
	if outcome == "accept" || outcome == "incomplete" {
		if !bytes.Equal(got, want) {
			t.Errorf("%s: output differs (%d bytes, C %d)", outcome, len(got), len(want))
		}
	} else if !bytes.HasPrefix(want, got) && !bytes.HasPrefix(got, want) {
		t.Errorf("%s: outputs diverge (%d bytes, C %d)", outcome, len(got), len(want))
	}
	return outcome, streamClass(e, last)
}

// TestDecompressStreamErrorsCref compares decompressStream with cref
// dstream (see streamCref, which names the deliberate deviations) on the
// malicious corpus (7-byte input chunks, 4096-byte output; the error must
// also be the sentinel MANIFEST.txt records for DecodeAll, incomplete
// input io.ErrUnexpectedEOF) and on 2,000 seeded mutations of the decoder
// seeds plus splitSeed (whose literals take the split placement in
// streaming), both with 7-byte chunks and with the whole input in one
// chunk and a 1 MiB output buffer (the shortcut).
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
			if k == "" {
				t.Errorf("%s: no outcome", c.name)
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
// less than 64 KiB, with 1-byte and whole-input chunks. So does an RLE
// block claiming a 2 MiB run, which fails (over blockSizeMax) without
// being written.
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
		want dErr
	}{
		{"compressed", cat(win, malBlockHeader(btCompressed, 100000, true), body), 0},
		{"raw", cat(win, malBlockHeader(btRaw, 100000, true), body), 0},
		{"fcs-2^60", cat(winFCS, malBlockHeader(btCompressed, 100000, true), body), 0},
		{"skippable-4GiB", cat([]byte{0x50, 0x2A, 0x4D, 0x18, 0xF7, 0xFF, 0xFF, 0xFF}, body), 0},
		{"rle-2MiB", cat(win, malBlockHeader(btRLE, 1<<21-1, true), []byte{'r'}), dErrDecodedBlockTooLarge},
	} {
		for _, chunk := range []int{1, len(c.in)} {
			d := newDCtx()
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			out, e, last := dstreamRun(t, d, c.in, fixedChunks(chunk), 4096, 0)
			runtime.ReadMemStats(&m1)
			if c.want != 0 && e != c.want || c.want == 0 && (e != 0 || last == 0) {
				t.Errorf("%s/%d: err %d, hint %d: want err %d (0: incomplete)", c.name, chunk, e, last, c.want)
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

// FuzzDecompressStream decodes arbitrary input through decompressStream
// with chunks and output buffers chosen by chunkSeed (no panic), and
// requires DecodeAll's result (decompressFrames, max window and memory
// 64 MiB; inputs over the memory limit are skipped): the same output and
// error class, whatever the chunking. Three exceptions, which a stream
// cannot avoid:
//
//   - Input cut short: one-shot decoding fails first where a block runs past
//     the input's end (io.ErrUnexpectedEOF); the stream fails the same way
//     or on what it checked before the cut (a block header), and has
//     delivered the part of a cut raw block that arrived.
//   - A block that regenerates too much: DecodeAll stops it at the end of
//     its output space (the content size, or the frame's block count ×
//     blockSizeMax, which a stream does not know) with ErrFrameSizeExceeded
//     or ErrUnexpectedBlockSize; the stream stops it at the end of its room
//     (2 × blockSizeMax and more, or the content size). Either may name the
//     other of those two errors, or, having decoded further than the
//     other, fail the block on another check. The output (the blocks
//     before it) is the same.
//   - A match offset beyond the frame's window (invalid): the stream keeps
//     a round buffer of the window plus 2 blocks, so how much history
//     there is beyond the window depends on where the buffer wrapped
//     (which raw blocks cut by the input can move); DecodeAll keeps all
//     output and only fails offsets beyond it. The stream may fail such an
//     offset (ErrWindowSizeTooSmall) where DecodeAll goes on.
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
		var oc error
		if oe != 0 {
			oc = oe.toError("decompress")
		}
		if oc == io.ErrUnexpectedEOF {
			if sc == nil || !bytes.HasPrefix(got, want) {
				t.Fatalf("one-shot %v, stream %v (err %d), %d bytes (one-shot %d)", oc, sc, e, len(got), len(want))
			}
			return
		}
		if e == dErrOffsetBeyondHistory && !d.fParams.singleSegment {
			if !bytes.HasPrefix(want, got) {
				t.Fatalf("offset beyond history: stream output is not a prefix of one-shot's")
			}
			return
		}
		sameClass := oc == nil && sc == nil || oc != nil && sc != nil && errClass(oc) == errClass(sc)
		blockOrFrame := func(err error) bool {
			return errors.Is(err, ErrFrameSizeExceeded) || errors.Is(err, ErrUnexpectedBlockSize)
		}
		if !sameClass && !(oc != nil && sc != nil && (blockOrFrame(oc) || blockOrFrame(sc))) {
			t.Fatalf("one-shot %v (err %d), stream %v (err %d)", oc, oe, sc, e)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("outputs differ: stream %d bytes, one-shot %d (%v)", len(got), len(want), oc)
		}
	})
}
