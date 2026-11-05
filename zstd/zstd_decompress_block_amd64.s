//go:build amd64 && !appengine && !noasm && !purego && gc

// The sequence decode-and-execute fast loop of libzstd v1.5.7
// lib/decompress/zstd_decompress_block.c (Copyright (c) Meta Platforms,
// Inc. and affiliates; BSD/GPLv2 dual license):
// ZSTD_decompressSequences_body's loop, with ZSTD_decodeSequence (64-bit,
// not the last sequence) and ZSTD_execSequence's fast path inlined, and
// (decodeExecSeqsSplitAsm) ZSTD_decompressSequences_bodySplitLitBuffer's
// first loop, with ZSTD_execSequenceSplitLitBuffer's fast path. C has
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
// decompressSequencesBody, seqFast.runGo). The body is in
// zstd_decompress_block_seqloop_amd64.h. Runs while nbSeq > 1 and
// ptr >= 16, decoding each sequence (decodeSequence,
// isLastSeq false) and executing it (execSequence's fast path). It stops
// with nothing of the sequence consumed when nbSeq <= 1, ptr < 16, or the
// offset cell has nbAdditionalBits 1 and baseValue != 1 (see TABLE
// INVARIANT); and, with
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
#include "zstd_decompress_block_seqloop_amd64.h"


// func decodeExecSeqsSplitAsm(a *seqAsmArgs)
//
// C: zstd_decompress_block.c:ZSTD_decompressSequences_bodySplitLitBuffer's
// first loop (Go: decompressSequencesBodySplitLitBuffer, seqFast.runGo
// with split), whose literals are in out ahead of op (lits = out) up to
// litEnd = dctx->litBufferEnd, executed with
// ZSTD_execSequenceSplitLitBuffer. It is decodeExecSeqsAsm (the same body,
// with SEQ_SPLIT) with these differences: iLitEnd > litEnd is C's break to
// the litExtraBuffer switch (Go checks it on the pending sequence, as C
// does before executing), and the loop also hands back at
// ZSTD_execSequenceSplitLitBuffer's oMatchEnd > oend_w, with the caller's
// oend_w = litPtr + litLength - WILDCOPY_OVERLENGTH = iLitEnd - 32. It
// keeps the oend - 32 check too, which then never fires: iLitEnd <=
// litEnd <= oend - 32 in every split layout. Preconditions: as
// decodeExecSeqsAsm's (sequencesFastSplit checks them with asmOK).
//
// BOUNDS: (1) to (7) of decodeExecSeqsAsm, which hold with or without the
// extra hand-back, and with lits = out or not. In addition:
// (5s) Split (lits = out): the literals not yet read are
//     out[litPtr, litEnd), ahead of op. A sequence runs only with
//     oMatchEnd <= iLitEnd - 32 (C's oend_w; else handed back), so every
//     write of it, below oMatchEnd + 32 by (5), is below iLitEnd: the
//     next sequences' literals are never overwritten (C's
//     WILDCOPY_OVERLENGTH reasoning for oend_w). Its own literals are
//     read at litPtr >= op + matchLength + 32 >= op + 32 and copied
//     forward in 16-byte loads each done before the store at the same
//     step, so each store lands below every byte still to be read: the
//     copy equals C's (non-overlapping) ZSTD_copy16/ZSTD_wildcopy.
//     Memory safety does not depend on this: (4) and (5) bound every
//     access with or without split, and with lits != out.
TEXT ·decodeExecSeqsSplitAsm(SB), NOSPLIT, $112-8
#define SEQ_SPLIT
#include "zstd_decompress_block_seqloop_amd64.h"
#undef SEQ_SPLIT

