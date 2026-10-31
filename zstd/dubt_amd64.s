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

// TREEWALK: the smaller/larger step shared by the optimal parser's walks
// (as in insertDUBT1Asm): DX matchLength, CX match position. Continues at
// loop, or ends at end.
#define TREEWALK(btLow, loop, end, smallstop, larger, largestop) \
	LEAQ  (SI)(CX*1), AX; \
	MOVBLZX (AX)(DX*1), AX; \
	CMPB  AX, (BX)(DX*1); \
	JAE   larger; \
	MOVL  R14, (R12); \
	MOVQ  DX, R10; \
	CMPL  R14, btLow; \
	JLS   smallstop; \
	MOVQ  8(SP), AX; \
	LEAQ  4(AX), R12; \
	MOVL  4(AX), R14; \
	DECL  R15; \
	JMP   loop; \
smallstop: \
	LEAQ  0(SP), R12; \
	JMP   end; \
larger: \
	MOVL  R14, (R13); \
	MOVQ  DX, R11; \
	CMPL  R14, btLow; \
	JLS   largestop; \
	MOVQ  8(SP), R13; \
	MOVL  (R13), R14; \
	DECL  R15; \
	JMP   loop; \
largestop: \
	LEAQ  0(SP), R13

// func insertBt1Asm(bt *uint32, b *byte, ip, iend int, curr, baseIdx, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, matchEndIdx uint32)
//
// C: zstd_opt.c:ZSTD_insertBt1 (extDict 0), the tree walk.
// Extra frame: 16(SP) bestLength, 24(SP) matchEndIdx.
TEXT ·insertBt1Asm(SB), NOSPLIT, $32-76
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
	MOVL matchIndex+56(FP), R14
	MOVL nbCompares+44(FP), R15
	MOVQ $8, 16(SP)             // bestLength
	MOVL curr+32(FP), AX
	ADDL $9, AX
	MOVL AX, 24(SP)             // matchEndIdx = curr + 8 + 1

bloop:
	TESTL R15, R15
	JEQ   bend
	CMPL  R14, windowLow+52(FP)
	JB    bend
	MATCHLEN(btMask+40(FP), baseIdx+36(FP), bbad, bcnt8, bcdiff, bctail, bcdone)
	CMPQ  DX, 16(SP)
	JLE   bnotbetter
	MOVQ  DX, 16(SP)
	MOVL  24(SP), AX
	SUBL  R14, AX
	CMPQ  DX, AX
	JLS   bnotbetter
	LEAL  (R14)(DX*1), AX
	MOVL  AX, 24(SP)

bnotbetter:
	LEAQ  (R8)(DX*1), AX
	CMPQ  AX, R9
	JEQ   bend                  // equal: no way to know if inf or sup
	TREEWALK(btLow+48(FP), bloop, bend, bsmallstop, blarger, blargestop)

bend:
	MOVL $0, (R13)
	MOVL $0, (R12)
	MOVQ 16(SP), AX
	MOVQ AX, bestLength+64(FP)
	MOVL 24(SP), AX
	MOVL AX, matchEndIdx+72(FP)
	RET

bbad:
	MOVL $0, matchEndIdx+72(FP)
	RET

