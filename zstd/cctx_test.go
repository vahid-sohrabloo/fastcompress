package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// compress2Case is one ZSTD_compress2 parameter set.
type compress2Case struct {
	level     int
	checksum  bool
	windowLog uint32 // 0 = level default
}

func (c compress2Case) String() string {
	s := "L" + strconv.Itoa(c.level)
	if c.checksum {
		s += "/c1"
	} else {
		s += "/c0"
	}
	if c.windowLog != 0 {
		s += "/w" + strconv.Itoa(int(c.windowLog))
	}
	return s
}

func (c compress2Case) crefArgs(mode string) []string {
	args := []string{mode, "-level", strconv.Itoa(c.level), "-checksum", "0", "-pledged", "1"}
	if c.checksum {
		args[4] = "1"
	}
	if c.windowLog != 0 {
		args = append(args, "-window-log", strconv.Itoa(int(c.windowLog)))
	}
	return args
}

func (c compress2Case) newCCtx(t *testing.T) *cctx {
	t.Helper()
	cc := newCCtx()
	cc.setLevel(c.level)
	cc.setChecksum(c.checksum)
	if err := cc.setWindowLog(c.windowLog); err != nil {
		t.Fatal(err)
	}
	return cc
}

func level1Cases() []compress2Case {
	var cases []compress2Case
	for _, lvl := range []int{1, 3} {
		for _, wl := range []uint32{0, 10, 17} {
			for _, cs := range []bool{false, true} {
				cases = append(cases, compress2Case{level: lvl, checksum: cs, windowLog: wl})
			}
		}
	}
	return cases
}

// rowCases are the level 7 and 11 parameter sets. Their strategies (lazy,
// lazy2) use the row-based match finder when the window log is above 14.
func rowCases() []compress2Case {
	return []compress2Case{{7, true, 0}, {7, false, 15}, {11, true, 0}, {11, false, 17}}
}

// ported reports whether this port has the match finder that compress2
// selects with c for an input of n bytes: fast, dfast, and the row-based
// greedy/lazy/lazy2. Other inputs make compress2 return
// errStrategyUnsupported (unless they are too small to search at all).
func (c compress2Case) ported(n int) bool {
	var p cctxParams
	cctxParamsInit(&p, c.level)
	p.cParams.windowLog = c.windowLog
	cp := getCParamsFromCCtxParams(&p, uint64(n), 0, cpmNoAttachDict)
	return cp.strategy <= zstdDfast || rowMatchFinderUsed(cp.strategy, resolveRowMatchFinderMode(psAuto, &cp))
}

// seqStep is one compress2 call on a reused context.
type seqStep struct {
	name string
	c    compress2Case
	src  []byte
}

// crefSeq returns C's frames for the steps compressed one after another
// through ONE ZSTD_CCtx (cref compress-seq): the reference for a reused cctx.
func crefSeq(t *testing.T, steps []seqStep) [][]byte {
	t.Helper()
	var in []byte
	var sizes, levels, checksums, wlogs []string
	for _, s := range steps {
		in = append(in, s.src...)
		sizes = append(sizes, strconv.Itoa(len(s.src)))
		levels = append(levels, strconv.Itoa(s.c.level))
		cs := "0"
		if s.c.checksum {
			cs = "1"
		}
		checksums = append(checksums, cs)
		wlogs = append(wlogs, strconv.Itoa(int(s.c.windowLog)))
	}
	out := creftest.Run(t, in, "compress-seq", "-pledged", "1", "-sizes", strings.Join(sizes, ","),
		"-levels", strings.Join(levels, ","), "-checksums", strings.Join(checksums, ","),
		"-window-logs", strings.Join(wlogs, ","))
	frames := make([][]byte, 0, len(steps))
	for len(out) > 0 {
		n := int(binary.LittleEndian.Uint32(out))
		frames = append(frames, out[4:4+n])
		out = out[4+n:]
	}
	if len(frames) != len(steps) {
		t.Fatalf("cref compress-seq returned %d frames for %d steps", len(frames), len(steps))
	}
	return frames
}

// runSeq compresses the steps with one Go context and requires each frame
// to equal C's from one reused C context.
func runSeq(t *testing.T, steps []seqStep) {
	t.Helper()
	want := crefSeq(t, steps)
	cc := newCCtx()
	for i, s := range steps {
		cc.setLevel(s.c.level)
		cc.setChecksum(s.c.checksum)
		if err := cc.setWindowLog(s.c.windowLog); err != nil {
			t.Fatal(err)
		}
		got, err := cc.compress2(nil, s.src)
		if err != nil {
			t.Fatalf("step %d %s/%s: compress2: %v", i, s.c, s.name, err)
		}
		if !bytes.Equal(got, want[i]) {
			t.Fatalf("step %d %s/%s: frame differs from C's reused context (Go %d bytes, C %d bytes)%s",
				i, s.c, s.name, len(got), len(want[i]), firstDiff(got, want[i]))
		}
	}
}

