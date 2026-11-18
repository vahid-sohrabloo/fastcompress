//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Frame locals (hardware SP).
#define DBIAS 0(SP)      // d - dictBaseIdx: an extDict index's address
#define BBIAS 8(SP)      // b - baseIdx: a prefix index's address
#define DENDP 16(SP)     // address of dictEnd
#define IENDP 24(SP)     // address of iend
#define DSTARTP 32(SP)   // address of dictStart
#define PSTARTP 40(SP)   // address of prefixStart
#define ILIMIT 48(SP)
#define PRIMES 56(SP)    // primeS << shiftS
#define CURR 64(SP)
#define C2END 72(SP)     // COUNT2's
#define C2LEN 80(SP)     // COUNT2's
#define PS7 88(SP)       // prefixStartIndex - 7
#define PS3 96(SP)       // prefixStartIndex - 3
#define MLOW 104(SP)     // the match's segment's low end
#define MPTR 112(SP)     // the match's address
#define OFFSET 120(SP)
#define MINLEN 128(SP)
#define CURR2 136(SP)

// HASHL(pos, dst): long hash, (le64(b[pos:]) * prime8bytes) >> hShiftL.
// HASHS(pos, dst): short hash, (le64(b[pos:]) * PRIMES) >> hShiftS.
// Both clobber CX.
#define HASHL(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	MOVQ  $0xCF1BBCDCB7A56463, CX; \
	IMULQ CX, dst; \
	MOVQ  hShiftL+112(FP), CX; \
	SHRQ  CX, dst

#define HASHS(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	IMULQ PRIMES, dst; \
	MOVQ  hShiftS+128(FP), CX; \
	SHRQ  CX, dst

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
// ZSTD_count_2segments(ip, match, iend, mEnd, prefixStart)). Pointers are
// addresses. Clobbers R9, R10, R13, CX, BX and Y0.
#define COUNT2(c1, d1, c2, d2, done) \
	MOVQ    IENDP, R13; \
	SUBQ    R9, R13; \
	MOVQ    DX, CX; \
	SUBQ    R10, CX; \
	CMPQ    CX, R13; \
	CMOVQLT CX, R13; \
	MOVQ    DX, C2END; \
	COUNTI(c1, d1); \
	LEAQ    (R10)(R11*1), CX; \
	CMPQ    CX, C2END; \
	JNE     done; \
	MOVQ    R11, C2LEN; \
	ADDQ    R11, R9; \
	MOVQ    PSTARTP, R10; \
	MOVQ    IENDP, R13; \
	SUBQ    R9, R13; \
	COUNTI(c2, d2); \
	ADDQ    C2LEN, R11; \
done:

// SEGBASE(idx, dst): dst = the address bias of idx's segment (DBIAS below
// prefixStartIndex, else BBIAS), so dst + idx is idx's address.
#define SEGBASE(idx, dst) \
	MOVQ    BBIAS, dst; \
	CMPL    idx, prefixStartIndex+88(FP); \
	CMOVQCS DBIAS, dst

