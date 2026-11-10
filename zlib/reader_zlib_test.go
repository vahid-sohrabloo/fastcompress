package zlib_test

import (
	"bytes"
	stdzlib "compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"testing"
	"testing/iotest"

	pkg "github.com/vahid-sohrabloo/fastzstd/zlib"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// Every 2-byte header (CMF, FLG) in front of a valid body and Adler-32:
// NewReader's error class and the data match the standard library's
// (FCHECK, CM, CINFO, FDICT, FLEVEL ignored).
func TestReaderHeaderMatchesStdlib(t *testing.T) {
	c, _ := pkg.NewCompressor(6)
	enc, _ := c.AppendCompress(nil, []byte("zlib header test"))
	body := enc[2:]
	ours, _ := pkg.NewReader(bytes.NewReader(enc))
	std, _ := stdzlib.NewReader(bytes.NewReader(enc))
	in := make([]byte, 2+len(body))
	copy(in[2:], body)
	step := 1
	if testing.Short() {
		step = 7
	}
	for h := 0; h < 1<<16; h += step {
		binary.BigEndian.PutUint16(in, uint16(h))
		err := ours.(pkg.Resetter).Reset(bytes.NewReader(in), nil)
		serr := std.(stdzlib.Resetter).Reset(bytes.NewReader(in), nil)
		if rdClass(err) != rdClass(serr) {
			t.Fatalf("header %04x: %v, stdlib %v", h, err, serr)
		}
		if err == nil {
			got, err := io.ReadAll(ours)
			want, serr := io.ReadAll(std)
			if rdClass(err) != rdClass(serr) || !bytes.Equal(got, want) {
				t.Fatalf("header %04x: read %v, stdlib %v", h, err, serr)
			}
		}
	}
}

// Preset dictionaries: streams of our Writer and of the standard library's
// decode with the dictionary (also through Reset); a wrong or missing one
// is ErrDictionary, an unneeded one is ignored; the empty dictionary's
// DICTID reads without one. As the standard library.
func TestReaderDict(t *testing.T) {
	data := bytes.Repeat([]byte("dictionary words dictionary "), 400)
	dict := []byte("dictionary words dictionary ")
	big := append(bytes.Repeat([]byte{9}, 40000), dict...)
	var streams [][]byte
	var dicts [][]byte
	for _, d := range [][]byte{dict, big, {}} {
		var b bytes.Buffer
		w, err := pkg.NewWriterLevelDict(&b, 6, d)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.Close()
		streams = append(streams, bytes.Clone(b.Bytes()))
		dicts = append(dicts, d)
		b.Reset()
		sw, _ := stdzlib.NewWriterLevelDict(&b, 6, d)
		sw.Write(data)
		sw.Close()
		streams = append(streams, bytes.Clone(b.Bytes()))
		dicts = append(dicts, d)
	}
	plain, _ := pkg.NewCompressor(6)
	p, _ := plain.AppendCompress(nil, data)
	streams = append(streams, p)
	dicts = append(dicts, nil)
	for i, s := range streams {
		for _, d := range [][]byte{dicts[i], nil, []byte("wrong"), {}} {
			r, err := pkg.NewReaderDict(bytes.NewReader(s), d)
			sr, serr := stdzlib.NewReaderDict(bytes.NewReader(s), d)
			if rdClass(err) != rdClass(serr) {
				t.Fatalf("stream %d dict %q: %v, stdlib %v", i, d, err, serr)
			}
			if err != nil {
				continue
			}
			got, err := io.ReadAll(r)
			want, serr := io.ReadAll(sr)
			if rdClass(err) != rdClass(serr) || !bytes.Equal(got, want) || (err == nil && !bytes.Equal(got, data)) {
				t.Fatalf("stream %d dict %q: read %v, stdlib %v", i, d, err, serr)
			}
			// Reset with the dictionary again.
			if err := r.(pkg.Resetter).Reset(bytes.NewReader(s), d); err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("stream %d dict %q: after Reset: %v", i, d, err)
			}
		}
	}
}

