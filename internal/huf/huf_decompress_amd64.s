//go:build amd64 && !appengine && !noasm && !purego && gc

// Port of libzstd v1.5.7 lib/decompress/huf_decompress_amd64.S (Copyright
// (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the
// fast 4X1 and 4X2 Huffman decoding loops. The loop bodies are C's
// instruction for instruction; the differences are the entry and exit
// (Go's ABI0 frame instead of pushing every register, fastArgs offsets
// turned into pointers and back) and the stack slots, which are the frame's
// at C's offsets.

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

// Registers (C's mapping): op[0..3] SI BX CX DI, ip[0..3] R8 R9 R10 R11,
// bits[0..3] BP DX R12 R13, dtable R14, olimit R15. AX is scratch. BP is
// free to use: the frame is non-empty, so the assembler saves BP in the
// prologue and restores it before RET.

// LOAD_ARGS(dtOff) loads fastArgs (in AX) into the registers above, adding
// ilowest to ip[] and dst to op[]; it leaves dst in R15 and ilowest in AX.
#define LOAD_ARGS(dtOff) \
	MOVQ ARGS_DST(AX), R15; \
	MOVQ ARGS_OP+0(AX), SI; \
	MOVQ ARGS_OP+8(AX), BX; \
	MOVQ ARGS_OP+16(AX), CX; \
	MOVQ ARGS_OP+24(AX), DI; \
	ADDQ R15, SI; \
	ADDQ R15, BX; \
	ADDQ R15, CX; \
	ADDQ R15, DI; \
	MOVQ ARGS_BITS+0(AX), BP; \
	MOVQ ARGS_BITS+8(AX), DX; \
	MOVQ ARGS_BITS+16(AX), R12; \
	MOVQ ARGS_BITS+24(AX), R13; \
	MOVQ ARGS_DT(AX), R14; \
	LEAQ dtOff(R14), R14; \
	MOVQ ARGS_IP+0(AX), R8; \
	MOVQ ARGS_IP+8(AX), R9; \
	MOVQ ARGS_IP+16(AX), R10; \
	MOVQ ARGS_IP+24(AX), R11; \
	MOVQ ARGS_ILOWEST(AX), AX; \
	ADDQ AX, R8; \
	ADDQ AX, R9; \
	ADDQ AX, R10; \
	ADDQ AX, R11

// STORE_ARGS saves ip[], op[] and bits[] back to fastArgs as offsets.
// Clobbers AX, R14, R15.
#define STORE_ARGS \
	MOVQ args+0(FP), AX; \
	MOVQ ARGS_ILOWEST(AX), R14; \
	MOVQ ARGS_DST(AX), R15; \
	SUBQ R14, R8; \
	SUBQ R14, R9; \
	SUBQ R14, R10; \
	SUBQ R14, R11; \
	SUBQ R15, SI; \
	SUBQ R15, BX; \
	SUBQ R15, CX; \
	SUBQ R15, DI; \
	MOVQ R8, ARGS_IP+0(AX); \
	MOVQ R9, ARGS_IP+8(AX); \
	MOVQ R10, ARGS_IP+16(AX); \
	MOVQ R11, ARGS_IP+24(AX); \
	MOVQ SI, ARGS_OP+0(AX); \
	MOVQ BX, ARGS_OP+8(AX); \
	MOVQ CX, ARGS_OP+16(AX); \
	MOVQ DI, ARGS_OP+24(AX); \
	MOVQ BP, ARGS_BITS+0(AX); \
	MOVQ DX, ARGS_BITS+8(AX); \
	MOVQ R12, ARGS_BITS+16(AX); \
	MOVQ R13, ARGS_BITS+24(AX)

// X1 frame (C's stack after its pushes and subq $24): 0(SP), 8(SP),
// 16(SP) ip[1..3] (and BX, DX while computing olimit), 24(SP) olimit,
// 32(SP) oend, 40(SP) ilowest.

// GET_NEXT_DELT(var, bits): var = dt[bits >> 53] (C's var[n] aliases
// ip[1..3] and olimit: R15 R9 R10 R11).
#define GET_NEXT_DELT(var, bits) \
	MOVQ    $53, var; \
	SHRXQ   var, bits, var; \
	MOVWLZX (R14)(var*2), var

// DECODE_FROM_DELT(var, bits, op, idx): bits <<= var & 63;
// op[idx] = byte(var >> 8).
#define DECODE_FROM_DELT(var, bits, op, idx) \
	MOVQ  var, AX; \
	SHLXQ var, bits, bits; \
	MOVB  AH, idx(op)

