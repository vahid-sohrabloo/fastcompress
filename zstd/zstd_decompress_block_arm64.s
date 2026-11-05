//go:build arm64 && !appengine && !noasm && !purego && gc

// The sequence decode-and-execute fast loop of libzstd v1.5.7
// lib/decompress/zstd_decompress_block.c (Copyright (c) Meta Platforms,
// Inc. and affiliates; BSD/GPLv2 dual license) for arm64: the same loop as
// zstd_decompress_block_amd64.s (ZSTD_decompressSequences_body's loop, with
// ZSTD_decodeSequence (64-bit, not the last sequence) and
// ZSTD_execSequence's fast path inlined, and, in decodeExecSeqsSplitAsm,
// ZSTD_decompressSequences_bodySplitLitBuffer's first loop with
// ZSTD_execSequenceSplitLitBuffer's fast path). C has no assembly here;
// this follows the Go port statement for statement (decodeSequence,
// execSequence, wildcopy, overlapCopy8 and the bitstream's ReadBitsFast,
// ReadBits and Reload), and stops, handing the rest back to Go, at C's
// branch points to the slow paths. Baseline arm64 only: LSLV/LSRV shift by
// the register mod 64 as SHLX/SHRX do, and BZHI is a mask built with LSLV
// and kept whole for nbBits >= 64.

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

// Frame slots: the states.
#define STLL 8
#define STML 16
#define STOF 24

// Registers: bitContainer R0, bitsConsumed R1, ptr R2, op R3, litPtr R4,
// the states' cells (C's ZSTD_seqSymbol, loaded whole: nextState bits
// 0-15, nbAdditionalBits 16-23, nbBits 24-31, baseValue 32-63) LL R5, ML
// R6, Of R7, prevOffset[0..2] R8-R10; per sequence litLength R11,
// matchLength R12, offset R13; tables (C: dt + 1) LL R14, ML R15, Of R16,
// nbSeq R17; pointers out + oendW R19, lits + litEnd R20, out +
// prefixStart R21, src + 16 R22; R23-R26 and V0 scratch (R24 holds
// oMatchEnd from the hand-back checks to seqDone). R18 (platform), R27
// (REGTMP), R28 (g), R29 (FP) and R30 (LR) are not touched.

// SEQ_PENDING stores the pending sequence, SEQ_EXIT the loop's state,
// into the arguments at R23 (each entry reloads R23 = a+0(FP) first, in
// the .s, where go vet's asmdecl checks it). SEQ_PENDING clobbers R24,
// SEQ_EXIT R2-R4 and R24.
#define SEQ_PENDING \
	MOVD R11, ARGS_LL(R23); \
	MOVD R12, ARGS_ML(R23); \
	MOVD R13, ARGS_OFF(R23); \
	MOVD $1, R24; \
	MOVD R24, ARGS_PENDING(R23)

#define SEQ_EXIT \
	MOVD R0, ARGS_BC(R23); \
	MOVD R1, ARGS_CONSUMED(R23); \
	MOVD ARGS_SRC(R23), R24; \
	SUB  R24, R2, R2; \
	MOVD R2, ARGS_PTR(R23); \
	MOVD STLL(RSP), R24; \
	MOVD R24, ARGS_STLL(R23); \
	MOVD STML(RSP), R24; \
	MOVD R24, ARGS_STML(R23); \
	MOVD STOF(RSP), R24; \
	MOVD R24, ARGS_STOF(R23); \
	MOVD R8, ARGS_REP+0(R23); \
	MOVD R9, ARGS_REP+8(R23); \
	MOVD R10, ARGS_REP+16(R23); \
	MOVD ARGS_OUT(R23), R24; \
	SUB  R24, R3, R3; \
	MOVD R3, ARGS_OP(R23); \
	MOVD ARGS_LITS(R23), R24; \
	SUB  R24, R4, R4; \
	MOVD R4, ARGS_LITPTR(R23); \
	MOVD R17, ARGS_NBSEQ(R23)