// func compressBlockDoubleFastExtDictAsm(hashLong, hashSmall *uint32, b, d *byte, seqs *seqDef, lits *byte,
//	istart, iend, prefixStart, dictStart, dictEnd int,
//	prefixStartIndex, baseIdx, dictStartIndex, dictBaseIdx, rep1, rep2 uint32,
//	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockDoubleFastExtDictGeneric's loop (C:
// zstd_double_fast.c:ZSTD_compressBlock_doubleFast_extDict_generic); the
// store of a sequence is compressBlockFastAsm's.
//
// Registers: SI b, DI hashLong, R15 hashSmall, R8 ip, R14 offset1, R11
// read64(ip), AX curr, DX matchIndex, R13 matchLongIndex, the rest
// scratch. offset2, anchor, nSeq and nLits live in their argument and
// result slots, the rest in the frame (see the defines). An index below
// prefixStartIndex addresses the extDict from DBIAS, any other the prefix
// from BBIAS (no calls in the search loop, so the GC never sees them).
// For a match: R8 ip, AX mLength, R12 offBase.
//
// BOUNDS (the Go caller checks: iend <= len(b), dictEnd <= len(d),
// baseIdx <= prefixStartIndex = baseIdx + prefixStart <= baseIdx + istart,
// dictBaseIdx <= dictStartIndex = dictBaseIdx + dictStart,
// prefixStartIndex = dictBaseIdx + dictEnd, 8 <= dictEnd, the table sizes,
// the room in seqs and lits):
// (1) Input reads: 8 bytes at ip, ip + 1 (searched positions are
//     < ilimit = iend - 8) and, for the insertions after a match, at
//     curr + 2 < ip, ip - 2 and ip - 1 with ip <= ilimit; literals from
//     anchor..ip (16-byte copies only when anchor + 16 <= iend); counts
//     stop at iend; the backward extension reads b[ip - 1] with
//     ip > anchor >= istart >= 0.
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
TEXT ·compressBlockDoubleFastExtDictAsm(SB), NOSPLIT, $144-184
	MOVQ hashLong+0(FP), DI
	MOVQ hashSmall+8(FP), R15
	MOVQ b+16(FP), SI
	MOVQ d+24(FP), AX
	MOVQ AX, DX
	ADDQ dictEnd+80(FP), DX
	MOVQ DX, DENDP
	MOVQ AX, DX
	ADDQ dictStart+72(FP), DX
	MOVQ DX, DSTARTP
	MOVL dictBaseIdx+100(FP), DX
	SUBQ DX, AX
	MOVQ AX, DBIAS
	MOVQ SI, AX
	MOVL baseIdx+92(FP), DX
	SUBQ DX, AX
	MOVQ AX, BBIAS
	MOVQ SI, AX
	ADDQ iend+56(FP), AX
	MOVQ AX, IENDP
	MOVQ SI, AX
	ADDQ prefixStart+64(FP), AX
	MOVQ AX, PSTARTP
	MOVL prefixStartIndex+88(FP), AX
	SUBL $7, AX
	MOVL AX, PS7
	ADDL $4, AX
	MOVL AX, PS3
	MOVL rep1+104(FP), R14
	MOVQ istart+48(FP), R8
	MOVQ R8, anchor+144(FP)
	MOVQ $0, nSeq+152(FP)
	MOVQ $0, nLits+160(FP)
	MOVL $0, llType+176(FP)
	MOVL $0, llPos+180(FP)
	MOVQ iend+56(FP), AX
	SUBQ $8, AX
	MOVQ AX, ILIMIT
	MOVQ primeS+136(FP), AX
	MOVQ shiftS+120(FP), CX
	SHLQ CX, AX
	MOVQ AX, PRIMES
	JMP  loop
	PCALIGN $64

loop:
	CMPQ  R8, ILIMIT
	JGE   cleanup
	MOVQ  (SI)(R8*1), R11   // read64(ip)
	MOVQ  R11, BX
	IMULQ PRIMES, BX
	MOVQ  hShiftS+128(FP), CX
	SHRQ  CX, BX            // hSmall
	MOVQ  R11, R12
	MOVQ  $0xCF1BBCDCB7A56463, CX
	IMULQ CX, R12
	MOVQ  hShiftL+112(FP), CX
	SHRQ  CX, R12           // hLong
	MOVL  (R15)(BX*4), DX   // matchIndex
	MOVL  (DI)(R12*4), R13  // matchLongIndex
	MOVL  baseIdx+92(FP), AX
	ADDL  R8, AX            // curr
	MOVL  AX, (R15)(BX*4)
	MOVL  AX, (DI)(R12*4)
	MOVL  AX, CURR

	// repcode at ip+1: repIndex = curr + 1 - offset1
	LEAL 1(AX), R9
	SUBL R14, R9
	MOVL prefixStartIndex+88(FP), CX
	DECL CX
	SUBL R9, CX
	CMPL CX, $3
	JB   norep              // !indexOverlapCheck
	LEAL 1(AX), CX
	SUBL dictStartIndex+96(FP), CX
	CMPL R14, CX
	JA   norep              // offset1 > curr + 1 - dictStartIndex
	SEGBASE(R9, R10)
	MOVL (R10)(R9*1), CX
	CMPL CX, 1(SI)(R8*1)
	JEQ  foundrep