#define DECODE_AND_GET_NEXT(var, bits, op, idx) \
	DECODE_FROM_DELT(var, bits, op, idx); \
	GET_NEXT_DELT(var, bits)

#define X1_DECODE_ALL(idx) \
	DECODE_AND_GET_NEXT(R15, BP, SI, idx); \
	DECODE_AND_GET_NEXT(R9, DX, BX, idx); \
	DECODE_AND_GET_NEXT(R10, R12, CX, idx); \
	DECODE_AND_GET_NEXT(R11, R13, DI, idx)

// X1_RELOAD_BITS(bits, op, ip): ctz = TrailingZeros64(bits); op += 5;
// ip -= ctz >> 3; bits = (load64(ip) | 1) << (ctz & 7).
#define X1_RELOAD_BITS(bits, op, ip) \
	BSFQ  bits, bits; \
	MOVQ  bits, AX; \
	ANDQ  $7, AX; \
	SHRQ  $3, bits; \
	LEAQ  5(op), op; \
	SUBQ  bits, ip; \
	MOVQ  (ip), bits; \
	ORQ   $1, bits; \
	SHLXQ AX, bits, bits

// func decompress4X1FastAsmLoop(args *fastArgs)
//
// C: huf_decompress_amd64.S:HUF_decompress4X1_usingDTable_internal_fast_asm_loop
// (huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast_c_loop).
// Preconditions (decompressFastArgsInit, newFastArgs): dt.desc.tableLog ==
// 11; every iend[i] - ip[i] == 8 and every length_i >= 8; 0 <= op[0] <
// op[1] < op[2] < op[3] < oend == len(dst), op[i+1] - op[i] ==
// (len(dst)+3)/4; ilowest = &src[0], dst = &dst[0].
//
// BOUNDS:
// (1) Reads are the 8 bytes at ip[i]. decompressFastArgsInit starts every
//     ip[i] at iend[i] - 8 <= len(src) - 8 (the length_i >= 8 checks), and
//     ip[i] only decreases (SUBQ of ctz >> 3), so every read ends at or
//     below src + len(src).
// (2) At each olimit computation ip[i] >= ip[0] for i = 1..3 (checked,
//     else exit) and iters <= (ip[0] - ilowest) / 7. An iteration consumes
//     at most 5 * 11 = 55 bits per stream (X1 TABLE INVARIANT: every
//     entry's nbBits, entry & 63, is <= 11, as the builder caps it at
//     tableLog == 11) and the reload keeps fewer than 8 consumed, so ctz
//     <= 62 and ip[i] drops by at most 7 bytes. op[3]
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
// (5) Only AX..R15 are used, BP saved and restored by the frame; no stack
//     writes beyond the declared 48-byte frame (0..47(SP)).
TEXT ·decompress4X1FastAsmLoop(SB), NOSPLIT, $48-8
	MOVQ args+0(FP), AX
	MOVQ ARGS_OEND(AX), R15
	ADDQ ARGS_DST(AX), R15
	MOVQ R15, 32(SP)               // oend
	LOAD_ARGS(DTABLE_X1)
	MOVQ AX, 40(SP)                // ilowest

x1_compute_olimit:
	// Computes how many iterations we can do safely; R15, AX may be
	// clobbered, BX and DX must be saved, op3 and ip0 must not be clobbered.
	MOVQ BX, 0(SP)
	MOVQ DX, 8(SP)

	MOVQ 32(SP), AX                // AX = oend
	SUBQ DI, AX                    // AX = oend - op3

	// R15 = (oend - op3) / 5
	MOVQ $-3689348814741910323, DX
	MULQ DX
	MOVQ DX, R15
	SHRQ $2, R15

	MOVQ R8, AX                    // AX = ip0
	MOVQ 40(SP), DX                // DX = ilowest
	SUBQ DX, AX                    // AX = ip0 - ilowest
	MOVQ AX, BX                    // BX = ip0 - ilowest

	// DX = (ip0 - ilowest) / 7
	MOVQ $2635249153387078803, DX
	MULQ DX
	SUBQ DX, BX
	SHRQ $1, BX
	ADDQ BX, DX
	SHRQ $2, DX

	// R15 = min(DX, R15)
	CMPQ    R15, DX
	CMOVQHI DX, R15

	// R15 = R15 * 5
	LEAQ (R15)(R15*4), R15

	// olimit = op3 + R15
	ADDQ DI, R15

	MOVQ 8(SP), DX
	MOVQ 0(SP), BX

	// If op3 == olimit: exit
	MOVQ DI, AX
	CMPQ R15, AX
	JEQ  x1_exit

	// If ip1 < ip0, ip2 < ip1 or ip3 < ip2: exit
	CMPQ R9, R8
	JCS  x1_exit
	CMPQ R10, R9
	JCS  x1_exit
	CMPQ R11, R10
	JCS  x1_exit

	// Store clobbered variables on the stack.
	MOVQ R15, 24(SP)
	MOVQ R9, 0(SP)
	MOVQ R10, 8(SP)
	MOVQ R11, 16(SP)

	GET_NEXT_DELT(R15, BP)
	GET_NEXT_DELT(R9, DX)
	GET_NEXT_DELT(R10, R12)
	GET_NEXT_DELT(R11, R13)

	PCALIGN $64

