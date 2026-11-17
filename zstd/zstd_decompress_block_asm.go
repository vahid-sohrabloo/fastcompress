//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the sequence fast loop's and the sequence-table
// builder's assembly, shared by amd64 and arm64.

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/fse"

// seqAsmArgs is the state decodeExecSeqsAsm reads and advances
// (zstd_decompress_block_{amd64,arm64}.s read it at fixed offsets, checked by
// TestSeqAsmArgsLayout). ptr, op, oendW, litPtr, litEnd and prefixStart are
// offsets into src, out and lits, not pointers: the loop adds the bases on
// entry and subtracts them before returning, so the Go side never handles a
// pointer past the end of a buffer.
type seqAsmArgs struct {
	bitContainer uint64      // 0
	bitsConsumed uint64      // 8
	ptr          int         // 16: offset into src
	src          *byte       // 24
	stateLL      int         // 32
	stateML      int         // 40
	stateOf      int         // 48
	tableLL      *seqSymbol  // 56: C's dt + 1
	tableML      *seqSymbol  // 64
	tableOf      *seqSymbol  // 72
	rep          [repNum]int // 80: prevOffset
	out          *byte       // 104
	op           int         // 112
	oendW        int         // 120: oend - WILDCOPY_OVERLENGTH
	lits         *byte       // 128
	litPtr       int         // 136
	litEnd       int         // 144
	prefixStart  int         // 152
	nbSeq        int         // 160
	litLength    int         // 168: the pending sequence
	matchLength  int         // 176
	offset       int         // 184
	pending      int         // 192: 1 when the loop stopped on a sequence it decoded but did not execute
}

// sequencesFast runs the assembly fast loop while f.nbSeq > 1, executing
// with execSequence each sequence the loop decodes but hands back (one that
// takes execSequenceEnd or the extDict path). It does nothing when the
// assembly is off, or when f fails the loop's preconditions (asmOK).
func sequencesFast(st *seqState, f *seqFast, extDict []byte) dErr {
	if !useSeqDecAsm || !f.asmOK() {
		return 0
	}
	for f.nbSeq > 1 {
		sequence, pending := f.runAsm(st)
		if !pending {
			break
		}
		oneSeqSize, e := execSequence(f.out, f.op, f.oend, sequence, f.lits, &f.litPtr, f.litEnd, f.prefixStart, extDict)
		if e != 0 {
			return e
		}
		f.op += oneSeqSize
		f.nbSeq--
	}
	return 0
}

// sequencesFastSplit is sequencesFast for the first loop of
// decompressSequencesBodySplitLitBuffer (f.split, literals in f.out up to
// f.litEnd = dctx->litBufferEnd): it executes each handed-back sequence
// with execSequenceSplitLitBuffer, except the one whose literals cross
// f.litEnd, which it returns decoded but not executed, with brk true (C's
// break to the litExtraBuffer switch). Like sequencesFast, it does nothing
// when the assembly is off or f fails asmOK.
func sequencesFastSplit(st *seqState, f *seqFast, extDict []byte) (sequence seq, brk bool, e dErr) {
	if !useSeqDecAsm || !f.asmOK() {
		return seq{}, false, 0
	}
	for f.nbSeq > 1 {
		sequence, pending := f.runAsm(st)
		if !pending {
			break
		}
		if f.litPtr+sequence.litLength > f.litEnd {
			return sequence, true, 0
		}
		oneSeqSize, e := execSequenceSplitLitBuffer(f.out, f.op, f.oend, f.litPtr+sequence.litLength-wildcopyOverlength,
			sequence, &f.litPtr, f.litEnd, f.prefixStart, extDict)
		if e != 0 {
			return seq{}, false, e
		}
		f.op += oneSeqSize
		f.nbSeq--
	}
	return seq{}, false, 0
}

