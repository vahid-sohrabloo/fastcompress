//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import (
	"testing"
	"unsafe"
)

// TestRowAsmParamsLayout pins the field offsets row_amd64.s uses (P_* macros).
func TestRowAsmParamsLayout(t *testing.T) {
	var p rowAsmParams
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"hashTable", unsafe.Offsetof(p.hashTable), 0},
		{"tagTable", unsafe.Offsetof(p.tagTable), 8},
		{"b", unsafe.Offsetof(p.b), 16},
		{"cache", unsafe.Offsetof(p.cache), 24},
		{"nextToUpdate", unsafe.Offsetof(p.nextToUpdate), 32},
		{"entropy", unsafe.Offsetof(p.entropy), 40},
		{"shift", unsafe.Offsetof(p.shift), 48},
		{"hShift", unsafe.Offsetof(p.hShift), 56},
		{"prime", unsafe.Offsetof(p.prime), 64},
		{"saltAdj", unsafe.Offsetof(p.saltAdj), 72},
		{"baseIdx", unsafe.Offsetof(p.baseIdx), 80},
		{"rowLog", unsafe.Offsetof(p.rowLog), 84},
		{"rowMask", unsafe.Offsetof(p.rowMask), 88},
		{"nbAttempts", unsafe.Offsetof(p.nbAttempts), 92},
		{"lowestValid", unsafe.Offsetof(p.lowestValid), 96},
		{"maxDistance", unsafe.Offsetof(p.maxDistance), 100},
		{"isDictionary", unsafe.Offsetof(p.isDictionary), 104},
		{"pfHash2", unsafe.Offsetof(p.pfHash2), 120},
		{"pfTag2", unsafe.Offsetof(p.pfTag2), 128},
	} {
		if c.got != c.want {
			t.Errorf("rowAsmParams.%s at offset %d, row_amd64.s expects %d", c.name, c.got, c.want)
		}
	}
}
