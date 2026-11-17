// The main loop of LZ4_compress_generic_validated (lz4 v1.11.1 lib/lz4.c,
// Copyright (c) Yann Collet, BSD 2-Clause) for noDict, notLimited or
// limitedOutput, without suspension, included by lz4_compress_amd64.s for the
// byU32 and byU16 tables. The includer defines TABLE_LOAD(h, dst),
// TABLE_STORE(val, h) and HASH(ptr, dst) (R12 is HASH's scratch register),
// and LZ4_NODICT for noDict callers (a candidate at or above lowIdx =
// startIndex is within src, so the search loop compares its 4 bytes before
// checking the distance, as C's noDict) and LZ4_U16 for the byU16 table
// (indexes below 64 KB: no distance checks, as C), and LZ4_FILL for
// fillOutput (destSize; noDict): C's three output checks of fillOutput
// replace limitedOutput's, ending at _last_literals (Go's, from anchor and
// op) or cutting a match, with filledIp at 48(SP).
//
// Frame locals (hardware SP): 0 matchlimit (iend-5), 8 olimit (dst+olimit,
// or the largest address when not limited), 16 dstEnd, 24 acceleration<<6,
// 32 lowLimit (src - prefixLen), 40 clampIdx.
//
// Registers: SI ip, DI op, R8 anchor, R9 base (src - startIndex), R10 table,
// R11 mflimitPlusOne (iend-11), R13 lowIdx (C's prefixIdxLimit for dictSmall,
// else 0); in the search loop AX
// matchIndex, BX step, CX searchMatchNb, DX forwardH / h, R14 forwardIp,
// R15 current; R14 token and R15 match from the catch up to the match
// length; AX, BX, CX, DX, R12 scratch otherwise.

	MOVQ src_base+0(FP), AX
	MOVQ AX, BX
	SUBQ prefixLen+96(FP), BX
	MOVQ BX, 32(SP)              // lowLimit
	MOVQ clampIdx+88(FP), BX
	MOVQ BX, 40(SP)
	MOVQ src_len+8(FP), CX
	LEAQ (AX)(CX*1), DX          // iend
	LEAQ -11(DX), R11            // mflimitPlusOne = iend - MFLIMIT + 1
	LEAQ -5(DX), BX
	MOVQ BX, 0(SP)               // matchlimit = iend - LASTLITERALS
	MOVQ dst_base+24(FP), DI
	MOVQ dst_len+32(FP), BX
	ADDQ DI, BX
	MOVQ BX, 16(SP)              // dstEnd
	MOVQ olimit+72(FP), BX
	TESTQ BX, BX
	JGE  limited
	MOVQ $-1, BX                 // notLimited: no output check
	JMP  olimitSet

limited:
	ADDQ DI, BX

olimitSet:
	MOVQ BX, 8(SP)
	MOVQ table+48(FP), R10
	MOVQ startIndex+56(FP), R13
	MOVQ AX, R9
	SUBQ R13, R9                 // base
	MOVQ lowIdx+80(FP), R13
	MOVQ accel+64(FP), BX
	SHLQ $6, BX
	MOVQ BX, 24(SP)              // acceleration << LZ4_skipTrigger
	MOVQ anchor0+112(FP), R8
	ADDQ AX, R8                  // anchor
	MOVQ ip0+104(FP), SI
	ADDQ AX, SI                  // ip (from the start: src+1, the first byte is in the table)
	MOVQ op0+120(FP), BX
	ADDQ BX, DI                  // op
	CMPQ resume+128(FP), $0
	JNE  fillTable               // C: goto _resume
	HASH(SI, DX)                 // forwardH

search:
	MOVQ SI, R14                 // forwardIp
	MOVL $1, BX                  // step
	MOVQ 24(SP), CX              // searchMatchNb

searchLoop:
	TABLE_LOAD(DX, AX)           // matchIndex = table[h]
	MOVQ R14, SI                 // ip = forwardIp
	MOVQ R14, R15
	SUBQ R9, R15                 // current
	ADDQ BX, R14                 // forwardIp += step
	MOVQ CX, BX
	SHRQ $6, BX                  // step = searchMatchNb++ >> LZ4_skipTrigger
	INCQ CX
	CMPQ R14, R11
	JHI  lastLits                // forwardIp > mflimitPlusOne
	TABLE_STORE(R15, DX)         // table[h] = current
	HASH(R14, DX)                // forwardH = hash(forwardIp)
	CMPL AX, R13
	JLO  searchLoop              // dictSmall: matchIndex < prefixIdxLimit (no-op for lowIdx 0)
#ifdef LZ4_NODICT
	MOVL (R9)(AX*1), R12
	CMPL R12, (SI)
	JNE  searchLoop
#ifndef LZ4_U16
	LEAL 65535(AX), R12
	CMPL R12, R15
	JLO  searchLoop              // matchIndex + LZ4_DISTANCE_MAX < current: too far
#endif
#else
	LEAL 65535(AX), R12
	CMPL R12, R15
	JLO  searchLoop              // matchIndex + LZ4_DISTANCE_MAX < current: too far
	MOVL (R9)(AX*1), R12
	CMPL R12, (SI)
	JNE  searchLoop