// asmOK reports whether f meets the preconditions the loops' BOUNDS rely
// on (zstd_decompress_block_{amd64,arm64}.s, both entries): out holds oend, lits
// holds WILDCOPY_OVERLENGTH bytes past litEnd (every literal buffer
// placement that reaches decompressSequencesBody leaves them, see
// execSequence; so does the split layout, lits = out with litEnd =
// litBufferEnd = expectedWriteSize - WILDCOPY_OVERLENGTH),
// op <= oend, 0 <= litPtr <= litEnd and 0 <= prefixStart <= op. op only grows
// and every executed sequence keeps op <= oend and litPtr <= litEnd, so
// checking them once per block is enough.
func (f *seqFast) asmOK() bool {
	return len(f.out) != 0 && f.oend <= len(f.out) && f.litEnd+wildcopyOverlength <= len(f.lits) &&
		f.op <= f.oend && 0 <= f.litPtr && f.litPtr <= f.litEnd && 0 <= f.prefixStart && f.prefixStart <= f.op
}

// runAsm runs the assembly loop once from st and f (decodeExecSeqsSplitAsm
// with f.split, else decodeExecSeqsAsm), and stores the state it leaves
// back into them. It returns the sequence the loop stopped on, if any
// (pending). Preconditions: sequencesFast's, or sequencesFastSplit's.
func (f *seqFast) runAsm(st *seqState) (seq, bool) {
	src, ptr, container, consumed := st.dStream.Raw()
	a := seqAsmArgs{
		bitContainer: container,
		bitsConsumed: uint64(consumed),
		ptr:          ptr,
		src:          &src[0],
		stateLL:      st.stateLL.state,
		stateML:      st.stateML.state,
		stateOf:      st.stateOffb.state,
		tableLL:      &st.stateLL.table[0],
		tableML:      &st.stateML.table[0],
		tableOf:      &st.stateOffb.table[0],
		rep:          st.prevOffset,
		out:          &f.out[0],
		op:           f.op,
		oendW:        f.oend - wildcopyOverlength,
		lits:         &f.lits[0],
		litPtr:       f.litPtr,
		litEnd:       f.litEnd,
		prefixStart:  f.prefixStart,
		nbSeq:        f.nbSeq,
	}
	if f.split {
		decodeExecSeqsSplitAsm(&a)
	} else {
		decodeExecSeqsAsm(&a)
	}
	st.dStream.SetRaw(src, a.ptr, a.bitContainer, uint32(a.bitsConsumed))
	st.stateLL.state, st.stateML.state, st.stateOffb.state = a.stateLL, a.stateML, a.stateOf
	st.prevOffset = a.rep
	f.op, f.litPtr, f.nbSeq = a.op, a.litPtr, a.nbSeq
	return seq{litLength: a.litLength, matchLength: a.matchLength, offset: a.offset}, a.pending != 0
}

// buildFSETableFast builds the table with buildFSETableAsm, and reports
// whether it did. It does nothing when the assembly is off or the
// arguments break its preconditions: fse.MinTableLog <= tableLog <=
// maxFSELog, 1 <= maxSV1 <= maxSeq+1, and norm, baseValue and nbAdd with
// maxSV1 entries, dt with 1+(1<<tableLog) cells.
func buildFSETableFast(dt []seqSymbol, norm []int16, maxSV1 uint, baseValue []uint32, nbAdd []uint8, tableLog uint, wksp *buildFSETableWksp) bool {
	if !useFSETableAsm || tableLog < fse.MinTableLog || tableLog > maxFSELog || maxSV1 == 0 || maxSV1 > maxSeq+1 ||
		uint(len(norm)) < maxSV1 || uint(len(baseValue)) < maxSV1 || uint(len(nbAdd)) < maxSV1 || uint(len(dt)) < 1+1<<tableLog {
		return false
	}
	return buildFSETableAsm(&dt[0], &norm[0], maxSV1, &baseValue[0], &nbAdd[0], tableLog, wksp)
}