// Fields of btAsmParams (see dubt_generic.go; TestBtAsmParamsLayout).
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
// C: zstd_opt.c:ZSTD_insertBtAndGetAllMatches (noDict, mls 4 to 6).
// Registers as in the functions above. Frame: 0(SP) dummy32, 8(SP)
// nextPtr, 16(SP) bestLength, 24(SP) matchEndIdx, 28(SP) mnum, 32(SP)
// curr, 36(SP) btLow, 40(SP) matchLow, 44(SP) windowLow, 48(SP) hash,
// 56(SP) p, 64(SP) btMask, 68(SP) baseIdx, 72(SP) the first candidate.
//
// Bounds: btGetAllNoDict checks 0 < ip, ip + 8 <= iLimit <= len(b) and
// lowestValid <= curr; btAsmSetup checks the table sizes and lowestValid >=
// baseIdx. So windowLow (>= lowestValid) and matchLow lie in [baseIdx,
// curr]; a repcode is read only at an index in [windowLow, curr), and a
// tree candidate only at one >= matchLow and (MATCHLEN) before ip.
TEXT ·btGetAllNoDictAsm(SB), NOSPLIT, $80-37
	MOVQ p+0(FP), AX
	MOVQ AX, 56(SP)
	MOVQ BP_BT(AX), DI
	MOVQ BP_B(AX), SI
	MOVQ ip+8(FP), R8
	MOVQ BP_ILIMIT(AX), R9
	LEAQ (SI)(R8*1), BX
	MOVL BP_BTMASK(AX), CX
	MOVL CX, 64(SP)
	MOVL BP_BASEIDX(AX), CX
	MOVL CX, 68(SP)
	ADDL R8, CX
	MOVL CX, 32(SP)             // curr

	// hash, and the first candidate
	MOVQ  (BX), DX
	IMULQ BP_PRIME(AX), DX
	MOVQ  BP_HSHIFT(AX), CX
	SHRQ  CX, DX
	MOVQ  DX, 48(SP)
	MOVQ  BP_HASH(AX), CX
	MOVL  (CX)(DX*4), CX
	MOVL  CX, 72(SP)

	// btLow = btMask < curr ? curr - btMask : 0
	XORL    DX, DX
	MOVL    32(SP), CX
	SUBL    64(SP), CX
	CMOVLHI CX, DX
	MOVL    DX, 36(SP)

	// windowLow = isDictionary ? lowestValid : withinWindow; matchLow = windowLow or 1
	MOVL BP_LOWEST(AX), DX
	MOVL 32(SP), CX
	SUBL DX, CX
	CMPL CX, BP_MAXDIST(AX)
	JLS  wlok
	MOVL 32(SP), DX
	SUBL BP_MAXDIST(AX), DX

wlok:
	CMPL BP_ISDICT(AX), $0
	JEQ  wlset
	MOVL BP_LOWEST(AX), DX

wlset:
	MOVL    DX, 44(SP)
	MOVL    $1, CX
	TESTL   DX, DX
	CMOVLNE DX, CX
	MOVL    CX, 40(SP)

	MOVL     $0, 28(SP)         // mnum
	MOVL     lengthToBeat+28(FP), CX
	DECQ     CX                 // (the 32-bit load zero-extends; lengthToBeat >= 1)
	MOVQ     CX, 16(SP)         // bestLength
	MOVL     32(SP), CX
	ADDL     $9, CX
	MOVL     CX, 24(SP)         // matchEndIdx = curr + 8 + 1
	MOVB     $0, bad+36(FP)

	// repcodes: for repCode in [ll0, ll0 + 3)
	MOVL ll0+24(FP), R10
	LEAL 3(R10), R11

reploop:
	CMPL R10, R11
	JAE  repdone
	MOVQ rep+16(FP), CX
	CMPL R10, $3
	JNE  repn
	MOVL (CX), DX
	DECL DX                     // rep[0] - 1
	JMP  repgot

repn:
	MOVL (CX)(R10*4), DX

repgot:
	// if repOffset-1 < curr-dictLimit (discards 0 and -1) && repIndex >= windowLow
	LEAL -1(DX), CX
	MOVL 32(SP), R12
	SUBL BP_DICTLIMIT(AX), R12
	CMPL CX, R12
	JAE  repnext
	MOVL 32(SP), CX
	SUBL DX, CX
	CMPL CX, 44(SP)
	JB   repnext
	MOVQ R8, R12
	SUBQ DX, R12                // ip - repOffset (DX zero-extended)
	MOVL (SI)(R12*1), CX
	CMPL CX, (BX)
	JNE  repnext
	// repLen = count(ip+4, ip+4-repOffset, iLimit) + 4
	LEAQ 4(SI)(R12*1), R12
	MOVQ R9, R15
	SUBQ R8, R15
	SUBQ $4, R15
	XORL R13, R13

rc8:
	LEAQ 8(R13), CX
	CMPQ CX, R15
	JA   rctail
	MOVQ (R12)(R13*1), CX
	XORQ 4(BX)(R13*1), CX
	JNE  rcdiff
	ADDQ $8, R13
	JMP  rc8

