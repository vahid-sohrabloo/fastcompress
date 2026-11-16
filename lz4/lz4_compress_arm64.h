// The main loop of LZ4_compress_generic_validated (lz4 v1.11.1 lib/lz4.c,
// Copyright (c) Yann Collet, BSD 2-Clause) for noDict, notLimited or
// limitedOutput, without suspension, included by lz4_compress_arm64.s for the
// byU32 and byU16 tables; step for step lz4_compress_amd64.h. The includer
// defines TABLE_LOAD(h, dst), TABLE_STORE(val, h), HASH(ptr, dst) (with
// the multiplier HASH_PRIME in R17), and LZ4_NODICT for noDict callers (a
// candidate at or above lowIdx = startIndex is within src, so the search
// loop compares its 4 bytes before checking the distance, as C's noDict)
// and LZ4_U16 for the byU16 table (indexes below 64 KB: no distance checks,
// as C), and LZ4_FILL for fillOutput (destSize; noDict): C's three output
// checks of fillOutput replace limitedOutput's, ending at _last_literals
// (Go's, from anchor and op) or cutting a match, with filledIp in R25.
//
// Registers: R0 ip, R1 op, R2 anchor, R3 base (src - startIndex), R4 table,
// R5 mflimitPlusOne (iend-11), R6 lowIdx (C's prefixIdxLimit for dictSmall,
// else 0), R17 the hash multiplier, R19 matchlimit (iend-5), R20 olimit
// (dst+olimit, or the largest address when not limited), R21 dstEnd, R22
// acceleration<<6, R23 lowLimit (src - prefixLen), R24 clampIdx, R25
// filledIp, R26 LZ4_DISTANCE_MAX; in the search loop R7 matchIndex, R8
// step, R9 searchMatchNb, R10 forwardH / h, R11 forwardIp, R12 current;
// R11 token, R12 match and R9 litLength / matchCode from the catch up to
// the match length; R7-R10, R13-R16 scratch otherwise.

	MOVD src_base+0(FP), R13
	MOVD prefixLen+96(FP), R14
	SUB  R14, R13, R23           // lowLimit
	MOVD clampIdx+88(FP), R24
	MOVD src_len+8(FP), R14
	ADD  R13, R14, R14           // iend
	SUB  $11, R14, R5            // mflimitPlusOne = iend - MFLIMIT + 1
	SUB  $5, R14, R19            // matchlimit = iend - LASTLITERALS
	MOVD dst_base+24(FP), R1
	MOVD dst_len+32(FP), R21
	ADD  R1, R21, R21            // dstEnd
	MOVD olimit+72(FP), R20
	CMP  $0, R20
	BGE  limited
	MOVD $-1, R20                // notLimited: no output check
	JMP  olimitSet

limited:
	ADD R1, R20, R20

olimitSet:
	MOVD table+48(FP), R4
	MOVD startIndex+56(FP), R15
	SUB  R15, R13, R3            // base
	MOVD lowIdx+80(FP), R6
	MOVD accel+64(FP), R22
	LSL  $6, R22, R22            // acceleration << LZ4_skipTrigger
	MOVD anchor0+112(FP), R2
	ADD  R13, R2, R2             // anchor
	MOVD ip0+104(FP), R0
	ADD  R13, R0, R0             // ip (from the start: src+1, the first byte is in the table)
	MOVD op0+120(FP), R14
	ADD  R14, R1, R1             // op
	MOVD HASH_PRIME, R17
	MOVD $65535, R26
	MOVD resume+128(FP), R14
	CBNZ R14, fillTable          // C: goto _resume
	HASH(R0, R10)                // forwardH

search:
	MOVD R0, R11                 // forwardIp
	MOVD $1, R8                  // step
	MOVD R22, R9                 // searchMatchNb

