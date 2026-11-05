//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

package huf

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// TestHUFFastArgsLayout checks the offsets huf_decompress_{amd64,arm64}.s use:
// fastArgs has C's HUF_DecompressFastArgs layout (plus dst), and the
// DTable's x1 and x2.
func TestHUFFastArgsLayout(t *testing.T) {
	var a fastArgs
	var dt DTable
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"fastArgs.ip", unsafe.Offsetof(a.ip), 0},
		{"fastArgs.op", unsafe.Offsetof(a.op), 32},
		{"fastArgs.bits", unsafe.Offsetof(a.bits), 64},
		{"fastArgs.dt", unsafe.Offsetof(a.dt), 96},
		{"fastArgs.ilowest", unsafe.Offsetof(a.ilowest), 104},
		{"fastArgs.oend", unsafe.Offsetof(a.oend), 112},
		{"fastArgs.iend", unsafe.Offsetof(a.iend), 120},
		{"fastArgs.dst", unsafe.Offsetof(a.dst), 152},
		{"DTable.x1", unsafe.Offsetof(dt.x1), 4},
		{"DTable.x2", unsafe.Offsetof(dt.x2), 8196},
	} {
		if c.got != c.want {
			t.Errorf("%s at %d, want %d", c.name, c.got, c.want)
		}
	}
}

// withHufAsm runs fn with the fast-loop assembly on (where
// hufFastAsmAvailable, as cpu.X86 is now, allows it: BMI2 on amd64, always
// on arm64) or off, then restores the switches.
func withHufAsm(on bool, fn func()) {
	s1, s2 := useHuf4X1Asm, useHuf4X2Asm
	useHuf4X1Asm = on && hufFastAsmAvailable()
	useHuf4X2Asm = on && hufFastAsmAvailable()
	defer func() { useHuf4X1Asm, useHuf4X2Asm = s1, s2 }()
	fn()
}

// readFastTable reads table as an X1 (x2 false) or X2 table into a fresh
// DTable; ok reports success with tableLog 11 (the fast loops' only one).
// It fails t if an X2 table breaks the X2 assembly's TABLE INVARIANT.
func readFastTable(t *testing.T, table []byte, x2 bool) (dt *DTable, hSize int, ok bool) {
	t.Helper()
	dt = new(DTable)
	dt.Init(TableLogMax)
	var err error
	if x2 {
		hSize, err = ReadDTableX2(dt, table, 0)
	} else {
		hSize, err = ReadDTableX1(dt, table, 0)
	}
	ok = err == nil && dt.desc.tableLog == decoderFastTableLog
	if ok && x2 {
		if v := x2TableViolation(dt); v != "" {
			t.Fatalf("X2 table breaks the asm's TABLE INVARIANT: %s", v)
		}
	}
	return dt, hSize, ok
}

// x2TableViolation describes the first entry of dt.x2[:2048] that breaks
// the TABLE INVARIANT of huf_decompress_{amd64,arm64}.s (length 1 or 2, nbBits <=
// 11), or returns "".
func x2TableViolation(dt *DTable) string {
	for i, e := range dt.x2[:1<<decoderFastTableLog] {
		if length, nbBits := e>>24, (e>>16)&0xFF; length < 1 || length > 2 || nbBits > decoderFastTableLog {
			return fmt.Sprintf("x2[%d] = %#08x: length %d, nbBits %d", i, e, length, nbBits)
		}
	}
	return ""
}

// TestHUFX2TableInvariant checks the X2 TABLE INVARIANT on every
// tableLog-11 X2 table built from the test streams, and that
// x2TableViolation catches tables that break it.
func TestHUFX2TableInvariant(t *testing.T) {
	var good *DTable
	for _, s := range hufStreams(t) {
		if dt, _, ok := readFastTable(t, s.comp, true); ok {
			good = dt
		}
	}
	if good == nil {
		t.Fatal("no tableLog-11 X2 table among the test streams")
	}
	for _, m := range []struct {
		name   string
		mutate func(e uint32) uint32
	}{
		{"length3", func(e uint32) uint32 { return e&0x00FFFFFF | 3<<24 }},
		{"length0", func(e uint32) uint32 { return 0 }}, // an unfilled entry
		{"nbBits12", func(e uint32) uint32 { return e&0xFF00FFFF | 12<<16 }},
	} {
		for _, i := range []int{0, 1<<decoderFastTableLog - 1} {
			bad := *good
			bad.x2[i] = m.mutate(bad.x2[i])
			if x2TableViolation(&bad) == "" {
				t.Errorf("%s at x2[%d] not caught", m.name, i)
			}
		}
	}
}