x1_loop_body:
	// Decode 5 symbols in each of the 4 streams (20 total).
	X1_DECODE_ALL(0)
	X1_DECODE_ALL(1)
	X1_DECODE_ALL(2)
	X1_DECODE_ALL(3)
	DECODE_FROM_DELT(R15, BP, SI, 4)
	DECODE_FROM_DELT(R9, DX, BX, 4)
	DECODE_FROM_DELT(R10, R12, CX, 4)
	DECODE_FROM_DELT(R11, R13, DI, 4)

	// Load ip[1,2,3] from the stack (var[] aliases them).
	MOVQ 0(SP), R9
	MOVQ 8(SP), R10
	MOVQ 16(SP), R11

	// Reload each stream and fetch the next table entry.
	X1_RELOAD_BITS(BP, SI, R8)
	GET_NEXT_DELT(R15, BP)

	X1_RELOAD_BITS(DX, BX, R9)
	MOVQ R9, 0(SP)
	GET_NEXT_DELT(R9, DX)

	X1_RELOAD_BITS(R12, CX, R10)
	MOVQ R10, 8(SP)
	GET_NEXT_DELT(R10, R12)

	X1_RELOAD_BITS(R13, DI, R11)
	MOVQ R11, 16(SP)
	GET_NEXT_DELT(R11, R13)

	// If op3 < olimit: continue the loop.
	CMPQ 24(SP), DI
	JHI  x1_loop_body

	// Reload ip[1,2,3] from the stack and recompute olimit.
	MOVQ 0(SP), R9
	MOVQ 8(SP), R10
	MOVQ 16(SP), R11
	JMP  x1_compute_olimit

x1_exit:
	STORE_ARGS
	RET

// X2 frame (C's stack after its pushes and subq $8): 0(SP) scratch (DX
// while computing olimit, R8 in the loop), 8(SP)..32(SP) oend[0..3],
// 40(SP) ilowest, 48(SP) olimit.

// X2_DECODE(bits, op): e = dt[bits >> 53]; store16(op, e.sequence);
// bits <<= e.nbBits; op += e.length. Clobbers AX, R8, R15.
#define X2_DECODE(bits, op) \
	MOVQ    bits, AX; \
	SHRQ    $53, AX; \
	MOVWLZX 0(R14)(AX*4), R8; \
	MOVBLZX 2(R14)(AX*4), R15; \
	MOVBLZX 3(R14)(AX*4), AX; \
	MOVW    R8, (op); \
	SHLXQ   R15, bits, bits; \
	ADDQ    AX, op

#define X2_DECODE_ALL \
	X2_DECODE(BP, SI); \
	X2_DECODE(DX, BX); \
	X2_DECODE(R12, CX); \
	X2_DECODE(R13, DI)

// X2_RELOAD_BITS(bits, ip): ctz = TrailingZeros64(bits);
// ip -= ctz >> 3; bits = (load64(ip) | 1) << (ctz & 7).
#define X2_RELOAD_BITS(bits, ip) \
	BSFQ  bits, bits; \
	MOVQ  bits, AX; \
	SHRQ  $3, bits; \
	ANDQ  $7, AX; \
	SUBQ  bits, ip; \
	MOVQ  (ip), bits; \
	ORQ   $1, bits; \
	SHLXQ AX, bits, bits

