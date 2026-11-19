//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// The extDict binary-tree loops (insertDUBT1ExtDict, the search of
// dubtFindBestMatchExtDict, the walks of insertBt1ExtDict and
// insertBtAndGetAllMatchesExtDict), as zstd_lazy_dubt_ext_amd64.s.
// Shared registers: R0 bt, R1 b - baseIdx and R2 d - dictBaseIdx (an index
// below dictLimit addresses the extDict from R2, any other the prefix from
// R1; no calls, so the GC never sees them), R3 the address of the position
// inserted or searched (ip), R4 the end of its segment, R5
// commonLengthSmaller, R6 commonLengthLarger, R7 smallerPtr, R8 largerPtr,
// R9 matchIndex, R10 nbCompares, R11 btMask, R12 curr, R13 dictLimit, R19
// btLow, R20 windowLow (matchLow), R21 the address of dictEnd, R22 of
// prefixStart, R23 nextPtr, R24 the count's limit, R25 bestLength, R26
// matchEndIdx, R14-R17 scratch (R16 the match length). Frame: 8 dummy32,
// 16 offBase or mnum, 24 matches.
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

// CNT(cnt8, cdiff, ctail, cdone): R16 += the common prefix length of
// R15[R16:] and R3[R16:], up to the limit R24 for R16. Clobbers R14, R17.
#define CNT(cnt8, cdiff, ctail, cdone) \
cnt8: \
	ADD   $8, R16, R14; \
	CMP   R24, R14; \
	BHI   ctail; \
	MOVD  (R15)(R16), R14; \
	MOVD  (R3)(R16), R17; \
	EOR   R14, R17, R17; \
	CBNZ  R17, cdiff; \
	ADD   $8, R16; \
	B     cnt8; \
cdiff: \
	RBIT  R17, R17; \
	CLZ   R17, R17; \
	ADD   R17>>3, R16, R16; \
	B     cdone; \
ctail: \
	CMP   R24, R16; \
	BHS   cdone; \
	MOVBU (R15)(R16), R14; \
	MOVBU (R3)(R16), R17; \
	CMPW  R14, R17; \
	BNE   cdone; \
	ADD   $1, R16; \
	B     ctail; \
cdone:

// MATCHLENX: R16 = min(cls, cll) + the common length of the candidate at
// matchIndex (R9) with the position, over both segments (see BOUNDS);
// R23 = &bt[2*(matchIndex&btMask)]. Jumps to bad if the candidate is not
// before the position (curr).
#define MATCHLENX(bad, pfx, cnt, done, a8, ad, at, ae, b8, bd, bk, be) \
	ANDW  R11, R9, R14; \
	ADD   R14<<3, R0, R23; \
	CMP   R6, R5; \
	CSEL  LT, R5, R6, R16; \
	CMPW  R12, R9; \
	BHS   bad; \
	SUB   R3, R4, R24; \
	ADDW  R16, R9, R14; \
	CMPW  R13, R14; \
	BHS   pfx; \
	ADD   R9, R2, R15; \
	SUB   R15, R21, R14; \
	CMP   R24, R14; \
	BHS   cnt; \
	MOVD  R14, R24; \
	CNT(a8, ad, at, ae); \
	CMP   R24, R16; \
	BNE   done; \
	SUB   R16, R22, R15; \
	SUB   R3, R4, R24; \
	B     cnt; \
pfx: \
	ADD   R9, R1, R15; \
cnt: \
	CNT(b8, bd, bk, be); \
done:

// MBYTE: R14 = the candidate's byte at matchLength (R16), from the segment
// of matchIndex + matchLength (C: match[matchLength]); R17 = the
// position's byte there.
#define MBYTE \
	ADDW  R16, R9, R14; \
	CMPW  R13, R14; \
	CSEL  LO, R2, R1, R15; \
	MOVBU (R15)(R14), R14; \
	MOVBU (R3)(R16), R17

