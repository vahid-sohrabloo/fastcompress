// Package cpu reports the x86 instruction-set extensions the assembly
// routines may use. It is a minimal stdlib-only stand-in for
// golang.org/x/sys/cpu: every field is false off amd64, or when assembly is
// disabled (build tags noasm, purego, appengine, or a non-gc compiler).
package cpu

import "fmt"

// X86 holds the features detected at start-up. Tests may clear them (see
// DisableBMI2 and DisableAVX2) to exercise the baseline paths.
var X86 struct {
	HasBMI1   bool // TZCNT, ANDN, ...
	HasBMI2   bool // SHLX, SHRX, BZHI, ...
	HasAVX2   bool // 256-bit integer SIMD (OS support for YMM state checked)
	HasPOPCNT bool // POPCNT
}

// DisableBMI2 clears HasBMI2 and returns a function that restores it.
func DisableBMI2() (restore func()) {
	old := X86.HasBMI2
	X86.HasBMI2 = false
	return func() { X86.HasBMI2 = old }
}

// DisableAVX2 clears HasAVX2 and returns a function that restores it.
func DisableAVX2() (restore func()) {
	old := X86.HasAVX2
	X86.HasAVX2 = false
	return func() { X86.HasAVX2 = old }
}

// AsmRoutine is an assembly routine's on/off switch, registered by the
// package that owns it so tests can compare its output with the Go body.
type AsmRoutine struct {
	Name      string
	Use       *bool       // true: the assembly runs
	Available func() bool // whether the CPU supports it (reads X86)
}

var (
	asmRoutines []AsmRoutine
	asmOff      bool // SetAsmMode("off"): switches registered later start off
)

// RegisterAsm records an assembly routine's switch and sets it to
// available() (false after SetAsmMode("off")). Packages call it from init
// in their <file>_amd64.go. A routine with BMI2 and baseline variants
// registers the BMI2 one's switch separately, with an available func that
// checks X86.HasBMI2.
func RegisterAsm(name string, use *bool, available func() bool) {
	*use = !asmOff && available()
	asmRoutines = append(asmRoutines, AsmRoutine{name, use, available})
}

// SetAsmMode sets every assembly switch, including those registered later,
// for the fuzz tests (FASTZSTD_FUZZ_ASM): "on" or "" to available(),
// "nobmi2" to available() after clearing X86.HasBMI2, "off" to false.
func SetAsmMode(mode string) error {
	switch mode {
	case "", "on":
		asmOff = false
	case "nobmi2":
		asmOff = false
		X86.HasBMI2 = false
	case "off":
		asmOff = true
	default:
		return fmt.Errorf("cpu: unknown asm mode %q (want off, nobmi2 or on)", mode)
	}
	for _, r := range asmRoutines {
		*r.Use = !asmOff && r.Available()
	}
	return nil
}

// AsmRoutines returns the registered assembly routines.
func AsmRoutines() []AsmRoutine { return asmRoutines }
