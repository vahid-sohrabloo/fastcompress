package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"slices"
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
// lazy2) use the row-based match finder when the window log is above 14, and
// the hash chain at or below it (level 7 at 16 KiB or less).
func rowCases() []compress2Case {
	return []compress2Case{{7, true, 0}, {7, false, 15}, {11, true, 0}, {11, false, 17}}
}

// hcCases force the hash-chain match finder (window log 14 or less) for
// greedy (5), lazy (6) and lazy2 (7, 9), whatever the input size.
func hcCases() []compress2Case {
	return []compress2Case{{5, false, 14}, {6, true, 12}, {7, false, 14}, {9, true, 10}, {7, true, 0}}
}

// btCases use the binary tree (btlazy2) beyond level 11's 16 KiB to 256
// KiB: level 10 at 16 KiB or less (searchLog 9), level 12 at 256 KiB or
// less (searchLog 7) and level 13 above 256 KiB (minMatch 5, multi-block).
func btCases() []compress2Case {
	return []compress2Case{{10, false, 0}, {12, true, 0}, {13, false, 17}}
}

// optCases use the optimal parser (btopt): level 12 at 16 KiB or less
// (minMatch 3, so hashTable3), and levels 13 to 16 with window log 16,
// which keeps the post-block splitter off (it needs window log 17 or more)
// so that btopt runs on larger and multi-block inputs: level 13 up to 256
// KiB (minMatch 4, 64 KiB blocks above 64 KiB), 14 and 15 up to 256 KiB
// (minMatch 3; targetLength 32 and 256) and 16 above 256 KiB (minMatch 5).
func optCases() []compress2Case {
	return []compress2Case{{12, false, 0}, {13, true, 16}, {14, false, 16}, {15, true, 16}, {16, false, 16}}
}

