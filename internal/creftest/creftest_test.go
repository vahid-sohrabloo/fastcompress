package creftest

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestCrefRoundTrip(t *testing.T) {
	names := map[string][]byte{}
	for k, v := range Inputs(t) {
		names[k] = v
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		in := names[name]
		t.Run(name, func(t *testing.T) {
			got := DecodeC(t, Run(t, in, "compress", "-level", "3"))
			if !bytes.Equal(got, in) {
				t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(in))
			}
		})
	}
}

func TestCrefModes(t *testing.T) {
	in := text(5, 300*kib)
	check := func(name string, frame []byte) {
		t.Helper()
		if got := DecodeC(t, frame); !bytes.Equal(got, in) {
			t.Fatalf("%s: round trip mismatch", name)
		}
	}
	known := Run(t, in, "compress", "-level", "3", "-checksum", "1")
	unknown := Run(t, in, "compress", "-level", "3", "-checksum", "1", "-pledged", "0")
	check("pledged", known)
	check("unpledged", unknown)
	// Frame header descriptor: bits 7-6 = Frame_Content_Size_flag, bit 5 =
	// Single_Segment. Unknown size => no FCS field (flag 0, not single segment).
	if unknown[4]&0xC0 != 0 || unknown[4]&0x20 != 0 {
		t.Fatalf("-pledged 0 frame header descriptor %#x declares a content size", unknown[4])
	}
	if known[4]&0xC0 == 0 && known[4]&0x20 == 0 {
		t.Fatalf("-pledged 1 frame header descriptor %#x has no content size", known[4])
	}
	check("stream", Run(t, in, "stream", "-level", "3", "-checksum", "-chunks", "1000,99000,207200", "-flush-after", "0,1"))
	check("window-log", Run(t, in, "compress", "-level", "3", "-window-log", "10"))

	if got := strings.TrimSpace(string(Run(t, []byte("abc"), "xxh64", "-seed", "0"))); got != "44bc2cf5ad770999" {
		t.Fatalf("xxh64(abc)=%s", got)
	}

	seqs := string(Run(t, in, "seqs", "-level", "3"))
	if !strings.HasPrefix(seqs, "block type=") || !strings.Contains(seqs, "\nlonglen type=") || !strings.Contains(seqs, "\nlits=") {
		t.Fatalf("unexpected seqs output: %.200s", seqs)
	}

	if out := Run(t, in[:20000], "fse", "-tablelog", "11"); len(out) < 2 || len(out) >= 20000 {
		t.Fatalf("fse output size %d", len(out))
	}
	if out := Run(t, bytes.Repeat([]byte{7}, 100), "huf4x"); !bytes.Equal(out, []byte{7}) {
		t.Fatalf("huf4x RLE output %v", out)
	}
}

// frameBlocks returns the number of blocks in a single zstd frame.
func frameBlocks(t *testing.T, f []byte) int {
	t.Helper()
	if len(f) < 6 || f[0] != 0x28 || f[1] != 0xb5 || f[2] != 0x2f || f[3] != 0xfd {
		t.Fatal("not a zstd frame")
	}
	fhd := f[4]
	pos := 5
	single := fhd&0x20 != 0
	if !single {
		pos++
	}
	pos += [4]int{0, 1, 2, 4}[fhd&3]
	fcs := fhd >> 6
	switch {
	case fcs == 0 && single:
		pos++
	case fcs == 1:
		pos += 2
	case fcs == 2:
		pos += 4
	case fcs == 3:
		pos += 8
	}
	n := 0
	for {
		h := uint32(f[pos]) | uint32(f[pos+1])<<8 | uint32(f[pos+2])<<16
		pos += 3
		n++
		size := int(h >> 3)
		if (h>>1)&3 != 1 { // not RLE: content is size bytes
			pos += size
		} else {
			pos++
		}
		if h&1 != 0 {
			return n
		}
	}
}

func countPrefix(s, prefix string) int { return strings.Count("\n"+s, "\n"+prefix) }