#endif
	LEAQ (R9)(AX*1), R15         // match

	// Catch up
#ifdef LZ4_FILL
	MOVQ SI, 48(SP)              // filledIp = ip
#endif
	MOVQ 32(SP), R12             // lowLimit
	CMPQ R15, R12
	JLS  encLits
	MOVBLZX -1(SI), AX
	CMPB AL, -1(R15)
	JNE  encLits

catchUp:
	DECQ SI
	DECQ R15
	CMPQ SI, R8
	JLS  encLits
	CMPQ R15, R12
	JLS  encLits
	MOVBLZX -1(SI), AX
	CMPB AL, -1(R15)
	JEQ  catchUp

encLits:
	MOVQ SI, CX
	SUBQ R8, CX                  // litLength
	MOVQ DI, R14                 // token
	INCQ DI
	MOVQ CX, AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX                 // litLength / 255
#ifdef LZ4_FILL
	LEAQ 240(CX), AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX                 // (litLength + 240) / 255
	LEAQ 11(DI)(CX*1), R12
	ADDQ AX, R12
	CMPQ R12, 8(SP)
	JHI  fillStop                // op + (litLength+240)/255 + litLength + 2+1 + MFLIMIT-MINMATCH > olimit
#else
	LEAQ 8(DI)(CX*1), R12
	ADDQ AX, R12
	CMPQ R12, 8(SP)
	JHI  fail                    // limitedOutput: op + litLength + 2+1+LASTLITERALS + litLength/255 > olimit
#endif
	CMPQ CX, $15
	JHS  litRun
	MOVQ CX, AX
	SHLQ $4, AX
	MOVB AL, (R14)
	JMP  copyLits

litRun:
	MOVB $0xF0, (R14)
	LEAQ -15(CX), AX
	CMPQ AX, $255
	JLO  litRunLast
	LEAQ (DI)(AX*1), R12
	CMPQ R12, 16(SP)
	JHS  fail2                   // cannot happen with dst >= LZ4_compressBound or the check above

litRunLoop:
	MOVB $255, (DI)
	INCQ DI
	SUBQ $255, AX
	CMPQ AX, $255
	JHS  litRunLoop

litRunLast:
	MOVB AL, (DI)
	INCQ DI

copyLits:
	LEAQ (DI)(CX*1), BX          // end of the literals in dst
	LEAQ 8(BX), R12
	CMPQ R12, 16(SP)
	JHI  exactLits
	MOVQ R8, AX

copyLits8:
	MOVQ (AX), DX
	MOVQ DX, (DI)
	ADDQ $8, AX
	ADDQ $8, DI
	CMPQ DI, BX
	JLO  copyLits8
	MOVQ BX, DI
	JMP  encOffset

exactLits:
	MOVQ R8, AX
	CMPQ DI, BX
	JHS  encOffset

exactLitsLoop:
	MOVB (AX), DX
	MOVB DX, (DI)
	INCQ AX
	INCQ DI
	CMPQ DI, BX
	JLO  exactLitsLoop

encOffset:
	// _next_match: R14 token, SI ip, R15 match
#ifdef LZ4_FILL
	LEAQ 11(DI), AX
	CMPQ AX, 8(SP)
	JHI  fillStop                // op + 2+1 + MFLIMIT-MINMATCH > olimit: op = token
#endif
	LEAQ 2(DI), AX
	CMPQ AX, 16(SP)
	JHI  fail2
	MOVQ SI, AX
	SUBQ R15, AX
	MOVW AX, (DI)                // offset
	ADDQ $2, DI

	// match length: LZ4_count(ip+MINMATCH, match+MINMATCH, matchlimit)
	LEAQ 4(SI), AX
	LEAQ 4(R15), BX
	MOVQ 0(SP), R12
	LEAQ -7(R12), CX

count8:
	CMPQ AX, CX
	JHS  countTail
	MOVQ (AX), DX
	XORQ (BX), DX
	JNE  countDiff
	ADDQ $8, AX
	ADDQ $8, BX
	JMP  count8

countDiff:
	BSFQ DX, DX
	SHRQ $3, DX
	ADDQ DX, AX
	JMP  countDone

countTail:
	LEAQ -3(R12), CX
	CMPQ AX, CX
	JHS  count2
	MOVL (AX), DX
	CMPL DX, (BX)
	JNE  count2
	ADDQ $4, AX
	ADDQ $4, BX

count2:
	LEAQ -1(R12), CX
	CMPQ AX, CX
	JHS  count1
	MOVWLZX (AX), DX
	CMPW DX, (BX)
	JNE  count1
	ADDQ $2, AX
	ADDQ $2, BX

count1:
	CMPQ AX, R12
	JHS  countDone
	MOVB (AX), DX
	CMPB DL, (BX)
	JNE  countDone
	INCQ AX

