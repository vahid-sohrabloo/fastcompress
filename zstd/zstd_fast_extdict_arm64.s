//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Frame locals.
#define DBIAS 8(RSP)     // d - dictBaseIdx: an extDict index's address
#define BBIAS 16(RSP)    // b - baseIdx: a prefix index's address
#define DENDP 24(RSP)    // address of dictEnd
#define IENDP 32(RSP)    // address of iend
#define DSTARTP 40(RSP)  // address of dictStart
#define PSTARTP 48(RSP)  // address of prefixStart
#define HASH1 56(RSP)    // hash1, kept over a match
#define IP1 64(RSP)      // ip1, kept over a match
#define C2IP 72(RSP)     // COUNT2's
#define C2LEN 80(RSP)    // COUNT2's

// HASH(pos, dst): dst = (le64(b[pos:]) * (prime << mlsShift)) >> hShift.
#define HASH(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R24, dst, dst; \
	LSR  R25, dst, dst

// COUNT2(done): R17 = count2Segments of the input at R14 against the
// match at R15, whose segment ends at R16 (C: ZSTD_count_2segments(ip,
// match, iend, mEnd, prefixStart)): a match that reaches R16, the end of
// the extDict, goes on at prefixStart. Pointers are addresses. Clobbers
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

// CANDIDATE(hit, miss): compare the 4 bytes at matchIdx (R7) with ip0's,
// going to hit if they match; a miss falls through to the label miss,
// which must follow. Below dictStartIndex (R26) there is no candidate
// (C: mval = read32(ip0) ^ 1). Below prefixStartIndex (R23) the candidate
// is in the extDict (R20 + matchIdx), else in the prefix (R19 +
// matchIdx). matchIdx >= current0 (R13) goes to bad, and so does an
// extDict candidate within 3 bytes of the extDict's end (the Go code's
// 4-byte read there would leave the extDict's slice).
#define CANDIDATE(hit, miss) \
	CMPW  R26, R7; \
	BLO   miss; \
	CMPW  R13, R7; \
	BHS   bad; \
	SUBW  R23, R7, R14; \
	ADDW  $3, R14; \
	CMPW  $3, R14; \
	BLO   bad; \
	CMPW  R23, R7; \
	CSEL  LO, R20, R19, R15; \
	MOVWU (R15)(R7), R15; \
	MOVWU (R0)(R2), R16; \
	CMPW  R15, R16; \
	BEQ   hit

