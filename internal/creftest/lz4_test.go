package creftest

import (
	"bytes"
	"strings"
	"testing"
)

func TestLZ4Version(t *testing.T) {
	if v := strings.TrimSpace(string(LZ4Run(t, nil, "version"))); v != "1.11.1" {
		t.Fatalf("lz4ref is lz4 %q, want 1.11.1", v)
	}
}

// TestLZ4RefRoundTrip checks that lz4ref's encoders and decoders agree.
func TestLZ4RefRoundTrip(t *testing.T) {
	in := text(1, 300*kib)
	for _, args := range [][]string{{"fast", "accel=1"}, {"fast", "accel=9"}, {"hc", "level=9"}, {"hc", "level=12"}} {
		blk := LZ4Run(t, in, args...)
		out, err := LZ4Dec(t, blk, "dec", "cap=400000")
		if err != nil || !bytes.Equal(out, in) {
			t.Fatalf("%v: round trip failed: %v", args, err)
		}
	}
	fr := LZ4Run(t, in, "frame", "level=1", "cchk=1", "linked=1", "plan=w100000,w5,f,w207195")
	out, err := LZ4Dec(t, fr, "framedec")
	if err != nil || !bytes.Equal(out, in) {
		t.Fatalf("frame round trip failed: %v", err)
	}
	if _, err := LZ4Dec(t, []byte{0xF0, 1, 2}, "dec", "cap=100"); err == nil {
		t.Fatal("garbage block decoded")
	}
}
