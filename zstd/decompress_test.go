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
	"strings"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// dctxDecode decodes src with a fresh dctx at default parameters (max
// window 1<<29, as cref's -max-window-log 29).
func dctxDecode(src []byte) ([]byte, error) {
	return newDCtx().decompressMultiFrame(nil, src, nil)
}

// sentinelByName maps MANIFEST.txt expectations to the errors they name.
var sentinelByName = map[string]error{
	"ErrBlockTooSmall": ErrBlockTooSmall, "ErrCRCMismatch": ErrCRCMismatch,
	"ErrCompressedSizeTooBig": ErrCompressedSizeTooBig, "ErrDecoderSizeExceeded": ErrDecoderSizeExceeded,
	"ErrFrameSizeExceeded": ErrFrameSizeExceeded, "ErrFrameSizeMismatch": ErrFrameSizeMismatch,
	"ErrMagicMismatch": ErrMagicMismatch, "ErrReservedBlockType": ErrReservedBlockType,
	"ErrUnexpectedBlockSize": ErrUnexpectedBlockSize, "ErrUnknownDictionary": ErrUnknownDictionary,
	"ErrWindowSizeExceeded": ErrWindowSizeExceeded, "ErrWindowSizeTooSmall": ErrWindowSizeTooSmall,
	"unexpected-eof": io.ErrUnexpectedEOF,
}

// checkSentinel fails t unless err matches want (a sentinel, or nil for
// "corrupt": a *corruptError) and no other decoder sentinel.
func checkSentinel(t *testing.T, what string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want %v", what, want)
		return
	}
	for _, s := range decoderSentinels {
		if errors.Is(err, s) != (s == want) {
			t.Errorf("%s: %v: errors.Is(err, %q) = %v", what, err, s, errors.Is(err, s))
		}
	}
	var ce *corruptError
	if want == nil && !errors.As(err, &ce) {
		t.Errorf("%s: %T %v, want *corruptError", what, err, err)
	}
}

// TestDecompressCref decodes every decode corpus entry without a
// dictionary (the dictionary task covers those) and compares with Want, or
// with C's error.
func TestDecompressCref(t *testing.T) {
	skipped := 0
	for _, name := range creftest.FrameNames(t) {
		if strings.HasPrefix(name, "dict/") || strings.HasPrefix(name, "dict-err/mismatch/") {
			skipped++
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := creftest.LoadFrame(t, name)
			if f.Dict != nil {
				t.Fatal("dictionary entry not skipped")
			}
			got, err := dctxDecode(f.Data)
			if f.CErr != "" {
				if f.CErr != "Dictionary mismatch" {
					t.Fatalf("unexpected C error %q in the corpus", f.CErr)
				}
				checkSentinel(t, name, err, ErrUnknownDictionary)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, f.Want) {
				t.Fatalf("output differs (%d bytes, want %d)", len(got), len(f.Want))
			}
		})
	}
	t.Logf("%d dictionary entries left to the dictionary task", skipped)
}

