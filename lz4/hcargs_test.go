package lz4

import (
	"testing"
	"unsafe"
)

// TestHCChainArgsLayout pins the hcChainArgs offsets lz4hc_amd64.s uses.
func TestHCChainArgsLayout(t *testing.T) {
	var a hcChainArgs
	want := []struct {
		name string
		off  uintptr
		got  uintptr
	}{
		{"win", 0, unsafe.Offsetof(a.win)}, {"chain", 8, unsafe.Offsetof(a.chain)},
		{"ip", 16, unsafe.Offsetof(a.ip)}, {"iLowLimit", 24, unsafe.Offsetof(a.iLowLimit)},
		{"iHighLimit", 32, unsafe.Offsetof(a.iHighLimit)}, {"prefixIdx", 40, unsafe.Offsetof(a.prefixIdx)},
		{"lowest", 48, unsafe.Offsetof(a.lowest)}, {"ipIndex", 56, unsafe.Offsetof(a.ipIndex)},
		{"pattern", 64, unsafe.Offsetof(a.pattern)}, {"maxLength", 72, unsafe.Offsetof(a.maxLength)},
		{"pa", 80, unsafe.Offsetof(a.pa)}, {"matchIndex", 88, unsafe.Offsetof(a.matchIndex)},
		{"nbAttempts", 96, unsafe.Offsetof(a.nbAttempts)}, {"longest", 104, unsafe.Offsetof(a.longest)},
		{"offset", 112, unsafe.Offsetof(a.offset)}, {"sBack", 120, unsafe.Offsetof(a.sBack)},
		{"hashTable", 128, unsafe.Offsetof(a.hashTable)}, {"next", 136, unsafe.Offsetof(a.next)},
		{"insert", 144, unsafe.Offsetof(a.insert)}, {"chainSwap", 152, unsafe.Offsetof(a.chainSwap)},
		{"favor", 160, unsafe.Offsetof(a.favor)}, {"chainPos", 168, unsafe.Offsetof(a.chainPos)},
	}
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layout only")
	}
	for _, w := range want {
		if w.got != w.off {
			t.Errorf("%s at %d, assembly expects %d", w.name, w.got, w.off)
		}
	}
}