// func compressBlockFastExtDictAsm(hashTable *uint32, b, d *byte, seqs *seqDef, lits *byte,
//	ip0, istart, iend, prefixStart, dictStart, dictEnd int,
//	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
//	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockFastExtDictGeneric's loop (C: zstd_fast.c:
// ZSTD_compressBlock_fast_extDict_generic, _start to _cleanup), as
// zstd_fast_extdict_amd64.s; the store of a sequence is
// compressBlockFastAsm's.
//
// Registers: R0 b, R1 hashTable, R2 ip0, R3 ip1, R4 ip2 (ip3 = ip2 + 1),
// R5 hash0, R6 hash1, R7 matchIdx, R8 rep1, R9 step, R10 nextStep, R11
// rep2, R12 anchor, R13 current0, R21 ilimit, R22 baseIdx, R23
// prefixStartIndex, R24 prime << mlsShift, R25 hShift, R26
// dictStartIndex; in the search loop R19 b - baseIdx and R20 d -
// dictBaseIdx (an index below prefixStartIndex addresses the extDict from
// R20, any other the prefix from R19; reloaded from the frame after the
// helpers clobber them; no calls in the loop, so the GC never sees them);
// R14-R17 scratch. nSeq and nLits live in their result slots. After a
// match: R2 ip0, R5 the match's address, R9 the end of its segment, R6
// mLength, R7 offBase; store takes R3 litLen and R10 the return selector.
//
// BOUNDS (the Go caller checks: iend <= len(b), dictEnd <= len(d),
// baseIdx <= prefixStartIndex = baseIdx + prefixStart <= baseIdx + istart,
// dictBaseIdx <= dictStartIndex = dictBaseIdx + dictStart,
// prefixStartIndex = dictBaseIdx + dictEnd, 0 < istart <= ip0, the table
// size, the room in seqs and lits; rep1 and rep2 are below ip0's index -
// dictStartIndex):
// (1) Input reads: hashes read 8 bytes at positions < ilimit = iend - 8
//     (ip0..ip3 < ilimit, current0 + 2 < ip0 <= ilimit after a match);
//     4-byte compares at ip0 and ip2 likewise; literals are copied from
//     anchor..ip0 by fastCopyLits; counts stop at iend; the backward
//     extension reads b[ip0 - 1] with ip0 > anchor >= istart > 0 (a
//     repcode at ip2 reads b[ip2 - 1]).
// (2) Candidate reads: CANDIDATE reads 4 bytes at dictStartIndex <=
//     matchIdx < current0, in the extDict only up to prefixStartIndex - 4
//     (d[dictStart:dictEnd-4+4]), else in b[prefixStart:ip0].
// (3) Repcode reads: a repcode's index is above dictStartIndex (the
//     caller's maxRep check, then every offset is taken from a match at or
//     above dictStartIndex at an earlier position) and below the position
//     searched; prefixStartIndex - repIndex >= 4 (or, after a match,
//     indexOverlapCheck) keeps its 4 bytes in one segment, and the
//     backward step reads one byte below it, still at or above dictStart.
// (4) Match reads: the backward extension stops at the segment's low end
//     (dictStart or prefixStart); COUNT2 reads the match only up to its
//     segment's end (dictEnd or iend) and then the prefix from
//     prefixStart, both no further than the input from ip0.
// (5) Table accesses: every hash is < 1 << hlog (64 - hShift bits).
// (6) Writes: seqs and lits as in compressBlockFastAsm (one sequence per
//     match of >= 4 bytes, literals copied exactly).
TEXT ·compressBlockFastExtDictAsm(SB), NOSPLIT, $88-184
	MOVD  hashTable+0(FP), R1
	MOVD  b+8(FP), R0
	MOVD  d+16(FP), R14
	MOVD  dictEnd+80(FP), R15
	ADD   R14, R15
	MOVD  R15, DENDP
	MOVD  dictStart+72(FP), R15
	ADD   R14, R15
	MOVD  R15, DSTARTP
	MOVWU dictBaseIdx+100(FP), R15
	SUB   R15, R14
	MOVD  R14, DBIAS
	MOVWU baseIdx+92(FP), R22
	SUB   R22, R0, R14
	MOVD  R14, BBIAS
	MOVD  iend+56(FP), R14
	ADD   R0, R14
	MOVD  R14, IENDP
	MOVD  prefixStart+64(FP), R14
	ADD   R0, R14
	MOVD  R14, PSTARTP
	MOVWU prefixStartIndex+88(FP), R23
	MOVWU dictStartIndex+96(FP), R26
	MOVWU rep1+104(FP), R8
	MOVWU rep2+108(FP), R11
	MOVD  ip0+40(FP), R2
	MOVD  istart+48(FP), R12
	MOVD  ZR, nSeq+152(FP)
	MOVD  ZR, nLits+160(FP)
	MOVW  ZR, llType+176(FP)
	MOVW  ZR, llPos+180(FP)
	MOVD  iend+56(FP), R21
	SUB   $8, R21                 // ilimit = iend - HASH_READ_SIZE
	MOVD  prime+136(FP), R24
	MOVD  mlsShift+120(FP), R14
	LSL   R14, R24, R24
	MOVD  hShift+128(FP), R25

start:
	MOVD BBIAS, R19
	MOVD DBIAS, R20
	MOVD stepSize+112(FP), R9
	ADD  $128, R2, R10            // nextStep = ip0 + kStepIncr
	ADD  $1, R2, R3
	ADD  R9, R2, R4
	ADD  $1, R4, R14              // ip3
	CMP  R21, R14
	BGE  cleanup
	HASH(R2, R5)
	HASH(R3, R6)
	MOVWU (R1)(R5<<2), R7

inner:
	// current0 = idx(ip0); hashTable[hash0] = current0
	ADDW R2, R22, R13
	MOVW R13, (R1)(R5<<2)
	// repcode at ip2: repIndex = idx(ip2) - rep1, valid when
	// prefixStartIndex - repIndex >= 4 (unsigned) and rep1 > 0.
	ADDW  R4, R22, R14
	SUBW  R8, R14, R14
	SUBW  R14, R23, R15
	CMPW  $4, R15
	BLO   norep
	CMPW  R23, R14
	CSEL  LO, R20, R19, R15
	MOVWU (R15)(R14), R15
	MOVWU (R0)(R4), R16
	CMPW  R15, R16
	BEQ   maybeRep

