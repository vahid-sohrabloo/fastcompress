package zstd

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// withAsm runs fn with every registered assembly routine switched on (when
// the CPU supports it, per cpu.X86 now, so cpu.DisableBMI2 selects the
// baseline variants) or off, then restores the switches. fn must not return
// before the work it starts is done: run parallel subtests inside a t.Run
// within fn, which waits for them.
func withAsm(t testing.TB, on bool, fn func()) {
	t.Helper()
	routines := cpu.AsmRoutines()
	saved := make([]bool, len(routines))
	for i, r := range routines {
		saved[i] = *r.Use
		*r.Use = on && r.Available()
	}
	defer func() {
		for i, r := range routines {
			*r.Use = saved[i]
		}
	}()
	fn()
}

// crefTests are the tests whose output is checked against C. Running them
// with assembly on and off proves both produce C's bytes.
var crefTests = []struct {
	name string
	fn   func(*testing.T)
}{
	{"Count", TestCount},
	{"RowGetMatchMask", TestRowGetMatchMask},
	{"Compress2Cref", TestCompress2Cref},
	{"Compress2IndexResetTables", TestCompress2IndexResetTables},
	{"Compress2ReuseMultiBlock", TestCompress2ReuseMultiBlock},
	{"Compress2ReuseSmallFrames", TestCompress2ReuseSmallFrames},
	{"Compress2ReuseLargeFrames", TestCompress2ReuseLargeFrames},
	{"Compress2OverflowCorrectionBtlazy2", TestCompress2OverflowCorrectionBtlazy2},
	{"Compress2OverflowCorrectionBtopt", TestCompress2OverflowCorrectionBtopt},
	{"EncodeAllCref", TestEncodeAllCref},
	{"EncodeAllOptionsCref", TestEncodeAllOptionsCref},
	{"EntropyBlockCref", TestEntropyBlockCref},
	{"DictCref", TestDictCref},
	{"DictRowSaltCref", TestDictRowSaltCref},
	{"GreedyRowCref", TestGreedyRowCref},
	{"RowHashSaltCref", TestRowHashSaltCref},
	{"BtoptFewLiteralsCref", TestBtoptFewLiteralsCref},
	{"SplitBlockCref", TestSplitBlockCref},
	{"CrefStreaming", TestCrefStreaming},
	{"CrefStreamingChecksumOff", TestCrefStreamingChecksumOff},
	{"CrefStreamExtDict", TestCrefStreamExtDict},
	{"CrefStreamReset", TestCrefStreamReset},
	{"CrefStreamReadFrom", TestCrefStreamReadFrom},
	{"CrefStreamContentSize", TestCrefStreamContentSize},
	{"DictCCtxLevels", TestDictCCtxLevels},
	{"DictRepBeyondLoadedContent", TestDictRepBeyondLoadedContent},
	{"DictTinyRaw", TestDictTinyRaw},
	{"Compress2AliasedInputs", TestCompress2AliasedInputs},
	{"ConcurrentBlocks", TestConcurrentBlocks},
	{"AsmEquivParams", TestAsmEquivParams},
	{"RowDictMinMatch3Cref", TestRowDictMinMatch3Cref},
	{"DecompressCref", TestDecompressCref},
	{"DecompressDictCref", TestDecompressDictCref},
	{"DecompressStreamCref", TestDecompressStreamCref},
	{"DecompressBlockCref", TestDecompressBlockCref},
}

// TestAsmEquiv runs every C-reference test with the assembly routines off,
// on, on a BMI2 host on without BMI2 (the baseline-amd64 variants;
// elsewhere asm=on already ran those), and on an AVX2 host on without AVX2
// (the scalar match-length count). Each run must reproduce C's output, so
// all runs produce identical bytes.
func TestAsmEquiv(t *testing.T) {
	// Single-goroutine, and the full cref suite up to four times: over 20
	// minutes under -race for nothing the race detector checks. The other
	// CI passes run it in full.
	if raceEnabled {
		t.Skip("no goroutines to check; runs without -race")
	}
	if len(cpu.AsmRoutines()) == 0 {
		t.Log("no assembly routines in this build; running the Go bodies only")
	}
	run := func(name string, on bool) {
		withAsm(t, on, func() {
			t.Run(name, func(t *testing.T) {
				for _, c := range crefTests {
					t.Run(c.name, c.fn)
				}
			})
		})
	}
	run("asm=off", false)
	if len(cpu.AsmRoutines()) == 0 {
		return
	}
	run("asm=on", true)
	// Without the feature, asm=on already ran the fallback (always so off amd64).
	if cpu.X86.HasBMI2 {
		restore := cpu.DisableBMI2()
		run("asm=on,bmi2=off", true)
		restore()
	}
	if cpu.X86.HasAVX2 {
		restore := cpu.DisableAVX2()
		run("asm=on,avx2=off", true)
		restore()
	}
}

