//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// The extDict binary-tree loops (insertDUBT1ExtDict, the search of
// dubtFindBestMatchExtDict). Shared registers: DI bt, SI b - baseIdx and R8
// d - dictBaseIdx (an index below dictLimit addresses the extDict from R8,
// any other the prefix from SI; no calls, so the GC never sees them), BX
// the address of the position inserted or searched (ip), R9 the end of its
// segment, R10 commonLengthSmaller, R11 commonLengthLarger, R12 smallerPtr,
// R13 largerPtr, R14 matchIndex, R15 nbCompares, AX/CX/DX scratch.
// Frame: 0 dummy32, 8 nextPtr, 16 the count's limit, 24 the address of
// dictEnd, 32 of prefixStart.
//
// BOUNDS (the Go callers check: baseIdx <= dictLimit, dictBaseIdx <=
// windowLow, dictLimit - dictBaseIdx <= len(d), iend <= len(b); the
// position is below its segment's end):
// (1) Candidates: windowLow < matchIndex < curr (else bad); one below
//     dictLimit lies in d[windowLow-dictBaseIdx:dictEnd], any other in
//     b[dictLimit-baseIdx:] below the position.
// (2) Counts (MATCHLENX): the input from the position up to its segment's
//     end (iend, or dictEnd for a position in the extDict); the match
//     byte by byte with it, up to dictEnd for an extDict match, which then
//     goes on at prefixStart (C: ZSTD_count_2segments). A match reaching
//     the position's end stops the walk before any other read.
// (3) The byte after the common part (C: match[matchLength]) is read in
//     the segment of matchIndex + matchLength, below dictEnd or below the
//     position's index + matchLength < its end. The searches end on a
//     longest-so-far match reaching the end; one that is not the longest
//     cannot reach it (the longest would have), else bad.

// CNT(cnt8, cdiff, ctail, cdone): DX += the common prefix length of
// CX[DX:] and BX[DX:], up to the limit 16(SP) for DX.
#define CNT(cnt8, cdiff, ctail, cdone) \
cnt8: \
	LEAQ  8(DX), AX; \
	CMPQ  AX, 16(SP); \
	JA    ctail; \
	MOVQ  (CX)(DX*1), AX; \
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
	CMPQ  DX, 16(SP); \
	JAE   cdone; \
	MOVBLZX (CX)(DX*1), AX; \
	CMPB  AX, (BX)(DX*1); \
	JNE   cdone; \
	INCQ  DX; \
	JMP   ctail; \
cdone:

// MATCHLENX: DX = min(cls, cll) + the common length of the candidate at
// matchIndex (R14) with the position, over both segments (see BOUNDS);
// 8(SP) = &bt[2*(matchIndex&btMask)]. Jumps to bad if the candidate is not
// before the position (curr).
#define MATCHLENX(btMask, curr, dictLimit, bad, pfx, cnt, done, a8, ad, at, ae, b8, bd, bk, be) \
	MOVL    R14, AX; \
	ANDL    btMask, AX; \
	LEAQ    (DI)(AX*8), AX; \
	MOVQ    AX, 8(SP); \
	MOVQ    R10, DX; \
	CMPQ    R11, DX; \
	CMOVQLT R11, DX; \
	CMPL    R14, curr; \
	JAE     bad; \
	MOVQ    R9, AX; \
	SUBQ    BX, AX; \
	MOVQ    AX, 16(SP); \
	LEAL    (R14)(DX*1), AX; \
	CMPL    AX, dictLimit; \
	JAE     pfx; \
	LEAQ    (R8)(R14*1), CX; \
	MOVQ    24(SP), AX; \
	SUBQ    CX, AX; \
	CMPQ    AX, 16(SP); \
	JAE     cnt; \
	MOVQ    AX, 16(SP); \
	CNT(a8, ad, at, ae); \
	CMPQ    DX, 16(SP); \
	JNE     done; \
	MOVQ    32(SP), CX; \
	SUBQ    DX, CX; \
	MOVQ    R9, AX; \
	SUBQ    BX, AX; \
	MOVQ    AX, 16(SP); \
	JMP     cnt; \
