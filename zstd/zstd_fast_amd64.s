//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASHF(pos, dst): dst = (le64(b[pos:]) * (prime << mlsShift)) >> CX,
// which is ((le64 << mlsShift) * prime) >> hShift mod 2^64 with CX = hShift.
#define HASHF(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	IMULQ 32(SP), dst; \
	SHRQ  CX, dst

// HASH(pos, dst) is HASHF loading CX = hShift first.
#define HASH(pos, dst) \
	MOVQ hShift+96(FP), CX; \
	HASHF(pos, dst)

// func compressBlockFastAsm(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
//	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockFastNoDictGeneric's loop (C: zstd_fast.c:
// ZSTD_compressBlock_fast_noDict_generic, _start to _cleanup).
//
// Registers in the search loop: SI b, DI hashTable, R8 ip0, R9 ip1, R10 ip2
// (ip3 = ip2 + 1), R11 hash0, R12 hash1, R13 matchIdx, R14 rep1,
// BX baseIdx, R15 b - baseIdx (so an index addresses b directly; no calls
// in the loop, so the GC never sees it), CX hShift, AX/DX scratch. BX, R15
// and CX are reloaded at start. rep2, anchor, nSeq and nLits live in their
// argument and result slots; the frame holds current0 at 0(SP), ilimit at
// 8(SP), step at 16(SP), nextStep at 24(SP) and prime << mlsShift at 32(SP).
// After a match: R8 ip0, DX match0, AX mLength, R12 offBase.
TEXT ·compressBlockFastAsm(SB), NOSPLIT, $40-152
	MOVQ hashTable+0(FP), DI
	MOVQ b+8(FP), SI
	MOVL rep1+72(FP), R14
	MOVQ ip0+32(FP), R8
	MOVQ istart+40(FP), AX
	MOVQ AX, anchor+112(FP)
	MOVQ $0, nSeq+120(FP)
	MOVQ $0, nLits+128(FP)
	MOVL $0, llType+144(FP)
	MOVL $0, llPos+148(FP)
	MOVQ iend+48(FP), AX
	SUBQ $8, AX
	MOVQ AX, 8(SP)          // ilimit = iend - HASH_READ_SIZE
	MOVQ prime+104(FP), AX
	MOVQ mlsShift+88(FP), CX
	SHLQ CX, AX
	MOVQ AX, 32(SP)

start:
	MOVQ stepSize+80(FP), AX
	MOVQ AX, 16(SP)         // step
	LEAQ (R8)(AX*1), R10
	LEAQ 128(R8), AX
	MOVQ AX, 24(SP)         // nextStep = ip0 + kStepIncr
	LEAQ 1(R8), R9
	LEAQ 1(R10), AX         // ip3
	CMPQ AX, 8(SP)
	JGE  cleanup
	MOVL baseIdx+68(FP), BX
	MOVQ SI, R15
	SUBQ BX, R15
	MOVQ hShift+96(FP), CX
	HASHF(R8, R11)
	HASHF(R9, R12)
	MOVL (DI)(R11*4), R13

inner:
	// rval = read32(ip2 - rep1)
	MOVQ R10, AX
	SUBQ R14, AX
	MOVL (SI)(AX*1), DX
	// current0 = idx(ip0); hashTable[hash0] = current0
	LEAL (R8)(BX*1), AX
	MOVL AX, 0(SP)
	MOVL AX, (DI)(R11*4)
	// repcode at ip2
	CMPL  DX, (SI)(R10*1)
	JNE   norep
	TESTL R14, R14
	JEQ   norep
	LEAL  (R9)(BX*1), AX
	MOVL  AX, (DI)(R12*4)  // hashTable[hash1] = idx(ip1)
	JMP   foundrep

norep:
	// prefixStartIndex <= matchIdx < current0 (AX), so R15 + matchIdx lies
	// in b[prefixStart:ip0] (prefixStartIndex >= baseIdx: the Go caller
	// falls back otherwise).
	CMPL R13, prefixStartIndex+64(FP)
	JB   miss1
	CMPL R13, AX
	JAE  bad
	MOVL (R15)(R13*1), DX
	CMPL DX, (SI)(R8*1)
	JNE  miss1
	LEAL (R9)(BX*1), AX
	MOVL AX, (DI)(R12*4)   // hashTable[hash1] = idx(ip1)
	JMP  foundoffset

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
	CMPL R13, prefixStartIndex+64(FP)
	JB   miss2
	CMPL R13, AX
	JAE  bad
	MOVL (R15)(R13*1), DX
	CMPL DX, (SI)(R8*1)
	JNE  miss2
	CMPQ 16(SP), $4
	JA   foundoffset
	LEAL (R9)(BX*1), AX
	MOVL AX, (DI)(R12*4)
	JMP  foundoffset