// RELOAD is DStream.Reload for ptr >= 8 (C: BIT_reloadDStream): nothing in
// overflow (bitsConsumed > 64), else ptr -= bitsConsumed >> 3,
// bitsConsumed &= 7 and the container is the 8 bytes at ptr. skip is a
// label after it.
#define RELOAD(skip) \
	CMP  $64, R1; \
	BHI  skip; \
	SUB  R1>>3, R2, R2; \
	AND  $7, R1, R1; \
	MOVD (R2), R0

// READ_BITS_FAST(n, v, t): v = DStream.ReadBitsFast(n) for n >= 1 (C:
// BIT_readBitsFast): (container << (bitsConsumed & 63)) >> (-n & 63);
// bitsConsumed += n. Clobbers t.
#define READ_BITS_FAST(n, v, t) \
	LSL R1, R0, v; \
	NEG n, t; \
	LSR t, v, v; \
	ADD n, R1, R1

// UPDATE_STATE(cell, table, state): state = cell.nextState +
// DStream.ReadBits(cell.nbBits) (C: ZSTD_updateFseStateWithDInfo;
// BIT_readBits: (container >> ((64 - bitsConsumed - nbBits) & 63)) masked
// to nbBits bits, all 64 kept for nbBits >= 64, as amd64's BZHI does),
// saved in the frame slot state; cell = table[state], the next sequence's.
// Clobbers R23-R25.
#define UPDATE_STATE(cell, table, state) \
	UBFX $24, cell, $8, R24; \
	MOVD $64, R23; \
	SUB  R1, R23, R23; \
	SUB  R24, R23, R23; \
	LSR  R23, R0, R23; \
	MOVD $-1, R25; \
	LSL  R24, R25, R25; \
	CMP  $64, R24; \
	CSEL LO, R25, ZR, R25; \
	BIC  R25, R23, R23; \
	ADD  R24, R1, R1; \
	AND  $0xFFFF, cell, R24; \
	ADD  R23, R24, R24; \
	MOVD R24, state(RSP); \
	MOVD (table)(R24<<3), cell

// func decodeExecSeqsAsm(a *seqAsmArgs)
//
// C: zstd_decompress_block.c:ZSTD_decompressSequences_body's loop (Go:
// decompressSequencesBody, seqFast.runGo). The body is in
// zstd_decompress_block_seqloop_arm64.h. Runs while nbSeq > 1 and
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
// (7) Frame $32-8: one pointer argument, no results; the state slots at
//     8, 16 and 24(RSP). The argument is read only here (a+0(FP)), where
//     go vet's asmdecl checks it; the #included body uses registers and
//     frame slots.
//
// TABLE INVARIANT: an offset cell with nbAdditionalBits 1 has baseValue 1
// (OF_base[1]: buildFSETable and buildSeqTableRLE take both from the code,
// OF_bits[1] == 1). The loop checks it before using prevOffset[offset].
TEXT ·decodeExecSeqsAsm(SB), NOSPLIT, $32-8
	MOVD a+0(FP), R23
#include "zstd_decompress_block_seqloop_arm64.h"

pending:
	MOVD a+0(FP), R23
	SEQ_PENDING

exit:
	MOVD a+0(FP), R23
	SEQ_EXIT
	RET


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
TEXT ·decodeExecSeqsSplitAsm(SB), NOSPLIT, $32-8
	MOVD a+0(FP), R23
#define SEQ_SPLIT
#include "zstd_decompress_block_seqloop_arm64.h"
#undef SEQ_SPLIT

pending:
	MOVD a+0(FP), R23
	SEQ_PENDING

exit:
	MOVD a+0(FP), R23
	SEQ_EXIT
	RET