pfx: \
	LEAQ    (SI)(R14*1), CX; \
cnt: \
	CNT(b8, bd, bk, be); \
done:

// MBYTE: AX = the candidate's byte at matchLength (DX), from the segment
// of matchIndex + matchLength (C: match[matchLength]).
#define MBYTE(dictLimit) \
	LEAL    (R14)(DX*1), AX; \
	LEAQ    (SI)(AX*1), CX; \
	CMPL    AX, dictLimit; \
	LEAQ    (R8)(AX*1), AX; \
	CMOVQCC CX, AX; \
	MOVBLZX (AX), AX

// func insertDUBT1ExtAsm(bt *uint32, b, d *byte, iend int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, windowLow uint32) bool
//
// C: zstd_lazy.c:ZSTD_insertDUBT1 (extDict); see insertDUBT1ExtDict.
TEXT ·insertDUBT1ExtAsm(SB), NOSPLIT, $40-65
	MOVQ bt+0(FP), DI
	MOVQ b+8(FP), SI
	MOVL baseIdx+36(FP), AX
	SUBQ AX, SI                 // b - baseIdx
	MOVQ d+16(FP), R8
	MOVL dictBaseIdx+40(FP), AX
	SUBQ AX, R8                 // d - dictBaseIdx
	MOVL dictLimit+44(FP), AX
	LEAQ (R8)(AX*1), CX
	MOVQ CX, 24(SP)             // the address of dictEnd
	LEAQ (SI)(AX*1), CX
	MOVQ CX, 32(SP)             // the address of prefixStart
	// the position and its segment's end: in the extDict below dictLimit
	MOVL    curr+32(FP), AX
	LEAQ    (SI)(AX*1), BX
	MOVQ    b+8(FP), R9
	ADDQ    iend+24(FP), R9
	CMPL    AX, dictLimit+44(FP)
	JAE     ipset
	LEAQ    (R8)(AX*1), BX
	MOVQ    24(SP), R9

ipset:
	XORL R10, R10
	XORL R11, R11
	MOVL curr+32(FP), AX
	ANDL btMask+48(FP), AX
	LEAQ (DI)(AX*8), R12        // smallerPtr = &bt[2*(curr&btMask)]
	LEAQ 4(R12), R13            // largerPtr
	MOVL (R12), R14             // matchIndex = *smallerPtr
	MOVL nbCompares+52(FP), R15
	MOVB $1, ret+64(FP)

iloop:
	TESTL R15, R15
	JEQ   iend
	CMPL  R14, windowLow+60(FP)
	JLS   iend
	MATCHLENX(btMask+48(FP), curr+32(FP), dictLimit+44(FP), ibad, ipfx, icnt, idone, ia8, iad, iat, iae, ib8, ibd, ibk, ibe)
	LEAQ  (BX)(DX*1), AX
	CMPQ  AX, R9
	JEQ   iend                  // equal: no way to know if inf or sup
	MBYTE(dictLimit+44(FP))
	CMPB  AX, (BX)(DX*1)
	JAE   ilarger
	// match is smaller than current
	MOVL  R14, (R12)
	MOVQ  DX, R10
	CMPL  R14, btLow+56(FP)
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
	CMPL R14, btLow+56(FP)
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
	MOVB $0, ret+64(FP)
	RET

