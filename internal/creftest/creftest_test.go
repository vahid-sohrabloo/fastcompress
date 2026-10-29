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