miss2:
	MOVL (DI)(R12*4), R13
	MOVQ R12, R11
	HASHF(R10, R12)
	MOVQ R9, R8            // ip0 = ip1
	MOVQ R10, R9           // ip1 = ip2
	MOVQ 16(SP), R10
	ADDQ R8, R10           // ip2 = ip0 + step (ip3 = ip1 + step = ip2 + 1)
	CMPQ R10, 24(SP)
	JLT  nostep
	INCQ 16(SP)
	ADDQ $128, 24(SP)

nostep:
	LEAQ 1(R10), AX
	CMPQ AX, 8(SP)
	JGE  cleanup
	JMP  inner

foundrep:
	MOVQ    R10, R8        // ip0 = ip2
	MOVQ    R8, DX
	SUBQ    R14, DX        // match0
	XORL    AX, AX
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(SI)(DX*1), R11
	CMPL    CX, R11
	JNE     rep0
	MOVL    $1, AX
	DECQ    R8
	DECQ    DX

rep0:
	ADDQ $4, AX
	MOVL $1, R12           // repcode1ToOffBase
	JMP  match

foundoffset:
	MOVL R13, DX
	SUBL BX, DX            // match0
	MOVL R14, rep2+76(FP)  // rep2 = rep1
	MOVQ R8, R14
	SUBQ DX, R14           // rep1 = ip0 - match0
	LEAQ 3(R14), R12       // offsetToOffBase(rep1)
	MOVL $4, AX

back:
	CMPQ    R8, anchor+112(FP)
	JLE     match
	CMPQ    DX, prefixStart+56(FP)
	JLE     match
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(SI)(DX*1), R11
	CMPL    CX, R11
	JNE     match
	DECQ    R8
	DECQ    DX
	INCQ    AX
	JMP     back

match:
	// mLength += count(b, ip0+mLength, match0+mLength, iend)
	LEAQ (R8)(AX*1), R9
	MOVQ iend+48(FP), R13
	SUBQ R9, R13
	ADDQ SI, R9
	LEAQ (DX)(AX*1), R10
	ADDQ SI, R10
	CALL ·fastCount(SB)
	ADDQ R11, AX

	// storeSeq(ss, ip0-anchor, b[anchor:], offBase, mLength)
	MOVQ R8, R9
	SUBQ anchor+112(FP), R9
	XORL R15, R15          // return to aftermatch
	JMP  store

aftermatch:
	ADDQ AX, R8            // ip0 += mLength
	MOVQ R8, anchor+112(FP)
	CMPQ R8, 8(SP)
	JGT  start

	// Fill table
	MOVL 0(SP), R13
	ADDL $2, R13           // current0 + 2
	MOVL R13, R9
	SUBL baseIdx+68(FP), R9
	HASH(R9, R11)
	MOVL R13, (DI)(R11*4)
	LEAQ -2(R8), R9
	HASH(R9, R11)
	MOVL baseIdx+68(FP), R13
	ADDL R9, R13
	MOVL R13, (DI)(R11*4)

	MOVL  rep2+76(FP), R13
	TESTL R13, R13
	JEQ   start            // repOffset2 == 0 means invalidated

reploop:
	CMPQ R8, 8(SP)
	JGT  start
	MOVL rep2+76(FP), R13
	MOVQ R8, R9
	SUBQ R13, R9
	MOVL (SI)(R9*1), CX
	CMPL CX, (SI)(R8*1)
	JNE  start
	// rLength = count(b, ip0+4, ip0+4-rep2, iend) + 4
	LEAQ 4(SI)(R9*1), R10
	LEAQ 4(SI)(R8*1), R9
	MOVQ iend+48(FP), R13
	SUBQ R8, R13
	SUBQ $4, R13
	CALL ·fastCount(SB)
	LEAQ 4(R11), AX
	// swap rep1 and rep2
	MOVL rep2+76(FP), CX
	MOVL R14, rep2+76(FP)
	MOVL CX, R14
	// hashTable[hash(ip0)] = idx(ip0)
	HASH(R8, R11)
	MOVL baseIdx+68(FP), R13
	ADDL R8, R13
	MOVL R13, (DI)(R11*4)
	XORL R9, R9            // no literals
	MOVL $1, R12           // repcode1ToOffBase
	MOVL $1, R15           // return to afterrep
	JMP  store

afterrep:
	ADDQ AX, R8
	MOVQ R8, anchor+112(FP)
	JMP  reploop

	// store: R9 litLen (literals from anchor), R12 offBase, AX matchLength;
	// returns to aftermatch (R15 = 0) or afterrep (R15 = 1).
	// Clobbers R9-R11, R13, BX, CX, X0.