// func buildFSETableAsm(dt *seqSymbol, norm *int16, maxSV1 uint, baseValue *uint32, nbAdd *uint8, tableLog uint, wksp *buildFSETableWksp) bool
//
// C: zstd_decompress_block.c:ZSTD_buildFSETable_body (Go: buildFSETable),
// with C's three stages: the init loop (symbolNext, the low-probability
// cells from the top down, the header), the spread (C's 8-byte lay-down
// then the 2-way unrolled scatter when no symbol has count -1, the
// step walk skipping the low-probability area otherwise), and the cells
// loop (nbBits with BSR, nextState with SHLX, then the four fields stored
// one by one as gcc -O3 does). It returns false, having written at most
// symbolNext and the baseValue of some low-probability cells (which the Go
// body then rebuilds), when norm is not a valid distribution: a count
// below -1, or counts (-1 counted as 1) not summing to 1 << tableLog. C
// presumes a valid one (fse.ReadNCount's), and so does the Go body; only
// valid ones reach either from the decoder. Preconditions (buildFSETableFast checks
// them): 5 <= tableLog <= maxFSELog (C's FSE_MIN_TABLELOG; BOUNDS (6)
// needs an odd step, which tableLog >= 4 gives: at 3 it is 8), 1 <= maxSV1
// <= maxSeq + 1, and norm, baseValue and nbAdd have maxSV1 entries, dt
// 1 + (1 << tableLog) cells.
//
// Registers: dt DI, norm SI, maxSV1 R8, baseValue R9, nbAdd R10,
// symbolNext R11 (wksp; spread is at wksp + 106, C's symbolNext + MaxSeq +
// 1), tableSize R12, highThreshold R13 (signed), tableLog R15. R9 and R10
// are reused by the spread and reloaded for the cells loop.
//
// BOUNDS:
// (1) Init: s < maxSV1 <= len(norm), <= maxSeq + 1 = len(symbolNext). A
//     count -1 writes tableDecode[highThreshold] only after checking
//     highThreshold >= 0 (else false); highThreshold starts at
//     tableSize - 1 and only decreases, so the cell is in dt.
// (2) Validation: the init loop returns false at a count below -1, and
//     after it, before the header or anything else is written, so does a
//     sum (-1 as 1) other than tableSize. Then highThreshold + 1 = the sum
//     of the positive counts (it is -1 when every count is -1).
// (3) Lay-down (no count -1): the 8-byte stores for symbol s are at pos + i,
//     i < max(n, 1) in steps of 8, where pos is the sum of the earlier
//     counts; so they end at most at tableSize + 8 <= (1 << maxFSELog) +
//     8 = len(spread) (C: the +8 of ZSTD_BUILD_FSE_TABLE_WKSP_SIZE). spread[0, tableSize) then
//     holds each symbol s < maxSV1 count[s] times.
// (4) Scatter: reads spread[0, tableSize), writes tableDecode[position &
//     (tableSize - 1)].
// (5) Step walk: writes tableDecode[position], position masked to the
//     table. The walk runs only for a positive count, so then
//     highThreshold >= 0 (2) and its skip loop ends: position 0 <=
//     highThreshold is reached.
// (6) Cells: u < tableSize. Every cell holds a symbol < maxSV1 written by
//     this call: the step is odd (tableLog >= 5), so the scatter is a
//     permutation of the table, and the walk visits each of
//     [0, highThreshold] once in its first highThreshold + 1 = sum of
//     positive counts stores (2); the cells above highThreshold are the
//     -1 symbols' (1). nextState = symbolNext[symbol] >= 1 for every
//     symbol in a cell, so BSR's input is never 0. So symbolNext[symbol], baseValue[symbol] and
//     nbAdd[symbol] are in bounds (the lengths are preconditions).
// (7) Frame $0-57: seven 8-byte arguments and the bool result; no calls,
//     no globals, BP untouched.
TEXT ·buildFSETableAsm(SB), NOSPLIT, $0-57
	MOVQ dt+0(FP), DI
	MOVQ norm+8(FP), SI
	MOVQ maxSV1+16(FP), R8
	MOVQ tableLog+40(FP), R15
	MOVQ wksp+48(FP), R11
	MOVQ R15, CX
	MOVL $1, R12
	SHLQ CX, R12
	LEAQ -1(R12), R13

	// Init: BX s, DX the sum, AX the count, R14 the OR of the counts
	// other than -1: below largeLimit (a power of two) exactly when each
	// of them is.
	XORL BX, BX
	XORL DX, DX
	XORL R14, R14

fseInit:
	MOVWQSX (SI)(BX*2), AX
	CMPQ    AX, $-1
	JEQ     fseLowProb
	JLT     fseFail
	ORQ     AX, R14
	ADDQ    AX, DX
	MOVW    AX, (R11)(BX*2)
	INCQ    BX
	CMPQ    BX, R8
	JLT     fseInit
	JMP     fseInitDone

fseLowProb:
	TESTQ R13, R13
	JS    fseFail
	MOVL  BX, 12(DI)(R13*8) // tableDecode[highThreshold].baseValue = s
	DECQ  R13
	MOVW  $1, (R11)(BX*2)
	INCQ  DX
	INCQ  BX
	CMPQ  BX, R8
	JLT   fseInit

