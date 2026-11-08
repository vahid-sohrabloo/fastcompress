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
//	llPos+156(4); frame $104-160 on amd64, $16-160 on arm64.
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
	MOVQ $0xCF1BBCDCB7A56463, AX
	MOVQ AX, PRIMEL

outer:
	MOVQ $1, STEP
	MOVL baseIdx+76(FP), AX
	MOVQ SI, R10
	SUBQ AX, R10
	LEAQ 256(R8), AX
	MOVQ AX, NEXTSTEP       // nextStep = ip + kStepIncr
	LEAQ 1(R8), R9          // ip1
	CMPQ R9, ILIMIT
	JGT  cleanup
	HASHL(R8, R12)
	MOVL (DI)(R12*4), R13   // idxl0
	MOVQ (SI)(R8*1), R11    // read64(ip)
	// inner starts 16 bytes past a 64-byte boundary (PCALIGN $64 also
	// aligns the function to 64): of the four 16-byte offsets, the one that
	// measured fastest, and it keeps the loop's layout independent of where
	// the linker puts the function. The padding is jumped over.
	JMP  inner
	PCALIGN $64
	LONG $0x90909090; LONG $0x90909090; LONG $0x90909090; LONG $0x90909090

	// The search loop keeps its state in registers, as C's does (no stores
	// but the two table updates): AX curr, DX idxs0, R12 hl0 then hl1,
	// R13 idxl0 then idxl1, R11 read64(ip) and, from norep, BX
	// read64(ip1), which becomes the next position's R11 (each position's
	// 8 bytes are read once, as C carries them). The hit paths save curr,
	// hl1 and idxl1 to CURR, HL1 and IDXL1 for the code after the match.
inner:
	MOVQ  R11, BX
	IMULQ PRIMES, BX
	MOVQ  hShiftS+104(FP), CX
	SHRQ  CX, BX            // hs0
	MOVL  baseIdx+76(FP), AX
	ADDL  R8, AX            // curr
	MOVL  (R15)(BX*4), DX   // idxs0
	MOVL  AX, (DI)(R12*4)   // hashLong[hl0] = curr
	MOVL  AX, (R15)(BX*4)   // hashSmall[hs0] = curr

	// repcode at ip+1: C compares first and tests offset1 > 0 only on
	// equality. The misses fall through: the loop's only taken branch is
	// then the one back to inner (C's layout). While offset1 == 0 (until a
	// frame's first match), every position takes the two taken branches
	// through maybeRep. gcc's own layout avoids that. It jumps past the
	// offset1 test on a miss and falls through it on equality. But it
	// measured slower (L3 x-ray +2.1%, x-ray -B16K +1.0%, xml
	// -B16K +0.8%), so the layout stays as is.
	MOVQ R8, BX
	SUBQ R14, BX            // ip - offset1
	MOVL 1(SI)(BX*1), CX
	CMPL CX, 1(SI)(R8*1)
	JEQ  maybeRep

norep:
	MOVQ  (SI)(R9*1), BX    // read64(ip1)
	MOVQ  BX, R12
	IMULQ PRIMEL, R12
	MOVQ  hShiftL+88(FP), CX
	SHRQ  CX, R12           // hl1 (hl0 is no longer needed)

	// long match at ip (see LONGCAND)
	MOVL AX, CX
	LONGCAND

nolong:
	MOVL (DI)(R12*4), R13   // idxl1 = hashLong[hl1] (idxl0 is no longer needed)

	// short match at ip (see SHORTCAND)
	SHORTCAND

noshort:
	CMPQ R9, NEXTSTEP
	JGE  stepinc

nostep:
	MOVQ R9, R8             // ip = ip1; hl0 = hl1 and idxl0 = idxl1 are in place
	MOVQ BX, R11
	ADDQ STEP, R9           // ip1 += step
	CMPQ R9, ILIMIT
	JLE  inner
	JMP  cleanup

stepinc:
	PREFETCHT0 64(SI)(R9*1)  // C: PREFETCH_L1(ip1 + 64), PREFETCH_L1(ip1 + 128)
	PREFETCHT0 128(SI)(R9*1)
	INCQ STEP
	ADDQ $256, NEXTSTEP
	JMP  nostep

