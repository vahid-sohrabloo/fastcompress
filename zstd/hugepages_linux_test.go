//go:build linux

package zstd

import (
	"bytes"
	"runtime"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// hugeTestInput is every creftest input concatenated (about 3.5 MiB), large
// enough for 4 MiB and 16 MiB hash tables at levels 7 and 11.
func hugeTestInput(t *testing.T) []byte {
	names, inputs := sortedInputs(t)
	var b []byte
	for _, n := range names {
		b = append(b, inputs[n]...)
	}
	return b
}

// TestHugePagesOutput requires WithHugePages(true) to produce the same
// EncodeAll and streaming output as the default at every EncoderLevel, and
// the large tables to come from 2 MiB-aligned huge-page mappings.
func TestHugePagesOutput(t *testing.T) {
	src := hugeTestInput(t)
	for _, lv := range encoderLevels {
		off := newTestEncoder(t, WithEncoderLevel(lv.l))
		on := newTestEncoder(t, WithEncoderLevel(lv.l), WithHugePages(true))
		want := off.EncodeAll(src, nil)
		for i := range 2 { // the second call reuses the pooled context and its tables
			if got := on.EncodeAll(src, nil); !bytes.Equal(got, want) {
				t.Fatalf("level %d call %d: EncodeAll output differs with huge pages", lv.c, i)
			}
		}
		var sOff, sOn bytes.Buffer
		for _, s := range []struct {
			e *Encoder
			b *bytes.Buffer
		}{{off, &sOff}, {on, &sOn}} {
			s.e.Reset(s.b)
			if _, err := s.e.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := s.e.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(sOn.Bytes(), sOff.Bytes()) {
			t.Fatalf("level %d: stream output differs with huge pages", lv.c)
		}

		// The tables of 1 MiB or more are the mappings' aligned starts.
		c := on.newCCtx()
		if _, err := c.compress2(nil, src); err != nil {
			t.Fatal(err)
		}
		ms := &c.blockState.matchState
		checked := 0
		for slot, tbl := range map[int][]byte{
			hugeHash:  u32Bytes(ms.hashTable),
			hugeChain: u32Bytes(ms.chainTable),
			hugeHash3: u32Bytes(ms.hashTable3),
			hugeTag:   ms.tagTable,
		} {
			if len(tbl) < hugeMinBytes {
				continue
			}
			checked++
			if ms.huge.mem[slot] == nil || uintptr(unsafe.Pointer(&tbl[0]))%hugePageSize != 0 {
				t.Fatalf("level %d: table slot %d (%d bytes) is not a huge-page mapping", lv.c, slot, len(tbl))
			}
		}
		if lv.c >= 7 && checked == 0 {
			t.Fatalf("level %d: no table of %d bytes or more", lv.c, hugeMinBytes)
		}
		runtime.KeepAlive(c)
	}
}

func u32Bytes(t []uint32) []byte {
	if len(t) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&t[0])), 4*len(t))
}

// TestHugePagesReleased checks that a table that outgrows its mapping
// unmaps the old one, and that dropped encoders' mappings are unmapped by
// the GC cleanup.
func TestHugePagesReleased(t *testing.T) {
	src := hugeTestInput(t)
	base := hugeLive.Load()

	c := newCCtx()
	c.hugePages = true
	for _, level := range []int{7, 11, 12} { // hashLog 20, 22, 23: each grows the hash table
		c.setLevel(level)
		if _, err := c.compress2(nil, src); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range c.blockState.matchState.huge.mem {
			if m != nil {
				n++
			}
		}
		if live := hugeLive.Load() - base; live != int64(n) {
			t.Fatalf("level %d: %d live mappings, want %d (one per huge table)", level, live, n)
		}
	}
	runtime.KeepAlive(c)
	c = nil

	for range 4 {
		e := newTestEncoder(t, WithEncoderLevel(SpeedBestCompression), WithHugePages(true))
		frame := e.EncodeAll(src, nil)
		if !slices.Equal(creftest.DecodeC(t, frame), src) {
			t.Fatal("round trip")
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for hugeLive.Load() != base {
		if time.Now().After(deadline) {
			t.Fatalf("%d huge-page mappings still live after the encoders were dropped", hugeLive.Load()-base)
		}
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}
