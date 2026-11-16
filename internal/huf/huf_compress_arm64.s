//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// ADDSYM(i): add the code of src[R1+i] (C: HUF_addBits): container =
// container >> nbBits | code; pos += nbBits. Clobbers R3-R5.
#define ADDSYM(i) \
	ADD   R1, R0, R3; \
	MOVBU i(R3), R3; \
	MOVD  (R2)(R3<<3), R4; \
	AND   $0xFF, R4, R5; \
	AND   $-256, R4, R4; \
	LSR   R5, R8, R8; \
	ORR   R4, R8, R8; \
	ADD   R5, R9, R9

// FLUSH: C's HUF_flushBits with the clamp. Clobbers R5, R6.
#define FLUSH \
	MOVD $64, R5; \
	SUB  R9, R5, R5; \
	LSR  R5, R8, R6; \
	MOVD R6, (R10); \
	LSR  $3, R9, R5; \
	ADD  R5, R10, R10; \
	CMP  R11, R10; \
	CSEL HI, R11, R10, R10; \
	AND  $7, R9, R9

// func compress1XAsm(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
//
// As huf_compress_amd64.s. Registers: R8 container, R9 bit count, R10 write
// pointer, R11 its clamp (dst + dstLen - 8), R0 src, R1 symbols left, R2 ct.
TEXT ·compress1XAsm(SB), NOSPLIT|NOFRAME, $0-48
	MOVD dst+0(FP), R10
	MOVD dstLen+8(FP), R11
	ADD  R10, R11, R11
	SUB  $8, R11
	MOVD src+16(FP), R0
	MOVD srcLen+24(FP), R1
	MOVD ct+32(FP), R2
	MOVD ZR, R8
	MOVD ZR, R9

	// first srcLen % 4 symbols, from the end
rem:
	TST  $3, R1
	BEQ  loop4
	SUB  $1, R1
	ADDSYM(0)
	B    rem

loop4:
	FLUSH
	CMP  $4, R1
	BLT  close
	SUB  $4, R1
	ADDSYM(3)
	ADDSYM(2)
	ADDSYM(1)
	ADDSYM(0)
	B    loop4

close:
	// endMark: 1 bit of value 1 (C: HUF_closeCStream)
	LSR  $1, R8, R8
	ORR  $0x8000000000000000, R8, R8
	ADD  $1, R9
	FLUSH
	CMP  R11, R10
	BHS  overflow
	MOVD dst+0(FP), R5
	SUB  R5, R10, R10
	CBZ  R9, nopad
	ADD  $1, R10

nopad:
	MOVD R10, ret+40(FP)
	RET

overflow:
	MOVD ZR, ret+40(FP)
	RET

// The 5-code variants below follow huf_compress_amd64.s's BMI2 ones: whole
// elts (a CElt is the code in its top nbBits bits, zeros, and nbBits <= 12
// in the low 4 bits; LSRV reads only a shift's low 6 bits, so a shift by
// the elt shifts by nbBits, the low byte of the bit count stays exact and
// the container gets noise only in its low 4 bits, below the valid ones),
// and 5 codes combined into one field before they are added (C: kUnroll
// 5), for tableLog <= 11. Registers as in compress1XAsm, with R12 the
// position of the group in src, R13, R14 the group's field and length and
// (compress1XAsmX10) R15 the number of 5-code groups.

// ADDE(i): ADDSYM with the whole elt of src[R12+i]. Clobbers R3, R4.
#define ADDE(i) \
	MOVBU i(R12), R3; \
	MOVD  (R2)(R3<<3), R4; \
	LSR   R4, R8, R8; \
	ORR   R4, R8, R8; \
	ADD   R4, R9, R9

// GROUP5(i): R13 = the 5 codes of src[R12+i+4] .. src[R12+i] (in that
// order) as one top-aligned field, R14 their total length in its low byte
// (C: five HUF_addBits regrouped: (C >> a | A) >> b | B == C >> (a+b) |
// (A >> b | B)). The last elt's noise bits are cleared: 7 + 5 codes of 11
// bits fill 62 container bits, so noise in bits 2-3 would reach them (C:
// kLastFast = 0 for tableLog 11); the earlier elts' noise is shifted at
// least 11 bits lower whenever the total gets that high. Clobbers R3, R4.
#define GROUP5(i) \
	MOVBU i+4(R12), R3; \
	MOVD  (R2)(R3<<3), R13; \
	MOVD  R13, R14; \
	MOVBU i+3(R12), R3; \
	MOVD  (R2)(R3<<3), R4; \
	LSR   R4, R13, R13; \
	ORR   R4, R13, R13; \
	ADD   R4, R14, R14; \
	MOVBU i+2(R12), R3; \
	MOVD  (R2)(R3<<3), R4; \
	LSR   R4, R13, R13; \
	ORR   R4, R13, R13; \
	ADD   R4, R14, R14; \
	MOVBU i+1(R12), R3; \
	MOVD  (R2)(R3<<3), R4; \
	LSR   R4, R13, R13; \
	ORR   R4, R13, R13; \
	ADD   R4, R14, R14; \
	MOVBU i(R12), R3; \
	MOVD  (R2)(R3<<3), R4; \
	LSR   R4, R13, R13; \
	ADD   R4, R14, R14; \
	AND   $-16, R4, R4; \
	ORR   R4, R13, R13