func sortedInputs(t *testing.T) ([]string, map[string][]byte) {
	inputs := creftest.Inputs(t)
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, inputs
}

// TestCompress2Cref requires cctx.compress2 to produce exactly the frame of
// C's ZSTD_compress2 at levels 1 and 3, with and without checksum and with
// window logs 10 and 17, and at levels 7 and 11 (rowCases) wherever their
// strategy is ported, for every input, including multi-block ones that C
// pre-splits (inputs over 128 KiB).
func TestCompress2Cref(t *testing.T) {
	names, inputs := sortedInputs(t)
	for _, c := range append(level1Cases(), rowCases()...) {
		for _, name := range names {
			src := inputs[name]
			t.Run(c.String()+"/"+name, func(t *testing.T) {
				t.Parallel()
				cc := c.newCCtx(t)
				got, err := cc.compress2(nil, src)
				if err != nil {
					if !c.ported(len(src)) && errors.Is(err, errStrategyUnsupported) {
						return // a later task ports this strategy
					}
					t.Fatalf("compress2: %v", err)
				}
				// A second frame from the same context must be identical.
				// (C's row match finder changes its hash salt on every
				// reset, but that cannot change output: see
				// TestRowHashSaltNeutral.)
				again, err := cc.compress2([]byte("prefix"), src)
				if err != nil {
					t.Fatalf("compress2 (reuse): %v", err)
				}
				if string(again[:6]) != "prefix" || !bytes.Equal(again[6:], got) {
					t.Fatalf("reused context produced a different frame")
				}
				want := creftest.Run(t, src, c.crefArgs("compress")...)
				if !bytes.Equal(got, want) {
					t.Fatalf("frame differs from C (Go %d bytes, C %d bytes)%s", len(got), len(want), firstDiff(got, want))
				}
			})
		}
	}
}

// TestCompress2Reuse compresses every single-block input with ONE context,
// alternating parameters (each input once at levels 1/3 and once at level
// 7/11 where ported), and requires each frame to equal C's from ONE reused
// C context: index continuation, uncleared tables and the row match
// finder's per-reset hash salt must all behave as in C.
func TestCompress2Reuse(t *testing.T) {
	names, inputs := sortedInputs(t)
	cases, rc := level1Cases(), rowCases()
	var steps []seqStep
	for i, name := range names {
		src := inputs[name]
		if len(src) > 128<<10 {
			continue
		}
		steps = append(steps, seqStep{name, cases[i%len(cases)], src})
		if c := rc[i%len(rc)]; c.ported(len(src)) {
			steps = append(steps, seqStep{name, c, src})
		}
	}
	runSeq(t, steps)
}

// TestGetCParams checks parameter selection against clevels.h rows and the
// ZSTD_adjustCParams_internal downsizing for known source sizes.
func TestGetCParams(t *testing.T) {
	for _, tc := range []struct {
		level int
		size  uint64
		want  compressionParameters
	}{
		// > 256 KiB: table 0 row 1 unchanged.
		{1, 1 << 20, compressionParameters{19, 13, 14, 1, 7, 0, zstdFast}},
		// 128 KiB: table 2 row 1; windowLog 17 already fits.
		{1, 128 << 10, compressionParameters{17, 12, 13, 1, 6, 0, zstdFast}},
		// 100 bytes: table 3 row 1; windowLog -> srcLog 7 -> ABSOLUTEMIN 10
		// is applied after hashLog (8) and chainLog (7) were cut.
		{1, 100, compressionParameters{10, 7, 8, 1, 5, 0, zstdFast}},
		// 0 bytes means 0: HASHLOG_MIN srcLog 6.
		{1, 0, compressionParameters{10, 6, 7, 1, 5, 0, zstdFast}},
		// Level 11, 16 KiB: btopt (btScale reduces the cycle log).
		{11, 16 << 10, compressionParameters{14, 15, 14, 3, 4, 12, zstdBtopt}},
		// Unknown size: no downsizing.
		{3, contentSizeUnknown, compressionParameters{21, 16, 17, 1, 5, 0, zstdDfast}},
	} {
		if got := getCParams(tc.level, tc.size, 0); got != tc.want {
			t.Errorf("getCParams(%d, %d) = %+v, want %+v", tc.level, tc.size, got, tc.want)
		}
	}
}

