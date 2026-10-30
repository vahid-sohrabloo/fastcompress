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
