//go:build amd64 && !appengine && !noasm && !purego && gc

// The main loop of LZ4MID_compress (level 2; lz4 v1.11.1 lib/lz4hc.c,
// Copyright (c) Yann Collet, BSD 2-Clause) for a prefix without external or
// attached dictionary, notLimited or limitedOutput. It follows midCompress
// in lz4hc.go statement for statement and returns before the last literals.

#include "textflag.h"

// LZ4MID_hash8Ptr: ((le64 << 8) * 58295818150454627) >> (64-14); R15 scratch
#define HASH8(ptr, dst) \
	MOVQ  (ptr), dst; \
	SHLQ  $8, dst; \
	MOVQ  $58295818150454627, R15; \
	IMULQ R15, dst; \
	SHRQ  $50, dst

// LZ4MID_hash4Ptr: (le32 * 2654435761) >> (32-14)
#define HASH4(ptr, dst) \
	MOVL   (ptr), dst; \
	IMUL3L $-1640531535, dst, dst; \
	SHRL   $18, dst

// COUNTMATCH(ipreg, ipIndexReg, posReg, out): LZ4MID_countMatch without
// external dictionary: 0 if ipIndex - pos > LZ4_DISTANCE_MAX or pos <
// prefixIdx, else LZ4_count(ip, win + pos - prefixIdx, matchlimit).
// Clobbers AX, BX, CX, DX.
#define COUNTMATCH(ipreg, ipIndexReg, posReg, out, done) \
	XORL  out, out; \
	MOVL  ipIndexReg, AX; \
	SUBL  posReg, AX; \
	CMPL  AX, $65535; \
	JHI   done; \
	CMPL  posReg, R13; \
	JLO   done; \
	MOVL  posReg, BX; \
	SUBL  R13, BX; \
	ADDQ  R9, BX; \
	MOVQ  ipreg, AX; \
	CALL  midCount<>(SB); \
	MOVQ  CX, out; \
done:

// func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int) (nip, anchor, op, status int)
//
// win is the prefix (prefixPtr = &win[0], index i is position prefixIdx + i);
// the input is win[ip:iend]; t is the LZ4MID tables (hash4 then hash8, 16384
// entries each); olimit < 0 is notLimited. It returns ip, anchor and op at
// the end of the main loop (status 0), or status 1 when a sequence does not
// fit (limitedOutput: C returns 0).
//
// Registers: SI ip, DI op, R8 anchor, R9 win, R10 hash4 table, R11 mflimit,
// R12 matchlimit, R13 prefixIdx, R14 ipIndex, R15 hash scratch; AX, BX, CX,
// DX scratch; locals 0 olimit, 8 dstEnd, 16 ilimitIdx, 24 matchLength,
// 32 matchDistance, 40 hash8 table, 48 dst, 56 and 64 the hash and the
// candidate at ip+1.
//
// Preconditions (checked by midCompress): iend - ip >= LZ4_minLength,
// iend <= len(win), and the input is the end of win (iend = len(win));
// every table entry is a position below the current one (prefixIdx +
// index); len(dst) >= olimit (limited) or LZ4_compressBound(iend - ip).
//
// BOUNDS:
// (1) win reads: ip <= mflimit = iend-12 in the loop; hashes read 8 bytes at
//     ip, ip+1 and ip+2 (catch back moves ip down only), and at ip-5, ip-3,
//     ip-2 and 4 bytes at ip-1 after a match whose end is below ilimit+2,
//     i.e. ip-2 < iend-8, so these reads end below iend; ip-5 is read only
//     when ip > 5 (C's check), ip-3 > anchor >= 0 after a match of at least
//     4 bytes. Candidates are read only at positions >= prefixIdx (index
//     >= 0) and below ip (table entries are earlier positions), by
//     midCount, which reads below matchlimit = iend-5 on the ip side and at
//     the same distance behind on the match side. The catch back reads
//     ip[-1] (ip > anchor >= 0) and ip[-dist-1] with ip > dist.
// (2) dst writes: before a sequence, op + litLen/255 + litLen + 8 <= olimit
//     (limited) is checked; with notLimited dst holds the bound. Every
//     write is also checked against dstEnd (status 2, which cannot happen
//     within these limits): the literal run bytes, the offset, the match run
//     below op + mlCode/255 + 2; the literals are copied in 8-byte chunks
//     when op+litLen+8 <= dstEnd, else byte by byte.
TEXT ·midCompressAsm(SB), NOSPLIT, $72-120
	MOVQ win_base+0(FP), R9
	MOVQ ip+24(FP), SI
	ADDQ R9, SI
	MOVQ SI, R8                  // anchor
	MOVQ iend+32(FP), AX
	ADDQ R9, AX                  // iend
	LEAQ -12(AX), R11            // mflimit
	LEAQ -5(AX), R12             // matchlimit
	LEAQ -8(AX), BX              // ilimit
	SUBQ R9, BX
	MOVQ prefixIdx+40(FP), R13
	ADDL R13, BX
	MOVQ BX, 16(SP)              // ilimitIdx
	MOVQ t+48(FP), R10
	LEAQ 65536(R10), BX
	MOVQ BX, 40(SP)              // hash8 table
	MOVQ dst_base+56(FP), DI
	MOVQ DI, 48(SP)
	MOVQ dst_len+64(FP), BX
	ADDQ DI, BX
	MOVQ BX, 8(SP)               // dstEnd
	MOVQ olimit+80(FP), BX
	TESTQ BX, BX
	JGE  limited
	MOVQ $-1, BX
	JMP  olimitSet

