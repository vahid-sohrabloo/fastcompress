//go:build arm64 && !appengine && !noasm && !purego && gc

// The fast 4X1 and 4X2 Huffman decoding loops for arm64. C has no aarch64
// assembly for them: these follow libzstd v1.5.7
// lib/decompress/huf_decompress.c (Copyright (c) Meta Platforms, Inc. and
// affiliates; BSD/GPLv2 dual license)
// HUF_decompress4X{1,2}_usingDTable_internal_fast_c_loop as clang -O3
// --target=aarch64-linux-gnu compiles it (symbol-major across the four
// streams, rbit+clz for the trailing-zero count), with the entry, exit and
// argument block of huf_decompress_amd64.s. The 31 registers hold the
// whole state, so there are no stack slots.

#include "textflag.h"

// fastArgs offsets (C: HUF_DecompressFastArgs; TestHUFFastArgsLayout).
#define ARGS_IP      0
#define ARGS_OP      32
#define ARGS_BITS    64
#define ARGS_DT      96
#define ARGS_ILOWEST 104
#define ARGS_OEND    112
#define ARGS_DST     152

// DTable offsets of x1 and x2 (TestHUFFastArgsLayout).
#define DTABLE_X1 4
#define DTABLE_X2 8196

// Registers: op[0..3] R0-R3, ip[0..3] R4-R7, bits[0..3] R8-R11, dtable
// R12, olimit R13. X1: oend R14, ilowest R15, scratch R16, R17, R19-R25.
// X2: oend[0..3] R14-R17, scratch R19-R26. R18 (platform), R27 (REGTMP),
// R28 (g), R29 (FP) and R30 (LR) are not touched.

// LOAD_ARGS(dtOff) loads fastArgs (in R26) into the registers above,
// adding ilowest to ip[] and dst to op[]; it leaves dst in R16 and ilowest
// in R15.
#define LOAD_ARGS(dtOff) \
	MOVD ARGS_DST(R26), R16; \
	LDP  ARGS_OP+0(R26), (R0, R1); \
	LDP  ARGS_OP+16(R26), (R2, R3); \
	ADD  R16, R0, R0; \
	ADD  R16, R1, R1; \
	ADD  R16, R2, R2; \
	ADD  R16, R3, R3; \
	LDP  ARGS_BITS+0(R26), (R8, R9); \
	LDP  ARGS_BITS+16(R26), (R10, R11); \
	MOVD ARGS_DT(R26), R12; \
	ADD  $dtOff, R12, R12; \
	MOVD ARGS_ILOWEST(R26), R15; \
	LDP  ARGS_IP+0(R26), (R4, R5); \
	LDP  ARGS_IP+16(R26), (R6, R7); \
	ADD  R15, R4, R4; \
	ADD  R15, R5, R5; \
	ADD  R15, R6, R6; \
	ADD  R15, R7, R7

// STORE_ARGS saves ip[], op[] and bits[] back to fastArgs as offsets.
// Clobbers R19-R21.
#define STORE_ARGS \
	MOVD args+0(FP), R19; \
	MOVD ARGS_ILOWEST(R19), R20; \
	MOVD ARGS_DST(R19), R21; \
	SUB  R20, R4, R4; \
	SUB  R20, R5, R5; \
	SUB  R20, R6, R6; \
	SUB  R20, R7, R7; \
	SUB  R21, R0, R0; \
	SUB  R21, R1, R1; \
	SUB  R21, R2, R2; \
	SUB  R21, R3, R3; \
	STP  (R4, R5), ARGS_IP+0(R19); \
	STP  (R6, R7), ARGS_IP+16(R19); \
	STP  (R0, R1), ARGS_OP+0(R19); \
	STP  (R2, R3), ARGS_OP+16(R19); \
	STP  (R8, R9), ARGS_BITS+0(R19); \
	STP  (R10, R11), ARGS_BITS+16(R19)

