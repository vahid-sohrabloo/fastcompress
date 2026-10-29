package zstd

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
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
