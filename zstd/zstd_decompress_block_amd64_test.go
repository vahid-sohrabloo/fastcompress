//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// runGo is decodeExecSeqsAsm in Go, the oracle of the equivalence tests:
// while f.nbSeq > 1 and the bitstream's ptr >= 16, it decodes a sequence
// (stopping first, with nothing consumed, on an offset cell with
// nbAdditionalBits 1 and baseValue != 1) and executes it with execSequence,
// unless execSequence would take a slow path (execSequenceEnd, or the
// extDict path or its error): then it returns that sequence, pending.
// With f.split it is decompressSequencesBodySplitLitBuffer's first loop:
// it executes with execSequenceSplitLitBuffer (oend_w = iLitEnd - 32), and
// also returns the sequence pending at that function's oMatchEnd > oend_w;
// iLitEnd > litEnd is then C's break.
// It counts each branch of the assembly it takes in seqArmHits.
func (f *seqFast) runGo(st *seqState) (seq, bool) {
	for f.nbSeq > 1 {
		if _, ptr, _, _ := st.dStream.Raw(); ptr < 16 {
			seqArmHits[armStopPtr].Add(1)
			return seq{}, false
		}
		if c := &st.stateOffb.table[st.stateOffb.state]; c.nbAdditionalBits == 1 && c.baseValue != 1 {
			seqArmHits[armStopInvariant].Add(1)
			return seq{}, false
		}
		countDecodeArms(st)
		sq := decodeSequence(st, false, false)
		if _, _, _, consumed := st.dStream.Raw(); consumed > 64 {
			seqArmHits[armOverflow].Add(1)
		}
		oLitEnd := f.op + sq.litLength
		oMatchEnd := oLitEnd + sq.matchLength
		iLitEnd := f.litPtr + sq.litLength
		switch {
		case iLitEnd > f.litEnd && f.split:
			seqArmHits[armSplitBreak].Add(1)
			return sq, true
		case iLitEnd > f.litEnd:
			seqArmHits[armPendLit].Add(1)
			return sq, true
		case oMatchEnd > f.oend-wildcopyOverlength:
			seqArmHits[armPendOend].Add(1)
			return sq, true
		case f.split && oMatchEnd > iLitEnd-wildcopyOverlength:
			seqArmHits[armPendSplit].Add(1)
			return sq, true
		case uint(sq.offset) > uint(oLitEnd-f.prefixStart):
			seqArmHits[armPendOffset].Add(1)
			return sq, true
		}
		countExecArms(sq)
		var n int
		var e dErr
		if f.split {
			seqArmHits[armSplitExec].Add(1)
			n, e = execSequenceSplitLitBuffer(f.out, f.op, f.oend, iLitEnd-wildcopyOverlength, sq, &f.litPtr, f.litEnd, f.prefixStart, nil)
		} else {
			n, e = execSequence(f.out, f.op, f.oend, sq, f.lits, &f.litPtr, f.litEnd, f.prefixStart, nil)
		}
		if e != 0 {
			panic("execSequence failed on its fast path")
		}
		f.op += n
		f.nbSeq--
	}
	seqArmHits[armStopNbSeq].Add(1)
	return seq{}, false
}

// seqArm is a branch of decodeExecSeqsAsm. runGo counts the ones it takes
// in seqArmHits (atomic: safe if the tests ever run in parallel), and the
// equivalence tests require each to be reached (checkSeqArms), since Go
// coverage does not see the assembly. Loop back-edges (a second litLoop,
// matchLoop or copy8Loop pass) are not counted: each loop is only
// asserted to be entered.
type seqArm int

const (
	armStopNbSeq     seqArm = iota // nbSeq <= 1: the last sequence is Go's
	armStopPtr                     // ptr < 16
	armStopInvariant               // TABLE INVARIANT broken
	armPendLit                     // iLitEnd > litEnd (execSequenceEnd)
	armPendOend                    // oMatchEnd > oend - 32 (execSequenceEnd)
	armPendOffset                  // offset > oLitEnd - prefixStart (extDict, error)
	armOfNew                       // ofBits > 1
	armRep0                        // ofBits 0, litLength != 0: prevOffset[0]
	armRep0LL0                     // ofBits 0, litLength 0: prevOffset[1]
	armRep1                        // ofBits 1, offset 1
	armRep2                        // ofBits 1, offset 2 (also ll0 with bit 0)
	armRep3                        // ofBits 1, offset 3: prevOffset[0] - 1 (ll0)
	armReload1                     // totalBits >= 31: the first reload
	armMLBits0                     // ML cell nbAdditionalBits 0: no ML bits read
	armLLBits0                     // LL cell nbAdditionalBits 0: no LL bits read
	armReload1Skip                 // totalBits < 31
	armOverflow                    // a reload in overflow (bitsConsumed > 64)
	armLit16                       // litLength <= 16: one copy16
	armLit32                       // litLength 17..32: two
	armLitLoop                     // litLength > 32: litLoop
	armOffLt8                      // offset < 8: the dec32/dec64 spread
	armOff8To15                    // offset 8..15: copy8
	armOff16                       // offset >= 16: copy16
	armMatchLoop                   // offset >= 16, matchLength > 16: matchLoop
	armCopy8Loop                   // offset < 16, matchLength > 8: copy8Loop
	armSplitBreak                  // split: iLitEnd > litEnd, C's break to litExtraBuffer
	armPendSplit                   // split: oMatchEnd > iLitEnd - 32 (execSequenceEndSplitLitBuffer)
	armSplitExec                   // split: a sequence executed (execSequenceSplitLitBuffer's fast path)
	numSeqArms
)