// func buildFSETableAsm(dt *seqSymbol, norm *int16, maxSV1 uint, baseValue *uint32, nbAdd *uint8, tableLog uint, wksp *buildFSETableWksp) bool
//
// C: zstd_decompress_block.c:ZSTD_buildFSETable_body (Go: buildFSETable),
// as buildFSETableAsm of zstd_decompress_block_amd64.s, stage for stage:
// the init loop (symbolNext, the low-probability cells from the top down,
// the header), the spread (C's 8-byte lay-down then the 2-way unrolled
// scatter when no symbol has count -1, the step walk skipping the
// low-probability area otherwise), and the cells loop (nbBits = tableLog -
// highbit(nextState) with CLZW, nextState with LSLW, then the four fields
// stored one by one). It returns false, having written at most symbolNext
// and the baseValue of some low-probability cells (which the Go body then
// rebuilds), when norm is not a valid distribution: a count below -1, or
// counts (-1 counted as 1) not summing to 1 << tableLog. C presumes a
// valid one (fse.ReadNCount's), and so does the Go body; only valid ones
// reach either from the decoder. Preconditions (buildFSETableFast checks
// them): 5 <= tableLog <= maxFSELog (C's FSE_MIN_TABLELOG; BOUNDS (6)
// needs an odd step, which tableLog >= 4 gives: at 3 it is 8), 1 <= maxSV1
// <= maxSeq + 1, and norm, baseValue and nbAdd have maxSV1 entries, dt
// 1 + (1 << tableLog) cells.
//
// Registers: dt R0, norm R1, maxSV1 R2, baseValue R3, nbAdd R4,
// symbolNext R5 (wksp; spread is at wksp + 106, C's symbolNext + MaxSeq +
// 1), tableSize R6, highThreshold R7 (signed), tableLog R8, s R9, the sum
// R10, the count R11, the OR of the counts R12, step R13, spread R14,
// tableMask R15, position R16; R17, R19, R20 scratch.
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
//     symbol in a cell, so CLZW's input is never 0. So symbolNext[symbol], baseValue[symbol] and
//     nbAdd[symbol] are in bounds (the lengths are preconditions).
// (7) Frame $0-57 (NOFRAME): seven 8-byte arguments and the bool result;
//     no calls, no globals, no stack writes; only R0-R17, R19 and R20 are
//     written.
TEXT ·buildFSETableAsm(SB), NOSPLIT|NOFRAME, $0-57
	MOVD dt+0(FP), R0
	MOVD norm+8(FP), R1
	MOVD maxSV1+16(FP), R2
	MOVD baseValue+24(FP), R3
	MOVD nbAdd+32(FP), R4
	MOVD tableLog+40(FP), R8
	MOVD wksp+48(FP), R5
	MOVD $1, R6
	LSL  R8, R6, R6
	SUB  $1, R6, R7

	// Init: R9 s, R10 the sum, R11 the count, R12 the OR of the counts
	// other than -1: below largeLimit (a power of two) exactly when each
	// of them is.
	MOVD ZR, R9
	MOVD ZR, R10
	MOVD ZR, R12

fseInit:
	MOVH (R1)(R9<<1), R11
	CMN  $1, R11
	BEQ  fseLowProb
	BLT  fseFail
	ORR  R11, R12, R12
	ADD  R11, R10, R10
	MOVH R11, (R5)(R9<<1)
	ADD  $1, R9, R9
	CMP  R2, R9
	BLT  fseInit
	B    fseInitDone

fseLowProb:
	TBNZ $63, R7, fseFail
	ADD  R7<<3, R0, R17
	MOVW R9, 12(R17)       // tableDecode[highThreshold].baseValue = s
	SUB  $1, R7, R7
	MOVD $1, R17
	MOVH R17, (R5)(R9<<1)
	ADD  $1, R10, R10
	ADD  $1, R9, R9
	CMP  R2, R9
	BLT  fseInit

