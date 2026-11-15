// The body of hcOptimalAsm and hcOptimalFavAsm (lz4hc_parse_amd64.s),
// included after each TEXT line: OPT_FAV is favorDecSpeed.

	PARSE_SETUP(limited, olimSet)
	MOVQ A_SUFF(DI), AX
	MOVQ AX, O_SUFF(SP)
	MOVQ A_OPT(DI), AX
	MOVQ AX, O_OPT(SP)
	MOVQ A_NBSEQ(DI), AX
	MOVQ AX, O_NBSEQ(SP)

optMain:
	MOVQ P_IP(SP), R9
	CMPQ R9, P_MFLIMIT(SP)
	JHI  endLoop
	CMPQ R9, P_STOP(SP)
	JHI  stopped
	MOVQ R9, R10
	MOVL $3, BX
	MOVQ P_MAXATT(SP), R15
#ifdef OPT_FAV
	OPT_CALLF(s1x, s1d, s1c, s1done)  // LZ4HC_FindLongestMatch
#else
	OPT_CALL(s1x, s1d, s1done)   // LZ4HC_FindLongestMatch
#endif
	CMPQ BX, $4
	JHS  firstFound
	INCQ P_IP(SP)
	JMP  optMain

firstFound:
	MOVQ BX, O_SEQLEN(SP)
	MOVQ AX, O_SEQOFF(SP)
	CMPQ BX, O_SUFF(SP)
	JLE  firstPrices
	MOVQ $0, O_SEQPOS(SP)        // good enough: immediate encoding
	JMP  optEncode

firstPrices:
	// llen = LZ4HC_pricedLitlen(ip - anchor)
	MOVQ P_IP(SP), CX
	SUBQ P_ANCHOR(SP), CX
	CMPQ CX, $270
	JLT  llenSet
	LEAQ -15(CX), AX
	XORL DX, DX
	MOVL $255, R9
	DIVQ R9
	LEAQ 270(DX), CX             // reduceFrom + (llen - RUN_MASK) % 255

llenSet:
	// LZ4HC_setLiteral(opt, rPos, llen + rPos, LZ4HC_literalsPrice(llen + rPos))
	MOVQ O_OPT(SP), DI
	XORL R10, R10

firstLits:
	LEAQ (CX)(R10*1), R9         // litlen
	MOVQ R9, R14
	CMPQ R9, $15
	JLT  firstLitPrice
	LEAQ -15(R9), AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX
	LEAQ 1(R9)(AX*1), R14

firstLitPrice:
	SHLQ $2, R14
	MOVQ R10, AX
	SHLQ $4, AX
	MOVL R14, (DI)(AX*1)
	MOVL $0, 4(DI)(AX*1)
	MOVL $1, 8(DI)(AX*1)
	MOVL R9, 12(DI)(AX*1)
	INCQ R10
	CMPQ R10, $4
	JLO  firstLits

	XORL CX, CX                  // cur 0
	MOVQ O_SEQLEN(SP), BX
	MOVQ O_SEQOFF(SP), AX
	XORL DX, DX                  // lastPos 0
#ifdef OPT_FAV
	CALL hcAddMatchF<>(SB)
#else
	CALL hcAddMatch<>(SB)
#endif
	MOVQ DX, O_LASTPOS(SP)
	MOVQ $1, O_CUR(SP)

curLoop:
	MOVQ O_CUR(SP), CX
	CMPQ CX, O_LASTPOS(SP)
	JGE  curDone
	MOVQ P_IP(SP), R9
	ADDQ CX, R9                  // curPtr
	CMPQ R9, P_MFLIMIT(SP)
	JHI  curDone
	MOVQ CX, AX
	SHLQ $4, AX
	ADDQ O_OPT(SP), AX
	MOVL 16(AX), DX
	CMPL DX, (AX)
	JLE  curNext                 // opt[cur+1].price <= opt[cur].price
	MOVQ P_MAXATT(SP), R15
#ifdef OPT_FAV
	// favorDecSpeed: a position little cheaper than the next one is
	// searched only near lastPos, with nbSearchesSeq
	MOVL (AX), R13
	ADDL $4, R13
	CMPL DX, R13
	JGE  curSearch               // opt[cur+1].price >= opt[cur].price + LZ4HC_PRICE_SCALE
	MOVQ O_LASTPOS(SP), R13
	SUBQ CX, R13
	CMPQ R13, $4
	JGT  curNext                 // lastPos - cur > LZ4HC_SEQ_SEARCH_DIST
	MOVQ O_NBSEQ(SP), R15