// TestDecompressOurFrames decodes the encoder's output: every level with
// default options, and level 1 with each option set of
// TestEncodeAllOptionsCref plus single segment, padding and zero frames,
// over every input; also EncodeAll appending to earlier frames.
func TestDecompressOurFrames(t *testing.T) {
	names, inputs := sortedInputs(t)
	type cfg struct {
		name string
		opts []EOption
	}
	var cfgs []cfg
	for _, lv := range encoderLevels {
		cfgs = append(cfgs, cfg{fmt.Sprintf("L%d", lv.c), []EOption{WithEncoderLevel(lv.l), WithZeroFrames(true)}})
	}
	for _, c := range []cfg{
		{"crc-off", []EOption{WithEncoderCRC(false)}},
		{"window-1KiB", []EOption{WithWindowSize(MinWindowSize)}},
		{"window-128KiB", []EOption{WithWindowSize(128 << 10)}},
		{"window-max", []EOption{WithWindowSize(MaxWindowSize)}},
		{"no-entropy", []EOption{WithNoEntropyCompression(true)}},
		{"all-lit-entropy", []EOption{WithAllLitEntropyCompression(true)}},
		{"all-lit-entropy-off", []EOption{WithAllLitEntropyCompression(false)}},
		{"no-entropy-wins", []EOption{WithAllLitEntropyCompression(true), WithNoEntropyCompression(true)}},
		{"neutral", []EOption{WithEncoderConcurrency(1), WithLowerEncoderMem(true), WithSingleSegment(false)}},
		{"single-segment", []EOption{WithSingleSegment(true)}},
		{"padding-7", []EOption{WithEncoderPadding(7)}},
		{"padding-1000", []EOption{WithEncoderPadding(1000)}},
		{"padding-128KiB", []EOption{WithEncoderPadding(128 << 10)}},
		{"zero-frames", []EOption{WithZeroFrames(true), WithEncoderCRC(false)}},
	} {
		c.opts = append([]EOption{WithEncoderLevel(SpeedFastest)}, c.opts...)
		cfgs = append(cfgs, c)
	}
	for _, c := range cfgs {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newTestEncoder(t, c.opts...)
			d := newDCtx()
			var all, allWant []byte
			for _, name := range names {
				in := inputs[name]
				frame := e.EncodeAll(in, nil)
				got, err := d.decompressMultiFrame(nil, frame, nil)
				if err != nil || !bytes.Equal(got, in) {
					t.Errorf("%s: err %v, output equal %v", name, err, bytes.Equal(got, in))
				}
				if len(in) < 1<<20 {
					all = e.EncodeAll(in, all)
					allWant = append(allWant, in...)
				}
			}
			// Several frames (and padding) in one input, decoded after a
			// prefix that must stay untouched.
			prefix := []byte("prefix")
			got, err := d.decompressMultiFrame(slices.Clone(prefix), all, nil)
			if err != nil || !bytes.Equal(got, append(prefix, allWant...)) {
				t.Errorf("concatenated: err %v, output equal %v", err, bytes.Equal(got, append(prefix, allWant...)))
			}
		})
	}
}

// TestMaliciousCorpus decodes every malicious corpus case and checks the
// error MANIFEST.txt records for it. Cases that need a dictionary are the
// dictionary task's.
func TestMaliciousCorpus(t *testing.T) {
	lines, _ := readManifest(t)
	byFile := map[string]malCase{}
	for _, c := range maliciousCases(t) {
		byFile[c.file()] = c
	}
	for _, l := range lines {
		file, rest, _ := strings.Cut(l, " ")
		expect, _, _ := strings.Cut(rest, " ")
		c, ok := byFile[file]
		if !ok {
			t.Fatalf("%s: not built by maliciousCases", file)
		}
		t.Run(c.name, func(t *testing.T) {
			if c.dict != "" {
				t.Skip("needs a dictionary (dictionary task)")
			}
			data := c.data
			if !c.gen() {
				var err error
				if data, err = os.ReadFile(filepath.Join(malDir, file)); err != nil {
					t.Fatal(err)
				}
			}
			want, ok := sentinelByName[expect]
			if !ok && expect != "corrupt" {
				t.Fatalf("unknown expectation %q", expect)
			}
			_, err := dctxDecode(data)
			checkSentinel(t, c.name, err, want)
			if c.control != nil {
				if _, err := dctxDecode(c.control); err != nil {
					t.Errorf("control variant: %v", err)
				}
			}
		})
	}
}