func TestCrefSeqs(t *testing.T) {
	dir := t.TempDir()
	inputs := Inputs(t)
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	check := func(t *testing.T, name, level string) {
		in := inputs[name]
		fout := filepath.Join(dir, "frame")
		dump := string(Run(t, in, "seqs", "-level", level, "-frame-out", fout))
		frame, err := os.ReadFile(fout)
		if err != nil {
			t.Fatal(err)
		}
		if want := Run(t, in, "compress", "-level", level); !bytes.Equal(frame, want) {
			t.Fatalf("seqs frame differs from compress frame (%d vs %d bytes)", len(frame), len(want))
		}
		if got, want := countPrefix(dump, "block "), frameBlocks(t, frame); got != want {
			t.Fatalf("seqs dumped %d blocks, frame has %d", got, want)
		}
		if got := countPrefix(dump, "lits="); got != countPrefix(dump, "block ") {
			t.Fatalf("lits lines %d != blocks", got)
		}
	}
	// Every Inputs entry, at level 3 (and 1 for small ones; the big ones get all levels below).
	for _, name := range names {
		t.Run(name, func(t *testing.T) { check(t, name, "3") })
	}
	if testing.Short() {
		return
	}
	for _, name := range []string{"silesia/mozilla", "silesia/samba", "silesia/ooffice", "text4MiB"} {
		if _, ok := inputs[name]; !ok {
			continue
		}
		for _, lvl := range []string{"1", "3", "7", "11"} {
			t.Run(name+"/L"+lvl, func(t *testing.T) { check(t, name, lvl) })
		}
	}
}

func TestCrefUnpledgedMatchesSingleChunkStream(t *testing.T) {
	in := text(77, 3*mib+12345)
	a := Run(t, in, "compress", "-level", "3", "-pledged", "0")
	b := Run(t, in, "stream", "-level", "3", "-chunks", strconv.Itoa(len(in)))
	if !bytes.Equal(a, b) {
		t.Fatalf("compress -pledged 0 (%d bytes) != stream single chunk (%d bytes)", len(a), len(b))
	}
}

func TestCrefDecodeModes(t *testing.T) {
	for _, name := range FrameNames(t) {
		t.Run(name, func(t *testing.T) {
			f := LoadFrame(t, name)
			check := func(mode string, got []byte, e *CError) {
				t.Helper()
				if f.CErr != "" {
					if e == nil || e.Name != f.CErr {
						t.Fatalf("%s: error %v, want %q", mode, e, f.CErr)
					}
					return
				}
				if e != nil {
					t.Fatalf("%s: %v", mode, e)
				}
				if !bytes.Equal(got, f.Want) {
					t.Fatalf("%s: got %d bytes, want %d", mode, len(got), len(f.Want))
				}
			}
			got, e := DecodeCRef(t, f.Data, f.Args...)
			check("decompress", got, e)
			got, e = DStreamCRef(t, f.Data, append([]string{"-chunk-size", "4096", "-out-size", "131072"}, f.Args...)...)
			check("dstream 4096/131072", got, e)
			// One byte in, one byte out costs a C call per byte: only up to 8 MiB.
			if len(f.Data)+len(f.Want) <= 8*mib {
				got, e = DStreamCRef(t, f.Data, append([]string{"-chunk-size", "1", "-out-size", "1"}, f.Args...)...)
				check("dstream 1/1", got, e)
			}
		})
	}
}

// TestCrefDecodeShortInput pins how decompress and dstream differ on empty
// and truncated input (documented in cref.c and on DecodeCRef/DStreamCRef).
func TestCrefDecodeShortInput(t *testing.T) {
	in := text(12, 20000)
	frame := Run(t, in, "compress", "-level", "3", "-checksum", "1")
	ds := func(b []byte) ([]byte, *CError) { return DStreamCRef(t, b, "-chunk-size", "4096", "-out-size", "4096") }
	for _, c := range []struct {
		name string
		in   []byte
	}{{"empty", []byte{}}, {"nil", nil}} {
		if got, e := DecodeCRef(t, c.in); e != nil || len(got) != 0 {
			t.Errorf("%s: decompress %v, %d bytes", c.name, e, len(got))
		}
		if got, e := ds(c.in); e != nil || len(got) != 0 {
			t.Errorf("%s: dstream %v, %d bytes", c.name, e, len(got))
		}
	}
	for _, n := range []int{1, 3, 4, 100, len(frame) - 4, len(frame) - 1} {
		cut := frame[:n]
		// C reports a frame missing all or part of its checksum as checksum_wrong.
		want := &CError{72, "Src size is incorrect"}
		if n >= len(frame)-4 {
			want = &CError{22, "Restored data doesn't match checksum"}
		}
		if _, e := DecodeCRef(t, cut); e == nil || *e != *want {
			t.Errorf("%d bytes: decompress %v, want %v", n, e, want)
		}
		got, e := ds(cut)
		if e == nil || e.Code != -1 || e.Name != "incomplete" {
			t.Errorf("%d bytes: dstream %v, want incomplete", n, e)
		}
		if !bytes.HasPrefix(in, got) {
			t.Errorf("%d bytes: dstream output is not a prefix of the content", n)
		}
	}
}

