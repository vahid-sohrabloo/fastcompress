package zlib_test

import (
	"bytes"
	stdzlib "compress/zlib"
	"errors"
	"io"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/zlib"
)

// The FDICT header (with the dictionary's Adler-32) is the standard
// library's, the stream decodes with its NewReaderDict, Reset keeps the
// dictionary.
func TestWriterDict(t *testing.T) {
	for _, dl := range []int{0, 1, 1000, 50000} {
		dict := streamData(uint64(dl), dl)
		data := append(bytes.Clone(dict[max(0, dl-20000):]), streamData(5, 30000)...)
		for _, level := range []int{zlib.HuffmanOnly, 0, 1, 6, 9, 12} {
			var b bytes.Buffer
			z, err := zlib.NewWriterLevelDict(&b, level, dict)
			if err != nil {
				t.Fatal(err)
			}
			for round := range 2 {
				b.Reset()
				z.Reset(&b)
				z.Write(data)
				z.Close()
				r, err := stdzlib.NewReaderDict(bytes.NewReader(b.Bytes()), dict)
				if err != nil {
					t.Fatalf("dict %d level %d: %v", dl, level, err)
				}
				got, err := io.ReadAll(r)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("dict %d level %d round %d: err %v", dl, level, round, err)
				}
				if level == 6 || level == 9 || level == zlib.HuffmanOnly || level == 1 {
					var sb bytes.Buffer
					sz, _ := stdzlib.NewWriterLevelDict(&sb, level, dict)
					sz.Close()
					hl := 6
					if b.Bytes()[1]&0x20 == 0 {
						hl = 2
					}
					if !bytes.Equal(b.Bytes()[:hl], sb.Bytes()[:hl]) {
						t.Fatalf("dict %d level %d: header % x, stdlib % x", dl, level, b.Bytes()[:hl], sb.Bytes()[:hl])
					}
				}
			}
		}
	}
	if _, err := zlib.NewWriterLevelDict(io.Discard, -3, nil); !errors.Is(err, zlib.ErrInvalidLevel) {
		t.Fatalf("level -3: %v", err)
	}
}

// FLEVEL: libdeflate's per level, 0 for HuffmanOnly (the standard library's).
func TestWriterFLEVEL(t *testing.T) {
	want := map[int]byte{zlib.HuffmanOnly: 0x01, -1: 0x9c, 0: 0x01, 1: 0x01, 2: 0x5e, 5: 0x5e, 6: 0x9c, 7: 0x9c,
		8: 0xda, 12: 0xda}
	for level, flg := range want {
		var b bytes.Buffer
		z, _ := zlib.NewWriterLevel(&b, level)
		z.Close()
		if b.Bytes()[0] != 0x78 || b.Bytes()[1] != flg {
			t.Errorf("level %d: header % x, want 78 %02x", level, b.Bytes()[:2], flg)
		}
	}
	// Header and trailer write errors.
	z := zlib.NewWriter(&failWriter{})
	if err := z.Flush(); err != errFail || z.Close() != errFail {
		t.Fatal("header write error not sticky")
	}
	z = zlib.NewWriter(shortWriter{})
	if _, err := z.Write([]byte("x")); err != io.ErrShortWrite {
		t.Fatalf("short header: %v", err)
	}
	// The body write of Close fails.
	z = zlib.NewWriter(&failWriter{ok: 1})
	z.Write([]byte("x"))
	if err := z.Close(); err != errFail {
		t.Fatalf("body: %v", err)
	}
	z = zlib.NewWriter(&failWriter{ok: 2})
	z.Write([]byte("x"))
	if err := z.Close(); err != errFail {
		t.Fatalf("trailer: %v", err)
	}
	z = zlib.NewWriter(&shortTail4{})
	if err := z.Close(); err != io.ErrShortWrite {
		t.Fatalf("short trailer: %v", err)
	}
	var b bytes.Buffer
	z = zlib.NewWriter(&b)
	z.Close()
	n := b.Len()
	z.Write([]byte("x"))
	z.Flush()
	if b.Len() != n {
		t.Fatal("wrote after Close")
	}
}

// shortTail4 shortens the 4-byte trailer write.
type shortTail4 struct{}

func (shortTail4) Write(p []byte) (int, error) {
	if len(p) == 4 {
		return 2, nil
	}
	return len(p), nil
}
