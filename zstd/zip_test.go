package zstd

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

func TestZipMethods(t *testing.T) {
	if ZipMethodWinZip != 93 || ZipMethodPKWare != 20 {
		t.Fatal("wrong zip method numbers")
	}
}

func TestZipCompressor(t *testing.T) {
	inputs := creftest.Inputs(t)
	for _, method := range []uint16{ZipMethodWinZip, ZipMethodPKWare} {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		zw.RegisterCompressor(method, ZipCompressor(WithEncoderLevel(SpeedFastest)))
		names := make([]string, 0, len(inputs))
		for name, in := range inputs {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(in); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if len(zr.File) != len(names) {
			t.Fatalf("got %d entries, want %d", len(zr.File), len(names))
		}
		for _, f := range zr.File {
			if f.Method != method {
				t.Fatalf("%s: method %d", f.Name, f.Method)
			}
			in := inputs[f.Name]
			if len(in) == 0 {
				continue // no frame for empty input without WithZeroFrames
			}
			r, err := f.OpenRaw()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if got := creftest.DecodeC(t, raw); !bytes.Equal(got, in) {
				t.Fatalf("%s: C decode mismatch", f.Name)
			}
			var want bytes.Buffer
			e := newTestEncoder(t, WithEncoderLevel(SpeedFastest))
			e.Reset(&want)
			if _, err := e.Write(in); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, want.Bytes()) {
				t.Fatalf("%s: entry differs from the Encoder stream", f.Name)
			}
		}
	}
}

func TestZipCompressorBadOption(t *testing.T) {
	if _, err := ZipCompressor(WithWindowSize(3))(io.Discard); err == nil {
		t.Fatal("want error")
	}
}

func TestEncodeTo(t *testing.T) {
	e := newTestEncoder(t)
	for name, in := range creftest.Inputs(t) {
		prefix := []byte("prefix")
		got := EncodeTo(append([]byte(nil), prefix...), in)
		want := append(prefix, e.EncodeAll(in, nil)...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: EncodeTo differs from EncodeAll", name)
		}
		if len(in) > 0 && !bytes.Equal(got[len(prefix):], crefLevel(t, 3, in)) {
			t.Fatalf("%s: EncodeTo differs from C level 3", name)
		}
	}
}

func TestEncoderDictDelete(t *testing.T) {
	in := creftest.Inputs(t)["text"]
	if in == nil {
		for _, v := range creftest.Inputs(t) {
			if len(v) > 1000 {
				in = v
				break
			}
		}
	}
	raw := in[:512]
	nodict := newTestEncoder(t).EncodeAll(in, nil)
	e := newTestEncoder(t, WithEncoderDictRaw(7, raw))
	if bytes.Equal(e.EncodeAll(in, nil), nodict) {
		t.Fatal("dictionary had no effect")
	}
	if err := e.ResetWithOptions(io.Discard, WithEncoderDictDelete()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e.EncodeAll(in, nil), nodict) {
		t.Fatal("output after WithEncoderDictDelete differs from no-dict output")
	}
}

func TestConcurrentBlocks(t *testing.T) {
	for name, in := range creftest.Inputs(t) {
		want := newTestEncoder(t).EncodeAll(in, nil)
		e := newTestEncoder(t, WithConcurrentBlocks(true))
		if !bytes.Equal(e.EncodeAll(in, nil), want) {
			t.Fatalf("%s: WithConcurrentBlocks changed the output", name)
		}
	}
	e := newTestEncoder(t, WithConcurrentBlocks(true))
	if err := e.ResetWithOptions(nil, WithConcurrentBlocks(false)); err == nil {
		t.Fatal("changing WithConcurrentBlocks on reset must fail")
	}
}