curSearch:
#endif
	MOVQ R9, R10
	MOVL $3, BX
#ifdef OPT_FAV
	OPT_CALLF(s2x, s2d, s2c, s2done)
#else
	OPT_CALL(s2x, s2d, s2done)
#endif
	CMPQ BX, $4
	JLO  curNext
	MOVQ O_CUR(SP), CX
	CMPQ BX, O_SUFF(SP)
	JGT  curImmediate
	LEAQ (BX)(CX*1), DX
	CMPQ DX, $4096
	JGE  curImmediate
	MOVQ O_OPT(SP), DI
	MOVQ O_LASTPOS(SP), DX
#ifdef OPT_FAV
	CALL hcAddMatchF<>(SB)
#else
	CALL hcAddMatch<>(SB)
#endif
	MOVQ DX, O_LASTPOS(SP)

curNext:
	INCQ O_CUR(SP)
	JMP  curLoop

curImmediate:
	MOVQ CX, O_SEQPOS(SP)
	MOVQ BX, O_SEQLEN(SP)
	MOVQ AX, O_SEQOFF(SP)
	JMP  optEncode

curDone:
	MOVQ O_LASTPOS(SP), CX
	MOVQ CX, AX
	SHLQ $4, AX
	ADDQ O_OPT(SP), AX
	MOVLQSX 8(AX), DX            // lastSeq.len = opt[lastPos].mlen
	MOVQ DX, O_SEQLEN(SP)
	MOVLQSX 4(AX), BX
	MOVQ BX, O_SEQOFF(SP)
	SUBQ DX, CX
	MOVQ CX, O_SEQPOS(SP)        // lastSeqPos = lastPos - lastSeq.len

optEncode:
	// reverse traversal: move each step to its starting position
	MOVQ O_OPT(SP), DI
	MOVQ O_SEQPOS(SP), CX        // pos
	MOVQ O_SEQLEN(SP), BX        // mlen
	MOVQ O_SEQOFF(SP), DX        // off

revLoop:
	TESTQ CX, CX
	JLE  revDone
	MOVQ CX, AX
	SHLQ $4, AX
	MOVLQSX 8(DI)(AX*1), R9      // prevMlen
	MOVLQSX 4(DI)(AX*1), R10     // prevOff
	MOVL BX, 8(DI)(AX*1)
	MOVL DX, 4(DI)(AX*1)
	MOVQ R9, BX
	MOVQ R10, DX
	SUBQ BX, CX
	JMP  revLoop

revDone:
	MOVL BX, 8(DI)
	MOVL DX, 4(DI)
	MOVQ $0, O_RPOS(SP)

encLoop:
	MOVQ O_RPOS(SP), CX
	CMPQ CX, O_SEQPOS(SP)
	JGT  optMain
	MOVQ CX, AX
	SHLQ $4, AX
	ADDQ O_OPT(SP), AX
	MOVLQSX 8(AX), DX            // ml
	CMPQ DX, $1
	JNE  encSeq
	INCQ P_IP(SP)                // literal
	INCQ O_RPOS(SP)
	JMP  encLoop

encSeq:
	ADDQ DX, O_RPOS(SP)
	MOVQ DX, O_ML(SP)
	MOVLQSX 4(AX), DX
	MOVQ DX, O_OFFE(SP)
	ENCODE(O_ML, O_OFFE, fail, fail2, e1a, e1b, e1c, e1d, e1e, e1f, e1g, e1h, e1i, e1j, e1k)
	JMP  encLoop

endLoop:
	MOVQ $0, BX
	JMP  out

stopped:
	MOVQ $2, BX

out:
	MOVQ P_ARGS(SP), DI
	MOVQ P_IP(SP), AX
	SUBQ SI, AX
	MOVQ AX, A_IP(DI)
	MOVQ P_ANCHOR(SP), AX
	SUBQ SI, AX
	MOVQ AX, A_ANCHOR(DI)
	MOVQ P_OP(SP), AX
	SUBQ P_DSTBASE(SP), AX
	MOVQ AX, A_OP(DI)
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ BX, ret+8(FP)
	RET

fail:
	MOVQ O_ML(SP), AX
	MOVQ O_OFFE(SP), DX
	MOVQ $1, BX
	JMP  failOut

fail2:
	MOVQ $3, BX
	JMP  out

failOut:
	MOVQ P_ARGS(SP), DI
	MOVQ AX, A_MLEN(DI)
	MOVQ DX, A_MOFF(DI)
	JMP  out