// TestDecompressErrorsIs pins the error mapping rows of the frame decoder
// not covered by a malicious corpus case (each sentinel alone matches).
func TestDecompressErrorsIs(t *testing.T) {
	text := malText(600)
	frame := malEncode(t, text, true)
	for _, c := range []struct {
		name      string
		in        []byte
		maxMemory uint64
		want      error
	}{
		{"memory limit, FCS known", frame, 599, ErrDecoderSizeExceeded},
		{"memory limit, FCS unknown", malEncodeNoFCS(t, text), 599, ErrDecoderSizeExceeded},
		{"memory limit, second frame", append(slices.Clone(frame), frame...), 1000, ErrDecoderSizeExceeded},
		{"garbage after a frame", append(slices.Clone(frame), "garbage!"...), 0, ErrMagicMismatch},
		{"1 garbage byte after a frame", append(slices.Clone(frame), 0x27), 0, ErrMagicMismatch},
		{"magic prefix after a frame", append(slices.Clone(frame), 0x28, 0xB5), 0, io.ErrUnexpectedEOF},
		{"skippable prefix after a frame", append(slices.Clone(frame), 0x5F, 0x2A, 0x4D, 0x18), 0, io.ErrUnexpectedEOF},
		{"checksum cut", frame[:len(frame)-2], 0, io.ErrUnexpectedEOF},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newDCtx()
			if c.maxMemory != 0 {
				d.maxMemory = c.maxMemory
			}
			got, err := d.decompressMultiFrame(nil, c.in, nil)
			checkSentinel(t, c.name, err, c.want)
			if !bytes.HasPrefix(text, got) && !bytes.HasPrefix(append(slices.Clone(text), text...), got) {
				t.Errorf("output on error is not a prefix of the content")
			}
		})
	}
	// A memory limit that the output fits exactly.
	d := newDCtx()
	d.maxMemory = uint64(len(text))
	if got, err := d.decompressMultiFrame(nil, malEncodeNoFCS(t, text), nil); err != nil || !bytes.Equal(got, text) {
		t.Errorf("limit = output size: %v", err)
	}
}

// TestDecompressBlockOverMaxCRef pins the block-over-max deviation: a raw
// block of 1500 bytes in a frame with a 1 KiB window (blockSizeMax 1024)
// and FCS 1500, followed by an empty last block. C one-shot decodes it
// (its output space, FCS, has room); streaming C rejects it after the
// block (corruption_detected), and so do we (ErrUnexpectedBlockSize).
func TestDecompressBlockOverMaxCRef(t *testing.T) {
	content := malText(1500)
	frame := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x40, 0x00} // FCS field 2 bytes, window 1 KiB
	frame = binary.LittleEndian.AppendUint16(frame, 1500-256)
	frame = append(frame, malBlockHeader(btRaw, 1500, false)...)
	frame = append(frame, content...)
	frame = append(frame, malBlockHeader(btRaw, 0, true)...)
	if got, cerr := creftest.DecodeCRef(t, frame); cerr != nil || !bytes.Equal(got, content) {
		t.Fatalf("C one-shot: %v", cerr)
	}
	if _, serr := creftest.DStreamCRef(t, frame, "-max-window-log", "29", "-chunk-size", "7", "-out-size", "4096"); serr == nil || serr.Code != int(dErrCorruptionDetected) {
		t.Fatalf("C streaming: %v, want corruption_detected", serr)
	}
	got, e := newDCtx().decompressFrames(nil, frame, nil)
	if e != dErrDecodedBlockTooLarge || len(got) != 0 {
		t.Fatalf("Go: (%d bytes, %d), want dErrDecodedBlockTooLarge", len(got), e)
	}
	_, err := dctxDecode(frame)
	checkSentinel(t, "block over max", err, ErrUnexpectedBlockSize)
}

