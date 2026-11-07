//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Shared by both functions (as zstd_lazy_dubt_amd64.s): R0 bt, R1 b, R2 ip, R3 iend,
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

// Fields of btAsmParams (see zstd_lazy_dubt_generic.go; TestBtAsmParamsLayout).
#define BP_BT 0
#define BP_B 8
#define BP_HASH 16
#define BP_MATCHES 24
#define BP_NEXT 32
#define BP_PRIME 40
#define BP_HSHIFT 48
#define BP_BASEIDX 56
#define BP_BTMASK 60
#define BP_NBCMP 64
#define BP_LOWEST 68
#define BP_MAXDIST 72
#define BP_ISDICT 76
#define BP_DICTLIMIT 80
#define BP_SUFF 84
#define BP_ILIMIT 88

// func btGetAllNoDictAsm(p *btAsmParams, ip int, rep *[repNum]uint32, ll0, lengthToBeat uint32) (mnum uint32, bad bool)
//
// C: zstd_opt.c:ZSTD_btGetAllMatches_internal (noDict, mls 4 to 6): the
// skip check, the tree update (ZSTD_updateTree_internal with
// ZSTD_insertBt1) and ZSTD_insertBtAndGetAllMatches, as zstd_lazy_dubt_amd64.s (see
// it for the bounds). Registers as in the functions above, with R15
// windowLow while updating, then matchLow; R21 bestLength, R22 mnum, R23
// matchEndIdx, R24 curr, R25 p, R26 the position being inserted (C's idx).
// Frame: 8(RSP) dummy32, 16(RSP) hash, 24(RSP) windowLow.
TEXT ·btGetAllNoDictAsm(SB), NOSPLIT, $32-37
	MOVD  p+0(FP), R25
	MOVD  BP_BT(R25), R0
	MOVD  BP_B(R25), R1
	MOVD  BP_ILIMIT(R25), R3
	MOVWU BP_BTMASK(R25), R12
	MOVWU BP_BASEIDX(R25), R13
	MOVD  ip+8(FP), R2
	ADDW  R2, R13, R24          // curr
	MOVB  ZR, bad+36(FP)

	// skipped area
	MOVD  BP_NEXT(R25), R16
	MOVWU (R16), R26            // nextToUpdate
	CMPW  R26, R24
	BHS   wlow
	MOVW  ZR, mnum+32(FP)
	RET

wlow:
	// windowLow = isDictionary ? lowestValid : withinWindow
	MOVWU BP_LOWEST(R25), R16
	MOVWU BP_MAXDIST(R25), R17
	SUBW  R16, R24, R19
	CMPW  R17, R19
	BLS   wlok
	SUBW  R17, R24, R16

wlok:
	MOVWU BP_ISDICT(R25), R19
	CBZW  R19, wlset
	MOVWU BP_LOWEST(R25), R16

wlset:
	MOVW  R16, 24(RSP)
	MOVW  R16, R15

	// update the tree: insert idx = nextToUpdate (R26) .. curr-1, each
	// walk bounded by windowLow (C computes it from the target, curr)
	CMPW  R24, R26
	BHS   search

uloop:
	SUBW  R13, R26, R2          // its position
	ADD   R2, R1, R4
	MOVD  (R4), R16
	MOVD  BP_PRIME(R25), R17
	MUL   R17, R16, R16
	MOVD  BP_HSHIFT(R25), R17
	LSR   R17, R16, R16
	MOVD  BP_HASH(R25), R17
	MOVWU (R17)(R16<<2), R9     // matchIndex
	MOVW  R26, (R17)(R16<<2)    // hashTable[h] = idx
	SUBSW R12, R26, R14
	CSELW HI, R14, ZR, R14      // btLow = btMask < idx ? idx - btMask : 0
	MOVD  $8, R21               // bestLength
	ADDW  $9, R26, R23          // matchEndIdx = idx + 8 + 1
	ANDW  R12, R26, R16
	ADD   R16<<3, R0, R7        // smallerPtr = &bt[2*(idx&btMask)]
	ADD   $4, R7, R8            // largerPtr
	MOVD  ZR, R5
	MOVD  ZR, R6
	MOVWU BP_NBCMP(R25), R10

