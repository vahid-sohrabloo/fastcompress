// The body of decodeExecSeqsAsm and decodeExecSeqsSplitAsm
// (zstd_decompress_block_amd64.s, which has their BOUNDS and includes this
// in each TEXT symbol; labels are local to each symbol). With SEQ_SPLIT
// defined it adds ZSTD_execSequenceSplitLitBuffer's oMatchEnd > oend_w
// hand-back. Two entries keep decodeExecSeqsAsm's loop exactly as it was:
// a run-time switch in the shared loop cost 1 to 2% on blocks that never
// split. On entry AX is the argument a; the including TEXT supplies the
// pending and exit labels (SEQ_PENDING, SEQ_EXIT). No a+0(FP) here: go
// vet's asmdecl does not read #included files, so every FP reference stays
// in the .s, where it checks them.

	MOVQ ARGS_BC(AX), R8
	MOVQ ARGS_CONSUMED(AX), R9
	MOVQ ARGS_SRC(AX), CX
	MOVQ ARGS_PTR(AX), R10
	ADDQ CX, R10
	LEAQ 16(CX), CX
	MOVQ CX, SRCLIMIT(SP)

	MOVQ ARGS_TLL(AX), CX
	MOVQ CX, TLL(SP)
	MOVQ ARGS_STLL(AX), DX
	MOVQ DX, STLL(SP)
	MOVQ (CX)(DX*8), R13
	MOVQ ARGS_TML(AX), CX
	MOVQ CX, TML(SP)
	MOVQ ARGS_STML(AX), DX
	MOVQ DX, STML(SP)
	MOVQ (CX)(DX*8), R14
	MOVQ ARGS_TOF(AX), CX
	MOVQ CX, TOF(SP)
	MOVQ ARGS_STOF(AX), DX
	MOVQ DX, STOF(SP)
	MOVQ (CX)(DX*8), R15

	MOVQ ARGS_REP+0(AX), BX
	MOVQ ARGS_REP+8(AX), CX
	MOVQ CX, REP1(SP)
	MOVQ ARGS_REP+16(AX), CX
	MOVQ CX, REP2(SP)

	MOVQ ARGS_OUT(AX), CX
	MOVQ ARGS_OP(AX), R11
	ADDQ CX, R11
	MOVQ ARGS_OENDW(AX), DX
	ADDQ CX, DX
	MOVQ DX, OENDW(SP)
	MOVQ ARGS_PREFIX(AX), DX
	ADDQ CX, DX
	MOVQ DX, PREFIX(SP)
	MOVQ ARGS_LITS(AX), CX
	MOVQ ARGS_LITPTR(AX), R12
	ADDQ CX, R12
	MOVQ ARGS_LITEND(AX), DX
	ADDQ CX, DX
	MOVQ DX, LITEND(SP)
	MOVQ ARGS_NBSEQ(AX), DX
	MOVQ DX, NBSEQ(SP)
	MOVQ $0, ARGS_PENDING(AX)

loop:
	// for ; nbSeq > 1; nbSeq-- (the last sequence is Go's), while ptr >= 16.
	CMPQ NBSEQ(SP), $1
	JLE  exit
	CMPQ R10, SRCLIMIT(SP)
	JCS  exit

	// decodeSequence, from the three cells.
	MOVQ R13, SI
	SHRQ $32, SI         // litLength = llDInfo.baseValue
	MOVQ R14, DI
	SHRQ $32, DI         // matchLength = mlDInfo.baseValue
	BYTE_OF(R15, 16, CX) // ofBits
	CMPQ CX, $1
	JHI  ofLong
	JCS  ofRepeat

	// ofBits == 1: offset = ofBase + ll0 + ReadBitsFast(1).
	MOVQ R15, AX
	SHRQ $32, AX
	CMPQ AX, $1
	JNE  exit            // TABLE INVARIANT broken: nothing consumed yet
	SHLXQ R9, R8, AX
	SHRQ  $63, AX
	INCQ  R9
	XORL  DX, DX
	TESTQ SI, SI
	SETEQ DL             // ll0
	LEAQ  1(AX)(DX*1), DX
	CMPQ  DX, $2
	JEQ   ofRep2
	JHI   ofRep0Minus1
	// offset 1: temp = prevOffset[1].
	MOVQ REP1(SP), DX
	CMPQ DX, $1
	SBBQ $0, DX          // temp -= !temp
	MOVQ BX, REP1(SP)
	MOVQ DX, BX
	JMP  ofDone

ofRep2:
	// offset 2: temp = prevOffset[2].
	MOVQ REP2(SP), DX
	CMPQ DX, $1
	SBBQ $0, DX
	MOVQ REP1(SP), AX
	MOVQ AX, REP2(SP)
	MOVQ BX, REP1(SP)
	MOVQ DX, BX
	JMP  ofDone

ofRep0Minus1:
	// offset 3: temp = prevOffset[0] - 1.
	LEAQ -1(BX), DX
	CMPQ DX, $1
	SBBQ $0, DX
	MOVQ REP1(SP), AX
	MOVQ AX, REP2(SP)
	MOVQ BX, REP1(SP)
	MOVQ DX, BX
	JMP  ofDone

ofRepeat:
	// ofBits == 0: offset = prevOffset[ll0], and prevOffset[1] =
	// prevOffset[!ll0].
	MOVQ  BX, DX
	TESTQ SI, SI
	JNE   ofDone
	MOVQ  REP1(SP), DX
	MOVQ  BX, REP1(SP)
	MOVQ  DX, BX
	JMP   ofDone