limited:
	ADDQ DI, BX

olimitSet:
	MOVQ BX, 0(SP)

loop:
	CMPQ SI, R11
	JHI  done                    // ip > mflimit
	MOVQ SI, R14
	SUBQ R9, R14
	ADDL R13, R14                // ipIndex

	// search long match
	HASH8(SI, DX)
	MOVQ 40(SP), BX
	MOVL (BX)(DX*4), CX          // pos8
	MOVL R14, (BX)(DX*4)
	MOVQ CX, 32(SP)
	COUNTMATCH(SI, R14, 32(SP), DX, cm8)
	MOVQ DX, 24(SP)
	MOVL R14, AX
	SUBL 32(SP), AX
	MOVQ AX, 32(SP)              // matchDistance = ipIndex - pos8
	CMPQ DX, $4
	JHS  found

	// search short match
	HASH4(SI, DX)
	MOVL (R10)(DX*4), CX         // pos4
	MOVL R14, (R10)(DX*4)
	MOVQ CX, 32(SP)
	COUNTMATCH(SI, R14, 32(SP), DX, cm4)
	MOVQ DX, 24(SP)
	MOVL R14, AX
	SUBL 32(SP), AX
	MOVQ AX, 32(SP)              // matchDistance = ipIndex - pos4
	CMPQ DX, $4
	JHS  found

	// no match: skip faster over incompressible data
	MOVQ SI, AX
	SUBQ R8, AX
	SHRQ $9, AX
	LEAQ 1(SI)(AX*1), SI
	JMP  loop

found:
	CMPQ 24(SP), $8
	JHS  encode
	CMPQ SI, R11
	JHS  encode                  // ip < mflimit only
	LEAQ 1(SI), AX
	HASH8(AX, DX)
	MOVQ 40(SP), BX
	MOVL (BX)(DX*4), CX          // pos8 at ip+1
	MOVQ DX, 56(SP)
	MOVQ CX, 64(SP)
	LEAQ 1(SI), DX
	LEAL 1(R14), AX
	MOVQ AX, R15
	MOVL CX, AX
	// ml2 = LZ4MID_countMatch(ip+1, ipIndex+1, pos8)
	XORL CX, CX
	MOVL R15, BX
	SUBL AX, BX
	CMPL BX, $65535
	JHI  ml2done
	CMPL AX, R13
	JLO  ml2done
	MOVL AX, BX
	SUBL R13, BX
	ADDQ R9, BX
	MOVQ DX, AX
	CALL midCount<>(SB)

