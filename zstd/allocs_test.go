package zstd

import (
	"bytes"
	"io"
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
}
