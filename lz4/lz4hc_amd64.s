//go:build amd64 && !appengine && !noasm && !purego && gc

// The candidate loop of LZ4HC_InsertAndGetWiderMatch (lz4 v1.11.1
// lib/lz4hc.c, Copyright (c) Yann Collet, BSD 2-Clause) for candidates
// within the prefix, with chainSwap (LZ4HC_sparsestChain, levels 10 to 12)
// and favorDecSpeed.
// It follows the Go loop in insertAndGetWiderMatch and returns to it where
// C's loop needs more: a candidate in the external dictionary, or the
// pattern analysis.

#include "textflag.h"

// hcChainArgs offsets (TestHCChainArgsLayout)
#define A_WIN     0
#define A_CHAIN   8
#define A_IP      16
#define A_ILOW    24
#define A_IHIGH   32
#define A_PREFIX  40
#define A_LOWEST  48
#define A_IPINDEX 56
#define A_PATTERN 64
#define A_MAXLEN  72
#define A_PA      80
#define A_MINDEX  88
#define A_NBATT   96
#define A_LONGEST 104
#define A_OFFSET  112
#define A_SBACK   120
#define A_HASH    128
#define A_NEXT    136
#define A_INSERT  144
#define A_SWAP    152
#define A_FAVOR   160
#define A_CPOS    168

// func hcChainAsm(a *hcChainArgs) int
//
// Runs iterations of the loop while matchIndex >= lowestMatchIndex,
// nbAttempts > 0 and longest < maxLength (then returns 0), for candidates
// matchIndex >= prefixIdx; it returns 1 at a candidate below prefixIdx
// (nothing done for it) and 2 after a candidate whose chain distance is 1
// with patternAnalysis (candidate evaluated, nbAttempts decremented, the
// chain not followed). matchIndex, nbAttempts, longest, offset, sBack and
// matchChainPos are read from and written back to a.
//
// Registers: DI a, SI win, R8 chain, R9 ip, R10 iLowLimit, R11 iHighLimit
// (pointers), R12 prefixIdx, R14 matchIndex, R15 nbAttempts, BX longest;
// AX, CX, DX, R13 scratch; 0(SP) the back count (then the end of the
// sparsest chain search), 8(SP) matchLength, 16(SP) its accel.
//
// Preconditions (checked by insertAndGetWiderMatch): win holds
// [0, iHighLimit); 0 <= iLowLimit <= ip < iHighLimit; maxLength =
// iHighLimit - iLowLimit; longest > ip - iLowLimit (lookBack); every
// candidate reached is below ipIndex (table and chain entries are earlier
// positions; chain distances are >= 1 except at the PA return).
//
// BOUNDS:
// (1) A candidate is read only when prefixIdx <= matchIndex (else return
//     1) and matchIndex < ipIndex (table and chain entries are earlier
//     positions), so mp = win + matchIndex - prefixIdx is in [win, ip).
// (2) The 2-byte probe at iLowLimit + longest - 1: 0 <= longest - 1 and
//     iLowLimit + longest + 1 <= iLowLimit + maxLength = iHighLimit, below
//     the end of win. The one at mp - lookBack + longest - 1 is at most
//     longest - 1 - lookBack bytes past mp; it lies in [win, ip + longest)
//     because the callers pass longest >= lookBack + 1 (C relies on the
//     same: _Search2 passes longest = m1.len with lookBack m1.len - 2,
//     _Search3 longest = m2.len with lookBack m2.len - 3, FindBestMatch
//     lookBack 0), and mp - lookBack + longest - 1 >= mp >= win.
// (3) countBack reads ip[back-4..back) and mp[back-4..back) only while
//     back - 4 >= max(iLowLimit - ip, win - mp), then single bytes above the
//     same minimum: within [iLowLimit, ip) and [win, mp).
// (4) count reads ip+4.. below iHighLimit (8-byte loads while 8 bytes
//     remain, then 4, 2, 1) and mp+4.. at the same distance, which is below
//     ip + (iHighLimit - ip) - (ip - mp) < iHighLimit.
// (5) The chain is read at (uint16)(matchIndex + pos), within its 65536
//     entries.
//
// With a.insert != 0 it first runs LZ4HC_Insert up to ip (a.nextToUpdate is
// updated) and takes matchIndex from the hash table, as the start of
// LZ4HC_InsertAndGetWiderMatch.
//
// BOUNDS (insert): positions nextToUpdate <= idx < ipIndex, with
// nextToUpdate >= prefixIdx (Go checks it), are read as 4 bytes at win +
// idx - prefixIdx <= ip - 1, below iHighLimit; the hash is 15 bits (the
// table's 32768 entries); the chain is written at (uint16)idx.
TEXT ·hcChainAsm(SB), NOSPLIT, $24-16
	MOVQ a+0(FP), DI
	MOVQ A_WIN(DI), SI
	MOVQ A_CHAIN(DI), R8
	MOVQ A_IP(DI), R9
	ADDQ SI, R9
	MOVQ A_ILOW(DI), R10
	ADDQ SI, R10
	MOVQ A_IHIGH(DI), R11
	ADDQ SI, R11
	MOVQ A_PREFIX(DI), R12
	CMPQ A_INSERT(DI), $0
	JEQ  noInsert
	MOVQ A_HASH(DI), R15
	MOVQ A_NEXT(DI), CX
	MOVQ A_IPINDEX(DI), DX