ofLong:
	// ofBits > 1: offset = ofBase + ReadBitsFast(ofBits).
	READ_BITS_FAST(CX, AX, BP)
	MOVQ R15, DX
	SHRQ $32, DX
	ADDQ AX, DX
	MOVQ REP1(SP), AX
	MOVQ AX, REP2(SP)
	MOVQ BX, REP1(SP)
	MOVQ DX, BX

ofDone:
	BYTE_OF(R14, 16, CX)     // mlBits
	TESTQ   CX, CX
	JEQ     mlDone
	READ_BITS_FAST(CX, AX, BP)
	ADDQ    AX, DI

mlDone:
	// if totalBits >= STREAM_ACCUMULATOR_MIN_64 - (LLFSELog+MLFSELog+OffFSELog)
	BYTE_OF(R13, 16, AX)     // llBits
	BYTE_OF(R15, 16, BP)
	ADDQ    CX, BP
	ADDQ    AX, BP
	CMPQ    BP, $31
	JCS     reload1Done
	RELOAD(reload1Done)

reload1Done:
	TESTQ AX, AX
	JEQ   llDone
	READ_BITS_FAST(AX, CX, BP)
	ADDQ  CX, SI

llDone:
	UPDATE_STATE(R13, TLL, STLL)
	UPDATE_STATE(R14, TML, STML)
	UPDATE_STATE(R15, TOF, STOF)
	RELOAD(reload2Done)

reload2Done:
	// execSequence: hand back the sequences for its slow paths.
	LEAQ (R11)(SI*1), AX // oLitEnd
	LEAQ (R12)(SI*1), BP // iLitEnd
	CMPQ BP, LITEND(SP)
	JHI  pending
	LEAQ (AX)(DI*1), CX  // oMatchEnd
	CMPQ CX, OENDW(SP)
	JHI  pending
#ifdef SEQ_SPLIT
	// ZSTD_execSequenceSplitLitBuffer: oMatchEnd > oend_w, where the
	// caller passes oend_w = litPtr + litLength - WILDCOPY_OVERLENGTH.
	SUBQ $32, BP
	CMPQ CX, BP
	JHI  pending
#endif
	MOVQ CX, OMEND(SP)
	MOVQ AX, CX
	SUBQ PREFIX(SP), CX
	CMPQ DX, CX          // uint(offset) > uint(oLitEnd - prefixStart)
	JHI  pending

	// Copy literals: copy16, then wildcopy(op+16, litPtr+16, litLength-16).
	MOVOU (R12), X0
	MOVOU X0, (R11)
	CMPQ  SI, $16
	JLS   litDone
	MOVOU 16(R12), X0
	MOVOU X0, 16(R11)
	CMPQ  SI, $32
	JLS   litDone
	LEAQ  32(R11), CX
	LEAQ  32(R12), BP

litLoop:
	MOVOU (BP), X0
	MOVOU X0, (CX)
	MOVOU 16(BP), X0
	MOVOU X0, 16(CX)
	ADDQ  $32, CX
	ADDQ  $32, BP
	CMPQ  CX, AX
	JCS   litLoop

litDone:
	MOVQ AX, R11         // op = oLitEnd
	ADDQ SI, R12         // litPtr = iLitEnd

	// Copy match.
	MOVQ R11, AX
	SUBQ DX, AX          // match = oLitEnd - offset
	CMPQ DX, $16
	JCS  matchShort

	// offset >= WILDCOPY_VECLEN: wildcopy(op, match, matchLength, noOverlap).
	MOVOU (AX), X0
	MOVOU X0, (R11)
	CMPQ  DI, $16
	JLS   seqDone
	LEAQ  16(R11), CX
	ADDQ  $16, AX
	MOVQ  OMEND(SP), BP

matchLoop:
	MOVOU (AX), X0
	MOVOU X0, (CX)
	MOVOU 16(AX), X0
	MOVOU X0, 16(CX)
	ADDQ  $32, CX
	ADDQ  $32, AX
	CMPQ  CX, BP
	JCS   matchLoop
	JMP   seqDone

matchShort:
	// overlapCopy8(out, op, match, offset).
	CMPQ DX, $8
	JCC  matchCopy8
	MOVB (AX), CL        // offset < 8: four bytes one by one,
	MOVB CL, (R11)
	MOVB 1(AX), CL
	MOVB CL, 1(R11)
	MOVB 2(AX), CL
	MOVB CL, 2(R11)
	MOVB 3(AX), CL
	MOVB CL, 3(R11)
	MOVQ DX, CX
	SHLQ $3, CX
	MOVQ $0x0404040401020100, BP // dec32table
	SHRXQ CX, BP, BP
	MOVBQZX BP, BP
	ADDQ BP, AX          // match += dec32table[offset]
	MOVL (AX), BP        // ZSTD_copy4
	MOVL BP, 4(R11)
	MOVQ $0x0B0A090807080808, BP // dec64table
	SHRXQ CX, BP, BP
	MOVBQZX BP, BP
	SUBQ BP, AX          // match -= dec64table[offset]
	JMP  matchShortRest

matchCopy8:
	MOVQ (AX), CX        // ZSTD_copy8
	MOVQ CX, (R11)

matchShortRest:
	// op += 8, match += 8; now op - match is in 8..15, so wildcopy(op,
	// match, matchLength-8, overlapSrcBeforeDst) is its COPY8 loop.
	ADDQ $8, AX
	LEAQ 8(R11), CX
	CMPQ DI, $8
	JLS  seqDone
	MOVQ OMEND(SP), BP

copy8Loop:
	MOVQ (AX), DX
	MOVQ DX, (CX)
	ADDQ $8, CX
	ADDQ $8, AX
	CMPQ CX, BP
	JCS  copy8Loop

seqDone:
	MOVQ OMEND(SP), R11      // op += sequenceLength
	DECQ NBSEQ(SP)
	JMP  loop