// A closed zip entry must not expose the pooled Encoder through promoted
// methods (Flush, ReadFrom, ...), which would nil-panic or write into an
// encoder already handed to another entry.
func TestZipWriterAfterClose(t *testing.T) {
	w, err := ZipCompressor()(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.(interface{ Flush() error }); ok {
		t.Fatal("zip entry writer exposes Flush")
	}
	if _, ok := w.(io.ReaderFrom); ok {
		t.Fatal("zip entry writer exposes ReadFrom")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("Write after Close succeeded")
	}
}

// zipEntry is one entry of a test archive: raw entries hold a frame written
// with CreateRaw.
type zipEntry struct {
	name   string
	data   []byte // the decompressed content
	raw    []byte // nil: compressed with ZipCompressor
	method uint16
}

// zipArchive writes entries into a zip archive.
func zipArchive(t *testing.T, entries []zipEntry) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(ZipMethodWinZip, ZipCompressor())
	zw.RegisterCompressor(ZipMethodPKWare, ZipCompressor())
	for _, e := range entries {
		var w io.Writer
		var err error
		if e.raw == nil {
			w, err = zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: e.method})
		} else {
			w, err = zw.CreateRaw(&zip.FileHeader{
				Name:               e.name,
				Method:             e.method,
				CRC32:              crc32.ChecksumIEEE(e.data),
				CompressedSize64:   uint64(len(e.raw)),
				UncompressedSize64: uint64(len(e.data)),
			})
		}
		if err != nil {
			t.Fatal(err)
		}
		in := e.raw
		if in == nil {
			in = e.data
		}
		if _, err := w.Write(in); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

func readZipFile(f *zip.File) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(r)
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	return b, err
}

func TestZipDecompressor(t *testing.T) {
	inputs := creftest.Inputs(t)
	var entries []zipEntry
	for name, in := range inputs {
		if len(in) > 1<<20 {
			continue // the decoder's own tests cover large inputs
		}
		var c []byte
		if len(in) > 0 {
			c = crefLevel(t, 19, in)
		}
		for _, m := range []uint16{ZipMethodWinZip, ZipMethodPKWare} {
			p := fmt.Sprintf("%d/", m)
			entries = append(entries, zipEntry{name: p + "go/" + name, data: in, method: m})
			if c != nil {
				entries = append(entries, zipEntry{name: p + "c/" + name, data: in, raw: c, method: m})
			}
		}
	}
	before := runtime.NumGoroutine()
	zr := zipArchive(t, entries)
	zr.RegisterDecompressor(ZipMethodWinZip, ZipDecompressor())
	zr.RegisterDecompressor(ZipMethodPKWare, ZipDecompressor())
	want := map[string][]byte{}
	for _, e := range entries {
		want[e.name] = e.data
	}
	for _, f := range zr.File {
		got, err := readZipFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if !bytes.Equal(got, want[f.Name]) {
			t.Fatalf("%s: content differs", f.Name)
		}
	}

	// 64 entries read concurrently, through one decompressor (one pool).
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		f := zr.File[i%len(zr.File)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := readZipFile(f)
			if err == nil && !bytes.Equal(got, want[f.Name]) {
				err = fmt.Errorf("%s: content differs", f.Name)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// No goroutine is left (see also TestZipDecompressorIdle).
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("goroutines: %d before, %d after", before, n)
	}
}

func TestZipDecompressorCorrupt(t *testing.T) {
	in := malText(200 << 10)
	frame := crefLevel(t, 3, in)
	flipped := bytes.Clone(frame)
	flipped[len(flipped)-1] ^= 0x55 // in the content checksum
	zr := zipArchive(t, []zipEntry{
		{name: "truncated", data: in, raw: frame[:len(frame)-10], method: ZipMethodWinZip},
		{name: "flipped", data: in, raw: flipped, method: ZipMethodWinZip},
		{name: "garbage", data: in, raw: []byte("not a zstd frame at all"), method: ZipMethodWinZip},
	})
	zr.RegisterDecompressor(ZipMethodWinZip, ZipDecompressor())
	for _, f := range zr.File {
		_, err := readZipFile(f)
		if err == nil {
			t.Fatalf("%s: no error", f.Name)
		}
		// The decoder, not zip's CRC check, must catch it.
		if errors.Is(err, zip.ErrChecksum) {
			t.Fatalf("%s: only zip's checksum failed: %v", f.Name, err)
		}
		if f.Name == "flipped" && !errors.Is(err, ErrCRCMismatch) {
			t.Fatalf("flipped: %v, want ErrCRCMismatch", err)
		}
	}
}

// zipWindowFrame is a frame whose window is 2^exp + mantissa*2^exp/8 bytes,
// with one empty last raw block.
func zipWindowFrame(exp, mantissa byte) []byte {
	return []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, (exp-10)<<3 | mantissa, 0x01, 0, 0}
}

