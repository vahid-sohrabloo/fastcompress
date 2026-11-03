package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
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

func newTestDecoder(t testing.TB, opts ...DOption) *Decoder {
	t.Helper()
	d, err := NewReader(nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// frameDictOpts returns the options that register a corpus entry's
// dictionary: cref gives raw content the ID 0, so its frames carry none.
func frameDictOpts(f creftest.Frame) []DOption {
	switch {
	case f.Dict == nil:
		return nil
	case f.RawDict:
		return []DOption{WithDecoderDictRaw(0, f.Dict)}
	}
	return []DOption{WithDecoderDicts(f.Dict)}
}

// checkCorpusResult fails t unless a decode of corpus entry f gave
// prefix + f.Want, or C's error (the dict-err entries).
func checkCorpusResult(t *testing.T, what string, f creftest.Frame, got []byte, err error, prefix []byte) {
	t.Helper()
	if f.CErr != "" {
		if f.CErr != "Dictionary mismatch" {
			t.Fatalf("unexpected C error %q in the corpus", f.CErr)
		}
		checkSentinel(t, what, err, ErrUnknownDictionary)
		return
	}
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !bytes.HasPrefix(got, prefix) || !bytes.Equal(got[len(prefix):], f.Want) {
		t.Fatalf("%s: output differs (%d bytes, want %d + %d)", what, len(got), len(prefix), len(f.Want))
	}
}

// chunkReader returns its data in reads of the sizes next returns. It has
// no Bytes method, so a Decoder streams it.
type chunkReader struct {
	b    []byte
	next func() int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), max(r.next(), 1))], r.b)
	r.b = r.b[n:]
	return n, nil
}

func wholeReader(b []byte) io.Reader {
	return &chunkReader{b: b, next: func() int { return len(b) }}
}

// readAll resets d to r and reads it with buffers of the sizes pSize
// returns until an error (io.EOF is success, nil). It stops after more
// than limit bytes (0: no limit).
func readAll(t testing.TB, d *Decoder, r io.Reader, pSize func() int, limit int) ([]byte, error) {
	t.Helper()
	if err := d.Reset(r); err != nil {
		t.Fatal(err)
	}
	var out []byte
	var buf []byte
	for {
		p := pSize()
		if cap(buf) < p {
			buf = make([]byte, p)
		}
		n, err := d.Read(buf[:p])
		if n < 0 || n > p {
			t.Fatalf("Read returned %d for %d bytes", n, p)
		}
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if limit > 0 && len(out) > limit {
			return out, nil
		}
	}
}

// writeToAll resets d to r and decodes it with WriteTo.
func writeToAll(t testing.TB, d *Decoder, r io.Reader) ([]byte, error) {
	t.Helper()
	if err := d.Reset(r); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	n, err := d.WriteTo(&b)
	if n != int64(b.Len()) {
		t.Fatalf("WriteTo returned %d, wrote %d", n, b.Len())
	}
	return b.Bytes(), err
}

func fixedSize(n int) func() int { return func() int { return n } }

// TestDecodeAllCref decodes every decode corpus entry with DecodeAll after
// a prefix that must survive, with no spare capacity in dst and with
// exactly enough.
func TestDecodeAllCref(t *testing.T) {
	for _, name := range creftest.FrameNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := creftest.LoadFrame(t, name)
			d := newTestDecoder(t, frameDictOpts(f)...)
			prefix := []byte("prefix")
			got, err := d.DecodeAll(f.Data, slices.Clone(prefix))
			checkCorpusResult(t, "no capacity", f, got, err, prefix)
			dst := append(make([]byte, 0, len(prefix)+len(f.Want)), prefix...)
			got, err = d.DecodeAll(f.Data, dst)
			checkCorpusResult(t, "capacity", f, got, err, prefix)
			// A frame with a content size (pledged) decodes in dst's
			// capacity. (Without one, decompressFrames reserves its own
			// guess when it exceeds the spare capacity.)
			if strings.HasSuffix(name, "/p1") && err == nil && len(f.Want) > 0 && &got[0] != &dst[0] {
				t.Fatal("output did not stay in dst's capacity")
			}
		})
	}
}

// TestDecoderReadCref decodes every corpus entry with Read: whole input,
// 1-byte input and 1-byte reads (small entries), random input chunks and
// read sizes, and from a bytes.Buffer (WithDecodeBuffersBelow's path).
func TestDecoderReadCref(t *testing.T) {
	for _, name := range creftest.FrameNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := creftest.LoadFrame(t, name)
			d := newTestDecoder(t, append(frameDictOpts(f), WithDecoderConcurrency(1))...)
			got, err := readAll(t, d, wholeReader(f.Data), fixedSize(4096), 0)
			checkCorpusResult(t, "whole", f, got, err, nil)
			if len(f.Want) <= 64<<10 {
				got, err = readAll(t, d, &chunkReader{b: f.Data, next: fixedSize(1)}, fixedSize(1), 0)
				checkCorpusResult(t, "1-byte", f, got, err, nil)
			}
			r := rand.New(rand.NewPCG(1, uint64(len(f.Data))))
			got, err = readAll(t, d, &chunkReader{b: f.Data, next: func() int { return 1 + r.IntN(200000) }},
				func() int { return 1 + r.IntN(2*dStreamOutSize) }, 0)
			checkCorpusResult(t, "random", f, got, err, nil)
			got, err = readAll(t, d, bytes.NewBuffer(f.Data), fixedSize(4096), 0)
			checkCorpusResult(t, "buffer", f, got, err, nil)
		})
	}
}

// TestDecoderWriteToCref decodes every corpus entry with WriteTo: whole
// input, 7-byte input chunks and from a bytes.Buffer.
func TestDecoderWriteToCref(t *testing.T) {
	for _, name := range creftest.FrameNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := creftest.LoadFrame(t, name)
			d := newTestDecoder(t, append(frameDictOpts(f), WithDecoderConcurrency(1))...)
			got, err := writeToAll(t, d, wholeReader(f.Data))
			checkCorpusResult(t, "whole", f, got, err, nil)
			got, err = writeToAll(t, d, &chunkReader{b: f.Data, next: fixedSize(7)})
			checkCorpusResult(t, "7-byte", f, got, err, nil)
			got, err = writeToAll(t, d, bytes.NewBuffer(f.Data))
			checkCorpusResult(t, "buffer", f, got, err, nil)
		})
	}
}

