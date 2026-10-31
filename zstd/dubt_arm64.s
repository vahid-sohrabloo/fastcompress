//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Shared by both functions (as dubt_amd64.s): R0 bt, R1 b, R2 ip, R3 iend,
// R4 b+ip, R5 commonLengthSmaller, R6 commonLengthLarger, R7 smallerPtr,
// R8 largerPtr, R9 matchIndex, R10 nbCompares, R11 nextPtr, R12 btMask,
// R13 baseIdx, R14 btLow, R15 windowLow, R16/R17/R19/R20 scratch.
// Frame: 8(RSP) dummy32.

// MATCHLEN: R17 = b + matchIndex's position, R16 = min(cls, cll) +
// ZSTD_count from there (C's matchLength); R11 = &bt[2*(matchIndex&btMask)].
// Jumps to bad if the candidate is not before ip.
#define MATCHLEN(bad, cnt8, cdiff, ctail, cdone) \
	ANDW  R12, R9, R16; \
	ADD   R16<<3, R0, R11; \
	CMP   R5, R6; \
	CSEL  LT, R6, R5, R16; \
	SUBW  R13, R9, R17; \
	CMP   R2, R17; \
	BHS   bad; \
	ADD   R17, R1, R17; \
cnt8: \
	ADD   R2, R16, R19; \
	ADD   $8, R19; \
	CMP   R3, R19; \
	BHI   ctail; \
	MOVD  (R17)(R16), R19; \
	MOVD  (R4)(R16), R20; \
	EOR   R19, R20, R20; \
	CBNZ  R20, cdiff; \
	ADD   $8, R16; \
	B     cnt8; \
cdiff: \
	RBIT  R20, R20; \
	CLZ   R20, R20; \
	ADD   R20>>3, R16, R16; \
	B     cdone; \
ctail: \
	ADD   R2, R16, R19; \
	CMP   R3, R19; \
	BHS   cdone; \
	MOVBU (R17)(R16), R19; \
	MOVBU (R4)(R16), R20; \
	CMPW  R19, R20; \
	BNE   cdone; \
	ADD   $1, R16; \
	B     ctail; \
cdone:

// SETUP: the shared registers from the arguments (all but matchIndex and
// nbCompares).
#define SETUP \
	MOVD  bt+0(FP), R0; \
	MOVD  b+8(FP), R1; \
	MOVD  ip+16(FP), R2; \
	MOVD  iend+24(FP), R3; \
	ADD   R2, R1, R4; \
	MOVD  ZR, R5; \
	MOVD  ZR, R6; \
	MOVWU btMask+40(FP), R12; \
	MOVWU baseIdx+36(FP), R13; \
	MOVWU btLow+48(FP), R14; \
	MOVWU windowLow+52(FP), R15; \
	MOVWU curr+32(FP), R16; \
	ANDW  R12, R16, R16; \
	ADD   R16<<3, R0, R7; \
	ADD   $4, R7, R8

// func insertDUBT1Asm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow uint32) bool
//
// C: zstd_lazy.c:ZSTD_insertDUBT1 (noDict).
TEXT ·insertDUBT1Asm(SB), NOSPLIT, $16-57
	SETUP
	MOVWU (R7), R9              // matchIndex = *smallerPtr
	MOVWU nbCompares+44(FP), R10
	MOVD  $1, R16
	MOVB  R16, ret+56(FP)

iloop:
	CBZW  R10, iend
	CMPW  R15, R9
	BLS   iend
	MATCHLEN(ibad, icnt8, icdiff, ictail, icdone)
	ADD   R2, R16, R19
	CMP   R3, R19
	BEQ   iend                  // equal: no way to know if inf or sup
	MOVBU (R17)(R16), R19
	MOVBU (R4)(R16), R20
	CMPW  R20, R19
	BHS   ilarger
	// match is smaller than current
	MOVW  R9, (R7)
	MOVD  R16, R5
	CMPW  R14, R9
	BLS   ismallstop
	ADD   $4, R11, R7
	MOVWU 4(R11), R9
	SUBW  $1, R10
	B     iloop

ismallstop:
	ADD $8, RSP, R7
	B   iend

ilarger:
	MOVW  R9, (R8)
	MOVD  R16, R6
	CMPW  R14, R9
	BLS   ilargestop
	MOVD  R11, R8
	MOVWU (R11), R9
	SUBW  $1, R10
	B     iloop

ilargestop:
	ADD $8, RSP, R8

iend:
	MOVW ZR, (R8)
	MOVW ZR, (R7)
	RET

ibad:
	MOVB ZR, ret+56(FP)
	RET