store:
	TESTQ R9, R9
	JEQ   storeseq
	MOVQ  lits+24(FP), R10
	ADDQ  nLits+128(FP), R10
	MOVQ  anchor+112(FP), R11
	ADDQ  SI, R11
	CALL  ·fastCopyLits(SB)
	ADDQ  R9, nLits+128(FP)

storeseq:
	MOVQ seqs+16(FP), R10
	MOVQ nSeq+120(FP), R11
	MOVL R12, (R10)(R11*8)     // offBase
	MOVW R9, 4(R10)(R11*8)     // litLength
	LEAQ -3(AX), CX            // mlBase = matchLength - MINMATCH
	MOVW CX, 6(R10)(R11*8)
	CMPQ R9, $0xFFFF
	JLS  nolongll
	MOVL $1, llType+144(FP)    // lltLiteralLength
	MOVL R11, llPos+148(FP)

nolongll:
	CMPQ CX, $0xFFFF
	JLS  nolongml
	MOVL $2, llType+144(FP)    // lltMatchLength
	MOVL R11, llPos+148(FP)

nolongml:
	INCQ  R11
	MOVQ  R11, nSeq+120(FP)
	TESTL R15, R15
	JEQ   aftermatch
	JMP   afterrep

cleanup:
	MOVL R14, rep1Out+136(FP)
	MOVL rep2+76(FP), AX
	MOVL AX, rep2Out+140(FP)
	RET

bad:
	MOVQ $-1, nSeq+120(FP)
	RET

// fastCopyLits copies R9 (> 0) bytes from R11 to R10 without reading or
// writing outside either range. Clobbers CX, R13, X0.
TEXT ·fastCopyLits(SB), NOSPLIT, $0-0
	CMPQ R9, $16
	JB   small
	XORL R13, R13

loop16:
	MOVOU (R11)(R13*1), X0
	MOVOU X0, (R10)(R13*1)
	ADDQ  $16, R13
	LEAQ  16(R13), CX
	CMPQ  CX, R9
	JLS   loop16
	MOVOU -16(R11)(R9*1), X0   // last 16 bytes (may overlap)
	MOVOU X0, -16(R10)(R9*1)
	RET

small:
	CMPQ R9, $8
	JB   lt8
	MOVQ (R11), CX
	MOVQ CX, (R10)
	MOVQ -8(R11)(R9*1), CX
	MOVQ CX, -8(R10)(R9*1)
	RET

lt8:
	CMPQ R9, $4
	JB   lt4
	MOVL (R11), CX
	MOVL CX, (R10)
	MOVL -4(R11)(R9*1), CX
	MOVL CX, -4(R10)(R9*1)
	RET

lt4:
	MOVB (R11), CX
	MOVB CX, (R10)
	CMPQ R9, $2
	JB   done
	MOVW -2(R11)(R9*1), CX
	MOVW CX, -2(R10)(R9*1)

done:
	RET

// fastCount returns in R11 the length of the common prefix of R9[0:R13]
// and R10[0:R13] (C: ZSTD_count). Reads only within those ranges.
// Once the first 8 bytes match it compares 32 bytes per step with AVX2
// when useAVX2 is set. Clobbers CX, BX, Y0.
TEXT ·fastCount(SB), NOSPLIT, $0-0
	XORL R11, R11
	CMPQ R13, $8
	JLT  tail
	MOVQ (R10), CX
	XORQ (R9), CX
	JNE  diff
	MOVL $8, R11
	CMPB ·useAVX2(SB), $0
	JEQ  cnt8

cnt32:
	LEAQ 32(R11), CX
	CMPQ CX, R13
	JGT  cnt32end
	VMOVDQU   (R10)(R11*1), Y0
	VPCMPEQB  (R9)(R11*1), Y0, Y0
	VPMOVMSKB Y0, CX
	INCL      CX               // all equal: 0xFFFFFFFF + 1 = 0; else the lowest set bit is the first mismatch
	JNE       cnt32diff
	ADDQ      $32, R11
	JMP       cnt32

cnt32diff:
	VZEROUPPER
	BSFL CX, CX
	ADDQ CX, R11
	RET

cnt32end:
	VZEROUPPER

cnt8:
	LEAQ 8(R11), CX
	CMPQ CX, R13
	JGT  tail
	MOVQ (R10)(R11*1), CX
	XORQ (R9)(R11*1), CX
	JNE  diff
	ADDQ $8, R11
	JMP  cnt8

diff:
	BSFQ CX, CX
	SHRQ $3, CX
	ADDQ CX, R11
	RET

tail:
	CMPQ    R11, R13
	JGE     out
	MOVBLZX (R10)(R11*1), CX
	MOVBLZX (R9)(R11*1), BX
	CMPL    CX, BX
	JNE     out
	INCQ    R11
	JMP     tail

out:
	RET
