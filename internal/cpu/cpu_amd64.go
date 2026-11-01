//go:build amd64 && !appengine && !noasm && !purego && gc

package cpu

// cpuid executes CPUID with the given EAX and ECX inputs.
//
//go:noescape
func cpuid(eaxArg, ecxArg uint32) (eax, ebx, ecx, edx uint32)

// xgetbv returns XCR0 (XGETBV with ECX = 0). Only valid when OSXSAVE is set.
func xgetbv() (eax, edx uint32)

func init() {
	maxID, _, _, _ := cpuid(0, 0)
	if maxID < 7 {
		return
	}
	_, _, ecx1, _ := cpuid(1, 0)
	const (
		popcnt  = 1 << 23
		osxsave = 1 << 27
		avx     = 1 << 28
	)
	X86.HasPOPCNT = ecx1&popcnt != 0
	osYMM := false
	if ecx1&osxsave != 0 && ecx1&avx != 0 {
		xcr0, _ := xgetbv()
		osYMM = xcr0&6 == 6 // XMM and YMM state enabled by the OS
	}
	_, ebx7, _, _ := cpuid(7, 0)
	X86.HasBMI1 = ebx7&(1<<3) != 0
	X86.HasAVX2 = ebx7&(1<<5) != 0 && osYMM
	X86.HasBMI2 = ebx7&(1<<8) != 0
}
