// Code included by zstd_double_fast_amd64.s: the body of
// compressBlockDoubleFastAsm and compressBlockDoubleFastAsmStale, which
// differ only in LONGCAND and SHORTCAND.
//
// go vet's asmdecl does not read #included files, so it does not check
// this body: every name+off(FP) here must match, by hand, the Go
// declarations of compressBlockDoubleFastAsm and
// compressBlockDoubleFastAsmStale (zstd_double_fast_amd64.go / _arm64.go)
// in offset and width (L/W loads and stores for the 4-byte fields), and
// the TEXT frame sizes in the .s must match too. Argument offsets (4-byte
// fields marked (4), the rest 8 bytes):
//	hashLong+0 hashSmall+8 b+16 seqs+24 lits+32 ip+40 istart+48 iend+56
//	prefixLowest+64 prefixLowestIndex+72(4) baseIdx+76(4) rep1+80(4)
//	rep2+84(4) hShiftL+88 shiftS+96 hShiftS+104 primeS+112 anchor+120
//	nSeq+128 nLits+136 rep1Out+144(4) rep2Out+148(4) llType+152(4)
//	llPos+156(4); frame $88-160 on amd64, $16-160 on arm64.
// No test checks the offsets as such: a mismatch shows up only through
// TestAsmEquiv / TestCompress2* (output must equal C's) if it changes the
// output on their inputs.

	MOVQ hashLong+0(FP), DI
	MOVQ hashSmall+8(FP), R15
	MOVQ b+16(FP), SI
	MOVQ ip+40(FP), R8
	MOVL rep1+80(FP), R14
	MOVQ istart+48(FP), AX
	MOVQ AX, anchor+120(FP)
	MOVQ $0, nSeq+128(FP)
	MOVQ $0, nLits+136(FP)
	MOVL $0, llType+152(FP)
	MOVL $0, llPos+156(FP)
	MOVQ iend+56(FP), AX
	SUBQ $8, AX
	MOVQ AX, ILIMIT
	MOVQ primeS+112(FP), AX
	MOVQ shiftS+96(FP), CX
	SHLQ CX, AX
	MOVQ AX, PRIMES

outer:
	MOVQ $1, STEP
	MOVL baseIdx+76(FP), AX
	MOVQ SI, R10
	SUBQ AX, R10
	LEAQ 256(R8), R11       // nextStep = ip + kStepIncr
	LEAQ 1(R8), R9          // ip1
	CMPQ R9, ILIMIT
	JGT  cleanup
	HASHL(R8, R12)
	MOVL (DI)(R12*4), R13   // idxl0

inner:
	HASHS(R8, BX)
	MOVL (R15)(BX*4), DX
	MOVL DX, IDXS0
	MOVL baseIdx+76(FP), AX
	ADDL R8, AX
	MOVL AX, CURR
	MOVL AX, (DI)(R12*4)    // hashLong[hl0] = curr
	MOVL AX, (R15)(BX*4)    // hashSmall[hs0] = curr

	// repcode at ip+1
	TESTL R14, R14
	JEQ   norep
	LEAQ  1(R8), AX
	SUBQ  R14, AX
	MOVL  (SI)(AX*1), DX
	CMPL  DX, 1(SI)(R8*1)
	JNE   norep
	// mLength = count(b, ip+1+4, ip+1+4-offset1, iend) + 4
	LEAQ  4(SI)(AX*1), R10  // ip+1+4-offset1 (step is not needed after a repcode)
	LEAQ  5(SI)(R8*1), R9
	MOVQ  iend+56(FP), R13
	SUBQ  R8, R13
	SUBQ  $5, R13
	CALL  ·fastCount(SB)
	LEAQ  4(R11), AX
	INCQ  R8
	MOVQ  R8, R9
	SUBQ  anchor+120(FP), R9
	MOVL  $1, R12           // repcode1ToOffBase
	MOVQ  $0, RETSEL           // return to matchstored
	JMP   store

