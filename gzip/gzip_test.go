package gzip_test

import (
	"bytes"
	stdgzip "compress/gzip"
	"errors"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastcompress/gzip"
	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// gzip-specific behaviour (the shared tests are in api_test.go).

// TestFirstMemberOnly: of concatenated members only the first is decoded;
// DecompressEx's nIn points at the next one, so a loop decodes them all;
// libdeflate agrees on every request.
func TestFirstMemberOnly(t *testing.T) {
	c := newCompressor(t, 6)
	first, second := []byte("the first member, the first member"), bytes.Repeat([]byte("second "), 50)
	m1 := compress(t, c, first)
	both := append(append([]byte(nil), m1...), compress(t, c, second)...)
	d := gzip.NewDecompressor()

	out := make([]byte, len(first))
	if _, err := d.Decompress(out, both); err != nil || !bytes.Equal(out, first) {
		t.Fatalf("Decompress: %v", err)
	}
	if _, err := d.Decompress(make([]byte, len(first)+len(second)), both); err != gzip.ErrShortOutput {
		t.Fatalf("Decompress into both sizes: %v, want ErrShortOutput", err)
	}
	got, err := d.AppendDecompress(nil, both, 0)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("AppendDecompress: %v %q", err, got)
	}
	var all []byte
	for src := both; len(src) > 0; {
		buf := make([]byte, 1000)
		nIn, nOut, err := d.DecompressEx(buf, src)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, buf[:nOut]...)
		src = src[nIn:]
	}
	if want := append(append([]byte(nil), first...), second...); !bytes.Equal(all, want) {
		t.Fatalf("member loop: got %q", all)
	}
	runDiff(t, d, []ldReq{
		{"both exact first", both, len(first), true},
		{"both exact sum", both, len(first) + len(second), true},
		{"both ex", both, 1000, false},
	})
	if nIn, _, _ := d.DecompressEx(make([]byte, 100), both); nIn != len(m1) {
		t.Fatalf("nIn = %d, want the first member's %d bytes", nIn, len(m1))
	}
}

// TestOurHeader: the standard library reads our header as libdeflate writes
// it (no name/comment/extra, no time stamp, OS 255) and XFL per level.
func TestOurHeader(t *testing.T) {
	for _, level := range allLevels {
		c := newCompressor(t, level)
		s := compress(t, c, []byte("header"))
		r, err := stdgzip.NewReader(bytes.NewReader(s))
		if err != nil {
			t.Fatal(err)
		}
		h := r.Header
		if h.Name != "" || h.Comment != "" || h.Extra != nil || !h.ModTime.IsZero() || h.OS != 255 {
			t.Fatalf("level %d: header %+v", level, h)
		}
		xfl := byte(0)
		if cl := cLevel(level); cl < 2 {
			xfl = 4
		} else if cl >= 8 {
			xfl = 2
		}
		if s[3] != 0 || s[8] != xfl {
			t.Fatalf("level %d: FLG %#x XFL %#x, want 0, %#x", level, s[3], s[8], xfl)
		}
	}
}

// TestHeaderFields: optional header fields written by the standard library
// are skipped; nIn covers the whole member.
func TestHeaderFields(t *testing.T) {
	data := bytes.Repeat([]byte("fields "), 300)
	var b bytes.Buffer
	w := stdgzip.NewWriter(&b)
	w.Name, w.Comment, w.Extra = "name.txt", "a comment", []byte{'A', 'B', 3, 0, 1, 2, 3}
	w.ModTime = time.Unix(1700000000, 0)
	w.OS = 3
	w.Write(data)
	w.Close()
	s := b.Bytes()
	d := gzip.NewDecompressor()
	buf := make([]byte, len(data)+5)
	nIn, nOut, err := d.DecompressEx(buf, s)
	if err != nil || nIn != len(s) || !bytes.Equal(buf[:nOut], data) {
		t.Fatalf("DecompressEx: nIn=%d/%d nOut=%d err=%v", nIn, len(s), nOut, err)
	}
}

// TestHeaderCRCAndTrailer: FHCRC is skipped without being checked (as
// libdeflate); a wrong CRC-32 or ISIZE is ErrBadData. libdeflate agrees.
func TestHeaderCRCAndTrailer(t *testing.T) {
	data := []byte("header crc and trailer checks")
	s := compress(t, newCompressor(t, 6), data)
	// Insert a (wrong) header CRC: FLG |= FHCRC, 2 bytes after the header.
	h := append(append(append([]byte(nil), s[:10]...), 0xde, 0xad), s[10:]...)
	h[3] |= 0x02
	d := gzip.NewDecompressor()
	out := make([]byte, len(data))
	if _, err := d.Decompress(out, h); err != nil || !bytes.Equal(out, data) {
		t.Fatalf("FHCRC with a wrong CRC: %v", err)
	}
	reqs := []ldReq{{"fhcrc", h, len(data), true}}
	for _, off := range []int{len(s) - 8, len(s) - 5, len(s) - 4, len(s) - 1} {
		m := append([]byte(nil), s...)
		m[off] ^= 0x01
		if _, err := d.Decompress(out, m); !errors.Is(err, gzip.ErrBadData) {
			t.Fatalf("trailer byte %d flipped: %v", off, err)
		}
		reqs = append(reqs, ldReq{"trailer", m, len(data), true}, ldReq{"trailer", m, 2 * len(data), false})
	}
	counts := runDiff(t, d, reqs)
	if counts[creftest.LDBadData] != 8 {
		t.Fatalf("libdeflate result codes %v", counts)
	}
}
