//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

package zstd

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
	"unsafe"
)

// TestRowAsmParamsLayout pins the field offsets row_amd64.s and row_arm64.s use (P_* macros).
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
		{"iLimit", unsafe.Offsetof(p.iLimit), 136},
		{"skip", unsafe.Offsetof(p.skip), 144},
	} {
		if c.got != c.want {
			t.Errorf("rowAsmParams.%s at offset %d, the assembly expects %d", c.name, c.got, c.want)
		}
	}
}

// TestRowAsmFinderEquiv compares, search by search, the row finder's
// assembly entry points that the whole-loop lazyRow* leaves unused in
// practice with the Go bodies: rowFindBestMatch with the Go helpers,
// rowFindBestMatch with the assembly rowUpdateCached and rowSearch, and
// rowFindBestMatchAsmCall, each on its own copy of the same match state.
// Every result and the final tables must agree.
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
				msGo, msAsm, msFind := mk(), mk(), mk()
				iend := len(src)
				useRowAsm = true
				rowAsmSetup(msFind, mls, rowLog, iend)
				if !msFind.rowAsm.ok {
					t.Fatal("rowAsmSetup refused the match state")
				}
				skipping := false
				for ip := 1; ip < iend-24; {
					if r.IntN(64) == 0 {
						if skipping {
							for _, ms := range []*matchState{msGo, msAsm, msFind} {
								rowFillHashCache(ms, src, baseIdx, mls, ms.nextToUpdate, iend-24)
							}
						}
						skipping = !skipping
					}
					msGo.lazySkipping, msAsm.lazySkipping, msFind.lazySkipping = skipping, skipping, skipping
					useRowAsm = false
					ml1, ob1 := rowFindBestMatch(msGo, ip, iend, mls, rowLog)
					useRowAsm = true
					ml2, ob2 := rowFindBestMatch(msAsm, ip, iend, mls, rowLog)
					ml3, ob3 := rowFindBestMatchAsmCall(msFind, ip, iend)
					if ml1 != ml2 || ob1 != ob2 || ml1 != ml3 || ob1 != ob3 {
						t.Fatalf("rowLog %d mls %d dict %v ip %d: Go (%d, %d), rowSearch asm (%d, %d), rowFindBestMatchAsm (%d, %d)",
							rowLog, mls, dict, ip, ml1, ob1, ml2, ob2, ml3, ob3)
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
				for _, ms := range []*matchState{msAsm, msFind} {
					if !slices.Equal(ms.hashTable, msGo.hashTable) || !bytes.Equal(ms.tagTable, msGo.tagTable) ||
						ms.hashCache != msGo.hashCache || ms.nextToUpdate != msGo.nextToUpdate ||
						ms.hashSaltEntropy != msGo.hashSaltEntropy {
						t.Fatalf("rowLog %d mls %d dict %v: the tables differ from Go's", rowLog, mls, dict)
					}
				}
				msFind.rowAsm = rowAsmParams{}
			}
		}
	}
}
