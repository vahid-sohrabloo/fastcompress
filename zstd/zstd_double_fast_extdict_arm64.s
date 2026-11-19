//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Frame locals.
#define DBIAS 8(RSP)     // d - dictBaseIdx: an extDict index's address
#define BBIAS 16(RSP)    // b - baseIdx: a prefix index's address
#define DENDP 24(RSP)    // address of dictEnd
#define IENDP 32(RSP)    // address of iend
#define DSTARTP 40(RSP)  // address of dictStart
#define PSTARTP 48(RSP)  // address of prefixStart
#define C2IP 56(RSP)     // COUNT2's
#define C2LEN 64(RSP)    // COUNT2's
#define MLOW 72(RSP)     // the match's segment's low end
#define MPTR 80(RSP)     // the match's address
#define OFFSET 88(RSP)
#define MINLEN 96(RSP)

// HASHL(pos, dst): long hash, (le64(b[pos:]) * prime8bytes) >> hShiftL.
// HASHS(pos, dst): short hash, (le64(b[pos:]) * (primeS << shiftS)) >> hShiftS.
#define HASHL(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R23, dst, dst; \
	LSR  R24, dst, dst

#define HASHS(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R25, dst, dst; \
	LSR  R26, dst, dst

// COUNT2(done): R17 = count2Segments of the input at R14 against the
// match at R15, whose segment ends at R16 (C: ZSTD_count_2segments(ip,
// match, iend, mEnd, prefixStart)). Pointers are addresses. Clobbers
// R14-R16, R19, R20 (fastCount's).
#define COUNT2(done) \
	MOVD R14, C2IP; \
	SUB  R15, R16, R16; \
	MOVD R16, C2LEN; \
	MOVD IENDP, R17; \
	SUB  R14, R17, R17; \
	CMP  R17, R16; \
	CSEL HI, R17, R16, R16; \
	CALL ·fastCount(SB); \
	MOVD C2LEN, R16; \
	CMP  R16, R17; \
	BNE  done; \
	MOVD R17, C2LEN; \
	MOVD C2IP, R14; \
	ADD  R17, R14; \
	MOVD PSTARTP, R15; \
	MOVD IENDP, R16; \
	SUB  R14, R16, R16; \
	CALL ·fastCount(SB); \
	MOVD C2LEN, R16; \
	ADD  R16, R17; \
done:

