//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASHL(pos, dst): long hash, (le64(b[pos:]) * prime8bytes) >> hShiftL.
// HASHS(pos, dst): short hash, ((le64(b[pos:]) << shiftS) * primeS) >> hShiftS,
// computed as (le64 * PRIMES) >> hShiftS with PRIMES = primeS << shiftS.
// Both clobber CX.
#define HASHL(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	MOVQ  $0xCF1BBCDCB7A56463, CX; \
	IMULQ CX, dst; \
	MOVQ  hShiftL+88(FP), CX; \
	SHRQ  CX, dst

#define HASHS(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	IMULQ PRIMES, dst; \
	MOVQ  hShiftS+104(FP), CX; \
	SHRQ  CX, dst

// Frame locals (hardware SP).
#define CURR 0(SP)
#define HL1 8(SP)
#define IDXS0 16(SP)
#define IDXL1 24(SP)
#define IP1 32(SP)
#define STEP 40(SP)
#define RETSEL 48(SP)
#define ILIMIT 56(SP)
#define MLEN 64(SP)
#define MATCHL1 72(SP)
#define PRIMES 80(SP)

// func compressBlockDoubleFastAsm(hashLong, hashSmall *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip, istart, iend, prefixLowest int, prefixLowestIndex, baseIdx, rep1, rep2 uint32,
//	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockDoubleFastNoDictGeneric's outer loop (C:
// zstd_double_fast.c:ZSTD_compressBlock_doubleFast_noDict_generic).
//
// Registers: SI b, DI hashLong, R15 hashSmall, R8 ip, R9 ip1, R10 step,
// R11 nextStep, R12 hl0, R13 idxl0, R14 offset1; AX/BX/CX/DX scratch.
// offset2, anchor, nSeq and nLits live in their argument/result slots.
// For a match: R8 ip, DX match position, AX mLength, R12 offBase.
TEXT ·compressBlockDoubleFastAsm(SB), NOSPLIT, $88-160
	MOVQ hashLong+0(FP), DI
	MOVQ hashSmall+8(FP), R15
	MOVQ b+16(FP), SI
	MOVQ ip+40(FP), R8
	MOVL rep1+80(FP), R14
	MOVQ istart+48(FP), AX
	MOVQ AX, anchor+120(FP)
	MOVQ $0, nSeq+128(FP)
	MOVQ $0, nLits+136(FP)
	MOVL $0, llType+152(FP)
	MOVL $0, llPos+156(FP)
	MOVQ iend+56(FP), AX
	SUBQ $8, AX
	MOVQ AX, ILIMIT
	MOVQ primeS+112(FP), AX
	MOVQ shiftS+96(FP), CX
	SHLQ CX, AX
	MOVQ AX, PRIMES

outer:
	MOVL $1, R10            // step
	LEAQ 256(R8), R11       // nextStep = ip + kStepIncr
	LEAQ 1(R8), R9          // ip1
	CMPQ R9, ILIMIT
	JGT  cleanup
	HASHL(R8, R12)
	MOVL (DI)(R12*4), R13   // idxl0

inner:
	HASHS(R8, BX)
	MOVL (R15)(BX*4), DX
	MOVL DX, IDXS0
	MOVL baseIdx+76(FP), AX
	ADDL R8, AX
	MOVL AX, CURR
	MOVL AX, (DI)(R12*4)    // hashLong[hl0] = curr
	MOVL AX, (R15)(BX*4)    // hashSmall[hs0] = curr

	// repcode at ip+1
	TESTL R14, R14
	JEQ   norep
	LEAQ  1(R8), AX
	SUBQ  R14, AX
	MOVL  (SI)(AX*1), DX
	CMPL  DX, 1(SI)(R8*1)
	JNE   norep
	// mLength = count(b, ip+1+4, ip+1+4-offset1, iend) + 4
	LEAQ  4(SI)(AX*1), R10  // ip+1+4-offset1 (step is not needed after a repcode)
	LEAQ  5(SI)(R8*1), R9
	MOVQ  iend+56(FP), R13
	SUBQ  R8, R13
	SUBQ  $5, R13
	CALL  ·fastCount(SB)
	LEAQ  4(R11), AX
	INCQ  R8
	MOVQ  R8, R9
	SUBQ  anchor+120(FP), R9
	MOVL  $1, R12           // repcode1ToOffBase
	MOVQ  $0, RETSEL           // return to matchstored
	JMP   store

