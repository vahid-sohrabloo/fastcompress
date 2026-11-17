package cpu

import "testing"

func TestDisableBMI2(t *testing.T) {
	saved := X86.HasBMI2
	defer func() { X86.HasBMI2 = saved }()
	X86.HasBMI2 = true
	restore := DisableBMI2()
	if X86.HasBMI2 {
		t.Fatal("HasBMI2 still set")
	}
	restore()
	if !X86.HasBMI2 {
		t.Fatal("HasBMI2 not restored")
	}
}

func TestDisableAVX2(t *testing.T) {
	saved := X86.HasAVX2
	defer func() { X86.HasAVX2 = saved }()
	X86.HasAVX2 = true
	restore := DisableAVX2()
	if X86.HasAVX2 {
		t.Fatal("HasAVX2 still set")
	}
	restore()
	if !X86.HasAVX2 {
		t.Fatal("HasAVX2 not restored")
	}
}

func TestDetect(t *testing.T) {
	t.Logf("%+v", X86)
}

func TestSetAsmMode(t *testing.T) {
	savedX86, savedRoutines, savedOff, savedDetected := X86, asmRoutines, asmOff, detectedBMI2
	defer func() { X86, asmRoutines, asmOff, detectedBMI2 = savedX86, savedRoutines, savedOff, savedDetected }()
	asmRoutines, asmOff = nil, false
	X86.HasBMI2, detectedBMI2 = true, true
	var base, bmi2, late bool
	RegisterAsm("base", &base, func() bool { return true })
	RegisterAsm("bmi2", &bmi2, func() bool { return X86.HasBMI2 })
	if !base || !bmi2 {
		t.Fatal("registered switches not on")
	}

	if err := SetAsmMode("nobmi2"); err != nil {
		t.Fatal(err)
	}
	if X86.HasBMI2 || !base || bmi2 {
		t.Fatalf("nobmi2: HasBMI2=%v base=%v bmi2=%v, want false true false", X86.HasBMI2, base, bmi2)
	}

	if err := SetAsmMode("off"); err != nil {
		t.Fatal(err)
	}
	RegisterAsm("late", &late, func() bool { return true }) // registered after the mode is set
	if base || bmi2 || late {
		t.Fatalf("off: base=%v bmi2=%v late=%v, want all false", base, bmi2, late)
	}

	for _, mode := range []string{"on", "nobmi2", ""} {
		if err := SetAsmMode(mode); err != nil {
			t.Fatal(err)
		}
		want := mode != "nobmi2"
		if X86.HasBMI2 != want || !base || bmi2 != want || !late {
			t.Fatalf("%q after nobmi2: HasBMI2=%v base=%v bmi2=%v late=%v, want %v true %v true",
				mode, X86.HasBMI2, base, bmi2, late, want, want)
		}
	}

	if err := SetAsmMode("bogus"); err == nil {
		t.Fatal("bogus mode accepted")
	}
}