// malEncodeNoFCS returns a frame of src without a frame content size.
func malEncodeNoFCS(t testing.TB, src []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	e, err := NewWriter(&buf, WithEncoderLevel(SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if fhd := buf.Bytes()[4]; fhd>>6 != 0 || fhd&0x20 != 0 {
		t.Fatalf("frame has a content size (FHD %#x)", fhd)
	}
	return buf.Bytes()
}

// TestDecompressParams checks setIgnoreChecksum, setMaxWindow and reset.
func TestDecompressParams(t *testing.T) {
	text := malText(600)
	frame := malEncode(t, text, true)
	bad := slices.Clone(frame)
	bad[len(bad)-1] ^= 1
	d := newDCtx()
	if _, err := d.decompressMultiFrame(nil, bad, nil); err != ErrCRCMismatch {
		t.Fatalf("checksum: %v", err)
	}
	d.setIgnoreChecksum(true)
	if got, err := d.decompressMultiFrame(nil, bad, nil); err != nil || !bytes.Equal(got, text) {
		t.Fatalf("ignored checksum: %v", err)
	}
	// Ignoring the checksum still needs its 4 bytes (C too).
	if _, err := d.decompressMultiFrame(nil, bad[:len(bad)-1], nil); err != io.ErrUnexpectedEOF {
		t.Fatalf("ignored, cut checksum: %v", err)
	}
	// The frame's window is 1 KiB (single segment, 600 bytes): the 1 KiB
	// minimum applies, as in C.
	d.setMaxWindow(1<<10 - 1)
	if _, err := d.decompressMultiFrame(nil, frame, nil); err != ErrWindowSizeExceeded {
		t.Fatalf("max window 1023: %v", err)
	}
	d.setMaxWindow(1 << 10)
	if _, err := d.decompressMultiFrame(nil, frame, nil); err != nil {
		t.Fatalf("max window 1024: %v", err)
	}
	d.setMaxWindow(1 << 10)
	d.reset(true)
	if d.maxWindowSize != 1<<10 || !d.forceIgnoreChecksum {
		t.Fatal("reset(sessionOnly) reset the parameters")
	}
	d.reset(false)
	if d.maxWindowSize != MaxWindowSize || d.forceIgnoreChecksum || d.maxMemory != 64<<30 {
		t.Fatal("reset(false) kept parameters")
	}
}

// TestReadSkippableFrame checks readSkippableFrame (C ZSTD_readSkippableFrame)
// and readSkippableFrameSize on hand-made frames.
func TestReadSkippableFrame(t *testing.T) {
	skip := func(variant, size uint32, payload []byte) []byte {
		b := binary.LittleEndian.AppendUint32(nil, magicSkippableStart+variant)
		b = binary.LittleEndian.AppendUint32(b, size)
		return append(b, payload...)
	}
	payload := []byte("payload!")
	for _, c := range []struct {
		name    string
		src     []byte
		dstSize int
		n       int
		variant uint32
		err     dErr
	}{
		{"ok", skip(5, 8, payload), 8, 8, 5, 0},
		{"ok, larger dst, trailing input", append(skip(15, 8, payload), 1, 2, 3), 100, 8, 15, 0},
		{"empty payload", skip(0, 0, nil), 0, 0, 0, 0},
		{"dst too small", skip(5, 8, payload), 7, 0, 0, dErrDstSizeTooSmall},
		{"not skippable", append([]byte{0x28, 0xB5, 0x2F, 0xFD, 8, 0, 0, 0}, payload...), 8, 0, 0, dErrFrameParameterUnsupported},
		{"size+8 overflows", skip(1, 0xFFFFFFF8, payload), 8, 0, 0, dErrSrcSizeWrong},
		{"size 0xFFFFFFFF", skip(1, 0xFFFFFFFF, payload), 8, 0, 0, dErrSrcSizeWrong},
		{"short frame", skip(1, 9, payload), 9, 0, 0, dErrSrcSizeWrong},
		{"short header", skip(1, 0, nil)[:7], 9, 0, 0, dErrSrcSizeWrong},
	} {
		dst := make([]byte, c.dstSize)
		n, variant, err := readSkippableFrame(dst, c.src)
		if n != c.n || variant != c.variant || err != c.err {
			t.Errorf("%s: (%d, %d, %d), want (%d, %d, %d)", c.name, n, variant, err, c.n, c.variant, c.err)
		}
		if err == 0 && !bytes.Equal(dst[:n], c.src[8:8+n]) {
			t.Errorf("%s: payload differs", c.name)
		}
	}
	// readSkippableFrameSize: C rejects size+8 overflowing 32 bits as
	// unsupported (readSkippableFrame reads that as srcSize_wrong).
	for _, c := range []struct {
		size uint32
		n    int
		err  dErr
	}{{0xFFFFFFF7, 0, dErrSrcSizeWrong}, {0xFFFFFFF8, 0, dErrFrameParameterUnsupported}, {8, 16, 0}, {9, 0, dErrSrcSizeWrong}} {
		if n, err := readSkippableFrameSize(skip(0, c.size, payload)); n != c.n || err != c.err {
			t.Errorf("readSkippableFrameSize(size %#x) = (%d, %d), want (%d, %d)", c.size, n, err, c.n, c.err)
		}
	}
}

// TestDecompressHugeHeader: frames declaring a window near 4 GiB (window
// log 31, mantissa 7) or a frame content size of 2^62 over a tiny body
// fail cleanly, on 386 too, with default parameters (window limit) and
// with every limit lifted (the content size is then capped by the blocks
// present), without allocating from the header.
func TestDecompressHugeHeader(t *testing.T) {
	body := []byte{1, 0, 0} // last raw block of 0 bytes
	hdr := func(fhd byte, fields ...byte) []byte {
		return append(append([]byte{0x28, 0xB5, 0x2F, 0xFD, fhd}, fields...), body...)
	}
	fcs62 := binary.LittleEndian.AppendUint64(nil, 1<<62)
	for _, c := range []struct {
		name         string
		in           []byte
		dflt, lifted error
	}{
		// window descriptor exponent 21 (log 31), mantissa 7
		{"window ~4GiB", hdr(0x00, 21<<3|7), ErrWindowSizeExceeded, nil},
		{"window ~4GiB, FCS 2^62", hdr(0xC0, append([]byte{21<<3 | 7}, fcs62...)...), ErrWindowSizeExceeded, ErrFrameSizeMismatch},
		{"single segment, FCS 2^62", hdr(0xE0, fcs62...), ErrWindowSizeExceeded, ErrFrameSizeMismatch},
		{"FCS 2^62, window 1 MiB", hdr(0xC0, append([]byte{10 << 3}, fcs62...)...), ErrFrameSizeMismatch, ErrFrameSizeMismatch},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, lifted := range []bool{false, true} {
				d := newDCtx()
				want := c.dflt
				if lifted {
					d.setMaxWindow(^uint64(0))
					d.maxMemory = ^uint64(0)
					want = c.lifted
				}
				var ms0, ms1 runtime.MemStats
				runtime.ReadMemStats(&ms0)
				got, err := d.decompressMultiFrame(nil, c.in, nil)
				runtime.ReadMemStats(&ms1)
				if err != want || len(got) != 0 {
					t.Errorf("lifted %v: (%d bytes, %v), want %v", lifted, len(got), err, want)
				}
				if a := ms1.TotalAlloc - ms0.TotalAlloc; a > 256<<10 { // at most one 128 KiB block
					t.Errorf("lifted %v: allocated %d bytes", lifted, a)
				}
			}
		})
	}
}

