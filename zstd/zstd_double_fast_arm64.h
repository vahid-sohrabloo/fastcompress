// Code included by zstd_double_fast_arm64.s: the body of
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

	MOVD  hashLong+0(FP), R1
	MOVD  hashSmall+8(FP), R2
	MOVD  b+16(FP), R0
	MOVD  ip+40(FP), R3
	MOVWU rep1+80(FP), R9
	MOVD  istart+48(FP), R14
	MOVD  R14, anchor+120(FP)
	MOVD  ZR, nSeq+128(FP)
	MOVD  ZR, nLits+136(FP)
	MOVW  ZR, llType+152(FP)
	MOVW  ZR, llPos+156(FP)
	MOVD  iend+56(FP), R13
	SUB   $8, R13               // ilimit
	MOVWU baseIdx+76(FP), R21
	MOVWU prefixLowestIndex+72(FP), R22
	MOVD  $0xCF1BBCDCB7A56463, R23
	MOVD  hShiftL+88(FP), R24
	MOVD  primeS+112(FP), R25
	MOVD  shiftS+96(FP), R14
	LSL   R14, R25, R25
	MOVD  hShiftS+104(FP), R26

outer:
	MOVD  $1, R5                // step
	ADD   $256, R3, R6          // nextStep = ip + kStepIncr
	ADD   $1, R3, R4            // ip1
	CMP   R13, R4
	BGT   cleanup
	HASHL(R3, R7)
	MOVWU (R1)(R7<<2), R8       // idxl0

inner:
	HASHS(R3, R14)
	MOVWU (R2)(R14<<2), R15
	MOVW  R15, IDXS0
	ADDW  R3, R21, R10          // curr
	MOVW  R10, (R1)(R7<<2)      // hashLong[hl0] = curr
	MOVW  R10, (R2)(R14<<2)     // hashSmall[hs0] = curr

	// repcode at ip+1
	CBZW  R9, norep
	ADD   $1, R3, R14
	SUB   R9, R14, R14
	MOVWU (R0)(R14), R15
	ADD   $1, R3, R16
	MOVWU (R0)(R16), R16
	CMPW  R15, R16
	BNE   norep
	// mLength = count(b, ip+1+4, ip+1+4-offset1, iend) + 4
	ADD   R0, R14, R15
	ADD   $4, R15
	ADD   R0, R3, R14
	ADD   $5, R14
	MOVD  iend+56(FP), R16
	SUB   R3, R16, R16
	SUB   $5, R16
	CALL  ·fastCount(SB)
	ADD   $4, R17, R7
	ADD   $1, R3
	MOVD  anchor+120(FP), R14
	SUB   R14, R3, R4
	MOVD  $1, R8                // repcode1ToOffBase
	MOVD  ZR, R5                // return to matchstored
	B     store

norep:
	HASHL(R4, R11)              // hl1

	// long match at ip (see LONGCAND); R14 = matchl0
	LONGCAND
	MOVD  R14, R6               // matchl0
	// mLength = count(b, ip+8, matchl0+8, iend) + 8
	ADD   R0, R3, R14
	ADD   $8, R14
	ADD   R0, R6, R15
	ADD   $8, R15
	MOVD  iend+56(FP), R16
	SUB   R3, R16, R16
	SUB   $8, R16
	CALL  ·fastCount(SB)
	ADD   $8, R17, R7
	SUB   R6, R3, R8            // offset
	B     catchup

nolong:
	MOVWU (R1)(R11<<2), R12     // idxl1 = hashLong[hl1]

	// short match at ip (see SHORTCAND); R14 = matchs0
	MOVWU IDXS0, R20
	SHORTCAND

noshort:
	CMP   R6, R4
	BLT   nostep
	ADD   $1, R5
	ADD   $256, R6

nostep:
	MOVD  R4, R3                // ip = ip1
	ADD   R5, R4                // ip1 += step
	MOVD  R11, R7               // hl0 = hl1
	MOVD  R12, R8               // idxl0 = idxl1
	CMP   R13, R4
	BGT   cleanup
	B     inner

searchlong:
	// C: _search_next_long. R14 = matchs0.
	MOVD  R14, R6
	ADD   R0, R3, R14
	ADD   $4, R14
	ADD   R0, R6, R15
	ADD   $4, R15
	MOVD  iend+56(FP), R16
	SUB   R3, R16, R16
	SUB   $4, R16
	CALL  ·fastCount(SB)
	ADD   $4, R17, R7           // mLength
	SUB   R6, R3, R8            // offset

	// long match at ip1
	CMPW  R22, R12
	BLS   catchup               // only if idxl1 > prefixLowestIndex
	SUBW  R21, R12, R12         // matchl1
	CMP   R4, R12
	BHS   bad
	MOVD  (R0)(R12), R15
	MOVD  (R0)(R4), R16
	CMP   R15, R16
	BNE   catchup
	// l1len = count(b, ip1+8, matchl1+8, iend) + 8
	ADD   R0, R4, R14
	ADD   $8, R14
	ADD   R0, R12, R15
	ADD   $8, R15
	MOVD  iend+56(FP), R16
	SUB   R4, R16, R16
	SUB   $8, R16
	CALL  ·fastCount(SB)
	ADD   $8, R17               // l1len
	CMP   R7, R17
	BLE   catchup
	MOVD  R4, R3                // ip = ip1
	MOVD  R17, R7
	SUB   R12, R3, R8           // offset = ip - matchl1
	MOVD  R12, R6               // matchs0 = matchl1