func TestCrefDecodeErrors(t *testing.T) {
	in := text(8, 50000)
	frame := Run(t, in, "compress", "-level", "3", "-checksum", "1")

	h := FHeaderCRef(t, frame)
	for k, want := range map[string]string{
		"needed": "0", "frameContentSize": "50000", "frameType": "0", "dictID": "0",
		"checksumFlag": "1", "compressedSize": strconv.Itoa(len(frame)),
		"decompressBound": "50000", "lastBlock": "1",
	} {
		if h[k] != want {
			t.Errorf("fheader %s=%q, want %q (all: %v)", k, h[k], want, h)
		}
	}
	if h := FHeaderCRef(t, frame[:5]); h["needed"] == "0" || h["needed"] == "" {
		t.Errorf("fheader on 5 bytes: needed=%q", h["needed"])
	}
	if h := FHeaderCRef(t, []byte("not a zstd frame")); h["err"] == "" || h["errName"] == "" {
		t.Errorf("fheader on garbage: %v", h)
	}

	// Checksum mismatch: flip the last byte (the checksum's top byte).
	bad := bytes.Clone(frame)
	bad[len(bad)-1] ^= 1
	if _, e := DecodeCRef(t, bad); e == nil || e.Name != "Restored data doesn't match checksum" {
		t.Errorf("checksum flip: %v", e)
	}
	if got, e := DecodeCRef(t, bad, "-ignore-checksum", "1"); e != nil || !bytes.Equal(got, in) {
		t.Errorf("checksum flip with -ignore-checksum 1: %v", e)
	}
	// C checks the checksum before it flushes the last block's output.
	got, e := DStreamCRef(t, bad, "-chunk-size", "1000", "-out-size", "1000")
	if e == nil || e.Code != 22 || !bytes.HasPrefix(in, got) {
		t.Errorf("dstream checksum flip: %v, %d bytes", e, len(got))
	}
	if _, e := DStreamCRef(t, frame[:len(frame)-10], "-chunks", strconv.Itoa(len(frame)-10), "-out-size", "4096"); e == nil || e.Code != -1 {
		t.Errorf("dstream truncated: %v", e)
	}
	big := Run(t, text(9, 300*kib), "compress", "-level", "3", "-pledged", "0", "-window-log", "18")
	// ZSTD_d_windowLogMax limits only streaming decompression.
	if _, e := DStreamCRef(t, big, "-max-window-log", "17", "-chunk-size", "4096", "-out-size", "4096"); e == nil || e.Name != "Frame requires too much memory for decoding" {
		t.Errorf("dstream -max-window-log 17: %v", e)
	}
	if got, e := DecodeCRef(t, big, "-max-window-log", "17"); e != nil || len(got) != 300*kib {
		t.Errorf("decompress -max-window-log 17: %v", e)
	}

	sk := Run(t, []byte("payload"), "skippable", "-variant", "5")
	if want := append([]byte{0x55, 0x2a, 0x4d, 0x18, 7, 0, 0, 0}, "payload"...); !bytes.Equal(sk, want) {
		t.Errorf("skippable: % x", sk)
	}

	content := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(content, text(10, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	var samples []byte
	var sizes []string
	for i := range 50 {
		s := text(uint64(100+i), 1000+i*10)
		samples = append(samples, s...)
		sizes = append(sizes, strconv.Itoa(len(s)))
	}
	d := Run(t, samples, "finalize-dict", "-level", "3", "-dict-id", "1234", "-content", content, "-sizes", strings.Join(sizes, ","))
	if len(d) < 4096+8 || !bytes.Equal(d[:8], []byte{0x37, 0xa4, 0x30, 0xec, 0xd2, 0x04, 0, 0}) {
		t.Errorf("finalize-dict: %d bytes, header % x", len(d), d[:min(8, len(d))])
	}
}