norep:
	CANDIDATE(hit1, miss1)

miss1:
	MOVWU (R1)(R6<<2), R7         // matchIdx = hashTable[hash1]
	MOVD  R6, R5                  // hash0 = hash1
	HASH(R4, R6)                  // hash1 = hash(ip2)
	MOVD  R3, R2                  // ip0 = ip1
	MOVD  R4, R3                  // ip1 = ip2
	ADD   $1, R4                  // ip2 = ip3
	ADDW  R2, R22, R13
	MOVW  R13, (R1)(R5<<2)
	CANDIDATE(hit2, miss2)

miss2:
	MOVWU (R1)(R6<<2), R7
	MOVD  R6, R5
	HASH(R4, R6)
	MOVD  R3, R2                  // ip0 = ip1
	MOVD  R4, R3                  // ip1 = ip2
	ADD   R9, R2, R4              // ip2 = ip0 + step (ip3 = ip1 + step = ip2 + 1)
	CMP   R10, R4
	BGE   stepinc

nostep:
	ADD  $1, R4, R14
	CMP  R21, R14
	BLT  inner
	B    cleanup

stepinc:
	ADD $1, R9
	ADD $128, R10
	B   nostep

maybeRep:
	CBZW  R8, norep
	MOVD  R6, HASH1
	MOVD  R3, IP1
	MOVD  R4, R2                  // ip0 = ip2
	ADDW  R2, R22, R14
	SUBW  R8, R14, R14            // repIndex
	MOVD  R19, R15
	MOVD  IENDP, R9
	CMPW  R23, R14
	CSEL  LO, R20, R15, R15
	MOVD  DENDP, R16
	CSEL  LO, R16, R9, R9         // the end of the match's segment
	ADD   R14, R15, R5            // match0
	MOVD  ZR, R6
	ADD   R2, R0, R14
	MOVBU -1(R14), R15
	MOVBU -1(R5), R16
	CMPW  R15, R16
	BNE   rep0
	MOVD  $1, R6
	SUB   $1, R2
	SUB   $1, R5

rep0:
	ADD  $4, R6
	MOVD $1, R7                   // repcode1ToOffBase
	B    match

hit1:
hit2:
	MOVD  R6, HASH1
	MOVD  R3, IP1
	SUBW  R7, R13, R14            // offset = current0 - matchIdx
	MOVD  R8, R11                 // rep2 = rep1
	MOVD  R14, R8                 // rep1 = offset
	MOVD  R7, R15                 // matchIdx
	ADD   $3, R14, R7             // offsetToOffBase(offset)
	MOVD  IENDP, R9
	MOVD  PSTARTP, R17            // lowMatchPtr
	CMPW  R23, R15
	CSEL  LO, R20, R19, R5
	MOVD  DENDP, R16
	CSEL  LO, R16, R9, R9         // matchEnd
	MOVD  DSTARTP, R16
	CSEL  LO, R16, R17, R17
	ADD   R15, R5                 // match0
	MOVD  $4, R6

back:
	CMP   R12, R2
	BLE   match
	CMP   R17, R5
	BLS   match
	ADD   R2, R0, R14
	MOVBU -1(R14), R15
	MOVBU -1(R5), R16
	CMPW  R15, R16
	BNE   match
	SUB   $1, R2
	SUB   $1, R5
	ADD   $1, R6
	B     back

match:
	// mLength += count2Segments(ip0+mLength, match0+mLength, iend, matchEnd, prefixStart)
	ADD  R6, R2, R14
	ADD  R0, R14
	ADD  R6, R5, R15
	MOVD R9, R16
	COUNT2(cnt1done)
	ADD  R17, R6

	// storeSeq(ss, ip0-anchor, b[anchor:], offBase, mLength)
	SUB  R12, R2, R3
	MOVD ZR, R10                  // return to aftermatch
	B    store

