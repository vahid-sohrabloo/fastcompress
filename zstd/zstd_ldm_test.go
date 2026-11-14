package zstd

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// ldmInput returns n bytes with long-distance repeats: four random
// segments (2 MiB, or n/12 for n below 24 MiB), then copies of them (in another order, some starting mid-way,
// with one byte changed every 8 KiB) far enough back that only the
// long-distance matcher reaches them at levels 16 to 21 (their binary
// trees cover 2 to 32 MiB), a zero run (an overlapping repeat:
// ZSTD_ldm_generateSequences_internal's skip) and random filler.
func ldmInput(n int) []byte {
	r := rand.New(rand.NewPCG(7, uint64(n)))
	seg := min(2<<20, n/12)
	segs := make([][]byte, 4)
	for i := range segs {
		segs[i] = make([]byte, seg)
		for j := range segs[i] {
			segs[i][j] = byte(r.Uint32())
		}
	}
	out := make([]byte, 0, n+seg)
	for _, s := range segs {
		out = append(out, s...)
	}
	for k := 0; len(out) < n; k++ {
		s := segs[(k*3+1)%len(segs)]
		start := 0
		if k%3 == 1 {
			start = r.IntN(seg / 2)
		}
		at := len(out)
		out = append(out, s[start:]...)
		for j := at + r.IntN(8<<10); j < len(out); j += 8 << 10 {
			out[j]++
		}
		switch k % 4 {
		case 1:
			out = append(out, make([]byte, seg/4)...)
		case 3:
			for range seg / 7 {
				out = append(out, byte(r.Uint32()))
			}
		}
	}
	return out[:n]
}

// countLDM installs ldmSeqHook for the test and returns a pointer to the
// number of long-distance sequences generated.
func countLDM(t *testing.T) *int {
	t.Helper()
	n, _ := countLDMExt(t)
	return n
}

// countLDMExt is countLDM, also returning a pointer to the number of
// sequences generated while the LDM window had an extDict segment.
func countLDMExt(t *testing.T) (n, ext *int) {
	t.Helper()
	n, ext = new(int), new(int)
	ldmSeqHook = func(k int, extDict bool) {
		*n += k
		if extDict {
			*ext += k
		}
	}
	t.Cleanup(func() { ldmSeqHook = nil })
	return n, ext
}

// TestLDMAdjustParameters checks ZSTD_ldm_adjustParameters' derived values
// for the strategies that enable LDM.
func TestLDMAdjustParameters(t *testing.T) {
	for _, tc := range []struct {
		strat                                         strategy
		windowLog                                     uint32
		hashLog, bucketSizeLog, minMatch, hashRateLog uint32
	}{
		{zstdBtopt, 27, 22, 7, 64, 5},
		{zstdBtultra, 27, 22, 8, 32, 5},
		{zstdBtultra2, 27, 23, 8, 32, 4},
		{zstdBtultra2, 31, 27, 8, 32, 4},
	} {
		cp := compressionParameters{windowLog: tc.windowLog, strategy: tc.strat}
		p := ldmParams{enableLdm: resolveEnableLdm(psAuto, &cp)}
		if p.enableLdm != psEnable {
			t.Fatalf("%+v: LDM not enabled", tc)
		}
		ldmAdjustParameters(&p, &cp)
		if p.hashLog != tc.hashLog || p.bucketSizeLog != tc.bucketSizeLog || p.minMatchLength != tc.minMatch ||
			p.hashRateLog != tc.hashRateLog || p.windowLog != tc.windowLog {
			t.Errorf("%+v: got %+v", tc, p)
		}
	}
	if resolveEnableLdm(psAuto, &compressionParameters{windowLog: 26, strategy: zstdBtultra2}) != psDisable ||
		resolveEnableLdm(psAuto, &compressionParameters{windowLog: 27, strategy: zstdBtlazy2}) != psDisable {
		t.Error("LDM enabled below btopt or window log 27")
	}
}

