//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// The routines here mirror row_amd64.s. The match mask is computed as in C's
// ZSTD_row_getNEONMask: a 64-bit mask with G = 4, 2 or 1 bits per entry for
// rowLog 4, 5 or 6 (entry n at bits n*G .. n*G+G-1, its top bit kept),
// rotated right by head*G; matchPos = (head + ctz/G) & rowMask, the same
// candidates in the same order as a one-bit-per-entry mask.

// func rowUpdateCachedAsm(hashTable *uint32, tagTable *byte, cache *[8]uint32, b *byte,
//	idx, end, baseIdx, rowLog, rowMask uint32, shift, hShift, prime, saltAdj uint64)
//
// Registers: R0 hashTable, R1 tagTable, R2 cache, R3 b - baseIdx (so an
// index addresses b directly), R4 idx, R5 end, R6 prime << shift, R7 saltAdj,
// R8 hShift, R9 rowLog, R10 rowMask, R11-R17 scratch. No calls and no stack
// frame, so the out-of-object R3 is never seen by the GC.
TEXT ·rowUpdateCachedAsm(SB), NOSPLIT|NOFRAME, $0-88
	MOVD  hashTable+0(FP), R0
	MOVD  tagTable+8(FP), R1
	MOVD  cache+16(FP), R2
	MOVD  b+24(FP), R3
	MOVWU idx+32(FP), R4
	MOVWU end+36(FP), R5
	MOVWU baseIdx+40(FP), R11
	SUB   R11, R3, R3
	MOVWU rowLog+44(FP), R9
	MOVWU rowMask+48(FP), R10
	MOVD  shift+56(FP), R11
	MOVD  prime+72(FP), R6
	LSL   R11, R6, R6           // (v << shift) * prime == v * (prime << shift)
	MOVD  saltAdj+80(FP), R7
	MOVD  hShift+64(FP), R8

loop:
	// newHash = hash of idx + rowHashCacheSize
	ADD  R4, R3, R11
	MOVD 8(R11), R12
	MUL  R6, R12, R12
	EOR  R7, R12, R12
	LSR  R8, R12, R12

	// prefetch its row (C: ZSTD_row_prefetch)
	LSRW $8, R12, R13
	LSLW R9, R13, R13
	ADD  R13<<2, R0, R14
	PRFM (R14), PLDL1KEEP
	CMPW $5, R9
	BLO  pftag
	PRFM 64(R14), PLDL1KEEP

pftag:
	ADD  R13, R1, R14
	PRFM (R14), PLDL1KEEP
	CMPW $6, R9
	BNE  pfdone
	PRFM 32(R14), PLDL1KEEP

pfdone:
	// hash = cache[idx&7]; cache[idx&7] = newHash
	ANDW  $7, R4, R11
	MOVWU (R2)(R11<<2), R15
	MOVW  R12, (R2)(R11<<2)

	// relRow = (hash >> 8) << rowLog
	LSRW $8, R15, R13
	LSLW R9, R13, R13

	// pos = rowNextIndex(tagRow, rowMask)
	MOVBU (R1)(R13), R16
	SUBW  $1, R16
	ANDW  R10, R16, R16
	CBNZW R16, pos
	MOVW  R10, R16

pos:
	MOVB R16, (R1)(R13)

	// tagRow[pos] = byte(hash); hashTable[relRow+pos] = idx
	ADD  R16, R13, R13
	MOVB R15, (R1)(R13)
	MOVW R4, (R0)(R13<<2)

	ADDW $1, R4
	CMPW R5, R4
	BLO  loop
	RET

// MASK(tagRow, rowLog, head, out, lg, t): out = the NEON match mask of the
// tagRow entries equal to the tag splatted in V0, rotated by head (C:
// ZSTD_row_getNEONMask); lg = log2 of its bits per entry. Clobbers t,
// V1-V4. The labels are passed in to keep them unique per function.
#define MASK(tagRow, rowLog, head, out, lg, t, m32, m64, mdone) \
	CMPW  $5, rowLog; \
	BEQ   m32; \
	BHI   m64; \
	VLD1  (tagRow), [V1.B16]; \
	VCMEQ V0.B16, V1.B16, V1.B16; \
	VSHRN $4, V1.H8, V1.B8; \
	VMOV  V1.D[0], out; \
	MOVD  $2, lg; \
	LSLW  $2, head, t; \
	ROR   t, out, out; \
	AND   $0x8888888888888888, out, out; \
	B     mdone; \