// truncCheck decodes every prefix of data (the parts concatenated) and
// checks that the prefixes ending at a part boundary decode to the
// concatenated wants, and every other one fails with io.ErrUnexpectedEOF.
// With testing.Short(), inputs over 4 KiB are cut at every length only
// within 2 KiB of either end and of a part boundary, and at every 61st
// length elsewhere (each cut decodes everything before it, so every
// length costs time quadratic in the input).
func truncCheck(t *testing.T, parts, wants [][]byte) {
	t.Helper()
	data := bytes.Join(parts, nil)
	complete := map[int][]byte{0: {}}
	pos, want := 0, []byte{}
	for i, p := range parts {
		pos += len(p)
		want = append(want, wants[i]...)
		complete[pos] = slices.Clone(want)
	}
	near := func(n int) bool {
		if !testing.Short() || len(data) <= 4<<10 || n%61 == 0 || n < 2<<10 || n > len(data)-2<<10 {
			return true
		}
		for b := range complete {
			if n > b-2<<10 && n < b+2<<10 {
				return true
			}
		}
		return false
	}
	d := newDCtx()
	buf := make([]byte, 0, len(want))
	for n := 1; n < len(data); n++ {
		if !near(n) {
			continue
		}
		got, err := d.decompressMultiFrame(buf[:0], data[:n], nil)
		if w, ok := complete[n]; ok {
			if err != nil || !bytes.Equal(got, w) {
				t.Fatalf("cut at part boundary %d: %v", n, err)
			}
		} else if err != io.ErrUnexpectedEOF {
			t.Fatalf("cut at %d/%d: %v, want io.ErrUnexpectedEOF", n, len(data), err)
		}
	}
}