rcdiff:
	BSFQ CX, CX
	SHRQ $3, CX
	ADDQ CX, R13
	JMP  rcdone

rctail:
	CMPQ    R13, R15
	JAE     rcdone
	MOVBLZX (R12)(R13*1), CX
	CMPB    CX, 4(BX)(R13*1)
	JNE     rcdone
	INCQ    R13
	JMP     rctail

rcdone:
	ADDQ $4, R13                // repLen
	CMPQ R13, 16(SP)
	JLE  repnext
	MOVQ R13, 16(SP)
	MOVL 28(SP), CX
	MOVQ BP_MATCHES(AX), R12
	LEAQ (R12)(CX*8), R12
	MOVL R10, DX
	SUBL ll0+24(FP), DX
	INCL DX
	MOVL DX, (R12)              // off = repCode - ll0 + 1
	MOVL R13, 4(R12)            // len
	INCL 28(SP)
	MOVL BP_SUFF(AX), CX
	CMPQ R13, CX
	JA   repret                 // repLen > sufficientLen: best possible
	LEAQ (R8)(R13*1), CX
	CMPQ CX, R9
	JEQ  repret

repnext:
	INCL R10
	JMP  reploop

repret:
	MOVL 28(SP), CX
	MOVL CX, mnum+32(FP)
	RET

repdone:
	// hashTable[h] = curr
	MOVQ BP_HASH(AX), CX
	MOVQ 48(SP), DX
	MOVL 32(SP), R12
	MOVL R12, (CX)(DX*4)

	XORL R10, R10
	XORL R11, R11
	MOVL 32(SP), CX
	ANDL 64(SP), CX
	LEAQ (DI)(CX*8), R12        // smallerPtr = &bt[2*(curr&btMask)]
	LEAQ 4(R12), R13            // largerPtr
	MOVL 72(SP), R14
	MOVL BP_NBCMP(AX), R15

gloop:
	TESTL R15, R15
	JEQ   gend
	CMPL  R14, 40(SP)
	JB    gend                  // matchIndex < matchLow
	MATCHLEN(64(SP), 68(SP), gbad, gcnt8, gcdiff, gctail, gcdone)
	CMPQ  DX, 16(SP)
	JLE   gnext
	MOVL  24(SP), AX
	SUBL  R14, AX
	CMPQ  DX, AX
	JLS   gme
	LEAL  (R14)(DX*1), AX
	MOVL  AX, 24(SP)

gme:
	MOVQ  DX, 16(SP)            // bestLength
	MOVL  28(SP), AX
	CMPL  AX, $4099             // optSize
	JAE   gbad
	MOVQ  56(SP), CX
	MOVQ  BP_MATCHES(CX), CX
	LEAQ  (CX)(AX*8), CX
	MOVL  DX, 4(CX)             // len
	MOVL  32(SP), AX
	SUBL  R14, AX
	ADDL  $3, AX
	MOVL  AX, (CX)              // off = offsetToOffBase(curr - matchIndex)
	INCL  28(SP)
	MOVL  R14, CX
	SUBL  68(SP), CX            // re-derive the match position
	CMPQ  DX, $4096
	JA    gend                  // matchLength > optNum
	LEAQ  (R8)(DX*1), AX
	CMPQ  AX, R9
	JEQ   gend                  // equal: no way to know if inf or sup

gnext:
	TREEWALK(36(SP), gloop, gend, gsmallstop, glarger, glargestop)

gend:
	MOVL $0, (R13)
	MOVL $0, (R12)
	MOVQ 56(SP), AX
	MOVQ BP_NEXT(AX), CX
	MOVL 24(SP), DX
	SUBL $8, DX
	MOVL DX, (CX)               // nextToUpdate = matchEndIdx - 8
	MOVL 28(SP), AX
	MOVL AX, mnum+32(FP)
	RET

gbad:
	MOVB $1, bad+36(FP)
	RET