norep:
	HASHL(R9, BX)
	MOVL BX, HL1

	// long match at ip
	CMPL R13, prefixLowestIndex+72(FP)
	JB   nolong
	MOVL R13, DX
	SUBL baseIdx+76(FP), DX
	CMPQ DX, R8
	JAE  bad
	MOVQ (SI)(DX*1), AX
	CMPQ AX, (SI)(R8*1)
	JNE  nolong
	MOVQ R9, IP1
	MOVQ R10, STEP
	// mLength = count(b, ip+8, matchl0+8, iend) + 8
	LEAQ 8(SI)(R8*1), R9
	LEAQ 8(SI)(DX*1), R10
	MOVQ iend+56(FP), R13
	SUBQ R8, R13
	SUBQ $8, R13
	CALL ·fastCount(SB)
	LEAQ 8(R11), AX
	MOVQ R8, R12
	SUBQ DX, R12            // offset
	JMP  catchup

nolong:
	MOVL (DI)(BX*4), AX
	MOVL AX, IDXL1          // idxl1 = hashLong[hl1]

	// short match at ip
	MOVL IDXS0, DX
	CMPL DX, prefixLowestIndex+72(FP)
	JB   noshort
	SUBL baseIdx+76(FP), DX
	CMPQ DX, R8
	JAE  bad
	MOVL (SI)(DX*1), AX
	CMPL AX, (SI)(R8*1)
	JEQ  searchlong

noshort:
	CMPQ R9, R11
	JLT  nostep
	INCQ R10
	ADDQ $256, R11

nostep:
	MOVQ R9, R8             // ip = ip1
	ADDQ R10, R9            // ip1 += step
	MOVL HL1, R12           // hl0 = hl1
	MOVL IDXL1, R13         // idxl0 = idxl1
	CMPQ R9, ILIMIT
	JGT  cleanup
	JMP  inner

searchlong:
	// C: _search_next_long. DX = matchs0.
	MOVQ R9, IP1
	MOVQ R10, STEP
	LEAQ 4(SI)(R8*1), R9
	LEAQ 4(SI)(DX*1), R10
	MOVQ iend+56(FP), R13
	SUBQ R8, R13
	SUBQ $4, R13
	CALL ·fastCount(SB)
	LEAQ 4(R11), AX         // mLength
	MOVQ R8, R12
	SUBQ DX, R12            // offset

	// long match at ip1
	MOVL IDXL1, BX
	CMPL BX, prefixLowestIndex+72(FP)
	JLS  catchup            // only if idxl1 > prefixLowestIndex
	SUBL baseIdx+76(FP), BX // matchl1
	MOVQ IP1, R9
	CMPQ BX, R9
	JAE  bad
	MOVQ (SI)(BX*1), CX
	CMPQ CX, (SI)(R9*1)
	JNE  catchup
	MOVQ AX, MLEN
	MOVQ BX, MATCHL1
	MOVL BX, CX
	// l1len = count(b, ip1+8, matchl1+8, iend) + 8
	MOVQ iend+56(FP), R13
	SUBQ R9, R13
	SUBQ $8, R13
	LEAQ 8(SI)(R9*1), R9
	LEAQ 8(SI)(CX*1), R10
	CALL ·fastCount(SB)
	LEAQ 8(R11), R11        // l1len
	MOVQ MATCHL1, BX
	MOVQ MLEN, AX
	CMPQ R11, AX
	JLE  catchup
	MOVQ IP1, R8            // ip = ip1
	MOVQ R11, AX
	MOVQ R8, R12
	SUBQ BX, R12            // offset = ip - matchl1
	MOVQ BX, DX             // matchs0 = matchl1

catchup:
	// while ip > anchor && match > prefixLowest && b[ip-1] == b[match-1]
	CMPQ    R8, anchor+120(FP)
	JLE     matchfound
	CMPQ    DX, prefixLowest+64(FP)
	JLE     matchfound
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(SI)(DX*1), BX
	CMPL    CX, BX
	JNE     matchfound
	DECQ    R8
	DECQ    DX
	INCQ    AX
	JMP     catchup

matchfound:
	// C: _match_found. R12 = offset.
	MOVL R14, rep2+84(FP)   // offset2 = offset1
	MOVL R12, R14           // offset1 = offset
	ADDL $3, R12            // offsetToOffBase(offset)
	CMPQ STEP, $4
	JGE  nohl1
	MOVL HL1, BX
	MOVQ IP1, CX
	ADDL baseIdx+76(FP), CX
	MOVL CX, (DI)(BX*4)     // hashLong[hl1] = idx(ip1)

