package zstd

import (
	"bytes"
	"io"
	"math/bits"
	"runtime/debug"
	"testing"
)

// TestPipelinedStreamAllocs: a pipelined stream (WithDecoderConcurrency
// above 1) read to its end reuses its pipeline's generation (channels,
// framer buffer, goroutine functions) for the next stream, so Reset and
// Read or WriteTo do not allocate in steady state, with and without a
// dictionary (it was 11 allocations and 271 KB per stream). GC is off so
// that the unit and block pools are kept; the two goroutines per stream
// make the runtime allocate when it has no idle one, so the average is
// checked to be below one.
func TestPipelinedStreamAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	dict := asyncTestData(16<<10, 3)
	for _, size := range []int{1 << 10, 300 << 10, 2 << 20} {
		src := asyncTestData(size, 9)
		for _, withDict := range []bool{false, true} {
			eopts := []EOption{WithEncoderConcurrency(1)}
			dopts := []DOption{WithDecoderConcurrency(4)}
			if withDict {
				eopts = append(eopts, WithEncoderDictRaw(77, dict))
				dopts = append(dopts, WithDecoderDictRaw(77, dict))
			}
			e := newTestEncoder(t, eopts...)
			var frame bytes.Buffer
			e.Reset(&frame)
			if _, err := e.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			d, err := NewReader(nil, dopts...)
			if err != nil {
				t.Fatal(err)
			}
			rd := bytes.NewReader(nil)
			var r io.Reader = struct{ io.Reader }{rd} // no Bytes: streamed
			buf := make([]byte, 32<<10)
			var got bytes.Buffer
			got.Grow(size)
			for _, c := range []struct {
				name string
				run  func() error
			}{
				{"Read", func() error {
					for {
						n, err := d.Read(buf)
						got.Write(buf[:n])
						if err == io.EOF {
							return nil
						}
						if err != nil {
							return err
						}
					}
				}},
				{"WriteTo", func() error { _, err := d.WriteTo(&got); return err }},
			} {
				run := func() {
					rd.Reset(frame.Bytes())
					got.Reset()
					if err := d.Reset(r); err != nil {
						t.Fatal(err)
					}
					if err := c.run(); err != nil {
						t.Fatal(err)
					}
				}
				for range 5 {
					run()
				}
				if a := testing.AllocsPerRun(10, run); a >= 1 {
					t.Errorf("%d bytes, dict %v, %s: %v allocations per stream, want 0", size, withDict, c.name, a)
				}
				if !bytes.Equal(got.Bytes(), src) {
					t.Fatalf("%d bytes, dict %v, %s: wrong output", size, withDict, c.name)
				}
			}
			d.Close()
		}
	}
}

