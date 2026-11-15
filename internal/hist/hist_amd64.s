//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// COUNT8(off, w, wh): count the 8 bytes at off(SI) into tables 0-7, byte i
// into table i (ws holds 8 tables of 256 counts). The two bytes in the low
// 16 bits of w are counted, then w is shifted right by 16. Clobbers w, CX,
// DX.
#define COUNT8(off, w, wh) \
	MOVQ    off(SI), w; \
	MOVBLZX w, CX; \
	MOVBLZX wh, DX; \
	INCL    (DI)(CX*4); \
	INCL    1024(DI)(DX*4); \
	SHRQ    $16, w; \
	MOVBLZX w, CX; \
	MOVBLZX wh, DX; \
	INCL    2048(DI)(CX*4); \
	INCL    3072(DI)(DX*4); \
	SHRQ    $16, w; \
	MOVBLZX w, CX; \
	MOVBLZX wh, DX; \
	INCL    4096(DI)(CX*4); \
	INCL    5120(DI)(DX*4); \
	SHRQ    $16, w; \
	MOVBLZX w, CX; \
	MOVBLZX wh, DX; \
	INCL    6144(DI)(CX*4); \
	INCL    7168(DI)(DX*4)

// func countStripes8Asm(ws *[8 * 256]uint32, src *byte, n int)
TEXT ·countStripes8Asm(SB), NOSPLIT, $0-24
	MOVQ ws+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ n+16(FP), R8
	LEAQ (SI)(R8*1), R8      // end

loop32:
	LEAQ 32(SI), AX
	CMPQ AX, R8
	JHI  loop8
	COUNT8(0, AX, AH)
	COUNT8(8, BX, BH)
	COUNT8(16, AX, AH)
	COUNT8(24, BX, BH)
	ADDQ $32, SI
	JMP  loop32

loop8:
	LEAQ 8(SI), AX
	CMPQ AX, R8
	JHI  tail8
	COUNT8(0, AX, AH)
	ADDQ $8, SI
	JMP  loop8

tail8:
	CMPQ    SI, R8
	JAE     done8
	MOVBLZX (SI), CX
	INCL    (DI)(CX*4)
	INCQ    SI
	JMP     tail8

done8:
	RET

// func countMerge8Asm(ws *[8 * 256]uint32) uint32
//
// countMergeAsm for the 8 tables of countStripes8Asm.
TEXT ·countMerge8Asm(SB), NOSPLIT, $0-12
	MOVQ ws+0(FP), DI
	PXOR X4, X4              // running max per lane
	XORL AX, AX

merge8:
	MOVOU (DI)(AX*1), X0
	MOVOU 1024(DI)(AX*1), X1
	MOVOU 2048(DI)(AX*1), X2
	MOVOU 3072(DI)(AX*1), X3
	PADDL X1, X0
	PADDL X3, X2
	MOVOU 4096(DI)(AX*1), X1
	MOVOU 5120(DI)(AX*1), X3
	PADDL X1, X0
	PADDL X3, X2
	MOVOU 6144(DI)(AX*1), X1
	MOVOU 7168(DI)(AX*1), X3
	PADDL X1, X0
	PADDL X3, X2
	PADDL X2, X0
	MOVOU X0, (DI)(AX*1)
	MOVO  X0, X5
	PCMPGTL X4, X5           // lanes where the sum is larger
	PAND  X5, X0
	PANDN X4, X5
	POR   X0, X5
	MOVO  X5, X4
	ADDQ  $16, AX
	CMPQ  AX, $1024
	JNE   merge8

	// horizontal max of the 4 lanes
	PSHUFD $0x4e, X4, X0
	MOVO   X4, X1
	PCMPGTL X0, X1
	PAND   X1, X4
	PANDN  X0, X1
	POR    X1, X4
	PSHUFD $0xb1, X4, X0
	MOVO   X4, X1
	PCMPGTL X0, X1
	PAND   X1, X4
	PANDN  X0, X1
	POR    X1, X4
	MOVL   X4, AX
	MOVL   AX, ret+8(FP)
	RET
