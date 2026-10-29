//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Frame locals of rowFindBestMatchAsm (after its 256-byte candidate buffer).
#define CURR 256(SP)
#define LOWLIM 260(SP)
#define TAG 264(SP)

// func rowUpdateCachedAsm(hashTable *uint32, tagTable *byte, cache *[8]uint32, b *byte,
//	idx, end, baseIdx, rowLog, rowMask uint32, shift, hShift, prime, saltAdj uint64)
//
// Registers: AX hashTable, BX tagTable, DX cache, SI b - baseIdx (so an
// index addresses b directly), R8 idx, R9 end, R11 prime, R12 saltAdj,
// R13 shift, R14 hShift, CX shift counts, DI/R10/R15 scratch.
// No calls and no stack frame, so the out-of-object SI is never seen by the GC.
TEXT ·rowUpdateCachedAsm(SB), NOSPLIT, $0-88
	MOVQ hashTable+0(FP), AX
	MOVQ tagTable+8(FP), BX
	MOVQ cache+16(FP), DX
	MOVQ b+24(FP), SI
	MOVL idx+32(FP), R8
	MOVL end+36(FP), R9
	MOVL baseIdx+40(FP), R10
	SUBQ R10, SI
	MOVQ shift+56(FP), R13
	MOVQ hShift+64(FP), R14
	MOVQ prime+72(FP), R11
	MOVQ saltAdj+80(FP), R12

loop:
	// newHash = hash of idx + rowHashCacheSize
	MOVL R8, R10
	MOVQ 8(SI)(R10*1), R15
	MOVQ R13, CX
	SHLQ CX, R15
	IMULQ R11, R15
	XORQ R12, R15
	MOVQ R14, CX
	SHRQ CX, R15

	// prefetch its row (C: ZSTD_row_prefetch)
	MOVL R15, DI
	SHRL $8, DI
	MOVL rowLog+44(FP), CX
	SHLL CX, DI
	PREFETCHT0 (AX)(DI*4)
	CMPL CX, $5
	JB   pftag
	PREFETCHT0 64(AX)(DI*4)

pftag:
	PREFETCHT0 (BX)(DI*1)
	CMPL CX, $6
	JNE  pfdone
	PREFETCHT0 32(BX)(DI*1)

pfdone:
	// hash = cache[idx&7]; cache[idx&7] = newHash
	MOVL R8, R10
	ANDL $7, R10
	MOVL (DX)(R10*4), DI
	MOVL R15, (DX)(R10*4)
	MOVL DI, R15

	// relRow = (hash >> 8) << rowLog
	SHRL $8, DI
	SHLL CX, DI

	// pos = rowNextIndex(tagRow, rowMask)
	MOVBLZX (BX)(DI*1), R10
	DECL    R10
	MOVL    rowMask+48(FP), CX
	ANDL    CX, R10
	CMOVLEQ CX, R10
	MOVB    R10, (BX)(DI*1)

	// tagRow[pos] = byte(hash); hashTable[relRow+pos] = idx
	ADDQ R10, DI
	MOVB R15, (BX)(DI*1)
	MOVL R8, (AX)(DI*4)

	INCL R8
	CMPL R8, R9
	JB   loop
	RET

// func rowSearchAsm(hashTable *uint32, tagTable *byte, b *byte, ip, iLimit int,
//	baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, nextToUpdate uint32) (ml int, best uint32)
//
// Registers: AX row, BX tagRow, SI b, DI ip, R8 iLimit, R9 baseIdx,
// R10 headGrouped, R11 matches, R12 rowMask, R13 numMatches,
// R14 nbAttempts, R15 lowLimit, CX/DX scratch. The candidates are kept in
// the 256-byte frame (C: matchBuffer).
TEXT ·rowSearchAsm(SB), NOSPLIT, $256-84
	MOVQ hashTable+0(FP), AX
	MOVQ tagTable+8(FP), BX
	MOVL relRow+44(FP), CX
	LEAQ (AX)(CX*4), AX
	ADDQ CX, BX
	MOVQ b+16(FP), SI
	MOVQ ip+24(FP), DI
	MOVQ iLimit+32(FP), R8
	MOVL baseIdx+40(FP), R9
	MOVL nbAttempts+56(FP), R14
	MOVL lowLimit+60(FP), R15

	// rowMask, headGrouped
	MOVL rowLog+52(FP), CX
	MOVL $1, R12
	SHLL CX, R12
	DECL R12
	MOVBLZX (BX), R10
	ANDL R12, R10

	// X0 = tag in every byte
	MOVL tag+48(FP), DX
	MOVQ DX, X0
	PUNPCKLBW X0, X0
	PUNPCKLWL X0, X0
	PSHUFL $0, X0, X0

	// matches = bit n set when tagRow[n] == tag, rotated right by
	// headGrouped within the row (C: ZSTD_row_getSSEMask)
	MOVOU     (BX), X1
	PCMPEQB   X0, X1
	PMOVMSKB  X1, R11
	MOVL      R10, CX
	CMPL      R12, $15
	JNE       mask32
	RORW      CX, R11
	MOVWLZX   R11, R11
	JMP       gather