fseInitDone:
	CMPQ DX, R12
	JNE  fseFail

	// Header (C: ZSTD_seqSymbol_header): fastMode in nextState, 1 unless
	// a count is >= largeLimit = tableSize / 2; tableLog in baseValue.
	XORL  AX, AX
	MOVQ  R12, CX
	SHRQ  $1, CX
	CMPQ  R14, CX
	SETLT AX
	MOVQ  R15, CX
	SHLQ  $32, CX
	ORQ   CX, AX
	MOVQ  AX, (DI)

	// AX = step (C: FSE_TABLESTEP), R14 = spread.
	MOVQ R12, AX
	SHRQ $1, AX
	MOVQ R12, CX
	SHRQ $3, CX
	LEAQ 3(AX)(CX*1), AX
	LEAQ 106(R11), R14
	LEAQ -1(R12), CX
	CMPQ R13, CX
	JNE  fseSlow

	// Lay down the symbols in order, 8 bytes at a time: R9 = spread + pos,
	// DX = s * 0x0101010101010101, CX = 0x0101010101010101, R10 = n;
	// SI is the inner loop's i (C: i = 8; i < n; i += 8), then norm again.
	MOVQ R14, R9
	XORL DX, DX
	XORL BX, BX
	MOVQ $0x0101010101010101, CX

fseLay:
	MOVWQSX (SI)(BX*2), R10
	MOVQ    DX, (R9)
	CMPQ    R10, $8
	JLE     fseLayNext
	MOVL    $8, SI

fseLayInner:
	MOVQ DX, (R9)(SI*1)
	ADDQ $8, SI
	CMPQ SI, R10
	JLT  fseLayInner
	MOVQ norm+8(FP), SI

fseLayNext:
	ADDQ R10, R9
	ADDQ CX, DX
	INCQ BX
	CMPQ BX, R8
	JLT  fseLay

	// Spread the positions across the table, unrolled twice (C's
	// unroll = 2): R9 = position, BX = s, R13 = tableMask.
	XORL R9, R9
	XORL BX, BX

fseScatter:
	MOVBLZX (R14)(BX*1), CX
	MOVQ    R9, DX
	ANDQ    R13, DX
	MOVL    CX, 12(DI)(DX*8)
	ADDQ    AX, R9
	MOVBLZX 1(R14)(BX*1), CX
	MOVQ    R9, DX
	ANDQ    R13, DX
	MOVL    CX, 12(DI)(DX*8)
	ADDQ    AX, R9
	ANDQ    R13, R9
	ADDQ    $2, BX
	CMPQ    BX, R12
	JLT     fseScatter
	JMP     fseCells

	// Step walk: R9 = position, BX = s, R10 = the count left, R14 =
	// tableMask, R13 = highThreshold.
fseSlow:
	LEAQ -1(R12), R14
	XORL R9, R9
	XORL BX, BX

fseSlowSym:
	MOVWQSX (SI)(BX*2), R10
	TESTQ   R10, R10
	JLE     fseSlowNext

fseSlowCell:
	MOVL BX, 12(DI)(R9*8)

fseSlowStep:
	ADDQ AX, R9
	ANDQ R14, R9
	CMPQ R9, R13
	JHI  fseSlowStep
	DECQ R10
	JNZ  fseSlowCell

fseSlowNext:
	INCQ BX
	CMPQ BX, R8
	JLT  fseSlowSym

	// Cells: R14 = &tableDecode[u], R13 = its end, AX = symbol, DX =
	// nextState then the cell, CX = nbBits.
fseCells:
	MOVQ baseValue+24(FP), R9
	MOVQ nbAdd+32(FP), R10
	LEAQ 8(DI), R14
	LEAQ 8(DI)(R12*8), R13

fseCell:
	MOVL    4(R14), AX
	MOVWLZX (R11)(AX*2), DX
	LEAL    1(DX), BX
	MOVW    BX, (R11)(AX*2)
	BSRL    DX, BX
	MOVL    R15, CX
	SUBL    BX, CX
	SHLXL   CX, DX, DX
	SUBL    R12, DX
	MOVB    CX, 3(R14)
	MOVW    DX, (R14)
	MOVBLZX (R10)(AX*1), BX
	MOVB    BX, 2(R14)
	MOVL    (R9)(AX*4), BX
	MOVL    BX, 4(R14)
	ADDQ    $8, R14
	CMPQ    R14, R13
	JNE     fseCell

	MOVB $1, ret+56(FP)
	RET

fseFail:
	MOVB $0, ret+56(FP)
	RET