ubloop:
	CBZW  R10, ubend
	CMPW  R15, R9
	BLO   ubend                 // matchIndex < windowLow
	MATCHLEN(gbad, ubcnt8, ubcdiff, ubctail, ubcdone)
	CMP   R21, R16
	BLE   ubnotbetter
	MOVD  R16, R21
	SUBW  R9, R23, R19
	CMP   R19, R16
	BLS   ubnotbetter
	ADDW  R16, R9, R23

ubnotbetter:
	ADD   R2, R16, R19
	CMP   R3, R19
	BEQ   ubend                 // equal: no way to know if inf or sup
	MOVBU (R17)(R16), R19
	MOVBU (R4)(R16), R20
	CMPW  R20, R19
	BHS   ublarger
	MOVW  R9, (R7)
	MOVD  R16, R5
	CMPW  R14, R9
	BLS   ubsmallstop
	ADD   $4, R11, R7
	MOVWU 4(R11), R9
	SUBW  $1, R10
	B     ubloop

ubsmallstop:
	ADD $8, RSP, R7
	B   ubend

ublarger:
	MOVW  R9, (R8)
	MOVD  R16, R6
	CMPW  R14, R9
	BLS   ublargestop
	MOVD  R11, R8
	MOVWU (R11), R9
	SUBW  $1, R10
	B     ubloop

ublargestop:
	ADD $8, RSP, R8

ubend:
	MOVW  ZR, (R8)
	MOVW  ZR, (R7)

	// idx += max(bestLength > 384 ? min(192, bestLength - 384) : 0, matchEndIdx - (idx + 8))
	SUBW  R26, R23, R16
	SUBW  $8, R16
	SUBS  $384, R21, R17
	BLE   ufwd
	MOVD  $192, R19
	CMP   R19, R17
	CSEL  GT, R19, R17, R17
	CMPW  R16, R17
	CSELW HI, R17, R16, R16

ufwd:
	ADDW  R16, R26, R26
	CMPW  R24, R26
	BLO   uloop
	MOVD  BP_NEXT(R25), R16
	MOVW  R24, (R16)            // nextToUpdate = curr

search:
	MOVD  ip+8(FP), R2
	ADD   R2, R1, R4

	// hash, and the first candidate
	MOVD  (R4), R16
	MOVD  BP_PRIME(R25), R17
	MUL   R17, R16, R16
	MOVD  BP_HSHIFT(R25), R17
	LSR   R17, R16, R16
	MOVD  R16, 16(RSP)
	MOVD  BP_HASH(R25), R17
	MOVWU (R17)(R16<<2), R9

	// btLow = btMask < curr ? curr - btMask : 0
	SUBSW R12, R24, R14
	CSELW HI, R14, ZR, R14

	// matchLow = windowLow or 1
	MOVWU 24(RSP), R16
	MOVW  $1, R15
	CMPW  $0, R16
	CSELW NE, R16, R15, R15

	MOVD  ZR, R22               // mnum
	MOVWU lengthToBeat+28(FP), R21
	SUB   $1, R21               // bestLength (lengthToBeat >= 1)
	ADDW  $9, R24, R23          // matchEndIdx = curr + 8 + 1

	// repcodes: R7 repCode in [ll0, R8 = ll0 + 3)
	MOVWU ll0+24(FP), R7
	ADDW  $3, R7, R8

reploop:
	CMPW  R8, R7
	BHS   repdone
	MOVD  rep+16(FP), R16
	CMPW  $3, R7
	BNE   repn
	MOVWU (R16), R17
	SUBW  $1, R17               // rep[0] - 1
	B     repgot

repn:
	MOVWU (R16)(R7<<2), R17

repgot:
	// if repOffset-1 < curr-dictLimit (discards 0 and -1) && repIndex >= windowLow
	SUBW  $1, R17, R16
	MOVWU BP_DICTLIMIT(R25), R19
	SUBW  R19, R24, R19
	CMPW  R19, R16
	BHS   repnext
	SUBW  R17, R24, R16
	MOVWU 24(RSP), R19
	CMPW  R19, R16
	BLO   repnext
	SUB   R17, R4, R16          // b + ip - repOffset
	MOVWU (R16), R19
	MOVWU (R4), R20
	CMPW  R19, R20
	BNE   repnext
	// repLen = count(ip+4, ip+4-repOffset, iLimit) + 4
	ADD   $4, R16
	ADD   $4, R4, R5
	SUB   R2, R3, R10
	SUB   $4, R10
	MOVD  ZR, R11