mask32:
	MOVOU    16(BX), X2
	PCMPEQB  X0, X2
	PMOVMSKB X2, DX
	SHLL     $16, DX
	ORL      DX, R11
	CMPL     R12, $31
	JNE      mask64
	RORL     CX, R11
	JMP      gather

mask64:
	MOVOU    32(BX), X3
	PCMPEQB  X0, X3
	PMOVMSKB X3, DX
	SHLQ     $32, DX
	ORQ      DX, R11
	MOVOU    48(BX), X3
	PCMPEQB  X0, X3
	PMOVMSKB X3, DX
	SHLQ     $48, DX
	ORQ      DX, R11
	RORQ     CX, R11

gather:
	XORL R13, R13

gatherloop:
	TESTQ R11, R11
	JEQ   insert
	TESTL R14, R14
	JEQ   insert
	BSFQ  R11, CX
	LEAQ  -1(R11), DX
	ANDQ  DX, R11          // matches &= matches - 1
	ADDL  R10, CX
	ANDL  R12, CX          // matchPos
	TESTL CX, CX
	JEQ   gatherloop       // the head entry is not a candidate
	MOVL  (AX)(CX*4), DX   // matchIndex
	CMPL  DX, R15
	JB    insert           // below lowLimit: stop
	MOVL  DX, CX
	SUBL  R9, CX           // its position in b
	CMPQ  CX, DI
	JAE   bad              // not before ip: table invariant broken
	PREFETCHT0 (SI)(CX*1)
	MOVL  DX, 0(SP)(R13*4)
	INCL  R13
	DECL  R14
	JMP   gatherloop

insert:
	// insert ip (nextToUpdate) at rowNextIndex(tagRow, rowMask)
	MOVBLZX (BX), CX
	DECL    CX
	ANDL    R12, CX
	CMOVLEQ R12, CX
	MOVB    CX, (BX)
	MOVL    tag+48(FP), DX
	MOVB    DX, (BX)(CX*1)
	MOVL    nextToUpdate+64(FP), DX
	MOVL    DX, (AX)(CX*4)

	// longest match: R10 ml, R11 best, R12 k, R13 numMatches,
	// BX b+ip, R15 iLimit-ip, AX b+match, R14 length counted
	MOVL $3, R10
	XORL R11, R11
	XORL R12, R12
	LEAQ (SI)(DI*1), BX
	MOVQ R8, R15
	SUBQ DI, R15

cmploop:
	CMPQ R12, R13
	JAE  done
	MOVL 0(SP)(R12*4), DX
	INCQ R12
	MOVL DX, CX
	SUBL R9, CX
	LEAQ (SI)(CX*1), AX
	MOVL -3(AX)(R10*1), R14
	CMPL R14, -3(BX)(R10*1)
	JNE  cmploop

	// R14 = count(b, ip, match, iLimit)
	XORL R14, R14

cnt8:
	LEAQ 8(R14), CX
	CMPQ CX, R15
	JA   cnttail
	MOVQ (AX)(R14*1), CX
	XORQ (BX)(R14*1), CX
	JNE  cntdiff
	ADDQ $8, R14
	JMP  cnt8

cntdiff:
	BSFQ CX, CX
	SHRQ $3, CX
	ADDQ CX, R14
	JMP  cntdone