m32: \
	VLD2  (tagRow), [V1.H8, V2.H8]; \
	VCMEQ V0.B16, V1.B16, V1.B16; \
	VCMEQ V0.B16, V2.B16, V2.B16; \
	VSHRN $6, V1.H8, V1.B8; \
	VSHRN $6, V2.H8, V2.B8; \
	VSLI  $4, V2.B8, V1.B8; \
	VMOV  V1.D[0], out; \
	MOVD  $1, lg; \
	LSLW  $1, head, t; \
	ROR   t, out, out; \
	AND   $0x5555555555555555, out, out; \
	B     mdone; \
m64: \
	VLD4  (tagRow), [V1.B16, V2.B16, V3.B16, V4.B16]; \
	VCMEQ V0.B16, V1.B16, V1.B16; \
	VCMEQ V0.B16, V2.B16, V2.B16; \
	VCMEQ V0.B16, V3.B16, V3.B16; \
	VCMEQ V0.B16, V4.B16, V4.B16; \
	VSRI  $1, V1.B16, V2.B16; \
	VSRI  $1, V3.B16, V4.B16; \
	VSRI  $2, V2.B16, V4.B16; \
	VSRI  $4, V4.B16, V4.B16; \
	VSHRN $4, V4.H8, V4.B8; \
	VMOV  V4.D[0], out; \
	MOVD  ZR, lg; \
	ROR   head, out, out; \
mdone:

// func rowSearchAsm(hashTable *uint32, tagTable *byte, b *byte, ip, iLimit int,
//	baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, nextToUpdate uint32) (ml int, best uint32)
//
// Registers: R0 row, R1 tagRow, R2 b, R3 ip, R4 iLimit, R5 baseIdx,
// R6 head, R7 matches, R8 rowMask, R9 numMatches, R10 nbAttempts,
// R11 lowLimit, R12 lg, R13 candidate buffer, R14-R17, R19, R20 scratch.
// The candidates are kept in the 256-byte frame (C: matchBuffer).
TEXT ·rowSearchAsm(SB), NOSPLIT, $256-84
	MOVD  hashTable+0(FP), R0
	MOVD  tagTable+8(FP), R1
	MOVWU relRow+44(FP), R14
	ADD   R14<<2, R0, R0
	ADD   R14, R1, R1
	MOVD  b+16(FP), R2
	MOVD  ip+24(FP), R3
	MOVD  iLimit+32(FP), R4
	MOVWU baseIdx+40(FP), R5
	MOVWU nbAttempts+56(FP), R10
	MOVWU lowLimit+60(FP), R11

	// rowMask, head
	MOVWU rowLog+52(FP), R15
	MOVD  $1, R8
	LSLW  R15, R8, R8
	SUBW  $1, R8
	MOVBU (R1), R6
	ANDW  R8, R6, R6

	MOVWU tag+48(FP), R14
	VDUP  R14, V0.B16
	MASK(R1, R15, R6, R7, R12, R14, mask32, mask64, gather)
	MOVD ZR, R9
	ADD  $8, RSP, R13

gatherloop:
	CBZ   R7, insert
	CBZW  R10, insert
	RBIT  R7, R14
	CLZ   R14, R14
	SUB   $1, R7, R15
	AND   R15, R7, R7           // matches &= matches - 1
	LSR   R12, R14, R14
	ADDW  R14, R6, R14
	ANDW  R8, R14, R14          // matchPos
	CBZW  R14, gatherloop       // the head entry is not a candidate
	MOVWU (R0)(R14<<2), R15     // matchIndex
	CMPW  R11, R15
	BLO   insert                // below lowLimit: stop
	SUBW  R5, R15, R14          // its position in b
	CMP   R3, R14
	BHS   bad                   // not before ip: table invariant broken
	ADD   R14, R2, R16
	PRFM  (R16), PLDL1KEEP
	MOVW  R15, (R13)(R9<<2)
	ADD   $1, R9
	SUBW  $1, R10
	B     gatherloop

insert:
	// insert ip (nextToUpdate) at rowNextIndex(tagRow, rowMask)
	MOVBU (R1), R14
	SUBW  $1, R14
	ANDW  R8, R14, R14
	CBNZW R14, ins
	MOVW  R8, R14

ins:
	MOVB  R14, (R1)
	MOVWU tag+48(FP), R15
	MOVB  R15, (R1)(R14)
	MOVWU nextToUpdate+64(FP), R15
	MOVW  R15, (R0)(R14<<2)

	// longest match: R6 ml, R7 best, R8 k, R9 numMatches, R1 b+ip,
	// R4 iLimit-ip, R0 b+match, R10 length counted
	MOVD $3, R6
	MOVD ZR, R7
	MOVD ZR, R8
	ADD  R3, R2, R1
	SUB  R3, R4, R4