// MERGE: add the group built by GROUP5 to the container.
#define MERGE \
	LSR R14, R8, R8; \
	ORR R13, R8, R8; \
	ADD R14, R9, R9

// FLUSHE: FLUSH for a bit count with noise above its low byte (the shift
// by 64 - pos reads only its low 6 bits). Clobbers R5, R6.
#define FLUSHE \
	NEG  R9, R5; \
	LSR  R5, R8, R6; \
	MOVD R6, (R10); \
	AND  $0xFF, R9, R5; \
	LSR  $3, R5, R5; \
	ADD  R5, R10, R10; \
	CMP  R11, R10; \
	CSEL HI, R11, R10, R10; \
	AND  $7, R9, R9

// FLUSHF: FLUSHE without the clamp (C: HUF_flushBits with kFast), for a
// dst that holds the tight bound (see compress1XAsmX10). Clobbers R5, R6.
#define FLUSHF \
	NEG  R9, R5; \
	LSR  R5, R8, R6; \
	MOVD R6, (R10); \
	AND  $0xFF, R9, R5; \
	LSR  $3, R5, R5; \
	ADD  R5, R10, R10; \
	AND  $7, R9, R9

// CLOSE(overflow, nopad): the end mark, its clamped flush and the result,
// as compress1XAsm's close.
#define CLOSE(overflow, nopad) \
	LSR  $1, R8, R8; \
	ORR  $0x8000000000000000, R8, R8; \
	ADD  $1, R9; \
	FLUSHE; \
	CMP  R11, R10; \
	BHS  overflow; \
	MOVD dst+0(FP), R5; \
	SUB  R5, R10, R10; \
	CBZ  R9, nopad; \
	ADD  $1, R10; \
nopad: \
	MOVD R10, ret+40(FP); \
	RET; \
overflow: \
	MOVD ZR, ret+40(FP); \
	RET

// func compress1XAsmX5(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
//
// compress1XAsm with 5 codes per flush (C: kUnroll 5), for tables with
// tableLog <= 11 (5 codes and the 7 bits a flush leaves fit in 64).
TEXT ·compress1XAsmX5(SB), NOSPLIT|NOFRAME, $0-48
	MOVD dst+0(FP), R10
	MOVD dstLen+8(FP), R11
	ADD  R10, R11, R11
	SUB  $8, R11
	MOVD src+16(FP), R0
	MOVD srcLen+24(FP), R1
	MOVD ct+32(FP), R2
	MOVD ZR, R8
	MOVD ZR, R9
	// first srcLen % 5 symbols, from the end (at most 4 codes)
	MOVD $5, R5
	UDIV R5, R1, R6
	MUL  R5, R6, R7
	SUB  R7, R1, R7

x5rem:
	CBZ  R7, x5loop
	SUB  $1, R7
	SUB  $1, R1
	ADD  R1, R0, R12
	ADDE(0)
	B    x5rem

x5loop:
	FLUSHE
	CMP  $5, R1
	BLT  x5close
	SUB  $5, R1
	ADD  R1, R0, R12
	GROUP5(0)
	MERGE
	B    x5loop

x5close:
	CLOSE(x5overflow, x5nopad)

// func compress1XAsmX10(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
//
// compress1XAsmX5 with 10 codes per loop iteration and no clamp on the
// flushes (C: HUF_compress1X_usingCTable_internal_body_loop with kUnroll 5
// and kFastFlush, which it uses when dstLen >= HUF_tightCompressBound).
// Preconditions: tableLog <= 11 and dstLen >= srcLen*tableLog/8 + 8, so no
// flush before the end mark writes past dst + dstLen - 8 (the end mark's
// flush is clamped and checked as in compress1XAsm).
TEXT ·compress1XAsmX10(SB), NOSPLIT|NOFRAME, $0-48
	MOVD dst+0(FP), R10
	MOVD dstLen+8(FP), R11
	ADD  R10, R11, R11
	SUB  $8, R11
	MOVD src+16(FP), R0
	MOVD srcLen+24(FP), R1
	MOVD ct+32(FP), R2
	MOVD ZR, R8
	MOVD ZR, R9
	// first srcLen % 5 symbols, from the end (at most 4 codes)
	MOVD $5, R5
	UDIV R5, R1, R15             // the number of 5-code groups
	MUL  R5, R15, R7
	SUB  R7, R1, R7

x10rem:
	CBZ  R7, x10odd
	SUB  $1, R7
	SUB  $1, R1
	ADD  R1, R0, R12
	ADDE(0)
	B    x10rem

x10odd:
	FLUSHF
	ADD  R1, R0, R12
	// an odd number of 5-code groups: one group first
	TBZ  $0, R15, x10check
	SUB  $5, R12
	GROUP5(0)
	MERGE
	FLUSHF

x10check:
	CMP  R0, R12
	BEQ  x10close
	PCALIGN $16

x10loop:
	SUB  $10, R12
	GROUP5(5)
	MERGE
	FLUSHF
	GROUP5(0)
	MERGE
	FLUSHF
	CMP  R0, R12
	BNE  x10loop

x10close:
	CLOSE(x10overflow, x10nopad)
