//go:build linux

package zstd

import (
	"bytes"
	"runtime"
	"slices"
	"sync"
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
// the GC cleanup. It follows only the mappings made while it runs (through
// hugeTrace), so other tests' encoders, whose cleanups may run meanwhile,
// do not affect it.
func TestHugePagesReleased(t *testing.T) {
	src := hugeTestInput(t)
	// This test's mappings, in order; an address can be mapped again after
	// an unmap, so a mapping is identified by its record, not its address.
	type mapping struct {
		start *byte
		live  bool
	}
	var mu sync.Mutex
	var maps []*mapping
	trace := func(mem []byte, mapped bool) {
		mu.Lock()
		defer mu.Unlock()
		if p := unsafe.SliceData(mem); mapped {
			maps = append(maps, &mapping{p, true})
		} else {
			for _, m := range maps {
				if m.start == p && m.live {
					m.live = false
				}
			}
		}
	}
	hugeTrace.Store(&trace)
	defer hugeTrace.Store(nil)
	current := func(m []byte) *mapping { // the live record of mapping m
		mu.Lock()
		defer mu.Unlock()
		for _, r := range maps {
			if r.start == unsafe.SliceData(m) && r.live {
				return r
			}
		}
		return nil
	}
	isLive := func(r *mapping) bool {
		mu.Lock()
		defer mu.Unlock()
		return r.live
	}
	nLive := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, r := range maps {
			if r.live {
				n++
			}
		}
		return n
	}

	c := newCCtx()
	c.hugePages = true
	// oldHash is the previous level's hash table mapping.
	var oldHash *mapping
	for _, level := range []int{7, 11, 12} { // hashLog 20, 22, 23: each grows the hash table
		c.setLevel(level)
		if _, err := c.compress2(nil, src); err != nil {
			t.Fatal(err)
		}
		if oldHash != nil && isLive(oldHash) {
			t.Fatalf("level %d: the replaced hash table mapping is still mapped", level)
		}
		huge := &c.blockState.matchState.huge.mem
		n := 0
		for slot, m := range huge {
			if m != nil {
				n++
				if current(m) == nil {
					t.Fatalf("level %d: the mapping of table slot %d is not live", level, slot)
				}
			}
		}
		if huge[hugeHash] == nil || nLive() != n {
			t.Fatalf("level %d: %d live mappings, want %d (one per huge table, including the hash table)", level, nLive(), n)
		}
		oldHash = current(huge[hugeHash])
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
	for nLive() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d huge-page mappings still live after the encoders were dropped", nLive())
		}
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}