rc8:
	ADD   $8, R11, R19
	CMP   R10, R19
	BHI   rctail
	MOVD  (R16)(R11), R19
	MOVD  (R5)(R11), R20
	EOR   R19, R20, R20
	CBNZ  R20, rcdiff
	ADD   $8, R11
	B     rc8

rcdiff:
	RBIT  R20, R20
	CLZ   R20, R20
	ADD   R20>>3, R11, R11
	B     rcdone

rctail:
	CMP   R10, R11
	BHS   rcdone
	MOVBU (R16)(R11), R19
	MOVBU (R5)(R11), R20
	CMPW  R19, R20
	BNE   rcdone
	ADD   $1, R11
	B     rctail

rcdone:
	ADD   $4, R11               // repLen
	CMP   R21, R11
	BLE   repnext
	MOVD  R11, R21
	MOVD  BP_MATCHES(R25), R16
	ADD   R22<<3, R16, R16
	MOVWU ll0+24(FP), R19
	SUBW  R19, R7, R19
	ADDW  $1, R19
	MOVW  R19, (R16)            // off = repCode - ll0 + 1
	MOVW  R11, 4(R16)           // len
	ADD   $1, R22
	MOVWU BP_SUFF(R25), R19
	CMP   R19, R11
	BHI   repret                // repLen > sufficientLen: best possible
	ADD   R2, R11, R19
	CMP   R3, R19
	BEQ   repret

repnext:
	ADDW  $1, R7
	B     reploop

repret:
	MOVW  R22, mnum+32(FP)
	RET

repdone:
	// hashTable[h] = curr
	MOVD  16(RSP), R16
	MOVD  BP_HASH(R25), R17
	MOVW  R24, (R17)(R16<<2)
	MOVD  ZR, R5
	MOVD  ZR, R6
	ANDW  R12, R24, R16
	ADD   R16<<3, R0, R7        // smallerPtr = &bt[2*(curr&btMask)]
	ADD   $4, R7, R8            // largerPtr
	MOVWU BP_NBCMP(R25), R10

gloop:
	CBZW  R10, gend
	CMPW  R15, R9
	BLO   gend                  // matchIndex < matchLow
	MATCHLEN(gbad, gcnt8, gcdiff, gctail, gcdone)
	CMP   R21, R16
	BLE   gnext
	SUBW  R9, R23, R19
	CMP   R19, R16
	BLS   gme
	ADDW  R16, R9, R23

gme:
	MOVD  R16, R21              // bestLength
	CMP   $4099, R22            // optSize
	BHS   gbad
	MOVD  BP_MATCHES(R25), R19
	ADD   R22<<3, R19, R19
	MOVW  R16, 4(R19)           // len
	SUBW  R9, R24, R20
	ADDW  $3, R20
	MOVW  R20, (R19)            // off = offsetToOffBase(curr - matchIndex)
	ADD   $1, R22
	CMP   $4096, R16
	BHI   gend                  // matchLength > optNum
	ADD   R2, R16, R19
	CMP   R3, R19
	BEQ   gend                  // equal: no way to know if inf or sup

gnext:
	MOVBU (R17)(R16), R19
	MOVBU (R4)(R16), R20
	CMPW  R20, R19
	BHS   glarger
	MOVW  R9, (R7)
	MOVD  R16, R5
	CMPW  R14, R9
	BLS   gsmallstop
	ADD   $4, R11, R7
	MOVWU 4(R11), R9
	SUBW  $1, R10
	B     gloop

gsmallstop:
	ADD $8, RSP, R7
	B   gend

glarger:
	MOVW  R9, (R8)
	MOVD  R16, R6
	CMPW  R14, R9
	BLS   glargestop
	MOVD  R11, R8
	MOVWU (R11), R9
	SUBW  $1, R10
	B     gloop

glargestop:
	ADD $8, RSP, R8