// TestAsmEquivMatchLengths compresses inputs made of one random block
// repeated with a changed byte every n bytes, for n around the count
// loops' 8- and 32-byte steps, at every EncoderLevel with the assembly off,
// on and (on an AVX2 host) on without AVX2. The frames must be equal and
// decode with C.
func TestAsmEquivMatchLengths(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	unit := make([]byte, 4096)
	for i := range unit {
		unit[i] = byte(r.IntN(256))
	}
	var lens []int
	for n := 4; n <= 140; n++ {
		lens = append(lens, n)
	}
	lens = append(lens, 255, 256, 257, 1000, 4095)
	for _, n := range lens {
		src := make([]byte, 0, 64<<10)
		for len(src) < cap(src) {
			src = append(src, unit...)
		}
		for i := n; i < len(src); i += n + 1 + r.IntN(3) {
			src[i]++
		}
		for _, lv := range encoderLevels {
			e := newTestEncoder(t, WithEncoderLevel(lv.l))
			var off, on, noAVX2 []byte
			withAsm(t, false, func() { off = e.EncodeAll(src, nil) })
			withAsm(t, true, func() { on = e.EncodeAll(src, nil) })
			if cpu.X86.HasAVX2 {
				restore := cpu.DisableAVX2()
				withAsm(t, true, func() { noAVX2 = e.EncodeAll(src, nil) })
				restore()
			} else {
				noAVX2 = on
			}
			if !bytes.Equal(off, on) || !bytes.Equal(off, noAVX2) {
				t.Fatalf("n %d L%d: assembly changes the output (off %d, on %d, avx2=off %d bytes)",
					n, lv.c, len(off), len(on), len(noAVX2))
			}
			if got := creftest.DecodeC(t, on); !bytes.Equal(got, src) {
				t.Fatalf("n %d L%d: C decode mismatch", n, lv.c)
			}
		}
	}
}

// TestAsmEquivRepLongLength reaches the fast and double-fast loops'
// repcode-loop store of a match longer than 0xFFFF+MINMATCH, which must
// set the long-length marker. The input is a 66 KB random block W, 300
// random bytes, then twice (W[:30], K, W[70:]), with K 40 random bytes seen
// at the start. That gives a match, a match at another offset, then a ~66 KB
// continuation at the previous offset (rep2). It runs at levels 1 and 3,
// with the assembly off and on; the frames must be equal and decode with C.
func TestAsmEquivRepLongLength(t *testing.T) {
	for seed := uint64(0); seed < 8; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		rnd := func(n int) []byte {
			b := make([]byte, n)
			for i := range b {
				b[i] = byte(r.IntN(256))
			}
			return b
		}
		w := rnd(66 << 10)
		k := rnd(40)
		src := append(append(append([]byte{}, k...), w...), rnd(300)...)
		for range 2 {
			src = append(src, w[:30]...)
			src = append(src, k...)
			src = append(src, w[70:]...)
		}
		for _, lv := range encoderLevels[:2] {
			e := newTestEncoder(t, WithEncoderLevel(lv.l))
			var off, on []byte
			withAsm(t, false, func() { off = e.EncodeAll(src, nil) })
			withAsm(t, true, func() { on = e.EncodeAll(src, nil) })
			if !bytes.Equal(off, on) {
				t.Fatalf("seed %d L%d: assembly changes the output (off %d, on %d bytes)", seed, lv.c, len(off), len(on))
			}
			if got := creftest.DecodeC(t, on); !bytes.Equal(got, src) {
				t.Fatalf("seed %d L%d: C decode mismatch", seed, lv.c)
			}
		}
	}
}