// SETUP(baseIdx, dictBaseIdx, dictLimit, curr, iend, btMask, nbCompares,
// btLow, windowLow): the shared registers from the arguments (the position
// in the prefix), smallerPtr and largerPtr at curr's pair.
#define SETUP(baseIdx, dictBaseIdx, dictLimit, curr, iend, btMask, nbCompares, btLow, windowLow) \
	MOVD  bt+0(FP), R0; \
	MOVD  b+8(FP), R1; \
	MOVWU baseIdx, R14; \
	SUB   R14, R1, R1; \
	MOVD  d+16(FP), R2; \
	MOVWU dictBaseIdx, R14; \
	SUB   R14, R2, R2; \
	MOVWU dictLimit, R13; \
	ADD   R13, R2, R21; \
	ADD   R13, R1, R22; \
	MOVWU curr, R12; \
	ADD   R12, R1, R3; \
	MOVD  b+8(FP), R4; \
	MOVD  iend, R14; \
	ADD   R14, R4; \
	MOVWU btMask, R11; \
	MOVWU nbCompares, R10; \
	MOVWU btLow, R19; \
	MOVWU windowLow, R20; \
	MOVD  ZR, R5; \
	MOVD  ZR, R6; \
	ANDW  R11, R12, R14; \
	ADD   R14<<3, R0, R7; \
	ADD   $4, R7, R8

// STEP(loop, end, smallstop, largestop, larger): C's "match is smaller /
// larger than current" step, after MBYTE: R14 the candidate's byte, R17
// the position's.
#define STEP(loop, end, smallstop, largestop, larger) \
	CMPW  R17, R14; \
	BHS   larger; \
	MOVW  R9, (R7); \
	MOVD  R16, R5; \
	CMPW  R19, R9; \
	BLS   smallstop; \
	ADD   $4, R23, R7; \
	MOVWU 4(R23), R9; \
	SUBW  $1, R10; \
	B     loop; \
smallstop: \
	ADD   $8, RSP, R7; \
	B     end; \
larger: \
	MOVW  R9, (R8); \
	MOVD  R16, R6; \
	CMPW  R19, R9; \
	BLS   largestop; \
	MOVD  R23, R8; \
	MOVWU (R23), R9; \
	SUBW  $1, R10; \
	B     loop; \
largestop: \
	ADD   $8, RSP, R8

// func insertDUBT1ExtAsm(bt *uint32, b, d *byte, iend int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, windowLow uint32) bool
//
// C: zstd_lazy.c:ZSTD_insertDUBT1 (extDict); see insertDUBT1ExtDict. The
// position may lie in the extDict (its segment then ends at dictEnd).
TEXT ·insertDUBT1ExtAsm(SB), NOSPLIT, $24-65
	SETUP(baseIdx+36(FP), dictBaseIdx+40(FP), dictLimit+44(FP), curr+32(FP), iend+24(FP), btMask+48(FP), nbCompares+52(FP), btLow+56(FP), windowLow+60(FP))
	CMPW  R13, R12
	BHS   ipset
	ADD   R12, R2, R3           // the position is in the extDict
	MOVD  R21, R4

ipset:
	MOVWU (R7), R9              // matchIndex = *smallerPtr
	MOVD  $1, R14
	MOVB  R14, ret+64(FP)

iloop:
	CBZW  R10, iend
	CMPW  R20, R9
	BLS   iend
	MATCHLENX(ibad, ipfx, icnt, idone, ia8, iad, iat, iae, ib8, ibd, ibk, ibe)
	ADD   R16, R3, R14
	CMP   R4, R14
	BEQ   iend                  // equal: no way to know if inf or sup
	MBYTE
	STEP(iloop, iend, ismallstop, ilargestop, ilarger)

iend:
	MOVW ZR, (R8)
	MOVW ZR, (R7)
	RET

ibad:
	MOVB ZR, ret+64(FP)
	RET

