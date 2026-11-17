package zstd

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// TestSplitBlockCref compares splitBlock with C's ZSTD_splitBlock (cref
// presplit) at every split level on 128 KiB windows of the test inputs and of
// synthetic mixtures whose statistics change at various points.
func TestSplitBlockCref(t *testing.T) {
	_, inputs := sortedInputs(t)
	var windows [][]byte
	var names []string
	for name, in := range inputs {
		for off := 0; off+128<<10 <= len(in); off += 100_000 {
			windows = append(windows, in[off:off+128<<10])
			names = append(names, name+"@"+strconv.Itoa(off))
		}
	}
	rng := rand.New(rand.NewSource(1))
	text := inputs["text16KiB"]
	for _, cut := range []int{4 << 10, 8 << 10, 20 << 10, 40 << 10, 64 << 10, 90 << 10, 120 << 10} {
		for _, kind := range []string{"random", "zeros", "text"} {
			w := make([]byte, 128<<10)
			for i := 0; i < cut; i++ {
				w[i] = text[i%len(text)]
			}
			switch kind {
			case "random":
				rng.Read(w[cut:])
			case "text":
				for i := cut; i < len(w); i++ {
					w[i] = text[(i*7+13)%len(text)]/2 + byte(i%5)
				}
			}
			windows = append(windows, w)
			names = append(names, "mix-"+kind+"-"+strconv.Itoa(cut))
		}
	}
	nonFull := make([]int, 5)
	wants := make([][]int, 5)
	want := func(level, i int) int {
		if wants[level] == nil {
			wants[level] = make([]int, len(windows))
			for j, w := range windows {
				out := creftest.Run(t, w, "presplit", "-level", strconv.Itoa(level))
				n, err := strconv.Atoi(strings.TrimSpace(string(out)))
				if err != nil {
					t.Fatalf("cref output %q: %v", out, err)
				}
				wants[level][j] = n
				if n != 128<<10 {
					nonFull[level]++
				}
			}
		}
		return wants[level][i]
	}
	// One workspace for every call: a call must not depend on what an
	// earlier one left in the workspace. Levels run in decreasing hashLog
	// order, then increasing, then per window alternating byChunks levels
	// with level 0 (fromBorders).
	ws := new(fpStats)
	check := func(level, i int) {
		if got, w := splitBlock(windows[i], 128<<10, level, ws), want(level, i); got != w {
			t.Errorf("level %d %s: splitBlock = %d, C = %d", level, names[i], got, w)
		}
	}
	for _, level := range []int{4, 3, 2, 1, 0, 1, 2, 3, 4} {
		for i := range windows {
			check(level, i)
		}
	}
	for i := range windows {
		for _, level := range []int{2, 0, 4, 0, 1, 3} {
			check(level, i)
		}
	}
	t.Logf("windows=%d, non-full-block splits per level: %v", len(windows), nonFull)
	for level, n := range nonFull {
		if n == 0 {
			t.Errorf("level %d: no window made C split; test is vacuous", level)
		}
	}
}
