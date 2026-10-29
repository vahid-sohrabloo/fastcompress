package zstd

import (
	"bytes"
	"encoding/binary"
	"strconv"
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
	return crefLevel(t, 1, in, extra...)
}

// crefLevel is crefLevel1 for any C level.
func crefLevel(t testing.TB, level int, in []byte, extra ...string) []byte {
	t.Helper()
	args := append([]string{"compress", "-level", strconv.Itoa(level), "-checksum", "1", "-pledged", "1"}, extra...)
	return creftest.Run(t, in, args...)
}

// encoderLevels are the four EncoderLevels and their C levels.
var encoderLevels = []struct {
	l EncoderLevel
	c int
}{{SpeedFastest, 1}, {SpeedDefault, 3}, {SpeedBetterCompression, 7}, {SpeedBestCompression, 11}}

// TestEncodeAllCref requires EncodeAll at each EncoderLevel with otherwise
// default options to equal C's ZSTD_compress2 at the mapped level with
// checksum on, for every input whose strategy is ported. WithZeroFrames makes
// the empty input produce C's empty frame. At levels 7 and 11 each input gets
// a fresh Encoder (so a fresh cctx, like cref's fresh ZSTD_CCtx); reuse is
// covered by TestEncodeAllConcurrent and the cctx reuse tests.
func TestEncodeAllCref(t *testing.T) {
	names, inputs := sortedInputs(t)
	for _, lv := range encoderLevels {
		e := newTestEncoder(t, WithEncoderLevel(lv.l), WithZeroFrames(true))
		for _, name := range names {
			in := inputs[name]
			if !(compress2Case{level: lv.c}).ported(len(in)) {
				continue
			}
			if lv.c >= 7 {
				e = newTestEncoder(t, WithEncoderLevel(lv.l), WithZeroFrames(true))
			}
			want := crefLevel(t, lv.c, in)
			got := e.EncodeAll(in, nil)
			if !bytes.Equal(got, want) {
				t.Errorf("L%d/%s: EncodeAll differs from cref (got %d bytes, want %d)", lv.c, name, len(got), len(want))
			}
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
// all inputs whose strategy is ported; every output must equal cref's from a
// fresh context. Which pooled cctx serves a call is nondeterministic, so this
// also asserts that context reuse does not change output, which holds for C
// too: the row match finder's hash salt changes on every reset but only
// relabels rows and tags (TestRowHashSaltNeutral). Run it under -race.
func TestEncodeAllConcurrent(t *testing.T) {
	names, inputs := sortedInputs(t)
	for _, lv := range encoderLevels {
		var ported []string
		want := make(map[string][]byte, len(names))
		for _, name := range names {
			if (compress2Case{level: lv.c}).ported(len(inputs[name])) {
				ported = append(ported, name)
				want[name] = crefLevel(t, lv.c, inputs[name])
			}
		}
		e := newTestEncoder(t, WithEncoderLevel(lv.l), WithZeroFrames(true))
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range ported {
					name := ported[(i+g)%len(ported)]
					if got := e.EncodeAll(inputs[name], nil); !bytes.Equal(got, want[name]) {
						t.Errorf("L%d goroutine %d, %s: differs from cref", lv.c, g, name)
					}
				}
			}()
		}
		wg.Wait()
	}
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

// TestEncodeAllEveryLevelPorted: every EncoderLevel has its C strategy
// ported for every input size (level 11 at 16 KiB or less is btopt), so
// TestEncodeAllCref and TestEncodeAllConcurrent skip nothing.
func TestEncodeAllEveryLevelPorted(t *testing.T) {
	names, inputs := sortedInputs(t)
	for _, lv := range encoderLevels {
		for _, name := range names {
			if !(compress2Case{level: lv.c}).ported(len(inputs[name])) {
				t.Errorf("L%d/%s: strategy not ported", lv.c, name)
			}
		}
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
