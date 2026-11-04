//go:build amd64 && !appengine && !noasm && !purego && gc

// The Go side of the sequence fast loop's assembly.

package zstd

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

// asmOK reports whether f meets the preconditions the loop's BOUNDS rely
// on (zstd_decompress_block_amd64.s): out holds oend, lits holds
// WILDCOPY_OVERLENGTH bytes past litEnd (every literal buffer placement
// that reaches decompressSequencesBody leaves them; see execSequence),
// op <= oend, 0 <= litPtr <= litEnd and 0 <= prefixStart <= op. op only grows
// and every executed sequence keeps op <= oend and litPtr <= litEnd, so
// checking them once per block is enough.
func (f *seqFast) asmOK() bool {
	return len(f.out) != 0 && f.oend <= len(f.out) && f.litEnd+wildcopyOverlength <= len(f.lits) &&
		f.op <= f.oend && 0 <= f.litPtr && f.litPtr <= f.litEnd && 0 <= f.prefixStart && f.prefixStart <= f.op
}

// runAsm runs decodeExecSeqsAsm once from st and f, and stores the state it
// leaves back into them. It returns the sequence the loop stopped on, if
// any (pending). Preconditions: sequencesFast's.
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
	decodeExecSeqsAsm(&a)
	st.dStream.SetRaw(src, a.ptr, a.bitContainer, uint32(a.bitsConsumed))
	st.stateLL.state, st.stateML.state, st.stateOffb.state = a.stateLL, a.stateML, a.stateOf
	st.prevOffset = a.rep
	f.op, f.litPtr, f.nbSeq = a.op, a.litPtr, a.nbSeq
	return seq{litLength: a.litLength, matchLength: a.matchLength, offset: a.offset}, a.pending != 0
}