ml2done:
	MOVQ 64(SP), AX              // pos8
	MOVQ 56(SP), DX              // h8
	CMPQ CX, 24(SP)
	JLS  encode                  // ml2 <= matchLength
	CMPL AX, R14
	JEQ  encode                  // pos8 == ipIndex
	MOVQ 40(SP), BX
	LEAL 1(R14), R15
	MOVL R15, (BX)(DX*4)
	INCQ SI
	MOVQ CX, 24(SP)
	SUBL AX, R15
	MOVQ R15, 32(SP)             // matchDistance = ipIndex + 1 - pos8

encode:
	// catch back
	MOVQ 32(SP), CX              // matchDistance
	MOVQ 24(SP), DX              // matchLength

catchBack:
	CMPQ SI, R8
	JLS  catchDone
	MOVQ SI, AX
	SUBQ R9, AX
	CMPQ AX, CX
	JLS  catchDone               // (ip - prefixPtr) <= matchDistance
	MOVQ SI, AX
	SUBQ CX, AX
	MOVB -1(AX), BX
	CMPB BL, -1(SI)
	JNE  catchDone
	DECQ SI
	INCQ DX
	JMP  catchBack

catchDone:
	MOVQ DX, 24(SP)

	// fill table with beginning of match
	MOVQ SI, R14
	SUBQ R9, R14
	ADDL R13, R14                // startMatchIdx
	MOVQ 40(SP), BX
	LEAQ 1(SI), AX
	HASH8(AX, DX)
	LEAL 1(R14), CX
	MOVL CX, (BX)(DX*4)
	LEAQ 2(SI), AX
	HASH8(AX, DX)
	LEAL 2(R14), CX
	MOVL CX, (BX)(DX*4)
	LEAQ 1(SI), AX
	HASH4(AX, DX)
	LEAL 1(R14), CX
	MOVL CX, (R10)(DX*4)

	// LZ4HC_encodeSequence(matchLength, matchDistance)
	MOVQ SI, CX
	SUBQ R8, CX                  // litLen
	MOVQ CX, AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX                 // litLen / 255
	LEAQ 9(DI)(CX*1), DX         // op+1 (token) + litLen + 2+1+LASTLITERALS
	ADDQ AX, DX
	CMPQ DX, 0(SP)
	JHI  fail
	MOVQ DI, R14                 // token
	INCQ DI
	CMPQ CX, $15
	JHS  litRun
	MOVQ CX, AX
	SHLQ $4, AX
	MOVB AL, (R14)
	JMP  copyLits

litRun:
	MOVB $0xF0, (R14)
	LEAQ -15(CX), AX
	LEAQ (DI)(AX*1), DX
	CMPQ DX, 8(SP)
	JHS  fail2

litRunLoop:
	CMPQ AX, $255
	JLO  litRunLast
	MOVB $255, (DI)
	INCQ DI
	SUBQ $255, AX
	JMP  litRunLoop

litRunLast:
	MOVB AL, (DI)
	INCQ DI

copyLits:
	LEAQ (DI)(CX*1), BX          // end of the literals in dst
	MOVQ R8, AX
	LEAQ 8(BX), DX
	CMPQ DX, 8(SP)
	JHI  exactLits

copyLits8:
	MOVQ (AX), DX
	MOVQ DX, (DI)
	ADDQ $8, AX
	ADDQ $8, DI
	CMPQ DI, BX
	JLO  copyLits8
	MOVQ BX, DI
	JMP  offsetOut

exactLits:
	CMPQ DI, BX
	JHS  offsetOut
	MOVB (AX), DX
	MOVB DX, (DI)
	INCQ AX
	INCQ DI
	JMP  exactLits

offsetOut:
	// offset
	LEAQ 2(DI), AX
	CMPQ AX, 8(SP)
	JHI  fail2
	MOVQ 32(SP), AX
	MOVW AX, (DI)
	ADDQ $2, DI

	// match length
	MOVQ 24(SP), CX
	SUBQ $4, CX                  // mlCode
	MOVQ CX, AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX                 // mlCode / 255
	LEAQ 6(DI)(AX*1), DX         // op + mlCode/255 + 1+LASTLITERALS
	CMPQ DX, 0(SP)
	JHI  fail
	LEAQ 2(DI)(AX*1), DX
	CMPQ DX, 8(SP)
	JHI  fail2
	CMPQ CX, $15
	JHS  mlRun
	ADDB CL, (R14)
	JMP  mlDone

