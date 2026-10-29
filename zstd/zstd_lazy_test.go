package zstd

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// TestRowGetMatchMask checks the SWAR mask against a byte-by-byte one for
// every row size and head.
func TestRowGetMatchMask(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	row := make([]byte, 64)
	for _, rowEntries := range []uint32{16, 32, 64} {
		for range 2000 {
			for i := range row {
				row[i] = byte(r.IntN(4)) // few values: many matches
			}
			tag := byte(r.IntN(4))
			for head := uint32(0); head < rowEntries; head++ {
				var want uint64
				for n := uint32(0); n < rowEntries; n++ {
					if row[(head+n)%rowEntries] == tag {
						want |= 1 << n
					}
				}
				if got := rowGetMatchMask(row, tag, head, rowEntries); got != want {
					t.Fatalf("rowEntries %d head %d: mask %x, want %x", rowEntries, head, got, want)
				}
			}
		}
	}
}

// TestRowHashSaltCref requires the row match finder's hash salt and
// collected salt entropy to evolve as in C over a sequence of frames on one
// context, mixing row levels with levels that do not use it.
func TestRowHashSaltCref(t *testing.T) {
	_, inputs := sortedInputs(t)
	steps := []seqStep{
		{"text128KiB", compress2Case{level: 7}, inputs["text128KiB"]},
		{"text256KiB+1", compress2Case{level: 11}, inputs["text256KiB+1"]},
		{"text16KiB", compress2Case{level: 1}, inputs["text16KiB"]},
		{"zstd-format.md", compress2Case{level: 7, windowLog: 15}, inputs["zstd-format.md"]},
		{"text16KiB", compress2Case{level: 3}, inputs["text16KiB"]},
		{"win512KiB", compress2Case{level: 7}, inputs["win512KiB"]},
	}
	saltFile := filepath.Join(t.TempDir(), "salts")
	var in []byte
	var sizes, levels, wlogs []string
	for _, s := range steps {
		in = append(in, s.src...)
		sizes = append(sizes, fmt.Sprint(len(s.src)))
		levels = append(levels, fmt.Sprint(s.c.level))
		wlogs = append(wlogs, fmt.Sprint(s.c.windowLog))
	}
	creftest.Run(t, in, "compress-seq", "-sizes", strings.Join(sizes, ","), "-levels", strings.Join(levels, ","),
		"-window-logs", strings.Join(wlogs, ","), "-salt-out", saltFile)
	b, err := os.ReadFile(saltFile)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSpace(string(b)), "\n")
	cc := newCCtx()
	for i, s := range steps {
		cc.setLevel(s.c.level)
		if err := cc.setWindowLog(s.c.windowLog); err != nil {
			t.Fatal(err)
		}
		if _, err := cc.compress2(nil, s.src); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		ms := &cc.blockState.matchState
		if got := fmt.Sprintf("%016x %d", ms.hashSalt, ms.hashSaltEntropy); got != want[i] {
			t.Fatalf("step %d %s/%s: salt state %q, C %q", i, s.c, s.name, got, want[i])
		}
	}
}

// TestRowHashSaltNeutral: neither the hash salt (and so the history that
// sets it) nor the content left in the tag table changes output. The salt is
// XORed into the hash before its final shift, which relabels rows and tags
// by constants; leftover tags are only ever followed by indices below the
// window. This is why a reused context, in C as here, produces the same
// frames as a fresh one, and why the tag table need not reproduce C's
// workspace bytes (resizeTagTable).
func TestRowHashSaltNeutral(t *testing.T) {
	_, inputs := sortedInputs(t)
	r := rand.New(rand.NewPCG(5, 6))
	for _, tc := range []struct {
		level int
		name  string
	}{{7, "text128KiB"}, {7, "win512KiB"}, {11, "text256KiB+1"}, {11, "win512KiB-1"}} {
		src := inputs[tc.name]
		c := compress2Case{level: tc.level}
		want := creftest.Run(t, src, c.crefArgs("compress")...)
		cc := c.newCCtx(t)
		for i := range 4 {
			ms := &cc.blockState.matchState
			ms.hashSalt = r.Uint64()
			ms.hashSaltEntropy = r.Uint32()
			for j := range ms.tagTable {
				ms.tagTable[j] = byte(r.Uint32())
			}
			got, err := cc.compress2(nil, src)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("L%d/%s round %d: frame differs from C%s", tc.level, tc.name, i, firstDiff(got, want))
			}
		}
	}
}

// TestGreedyRowCref is a sanity check of depth 0 (greedy, level 5), which
// shares the lazy parser: frames must equal C's for a few inputs whose
// window log is above 14.
func TestGreedyRowCref(t *testing.T) {
	_, inputs := sortedInputs(t)
	for _, name := range []string{"zstd-format.md", "text128KiB", "text256KiB+1", "win512KiB", "litrle"} {
		src := inputs[name]
		c := compress2Case{level: 5}
		if !c.ported(len(src)) {
			t.Fatalf("%s: level 5 does not use the row match finder", name)
		}
		got, err := c.newCCtx(t).compress2(nil, src)
		if err != nil {
			t.Fatal(err)
		}
		if want := creftest.Run(t, src, c.crefArgs("compress")...); !bytes.Equal(got, want) {
			t.Fatalf("L5/%s: frame differs from C (Go %d bytes, C %d bytes)%s", name, len(got), len(want), firstDiff(got, want))
		}
	}
}
