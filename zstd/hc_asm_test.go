//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

package zstd

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// TestHcAsmLazyEquiv runs compressBlockLazyGeneric with the hash-chain
// finder block by block with the Go loop and with the assembly lazyHc*, each
// on its own copy of the same match state, for every mls (6 is not reached
// from any level table), depth, dictionary flag and random table sizes.
// The sequences, literals, repcodes and final tables must agree. On an
// AVX2 host it runs again without AVX2 (the scalar count).
func TestHcAsmLazyEquiv(t *testing.T) {
	withAsm(t, true, func() { testHcAsmLazyEquiv(t) })
	if cpu.X86.HasAVX2 {
		restore := cpu.DisableAVX2()
		defer restore()
		withAsm(t, true, func() { testHcAsmLazyEquiv(t) })
	}
}

func testHcAsmLazyEquiv(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	r := rand.New(rand.NewPCG(9, 10))
	src := make([]byte, 0, 1<<18)
	for len(src) < 1<<18 {
		if r.IntN(16) == 0 { // incompressible: lazy skipping
			for range 2048 + r.IntN(4096) {
				src = append(src, byte(r.Uint32()))
			}
		}
		chunk := text[r.IntN(len(text)/2):]
		chunk = chunk[:min(len(chunk), 1+r.IntN(4096))]
		src = append(src, chunk...)
		if r.IntN(4) == 0 {
			src = append(src, byte(r.Uint32())) // break some matches
		}
	}
	saved := useHcAsm
	defer func() { useHcAsm = saved }()
	for _, mls := range []uint32{4, 5, 6} {
		for depth := range 3 {
			for _, dict := range []bool{false, true} {
				const baseIdx = 5
				cp := compressionParameters{
					windowLog: uint32(10 + r.IntN(8)),
					chainLog:  uint32(6 + r.IntN(11)),
					hashLog:   uint32(8 + r.IntN(9)),
					searchLog: uint32(1 + r.IntN(8)),
					minMatch:  mls,
				}
				mk := func() *matchState {
					ms := &matchState{}
					ms.window = window{base: src, baseIdx: baseIdx, dictLimit: baseIdx, lowLimit: baseIdx}
					ms.cParams = cp
					ms.hashTable = make([]uint32, 1<<cp.hashLog)
					ms.chainTable = make([]uint32, 1<<cp.chainLog)
					ms.nextToUpdate = baseIdx
					if dict {
						ms.loadedDictEnd = baseIdx + 1
					}
					return ms
				}
				msGo, msAsm := mk(), mk()
				repGo, repAsm := [repNum]uint32{1, 4, 8}, [repNum]uint32{1, 4, 8}
				var ssGo, ssAsm seqStore
				for _, ss := range []*seqStore{&ssGo, &ssAsm} {
					ss.literals = make([]byte, 0, 1<<17)
					ss.sequences = make([]seqDef, 0, 1<<15)
				}
				for pos := 0; pos < len(src); {
					n := min(len(src)-pos, 1+r.IntN(1<<17))
					blk := src[pos : pos+n]
					resetSeqStore(&ssGo)
					resetSeqStore(&ssAsm)
					useHcAsm = false
					litGo := compressBlockLazyGeneric(msGo, &ssGo, &repGo, blk, uint32(pos)+baseIdx, searchHashChain, depth)
					useHcAsm = true
					litAsm := compressBlockLazyGeneric(msAsm, &ssAsm, &repAsm, blk, uint32(pos)+baseIdx, searchHashChain, depth)
					if litGo != litAsm || repGo != repAsm || !slices.Equal(ssGo.sequences, ssAsm.sequences) ||
						!bytes.Equal(ssGo.literals, ssAsm.literals) || ssGo.longLengthType != ssAsm.longLengthType ||
						ssGo.longLengthPos != ssAsm.longLengthPos || msGo.lazySkipping != msAsm.lazySkipping {
						t.Fatalf("mls %d depth %d dict %v %+v block [%d, %d): asm differs from Go (%d vs %d sequences)",
							mls, depth, dict, cp, pos, pos+n, len(ssAsm.sequences), len(ssGo.sequences))
					}
					pos += n
				}
				if !slices.Equal(msAsm.hashTable, msGo.hashTable) || !slices.Equal(msAsm.chainTable, msGo.chainTable) ||
					msAsm.nextToUpdate != msGo.nextToUpdate {
					t.Fatalf("mls %d depth %d dict %v: the tables differ from Go's", mls, depth, dict)
				}
			}
		}
	}
}