// TestTruncationsCorpus cuts every decode corpus entry of at most 64 KiB
// (no dictionary) at every length (see truncCheck for testing.Short()):
// only cuts at frame boundaries decode
// (to the frames before the cut), every other cut is io.ErrUnexpectedEOF.
// C is run on a sample of the cuts (both ends, every boundary ±1 and 8
// random cuts per entry) and must agree on accept/reject; C's behaviour at
// every length is pinned for each frame type by TestTruncationsCRef.
func TestTruncationsCorpus(t *testing.T) {
	n := 0
	for _, name := range creftest.FrameNames(t) {
		if strings.HasPrefix(name, "dict") {
			continue
		}
		f := creftest.LoadFrame(t, name)
		if len(f.Data) > 64<<10 || f.CErr != "" {
			continue
		}
		n++
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Split into frames: the boundaries are where C's
			// ZSTD_findFrameCompressedSize says, checked by cref fheader in
			// TestFrameHeaderCref.
			var parts, wants [][]byte
			rest := f.Data
			for len(rest) > 0 {
				fsi := findFrameSizeInfo(rest)
				if fsi.err != 0 {
					t.Fatalf("frame walk: %d", fsi.err)
				}
				p := rest[:fsi.compressedSize]
				w, err := dctxDecode(p)
				if err != nil {
					t.Fatal(err)
				}
				parts, wants = append(parts, p), append(wants, w)
				rest = rest[fsi.compressedSize:]
			}
			if !bytes.Equal(bytes.Join(wants, nil), f.Want) {
				t.Fatal("frames do not decode to Want")
			}
			truncCheck(t, parts, wants)
			cuts := map[int]bool{1: true, len(f.Data) - 1: true}
			pos := 0
			for _, p := range parts {
				pos += len(p)
				cuts[pos-1], cuts[pos], cuts[pos+1] = true, true, true
			}
			r := rand.New(rand.NewPCG(uint64(len(f.Data)), 9))
			for range 8 {
				cuts[1+r.IntN(len(f.Data))] = true
			}
			for c := range cuts {
				if c < 1 || c >= len(f.Data) {
					continue
				}
				_, cerr := creftest.DecodeCRef(t, f.Data[:c], f.Args...)
				_, err := dctxDecode(f.Data[:c])
				if (cerr == nil) != (err == nil) {
					t.Errorf("cut at %d: C %v, Go %v", c, cerr, err)
				}
			}
		})
	}
	t.Logf("%d corpus entries", n)
}

// --- differential mutations ---------------------------------------------

// splitSeed returns a frame whose single compressed block holds 90,000
// bytes of 32 random symbols (nearly all literals, Huffman-coded) and FCS
// 90,000: one-shot decoding
// gives it exactly FCS bytes of output space, so its literals take the
// split placement (more than litBufferExtraSize, and no room after the
// block).
func splitSeed(t testing.TB) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(1, 2))
	in := make([]byte, 90000)
	for i := range in {
		in[i] = 'A' + byte(r.IntN(32))
	}
	return newTestEncoder(t, WithEncoderLevel(SpeedFastest)).EncodeAll(in, nil)
}

