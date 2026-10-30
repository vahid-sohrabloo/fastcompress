package cpu

import "testing"

func TestDisableBMI2(t *testing.T) {
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