mlRun:
	ADDB $15, (R14)
	SUBQ $15, CX

mlRunLoop:
	CMPQ CX, $255
	JLO  mlRunLast
	MOVB $255, (DI)
	INCQ DI
	SUBQ $255, CX
	JMP  mlRunLoop

mlRunLast:
	MOVB CL, (DI)
	INCQ DI

mlDone:
	ADDQ 24(SP), SI              // ip += matchLength
	MOVQ SI, R8                  // anchor = ip

	// fill table with end of match
	MOVQ SI, R14
	SUBQ R9, R14
	ADDL R13, R14                // endMatchIdx
	LEAL -2(R14), AX
	CMPL AX, 16(SP)
	JHS  loop                    // pos_m2 >= ilimitIdx
	MOVQ 40(SP), BX
	MOVQ SI, AX
	SUBQ R9, AX
	CMPQ AX, $5
	JLE  noM5
	LEAQ -5(SI), AX
	HASH8(AX, DX)
	LEAL -5(R14), CX
	MOVL CX, (BX)(DX*4)

noM5:
	LEAQ -3(SI), AX
	HASH8(AX, DX)
	LEAL -3(R14), CX
	MOVL CX, (BX)(DX*4)
	LEAQ -2(SI), AX
	HASH8(AX, DX)
	LEAL -2(R14), CX
	MOVL CX, (BX)(DX*4)
	LEAQ -2(SI), AX
	HASH4(AX, DX)
	LEAL -2(R14), CX
	MOVL CX, (R10)(DX*4)
	LEAQ -1(SI), AX
	HASH4(AX, DX)
	LEAL -1(R14), CX
	MOVL CX, (R10)(DX*4)
	JMP  loop

done:
	MOVQ win_base+0(FP), AX
	SUBQ AX, SI
	MOVQ SI, nip+88(FP)
	SUBQ AX, R8
	MOVQ R8, anchor+96(FP)
	MOVQ 48(SP), AX
	SUBQ AX, DI
	MOVQ DI, op+104(FP)
	MOVQ $0, status+112(FP)
	RET

fail:
	MOVQ $1, status+112(FP)
	RET

fail2:
	MOVQ $2, status+112(FP)
	RET

// midCount: CX = LZ4_count(AX, BX, R12): the common prefix of the bytes at
// AX (below matchlimit R12) and at BX. Clobbers AX, BX, DX.
//
// BOUNDS: 8-byte loads at AX while AX < R12-7, then 4, 2 and 1 bytes while
// they fit below R12; BX advances with AX and lies behind it.
TEXT midCount<>(SB), NOSPLIT, $0
	MOVQ AX, CX

mc8:
	LEAQ -7(R12), DX
	CMPQ CX, DX
	JHS  mcTail
	MOVQ (CX), DX
	XORQ (BX), DX
	JNE  mcDiff
	ADDQ $8, CX
	ADDQ $8, BX
	JMP  mc8

mcDiff:
	BSFQ DX, DX
	SHRQ $3, DX
	ADDQ DX, CX
	SUBQ AX, CX
	RET

mcTail:
	LEAQ -3(R12), DX
	CMPQ CX, DX
	JHS  mc2
	MOVL (CX), DX
	CMPL DX, (BX)
	JNE  mc2
	ADDQ $4, CX
	ADDQ $4, BX

mc2:
	LEAQ -1(R12), DX
	CMPQ CX, DX
	JHS  mc1
	MOVWLZX (CX), DX
	CMPW DX, (BX)
	JNE  mc1
	ADDQ $2, CX
	ADDQ $2, BX

mc1:
	CMPQ CX, R12
	JHS  mcDone
	MOVB (CX), DX
	CMPB DL, (BX)
	JNE  mcDone
	INCQ CX

mcDone:
	SUBQ AX, CX
	RET
