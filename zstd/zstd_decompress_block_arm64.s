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
