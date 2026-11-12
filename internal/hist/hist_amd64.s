//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// COUNT4(off, w, wh): count the 4 bytes at off(SI) into tables 0-3, byte i
// into table i. The word is loaded with a 32-bit load, as C's
// HIST_count_parallel_wksp does, so the four words of an iteration are
// independent loads; the second byte is read from the high-byte register
// (wh = AH or BH). Clobbers w, CX, DX.
#define COUNT4(off, w, wh) \
	MOVL    off(SI), w; \
	MOVBLZX w, CX; \
	MOVBLZX wh, DX; \
	INCL    (DI)(CX*4); \
	INCL    1024(DI)(DX*4); \
	MOVL    w, CX; \
	SHRL    $24, w; \
	SHRL    $16, CX; \
	MOVBLZX CL, CX; \
	INCL    2048(DI)(CX*4); \
	INCL    3072(DI)(w*4)

// func countStripesAsm(ws *[1024]uint32, src *byte, n int)
TEXT ·countStripesAsm(SB), NOSPLIT, $0-24
	MOVQ ws+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ n+16(FP), R8
	LEAQ (SI)(R8*1), R8      // end

loop16:
	LEAQ 16(SI), AX
	CMPQ AX, R8
	JHI  tail
	COUNT4(0, AX, AH)
	COUNT4(4, BX, BH)
	COUNT4(8, AX, AH)
	COUNT4(12, BX, BH)
	ADDQ $16, SI
	JMP  loop16

tail:
	CMPQ    SI, R8
	JAE     done
	MOVBLZX (SI), CX
	INCL    (DI)(CX*4)
	INCQ    SI
	JMP     tail

done:
	RET

// func countMergeAsm(ws *[1024]uint32) uint32
//
// Adds tables 1-3 of ws into table 0 and returns the largest sum, 4 lanes
// at a time (SSE2; C's loop as gcc vectorizes it). The counts are below
// 2^31 (countMerge's precondition), so the signed compare orders them.
TEXT ·countMergeAsm(SB), NOSPLIT, $0-12
	MOVQ ws+0(FP), DI
	PXOR X4, X4              // running max per lane
	XORL AX, AX

merge:
	MOVOU (DI)(AX*1), X0
	MOVOU 1024(DI)(AX*1), X1
	MOVOU 2048(DI)(AX*1), X2
	MOVOU 3072(DI)(AX*1), X3
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
	JNE   merge

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