// func dubtSearchExtAsm(bt *uint32, b, d *byte, iend int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, offBase, matchEndIdx uint32)
//
// C: zstd_lazy.c:ZSTD_DUBT_findBestMatch, "find longest match" (extDict);
// see dubtFindBestMatchExtDict. The position is in the prefix.
// Extra frame: 40 bestLength, 48 offBase, 52 matchEndIdx.
TEXT ·dubtSearchExtAsm(SB), NOSPLIT, $56-88
	MOVQ bt+0(FP), DI
	MOVQ b+8(FP), SI
	MOVL baseIdx+36(FP), AX
	SUBQ AX, SI                 // b - baseIdx
	MOVQ d+16(FP), R8
	MOVL dictBaseIdx+40(FP), AX
	SUBQ AX, R8                 // d - dictBaseIdx
	MOVL dictLimit+44(FP), AX
	LEAQ (R8)(AX*1), CX
	MOVQ CX, 24(SP)             // the address of dictEnd
	LEAQ (SI)(AX*1), CX
	MOVQ CX, 32(SP)             // the address of prefixStart
	MOVL curr+32(FP), AX
	LEAQ (SI)(AX*1), BX
	MOVQ b+8(FP), R9
	ADDQ iend+24(FP), R9
	XORL R10, R10
	XORL R11, R11
	ANDL btMask+48(FP), AX
	LEAQ (DI)(AX*8), R12
	LEAQ 4(R12), R13
	MOVL matchIndex+64(FP), R14
	MOVL nbCompares+52(FP), R15
	MOVQ $0, 40(SP)
	MOVL $999999999, 48(SP)     // offBaseNotFound
	MOVL curr+32(FP), AX
	ADDL $9, AX
	MOVL AX, 52(SP)             // matchEndIdx = curr + 8 + 1

sloop:
	TESTL R15, R15
	JEQ   send
	CMPL  R14, windowLow+60(FP)
	JLS   send
	MATCHLENX(btMask+48(FP), curr+32(FP), dictLimit+44(FP), sbad, spfx, scnt, sdone, sa8, sad, sat, sae, sb8, sbd, sbk, sbe)
	CMPQ  DX, 40(SP)
	JLE   snotbetter
	// if matchLength > matchEndIdx - matchIndex: matchEndIdx = matchIndex + matchLength
	MOVL  52(SP), AX
	SUBL  R14, AX
	CMPQ  DX, AX
	JLS   sendok
	LEAL  (R14)(DX*1), AX
	MOVL  AX, 52(SP)

sendok:
	// if 4*(matchLength-bestLength) > highbit32(curr-matchIndex+1) - highbit32(offBase)
	MOVL  curr+32(FP), AX
	SUBL  R14, AX
	INCL  AX
	BSRL  AX, AX
	BSRL  48(SP), CX
	SUBQ  CX, AX
	MOVQ  DX, CX
	SUBQ  40(SP), CX
	SHLQ  $2, CX
	CMPQ  CX, AX
	JLE   snobest
	MOVQ  DX, 40(SP)
	MOVL  curr+32(FP), AX
	SUBL  R14, AX
	ADDL  $3, AX
	MOVL  AX, 48(SP)

snobest:
	LEAQ  (BX)(DX*1), AX
	CMPQ  AX, R9
	JEQ   send

snotbetter:
	LEAQ  (BX)(DX*1), AX
	CMPQ  AX, R9
	JEQ   sbad                  // would read past the input (impossible: see BOUNDS)
	MBYTE(dictLimit+44(FP))
	CMPB  AX, (BX)(DX*1)
	JAE   slarger
	MOVL  R14, (R12)
	MOVQ  DX, R10
	CMPL  R14, btLow+56(FP)
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
	CMPL R14, btLow+56(FP)
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
	MOVQ 40(SP), AX
	MOVQ AX, bestLength+72(FP)
	MOVL 48(SP), AX
	MOVL AX, offBase+80(FP)
	MOVL 52(SP), AX
	MOVL AX, matchEndIdx+84(FP)
	RET

sbad:
	MOVL $0, matchEndIdx+84(FP)
	RET

