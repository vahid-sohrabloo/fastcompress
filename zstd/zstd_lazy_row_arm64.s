//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// func rowUpdateCachedAsm(hashTable *uint32, tagTable *byte, cache *[8]uint32, b *byte,
//	idx, end, baseIdx, rowLog, rowMask uint32, shift, hShift, prime, saltAdj uint64)
//
// Registers: R0 hashTable, R1 tagTable, R2 cache, R3 b - baseIdx (so an
// index addresses b directly), R4 idx, R5 end, R6 prime << shift, R7 saltAdj,
// R8 hShift, R9 rowLog, R10 rowMask, R11-R17 scratch. No calls and no stack
// frame, so the out-of-object R3 is never seen by the GC.
TEXT ·rowUpdateCachedAsm(SB), NOSPLIT|NOFRAME, $0-88
	MOVD  hashTable+0(FP), R0
	MOVD  tagTable+8(FP), R1
	MOVD  cache+16(FP), R2
	MOVD  b+24(FP), R3
	MOVWU idx+32(FP), R4
	MOVWU end+36(FP), R5
	MOVWU baseIdx+40(FP), R11
	SUB   R11, R3, R3
	MOVWU rowLog+44(FP), R9
	MOVWU rowMask+48(FP), R10
	MOVD  shift+56(FP), R11
	MOVD  prime+72(FP), R6
	LSL   R11, R6, R6           // (v << shift) * prime == v * (prime << shift)
	MOVD  saltAdj+80(FP), R7
	MOVD  hShift+64(FP), R8

loop:
	// newHash = hash of idx + rowHashCacheSize
	ADD  R4, R3, R11
	MOVD 8(R11), R12
	MUL  R6, R12, R12
	EOR  R7, R12, R12
	LSR  R8, R12, R12

	// prefetch its row (C: ZSTD_row_prefetch)
	LSRW $8, R12, R13
	LSLW R9, R13, R13
	ADD  R13<<2, R0, R14
	PRFM (R14), PLDL1KEEP
	CMPW $5, R9
	BLO  pftag
	PRFM 64(R14), PLDL1KEEP

pftag:
	ADD  R13, R1, R14
	PRFM (R14), PLDL1KEEP
	CMPW $6, R9
	BNE  pfdone
	PRFM 32(R14), PLDL1KEEP

pfdone:
	// hash = cache[idx&7]; cache[idx&7] = newHash
	ANDW  $7, R4, R11
	MOVWU (R2)(R11<<2), R15
	MOVW  R12, (R2)(R11<<2)

	// relRow = (hash >> 8) << rowLog
	LSRW $8, R15, R13
	LSLW R9, R13, R13

	// pos = rowNextIndex(tagRow, rowMask)
	MOVBU (R1)(R13), R16
	SUBW  $1, R16
	ANDW  R10, R16, R16
	CBNZW R16, pos
	MOVW  R10, R16

pos:
	MOVB R16, (R1)(R13)

	// tagRow[pos] = byte(hash); hashTable[relRow+pos] = idx
	ADD  R16, R13, R13
	MOVB R15, (R1)(R13)
	MOVW R4, (R0)(R13<<2)

	ADDW $1, R4
	CMPW R5, R4
	BLO  loop
	RET
