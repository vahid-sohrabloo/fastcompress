//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// ADD(i): add the code of src[i] (C: HUF_addBits with kFast): container =
// container >> elt | elt; pos += elt. A CElt is the code in its top nbBits
// bits, zeros, and nbBits (<= 12) in the low 4 bits, so a shift by the
// whole elt shifts by nbBits (the count's low 6 bits), the low byte of
// pos stays the bit count (its upper bits are noise; FLUSH masks them),
// and the container gets noise only in its low 4 bits, below the at most
// 55 valid top bits (7 + 4 codes of 12). Clobbers BX, CX.
#define ADD(i) \
	MOVBLZX i(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), CX; \
	SHRQ    CX, R8; \
	ORQ     CX, R8; \
	ADDQ    CX, R9

// FLUSH: C's HUF_flushBits with the clamp (nbBits = pos & 0xFF; the shift
// by 64 - pos only reads its low 6 bits). Clobbers AX, CX.
#define FLUSH \
	MOVQ    $64, CX; \
	SUBQ    R9, CX; \
	MOVQ    R8, AX; \
	SHRQ    CX, AX; \
	MOVQ    AX, (R10); \
	MOVBQZX R9, CX; \
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
// (in that order) as one top-aligned field, R13 = their total length in
// its low byte (C: four HUF_addBits, regrouped: (C >> a | A) >> b | B ==
// C >> (a+b) | (A >> b | B)), with whole elts as in ADD. BMI2. Clobbers
// AX, BX.
#define GROUP(i) \
	MOVBLZX i+3(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), R12; \
	MOVQ    R12, R13; \
	MOVBLZX i+2(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    AX, R13; \
	MOVBLZX i+1(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    AX, R13; \
	MOVBLZX i(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    AX, R13

// FLUSHX: FLUSH with BMI2 shifts. Clobbers AX, CX.
#define FLUSHX \
	MOVQ    $64, CX; \
	SUBQ    R9, CX; \
	SHRXQ   CX, R8, AX; \
	MOVQ    AX, (R10); \
	MOVBQZX R9, CX; \
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

// GROUP5(i): GROUP for the 5 codes of src[i+4] .. src[i], tableLog <= 11.
// The last elt's noise bits are cleared: 7 + 5 codes of 11 bits fill 62
// container bits, so noise in bits 2-3 would reach them (C: kLastFast = 0
// for tableLog 11). The earlier elts' noise is shifted at least 11 bits
// lower whenever the total gets that high. Clobbers AX, BX.
#define GROUP5(i) \
	MOVBLZX i+4(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), R12; \
	MOVQ    R12, R13; \
	MOVBLZX i+3(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    AX, R13; \
	MOVBLZX i+2(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    AX, R13; \
	MOVBLZX i+1(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ORQ     AX, R12; \
	ADDQ    AX, R13; \
	MOVBLZX i(SI)(DX*1), BX; \
	MOVQ    (DI)(BX*8), AX; \
	SHRXQ   AX, R12, R12; \
	ADDQ    AX, R13; \
	ANDQ    $-16, AX; \
	ORQ     AX, R12

// func compress1XAsmBMI2x5(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
//
// compress1XAsmBMI2 with 5 codes per flush (C: kUnroll 5), for tables
// with tableLog <= 11.
TEXT ·compress1XAsmBMI2x5(SB), NOSPLIT, $0-48
	MOVQ dst+0(FP), R10
	MOVQ dstLen+8(FP), R11
	LEAQ -8(R10)(R11*1), R11
	MOVQ src+16(FP), SI
	MOVQ srcLen+24(FP), DX
	MOVQ ct+32(FP), DI
	XORL R8, R8
	XORL R9, R9
	// first srcLen % 5 symbols, from the end (at most 4 codes)
	MOVQ DX, AX
	MOVQ $0xCCCCCCCCCCCCCCCD, CX
	MULQ CX                 // DX:AX = srcLen * ceil(2^66 / 5)
	SHRQ $2, DX             // srcLen / 5
	LEAQ (DX)(DX*4), CX
	MOVQ srcLen+24(FP), DX
	SUBQ DX, CX
	NEGQ CX                 // srcLen % 5
	MOVQ CX, R14

x5rem:
	TESTQ R14, R14
	JEQ   x5loop
	DECQ  R14
	DECQ  DX
	ADD(0)
	JMP   x5rem

x5loop:
	FLUSHX
	CMPQ DX, $5
	JLT  x5close
	SUBQ $5, DX
	GROUP5(0)
	SHRXQ R13, R8, R8
	ORQ   R12, R8
	ADDQ  R13, R9
	JMP   x5loop

x5close:
	SHRQ $1, R8
	MOVQ $0x8000000000000000, AX
	ORQ  AX, R8
	INCQ R9
	FLUSHX
	CMPQ R10, R11
	JAE  x5overflow
	MOVQ R9, CX
	SUBQ dst+0(FP), R10
	TESTQ CX, CX
	JEQ  x5nopad
	INCQ R10

x5nopad:
	MOVQ R10, ret+40(FP)
	RET

x5overflow:
	MOVQ $0, ret+40(FP)
	RET