cmploop:
	CMP   R9, R8
	BHS   done
	MOVWU (R13)(R8<<2), R11
	ADD   $1, R8
	SUBW  R5, R11, R14
	ADD   R14, R2, R0
	ADD   R6, R0, R14
	MOVWU -3(R14), R14
	ADD   R6, R1, R15
	MOVWU -3(R15), R15
	CMPW  R14, R15
	BNE   cmploop

	// R10 = count(b, ip, match, iLimit)
	MOVD ZR, R10

cnt8:
	ADD  $8, R10, R14
	CMP  R4, R14
	BHI  cnttail
	MOVD (R0)(R10), R14
	MOVD (R1)(R10), R15
	EOR  R14, R15, R15
	CBNZ R15, cntdiff
	ADD  $8, R10
	B    cnt8

cntdiff:
	RBIT R15, R15
	CLZ  R15, R15
	ADD  R15>>3, R10, R10
	B    cntdone

cnttail:
	CMP   R4, R10
	BHS   cntdone
	MOVBU (R0)(R10), R14
	MOVBU (R1)(R10), R15
	CMPW  R14, R15
	BNE   cntdone
	ADD   $1, R10
	B     cnttail

cntdone:
	CMP  R6, R10
	BLE  cmploop
	MOVD R10, R6
	MOVD R11, R7
	CMP  R4, R10
	BNE  cmploop                // ip+ml == iLimit: best possible

done:
	MOVD R6, ml+72(FP)
	MOVW R7, best+80(FP)
	RET

bad:
	MOVD $-1, R14
	MOVD R14, ml+72(FP)
	MOVW ZR, best+80(FP)
	RET

// Fields of rowAsmParams (see row_generic.go), with p in R21.
#define P_HASHTABLE 0(R21)
#define P_TAGTABLE 8(R21)
#define P_B 16(R21)
#define P_CACHE 24(R21)
#define P_NEXT 32(R21)
#define P_ENTROPY 40(R21)
#define P_SHIFT 48(R21)
#define P_HSHIFT 56(R21)
#define P_PRIME 64(R21)
#define P_SALT 72(R21)
#define P_BASEIDX 80(R21)
#define P_ROWLOG 84(R21)
#define P_ROWMASK 88(R21)
#define P_ATTEMPTS 92(R21)
#define P_LOWEST 96(R21)
#define P_MAXDIST 100(R21)
#define P_ISDICT 104(R21)
#define P_PFHASH2 120(R21)
#define P_PFTAG2 128(R21)

// Frame locals of rowFindBestMatchAsm (after its 256-byte candidate buffer).
#define CURR 264(RSP)
#define LOWLIM 268(RSP)
#define TAG 272(RSP)

// HROW(pos, dst): the salted row hash of b[pos:] (R2 = b). Clobbers R19, R20.
#define HROW(pos, dst) \
	MOVD (R2)(pos), dst; \
	MOVD P_SHIFT, R19; \
	MOVD P_PRIME, R20; \
	LSL  R19, R20, R20; \
	MUL  R20, dst, dst; \
	MOVD P_SALT, R19; \
	EOR  R19, dst, dst; \
	MOVD P_HSHIFT, R19; \
	LSR  R19, dst, dst

// PFROW(hash, t): C's ZSTD_row_prefetch for the row of hash. Clobbers t, R19, R20.
#define PFROW(hash, t) \
	LSRW  $8, hash, t; \
	MOVWU P_ROWLOG, R19; \
	LSLW  R19, t, t; \
	MOVD  P_HASHTABLE, R19; \
	ADD   t<<2, R19, R19; \
	PRFM  (R19), PLDL1KEEP; \
	MOVD  P_PFHASH2, R20; \
	ADD   R20, R19, R19; \
	PRFM  (R19), PLDL1KEEP; \
	MOVD  P_TAGTABLE, R19; \
	ADD   t, R19, t; \
	PRFM  (t), PLDL1KEEP; \
	MOVD  P_PFTAG2, R20; \
	ADD   R20, t, t; \
	PRFM  (t), PLDL1KEEP

// rowUpdateAsmHelper: rowUpdateCachedGeneric for [R3, R4) with the
// parameters at R21, prefetching each new hash's row. Clobbers R0-R17,
// R19, R20.
TEXT ·rowUpdateAsmHelper(SB), NOSPLIT|NOFRAME, $0-0
	CMPW  R4, R3
	BHS   hdone
	MOVD  P_HASHTABLE, R0
	MOVD  P_TAGTABLE, R1
	MOVD  P_CACHE, R5
	MOVD  P_B, R2
	MOVWU P_BASEIDX, R6
	SUB   R6, R2, R2            // b - baseIdx
	MOVD  P_SHIFT, R6
	MOVD  P_PRIME, R7
	LSL   R6, R7, R7
	MOVD  P_SALT, R8
	MOVD  P_HSHIFT, R9
	MOVWU P_ROWLOG, R10
	MOVWU P_ROWMASK, R11

