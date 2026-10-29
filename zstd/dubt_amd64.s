//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Shared by both functions: DI bt, SI b, R8 ip, R9 iend, BX b+ip,
// R10 commonLengthSmaller, R11 commonLengthLarger, R12 smallerPtr,
// R13 largerPtr, R14 matchIndex, R15 nbCompares, AX/CX/DX scratch.
// Frame: 0(SP) dummy32, 8(SP) nextPtr.

// MATCHLEN: CX = matchIndex's position, DX = min(cls, cll) + ZSTD_count
// from there (C's matchLength); 8(SP) = &bt[2*(matchIndex&btMask)].
// Jumps to bad if the candidate is not before ip.
#define MATCHLEN(btMask, baseIdx, bad, cnt8, cdiff, ctail, cdone) \
	MOVL  R14, AX; \
	ANDL  btMask, AX; \
	LEAQ  (DI)(AX*8), AX; \
	MOVQ  AX, 8(SP); \
	MOVQ  R10, DX; \
	CMPQ  R11, DX; \
	CMOVQLT R11, DX; \
	MOVL  R14, CX; \
	SUBL  baseIdx, CX; \
	CMPQ  CX, R8; \
	JAE   bad; \
cnt8: \
	LEAQ  8(R8)(DX*1), AX; \
	CMPQ  AX, R9; \
	JA    ctail; \
	LEAQ  (SI)(CX*1), AX; \
	MOVQ  (AX)(DX*1), AX; \
	XORQ  (BX)(DX*1), AX; \
	JNE   cdiff; \
	ADDQ  $8, DX; \
	JMP   cnt8; \
cdiff: \
	BSFQ  AX, AX; \
	SHRQ  $3, AX; \
	ADDQ  AX, DX; \
	JMP   cdone; \
ctail: \
	LEAQ  (R8)(DX*1), AX; \
	CMPQ  AX, R9; \
	JAE   cdone; \
	LEAQ  (SI)(CX*1), AX; \
	MOVBLZX (AX)(DX*1), AX; \
	CMPB  AX, (BX)(DX*1); \
	JNE   cdone; \
	INCQ  DX; \
	JMP   ctail; \
cdone:

// func insertDUBT1Asm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow uint32) bool
//
// C: zstd_lazy.c:ZSTD_insertDUBT1 (noDict).
TEXT ·insertDUBT1Asm(SB), NOSPLIT, $16-57
	MOVQ bt+0(FP), DI
	MOVQ b+8(FP), SI
	MOVQ ip+16(FP), R8
	MOVQ iend+24(FP), R9
	LEAQ (SI)(R8*1), BX
	XORL R10, R10
	XORL R11, R11
	MOVL curr+32(FP), AX
	ANDL btMask+40(FP), AX
	LEAQ (DI)(AX*8), R12        // smallerPtr = &bt[2*(curr&btMask)]
	LEAQ 4(R12), R13            // largerPtr
	MOVL (R12), R14             // matchIndex = *smallerPtr
	MOVL nbCompares+44(FP), R15
	MOVB $1, ret+56(FP)

iloop:
	TESTL R15, R15
	JEQ   iend
	CMPL  R14, windowLow+52(FP)
	JLS   iend
	MATCHLEN(btMask+40(FP), baseIdx+36(FP), ibad, icnt8, icdiff, ictail, icdone)
	LEAQ  (R8)(DX*1), AX
	CMPQ  AX, R9
	JEQ   iend                  // equal: no way to know if inf or sup
	LEAQ  (SI)(CX*1), AX
	MOVBLZX (AX)(DX*1), AX
	CMPB  AX, (BX)(DX*1)
	JAE   ilarger
	// match is smaller than current
	MOVL  R14, (R12)
	MOVQ  DX, R10
	CMPL  R14, btLow+48(FP)
	JLS   ismallstop
	MOVQ  8(SP), AX
	LEAQ  4(AX), R12
	MOVL  4(AX), R14
	DECL  R15
	JMP   iloop

ismallstop:
	LEAQ 0(SP), R12
	JMP  iend

ilarger:
	MOVL R14, (R13)
	MOVQ DX, R11
	CMPL R14, btLow+48(FP)
	JLS  ilargestop
	MOVQ 8(SP), R13
	MOVL (R13), R14
	DECL R15
	JMP  iloop