cnttail:
	CMPQ    R14, R15
	JAE     cntdone
	MOVBLZX (AX)(R14*1), CX
	MOVBLZX (BX)(R14*1), R8
	CMPL    CX, R8
	JNE     cntdone
	INCQ    R14
	JMP     cnttail

cntdone:
	CMPQ R14, R10
	JLE  cmploop
	MOVQ R14, R10
	MOVL DX, R11
	CMPQ R14, R15
	JNE  cmploop          // ip+ml == iLimit: best possible

done:
	MOVQ R10, ml+72(FP)
	MOVL R11, best+80(FP)
	RET

bad:
	MOVQ $-1, ml+72(FP)
	MOVL $0, best+80(FP)
	RET

// Fields of rowAsmParams (see row_generic.go).
#define P_HASHTABLE 0(R15)
#define P_TAGTABLE 8(R15)
#define P_B 16(R15)
#define P_CACHE 24(R15)
#define P_NEXT 32(R15)
#define P_ENTROPY 40(R15)
#define P_SHIFT 48(R15)
#define P_HSHIFT 56(R15)
#define P_PRIME 64(R15)
#define P_SALT 72(R15)
#define P_BASEIDX 80(R15)
#define P_ROWLOG 84(R15)
#define P_ROWMASK 88(R15)
#define P_ATTEMPTS 92(R15)
#define P_LOWEST 96(R15)
#define P_MAXDIST 100(R15)
#define P_ISDICT 104(R15)
#define P_PFHASH2 120(R15)
#define P_PFTAG2 128(R15)

// HROW(pos, dst): the salted row hash of b[pos:] (SI = b). Clobbers CX.
#define HROW(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	MOVQ  P_SHIFT, CX; \
	SHLQ  CX, dst; \
	IMULQ P_PRIME, dst; \
	XORQ  P_SALT, dst; \
	MOVQ  P_HSHIFT, CX; \
	SHRQ  CX, dst

// PFROW(hash, t): C's ZSTD_row_prefetch for the row of hash. Clobbers CX, t.
#define PFROW(hash, t) \
	MOVL       hash, t; \
	SHRL       $8, t; \
	MOVL       P_ROWLOG, CX; \
	SHLL       CX, t; \
	MOVQ       P_HASHTABLE, CX; \
	LEAQ       (CX)(t*4), CX; \
	PREFETCHT0 (CX); \
	ADDQ       P_PFHASH2, CX; \
	PREFETCHT0 (CX); \
	ADDQ       P_TAGTABLE, t; \
	PREFETCHT0 (t); \
	ADDQ       P_PFTAG2, t; \
	PREFETCHT0 (t)

// rowUpdateAsmHelper: rowUpdateCachedGeneric for [R8, R9) with the
// parameters at R15, prefetching each new hash's row. Clobbers AX, BX, CX,
// DX, DI, SI, R8, R10-R14.
TEXT ·rowUpdateAsmHelper(SB), NOSPLIT, $0-0
	CMPL R8, R9
	JAE  hdone
	MOVQ P_HASHTABLE, AX
	MOVQ P_TAGTABLE, BX
	MOVQ P_CACHE, DX
	MOVQ P_B, SI
	MOVL P_BASEIDX, R10
	SUBQ R10, SI
	MOVQ P_SHIFT, R13
	MOVQ P_HSHIFT, R14
	MOVQ P_PRIME, R11
	MOVQ P_SALT, R12

hloop:
	MOVL R8, R10
	MOVQ 8(SI)(R10*1), DI
	MOVQ R13, CX
	SHLQ CX, DI
	IMULQ R11, DI
	XORQ R12, DI
	MOVQ R14, CX
	SHRQ CX, DI               // newHash
	PFROW(DI, R10)
	MOVL R8, R10
	ANDL $7, R10
	MOVL (DX)(R10*4), CX      // hash
	MOVL DI, (DX)(R10*4)
	MOVL CX, DI
	SHRL $8, CX
	MOVL CX, R10
	MOVL P_ROWLOG, CX
	SHLL CX, R10              // relRow
	MOVBLZX (BX)(R10*1), CX
	DECL    CX
	ANDL    P_ROWMASK, CX
	JNE     hpos
	MOVL    P_ROWMASK, CX

hpos:
	MOVB CX, (BX)(R10*1)
	ADDQ CX, R10
	MOVB DI, (BX)(R10*1)
	MOVL R8, (AX)(R10*4)
	INCL R8
	CMPL R8, R9
	JB   hloop

