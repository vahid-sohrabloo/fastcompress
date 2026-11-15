// The main loop of LZ4_compress_generic_validated for usingExtDict and a
// byU32 table (lz4 v1.11.1 lib/lz4.c, Copyright (c) Yann Collet, BSD
// 2-Clause): lz4_compress_amd64.h's loop with candidates in an external
// dictionary, matches running from it into the input, and the suspension at
// seqLimit for LZ4_compress_resumeWithPrefix. Included by
// lz4_compress_amd64.s for usingExtDict, and with LZ4_DICTCTX for
// usingDictCtx: a table entry below startIndex is replaced by the attached
// dictionary's (96(SP)) plus dictDelta (104(SP)), which indexes the
// dictionary as an external one (dictBase = dictEnd - startIndex). The
// results are at R_ANCHOR, R_OP and R_STATUS.
//
// Frame locals (hardware SP): 0 matchlimit, 8 olimit, 16 dstEnd,
// 24 acceleration<<6, 32 source, 40 dictBase (dictEnd - startIndex),
// 48 dictionary, 56 dictEnd, 64 seqLimit, 72 offset, 80 match in the
// dictionary, 88 startIndex (LZ4_DICTCTX: 96 the attached table, 104
// dictDelta).
//
// Registers: as lz4_compress_amd64.h (R9 base = src - startIndex for
// candidates >= startIndex, 40(SP) for those below).

#ifdef LZ4_DICTCTX
// CTX_LOAD(h, dst): an entry below startIndex is the attached
// dictionary's, plus dictDelta (R12 scratch)
#define CTX_LOAD(h, dst, skip) \
	CMPL dst, 88(SP); \
	JHS  skip; \
	MOVQ 96(SP), R12; \
	MOVL (R12)(h*4), dst; \
	ADDL 104(SP), dst; \
skip:
#define R_ANCHOR anchor+136(FP)
#define R_OP     op+144(FP)
#define R_STATUS status+152(FP)
#else
#define CTX_LOAD(h, dst, skip)
#define R_ANCHOR anchor+120(FP)
#define R_OP     op+128(FP)
#define R_STATUS status+136(FP)
#endif

	MOVQ src_base+0(FP), AX
	MOVQ AX, 32(SP)
	MOVQ src_len+8(FP), CX
	LEAQ (AX)(CX*1), DX          // iend
	LEAQ -11(DX), R11            // mflimitPlusOne
	LEAQ -5(DX), BX
	MOVQ BX, 0(SP)               // matchlimit
	MOVQ seqLimit+112(FP), BX
	ADDQ AX, BX
	MOVQ BX, 64(SP)              // seqLimit
	MOVQ dst_base+24(FP), DI
	MOVQ dst_len+32(FP), BX
	ADDQ DI, BX
	MOVQ BX, 16(SP)              // dstEnd
	MOVQ olimit+72(FP), BX
	ADDQ DI, BX
	MOVQ BX, 8(SP)               // olimit (limitedOutput)
	MOVQ table+48(FP), R10
	MOVQ startIndex+56(FP), R13
	MOVQ R13, 88(SP)
	MOVQ AX, R9
	SUBQ R13, R9                 // base
	MOVQ dict_base+88(FP), BX
	MOVQ BX, 48(SP)              // dictionary
	ADDQ dict_len+96(FP), BX
	MOVQ BX, 56(SP)              // dictEnd
	SUBQ R13, BX
	MOVQ BX, 40(SP)              // dictBase
	MOVQ lowIdx+80(FP), R13
	MOVQ accel+64(FP), BX
	SHLQ $6, BX
	MOVQ BX, 24(SP)
#ifdef LZ4_DICTCTX
	MOVQ dctx+120(FP), BX
	MOVQ BX, 96(SP)
	MOVQ dictDelta+128(FP), BX
	MOVQ BX, 104(SP)
#endif
	MOVQ AX, R8                  // anchor = source
	LEAQ 1(AX), SI               // ip: the first byte is in the table (Go)
	HASH(SI, DX)                 // forwardH

search:
	MOVQ SI, R14                 // forwardIp
	MOVL $1, BX                  // step
	MOVQ 24(SP), CX              // searchMatchNb