nohl1:
	MOVQ R8, R9
	SUBQ anchor+120(FP), R9
	MOVQ $0, RETSEL
	JMP  store

matchstored:
	ADDQ AX, R8             // ip += mLength
	MOVQ R8, anchor+120(FP)
	CMPQ R8, ILIMIT
	JGT  outer

	// Complementary insertion
	MOVL CURR, R13
	ADDL $2, R13            // indexToInsert
	MOVL R13, R9
	SUBL baseIdx+76(FP), R9
	HASHL(R9, BX)
	MOVL R13, (DI)(BX*4)
	LEAQ -2(R8), R10
	HASHL(R10, BX)
	MOVL baseIdx+76(FP), DX
	ADDL R10, DX
	MOVL DX, (DI)(BX*4)
	HASHS(R9, BX)
	MOVL R13, (R15)(BX*4)
	LEAQ -1(R8), R10
	HASHS(R10, BX)
	MOVL baseIdx+76(FP), DX
	ADDL R10, DX
	MOVL DX, (R15)(BX*4)

reploop:
	// while ip <= ilimit && offset2 > 0 && read32(ip) == read32(ip-offset2)
	CMPQ  R8, ILIMIT
	JGT   outer
	MOVL  rep2+84(FP), DX
	TESTL DX, DX
	JEQ   outer
	MOVQ  R8, R10
	SUBQ  DX, R10
	MOVL  (SI)(R10*1), CX
	CMPL  CX, (SI)(R8*1)
	JNE   outer
	// rLength = count(b, ip+4, ip+4-offset2, iend) + 4
	LEAQ  4(SI)(R10*1), R10
	LEAQ  4(SI)(R8*1), R9
	MOVQ  iend+56(FP), R13
	SUBQ  R8, R13
	SUBQ  $4, R13
	CALL  ·fastCount(SB)
	LEAQ  4(R11), AX
	MOVL  rep2+84(FP), CX   // swap offset2 <=> offset1
	MOVL  R14, rep2+84(FP)
	MOVL  CX, R14
	HASHS(R8, BX)
	MOVL  baseIdx+76(FP), DX
	ADDL  R8, DX
	MOVL  DX, (R15)(BX*4)   // hashSmall[hashS(ip)] = idx(ip)
	HASHL(R8, BX)
	MOVL  DX, (DI)(BX*4)    // hashLong[hashL(ip)] = idx(ip)
	XORL  R9, R9            // no literals
	MOVL  $1, R12           // repcode1ToOffBase
	MOVQ  $1, RETSEL           // return to afterrep
	JMP   store

afterrep:
	ADDQ AX, R8
	MOVQ R8, anchor+120(FP)
	JMP  reploop

	// store: R9 litLen (literals from anchor), R12 offBase, AX matchLength;
	// returns to matchstored (RETSEL = 0) or afterrep (RETSEL = 1).
	// Clobbers R9-R11, R13, BX, CX, X0.
store:
	TESTQ R9, R9
	JEQ   storeseq
	MOVQ  lits+32(FP), R10
	ADDQ  nLits+136(FP), R10
	MOVQ  anchor+120(FP), R11
	ADDQ  SI, R11
	CALL  ·fastCopyLits(SB)
	ADDQ  R9, nLits+136(FP)

storeseq:
	MOVQ seqs+24(FP), R10
	MOVQ nSeq+128(FP), R11
	MOVL R12, (R10)(R11*8)     // offBase
	MOVW R9, 4(R10)(R11*8)     // litLength
	LEAQ -3(AX), CX            // mlBase = matchLength - MINMATCH
	MOVW CX, 6(R10)(R11*8)
	CMPQ R9, $0xFFFF
	JLS  nolongll
	MOVL $1, llType+152(FP)    // lltLiteralLength
	MOVL R11, llPos+156(FP)

nolongll:
	CMPQ CX, $0xFFFF
	JLS  nolongml
	MOVL $2, llType+152(FP)    // lltMatchLength
	MOVL R11, llPos+156(FP)

nolongml:
	INCQ R11
	MOVQ R11, nSeq+128(FP)
	CMPQ RETSEL, $0
	JEQ  matchstored
	JMP  afterrep

cleanup:
	MOVL R14, rep1Out+144(FP)
	MOVL rep2+84(FP), AX
	MOVL AX, rep2Out+148(FP)
	RET

bad:
	MOVQ $-1, nSeq+128(FP)
	RET