catchup:
	// while ip > anchor && match > prefixLowest && b[ip-1] == b[match-1]
	MOVD  anchor+120(FP), R14
	MOVD  prefixLowest+64(FP), R15

cloop:
	CMP   R14, R3
	BLE   matchfound
	CMP   R15, R6
	BLE   matchfound
	ADD   R3, R0, R16
	MOVBU -1(R16), R16
	ADD   R6, R0, R17
	MOVBU -1(R17), R17
	CMPW  R16, R17
	BNE   matchfound
	SUB   $1, R3
	SUB   $1, R6
	ADD   $1, R7
	B     cloop

matchfound:
	// C: _match_found. R8 = offset.
	MOVW  R9, rep2+84(FP)       // offset2 = offset1
	MOVWU R8, R9                // offset1 = offset
	ADD   $3, R8                // offsetToOffBase(offset)
	CMP   $4, R5
	BGE   nohl1
	ADDW  R4, R21, R14
	MOVW  R14, (R1)(R11<<2)     // hashLong[hl1] = idx(ip1)

nohl1:
	MOVD  anchor+120(FP), R14
	SUB   R14, R3, R4
	MOVD  ZR, R5
	B     store

matchstored:
	ADD   R7, R3                // ip += mLength
	MOVD  R3, anchor+120(FP)
	CMP   R13, R3
	BGT   outer

	// Complementary insertion
	ADDW  $2, R10, R14          // indexToInsert
	SUBW  R21, R14, R15
	HASHL(R15, R16)
	MOVW  R14, (R1)(R16<<2)
	SUB   $2, R3, R17
	HASHL(R17, R16)
	ADDW  R17, R21, R19
	MOVW  R19, (R1)(R16<<2)
	HASHS(R15, R16)
	MOVW  R14, (R2)(R16<<2)
	SUB   $1, R3, R17
	HASHS(R17, R16)
	ADDW  R17, R21, R19
	MOVW  R19, (R2)(R16<<2)

reploop:
	// while ip <= ilimit && offset2 > 0 && read32(ip) == read32(ip-offset2)
	CMP   R13, R3
	BGT   outer
	MOVWU rep2+84(FP), R14
	CBZW  R14, outer
	SUB   R14, R3, R15
	MOVWU (R0)(R15), R16
	MOVWU (R0)(R3), R17
	CMPW  R16, R17
	BNE   outer
	// rLength = count(b, ip+4, ip+4-offset2, iend) + 4
	ADD   R0, R15, R15
	ADD   $4, R15
	ADD   R0, R3, R14
	ADD   $4, R14
	MOVD  iend+56(FP), R16
	SUB   R3, R16, R16
	SUB   $4, R16
	CALL  ·fastCount(SB)
	ADD   $4, R17, R7
	MOVWU rep2+84(FP), R14      // swap offset2 <=> offset1
	MOVW  R9, rep2+84(FP)
	MOVD  R14, R9
	HASHS(R3, R16)
	ADDW  R3, R21, R14
	MOVW  R14, (R2)(R16<<2)     // hashSmall[hashS(ip)] = idx(ip)
	HASHL(R3, R16)
	MOVW  R14, (R1)(R16<<2)     // hashLong[hashL(ip)] = idx(ip)
	MOVD  ZR, R4                // no literals
	MOVD  $1, R8                // repcode1ToOffBase
	MOVD  $1, R5                // return to afterrep
	B     store

afterrep:
	ADD   R7, R3
	MOVD  R3, anchor+120(FP)
	B     reploop

	// store: R4 litLen (literals from anchor), R8 offBase, R7 matchLength;
	// returns to matchstored (R5 = 0) or afterrep (R5 = 1).
	// Clobbers R14-R17, R19, R20, V0.
store:
	CBZ   R4, storeseq
	MOVD  lits+32(FP), R14
	MOVD  nLits+136(FP), R17
	ADD   R17, R14
	MOVD  anchor+120(FP), R15
	ADD   R0, R15
	MOVD  R4, R16
	CALL  ·fastCopyLits(SB)
	MOVD  nLits+136(FP), R17
	ADD   R4, R17
	MOVD  R17, nLits+136(FP)

storeseq:
	MOVD  seqs+24(FP), R14
	MOVD  nSeq+128(FP), R15
	ADD   R15<<3, R14, R14
	MOVW  R8, (R14)             // offBase
	MOVH  R4, 4(R14)            // litLength
	SUB   $3, R7, R16           // mlBase = matchLength - MINMATCH
	MOVH  R16, 6(R14)
	CMP   $0xFFFF, R4
	BLS   nolongll
	MOVD  $1, R17               // lltLiteralLength
	MOVW  R17, llType+152(FP)
	MOVW  R15, llPos+156(FP)

nolongll:
	CMP   $0xFFFF, R16
	BLS   nolongml
	MOVD  $2, R17               // lltMatchLength
	MOVW  R17, llType+152(FP)
	MOVW  R15, llPos+156(FP)

nolongml:
	ADD   $1, R15
	MOVD  R15, nSeq+128(FP)
	CBZ   R5, matchstored
	B     afterrep

cleanup:
	MOVW  R9, rep1Out+144(FP)
	MOVWU rep2+84(FP), R14
	MOVW  R14, rep2Out+148(FP)
	RET

bad:
	MOVD  $-1, R14
	MOVD  R14, nSeq+128(FP)
	RET
