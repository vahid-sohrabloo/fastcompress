package flate_test

import (
	"bytes"
	stdflate "compress/flate"
	"errors"
	"io"
	"testing"

	pkg "github.com/vahid-sohrabloo/fastcompress/flate"
)

// The error types have the standard library's texts and work with
// errors.As; invalid data is a CorruptInputError (also from Close) whose
// offset is within the input.
func TestReaderErrorTypes(t *testing.T) {
	inner := errors.New("inner")
	for _, c := range []struct{ ours, std error }{
		{pkg.CorruptInputError(17), stdflate.CorruptInputError(17)},
		{pkg.InternalError("x"), stdflate.InternalError("x")},
		{&pkg.ReadError{Offset: 3, Err: inner}, &stdflate.ReadError{Offset: 3, Err: inner}},
		{&pkg.WriteError{Offset: 4, Err: inner}, &stdflate.WriteError{Offset: 4, Err: inner}},
	} {
		if c.ours.Error() != c.std.Error() {
			t.Errorf("%q, stdlib %q", c.ours.Error(), c.std.Error())
		}
	}
	// A stored block, then a block of BTYPE 3, then padding.
	bad := append([]byte{0, 5, 0, 0xfa, 0xff, 'a', 'b', 'c', 'd', 'e', 0x07}, make([]byte, 100)...)
	for _, in := range [][]byte{{0x07, 0, 0}, bad} {
		r := pkg.NewReader(bytes.NewReader(in))
		_, err := io.ReadAll(r)
		var ce pkg.CorruptInputError
		if !errors.As(err, &ce) || ce <= 0 || int(ce) > len(in) {
			t.Fatalf("%v: want a CorruptInputError within the input (%d bytes)", err, len(in))
		}
		if cerr := r.Close(); cerr != err {
			t.Fatalf("Close: %v, want %v", cerr, err)
		}
	}
}

// NewReaderDict and Reset with a dictionary: streams of our Writer and of
// the standard library's with a preset dictionary (longer than 32 KiB too).
func TestReaderDict(t *testing.T) {
	data := bytes.Repeat([]byte("preset dictionary content "), 300)
	for _, dict := range [][]byte{[]byte("preset dictionary content "), append(make([]byte, 40000), "preset dictionary"...)} {
		var b bytes.Buffer
		w, _ := pkg.NewWriterDict(&b, 9, dict)
		w.Write(data)
		w.Close()
		ours := b.Bytes()
		var sb bytes.Buffer
		sw, _ := stdflate.NewWriterDict(&sb, 9, dict)
		sw.Write(data)
		sw.Close()
		for _, s := range [][]byte{ours, sb.Bytes()} {
			r := pkg.NewReaderDict(bytes.NewReader(s), dict)
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("dict %d bytes: %v", len(dict), err)
			}
			if err := r.(pkg.Resetter).Reset(bytes.NewReader(s), dict); err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("after Reset: %v", err)
			}
			want, werr := io.ReadAll(stdflate.NewReaderDict(bytes.NewReader(s), dict))
			if werr != nil || !bytes.Equal(want, data) {
				t.Fatalf("stdlib: %v", werr)
			}
		}
	}
}

// The ReadCloser implements Resetter, and a Reader (io.Reader with
// ReadByte, such as bufio.Reader) satisfies the Reader interface.
func TestReaderInterfaces(t *testing.T) {
	var _ pkg.Reader = (*bytes.Reader)(nil)
	if _, ok := pkg.NewReader(nil).(pkg.Resetter); !ok {
		t.Fatal("not a Resetter")
	}
	if _, ok := pkg.NewReaderDict(nil, nil).(stdflate.Resetter); !ok {
		t.Fatal("not a compress/flate.Resetter")
	}
}