// mutate returns a mutation of a seed: a single or double bit flip, a byte
// replacement, or a splice of two seeds. Positions favour the first 32
// bytes (frame and block headers) one time in three.
func mutate(r *rand.Rand, seeds [][]byte) []byte {
	pos := func(b []byte) int {
		if r.IntN(3) == 0 {
			return r.IntN(min(32, len(b)))
		}
		return r.IntN(len(b))
	}
	b := slices.Clone(seeds[r.IntN(len(seeds))])
	switch r.IntN(4) {
	case 0:
		p := pos(b)
		b[p] ^= 1 << r.IntN(8)
	case 1:
		p := pos(b)
		b[p] ^= 1 << r.IntN(8)
		p = pos(b)
		b[p] ^= 1 << r.IntN(8)
	case 2:
		b[pos(b)] = byte(r.Uint32())
	case 3:
		o := seeds[r.IntN(len(seeds))]
		b = append(b[:r.IntN(len(b)+1)], o[r.IntN(len(o)+1):]...)
	}
	return b
}

// TestDecodeDifferentialMutations decodes 20,000 seeded mutations of the
// decoder seeds (plus splitSeed) with C one-shot (cref decompress) and
// with decompressMultiFrame. Both accept with the same output, or both
// reject with the same C error code (our dErr's cCode). The deliberate
// deviations of the error mapping table are compared with C streaming
// (cref dstream, 7-byte input chunks) instead, and counted by name:
//
//   - garbage-after-frame: bytes after a frame that start no frame; C
//     one-shot says srcSize_wrong, streaming and we prefix_unknown.
//   - checksum-cut: input ending inside the checksum; C one-shot says
//     checksum_wrong, streaming "incomplete", we srcSize_wrong.
//   - window-limit: a window above 1<<29, which C one-shot does not check.
//   - block-over-max: a block regenerating more than blockSizeMax, which
//     C one-shot accepts when its output space has room.
//   - output-space: a frame regenerating more than its output space; C
//     one-shot's space (cref: the sum of all frames' bounds, or 1 MiB and
//     up when the input has no bound) differs from ours (each frame's own,
//     from the blocks present). A dstSize_tooSmall of ours may be streaming
//     C's corruption_detected for a block over blockSizeMax.
//   - cref-oom: decompressBound above 1 GiB, which cref decompress cannot
//     allocate.
func TestDecodeDifferentialMutations(t *testing.T) {
	creftest.Bin(t)
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
	if len(splitFrame) > maxSeedLen {
		t.Fatalf("split seed is %d bytes", len(splitFrame))
	}
	{
		// The block's literals, decoded into the frame's output space as
		// decompressFrame does, take the split placement (the sequences
		// move them on, so the dctx does not show it after decoding).
		d := newDCtx()
		if _, err := d.decompressMultiFrame(nil, splitFrame, nil); err != nil {
			t.Fatalf("split seed: %v", err)
		}
		fcs := int(d.fParams.frameContentSize)
		d.decompressBegin()
		if e := d.decodeFrameHeader(splitFrame[:d.fParams.headerSize]); e != 0 {
			t.Fatal(e)
		}
		_, e := d.decodeLiteralsBlock(splitFrame[d.fParams.headerSize+blockHeaderSize:], make([]byte, fcs), fcs, notStreaming)
		if e != 0 || d.litBufferLocation != split {
			t.Fatalf("split seed: err %d, literals placed %d", e, d.litBufferLocation)
		}
	}
	seeds = append(seeds, splitFrame)

	const n = 20000
	workers := runtime.GOMAXPROCS(0)
	var mu sync.Mutex
	stats := map[string]int{}
	count := func(k string) {
		mu.Lock()
		stats[k]++
		mu.Unlock()
	}
	for w := range workers {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			t.Parallel()
			for i := w; i < n; i += workers {
				r := rand.New(rand.NewPCG(uint64(i), 0xD1FF))
				data := mutate(r, seeds)
				if k := diffOne(t, data); k != "" {
					count(k)
				}
				if t.Failed() {
					t.Fatalf("mutation %d: %x", i, data)
				}
			}
		})
	}
	t.Cleanup(func() { t.Logf("outcomes: %v", stats) })
}