// func compressBlockDoubleFastExtDictAsm(hashLong, hashSmall *uint32, b, d *byte, seqs *seqDef, lits *byte,
//	istart, iend, prefixStart, dictStart, dictEnd int,
//	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
//	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockDoubleFastExtDictGeneric's loop (C:
// zstd_double_fast.c:ZSTD_compressBlock_doubleFast_extDict_generic), as
// zstd_double_fast_extdict_amd64.s; the store of a sequence is
// compressBlockFastAsm's.
//
// Registers: R0 b, R1 hashLong, R2 hashSmall, R3 ip, R4 anchor, R6
// dictStartIndex, R7 ilimit, R8 offset2, R9 offset1, R10 curr, R11
// read64(ip), R12 matchIndex, R13 matchLongIndex, R21 baseIdx, R22
// prefixStartIndex, R23 prime8bytes, R24 hShiftL, R25 primeS << shiftS,
// R26 hShiftS; in the search loop R19 b - baseIdx and R20 d - dictBaseIdx
// (an index below prefixStartIndex addresses the extDict from R20, any
// other the prefix from R19; reloaded from the frame after the helpers
// clobber them; no calls in the loop, so the GC never sees them); R14-R17
// scratch. nSeq and nLits live in their result slots. For a match: R3
// ip, R5 mLength, R13 offBase.
//
// BOUNDS (the Go caller checks: iend <= len(b), dictEnd <= len(d),
// baseIdx <= prefixStartIndex = baseIdx + prefixStart <= baseIdx + istart,
// dictBaseIdx <= dictStartIndex = dictBaseIdx + dictStart,
// prefixStartIndex = dictBaseIdx + dictEnd, 8 <= dictEnd, the table sizes,
// the room in seqs and lits):
// (1) Input reads: 8 bytes at ip, ip + 1 (searched positions are
//     < ilimit = iend - 8) and, for the insertions after a match, at
//     curr + 2 < ip, ip - 2 and ip - 1 with ip <= ilimit; literals from
//     anchor..ip by fastCopyLits; counts stop at iend; the backward
//     extension reads b[ip - 1] with ip > anchor >= istart >= 0.
// (2) Candidate reads: 8 (long) or 4 (short) bytes at dictStartIndex <
//     idx < curr (curr + 1 for the long candidate at ip + 1); in the
//     extDict idx is at most prefixStartIndex - 8 or - 4, so the read is
//     within d[dictStart:dictEnd], else within b[prefixStart:ip+8]. An
//     index at or past the position, or in the extDict's last 7 (3)
//     bytes, goes to bad (the Go code reads past d's slice there).
// (3) Repcode reads: indexOverlapCheck(prefixStartIndex, repIndex) and
//     offset <= the position's index - dictStartIndex keep the 4 bytes at
//     repIndex in one segment, at or above dictStartIndex and at most at
//     the position read (offset 0 reads the position itself).
// (4) Match reads: the backward extension stops at the segment's low end
//     (dictStart or prefixStart); COUNT2 reads the match only up to its
//     segment's end (dictEnd or iend) and then the prefix from
//     prefixStart, no further than the input from ip.
// (5) Table accesses: every hash is < the table's size (64 - hShift bits).
// (6) Writes: seqs and lits as in compressBlockFastAsm.
TEXT ·compressBlockDoubleFastExtDictAsm(SB), NOSPLIT, $104-184
	MOVD  hashLong+0(FP), R1
	MOVD  hashSmall+8(FP), R2
	MOVD  b+16(FP), R0
	MOVD  d+24(FP), R14
	MOVD  dictEnd+80(FP), R15
	ADD   R14, R15
	MOVD  R15, DENDP
	MOVD  dictStart+72(FP), R15
	ADD   R14, R15
	MOVD  R15, DSTARTP
	MOVWU dictBaseIdx+100(FP), R15
	SUB   R15, R14
	MOVD  R14, DBIAS
	MOVWU baseIdx+92(FP), R21
	SUB   R21, R0, R14
	MOVD  R14, BBIAS
	MOVD  iend+56(FP), R14
	ADD   R0, R14
	MOVD  R14, IENDP
	MOVD  prefixStart+64(FP), R14
	ADD   R0, R14
	MOVD  R14, PSTARTP
	MOVWU prefixStartIndex+88(FP), R22
	MOVWU dictStartIndex+96(FP), R6
	MOVWU rep1+104(FP), R9
	MOVWU rep2+108(FP), R8
	MOVD  istart+48(FP), R3
	MOVD  R3, R4                // anchor
	MOVD  ZR, nSeq+152(FP)
	MOVD  ZR, nLits+160(FP)
	MOVW  ZR, llType+176(FP)
	MOVW  ZR, llPos+180(FP)
	MOVD  iend+56(FP), R7
	SUB   $8, R7                // ilimit
	MOVD  $0xCF1BBCDCB7A56463, R23
	MOVD  hShiftL+112(FP), R24
	MOVD  primeS+136(FP), R25
	MOVD  shiftS+120(FP), R14
	LSL   R14, R25, R25
	MOVD  hShiftS+128(FP), R26

reload:
	MOVD BBIAS, R19
	MOVD DBIAS, R20

loop:
	CMP   R7, R3
	BGE   cleanup
	MOVD  (R0)(R3), R11         // read64(ip)
	MUL   R25, R11, R14
	LSR   R26, R14, R14         // hSmall
	MUL   R23, R11, R15
	LSR   R24, R15, R15         // hLong
	MOVWU (R2)(R14<<2), R12     // matchIndex
	MOVWU (R1)(R15<<2), R13     // matchLongIndex
	ADDW  R3, R21, R10          // curr
	MOVW  R10, (R2)(R14<<2)
	MOVW  R10, (R1)(R15<<2)

	// repcode at ip+1: repIndex = curr + 1 - offset1
	ADDW  $1, R10, R14
	SUBW  R9, R14, R14
	SUBW  R14, R22, R15
	SUBW  $1, R15
	CMPW  $3, R15
	BLO   norep                 // !indexOverlapCheck
	ADDW  $1, R10, R15
	SUBW  R6, R15
	CMPW  R15, R9
	BHI   norep                 // offset1 > curr + 1 - dictStartIndex
	CMPW  R22, R14
	CSEL  LO, R20, R19, R15
	MOVWU (R15)(R14), R16
	ADD   $1, R3, R17
	MOVWU (R0)(R17), R17
	CMPW  R16, R17
	BEQ   foundrep

norep:
	// long match at ip
	CMPW  R6, R13
	BLS   nolong
	CMPW  R10, R13
	BHS   bad
	SUBW  R22, R13, R15
	ADDW  $7, R15
	CMPW  $7, R15
	BLO   bad
	CMPW  R22, R13
	CSEL  LO, R20, R19, R15
	MOVD  (R15)(R13), R15
	CMP   R15, R11
	BEQ   foundlong