maybeRep:
	TESTL R14, R14
	JEQ   norep
	MOVL  AX, CURR

repmatch:
	// mLength = count(b, ip+1+4, ip+1+4-offset1, iend) + 4
	LEAQ  5(SI)(BX*1), R10  // ip+1+4-offset1 (step is not needed after a repcode)
	LEAQ  5(SI)(R8*1), R9
	MOVQ  iend+56(FP), R13
	SUBQ  R8, R13
	SUBQ  $5, R13
	COUNTI(cnt1call, cnt1done)
	LEAQ  4(R11), AX
	INCQ  R8
	MOVQ  R8, R9
	SUBQ  anchor+120(FP), R9
	MOVL  $1, R12           // repcode1ToOffBase
	JMP   store

longmatch:
	MOVL CX, CURR
	MOVL R12, HL1
	MOVL R13, DX
	SUBL baseIdx+76(FP), DX  // matchl0
	MOVQ R9, IP1
	// mLength = count(b, ip+8, matchl0+8, iend) + 8
	LEAQ 8(SI)(R8*1), R9
	LEAQ 8(SI)(DX*1), R10
	MOVQ iend+56(FP), R13
	SUBQ R8, R13
	SUBQ $8, R13
	COUNTI(cnt2call, cnt2done)
	LEAQ 8(R11), AX
	MOVQ R8, R12
	SUBQ DX, R12            // offset
	JMP  catchup

searchlong:
	// C: _search_next_long.
	MOVL CX, CURR
	MOVL R12, HL1
	MOVL R13, IDXL1
	SUBL baseIdx+76(FP), DX // matchs0
	MOVQ R9, IP1
	LEAQ 4(SI)(R8*1), R9
	LEAQ 4(SI)(DX*1), R10
	MOVQ iend+56(FP), R13
	SUBQ R8, R13
	SUBQ $4, R13
	COUNTI(cnt3call, cnt3done)
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
	COUNTI(cnt4call, cnt4done)
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

	// store: R9 litLen (literals from anchor), R12 offBase, AX matchLength;
	// falls through to matchstored. Clobbers R9-R11, R13, BX, CX, X0.
store:
	TESTQ R9, R9
	JEQ   storeseq
	MOVQ  lits+32(FP), R10
	ADDQ  nLits+136(FP), R10
	MOVQ  anchor+120(FP), R11
	ADDQ  R9, nLits+136(FP)
	// up to 16 literals: one 16-byte copy (C: ZSTD_storeSeq's ZSTD_copy16).
	// The literal buffer has 16 bytes of slack (compressBlockDoubleFastNoDict
	// checks), and the source must hold the 16 bytes (else fastCopyLits).
	CMPQ  R9, $16
	JA    litcall
	LEAQ  16(R11), CX
	CMPQ  CX, iend+56(FP)
	JA    litcall
	MOVOU (SI)(R11*1), X0
	MOVOU X0, (R10)
	JMP   storeseq

litcall:
	ADDQ  SI, R11
	CALL  ·fastCopyLits(SB)

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
	COUNTI(cnt5call, cnt5done)
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
	// store the sequence (no literals, repcode1ToOffBase), as store does
	MOVQ  seqs+24(FP), R10
	MOVQ  nSeq+128(FP), R11
	MOVQ  $1, (R10)(R11*8)  // offBase 1, litLength 0
	LEAQ  -3(AX), CX        // mlBase = matchLength - MINMATCH
	MOVW  CX, 6(R10)(R11*8)
	CMPQ  CX, $0xFFFF
	JLS   repshort
	MOVL  $2, llType+152(FP) // lltMatchLength
	MOVL  R11, llPos+156(FP)

repshort:
	INCQ  R11
	MOVQ  R11, nSeq+128(FP)
	ADDQ AX, R8
	MOVQ R8, anchor+120(FP)
	JMP  reploop

cleanup:
	MOVL R14, rep1Out+144(FP)
	MOVL rep2+84(FP), AX
	MOVL AX, rep2Out+148(FP)
	RET

bad:
	MOVQ $-1, nSeq+128(FP)
	RET