// TestLDMStreamCref streams inputs with long-distance repeats through a
// context of unknown size (the window keeps its log of 27, so C enables
// LDM) and requires C's frame: levels 16 (btopt), 18 (btultra) and 19
// (btultra2, whose first pass runs with the LDM's candidates) with window
// log 27, and level 22 (window log 27 by default) in full mode, in random
// chunks with flushes, and in uniform 1 MiB chunks with a checksum. The
// LDM must have produced sequences, and the frame must differ from the one
// without LDM.
func TestLDMStreamCref(t *testing.T) {
	n := 9 << 20
	cases := []streamCase{
		{level: 16, windowLog: 27, flushEvery: 7},
		{level: 18, windowLog: 27, chunk: 1 << 20, checksum: true},
		{level: 19, windowLog: 27},
	}
	if !testing.Short() {
		n = 24 << 20
		cases = append(cases, streamCase{level: 22, chunk: 1 << 20, flushEvery: 5}, streamCase{level: 17, windowLog: 28})
	}
	src := ldmInput(n)
	for _, sc := range cases {
		seqs := countLDM(t)
		got := cctxStream(t, sc, src)
		if *seqs == 0 {
			t.Fatalf("%v: the LDM produced no sequences", sc)
		}
		want := creftest.Run(t, src, sc.crefArgs(len(src))...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%v: frame differs from C (Go %d bytes, C %d bytes)%s", sc, len(got), len(want), firstDiff(got, want))
		}
		off := sc
		off.ldm = psDisable
		if bytes.Equal(cctxStream(t, off, src), got) {
			t.Fatalf("%v: the same frame without LDM", sc)
		}
		t.Logf("%v: %d bytes -> %d, %d LDM sequences", sc, len(src), len(got), *seqs)
	}
}