// func dubtSearchExtAsm(bt *uint32, b, d *byte, iend int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, offBase, matchEndIdx uint32)
//
// C: zstd_lazy.c:ZSTD_DUBT_findBestMatch, "find longest match" (extDict);
// see dubtFindBestMatchExtDict. The position is in the prefix. 16(RSP)
// holds offBase.
TEXT ·dubtSearchExtAsm(SB), NOSPLIT, $24-88
	SETUP(baseIdx+36(FP), dictBaseIdx+40(FP), dictLimit+44(FP), curr+32(FP), iend+24(FP), btMask+48(FP), nbCompares+52(FP), btLow+56(FP), windowLow+60(FP))
	MOVWU matchIndex+64(FP), R9
	MOVD  ZR, R25               // bestLength
	MOVW  $999999999, R14       // offBaseNotFound
	MOVW  R14, 16(RSP)
	ADDW  $9, R12, R26          // matchEndIdx = curr + 8 + 1

sloop:
	CBZW  R10, send
	CMPW  R20, R9
	BLS   send
	MATCHLENX(sbad, spfx, scnt, sdone, sa8, sad, sat, sae, sb8, sbd, sbk, sbe)
	CMP   R25, R16
	BLE   snotbetter
	// if matchLength > matchEndIdx - matchIndex: matchEndIdx = matchIndex + matchLength
	SUBW  R9, R26, R14
	CMP   R14, R16
	BLS   sendok
	ADDW  R16, R9, R26

sendok:
	// if 4*(matchLength-bestLength) > highbit32(curr-matchIndex+1) - highbit32(offBase)
	SUBW  R9, R12, R14
	ADDW  $1, R14
	CLZW  R14, R14
	MOVWU 16(RSP), R15
	CLZW  R15, R15
	SUB   R14, R15, R14
	SUB   R25, R16, R15
	LSL   $2, R15
	CMP   R14, R15
	BLE   snobest
	MOVD  R16, R25
	SUBW  R9, R12, R14
	ADDW  $3, R14
	MOVW  R14, 16(RSP)

snobest:
	ADD   R16, R3, R14
	CMP   R4, R14
	BEQ   send

snotbetter:
	ADD   R16, R3, R14
	CMP   R4, R14
	BEQ   sbad                  // would read past the input (impossible: see BOUNDS)
	MBYTE
	STEP(sloop, send, ssmallstop, slargestop, slarger)

send:
	MOVW  ZR, (R8)
	MOVW  ZR, (R7)
	MOVD  R25, bestLength+72(FP)
	MOVWU 16(RSP), R14
	MOVW  R14, offBase+80(FP)
	MOVW  R26, matchEndIdx+84(FP)
	RET

sbad:
	MOVW ZR, matchEndIdx+84(FP)
	RET

// func insertBt1ExtAsm(bt *uint32, b, d *byte, iend int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, windowLow, matchIndex uint32) (bestLength int, matchEndIdx uint32)
//
// C: zstd_opt.c:ZSTD_insertBt1 (extDict), the tree walk; see
// insertBt1ExtDict. The position is in the prefix. Candidates are >=
// windowLow (the BOUNDS above hold the same).
TEXT ·insertBt1ExtAsm(SB), NOSPLIT, $24-84
	SETUP(baseIdx+36(FP), dictBaseIdx+40(FP), dictLimit+44(FP), curr+32(FP), iend+24(FP), btMask+48(FP), nbCompares+52(FP), btLow+56(FP), windowLow+60(FP))
	MOVWU matchIndex+64(FP), R9
	MOVD  $8, R25               // bestLength
	ADDW  $9, R12, R26          // matchEndIdx = curr + 8 + 1

tloop:
	CBZW  R10, tend
	CMPW  R20, R9
	BLO   tend
	MATCHLENX(tbad, tpfx, tcnt, tdone, ta8, tad, tat, tae, tb8, tbd, tbk, tbe)
	CMP   R25, R16
	BLE   tnotbetter
	MOVD  R16, R25
	SUBW  R9, R26, R14
	CMP   R14, R16
	BLS   tnotbetter
	ADDW  R16, R9, R26