// func dubtSearchAsm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, offBase, matchEndIdx uint32)
//
// C: zstd_lazy.c:ZSTD_DUBT_findBestMatch, "find longest match" (noDict).
// Extra registers: R21 bestLength, R22 offBase, R23 matchEndIdx, R24 curr.
TEXT ·dubtSearchAsm(SB), NOSPLIT, $16-80
	SETUP
	MOVWU matchIndex+56(FP), R9
	MOVWU nbCompares+44(FP), R10
	MOVD  ZR, R21
	MOVD  $999999999, R22       // offBaseNotFound
	MOVWU curr+32(FP), R24
	ADDW  $9, R24, R23          // matchEndIdx = curr + 8 + 1

sloop:
	CBZW  R10, send
	CMPW  R15, R9
	BLS   send
	MATCHLEN(sbad, scnt8, scdiff, sctail, scdone)
	CMP   R21, R16
	BLE   snotbetter
	// if matchLength > matchEndIdx - matchIndex: matchEndIdx = matchIndex + matchLength
	SUBW  R9, R23, R19
	CMP   R19, R16
	BLS   sendok
	ADDW  R16, R9, R23

sendok:
	// if 4*(matchLength-bestLength) > highbit32(curr-matchIndex+1) - highbit32(offBase)
	// (highbit32(x) = 31 - clz32(x), so the right side is clz(offBase) - clz(curr-matchIndex+1))
	SUBW  R9, R24, R19
	ADDW  $1, R19
	CLZW  R19, R19
	CLZW  R22, R20
	SUB   R19, R20, R19
	SUB   R21, R16, R20
	LSL   $2, R20
	CMP   R19, R20
	BLE   snobest
	MOVD  R16, R21
	SUBW  R9, R24, R22
	ADDW  $3, R22

snobest:
	ADD   R2, R16, R19
	CMP   R3, R19
	BEQ   send

snotbetter:
	MOVBU (R17)(R16), R19
	MOVBU (R4)(R16), R20
	CMPW  R20, R19
	BHS   slarger
	MOVW  R9, (R7)
	MOVD  R16, R5
	CMPW  R14, R9
	BLS   ssmallstop
	ADD   $4, R11, R7
	MOVWU 4(R11), R9
	SUBW  $1, R10
	B     sloop

ssmallstop:
	ADD $8, RSP, R7
	B   send

slarger:
	MOVW  R9, (R8)
	MOVD  R16, R6
	CMPW  R14, R9
	BLS   slargestop
	MOVD  R11, R8
	MOVWU (R11), R9
	SUBW  $1, R10
	B     sloop

slargestop:
	ADD $8, RSP, R8

send:
	MOVW ZR, (R8)
	MOVW ZR, (R7)
	MOVD R21, bestLength+64(FP)
	MOVW R22, offBase+72(FP)
	MOVW R23, matchEndIdx+76(FP)
	RET

sbad:
	MOVW ZR, matchEndIdx+76(FP)
	RET

// func insertBt1Asm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, matchEndIdx uint32)
//
// C: zstd_opt.c:ZSTD_insertBt1 (extDict 0), the tree walk.
// Extra registers: R21 bestLength, R23 matchEndIdx.
TEXT ·insertBt1Asm(SB), NOSPLIT, $16-76
	SETUP
	MOVWU matchIndex+56(FP), R9
	MOVWU nbCompares+44(FP), R10
	MOVD  $8, R21
	MOVWU curr+32(FP), R23
	ADDW  $9, R23               // matchEndIdx = curr + 8 + 1

bloop:
	CBZW  R10, bend
	CMPW  R15, R9
	BLO   bend                  // matchIndex < windowLow
	MATCHLEN(bbad, bcnt8, bcdiff, bctail, bcdone)
	CMP   R21, R16
	BLE   bnotbetter
	MOVD  R16, R21
	SUBW  R9, R23, R19
	CMP   R19, R16
	BLS   bnotbetter
	ADDW  R16, R9, R23

bnotbetter:
	ADD   R2, R16, R19
	CMP   R3, R19
	BEQ   bend                  // equal: no way to know if inf or sup
	MOVBU (R17)(R16), R19
	MOVBU (R4)(R16), R20
	CMPW  R20, R19
	BHS   blarger
	MOVW  R9, (R7)
	MOVD  R16, R5
	CMPW  R14, R9
	BLS   bsmallstop
	ADD   $4, R11, R7
	MOVWU 4(R11), R9
	SUBW  $1, R10
	B     bloop

bsmallstop:
	ADD $8, RSP, R7
	B   bend

blarger:
	MOVW  R9, (R8)
	MOVD  R16, R6
	CMPW  R14, R9
	BLS   blargestop
	MOVD  R11, R8
	MOVWU (R11), R9
	SUBW  $1, R10
	B     bloop

blargestop:
	ADD $8, RSP, R8

bend:
	MOVW ZR, (R8)
	MOVW ZR, (R7)
	MOVD R21, bestLength+64(FP)
	MOVW R23, matchEndIdx+72(FP)
	RET

bbad:
	MOVW ZR, matchEndIdx+72(FP)
	RET