nolong:
	// short match at ip
	CMPW  R6, R12
	BLS   noshort
	CMPW  R10, R12
	BHS   bad
	SUBW  R22, R12, R15
	ADDW  $3, R15
	CMPW  $3, R15
	BLO   bad
	CMPW  R22, R12
	CSEL  LO, R20, R19, R15
	MOVWU (R15)(R12), R15
	CMPW  R15, R11
	BEQ   foundshort

noshort:
	SUB R4, R3, R14
	LSR $8, R14                 // kSearchStrength
	ADD $1, R14
	ADD R14, R3
	B   loop

foundrep:
	// mLength = count2Segments(ip+1+4, repMatch+4, iend, repEnd, prefixStart) + 4
	MOVD  IENDP, R16
	CMPW  R22, R14
	MOVD  DENDP, R17
	CSEL  LO, R17, R16, R16     // repEnd
	ADD   R14, R15              // repMatch
	ADD   $4, R15
	ADD   R0, R3, R14
	ADD   $5, R14
	COUNT2(cnt1done)
	ADD   $4, R17, R5
	ADD   $1, R3
	MOVD  $1, R13               // repcode1ToOffBase
	B     store

foundlong:
	SUBW R13, R10, R14
	MOVW R14, OFFSET            // offset = curr - matchLongIndex
	MOVD $8, R15
	B    lmatch

foundshort:
	ADD   $1, R3, R14
	MOVD  (R0)(R14), R11        // read64(ip+1)
	MUL   R23, R11, R15
	LSR   R24, R15, R15         // h3
	MOVWU (R1)(R15<<2), R13     // matchIndex3
	ADDW  $1, R10, R16          // curr + 1
	MOVW  R16, (R1)(R15<<2)
	CMPW  R6, R13
	BLS   short4
	CMPW  R16, R13
	BHS   bad
	SUBW  R22, R13, R15
	ADDW  $7, R15
	CMPW  $7, R15
	BLO   bad
	CMPW  R22, R13
	CSEL  LO, R20, R19, R15
	MOVD  (R15)(R13), R15
	CMP   R15, R11
	BNE   short4
	ADD   $1, R3                // ip++
	SUBW  R13, R16, R16
	MOVW  R16, OFFSET           // offset = curr + 1 - matchIndex3
	MOVD  $8, R15
	B     lmatch

short4:
	MOVWU R12, R13              // zero-extended: R13 is added to a base in lmatch (an index over 2^31 must not go negative)
	SUBW R13, R10, R16
	MOVW R16, OFFSET            // offset = curr - matchIndex
	MOVD $4, R15

lmatch:
	// R3 ip, R13 the match's index, R15 its length so far (8 or 4):
	// mLength = count2Segments(ip+R15, match+R15, ...) + R15, then the
	// backward extension (C: catch up).
	MOVD  R15, MINLEN
	MOVD  IENDP, R17
	MOVD  PSTARTP, R11
	CMPW  R22, R13
	CSEL  LO, R20, R19, R16
	MOVD  DENDP, R14
	CSEL  LO, R14, R17, R17     // the match's segment's end
	MOVD  DSTARTP, R14
	CSEL  LO, R14, R11, R11     // and its low end
	ADD   R13, R16              // match
	MOVD  R16, MPTR
	MOVD  R11, MLOW
	ADD   R0, R3, R14
	ADD   R15, R14
	ADD   R15, R16, R15
	MOVD  R17, R16
	COUNT2(cnt2done)
	MOVD  MINLEN, R5
	ADD   R17, R5
	MOVD  MPTR, R16
	MOVD  MLOW, R11

catchup:
	CMP   R4, R3
	BLE   caught
	CMP   R11, R16
	BLS   caught
	ADD   R0, R3, R14
	MOVBU -1(R14), R14
	MOVBU -1(R16), R15
	CMPW  R14, R15
	BNE   caught
	SUB   $1, R3
	SUB   $1, R16
	ADD   $1, R5
	B     catchup

caught:
	MOVD  R9, R8                // offset2 = offset1
	MOVWU OFFSET, R9            // offset1 = offset
	ADD   $3, R9, R13           // offsetToOffBase(offset)

