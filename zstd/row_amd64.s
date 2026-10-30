//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// func rowUpdateCachedAsm(hashTable *uint32, tagTable *byte, cache *[8]uint32, b *byte,
//	idx, end, baseIdx, rowLog, rowMask uint32, shift, hShift, prime, saltAdj uint64)
//
// Registers: AX hashTable, BX tagTable, DX cache, SI b - baseIdx (so an
// index addresses b directly), R8 idx, R9 end, R11 prime, R12 saltAdj,
// R13 shift, R14 hShift, CX shift counts, DI/R10/R15 scratch.
// No calls and no stack frame, so the out-of-object SI is never seen by the GC.
TEXT ·rowUpdateCachedAsm(SB), NOSPLIT, $0-88
	MOVQ hashTable+0(FP), AX
	MOVQ tagTable+8(FP), BX
	MOVQ cache+16(FP), DX
	MOVQ b+24(FP), SI
	MOVL idx+32(FP), R8
	MOVL end+36(FP), R9
	MOVL baseIdx+40(FP), R10
	SUBQ R10, SI
	MOVQ shift+56(FP), R13
	MOVQ hShift+64(FP), R14
	MOVQ prime+72(FP), R11
	MOVQ saltAdj+80(FP), R12

loop:
	// newHash = hash of idx + rowHashCacheSize
	MOVL R8, R10
	MOVQ 8(SI)(R10*1), R15
	MOVQ R13, CX
	SHLQ CX, R15
	IMULQ R11, R15
	XORQ R12, R15
	MOVQ R14, CX
	SHRQ CX, R15

	// prefetch its row (C: ZSTD_row_prefetch)
	MOVL R15, DI
	SHRL $8, DI
	MOVL rowLog+44(FP), CX
	SHLL CX, DI
	PREFETCHT0 (AX)(DI*4)
	CMPL CX, $5
	JB   pftag
	PREFETCHT0 64(AX)(DI*4)

pftag:
	PREFETCHT0 (BX)(DI*1)
	CMPL CX, $6
	JNE  pfdone
	PREFETCHT0 32(BX)(DI*1)

pfdone:
	// hash = cache[idx&7]; cache[idx&7] = newHash
	MOVL R8, R10
	ANDL $7, R10
	MOVL (DX)(R10*4), DI
	MOVL R15, (DX)(R10*4)
	MOVL DI, R15

	// relRow = (hash >> 8) << rowLog
	SHRL $8, DI
	SHLL CX, DI

	// pos = rowNextIndex(tagRow, rowMask)
	MOVBLZX (BX)(DI*1), R10
	DECL    R10
	MOVL    rowMask+48(FP), CX
	ANDL    CX, R10
	CMOVLEQ CX, R10
	MOVB    R10, (BX)(DI*1)

	// tagRow[pos] = byte(hash); hashTable[relRow+pos] = idx
	ADDQ R10, DI
	MOVB R15, (BX)(DI*1)
	MOVL R8, (AX)(DI*4)

	INCL R8
	CMPL R8, R9
	JB   loop
	RET
