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

// func rowSearchAsm(hashTable *uint32, tagTable *byte, b *byte, ip, iLimit int,
//	baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, nextToUpdate uint32) (ml int, best uint32)
//
// Registers: AX row, BX tagRow, SI b, DI ip, R8 iLimit, R9 baseIdx,
// R10 headGrouped, R11 matches, R12 rowMask, R13 numMatches,
// R14 nbAttempts, R15 lowLimit, CX/DX scratch. The candidates are kept in
// the 256-byte frame (C: matchBuffer).
TEXT ·rowSearchAsm(SB), NOSPLIT, $256-84
	MOVQ hashTable+0(FP), AX
	MOVQ tagTable+8(FP), BX
	MOVL relRow+44(FP), CX
	LEAQ (AX)(CX*4), AX
	ADDQ CX, BX
	MOVQ b+16(FP), SI
	MOVQ ip+24(FP), DI
	MOVQ iLimit+32(FP), R8
	MOVL baseIdx+40(FP), R9
	MOVL nbAttempts+56(FP), R14
	MOVL lowLimit+60(FP), R15

	// rowMask, headGrouped
	MOVL rowLog+52(FP), CX
	MOVL $1, R12
	SHLL CX, R12
	DECL R12
	MOVBLZX (BX), R10
	ANDL R12, R10

	// X0 = tag in every byte
	MOVL tag+48(FP), DX
	MOVQ DX, X0
	PUNPCKLBW X0, X0
	PUNPCKLWL X0, X0
	PSHUFL $0, X0, X0

	// matches = bit n set when tagRow[n] == tag, rotated right by
	// headGrouped within the row (C: ZSTD_row_getSSEMask)
	MOVOU     (BX), X1
	PCMPEQB   X0, X1
	PMOVMSKB  X1, R11
	MOVL      R10, CX
	CMPL      R12, $15
	JNE       mask32
	RORW      CX, R11
	MOVWLZX   R11, R11
	JMP       gather

mask32:
	MOVOU    16(BX), X2
	PCMPEQB  X0, X2
	PMOVMSKB X2, DX
	SHLL     $16, DX
	ORL      DX, R11
	CMPL     R12, $31
	JNE      mask64
	RORL     CX, R11
	JMP      gather

mask64:
	MOVOU    32(BX), X3
	PCMPEQB  X0, X3
	PMOVMSKB X3, DX
	SHLQ     $32, DX
	ORQ      DX, R11
	MOVOU    48(BX), X3
	PCMPEQB  X0, X3
	PMOVMSKB X3, DX
	SHLQ     $48, DX
	ORQ      DX, R11
	RORQ     CX, R11

gather:
	XORL R13, R13

gatherloop:
	TESTQ R11, R11
	JEQ   insert
	TESTL R14, R14
	JEQ   insert
	BSFQ  R11, CX
	LEAQ  -1(R11), DX
	ANDQ  DX, R11          // matches &= matches - 1
	ADDL  R10, CX
	ANDL  R12, CX          // matchPos
	TESTL CX, CX
	JEQ   gatherloop       // the head entry is not a candidate
	MOVL  (AX)(CX*4), DX   // matchIndex
	CMPL  DX, R15
	JB    insert           // below lowLimit: stop
	MOVL  DX, CX
	SUBL  R9, CX           // its position in b
	CMPQ  CX, DI
	JAE   bad              // not before ip: table invariant broken
	PREFETCHT0 (SI)(CX*1)
	MOVL  DX, 0(SP)(R13*4)
	INCL  R13
	DECL  R14
	JMP   gatherloop

insert:
	// insert ip (nextToUpdate) at rowNextIndex(tagRow, rowMask)
	MOVBLZX (BX), CX
	DECL    CX
	ANDL    R12, CX
	CMOVLEQ R12, CX
	MOVB    CX, (BX)
	MOVL    tag+48(FP), DX
	MOVB    DX, (BX)(CX*1)
	MOVL    nextToUpdate+64(FP), DX
	MOVL    DX, (AX)(CX*4)

	// longest match: R10 ml, R11 best, R12 k, R13 numMatches,
	// BX b+ip, R15 iLimit-ip, AX b+match, R14 length counted
	MOVL $3, R10
	XORL R11, R11
	XORL R12, R12
	LEAQ (SI)(DI*1), BX
	MOVQ R8, R15
	SUBQ DI, R15

cmploop:
	CMPQ R12, R13
	JAE  done
	MOVL 0(SP)(R12*4), DX
	INCQ R12
	MOVL DX, CX
	SUBL R9, CX
	LEAQ (SI)(CX*1), AX
	MOVL -3(AX)(R10*1), R14
	CMPL R14, -3(BX)(R10*1)
	JNE  cmploop

	// R14 = count(b, ip, match, iLimit)
	XORL R14, R14

cnt8:
	LEAQ 8(R14), CX
	CMPQ CX, R15
	JA   cnttail
	MOVQ (AX)(R14*1), CX
	XORQ (BX)(R14*1), CX
	JNE  cntdiff
	ADDQ $8, R14
	JMP  cnt8

cntdiff:
	BSFQ CX, CX
	SHRQ $3, CX
	ADDQ CX, R14
	JMP  cntdone

cnttail:
	CMPQ    R14, R15
	JAE     cntdone
	MOVBLZX (AX)(R14*1), CX
	MOVBLZX (BX)(R14*1), R8
	CMPL    CX, R8
	JNE     cntdone
	INCQ    R14
	JMP     cnttail

cntdone:
	CMPQ R14, R10
	JLE  cmploop
	MOVQ R14, R10
	MOVL DX, R11
	CMPQ R14, R15
	JNE  cmploop          // ip+ml == iLimit: best possible

done:
	MOVQ R10, ml+72(FP)
	MOVL R11, best+80(FP)
	RET

bad:
	MOVQ $-1, ml+72(FP)
	MOVL $0, best+80(FP)
	RET