searchLoop:
	TABLE_LOAD(R10, R7)          // matchIndex = table[h]
	MOVD R11, R0                 // ip = forwardIp
	SUB  R3, R11, R12            // current
	ADD  R8, R11, R11            // forwardIp += step
	LSR  $6, R9, R8              // step = searchMatchNb++ >> LZ4_skipTrigger
	ADD  $1, R9, R9
	CMP  R5, R11
	BHI  lastLits                // forwardIp > mflimitPlusOne
	TABLE_STORE(R12, R10)        // table[h] = current
	HASH(R11, R10)               // forwardH = hash(forwardIp)
	CMPW R6, R7
	BLO  searchLoop              // dictSmall: matchIndex < prefixIdxLimit (no-op for lowIdx 0)
#ifdef LZ4_NODICT
	MOVWU (R3)(R7), R13
	MOVWU (R0), R14
	CMPW  R13, R14
	BNE   searchLoop
#ifndef LZ4_U16
	ADDW R26, R7, R13
	CMPW R12, R13
	BLO  searchLoop              // matchIndex + LZ4_DISTANCE_MAX < current: too far
#endif
#else
	ADDW R26, R7, R13
	CMPW R12, R13
	BLO  searchLoop              // matchIndex + LZ4_DISTANCE_MAX < current: too far
	MOVWU (R3)(R7), R13
	MOVWU (R0), R14
	CMPW  R13, R14
	BNE   searchLoop
#endif
	ADD R3, R7, R12              // match

	// Catch up
#ifdef LZ4_FILL
	MOVD R0, R25                 // filledIp = ip
#endif
	CMP   R23, R12
	BLS   encLits                // match <= lowLimit
	MOVBU -1(R0), R13
	MOVBU -1(R12), R14
	CMPW  R13, R14
	BNE   encLits

catchUp:
	SUB   $1, R0, R0
	SUB   $1, R12, R12
	CMP   R2, R0
	BLS   encLits
	CMP   R23, R12
	BLS   encLits
	MOVBU -1(R0), R13
	MOVBU -1(R12), R14
	CMPW  R13, R14
	BEQ   catchUp

encLits:
	SUB  R2, R0, R9              // litLength
	MOVD R1, R11                 // token
	ADD  $1, R1, R1
	MOVD $0x80808081, R16
#ifdef LZ4_FILL
	ADD  $240, R9, R13
	MUL  R16, R13, R13
	LSR  $39, R13, R13           // (litLength + 240) / 255
	ADD  R9, R1, R14
	ADD  R13, R14, R14
	ADD  $11, R14, R14
	CMP  R20, R14
	BHI  fillStop                // op + (litLength+240)/255 + litLength + 2+1 + MFLIMIT-MINMATCH > olimit
#else
	MUL  R16, R9, R13
	LSR  $39, R13, R13           // litLength / 255
	ADD  R9, R1, R14
	ADD  R13, R14, R14
	ADD  $8, R14, R14
	CMP  R20, R14
	BHI  fail                    // limitedOutput: op + litLength + 2+1+LASTLITERALS + litLength/255 > olimit
#endif
	CMP  $15, R9
	BHS  litRun
	LSLW $4, R9, R13
	MOVB R13, (R11)
	JMP  copyLits

litRun:
	MOVD $0xF0, R13
	MOVB R13, (R11)
	SUB  $15, R9, R13
	CMP  $255, R13
	BLO  litRunLast
	ADD  R13, R1, R14
	CMP  R21, R14
	BHS  fail2                   // cannot happen with dst >= LZ4_compressBound or the check above
	MOVD $255, R14

litRunLoop:
	MOVB R14, (R1)
	ADD  $1, R1, R1
	SUB  $255, R13, R13
	CMP  $255, R13
	BHS  litRunLoop

litRunLast:
	MOVB R13, (R1)
	ADD  $1, R1, R1

copyLits:
	ADD  R9, R1, R8              // end of the literals in dst
	ADD  $8, R8, R14
	CMP  R21, R14
	BHI  exactLits
	MOVD R2, R13

copyLits8:
	MOVD.P 8(R13), R14
	MOVD.P R14, 8(R1)
	CMP    R8, R1
	BLO    copyLits8
	MOVD   R8, R1
	JMP    encOffset

