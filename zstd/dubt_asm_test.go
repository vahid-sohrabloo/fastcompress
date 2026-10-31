//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

package zstd

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// TestBtAsmBlockEquiv is TestHcAsmLazyEquiv for the binary-tree assembly:
// btlazy2 (dubtFindBestMatchAsm, insertDUBT1Asm, dubtSearchAsm) and btopt
// (btGetAllNoDictAsm, insertBt1Asm) run block by block with the Go code and
// with the assembly, each on its own copy of the same match state, for mls
// 4 to 6, the dictionary flag with the window starting after the first
// index (C's isDictionary candidate bound; see TestHcAsmLazyEquiv for why
// no public API reaches it), and random table sizes and window logs, over
// text, random bytes and short-period runs (long matches overlapping their
// source, after which the searches skip positions below nextToUpdate). The
// sequences, literals, repcodes and final tables must agree, and the
// assembly must have run. On an AVX2 host it runs again without AVX2.
func TestBtAsmBlockEquiv(t *testing.T) {
	withAsm(t, true, func() { testBtAsmBlockEquiv(t) })
	if cpu.X86.HasAVX2 {
		restore := cpu.DisableAVX2()
		defer restore()
		withAsm(t, true, func() { testBtAsmBlockEquiv(t) })
	}
}

func testBtAsmBlockEquiv(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	r := rand.New(rand.NewPCG(13, 14))
	src := make([]byte, 0, 1<<18)
	for len(src) < 1<<18 {
		if r.IntN(16) == 0 {
			for range 2048 + r.IntN(4096) {
				src = append(src, byte(r.Uint32()))
			}
		}
		if r.IntN(8) == 0 { // a run of a short period: matches overlapping their source skip positions (nextToUpdate > curr)
			period := src[len(src)-1-r.IntN(8):]
			for range 64 + r.IntN(1024) {
				src = append(src, period[0])
				period = src[len(src)-len(period):]
			}
		}
		chunk := text[r.IntN(len(text)/2):]
		chunk = chunk[:min(len(chunk), 1+r.IntN(4096))]
		src = append(src, chunk...)
		if r.IntN(4) == 0 {
			src = append(src, byte(r.Uint32()))
		}
	}
	saved := useDubtAsm
	defer func() { useDubtAsm = saved }()
	for _, strat := range []strategy{zstdBtlazy2, zstdBtopt} {
		for _, mls := range []uint32{4, 5, 6} {
			for _, dict := range []bool{false, true} {
				const baseIdx = 5
				start := 0
				if dict {
					start = 1 + r.IntN(4096)
				}
				cp := compressionParameters{
					windowLog:    uint32(10 + r.IntN(8)),
					chainLog:     uint32(6 + r.IntN(11)),
					hashLog:      uint32(8 + r.IntN(9)),
					searchLog:    uint32(1 + r.IntN(8)),
					minMatch:     mls,
					targetLength: uint32(r.IntN(128)),
					strategy:     strat,
				}
				mk := func() *matchState {
					ms := &matchState{}
					lowLimit := baseIdx + uint32(start)
					ms.window = window{base: src, baseIdx: baseIdx, dictLimit: lowLimit, lowLimit: lowLimit}
					ms.cParams = cp
					ms.hashTable = make([]uint32, 1<<cp.hashLog)
					ms.chainTable = make([]uint32, 1<<cp.chainLog)
					ms.nextToUpdate = lowLimit
					if dict {
						ms.loadedDictEnd = lowLimit + 1
					}
					ms.opt.matchTable = new([optSize]optMatch)
					ms.opt.priceTable = new([optSize]optimal)
					ms.opt.symbolCosts = &entropyCTables{}
					return ms
				}
				msGo, msAsm := mk(), mk()
				repGo, repAsm := [repNum]uint32{1, 4, 8}, [repNum]uint32{1, 4, 8}
				var ssGo, ssAsm seqStore
				for _, ss := range []*seqStore{&ssGo, &ssAsm} {
					ss.literals = make([]byte, 0, 1<<17)
					ss.sequences = make([]seqDef, 0, 1<<15)
				}
				compress := func(ms *matchState, ss *seqStore, rep *[repNum]uint32, blk []byte, idx uint32) int {
					if strat == zstdBtopt {
						return compressBlockBtopt(ms, ss, rep, blk, idx)
					}
					return compressBlockBtlazy2(ms, ss, rep, blk, idx)
				}
				for pos := start; pos < len(src); {
					n := min(len(src)-pos, 1+r.IntN(1<<17))
					blk := src[pos : pos+n]
					resetSeqStore(&ssGo)
					resetSeqStore(&ssAsm)
					useDubtAsm = false
					litGo := compress(msGo, &ssGo, &repGo, blk, uint32(pos)+baseIdx)
					useDubtAsm = true
					litAsm := compress(msAsm, &ssAsm, &repAsm, blk, uint32(pos)+baseIdx)
					if litGo != litAsm || repGo != repAsm || !slices.Equal(ssGo.sequences, ssAsm.sequences) ||
						!bytes.Equal(ssGo.literals, ssAsm.literals) || ssGo.longLengthType != ssAsm.longLengthType ||
						ssGo.longLengthPos != ssAsm.longLengthPos {
						t.Fatalf("strategy %d mls %d dict %v %+v block [%d, %d): asm differs from Go (%d vs %d sequences)",
							strat, mls, dict, cp, pos, pos+n, len(ssAsm.sequences), len(ssGo.sequences))
					}
					if !slices.Equal(msAsm.hashTable, msGo.hashTable) || !slices.Equal(msAsm.chainTable, msGo.chainTable) ||
						msAsm.nextToUpdate != msGo.nextToUpdate {
						t.Fatalf("strategy %d mls %d dict %v block [%d, %d): the tables differ from Go's", strat, mls, dict, pos, pos+n)
					}
					pos += n
				}
				if msGo.asmCalls != 0 || msAsm.asmCalls == 0 {
					t.Fatalf("strategy %d mls %d dict %v: the assembly ran %d times (Go side %d)", strat, mls, dict, msAsm.asmCalls, msGo.asmCalls)
				}
			}
		}
	}
}
