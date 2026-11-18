//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASHF(pos, dst): dst = (le64(b[pos:]) * (prime << mlsShift)) >> CX,
// with CX = hShift (as in zstd_fast_amd64.s).
#define HASHF(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	IMULQ BP, dst; \
	SHRQ  CX, dst

// HASH(pos, dst) is HASHF loading CX = hShift first.
#define HASH(pos, dst) \
	MOVQ hShift+128(FP), CX; \
	HASHF(pos, dst)

// COUNTI(call, done): R11 = fastCount(R9, R10, R13), with the first 8
// bytes compared inline. Clobbers CX (and, through the call, BX and Y0).
#define COUNTI(call, done) \
	CMPQ R13, $8; \
	JLT  call; \
	MOVQ (R10), CX; \
	XORQ (R9), CX; \
	JEQ  call; \
	BSFQ CX, R11; \
	SHRQ $3, R11; \
	JMP  done; \
call: \
	CALL ·fastCount(SB); \
done:

// COUNT2(c1, d1, c2, d2, done): R11 = count2Segments of the input at R9
// against the match at R10, whose segment ends at DX (C:
// ZSTD_count_2segments(ip, match, iend, mEnd, prefixStart)): a match that
// reaches DX, the end of the extDict, goes on at prefixStart. Pointers are
// addresses; the input ends at 48(SP), prefixStart is 64(SP). Uses 88(SP)
// and 104(SP); clobbers R9, R10, R13, CX, BX and Y0.
#define COUNT2(c1, d1, c2, d2, done) \
	MOVQ    48(SP), R13; \
	SUBQ    R9, R13; \
	MOVQ    DX, CX; \
	SUBQ    R10, CX; \
	CMPQ    CX, R13; \
	CMOVQLT CX, R13; \
	MOVQ    DX, 88(SP); \
	COUNTI(c1, d1); \
	LEAQ    (R10)(R11*1), CX; \
	CMPQ    CX, 88(SP); \
	JNE     done; \
	MOVQ    R11, 104(SP); \
	ADDQ    R11, R9; \
	MOVQ    64(SP), R10; \
	MOVQ    48(SP), R13; \
	SUBQ    R9, R13; \
	COUNTI(c2, d2); \
	ADDQ    104(SP), R11; \
done:

// CANDIDATE(hit, miss): compare the 4 bytes at matchIdx (R13) with ip0's,
// going to hit if they match; a miss falls through to the label miss,
// which must follow. Below dictStartIndex there is no candidate (C:
// mval = read32(ip0) ^ 1). Below prefixStartIndex the candidate is in
// the extDict (address 32(SP) + matchIdx), else in the prefix (R15 +
// matchIdx). AX = current0; matchIdx >= current0 goes to bad, and so
// does an extDict candidate within 3 bytes of the extDict's end (the Go
// code's 4-byte read there would leave the extDict's slice).
#define CANDIDATE(hit, miss) \
	CMPL    R13, dictStartIndex+96(FP); \
	JB      miss; \
	CMPL    R13, AX; \
	JAE     bad; \
	MOVL    R13, DX; \
	SUBL    96(SP), DX; \
	CMPL    DX, $3; \
	JB      bad; \
	MOVQ    R15, DX; \
	CMPL    R13, prefixStartIndex+88(FP); \
	CMOVQCS 32(SP), DX; \
	MOVL    (DX)(R13*1), DX; \
	CMPL    DX, (SI)(R8*1); \
	JEQ     hit