hloop:
	ADD   R3, R2, R12
	MOVD  8(R12), R13
	MUL   R7, R13, R13
	EOR   R8, R13, R13
	LSR   R9, R13, R13          // newHash
	PFROW(R13, R14)
	ANDW  $7, R3, R12
	MOVWU (R5)(R12<<2), R15     // hash
	MOVW  R13, (R5)(R12<<2)
	LSRW  $8, R15, R14
	LSLW  R10, R14, R14         // relRow
	MOVBU (R1)(R14), R16
	SUBW  $1, R16
	ANDW  R11, R16, R16
	CBNZW R16, hpos
	MOVW  R11, R16

hpos:
	MOVB R16, (R1)(R14)
	ADD  R16, R14, R14
	MOVB R15, (R1)(R14)
	MOVW R3, (R0)(R14<<2)
	ADDW $1, R3
	CMPW R4, R3
	BLO  hloop

hdone:
	RET

// func rowFindBestMatchAsm(p *rowAsmParams, ip, iLimit int, lazySkipping bool) (ml int, offBase uint32)
//
// C: zstd_lazy.c:ZSTD_RowFindBestMatch (noDict) with ZSTD_row_update_internal
// and ZSTD_row_nextCachedHash. R21 = p throughout; the candidates are kept
// in the first 256 bytes of the frame (from 8(RSP)). The search registers
// are as in rowSearchAsm.
TEXT ·rowFindBestMatchAsm(SB), NOSPLIT, $280-44
	MOVD  p+0(FP), R21
	MOVD  P_B, R2
	MOVD  ip+8(FP), R3
	MOVWU P_BASEIDX, R14
	ADDW  R3, R14, R14
	MOVW  R14, CURR

	// lowLimit = isDictionary ? lowestValid : withinMaxDistance
	MOVWU P_LOWEST, R15
	MOVWU P_MAXDIST, R16
	SUBW  R15, R14, R17
	CMPW  R16, R17
	BLS   lowok
	SUBW  R16, R14, R15

lowok:
	MOVWU P_ISDICT, R17
	CBZW  R17, lowset
	MOVWU P_LOWEST, R15

lowset:
	MOVW  R15, LOWLIM
	MOVBU lazySkipping+24(FP), R17
	CBNZW R17, skipping

	// ZSTD_row_update_internal(ms, ip, mls, rowLog, rowMask, useCache=1)
	MOVD  P_NEXT, R17
	MOVWU (R17), R3             // idx
	MOVWU CURR, R4              // target
	SUBW  R3, R4, R17
	CMPW  $384, R17             // kSkipThreshold
	BLS   upd
	ADDW  $96, R3, R4           // kMaxMatchStartPositionsToUpdate
	CALL  ·rowUpdateAsmHelper(SB)
	// ZSTD_row_fillHashCache from idx = target - 32 (8 entries: they all
	// lie before ip+1)
	MOVD  P_B, R2
	MOVD  P_CACHE, R5
	MOVWU P_BASEIDX, R6
	MOVWU CURR, R3
	SUBW  $32, R3               // kMaxMatchEndPositionsToUpdate
	MOVD  $8, R7

fill:
	SUBW  R6, R3, R0
	HROW(R0, R10)
	PFROW(R10, R11)
	ANDW  $7, R3, R0
	MOVW  R10, (R5)(R0<<2)
	ADDW  $1, R3
	SUBS  $1, R7
	BNE   fill
	MOVWU CURR, R4
	SUBW  $32, R4, R3

upd:
	CALL  ·rowUpdateAsmHelper(SB)
	MOVD  P_NEXT, R17
	MOVWU CURR, R14
	MOVW  R14, (R17)            // nextToUpdate = target

	// hash = ZSTD_row_nextCachedHash(curr)
	MOVD  P_B, R2
	MOVD  ip+8(FP), R3
	ADD   $8, R3, R0
	HROW(R0, R10)
	PFROW(R10, R11)
	MOVD  P_CACHE, R5
	ANDW  $7, R14, R0
	MOVWU (R5)(R0<<2), R16
	MOVW  R10, (R5)(R0<<2)
	B     havehash