exactLits:
	MOVD R2, R13
	CMP  R8, R1
	BHS  encOffset

exactLitsLoop:
	MOVBU.P 1(R13), R14
	MOVB.P  R14, 1(R1)
	CMP     R8, R1
	BLO     exactLitsLoop

encOffset:
	// _next_match: R11 token, R0 ip, R12 match
#ifdef LZ4_FILL
	ADD $11, R1, R13
	CMP R20, R13
	BHI fillStop                 // op + 2+1 + MFLIMIT-MINMATCH > olimit: op = token
#endif
	ADD  $2, R1, R13
	CMP  R21, R13
	BHI  fail2
	SUB  R12, R0, R13
	MOVH R13, (R1)               // offset
	ADD  $2, R1, R1

	// match length: LZ4_count(ip+MINMATCH, match+MINMATCH, matchlimit)
	ADD $4, R0, R13
	ADD $4, R12, R14
	SUB $7, R19, R15

count8:
	CMP  R15, R13
	BHS  countTail
	MOVD (R13), R16
	MOVD (R14), R10
	EOR  R10, R16, R16
	CBNZ R16, countDiff
	ADD  $8, R13, R13
	ADD  $8, R14, R14
	JMP  count8

countDiff:
	RBIT R16, R16
	CLZ  R16, R16
	ADD  R16>>3, R13, R13
	JMP  countDone

countTail:
	SUB   $3, R19, R15
	CMP   R15, R13
	BHS   count2
	MOVWU (R13), R16
	MOVWU (R14), R10
	CMPW  R16, R10
	BNE   count2
	ADD   $4, R13, R13
	ADD   $4, R14, R14

count2:
	SUB   $1, R19, R15
	CMP   R15, R13
	BHS   count1
	MOVHU (R13), R16
	MOVHU (R14), R10
	CMPW  R16, R10
	BNE   count1
	ADD   $2, R13, R13
	ADD   $2, R14, R14

count1:
	CMP   R19, R13
	BHS   countDone
	MOVBU (R13), R16
	MOVBU (R14), R10
	CMPW  R16, R10
	BNE   countDone
	ADD   $1, R13, R13

countDone:
	SUB  R0, R13, R9
	SUB  $4, R9, R9              // matchCode
	MOVD R13, R0                 // ip += matchCode + MINMATCH
	MOVD $0x80808081, R16
	ADD  $240, R9, R14
	MUL  R16, R14, R14
	LSR  $39, R14, R14           // (matchCode + 240) / 255
	ADD  R1, R14, R14
	ADD  $6, R14, R14
	CMP  R20, R14
#ifdef LZ4_FILL
	BHI  fillCut                 // op + 1+LASTLITERALS + (matchCode+240)/255 > olimit

mlEncode:
#else
	BHI  fail                    // limitedOutput: op + 1+LASTLITERALS + (matchCode+240)/255 > olimit
#endif
	CMP   $15, R9
	BHS   mlRun
	MOVBU (R11), R13
	ADD   R9, R13, R13
	MOVB  R13, (R11)
	JMP   afterML

mlRun:
	MOVBU (R11), R13
	ADD   $15, R13, R13
	MOVB  R13, (R11)
	SUB   $15, R9, R9
	MOVD  $0x80808081, R16
	MUL   R16, R9, R13
	LSR   $39, R13, R13
	ADD   R1, R13, R13
	ADD   $5, R13, R13
	CMP   R21, R13
	BHI   fail2                  // cannot happen with dst >= LZ4_compressBound or the check above
	MOVD  $-1, R15
	MOVW  R15, (R1)

mlRunLoop:
	CMP  $1020, R9
	BLO  mlRunRem
	ADD  $4, R1, R1
	MOVW R15, (R1)
	SUB  $1020, R9, R9
	JMP  mlRunLoop