// func compressBlockFastExtDictAsm(hashTable *uint32, b, d *byte, seqs *seqDef, lits *byte,
//	ip0, istart, iend, prefixStart, dictStart, dictEnd int,
//	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
//	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockFastExtDictGeneric's loop (C: zstd_fast.c:
// ZSTD_compressBlock_fast_extDict_generic, _start to _cleanup); the store
// of a sequence is compressBlockFastAsm's.
//
// Registers in the search loop: SI b, DI hashTable, R8 ip0, R9 ip1, R10
// ip2 (ip3 = ip2 + 1), R11 hash0, R12 hash1, R13 matchIdx, R14 rep1, BX
// baseIdx, R15 b - baseIdx and 32(SP) d - dictBaseIdx (an index below
// prefixStartIndex addresses the extDict from 32(SP), any other the prefix
// from R15; no calls in the loop, so the GC never sees them), CX hShift,
// BP prime << mlsShift, AX/DX scratch. The frame: 0 current0, 8 ilimit,
// 16 step, 24 nextStep, 32 d - dictBaseIdx, 40 the address of dictEnd, 48
// of iend, 56 of dictStart, 64 of prefixStart, 72 hash1 and 80 ip1 kept
// over a match, 88 and 104 COUNT2's, 96 prefixStartIndex - 3. After a
// match is found: R8 ip0, DX the match's address, AX mLength, R12 offBase,
// 88(SP) the end of the match's segment.
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
//     anchor..ip0 (16-byte copies only when anchor + 16 <= iend); counts
//     stop at iend; the backward extension reads b[ip0 - 1] with
//     ip0 > anchor >= istart > 0 (a repcode at ip2 reads b[ip2 - 1]).
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
//     match of >= 4 bytes, literals with 16 bytes of slack).
TEXT ·compressBlockFastExtDictAsm(SB), NOSPLIT, $112-184
	MOVQ hashTable+0(FP), DI
	MOVQ b+8(FP), SI
	MOVQ d+16(FP), AX
	MOVQ AX, DX
	ADDQ dictEnd+80(FP), DX
	MOVQ DX, 40(SP)          // dictEnd address
	MOVQ AX, DX
	ADDQ dictStart+72(FP), DX
	MOVQ DX, 56(SP)          // dictStart address
	MOVL dictBaseIdx+100(FP), DX
	SUBQ DX, AX
	MOVQ AX, 32(SP)          // d - dictBaseIdx
	MOVQ SI, AX
	ADDQ iend+56(FP), AX
	MOVQ AX, 48(SP)          // iend address
	MOVQ SI, AX
	ADDQ prefixStart+64(FP), AX
	MOVQ AX, 64(SP)          // prefixStart address
	MOVL prefixStartIndex+88(FP), AX
	SUBL $3, AX
	MOVL AX, 96(SP)          // prefixStartIndex - 3
	MOVL rep1+104(FP), R14
	MOVQ ip0+40(FP), R8
	MOVQ istart+48(FP), AX
	MOVQ AX, anchor+144(FP)
	MOVQ $0, nSeq+152(FP)
	MOVQ $0, nLits+160(FP)
	MOVL $0, llType+176(FP)
	MOVL $0, llPos+180(FP)
	MOVQ iend+56(FP), AX
	SUBQ $8, AX
	MOVQ AX, 8(SP)           // ilimit = iend - HASH_READ_SIZE
	MOVQ prime+136(FP), AX
	MOVQ mlsShift+120(FP), CX
	SHLQ CX, AX
	MOVQ AX, BP              // prime << mlsShift (the helpers called do not touch BP)

start:
	MOVQ stepSize+112(FP), AX
	MOVQ AX, 16(SP)          // step
	LEAQ (R8)(AX*1), R10     // ip2 = ip0 + step
	LEAQ 128(R8), AX
	MOVQ AX, 24(SP)          // nextStep = ip0 + kStepIncr
	LEAQ 1(R8), R9
	LEAQ 1(R10), AX          // ip3
	CMPQ AX, 8(SP)
	JGE  cleanup
	MOVL baseIdx+92(FP), BX
	MOVQ SI, R15
	SUBQ BX, R15
	MOVQ hShift+128(FP), CX
	HASHF(R8, R11)
	HASHF(R9, R12)
	MOVL (DI)(R11*4), R13
	JMP  inner
	PCALIGN $64

inner:
	// current0 = idx(ip0); hashTable[hash0] = current0
	LEAL (R8)(BX*1), AX
	MOVL AX, 0(SP)
	MOVL AX, (DI)(R11*4)
	// repcode at ip2: repIndex = idx(ip2) - rep1, valid when
	// prefixStartIndex - repIndex >= 4 (unsigned) and rep1 > 0. R9
	// (ip1 = ip0 + 1 here) is scratch until norep1.
	LEAL (R10)(BX*1), DX
	SUBL R14, DX
	MOVL prefixStartIndex+88(FP), R9
	SUBL DX, R9
	CMPL R9, $4
	JB   norep1
	MOVQ R15, R9
	CMPL DX, prefixStartIndex+88(FP)
	CMOVQCS 32(SP), R9
	MOVL (R9)(DX*1), DX
	CMPL DX, (SI)(R10*1)
	JEQ  maybeRep

norep1:
	LEAQ 1(R8), R9
	CANDIDATE(hit1, miss1)