// func dubtFindBestMatchAsm(p *btAsmParams, ip int) (bestLength int, offBase uint32, bad bool)
//
// C: zstd_lazy.c:ZSTD_updateDUBT + ZSTD_DUBT_findBestMatch (noDict): insert
// the positions up to ip as unsorted candidates, sort the unsorted ones of
// ip's hash (insertDUBT1Asm), then search (dubtSearchAsm). The two loops are
// called with their stack arguments at 0(SP) (insertDUBT1Asm: 0-57,
// dubtSearchAsm: 0-80). Locals: 96(SP) p, 104(SP) curr, 108(SP)
// unsortLimit, 112(SP) hash, 120(SP) nbCandidates, 124(SP) windowLow,
// 128(SP) btLow, 132(SP) the next candidate to sort.
//
// Bounds: dubtFindBestMatchFast checks 0 < ip, ip + 8 <= iLimit, baseIdx
// <= nextToUpdate and lowestValid <= curr; btAsmSetup the table sizes and
// lowestValid >= baseIdx. The inserted positions are in [nextToUpdate, ip),
// so their 8 hashed bytes lie in b[:ip+8]. A candidate sorted here is >
// unsortLimit >= windowLow >= baseIdx, and must be < curr (else bad), so
// it satisfies insertDUBT1Asm's preconditions with windowLow computed as
// insertDUBT1 does (>= lowestValid >= baseIdx).
TEXT ·dubtFindBestMatchAsm(SB), NOSPLIT, $144-29
	MOVQ p+0(FP), R15
	MOVQ R15, 96(SP)
	MOVB $0, bad+28(FP)
	MOVQ BP_B(R15), SI
	MOVL BP_BASEIDX(R15), BX
	MOVQ SI, R9
	SUBQ BX, R9                 // b - baseIdx: an index addresses b directly
	MOVQ ip+8(FP), R8
	ADDL R8, BX
	MOVL BX, 104(SP)            // curr
	MOVQ BP_HASH(R15), R10
	MOVQ BP_BT(R15), R11
	MOVL BP_BTMASK(R15), R13
	MOVQ BP_PRIME(R15), R14
	MOVQ BP_HSHIFT(R15), CX

	// updateDUBT: [nextToUpdate, curr) as unsorted candidates
	MOVQ BP_NEXT(R15), AX
	MOVL (AX), DX
	JMP  uck

uloop:
	MOVQ  (R9)(DX*1), AX
	IMULQ R14, AX
	SHRQ  CX, AX
	MOVL  (R10)(AX*4), R12
	MOVL  DX, (R10)(AX*4)
	MOVL  DX, AX
	ANDL  R13, AX
	MOVL  R12, (R11)(AX*8)      // bt[2*(idx&btMask)] = matchIndex
	MOVL  $1, 4(R11)(AX*8)      // dubtUnsortedMark
	INCL  DX

uck:
	CMPL DX, BX
	JB   uloop
	MOVQ BP_NEXT(R15), AX
	MOVL BX, (AX)               // nextToUpdate = curr

	MOVQ  (SI)(R8*1), AX
	IMULQ R14, AX
	SHRQ  CX, AX
	MOVQ  AX, 112(SP)
	MOVL  (R10)(AX*4), DX       // matchIndex

	// windowLow (getLowestMatchIndex), btLow, unsortLimit = max(btLow, windowLow)
	MOVL BP_LOWEST(R15), AX
	MOVL BX, CX
	SUBL AX, CX
	CMPL CX, BP_MAXDIST(R15)
	JLS  wlok
	MOVL BX, AX
	SUBL BP_MAXDIST(R15), AX

wlok:
	CMPL BP_ISDICT(R15), $0
	JEQ  wlset
	MOVL BP_LOWEST(R15), AX

wlset:
	MOVL    AX, 124(SP)
	XORL    CX, CX
	SUBL    R13, BX
	CMOVLHI BX, CX
	MOVL    CX, 128(SP)
	CMPL    CX, AX
	CMOVLLS AX, CX
	MOVL    CX, 108(SP)

	// reach the end of the unsorted candidates, reversing the chain
	MOVL BP_NBCMP(R15), R12     // nbCandidates
	XORL BX, BX                 // previousCandidate

