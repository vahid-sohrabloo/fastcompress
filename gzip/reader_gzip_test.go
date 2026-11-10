package gzip_test

import (
	"bufio"
	"bytes"
	stdflate "compress/flate"
	stdgzip "compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"
	"time"

	pkg "github.com/vahid-sohrabloo/fastzstd/gzip"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// stdMember writes one member with the standard library's Writer.
func stdMember(t testing.TB, h stdgzip.Header, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := stdgzip.NewWriter(&b)
	w.Header = h
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// rawMember builds a member with the given FLG and header fields (after
// the fixed 10 bytes, as raw bytes) and a valid body and trailer for data.
func rawMember(flg byte, fields []byte, data []byte) []byte {
	c, _ := pkg.NewCompressor(6)
	enc, _ := c.AppendCompress(nil, data)
	m := []byte{0x1f, 0x8b, 8, flg, 1, 2, 3, 4, 0, 3}
	m = append(m, fields...)
	return append(m, enc[10:]...)
}

// withHCRC appends the CRC-16 of hdr (FHCRC).
func withHCRC(hdr []byte) []byte {
	return binary.LittleEndian.AppendUint16(hdr, uint16(crc32.ChecksumIEEE(hdr)))
}

// sameHeader compares our Header with the standard library's.
func sameHeader(a pkg.Header, b stdgzip.Header) bool {
	return a.Name == b.Name && a.Comment == b.Comment && bytes.Equal(a.Extra, b.Extra) &&
		(a.Extra == nil) == (b.Extra == nil) && a.ModTime.Equal(b.ModTime) && a.OS == b.OS
}

// Headers: every field, Latin-1 strings, string limits, FHCRC, FTEXT and
// reserved bits, and truncations inside each field: NewReader's error and
// the Header fields match the standard library's.
func TestReaderHeaderMatchesStdlib(t *testing.T) {
	data := []byte("header test data")
	var ins [][]byte
	for _, h := range []stdgzip.Header{
		{}, {OS: 255}, {Name: "file.txt", OS: 3}, {Comment: "a comment"}, {Extra: []byte{}}, {Extra: []byte{1, 2, 3}},
		{Extra: bytes.Repeat([]byte{7}, 65535)}, {Name: "naïve éÿ", Comment: "x y"},
		{Name: strings.Repeat("n", 511)}, {Comment: strings.Repeat("c", 511)},
		{ModTime: time.Unix(1234567890, 0)}, {ModTime: time.Unix(1<<32-1, 0)},
		{Name: "all", Comment: "fields", Extra: []byte("ex"), ModTime: time.Unix(1, 0), OS: 11},
	} {
		ins = append(ins, stdMember(t, h, data))
	}
	ins = append(ins,
		rawMember(0x08, []byte(strings.Repeat("n", 512)+"\x00"), data), // name too long
		rawMember(0x10, []byte(strings.Repeat("c", 512)+"\x00"), data), // comment too long
		rawMember(0x01, nil, data),                                     // FTEXT
		rawMember(0x20, nil, data), rawMember(0xe0, nil, data),         // reserved bits
		rawMember(0x02, []byte{0, 0}, data), // wrong FHCRC
		rawMember(0x1e, []byte("\x02\x00ab"+"name\x00"+"comment\x00"), data),
	)
	hdr := []byte{0x1f, 0x8b, 8, 0x1e, 1, 2, 3, 4, 0, 3, 2, 0, 'a', 'b', 'n', 0, 'c', 0}
	ins = append(ins, rawMember(0x1e, withHCRC(hdr)[10:], data)) // right FHCRC over all fields
	var trunc [][]byte
	for _, in := range ins {
		for cut := 0; cut < min(len(in), 40); cut++ {
			trunc = append(trunc, in[:cut])
		}
	}
	for i, in := range append(ins, trunc...) {
		z, err := pkg.NewReader(bytes.NewReader(in))
		s, serr := stdgzip.NewReader(bytes.NewReader(in))
		if rdClass(err) != rdClass(serr) {
			t.Fatalf("input %d: NewReader %v, stdlib %v", i, err, serr)
		}
		if err != nil {
			if z != nil {
				t.Fatalf("input %d: non-nil Reader with error", i)
			}
			continue
		}
		if !sameHeader(z.Header, s.Header) {
			t.Fatalf("input %d: Header %+v, stdlib %+v", i, z.Header, s.Header)
		}
		got, err := io.ReadAll(z)
		want, serr := io.ReadAll(s)
		if rdClass(err) != rdClass(serr) || !bytes.Equal(got, want) {
			t.Fatalf("input %d: read %v, stdlib %v", i, err, serr)
		}
	}
}

// Concatenated members read as one stream (Header of the first); with
// Multistream(false) member by member (Reset then Multistream(false)),
// each ByteReader source left just after the member, and Reset returns
// io.EOF after the last; as the standard library.
func TestReaderMultistream(t *testing.T) {
	var file, want []byte
	var names []string
	for i := range 4 {
		d := streamData(uint64(i), 1000*i)
		h := stdgzip.Header{Name: strings.Repeat("m", i+1)}
		if i == 2 {
			// one of ours, with Extra
			var b bytes.Buffer
			w := pkg.NewWriter(&b)
			w.Name, w.Extra = h.Name, []byte("x")
			w.Write(d)
			w.Close()
			file = append(file, b.Bytes()...)
		} else {
			file = append(file, stdMember(t, h, d)...)
		}
		want = append(want, d...)
		names = append(names, h.Name)
	}
	z0, err := pkg.NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(z0); err != nil || !bytes.Equal(got, want) || z0.Name != names[0] {
		t.Fatalf("multistream: %v, Name %q (want the first member's %q)", err, z0.Name, names[0])
	}
	for _, src := range rdSources {
		got, err, _ := rdReadAll(file, src, 1, newReader)
		if err != io.EOF || !bytes.Equal(got, want) {
			t.Fatalf("%s: multistream: %d bytes, %v", src.name, len(got), err)
		}
		if !src.exact {
			continue
		}
		r, rest := src.make(append(append([]byte(nil), file...), "TAIL"...), 2)
		z, err := pkg.NewReader(r)
		if err != nil {
			t.Fatal(err)
		}
		var all []byte
		for i := 0; ; i++ {
			z.Multistream(false)
			if i < len(names) && z.Name != names[i] {
				t.Fatalf("%s: member %d name %q", src.name, i, z.Name)
			}
			b, err := io.ReadAll(z)
			if err != nil {
				t.Fatalf("%s: member %d: %v", src.name, i, err)
			}
			all = append(all, b...)
			if i == len(names)-1 {
				break
			}
			if err := z.Reset(r); err != nil {
				t.Fatalf("%s: Reset %d: %v", src.name, i, err)
			}
		}
		if !bytes.Equal(all, want) || string(rest()) != "TAIL" {
			t.Fatalf("%s: member by member: equal %v", src.name, bytes.Equal(all, want))
		}
		// Multistream(true) after the end: still io.EOF (sticky).
		z.Multistream(true)
		if n, err := z.Read(make([]byte, 10)); n != 0 || err != io.EOF {
			t.Fatalf("%s: after Multistream(true): %d %v", src.name, n, err)
		}
	}
	// Reset with nothing left: io.EOF, like stdlib.
	z, _ := pkg.NewReader(bytes.NewReader(file))
	if err := z.Reset(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("Reset at the end: %v", err)
	}
}

// Bytes after the last member are read as a header: the standard
// library's error class for each tail.
func TestReaderTrailingGarbage(t *testing.T) {
	m := stdMember(t, stdgzip.Header{}, []byte("abc"))
	for _, tail := range []string{"", "\x00", strings.Repeat("\x00", 20), "x", "\x1f", "\x1f\x8b", "\x1f\x8b\x08\x00",
		"\x1f\x8b\x09\x00\x00\x00\x00\x00\x00\x00", "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\x00", "garbage garbage"} {
		in := append(append([]byte(nil), m...), tail...)
		got, c := rdOutcome(in, newReader)
		sgot, sc := rdOutcome(in, stdNewReader)
		if c != sc || !bytes.Equal(got, sgot) {
			t.Fatalf("tail %q: %q %s, stdlib %q %s", tail, got, c, sgot, sc)
		}
	}
}

// zeroMember is a gzip member of n zero bytes made of stored blocks,
// generated on the fly (n may exceed 4 GiB; nothing is kept in memory),
// with the true CRC-32 and an ISIZE of n+isizeDelta.
type zeroMember struct {
	n, left    int64
	isizeDelta uint32
	crc        uint32
	buf        []byte
	stage      int
}

func newZeroMember(n int64, isizeDelta uint32) *zeroMember {
	return &zeroMember{n: n, left: n, isizeDelta: isizeDelta,
		buf: []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255}}
}

var zeroBlock = make([]byte, 65535)

func (z *zeroMember) Read(p []byte) (int, error) {
	for len(z.buf) == 0 {
		switch {
		case z.left > 0:
			k := min(z.left, 65535)
			z.left -= k
			final := byte(0)
			if z.left == 0 {
				final = 1
			}
			z.buf = append(z.buf[:0], final, byte(k), byte(k>>8), ^byte(k), ^byte(k>>8))
			z.buf = append(z.buf, zeroBlock[:k]...)
			z.crc = crc32.Update(z.crc, crc32.IEEETable, zeroBlock[:k])
		case z.stage == 0:
			z.stage = 1
			z.buf = binary.LittleEndian.AppendUint32(z.buf[:0], z.crc)
			z.buf = binary.LittleEndian.AppendUint32(z.buf, uint32(z.n)+z.isizeDelta)
		default:
			return 0, io.EOF
		}
	}
	n := copy(p, z.buf)
	z.buf = z.buf[n:]
	return n, nil
}

// ISIZE is the size mod 2^32: a member of 4 GiB + 5 bytes has ISIZE 5
// (and ISIZE 4 is ErrChecksum). Not in -short mode (4 GiB is decoded).
func TestReaderISIZEMod32(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("decodes 8 GiB")
	}
	for _, delta := range []uint32{0, ^uint32(0)} {
		z, err := pkg.NewReader(bufio.NewReaderSize(newZeroMember(1<<32+5, delta), 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, z)
		want := error(nil)
		if delta != 0 {
			want = pkg.ErrChecksum
		}
		if n != 1<<32+5 || err != want {
			t.Fatalf("ISIZE delta %d: %d bytes, err %v (want %v)", delta, n, err, want)
		}
	}
}

// A small member through a ByteReader never needs more than the member.
func TestReaderISIZESmall(t *testing.T) {
	for _, delta := range []uint32{0, 1} {
		z, err := pkg.NewReader(newZeroMember(200000, delta))
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, z)
		if n != 200000 || (err == nil) != (delta == 0) {
			t.Fatalf("delta %d: %d bytes, %v", delta, n, err)
		}
	}
}