skipping:
	HROW(R3, R16)
	MOVD P_NEXT, R17
	MOVW R14, (R17)

havehash:
	// R16 = hash, R2 = b, R3 = ip
	MOVD  P_ENTROPY, R17
	MOVWU (R17), R15
	ADDW  R16, R15, R15
	MOVW  R15, (R17)            // collect salt entropy
	ANDW  $0xFF, R16, R15
	MOVW  R15, TAG
	VDUP  R15, V0.B16
	LSRW  $8, R16, R16
	MOVWU P_ROWLOG, R17
	LSLW  R17, R16, R16         // relRow
	MOVD  P_HASHTABLE, R0
	ADD   R16<<2, R0, R0        // row
	MOVD  P_TAGTABLE, R1
	ADD   R16, R1, R1           // tagRow
	MOVD  iLimit+16(FP), R4
	MOVWU P_BASEIDX, R5
	MOVWU P_ATTEMPTS, R10
	MOVWU P_ROWMASK, R8
	MOVBU (R1), R6
	ANDW  R8, R6, R6            // head
	MASK(R1, R17, R6, R7, R12, R14, fmask32, fmask64, fgather)
	MOVWU LOWLIM, R11
	MOVD  ZR, R9
	ADD   $8, RSP, R13

fgatherloop:
	CBZ   R7, finsert
	CBZW  R10, finsert
	RBIT  R7, R14
	CLZ   R14, R14
	SUB   $1, R7, R15
	AND   R15, R7, R7
	LSR   R12, R14, R14
	ADDW  R14, R6, R14
	ANDW  R8, R14, R14
	CBZW  R14, fgatherloop
	MOVWU (R0)(R14<<2), R15
	CMPW  R11, R15
	BLO   finsert
	SUBW  R5, R15, R14
	CMP   R3, R14
	BHS   fbad
	ADD   R14, R2, R16
	PRFM  (R16), PLDL1KEEP
	MOVW  R15, (R13)(R9<<2)
	ADD   $1, R9
	SUBW  $1, R10
	B     fgatherloop

finsert:
	MOVBU (R1), R14
	SUBW  $1, R14
	ANDW  R8, R14, R14
	CBNZW R14, fins
	MOVW  R8, R14

fins:
	MOVB  R14, (R1)
	MOVWU TAG, R15
	MOVB  R15, (R1)(R14)
	MOVD  P_NEXT, R16
	MOVWU (R16), R15
	MOVW  R15, (R0)(R14<<2)
	ADDW  $1, R15
	MOVW  R15, (R16)            // nextToUpdate++

	MOVD $3, R6
	MOVD ZR, R7
	MOVD ZR, R8
	ADD  R3, R2, R1
	SUB  R3, R4, R4

fcmploop:
	CMP   R9, R8
	BHS   fdone
	MOVWU (R13)(R8<<2), R11
	ADD   $1, R8
	SUBW  R5, R11, R14
	ADD   R14, R2, R0
	ADD   R6, R0, R14
	MOVWU -3(R14), R14
	ADD   R6, R1, R15
	MOVWU -3(R15), R15
	CMPW  R14, R15
	BNE   fcmploop
	MOVD  ZR, R10

fcnt8:
	ADD  $8, R10, R14
	CMP  R4, R14
	BHI  fcnttail
	MOVD (R0)(R10), R14
	MOVD (R1)(R10), R15
	EOR  R14, R15, R15
	CBNZ R15, fcntdiff
	ADD  $8, R10
	B    fcnt8

fcntdiff:
	RBIT R15, R15
	CLZ  R15, R15
	ADD  R15>>3, R10, R10
	B    fcntdone

fcnttail:
	CMP   R4, R10
	BHS   fcntdone
	MOVBU (R0)(R10), R14
	MOVBU (R1)(R10), R15
	CMPW  R14, R15
	BNE   fcntdone
	ADD   $1, R10
	B     fcnttail

fcntdone:
	CMP  R6, R10
	BLE  fcmploop
	MOVD R10, R6
	MOVD R11, R7
	CMP  R4, R10
	BNE  fcmploop

fdone:
	MOVD R6, ml+32(FP)
	CMP  $3, R6
	BLE  fnone
	MOVWU CURR, R14
	SUBW R7, R14
	ADDW $3, R14                // offsetToOffBase(curr - matchIndex)
	MOVW R14, offBase+40(FP)
	RET

fnone:
	MOVD $999999999, R14        // offBaseNotFound
	MOVW R14, offBase+40(FP)
	RET

fbad:
	MOVD $-1, R14
	MOVD R14, ml+32(FP)
	MOVW ZR, offBase+40(FP)
	RET
