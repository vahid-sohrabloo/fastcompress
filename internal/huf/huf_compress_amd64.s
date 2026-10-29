//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// ADD(i): add the code of src[i] (C: HUF_addBits): container =
// container >> nbBits | code; pos += nbBits. Clobbers AX, BX, CX.
#define ADD(i) \
	MOVBLZX i(SI)(DX*1), BX; \
	MOVBLZX (DI)(BX*8), CX; \
	MOVQ    (DI)(BX*8), AX; \
	ANDQ    $-256, AX; \
	SHRQ    CX, R8; \
	ORQ     AX, R8; \
	ADDQ    CX, R9

// FLUSH: C's HUF_flushBits with the clamp. Clobbers AX, CX.
#define FLUSH \
	MOVQ    $64, CX; \
	SUBQ    R9, CX; \
	MOVQ    R8, AX; \
	SHRQ    CX, AX; \
	MOVQ    AX, (R10); \
	MOVQ    R9, CX; \
	SHRQ    $3, CX; \
	ADDQ    CX, R10; \
	CMPQ    R10, R11; \
	CMOVQHI R11, R10; \
	ANDQ    $7, R9

// func compress1XAsm(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
//
// Registers: R8 container, R9 bit count, R10 write pointer, R11 its clamp
// (dst + dstLen - 8), SI src, DX symbols left, DI ct.
TEXT ·compress1XAsm(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), R10
	MOVQ dstLen+8(FP), R11
	LEAQ -8(R10)(R11*1), R11
	MOVQ src+16(FP), SI
	MOVQ srcLen+24(FP), DX
	MOVQ ct+32(FP), DI
	XORL R8, R8
	XORL R9, R9

	// first srcLen % 4 symbols, from the end
rem:
	TESTQ $3, DX
	JEQ   loop4
	DECQ  DX
	ADD(0)
	JMP   rem

loop4:
	FLUSH
	CMPQ DX, $4
	JLT  close
	SUBQ $4, DX
	ADD(3)
	ADD(2)
	ADD(1)
	ADD(0)
	JMP  loop4

close:
	// endMark: 1 bit of value 1 (C: HUF_closeCStream)
	SHRQ $1, R8
	MOVQ $0x8000000000000000, AX
	ORQ  AX, R8
	INCQ R9
	FLUSH
	CMPQ R10, R11
	JAE  overflow
	MOVQ R9, CX
	SUBQ dst+0(FP), R10
	TESTQ CX, CX
	JEQ  nopad
	INCQ R10

nopad:
	MOVQ R10, ret+40(FP)
	RET

overflow:
	MOVQ $0, ret+40(FP)
	RET

// GROUP(i): R12 = the 4 codes of src[i+3], src[i+2], src[i+1], src[i]
// (in that order) as one top-aligned field, R13 = their total length
// (C: four HUF_addBits, regrouped: (C >> a | A) >> b | B == C >> (a+b) |
// (A >> b | B)). BMI2. Clobbers AX, BX, CX.
#define GROUP(i) \
	MOVBLZX i+3(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), R12; \
	MOVBQZX R12, R13; \
	ANDQ    $-256, R12; \
	MOVBLZX i+2(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	MOVBQZX AX, CX; \
	ANDQ    $-256, AX; \
	SHRXQ   CX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    CX, R13; \
	MOVBLZX i+1(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	MOVBQZX AX, CX; \
	ANDQ    $-256, AX; \
	SHRXQ   CX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    CX, R13; \
	MOVBLZX i(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	MOVBQZX AX, CX; \
	ANDQ    $-256, AX; \
	SHRXQ   CX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    CX, R13

// FLUSHX: FLUSH with BMI2 shifts. Clobbers AX, CX.
#define FLUSHX \
	MOVQ    $64, CX; \
	SUBQ    R9, CX; \
	SHRXQ   CX, R8, AX; \
	MOVQ    AX, (R10); \
	MOVQ    R9, CX; \
	SHRQ    $3, CX; \
	ADDQ    CX, R10; \
	CMPQ    R10, R11; \
	CMOVQHI R11, R10; \
	ANDQ    $7, R9

// func compress1XAsmBMI2(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
//
// compress1XAsm with BMI2 shifts and the 4 codes between flushes combined
// before they are added, which takes the container off the critical path.
TEXT ·compress1XAsmBMI2(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), R10
	MOVQ dstLen+8(FP), R11
	LEAQ -8(R10)(R11*1), R11
	MOVQ src+16(FP), SI
	MOVQ srcLen+24(FP), DX
	MOVQ ct+32(FP), DI
	XORL R8, R8
	XORL R9, R9

xrem:
	TESTQ $3, DX
	JEQ   xloop4
	DECQ  DX
	ADD(0)
	JMP   xrem

xloop4:
	FLUSHX
	CMPQ DX, $4
	JLT  xclose
	SUBQ $4, DX
	GROUP(0)
	SHRXQ R13, R8, R8
	ORQ   R12, R8
	ADDQ  R13, R9
	JMP   xloop4

xclose:
	SHRQ $1, R8
	MOVQ $0x8000000000000000, AX
	ORQ  AX, R8
	INCQ R9
	FLUSHX
	CMPQ R10, R11
	JAE  xoverflow
	MOVQ R9, CX
	SUBQ dst+0(FP), R10
	TESTQ CX, CX
	JEQ  xnopad
	INCQ R10

xnopad:
	MOVQ R10, ret+40(FP)
	RET

xoverflow:
	MOVQ $0, ret+40(FP)
	RET