// A zero Reader can be closed; a later member's header cut inside its
// Extra field is io.ErrUnexpectedEOF, as with the standard library.
func TestReaderZeroAndLaterHeader(t *testing.T) {
	var z pkg.Reader
	if err := z.Close(); err != nil {
		t.Fatalf("Close of a zero Reader: %v", err)
	}
	first := stdMember(t, stdgzip.Header{}, []byte("first"))
	// (No name after the Extra field: a misread Extra length would not be
	// absorbed by the search for the name's NUL.)
	second := stdMember(t, stdgzip.Header{Extra: bytes.Repeat([]byte{1}, 2000)}, []byte("second"))
	for cut := 10; cut < 2020; cut += 7 {
		in := append(append([]byte(nil), first...), second[:cut]...)
		got, c := rdOutcome(in, newReader)
		sgot, sc := rdOutcome(in, stdNewReader)
		if c != sc || !bytes.Equal(got, sgot) {
			t.Fatalf("cut %d: %s, stdlib %s", cut, c, sc)
		}
	}
	got, c := rdOutcome(append(append([]byte(nil), first...), second...), newReader)
	if c != "nil" || string(got) != "firstsecond" {
		t.Fatalf("two members: %q %s", got, c)
	}
}

// The header fields of later members are not kept, so reading a file whose
// later members have a name and Extra allocates nothing once warmed up.
func TestReaderMultistreamAllocs(t *testing.T) {
	file := stdMember(t, stdgzip.Header{}, []byte("first "))
	for range 3 {
		file = append(file, stdMember(t, stdgzip.Header{Name: "later", Comment: "c", Extra: []byte("x")},
			[]byte("later "))...)
	}
	br := bytes.NewReader(file)
	z, err := pkg.NewReader(br)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	allocs := testing.AllocsPerRun(5, func() {
		br.Reset(file)
		if err := z.Reset(br); err != nil {
			t.Fatal(err)
		}
		n := 0
		for {
			k, err := z.Read(buf[n:])
			n += k
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if string(buf[:n]) != "first later later later " {
			t.Fatalf("got %q", buf[:n])
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations", allocs)
	}
}

// After ErrChecksum a bytes.Reader is left just after the trailer; the
// flate.CorruptInputError offset of a corrupt later member counts from its
// DEFLATE data (not from the start of the input); the data of the members
// before it and of its part before the invalid block are delivered.
func TestReaderChecksumPositionAndCorruptOffset(t *testing.T) {
	data := []byte(strings.Repeat("checksum position ", 3000))
	m := stdMember(t, stdgzip.Header{}, data)
	bad := append([]byte(nil), m...)
	bad[len(bad)-5] ^= 1 // CRC-32
	br := bytes.NewReader(append(bad, "TAIL"...))
	z, err := pkg.NewReader(br)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(z); err != pkg.ErrChecksum || !bytes.Equal(got, data) || br.Len() != 4 {
		t.Fatalf("bad CRC: %v, %d bytes, %d left", err, len(got), br.Len())
	}

	// Second member: a valid stored block, then BTYPE 3.
	second := []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3, 0x00, 3, 0, 0xfc, 0xff, 'x', 'y', 'z', 0x07, 0, 0, 0, 0}
	for _, src := range rdSources {
		got, err, _ := rdReadAll(append(append([]byte(nil), m...), second...), src, 1, newReader)
		var ce flate.CorruptInputError
		if !errors.As(err, &ce) || int(ce) <= 0 || int(ce) > len(second)-10 || string(got) != string(data)+"xyz" {
			t.Fatalf("%s: %v (offset %d of %d), %d bytes", src.name, err, ce, len(second)-10, len(got))
		}
	}
}

// A corrupt member that is not the last one: the flate.CorruptInputError
// offset lies inside its DEFLATE data, just after the invalid block header
// (not past the end of the data, whatever was read ahead: its trailer and
// the next member).
func TestReaderCorruptOffsetNonLastMember(t *testing.T) {
	var words []byte
	x := uint32(1)
	for len(words) < 200000 {
		x = x*1664525 + 1013904223
		words = append(words, []string{"alpha ", "beta ", "gamma ", "delta ", "eps ", "zeta ", "eta ", "theta "}[x>>29]...)
		words = append(words, byte('a'+x>>8%26))
	}
	m1 := stdMember(t, stdgzip.Header{}, []byte("first member "))
	m3 := stdMember(t, stdgzip.Header{}, words[:50000])
	k := 0
	for _, n := range []int{0, 1000, 40000, 200000} {
		// The data of m2: n bytes, a sync flush (byte aligned), then an
		// invalid block: BTYPE 3, or a stored block whose NLEN is not
		// ~LEN.
		var body bytes.Buffer
		fw, _ := stdflate.NewWriter(&body, 6)
		fw.Write(words[:n])
		fw.Flush()
		for _, badBlock := range [][]byte{{0x07}, {0x01, 5, 0, 0, 0}} {
			deflateData := append(append([]byte(nil), body.Bytes()...), badBlock...)
			m2 := append([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3}, deflateData...)
			m2 = append(m2, 1, 2, 3, 4, 5, 6, 7, 8) // trailer (not read)
			in := append(append(append([]byte(nil), m1...), m2...), m3...)
			for _, src := range rdSources {
				k++
				got, err, _ := rdReadAll(in, src, uint64(k), newReader)
				var ce flate.CorruptInputError
				if !errors.As(err, &ce) || int(ce) != len(deflateData) || string(got) != "first member "+string(words[:n]) {
					t.Fatalf("%s, %d bytes, bad block %x: %v (offset %d, DEFLATE data %d), %d bytes", src.name, n, badBlock, err, ce, len(deflateData), len(got))
				}
			}
		}
	}
}

// N-NIT-1: after ErrChecksum, Close returns what the standard library's
// returns (nil), also when the Seek back over the bytes read ahead fails;
// Read keeps returning ErrChecksum.
func TestReaderCloseAfterChecksumSeekError(t *testing.T) {
	m := stdMember(t, stdgzip.Header{}, []byte(strings.Repeat("close after checksum ", 500)))
	m[len(m)-5] ^= 1 // CRC-32
	in := append(m, "TAIL"...)
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