mlRunRem:
	MUL  R16, R9, R13
	LSR  $39, R13, R13           // matchCode / 255 (< 4)
	ADD  R13, R1, R1
	LSL  $8, R13, R14
	SUB  R13, R14, R14           // 255 * (matchCode / 255)
	SUB  R14, R9, R9
	MOVB R9, (R1)
	ADD  $1, R1, R1

afterML:
	MOVD R0, R2                  // anchor = ip
	CMP  R5, R0
	BHS  lastLits                // ip >= mflimitPlusOne: end of chunk

fillTable:
	// Fill table, and test next position
	HASH(R0, R10)                // h
	SUB   R3, R0, R12            // current
	SUB   $1, R0, R13
	HASH(R13, R9)                // hPrev
	TABLE_LOAD(R9, R8)           // prevIndex
	SUBW  $1, R12, R13
	TABLE_STORE(R13, R9)         // table[hPrev] = current - 1
	CMPW  R24, R8
	CSELW LO, R24, R8, R8        // used table (noDict, dictSmall): a stale entry yields startIndex
	ADDW  $1, R8, R8             // ctxIndex
	CMPW  R6, R8
	BLO   useHash                // dictSmall: ctxIndex < prefixIdxLimit
#ifndef LZ4_U16
	ADDW  R26, R8, R13
	CMPW  R12, R13
	BLO   useHash                // ctxIndex + LZ4_DISTANCE_MAX < current
#endif
	MOVD  (R3)(R8), R13
	MOVD  (R0), R14
	CMP   R13, R14
	BNE   useHash                // LZ4_match8(base + ctxIndex, ip)
	MOVD  R8, R7
	JMP   haveIndex

useHash:
	TABLE_LOAD(R10, R7)          // matchIndex = table[h]

haveIndex:
	TABLE_STORE(R12, R10)        // table[h] = current
	CMPW  R6, R7
	BLO   nextPos                // dictSmall: matchIndex < prefixIdxLimit
#ifndef LZ4_U16
	ADDW  R26, R7, R9
	CMPW  R12, R9
	BLO   nextPos                // too far
#endif
	ADD   R3, R7, R12            // match
	MOVWU (R12), R9
	MOVWU (R0), R13
	CMPW  R9, R13
	BNE   nextPos
	MOVD  R1, R11                // token = op++; *token = 0
	MOVB  ZR, (R1)
	ADD   $1, R1, R1
	JMP   encOffset

nextPos:
	ADD $1, R0, R0
	HASH(R0, R10)                // forwardH = hash(++ip)
	JMP search

lastLits:
	MOVD src_base+0(FP), R13
	SUB  R13, R2, R2
	MOVD R2, anchor+136(FP)
	MOVD dst_base+24(FP), R13
	SUB  R13, R1, R1
	MOVD R1, op+144(FP)
	MOVD ZR, status+152(FP)
	RET

#ifdef LZ4_FILL
fillStop:
	MOVD R11, R1                 // op = token
	JMP  lastLits

fillCut:
	// the match description too long: newMatchCode = 15-1 + (olimit - op
	// - 1 - LASTLITERALS) * 255; ip moves back by the difference, and the
	// positions from ip to filledIp leave the table (LZ4_clearHash)
	SUB  R1, R20, R13
	SUB  $6, R13, R13
	LSL  $8, R13, R14
	SUB  R13, R14, R13
	ADD  $14, R13, R13           // newMatchCode
	SUB  R9, R0, R0
	ADD  R13, R0, R0             // ip -= matchCode - newMatchCode
	MOVD R13, R9
	CMP  R25, R0
	BHI  mlEncode
	MOVD R0, R8

fillClear:
	HASH(R8, R10)
	TABLE_STORE(ZR, R10)
	ADD $1, R8, R8
	CMP R25, R8
	BLS fillClear
	JMP mlEncode
#endif

fail:
	MOVD $1, R13
	MOVD R13, status+152(FP)
	RET

fail2:
	MOVD $2, R13
	MOVD R13, status+152(FP)
	RET
