// Code included by zstd_fast_arm64.s: the body of compressBlockFastAsm
// and compressBlockFastAsmStale, which differ only in CANDIDATE(hit, miss).
//
// go vet's asmdecl does not read #included files, so it does not check
// this body: every name+off(FP) here must match, by hand, the Go
// declarations of compressBlockFastAsm and compressBlockFastAsmStale
// (zstd_fast_amd64.go / _arm64.go) in offset and width (L/W loads and
// stores for the 4-byte fields), and the TEXT frame sizes in the .s must
// match too. Argument offsets (4-byte fields marked (4), the rest 8
// bytes):
//	hashTable+0 b+8 seqs+16 lits+24 ip0+32 istart+40 iend+48
//	prefixStart+56 prefixStartIndex+64(4) baseIdx+68(4) rep1+72(4)
//	rep2+76(4) stepSize+80 mlsShift+88 hShift+96 prime+104 anchor+112
//	nSeq+120 nLits+128 rep1Out+136(4) rep2Out+140(4) llType+144(4)
//	llPos+148(4); frame $40-152 on amd64, $8-152 on arm64.
// No test checks the offsets as such: a mismatch shows up only through
// TestAsmEquiv / TestCompress2* (output must equal C's) if it changes the
// output on their inputs.

	MOVD  hashTable+0(FP), R1
	MOVD  b+8(FP), R0
	MOVWU rep1+72(FP), R8
	MOVWU rep2+76(FP), R11
	MOVD  ip0+32(FP), R2
	MOVD  istart+40(FP), R12
	MOVD  ZR, nSeq+120(FP)
	MOVD  ZR, nLits+128(FP)
	MOVW  ZR, llType+144(FP)
	MOVW  ZR, llPos+148(FP)
	MOVD  iend+48(FP), R21
	SUB   $8, R21                 // ilimit = iend - HASH_READ_SIZE
	MOVWU baseIdx+68(FP), R22
	MOVWU prefixStartIndex+64(FP), R23
	MOVD  prime+104(FP), R24
	MOVD  mlsShift+88(FP), R14
	LSL   R14, R24, R24
	MOVD  hShift+96(FP), R25

start:
	MOVD stepSize+80(FP), R9
	ADD  $128, R2, R10            // nextStep = ip0 + kStepIncr
	ADD  $1, R2, R3
	ADD  R9, R2, R4
	ADD  $1, R4, R14              // ip3
	CMP  R21, R14
	BGE  cleanup
	HASH(R2, R5)
	HASH(R3, R6)
	MOVWU (R1)(R5<<2), R7

inner:
	// rval = read32(ip2 - rep1)
	SUB   R8, R4, R14
	MOVWU (R0)(R14), R15
	// current0 = idx(ip0); hashTable[hash0] = current0
	ADDW  R2, R22, R13
	MOVW  R13, (R1)(R5<<2)
	// repcode at ip2. The misses fall through: the loop's only taken
	// branch is then the one back to inner (C's layout).
	MOVWU (R0)(R4), R14
	CMPW  R14, R15
	BEQ   maybeRep

norep:
	CANDIDATE(hit1, miss1)

miss1:
	MOVWU (R1)(R6<<2), R7         // matchIdx = hashTable[hash1]
	MOVD  R6, R5                  // hash0 = hash1
	HASH(R4, R6)                  // hash1 = hash(ip2)
	MOVD  R3, R2                  // ip0 = ip1
	MOVD  R4, R3                  // ip1 = ip2
	ADD   $1, R4                  // ip2 = ip3
	ADDW  R2, R22, R13
	MOVW  R13, (R1)(R5<<2)
	CANDIDATE(hit2, miss2)

miss2:
	MOVWU (R1)(R6<<2), R7
	MOVD  R6, R5
	HASH(R4, R6)
	MOVD  R3, R2                  // ip0 = ip1
	MOVD  R4, R3                  // ip1 = ip2
	ADD   R9, R2, R4              // ip2 = ip0 + step (ip3 = ip1 + step = ip2 + 1)
	CMP   R10, R4
	BGE   stepinc

nostep:
	ADD  $1, R4, R14
	CMP  R21, R14
	BLT  inner
	B    cleanup

stepinc:
	ADD $1, R9
	ADD $128, R10
	B   nostep

maybeRep:
	CBZW R8, norep
	ADDW R3, R22, R14
	MOVW R14, (R1)(R6<<2)         // hashTable[hash1] = idx(ip1)
	B    foundrep

hit1:
	ADDW R3, R22, R14
	MOVW R14, (R1)(R6<<2)         // hashTable[hash1] = idx(ip1)
	B    foundoffset

hit2:
	CMP  $4, R9
	BHI  foundoffset
	ADDW R3, R22, R14
	MOVW R14, (R1)(R6<<2)
	B    foundoffset

foundrep:
	MOVD  R4, R2                  // ip0 = ip2
	SUB   R8, R2, R5              // match0
	MOVD  ZR, R6
	ADD   R2, R0, R14
	MOVBU -1(R14), R15
	ADD   R5, R0, R14
	MOVBU -1(R14), R16
	CMPW  R15, R16
	BNE   rep0
	MOVD  $1, R6
	SUB   $1, R2
	SUB   $1, R5