// TestDecodeAllConcurrent runs 16 goroutines decoding the corpus (entries
// up to 8 MiB, without raw-content dictionaries) with DecodeAll on one
// Decoder of concurrency 4 that has every zstd-format dictionary, while a
// stream is decoded on it too. Run it with -race.
func TestDecodeAllConcurrent(t *testing.T) {
	var frames []creftest.Frame
	var dicts [][]byte
	for _, name := range creftest.FrameNames(t) {
		f := creftest.LoadFrame(t, name)
		if f.RawDict || f.CErr != "" || len(f.Want) > 8<<20 {
			continue
		}
		if f.Dict != nil && !slices.ContainsFunc(dicts, func(b []byte) bool { return bytes.Equal(b, f.Dict) }) {
			dicts = append(dicts, f.Dict)
		}
		frames = append(frames, f)
	}
	if len(frames) == 0 || len(dicts) < 2 {
		t.Fatalf("%d frames, %d dictionaries", len(frames), len(dicts))
	}
	d := newTestDecoder(t, WithDecoderConcurrency(4), WithDecoderDicts(dicts...))
	if cap(d.dctxs) != 4 {
		t.Fatalf("%d contexts, want 4", cap(d.dctxs))
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range frames {
				f := frames[(i+g*7)%len(frames)]
				got, err := d.DecodeAll(f.Data, nil)
				if err != nil || !bytes.Equal(got, f.Want) {
					t.Errorf("goroutine %d: err %v, output equal %v", g, err, bytes.Equal(got, f.Want))
					return
				}
			}
		})
	}
	wg.Go(func() {
		for _, f := range frames[:min(len(frames), 50)] {
			if err := d.Reset(wholeReader(f.Data)); err != nil {
				t.Error(err)
				return
			}
			var b bytes.Buffer
			if _, err := d.WriteTo(&b); err != nil || !bytes.Equal(b.Bytes(), f.Want) {
				t.Errorf("stream: err %v, output equal %v", err, bytes.Equal(b.Bytes(), f.Want))
				return
			}
		}
	})
	wg.Wait()
}

// emptyBlocksFrame is a frame (window 1 MiB, 8-byte frame content size
// fcs) of nb empty raw blocks, the last one flagged last.
func emptyBlocksFrame(fcs uint64, nb int) []byte {
	b := []byte{0x28, 0xB5, 0x2F, 0xFD, 0xC0, malWindow1M}
	b = binary.LittleEndian.AppendUint64(b, fcs)
	for i := range nb {
		b = append(b, malBlockHeader(btRaw, 0, i == nb-1)...)
	}
	return b
}

// TestDecodeAllAllocBound pins Review Focus 2 for DecodeAll: memory
// follows validated structure, never a header's claim (TotalAlloc delta of
// one DecodeAll on a warmed Decoder). A trusted frame content size (at most
// 64 times the frame's compressed size) is reserved up front; on 32-bit
// platforms the default memory limit (1 GiB, also the cap of an explicit
// WithDecoderMaxMemory) keeps that within the address space.
func TestDecodeAllAllocBound(t *testing.T) {
	body := make([]byte, 50)
	for i := range body {
		body[i] = byte(i*7 + 1)
	}
	fcs60 := []byte{0x28, 0xB5, 0x2F, 0xFD, 0xC0, malWindow1M}
	fcs60 = binary.LittleEndian.AppendUint64(fcs60, 1<<60)
	const nb = 8193 // nb × 128 KiB > 1 GiB
	trusted := emptyBlocksFrame(64*uint64(14+3*nb), nb)
	big := emptyBlocksFrame(1<<30+1, nb)
	is32 := strconv.IntSize == 32
	bigWant, bigBound := ErrFrameSizeMismatch, uint64(1<<20+64<<10)
	if is32 {
		bigWant, bigBound = ErrDecoderSizeExceeded, 64<<10
	}
	for _, c := range []struct {
		name  string
		in    []byte
		opts  []DOption
		want  error
		bound uint64
	}{
		{"fcs-2^60", slices.Concat(fcs60, malBlockHeader(btCompressed, 100000, true), body), nil, io.ErrUnexpectedEOF, 64 << 10},
		{"window-2^41", []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 31 << 3, 0, 0, 0}, nil, ErrWindowSizeExceeded, 64 << 10},
		{"skippable-4GiB", slices.Concat([]byte{0x50, 0x2A, 0x4D, 0x18, 0xF7, 0xFF, 0xFF, 0xFF}, body), nil, io.ErrUnexpectedEOF, 64 << 10},
		{"trusted-fcs", trusted, nil, ErrFrameSizeMismatch, 64*uint64(len(trusted)) + 64<<10},
		{"fcs-1GiB+1", big, nil, bigWant, bigBound},
		{"fcs-1GiB+1-restic", big, []DOption{WithDecoderMaxMemory(16 << 30)}, bigWant, bigBound},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDecoder(t, append(c.opts, WithDecoderConcurrency(1))...)
			valid := malEncode(t, malText(1000), true)
			if _, err := d.DecodeAll(valid, nil); err != nil {
				t.Fatal(err)
			}
			alloc := uint64(math.MaxUint64)
			for range 2 {
				var m0, m1 runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&m0)
				out, err := d.DecodeAll(c.in, nil)
				runtime.ReadMemStats(&m1)
				if !errors.Is(err, c.want) || len(out) != 0 {
					t.Fatalf("%d bytes, %v; want %v", len(out), err, c.want)
				}
				alloc = min(alloc, m1.TotalAlloc-m0.TotalAlloc)
			}
			if alloc > c.bound {
				t.Fatalf("allocated %d bytes (> %d) for %d bytes of input", alloc, c.bound, len(c.in))
			}
		})
	}
}

// TestDecodeAllAllocBound32 decodes, on 32-bit platforms only, a frame
// whose trusted content size (64 × 16 MiB of empty blocks) is the default
// memory limit: the reservation (1 GiB) succeeds, the frame fails, no
// fatal out-of-memory error.
func TestDecodeAllAllocBound32(t *testing.T) {
	if strconv.IntSize != 32 || testing.Short() {
		t.Skip("32-bit platforms, not short")
	}
	nb := (1<<30/64)/3 + 1 // trusted: FCS / 64 <= 14 + 3 × nb
	in := emptyBlocksFrame(1<<30, nb)
	d := newTestDecoder(t, WithDecoderConcurrency(1))
	if _, err := d.DecodeAll(in, nil); !errors.Is(err, ErrFrameSizeMismatch) && !errors.Is(err, ErrDecoderSizeExceeded) {
		t.Fatal(err)
	}
}

