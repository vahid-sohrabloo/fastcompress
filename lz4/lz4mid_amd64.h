// The body of midCompressAsm and midCompressDictAsm (lz4mid_amd64.s),
// included after each TEXT line: MID_DICT adds the external dictionary and
// the attached one; without it the routine is the prefix-only loop.

#ifdef MID_DICT
	MOVQ $0, F_DHASH(SP)
	MOVQ d+104(FP), AX
	TESTQ AX, AX
	JEQ  noDictCtx
	MOVQ D_HASH(AX), BX
	MOVQ BX, F_DHASH(SP)
	MOVQ D_CHAIN(AX), BX
	MOVQ BX, F_DCHAIN(SP)
	MOVQ D_BASE(AX), BX
	MOVQ BX, F_DBASE(SP)
	MOVQ D_END(AX), BX
	MOVQ BX, F_DEND(SP)
	MOVQ D_MID(AX), BX
	MOVQ BX, F_DMID(SP)

noDictCtx:
	MOVQ lowLimit+96(FP), AX
	MOVQ AX, F_LOWLIM(SP)
	MOVQ dict+88(FP), BX
	SUBQ AX, BX
	MOVQ BX, F_DICTB(SP)
#endif
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

	// the short match's entry, loaded early (another table)
	HASH4(SI, DX)
	MOVQ DX, 72(SP)
	MOVL (R10)(DX*4), CX
	MOVQ CX, 80(SP)

	// search long match
	HASH8(SI, DX)
	MOVQ 40(SP), BX
	MOVL (BX)(DX*4), CX          // pos8
	MOVL R14, (BX)(DX*4)
	MOVQ CX, 32(SP)
#ifdef MID_DICT
	COUNTMATCHX(SI, R14, CX, DX, cm8slow, cm8dict, cm8)
#else
	COUNTMATCH(SI, R14, CX, DX, cm8slow, cm8)
#endif
	MOVQ DX, 24(SP)
	MOVL R14, AX
	SUBL 32(SP), AX
	MOVQ AX, 32(SP)              // matchDistance = ipIndex - pos8
	CMPQ DX, $4
	JHS  found

	// search short match
	MOVQ 72(SP), DX
	MOVQ 80(SP), CX              // pos4
	MOVL R14, (R10)(DX*4)
	MOVQ CX, 32(SP)
#ifdef MID_DICT
	COUNTMATCHX(SI, R14, CX, DX, cm4slow, cm4dict, cm4)
#else
	COUNTMATCH(SI, R14, CX, DX, cm4slow, cm4)
#endif
	MOVQ DX, 24(SP)
	MOVL R14, AX
	SUBL 32(SP), AX
	MOVQ AX, 32(SP)              // matchDistance = ipIndex - pos4
	CMPQ DX, $4
	JHS  found

#ifdef MID_DICT
	// no match: the attached dictionary, if any and in reach
	CMPQ F_DHASH(SP), $0
	JNE  dictSearch

skip:
#endif
	// no match: skip faster over incompressible data
	MOVQ SI, AX
	SUBQ R8, AX
	SHRQ $9, AX
	LEAQ 1(SI)(AX*1), SI
	JMP  loop

#ifdef MID_DICT
	// LZ4HC_searchExtDict(ip, ipIndex, ip, matchlimit, dictCtx,
	// gDictEndIndex = lowLimit, MINMATCH-1, 2)
dictSearch:
	MOVL R14, AX
	SUBL F_LOWLIM(SP), AX
	CMPL AX, $(65535-8)
	JHS  skip                    // ipIndex - gDictEndIndex >= LZ4_DISTANCE_MAX - 8
	CMPQ F_DMID(SP), $0
	JNE  dictMid
	MOVL (SI), AX
	IMUL3L $-1640531535, AX, AX
	SHRL $17, AX                 // LZ4HC_hashPtr
	MOVQ F_DHASH(SP), BX
	MOVL (BX)(AX*4), CX          // dictMatchIndex
	MOVQ $3, 24(SP)              // currentBestML
	MOVQ $2, F_NB(SP)

dsLoop:
	MOVL CX, AX
	ADDL F_LOWLIM(SP), AX
	SUBL F_DEND(SP), AX          // matchIndex
	MOVL R14, DX
	SUBL AX, DX                  // ipIndex - matchIndex
	CMPL DX, $65535
	JHI  dsDone
	CMPQ F_NB(SP), $0
	JLE  dsDone
	DECQ F_NB(SP)
	MOVQ DX, F_DOFF(SP)
	MOVL CX, BX
	ADDQ F_DBASE(SP), BX         // matchPtr
	MOVL (BX), AX
	CMPL AX, (SI)
	JNE  dsNext
	MOVQ CX, F_DMI(SP)
	MOVL F_DEND(SP), AX
	SUBL CX, AX
	ADDQ SI, AX                  // vLimit = ip + dictEndOffset - dictMatchIndex
	CMPQ AX, R12
	CMOVQHI R12, AX
	MOVQ R12, F_R12(SP)
	MOVQ AX, R12
	LEAQ 4(SI), AX
	ADDQ $4, BX
	CALL midCount<>(SB)
	MOVQ F_R12(SP), R12
	ADDQ $4, CX                  // mlt
	CMPQ CX, 24(SP)
	JLE  dsRestore
	MOVQ CX, 24(SP)
	MOVQ F_DOFF(SP), AX
	MOVQ AX, 32(SP)

