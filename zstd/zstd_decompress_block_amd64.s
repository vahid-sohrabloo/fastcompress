//go:build amd64 && !appengine && !noasm && !purego && gc

// The sequence decode-and-execute fast loop of libzstd v1.5.7
// lib/decompress/zstd_decompress_block.c (Copyright (c) Meta Platforms,
// Inc. and affiliates; BSD/GPLv2 dual license):
// ZSTD_decompressSequences_body's loop, with ZSTD_decodeSequence (64-bit,
// not the last sequence) and ZSTD_execSequence's fast path inlined. C has
// no assembly here; this follows the Go port statement for statement
// (decodeSequence, execSequence, wildcopy, overlapCopy8 and the
// bitstream's ReadBitsFast, ReadBits and Reload), and stops, handing the
// rest back to Go, at C's branch points to the slow paths.

#include "textflag.h"

// seqAsmArgs offsets (TestSeqAsmArgsLayout).
#define ARGS_BC       0
#define ARGS_CONSUMED 8
#define ARGS_PTR      16
#define ARGS_SRC      24
#define ARGS_STLL     32
#define ARGS_STML     40
#define ARGS_STOF     48
#define ARGS_TLL      56
#define ARGS_TML      64
#define ARGS_TOF      72
#define ARGS_REP      80
#define ARGS_OUT      104
#define ARGS_OP       112
#define ARGS_OENDW    120
#define ARGS_LITS     128
#define ARGS_LITPTR   136
#define ARGS_LITEND   144
#define ARGS_PREFIX   152
#define ARGS_NBSEQ    160
#define ARGS_LL       168
#define ARGS_ML       176
#define ARGS_OFF      184
#define ARGS_PENDING  192

// Frame slots.
#define TLL      0   // tables (C: dt + 1)
#define TML      8
#define TOF      16
#define REP1     24  // prevOffset[1], prevOffset[2]
#define REP2     32
#define NBSEQ    40
#define OENDW    48  // pointers: out + oendW, lits + litEnd, out + prefixStart,
#define LITEND   56  // src + 16
#define PREFIX   64
#define SRCLIMIT 72
#define STLL     80  // the states
#define STML     88
#define STOF     96
#define OMEND    104 // oMatchEnd

// Registers: bitContainer R8, bitsConsumed R9, ptr R10, op R11, litPtr R12,
// the states' cells (C's ZSTD_seqSymbol, loaded whole: nextState bits
// 0-15, nbAdditionalBits 16-23, nbBits 24-31, baseValue 32-63) LL R13, ML
// R14, Of R15, prevOffset[0] BX; per sequence litLength SI, matchLength DI,
// offset DX; AX CX BP scratch. BP is free to use: the frame is non-empty,
// so the assembler saves BP in the prologue and restores it before RET.

// BYTE_OF(cell, shift, r): r = (cell >> shift) & 0xFF.
#define BYTE_OF(cell, shift, r) \
	MOVQ    cell, r; \
	SHRQ    $shift, r; \
	MOVBQZX r, r

// RELOAD is DStream.Reload for ptr >= 8 (C: BIT_reloadDStream): nothing in
// overflow (bitsConsumed > 64), else ptr -= bitsConsumed >> 3,
// bitsConsumed &= 7 and the container is the 8 bytes at ptr. Clobbers BP.
// skip is a label after it.
#define RELOAD(skip) \
	CMPQ R9, $64; \
	JHI  skip; \
	MOVQ R9, BP; \
	SHRQ $3, BP; \
	SUBQ BP, R10; \
	ANDQ $7, R9; \
	MOVQ (R10), R8

// READ_BITS_FAST(n, v, t): v = DStream.ReadBitsFast(n) for n >= 1 (C:
// BIT_readBitsFast): (container << (bitsConsumed & 63)) >> (-n & 63);
// bitsConsumed += n. Clobbers t.
#define READ_BITS_FAST(n, v, t) \
	SHLXQ R9, R8, v; \
	MOVQ  n, t; \
	NEGQ  t; \
	SHRXQ t, v, v; \
	ADDQ  n, R9

// UPDATE_STATE(cell, table, state): state = cell.nextState +
// DStream.ReadBits(cell.nbBits) (C: ZSTD_updateFseStateWithDInfo;
// BIT_readBits, whose mask keeps all 64 bits for nbBits >= 64, as BZHI
// does), saved in the frame slot state; cell = table[state], the next
// sequence's. Clobbers AX, CX.
#define UPDATE_STATE(cell, table, state) \
	BYTE_OF(cell, 24, CX); \
	MOVQ    $64, AX; \
	SUBQ    R9, AX; \
	SUBQ    CX, AX; \
	SHRXQ   AX, R8, AX; \
	BZHIQ   CX, AX, AX; \
	ADDQ    CX, R9; \
	MOVWQZX cell, CX; \
	ADDQ    AX, CX; \
	MOVQ    CX, state(SP); \
	MOVQ    table(SP), AX; \
	MOVQ    (AX)(CX*8), cell

