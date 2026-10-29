//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// COUNT4(r): count the low 4 bytes of r into tables 0-3; clobbers r, CX.
#define COUNT4(r) \
	MOVBLZX r, CX; \
	INCL    (DI)(CX*4); \
	SHRQ    $8, r; \
	MOVBLZX r, CX; \
	INCL    1024(DI)(CX*4); \
	SHRQ    $8, r; \
	MOVBLZX r, CX; \
	INCL    2048(DI)(CX*4); \
	SHRQ    $8, r; \
	MOVBLZX r, CX; \
	INCL    3072(DI)(CX*4); \
	SHRQ    $8, r

// func countStripesAsm(ws *[1024]uint32, src *byte, n int)
TEXT ·countStripesAsm(SB), NOSPLIT, $0-24
	MOVQ ws+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ n+16(FP), DX
	LEAQ (SI)(DX*1), DX      // end

loop16:
	LEAQ 16(SI), AX
	CMPQ AX, DX
	JHI  tail
	MOVQ (SI), AX
	MOVQ 8(SI), BX
	COUNT4(AX)
	COUNT4(AX)
	COUNT4(BX)
	COUNT4(BX)
	ADDQ $16, SI
	JMP  loop16

tail:
	CMPQ    SI, DX
	JAE     done
	MOVBLZX (SI), CX
	INCL    (DI)(CX*4)
	INCQ    SI
	JMP     tail

done:
	RET