searchLoop:
	TABLE_LOAD(DX, AX)           // matchIndex = table[h]
	CTX_LOAD(DX, AX, ctx1)
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
	JLO  searchLoop              // dictSmall: matchIndex < prefixIdxLimit
	LEAL 65535(AX), R12
	CMPL R12, R15
	JLO  searchLoop              // matchIndex + LZ4_DISTANCE_MAX < current: too far
	MOVQ R9, R12
	CMPL AX, 88(SP)
	CMOVQCS 40(SP), R12          // matchIndex < startIndex: within the dictionary (no branch, as C)
	MOVL (R12)(AX*1), R15
	CMPL R15, (SI)
	JNE  searchLoop
	XORL R15, R15
	MOVL $1, CX
	CMPL AX, 88(SP)
	CMOVLCS CX, R15
	MOVQ R15, 80(SP)             // the match is in the dictionary
	MOVQ SI, R15
	SUBQ R9, R15
	SUBL AX, R15
	MOVQ R15, 72(SP)             // offset = current - matchIndex
	LEAQ (R12)(AX*1), R15        // match

	// Catch up: lowLimit is the dictionary for a match in it, else source
	MOVQ 32(SP), R12
	CMPQ 80(SP), $0
	CMOVQNE 48(SP), R12
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
	LEAQ 8(DI)(CX*1), R12
	ADDQ AX, R12
	CMPQ R12, 8(SP)
	JHI  fail                    // limitedOutput: op + litLength + 2+1+LASTLITERALS + litLength/255 > olimit
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
	LEAQ 2(DI), AX
	CMPQ AX, 16(SP)
	JHI  fail2
	MOVQ 72(SP), AX
	MOVW AX, (DI)                // offset
	ADDQ $2, DI

	// match length: LZ4_count(ip+MINMATCH, match+MINMATCH, limit); within
	// the dictionary limit = min(ip + (dictEnd - match), matchlimit), then
	// on from the start of source
	LEAQ 4(SI), AX
	LEAQ 4(R15), BX
	MOVQ 0(SP), R12
	CMPQ 80(SP), $0
	JEQ  countLimit
	MOVQ 56(SP), R12
	SUBQ R15, R12
	ADDQ SI, R12                 // ip + (dictEnd - match)
	CMPQ R12, 0(SP)
	JLS  countLimit
	MOVQ 0(SP), R12

countLimit:
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
	JHS  countEnd
	MOVB (AX), DX
	CMPB DL, (BX)
	JNE  countEnd
	INCQ AX

countEnd:
	CMPQ 80(SP), $0
	JEQ  countDone
	CMPQ AX, R12
	JNE  countDone               // the match ends within the dictionary
	MOVQ $0, 80(SP)
	MOVQ 32(SP), BX              // continue at the start of source
	MOVQ 0(SP), R12
	LEAQ -7(R12), CX
	JMP  count8

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
	JHI  fail                    // limitedOutput: op + 1+LASTLITERALS + (matchCode+240)/255 > olimit
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
	CMPQ SI, 64(SP)
	JLO  fillTable               // ip < seqLimit
	CMPQ SI, R11
	JHS  lastLits                // ip >= mflimitPlusOne: end of chunk
	JMP  suspend                 // beyond reach of the dictionary: resume in prefix mode

fillTable:
	// Fill table, and test next position
	HASH(SI, DX)                 // h
	MOVQ SI, R15
	SUBQ R9, R15                 // current
	LEAQ -1(SI), AX
	HASH(AX, CX)                 // hPrev
	TABLE_LOAD(CX, BX)           // prevIndex
	CTX_LOAD(CX, BX, ctx2)
	LEAL -1(R15), AX
	TABLE_STORE(AX, CX)          // table[hPrev] = current - 1
	INCL BX                      // ctxIndex
	CMPL BX, R13
	JLO  useHash                 // dictSmall: ctxIndex < prefixIdxLimit
	LEAL 65535(BX), AX
	CMPL AX, R15
	JLO  useHash                 // ctxIndex + LZ4_DISTANCE_MAX < current
	MOVQ R9, R12
	CMPL BX, 88(SP)
	JHS  ctxRead
	LEAL 8(BX), AX
	CMPL AX, 88(SP)
	JHI  useHash                 // within the dictionary: the 8 bytes must be in it
	MOVQ 40(SP), R12

ctxRead:
	MOVQ (R12)(BX*1), AX
	CMPQ AX, (SI)
	JNE  useHash                 // LZ4_match8(ctxIndex, ip)
	MOVL BX, AX
	JMP  haveIndex

useHash:
	TABLE_LOAD(DX, AX)           // matchIndex = table[h]
	CTX_LOAD(DX, AX, ctx3)

haveIndex:
	TABLE_STORE(R15, DX)         // table[h] = current
	CMPL AX, R13
	JLO  nextPos                 // dictSmall: matchIndex < prefixIdxLimit
	LEAL 65535(AX), CX
	CMPL CX, R15
	JLO  nextPos                 // too far
	MOVQ R9, R12
	XORL BX, BX
	MOVL $1, CX
	CMPL AX, 88(SP)
	CMOVQCS 40(SP), R12
	CMOVLCS CX, BX
	MOVL (R12)(AX*1), CX
	CMPL CX, (SI)
	JNE  nextPos
	MOVQ BX, 80(SP)
	SUBL AX, R15
	MOVQ R15, 72(SP)             // offset = current - matchIndex
	LEAQ (R12)(AX*1), R15        // match
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
	MOVQ R8, R_ANCHOR
	MOVQ dst_base+24(FP), AX
	SUBQ AX, DI
	MOVQ DI, R_OP
	MOVQ $0, R_STATUS
	RET

suspend:
	MOVQ src_base+0(FP), AX
	SUBQ AX, R8
	MOVQ R8, R_ANCHOR
	MOVQ dst_base+24(FP), AX
	SUBQ AX, DI
	MOVQ DI, R_OP
	MOVQ $3, R_STATUS
	RET

fail:
	MOVQ $1, R_STATUS
	RET

fail2:
	MOVQ $2, R_STATUS
	RET

#undef CTX_LOAD
#undef R_ANCHOR
#undef R_OP
#undef R_STATUS