// func insertBt1ExtAsm(bt *uint32, b, d *byte, iend int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, matchEndIdx uint32)
//
// C: zstd_opt.c:ZSTD_insertBt1 (extDict), the tree walk; see
// insertBt1ExtDict. The position is in the prefix. Candidates are >=
// windowLow (the BOUNDS above hold the same). Extra frame: 40 bestLength,
// 48 matchEndIdx.
TEXT ·insertBt1ExtAsm(SB), NOSPLIT, $56-84
	MOVQ bt+0(FP), DI
	MOVQ b+8(FP), SI
	MOVL baseIdx+36(FP), AX
	SUBQ AX, SI                 // b - baseIdx
	MOVQ d+16(FP), R8
	MOVL dictBaseIdx+40(FP), AX
	SUBQ AX, R8                 // d - dictBaseIdx
	MOVL dictLimit+44(FP), AX
	LEAQ (R8)(AX*1), CX
	MOVQ CX, 24(SP)             // the address of dictEnd
	LEAQ (SI)(AX*1), CX
	MOVQ CX, 32(SP)             // the address of prefixStart
	MOVL curr+32(FP), AX
	LEAQ (SI)(AX*1), BX
	MOVQ b+8(FP), R9
	ADDQ iend+24(FP), R9
	XORL R10, R10
	XORL R11, R11
	ANDL btMask+48(FP), AX
	LEAQ (DI)(AX*8), R12
	LEAQ 4(R12), R13
	MOVL matchIndex+64(FP), R14
	MOVL nbCompares+52(FP), R15
	MOVQ $8, 40(SP)             // bestLength
	MOVL curr+32(FP), AX
	ADDL $9, AX
	MOVL AX, 48(SP)             // matchEndIdx = curr + 8 + 1

tloop:
	TESTL R15, R15
	JEQ   tend
	CMPL  R14, windowLow+60(FP)
	JCS   tend
	MATCHLENX(btMask+48(FP), curr+32(FP), dictLimit+44(FP), tbad, tpfx, tcnt, tdone, ta8, tad, tat, tae, tb8, tbd, tbk, tbe)
	CMPQ  DX, 40(SP)
	JLE   tnotbetter
	MOVQ  DX, 40(SP)
	MOVL  48(SP), AX
	SUBL  R14, AX
	CMPQ  DX, AX
	JLS   tnotbetter
	LEAL  (R14)(DX*1), AX
	MOVL  AX, 48(SP)

tnotbetter:
	LEAQ  (BX)(DX*1), AX
	CMPQ  AX, R9
	JEQ   tend                  // equal: no way to know if inf or sup
	MBYTE(dictLimit+44(FP))
	CMPB  AX, (BX)(DX*1)
	JAE   tlarger
	MOVL  R14, (R12)
	MOVQ  DX, R10
	CMPL  R14, btLow+56(FP)
	JLS   tsmallstop
	MOVQ  8(SP), AX
	LEAQ  4(AX), R12
	MOVL  4(AX), R14
	DECL  R15
	JMP   tloop

tsmallstop:
	LEAQ 0(SP), R12
	JMP  tend

tlarger:
	MOVL R14, (R13)
	MOVQ DX, R11
	CMPL R14, btLow+56(FP)
	JLS  tlargestop
	MOVQ 8(SP), R13
	MOVL (R13), R14
	DECL R15
	JMP  tloop

tlargestop:
	LEAQ 0(SP), R13

tend:
	MOVL $0, (R13)
	MOVL $0, (R12)
	MOVQ 40(SP), AX
	MOVQ AX, bestLength+72(FP)
	MOVL 48(SP), AX
	MOVL AX, matchEndIdx+80(FP)
	RET

tbad:
	MOVL $0, matchEndIdx+80(FP)
	RET

