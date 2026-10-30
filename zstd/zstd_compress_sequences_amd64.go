//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useSeqAsm selects the assembly loop of encodeSequences (baseline amd64);
// useSeqBMI2 its BMI2 variant.
var useSeqAsm, useSeqBMI2 bool

func init() {
	cpu.RegisterAsm("sequences", &useSeqAsm, func() bool { return true })
	cpu.RegisterAsm("sequences-bmi2", &useSeqBMI2, func() bool { return cpu.X86.HasBMI2 })
}

// encodeSequencesAsm encodes seqs[n-1] down to seqs[0] (C: the loop of
// ZSTD_encodeSequences_body with longOffsets = 0), continuing from the given
// bit writer state (buf, ptr, endPtr, container, bitPos) and FSE states,
// and returns the updated ones. stateTables are the CTables' u16 state
// tables (ct[1:]), tts their symbolTT, and mlMax/ofMax/llMax the tables'
// maxSymbolValue: a code above it (impossible: the tables are built from
// these codes' histograms) stops the loop with bitPosOut = 64 instead of
// reading outside the symbolTT.
//
// Unchecked precondition: the CTables are valid FSE tables (built by
// fse.BuildCTable or BuildCTableRLE, or C-checked repeats of them): the
// state-table reads stateTable[(state >> nbBitsOut) + deltaFindState] are
// not bounds-checked and stay inside the table only for such tables, as in
// C. With a malformed table the Go body would panic where this reads out of
// bounds.
//
// Preconditions (checked by encodeSequences): the code slices and seqs hold
// n entries; 0 <= ptr <= endPtr = len(buf) - 8 (each flush writes 8 bytes at ptr and
// clamps ptr to endPtr, as BIT_flushBits); bitPos < 8.
//
//go:noescape
func encodeSequencesAsm(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32)

// encodeSequencesAsmBMI2 is encodeSequencesAsm using BMI2.
//
//go:noescape
func encodeSequencesAsmBMI2(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32)

// encodeSequencesAsmVariant runs encodeSequencesAsmBMI2 when it is on, else
// encodeSequencesAsm.
func encodeSequencesAsmVariant(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32) {
	if useSeqBMI2 {
		return encodeSequencesAsmBMI2(seqs, llCodes, mlCodes, ofCodes, n,
			mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT, llBitsTab, mlBitsTab,
			buf, ptr, endPtr, container, bitPos, stML, stOF, stLL, mlMax, ofMax, llMax)
	}
	return encodeSequencesAsm(seqs, llCodes, mlCodes, ofCodes, n,
		mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT, llBitsTab, mlBitsTab,
		buf, ptr, endPtr, container, bitPos, stML, stOF, stLL, mlMax, ofMax, llMax)
}