insertLoop:
	CMPL CX, DX
	JHS  insertDone
	MOVL CX, AX
	SUBL R12, AX
	MOVL (SI)(AX*1), AX
	IMUL3L $-1640531535, AX, AX
	SHRL $17, AX                 // LZ4HC_hashPtr
	MOVL CX, BX
	SUBL (R15)(AX*4), BX         // delta = idx - hashTable[h]
	CMPL BX, $65535
	JLS  deltaOK
	MOVL $65535, BX

deltaOK:
	MOVWLZX CX, R13
	MOVW BX, (R8)(R13*2)         // DELTANEXTU16(chainTable, idx) = delta
	MOVL CX, (R15)(AX*4)         // hashTable[h] = idx
	INCL CX
	JMP  insertLoop

insertDone:
	MOVQ DX, A_NEXT(DI)
	MOVL (R9), AX
	IMUL3L $-1640531535, AX, AX
	SHRL $17, AX
	MOVL (R15)(AX*4), R14        // matchIndex = hashTable[LZ4HC_hashPtr(ip)]
	MOVQ R14, A_MINDEX(DI)

noInsert:
	MOVQ A_MINDEX(DI), R14
	MOVQ A_NBATT(DI), R15
	MOVQ A_LONGEST(DI), BX

loop:
	CMPL R14, A_LOWEST(DI)
	JLO  done
	TESTQ R15, R15
	JLE  done
	CMPQ BX, A_MAXLEN(DI)
	JGE  done
	CMPL R14, R12
	JLO  extCandidate
	DECQ R15
	MOVQ $0, 8(SP)               // matchLength
	CMPQ A_FAVOR(DI), $0
	JEQ  candidate
	MOVQ A_IPINDEX(DI), AX
	SUBL R14, AX
	CMPL AX, $8
	JLO  afterCand               // favorDecSpeed skips matches with offset < 8

candidate:
	MOVL R14, AX
	SUBL R12, AX
	ADDQ SI, AX                  // mp
	MOVWLZX -1(R10)(BX*1), CX    // iLowLimit + longest - 1
	MOVQ AX, DX
	SUBQ R9, DX
	ADDQ R10, DX                 // mp - lookBack
	MOVWLZX -1(DX)(BX*1), DX
	CMPL CX, DX
	JNE  afterCand
	MOVL (AX), CX
	CMPL CX, A_PATTERN(DI)
	JNE  afterCand

	// back = LZ4HC_countBack(ip, mp, iLowLimit, prefixPtr), if lookBack
	XORL DX, DX
	CMPQ R9, R10
	JEQ  backDone
	MOVQ R10, CX
	SUBQ R9, CX                  // iMin - ip
	MOVQ SI, R13
	SUBQ AX, R13                 // mMin - match
	CMPQ R13, CX
	CMOVQGT R13, CX              // min = max(...)

back4:
	MOVQ DX, R13
	SUBQ CX, R13
	CMPQ R13, $3
	JLE  backTail
	MOVL -4(R9)(DX*1), R13
	XORL -4(AX)(DX*1), R13
	JNE  backDiff
	SUBQ $4, DX
	JMP  back4

backDiff:
	BSRL R13, R13
	XORL $31, R13                // leading zeros
	SHRL $3, R13
	SUBQ R13, DX
	JMP  backDone

backTail:
	CMPQ DX, CX
	JLE  backDone
	MOVB -1(R9)(DX*1), R13
	CMPB R13, -1(AX)(DX*1)
	JNE  backDone
	DECQ DX
	JMP  backTail