// func decompress4X2FastAsmLoop(args *fastArgs)
//
// C: huf_decompress_amd64.S:HUF_decompress4X2_usingDTable_internal_fast_asm_loop
// (huf_decompress.c:HUF_decompress4X2_usingDTable_internal_fast_c_loop).
// Preconditions as for decompress4X1FastAsmLoop, with an X2 table.
//
// BOUNDS:
// (1) Reads: as decompress4X1FastAsmLoop (1); ip[i] only decreases from
//     iend[i] - 8 <= len(src) - 8.
// TABLE INVARIANT, which (2) and (3) rely on (X1 needs only its nbBits
//     <= 11 invariant): every entry of dt.x2[:2048] has length 1 or
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
//     Per-stream /10 bounds every stream, not only op[3].
// (4) The table index is bits >> 53 < 2048, and x2 has 1 << TableLogMax
//     = 4096 entries. The fast path only runs when dtLog == 11.
// (5) Only AX..R15 are used, BP saved and restored by the frame; no stack
//     writes beyond the declared 56-byte frame (0..55(SP)).
TEXT ·decompress4X2FastAsmLoop(SB), NOSPLIT, $56-8
	MOVQ args+0(FP), AX
	MOVQ ARGS_OEND(AX), R15
	ADDQ ARGS_DST(AX), R15
	MOVQ R15, 32(SP)               // oend3
	LOAD_ARGS(DTABLE_X2)
	MOVQ AX, 40(SP)                // ilowest
	MOVQ DI, 24(SP)                // oend2
	MOVQ CX, 16(SP)                // oend1
	MOVQ BX, 8(SP)                 // oend0

x2_compute_olimit:
	// Computes how many iterations we can do safely; R15, AX may be
	// clobbered, DX must be saved, op[] and ip0 must not be clobbered.
	MOVQ DX, 0(SP)

	// We can consume up to 7 input bytes each iteration.
	MOVQ R8, AX                    // AX = ip0
	MOVQ 40(SP), DX                // DX = ilowest
	SUBQ DX, AX                    // AX = ip0 - ilowest
	MOVQ AX, R15                   // R15 = ip0 - ilowest

	// DX = AX / 7
	MOVQ $2635249153387078803, DX
	MULQ DX
	SUBQ DX, R15
	SHRQ $1, R15
	ADDQ R15, DX
	SHRQ $2, DX

	// R15 = (ip0 - ilowest) / 7
	MOVQ DX, R15

	// R15 = min(R15, min(oend0 - op0, oend1 - op1, oend2 - op2, oend3 - op3) / 10)
	MOVQ 8(SP), AX                 // AX = oend0
	SUBQ SI, AX                    // AX = oend0 - op0
	MOVQ 16(SP), DX                // DX = oend1
	SUBQ BX, DX                    // DX = oend1 - op1

	CMPQ    DX, AX
	CMOVQHI AX, DX                 // DX = min(DX, AX)

	MOVQ 24(SP), AX                // AX = oend2
	SUBQ CX, AX                    // AX = oend2 - op2

	CMPQ    DX, AX
	CMOVQHI AX, DX                 // DX = min(DX, AX)

	MOVQ 32(SP), AX                // AX = oend3
	SUBQ DI, AX                    // AX = oend3 - op3

	CMPQ    DX, AX
	CMOVQHI AX, DX                 // DX = min(DX, AX)

	MOVQ $-3689348814741910323, AX
	MULQ DX
	SHRQ $3, DX                    // DX = DX / 10

	// R15 = min(DX, R15)
	CMPQ    R15, DX
	CMOVQHI DX, R15

	// olimit = op3 + 5 * R15
	MOVQ R15, AX
	LEAQ (DI)(AX*4), R15
	ADDQ AX, R15

	MOVQ 0(SP), DX

	// If op3 == olimit: exit
	MOVQ DI, AX
	CMPQ R15, AX
	JEQ  x2_exit

	// If ip1 < ip0, ip2 < ip1 or ip3 < ip2: exit
	CMPQ R9, R8
	JCS  x2_exit
	CMPQ R10, R9
	JCS  x2_exit
	CMPQ R11, R10
	JCS  x2_exit

	MOVQ R15, 48(SP)

	PCALIGN $64

x2_loop_body:
	// R8 (ip0) is clobbered by X2_DECODE: keep it on the stack.
	MOVQ R8, 0(SP)

	// Decode 5 symbols from each of the 4 streams (20 symbols total).
	X2_DECODE_ALL
	X2_DECODE_ALL
	X2_DECODE_ALL
	X2_DECODE_ALL
	X2_DECODE_ALL

	MOVQ 0(SP), R8

	X2_RELOAD_BITS(BP, R8)
	X2_RELOAD_BITS(DX, R9)
	X2_RELOAD_BITS(R12, R10)
	X2_RELOAD_BITS(R13, R11)

	CMPQ 48(SP), DI
	JHI  x2_loop_body
	JMP  x2_compute_olimit

x2_exit:
	STORE_ARGS
	RET