// TestHashPtr checks the C hash functions and the branch-free form the fast
// strategy uses.
func TestHashPtr(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, 64)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	for mls := uint32(4); mls <= 8; mls++ {
		for hBits := uint32(6); hBits <= 30; hBits++ {
			for i := 0; i+8 <= len(b); i++ {
				var want uint32
				u := binary.LittleEndian.Uint64(b[i:])
				switch mls {
				case 4:
					want = (uint32(u) * 2654435761) >> (32 - hBits)
				case 8:
					want = uint32((u * 0xCF1BBCDCB7A56463) >> (64 - hBits))
				default:
					p := map[uint32]uint64{5: 889523592379, 6: 227718039650203, 7: 58295818150454627}[mls]
					want = uint32(((u << (64 - 8*mls)) * p) >> (64 - hBits))
				}
				if got := hashPtr(b, i, hBits, mls); got != want {
					t.Fatalf("hashPtr(mls=%d, hBits=%d) = %d, want %d", mls, hBits, got, want)
				}
				if mls <= 7 {
					sh, prime := fastHashParams(mls)
					if got := uint32(((u << sh) * prime) >> (64 - hBits)); got != want {
						t.Fatalf("fast hash (mls=%d, hBits=%d) = %d, want %d", mls, hBits, got, want)
					}
				}
			}
		}
	}
}

func TestCount(t *testing.T) {
	b := []byte("abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz012345678X")
	for iEnd := 36; iEnd <= len(b); iEnd++ {
		want := 0
		for 36+want < iEnd && b[36+want] == b[want] {
			want++
		}
		if got := count(b, 36, 0, iEnd); got != want {
			t.Fatalf("count(iEnd=%d) = %d, want %d", iEnd, got, want)
		}
	}
}

// TestCompress2IndexResetTables shrinks the hash table across an index
// reset and grows it again: entries beyond the shrunk length still hold
// pre-reset indices, which lie ahead of the new window unless growing the
// table zeroes them (C: ZSTD_cwksp cleans tables beyond tableValidEnd). The
// reference is C's reused context for the same three frames (C does not
// restart the indices there; restarting must not change output).
func TestCompress2IndexResetTables(t *testing.T) {
	_, inputs := sortedInputs(t)
	for _, tc := range []struct {
		level      int
		big, small []byte
	}{
		{1, inputs["text16KiB"], inputs["text16KiB"][:1024]}, // hashLog 15, then 11
		// Level 3 (dfast) owns two tables (hash and chain), both resized.
		{3, inputs["text16KiB"], inputs["text16KiB"][:1024]},
		// Level 7 (row lazy, hashLog 19), then row lazy2 with hashLog 16:
		// hash and tag tables are resized.
		{7, inputs["text256KiB"], inputs["text16KiB+1"]},
	} {
		c := compress2Case{level: tc.level}
		want := crefSeq(t, []seqStep{{"big", c, tc.big}, {"small", c, tc.small}, {"big", c, tc.big}})
		cc := c.newCCtx(t)
		if _, err := cc.compress2(nil, tc.big); err != nil {
			t.Fatal(err)
		}
		// Force ZSTD_indexTooCloseToMax: the next frame restarts the indices.
		cc.blockState.matchState.window.nextSrc = currentMax
		if _, err := cc.compress2(nil, tc.small); err != nil {
			t.Fatal(err)
		}
		got, err := cc.compress2(nil, tc.big)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want[2]) {
			t.Fatalf("%s: frame after index reset differs from C (Go %d bytes, C %d bytes)%s", c, len(got), len(want[2]), firstDiff(got, want[2]))
		}
	}
}

// TestCompress2ReuseMultiBlock is TestCompress2Reuse for inputs larger
// than one block: levels 1/3 at window log 10 (1 KiB blocks, never
// pre-split by C) or the default window log (pre-split blocks), and levels
// 7/11 at the default window log or 15, all through one context.
func TestCompress2ReuseMultiBlock(t *testing.T) {
	names, inputs := sortedInputs(t)
	var steps []seqStep
	for i, name := range names {
		src := inputs[name]
		if len(src) <= 128<<10 {
			continue
		}
		c := compress2Case{level: 1 + 2*(i/2%2), checksum: i%2 == 1, windowLog: 10}
		if i%3 == 0 {
			c.windowLog = 0 // default window log: C pre-splits full blocks
		}
		steps = append(steps, seqStep{name, c, src})
		r := compress2Case{level: 7 + 4*(i/2%2), checksum: i%2 == 0}
		if i%3 == 1 {
			r.windowLog = 15
		}
		if r.ported(len(src)) {
			steps = append(steps, seqStep{name, r, src})
		}
	}
	runSeq(t, steps)
}