miss1:
	MOVL (DI)(R12*4), R13  // matchIdx = hashTable[hash1]
	MOVQ R12, R11          // hash0 = hash1
	HASHF(R10, R12)        // hash1 = hash(ip2)
	MOVQ R9, R8            // ip0 = ip1
	MOVQ R10, R9           // ip1 = ip2
	INCQ R10               // ip2 = ip3
	LEAL (R8)(BX*1), AX
	MOVL AX, 0(SP)
	MOVL AX, (DI)(R11*4)
	CANDIDATE(hit2, miss2)

miss2:
	MOVL (DI)(R12*4), R13
	MOVQ R12, R11
	HASHF(R10, R12)
	MOVQ R9, R8            // ip0 = ip1
	MOVQ R10, R9           // ip1 = ip2
	MOVQ 16(SP), R10
	ADDQ R8, R10           // ip2 = ip0 + step (ip3 = ip1 + step = ip2 + 1)
	CMPQ R10, 24(SP)
	JGE  stepinc

nostep:
	LEAQ 1(R10), AX
	CMPQ AX, 8(SP)
	JLT  inner
	JMP  cleanup

stepinc:
	INCQ 16(SP)
	ADDQ $128, 24(SP)
	JMP  nostep

maybeRep:
	TESTL R14, R14
	JEQ   norep1
	LEAQ  1(R8), R9
	MOVQ  R12, 72(SP)      // hash1
	MOVQ  R9, 80(SP)       // ip1
	MOVQ  R10, R8          // ip0 = ip2
	LEAL  (R8)(BX*1), AX
	SUBL  R14, AX          // repIndex
	MOVQ  R15, DX
	MOVQ  48(SP), CX
	CMPL  AX, prefixStartIndex+88(FP)
	CMOVQCS 32(SP), DX
	CMOVQCS 40(SP), CX
	ADDQ  AX, DX           // match0
	MOVQ  CX, 88(SP)       // its segment's end
	XORL  AX, AX
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(DX), R11
	CMPL  CX, R11
	JNE   rep0
	MOVL  $1, AX
	DECQ  R8
	DECQ  DX

rep0:
	ADDQ $4, AX
	MOVL $1, R12           // repcode1ToOffBase
	JMP  match

hit1:
hit2:
	MOVQ R12, 72(SP)       // hash1
	MOVQ R9, 80(SP)        // ip1
	MOVL 0(SP), AX
	SUBL R13, AX           // offset = current0 - matchIdx
	MOVL R14, rep2+108(FP) // rep2 = rep1
	MOVL AX, R14           // rep1 = offset
	LEAQ 3(AX), R12        // offsetToOffBase(offset)
	MOVQ R15, DX
	MOVQ 48(SP), CX
	MOVQ 64(SP), R9
	CMPL R13, prefixStartIndex+88(FP)
	CMOVQCS 32(SP), DX
	CMOVQCS 40(SP), CX
	CMOVQCS 56(SP), R9     // lowMatchPtr
	ADDQ R13, DX           // match0
	MOVQ CX, 88(SP)        // matchEnd
	MOVL $4, AX

back:
	CMPQ    R8, anchor+144(FP)
	JLE     match
	CMPQ    DX, R9
	JLS     match
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(DX), R11
	CMPL    CX, R11
	JNE     match
	DECQ    R8
	DECQ    DX
	INCQ    AX
	JMP     back

match:
	// mLength += count2Segments(ip0+mLength, match0+mLength, iend, matchEnd, prefixStart)
	LEAQ (R8)(AX*1), R9
	ADDQ SI, R9
	LEAQ (DX)(AX*1), R10
	MOVQ 88(SP), DX
	COUNT2(cnt1call, cnt1done, cnt2call, cnt2done, cnt12done)
	ADDQ R11, AX

	// storeSeq(ss, ip0-anchor, b[anchor:], offBase, mLength)
	MOVQ R8, R9
	SUBQ anchor+144(FP), R9
	TESTQ R9, R9
	JEQ   storeseq
	MOVQ  lits+32(FP), R10
	ADDQ  nLits+160(FP), R10
	MOVQ  anchor+144(FP), R11
	ADDQ  R9, nLits+160(FP)
	CMPQ  R9, $16
	JA    litcall
	LEAQ  16(R11), CX
	CMPQ  CX, iend+56(FP)
	JA    litcall
	MOVOU (SI)(R11*1), X0
	MOVOU X0, (R10)
	JMP   storeseq

litcall:
	ADDQ  SI, R11
	CALL  ·fastCopyLits(SB)

