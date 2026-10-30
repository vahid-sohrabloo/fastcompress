//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// INC(t, r, lsb): t[byte lsb/8 of r]++. Clobbers R7, R8.
#define INC(t, r, lsb) \
	UBFX  $lsb, r, $8, R7; \
	MOVWU (t)(R7<<2), R8; \
	ADDW  $1, R8; \
	MOVW  R8, (t)(R7<<2)

// COUNT8(r): count the 8 bytes of r, byte i into table i%4.
#define COUNT8(r) \
	INC(R0, r, 0); \
	INC(R10, r, 8); \
	INC(R11, r, 16); \
	INC(R12, r, 24); \
	INC(R0, r, 32); \
	INC(R10, r, 40); \
	INC(R11, r, 48); \
	INC(R12, r, 56)

// func countStripesAsm(ws *[1024]uint32, src *byte, n int)
//
// Registers: R0-R12 tables 0-3 (R0, R10, R11, R12), R1 src, R2 end.
TEXT ·countStripesAsm(SB), NOSPLIT, $0-24
	MOVD ws+0(FP), R0
	MOVD src+8(FP), R1
	MOVD n+16(FP), R2
	ADD  R1, R2, R2           // end
	ADD  $1024, R0, R10
	ADD  $2048, R0, R11
	ADD  $3072, R0, R12

loop16:
	ADD  $16, R1, R4
	CMP  R2, R4
	BHI  tail
	MOVD (R1), R5
	MOVD 8(R1), R6
	COUNT8(R5)
	COUNT8(R6)
	MOVD R4, R1
	B    loop16

tail:
	CMP   R2, R1
	BHS   done
	MOVBU.P 1(R1), R7
	MOVWU (R0)(R7<<2), R8
	ADDW  $1, R8
	MOVW  R8, (R0)(R7<<2)
	B     tail

done:
	RET