// diffOne compares one input as TestDecodeDifferentialMutations says and
// returns the outcome's name.
func diffOne(t *testing.T, data []byte) string {
	t.Helper()
	got, e := newDCtx().decompressFrames(nil, data, nil)
	var want []byte
	var cerr *creftest.CError
	deviation := ""
	if b := decompressBound(data); b != contentSizeError && b > 1<<30 {
		deviation = "cref-oom"
	} else {
		want, cerr = creftest.DecodeCRef(t, data)
		switch {
		case cerr == nil && e == 0:
			if !bytes.Equal(got, want) {
				t.Errorf("both accept, outputs differ")
			}
			return "accept"
		case cerr != nil && e != 0 && e.cCode() == cerr.Code:
			return "reject"
		}
		switch {
		case e == dErrPrefixUnknown:
			deviation = "garbage-after-frame"
		case e == dErrSrcSizeWrong && cerr != nil && cerr.Code == int(dErrChecksumWrong):
			deviation = "checksum-cut"
		case e == dErrFrameParameterWindowTooLarge:
			deviation = "window-limit"
		case e == dErrDecodedBlockTooLarge:
			deviation = "block-over-max"
		case e.cCode() == int(dErrDstSizeTooSmall) || e == dErrFrameSizeMismatch ||
			cerr != nil && (cerr.Code == int(dErrDstSizeTooSmall) || cerr.Code == int(dErrCorruptionDetected)):
			deviation = "output-space"
		default:
			t.Errorf("C one-shot %v, Go %d (%v): no deviation explains it", cerr, e, e)
			return ""
		}
	}
	sw, serr := creftest.DStreamCRef(t, data, "-max-window-log", "29", "-chunk-size", "7", "-out-size", "4096")
	switch {
	case serr == nil && e == 0:
		if !bytes.Equal(got, sw) {
			t.Errorf("%s: both accept, outputs differ from C streaming", deviation)
		}
	case serr != nil && e != 0 && (e.cCode() == serr.Code || serr.Code == -1 && e == dErrSrcSizeWrong):
	case deviation == "output-space" && serr != nil && e.cCode() == int(dErrDstSizeTooSmall) && serr.Code == int(dErrCorruptionDetected):
		// Streaming C had room for a block regenerating more than
		// blockSizeMax and rejected it afterwards
		// (dErrDecodedBlockTooLarge); our output space ended inside it.
		// Both are ErrUnexpectedBlockSize's row.
	default:
		t.Errorf("%s: C one-shot %v, C streaming %v, Go %d (%v)", deviation, cerr, serr, e, e)
	}
	return deviation
}

// FuzzDecompressDCtx decodes arbitrary input: no panic, and the output (on
// success or before an error) is no larger than decompressBound when that
// is known. dst's prefix stays untouched, and a reused dctx decodes the
// same.
func FuzzDecompressDCtx(f *testing.F) {
	for _, dir := range []string{seedsDir, malDir} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.HasSuffix(e.Name(), ".zst") {
				f.Add(b)
			}
		}
	}
	d := newDCtx()
	d.maxMemory = 1 << 26
	f.Fuzz(func(t *testing.T, data []byte) {
		prefix := []byte("prefix")
		got, err := d.decompressMultiFrame(slices.Clone(prefix), data, nil)
		if !bytes.HasPrefix(got, prefix) {
			t.Fatal("prefix modified")
		}
		got = got[len(prefix):]
		if b := decompressBound(data); b != contentSizeError && uint64(len(got)) > b {
			t.Fatalf("output %d bytes > decompressBound %d (err %v)", len(got), b, err)
		}
		got2, err2 := d.decompressMultiFrame(nil, data, nil)
		if !bytes.Equal(got, got2) || fmt.Sprint(err) != fmt.Sprint(err2) {
			t.Fatalf("reused dctx: %v vs %v", err, err2)
		}
	})
}
