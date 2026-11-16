// The body of hcAddMatch (lz4hc_parse_arm64.s): lz4hc_addmatch_amd64.h
// without favorDecSpeed, step for step. R4 &opt[cur] then &opt[cur + ml],
// R3 ll then the price, R7 the start price / scratch, R8 ml, R9 lastPos+3-cur,
// R16 the next ml where the price grows, R17 the last ml compared.

	LSL  $4, R13, R4
	ADD  R11, R4, R4             // &opt[cur]
	MOVW 12(R4), R3              // ll = opt[cur].litlen
	MOVD ZR, R7
	CMP  R13, R3
	BGT  amBase
	SUB  R3, R13, R8
	LSL  $4, R8, R8
	MOVW (R11)(R8), R7           // LZ4HC_runStartPrice: opt[cur-ll].price

amBase:
	// + LZ4HC_literalsPrice(ll)
	MOVD R3, R8
	CMP  $15, R3
	BLT  amLit
	SUB  $15, R3, R16
	MUL  R21, R16, R16
	LSR  $39, R16, R16
	ADD  R16, R3, R8
	ADD  $1, R8, R8

amLit:
	ADD  R8<<2, R7, R7           // basePrice + literals price
	LSL  $32, R12, R12           // the offset, in the high half of opt[pos]'s first 8 bytes
	// price = basePrice + LZ4HC_sequencePrice(ll, ml): + 12 below ml 19,
	// + 4 more at ml = 19, 19+255, ...
	ADD  $12, R7, R3
	MOVD $19, R16                // the next ml where the price grows
	ADD  $3, R14, R9
	SUB  R13, R9, R9             // pos > lastPos + 3  <=>  ml > lastPos + 3 - cur
	MOVD R9, R17
	CMP  R10, R17
	CSEL GT, R10, R17, R17       // the last ml compared with opt[pos].price
	MOVD $4, R8                  // ml
	ADD  $64, R4, R4             // &opt[cur + 4]

amCmp:
	CMP  R17, R8
	BGT  amFill
	CMP  R16, R8
	BNE  amCmpPrice
	ADD  $4, R3, R3
	ADD  $255, R16, R16

amCmpPrice:
	MOVW  (R4), R7
	CMP   R7, R3
	BGT   amCmpNext              // price > opt[pos].price
	MOVWU R3, R7                 // LZ4HC_setMatch: price, off, mlen = ml, litlen 0
	ORR   R12, R7, R7
	MOVD  R7, (R4)
	MOVD  R8, 8(R4)
	CMP   R10, R8
	BNE   amCmpNext
	ADD   R10, R13, R7           // pos of the whole match
	CMP   R7, R14
	CSEL  LT, R7, R14, R14       // lastPos = pos

amCmpNext:
	ADD $1, R8, R8
	ADD $16, R4, R4
	JMP amCmp

amFill:
	// pos > lastPos + 3: every length is set
	CMP  R10, R8
	BGT  amTail

amFillLoop:
	CMP  R16, R8
	BNE  amFillSet
	ADD  $4, R3, R3
	ADD  $255, R16, R16

amFillSet:
	MOVWU R3, R7
	ORR   R12, R7, R7
	MOVD  R7, (R4)
	MOVD  R8, 8(R4)
	ADD   $1, R8, R8
	ADD   $16, R4, R4
	CMP   R10, R8
	BLE   amFillLoop
	ADD   R10, R13, R14          // lastPos = cur + len (> lastPos + 3)

amTail:
	// LZ4HC_setLiteral(opt, lastPos+addLit, addLit, opt[lastPos].price + 4*addLit)
	LSL   $4, R14, R4
	ADD   R11, R4, R4
	MOVWU (R4), R3
	ADDW  $4, R3, R16
	MOVD  R16, 16(R4)            // price, off 0
	MOVD  $0x100000001, R16
	MOVD  R16, 24(R4)            // mlen 1, litlen 1
	ADDW  $8, R3, R16
	MOVD  R16, 32(R4)
	MOVD  $0x200000001, R16
	MOVD  R16, 40(R4)
	ADDW  $12, R3, R16
	MOVD  R16, 48(R4)
	MOVD  $0x300000001, R16
	MOVD  R16, 56(R4)
	RET