rep0:
	ADD  $4, R6
	MOVD $1, R7                   // repcode1ToOffBase
	B    match

foundoffset:
	SUBW R22, R7, R5              // match0
	MOVD R8, R11                  // rep2 = rep1
	SUB  R5, R2, R8               // rep1 = ip0 - match0
	ADD  $3, R8, R7               // offsetToOffBase(rep1)
	MOVD $4, R6
	MOVD prefixStart+56(FP), R15

back:
	CMP   R12, R2
	BLE   match
	CMP   R15, R5
	BLE   match
	ADD   R2, R0, R14
	MOVBU -1(R14), R16
	ADD   R5, R0, R14
	MOVBU -1(R14), R17
	CMPW  R16, R17
	BNE   match
	SUB   $1, R2
	SUB   $1, R5
	ADD   $1, R6
	B     back

match:
	// mLength += count(b, ip0+mLength, match0+mLength, iend)
	ADD  R6, R2, R14
	MOVD iend+48(FP), R16
	SUB  R14, R16, R16
	ADD  R0, R14, R14
	ADD  R6, R5, R15
	ADD  R0, R15, R15
	CALL ·fastCount(SB)
	ADD  R17, R6

	// storeSeq(ss, ip0-anchor, b[anchor:], offBase, mLength)
	SUB  R12, R2, R3
	MOVD ZR, R10                  // return to aftermatch
	B    store

aftermatch:
	ADD  R6, R2                   // ip0 += mLength
	MOVD R2, R12                  // anchor = ip0
	CMP  R21, R2
	BGT  start

	// Fill table
	ADDW $2, R13, R14             // current0 + 2
	SUBW R22, R14, R15
	HASH(R15, R16)
	MOVW R14, (R1)(R16<<2)
	SUB  $2, R2, R15
	HASH(R15, R16)
	ADDW R15, R22, R14
	MOVW R14, (R1)(R16<<2)
	CBZW R11, start               // repOffset2 == 0 means invalidated

reploop:
	CMP   R21, R2
	BGT   start
	SUB   R11, R2, R15
	MOVWU (R0)(R15), R16
	MOVWU (R0)(R2), R17
	CMPW  R16, R17
	BNE   start
	// rLength = count(b, ip0+4, ip0+4-rep2, iend) + 4
	ADD   R0, R15, R15
	ADD   $4, R15
	ADD   R0, R2, R14
	ADD   $4, R14
	MOVD  iend+48(FP), R16
	SUB   R2, R16, R16
	SUB   $4, R16
	CALL  ·fastCount(SB)
	ADD   $4, R17, R6
	// swap rep1 and rep2
	MOVD  R11, R14
	MOVD  R8, R11
	MOVD  R14, R8
	// hashTable[hash(ip0)] = idx(ip0)
	HASH(R2, R16)
	ADDW  R2, R22, R14
	MOVW  R14, (R1)(R16<<2)
	MOVD  ZR, R3                  // no literals
	MOVD  $1, R7                  // repcode1ToOffBase
	MOVD  $1, R10                 // return to afterrep
	B     store

afterrep:
	ADD  R6, R2
	MOVD R2, R12
	B    reploop

	// store: R3 litLen (literals from anchor), R7 offBase, R6 matchLength;
	// returns to aftermatch (R10 = 0) or afterrep (R10 = 1).
	// Clobbers R14-R17, R19, R20, V0.
store:
	CBZ  R3, storeseq
	MOVD lits+24(FP), R14
	MOVD nLits+128(FP), R17
	ADD  R17, R14
	ADD  R12, R0, R15
	MOVD R3, R16
	CALL ·fastCopyLits(SB)
	MOVD nLits+128(FP), R17
	ADD  R3, R17
	MOVD R17, nLits+128(FP)

storeseq:
	MOVD seqs+16(FP), R14
	MOVD nSeq+120(FP), R15
	ADD  R15<<3, R14, R14
	MOVW R7, (R14)                // offBase
	MOVH R3, 4(R14)               // litLength
	SUB  $3, R6, R16              // mlBase = matchLength - MINMATCH
	MOVH R16, 6(R14)
	CMP  $0xFFFF, R3
	BLS  nolongll
	MOVD $1, R17                  // lltLiteralLength
	MOVW R17, llType+144(FP)
	MOVW R15, llPos+148(FP)

nolongll:
	CMP  $0xFFFF, R16
	BLS  nolongml
	MOVD $2, R17                  // lltMatchLength
	MOVW R17, llType+144(FP)
	MOVW R15, llPos+148(FP)

nolongml:
	ADD  $1, R15
	MOVD R15, nSeq+120(FP)
	CBZ  R10, aftermatch
	B    afterrep

cleanup:
	MOVD R12, anchor+112(FP)
	MOVW R8, rep1Out+136(FP)
	MOVW R11, rep2Out+140(FP)
	RET

bad:
	MOVD $-1, R14
	MOVD R14, nSeq+120(FP)
	RET

