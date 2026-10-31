//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
	"unsafe"
)

// TestOptAsmParamsLayout checks the offsets opt_amd64.s uses for
// optAsmParams, optState and optimal.
func TestOptAsmParamsLayout(t *testing.T) {
	var p optAsmParams
	var o optState
	var x optimal
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"optAsmParams.bt", unsafe.Offsetof(p.bt), 0},
		{"optAsmParams.o", unsafe.Offsetof(p.o), 8},
		{"optAsmParams.opt", unsafe.Offsetof(p.opt), 16},
		{"optAsmParams.matches", unsafe.Offsetof(p.matches), 24},
		{"optAsmParams.b", unsafe.Offsetof(p.b), 32},
		{"optAsmParams.rep", unsafe.Offsetof(p.rep), 40},
		{"optAsmParams.ip", unsafe.Offsetof(p.ip), 48},
		{"optAsmParams.anchor", unsafe.Offsetof(p.anchor), 56},
		{"optAsmParams.iend", unsafe.Offsetof(p.iend), 64},
		{"optAsmParams.ilimit", unsafe.Offsetof(p.ilimit), 72},
		{"optAsmParams.sufficientLen", unsafe.Offsetof(p.sufficientLen), 80},
		{"optAsmParams.minML", unsafe.Offsetof(p.minML), 84},
		{"optAsmParams.storeStart", unsafe.Offsetof(p.storeStart), 88},
		{"optAsmParams.storeEnd", unsafe.Offsetof(p.storeEnd), 92},
		{"optState.litFreq", unsafe.Offsetof(o.litFreq), 0},
		{"optState.litLengthFreq", unsafe.Offsetof(o.litLengthFreq), 1024},
		{"optState.matchLengthFreq", unsafe.Offsetof(o.matchLengthFreq), 1168},
		{"optState.offCodeFreq", unsafe.Offsetof(o.offCodeFreq), 1380},
		{"optState.litSumBasePrice", unsafe.Offsetof(o.litSumBasePrice), 1544},
		{"optState.litLengthSumBasePrice", unsafe.Offsetof(o.litLengthSumBasePrice), 1548},
		{"optState.matchLengthSumBasePrice", unsafe.Offsetof(o.matchLengthSumBasePrice), 1552},
		{"optState.offCodeSumBasePrice", unsafe.Offsetof(o.offCodeSumBasePrice), 1556},
		{"optimal.price", unsafe.Offsetof(x.price), 0},
		{"optimal.off", unsafe.Offsetof(x.off), 8},
		{"optimal.mlen", unsafe.Offsetof(x.mlen), 12},
		{"optimal.litlen", unsafe.Offsetof(x.litlen), 16},
		{"optimal.rep", unsafe.Offsetof(x.rep), 20},
		{"sizeof optimal", unsafe.Sizeof(x), 32},
	} {
		if c.got != c.want {
			t.Errorf("%s at offset %d, the assembly expects %d", c.name, c.got, c.want)
		}
	}
}