// TestHUFAsmRegistered checks the predicates registered with
// cpu.RegisterAsm: both fast loops are registered with their switches; on
// amd64 they are off without BMI2 (SetAsmMode("nobmi2")) and follow the
// detected BMI2 otherwise; on arm64 (baseline) they are always on.
func TestHUFAsmRegistered(t *testing.T) {
	want := map[string]*bool{"huf.decompress4X1Fast": &useHuf4X1Asm, "huf.decompress4X2Fast": &useHuf4X2Asm}
	found := 0
	for _, r := range cpu.AsmRoutines() {
		if p, ok := want[r.Name]; ok {
			found++
			if r.Use != p {
				t.Errorf("%s registered with the wrong switch", r.Name)
			}
		}
	}
	if found != len(want) {
		t.Fatalf("found %d of %d huf fast-loop registrations", found, len(want))
	}
	defer func() {
		// "on" first restores the detected X86.HasBMI2, which "nobmi2"
		// cleared and "off" leaves alone; then the env mode.
		if err := cpu.SetAsmMode("on"); err != nil {
			t.Fatal(err)
		}
		if err := cpu.SetAsmMode(os.Getenv("FASTZSTD_FUZZ_ASM")); err != nil {
			t.Fatal(err)
		}
	}()
	if err := cpu.SetAsmMode("on"); err != nil {
		t.Fatal(err)
	}
	on := cpu.X86.HasBMI2 || runtime.GOARCH == "arm64"
	if useHuf4X1Asm != on || useHuf4X2Asm != on {
		t.Errorf("mode on, BMI2 %v: X1 asm %v, X2 asm %v", cpu.X86.HasBMI2, useHuf4X1Asm, useHuf4X2Asm)
	}
	if err := cpu.SetAsmMode("nobmi2"); err != nil {
		t.Fatal(err)
	}
	on = runtime.GOARCH == "arm64"
	if useHuf4X1Asm != on || useHuf4X2Asm != on {
		t.Errorf("mode nobmi2: X1 asm %v, X2 asm %v, want both %v", useHuf4X1Asm, useHuf4X2Asm, on)
	}
}

// checkFastLoops runs the Go and the assembly fast loop of the X1 or X2
// decoder on data (copied to a buffer ending at a guard page and to one
// starting at one) with dst ending at a guard page, and fails unless both
// leave the same ip, op and bits and write the same dst. It returns whether
// the fast loop ran (decompressFastArgsInit accepted the input).
func checkFastLoops(t *testing.T, dt *DTable, x2 bool, data []byte, dstSize int) bool {
	t.Helper()
	if len(data) < 10 || dstSize == 0 {
		return false
	}
	var ran bool
	for _, src := range [][]byte{guardmem.After(t, len(data)), guardmem.Before(t, len(data))} {
		copy(src, data)
		var goArgs decompressFastArgs
		goDst := make([]byte, dstSize)
		if r, err := decompressFastArgsInit(&goArgs, goDst, src, dt); err != nil || r == 0 {
			return false
		}
		ran = true
		asmArgs := goArgs
		asmDst := guardmem.After(t, dstSize)
		asmArgs.dst = asmDst
		if x2 {
			decompress4X2FastCLoop(&goArgs)
			decompress4X2FastLoop(&asmArgs, dt, 0)
		} else {
			decompress4X1FastCLoop(&goArgs)
			decompress4X1FastLoop(&asmArgs, dt, 0)
		}
		if goArgs.ip != asmArgs.ip || goArgs.op != asmArgs.op || goArgs.bits != asmArgs.bits {
			t.Fatalf("x2=%v: Go ip %v op %v bits %x, asm ip %v op %v bits %x", x2,
				goArgs.ip, goArgs.op, goArgs.bits, asmArgs.ip, asmArgs.op, asmArgs.bits)
		}
		if !bytes.Equal(goDst, asmDst) {
			t.Fatalf("x2=%v: dst differs", x2)
		}
	}
	return ran
}

// decode4X runs Decompress4X{1,2}UsingDTable on a copy of dt.
func decode4X(dt *DTable, x2 bool, src []byte, dstSize int, flags int) ([]byte, error) {
	dt2 := *dt
	dst := make([]byte, dstSize)
	if x2 {
		return dst, Decompress4X2UsingDTable(dst, src, &dt2, flags)
	}
	return dst, Decompress4X1UsingDTable(dst, src, &dt2, flags)
}