backDone:
	MOVQ DX, 0(SP)

	// matchLength = MINMATCH + LZ4_count(ip+4, mp+4, iHighLimit) - back
	LEAQ 4(R9), CX
	LEAQ 4(AX), DX
	LEAQ -7(R11), R13

count8:
	CMPQ CX, R13
	JHS  countTail
	MOVQ (CX), AX
	XORQ (DX), AX
	JNE  countDiff
	ADDQ $8, CX
	ADDQ $8, DX
	JMP  count8

countDiff:
	BSFQ AX, AX
	SHRQ $3, AX
	ADDQ AX, CX
	JMP  countDone

countTail:
	LEAQ -3(R11), R13
	CMPQ CX, R13
	JHS  count2
	MOVL (CX), AX
	CMPL AX, (DX)
	JNE  count2
	ADDQ $4, CX
	ADDQ $4, DX

count2:
	LEAQ -1(R11), R13
	CMPQ CX, R13
	JHS  count1
	MOVWLZX (CX), AX
	CMPW AX, (DX)
	JNE  count1
	ADDQ $2, CX
	ADDQ $2, DX

count1:
	CMPQ CX, R11
	JHS  countDone
	MOVB (CX), AX
	CMPB AL, (DX)
	JNE  countDone
	INCQ CX

countDone:
	SUBQ R9, CX                  // MINMATCH + count
	MOVQ 0(SP), DX
	SUBQ DX, CX                  // - back
	MOVQ CX, 8(SP)               // matchLength
	CMPQ CX, BX
	JLE  afterCand
	MOVQ CX, BX                  // longest
	MOVQ A_IPINDEX(DI), AX
	SUBL R14, AX
	MOVQ AX, A_OFFSET(DI)        // ipIndex - matchIndex
	MOVQ DX, A_SBACK(DI)

afterCand:
	// chainSwap: a longer match must also match at every position of this
	// one: jump to the chain going farthest back (LZ4HC_sparsestChain)
	CMPQ A_SWAP(DI), $0
	JEQ  follow
	CMPQ 8(SP), BX
	JNE  follow                  // matchLength != longest
	MOVL R14, AX
	ADDL BX, AX
	CMPL AX, A_IPINDEX(DI)
	JHI  follow                  // matchIndex + longest > ipIndex
	MOVL $1, CX                  // distanceToNextMatch
	XORL DX, DX                  // pos
	LEAQ -3(BX), AX
	MOVQ AX, 0(SP)               // end = longest - MINMATCH + 1
	MOVQ $16, 16(SP)             // accel = 1 << kTrigger

sparsest:
	CMPQ DX, 0(SP)
	JGE  sparsestDone
	MOVL R14, AX
	ADDL DX, AX
	MOVWLZX AX, AX
	MOVWLZX (R8)(AX*2), AX       // candidateDist
	MOVQ 16(SP), R13
	SHRQ $4, R13                 // step = accel >> kTrigger
	INCQ 16(SP)                  // accel++
	CMPL AX, CX
	JLS  sparsestNext
	MOVL AX, CX
	MOVQ DX, A_CPOS(DI)          // matchChainPos = pos
	MOVQ $16, 16(SP)

sparsestNext:
	ADDQ R13, DX
	JMP  sparsest

sparsestDone:
	CMPL CX, $1
	JLS  follow
	CMPL CX, R14
	JHI  done                    // avoid overflow
	SUBL CX, R14
	JMP  loop

follow:
	MOVWLZX R14, AX
	MOVWLZX (R8)(AX*2), AX       // DELTANEXTU16(chainTable, matchIndex)
	CMPQ A_PA(DI), $0
	JEQ  step
	CMPL AX, $1
	JNE  step
	CMPQ A_CPOS(DI), $0
	JEQ  patternOut

step:
	MOVQ A_CPOS(DI), CX
	TESTQ CX, CX
	JEQ  stepSub
	MOVL R14, AX
	ADDL CX, AX
	MOVWLZX AX, AX
	MOVWLZX (R8)(AX*2), AX       // DELTANEXTU16(chainTable, matchIndex + matchChainPos)

stepSub:
	SUBL AX, R14
	JMP  loop

done:
	MOVQ $0, ret+8(FP)
	JMP  out

extCandidate:
	MOVQ $1, ret+8(FP)
	JMP  out

patternOut:
	MOVQ $2, ret+8(FP)

out:
	MOVQ R14, A_MINDEX(DI)
	MOVQ R15, A_NBATT(DI)
	MOVQ BX, A_LONGEST(DI)
	RET