dsRestore:
	MOVQ F_DMI(SP), CX

dsNext:
	MOVQ F_DCHAIN(SP), BX
	MOVWLZX CX, AX
	MOVWLZX (BX)(AX*2), AX
	SUBL AX, CX                  // dictMatchIndex -= DELTANEXTU16
	JMP  dsLoop

dsDone:
	CMPQ 24(SP), $4
	JHS  encode
	JMP  skip

	// LZ4MID_searchExtDict: the dictionary's hash8 then hash4 entry
dictMid:
	HASH8(SI, DX)
	MOVQ F_DHASH(SP), BX
	MOVL 65536(BX)(DX*4), CX     // l8
	MOVL CX, AX
	ADDL F_LOWLIM(SP), AX
	SUBL F_DEND(SP), AX          // m8Index
	MOVL R14, DX
	SUBL AX, DX
	CMPL DX, $65535
	JHI  dictMid4
	MOVQ DX, 32(SP)
	MOVQ CX, F_DMI(SP)
	MOVL F_DEND(SP), AX
	SUBL CX, AX
	ADDQ SI, AX                  // ip + safeLen
	CMPQ AX, R12
	CMOVQHI R12, AX
	MOVL CX, BX
	ADDQ F_DBASE(SP), BX
	MOVQ R12, F_R12(SP)
	MOVQ AX, R12
	MOVQ SI, AX
	CALL midCount<>(SB)
	MOVQ F_R12(SP), R12
	MOVQ CX, 24(SP)
	CMPQ CX, $4
	JHS  encode

dictMid4:
	HASH4(SI, DX)
	MOVQ F_DHASH(SP), BX
	MOVL (BX)(DX*4), CX          // l4
	MOVL CX, AX
	ADDL F_LOWLIM(SP), AX
	SUBL F_DEND(SP), AX
	MOVL R14, DX
	SUBL AX, DX
	CMPL DX, $65535
	JHI  skip
	MOVQ DX, 32(SP)
	MOVL F_DEND(SP), AX
	SUBL CX, AX
	ADDQ SI, AX
	CMPQ AX, R12
	CMOVQHI R12, AX
	MOVL CX, BX
	ADDQ F_DBASE(SP), BX
	MOVQ R12, F_R12(SP)
	MOVQ AX, R12
	MOVQ SI, AX
	CALL midCount<>(SB)
	MOVQ F_R12(SP), R12
	MOVQ CX, 24(SP)
	CMPQ CX, $4
	JHS  encode
	JMP  skip

#endif

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
#ifdef MID_DICT
	JLO  ml2dict
#else
	JLO  ml2done
#endif
	MOVL AX, BX
	SUBL R13, BX
	ADDQ R9, BX
	LEAQ -7(R12), AX
	CMPQ DX, AX
	JHS  ml2slow
	MOVQ (DX), AX
	XORQ (BX), AX
	JEQ  ml2slow
	BSFQ AX, CX
	SHRQ $3, CX
	JMP  ml2done

ml2slow:
	MOVQ DX, AX
	CALL midCount<>(SB)
#ifdef MID_DICT
	JMP  ml2done

ml2dict:
	DICTCOUNT(DX, AX, CX, R15, ml2done)
#endif

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
	JHI  failML
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
	MOVQ SI, nip+112(FP)
	SUBQ AX, R8
	MOVQ R8, anchor+120(FP)
	MOVQ 48(SP), AX
	SUBQ AX, DI
	MOVQ DI, op+128(FP)
	MOVQ $0, status+136(FP)
	RET

failML:
	MOVQ R14, DI                 // op before the sequence

fail:
	MOVQ win_base+0(FP), AX
	SUBQ AX, SI
	MOVQ SI, nip+112(FP)
	SUBQ AX, R8
	MOVQ R8, anchor+120(FP)
	MOVQ 48(SP), AX
	SUBQ AX, DI
	MOVQ DI, op+128(FP)
	MOVQ 24(SP), AX
	MOVQ AX, ml+144(FP)
	MOVQ 32(SP), AX
	MOVQ AX, md+152(FP)
	MOVQ $1, status+136(FP)
	RET

fail2:
	MOVQ $2, status+136(FP)
	RET
