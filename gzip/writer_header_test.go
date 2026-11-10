package gzip_test

import (
	"bytes"
	stdgzip "compress/gzip"
	"encoding/binary"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/gzip"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// headerCases are Headers to compare with the standard library's writer.
var headerCases = []gzip.Header{
	{OS: 255},
	{Name: "a.txt", Comment: "a comment", Extra: []byte{'A', 'B', 2, 0, 1, 2}, ModTime: time.Unix(1700000000, 5),
		OS: 3},
	{Name: "café ÿ", OS: 255},
	// (The standard library's reader rejects strings over 511 bytes.)
	{Comment: strings.Repeat("c", 500)},
	{Extra: []byte{}},
	{Extra: make([]byte, 65535)},
	{ModTime: time.Unix(-5, 0)},
	{ModTime: time.Unix(1<<32-1, 0)},
	{ModTime: time.Unix(1<<32, 0)},
	// After 2106 the standard library truncates to 32 bits.
	{ModTime: time.Unix(1<<32+5, 0)},
	{ModTime: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)},
	{ModTime: time.Unix(0, 0)},
	// Errors: the standard library writes the header up to the bad field.
	{Name: "日本"},
	{Name: "a\x00b"},
	{Comment: "x\x00"},
	{Extra: []byte{1}, Name: "ok", Comment: "Ā"},
	{Name: "\u0100", Comment: "a valid comment after a bad name"},
	{Extra: make([]byte, 65536), Name: "n"},
	{Name: "bad \xff utf8"},
}

// toStd converts a Header to the standard library's.
func toStd(h gzip.Header) stdgzip.Header {
	return stdgzip.Header{Comment: h.Comment, Extra: h.Extra, ModTime: h.ModTime, Name: h.Name, OS: h.OS}
}