// FuzzEncodeAsmEquiv compresses src with the assembly routines on and off;
// the frames must be equal and decode with C to src. Every input goes
// through the four EncoderLevels (EncodeAll), and through a cctx at C level
// 1+level%22 (every ported strategy, small-input parameter tables) with,
// per mode bit, a raw-content dictionary (bit 0: its first 1-32 KiB of
// dickens, as a loaded dictionary sets isDictionary for the row finder) and
// a small window (bit 1: windowLog 10-17, so longer inputs overflow the
// window).
func FuzzEncodeAsmEquiv(f *testing.F) {
	i := 0
	for _, in := range creftest.Inputs(f) {
		if len(in) <= 1<<17 {
			f.Add(uint8(i), uint8(i*5), in)
			i++
		}
	}
	dictSrc := readTestdata(f, "dickens-32k.raw")
	f.Add(uint8(1), uint8(6), dictSrc[:20000]) // level 7, dictionary
	f.Add(uint8(3), uint8(10), dictSrc)        // level 11, dictionary, small window
	f.Add(uint8(2), uint8(5), dictSrc)         // level 6, small window
	encs := make([]*Encoder, len(encoderLevels))
	for i, lv := range encoderLevels {
		encs[i] = newTestEncoder(f, WithEncoderLevel(lv.l), WithZeroFrames(true))
	}
	f.Fuzz(func(t *testing.T, mode, level uint8, src []byte) {
		if len(src) > 1<<18 {
			return
		}
		for i, e := range encs {
			var off, on []byte
			withAsm(t, false, func() { off = e.EncodeAll(src, nil) })
			withAsm(t, true, func() { on = e.EncodeAll(src, nil) })
			if !bytes.Equal(off, on) {
				t.Fatalf("L%d: assembly changes the output (%d vs %d bytes)", encoderLevels[i].c, len(on), len(off))
			}
			if got := creftest.DecodeC(t, on); !bytes.Equal(got, src) {
				t.Fatalf("L%d: C decode mismatch", encoderLevels[i].c)
			}
		}

		lv := 1 + int(level)%22
		var dict []byte
		if mode&1 != 0 {
			dict = dictSrc[:1024*(1+int(mode>>2)%32)]
		}
		var wlog uint32
		if mode&2 != 0 {
			wlog = 10 + uint32(mode>>2)%8
		}
		compress := func() ([]byte, error) {
			c := newCCtx()
			c.setLevel(lv)
			if err := c.setWindowLog(wlog); err != nil {
				t.Fatal(err)
			}
			if dict != nil {
				if err := c.loadDictionary(dict, dctRawContent); err != nil {
					t.Fatal(err)
				}
			}
			return c.compress2(nil, src)
		}
		var off, on []byte
		var errOff, errOn error
		withAsm(t, false, func() { off, errOff = compress() })
		withAsm(t, true, func() { on, errOn = compress() })
		if (errOff != nil) != (errOn != nil) {
			t.Fatalf("L%d mode %d: error with asm off %v, on %v", lv, mode, errOff, errOn)
		}
		if errOff != nil {
			return // a strategy this port does not have
		}
		if !bytes.Equal(off, on) {
			t.Fatalf("cctx L%d mode %d: assembly changes the output (%d vs %d bytes)", lv, mode, len(on), len(off))
		}
		var got []byte
		if dict != nil {
			var err error
			if got, err = creftest.DecodeCDict(t, on, dict); err != nil {
				t.Fatalf("cctx L%d mode %d: C decode: %v", lv, mode, err)
			}
		} else {
			got = creftest.DecodeC(t, on)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("cctx L%d mode %d: C decode mismatch", lv, mode)
		}

		// With a small window and no dictionary, also stream src: the
		// context's input buffer (window + block) wraps, so the blocks
		// after the first window run the extDict compressors.
		if wlog != 0 && dict == nil {
			sc := streamCase{level: lv, windowLog: wlog, chunk: 1 + int(mode>>2)*997}
			var off, on []byte
			withAsm(t, false, func() { off = cctxStream(t, sc, src) })
			withAsm(t, true, func() { on = cctxStream(t, sc, src) })
			if !bytes.Equal(off, on) {
				t.Fatalf("stream %v: assembly changes the output (%d vs %d bytes)", sc, len(on), len(off))
			}
			if got := creftest.DecodeC(t, on); !bytes.Equal(got, src) {
				t.Fatalf("stream %v: C decode mismatch", sc)
			}
		}
	})
}