storeseq:
	MOVQ seqs+24(FP), R10
	MOVQ nSeq+152(FP), R11
	MOVL R12, (R10)(R11*8)     // offBase
	MOVW R9, 4(R10)(R11*8)     // litLength
	LEAQ -3(AX), CX            // mlBase = matchLength - MINMATCH
	MOVW CX, 6(R10)(R11*8)
	CMPQ R9, $0xFFFF
	JLS  nolongll
	MOVL $1, llType+176(FP)    // lltLiteralLength
	MOVL R11, llPos+180(FP)

nolongll:
	CMPQ CX, $0xFFFF
	JLS  nolongml
	MOVL $2, llType+176(FP)    // lltMatchLength
	MOVL R11, llPos+180(FP)

nolongml:
	INCQ R11
	MOVQ R11, nSeq+152(FP)

	ADDQ AX, R8            // ip0 += mLength
	MOVQ R8, anchor+144(FP)
	// if ip1 < ip0: hashTable[hash1] = idx(ip1)
	MOVQ 80(SP), R9
	CMPQ R9, R8
	JGE  noh1
	MOVQ 72(SP), R11
	MOVL baseIdx+92(FP), R13
	ADDL R9, R13
	MOVL R13, (DI)(R11*4)

noh1:
	CMPQ R8, 8(SP)
	JGT  start

	// Fill table
	MOVL 0(SP), R13
	ADDL $2, R13           // current0 + 2
	MOVL R13, R9
	SUBL baseIdx+92(FP), R9
	HASH(R9, R11)
	MOVL R13, (DI)(R11*4)
	LEAQ -2(R8), R9
	HASH(R9, R11)
	MOVL baseIdx+92(FP), R13
	ADDL R9, R13
	MOVL R13, (DI)(R11*4)

reploop:
	CMPQ  R8, 8(SP)
	JGT   start
	MOVL  rep2+108(FP), R13
	TESTL R13, R13
	JEQ   start
	MOVL  baseIdx+92(FP), AX
	ADDL  R8, AX
	SUBL  R13, AX          // repIndex2 = idx(ip0) - rep2
	// indexOverlapCheck: (prefixStartIndex - 1) - repIndex2 >= 3
	MOVL  prefixStartIndex+88(FP), DX
	DECL  DX
	SUBL  AX, DX
	CMPL  DX, $3
	JB    start
	MOVQ  R15, R10
	MOVQ  48(SP), DX
	CMPL  AX, prefixStartIndex+88(FP)
	CMOVQCS 32(SP), R10
	CMOVQCS 40(SP), DX
	ADDQ  AX, R10          // repMatch2
	MOVL  (R10), CX
	CMPL  CX, (SI)(R8*1)
	JNE   start
	// rLength = count2Segments(ip0+4, repMatch2+4, iend, repEnd2, prefixStart) + 4
	ADDQ  $4, R10
	LEAQ  4(SI)(R8*1), R9
	COUNT2(cnt3call, cnt3done, cnt4call, cnt4done, cnt34done)
	LEAQ  4(R11), AX
	// swap rep1 and rep2
	MOVL  rep2+108(FP), CX
	MOVL  R14, rep2+108(FP)
	MOVL  CX, R14
	// hashTable[hash(ip0)] = idx(ip0)
	HASH(R8, R11)
	MOVL  baseIdx+92(FP), R13
	ADDL  R8, R13
	MOVL  R13, (DI)(R11*4)
	// store the sequence (no literals, repcode1ToOffBase)
	MOVQ  seqs+24(FP), R10
	MOVQ  nSeq+152(FP), R11
	MOVQ  $1, (R10)(R11*8)     // offBase 1, litLength 0
	LEAQ  -3(AX), CX           // mlBase = matchLength - MINMATCH
	MOVW  CX, 6(R10)(R11*8)
	CMPQ  CX, $0xFFFF
	JLS   repshort
	MOVL  $2, llType+176(FP)   // lltMatchLength
	MOVL  R11, llPos+180(FP)

repshort:
	INCQ R11
	MOVQ R11, nSeq+152(FP)
	ADDQ AX, R8
	MOVQ R8, anchor+144(FP)
	JMP  reploop

cleanup:
	MOVL R14, rep1Out+168(FP)
	MOVL rep2+108(FP), AX
	MOVL AX, rep2Out+172(FP)
	RET

bad:
	MOVQ $-1, nSeq+152(FP)
	RET
