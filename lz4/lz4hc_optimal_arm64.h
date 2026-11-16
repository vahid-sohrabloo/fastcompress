// The body of hcOptimalAsm (lz4hc_parse_arm64.s): lz4hc_optimal_amd64.h
// without favorDecSpeed, step for step.

	PARSE_SETUP(limited, olimSet)
	MOVD A_SUFF(R11), R12
	MOVD R12, SL(O_SUFF)
	MOVD A_OPT(R11), R12
	MOVD R12, SL(O_OPT)

optMain:
	MOVD SL(P_IP), R3
	MOVD SL(P_MFLIMIT), R16
	CMP  R16, R3
	BHI  endLoop
	MOVD SL(P_STOP), R16
	CMP  R16, R3
	BHI  stopped
	MOVD R3, R4
	MOVD $3, R10
	MOVD SL(P_MAXATT), R9
	BL   hcWiderOpt<>(SB)        // LZ4HC_FindLongestMatch
	CMP  $4, R10
	BHS  firstFound
	INCSLOT(P_IP)
	JMP  optMain

firstFound:
	MOVD R10, SL(O_SEQLEN)
	MOVD R12, SL(O_SEQOFF)
	MOVD SL(O_SUFF), R16
	CMP  R16, R10
	BLE  firstPrices
	MOVD ZR, SL(O_SEQPOS)        // good enough: immediate encoding
	JMP  optEncode

firstPrices:
	// llen = LZ4HC_pricedLitlen(ip - anchor)
	MOVD SL(P_IP), R13
	MOVD SL(P_ANCHOR), R16
	SUB  R16, R13, R13
	CMP  $270, R13
	BLT  llenSet
	SUB  $15, R13, R12
	MOVD $255, R3
	UDIV R3, R12, R16
	MUL  R3, R16, R16
	SUB  R16, R12, R14
	ADD  $270, R14, R13          // reduceFrom + (llen - RUN_MASK) % 255

llenSet:
	// LZ4HC_setLiteral(opt, rPos, llen + rPos, LZ4HC_literalsPrice(llen + rPos))
	MOVD SL(O_OPT), R11
	MOVD ZR, R4

firstLits:
	ADD  R4, R13, R3             // litlen
	MOVD R3, R8
	CMP  $15, R3
	BLT  firstLitPrice
	SUB  $15, R3, R12
	MUL  R21, R12, R12
	LSR  $39, R12, R12
	ADD  R12, R3, R8
	ADD  $1, R8, R8

firstLitPrice:
	LSL  $2, R8, R8
	LSL  $4, R4, R12
	ADD  R11, R12, R12
	MOVW R8, (R12)
	MOVW ZR, 4(R12)
	MOVD $1, R16
	MOVW R16, 8(R12)
	MOVW R3, 12(R12)
	ADD  $1, R4, R4
	CMP  $4, R4
	BLO  firstLits

	MOVD ZR, R13                 // cur 0
	MOVD SL(O_SEQLEN), R10
	MOVD SL(O_SEQOFF), R12
	MOVD ZR, R14                 // lastPos 0
	BL   hcAddMatch<>(SB)
	MOVD R14, SL(O_LASTPOS)
	MOVD $1, R16
	MOVD R16, SL(O_CUR)

curLoop:
	MOVD SL(O_CUR), R13
	MOVD SL(O_LASTPOS), R16
	CMP  R16, R13
	BGE  curDone
	MOVD SL(P_IP), R3
	ADD  R13, R3, R3             // curPtr
	MOVD SL(P_MFLIMIT), R16
	CMP  R16, R3
	BHI  curDone
	LSL  $4, R13, R12
	MOVD SL(O_OPT), R16
	ADD  R16, R12, R12
	MOVW 16(R12), R14
	MOVW (R12), R16
	CMPW R16, R14
	BLE  curNext                 // opt[cur+1].price <= opt[cur].price
	MOVD SL(P_MAXATT), R9
	MOVD R3, R4
	MOVD $3, R10
	BL   hcWiderOpt<>(SB)
	CMP  $4, R10
	BLO  curNext
	MOVD SL(O_CUR), R13
	MOVD SL(O_SUFF), R16
	CMP  R16, R10
	BGT  curImmediate
	ADD  R13, R10, R14
	CMP  $4096, R14
	BGE  curImmediate
	MOVD SL(O_OPT), R11
	MOVD SL(O_LASTPOS), R14
	BL   hcAddMatch<>(SB)
	MOVD R14, SL(O_LASTPOS)

