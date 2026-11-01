package zstd

import (
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
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
	// One workspace for every call, levels in decreasing hashLog order: a
	// call must not depend on what an earlier one left in the workspace.
	ws := new(fpStats)
	for level := 4; level >= 0; level-- {
		for i, w := range windows {
			out := creftest.Run(t, w, "presplit", "-level", strconv.Itoa(level))
			want, err := strconv.Atoi(strings.TrimSpace(string(out)))
			if err != nil {
				t.Fatalf("cref output %q: %v", out, err)
			}
			got := splitBlock(w, 128<<10, level, ws)
			if got != want {
				t.Errorf("level %d %s: splitBlock = %d, C = %d", level, names[i], got, want)
			}
			if want != 128<<10 {
				nonFull[level]++
			}
		}
	}
	t.Logf("windows=%d, non-full-block splits per level: %v", len(windows), nonFull)
	for level, n := range nonFull {
		if n == 0 {
			t.Errorf("level %d: no window made C split; test is vacuous", level)
		}
	}
}