// Invalid compressed data is a flate.CorruptInputError, also from Close.
func TestReaderCorrupt(t *testing.T) {
	in := []byte{0x78, 0x9c, 0x07, 0, 0, 0, 0, 1}
	r, err := pkg.NewReader(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	var ce flate.CorruptInputError
	if !asCorrupt(err, &ce) || !asCorrupt(r.Close(), &ce) {
		t.Fatalf("got %v / %v, want a flate.CorruptInputError", err, r.Close())
	}
}

func asCorrupt(err error, ce *flate.CorruptInputError) bool {
	c, ok := err.(flate.CorruptInputError)
	*ce = c
	return ok && c > 0
}

// A header that asks for a dictionary but ends inside DICTID is
// io.ErrUnexpectedEOF, with or without a dictionary, as with the standard
// library.
func TestReaderDictIDTruncated(t *testing.T) {
	for _, in := range [][]byte{{0x78, 0xbb}, {0x78, 0xbb, 1, 2}} {
		for _, d := range [][]byte{nil, []byte("d")} {
			_, err := pkg.NewReaderDict(bytes.NewReader(in), d)
			_, serr := stdzlib.NewReaderDict(bytes.NewReader(in), d)
			if err != io.ErrUnexpectedEOF || rdClass(err) != rdClass(serr) {
				t.Fatalf("%x: %v, stdlib %v", in, err, serr)
			}
		}
	}
}

// Invalid data found while decoded data is still unread: Read returns the
// data first, but Close already reports the pending flate.CorruptInputError
// (as the standard library's Close reports its decoder's error).
func TestReaderClosePendingCorrupt(t *testing.T) {
	// A static block: literals "abc", then a match at distance 100 (past
	// the output), read one byte at a time so that the error is found in a
	// later decoder call than the data, inside the block.
	body := []byte{0x4b, 0x4c, 0x4a, 0x06, 0xda, 0x01, 0x00, 0, 0, 0, 0}
	in := append([]byte{0x78, 0x01}, body...)
	r, err := pkg.NewReader(iotest.OneByteReader(bytes.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if n, err := r.Read(buf); n != 2 || err != nil {
		t.Fatalf("Read = %d, %v", n, err)
	}
	var ce flate.CorruptInputError
	if !asCorrupt(r.Close(), &ce) {
		t.Fatalf("Close: %v", r.Close())
	}
}

// After ErrChecksum a bytes.Reader is left just after the trailer; the
// flate.CorruptInputError offset counts from the DEFLATE data (after the
// 2-byte header), and the data before the invalid block are delivered.
func TestReaderChecksumPositionAndCorruptOffset(t *testing.T) {
	var b bytes.Buffer
	w := stdzlib.NewWriter(&b)
	w.Write(bytes.Repeat([]byte("checksum position "), 3000))
	w.Close()
	bad := append(b.Bytes(), "TAIL"...)
	bad[len(bad)-5] ^= 1 // Adler-32
	br := bytes.NewReader(bad)
	r, err := pkg.NewReader(br)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(r); err != pkg.ErrChecksum || len(got) != 18*3000 || br.Len() != 4 {
		t.Fatalf("bad Adler-32: %v, %d bytes, %d left", err, len(got), br.Len())
	}

	// A valid stored block, then BTYPE 3.
	in := []byte{0x78, 0x01, 0x00, 3, 0, 0xfc, 0xff, 'x', 'y', 'z', 0x07, 0, 0, 0, 0}
	for _, src := range rdSources {
		got, err, _ := rdReadAll(in, src, 1, newReader)
		var ce flate.CorruptInputError
		if !asCorrupt(err, &ce) || int(ce) > len(in)-2 || string(got) != "xyz" {
			t.Fatalf("%s: %v (offset %d of %d), %q", src.name, err, ce, len(in)-2, got)
		}
	}
}

// N-NIT-1: after ErrChecksum, Close returns what the standard library's
// returns (ErrChecksum), also when the Seek back over the bytes read ahead
// fails; Read keeps returning ErrChecksum.
func TestReaderCloseAfterChecksumSeekError(t *testing.T) {
	enc := stdEncode(t, 6, bytes.Repeat([]byte("close after checksum "), 500))
	enc[len(enc)-1] ^= 1 // Adler-32
	in := append(enc, "TAIL"...)
	result := func(z io.ReadCloser) string {
		_, err := io.ReadAll(z)
		_, err2 := z.Read(make([]byte, 1))
		return fmt.Sprint(err, "|", z.Close(), "|", z.Close(), "|", err2)
	}
	std, _ := stdNewReader(bytes.NewReader(in))
	want := result(std)
	for _, r := range []io.Reader{bytes.NewReader(in), rdBadSeeker{bytes.NewReader(in)}} {
		z, err := newReader(r)
		if err != nil {
			t.Fatal(err)
		}
		if got := result(z); got != want {
			t.Fatalf("%T: %s, standard library %s", r, got, want)
		}
	}
}