// RELOAD_BITS(bits, ip, t, u): ctz = TrailingZeros64(bits);
// ip -= ctz >> 3; bits = (load64(ip) | 1) << (ctz & 7). Clobbers t, u.
#define RELOAD_BITS(bits, ip, t, u) \
	RBIT bits, t; \
	CLZ  t, t; \
	AND  $7, t, u; \
	SUB  t>>3, ip, ip; \
	MOVD (ip), bits; \
	ORR  $1, bits, bits; \
	LSL  u, bits, bits

// X1_DECODE(idx): for each stream s, e = dt[bits_s >> 53];
// bits_s <<= e & 63; op_s[idx] = byte(e >> 8). The four streams are
// interleaved step by step (temporaries R16, R17, R19, R20).
#define X1_DECODE(idx) \
	LSR   $53, R8, R16; \
	LSR   $53, R9, R17; \
	LSR   $53, R10, R19; \
	LSR   $53, R11, R20; \
	MOVHU (R12)(R16<<1), R16; \
	MOVHU (R12)(R17<<1), R17; \
	MOVHU (R12)(R19<<1), R19; \
	MOVHU (R12)(R20<<1), R20; \
	LSL   R16, R8, R8; \
	LSL   R17, R9, R9; \
	LSL   R19, R10, R10; \
	LSL   R20, R11, R11; \
	LSR   $8, R16, R16; \
	LSR   $8, R17, R17; \
	LSR   $8, R19, R19; \
	LSR   $8, R20, R20; \
	MOVB  R16, idx(R0); \
	MOVB  R17, idx(R1); \
	MOVB  R19, idx(R2); \
	MOVB  R20, idx(R3)

// func decompress4X1FastAsmLoop(args *fastArgs)
//
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast_c_loop
// (Go: decompress4X1FastCLoop). Preconditions as for the amd64
// decompress4X1FastAsmLoop (decompressFastArgsInit, newFastArgs):
// dt.desc.tableLog == 11; every iend[i] - ip[i] == 8 and every length_i >=
// 8; 0 <= op[0] < op[1] < op[2] < op[3] < oend == len(dst), op[i+1] - op[i]
// == (len(dst)+3)/4; ilowest = &src[0], dst = &dst[0].
//
// BOUNDS:
// (1) Reads are the 8 bytes at ip[i]. decompressFastArgsInit starts every
//     ip[i] at iend[i] - 8 <= len(src) - 8 (the length_i >= 8 checks), and
//     ip[i] only decreases (SUB of ctz >> 3), so every read ends at or
//     below src + len(src).
// (2) At each olimit computation ip[i] >= ip[0] for i = 1..3 (checked,
//     else exit) and iters <= (ip[0] - ilowest) / 7. An iteration consumes
//     at most 5 * 11 = 55 bits per stream and the reload keeps fewer than 8
//     consumed, so ctz <= 62 and ip[i] drops by at most 7 bytes. op[3]
//     advances exactly 5 per iteration, so exactly iters iterations run
//     before op[3] == olimit, and ip[i] >= ip[0] - 7 * iters >= ilowest =
//     &src[0] throughout.
// (3) Writes are 5 bytes per iteration at op[i]..op[i]+4. op[3] +
//     5 * iters <= oend = &dst[len(dst)]. Every op[i] advances by exactly
//     5 per iteration and op[i] < op[3] at entry, so every write is below
//     oend. Writing past a stream's segment would still be inside dst, and
//     initRemainingDStream rejects it afterwards (op[i] > segmentEnd ->
//     corruption).
// (4) The table index is bits >> 53 < 2048, and x1 has 1 << TableLogMax
//     = 4096 entries. The fast path only runs when dtLog == 11.
// (5) Only R0-R17 and R19-R26 are written; no frame and no stack writes
//     (NOFRAME, LR untouched).
TEXT ·decompress4X1FastAsmLoop(SB), NOSPLIT|NOFRAME, $0-8
	MOVD args+0(FP), R26
	LOAD_ARGS(DTABLE_X1)
	MOVD ARGS_OEND(R26), R14
	ADD  R16, R14, R14         // oend