hdone:
	RET


// func rowFindBestMatchAsm(p *rowAsmParams, ip, iLimit int, lazySkipping bool) (ml int, offBase uint32)
//
// C: zstd_lazy.c:ZSTD_RowFindBestMatch (noDict) with ZSTD_row_update_internal
// and ZSTD_row_nextCachedHash. R15 = p throughout; the candidates are kept
// in the first 256 bytes of the frame. Search registers as in rowSearchAsm.
TEXT ·rowFindBestMatchAsm(SB), NOSPLIT, $272-44
	MOVQ p+0(FP), R15
	MOVQ P_B, SI
	MOVQ ip+8(FP), DI
	MOVL P_BASEIDX, CX
	ADDL DI, CX
	MOVL CX, CURR

	// lowLimit = isDictionary ? lowestValid : withinMaxDistance
	MOVL P_LOWEST, DX
	MOVL CX, BX
	SUBL DX, BX
	CMPL BX, P_MAXDIST
	JLS  lowok
	MOVL CX, DX
	SUBL P_MAXDIST, DX

lowok:
	CMPL P_ISDICT, $0
	JEQ  lowset
	MOVL P_LOWEST, DX

lowset:
	MOVL DX, LOWLIM
	CMPB lazySkipping+24(FP), $0
	JNE  skipping

	// ZSTD_row_update_internal(ms, ip, mls, rowLog, rowMask, useCache=1)
	MOVQ P_NEXT, AX
	MOVL (AX), R8           // idx
	MOVL CURR, R9           // target
	MOVL R9, AX
	SUBL R8, AX
	CMPL AX, $384           // kSkipThreshold
	JLS  upd
	MOVL R8, R9
	ADDL $96, R9            // kMaxMatchStartPositionsToUpdate
	CALL ·rowUpdateAsmHelper(SB)
	// ZSTD_row_fillHashCache from idx = target - 32 (8 entries: they all
	// lie before ip+1)
	MOVQ P_B, SI
	MOVQ P_CACHE, DX
	MOVL CURR, R8
	SUBL $32, R8            // kMaxMatchEndPositionsToUpdate
	MOVL $8, BX

fill:
	MOVL R8, AX
	SUBL P_BASEIDX, AX
	HROW(AX, R10)
	PFROW(R10, R11)
	MOVL R8, AX
	ANDL $7, AX
	MOVL R10, (DX)(AX*4)
	INCL R8
	DECL BX
	JNE  fill
	MOVL CURR, R8
	SUBL $32, R8
	MOVL CURR, R9

upd:
	CALL ·rowUpdateAsmHelper(SB)
	MOVQ P_NEXT, AX
	MOVL CURR, CX
	MOVL CX, (AX)           // nextToUpdate = target

	// hash = ZSTD_row_nextCachedHash(curr)
	MOVQ P_B, SI
	MOVQ ip+8(FP), DI
	LEAQ 8(DI), AX
	HROW(AX, R10)
	PFROW(R10, R11)
	MOVQ P_CACHE, DX
	MOVL CURR, AX
	ANDL $7, AX
	MOVL (DX)(AX*4), BX
	MOVL R10, (DX)(AX*4)
	JMP  havehash

skipping:
	HROW(DI, BX)
	MOVQ P_NEXT, AX
	MOVL CURR, CX
	MOVL CX, (AX)

havehash:
	MOVQ P_ENTROPY, AX
	ADDL BX, (AX)           // collect salt entropy
	MOVL BX, DX
	ANDL $0xFF, DX
	MOVL DX, TAG
	SHRL $8, BX
	MOVL P_ROWLOG, CX
	SHLL CX, BX             // relRow
	MOVQ P_HASHTABLE, AX
	LEAQ (AX)(BX*4), AX     // row
	ADDQ P_TAGTABLE, BX     // tagRow
	MOVQ iLimit+16(FP), R8
	MOVL P_BASEIDX, R9
	MOVL P_ATTEMPTS, R14
	MOVL P_ROWMASK, R12
	MOVBLZX (BX), R10
	ANDL R12, R10           // headGrouped

	MOVQ      DX, X0
	PUNPCKLBW X0, X0
	PUNPCKLWL X0, X0
	PSHUFL    $0, X0, X0
	MOVOU     (BX), X1
	PCMPEQB   X0, X1
	PMOVMSKB  X1, R11
	MOVL      R10, CX
	CMPL      R12, $15
	JNE       fmask32
	RORW      CX, R11
	MOVWLZX   R11, R11
	JMP       fgather

