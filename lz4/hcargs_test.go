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

// TestHCParseArgsLayout pins the hcParseArgs offsets lz4hc_parse_amd64.s
// uses.
func TestHCParseArgsLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layout only")
	}
	var a hcParseArgs
	for _, w := range []struct {
		name     string
		off, got uintptr
	}{
		{"win", 0, unsafe.Offsetof(a.win)}, {"hashTable", 8, unsafe.Offsetof(a.hashTable)},
		{"chain", 16, unsafe.Offsetof(a.chain)}, {"prefixIdx", 24, unsafe.Offsetof(a.prefixIdx)},
		{"ip", 32, unsafe.Offsetof(a.ip)}, {"anchor", 40, unsafe.Offsetof(a.anchor)},
		{"op", 48, unsafe.Offsetof(a.op)}, {"iend", 56, unsafe.Offsetof(a.iend)},
		{"dst", 64, unsafe.Offsetof(a.dst)}, {"dstLen", 72, unsafe.Offsetof(a.dstLen)},
		{"olimit", 80, unsafe.Offsetof(a.olimit)}, {"next", 88, unsafe.Offsetof(a.next)},
		{"attempts", 96, unsafe.Offsetof(a.attempts)}, {"pa", 104, unsafe.Offsetof(a.pa)},
		{"stop", 112, unsafe.Offsetof(a.stop)}, {"suff", 120, unsafe.Offsetof(a.suff)},
		{"opt", 128, unsafe.Offsetof(a.opt)}, {"dict", 136, unsafe.Offsetof(a.dict)},
		{"lowLimit", 144, unsafe.Offsetof(a.lowLimit)}, {"mlen", 152, unsafe.Offsetof(a.mlen)},
		{"moff", 160, unsafe.Offsetof(a.moff)}, {"nbSeq", 168, unsafe.Offsetof(a.nbSeq)},
		{"dhash", 176, unsafe.Offsetof(a.dhash)}, {"dchain", 184, unsafe.Offsetof(a.dchain)},
		{"dbase", 192, unsafe.Offsetof(a.dbase)}, {"dend", 200, unsafe.Offsetof(a.dend)},
		{"dstart", 208, unsafe.Offsetof(a.dstart)},
	} {
		if w.got != w.off {
			t.Errorf("%s at %d, assembly expects %d", w.name, w.got, w.off)
		}
	}
	var o hcOptimal
	if unsafe.Sizeof(o) != 16 || unsafe.Offsetof(o.off) != 4 || unsafe.Offsetof(o.mlen) != 8 || unsafe.Offsetof(o.litlen) != 12 {
		t.Errorf("hcOptimal layout differs from the assembly's (price, off, mlen, litlen; 16 bytes)")
	}
}

// TestMidDictCtxLayout pins the midDictCtx offsets lz4mid_amd64.s uses.
func TestMidDictCtxLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layout only")
	}
	var d midDictCtx
	if unsafe.Offsetof(d.hash) != 0 || unsafe.Offsetof(d.chain) != 8 || unsafe.Offsetof(d.base) != 16 ||
		unsafe.Offsetof(d.end) != 24 || unsafe.Offsetof(d.mid) != 32 {
		t.Errorf("midDictCtx layout differs from the assembly's")
	}
}