chain:
	CMPL DX, CX
	JLS  chaindone
	MOVL DX, AX
	ANDL R13, AX
	CMPL 4(R11)(AX*8), $1
	JNE  chaindone
	CMPL R12, $1
	JLS  nullify
	MOVL BX, 4(R11)(AX*8)
	MOVL DX, BX
	MOVL (R11)(AX*8), DX
	DECL R12
	JMP  chain

nullify:
	// the last candidate is still unsorted: drop it
	MOVQ $0, (R11)(AX*8)

chaindone:
	MOVL R12, 120(SP)
	MOVL BX, 132(SP)

batch:
	// sort the stacked candidates, oldest first
	MOVL  132(SP), DX
	TESTL DX, DX
	JEQ   search
	MOVQ  96(SP), R15
	MOVQ  BP_BT(R15), R11
	MOVL  BP_BTMASK(R15), R13
	MOVL  DX, AX
	ANDL  R13, AX
	MOVL  4(R11)(AX*8), CX
	MOVL  CX, 132(SP)
	CMPL  DX, 104(SP)
	JAE   bad
	MOVQ  R11, 0(SP)            // bt
	MOVQ  BP_B(R15), AX
	MOVQ  AX, 8(SP)             // b
	MOVL  DX, AX
	SUBL  BP_BASEIDX(R15), AX
	MOVQ  AX, 16(SP)            // ip
	MOVQ  BP_ILIMIT(R15), AX
	MOVQ  AX, 24(SP)            // iend
	MOVL  DX, 32(SP)            // curr
	MOVL  BP_BASEIDX(R15), AX
	MOVL  AX, 36(SP)            // baseIdx
	MOVL  R13, 40(SP)           // btMask
	MOVL  120(SP), AX
	MOVL  AX, 44(SP)            // nbCompares = nbCandidates
	MOVL  108(SP), AX
	MOVL  AX, 48(SP)            // btLow = unsortLimit
	MOVL  BP_LOWEST(R15), AX    // windowLow, as insertDUBT1
	MOVL  DX, CX
	SUBL  AX, CX
	CMPL  CX, BP_MAXDIST(R15)
	JLS   bwlok
	MOVL  DX, AX
	SUBL  BP_MAXDIST(R15), AX

bwlok:
	MOVL AX, 52(SP)
	CALL ·insertDUBT1Asm(SB)
	CMPB 56(SP), $0
	JEQ  bad
	INCL 120(SP)
	JMP  batch

search:
	// find the longest match: matchIndex = hashTable[h]; hashTable[h] = curr
	MOVQ 96(SP), R15
	MOVQ BP_HASH(R15), AX
	MOVQ 112(SP), CX
	MOVL (AX)(CX*4), DX
	MOVL 104(SP), BX
	MOVL BX, (AX)(CX*4)
	MOVQ BP_BT(R15), AX
	MOVQ AX, 0(SP)
	MOVQ BP_B(R15), AX
	MOVQ AX, 8(SP)
	MOVQ ip+8(FP), AX
	MOVQ AX, 16(SP)
	MOVQ BP_ILIMIT(R15), AX
	MOVQ AX, 24(SP)
	MOVL BX, 32(SP)             // curr
	MOVL BP_BASEIDX(R15), AX
	MOVL AX, 36(SP)
	MOVL BP_BTMASK(R15), AX
	MOVL AX, 40(SP)
	MOVL BP_NBCMP(R15), AX
	MOVL AX, 44(SP)
	MOVL 128(SP), AX
	MOVL AX, 48(SP)             // btLow
	MOVL 124(SP), AX
	MOVL AX, 52(SP)             // windowLow
	MOVL DX, 56(SP)             // matchIndex
	CALL ·dubtSearchAsm(SB)
	MOVL  76(SP), AX            // matchEndIdx
	TESTL AX, AX
	JEQ   bad
	SUBL  $8, AX
	MOVQ  96(SP), R15
	MOVQ  BP_NEXT(R15), CX
	MOVL  AX, (CX)              // nextToUpdate = matchEndIdx - 8 (skip repetitive patterns)
	MOVQ  64(SP), AX
	MOVQ  AX, bestLength+16(FP)
	MOVL  72(SP), AX
	MOVL  AX, offBase+24(FP)
	RET

bad:
	MOVB $1, bad+28(FP)
	RET
