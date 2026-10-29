package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

func newTestEncoder(t testing.TB, opts ...EOption) *Encoder {
	t.Helper()
	e, err := NewWriter(nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// crefLevel1 returns C's ZSTD_compress2 frame for in at level 1 with the
// content checksum on (klauspost's default) plus extra cref options.
func crefLevel1(t testing.TB, in []byte, extra ...string) []byte {
	t.Helper()
	args := append([]string{"compress", "-level", "1", "-checksum", "1", "-pledged", "1"}, extra...)
	return creftest.Run(t, in, args...)
}

// TestEncodeAllCref requires EncodeAll at SpeedFastest with otherwise default
// options to equal C's ZSTD_compress2 at level 1 with checksum on, for every
// input. WithZeroFrames makes the empty input produce C's empty frame.
func TestEncodeAllCref(t *testing.T) {
	names, inputs := sortedInputs(t)
	e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithZeroFrames(true))
	for _, name := range names {
		in := inputs[name]
		want := crefLevel1(t, in)
		got := e.EncodeAll(in, nil)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: EncodeAll differs from cref (got %d bytes, want %d)", name, len(got), len(want))
		}
	}
}

// TestEncodeAllAppends checks EncodeAll appends to dst and leaves its
// existing content alone.
func TestEncodeAllAppends(t *testing.T) {
	in := creftest.Inputs(t)["text16KiB"]
	want := crefLevel1(t, in)
	prefix := []byte("prefix")
	dst := append(make([]byte, 0, 4), prefix...)
	got := newTestEncoder(t, WithEncoderLevel(SpeedFastest)).EncodeAll(in, dst)
	if !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], want) {
		t.Fatal("EncodeAll did not append C's frame to dst")
	}
}

// TestEncodeAllEmpty: without WithZeroFrames empty input adds nothing.
func TestEncodeAllEmpty(t *testing.T) {
	e := newTestEncoder(t, WithEncoderLevel(SpeedFastest))
	if got := e.EncodeAll(nil, nil); len(got) != 0 {
		t.Fatalf("EncodeAll(empty) = %x, want nothing", got)
	}
	dst := []byte("abc")
	if got := e.EncodeAll([]byte{}, dst); !bytes.Equal(got, dst) {
		t.Fatalf("EncodeAll(empty, dst) = %q, want %q", got, dst)
	}
}

// TestEncodeAllOptionsCref covers every option with a C equivalent (and the
// accepted-but-output-neutral ones) against cref with the mapped parameters.
func TestEncodeAllOptionsCref(t *testing.T) {
	cases := []struct {
		name  string
		opts  []EOption
		cargs []string
	}{
		{"crc-off", []EOption{WithEncoderCRC(false)}, []string{"-checksum", "0"}},
		{"window-1KiB", []EOption{WithWindowSize(MinWindowSize)}, []string{"-window-log", "10"}},
		{"window-128KiB", []EOption{WithWindowSize(128 << 10)}, []string{"-window-log", "17"}},
		{"window-max", []EOption{WithWindowSize(MaxWindowSize)}, []string{"-window-log", "29"}},
		{"no-entropy", []EOption{WithNoEntropyCompression(true)}, []string{"-lit-mode", "2"}},
		{"all-lit-entropy", []EOption{WithAllLitEntropyCompression(true)}, []string{"-lit-mode", "1"}},
		{"all-lit-entropy-off", []EOption{WithAllLitEntropyCompression(false)}, nil},
		{"no-entropy-wins", []EOption{WithAllLitEntropyCompression(true), WithNoEntropyCompression(true)}, []string{"-lit-mode", "2"}},
		{"neutral", []EOption{WithEncoderConcurrency(1), WithLowerEncoderMem(true), WithSingleSegment(false)}, nil},
	}
	names, inputs := sortedInputs(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEncoder(t, append([]EOption{WithEncoderLevel(SpeedFastest)}, c.opts...)...)
			for _, name := range names {
				in := inputs[name]
				if len(in) == 0 {
					continue
				}
				want := crefLevel1(t, in, c.cargs...)
				if got := e.EncodeAll(in, nil); !bytes.Equal(got, want) {
					t.Errorf("%s: differs from cref %v (got %d bytes, want %d)", name, c.cargs, len(got), len(want))
				}
			}
		})
	}
}