// TestLDMWriterCref streams through the Encoder (WithEncoderZstdLevel and
// WithWindowSize) and requires `cref stream-pipe`'s frame.
func TestLDMWriterCref(t *testing.T) {
	src := ldmInput(6 << 20)
	for _, tc := range []struct{ level, windowLog int }{{16, 27}, {20, 27}} {
		seqs := countLDM(t)
		var buf bytes.Buffer
		e, err := NewWriter(&buf, WithEncoderZstdLevel(tc.level), WithWindowSize(1<<tc.windowLog), WithEncoderCRC(true))
		if err != nil {
			t.Fatal(err)
		}
		for p := 0; p < len(src); p += 1 << 20 {
			if _, err := e.Write(src[p:min(p+1<<20, len(src))]); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if *seqs == 0 {
			t.Fatalf("L%d: the LDM produced no sequences", tc.level)
		}
		want := creftest.Run(t, src, "stream-pipe", "-chunk-size", strconv.Itoa(1<<20), "-level", strconv.Itoa(tc.level),
			"-checksum", "1", "-window-log", strconv.Itoa(tc.windowLog))
		if got := buf.Bytes(); !bytes.Equal(got, want) {
			t.Fatalf("L%d/w%d: Writer differs from C (Go %d bytes, C %d bytes)%s", tc.level, tc.windowLog, len(got), len(want), firstDiff(got, want))
		}
	}
}

// TestLDMCompress2Cref requires compress2 (known size: C only keeps a
// window log of 27, and so LDM, above 64 MiB) to equal ZSTD_compress2 on a
// 65 MiB input with long-distance repeats: level 16 and 19 with window log
// 27 and level 22 with its own, each with a fresh context, then the same
// cases one after another through ONE context against cref compress-seq
// (the LDM state is reset for every frame). Full mode only.
func TestLDMCompress2Cref(t *testing.T) {
	if testing.Short() {
		t.Skip("65 MiB inputs at levels 16 to 22")
	}
	src := ldmInput(65 << 20)
	cases := []compress2Case{{16, false, 27}, {19, true, 27}, {22, false, 0}}
	for _, c := range cases {
		seqs := countLDM(t)
		cc := c.newCCtx(t)
		got, err := cc.compress2(nil, src)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if cc.appliedParams.ldmParams.enableLdm != psEnable || *seqs == 0 {
			t.Fatalf("%s: LDM %v, %d sequences", c, cc.appliedParams.ldmParams.enableLdm, *seqs)
		}
		want := creftest.Run(t, src, c.crefArgs("compress")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: frame differs from C (Go %d bytes, C %d bytes)%s", c, len(got), len(want), firstDiff(got, want))
		}
		t.Logf("%s: %d bytes -> %d, %d LDM sequences", c, len(src), len(got), *seqs)
	}
	seqs := countLDM(t)
	runSeq(t, []seqStep{
		{"ldm65M", cases[0], src},
		{"small", compress2Case{19, false, 0}, src[:100<<10]},
		{"ldm65M", cases[2], src},
	})
	if *seqs == 0 {
		t.Fatal("reused context: the LDM produced no sequences")
	}
}

// TestLDMDictCref compresses inputs with long-distance repeats with a
// dictionary (the LDM state starts empty: C never loads a CDict into it)
// and requires C's frames: the Writer at level 16 with window log 27 and
// unknown size (the CDict is attached), against `cref stream-pipe`, and,
// in full mode, EncodeAll of 65 MiB at level 22 (known size above the
// cutoff: the CDict is copied) against ZSTD_compress2.
func TestLDMDictCref(t *testing.T) {
	src := ldmInput(6 << 20)
	var big []byte
	if !testing.Short() {
		big = ldmInput(65 << 20)
	}
	attached := map[bool]int{}
	cdictHook = func(_ strategy, attach bool) { attached[attach]++ }
	t.Cleanup(func() { cdictHook = nil })
	for _, d := range testDicts(t)[:3] {
		dargs := d.crefArgs(t)
		seqs := countLDM(t)
		var buf bytes.Buffer
		e, err := NewWriter(&buf, WithEncoderZstdLevel(16), WithWindowSize(1<<27), d.option())
		if err != nil {
			t.Fatal(err)
		}
		for p := 0; p < len(src); p += 1 << 20 {
			if _, err := e.Write(src[p:min(p+1<<20, len(src))]); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if *seqs == 0 {
			t.Fatalf("%s: Writer: the LDM produced no sequences", d.name)
		}
		want := creftest.Run(t, src, append([]string{"stream-pipe", "-chunk-size", strconv.Itoa(1 << 20), "-level", "16",
			"-checksum", "1", "-window-log", "27"}, dargs...)...)
		sameFrames(t, d, buf.Bytes(), want)

		if big == nil {
			continue
		}
		*seqs = 0
		got := newTestEncoder(t, WithEncoderZstdLevel(22), d.option()).EncodeAll(big, nil)
		if *seqs == 0 {
			t.Fatalf("%s: EncodeAll: the LDM produced no sequences", d.name)
		}
		want = creftest.Run(t, big, append([]string{"compress", "-level", "22", "-checksum", "1", "-pledged", "1"}, dargs...)...)
		sameFrames(t, d, got, want)
	}
	if attached[true] == 0 || (big != nil && attached[false] == 0) {
		t.Errorf("CDict attached %d times, copied %d times", attached[true], attached[false])
	}
}

// TestLDMExplicitCref enables long-distance matching explicitly (C:
// ZSTD_c_enableLongDistanceMatching, cref -ldm 1; not exposed by the
// Encoder) at levels whose strategies are below btopt, where
// ZSTD_ldm_blockCompress stores the LDM's sequences itself and runs the
// block compressor on the literals between them (maybeSplitSequence,
// ZSTD_ldm_limitTableUpdate, ZSTD_ldm_fillFastTables), and at btopt and
// btultra2 with small windows. The window log defaults to 27
// (ZSTD_LDM_DEFAULT_WINDOW_LOG), which compress2 shrinks to the input.
// compress2 and streaming must equal C's frames, with LDM sequences;
// streaming with window log 20 also runs the LDM's extDict search
// (ZSTD_count_2segments, ZSTD_ldm_countBackwardsMatch_2segments).
func TestLDMExplicitCref(t *testing.T) {
	src, small := ldmInput(3<<20), ldmInput(600<<10)
	levels := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 11, 12, 13, 15, 16, 19}
	if testing.Short() {
		levels = []int{1, 3, 5, 7, 11, 13, 16}
	}
	for _, level := range levels {
		for _, in := range [][]byte{src, small} {
			c := compress2Case{level: level, checksum: level%2 == 0}
			seqs := countLDM(t)
			cc := c.newCCtx(t)
			cc.requestedParams.ldmParams.enableLdm = psEnable
			got, err := cc.compress2(nil, in)
			if err != nil {
				t.Fatalf("%s: %v", c, err)
			}
			if *seqs == 0 {
				t.Fatalf("%s/%d: the LDM produced no sequences", c, len(in))
			}
			want := creftest.Run(t, in, append(c.crefArgs("compress"), "-ldm", "1")...)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s/%d/ldm1: frame differs from C (Go %d bytes, C %d bytes)%s", c, len(in), len(got), len(want), firstDiff(got, want))
			}
		}
		// Window log 27, and 20: C's input buffer (window + block) wraps,
		// so the LDM window (and the match state's) gets an extDict.
		for _, sc := range []streamCase{{level: level, ldm: psEnable, flushEvery: 3}, {level: level, windowLog: 20, ldm: psEnable, chunk: 100000}} {
			seqs, ext := countLDMExt(t)
			got := cctxStream(t, sc, src)
			if *seqs == 0 || (sc.windowLog == 20 && *ext == 0) {
				t.Fatalf("%v: %d LDM sequences, %d with an extDict", sc, *seqs, *ext)
			}
			want := creftest.Run(t, src, sc.crefArgs(len(src))...)
			if !bytes.Equal(got, want) {
				t.Fatalf("%v: frame differs from C (Go %d bytes, C %d bytes)%s", sc, len(got), len(want), firstDiff(got, want))
			}
		}
	}
}

