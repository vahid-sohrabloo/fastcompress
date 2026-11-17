//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// func seqCodesAsm(seqs *seqDef, n int, llCodes, ofCodes, mlCodes *byte)
//
// C: zstd_compress.c:ZSTD_seqToCodes' loop (zstd_compress_seqstore_amd64.s): per
// sequence (offBase u32 at 0, litLength u16 at 4, mlBase u16 at 6) the
// offset code is highbit(offBase) = 31 - CLZ, the literal length code
// llCodeTable[ll] or highbit(ll) + 19 above 63, the match length code
// mlCodeTable[ml] or highbit(ml) + 36 above 127.
TEXT ·seqCodesAsm(SB), NOSPLIT|NOFRAME, $0-40
	MOVD seqs+0(FP), R0
	MOVD n+8(FP), R1
	MOVD llCodes+16(FP), R2
	MOVD ofCodes+24(FP), R3
	MOVD mlCodes+32(FP), R4
	MOVD $·llCodeTable(SB), R5
	MOVD $·mlCodeTable(SB), R6
	MOVD $31, R10
	MOVD $50, R11               // 31 + 19
	MOVD $67, R12               // 31 + 36

loop:
	MOVWU  (R0), R7
	MOVHU  4(R0), R8
	MOVHU  6(R0), R9
	ADD    $8, R0
	CLZW   R7, R7
	SUB    R7, R10, R7
	MOVB.P R7, 1(R3)
	CMP    $63, R8
	BHI    lllong
	MOVBU  (R5)(R8), R8

llstore:
	MOVB.P R8, 1(R2)
	CMP    $127, R9
	BHI    mllong
	MOVBU  (R6)(R9), R9

mlstore:
	MOVB.P R9, 1(R4)
	SUBS   $1, R1, R1
	BNE    loop
	RET

lllong:
	CLZW R8, R8
	SUB  R8, R11, R8
	B    llstore

mllong:
	CLZW R9, R9
	SUB  R9, R12, R9
	B    mlstore