aftermatch:
	ADD  R6, R2                   // ip0 += mLength
	MOVD R2, R12                  // anchor = ip0
	// if ip1 < ip0: hashTable[hash1] = idx(ip1)
	MOVD IP1, R3
	CMP  R2, R3
	BGE  noh1
	MOVD HASH1, R6
	ADDW R3, R22, R14
	MOVW R14, (R1)(R6<<2)

noh1:
	CMP  R21, R2
	BGT  start

	// Fill table
	ADDW $2, R13, R14             // current0 + 2
	SUBW R22, R14, R15
	HASH(R15, R16)
	MOVW R14, (R1)(R16<<2)
	SUB  $2, R2, R15
	HASH(R15, R16)
	ADDW R15, R22, R14
	MOVW R14, (R1)(R16<<2)

reploop:
	CMP   R21, R2
	BGT   start
	CBZW  R11, start              // rep2 == 0
	ADDW  R2, R22, R14
	SUBW  R11, R14, R14           // repIndex2 = idx(ip0) - rep2
	// indexOverlapCheck: (prefixStartIndex - 1) - repIndex2 >= 3
	SUBW  R14, R23, R15
	SUBW  $1, R15
	CMPW  $3, R15
	BLO   start
	MOVD  BBIAS, R15
	MOVD  IENDP, R16
	CMPW  R23, R14
	MOVD  DBIAS, R17
	CSEL  LO, R17, R15, R15
	MOVD  DENDP, R17
	CSEL  LO, R17, R16, R16       // repEnd2
	ADD   R14, R15                // repMatch2
	MOVWU (R15), R17
	MOVWU (R0)(R2), R14
	CMPW  R14, R17
	BNE   start
	// rLength = count2Segments(ip0+4, repMatch2+4, iend, repEnd2, prefixStart) + 4
	ADD   $4, R15
	ADD   R0, R2, R14
	ADD   $4, R14
	COUNT2(cnt2done)
	ADD   $4, R17, R6
	// swap rep1 and rep2
	MOVD  R11, R14
	MOVD  R8, R11
	MOVD  R14, R8
	// hashTable[hash(ip0)] = idx(ip0)
	HASH(R2, R16)
	ADDW  R2, R22, R14
	MOVW  R14, (R1)(R16<<2)
	MOVD  ZR, R3                  // no literals
	MOVD  $1, R7                  // repcode1ToOffBase
	MOVD  $1, R10                 // return to afterrep
	B     store

afterrep:
	ADD  R6, R2
	MOVD R2, R12
	B    reploop

	// store: R3 litLen (literals from anchor), R7 offBase, R6 matchLength;
	// returns to aftermatch (R10 = 0) or afterrep (R10 = 1).
	// Clobbers R14-R17, R19, R20, V0.
store:
	CBZ  R3, storeseq
	MOVD lits+32(FP), R14
	MOVD nLits+160(FP), R17
	ADD  R17, R14
	ADD  R12, R0, R15
	MOVD R3, R16
	CALL ·fastCopyLits(SB)
	MOVD nLits+160(FP), R17
	ADD  R3, R17
	MOVD R17, nLits+160(FP)

storeseq:
	MOVD seqs+24(FP), R14
	MOVD nSeq+152(FP), R15
	ADD  R15<<3, R14, R14
	MOVW R7, (R14)                // offBase
	MOVH R3, 4(R14)               // litLength
	SUB  $3, R6, R16              // mlBase = matchLength - MINMATCH
	MOVH R16, 6(R14)
	CMP  $0xFFFF, R3
	BLS  nolongll
	MOVD $1, R17                  // lltLiteralLength
	MOVW R17, llType+176(FP)
	MOVW R15, llPos+180(FP)

nolongll:
	CMP  $0xFFFF, R16
	BLS  nolongml
	MOVD $2, R17                  // lltMatchLength
	MOVW R17, llType+176(FP)
	MOVW R15, llPos+180(FP)

nolongml:
	ADD  $1, R15
	MOVD R15, nSeq+152(FP)
	CBZ  R10, aftermatch
	B    afterrep

cleanup:
	MOVD R12, anchor+144(FP)
	MOVW R8, rep1Out+168(FP)
	MOVW R11, rep2Out+172(FP)
	RET

bad:
	MOVD $-1, R14
	MOVD R14, nSeq+152(FP)
	RET