var seqArmNames = [numSeqArms]string{
	"stop nbSeq<=1", "stop ptr<16", "stop table invariant",
	"pending iLitEnd>litEnd", "pending oMatchEnd>oendW", "pending offset>oLitEnd-prefixStart",
	"offset ofBits>1", "repeat ofBits 0", "repeat ofBits 0 ll0", "repeat offset 1", "repeat offset 2",
	"repeat offset 3", "reload1", "mlBits 0", "llBits 0", "reload1 skipped", "reload in overflow",
	"litLength<=16", "litLength 17..32", "litLength>32",
	"offset<8", "offset 8..15", "offset>=16", "matchLoop", "copy8Loop",
	"split break iLitEnd>litEnd", "split pending oMatchEnd>iLitEnd-32", "split executed",
}

var seqArmHits [numSeqArms]atomic.Int64

// equivArms are the branches TestSeqAsmEquiv's valid and corrupted corpus
// blocks reach, syntheticArms TestSeqAsmSyntheticStates's states;
// together, every branch (TestSeqAsmArmLists). The corpus does not reach
// the last sequence before the bitstream's last 16 bytes, a broken table
// invariant, oend (the blocks have room), a first reload (31 extra bits),
// overflow, or in split mode the output catching up with the literals
// (they start at least litBufferExtraSize - 32 bytes ahead); the
// synthetic block's matches are short.
var (
	equivArms = []seqArm{armStopPtr, armPendLit, armPendOffset, armOfNew, armRep0, armRep0LL0,
		armRep1, armRep2, armRep3, armMLBits0, armLLBits0, armReload1Skip, armLit16, armLit32, armLitLoop,
		armOffLt8, armOff8To15, armOff16, armMatchLoop, armCopy8Loop, armSplitBreak, armSplitExec}
	syntheticArms = []seqArm{armStopNbSeq, armStopPtr, armStopInvariant, armPendLit, armPendOend,
		armPendOffset, armOfNew, armRep0, armRep0LL0, armRep1, armRep2, armRep3, armReload1,
		armMLBits0, armLLBits0, armReload1Skip, armOverflow, armLit16, armLit32, armLitLoop, armOffLt8, armOff8To15,
		armOff16, armCopy8Loop, armSplitBreak, armPendSplit, armSplitExec}
)

// TestSeqAsmArmLists checks that equivArms and syntheticArms together
// name every branch.
func TestSeqAsmArmLists(t *testing.T) {
	var seen [numSeqArms]bool
	for _, a := range append(append([]seqArm(nil), equivArms...), syntheticArms...) {
		seen[a] = true
	}
	for i, ok := range seen {
		if !ok {
			t.Errorf("branch %q in neither list", seqArmNames[i])
		}
	}
}

// countDecodeArms counts the decodeSequence branches the assembly takes
// from st (before the sequence is decoded; past the invariant check).
func countDecodeArms(st *seqState) {
	ll := &st.stateLL.table[st.stateLL.state]
	ml := &st.stateML.table[st.stateML.state]
	of := &st.stateOffb.table[st.stateOffb.state]
	ll0 := 0
	if ll.baseValue == 0 {
		ll0 = 1
	}
	switch {
	case of.nbAdditionalBits > 1:
		seqArmHits[armOfNew].Add(1)
	case of.nbAdditionalBits == 0 && ll0 == 0:
		seqArmHits[armRep0].Add(1)
	case of.nbAdditionalBits == 0:
		seqArmHits[armRep0LL0].Add(1)
	default:
		ds := st.dStream // the bit decodeSequence reads first
		seqArmHits[armRep1+seqArm(ll0)+seqArm(ds.ReadBitsFast(1))].Add(1)
	}
	if ml.nbAdditionalBits == 0 {
		seqArmHits[armMLBits0].Add(1)
	}
	if ll.nbAdditionalBits == 0 {
		seqArmHits[armLLBits0].Add(1)
	}
	if ll.nbAdditionalBits+ml.nbAdditionalBits+of.nbAdditionalBits >= 31 {
		seqArmHits[armReload1].Add(1)
	} else {
		seqArmHits[armReload1Skip].Add(1)
	}
}

// countExecArms counts the execSequence fast-path branches the assembly
// takes for sq.
func countExecArms(sq seq) {
	switch {
	case sq.litLength <= 16:
		seqArmHits[armLit16].Add(1)
	case sq.litLength <= 32:
		seqArmHits[armLit32].Add(1)
	default:
		seqArmHits[armLitLoop].Add(1)
	}
	switch {
	case sq.offset < 8:
		seqArmHits[armOffLt8].Add(1)
	case sq.offset < 16:
		seqArmHits[armOff8To15].Add(1)
	default:
		seqArmHits[armOff16].Add(1)
		if sq.matchLength > 16 {
			seqArmHits[armMatchLoop].Add(1)
		}
		return
	}
	if sq.matchLength > 8 {
		seqArmHits[armCopy8Loop].Add(1)
	}
}

// seqArmCounts returns the current seqArmHits.
func seqArmCounts() (c [numSeqArms]int64) {
	for i := range c {
		c[i] = seqArmHits[i].Load()
	}
	return c
}

// checkSeqArms fails t for each of arms that runGo has not taken since
// before (seqArmCounts), and logs the counts.
func checkSeqArms(t *testing.T, before [numSeqArms]int64, arms ...seqArm) {
	t.Helper()
	now := seqArmCounts()
	var b strings.Builder
	for i := range now {
		fmt.Fprintf(&b, " %s=%d;", seqArmNames[i], now[i]-before[i])
	}
	t.Logf("branches taken:%s", b.String())
	for _, a := range arms {
		if now[a] == before[a] {
			t.Errorf("branch %q never taken", seqArmNames[a])
		}
	}
}