func TestZipDecompressorOptions(t *testing.T) {
	read := func(dec func(io.Reader) io.ReadCloser, in []byte) ([]byte, error) {
		r := dec(bytes.NewReader(in))
		b, err := io.ReadAll(r)
		if cerr := r.Close(); cerr != nil {
			t.Fatal("Close:", cerr)
		}
		return b, err
	}
	// The default max window is 128 MB (doc text).
	if _, err := read(ZipDecompressor(), zipWindowFrame(27, 1)); !errors.Is(err, ErrWindowSizeExceeded) {
		t.Fatalf("144 MiB window, default: %v", err)
	}
	d0 := newTestDecoder(t)
	if err := d0.Reset(bytes.NewReader(zipWindowFrame(27, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(d0); err != nil {
		t.Fatal("NewReader's default:", err)
	}
	if got, err := read(ZipDecompressor(), zipWindowFrame(27, 0)); err != nil || len(got) != 0 {
		t.Fatalf("128 MiB window, default: %d bytes, %v", len(got), err)
	}
	// Options override it, both ways.
	if _, err := read(ZipDecompressor(WithDecoderMaxWindow(1<<20)), zipWindowFrame(21, 0)); !errors.Is(err, ErrWindowSizeExceeded) {
		t.Fatalf("2 MiB window, max 1 MiB: %v", err)
	}
	if !testing.Short() {
		if got, err := read(ZipDecompressor(WithDecoderMaxWindow(1<<28)), zipWindowFrame(27, 1)); err != nil || len(got) != 0 {
			t.Fatalf("144 MiB window, max 256 MiB: %d bytes, %v", len(got), err)
		}
	}
	// An option error surfaces on the first Read.
	_, wantErr := NewReader(nil, WithDecoderMaxMemory(0))
	r := ZipDecompressor(WithDecoderMaxMemory(0))(bytes.NewReader(zipWindowFrame(10, 0)))
	for range 2 {
		if _, err := r.Read(make([]byte, 10)); wantErr == nil || err == nil || err.Error() != wantErr.Error() {
			t.Fatalf("bad option: Read error %v, want %v", err, wantErr)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// WithDecoderConcurrency(1) is forced, even over an explicit option.
	dec := ZipDecompressor(WithDecoderConcurrency(4))
	r = dec(bytes.NewReader(nil))
	zr, ok := r.(*zipReader)
	if !ok || zr.dec == nil || zr.dec.o.concurrent != 1 || zr.dec.o.pipelined() {
		t.Fatalf("concurrency not forced to 1: %#v", r)
	}
	// Close hands the Decoder back without its reader; a second Close and
	// a Read after Close are safe.
	d := zr.dec
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if d.s.r != nil {
		t.Fatal("pooled Decoder keeps the entry's reader")
	}
	if err := r.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
	if _, err := r.Read(make([]byte, 1)); err == nil {
		t.Fatal("Read after Close succeeded")
	}
	// The next entry reuses it, and only that entry: the second Close did
	// not pool it again.
	in := []byte("hello, zip")
	r = dec(bytes.NewReader(newTestEncoder(t).EncodeAll(in, nil)))
	if r.(*zipReader).dec != d {
		t.Fatal("Decoder not reused")
	}
	other := dec(bytes.NewReader(nil))
	if other.(*zipReader).dec == d {
		t.Fatal("one Decoder handed to two open entries")
	}
	defer other.Close()
	if b, err := io.ReadAll(r); err != nil || !bytes.Equal(b, in) {
		t.Fatalf("reused Decoder: %q, %v", b, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// A closed entry does not expose the Decoder's other methods.
	if _, ok := r.(io.WriterTo); ok {
		t.Fatal("zip entry reader exposes WriteTo")
	}
}

// The pool keeps at most zipPoolSize idle Decoders; the others are closed.
func TestZipDecompressorPoolCap(t *testing.T) {
	if want := map[int]int{64: 8, 32: 4}[strconv.IntSize]; zipPoolSize != want {
		t.Fatalf("zipPoolSize %d, want %d", zipPoolSize, want)
	}
	dec := ZipDecompressor()
	old := map[*Decoder]bool{}
	rs := make([]io.ReadCloser, zipPoolSize+3)
	for i := range rs {
		rs[i] = dec(bytes.NewReader(nil))
		old[rs[i].(*zipReader).dec] = true
	}
	for _, r := range rs {
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	reused := 0
	for range rs {
		r := dec(bytes.NewReader(nil))
		if old[r.(*zipReader).dec] {
			reused++
		}
		defer r.Close()
	}
	if reused != zipPoolSize {
		t.Fatalf("%d Decoders reused, want %d", reused, zipPoolSize)
	}
	closed := 0
	for d := range old {
		if d.closed.Load() {
			closed++
		}
	}
	if closed != len(rs)-zipPoolSize {
		t.Fatalf("%d Decoders closed, want %d", closed, len(rs)-zipPoolSize)
	}
}

// Entries left open mid-stream start no goroutine, even with an explicit
// WithDecoderConcurrency (which would pipeline them), and an idle pooled
// Decoder keeps no stream buffer over lowMemBuffers after a large-window
// entry, read to its end or not, and no output decoded at Reset from a
// reader with Bytes (WithDecodeBuffersBelow). Two decompressors stand for
// a registration under each zip method: each has its own pool.
func TestZipDecompressorIdle(t *testing.T) {
	in := malText(8 << 20)
	var frame bytes.Buffer
	e := newTestEncoder(t, WithWindowSize(128<<20))
	e.Reset(&frame)
	for p := in; len(p) > 0; p = p[min(len(p), 1<<20):] {
		if _, err := e.Write(p[:min(len(p), 1<<20)]); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	small := bytes.Repeat([]byte("zip "), 128<<10) // 512 KiB, under lowMemBuffers
	smallFrame := newTestEncoder(t).EncodeAll(small, nil)
	before := runtime.NumGoroutine()
	decs := []func(io.Reader) io.ReadCloser{
		ZipDecompressor(WithDecoderConcurrency(4)),
		ZipDecompressor(WithDecoderConcurrency(4)),
	}
	for k, dec := range decs {
		var rs []io.ReadCloser
		var ds []*Decoder
		for i := range zipPoolSize {
			r := dec(bytes.NewReader(frame.Bytes()))
			rs = append(rs, r)
			d := r.(*zipReader).dec
			ds = append(ds, d)
			if i%2 == 0 { // read to the end
				if b, err := io.ReadAll(r); err != nil || !bytes.Equal(b, in) {
					t.Fatalf("dec %d entry %d: %d bytes, %v", k, i, len(b), err)
				}
			} else if _, err := io.ReadFull(r, make([]byte, 6<<20)); err != nil { // stop mid-frame
				t.Fatal(err)
			}
			if n := runtime.NumGoroutine(); n > before {
				t.Fatalf("dec %d entry %d open: goroutines %d, %d before", k, i, n, before)
			}
			if dc := d.s.dc; dc == nil || cap(dc.outBuff) < 4<<20 {
				t.Fatalf("dec %d entry %d: no large window buffer to release", k, i)
			}
		}
		for i, r := range rs {
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			d := ds[i]
			if d.closed.Load() {
				t.Fatalf("dec %d entry %d: Decoder closed, not pooled", k, i)
			}
			if n := cap(d.s.dc.inBuff) + cap(d.s.dc.outBuff) + cap(d.s.buf); n > lowMemBuffers {
				t.Fatalf("dec %d entry %d: idle Decoder keeps %d bytes of stream buffers", k, i, n)
			}
		}
		// A reader with Bytes is decoded whole at Reset.
		r := dec(bytes.NewBuffer(smallFrame))
		d := r.(*zipReader).dec
		if !d.s.buffered || cap(d.s.buf) < len(small) {
			t.Fatalf("dec %d: Bytes reader not decoded at Reset", k)
		}
		if b, err := io.ReadAll(r); err != nil || !bytes.Equal(b, small) {
			t.Fatalf("dec %d: Bytes reader: %d bytes, %v", k, len(b), err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		if d.s.buf != nil {
			t.Fatalf("dec %d: idle Decoder keeps %d bytes of decoded output", k, cap(d.s.buf))
		}
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("after Close: goroutines %d, %d before", n, before)
	}
}
