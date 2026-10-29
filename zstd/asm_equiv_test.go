package zstd

import (
	"bytes"
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
}

// TestAsmEquiv runs every C-reference test with the assembly routines off,
// on, and on without BMI2 (the baseline-amd64 variants). Each run must
// reproduce C's output, so all three produce identical bytes.
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
	restore := cpu.DisableBMI2()
	defer restore()
	run("asm=on,bmi2=off", true)
}

// FuzzEncodeAsmEquiv compresses src at every level with the assembly
// routines on and off; the frames must be equal and decode with C to src.
func FuzzEncodeAsmEquiv(f *testing.F) {
	for _, in := range creftest.Inputs(f) {
		if len(in) <= 1<<17 {
			f.Add(in)
		}
	}
	encs := make([]*Encoder, len(encoderLevels))
	for i, lv := range encoderLevels {
		encs[i] = newTestEncoder(f, WithEncoderLevel(lv.l), WithZeroFrames(true))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
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
	})
}