// TestEncodeAllConcurrent runs EncodeAll on one Encoder from 8 goroutines over
// all inputs; every output must equal cref's. Run it under -race.
func TestEncodeAllConcurrent(t *testing.T) {
	names, inputs := sortedInputs(t)
	want := make(map[string][]byte, len(names))
	for _, name := range names {
		want[name] = crefLevel1(t, inputs[name])
	}
	e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithZeroFrames(true))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range names {
				name := names[(i+g)%len(names)]
				if got := e.EncodeAll(inputs[name], nil); !bytes.Equal(got, want[name]) {
					t.Errorf("goroutine %d, %s: differs from cref", g, name)
				}
			}
		}()
	}
	wg.Wait()
}

// TestEncodeAllPadding: the output is C's frame followed by one skippable
// frame that makes the total length (including what dst held) a multiple of
// n, and C decodes it to the input.
func TestEncodeAllPadding(t *testing.T) {
	in := creftest.Inputs(t)["text16KiB"]
	frame := crefLevel1(t, in)
	for _, n := range []int{1, 2, 7, 8, 9, 64, 4096, len(frame), len(frame) + 3} {
		for _, prefix := range []int{0, 5} {
			e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderPadding(n))
			got := e.EncodeAll(in, make([]byte, prefix))
			if len(got)%n != 0 {
				t.Fatalf("n=%d prefix=%d: len %d not a multiple of n", n, prefix, len(got))
			}
			body := got[prefix:]
			if !bytes.Equal(body[:len(frame)], frame) {
				t.Fatalf("n=%d prefix=%d: frame differs from cref", n, prefix)
			}
			pad := body[len(frame):]
			if len(pad) != 0 {
				if len(pad) < 8 || binary.LittleEndian.Uint32(pad)&^0xF != 0x184D2A50 ||
					int(binary.LittleEndian.Uint32(pad[4:])) != len(pad)-8 {
					t.Fatalf("n=%d prefix=%d: bad skippable frame header % x", n, prefix, pad[:min(8, len(pad))])
				}
			}
			if dec := creftest.DecodeC(t, body); !bytes.Equal(dec, in) {
				t.Fatalf("n=%d prefix=%d: C decode mismatch", n, prefix)
			}
			if max := e.MaxEncodedSize(len(in)); len(body) > max {
				t.Fatalf("n=%d: output %d > MaxEncodedSize %d", n, len(body), max)
			}
		}
	}
	// Empty input without WithZeroFrames emits nothing, not even padding.
	e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithEncoderPadding(64))
	if got := e.EncodeAll(nil, nil); len(got) != 0 {
		t.Fatalf("padding added to empty output: %d bytes", len(got))
	}
}

// TestMaxEncodedSize: every EncodeAll output fits the bound.
func TestMaxEncodedSize(t *testing.T) {
	names, inputs := sortedInputs(t)
	e := newTestEncoder(t, WithEncoderLevel(SpeedFastest), WithZeroFrames(true))
	for _, name := range names {
		in := inputs[name]
		if n, max := len(e.EncodeAll(in, nil)), e.MaxEncodedSize(len(in)); n > max {
			t.Errorf("%s: %d > MaxEncodedSize %d", name, n, max)
		}
	}
}