fmask32:
	MOVOU    16(BX), X2
	PCMPEQB  X0, X2
	PMOVMSKB X2, DX
	SHLL     $16, DX
	ORL      DX, R11
	CMPL     R12, $31
	JNE      fmask64
	RORL     CX, R11
	JMP      fgather

fmask64:
	MOVOU    32(BX), X3
	PCMPEQB  X0, X3
	PMOVMSKB X3, DX
	SHLQ     $32, DX
	ORQ      DX, R11
	MOVOU    48(BX), X3
	PCMPEQB  X0, X3
	PMOVMSKB X3, DX
	SHLQ     $48, DX
	ORQ      DX, R11
	RORQ     CX, R11

fgather:
	XORL R13, R13

fgatherloop:
	TESTQ R11, R11
	JEQ   finsert
	TESTL R14, R14
	JEQ   finsert
	BSFQ  R11, CX
	LEAQ  -1(R11), DX
	ANDQ  DX, R11
	ADDL  R10, CX
	ANDL  R12, CX
	TESTL CX, CX
	JEQ   fgatherloop
	MOVL  (AX)(CX*4), DX
	CMPL  DX, LOWLIM
	JB    finsert
	MOVL  DX, CX
	SUBL  R9, CX
	CMPQ  CX, DI
	JAE   fbad
	PREFETCHT0 (SI)(CX*1)
	MOVL  DX, 0(SP)(R13*4)
	INCL  R13
	DECL  R14
	JMP   fgatherloop

finsert:
	MOVBLZX (BX), CX
	DECL    CX
	ANDL    R12, CX
	CMOVLEQ R12, CX
	MOVB    CX, (BX)
	MOVL    TAG, DX
	MOVB    DX, (BX)(CX*1)
	MOVQ    P_NEXT, R15     // (p is not needed any more)
	MOVL    (R15), DX
	MOVL    DX, (AX)(CX*4)
	INCL    (R15)           // nextToUpdate++

	MOVL $3, R10
	XORL R11, R11
	XORL R12, R12
	LEAQ (SI)(DI*1), BX
	MOVQ R8, R15
	SUBQ DI, R15

fcmploop:
	CMPQ R12, R13
	JAE  fdone
	MOVL 0(SP)(R12*4), DX
	INCQ R12
	MOVL DX, CX
	SUBL R9, CX
	LEAQ (SI)(CX*1), AX
	MOVL -3(AX)(R10*1), R14
	CMPL R14, -3(BX)(R10*1)
	JNE  fcmploop
	XORL R14, R14

fcnt8:
	LEAQ 8(R14), CX
	CMPQ CX, R15
	JA   fcnttail
	MOVQ (AX)(R14*1), CX
	XORQ (BX)(R14*1), CX
	JNE  fcntdiff
	ADDQ $8, R14
	JMP  fcnt8

fcntdiff:
	BSFQ CX, CX
	SHRQ $3, CX
	ADDQ CX, R14
	JMP  fcntdone

fcnttail:
	CMPQ    R14, R15
	JAE     fcntdone
	MOVBLZX (AX)(R14*1), CX
	MOVBLZX (BX)(R14*1), R8
	CMPL    CX, R8
	JNE     fcntdone
	INCQ    R14
	JMP     fcnttail

fcntdone:
	CMPQ R14, R10
	JLE  fcmploop
	MOVQ R14, R10
	MOVL DX, R11
	CMPQ R14, R15
	JNE  fcmploop

fdone:
	MOVQ R10, ml+32(FP)
	CMPQ R10, $3
	JLE  fnone
	MOVL CURR, AX
	SUBL R11, AX
	ADDL $3, AX             // offsetToOffBase(curr - matchIndex)
	MOVL AX, offBase+40(FP)
	RET

fnone:
	MOVL $999999999, offBase+40(FP) // offBaseNotFound
	RET

fbad:
	MOVQ $-1, ml+32(FP)
	MOVL $0, offBase+40(FP)
	RET
