package lz4

import (
	"bytes"
	"io"
	"runtime/debug"
	"testing"
)

// allocsData returns n bytes of text with incompressible runs.
func allocsData(n int) []byte {
	b := randText(11, n)
	for i := 0; i+300 < n; i += 5000 {
		copy(b[i:], randText(uint64(i), 300)) // other words, other matches
		for j := range 100 {
			b[i+j] = byte(i*7 + j*13)
		}
	}
	return b
}

// TestBlockAllocs: the block compressors, the optimal parser (levels 10
// to 12) included, do not allocate once their context exists (the parser
// passed its arguments through a func value: one allocation per call
// before Go 1.26).
func TestBlockAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	src := allocsData(256 << 10)
	dst := make([]byte, CompressBound(len(src)))
	out := make([]byte, len(src))
	s := NewStreamHC(LevelHCMax)
	for _, level := range []int{2, 4, 9, 10, 11, 12} {
		run := func() { s.Compress(dst, src, level) }
		if a := testing.AllocsPerRun(3, run); a != 0 {
			t.Errorf("StreamHC.Compress level %d: %v allocations", level, a)
		}
	}
	n := CompressDefault(dst, src)
	if a := testing.AllocsPerRun(5, func() { CompressDefault(dst, src) }); a != 0 {
		t.Errorf("CompressDefault: %v allocations", a)
	}
	if a := testing.AllocsPerRun(5, func() { DecompressSafe(out, dst[:n]) }); a != 0 {
		t.Errorf("DecompressSafe: %v allocations", a)
	}
}

// TestFrameAllocs: CompressFrame takes a pooled context (it allocated
// the context and its tables, 18 KB fast and 270 KB HC, per call); a
// FrameCompressor, a Writer and a Reader reused for frame after frame do
// not allocate (the Writer boxed its closed error on every Close, the
// Reader's block header escaped once per block); AppendFrame and
// AppendDecompressFrame with a nil dst make one allocation of exactly the
// result. GC is off so that the pools are kept.
func TestFrameAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	src := allocsData(300 << 10)
	dict := allocsData(64 << 10)[:40<<10]
	cdict := NewCDict(dict)
	for _, p := range []Preferences{
		{},
		{FrameInfo: FrameInfo{BlockIndependent: true, ContentChecksum: true, BlockChecksum: true}},
		{FrameInfo: FrameInfo{BlockSizeID: Block4MB, ContentChecksum: true}, CompressionLevel: 9},
		{FrameInfo: FrameInfo{ContentSize: 300 << 10}, CompressionLevel: 11, AutoFlush: true},
	} {
		zero := func(name string, run func()) {
			t.Helper()
			for range 3 {
				run()
			}
			if a := testing.AllocsPerRun(5, run); a != 0 {
				t.Errorf("%+v: %s: %v allocations, want 0", p, name, a)
			}
		}
		fdst := make([]byte, CompressFrameBound(len(src), &p))
		n, err := CompressFrame(fdst, src, &p)
		if err != nil {
			t.Fatal(err)
		}
		frame := append([]byte(nil), fdst[:n]...)
		zero("CompressFrame", func() {
			if m, err := CompressFrame(fdst, src, &p); err != nil || m != n {
				t.Fatalf("CompressFrame: %d bytes, %v", m, err)
			}
		})
		zero("CompressFrameUsingCDict", func() { _, _ = CompressFrameUsingCDict(fdst, src, cdict, &p) })
		var fc FrameCompressor
		zero("FrameCompressor", func() { _, _ = fc.CompressFrame(fdst, src, &p) })
		out := make([]byte, 0, len(src))
		zero("AppendDecompressFrame with room", func() { out, _ = AppendDecompressFrame(out[:0], frame, nil) })

		var sb bytes.Buffer
		sb.Grow(2 * len(src))
		for _, w := range []*Writer{NewWriter(&sb, WithPreferences(p)), NewWriter(&sb, WithPreferences(p), WithDict(dict))} {
			zero("Writer", func() {
				sb.Reset()
				w.Reset(&sb)
				if _, err := w.Write(src[:1000]); err != nil {
					t.Fatal(err)
				}
				if err := w.Flush(); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(src[1000:]); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
		rd := bytes.NewReader(nil)
		var hidden io.Reader = struct{ io.Reader }{rd}
		r := NewReader(hidden)
		buf := make([]byte, 16<<10)
		var got bytes.Buffer
		got.Grow(len(src))
		zero("Reader.Read", func() {
			rd.Reset(frame)
			r.Reset(hidden)
			got.Reset()
			for {
				k, err := r.Read(buf)
				got.Write(buf[:k])
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		})
		if !bytes.Equal(got.Bytes(), src) {
			t.Fatalf("%+v: Reader: wrong output", p)
		}
		zero("Reader.WriteTo", func() {
			rd.Reset(frame)
			r.Reset(hidden)
			if _, err := r.WriteTo(io.Discard); err != nil {
				t.Fatal(err)
			}
		})

		// A nil dst: one allocation of exactly the result.
		for _, c := range []struct {
			name string
			run  func() ([]byte, error)
			want []byte
		}{
			{"AppendFrame", func() ([]byte, error) { return AppendFrame(nil, src, &p) }, frame},
			{"AppendDecompressFrame", func() ([]byte, error) { return AppendDecompressFrame(nil, frame, nil) }, src},
		} {
			var res []byte
			run := func() {
				if res, err = c.run(); err != nil {
					t.Fatal(err)
				}
			}
			run()
			if a := testing.AllocsPerRun(5, run); a != 1 {
				t.Errorf("%+v: %s(nil): %v allocations, want 1", p, c.name, a)
			}
			if !bytes.Equal(res, c.want) || cap(res) != len(res) {
				t.Errorf("%+v: %s(nil): len %d cap %d, want the %d bytes", p, c.name, len(res), cap(res), len(c.want))
			}
		}
	}
}

// TestFrameMTAllocs: the jobs of a multi-threaded frame keep their done
// channel and goroutine function (they were two allocations per job, so
// per Flush of a Writer). The runtime and the job pool allocate when the
// scheduling puts more jobs or goroutines in flight than before, so after
// a warm-up the average per frame is checked to be below one.
func TestFrameMTAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under the race detector")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	src := allocsData(4 << 20)
	for _, p := range []Preferences{
		{FrameInfo: FrameInfo{BlockIndependent: true}, NbWorkers: 4},
		{NbWorkers: 4},
	} {
		check := func(name string, run func()) {
			t.Helper()
			for range 10 {
				run()
			}
			if a := testing.AllocsPerRun(10, run); a >= 1 {
				t.Errorf("%+v: %s: %v allocations, want 0 (below 1 on average)", p, name, a)
			}
		}
		fdst := make([]byte, CompressFrameBound(len(src), &p))
		check("CompressFrame", func() { _, _ = CompressFrame(fdst, src, &p) })
		var sb bytes.Buffer
		sb.Grow(len(src))
		w := NewWriter(&sb, WithPreferences(p))
		check("Writer with a Flush per 64 KB", func() {
			sb.Reset()
			w.Reset(&sb)
			for q := src[:1<<20]; len(q) > 0; q = q[64<<10:] {
				if _, err := w.Write(q[:64<<10]); err != nil {
					t.Fatal(err)
				}
				if err := w.Flush(); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