norep:
	HASHL(R9, BX)
	MOVL BX, HL1

	// long match at ip (see LONGCAND)
	MOVL CURR, CX
	LONGCAND
	MOVL R13, DX
	SUBL baseIdx+76(FP), DX  // matchl0
	MOVQ R9, IP1
	// mLength = count(b, ip+8, matchl0+8, iend) + 8
	LEAQ 8(SI)(R8*1), R9
	LEAQ 8(SI)(DX*1), R10
	MOVQ iend+56(FP), R13
	SUBQ R8, R13
	SUBQ $8, R13
	CALL ·fastCount(SB)
	LEAQ 8(R11), AX
	MOVQ R8, R12
	SUBQ DX, R12            // offset
	JMP  catchup

nolong:
	MOVL (DI)(BX*4), AX
	MOVL AX, IDXL1          // idxl1 = hashLong[hl1]

	// short match at ip (see SHORTCAND)
	MOVL IDXS0, BX
	SHORTCAND

noshort:
	CMPQ R9, R11
	JLT  nostep
	INCQ STEP
	ADDQ $256, R11

nostep:
	MOVQ R9, R8             // ip = ip1
	ADDQ STEP, R9           // ip1 += step
	MOVL HL1, R12           // hl0 = hl1
	MOVL IDXL1, R13         // idxl0 = idxl1
	CMPQ R9, ILIMIT
	JGT  cleanup
	JMP  inner

searchlong:
	// C: _search_next_long.
	MOVL BX, DX
	SUBL baseIdx+76(FP), DX // matchs0
	MOVQ R9, IP1
	LEAQ 4(SI)(R8*1), R9
	LEAQ 4(SI)(DX*1), R10
	MOVQ iend+56(FP), R13
	SUBQ R8, R13
	SUBQ $4, R13
	CALL ·fastCount(SB)
	LEAQ 4(R11), AX         // mLength
	MOVQ R8, R12
	SUBQ DX, R12            // offset

	// long match at ip1
	MOVL IDXL1, BX
	CMPL BX, prefixLowestIndex+72(FP)
	JLS  catchup            // only if idxl1 > prefixLowestIndex
	SUBL baseIdx+76(FP), BX // matchl1
	MOVQ IP1, R9
	CMPQ BX, R9
	JAE  bad
	MOVQ (SI)(BX*1), CX
	CMPQ CX, (SI)(R9*1)
	JNE  catchup
	MOVQ AX, MLEN
	MOVQ BX, MATCHL1
	MOVL BX, CX
	// l1len = count(b, ip1+8, matchl1+8, iend) + 8
	MOVQ iend+56(FP), R13
	SUBQ R9, R13
	SUBQ $8, R13
	LEAQ 8(SI)(R9*1), R9
	LEAQ 8(SI)(CX*1), R10
	CALL ·fastCount(SB)
	LEAQ 8(R11), R11        // l1len
	MOVQ MATCHL1, BX
	MOVQ MLEN, AX
	CMPQ R11, AX
	JLE  catchup
	MOVQ IP1, R8            // ip = ip1
	MOVQ R11, AX
	MOVQ R8, R12
	SUBQ BX, R12            // offset = ip - matchl1
	MOVQ BX, DX             // matchs0 = matchl1

catchup:
	// while ip > anchor && match > prefixLowest && b[ip-1] == b[match-1]
	CMPQ    R8, anchor+120(FP)
	JLE     matchfound
	CMPQ    DX, prefixLowest+64(FP)
	JLE     matchfound
	MOVBLZX -1(SI)(R8*1), CX
	MOVBLZX -1(SI)(DX*1), BX
	CMPL    CX, BX
	JNE     matchfound
	DECQ    R8
	DECQ    DX
	INCQ    AX
	JMP     catchup

matchfound:
	// C: _match_found. R12 = offset.
	MOVL R14, rep2+84(FP)   // offset2 = offset1
	MOVL R12, R14           // offset1 = offset
	ADDL $3, R12            // offsetToOffBase(offset)
	CMPQ STEP, $4
	JGE  nohl1
	MOVL HL1, BX
	MOVQ IP1, CX
	ADDL baseIdx+76(FP), CX
	MOVL CX, (DI)(BX*4)     // hashLong[hl1] = idx(ip1)

nohl1:
	MOVQ R8, R9
	SUBQ anchor+120(FP), R9
	MOVQ $0, RETSEL
	JMP  store