norep:
	// long match at ip
	CMPL R13, dictStartIndex+96(FP)
	JLS  nolong
	CMPL R13, AX
	JAE  bad
	MOVL R13, CX
	SUBL PS7, CX
	CMPL CX, $7
	JB   bad
	SEGBASE(R13, R10)
	CMPQ R11, (R10)(R13*1)
	JEQ  foundlong

nolong:
	// short match at ip
	CMPL DX, dictStartIndex+96(FP)
	JLS  noshort
	CMPL DX, AX
	JAE  bad
	MOVL DX, CX
	SUBL PS3, CX
	CMPL CX, $3
	JB   bad
	SEGBASE(DX, R10)
	CMPL R11, (R10)(DX*1)
	JEQ  foundshort

noshort:
	MOVQ R8, CX
	SUBQ anchor+144(FP), CX
	SHRQ $8, CX             // kSearchStrength
	LEAQ 1(R8)(CX*1), R8
	JMP  loop

foundrep:
	// mLength = count2Segments(ip+1+4, repMatch+4, iend, repEnd, prefixStart) + 4
	ADDQ    R9, R10         // repMatch
	MOVQ    IENDP, DX
	CMPL    R9, prefixStartIndex+88(FP)
	CMOVQCS DENDP, DX
	ADDQ    $4, R10
	LEAQ    5(SI)(R8*1), R9
	COUNT2(cnt1call, cnt1done, cnt2call, cnt2done, cnt12done)
	LEAQ    4(R11), AX
	INCQ    R8
	MOVL    $1, R12         // repcode1ToOffBase
	JMP     store

foundlong:
	MOVL AX, CX
	SUBL R13, CX
	MOVL CX, OFFSET         // offset = curr - matchLongIndex
	MOVL $8, CX
	JMP  lmatch

foundshort:
	MOVQ  1(SI)(R8*1), R11  // read64(ip+1)
	MOVQ  R11, R12
	MOVQ  $0xCF1BBCDCB7A56463, CX
	IMULQ CX, R12
	MOVQ  hShiftL+112(FP), CX
	SHRQ  CX, R12           // h3
	MOVL  (DI)(R12*4), R13  // matchIndex3
	INCL  AX                // curr + 1
	MOVL  AX, (DI)(R12*4)
	CMPL  R13, dictStartIndex+96(FP)
	JLS   short4
	CMPL  R13, AX
	JAE   bad
	MOVL  R13, CX
	SUBL  PS7, CX
	CMPL  CX, $7
	JB    bad
	SEGBASE(R13, R10)
	CMPQ  R11, (R10)(R13*1)
	JNE   short4
	INCQ  R8                // ip++
	SUBL  R13, AX
	MOVL  AX, OFFSET        // offset = curr + 1 - matchIndex3
	MOVL  $8, CX
	JMP   lmatch

short4:
	MOVL DX, R13
	MOVL CURR, AX
	SUBL R13, AX
	MOVL AX, OFFSET         // offset = curr - matchIndex
	MOVL $4, CX

lmatch:
	// R8 ip, R13 the match's index, CX its length so far (8 or 4):
	// mLength = count2Segments(ip+CX, match+CX, ...) + CX, then the
	// backward extension (C: catch up).
	MOVQ    CX, MINLEN
	MOVQ    BBIAS, R10
	MOVQ    IENDP, DX
	MOVQ    PSTARTP, BX
	CMPL    R13, prefixStartIndex+88(FP)
	CMOVQCS DBIAS, R10
	CMOVQCS DENDP, DX
	CMOVQCS DSTARTP, BX
	ADDQ    R13, R10        // match
	MOVQ    R10, MPTR
	MOVQ    BX, MLOW
	LEAQ    (SI)(R8*1), R9
	ADDQ    CX, R9
	ADDQ    CX, R10
	COUNT2(cnt3call, cnt3done, cnt4call, cnt4done, cnt34done)
	MOVQ    MINLEN, AX
	ADDQ    R11, AX
	MOVQ    MPTR, DX
	MOVQ    MLOW, BX