tnotbetter:
	ADD   R16, R3, R14
	CMP   R4, R14
	BEQ   tend                  // equal: no way to know if inf or sup
	MBYTE
	STEP(tloop, tend, tsmallstop, tlargestop, tlarger)

tend:
	MOVW ZR, (R8)
	MOVW ZR, (R7)
	MOVD R25, bestLength+72(FP)
	MOVW R26, matchEndIdx+80(FP)
	RET

tbad:
	MOVW ZR, matchEndIdx+80(FP)
	RET

// func btWalkExtAsm(bt *uint32, b, d *byte, matches *optMatch, iLimit int, curr, baseIdx, dictBaseIdx, dictLimit, btMask, nbCompares, btLow, matchLow, matchIndex, mnum uint32, bestLength int) (mnumOut, matchEndIdx uint32, bad bool)
//
// C: zstd_opt.c:ZSTD_insertBtAndGetAllMatches (extDict), the tree walk
// after the repcodes and the hash3 lookup (see
// insertBtAndGetAllMatchesExtDict), appending to matches from mnum. The
// position is in the prefix. bad reports a candidate not before the
// position, or a full match table (both impossible). 16(RSP) holds mnum,
// 24(RSP) matches.
TEXT ·btWalkExtAsm(SB), NOSPLIT, $32-97
	SETUP(baseIdx+44(FP), dictBaseIdx+48(FP), dictLimit+52(FP), curr+40(FP), iLimit+32(FP), btMask+56(FP), nbCompares+60(FP), btLow+64(FP), matchLow+68(FP))
	MOVD  matches+24(FP), R14
	MOVD  R14, 24(RSP)
	MOVWU matchIndex+72(FP), R9
	MOVD  bestLength+80(FP), R25
	MOVWU mnum+76(FP), R14
	MOVW  R14, 16(RSP)
	ADDW  $9, R12, R26          // matchEndIdx = curr + 8 + 1
	MOVB  ZR, bad+96(FP)

wloop:
	CBZW  R10, wend
	CMPW  R20, R9
	BLO   wend
	MATCHLENX(wbad, wpfx, wcnt, wdone, wa8, wad, wat, wae, wb8, wbd, wbk, wbe)
	CMP   R25, R16
	BLE   wnotbetter
	SUBW  R9, R26, R14
	CMP   R14, R16
	BLS   wendok
	ADDW  R16, R9, R26

wendok:
	MOVD  R16, R25              // bestLength
	MOVWU 16(RSP), R14
	MOVW  $4099, R15            // optSize
	CMPW  R15, R14
	BHS   wbad
	MOVD  24(RSP), R15
	ADD   R14<<3, R15, R15
	SUBW  R9, R12, R17
	ADDW  $3, R17
	MOVW  R17, (R15)            // off = offsetToOffBase(curr - matchIndex)
	MOVW  R16, 4(R15)           // len
	ADDW  $1, R14
	MOVW  R14, 16(RSP)
	MOVD  $4096, R15            // optNum
	CMP   R15, R16
	BGT   wend
	ADD   R16, R3, R14
	CMP   R4, R14
	BEQ   wend                  // equal: no way to know if inf or sup

wnotbetter:
	ADD   R16, R3, R14
	CMP   R4, R14
	BEQ   wbad                  // would read past the input (impossible: see BOUNDS)
	MBYTE
	STEP(wloop, wend, wsmallstop, wlargestop, wlarger)

wend:
	MOVW  ZR, (R8)
	MOVW  ZR, (R7)
	MOVWU 16(RSP), R14
	MOVW  R14, mnumOut+88(FP)
	MOVW  R26, matchEndIdx+92(FP)
	RET

wbad:
	MOVD $1, R14
	MOVB R14, bad+96(FP)
	RET