// TestOptAsmEquiv runs compressBlockBtopt block by block with the Go match
// loop and with the assembly one (optParseAsm; the finder is the assembly
// in both), each on its own copy of the same match state, and requires the
// same sequences, literals, repcodes, statistics and tables. The input
// mixes text, random literal runs of every length up to a few hundred (literal
// lengths crossing the llCode table's end and powers of two inside a
// forward pass), copies from up to 3 MiB back (offset codes >= 20, priced
// with the long-distance handicap) and copies longer than optNum, over
// full-size blocks; mls 4 to 6, random search depths and target lengths
// (sufficientLen up to optNum - 1).
func TestOptAsmEquiv(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	r := rand.New(rand.NewPCG(20, 6))
	src := make([]byte, 0, 6<<20)
	for len(src) < 6<<20 {
		if r.IntN(2) == 0 { // text: close calls between literals and matches
			chunk := text[r.IntN(len(text)):]
			src = append(src, chunk[:min(len(chunk), r.IntN(3000))]...)
		}
		for range r.IntN(400) {
			src = append(src, byte(r.Uint32()))
		}
		n := 3 + r.IntN(64)
		switch r.IntN(16) {
		case 0:
			n = 3000 + r.IntN(3000)
		case 1, 2:
			n += r.IntN(1000)
		}
		dist := 1 + r.IntN(min(len(src), 64))
		if r.IntN(3) == 0 {
			dist = 1 + r.IntN(len(src))
		}
		for range n {
			src = append(src, src[len(src)-dist])
		}
	}
	savedOpt, savedDubt := useOptAsm, useDubtAsm
	defer func() { useOptAsm, useDubtAsm = savedOpt, savedDubt }()
	useDubtAsm = true
	for _, mls := range []uint32{4, 5, 6} {
		const baseIdx = 3
		cp := compressionParameters{
			windowLog:    23,
			chainLog:     uint32(16 + r.IntN(5)),
			hashLog:      uint32(16 + r.IntN(5)),
			searchLog:    uint32(1 + r.IntN(6)),
			minMatch:     mls,
			targetLength: []uint32{0, 16, 999, 1 << 17}[r.IntN(4)],
			strategy:     zstdBtopt,
		}
		mk := func() *matchState {
			ms := &matchState{}
			ms.window = window{base: src, baseIdx: baseIdx, dictLimit: baseIdx, lowLimit: baseIdx}
			ms.cParams = cp
			ms.hashTable = make([]uint32, 1<<cp.hashLog)
			ms.chainTable = make([]uint32, 1<<cp.chainLog)
			ms.nextToUpdate = baseIdx
			ms.opt.matchTable = new([optSize]optMatch)
			ms.opt.priceTable = new([optSize]optimal)
			ms.opt.symbolCosts = &entropyCTables{}
			return ms
		}
		msGo, msAsm := mk(), mk()
		repGo, repAsm := [repNum]uint32{1, 4, 8}, [repNum]uint32{1, 4, 8}
		var ssGo, ssAsm seqStore
		for _, ss := range []*seqStore{&ssGo, &ssAsm} {
			ss.literals = make([]byte, 0, blockSizeMax)
			ss.sequences = make([]seqDef, 0, blockSizeMax/3)
		}
		for pos := 0; pos < len(src); {
			n := min(len(src)-pos, blockSizeMax-r.IntN(2)*r.IntN(blockSizeMax))
			blk := src[pos : pos+n]
			resetSeqStore(&ssGo)
			resetSeqStore(&ssAsm)
			useOptAsm = false
			litGo := compressBlockBtopt(msGo, &ssGo, &repGo, blk, uint32(pos)+baseIdx)
			calls := msAsm.asmCalls
			useOptAsm = true
			litAsm := compressBlockBtopt(msAsm, &ssAsm, &repAsm, blk, uint32(pos)+baseIdx)
			if litGo != litAsm || repGo != repAsm || !slices.Equal(ssGo.sequences, ssAsm.sequences) ||
				!bytes.Equal(ssGo.literals, ssAsm.literals) || ssGo.longLengthType != ssAsm.longLengthType ||
				ssGo.longLengthPos != ssAsm.longLengthPos {
				t.Fatalf("mls %d %+v block [%d, %d): asm differs from Go (%d vs %d sequences)",
					mls, cp, pos, pos+n, len(ssAsm.sequences), len(ssGo.sequences))
			}
			if msGo.opt.litFreq != msAsm.opt.litFreq || msGo.opt.offCodeFreq != msAsm.opt.offCodeFreq ||
				msGo.opt.litLengthFreq != msAsm.opt.litLengthFreq || msGo.opt.matchLengthFreq != msAsm.opt.matchLengthFreq ||
				!slices.Equal(msAsm.hashTable, msGo.hashTable) || !slices.Equal(msAsm.chainTable, msGo.chainTable) ||
				msAsm.nextToUpdate != msGo.nextToUpdate {
				t.Fatalf("mls %d block [%d, %d): the statistics or tables differ from Go's", mls, pos, pos+n)
			}
			if n > 8 && msAsm.asmCalls != calls+1 { // optParseFast counts 1, the Go loop's finder calls 1 each
				t.Fatalf("mls %d block [%d, %d): the assembly parser did not run", mls, pos, pos+n)
			}
			pos += n
		}
	}
}