x1_compute_olimit:
	// iters = min((oend - op3) / 5, (ip0 - ilowest) / 7)
	SUB  R3, R14, R16
	MOVD $5, R17
	UDIV R17, R16, R16
	SUB  R15, R4, R19
	MOVD $7, R17
	UDIV R17, R19, R19
	CMP  R19, R16
	CSEL HI, R19, R16, R16

	// olimit = op3 + 5 * iters
	ADD R16<<2, R16, R16
	ADD R16, R3, R13

	// If op3 == olimit: exit
	CMP R13, R3
	BEQ x1_exit

	// If ip1 < ip0, ip2 < ip1 or ip3 < ip2: exit
	CMP R4, R5
	BLO x1_exit
	CMP R5, R6
	BLO x1_exit
	CMP R6, R7
	BLO x1_exit

x1_loop_body:
	// Decode 5 symbols in each of the 4 streams (20 total).
	X1_DECODE(0)
	X1_DECODE(1)
	X1_DECODE(2)
	X1_DECODE(3)
	X1_DECODE(4)

	// Reload each stream (C: HUF_4X1_RELOAD_STREAM).
	ADD $5, R0, R0
	ADD $5, R1, R1
	ADD $5, R2, R2
	ADD $5, R3, R3
	RELOAD_BITS(R8, R4, R16, R17)
	RELOAD_BITS(R9, R5, R19, R20)
	RELOAD_BITS(R10, R6, R21, R22)
	RELOAD_BITS(R11, R7, R23, R24)

	// If op3 < olimit: continue the loop.
	CMP R13, R3
	BLO x1_loop_body
	B   x1_compute_olimit

x1_exit:
	STORE_ARGS
	RET

// X2_DECODE(bits, op, t, u): e = dt[bits >> 53]; store16(op, e.sequence);
// bits <<= (e >> 16) & 63 (e.nbBits); op += e >> 24 (e.length). Clobbers
// t, u.
#define X2_DECODE(bits, op, t, u) \
	LSR   $53, bits, t; \
	MOVWU (R12)(t<<2), t; \
	MOVH  t, (op); \
	LSR   $16, t, u; \
	LSR   $24, t, t; \
	LSL   u, bits, bits; \
	ADD   t, op, op

#define X2_DECODE_ALL \
	X2_DECODE(R8, R0, R19, R20); \
	X2_DECODE(R9, R1, R21, R22); \
	X2_DECODE(R10, R2, R23, R24); \
	X2_DECODE(R11, R3, R25, R26)

