//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// func seqCodesAsm(seqs *seqDef, n int, llCodes, ofCodes, mlCodes *byte)
//
// C: zstd_compress.c:ZSTD_seqToCodes' loop, as gcc compiles it: per
// sequence (offBase u32 at 0, litLength u16 at 4, mlBase u16 at 6) the
// offset code is BSR(offBase), the literal length code llCodeTable[ll] or
// BSR(ll) + 19 above 63, the match length code mlCodeTable[ml] or
// BSR(ml) + 36 above 127.
TEXT ·seqCodesAsm(SB), NOSPLIT, $0-40
	MOVQ seqs+0(FP), SI
	MOVQ n+8(FP), CX
	MOVQ llCodes+16(FP), R8
	MOVQ ofCodes+24(FP), R9
	MOVQ mlCodes+32(FP), R10
	LEAQ ·llCodeTable(SB), R11
	LEAQ ·mlCodeTable(SB), R12
	XORL DX, DX

loop:
	BSRL    (SI)(DX*8), AX
	MOVB    AX, (R9)(DX*1)
	MOVWLZX 4(SI)(DX*8), AX
	CMPL    AX, $63
	JHI     lllong
	MOVBLZX (R11)(AX*1), AX
	MOVB    AX, (R8)(DX*1)
	MOVWLZX 6(SI)(DX*8), AX
	CMPL    AX, $127
	JHI     mllong

mlshort:
	MOVBLZX (R12)(AX*1), AX
	MOVB    AX, (R10)(DX*1)
	INCQ    DX
	CMPQ    DX, CX
	JNE     loop
	RET

lllong:
	BSRL    AX, AX
	ADDL    $19, AX
	MOVB    AX, (R8)(DX*1)
	MOVWLZX 6(SI)(DX*8), AX
	CMPL    AX, $127
	JLS     mlshort

mllong:
	BSRL    AX, AX
	ADDL    $36, AX
	MOVB    AX, (R10)(DX*1)
	INCQ    DX
	CMPQ    DX, CX
	JNE     loop
	RET