// TestAsmEquivStreamExtDict streams inputs of eight windows through a
// context at every level from 1 to 19, with windows of 2^10 to 2^17, so
// that its input buffer (window + block) wraps and the extDict block
// compressors run (C: ZSTD_compressBlock_*_extDict), the extDict starting
// at changing offsets of the buffer and matches crossing the
// extDict/prefix boundary. The frames with the assembly off and on must
// be equal and decode with C, and with the assembly on the extDict blocks
// must have run it (every extDict block of fast, double-fast and the row
// lazy parsers runs its match loop in assembly, the binary-tree ones call
// it per search; matchState.asmLoopRuns + asmCalls count both, the noDict
// blocks of the first window included).
func TestAsmEquivStreamExtDict(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	r := rand.New(rand.NewPCG(21, 22))
	windowLogs := []uint32{10, 12, 14, 17}
	levels := 19
	if testing.Short() {
		windowLogs = []uint32{10, 14}
		levels = 16
	}
	for _, wlog := range windowLogs {
		// Text with repeats from a window or more back (into the extDict),
		// random runs, and runs copied across the window's size.
		size := 8 << wlog
		src := make([]byte, 0, size)
		for len(src) < size { // not cap(src): an append past it makes cap grow
			switch r.IntN(4) {
			case 0:
				for range 1 + r.IntN(512) {
					src = append(src, byte(r.Uint32()))
				}
			case 1:
				if len(src) > 1<<wlog {
					from := len(src) - (1 << wlog) - r.IntN(256) + r.IntN(512)
					from = max(0, min(from, len(src)-1))
					n := min(1+r.IntN(2<<wlog), len(src)-from)
					src = append(src, src[from:from+n]...)
				}
			default:
				chunk := text[r.IntN(len(text)/2):]
				src = append(src, chunk[:min(len(chunk), 1+r.IntN(4096))]...)
			}
		}
		src = src[:8<<wlog]
		for lv := 1; lv <= levels; lv++ {
			sc := streamCase{level: lv, windowLog: wlog}
			var off, on []byte
			var runs int
			stream := func() []byte {
				c := newCCtx()
				c.setLevel(lv)
				if err := c.setWindowLog(wlog); err != nil {
					t.Fatal(err)
				}
				var res []byte
				buf := make([]byte, 0, cStreamOutSize)
				pos := 0
				for _, n := range sc.chunks(len(src)) {
					op := eContinue
					in := src[pos : pos+n]
					pos += n
					if pos == len(src) {
						op = eEnd
					}
					for {
						out, consumed, remaining, err := c.compressStream2(buf, in, op)
						if err != nil {
							t.Fatalf("%v: %v", sc, err)
						}
						res = append(res, out...)
						in = in[consumed:]
						if op == eContinue && len(in) == 0 || op != eContinue && remaining == 0 {
							break
						}
					}
				}
				ms := &c.blockState.matchState
				runs = ms.asmLoopRuns + ms.asmCalls
				return res
			}
			var ext extDictRuns
			ext.install(t)
			withAsm(t, false, func() { off = stream() })
			ext.take()
			withAsm(t, true, func() { on = stream() })
			extBlocks := 0
			for _, n := range ext.take() {
				extBlocks += n
			}
			if !bytes.Equal(off, on) {
				t.Fatalf("%v: assembly changes the output (%d vs %d bytes)%s", sc, len(on), len(off), firstDiff(on, off))
			}
			if got := creftest.DecodeC(t, on); !bytes.Equal(got, src) {
				t.Fatalf("%v: C decode mismatch", sc)
			}
			if len(cpu.AsmRoutines()) > 0 && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") && extBlocks > 0 && runs < extBlocks {
				t.Fatalf("%v: %d extDict blocks, but the assembly ran only %d times", sc, extBlocks, runs)
			}
		}
	}
}