// checkDecode4X fails unless the 4X decoder gives the same output and error
// with the assembly loop allowed and with FlagDisableAsm.
func checkDecode4X(t *testing.T, dt *DTable, x2 bool, src []byte, dstSize int) {
	t.Helper()
	a, ea := decode4X(dt, x2, src, dstSize, 0)
	g, eg := decode4X(dt, x2, src, dstSize, FlagDisableAsm)
	if ea != eg || !bytes.Equal(a, g) {
		t.Fatalf("x2=%v dstSize %d: asm err=%v, Go err=%v, equal=%v", x2, dstSize, ea, eg, bytes.Equal(a, g))
	}
}

// TestHUFAsmEquiv runs the 4X round-trip and fast-loop tests with the
// assembly off, on, and on with BMI2 cleared (the Go loop again), and
// compares the two loops directly on every four-stream tableLog-11 input.
func TestHUFAsmEquiv(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"X1RoundTrip", TestHUFDecompressX1RoundTrip},
		{"4X1FastVsBody", TestHUF4X1FastVsBody},
		{"4X1FastReadBelowStart", TestHUF4X1FastReadBelowStart},
		{"4X1SevenByteStream", TestHUF4X1SevenByteStream},
		{"X2RoundTrip", TestHUFDecompressX2RoundTrip},
		{"4X2FastVsBody", TestHUF4X2FastVsBody},
		{"4X2FastReadBelowStart", TestHUF4X2FastReadBelowStart},
		{"4XFastInputBound", TestHUF4XFastInputBound},
		{"EntryPoints", TestHUFDecompressEntryPoints},
	}
	run := func(name string, on bool) {
		withHufAsm(on, func() {
			t.Run(name, func(t *testing.T) {
				for _, c := range tests {
					t.Run(c.name, c.fn)
				}
			})
		})
	}
	run("asm=off", false)
	run("asm=on", true)
	if cpu.X86.HasBMI2 {
		restore := cpu.DisableBMI2()
		run("asm=on,bmi2=off", true)
		restore()
	}

	if !hufFastAsmAvailable() {
		t.Log("no BMI2: the direct loop comparison runs the Go loop twice")
	}
	withHufAsm(true, func() {
		ran := 0
		for _, s := range hufStreams(t) {
			if !s.four {
				continue
			}
			for _, x2 := range []bool{false, true} {
				dt, h, ok := readFastTable(t, s.comp, x2)
				if !ok {
					continue
				}
				data := s.comp[h:]
				for _, dstSize := range []int{len(s.src), len(s.src) - 1, len(s.src) + 7} {
					if checkFastLoops(t, dt, x2, data, dstSize) {
						ran++
					}
					checkDecode4X(t, dt, x2, data, dstSize)
				}
			}
		}
		if ran == 0 {
			t.Fatal("fast loops never ran")
		}
	})
}

// FuzzHUFAsmEquiv reads table as an X1 and as an X2 table and, for each
// that has tableLog 11, runs the Go and assembly fast loops on data with
// src and dst against guard pages (checkFastLoops), then the whole 4X
// decoder with and without FlagDisableAsm (checkDecode4X).
func FuzzHUFAsmEquiv(f *testing.F) {
	wksp := make([]byte, WorkspaceSize)
	for _, src := range [][]byte{
		bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123"), 40),
		[]byte("the quick brown fox jumps over the lazy dog, again and again and again"),
	} {
		dst := make([]byte, CompressBound(len(src)))
		rep := RepeatNone
		n, err := Compress4XRepeat(dst, src, 255, 11, wksp, nil, &rep, 0)
		if err != nil || n <= 1 {
			f.Fatalf("seed: n=%d err=%v", n, err)
		}
		var dt DTable
		dt.Init(TableLogMax)
		h, err := ReadDTableX1(&dt, dst[:n], 0)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(dst[:h], dst[h:n], uint32(len(src)))
	}
	f.Add([]byte{128 + 11, 0xA9, 0x87, 0x65, 0x43, 0x21, 0x10}, make([]byte, 4000), uint32(30000))
	f.Fuzz(func(t *testing.T, table, data []byte, dstSize uint32) {
		size := int(dstSize % (2 * BlockSizeMax))
		for _, x2 := range []bool{false, true} {
			dt, _, ok := readFastTable(t, table, x2)
			if !ok {
				continue
			}
			checkFastLoops(t, dt, x2, data, size)
			checkDecode4X(t, dt, x2, data, size)
		}
	})
}