gend:
	MOVW  ZR, (R8)
	MOVW  ZR, (R7)
	MOVD  BP_NEXT(R25), R19
	SUBW  $8, R23, R20
	MOVW  R20, (R19)            // nextToUpdate = matchEndIdx - 8
	MOVW  R22, mnum+32(FP)
	RET

gbad:
	MOVD  $1, R19
	MOVB  R19, bad+36(FP)
	RET

// func dubtFindBestMatchAsm(p *btAsmParams, ip int) (bestLength int, offBase uint32, bad bool)
//
// C: zstd_lazy.c:ZSTD_updateDUBT + ZSTD_DUBT_findBestMatch (noDict); see
// zstd_lazy_dubt_amd64.s for the steps and bounds. The two loops are called with
// their stack arguments at 8(RSP) (insertDUBT1Asm: 8-65, dubtSearchAsm:
// 8-88). Locals: 96(RSP) p, 104(RSP) curr, 108(RSP) unsortLimit, 112(RSP)
// hash, 120(RSP) nbCandidates, 124(RSP) windowLow, 128(RSP) btLow,
// 132(RSP) the next candidate to sort.
TEXT ·dubtFindBestMatchAsm(SB), NOSPLIT, $144-29
	MOVD  p+0(FP), R25
	MOVD  R25, 96(RSP)
	MOVB  ZR, bad+28(FP)
	MOVD  BP_B(R25), R1
	MOVWU BP_BASEIDX(R25), R3
	SUB   R3, R1, R9            // b - baseIdx (no GC safepoint while it lives)
	MOVD  ip+8(FP), R2
	ADDW  R2, R3, R3            // curr
	MOVW  R3, 104(RSP)
	MOVD  BP_HASH(R25), R10
	MOVD  BP_BT(R25), R11
	MOVWU BP_BTMASK(R25), R13
	MOVD  BP_PRIME(R25), R14
	MOVD  BP_HSHIFT(R25), R15

	// updateDUBT: [nextToUpdate, curr) as unsorted candidates
	MOVD  BP_NEXT(R25), R4
	MOVWU (R4), R5
	B     uck

uloop:
	MOVD  (R9)(R5), R6
	MUL   R14, R6, R6
	LSR   R15, R6, R6
	MOVWU (R10)(R6<<2), R7
	MOVW  R5, (R10)(R6<<2)
	ANDW  R13, R5, R6
	ADD   R6<<3, R11, R6
	MOVW  R7, (R6)              // bt[2*(idx&btMask)] = matchIndex
	MOVD  $1, R7
	MOVW  R7, 4(R6)             // dubtUnsortedMark
	ADDW  $1, R5

uck:
	CMPW  R3, R5
	BLO   uloop
	MOVW  R3, (R4)              // nextToUpdate = curr

	ADD   R2, R1, R6
	MOVD  (R6), R6
	MUL   R14, R6, R6
	LSR   R15, R6, R6
	MOVD  R6, 112(RSP)
	MOVWU (R10)(R6<<2), R5      // matchIndex

	// windowLow (getLowestMatchIndex), btLow, unsortLimit = max(btLow, windowLow)
	MOVWU BP_LOWEST(R25), R6
	MOVWU BP_MAXDIST(R25), R7
	SUBW  R6, R3, R8
	CMPW  R7, R8
	BLS   wlok
	SUBW  R7, R3, R6

wlok:
	MOVWU BP_ISDICT(R25), R8
	CBZW  R8, wlset
	MOVWU BP_LOWEST(R25), R6

wlset:
	MOVW  R6, 124(RSP)
	SUBSW R13, R3, R7
	CSELW HI, R7, ZR, R7
	MOVW  R7, 128(RSP)
	CMPW  R6, R7
	CSELW HI, R7, R6, R8
	MOVW  R8, 108(RSP)

	// reach the end of the unsorted candidates, reversing the chain
	MOVWU BP_NBCMP(R25), R12    // nbCandidates
	MOVD  ZR, R16               // previousCandidate

chain:
	CMPW  R8, R5
	BLS   chaindone
	ANDW  R13, R5, R6
	ADD   R6<<3, R11, R6
	MOVWU 4(R6), R7
	CMPW  $1, R7
	BNE   chaindone
	CMPW  $1, R12
	BLS   nullify
	MOVW  R16, 4(R6)
	MOVWU R5, R16
	MOVWU (R6), R5
	SUBW  $1, R12
	B     chain