store:
	// storeSeq(ss, ip-anchor, b[anchor:], offBase R13, mLength R5)
	SUB  R4, R3, R11
	CBZ  R11, storeseq
	MOVD lits+40(FP), R14
	MOVD nLits+160(FP), R17
	ADD  R17, R14
	ADD  R4, R0, R15
	MOVD R11, R16
	CALL ·fastCopyLits(SB)
	MOVD nLits+160(FP), R17
	ADD  R11, R17
	MOVD R17, nLits+160(FP)

storeseq:
	MOVD seqs+32(FP), R14
	MOVD nSeq+152(FP), R15
	ADD  R15<<3, R14, R14
	MOVW R13, (R14)             // offBase
	MOVH R11, 4(R14)            // litLength
	SUB  $3, R5, R16            // mlBase = matchLength - MINMATCH
	MOVH R16, 6(R14)
	CMP  $0xFFFF, R11
	BLS  nolongll
	MOVD $1, R17                // lltLiteralLength
	MOVW R17, llType+176(FP)
	MOVW R15, llPos+180(FP)

nolongll:
	CMP  $0xFFFF, R16
	BLS  nolongml
	MOVD $2, R17                // lltMatchLength
	MOVW R17, llType+176(FP)
	MOVW R15, llPos+180(FP)

nolongml:
	ADD  $1, R15
	MOVD R15, nSeq+152(FP)

	ADD  R5, R3                 // ip += mLength
	MOVD R3, R4                 // anchor = ip
	CMP  R7, R3
	BGT  reload

	// Complementary insertion
	ADDW $2, R10, R13           // indexToInsert = curr + 2
	SUBW R21, R13, R14
	HASHL(R14, R15)
	MOVW R13, (R1)(R15<<2)
	HASHS(R14, R15)
	MOVW R13, (R2)(R15<<2)
	SUB  $2, R3, R14
	HASHL(R14, R15)
	ADDW R14, R21, R16
	MOVW R16, (R1)(R15<<2)
	SUB  $1, R3, R14
	HASHS(R14, R15)
	ADDW R14, R21, R16
	MOVW R16, (R2)(R15<<2)

reploop:
	CMP   R7, R3
	BGT   reload
	ADDW  R3, R21, R10          // current2
	SUBW  R8, R10, R14          // repIndex2
	SUBW  R14, R22, R15
	SUBW  $1, R15
	CMPW  $3, R15
	BLO   reload                // !indexOverlapCheck
	SUBW  R6, R10, R15
	CMPW  R15, R8
	BHI   reload                // offset2 > current2 - dictStartIndex
	MOVD  BBIAS, R15
	MOVD  IENDP, R16
	MOVD  DBIAS, R17
	CMPW  R22, R14
	CSEL  LO, R17, R15, R15
	MOVD  DENDP, R17
	CSEL  LO, R17, R16, R16     // repEnd2
	ADD   R14, R15              // repMatch2
	MOVWU (R15), R17
	MOVWU (R0)(R3), R14
	CMPW  R14, R17
	BNE   reload
	// repLength2 = count2Segments(ip+4, repMatch2+4, iend, repEnd2, prefixStart) + 4
	ADD   $4, R15
	ADD   R0, R3, R14
	ADD   $4, R14
	COUNT2(cnt3done)
	ADD   $4, R17, R5
	// swap offset1 and offset2
	MOVD  R8, R14
	MOVD  R9, R8
	MOVD  R14, R9
	// hashSmall[hashS(ip)] = hashLong[hashL(ip)] = current2
	HASHS(R3, R15)
	MOVW  R10, (R2)(R15<<2)
	HASHL(R3, R15)
	MOVW  R10, (R1)(R15<<2)
	// store the sequence (no literals, repcode1ToOffBase)
	MOVD  seqs+32(FP), R14
	MOVD  nSeq+152(FP), R15
	ADD   R15<<3, R14, R14
	MOVD  $1, R16
	MOVD  R16, (R14)            // offBase 1, litLength 0
	SUB   $3, R5, R16           // mlBase = matchLength - MINMATCH
	MOVH  R16, 6(R14)
	CMP   $0xFFFF, R16
	BLS   repshort
	MOVD  $2, R17               // lltMatchLength
	MOVW  R17, llType+176(FP)
	MOVW  R15, llPos+180(FP)

repshort:
	ADD  $1, R15
	MOVD R15, nSeq+152(FP)
	ADD  R5, R3
	MOVD R3, R4
	B    reploop

cleanup:
	MOVD R4, anchor+144(FP)
	MOVW R9, rep1Out+168(FP)
	MOVW R8, rep2Out+172(FP)
	RET

bad:
	MOVD $-1, R14
	MOVD R14, nSeq+152(FP)
	RET
