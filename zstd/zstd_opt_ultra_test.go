package zstd

import (
	"bytes"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// ultraCases are the compress2 parameter sets of TestCompress2UltraCref:
// levels 13 to 22 with the level's window log (the post-block splitter
// runs above 64 KiB, where the window log reaches 17) and with window logs
// 16 and 14 (no splitter at any size, so the block compressors alone run
// on larger, multi-block inputs). -short keeps levels 13, 16, 19 and 22
// and window logs 0 and 16.
func ultraCases() []compress2Case {
	step, wls := 1, []uint32{0, 16, 14}
	if testing.Short() {
		step, wls = 3, []uint32{0, 16}
	}
	var cases []compress2Case
	for level := 13; level <= 22; level += step {
		for i, wl := range wls {
			cases = append(cases, compress2Case{level: level, checksum: (level+i)%2 == 0, windowLog: wl})
		}
	}
	return cases
}

// TestCompress2UltraCref requires cctx.compress2 to produce exactly the
// frame of C's ZSTD_compress2 at levels 13 to 22 (ultraCases) for every
// input where they select btultra or btultra2, with or without the
// post-block splitter (btultra2's first pass, ZSTD_initStats_ultra, runs
// on every frame's first block above 8 bytes), and requires both
// strategies to have run, on single- and multi-block inputs.
func TestCompress2UltraCref(t *testing.T) {
	names, inputs := sortedInputs(t)
	ran := map[strategy]int{}
	multi := map[strategy]int{}
	for _, c := range ultraCases() {
		for _, name := range names {
			src := inputs[name]
			if !c.ported(len(src)) || len(src) > 4<<20 { // the Silesia files would take hours
				continue
			}
			cc := c.newCCtx(t)
			got, err := cc.compress2(nil, src)
			if err != nil {
				t.Fatalf("%s/%s: compress2: %v", c, name, err)
			}
			strat := cc.appliedParams.cParams.strategy
			if strat < zstdBtultra {
				continue // btopt: covered by TestCompress2Cref
			}
			want := creftest.Run(t, src, c.crefArgs("compress")...)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s/%s (strategy %d): frame differs from C (Go %d bytes, C %d bytes)%s",
					c, name, strat, len(got), len(want), firstDiff(got, want))
			}
			ran[strat]++
			if len(src) > cc.blockSizeMax {
				multi[strat]++
			}
		}
	}
	for _, s := range []strategy{zstdBtultra, zstdBtultra2} {
		if ran[s] == 0 || multi[s] == 0 {
			t.Errorf("strategy %d: %d frames, %d multi-block", s, ran[s], multi[s])
		}
	}
}

// TestCompress2UltraReuse compresses the inputs up to 128 KiB with ONE
// context, alternating btultra and btultra2 parameter sets with level 1 and
// btopt ones, and requires each frame to equal C's from ONE reused C
// context: the optimal parser's statistics must be reset per frame so that
// btultra2's first pass runs again, and the history it drops must not leak
// into the next frame.
func TestCompress2UltraReuse(t *testing.T) {
	names, inputs := sortedInputs(t)
	cases := []compress2Case{{13, true, 0}, {1, false, 0}, {19, false, 16}, {16, true, 16}, {22, true, 14}, {18, false, 0}}
	var steps []seqStep
	for i, name := range names {
		src := inputs[name]
		if len(src) > 128<<10 {
			continue
		}
		for j := range 2 {
			if c := cases[(i+j)%len(cases)]; c.ported(len(src)) {
				steps = append(steps, seqStep{name, c, src})
			}
		}
	}
	runSeq(t, steps)
}
