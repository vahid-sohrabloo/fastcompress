// The body of hcAddMatch (lz4hc_parse_amd64.s), included after each
// variant's TEXT line: ADD_FAV is favorDecSpeed.

	MOVQ R11, (8+W_MP)(SP)
	MOVQ R12, (8+W_FWD)(SP)
	MOVQ CX, R10
	SHLQ $4, R10
	ADDQ DI, R10                 // &opt[cur]
	MOVLQSX 12(R10), R9          // ll = opt[cur].litlen
	XORL R13, R13
	CMPQ R9, CX
	JGT  amBase
	MOVQ CX, R14
	SUBQ R9, R14
	SHLQ $4, R14
	MOVLQSX (DI)(R14*1), R13     // LZ4HC_runStartPrice: opt[cur-ll].price

amBase:
	// + LZ4HC_literalsPrice(ll)
	MOVQ R9, R14
	CMPQ R9, $15
	JLT  amLit
	LEAQ -15(R9), R11
	MOVL $0x80808081, R12
	IMULQ R12, R11
	SHRQ $39, R11
	LEAQ 1(R9)(R11*1), R14

amLit:
	LEAQ (R13)(R14*4), R13       // basePrice + literals price
#ifdef ADD_FAV
	INCQ R13                     // + LZ4HC_SEQ_PRICE_FDS
#endif
	SHLQ $32, AX                 // the offset, in the high half of opt[pos]'s first 8 bytes
	// price = basePrice + LZ4HC_sequencePrice(ll, ml): + 12 below ml 19,
	// + 4 more at ml = 19, 19+255, ...
	LEAQ 12(R13), R9
	MOVL $19, R11                // the next ml where the price grows
	MOVQ DX, R15
	ADDQ $3, R15
	SUBQ CX, R15                 // pos > lastPos + 3  <=>  ml > lastPos + 3 - cur
	MOVQ R15, R12
	CMPQ R12, BX
	CMOVQGT BX, R12              // the last ml compared with opt[pos].price
	MOVL $4, R14                 // ml
	ADDQ $64, R10                // &opt[cur + 4]

amCmp:
	CMPQ R14, R12
	JGT  amFill
	CMPQ R14, R11
	JNE  amCmpPrice
	ADDQ $4, R9
	ADDQ $255, R11

amCmpPrice:
	MOVLQSX (R10), R13
	CMPQ R9, R13
#ifdef ADD_FAV
	JGE  amCmpNext               // price > opt[pos].price - 1
#else
	JGT  amCmpNext               // price > opt[pos].price
#endif
	MOVL R9, R13                 // LZ4HC_setMatch: price, off, mlen = ml, litlen 0
	ORQ  AX, R13
	MOVQ R13, (R10)
	MOVQ R14, 8(R10)
	CMPQ R14, BX
	JNE  amCmpNext
	LEAQ (CX)(BX*1), R13         // pos of the whole match
	CMPQ DX, R13
	CMOVQLT R13, DX              // lastPos = pos

amCmpNext:
	INCQ R14
	ADDQ $16, R10
	JMP  amCmp

amFill:
	// pos > lastPos + 3: every length is set
	CMPQ R14, BX
	JGT  amTail

amFillLoop:
	CMPQ R14, R11
	JNE  amFillSet
	ADDQ $4, R9
	ADDQ $255, R11

amFillSet:
	MOVL R9, R13
	ORQ  AX, R13
	MOVQ R13, (R10)
	MOVQ R14, 8(R10)
	INCQ R14
	ADDQ $16, R10
	CMPQ R14, BX
	JLE  amFillLoop
	LEAQ (CX)(BX*1), DX          // lastPos = cur + len (> lastPos + 3)

amTail:
	// LZ4HC_setLiteral(opt, lastPos+addLit, addLit, opt[lastPos].price + 4*addLit)
	MOVQ DX, R10
	SHLQ $4, R10
	ADDQ DI, R10
	MOVL (R10), R9
	LEAL 4(R9), R11
	MOVQ R11, 16(R10)            // price, off 0
	MOVQ $0x100000001, R11
	MOVQ R11, 24(R10)            // mlen 1, litlen 1
	LEAL 8(R9), R11
	MOVQ R11, 32(R10)
	MOVQ $0x200000001, R11
	MOVQ R11, 40(R10)
	LEAL 12(R9), R11
	MOVQ R11, 48(R10)
	MOVQ $0x300000001, R11
	MOVQ R11, 56(R10)
	MOVQ (8+W_MP)(SP), R11
	MOVQ (8+W_FWD)(SP), R12
	RET
