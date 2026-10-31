package zstd

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
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
}

// TestAsmEquiv runs every C-reference test with the assembly routines off,
// on, on a BMI2 host on without BMI2 (the baseline-amd64 variants;
// elsewhere asm=on already ran those), and on an AVX2 host on without AVX2
// (the scalar match-length count). Each run must reproduce C's output, so
// all runs produce identical bytes.
func TestAsmEquiv(t *testing.T) {
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
	})
}

// TestAsmEquivParams compresses one input with explicit compression
// parameters that no level table reaches (every strategy from fast to
// btopt with minMatch 3 to 7, so the hash-chain and binary-tree assembly
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
// run it.
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
	for s := zstdFast; s <= zstdBtopt; s++ {
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
		bt := c.cp.strategy == zstdBtlazy2 && asmOn("dubt") || c.cp.strategy == zstdBtopt && c.cp.minMatch > 3 && asmOn("dubt")
		if (hc || bt) && c.dict == nil && cc.blockState.matchState.asmCalls == 0 {
			t.Fatalf("%+v row %d n %d dict %d: the assembly never ran", c.cp, c.row, c.n, len(c.dict))
		}
	}
}
