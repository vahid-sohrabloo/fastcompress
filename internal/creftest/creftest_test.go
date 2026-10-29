package creftest

import (
	"bytes"
	"sort"
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
	if !strings.HasPrefix(seqs, "block\n") || !strings.Contains(seqs, "\nlast lit=") || !strings.Contains(seqs, "\nlits=") {
		t.Fatalf("unexpected seqs output: %.200s", seqs)
	}

	if out := Run(t, in[:20000], "fse", "-tablelog", "11"); len(out) < 2 || len(out) >= 20000 {
		t.Fatalf("fse output size %d", len(out))
	}
	if out := Run(t, bytes.Repeat([]byte{7}, 100), "huf4x"); !bytes.Equal(out, []byte{7}) {
		t.Fatalf("huf4x RLE output %v", out)
	}
}