// func decodeExecSeqsAsm(a *seqAsmArgs)
//
// C: zstd_decompress_block.c:ZSTD_decompressSequences_body's loop (Go:
// decompressSequencesBody, seqFast.runGo). Runs while nbSeq > 1 and
// ptr >= 16, decoding each sequence (decodeSequence, isLastSeq false) and
// executing it (execSequence's fast path). It stops with nothing of the
// sequence consumed when nbSeq <= 1, ptr < 16, or the offset cell has
// nbAdditionalBits 1 and baseValue != 1 (see TABLE INVARIANT); and, with
// pending = 1 and the sequence in litLength/matchLength/offset, decoded
// but not executed, at execSequence's branches to its slow paths:
// iLitEnd > litEnd or oMatchEnd > oend - WILDCOPY_OVERLENGTH
// (execSequenceEnd), or offset > oLitEnd - prefixStart as unsigned
// (the extDict path, or offset beyond history). Preconditions
// (sequencesFast checks the last five, seqFast.asmOK; runAsm passes the
// rest):
//   - the bitstream, states and tables are initSeqState's or later ones
//     of the same block; len(src) >= 1;
//   - each table has 1 << tableLog cells after its header, and every cell
//     has nextState + (1 << nbBits) <= 1 << tableLog
//     (TestBuildFSETableBound);
//   - 1 <= len(out) and oend <= len(out);
//   - litEnd + 32 <= len(lits);
//   - op <= oend;
//   - 0 <= litPtr <= litEnd;
//   - 0 <= prefixStart <= op.
//
// BOUNDS:
// (1) Bitstream reads are the 8 bytes at ptr. DStream.Init leaves ptr <=
//     len(src) - 8 and ptr only decreases. A sequence starts only with
//     ptr >= 16 and reloads at most twice; a reload in overflow
//     (bitsConsumed > 64) does nothing, and any other lowers ptr by
//     bitsConsumed >> 3 <= 8. So both reloads take the fast path
//     (C: ptr >= limitPtr, start + 8) and read at ptr >= 0.
// (2) Cell reads: table[state], on entry and right after each update (the
//     next sequence's cell, also when the loop then stops). state =
//     nextState + lowBits with lowBits < 1 << nbBits, and nextState +
//     (1 << nbBits) <= 1 << tableLog for every cell (TestBuildFSETableBound,
//     and checkTableBound on every table the equivalence tests use); the
//     first states (initFseState) are tableLog-bit values. The tables have
//     1 << tableLog cells after the header.
// (3) Repeat offsets: prevOffset[offset] with offset = 1 + ll0 + bit, in
//     1..3 (3 reads none), is read only when the offset cell has
//     nbAdditionalBits 1 and baseValue 1; other cells with
//     nbAdditionalBits 1 stop the loop first.
// (4) Literal reads: copy16 and wildcopy read at litPtr >= 0
//     (precondition; litPtr only grows) and below iLitEnd + 32 <=
//     litEnd + 32 <= len(lits) (iLitEnd > litEnd is handed back).
// (5) Writes: below oMatchEnd + 32 <= oend <= len(out) (oMatchEnd >
//     oendW is handed back); wildcopy and the 8-byte copies write less than
//     32 bytes past their end.
// (6) Match reads: match = oLitEnd - offset >= prefixStart >= 0 (offset >
//     oLitEnd - prefixStart is handed back), below the bytes being written.
//     The compare is unsigned: it bounds match only because
//     prefixStart <= op <= oLitEnd (precondition), so oLitEnd - prefixStart
//     does not wrap.
// (7) Frame $112-8: one pointer argument, no results; the slots above.
//
// TABLE INVARIANT: an offset cell with nbAdditionalBits 1 has baseValue 1
// (OF_base[1]: buildFSETable and buildSeqTableRLE take both from the code,
// OF_bits[1] == 1). The loop checks it before using prevOffset[offset].
TEXT ·decodeExecSeqsAsm(SB), NOSPLIT, $112-8
	MOVQ a+0(FP), AX

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

pending:
	MOVQ a+0(FP), AX
	MOVQ SI, ARGS_LL(AX)
	MOVQ DI, ARGS_ML(AX)
	MOVQ DX, ARGS_OFF(AX)
	MOVQ $1, ARGS_PENDING(AX)

exit:
	MOVQ a+0(FP), AX
	MOVQ R8, ARGS_BC(AX)
	MOVQ R9, ARGS_CONSUMED(AX)
	SUBQ ARGS_SRC(AX), R10
	MOVQ R10, ARGS_PTR(AX)
	MOVQ STLL(SP), CX
	MOVQ CX, ARGS_STLL(AX)
	MOVQ STML(SP), CX
	MOVQ CX, ARGS_STML(AX)
	MOVQ STOF(SP), CX
	MOVQ CX, ARGS_STOF(AX)
	MOVQ BX, ARGS_REP+0(AX)
	MOVQ REP1(SP), CX
	MOVQ CX, ARGS_REP+8(AX)
	MOVQ REP2(SP), CX
	MOVQ CX, ARGS_REP+16(AX)
	SUBQ ARGS_OUT(AX), R11
	MOVQ R11, ARGS_OP(AX)
	SUBQ ARGS_LITS(AX), R12
	MOVQ R12, ARGS_LITPTR(AX)
	MOVQ NBSEQ(SP), CX
	MOVQ CX, ARGS_NBSEQ(AX)
	RET
