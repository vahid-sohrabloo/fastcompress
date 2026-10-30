package zstd

import (
	"bytes"
	"math/rand/v2"
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