// The header bytes, the errors and the output on a header error are the
// standard library's (level 6, where both write XFL 0), and the standard
// library's reader returns the fields.
func TestWriterHeaderMatchesStdlib(t *testing.T) {
	data := []byte("some data for the member")
	for i, h := range headerCases {
		var sb, b bytes.Buffer
		sz, _ := stdgzip.NewWriterLevel(&sb, 6)
		sz.Header = toStd(h)
		_, sWerr := sz.Write(data)
		sCerr := sz.Close()

		z, _ := gzip.NewWriterLevel(&b, 6)
		z.Header = h
		_, werr := z.Write(data)
		cerr := z.Close()

		if errText(werr) != errText(sWerr) || errText(cerr) != errText(sCerr) {
			t.Fatalf("case %d: errors %v / %v, stdlib %v / %v", i, werr, cerr, sWerr, sCerr)
		}
		if sWerr != nil {
			if !bytes.Equal(b.Bytes(), sb.Bytes()) {
				t.Fatalf("case %d: output on error % x, stdlib % x", i, b.Bytes(), sb.Bytes())
			}
			continue
		}
		if !bytes.Equal(b.Bytes()[:headerEnd(t, b.Bytes())], sb.Bytes()[:headerEnd(t, sb.Bytes())]) {
			t.Fatalf("case %d: header % x, stdlib % x", i, b.Bytes()[:min(b.Len(), 40)], sb.Bytes()[:min(sb.Len(), 40)])
		}
		r, err := stdgzip.NewReader(bytes.NewReader(b.Bytes()))
		if err != nil {
			t.Fatalf("case %d: stdlib reader: %v", i, err)
		}
		got, err := io.ReadAll(r)
		sh := toStd(h)
		// MTIME is uint32(Unix()) for times after the epoch, else 0; the
		// reader returns 0 as the zero time.
		if mt := uint32(sh.ModTime.Unix()); sh.ModTime.Unix() <= 0 || mt == 0 {
			sh.ModTime = time.Time{}
		} else {
			sh.ModTime = time.Unix(int64(mt), 0)
		}
		if len(sh.Extra) == 0 && sh.Extra != nil {
			sh.Extra = nil // the reader returns nil for an empty field
		}
		rh := r.Header
		if err != nil || !bytes.Equal(got, data) || rh.Name != sh.Name || rh.Comment != sh.Comment ||
			!bytes.Equal(rh.Extra, sh.Extra) || !rh.ModTime.Equal(sh.ModTime) || rh.OS != sh.OS {
			t.Fatalf("case %d: stdlib reader: err %v header %+v, want %+v", i, err, rh, sh)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// headerEnd returns the length of the gzip header at the start of b.
func headerEnd(t *testing.T, b []byte) int {
	t.Helper()
	flg, n := b[3], 10
	if flg&4 != 0 {
		n += 2 + int(b[n]) | int(b[n+1])<<8
	}
	for _, f := range []byte{8, 16} {
		if flg&f != 0 {
			n += bytes.IndexByte(b[n:], 0) + 1
		}
	}
	return n
}

// With header fields set, the DEFLATE body of a single-chunk stream is
// still libdeflate's.
func TestWriterHeaderBodyMatchesLibdeflate(t *testing.T) {
	data := bytes.Repeat([]byte("header fields do not change the body. "), 3000)
	for _, level := range []int{0, 1, 6, 9, 12} {
		var b bytes.Buffer
		z, _ := gzip.NewWriterLevel(&b, level)
		z.Header = headerCases[1]
		z.Write(data)
		z.Close()
		raw := creftest.LDCompress(t, data, level, creftest.LDRaw)
		body := b.Bytes()[headerEnd(t, b.Bytes()) : b.Len()-8]
		if !bytes.Equal(body, raw) {
			t.Fatalf("level %d: body %d bytes, libdeflate %d", level, len(body), len(raw))
		}
	}
}

// XFL: libdeflate's per level, 0 for HuffmanOnly (the standard library's).
func TestWriterXFL(t *testing.T) {
	want := map[int]byte{gzip.HuffmanOnly: 0, -1: 0, 0: 4, 1: 4, 2: 0, 7: 0, 8: 2, 9: 2, 12: 2}
	for level, x := range want {
		var b bytes.Buffer
		z, _ := gzip.NewWriterLevel(&b, level)
		z.Close()
		if b.Bytes()[8] != x {
			t.Errorf("level %d: XFL %d, want %d", level, b.Bytes()[8], x)
		}
	}
	var sb bytes.Buffer
	sz, _ := stdgzip.NewWriterLevel(&sb, stdgzip.HuffmanOnly)
	sz.Close()
	if sb.Bytes()[8] != want[gzip.HuffmanOnly] {
		t.Errorf("stdlib HuffmanOnly XFL %d", sb.Bytes()[8])
	}
}

// Members written one after another (Reset on the same writer) are read
// by the standard library as one multi-member stream; Reset clears the
// Header to OS 255.
func TestWriterMultistream(t *testing.T) {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	var want []byte
	for i := range 4 {
		if z.OS != 255 || z.Name != "" {
			t.Fatalf("member %d: Header %+v after Reset", i, z.Header)
		}
		z.Name = "member" + string(rune('0'+i))
		part := streamData(uint64(50+i), 1000*(i+1)*(i+1))
		want = append(want, part...)
		z.Write(part)
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		z.Reset(&b)
	}
	r, err := stdgzip.NewReader(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("multistream: %d bytes, err %v", len(got), err)
	}
	// Member by member, with the names.
	br := bytes.NewReader(b.Bytes())
	r, _ = stdgzip.NewReader(br)
	for i := 0; ; i++ {
		r.Multistream(false)
		if _, err := io.Copy(io.Discard, r); err != nil || r.Name != "member"+string(rune('0'+i)) {
			t.Fatalf("member %d: name %q, err %v", i, r.Name, err)
		}
		if err := r.Reset(br); err == io.EOF {
			if i != 3 {
				t.Fatalf("%d members", i+1)
			}
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

// Errors of the header write are sticky like any other write error.
func TestWriterHeaderWriteError(t *testing.T) {
	z := gzip.NewWriter(&failWriter{})
	if err := z.Flush(); err != errFail {
		t.Fatalf("Flush: %v", err)
	}
	if err := z.Close(); err != errFail {
		t.Fatalf("Close: %v", err)
	}
	z = gzip.NewWriter(&failWriter{})
	if err := z.Close(); err != errFail {
		t.Fatalf("Close of an empty member: %v", err)
	}
	z = gzip.NewWriter(shortWriter{})
	if _, err := z.Write([]byte("x")); err != io.ErrShortWrite {
		t.Fatalf("short header write: %v", err)
	}
	// The trailer write fails.
	// The body write of Close fails.
	z = gzip.NewWriter(&failWriter{ok: 1})
	z.Write([]byte("x"))
	if err := z.Close(); err != errFail {
		t.Fatalf("body: %v", err)
	}
	z = gzip.NewWriter(&failWriter{ok: 2})
	z.Write([]byte("x"))
	if err := z.Close(); err != errFail {
		t.Fatalf("trailer: %v", err)
	}
	z = gzip.NewWriter(&shortTail{})
	z.Write([]byte("x"))
	if err := z.Close(); err != io.ErrShortWrite {
		t.Fatalf("short trailer: %v", err)
	}
	// A header error, then Flush and Close return it.
	z = gzip.NewWriter(io.Discard)
	z.Name = "Ā"
	if err := z.Flush(); err == nil || z.Close() != err {
		t.Fatalf("header error via Flush: %v", err)
	}
	// Writing after Close does not write a header.
	var b bytes.Buffer
	z = gzip.NewWriter(&b)
	z.Close()
	n := b.Len()
	z.Write([]byte("x"))
	z.Flush()
	if b.Len() != n {
		t.Fatal("wrote after Close")
	}
}

// shortTail writes everything except a final 8-byte trailer in full.
type shortTail struct{}

func (shortTail) Write(p []byte) (int, error) {
	if len(p) == 8 {
		return 4, nil
	}
	return len(p), nil
}

// A member larger than 16 MiB (and so than any 24-bit size) has the right
// CRC-32 and ISIZE: the standard library's reader checks both.
func TestWriterLargeMember(t *testing.T) {
	n := 17<<20 + 12345
	data := make([]byte, n)
	block := streamData(60, 1<<16)
	for i := 0; i < n; i += len(block) {
		copy(data[i:], block)
		data[i] = byte(i >> 16) // vary every block a little
	}
	var b bytes.Buffer
	z, _ := gzip.NewWriterLevel(&b, gzip.BestSpeed)
	for p := data; len(p) > 0; p = p[min(len(p), 3<<20):] {
		z.Write(p[:min(len(p), 3<<20)])
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if isize := binary.LittleEndian.Uint32(b.Bytes()[b.Len()-4:]); isize != uint32(n) {
		t.Fatalf("ISIZE %d, want %d", isize, n)
	}
	got, err := stdDecode(b.Bytes())
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stdlib reader: %d bytes, err %v", len(got), err)
	}
}
