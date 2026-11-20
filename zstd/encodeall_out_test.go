package zstd

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
)

// TestEncodeAllPooledOutput: with a dst that has no room for compressBound
// (nil, or too small), EncodeAll compresses into a pooled scratch buffer
// and appends only the frame. The frame equals the one compressed straight
// into a dst with room, dst's contents are kept, and the result has no
// worst-case slack.
func TestEncodeAllPooledOutput(t *testing.T) {
	text := malText(2 << 20)
	d := newTestDecoder(t)
	for _, lv := range []EncoderLevel{SpeedFastest, SpeedDefault, SpeedBestCompression} {
		e := newTestEncoder(t, WithEncoderLevel(lv), WithEncoderConcurrency(2))
		for _, n := range []int{0, 1, 7, 100, 4095, 4096, 65535, 65536, 300000, 2 << 20} {
			src := text[:n]
			want := e.EncodeAll(src, make([]byte, 0, compressBound(n)))
			got := e.EncodeAll(src, nil)
			if !bytes.Equal(got, want) {
				t.Fatalf("level %v, %d bytes: nil dst gives another frame", lv, n)
			}
			if limit := len(got) + len(got)/4 + 64; cap(got) > limit {
				t.Errorf("level %v, %d bytes: nil dst result has capacity %d for %d bytes", lv, n, cap(got), len(got))
			}
			prefix := []byte("prefix")
			got = e.EncodeAll(src, append(make([]byte, 0, 8), prefix...))
			if !bytes.HasPrefix(got, prefix) || !bytes.Equal(got[len(prefix):], want) {
				t.Fatalf("level %v, %d bytes: a short dst with a prefix gives another result", lv, n)
			}
			if plain, err := d.DecodeAll(want, nil); err != nil || !bytes.Equal(plain, src) {
				t.Fatalf("level %v, %d bytes: round trip: %v", lv, n, err)
			}
		}
	}
}

// TestEncodeAllPooledOutputAllocs: a call with a nil dst allocates its
// result, about the frame's size, and nothing else once the scratch pool
// is warm.
func TestEncodeAllPooledOutputAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	text := malText(1 << 20)
	e := newTestEncoder(t, WithEncoderLevel(SpeedDefault), WithEncoderConcurrency(1))
	for _, n := range []int{1000, 64 << 10, 1 << 20} {
		src := text[:n]
		out := e.EncodeAll(src, nil) // warm the context and the pool
		if a := testing.AllocsPerRun(5, func() { out = e.EncodeAll(src, nil) }); a != 1 {
			t.Errorf("%d bytes: %v allocations per call, want 1 (the result)", n, a)
		}
		// The best of several rounds: a garbage collection empties the
		// scratch pool (sync.Pool), and the next call then allocates one
		// scratch buffer again.
		perCall := 1 << 40
		const runs = 20
		for range 5 {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for range runs {
				out = e.EncodeAll(src, nil)
			}
			runtime.ReadMemStats(&after)
			perCall = min(perCall, int((after.TotalAlloc-before.TotalAlloc)/runs))
		}
		if limit := len(out) + len(out)/4 + 512; perCall > limit {
			t.Errorf("%d bytes: %d bytes allocated per call for a %d byte frame", n, perCall, len(out))
		}
	}
}

// TestEncodeAllPooledOutputConcurrent: concurrent calls of different sizes
// share the scratch pools without mixing their output.
func TestEncodeAllPooledOutputConcurrent(t *testing.T) {
	text := malText(1 << 20)
	e := newTestEncoder(t, WithEncoderLevel(SpeedDefault), WithEncoderConcurrency(4))
	sizes := []int{10, 5000, 70000, 300000, 1 << 20, 123, 40000, 700000}
	want := make([][]byte, len(sizes))
	for i, n := range sizes {
		want[i] = e.EncodeAll(text[:n], make([]byte, 0, compressBound(n)))
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for round := range 30 {
				i := (g + round) % len(sizes)
				if got := e.EncodeAll(text[:sizes[i]], nil); !bytes.Equal(got, want[i]) {
					t.Errorf("goroutine %d, %d bytes: output differs", g, sizes[i])
					return
				}
			}
		})
	}
	wg.Wait()
}