// func btWalkExtAsm(bt *uint32, b, d *byte, matches *optMatch, iLimit int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, matchLow, matchIndex, mnum uint32, bestLength int) (mnumOut, matchEndIdx uint32, bad bool)
//
// C: zstd_opt.c:ZSTD_insertBtAndGetAllMatches (extDict), the tree walk
// after the repcodes and the hash3 lookup (see
// insertBtAndGetAllMatchesExtDict), appending to matches from mnum. The
// position is in the prefix. bad reports a candidate not before the
// position, or a full match table (both impossible). Extra frame: 40
// bestLength, 48 matchEndIdx, 52 mnum, 56 matches.
TEXT ·btWalkExtAsm(SB), NOSPLIT, $64-97
	MOVQ bt+0(FP), DI
	MOVQ b+8(FP), SI
	MOVL baseIdx+44(FP), AX
	SUBQ AX, SI                 // b - baseIdx
	MOVQ d+16(FP), R8
	MOVL dictBaseIdx+48(FP), AX
	SUBQ AX, R8                 // d - dictBaseIdx
	MOVL dictLimit+52(FP), AX
	LEAQ (R8)(AX*1), CX
	MOVQ CX, 24(SP)             // the address of dictEnd
	LEAQ (SI)(AX*1), CX
	MOVQ CX, 32(SP)             // the address of prefixStart
	MOVQ matches+24(FP), AX
	MOVQ AX, 56(SP)
	MOVL curr+40(FP), AX
	LEAQ (SI)(AX*1), BX
	MOVQ b+8(FP), R9
	ADDQ iLimit+32(FP), R9
	XORL R10, R10
	XORL R11, R11
	ANDL btMask+56(FP), AX
	LEAQ (DI)(AX*8), R12
	LEAQ 4(R12), R13
	MOVL matchIndex+72(FP), R14
	MOVL nbCompares+60(FP), R15
	MOVQ bestLength+80(FP), AX
	MOVQ AX, 40(SP)
	MOVL mnum+76(FP), AX
	MOVL AX, 52(SP)
	MOVL curr+40(FP), AX
	ADDL $9, AX
	MOVL AX, 48(SP)             // matchEndIdx = curr + 8 + 1
	MOVB $0, bad+96(FP)

wloop:
	TESTL R15, R15
	JEQ   wend
	CMPL  R14, matchLow+68(FP)
	JCS   wend
	MATCHLENX(btMask+56(FP), curr+40(FP), dictLimit+52(FP), wbad, wpfx, wcnt, wdone, wa8, wad, wat, wae, wb8, wbd, wbk, wbe)
	CMPQ  DX, 40(SP)
	JLE   wnotbetter
	MOVL  48(SP), AX
	SUBL  R14, AX
	CMPQ  DX, AX
	JLS   wendok
	LEAL  (R14)(DX*1), AX
	MOVL  AX, 48(SP)

wendok:
	MOVQ  DX, 40(SP)            // bestLength
	MOVL  52(SP), AX
	CMPL  AX, $4099             // optSize
	JAE   wbad
	MOVQ  56(SP), CX
	LEAQ  (CX)(AX*8), CX
	MOVL  curr+40(FP), AX
	SUBL  R14, AX
	ADDL  $3, AX
	MOVL  AX, (CX)              // off = offsetToOffBase(curr - matchIndex)
	MOVL  DX, 4(CX)             // len
	INCL  52(SP)
	CMPQ  DX, $4096             // optNum
	JGT   wend
	LEAQ  (BX)(DX*1), AX
	CMPQ  AX, R9
	JEQ   wend                  // equal: no way to know if inf or sup

wnotbetter:
	LEAQ  (BX)(DX*1), AX
	CMPQ  AX, R9
	JEQ   wbad                  // would read past the input (impossible: see BOUNDS)
	MBYTE(dictLimit+52(FP))
	CMPB  AX, (BX)(DX*1)
	JAE   wlarger
	MOVL  R14, (R12)
	MOVQ  DX, R10
	CMPL  R14, btLow+64(FP)
	JLS   wsmallstop
	MOVQ  8(SP), AX
	LEAQ  4(AX), R12
	MOVL  4(AX), R14
	DECL  R15
	JMP   wloop

wsmallstop:
	LEAQ 0(SP), R12
	JMP  wend

wlarger:
	MOVL R14, (R13)
	MOVQ DX, R11
	CMPL R14, btLow+64(FP)
	JLS  wlargestop
	MOVQ 8(SP), R13
	MOVL (R13), R14
	DECL R15
	JMP  wloop

wlargestop:
	LEAQ 0(SP), R13

wend:
	MOVL $0, (R13)
	MOVL $0, (R12)
	MOVL 52(SP), AX
	MOVL AX, mnumOut+88(FP)
	MOVL 48(SP), AX
	MOVL AX, matchEndIdx+92(FP)
	RET

wbad:
	MOVB $1, bad+96(FP)
	RET