countDone:
	MOVQ AX, CX
	SUBQ SI, CX
	SUBQ $4, CX                  // matchCode
	MOVQ AX, SI                  // ip += matchCode + MINMATCH
	LEAQ 240(CX), AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX                 // (matchCode + 240) / 255
	LEAQ 6(DI)(AX*1), AX
	CMPQ AX, 8(SP)
#ifdef LZ4_FILL
	JHI  fillCut                 // op + 1+LASTLITERALS + (matchCode+240)/255 > olimit

mlEncode:
#else
	JHI  fail                    // limitedOutput: op + 1+LASTLITERALS + (matchCode+240)/255 > olimit
#endif
	CMPQ CX, $15
	JHS  mlRun
	ADDB CL, (R14)
	JMP  afterML

mlRun:
	ADDB $15, (R14)
	SUBQ $15, CX
	MOVQ CX, AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX
	LEAQ 5(DI)(AX*1), AX
	CMPQ AX, 16(SP)
	JHI  fail2                   // cannot happen with dst >= LZ4_compressBound or the check above
	MOVL $-1, (DI)

mlRunLoop:
	CMPQ CX, $1020
	JLO  mlRunRem
	ADDQ $4, DI
	MOVL $-1, (DI)
	SUBQ $1020, CX
	JMP  mlRunLoop

mlRunRem:
	MOVQ CX, AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX                 // matchCode / 255 (< 4)
	ADDQ AX, DI
	IMUL3Q $255, AX, AX
	SUBQ AX, CX
	MOVB CL, (DI)
	INCQ DI

afterML:
	MOVQ SI, R8                  // anchor = ip
	CMPQ SI, R11
	JHS  lastLits                // ip >= mflimitPlusOne: end of chunk

fillTable:
	// Fill table, and test next position
	HASH(SI, DX)                 // h
	MOVQ SI, R15
	SUBQ R9, R15                 // current
	LEAQ -1(SI), AX
	HASH(AX, CX)                 // hPrev
	TABLE_LOAD(CX, BX)           // prevIndex
	LEAL -1(R15), AX
	TABLE_STORE(AX, CX)          // table[hPrev] = current - 1
	CMPL BX, 40(SP)
	CMOVLCS 40(SP), BX           // used table (noDict, dictSmall): a stale entry yields startIndex
	INCL BX                      // ctxIndex
	CMPL BX, R13
	JLO  useHash                 // dictSmall: ctxIndex < prefixIdxLimit
#ifndef LZ4_U16
	LEAL 65535(BX), AX
	CMPL AX, R15
	JLO  useHash                 // ctxIndex + LZ4_DISTANCE_MAX < current
#endif
	MOVQ (R9)(BX*1), AX
	CMPQ AX, (SI)
	JNE  useHash                 // LZ4_match8(base + ctxIndex, ip)
	MOVL BX, AX
	JMP  haveIndex

useHash:
	TABLE_LOAD(DX, AX)           // matchIndex = table[h]

haveIndex:
	TABLE_STORE(R15, DX)         // table[h] = current
	CMPL AX, R13
	JLO  nextPos                 // dictSmall: matchIndex < prefixIdxLimit
#ifndef LZ4_U16
	LEAL 65535(AX), CX
	CMPL CX, R15
	JLO  nextPos                 // too far
#endif
	LEAQ (R9)(AX*1), R15         // match
	MOVL (R15), CX
	CMPL CX, (SI)
	JNE  nextPos
	MOVQ DI, R14                 // token = op++; *token = 0
	MOVB $0, (DI)
	INCQ DI
	JMP  encOffset

nextPos:
	INCQ SI
	HASH(SI, DX)                 // forwardH = hash(++ip)
	JMP  search

lastLits:
	MOVQ src_base+0(FP), AX
	SUBQ AX, R8
	MOVQ R8, anchor+136(FP)
	MOVQ dst_base+24(FP), AX
	SUBQ AX, DI
	MOVQ DI, op+144(FP)
	MOVQ $0, status+152(FP)
	RET

#ifdef LZ4_FILL
fillStop:
	MOVQ R14, DI                 // op = token
	JMP  lastLits

fillCut:
	// the match description too long: newMatchCode = 15-1 + (olimit - op
	// - 1 - LASTLITERALS) * 255; ip moves back by the difference, and the
	// positions from ip to filledIp leave the table (LZ4_clearHash)
	MOVQ 8(SP), AX
	SUBQ DI, AX
	SUBQ $6, AX
	IMUL3Q $255, AX, AX
	ADDQ $14, AX                 // newMatchCode
	SUBQ CX, SI
	ADDQ AX, SI                  // ip -= matchCode - newMatchCode
	MOVQ AX, CX
	CMPQ SI, 48(SP)
	JHI  mlEncode
	MOVQ SI, BX

fillClear:
	HASH(BX, DX)
	XORL AX, AX
	TABLE_STORE(AX, DX)
	INCQ BX
	CMPQ BX, 48(SP)
	JLS  fillClear
	JMP  mlEncode
#endif

fail:
	MOVQ $1, status+152(FP)
	RET

fail2:
	MOVQ $2, status+152(FP)
	RET