matchstored:
	ADDQ AX, R8             // ip += mLength
	MOVQ R8, anchor+120(FP)
	CMPQ R8, ILIMIT
	JGT  outer

	// Complementary insertion
	MOVL CURR, R13
	ADDL $2, R13            // indexToInsert
	MOVL R13, R9
	SUBL baseIdx+76(FP), R9
	HASHL(R9, BX)
	MOVL R13, (DI)(BX*4)
	LEAQ -2(R8), R10
	HASHL(R10, BX)
	MOVL baseIdx+76(FP), DX
	ADDL R10, DX
	MOVL DX, (DI)(BX*4)
	HASHS(R9, BX)
	MOVL R13, (R15)(BX*4)
	LEAQ -1(R8), R10
	HASHS(R10, BX)
	MOVL baseIdx+76(FP), DX
	ADDL R10, DX
	MOVL DX, (R15)(BX*4)

reploop:
	// while ip <= ilimit && offset2 > 0 && read32(ip) == read32(ip-offset2)
	CMPQ  R8, ILIMIT
	JGT   outer
	MOVL  rep2+84(FP), DX
	TESTL DX, DX
	JEQ   outer
	MOVQ  R8, R10
	SUBQ  DX, R10
	MOVL  (SI)(R10*1), CX
	CMPL  CX, (SI)(R8*1)
	JNE   outer
	// rLength = count(b, ip+4, ip+4-offset2, iend) + 4
	LEAQ  4(SI)(R10*1), R10
	LEAQ  4(SI)(R8*1), R9
	MOVQ  iend+56(FP), R13
	SUBQ  R8, R13
	SUBQ  $4, R13
	CALL  ·fastCount(SB)
	LEAQ  4(R11), AX
	MOVL  rep2+84(FP), CX   // swap offset2 <=> offset1
	MOVL  R14, rep2+84(FP)
	MOVL  CX, R14
	HASHS(R8, BX)
	MOVL  baseIdx+76(FP), DX
	ADDL  R8, DX
	MOVL  DX, (R15)(BX*4)   // hashSmall[hashS(ip)] = idx(ip)
	HASHL(R8, BX)
	MOVL  DX, (DI)(BX*4)    // hashLong[hashL(ip)] = idx(ip)
	XORL  R9, R9            // no literals
	MOVL  $1, R12           // repcode1ToOffBase
	MOVQ  $1, RETSEL           // return to afterrep
	JMP   store

afterrep:
	ADDQ AX, R8
	MOVQ R8, anchor+120(FP)
	JMP  reploop

	// store: R9 litLen (literals from anchor), R12 offBase, AX matchLength;
	// returns to matchstored (RETSEL = 0) or afterrep (RETSEL = 1).
	// Clobbers R9-R11, R13, BX, CX, X0.
store:
	TESTQ R9, R9
	JEQ   storeseq
	MOVQ  lits+32(FP), R10
	ADDQ  nLits+136(FP), R10
	MOVQ  anchor+120(FP), R11
	ADDQ  SI, R11
	CALL  ·fastCopyLits(SB)
	ADDQ  R9, nLits+136(FP)

storeseq:
	MOVQ seqs+24(FP), R10
	MOVQ nSeq+128(FP), R11
	MOVL R12, (R10)(R11*8)     // offBase
	MOVW R9, 4(R10)(R11*8)     // litLength
	LEAQ -3(AX), CX            // mlBase = matchLength - MINMATCH
	MOVW CX, 6(R10)(R11*8)
	CMPQ R9, $0xFFFF
	JLS  nolongll
	MOVL $1, llType+152(FP)    // lltLiteralLength
	MOVL R11, llPos+156(FP)

nolongll:
	CMPQ CX, $0xFFFF
	JLS  nolongml
	MOVL $2, llType+152(FP)    // lltMatchLength
	MOVL R11, llPos+156(FP)

nolongml:
	INCQ R11
	MOVQ R11, nSeq+128(FP)
	CMPQ RETSEL, $0
	JEQ  matchstored
	JMP  afterrep

cleanup:
	MOVL R14, rep1Out+144(FP)
	MOVL rep2+84(FP), AX
	MOVL AX, rep2Out+148(FP)
	RET

bad:
	MOVQ $-1, nSeq+128(FP)
	RET