curNext:
	INCSLOT(O_CUR)
	JMP  curLoop

curImmediate:
	MOVD R13, SL(O_SEQPOS)
	MOVD R10, SL(O_SEQLEN)
	MOVD R12, SL(O_SEQOFF)
	JMP  optEncode

curDone:
	MOVD SL(O_LASTPOS), R13
	LSL  $4, R13, R12
	MOVD SL(O_OPT), R16
	ADD  R16, R12, R12
	MOVW 8(R12), R14             // lastSeq.len = opt[lastPos].mlen
	MOVD R14, SL(O_SEQLEN)
	MOVW 4(R12), R10
	MOVD R10, SL(O_SEQOFF)
	SUB  R14, R13, R13
	MOVD R13, SL(O_SEQPOS)       // lastSeqPos = lastPos - lastSeq.len

optEncode:
	// reverse traversal: move each step to its starting position
	MOVD SL(O_OPT), R11
	MOVD SL(O_SEQPOS), R13       // pos
	MOVD SL(O_SEQLEN), R10       // mlen
	MOVD SL(O_SEQOFF), R14       // off

revLoop:
	CMP  $0, R13
	BLE  revDone
	LSL  $4, R13, R12
	ADD  R11, R12, R12
	MOVW 8(R12), R3              // prevMlen
	MOVW 4(R12), R4              // prevOff
	MOVW R10, 8(R12)
	MOVW R14, 4(R12)
	MOVD R3, R10
	MOVD R4, R14
	SUB  R10, R13, R13
	JMP  revLoop

revDone:
	MOVW R10, 8(R11)
	MOVW R14, 4(R11)
	MOVD ZR, SL(O_RPOS)

encLoop:
	MOVD SL(O_RPOS), R13
	MOVD SL(O_SEQPOS), R16
	CMP  R16, R13
	BGT  optMain
	LSL  $4, R13, R12
	MOVD SL(O_OPT), R16
	ADD  R16, R12, R12
	MOVW 8(R12), R14             // ml
	CMP  $1, R14
	BNE  encSeq
	INCSLOT(P_IP)                // literal
	INCSLOT(O_RPOS)
	JMP  encLoop

encSeq:
	MOVD SL(O_RPOS), R16
	ADD  R14, R16, R16
	MOVD R16, SL(O_RPOS)
	MOVD R14, SL(O_ML)
	MOVW 4(R12), R14
	MOVD R14, SL(O_OFFE)
	ENCODE(O_ML, O_OFFE, fail, fail2, e1a, e1b, e1c, e1d, e1e, e1f, e1g, e1h, e1i, e1j, e1k)
	JMP  encLoop

endLoop:
	MOVD ZR, R10
	JMP  out

stopped:
	MOVD $2, R10

out:
	MOVD SL(P_ARGS), R11
	MOVD SL(P_IP), R12
	SUB  R1, R12, R12
	MOVD R12, A_IP(R11)
	MOVD SL(P_ANCHOR), R12
	SUB  R1, R12, R12
	MOVD R12, A_ANCHOR(R11)
	MOVD SL(P_OP), R12
	MOVD SL(P_DSTBASE), R16
	SUB  R16, R12, R12
	MOVD R12, A_OP(R11)
	MOVD SL(W_NEXT), R12
	MOVD R12, A_NEXT(R11)
	MOVD R10, ret+8(FP)
	RET

fail:
	MOVD SL(O_ML), R12
	MOVD SL(O_OFFE), R14
	MOVD $1, R10
	JMP  failOut

fail2:
	MOVD $3, R10
	JMP  out

failOut:
	MOVD SL(P_ARGS), R11
	MOVD R12, A_MLEN(R11)
	MOVD R14, A_MOFF(R11)
	JMP  out