// ported reports whether this port has the match finder that compress2
// selects with c for an input of n bytes: fast, dfast, greedy/lazy/lazy2
// (row-based or hash chain), btlazy2 and btopt, without the post-block
// splitter. Other inputs make compress2 return errStrategyUnsupported
// (unless they are too small to search at all).
func (c compress2Case) ported(n int) bool {
	var p cctxParams
	cctxParamsInit(&p, c.level)
	p.cParams.windowLog = c.windowLog
	cp := getCParamsFromCCtxParams(&p, uint64(n), 0, cpmNoAttachDict)
	return cp.strategy <= zstdBtopt && resolveBlockSplitterMode(psAuto, &cp) != psEnable
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
// window logs 10 and 17, and at levels 5 to 16 (rowCases, hcCases,
// btCases, optCases) wherever their strategy is ported, for every input, including
// multi-block ones that C pre-splits (inputs over 128 KiB).
func TestCompress2Cref(t *testing.T) {
	names, inputs := sortedInputs(t)
	for _, c := range slices.Concat(level1Cases(), rowCases(), hcCases(), btCases(), optCases()) {
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
// alternating parameters (each input once at levels 1/3 and once with a
// rowCases, hcCases, btCases or optCases parameter set, levels 5 to 16,
// where ported), and requires each frame to equal C's from ONE reused C context:
// index continuation, uncleared tables, the row match finder's per-reset
// hash salt, the binary trees' stale entries and the optimal parser's
// statistics must all behave as in C.
func TestCompress2Reuse(t *testing.T) {
	names, inputs := sortedInputs(t)
	cases, rc := level1Cases(), slices.Concat(rowCases(), hcCases(), btCases(), optCases())
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
	for mls := uint32(3); mls <= 9; mls++ { // 3 and 9 hash 4 bytes, as C's default case
		for hBits := uint32(6); hBits <= 30; hBits++ {
			for i := 0; i+8 <= len(b); i++ {
				var want uint32
				u := binary.LittleEndian.Uint64(b[i:])
				switch mls {
				case 3, 4, 9:
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
				if mls >= 4 && mls <= 7 {
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
		// Level 11 btlazy2 (hashLog 19, chainLog 18), then btlazy2 with
		// hashLog and chainLog 15: hash and binary-tree tables are resized.
		{11, inputs["text256KiB"], inputs["text16KiB+1"]},
		// Level 11 btopt (hashLog 14, chainLog 15), then hashLog 11 and
		// chainLog 11; level 12 adds hashTable3 (hashLog3 14, then 10).
		// (small is a separate copy; inputs sharing memory with the
		// previous frame are TestCompress2AliasedInputs' subject.)
		{11, inputs["text16KiB"], bytes.Clone(inputs["text16KiB"][:1024])},
		{12, inputs["text16KiB"], bytes.Clone(inputs["text16KiB"][:1024])},
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
// pre-split by C) or the default window log (pre-split blocks), levels
// 7/11 at the default window log or 15, level 13 (btlazy2) at window
// log 17 or 18, and btopt over 64 KiB blocks (level 13 up to 256 KiB, level
// 16 above, window log 16), all through one context: the optimal parser's
// statistics carry over from block to block (ZSTD_rescaleFreqs).
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
		// btlazy2 over several blocks (level 13 above 256 KiB, windowLog
		// 17 or 18).
		if bt := (compress2Case{level: 13, checksum: i%2 == 1, windowLog: 17 + uint32(i%2)}); bt.ported(len(src)) {
			steps = append(steps, seqStep{name, bt, src})
		}
		opt := compress2Case{level: 13, checksum: i%2 == 0, windowLog: 16}
		if len(src) > 256<<10 {
			opt.level = 16
		}
		if getCParams(opt.level, uint64(len(src)), 0).strategy != zstdBtopt || !opt.ported(len(src)) {
			t.Fatalf("%s: %s is not btopt without post-splitter", name, opt)
		}
		steps = append(steps, seqStep{name, opt, src})
	}
	runSeq(t, steps)
}

// TestCompress2ReuseSmallFrames reuses one context for small frames of
// short repeats at levels 1 and 3. Every frame starts with the previous
// frames' table entries, all below its window, so the fast and double-fast
// finders see many stale candidates (rejected without a branch on the
// index in their assembly) whose bytes often equal the bytes at ip.
func TestCompress2ReuseSmallFrames(t *testing.T) {
	pat := []byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0xe2, 0xb4}
	rng := rand.New(rand.NewPCG(7, 11))
	var steps []seqStep
	for i := range 8 {
		var src []byte
		for len(src) < 20000+i*3001 {
			src = append(src, pat[:4+rng.IntN(7)]...)
			for range rng.IntN(4) {
				src = append(src, byte(rng.Uint32()))
			}
		}
		steps = append(steps, seqStep{"small" + strconv.Itoa(i), compress2Case{level: 1 + 2*(i%2)}, src})
	}
	runSeq(t, steps)
}

// TestCompress2OverflowCorrectionBtlazy2 starts a btlazy2 frame (level 11,
// 128 KiB blocks) just below currentMax, so overflow correction
// (ZSTD_reduceIndex with ZSTD_reduceTable_btlazy2) runs between blocks while
// the binary tree holds unsorted marks. Correction keeps the low cycleLog
// (= btLog) bits of every index and every in-window entry, so the frame must
// equal C's fresh-context frame, where no correction happens. Rescaling the
// unsorted mark like an index (preserveMark false) changes the frame.
func TestCompress2OverflowCorrectionBtlazy2(t *testing.T) {
	_, inputs := sortedInputs(t)
	for _, name := range []string{"text256KiB", "zstd-format.md", "text128KiB+1"} {
		src := inputs[name]
		c := compress2Case{level: 11}
		if getCParams(11, uint64(len(src)), 0).strategy != zstdBtlazy2 {
			t.Fatalf("%s: level 11 is not btlazy2", name)
		}
		checkOverflowCorrection(t, c, name, src)
	}
}

// TestCompress2OverflowCorrectionBtopt is TestCompress2OverflowCorrectionBtlazy2
// for btopt's binary tree (and hashTable3 at level 14): level 13 or 14 with
// window log 16 (64 KiB blocks).
func TestCompress2OverflowCorrectionBtopt(t *testing.T) {
	_, inputs := sortedInputs(t)
	for _, tc := range []struct {
		level int
		name  string
	}{{13, "text256KiB"}, {14, "zstd-format.md"}, {13, "text128KiB+1"}} {
		src := inputs[tc.name]
		c := compress2Case{level: tc.level, windowLog: 16}
		if getCParams(tc.level, uint64(len(src)), 0).strategy != zstdBtopt || !c.ported(len(src)) {
			t.Fatalf("%s: %s is not btopt without post-splitter", tc.name, c)
		}
		checkOverflowCorrection(t, c, tc.name, src)
	}
}

// checkOverflowCorrection compresses src with c starting just below
// currentMax and requires at least one overflow correction and C's frame.
func checkOverflowCorrection(t *testing.T, c compress2Case, name string, src []byte) {
	t.Helper()
	cc := c.newCCtx(t)
	cc.reset()
	if err := cc.initCompressStream2(eEnd, len(src), false); err != nil {
		t.Fatal(err)
	}
	// Continue the indices from just below currentMax (odd low bits),
	// as a long-reused context would: C's ZSTD_window_clear state.
	ms := &cc.blockState.matchState
	start := uint32(currentMax) - uint32(len(src)/2) - 12345
	ms.window.nextSrc, ms.window.lowLimit, ms.window.dictLimit = start, start, start
	ms.nextToUpdate = start
	out := make([]byte, compressBound(len(src)))
	n, err := cc.compressEnd(out, src)
	if err != nil {
		t.Fatal(err)
	}
	if ms.window.nbOverflowCorrections == 0 {
		t.Fatalf("%s: no overflow correction happened", name)
	}
	want := creftest.Run(t, src, c.crefArgs("compress")...)
	if got := out[:n]; !bytes.Equal(got, want) {
		t.Fatalf("%s/%s: frame after overflow correction differs from C (Go %d bytes, C %d bytes)%s", c, name, len(got), len(want), firstDiff(got, want))
	}
}

// TestCompress2AliasedInputs compresses, through ONE context per level,
// inputs that share memory with the previous frame's input: the same slice,
// a prefix, a suffix, an interior overlap, and an adjacent continuation
// (the next input starts where the previous one ended, so
// ZSTD_window_update sees it as contiguous). C's ZSTD_window_update
// compares the new input with the old prefix (now the dictionary) by
// address and shrinks the dictionary where they overlap; between frames
// the window is cleared, so with C's pointers that never changes a frame.
// Each frame must therefore equal C's from one reused context fed the same
// bytes in distinct buffers (cref compress-seq), and nothing may panic.
func TestCompress2AliasedInputs(t *testing.T) {
	_, inputs := sortedInputs(t)
	big := bytes.Clone(inputs["text16KiB"])
	n := len(big)
	parts := []struct {
		name string
		src  []byte
	}{
		{"whole", big},
		{"same", big},
		{"prefix", big[:4096]},
		{"whole-after-prefix", big},
		{"suffix", big[n-4096:]},
		{"interior", big[1000:9000]},
		{"overlap-before", big[500:5000]},
		{"head", big[:8000]},
		{"adjacent", big[8000:]},
		{"whole-again", big},
	}
	for _, level := range []int{1, 3, 7, 11} {
		c := compress2Case{level: level, checksum: true}
		var steps []seqStep
		for _, p := range parts {
			steps = append(steps, seqStep{p.name, c, p.src})
		}
		want := crefSeq(t, steps)
		cc := c.newCCtx(t)
		for i, s := range steps {
			got, err := func() (got []byte, err error) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("L%d step %d %s: panic: %v", level, i, s.name, r)
					}
				}()
				return cc.compress2(nil, s.src)
			}()
			if err != nil {
				t.Fatalf("L%d step %d %s: %v", level, i, s.name, err)
			}
			if !bytes.Equal(got, want[i]) {
				t.Fatalf("L%d step %d %s: frame differs from C (Go %d bytes, C %d bytes)%s", level, i, s.name, len(got), len(want[i]), firstDiff(got, want[i]))
			}
		}
	}
}

// TestWindowUpdateOverlap checks ZSTD_window_update's dictionary reduction
// when a non-contiguous input overlaps the old prefix in memory. The
// expected lowLimit is C's: min((ip + srcSize) - dictBase, dictLimit),
// where dictBase is index 0 of the dictionary (here 2 bytes before the
// array's first byte, which wrapped in the uint32 arithmetic before).
func TestWindowUpdateOverlap(t *testing.T) {
	arr := make([]byte, 1000)
	for _, tc := range []struct {
		name                   string
		first, second          [2]int
		wantLow, wantDictLimit uint32
	}{
		// dict: arr[300:700] at indices 2..402; input arr[100:350] ends at
		// index 2+50 = 52 of the dictionary.
		{"ends-inside", [2]int{300, 700}, [2]int{100, 350}, 52, 402},
		// input arr[200:600] covers the dictionary's end: lowLimit = dictLimit.
		{"covers-end", [2]int{0, 400}, [2]int{200, 600}, 402, 402},
		// input before the dictionary: no change (too small extDict rule
		// does not apply: 400 bytes).
		{"disjoint", [2]int{500, 900}, [2]int{0, 400}, 2, 402},
	} {
		var w window
		windowInit(&w)
		windowUpdate(&w, arr[tc.first[0]:tc.first[1]], false)
		windowUpdate(&w, arr[tc.second[0]:tc.second[1]], false)
		if w.lowLimit != tc.wantLow || w.dictLimit != tc.wantDictLimit {
			t.Errorf("%s: lowLimit %d dictLimit %d, want %d %d", tc.name, w.lowLimit, w.dictLimit, tc.wantLow, tc.wantDictLimit)
		}
	}
}