catchup:
	CMPQ    R8, anchor+144(FP)
	JLE     caught
	CMPQ    DX, BX
	JLS     caught
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(DX), R9
	CMPL    CX, R9
	JNE     caught
	DECQ    R8
	DECQ    DX
	INCQ    AX
	JMP     catchup

caught:
	MOVL R14, rep2+108(FP)  // offset2 = offset1
	MOVL OFFSET, R14        // offset1 = offset
	LEAQ 3(R14), R12        // offsetToOffBase(offset)

store:
	// storeSeq(ss, ip-anchor, b[anchor:], offBase R12, mLength AX)
	MOVQ  R8, R9
	SUBQ  anchor+144(FP), R9
	TESTQ R9, R9
	JEQ   storeseq
	MOVQ  lits+40(FP), R10
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
	MOVQ seqs+32(FP), R10
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

	ADDQ AX, R8             // ip += mLength
	MOVQ R8, anchor+144(FP)
	CMPQ R8, ILIMIT
	JGT  loop

	// Complementary insertion
	MOVL CURR, R13
	ADDL $2, R13            // indexToInsert = curr + 2
	MOVL R13, R9
	SUBL baseIdx+92(FP), R9
	HASHL(R9, R11)
	MOVL R13, (DI)(R11*4)
	HASHS(R9, R11)
	MOVL R13, (R15)(R11*4)
	LEAQ -2(R8), R9
	HASHL(R9, R11)
	MOVL baseIdx+92(FP), R13
	ADDL R9, R13
	MOVL R13, (DI)(R11*4)
	LEAQ -1(R8), R9
	HASHS(R9, R11)
	MOVL baseIdx+92(FP), R13
	ADDL R9, R13
	MOVL R13, (R15)(R11*4)

reploop:
	CMPQ R8, ILIMIT
	JGT  loop
	MOVL baseIdx+92(FP), AX
	ADDL R8, AX             // current2
	MOVL rep2+108(FP), CX
	MOVL AX, R9
	SUBL CX, R9             // repIndex2
	MOVL prefixStartIndex+88(FP), DX
	DECL DX
	SUBL R9, DX
	CMPL DX, $3
	JB   loop               // !indexOverlapCheck
	MOVL AX, DX
	SUBL dictStartIndex+96(FP), DX
	CMPL CX, DX
	JA   loop               // offset2 > current2 - dictStartIndex
	MOVQ    BBIAS, R10
	MOVQ    IENDP, DX
	CMPL    R9, prefixStartIndex+88(FP)
	CMOVQCS DBIAS, R10
	CMOVQCS DENDP, DX
	ADDQ    R9, R10         // repMatch2
	MOVL    (R10), CX
	CMPL    CX, (SI)(R8*1)
	JNE     loop
	MOVL    AX, CURR2
	// repLength2 = count2Segments(ip+4, repMatch2+4, iend, repEnd2, prefixStart) + 4
	ADDQ    $4, R10
	LEAQ    4(SI)(R8*1), R9
	COUNT2(cnt5call, cnt5done, cnt6call, cnt6done, cnt56done)
	LEAQ    4(R11), AX
	// swap offset1 and offset2
	MOVL    rep2+108(FP), CX
	MOVL    R14, rep2+108(FP)
	MOVL    CX, R14
	// hashSmall[hashS(ip)] = hashLong[hashL(ip)] = current2
	MOVL    CURR2, R13
	HASHS(R8, R11)
	MOVL    R13, (R15)(R11*4)
	HASHL(R8, R11)
	MOVL    R13, (DI)(R11*4)
	// store the sequence (no literals, repcode1ToOffBase)
	MOVQ    seqs+32(FP), R10
	MOVQ    nSeq+152(FP), R11
	MOVQ    $1, (R10)(R11*8)   // offBase 1, litLength 0
	LEAQ    -3(AX), CX         // mlBase = matchLength - MINMATCH
	MOVW    CX, 6(R10)(R11*8)
	CMPQ    CX, $0xFFFF
	JLS     repshort
	MOVL    $2, llType+176(FP) // lltMatchLength
	MOVL    R11, llPos+180(FP)

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