fseInitDone:
	CMP R6, R10
	BNE fseFail

	// Header (C: ZSTD_seqSymbol_header): fastMode in nextState, 1 unless
	// a count is >= largeLimit = tableSize / 2; tableLog in baseValue.
	LSR  $1, R6, R17
	CMP  R17, R12
	CSET LT, R17
	ORR  R8<<32, R17, R17
	MOVD R17, (R0)

	// R13 = step (C: FSE_TABLESTEP), R14 = spread, R15 = tableMask.
	LSR $1, R6, R13
	ADD R6>>3, R13, R13
	ADD $3, R13, R13
	ADD $106, R5, R14
	SUB $1, R6, R15
	CMP R15, R7
	BNE fseSlow

	// Lay down the symbols in order, 8 bytes at a time: R16 = spread + pos,
	// R17 = s * 0x0101010101010101, R19 = 0x0101010101010101, R11 = n,
	// R20 the inner loop's i (C: i = 8; i < n; i += 8).
	MOVD R14, R16
	MOVD ZR, R17
	MOVD ZR, R9
	MOVD $0x0101010101010101, R19

fseLay:
	MOVH (R1)(R9<<1), R11
	MOVD R17, (R16)
	CMP  $8, R11
	BLE  fseLayNext
	MOVD $8, R20

fseLayInner:
	MOVD R17, (R16)(R20)
	ADD  $8, R20, R20
	CMP  R11, R20
	BLT  fseLayInner

fseLayNext:
	ADD R11, R16, R16
	ADD R19, R17, R17
	ADD $1, R9, R9
	CMP R2, R9
	BLT fseLay

	// Spread the positions across the table, unrolled twice (C's
	// unroll = 2): R16 = position, R9 = s, R20 = spread + s.
	MOVD ZR, R16
	MOVD ZR, R9

fseScatter:
	ADD   R9, R14, R20
	MOVBU (R20), R11
	AND   R15, R16, R17
	ADD   R17<<3, R0, R17
	MOVW  R11, 12(R17)
	ADD   R13, R16, R16
	MOVBU 1(R20), R11
	AND   R15, R16, R17
	ADD   R17<<3, R0, R17
	MOVW  R11, 12(R17)
	ADD   R13, R16, R16
	AND   R15, R16, R16
	ADD   $2, R9, R9
	CMP   R6, R9
	BLT   fseScatter
	B     fseCells

	// Step walk: R16 = position, R9 = s, R11 = the count left.
fseSlow:
	MOVD ZR, R16
	MOVD ZR, R9

fseSlowSym:
	MOVH (R1)(R9<<1), R11
	CMP  $0, R11
	BLE  fseSlowNext

fseSlowCell:
	ADD  R16<<3, R0, R17
	MOVW R9, 12(R17)

fseSlowStep:
	ADD  R13, R16, R16
	AND  R15, R16, R16
	CMP  R7, R16
	BHI  fseSlowStep
	SUBS $1, R11, R11
	BNE  fseSlowCell

fseSlowNext:
	ADD $1, R9, R9
	CMP R2, R9
	BLT fseSlowSym

	// Cells: R14 = &tableDecode[u], R13 = its end, R11 = symbol, R16 =
	// nextState then the cell's, R17 = nbBits.
fseCells:
	ADD $8, R0, R14
	ADD R6<<3, R14, R13

fseCell:
	MOVWU 4(R14), R11
	MOVHU (R5)(R11<<1), R16
	ADD   $1, R16, R17
	MOVH  R17, (R5)(R11<<1)
	CLZW  R16, R17
	ADD   R8, R17, R17
	SUB   $31, R17, R17     // nbBits = tableLog - (31 - clz32(nextState))
	LSLW  R17, R16, R16
	SUBW  R6, R16, R16
	MOVB  R17, 3(R14)
	MOVH  R16, (R14)
	MOVBU (R4)(R11), R17
	MOVB  R17, 2(R14)
	MOVWU (R3)(R11<<2), R17
	MOVW  R17, 4(R14)
	ADD   $8, R14, R14
	CMP   R13, R14
	BNE   fseCell

	MOVD $1, R17
	MOVB R17, ret+56(FP)
	RET

fseFail:
	MOVB ZR, ret+56(FP)
	RET