// TestAsmEquivParams compresses one input with explicit compression
// parameters that no level table reaches (every strategy from fast to
// btultra2 with minMatch 3 to 7, so the hash-chain and binary-tree assembly
// run with mls 6, and random table sizes; hash chain or row for the lazy
// strategies), each without and with a raw-content dictionary loaded into
// the context, and requires C's frame (cref -cparams/-row). The inputs
// with a dictionary are over 32 KiB, so C copies the dictionary's tables
// (their blocks then run the Go extDict code until the window has left
// the dictionary; TestHcAsmLazyEquiv and TestBtAsmBlockEquiv cover the
// assembly's isDictionary bound); small windows over the 300 KiB input
// raise lowLimit above the first index. As a crefTests entry it runs with
// the assembly off, on, without BMI2 and without AVX2. With the assembly
// on, the hash-chain and binary-tree cases without a dictionary must have
// run it, and (from 4 KiB of input) so must every strategy's assembly
// match loop (matchState.asmLoopRuns).
func TestAsmEquivParams(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	r := rand.New(rand.NewPCG(11, 12))
	src := make([]byte, 0, 300<<10)
	for len(src) < 300<<10 {
		if r.IntN(16) == 0 {
			for range 1024 + r.IntN(4096) {
				src = append(src, byte(r.Uint32()))
			}
		}
		chunk := text[r.IntN(len(text)/2):]
		chunk = chunk[:min(len(chunk), 1+r.IntN(4096))]
		src = append(src, chunk...)
		if r.IntN(4) == 0 {
			src = append(src, byte(r.Uint32()))
		}
	}
	type pcase struct {
		cp   compressionParameters
		row  paramSwitch
		n    int
		dict []byte
	}
	var cases []pcase
	for s := zstdFast; s <= zstdBtultra2; s++ {
		for mm := uint32(3); mm <= 7; mm++ {
			for _, row := range []paramSwitch{psDisable, psEnable} {
				if row == psEnable && (s < zstdGreedy || s > zstdLazy2) {
					continue
				}
				for _, withDict := range []bool{false, true} {
					wl := uint32(10 + r.IntN(7))
					c := pcase{cp: compressionParameters{
						windowLog: wl, chainLog: uint32(6 + r.IntN(int(wl)-5)), hashLog: uint32(6 + r.IntN(int(wl)-5)),
						searchLog: uint32(1 + r.IntN(7)), minMatch: mm, targetLength: uint32(r.IntN(64)), strategy: s,
					}, row: row, n: 1 + r.IntN(len(src))}
					if withDict {
						c.dict = text[:1024*(4+r.IntN(29))]
						c.n = 40<<10 + r.IntN(len(src)-40<<10) // > 32 KiB: C copies the dictionary
					}
					cases = append(cases, c)
				}
			}
		}
	}
	asmOn := func(name string) bool {
		for _, a := range cpu.AsmRoutines() {
			if a.Name == name {
				return *a.Use
			}
		}
		return false
	}
	for _, c := range cases {
		cc := newCCtx()
		cc.setLevel(3)
		cc.requestedParams.cParams = c.cp
		cc.requestedParams.useRowMatchFinder = c.row
		args := []string{"compress", "-level", "3", "-pledged", "1", "-row", strconv.Itoa(int(c.row)), "-cparams",
			fmt.Sprintf("%d,%d,%d,%d,%d,%d,%d", c.cp.windowLog, c.cp.chainLog, c.cp.hashLog, c.cp.searchLog,
				c.cp.minMatch, c.cp.targetLength, c.cp.strategy)}
		if c.dict != nil {
			if err := cc.loadDictionary(c.dict, dctRawContent); err != nil {
				t.Fatal(err)
			}
			args = append(args, dictCase{dict: c.dict, raw: true}.crefArgs(t)...)
		}
		got, err := cc.compress2(nil, src[:c.n])
		if err != nil {
			t.Fatalf("%+v: %v", c.cp, err)
		}
		if want := creftest.Run(t, src[:c.n], args...); !bytes.Equal(got, want) {
			t.Fatalf("%+v row %d n %d dict %d: the frame differs from C's%s", c.cp, c.row, c.n, len(c.dict), firstDiff(got, want))
		}
		hc := c.cp.strategy >= zstdGreedy && c.cp.strategy <= zstdLazy2 && c.row == psDisable && asmOn("hc")
		bt := c.cp.strategy == zstdBtlazy2 && asmOn("dubt") || c.cp.strategy >= zstdBtopt && (c.cp.minMatch > 3 || btAsmMls3) && asmOn("dubt")
		if (hc || bt) && c.dict == nil && cc.blockState.matchState.asmCalls+cc.blockState.matchState.asmLoopRuns == 0 {
			t.Fatalf("%+v row %d n %d dict %d: the assembly never ran", c.cp, c.row, c.n, len(c.dict))
		}
		// The block-level match loops (fast, double-fast, the lazy parsers,
		// the optimal parser) must have run too, not only the finders.
		lazy := c.cp.strategy >= zstdGreedy && c.cp.strategy <= zstdLazy2
		loop := c.cp.strategy == zstdFast && asmOn("fast") || c.cp.strategy == zstdDfast && asmOn("doublefast") ||
			lazy && c.row == psDisable && asmOn("hc") || lazy && c.row == psEnable && asmOn("row") ||
			c.cp.strategy == zstdBtlazy2 && asmOn("dubt") || c.cp.strategy == zstdBtopt && (c.cp.minMatch > 3 || btAsmMls3) && asmOn("opt")
		if loop && c.dict == nil && c.n >= 4<<10 && cc.blockState.matchState.asmLoopRuns == 0 {
			t.Fatalf("%+v row %d n %d: the assembly match loop never ran", c.cp, c.row, c.n)
		}
	}
}