// TestStreamAllocs: a stream without a pipeline (WithDecoderConcurrency(1),
// kopia's pooled Decoder) reset to a reader and decoded with WriteTo or
// Read does not allocate in steady state: frames larger than the window
// (the round buffer wraps), with and without a content size, several
// frames in one stream, and small input decoded at Reset (a reader with
// Bytes, WithDecodeBuffersBelow). WriteTo writes from the stream's window
// buffer, so it never allocates Read's output buffer either.
func TestStreamAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	src := asyncTestData(5<<20, 4) // over level 3's 2 MiB window
	e := newTestEncoder(t, WithEncoderConcurrency(1))
	stream := func(parts ...[]byte) []byte {
		var b bytes.Buffer
		for _, p := range parts {
			e.Reset(&b)
			if _, err := e.Write(p); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return b.Bytes()
	}
	small := src[:10<<10]
	for _, c := range []struct {
		name  string
		frame []byte
		want  []byte
		bytes bool // a reader with Bytes and Len: decoded at Reset
	}{
		{"stream", stream(src), src, false},
		{"fcs", e.EncodeAll(src, nil), src, false},
		{"multi", stream(src[:1<<20], src[1<<20:3<<20], src[3<<20:]), src, false},
		{"small", stream(small), small, true},
	} {
		d, err := NewReader(nil, WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		got.Grow(len(c.want))
		buf := make([]byte, 64<<10)
		br := bytes.NewReader(nil)
		var bb bytes.Buffer
		// Read only: io.CopyBuffer with WriteTo and ReadFrom hidden.
		var onlyW io.Writer = struct{ io.Writer }{&got}
		var onlyR io.Reader = struct{ io.Reader }{d}
		for _, m := range []string{"WriteTo", "Read"} {
			run := func() {
				var r io.Reader = br
				if c.bytes {
					bb.Reset()
					bb.Write(c.frame)
					r = &bb
				} else {
					br.Reset(c.frame)
				}
				if err := d.Reset(r); err != nil {
					t.Fatal(err)
				}
				got.Reset()
				var err error
				if m == "WriteTo" {
					_, err = d.WriteTo(&got)
				} else {
					_, err = io.CopyBuffer(onlyW, onlyR, buf)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				run()
			}
			if m == "WriteTo" && d.s.out != nil {
				t.Errorf("%s: WriteTo allocated Read's output buffer", c.name)
			}
			if a := testing.AllocsPerRun(5, run); a != 0 {
				t.Errorf("%s, %s: %v allocations per stream, want 0", c.name, m, a)
			}
			if !bytes.Equal(got.Bytes(), c.want) {
				t.Fatalf("%s, %s: wrong output", c.name, m)
			}
		}
		d.Close()
	}
}

// TestConcurrentBlocksAllocs: with WithConcurrentBlocks, the zstdmt jobs
// start their goroutines without allocating (the job functions are bound
// once; it was one allocation per job), so EncodeAll into a dst with room
// and a reused stream do not allocate in steady state. The scheduling
// decides how many buffers and goroutines are in flight at once (the
// runtime and the buffer pool allocate when that grows), so after a
// warm-up the average is checked to be below one, not zero.
func TestConcurrentBlocksAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	src := asyncTestData(3<<20, 21)
	e := newTestEncoder(t, WithEncoderConcurrency(4), WithConcurrentBlocks(true), WithEncoderLevel(SpeedFastest))
	room := make([]byte, 0, e.MaxEncodedSize(len(src)))
	var sb bytes.Buffer
	sb.Grow(len(src))
	for _, c := range []struct {
		name string
		run  func()
	}{
		{"EncodeAll", func() { _ = e.EncodeAll(src, room) }},
		{"stream", func() {
			sb.Reset()
			e.Reset(&sb)
			if _, err := e.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		for range 20 {
			c.run()
		}
		if a := testing.AllocsPerRun(10, c.run); a >= 1 {
			t.Errorf("%s: %v allocations, want 0 (below 1 on average)", c.name, a)
		}
	}
}

// TestDecodeAllExactResult: DecodeAll(frame, nil) returns exactly the
// content in one allocation, also for a frame without a content size (up
// to newScratchMax; decoded in the context's buffer, then copied), whose
// reservation used to be the result's capacity (128 KiB for 1 KiB).
func TestDecodeAllExactResult(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	e := newTestEncoder(t, WithEncoderConcurrency(1))
	d, err := NewReader(nil, WithDecoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, size := range []int{1, 1000, 64 << 10, 300 << 10, newScratchMax} {
		src := asyncTestData(size, 5)
		var stream bytes.Buffer
		e.Reset(&stream)
		e.Write(src)
		e.Close()
		for _, c := range []struct {
			name  string
			frame []byte
		}{{"content size", e.EncodeAll(src, nil)}, {"no content size", stream.Bytes()}} {
			if c.name == "no content size" && bits.UintSize == 32 {
				continue // the exact-size path is 64-bit only (32-bit has a memory budget)
			}
			var out []byte
			run := func() {
				if out, err = d.DecodeAll(c.frame, nil); err != nil {
					t.Fatal(err)
				}
			}
			run()
			if a := testing.AllocsPerRun(5, run); a != 1 {
				t.Errorf("%d bytes, %s: %v allocations, want 1", size, c.name, a)
			}
			if !bytes.Equal(out, src) || cap(out) != len(out) {
				t.Fatalf("%d bytes, %s: len %d, cap %d", size, c.name, len(out), cap(out))
			}
			// An empty dst with room still decodes in place.
			dst := make([]byte, 0, size)
			if a := testing.AllocsPerRun(5, func() { out, _ = d.DecodeAll(c.frame, dst) }); a != 0 || &out[0] != &dst[:1][0] {
				t.Errorf("%d bytes, %s, dst with room: %v allocations", size, c.name, a)
			}
		}
	}
	// No output: no allocation, and not the context's buffer.
	empty := e.EncodeAll(nil, nil)
	var zero bytes.Buffer
	e.Reset(&zero)
	e.Close()
	for _, f := range [][]byte{empty, zero.Bytes()} {
		out, err := d.DecodeAll(f, nil)
		if err != nil || len(out) != 0 || cap(out) != 0 {
			t.Fatalf("empty frame: %v, len %d, cap %d", err, len(out), cap(out))
		}
	}
	if bits.UintSize == 64 { // (the scratch path is 64-bit only)
		// A fresh decoder keeps the reservation of an empty frame without a
		// content size: repeated calls do not allocate.
		// (magic, a descriptor without a content size, window, one empty last raw block)
		nocs := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x00, 0x01, 0x00, 0x00}
		fresh, err := NewReader(nil, WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		if _, err := fresh.DecodeAll(nocs, nil); err != nil {
			t.Fatal(err)
		}
		if a := testing.AllocsPerRun(10, func() {
			if out, err := fresh.DecodeAll(nocs, nil); err != nil || cap(out) != 0 {
				t.Fatalf("empty frame: %v, cap %d", err, cap(out))
			}
		}); a != 0 {
			t.Errorf("empty frame on a fresh decoder: %v allocations per call, want 0", a)
		}
	}
}