nullify:
	// the last candidate is still unsorted: drop it
	MOVD  ZR, (R6)

chaindone:
	MOVW  R12, 120(RSP)
	MOVW  R16, 132(RSP)

batch:
	// sort the stacked candidates, oldest first
	MOVWU 132(RSP), R5
	CBZW  R5, search
	MOVD  96(RSP), R25
	MOVD  BP_BT(R25), R11
	MOVWU BP_BTMASK(R25), R13
	ANDW  R13, R5, R6
	ADD   R6<<3, R11, R6
	MOVWU 4(R6), R7
	MOVW  R7, 132(RSP)
	MOVWU 104(RSP), R7
	CMPW  R7, R5
	BHS   bad
	MOVD  R11, 8(RSP)           // bt
	MOVD  BP_B(R25), R7
	MOVD  R7, 16(RSP)           // b
	MOVWU BP_BASEIDX(R25), R7
	SUBW  R7, R5, R8
	MOVD  R8, 24(RSP)           // ip
	MOVD  BP_ILIMIT(R25), R8
	MOVD  R8, 32(RSP)           // iend
	MOVW  R5, 40(RSP)           // curr
	MOVW  R7, 44(RSP)           // baseIdx
	MOVW  R13, 48(RSP)          // btMask
	MOVWU 120(RSP), R8
	MOVW  R8, 52(RSP)           // nbCompares = nbCandidates
	MOVWU 108(RSP), R8
	MOVW  R8, 56(RSP)           // btLow = unsortLimit
	MOVWU BP_LOWEST(R25), R8    // windowLow, as insertDUBT1
	MOVWU BP_MAXDIST(R25), R7
	SUBW  R8, R5, R6
	CMPW  R7, R6
	BLS   bwlok
	SUBW  R7, R5, R8

bwlok:
	MOVW  R8, 60(RSP)
	CALL  ·insertDUBT1Asm(SB)
	MOVBU 64(RSP), R7
	CBZW  R7, bad
	MOVWU 120(RSP), R7
	ADDW  $1, R7
	MOVW  R7, 120(RSP)
	B     batch

search:
	// find the longest match: matchIndex = hashTable[h]; hashTable[h] = curr
	MOVD  96(RSP), R25
	MOVD  BP_HASH(R25), R6
	MOVD  112(RSP), R7
	ADD   R7<<2, R6, R6
	MOVWU (R6), R5
	MOVWU 104(RSP), R8
	MOVW  R8, (R6)
	MOVD  BP_BT(R25), R7
	MOVD  R7, 8(RSP)
	MOVD  BP_B(R25), R7
	MOVD  R7, 16(RSP)
	MOVD  ip+8(FP), R7
	MOVD  R7, 24(RSP)
	MOVD  BP_ILIMIT(R25), R7
	MOVD  R7, 32(RSP)
	MOVW  R8, 40(RSP)           // curr
	MOVWU BP_BASEIDX(R25), R7
	MOVW  R7, 44(RSP)
	MOVWU BP_BTMASK(R25), R7
	MOVW  R7, 48(RSP)
	MOVWU BP_NBCMP(R25), R7
	MOVW  R7, 52(RSP)
	MOVWU 128(RSP), R7
	MOVW  R7, 56(RSP)           // btLow
	MOVWU 124(RSP), R7
	MOVW  R7, 60(RSP)           // windowLow
	MOVW  R5, 64(RSP)           // matchIndex
	CALL  ·dubtSearchAsm(SB)
	MOVWU 84(RSP), R7           // matchEndIdx
	CBZW  R7, bad
	SUBW  $8, R7
	MOVD  96(RSP), R25
	MOVD  BP_NEXT(R25), R6
	MOVW  R7, (R6)              // nextToUpdate = matchEndIdx - 8 (skip repetitive patterns)
	MOVD  72(RSP), R7
	MOVD  R7, bestLength+16(FP)
	MOVWU 80(RSP), R7
	MOVW  R7, offBase+24(FP)
	RET

bad:
	MOVD  $1, R7
	MOVB  R7, bad+28(FP)
	RET