ilargestop:
	LEAQ 0(SP), R13

iend:
	MOVL $0, (R13)
	MOVL $0, (R12)
	RET

ibad:
	MOVB $0, ret+56(FP)
	RET

// func dubtSearchAsm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, offBase, matchEndIdx uint32)
//
// C: zstd_lazy.c:ZSTD_DUBT_findBestMatch, "find longest match" (noDict).
// Extra frame: 16(SP) bestLength, 24(SP) offBase, 28(SP) matchEndIdx.
TEXT ·dubtSearchAsm(SB), NOSPLIT, $32-80
	MOVQ bt+0(FP), DI
	MOVQ b+8(FP), SI
	MOVQ ip+16(FP), R8
	MOVQ iend+24(FP), R9
	LEAQ (SI)(R8*1), BX
	XORL R10, R10
	XORL R11, R11
	MOVL curr+32(FP), AX
	ANDL btMask+40(FP), AX
	LEAQ (DI)(AX*8), R12
	LEAQ 4(R12), R13
	MOVL matchIndex+56(FP), R14
	MOVL nbCompares+44(FP), R15
	MOVQ $0, 16(SP)
	MOVL $999999999, 24(SP)     // offBaseNotFound
	MOVL curr+32(FP), AX
	ADDL $9, AX
	MOVL AX, 28(SP)             // matchEndIdx = curr + 8 + 1

sloop:
	TESTL R15, R15
	JEQ   send
	CMPL  R14, windowLow+52(FP)
	JLS   send
	MATCHLEN(btMask+40(FP), baseIdx+36(FP), sbad, scnt8, scdiff, sctail, scdone)
	CMPQ  DX, 16(SP)
	JLE   snotbetter
	// if matchLength > matchEndIdx - matchIndex: matchEndIdx = matchIndex + matchLength
	MOVL  28(SP), AX
	SUBL  R14, AX
	CMPQ  DX, AX
	JLS   sendok
	LEAL  (R14)(DX*1), AX
	MOVL  AX, 28(SP)

sendok:
	// if 4*(matchLength-bestLength) > highbit32(curr-matchIndex+1) - highbit32(offBase)
	MOVL  curr+32(FP), AX
	SUBL  R14, AX
	INCL  AX
	BSRL  AX, AX
	BSRL  24(SP), CX
	SUBQ  CX, AX                // (CX/AX are 32-bit results, zero-extended)
	MOVQ  DX, CX
	SUBQ  16(SP), CX
	SHLQ  $2, CX
	CMPQ  CX, AX
	JLE   snobest
	MOVQ  DX, 16(SP)
	MOVL  curr+32(FP), AX
	SUBL  R14, AX
	ADDL  $3, AX
	MOVL  AX, 24(SP)

snobest:
	// re-derive CX (match position), clobbered above
	MOVL  R14, CX
	SUBL  baseIdx+36(FP), CX
	LEAQ  (R8)(DX*1), AX
	CMPQ  AX, R9
	JEQ   send

snotbetter:
	LEAQ  (SI)(CX*1), AX
	MOVBLZX (AX)(DX*1), AX
	CMPB  AX, (BX)(DX*1)
	JAE   slarger
	MOVL  R14, (R12)
	MOVQ  DX, R10
	CMPL  R14, btLow+48(FP)
	JLS   ssmallstop
	MOVQ  8(SP), AX
	LEAQ  4(AX), R12
	MOVL  4(AX), R14
	DECL  R15
	JMP   sloop

ssmallstop:
	LEAQ 0(SP), R12
	JMP  send

slarger:
	MOVL R14, (R13)
	MOVQ DX, R11
	CMPL R14, btLow+48(FP)
	JLS  slargestop
	MOVQ 8(SP), R13
	MOVL (R13), R14
	DECL R15
	JMP  sloop

slargestop:
	LEAQ 0(SP), R13

send:
	MOVL $0, (R13)
	MOVL $0, (R12)
	MOVQ 16(SP), AX
	MOVQ AX, bestLength+64(FP)
	MOVL 24(SP), AX
	MOVL AX, offBase+72(FP)
	MOVL 28(SP), AX
	MOVL AX, matchEndIdx+76(FP)
	RET

sbad:
	MOVL $0, matchEndIdx+76(FP)
	RET
