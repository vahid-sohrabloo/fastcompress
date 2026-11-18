//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

package zstd

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
	"unsafe"
)

// TestRowAsmParamsLayout pins the field offsets row_lazy_*.s and hc_lazy_*.s use (P_* macros).
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
		{"hShift", unsafe.Offsetof(p.hShift), 48},
		{"saltAdj", unsafe.Offsetof(p.saltAdj), 56},
		{"baseIdx", unsafe.Offsetof(p.baseIdx), 64},
		{"nbAttempts", unsafe.Offsetof(p.nbAttempts), 68},
		{"lowestValid", unsafe.Offsetof(p.lowestValid), 72},
		{"maxDistance", unsafe.Offsetof(p.maxDistance), 76},
		{"isDictionary", unsafe.Offsetof(p.isDictionary), 80},
		{"iLimit", unsafe.Offsetof(p.iLimit), 96},
		{"skip", unsafe.Offsetof(p.skip), 104},
		{"chainTable", unsafe.Offsetof(p.chainTable), 112},
		{"chainMask", unsafe.Offsetof(p.chainMask), 120},
		{"bt", unsafe.Offsetof(p.bt), 128},
		{"dictBase", unsafe.Offsetof(p.dictBase), 136},
		{"dictBaseIdx", unsafe.Offsetof(p.dictBaseIdx), 144},
		{"dictLimit", unsafe.Offsetof(p.dictLimit), 148},
	} {
		if c.got != c.want {
			t.Errorf("rowAsmParams.%s at offset %d, the assembly expects %d", c.name, c.got, c.want)
		}
	}
}

// TestRowAsmFinderEquiv compares, search by search, rowFindBestMatch with
// the Go rowUpdateCached and with the assembly one (which the whole-loop
// lazyRow* leaves to the dictionary finders), each on its own copy of the
// same match state. Every result and the final tables must agree.
func TestRowAsmFinderEquiv(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	r := rand.New(rand.NewPCG(7, 8))
	src := make([]byte, 0, 1<<17)
	for len(src) < 1<<17 {
		chunk := text[r.IntN(len(text)/2):]
		chunk = chunk[:min(len(chunk), 1+r.IntN(4096))]
		src = append(src, chunk...)
		if r.IntN(4) == 0 {
			src = append(src, byte(r.Uint32())) // break some matches
		}
	}
	saved := useRowAsm
	defer func() { useRowAsm = saved }()
	for _, rowLog := range []uint32{4, 5, 6} {
		for _, mls := range []uint32{4, 5, 6} {
			for _, dict := range []bool{false, true} {
				const hashLog = 14
				windowLog := uint32(10 + r.IntN(8))
				searchLog := uint32(1 + r.IntN(7))
				salt := r.Uint64()
				const baseIdx = 5
				mk := func() *matchState {
					ms := &matchState{}
					ms.window = window{base: src, baseIdx: baseIdx, dictLimit: baseIdx, lowLimit: baseIdx}
					ms.cParams = compressionParameters{windowLog: windowLog, searchLog: searchLog, minMatch: mls}
					ms.rowHashLog = hashLog - rowLog
					ms.hashTable = make([]uint32, 1<<hashLog)
					ms.tagTable = make([]byte, 1<<hashLog)
					ms.hashSalt = salt
					ms.nextToUpdate = baseIdx + 1
					if dict {
						ms.loadedDictEnd = baseIdx + 1
					}
					rowFillHashCache(ms, src, baseIdx, mls, ms.nextToUpdate, len(src)-16)
					return ms
				}
				msGo, msAsm := mk(), mk()
				iend := len(src)
				skipping := false
				for ip := 1; ip < iend-24; {
					if r.IntN(64) == 0 {
						if skipping {
							for _, ms := range []*matchState{msGo, msAsm} {
								rowFillHashCache(ms, src, baseIdx, mls, ms.nextToUpdate, iend-24)
							}
						}
						skipping = !skipping
					}
					msGo.lazySkipping, msAsm.lazySkipping = skipping, skipping
					useRowAsm = false
					ml1, ob1 := rowFindBestMatch(msGo, ip, iend, mls, rowLog)
					useRowAsm = true
					ml2, ob2 := rowFindBestMatch(msAsm, ip, iend, mls, rowLog)
					if ml1 != ml2 || ob1 != ob2 {
						t.Fatalf("rowLog %d mls %d dict %v ip %d: Go (%d, %d), asm rowUpdateCached (%d, %d)",
							rowLog, mls, dict, ip, ml1, ob1, ml2, ob2)
					}
					switch n := r.IntN(100); {
					case n < 2:
						ip += 400 + r.IntN(400) // past kSkipThreshold
					case n < 10 && ml1 > 3:
						ip += ml1
					default:
						ip++
					}
				}
				if !slices.Equal(msAsm.hashTable, msGo.hashTable) || !bytes.Equal(msAsm.tagTable, msGo.tagTable) ||
					msAsm.hashCache != msGo.hashCache || msAsm.nextToUpdate != msGo.nextToUpdate ||
					msAsm.hashSaltEntropy != msGo.hashSaltEntropy {
					t.Fatalf("rowLog %d mls %d dict %v: the tables differ from Go's", rowLog, mls, dict)
				}
			}
		}
	}
}

// TestBtAsmParamsLayout pins the field offsets dubt_*.s use (BP_* macros).
func TestBtAsmParamsLayout(t *testing.T) {
	var p btAsmParams
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"bt", unsafe.Offsetof(p.bt), 0},
		{"b", unsafe.Offsetof(p.b), 8},
		{"hashTable", unsafe.Offsetof(p.hashTable), 16},
		{"matches", unsafe.Offsetof(p.matches), 24},
		{"nextToUpdate", unsafe.Offsetof(p.nextToUpdate), 32},
		{"prime", unsafe.Offsetof(p.prime), 40},
		{"hShift", unsafe.Offsetof(p.hShift), 48},
		{"baseIdx", unsafe.Offsetof(p.baseIdx), 56},
		{"btMask", unsafe.Offsetof(p.btMask), 60},
		{"nbCompares", unsafe.Offsetof(p.nbCompares), 64},
		{"lowestValid", unsafe.Offsetof(p.lowestValid), 68},
		{"maxDistance", unsafe.Offsetof(p.maxDistance), 72},
		{"isDictionary", unsafe.Offsetof(p.isDictionary), 76},
		{"dictLimit", unsafe.Offsetof(p.dictLimit), 80},
		{"sufficientLen", unsafe.Offsetof(p.sufficientLen), 84},
		{"iLimit", unsafe.Offsetof(p.iLimit), 88},
		{"hashTable3", unsafe.Offsetof(p.hashTable3), 104},
		{"nextToUpdate3", unsafe.Offsetof(p.nextToUpdate3), 112},
		{"hash3Shift", unsafe.Offsetof(p.hash3Shift), 120},
		{"mls3", unsafe.Offsetof(p.mls3), 124},
	} {
		if c.got != c.want {
			t.Errorf("btAsmParams.%s at offset %d, the assembly expects %d", c.name, c.got, c.want)
		}
	}
}