// func decompress4X2FastAsmLoop(args *fastArgs)
//
// C: huf_decompress.c:HUF_decompress4X2_usingDTable_internal_fast_c_loop
// (Go: decompress4X2FastCLoop). Preconditions as for
// decompress4X1FastAsmLoop, with an X2 table.
//
// BOUNDS:
// (1) Reads: as decompress4X1FastAsmLoop (1); ip[i] only decreases from
//     iend[i] - 8 <= len(src) - 8.
// TABLE INVARIANT, which (2) and (3) rely on, unlike X1, whose safety holds
//     for any table contents: every entry of dt.x2[:2048] has length 1 or
//     2 and nbBits <= 11. It holds because buildDEltX2U32 only stores level
//     1 or 2, and ReadStats only accepts a complete tree, so
//     fillDTableX2 writes every one of the 2048 entries and leaves no
//     zero (length 0) entry. TestHUFX2TableInvariant and every table
//     FuzzHUFAsmEquiv and TestHUFAsmEquiv build (readFastTable) assert it.
//     A length-0 entry would stall op[3] short of olimit while streams 0-2
//     keep reading below ilowest and writing past oend; length > 2 breaks
//     (3) directly.
// (2) At each olimit computation ip[i] >= ip[0] for i = 1..3 (checked,
//     else exit) and iters <= (ip[0] - ilowest) / 7. Every entry has
//     nbBits <= 11 (TABLE INVARIANT), so an iteration consumes at most 55
//     bits per stream and ip[i] drops by at most 7 bytes. op[3] advances by
//     at least 5 per iteration (length >= 1, TABLE INVARIANT), so at most
//     iters iterations run before op[3] >= olimit = op[3] + 5 * iters, and
//     ip[i] >= ilowest = &src[0] throughout.
// (3) Writes: each stream's oend[i] is op[i+1] at entry (oend[3] = oend =
//     &dst[len(dst)]), and iters <= min_i (oend[i] - op[i]) / 10. An
//     iteration writes 2 bytes at op[i] five times, advancing op[i] by at
//     most 2 each time (length <= 2, TABLE INVARIANT), so the last byte
//     written is below op[i] + 10 and op[i] stays <= oend[i] <= oend.
//     Per-stream /10 bounds every stream, not only op[3]
//     (min_i(x_i) / 10 == min_i(x_i / 10) for unsigned x_i).
// (4) The table index is bits >> 53 < 2048, and x2 has 1 << TableLogMax
//     = 4096 entries. The fast path only runs when dtLog == 11.
// (5) Only R0-R17 and R19-R26 are written; no frame and no stack writes
//     (NOFRAME, LR untouched).
TEXT ·decompress4X2FastAsmLoop(SB), NOSPLIT|NOFRAME, $0-8
	MOVD args+0(FP), R26
	LOAD_ARGS(DTABLE_X2)
	MOVD ARGS_OEND(R26), R17
	ADD  R16, R17, R17         // oend3 = oend
	MOVD R1, R14               // oend0 = op1
	MOVD R2, R15               // oend1 = op2
	MOVD R3, R16               // oend2 = op3

x2_compute_olimit:
	// iters = (ip0 - ilowest) / 7
	MOVD args+0(FP), R19
	MOVD ARGS_ILOWEST(R19), R19
	SUB  R19, R4, R19
	MOVD $7, R20
	UDIV R20, R19, R19

	// R20 = min(oend0 - op0, oend1 - op1, oend2 - op2, oend3 - op3) / 10
	SUB  R0, R14, R20
	SUB  R1, R15, R21
	CMP  R21, R20
	CSEL HI, R21, R20, R20
	SUB  R2, R16, R21
	CMP  R21, R20
	CSEL HI, R21, R20, R20
	SUB  R3, R17, R21
	CMP  R21, R20
	CSEL HI, R21, R20, R20
	MOVD $10, R21
	UDIV R21, R20, R20

	// iters = min(iters, R20); olimit = op3 + 5 * iters
	CMP  R20, R19
	CSEL HI, R20, R19, R19
	ADD  R19<<2, R19, R19
	ADD  R19, R3, R13

	// If op3 == olimit: exit
	CMP R13, R3
	BEQ x2_exit

	// If ip1 < ip0, ip2 < ip1 or ip3 < ip2: exit
	CMP R4, R5
	BLO x2_exit
	CMP R5, R6
	BLO x2_exit
	CMP R6, R7
	BLO x2_exit

x2_loop_body:
	// Decode 5 entries from each of the 4 streams.
	X2_DECODE_ALL
	X2_DECODE_ALL
	X2_DECODE_ALL
	X2_DECODE_ALL
	X2_DECODE_ALL

	// Reload each stream (C: HUF_4X2_RELOAD_STREAM).
	RELOAD_BITS(R8, R4, R19, R20)
	RELOAD_BITS(R9, R5, R21, R22)
	RELOAD_BITS(R10, R6, R23, R24)
	RELOAD_BITS(R11, R7, R25, R26)

	// If op3 < olimit: continue the loop.
	CMP R13, R3
	BLO x2_loop_body
	B   x2_compute_olimit

x2_exit:
	STORE_ARGS
	RET