// TestSeqAsmArgsLayout checks the offsets zstd_decompress_block_amd64.s
// uses, and seqSymbol's (C's ZSTD_seqSymbol) layout.
func TestSeqAsmArgsLayout(t *testing.T) {
	var a seqAsmArgs
	var s seqSymbol
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"bitContainer", unsafe.Offsetof(a.bitContainer), 0},
		{"bitsConsumed", unsafe.Offsetof(a.bitsConsumed), 8},
		{"ptr", unsafe.Offsetof(a.ptr), 16},
		{"src", unsafe.Offsetof(a.src), 24},
		{"stateLL", unsafe.Offsetof(a.stateLL), 32},
		{"stateML", unsafe.Offsetof(a.stateML), 40},
		{"stateOf", unsafe.Offsetof(a.stateOf), 48},
		{"tableLL", unsafe.Offsetof(a.tableLL), 56},
		{"tableML", unsafe.Offsetof(a.tableML), 64},
		{"tableOf", unsafe.Offsetof(a.tableOf), 72},
		{"rep", unsafe.Offsetof(a.rep), 80},
		{"out", unsafe.Offsetof(a.out), 104},
		{"op", unsafe.Offsetof(a.op), 112},
		{"oendW", unsafe.Offsetof(a.oendW), 120},
		{"lits", unsafe.Offsetof(a.lits), 128},
		{"litPtr", unsafe.Offsetof(a.litPtr), 136},
		{"litEnd", unsafe.Offsetof(a.litEnd), 144},
		{"prefixStart", unsafe.Offsetof(a.prefixStart), 152},
		{"nbSeq", unsafe.Offsetof(a.nbSeq), 160},
		{"litLength", unsafe.Offsetof(a.litLength), 168},
		{"matchLength", unsafe.Offsetof(a.matchLength), 176},
		{"offset", unsafe.Offsetof(a.offset), 184},
		{"pending", unsafe.Offsetof(a.pending), 192},
		{"seqSymbol.nextState", unsafe.Offsetof(s.nextState), 0},
		{"seqSymbol.nbAdditionalBits", unsafe.Offsetof(s.nbAdditionalBits), 2},
		{"seqSymbol.nbBits", unsafe.Offsetof(s.nbBits), 3},
		{"seqSymbol.baseValue", unsafe.Offsetof(s.baseValue), 4},
		{"sizeof seqSymbol", unsafe.Sizeof(s), 8},
	} {
		if c.got != c.want {
			t.Errorf("%s at %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestSeqAsmRegistered checks the predicate registered with
// cpu.RegisterAsm: the fast loop is registered with its switch, is off
// without BMI2 (SetAsmMode("nobmi2")) and follows the detected BMI2
// otherwise.
func TestSeqAsmRegistered(t *testing.T) {
	found := 0
	for _, r := range cpu.AsmRoutines() {
		if r.Name == "seq.decompressSequencesFast" {
			found++
			if r.Use != &useSeqDecAsm {
				t.Error("registered with the wrong switch")
			}
		}
	}
	if found != 1 {
		t.Fatalf("found %d registrations of seq.decompressSequencesFast", found)
	}
	defer func() {
		if err := cpu.SetAsmMode(os.Getenv("FASTZSTD_FUZZ_ASM")); err != nil {
			t.Fatal(err)
		}
	}()
	if err := cpu.SetAsmMode("on"); err != nil {
		t.Fatal(err)
	}
	if useSeqDecAsm != cpu.X86.HasBMI2 {
		t.Errorf("mode on, BMI2 %v: asm %v", cpu.X86.HasBMI2, useSeqDecAsm)
	}
	if err := cpu.SetAsmMode("nobmi2"); err != nil {
		t.Fatal(err)
	}
	if useSeqDecAsm {
		t.Error("mode nobmi2: asm on")
	}
}

// TestSeqAsmGuard checks that sequencesFast leaves f and st alone (the
// Go loop then decodes every sequence) when f breaks one of the loop's
// preconditions (seqFast.asmOK), here each of them in turn on a block the
// loop otherwise runs.
func TestSeqAsmGuard(t *testing.T) {
	withAsm(t, true, func() {
		if !useSeqDecAsm {
			t.Skip("no BMI2")
		}
		rng := rand.New(rand.NewPCG(2, 2))
		src := make([]byte, 1<<14)
		for i := range src {
			src[i] = "abcdefgh"[rng.IntN(8)]
		}
		frame := newTestEncoder(t).EncodeAll(src, nil)
		var zfh frameHeader
		if need, e := getFrameHeaderAdvanced(&zfh, frame); need != 0 || e != 0 {
			t.Fatal("frame header")
		}
		cSize, bp, e := getcBlockSize(frame[zfh.headerSize:])
		if e != 0 || bp.blockType != btCompressed {
			t.Fatal("no compressed first block")
		}
		block := frame[int(zfh.headerSize)+blockHeaderSize:][:cSize]
		const hist = 64
		d := newTestDctx(blockSizeMax)
		dstCap := 2*blockSizeMax + 4*wildcopyOverlength
		out := make([]byte, hist+dstCap)
		litCSize, e := d.decodeLiteralsBlock(block, out[hist:], dstCap, notStreaming)
		if e != 0 {
			t.Fatal(e)
		}
		nbSeq, hSize, e := decodeSeqHeaders(d, block[litCSize:])
		if e != 0 || nbSeq < 2 || d.litBufferLocation == split {
			t.Fatalf("headers: err %d, nbSeq %d, placement %d", e, nbSeq, d.litBufferLocation)
		}
		for _, c := range []struct {
			name   string
			mutate func(f *seqFast)
		}{
			{"none", func(*seqFast) {}},
			{"prefixStart>op", func(f *seqFast) { f.prefixStart = f.op + 1 }},
			{"prefixStart<0", func(f *seqFast) { f.prefixStart = -1 }},
			{"op>oend", func(f *seqFast) { f.op = f.oend + 1; f.prefixStart = 0 }},
			{"litPtr>litEnd", func(f *seqFast) { f.litPtr = f.litEnd + 1 }},
			{"litPtr<0", func(f *seqFast) { f.litPtr = -1 }},
			{"oend>len(out)", func(f *seqFast) { f.out = f.out[:f.oend-1] }},
			{"litEnd+32>len(lits)", func(f *seqFast) { f.lits = f.lits[:f.litEnd+wildcopyOverlength-1] }},
		} {
			r := newSeqFastRun(t, d, out, hist, dstCap, block[litCSize+hSize:], nbSeq, false, false, false)
			c.mutate(&r.f)
			f := r.f
			_, ptr, _, consumed := r.st.dStream.Raw()
			if e := sequencesFast(&r.st, &r.f, nil); e != 0 {
				t.Fatalf("%s: err %d", c.name, e)
			}
			moved := r.f.nbSeq != f.nbSeq
			if c.name == "none" {
				if !moved {
					t.Fatal("the loop did not run on the unbroken block")
				}
				continue
			}
			_, ptr2, _, consumed2 := r.st.dStream.Raw()
			if moved || r.f.op != f.op || r.f.litPtr != f.litPtr || ptr2 != ptr || consumed2 != consumed {
				t.Errorf("%s: the loop ran", c.name)
			}
			// The same through sequencesFastSplit (split, lits = out).
			r = newSeqFastRun(t, d, out, hist, dstCap, block[litCSize+hSize:], nbSeq, false, false, false)
			r.f.lits, r.f.split = r.f.out, true
			r.f.litPtr, r.f.litEnd = r.f.oend-wildcopyOverlength-1, r.f.oend-wildcopyOverlength
			c.mutate(&r.f)
			f = r.f
			if _, brk, e := sequencesFastSplit(&r.st, &r.f, nil); e != 0 || brk {
				t.Fatalf("%s: split: err %d, break %v", c.name, e, brk)
			}
			_, ptr2, _, consumed2 = r.st.dStream.Raw()
			if r.f.nbSeq != f.nbSeq || r.f.op != f.op || r.f.litPtr != f.litPtr || ptr2 != ptr || consumed2 != consumed {
				t.Errorf("%s: split: the loop ran", c.name)
			}
		}
	})
}

// seqFastRun is one side of a fast-loop comparison: its state and buffers,
// with, for split literals, its copy of litExtraBuffer.
type seqFastRun struct {
	st    seqState
	f     seqFast
	extra []byte
}

// newSeqFastRun starts a run of the block's sequences seqSrc, with d's
// tables and repeat offsets, on its own copy of the output space. With
// guard, the copy (after or before per outBefore), the bitstream (per
// srcBefore) and a copy of literals not in out (after) sit against a guard
// page; without, the literals are read where d's literal decoding left
// them (decompressSequencesBody's buffers). Lits in out stay in out. For
// split literals the run starts as decompressSequencesBodySplitLitBuffer's
// first loop (f.split), with a copy of d.litExtraBuffer (after a guard
// page with guard) for the switch.
func newSeqFastRun(t testing.TB, d *dctx, out []byte, ostart, dstCap int, seqSrc []byte, nbSeq int, guard, srcBefore, outBefore bool) *seqFastRun {
	alloc := func(n int, before bool) []byte {
		switch {
		case !guard:
			return make([]byte, n)
		case before:
			return guardmem.Before(t, n)
		}
		return guardmem.After(t, n)
	}
	r := &seqFastRun{}
	o := alloc(len(out), outBefore)
	copy(o, out)
	oend := ostart + d.litBuffer
	if d.litBufferLocation != inDst {
		oend = ostart + dstCap
	}
	lits, litBase := d.litBufferStart(o, ostart)
	litPtr := litBase + d.litPtr
	litEnd := litPtr + d.litSize
	isSplit := d.litBufferLocation == split
	if isSplit {
		litEnd = ostart + d.litBufferEnd
		r.extra = alloc(len(d.litExtraBuffer), false)
		copy(r.extra, d.litExtraBuffer[:])
	}
	if guard && d.litBufferLocation == notInDst {
		l := alloc(litEnd+wildcopyOverlength, false)
		copy(l, lits)
		lits = l
	}
	src := alloc(len(seqSrc), srcBefore)
	copy(src, seqSrc)
	if e := d.initSeqState(&r.st, src); e != 0 {
		return nil
	}
	r.f = seqFast{out: o, op: ostart, oend: oend, lits: lits, litPtr: litPtr, litEnd: litEnd,
		prefixStart: d.prefixStart, nbSeq: nbSeq, split: isSplit}
	return r
}

// execPending executes sq, which runAsm or runGo handed back, as the
// production callers do: sequencesFast with execSequence,
// sequencesFastSplit with execSequenceSplitLitBuffer, and at C's break
// (split, iLitEnd > litEnd) decompressSequencesBodySplitLitBuffer's
// switch: the leftover literals copied, then the literals from r.extra and
// execSequence, the run no longer split.
func (r *seqFastRun) execPending(sq seq, extDict []byte) dErr {
	f := &r.f
	var n int
	var e dErr
	switch {
	case f.split && f.litPtr+sq.litLength > f.litEnd:
		if leftoverLit := f.litEnd - f.litPtr; leftoverLit != 0 {
			if uint(leftoverLit) > uint(f.oend-f.op) {
				return dErrBlockDstSizeTooSmall
			}
			safecopyDstBeforeSrc(f.out, f.op, f.litPtr, leftoverLit)
			sq.litLength -= leftoverLit
			f.op += leftoverLit
		}
		f.lits, f.litPtr, f.litEnd, f.split = r.extra, 0, litBufferExtraSize, false
		n, e = execSequence(f.out, f.op, f.oend, sq, f.lits, &f.litPtr, f.litEnd, f.prefixStart, extDict)
	case f.split:
		n, e = execSequenceSplitLitBuffer(f.out, f.op, f.oend, f.litPtr+sq.litLength-wildcopyOverlength,
			sq, &f.litPtr, f.litEnd, f.prefixStart, extDict)
	default:
		n, e = execSequence(f.out, f.op, f.oend, sq, f.lits, &f.litPtr, f.litEnd, f.prefixStart, extDict)
	}
	if e != 0 {
		return e
	}
	f.op += n
	f.nbSeq--
	return 0
}

// seqSplitRan and seqSplitPhase2Ran count the sequences compareSeqFast ran
// through the fast loops in split mode, before and after the switch to
// litExtraBuffer.
var seqSplitRan, seqSplitPhase2Ran atomic.Int64

// sameSeqFastState fails t unless the two runs are in the same state.
func sameSeqFastState(t testing.TB, what string, a, g *seqFastRun) {
	t.Helper()
	_, ap, ac, an := a.st.dStream.Raw()
	_, gp, gc, gn := g.st.dStream.Raw()
	if ap != gp || ac != gc || an != gn {
		t.Fatalf("%s: bitstream asm (%d, %#x, %d), Go (%d, %#x, %d)", what, ap, ac, an, gp, gc, gn)
	}
	if a.st.stateLL.state != g.st.stateLL.state || a.st.stateML.state != g.st.stateML.state ||
		a.st.stateOffb.state != g.st.stateOffb.state || a.st.prevOffset != g.st.prevOffset {
		t.Fatalf("%s: states asm (%d %d %d) rep %v, Go (%d %d %d) rep %v", what,
			a.st.stateLL.state, a.st.stateML.state, a.st.stateOffb.state, a.st.prevOffset,
			g.st.stateLL.state, g.st.stateML.state, g.st.stateOffb.state, g.st.prevOffset)
	}
	if a.f.op != g.f.op || a.f.litPtr != g.f.litPtr || a.f.nbSeq != g.f.nbSeq || a.f.split != g.f.split {
		t.Fatalf("%s: asm op %d litPtr %d nbSeq %d split %v, Go op %d litPtr %d nbSeq %d split %v", what,
			a.f.op, a.f.litPtr, a.f.nbSeq, a.f.split, g.f.op, g.f.litPtr, g.f.nbSeq, g.f.split)
	}
	if !bytes.Equal(a.f.out, g.f.out) {
		t.Fatalf("%s: output differs", what)
	}
}

// compareSeqFast runs the assembly (where the CPU has BMI2; the Go loop
// again otherwise) and Go fast loops side by side from a and g, executing
// each pending sequence on both (execPending: for split runs, through the
// switch to litExtraBuffer and on), and fails t at the first difference.
// It returns the number of sequences the loops executed.
func compareSeqFast(t testing.TB, a, g *seqFastRun, extDict []byte) int {
	t.Helper()
	checkOK := func() {
		if f := a.f; !f.asmOK() {
			t.Fatalf("sequencesFast preconditions (split %v): oend %d, len(out) %d, litEnd %d, len(lits) %d, op %d, litPtr %d, prefixStart %d",
				f.split, f.oend, len(f.out), f.litEnd, len(f.lits), f.op, f.litPtr, f.prefixStart)
		}
	}
	checkOK()
	ran, switched := 0, false
	for a.f.nbSeq > 1 {
		wasSplit := a.f.split
		before := a.f.nbSeq
		var asq seq
		var ap bool
		if cpu.X86.HasBMI2 {
			asq, ap = a.f.runAsm(&a.st)
		} else {
			asq, ap = a.f.runGo(&a.st)
		}
		gsq, gp := g.f.runGo(&g.st)
		sameSeqFastState(t, "after the loop", a, g)
		if ap != gp || asq != gsq {
			t.Fatalf("asm pending %v %+v, Go pending %v %+v", ap, asq, gp, gsq)
		}
		n := before - a.f.nbSeq
		ran += n
		switch {
		case wasSplit:
			seqSplitRan.Add(int64(n))
		case switched:
			seqSplitPhase2Ran.Add(int64(n))
		}
		if !ap {
			break
		}
		ae, ge := a.execPending(asq, extDict), g.execPending(gsq, extDict)
		if ae != ge {
			t.Fatalf("pending sequence: asm err %d, Go err %d", ae, ge)
		}
		if ae != 0 {
			break
		}
		sameSeqFastState(t, "after the pending sequence", a, g)
		if wasSplit && !a.f.split {
			switched = true
			checkOK()
		}
	}
	return ran
}

// seqRawLitsRan counts the sequences checkBlockSeqFast ran through the
// fast loops with the literals raw in the block (notInDst), read in place:
// the production layout, whose litEnd + 32 <= len(lits) compareSeqFast
// checks (seqFast.asmOK).
var seqRawLitsRan atomic.Int64

// checkBlockSeqFast decodes block's literals and sequence headers on a
// shallow copy of d (d itself is left as it is), as decompressBlockInternal
// would with these arguments, and compares the fast loops on the block's
// sequences (compareSeqFast): with the buffers against guard pages (the
// output after and the bitstream after one, then both before one), and
// with the literals in place (compareSeqFast then also checks
// decompressSequencesBody's buffers against seqFast.asmOK). Split literals
// run as decompressSequencesBodySplitLitBuffer's loops. It returns the
// sequences the loops executed, or -1 when the block has no sequences to
// compare (a header error, or none).
func checkBlockSeqFast(t testing.TB, d *dctx, dst []byte, dstCap int, block []byte, streaming streamingOperation, checkContinuity bool) int {
	t.Helper()
	d2 := *d
	if checkContinuity {
		d2.checkContinuity(dst, dstCap)
	}
	ostart := len(dst)
	out := append([]byte(nil), dst[:ostart+dstCap]...)
	litCSize, e := d2.decodeLiteralsBlock(block, out[ostart:], dstCap, streaming)
	if e != 0 {
		return -1
	}
	nbSeq, hSize, e := decodeSeqHeaders(&d2, block[litCSize:])
	if e != 0 || nbSeq == 0 || (dstCap == 0 && nbSeq > 0) {
		return -1
	}
	checkTableBound(t, "LL", d2.llTptr)
	checkTableBound(t, "ML", d2.mlTptr)
	checkTableBound(t, "OF", d2.ofTptr)
	seqSrc := block[litCSize+hSize:]
	rawLits := d2.litBufferLocation == notInDst && len(d2.litBuf) != 0 && &d2.litBuf[0] == &block[0]
	ran := -1
	for _, m := range []struct{ guard, before bool }{{true, false}, {true, true}, {false, false}} {
		a := newSeqFastRun(t, &d2, out, ostart, dstCap, seqSrc, nbSeq, m.guard, m.before, m.before)
		if a == nil {
			return -1
		}
		g := newSeqFastRun(t, &d2, out, ostart, dstCap, seqSrc, nbSeq, false, false, false)
		ran = compareSeqFast(t, a, g, d2.extDict)
		if !m.guard && rawLits {
			seqRawLitsRan.Add(int64(ran))
		}
	}
	return ran
}

// TestSeqAsmEquiv runs the block decoder tests (TestAsmEquiv runs
// TestDecompressBlockCref) with the assembly off, on, and on with BMI2
// cleared (the Go loop again), and compares the fast
// loops directly on every compressed block of the corpus frames, decoded
// block by block in the modes of TestDecompressBlockCref (in_dst, not_in_dst
// and extDict history), and on blocks with a corrupted sequence bitstream.
func TestSeqAsmEquiv(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"DecompressBlockErrorsCref", TestDecompressBlockErrorsCref},
		{"DecompressBlockContinuity", TestDecompressBlockContinuity},
		{"DecompressBlockSplitLiterals", TestDecompressBlockSplitLiterals},
	}
	run := func(name string, on bool) {
		withAsm(t, on, func() {
			t.Run(name, func(t *testing.T) {
				for _, c := range tests {
					t.Run(c.name, c.fn)
				}
			})
		})
	}
	run("asm=off", false)
	run("asm=on", true)
	if cpu.X86.HasBMI2 {
		restore := cpu.DisableBMI2()
		run("asm=on,bmi2=off", true)
		restore()
	}

	if !cpu.X86.HasBMI2 {
		t.Log("no BMI2: the direct loop comparison runs the Go loop twice")
	}
	arms, raw := seqArmCounts(), seqRawLitsRan.Load()
	split1, split2 := seqSplitRan.Load(), seqSplitPhase2Ran.Load()
	blocks, ran := 0, 0
	compressedBlockHook = func(d *dctx, dst []byte, dstCap int, block []byte, streaming streamingOperation, cc bool) {
		// A subtest per block unmaps its guard pages when it ends.
		t.Run("block", func(t *testing.T) {
			if n := checkBlockSeqFast(t, d, dst, dstCap, block, streaming, cc); n >= 0 {
				blocks++
				ran += n
			}
		})
	}
	defer func() { compressedBlockHook = nil }()
	rng := rand.New(rand.NewPCG(3, 5))
	nFrames := 0
	for _, name := range creftest.FrameNames(t) {
		if strings.HasPrefix(name, "dict") || strings.HasPrefix(name, "multi/") || strings.HasPrefix(name, "skippable") {
			continue
		}
		f := creftest.LoadFrame(t, name)
		if f.CErr != "" || f.Dict != nil || len(f.Want) > 1<<21 {
			continue // larger frames: TestDecompressBlockCref, in TestAsmEquiv
		}
		fsi := findFrameSizeInfo(f.Data)
		if fsi.err != 0 || fsi.compressedSize != len(f.Data) || isSkippableFrame(f.Data) {
			continue
		}
		nFrames++
		for _, m := range []struct {
			mode  blockMode
			segAt int
		}{{bmBlock, -1}, {bmFrame, -1}, {bmStream, -1}, {bmBlock, max(1, fsi.nbBlocks/2)}} {
			decodeFrameByBlocks(new(dctx), f.Data, len(f.Want), 0, m.segAt, m.mode, false)
		}
		// The same frame with a byte of its last block's sequence
		// bitstream (read backwards from the end) changed.
		if len(f.Data) > 32 {
			c := append([]byte(nil), f.Data...)
			c[len(c)-1-rng.IntN(16)] ^= byte(1 + rng.IntN(255))
			decodeFrameByBlocks(new(dctx), c, len(f.Want), 0, -1, bmBlock, false)
		}
	}
	if nFrames < 100 || ran == 0 {
		t.Fatalf("%d frames, %d blocks, %d sequences through the fast loops", nFrames, blocks, ran)
	}
	t.Logf("%d frames, %d blocks, %d sequences through the fast loops", nFrames, blocks, ran)
	if n := seqRawLitsRan.Load() - raw; n == 0 {
		t.Error("no sequences through the fast loops with raw literals read in the block")
	} else {
		t.Logf("%d of them with raw literals read in the block", n)
	}
	if n1, n2 := seqSplitRan.Load()-split1, seqSplitPhase2Ran.Load()-split2; n1 == 0 || n2 == 0 {
		t.Errorf("split literals: %d sequences through the fast loops before the switch, %d after", n1, n2)
	} else {
		t.Logf("split literals: %d sequences through the fast loops before the switch, %d after", n1, n2)
	}
	checkSeqArms(t, arms, equivArms...)
}

// TestSeqAsmSyntheticStates compares the loops from states a valid stream
// does not reach: a bitstream in overflow or with random bits consumed, an
// offset table that breaks the TABLE INVARIANT, random repeat offsets
// (including 0 and C's (size_t)-1), output positions near oend, and split
// literals (random bytes of out) starting close ahead of op, which the
// output catches up with.
func TestSeqAsmSyntheticStates(t *testing.T) {
	r0 := rand.New(rand.NewPCG(1, 1))
	src := make([]byte, 1<<16)
	for i := range src {
		src[i] = "abcdefgh"[r0.IntN(8)] // short matches, offsets of all sizes
	}
	frame := newTestEncoder(t).EncodeAll(src, nil)
	var block []byte
	var zfh frameHeader
	if need, e := getFrameHeaderAdvanced(&zfh, frame); need != 0 || e != 0 {
		t.Fatal("frame header")
	}
	for ip := int(zfh.headerSize); ip < len(frame); {
		cSize, bp, e := getcBlockSize(frame[ip:])
		if e != 0 {
			t.Fatal(e)
		}
		if bp.blockType == btCompressed {
			block = frame[ip+blockHeaderSize : ip+blockHeaderSize+cSize]
			break
		}
		ip += blockHeaderSize + cSize
	}
	if block == nil {
		t.Fatal("no compressed block")
	}
	rng := rand.New(rand.NewPCG(9, 9))
	arms := seqArmCounts()
	ran := 0
	for iter := 0; iter < 3000; iter++ {
		d := newTestDctx(blockSizeMax)
		dstCap := blockSizeMax + 2*wildcopyOverlength + rng.IntN(1<<17)
		hist := rng.IntN(1 << 16)
		out := make([]byte, hist+dstCap)
		for i := range out {
			out[i] = byte(rng.Uint32())
		}
		litCSize, e := d.decodeLiteralsBlock(block, out[hist:], dstCap, streamingOperation(iter&1))
		if e != 0 {
			t.Fatal(e)
		}
		nbSeq, hSize, e := decodeSeqHeaders(d, block[litCSize:])
		if e != 0 || d.litBufferLocation == split {
			continue
		}
		checkTableBound(t, "LL", d.llTptr)
		checkTableBound(t, "ML", d.mlTptr)
		checkTableBound(t, "OF", d.ofTptr)
		d.prefixStart = rng.IntN(hist + 1)
		if iter%5 == 0 {
			nbSeq = min(nbSeq, 2+rng.IntN(8)) // the loop reaches nbSeq 1
		}
		runs := [2]*seqFastRun{
			newSeqFastRun(t, d, out, hist, dstCap, block[litCSize+hSize:], nbSeq, true, iter&2 != 0, iter&8 != 0),
			newSeqFastRun(t, d, out, hist, dstCap, block[litCSize+hSize:], nbSeq, false, false, false),
		}
		var ofTable []seqSymbol
		switch iter % 7 {
		case 0: // break the TABLE INVARIANT
			ofTable = append([]seqSymbol(nil), runs[0].st.stateOffb.table...)
			for i := range ofTable {
				if rng.IntN(4) == 0 {
					ofTable[i].nbAdditionalBits, ofTable[i].baseValue = 1, uint32(rng.IntN(4))
				}
			}
		case 3: // offsets of up to 31 extra bits: the first reload
			ofTable = append([]seqSymbol(nil), runs[0].st.stateOffb.table...)
			for i := range ofTable {
				if rng.IntN(4) == 0 {
					ofTable[i].nbAdditionalBits = uint8(24 + rng.IntN(8))
				}
			}
		}
		rep := [repNum]int{rng.IntN(70000), rng.IntN(20), -1}
		if iter%3 == 0 {
			rep[rng.IntN(repNum)] = 0
		}
		_, _, container, consumed := runs[0].st.dStream.Raw()
		if iter%2 == 0 {
			container = rng.Uint64()
		}
		if iter%4 < 2 {
			consumed = uint32(rng.IntN(80))
		}
		op := hist + rng.IntN(runs[0].f.oend-hist+1)
		// Split (decompressSequencesBodySplitLitBuffer's first loop): the
		// literals at out[litPtr:litEnd], up to 1 KiB ahead of op, then
		// in extra (random bytes, the same on both sides).
		isSplit := iter%6 == 1
		lim := len(out) - wildcopyOverlength
		litPtr := min(op+rng.IntN(1024), lim)
		litEnd := litPtr + rng.IntN(lim-litPtr+1)
		extra := make([]byte, litBufferExtraSize+wildcopyOverlength)
		for i := range extra {
			extra[i] = byte(rng.Uint32())
		}
		for i, r := range runs {
			s, ptr, _, _ := r.st.dStream.Raw()
			r.st.dStream.SetRaw(s, ptr, container, consumed)
			r.st.prevOffset = rep
			if ofTable != nil {
				r.st.stateOffb.table = ofTable
			}
			r.f.op = op
			if isSplit {
				r.f.lits, r.f.litPtr, r.f.litEnd, r.f.split = r.f.out, litPtr, litEnd, true
				r.extra = extra
				if i == 0 {
					r.extra = guardmem.After(t, len(extra))
					copy(r.extra, extra)
				}
			}
		}
		ran += compareSeqFast(t, runs[0], runs[1], nil)
	}
	t.Logf("%d sequences through the fast loops", ran)
	checkSeqArms(t, arms, syntheticArms...)
}

// FuzzSeqAsmEquiv decodes hostile blocks as FuzzDecompressBlock does (same
// inputs and flags, plus a seed whose block has split literals) and
// compares, on each, the fast loops directly
// (checkBlockSeqFast: guard pages, state after every call) and the whole
// block decode with the assembly on and off (size, error and output).
func FuzzSeqAsmEquiv(f *testing.F) {
	ents, _ := os.ReadDir(seedsDir)
	for i, e := range ents {
		frame, err := os.ReadFile(filepath.Join(seedsDir, e.Name()))
		if err != nil {
			continue
		}
		var zfh frameHeader
		if need, de := getFrameHeaderAdvanced(&zfh, frame); need != 0 || de != 0 || zfh.frameType != zstdFrame {
			continue
		}
		for ip := int(zfh.headerSize); ip < len(frame); {
			cSize, bp, de := getcBlockSize(frame[ip:])
			if de != 0 || ip+blockHeaderSize+cSize > len(frame) {
				break
			}
			if bp.blockType == btCompressed {
				f.Add(frame[ip+blockHeaderSize:ip+blockHeaderSize+cSize], uint16(i*97), uint16(i*13), uint32(blockSizeMax), byte(i))
			}
			ip += blockHeaderSize + cSize
			if bp.lastBlock {
				break
			}
		}
	}
	// A block with over litBufferExtraSize literals, decoded streaming
	// into exactly its size: split literals (the seeds above have none).
	r := rand.New(rand.NewPCG(20, 4))
	src := make([]byte, blockSizeMax)
	for i := 0; i < len(src); {
		if i > 64 && r.IntN(40) == 0 { // a match: about 25% of the bytes
			n := min(4+r.IntN(16), len(src)-i)
			copy(src[i:i+n], src[r.IntN(i-n+1):])
			i += n
			continue
		}
		src[i] = byte('a' + r.IntN(16))
		i++
	}
	if frame := newTestEncoder(f).EncodeAll(src, nil); len(frame) > 0 {
		var zfh frameHeader
		if need, de := getFrameHeaderAdvanced(&zfh, frame); need == 0 && de == 0 {
			if cSize, bp, de := getcBlockSize(frame[zfh.headerSize:]); de == 0 && bp.blockType == btCompressed {
				start := int(zfh.headerSize) + blockHeaderSize
				f.Add(frame[start:start+cSize], uint16(0), uint16(0), uint32(blockSizeMax), byte(0x71))
			}
		}
	}
	f.Fuzz(func(t *testing.T, block []byte, hist, ext uint16, dstCap uint32, flags byte) {
		setup := func() (*dctx, []byte, int) {
			d := new(dctx)
			d.decompressBegin()
			d.isFrameDecompression = flags&2 != 0
			d.fParams.blockSizeMax = uint32(min(blockSizeMax, 1<<(10+flags>>4%8)))
			capMax := 2*d.blockSizeMax() + 2*litBufferExtraSize
			dc := int(dstCap) % (capMax + 1)
			prev := make([]byte, ext)
			for i := range prev {
				prev[i] = byte(i * 7)
			}
			d.prevDst, d.previousDstEnd = prev, len(prev)
			buf := make([]byte, int(hist), int(hist)+2*dc)
			for i := range buf {
				buf[i] = byte(i * 5)
			}
			d.checkContinuity(buf[:0], len(buf)+dc)
			d.previousDstEnd = len(buf)
			return d, buf, dc
		}
		streaming := streamingOperation(flags & 1)
		d, buf, dc := setup()
		checkBlockSeqFast(t, d, buf, dc, block, streaming, true)

		var outs [2][]byte
		var sizes [2]int
		var errs [2]dErr
		for i, on := range []bool{false, true} {
			withAsm(t, on, func() {
				d, buf, dc := setup()
				for round := 0; round < 1+int(flags>>3&1); round++ {
					d.ddictIsCold = flags&4 != 0
					d.checkContinuity(buf, dc)
					n, e := d.decompressBlockInternal(buf, dc, block, streaming)
					sizes[i], errs[i] = n, e
					if e != 0 {
						break
					}
					buf = buf[:len(buf)+n]
					d.previousDstEnd = len(buf)
				}
				outs[i] = buf
			})
		}
		if sizes[0] != sizes[1] || errs[0] != errs[1] || !bytes.Equal(outs[0], outs[1]) {
			t.Fatalf("asm off: size %d err %d; asm on: size %d err %d; output equal %v",
				sizes[0], errs[0], sizes[1], errs[1], bytes.Equal(outs[0], outs[1]))
		}
	})
}
