//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASH(pos, dst): dst = (le64(b[pos:]) * (prime << mlsShift)) >> hShift,
// which equals ((le64 << mlsShift) * prime) >> hShift mod 2^64.
#define HASH(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R24, dst, dst; \
	LSR  R25, dst, dst

// func compressBlockFastAsm(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
//	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockFastNoDictGeneric's loop (C: zstd_fast.c:
// ZSTD_compressBlock_fast_noDict_generic, _start to _cleanup), as
// zstd_fast_amd64.s.
//
// Registers: R0 b, R1 hashTable, R2 ip0, R3 ip1, R4 ip2 (ip3 = ip2 + 1),
// R5 hash0, R6 hash1, R7 matchIdx, R8 rep1, R9 step, R10 nextStep,
// R11 rep2, R12 anchor, R13 current0, R21 ilimit, R22 baseIdx,
// R23 prefixStartIndex, R24 prime << mlsShift, R25 hShift; R14-R17, R19,
// R20 scratch (the helpers clobber them). nSeq and nLits live in their
// result slots. After a match: R2 ip0, R5 match0, R6 mLength, R7 offBase;
// store takes R3 litLen and R10 the return selector.
TEXT ·compressBlockFastAsm(SB), NOSPLIT, $8-152
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
	// repcode at ip2
	MOVWU (R0)(R4), R14
	CMPW  R14, R15
	BNE   norep
	CBZW  R8, norep
	ADDW  R3, R22, R14
	MOVW  R14, (R1)(R6<<2)        // hashTable[hash1] = idx(ip1)
	B     foundrep

norep:
	CMPW  R23, R7
	BLO   miss1
	SUBW  R22, R7, R14
	CMP   R2, R14
	BHS   bad
	MOVWU (R0)(R14), R15
	MOVWU (R0)(R2), R16
	CMPW  R15, R16
	BNE   miss1
	ADDW  R3, R22, R14
	MOVW  R14, (R1)(R6<<2)        // hashTable[hash1] = idx(ip1)
	B     foundoffset

miss1:
	MOVWU (R1)(R6<<2), R7         // matchIdx = hashTable[hash1]
	MOVD  R6, R5                  // hash0 = hash1
	HASH(R4, R6)                  // hash1 = hash(ip2)
	MOVD  R3, R2                  // ip0 = ip1
	MOVD  R4, R3                  // ip1 = ip2
	ADD   $1, R4                  // ip2 = ip3
	ADDW  R2, R22, R13
	MOVW  R13, (R1)(R5<<2)
	CMPW  R23, R7
	BLO   miss2
	SUBW  R22, R7, R14
	CMP   R2, R14
	BHS   bad
	MOVWU (R0)(R14), R15
	MOVWU (R0)(R2), R16
	CMPW  R15, R16
	BNE   miss2
	CMP   $4, R9
	BHI   foundoffset
	ADDW  R3, R22, R14
	MOVW  R14, (R1)(R6<<2)
	B     foundoffset

miss2:
	MOVWU (R1)(R6<<2), R7
	MOVD  R6, R5
	HASH(R4, R6)
	MOVD  R3, R2                  // ip0 = ip1
	MOVD  R4, R3                  // ip1 = ip2
	ADD   R9, R2, R4              // ip2 = ip0 + step (ip3 = ip1 + step = ip2 + 1)
	CMP   R10, R4
	BLT   nostep
	ADD   $1, R9
	ADD   $128, R10

nostep:
	ADD  $1, R4, R14
	CMP  R21, R14
	BGE  cleanup
	B    inner

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

// fastCopyLits copies R16 (> 0) bytes from R15 to R14 without reading or
// writing outside either range. Clobbers R17, R19, V0.
TEXT ·fastCopyLits(SB), NOSPLIT|NOFRAME, $0-0
	CMP  $16, R16
	BLO  small
	MOVD ZR, R17

loop16:
	ADD  R17, R15, R19
	VLD1 (R19), [V0.B16]
	ADD  R17, R14, R19
	VST1 [V0.B16], (R19)
	ADD  $16, R17
	ADD  $16, R17, R19
	CMP  R16, R19
	BLS  loop16
	ADD  R16, R15, R19            // last 16 bytes (may overlap)
	SUB  $16, R19
	VLD1 (R19), [V0.B16]
	ADD  R16, R14, R19
	SUB  $16, R19
	VST1 [V0.B16], (R19)
	RET

small:
	CMP  $8, R16
	BLO  lt8
	MOVD (R15), R17
	MOVD R17, (R14)
	ADD  R16, R15, R19
	MOVD -8(R19), R17
	ADD  R16, R14, R19
	MOVD R17, -8(R19)
	RET

lt8:
	CMP   $4, R16
	BLO   lt4
	MOVWU (R15), R17
	MOVW  R17, (R14)
	ADD   R16, R15, R19
	MOVWU -4(R19), R17
	ADD   R16, R14, R19
	MOVW  R17, -4(R19)
	RET

lt4:
	MOVBU (R15), R17
	MOVB  R17, (R14)
	CMP   $2, R16
	BLO   done
	ADD   R16, R15, R19
	MOVHU -2(R19), R17
	ADD   R16, R14, R19
	MOVH  R17, -2(R19)

done:
	RET

// fastCount returns in R17 the length of the common prefix of R14[0:R16]
// and R15[0:R16] (C: ZSTD_count). Reads only within those ranges.
// Clobbers R14-R16, R19, R20.
TEXT ·fastCount(SB), NOSPLIT|NOFRAME, $0-0
	MOVD ZR, R17

cnt8:
	CMP    $8, R16
	BLT    tail
	MOVD.P 8(R14), R19
	MOVD.P 8(R15), R20
	EOR    R19, R20, R20
	CBNZ   R20, diff
	ADD    $8, R17
	SUB    $8, R16
	B      cnt8

diff:
	RBIT R20, R20
	CLZ  R20, R20
	ADD  R20>>3, R17, R17
	RET

tail:
	CMP     $0, R16
	BLE     out
	MOVBU.P 1(R14), R19
	MOVBU.P 1(R15), R20
	CMPW    R19, R20
	BNE     out
	ADD     $1, R17
	SUB     $1, R16
	B       tail

out:
	RET