// dictFrame returns text encoded with the zstd dictionary train.dict, the
// dictionary and its ID.
func dictFrame(t testing.TB, text []byte) (frame, dict []byte, id uint32) {
	t.Helper()
	dict = readTestdata(t, "train.dict")
	e := newTestEncoder(t, WithEncoderDict(dict))
	return e.EncodeAll(text, nil), dict, binary.LittleEndian.Uint32(dict[4:])
}

// TestDecoderReset decodes three streams on one Decoder, changing its
// dictionaries with ResetWithOptions between them, then a stream after a
// failed one and after a Reset in the middle of a stream.
func TestDecoderReset(t *testing.T) {
	text := malText(300000)
	plain := malEncode(t, text, true)
	withDict, dict, _ := dictFrame(t, text)
	d := newTestDecoder(t, WithDecoderConcurrency(1))
	read := func(frame []byte) ([]byte, error) { return readAll(t, d, wholeReader(frame), fixedSize(4096), 0) }

	if got, err := read(plain); err != nil || !bytes.Equal(got, text) {
		t.Fatalf("stream 1: %v, equal %v", err, bytes.Equal(got, text))
	}
	if err := d.ResetWithOptions(wholeReader(withDict), WithDecoderDicts(dict)); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if _, err := d.WriteTo(&b); err != nil || !bytes.Equal(b.Bytes(), text) {
		t.Fatalf("stream 2 (dictionary added): %v, equal %v", err, bytes.Equal(b.Bytes(), text))
	}
	if err := d.ResetWithOptions(nil, WithDecoderDictDelete()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(withDict); !errors.Is(err, ErrUnknownDictionary) {
		t.Fatalf("stream 3 (dictionary deleted): %v", err)
	}
	if _, err := d.DecodeAll(withDict, nil); !errors.Is(err, ErrUnknownDictionary) {
		t.Fatalf("DecodeAll after the delete: %v", err)
	}
	if got, err := read(plain); err != nil || !bytes.Equal(got, text) {
		t.Fatalf("after a failed stream: %v, equal %v", err, bytes.Equal(got, text))
	}
	// Reset in the middle of a stream.
	if err := d.Reset(wholeReader(plain)); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Read(make([]byte, 1000)); n == 0 || err != nil {
		t.Fatalf("partial read: %d, %v", n, err)
	}
	if got, err := read(plain); err != nil || !bytes.Equal(got, text) {
		t.Fatalf("after a reset mid-stream: %v, equal %v", err, bytes.Equal(got, text))
	}
}

// countReader counts its Read calls; after its data it returns errors.
type countReader struct {
	b     []byte
	calls int
}

func (r *countReader) Read(p []byte) (int, error) {
	r.calls++
	if len(r.b) == 0 {
		return 0, errors.New("read after the data")
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// TestDecoderReadNoWait checks that Read returns the output it has
// instead of reading more input (which may block): a frame given in one
// read decodes with that one read, the next Read fails with the reader's
// error.
func TestDecoderReadNoWait(t *testing.T) {
	text := malText(5000)
	r := &countReader{b: malEncode(t, text, true)}
	d := newTestDecoder(t, WithDecoderConcurrency(1))
	if err := d.Reset(r); err != nil {
		t.Fatal(err)
	}
	p := make([]byte, 1<<20)
	if n, err := d.Read(p); err != nil || !bytes.Equal(p[:n], text) || r.calls != 1 {
		t.Fatalf("%d bytes, %v, %d reads", n, err, r.calls)
	}
	if _, err := d.Read(p); err == nil || err.Error() != "read after the data" {
		t.Fatalf("second Read: %v", err)
	}
	if _, err := d.Read(p); err == nil || err.Error() != "read after the data" {
		t.Fatalf("sticky: %v", err)
	}
}

// TestIOReadCloser reads and WriteTo's through IOReadCloser, which follows
// the Decoder's Reset; its Close closes the Decoder.
func TestIOReadCloser(t *testing.T) {
	text := malText(200000)
	frame := malEncode(t, text, true)
	d := newTestDecoder(t)
	rc := d.IOReadCloser()
	if err := d.Reset(wholeReader(frame)); err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(rc); err != nil || !bytes.Equal(got, text) {
		t.Fatalf("ReadAll: %v, equal %v", err, bytes.Equal(got, text))
	}
	if err := d.Reset(wholeReader(frame)); err != nil {
		t.Fatal(err)
	}
	wt, ok := rc.(io.WriterTo)
	if !ok {
		t.Fatal("no io.WriterTo")
	}
	var b bytes.Buffer
	if _, err := wt.WriteTo(&b); err != nil || !bytes.Equal(b.Bytes(), text) {
		t.Fatalf("WriteTo: %v, equal %v", err, bytes.Equal(b.Bytes(), text))
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecodeAll(frame, nil); err != ErrDecoderClosed {
		t.Fatalf("DecodeAll after Close: %v", err)
	}
	if _, err := rc.Read(make([]byte, 10)); err != ErrDecoderClosed {
		t.Fatalf("Read after Close: %v", err)
	}
}

// bytesLen is a reader with Bytes and Len (WithDecodeBuffersBelow) whose
// Read fails.
type bytesLen struct{ b []byte }

func (r bytesLen) Bytes() []byte            { return r.b }
func (r bytesLen) Len() int                 { return len(r.b) }
func (r bytesLen) Read([]byte) (int, error) { return 0, errors.New("Read called") }

// TestDecoderOptions checks each option's documented semantics
// (docs/superpowers/phase2-api-decisions.md, "Doc-comment facts").
func TestDecoderOptions(t *testing.T) {
	text := malText(300000)
	frame := malEncode(t, text, true)
	// w64k is not single segment: its window (64 KiB) is below any
	// memory limit these tests set, so the limit is the output's.
	w64k := newTestEncoder(t, WithWindowSize(64<<10)).EncodeAll(text, nil)

	t.Run("defaults", func(t *testing.T) {
		d := newTestDecoder(t)
		if d.o.concurrent != min(4, runtime.GOMAXPROCS(0)) || d.o.maxWindow != 1<<29 ||
			d.o.buffersBelow != 128<<10 || d.o.capLimit || d.o.ignoreChecksum || d.o.lowMem || len(d.o.dicts) != 0 {
			t.Fatalf("%+v", d.o)
		}
		wantMem := uint64(64 << 30)
		if strconv.IntSize == 32 {
			wantMem = 1 << 30
		}
		if d.o.maxMemory != wantMem {
			t.Fatalf("max memory %d, want %d", d.o.maxMemory, wantMem)
		}
	})

	t.Run("IgnoreChecksum", func(t *testing.T) {
		bad := slices.Clone(frame)
		bad[len(bad)-1] ^= 1
		d := newTestDecoder(t)
		if _, err := d.DecodeAll(bad, nil); !errors.Is(err, ErrCRCMismatch) {
			t.Fatalf("default: %v", err)
		}
		if err := d.ResetWithOptions(nil, IgnoreChecksum(true)); err != nil {
			t.Fatal(err)
		}
		if got, err := d.DecodeAll(bad, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("ignored: %v", err)
		}
		if got, err := readAll(t, d, wholeReader(bad), fixedSize(4096), 0); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("ignored, stream: %v", err)
		}
		if err := d.ResetWithOptions(nil, IgnoreChecksum(false)); err != nil {
			t.Fatal(err)
		}
		if _, err := readAll(t, d, wholeReader(bad), fixedSize(4096), 0); !errors.Is(err, ErrCRCMismatch) {
			t.Fatalf("checked again, stream: %v", err)
		}
	})

	t.Run("WithDecodeAllCapLimit", func(t *testing.T) {
		d := newTestDecoder(t, WithDecodeAllCapLimit(true))
		dst := append(make([]byte, 0, 3+len(text)), "pre"...)
		if got, err := d.DecodeAll(frame, dst); err != nil || !bytes.Equal(got[3:], text) || &got[0] != &dst[0] {
			t.Fatalf("exact capacity: %v", err)
		}
		if _, err := d.DecodeAll(frame, dst[:3:2+len(text)]); !errors.Is(err, ErrDecoderSizeExceeded) {
			t.Fatalf("1 byte short: %v", err)
		}
		if _, err := d.DecodeAll(frame, nil); !errors.Is(err, ErrDecoderSizeExceeded) {
			t.Fatalf("nil dst: %v", err)
		}
		// A lower WithDecoderMaxMemory limits it too.
		if err := d.ResetWithOptions(nil, WithDecoderMaxMemory(uint64(len(text)-1))); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DecodeAll(w64k, dst); !errors.Is(err, ErrDecoderSizeExceeded) {
			t.Fatalf("max memory below: %v", err)
		}
		// It disables WithDecodeBuffersBelow.
		if err := d.Reset(bytesLen{frame[:100]}); err != nil {
			t.Fatal(err)
		}
		if d.s.buffered {
			t.Fatal("buffered with WithDecodeAllCapLimit")
		}
		// Changeable with ResetWithOptions.
		if err := d.ResetWithOptions(nil, WithDecodeAllCapLimit(false), WithDecoderMaxMemory(1<<30)); err != nil {
			t.Fatal(err)
		}
		if got, err := d.DecodeAll(frame, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("disabled: %v", err)
		}
	})

	t.Run("WithDecodeBuffersBelow", func(t *testing.T) {
		small := malEncode(t, text[:5000], true)
		d := newTestDecoder(t)
		// Below the default 128 KiB: decoded from Bytes, never Read.
		if got, err := readAll(t, d, bytesLen{small}, fixedSize(100), 0); err != nil || !bytes.Equal(got, text[:5000]) {
			t.Fatalf("buffered: %v", err)
		}
		d = newTestDecoder(t, WithDecodeBuffersBelow(len(small)))
		if _, err := readAll(t, d, bytesLen{small}, fixedSize(100), 0); err == nil || err.Error() != "Read called" {
			t.Fatalf("not below the size: %v", err)
		}
		d = newTestDecoder(t, WithDecodeBuffersBelow(0))
		if _, err := readAll(t, d, bytesLen{nil}, fixedSize(100), 0); err == nil || err.Error() != "Read called" {
			t.Fatalf("size 0: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecodeBuffersBelow(0)); err != nil {
			t.Fatalf("same value: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecodeBuffersBelow(1000)); err == nil {
			t.Fatal("changed with ResetWithOptions")
		}
		// A buffer whose output exceeds the memory limit is streamed
		// (that limit is DecodeAll's; a stream's is the window).
		w64k := newTestEncoder(t, WithWindowSize(64<<10)).EncodeAll(text[:150000], nil)
		if len(w64k) >= 128<<10 {
			t.Fatalf("%d-byte frame", len(w64k))
		}
		d = newTestDecoder(t, WithDecoderMaxMemory(100000))
		if _, err := d.DecodeAll(w64k, nil); !errors.Is(err, ErrDecoderSizeExceeded) {
			t.Fatalf("DecodeAll over the memory limit: %v", err)
		}
		if got, err := readAll(t, d, bytes.NewBuffer(w64k), fixedSize(4096), 0); err != nil || !bytes.Equal(got, text[:150000]) {
			t.Fatalf("buffer over the memory limit: %v", err)
		}
	})

	t.Run("WithDecoderConcurrency", func(t *testing.T) {
		if _, err := NewReader(nil, WithDecoderConcurrency(-1)); err == nil {
			t.Fatal("-1 accepted")
		}
		d := newTestDecoder(t, WithDecoderConcurrency(0))
		if cap(d.dctxs) != runtime.GOMAXPROCS(0) {
			t.Fatalf("0: %d contexts", cap(d.dctxs))
		}
		d = newTestDecoder(t, WithDecoderConcurrency(3))
		if cap(d.dctxs) != 3 {
			t.Fatalf("3: %d contexts", cap(d.dctxs))
		}
		if err := d.ResetWithOptions(nil, WithDecoderConcurrency(3)); err != nil {
			t.Fatalf("same value: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecoderConcurrency(2)); err == nil {
			t.Fatal("changed with ResetWithOptions")
		}
		for _, n := range []int{1, 2} {
			d := newTestDecoder(t, WithDecoderConcurrency(n))
			if got, err := readAll(t, d, wholeReader(frame), fixedSize(4096), 0); err != nil || !bytes.Equal(got, text) {
				t.Fatalf("stream at %d: %v", n, err)
			}
		}
	})

	t.Run("WithDecoderDicts", func(t *testing.T) {
		withDict, dict, id := dictFrame(t, text)
		d := newTestDecoder(t, WithDecoderDicts(dict))
		if got, err := d.DecodeAll(withDict, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("registered: %v", err)
		}
		if got, err := d.DecodeAll(frame, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("frame without dictionary: %v", err)
		}
		if _, err := NewReader(nil, WithDecoderDicts([]byte("not a dictionary"))); err == nil {
			t.Fatal("raw content accepted")
		}
		if _, err := NewReader(nil, WithDecoderDicts(dict[:20])); err == nil { // tables cut short
			t.Fatal("corrupted dictionary accepted")
		}
		// The last dictionary with an ID wins: a raw one with the same ID
		// replaces it, then it replaces the raw one.
		d = newTestDecoder(t, WithDecoderDicts(dict), WithDecoderDictRaw(id, []byte("other")))
		if _, err := d.DecodeAll(withDict, nil); err == nil {
			t.Fatal("decoded with the replaced dictionary")
		}
		if err := d.ResetWithOptions(nil, WithDecoderDicts(dict)); err != nil {
			t.Fatal(err)
		}
		if got, err := d.DecodeAll(withDict, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("re-registered: %v", err)
		}
	})

	t.Run("WithDecoderDictRaw", func(t *testing.T) {
		content := readTestdata(t, "dickens-32k.raw")
		for _, id := range []uint32{0, 1, 0xFFFFFFFF} {
			e := newTestEncoder(t, WithEncoderDictRaw(id, content))
			f := e.EncodeAll(text, nil)
			d := newTestDecoder(t, WithDecoderDictRaw(id, content))
			if got, err := d.DecodeAll(f, nil); err != nil || !bytes.Equal(got, text) {
				t.Fatalf("id %d: %v", id, err)
			}
			if got, err := readAll(t, d, wholeReader(f), fixedSize(4096), 0); err != nil || !bytes.Equal(got, text) {
				t.Fatalf("id %d, stream: %v", id, err)
			}
			if id != 0 {
				d := newTestDecoder(t, WithDecoderDictRaw(id+1, content))
				if _, err := d.DecodeAll(f, nil); !errors.Is(err, ErrUnknownDictionary) {
					t.Fatalf("id %d registered as %d: %v", id, id+1, err)
				}
			}
		}
	})

	t.Run("WithDecoderDictDelete", func(t *testing.T) {
		withDict, dict, id := dictFrame(t, text)
		content := readTestdata(t, "dickens-32k.raw")
		raw := newTestEncoder(t, WithEncoderDictRaw(7, content)).EncodeAll(text, nil)
		d := newTestDecoder(t, WithDecoderDicts(dict), WithDecoderDictRaw(7, content))
		if err := d.ResetWithOptions(nil, WithDecoderDictDelete(id)); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DecodeAll(withDict, nil); !errors.Is(err, ErrUnknownDictionary) {
			t.Fatalf("deleted: %v", err)
		}
		if got, err := d.DecodeAll(raw, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("kept: %v", err)
		}
		// A failed ResetWithOptions changes nothing, not even the
		// dictionaries a later one starts from.
		if err := d.ResetWithOptions(nil, WithDecoderDictDelete(7), WithDecoderConcurrency(99)); err == nil {
			t.Fatal("concurrency changed")
		}
		if err := d.ResetWithOptions(nil); err != nil {
			t.Fatal(err)
		}
		if got, err := d.DecodeAll(raw, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("after a failed ResetWithOptions: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecoderDictDelete()); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DecodeAll(raw, nil); !errors.Is(err, ErrUnknownDictionary) {
			t.Fatalf("all deleted: %v", err)
		}
	})

	t.Run("WithDecoderLowmem", func(t *testing.T) {
		big := malEncode(t, malText(3<<20), true)
		d := newTestDecoder(t, WithDecoderLowmem(true))
		if got, err := readAll(t, d, wholeReader(big), fixedSize(4096), 0); err != nil || !bytes.Equal(got, malText(3<<20)) {
			t.Fatalf("stream: %v", err)
		}
		if dc := d.s.dc; cap(dc.inBuff)+cap(dc.outBuff) > 1<<20 {
			t.Fatalf("buffers kept: %d + %d bytes", cap(dc.inBuff), cap(dc.outBuff))
		}
		if err := d.ResetWithOptions(nil, WithDecoderLowmem(true)); err != nil {
			t.Fatalf("same value: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecoderLowmem(false)); err == nil {
			t.Fatal("changed with ResetWithOptions")
		}
		d = newTestDecoder(t)
		if _, err := readAll(t, d, wholeReader(big), fixedSize(4096), 0); err != nil {
			t.Fatal(err)
		}
		if dc := d.s.dc; cap(dc.outBuff) <= 1<<20 {
			t.Fatalf("default: buffers released (%d bytes)", cap(dc.outBuff))
		}
	})

	t.Run("WithDecoderMaxMemory", func(t *testing.T) {
		for _, n := range []uint64{0, 1<<63 + 1} {
			if _, err := NewReader(nil, WithDecoderMaxMemory(n)); err == nil {
				t.Fatalf("%d accepted", n)
			}
		}
		if _, err := NewReader(nil, WithDecoderMaxMemory(1<<63)); err != nil {
			t.Fatalf("1<<63: %v", err)
		}
		d := newTestDecoder(t, WithDecoderMaxMemory(uint64(len(text))))
		if got, err := d.DecodeAll(frame, nil); err != nil || !bytes.Equal(got, text) {
			t.Fatalf("at the limit: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecoderMaxMemory(uint64(len(text)-1))); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DecodeAll(w64k, nil); !errors.Is(err, ErrDecoderSizeExceeded) {
			t.Fatalf("1 byte over: %v", err)
		}
		// A single-segment frame's window is its content size: over the
		// limit, the window check fails first (mapping table).
		if _, err := d.DecodeAll(frame, nil); !errors.Is(err, ErrWindowSizeExceeded) {
			t.Fatalf("1 byte over, single segment: %v", err)
		}
		// Streams: the limit is the window, not the output.
		big := malText(3 << 20)
		w1M := newTestEncoder(t, WithWindowSize(1<<20)).EncodeAll(big, nil)
		d = newTestDecoder(t, WithDecoderMaxMemory(1<<20))
		if got, err := readAll(t, d, wholeReader(w1M), fixedSize(4<<20), 0); err != nil || !bytes.Equal(got, big) {
			t.Fatalf("stream, window at the limit: %v", err)
		}
		if _, err := d.DecodeAll(w1M, nil); !errors.Is(err, ErrDecoderSizeExceeded) {
			t.Fatalf("DecodeAll over the limit: %v", err)
		}
		d = newTestDecoder(t, WithDecoderMaxMemory(1<<20-1))
		if _, err := readAll(t, d, wholeReader(w1M), fixedSize(4096), 0); !errors.Is(err, ErrWindowSizeExceeded) {
			t.Fatalf("stream, window over the limit: %v", err)
		}
	})

	t.Run("WithDecoderMaxWindow", func(t *testing.T) {
		for _, n := range []uint64{0, MinWindowSize - 1, 1<<41 + 7<<38 + 1} {
			if _, err := NewReader(nil, WithDecoderMaxWindow(n)); err == nil {
				t.Fatalf("%d accepted", n)
			}
		}
		for _, n := range []uint64{MinWindowSize, 1<<41 + 7<<38} {
			if _, err := NewReader(nil, WithDecoderMaxWindow(n)); err != nil {
				t.Fatalf("%d: %v", n, err)
			}
		}
		big := malText(3 << 20)
		w1M := newTestEncoder(t, WithWindowSize(1<<20)).EncodeAll(big, nil)
		d := newTestDecoder(t, WithDecoderMaxWindow(1<<20))
		if got, err := d.DecodeAll(w1M, nil); err != nil || !bytes.Equal(got, big) {
			t.Fatalf("at the limit: %v", err)
		}
		if err := d.ResetWithOptions(nil, WithDecoderMaxWindow(1<<20-1)); err != nil {
			t.Fatal(err)
		}
		if _, err := d.DecodeAll(w1M, nil); !errors.Is(err, ErrWindowSizeExceeded) {
			t.Fatalf("over: %v", err)
		}
		if _, err := readAll(t, d, wholeReader(w1M), fixedSize(4096), 0); !errors.Is(err, ErrWindowSizeExceeded) {
			t.Fatalf("over, stream: %v", err)
		}
		// A lower max memory wins.
		d = newTestDecoder(t, WithDecoderMaxWindow(1<<29), WithDecoderMaxMemory(1<<20-1))
		if _, err := readAll(t, d, wholeReader(w1M), fixedSize(4096), 0); !errors.Is(err, ErrWindowSizeExceeded) {
			t.Fatalf("max memory lower, stream: %v", err)
		}
	})
}

// malCaseNamed returns the malicious corpus case name.
func malCaseNamed(t *testing.T, name string) []byte {
	t.Helper()
	for _, c := range maliciousCases(t) {
		if c.name == name {
			return c.data
		}
	}
	t.Fatalf("no malicious case %s", name)
	return nil
}

// TestErrorsIs pins every row of the error mapping table
// (docs/superpowers/phase2-api-decisions.md) through the public API: each
// sentinel alone matches, on every path that can give it (DecodeAll, Read
// and WriteTo; the memory limit is DecodeAll's only). The Snappy rows come
// with SnappyConverter.
func TestErrorsIs(t *testing.T) {
	frame := malEncode(t, malText(600), true)
	for _, c := range []struct {
		name string
		in   []byte
		want error
	}{
		{"ErrMagicMismatch", []byte{0x27, 0xB5, 0x2F, 0xFD, 0x00, 0x00, 0x01, 0x00, 0x00}, ErrMagicMismatch},
		{"ErrMagicMismatch/after-frame", slices.Concat(frame, []byte("garbage")), ErrMagicMismatch},
		{"ErrWindowSizeExceeded", malCaseNamed(t, "fh-window-log30"), ErrWindowSizeExceeded},
		{"ErrWindowSizeTooSmall", malCaseNamed(t, "match-offset-history-plus1"), ErrWindowSizeTooSmall},
		{"ErrReservedBlockType", malCaseNamed(t, "blk-type3"), ErrReservedBlockType},
		{"ErrCompressedSizeTooBig", malCaseNamed(t, "blk-compressed-128k-plus1"), ErrCompressedSizeTooBig},
		{"ErrBlockTooSmall", malCaseNamed(t, "lit-csize-gt-block"), ErrBlockTooSmall},
		{"ErrUnexpectedBlockSize", malCaseNamed(t, "blk-rle-128k-plus1"), ErrUnexpectedBlockSize},
		{"ErrFrameSizeExceeded", malCaseNamed(t, "fh-fcs0-body"), ErrFrameSizeExceeded},
		{"ErrFrameSizeMismatch", malCaseNamed(t, "fh-fcs-2p63"), ErrFrameSizeMismatch},
		{"ErrCRCMismatch", malCaseNamed(t, "cksum-flip"), ErrCRCMismatch},
		{"ErrUnknownDictionary", malCaseNamed(t, "fh-dictid-unknown"), ErrUnknownDictionary},
		{"io.ErrUnexpectedEOF", malCaseNamed(t, "blk-no-last"), io.ErrUnexpectedEOF},
		{"corrupt", malCaseNamed(t, "seq-no-endmark"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newTestDecoder(t, WithDecoderConcurrency(1))
			_, err := d.DecodeAll(c.in, nil)
			checkSentinel(t, "DecodeAll", err, c.want)
			_, err = readAll(t, d, wholeReader(c.in), fixedSize(4096), 0)
			checkSentinel(t, "Read", err, c.want)
			_, err = writeToAll(t, d, &chunkReader{b: c.in, next: fixedSize(7)})
			checkSentinel(t, "WriteTo", err, c.want)
		})
	}
	t.Run("ErrDecoderSizeExceeded", func(t *testing.T) {
		w1k := newTestEncoder(t, WithWindowSize(MinWindowSize)).EncodeAll(malText(5000), nil)
		d := newTestDecoder(t, WithDecoderMaxMemory(4999))
		_, err := d.DecodeAll(w1k, nil)
		checkSentinel(t, "max memory", err, ErrDecoderSizeExceeded)
		d = newTestDecoder(t, WithDecodeAllCapLimit(true))
		_, err = d.DecodeAll(frame, make([]byte, 0, 599))
		checkSentinel(t, "cap limit", err, ErrDecoderSizeExceeded)
	})
	t.Run("ErrDecoderClosed", func(t *testing.T) {
		d := newTestDecoder(t)
		d.Close()
		d.Close() // twice is fine
		_, err := d.DecodeAll(frame, nil)
		checkSentinel(t, "DecodeAll", err, ErrDecoderClosed)
		_, err = d.Read(make([]byte, 10))
		checkSentinel(t, "Read", err, ErrDecoderClosed)
		_, err = d.WriteTo(io.Discard)
		checkSentinel(t, "WriteTo", err, ErrDecoderClosed)
		checkSentinel(t, "Reset", d.Reset(wholeReader(frame)), ErrDecoderClosed)
		checkSentinel(t, "ResetWithOptions", d.ResetWithOptions(nil), ErrDecoderClosed)
	})
	t.Run("ErrDecoderNilInput", func(t *testing.T) {
		d := newTestDecoder(t)
		_, err := d.Read(make([]byte, 10))
		checkSentinel(t, "Read", err, ErrDecoderNilInput)
		_, err = d.WriteTo(io.Discard)
		checkSentinel(t, "WriteTo", err, ErrDecoderNilInput)
		if err := d.Reset(wholeReader(frame)); err != nil {
			t.Fatal(err)
		}
		if err := d.Reset(nil); err != nil {
			t.Fatal(err)
		}
		_, err = d.Read(make([]byte, 10))
		checkSentinel(t, "Read after Reset(nil)", err, ErrDecoderNilInput)
		if _, err := d.DecodeAll(frame, nil); err != nil {
			t.Fatalf("DecodeAll after Reset(nil): %v", err)
		}
	})
}

// TestRoundTrip decodes the Phase 1 Encoder's output, one-shot and
// streamed (random write sizes, a Flush in the middle), at each level, with
// DecodeAll, Read and WriteTo.
func TestRoundTrip(t *testing.T) {
	names, inputs := sortedInputs(t)
	d := newTestDecoder(t)
	r := rand.New(rand.NewPCG(7, 7))
	for _, lv := range encoderLevels {
		e := newTestEncoder(t, WithEncoderLevel(lv.l))
		for _, name := range names {
			in := inputs[name]
			var b bytes.Buffer
			e.Reset(&b)
			for p := in; len(p) > 0; {
				n := min(len(p), 1+r.IntN(300000))
				if _, err := e.Write(p[:n]); err != nil {
					t.Fatal(err)
				}
				p = p[n:]
				if len(p) > 0 && r.IntN(4) == 0 {
					if err := e.Flush(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			for _, frame := range [][]byte{e.EncodeAll(in, nil), b.Bytes()} {
				got, err := d.DecodeAll(frame, nil)
				if err != nil || !bytes.Equal(got, in) {
					t.Fatalf("L%d %s DecodeAll: %v", lv.c, name, err)
				}
				got, err = readAll(t, d, wholeReader(frame), fixedSize(1+r.IntN(1<<20)), 0)
				if err != nil || !bytes.Equal(got, in) {
					t.Fatalf("L%d %s Read: %v", lv.c, name, err)
				}
				got, err = writeToAll(t, d, wholeReader(frame))
				if err != nil || !bytes.Equal(got, in) {
					t.Fatalf("L%d %s WriteTo: %v", lv.c, name, err)
				}
			}
		}
	}
}

// resticBlob returns n bytes of the test inputs, one after another.
func resticBlob(t *testing.T, n int) []byte {
	names, inputs := sortedInputs(t)
	var b []byte
	for len(b) < n {
		for _, name := range names {
			b = append(b, inputs[name]...)
		}
		b = append(b, malText(1<<20)...)
	}
	return b[:n]
}

// TestResticDecoderOptions uses restic 0.19.1's decoders
// (bench/results/restic-check.md): getZstdDecoder's
// NewReader(nil, WithDecoderConcurrency(0), WithDecoderMaxMemory(16 GiB))
// and the checker's NewReader(nil), with DecodeAll into nil and into a
// reused buffer, over 1 MiB and 4 MiB blobs from restic's encoder options
// (CRC off, window 512 KiB) at its four levels.
func TestResticDecoderOptions(t *testing.T) {
	repo, err := NewReader(nil, WithDecoderConcurrency(0), WithDecoderMaxMemory(16*1024*1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if cap(repo.dctxs) != runtime.GOMAXPROCS(0) {
		t.Fatalf("%d contexts", cap(repo.dctxs))
	}
	checker, err := NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer checker.Close()
	var reuse []byte
	for _, size := range []int{1 << 20, 4 << 20} {
		blob := resticBlob(t, size)
		for _, lv := range encoderLevels {
			e := newTestEncoder(t, WithEncoderLevel(lv.l), WithEncoderCRC(false), WithWindowSize(512*1024))
			frame := e.EncodeAll(blob, nil)
			for _, d := range []*Decoder{repo, checker} {
				got, err := d.DecodeAll(frame, nil)
				if err != nil || !bytes.Equal(got, blob) {
					t.Fatalf("L%d %d: %v", lv.c, size, err)
				}
				reuse, err = d.DecodeAll(frame, reuse[:0])
				if err != nil || !bytes.Equal(reuse, blob) {
					t.Fatalf("L%d %d reused buffer: %v", lv.c, size, err)
				}
			}
		}
	}
}

// TestDecodeTo appends to dst with the shared default Decoder, whose
// output limit is 1 GiB.
func TestDecodeTo(t *testing.T) {
	text := malText(5000)
	got, err := DecodeTo([]byte("pre"), malEncode(t, text, true))
	if err != nil || string(got[:3]) != "pre" || !bytes.Equal(got[3:], text) {
		t.Fatalf("%v", err)
	}
	if defaultDecoder.o.maxMemory != 1<<30 {
		t.Fatalf("limit %d", defaultDecoder.o.maxMemory)
	}
}

// checkStreamResult fails t unless a stream's result (got, serr) is
// DecodeAll's (want, derr), apart from FuzzDecompressStream's three
// exceptions (input cut short, a block regenerating too much, and an
// offset beyond the window of a frame that is not single segment) and one
// more that a Decoder's reused contexts add:
//
//   - A sequence section that fails both ways may fail with
//     ErrWindowSizeTooSmall (offset beyond history) one way and as corrupt
//     (C's corruption_detected) the other: which check fails first depends
//     on which of C's two sequence decoders runs, and C picks the prefetch
//     one (ZSTD_decompressSequencesLong) when the dictionary is cold, i.e.
//     not the one the context's previous frame used, or when the history
//     is over 16 MiB and offsets are long. Both are the same C error.
//     The output (the blocks before it) is the same.
func checkStreamResult(t *testing.T, what string, singleSegment bool, got []byte, serr error, want []byte, derr error) {
	t.Helper()
	if derr == io.ErrUnexpectedEOF {
		if serr == nil || !bytes.HasPrefix(got, want) {
			t.Fatalf("%s: DecodeAll %v, stream %v, %d bytes (DecodeAll %d)", what, derr, serr, len(got), len(want))
		}
		return
	}
	if errors.Is(serr, ErrWindowSizeTooSmall) && !singleSegment {
		if !bytes.HasPrefix(want, got) {
			t.Fatalf("%s: offset beyond history: stream output is not a prefix of DecodeAll's", what)
		}
		return
	}
	blockOrFrame := func(err error) bool {
		return errors.Is(err, ErrFrameSizeExceeded) || errors.Is(err, ErrUnexpectedBlockSize)
	}
	seqCorrupt := func(err error) bool {
		var ce *corruptError
		return errors.Is(err, ErrWindowSizeTooSmall) || errors.As(err, &ce) && ce.code.cCode() == int(dErrCorruptionDetected)
	}
	sameClass := derr == nil && serr == nil || derr != nil && serr != nil && errClass(derr) == errClass(serr)
	if !sameClass && !(derr != nil && serr != nil && (blockOrFrame(derr) || blockOrFrame(serr) || seqCorrupt(derr) && seqCorrupt(serr))) {
		t.Fatalf("%s: DecodeAll %v, stream %v", what, derr, serr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: outputs differ: stream %d bytes, DecodeAll %d (%v)", what, len(got), len(want), derr)
	}
}

// fuzzSeeds adds the decode seeds and the malicious corpus to f, with
// arg as the second argument.
func fuzzSeeds(f *testing.F, arg func(i int) any) {
	for _, dir := range []string{seedsDir, malDir} {
		ents, _ := os.ReadDir(dir)
		for i, e := range ents {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil && strings.HasSuffix(e.Name(), ".zst") {
				f.Add(b, arg(i))
			}
		}
	}
}

// fuzzLimit bounds the output of the fuzz targets (max memory for
// DecodeAll; streams stop reading after it).
const fuzzLimit = 1 << 26

// FuzzDecodeAll decodes arbitrary input with DecodeAll under options
// chosen by opts (no panic; the prefix of dst survives), and requires Read
// and WriteTo to give its output and error class (checkStreamResult).
//
//	bit 0  IgnoreChecksum
//	bit 1  WithDecoderLowmem
//	bit 2  concurrency 1 (else 4)
//	bit 3  max window 1 MiB
//	bit 4  train-12k.dict registered
//	bit 5  dickens-32k.raw registered with ID 0
//	bit 6  WithDecodeAllCapLimit, dst with 1 MiB of capacity
//	bit 7  max memory 1 MiB
func FuzzDecodeAll(f *testing.F) {
	fuzzSeeds(f, func(i int) any { return uint8(i * 37) })
	dict := readTestdata(f, "train-12k.dict")
	raw := readTestdata(f, "dickens-32k.raw")
	f.Fuzz(func(t *testing.T, data []byte, opts uint8) {
		mem := uint64(fuzzLimit)
		if opts&128 != 0 {
			mem = 1 << 20
		}
		o := []DOption{IgnoreChecksum(opts&1 != 0), WithDecoderLowmem(opts&2 != 0), WithDecoderMaxMemory(mem)}
		if opts&4 != 0 {
			o = append(o, WithDecoderConcurrency(1))
		} else {
			o = append(o, WithDecoderConcurrency(4))
		}
		if opts&8 != 0 {
			o = append(o, WithDecoderMaxWindow(1<<20))
		}
		if opts&16 != 0 {
			o = append(o, WithDecoderDicts(dict))
		}
		if opts&32 != 0 {
			o = append(o, WithDecoderDictRaw(0, raw))
		}
		dst := []byte("pre")
		if opts&64 != 0 {
			o = append(o, WithDecodeAllCapLimit(true))
			dst = append(make([]byte, 0, 1<<20), dst...)
		}
		d := newTestDecoder(t, o...)
		want, derr := d.DecodeAll(data, dst)
		if string(want[:3]) != "pre" {
			t.Fatal("dst prefix modified")
		}
		want = want[3:]
		if errors.Is(derr, ErrDecoderSizeExceeded) {
			return // the stream has no such limit
		}
		got, serr := readAll(t, d, wholeReader(data), fixedSize(dStreamOutSize), fuzzLimit)
		checkStreamResult(t, "Read", d.s.dc.fParams.singleSegment, got, serr, want, derr)
		got, serr = writeToAll(t, d, wholeReader(data))
		checkStreamResult(t, "WriteTo", d.s.dc.fParams.singleSegment, got, serr, want, derr)
	})
}

// FuzzDecoderRead decodes arbitrary input with Read, with input chunks and
// read sizes chosen by chunkSeed, and with WriteTo (no panic), and requires
// DecodeAll's output and error class (checkStreamResult), also from a
// bytes.Buffer (WithDecodeBuffersBelow).
func FuzzDecoderRead(f *testing.F) {
	fuzzSeeds(f, func(i int) any { return uint64(i) })
	f.Fuzz(func(t *testing.T, data []byte, chunkSeed uint64) {
		d := newTestDecoder(t, WithDecoderMaxMemory(fuzzLimit), WithDecoderConcurrency(1))
		want, derr := d.DecodeAll(data, nil)
		if errors.Is(derr, ErrDecoderSizeExceeded) {
			return
		}
		r := rand.New(rand.NewPCG(chunkSeed, 0))
		maxChunk := 1 + r.IntN(len(data)+1)
		maxP := 1 + r.IntN(1<<17)
		got, serr := readAll(t, d, &chunkReader{b: data, next: func() int { return 1 + r.IntN(maxChunk) }},
			func() int { return 1 + r.IntN(maxP) }, fuzzLimit)
		checkStreamResult(t, "Read", d.s.dc.fParams.singleSegment, got, serr, want, derr)
		got, serr = writeToAll(t, d, &chunkReader{b: data, next: func() int { return 1 + r.IntN(maxChunk) }})
		checkStreamResult(t, "WriteTo", d.s.dc.fParams.singleSegment, got, serr, want, derr)
		// Under 128 KiB, from a buffer: DecodeAll's result exactly.
		got, serr = readAll(t, d, bytes.NewBuffer(data), fixedSize(maxP), fuzzLimit)
		if len(data) < 128<<10 && (!bytes.Equal(got, want) || serr != derr && (serr == nil || derr == nil || errClass(serr) != errClass(derr))) {
			t.Fatalf("buffered: %d bytes, %v; DecodeAll %d bytes, %v", len(got), serr, len(want), derr)
		}
		checkStreamResult(t, "buffered", d.s.dc.fParams.singleSegment, got, serr, want, derr)
	})
}