// TestLDMCompressUsingCDictCref requires compressUsingCDict (the trainers'
// ZSTD_compress_usingCDict) at level 22 on 65 MiB with copies of the
// dictionary to equal `cref compress-cdict`: above the CDict-parameters
// cutoff C reloads the dictionary content into the context, and with LDM
// on also into the LDM's window and hash table (ZSTD_loadDictionaryContent's
// ZSTD_ldm_fillHashTable). Full mode only.
func TestLDMCompressUsingCDictCref(t *testing.T) {
	if testing.Short() {
		t.Skip("65 MiB at level 22")
	}
	base := ldmInput(65 << 20)
	for _, d := range testDicts(t)[1:3] {
		src := slices.Clone(base)
		for p := 0; p+len(d.dict) <= len(src); p += 9 << 20 {
			copy(src[p:], d.dict)
		}
		seqs := countLDM(t)
		cd, err := createCDictLevel(d.dict, 22)
		if err != nil {
			t.Fatal(err)
		}
		c := newCCtx()
		dst := make([]byte, compressBound(len(src)))
		n, err := c.compressUsingCDict(dst, src, cd)
		if err != nil {
			t.Fatal(err)
		}
		if c.appliedParams.ldmParams.enableLdm != psEnable || c.ldmState.loadedDictEnd == 0 || *seqs == 0 {
			t.Fatalf("%s: LDM %v, LDM loadedDictEnd %d, %d LDM sequences", d.name,
				c.appliedParams.ldmParams.enableLdm, c.ldmState.loadedDictEnd, *seqs)
		}
		args := []string{"compress-cdict", "-level", "22"}
		want := creftest.Run(t, src, append(args, d.crefArgs(t)[:2]...)...)
		if got := dst[:n]; !bytes.Equal(got, want) {
			t.Fatalf("%s: frame differs from C (Go %d bytes, C %d bytes)%s", d.name, len(got), len(want), firstDiff(got, want))
		}
	}
}

// TestLDMHugePages checks that WithHugePages backs the LDM hash table with
// a huge-page mapping (where supported) without changing the output.
func TestLDMHugePages(t *testing.T) {
	src := ldmInput(4 << 20)
	var outs [2][]byte
	for i, huge := range []bool{false, true} {
		var buf bytes.Buffer
		e, err := NewWriter(&buf, WithEncoderZstdLevel(16), WithWindowSize(1<<27), WithHugePages(huge))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		outs[i] = buf.Bytes()
		if huge && hugePagesSupported {
			c := e.s.c
			tbl := c.ldmState.hashTable
			if c.blockState.matchState.huge == nil || c.blockState.matchState.huge.mem[hugeLdm] == nil ||
				uintptr(unsafe.Pointer(&tbl[0]))%hugePageSize != 0 {
				t.Fatalf("the LDM hash table (%d entries) is not a huge-page mapping", len(tbl))
			}
		}
	}
	if !bytes.Equal(outs[0], outs[1]) {
		t.Fatal("output differs with huge pages")
	}
}