// TestEncodeAllNotPorted: levels whose strategies are not ported yet panic
// with errStrategyNotPorted (EncodeAll has no error result). Removed once
// Tasks 12-16 port dfast/greedy/lazy.
func TestEncodeAllNotPorted(t *testing.T) {
	for _, l := range []EncoderLevel{SpeedDefault, SpeedBetterCompression, SpeedBestCompression} {
		t.Run(l.String(), func(t *testing.T) {
			e := newTestEncoder(t, WithEncoderLevel(l))
			defer func() {
				err, _ := recover().(error)
				if !errors.Is(err, errStrategyNotPorted) {
					t.Fatalf("recovered %v, want errStrategyNotPorted", err)
				}
			}()
			e.EncodeAll([]byte("hello hello hello"), nil)
		})
	}
}

func TestEncoderOptionErrors(t *testing.T) {
	bad := map[string]EOption{
		"window-below-min":  WithWindowSize(MinWindowSize / 2),
		"window-above-max":  WithWindowSize(MaxWindowSize * 2),
		"window-not-pow2":   WithWindowSize(3 << 10),
		"window-zero":       WithWindowSize(0),
		"padding-zero":      WithEncoderPadding(0),
		"padding-negative":  WithEncoderPadding(-1),
		"padding-too-large": WithEncoderPadding(1<<30 + 1),
		"concurrency-neg":   WithEncoderConcurrency(-1),
		"level-zero":        WithEncoderLevel(0),
		"level-too-high":    WithEncoderLevel(SpeedBestCompression + 1),
	}
	for name, o := range bad {
		if _, err := NewWriter(nil, o); err == nil {
			t.Errorf("%s: NewWriter accepted it", name)
		}
	}
	good := []EOption{
		WithWindowSize(MinWindowSize), WithWindowSize(MaxWindowSize), WithEncoderPadding(1),
		WithEncoderPadding(1 << 30), WithEncoderConcurrency(0), WithEncoderConcurrency(4),
	}
	for i, o := range good {
		if _, err := NewWriter(nil, o); err != nil {
			t.Errorf("good option %d: %v", i, err)
		}
	}
}

// TestEncoderLevelFromZstd: each zstd level maps to the highest EncoderLevel
// whose C level (1/3/7/11) does not exceed it; levels below 1 map to
// SpeedFastest.
func TestEncoderLevelFromZstd(t *testing.T) {
	want := map[int]EncoderLevel{
		-5: SpeedFastest, 0: SpeedFastest, 1: SpeedFastest, 2: SpeedFastest,
		3: SpeedDefault, 4: SpeedDefault, 5: SpeedDefault, 6: SpeedDefault,
		7: SpeedBetterCompression, 8: SpeedBetterCompression, 9: SpeedBetterCompression, 10: SpeedBetterCompression,
		11: SpeedBestCompression, 15: SpeedBestCompression, 19: SpeedBestCompression, 22: SpeedBestCompression,
	}
	for level, w := range want {
		if got := EncoderLevelFromZstd(level); got != w {
			t.Errorf("EncoderLevelFromZstd(%d) = %v, want %v", level, got, w)
		}
	}
}

func TestEncoderLevelString(t *testing.T) {
	for l, s := range map[EncoderLevel]string{
		SpeedFastest: "fastest", SpeedDefault: "default",
		SpeedBetterCompression: "better", SpeedBestCompression: "best",
	} {
		if got := l.String(); got != s {
			t.Errorf("%d.String() = %q, want %q", int(l), got, s)
		}
		for _, in := range []string{s, strings.ToUpper(s)} {
			ok, got := EncoderLevelFromString(in)
			if !ok || got != l {
				t.Errorf("EncoderLevelFromString(%q) = %v, %v; want true, %v", in, ok, got, l)
			}
		}
	}
	if ok, got := EncoderLevelFromString("nope"); ok || got != SpeedDefault {
		t.Errorf("EncoderLevelFromString(unknown) = %v, %v; want false, SpeedDefault", ok, got)
	}
	if s := EncoderLevel(0).String(); s == "" || s == "fastest" {
		t.Errorf("EncoderLevel(0).String() = %q", s)
	}
}
