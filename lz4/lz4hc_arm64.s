//go:build arm64 && !appengine && !noasm && !purego && gc

// The candidate loop of LZ4HC_InsertAndGetWiderMatch (lz4 v1.11.1
// lib/lz4hc.c, Copyright (c) Yann Collet, BSD 2-Clause) for candidates
// within the prefix, with chainSwap (LZ4HC_sparsestChain, levels 10 to 12)
// and favorDecSpeed.
// It follows the Go loop in insertAndGetWiderMatch (and lz4hc_amd64.s step
// for step) and returns to it where C's loop needs more: a candidate in the
// external dictionary, or the pattern analysis.

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
// Registers: R0 a, R1 win, R2 chain, R3 ip, R4 iLowLimit, R5 iHighLimit
// (pointers), R6 prefixIdx, R7 matchIndex, R8 nbAttempts, R9 longest; R14
// the back count (then the end of the sparsest chain search), R15
// matchLength, R16 its accel; R19 lowest, R20 maxLength, R21 ipIndex, R22
// pattern, R23 favor, R24 pa, R25 chainSwap, R26 matchChainPos (a's fields);
// R10-R13, R17 scratch.
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
TEXT ·hcChainAsm(SB), NOSPLIT, $0-16
	MOVD a+0(FP), R0
	MOVD A_WIN(R0), R1
	MOVD A_CHAIN(R0), R2
	MOVD A_IP(R0), R3
	ADD  R1, R3, R3
	MOVD A_ILOW(R0), R4
	ADD  R1, R4, R4
	MOVD A_IHIGH(R0), R5
	ADD  R1, R5, R5
	MOVD A_PREFIX(R0), R6
	MOVD A_INSERT(R0), R10
	CBZ  R10, noInsert
	MOVD A_HASH(R0), R13
	MOVD A_NEXT(R0), R11
	MOVD A_IPINDEX(R0), R12
	MOVW $2654435761, R16
	MOVD $65535, R17

insertLoop:
	CMPW  R12, R11
	BHS   insertDone
	SUBW  R6, R11, R10
	MOVWU (R1)(R10), R10
	MULW  R16, R10, R10
	LSRW  $17, R10, R10          // LZ4HC_hashPtr
	MOVWU (R13)(R10<<2), R9
	SUBW  R9, R11, R9            // delta = idx - hashTable[h]
	CMPW  R17, R9
	CSELW HI, R17, R9, R9        // min(delta, 65535)
	ANDW  $0xFFFF, R11, R15
	MOVH  R9, (R2)(R15<<1)       // DELTANEXTU16(chainTable, idx) = delta
	MOVW  R11, (R13)(R10<<2)     // hashTable[h] = idx
	ADDW  $1, R11, R11
	JMP   insertLoop

insertDone:
	MOVD  R12, A_NEXT(R0)
	MOVWU (R3), R10
	MULW  R16, R10, R10
	LSRW  $17, R10, R10
	MOVWU (R13)(R10<<2), R7      // matchIndex = hashTable[LZ4HC_hashPtr(ip)]
	MOVD  R7, A_MINDEX(R0)

noInsert:
	MOVD A_MINDEX(R0), R7
	MOVD A_NBATT(R0), R8
	MOVD A_LONGEST(R0), R9
	MOVD A_LOWEST(R0), R19
	MOVD A_MAXLEN(R0), R20
	MOVD A_IPINDEX(R0), R21
	MOVD A_PATTERN(R0), R22
	MOVD A_FAVOR(R0), R23
	MOVD A_PA(R0), R24
	MOVD A_SWAP(R0), R25
	MOVD A_CPOS(R0), R26

loop:
	CMPW R19, R7
	BLO  done
	CMP  $0, R8
	BLE  done
	CMP  R20, R9
	BGE  done
	CMPW R6, R7
	BLO  extCandidate
	SUB  $1, R8, R8
	MOVD ZR, R15                 // matchLength
	CBZ  R23, candidate
	SUBW R7, R21, R10
	CMPW $8, R10
	BLO  afterCand               // favorDecSpeed skips matches with offset < 8

candidate:
	SUBW  R6, R7, R10
	ADD   R1, R10, R10           // mp
	ADD   R9, R4, R11
	MOVHU -1(R11), R11           // iLowLimit + longest - 1
	SUB   R3, R10, R12
	ADD   R4, R12, R12           // mp - lookBack
	ADD   R9, R12, R12
	MOVHU -1(R12), R12
	CMPW  R11, R12
	BNE   afterCand
	MOVWU (R10), R11
	CMPW  R22, R11
	BNE   afterCand

	// back = LZ4HC_countBack(ip, mp, iLowLimit, prefixPtr), if lookBack
	MOVD ZR, R12
	CMP  R4, R3
	BEQ  backDone
	SUB  R3, R4, R11             // iMin - ip
	SUB  R10, R1, R13            // mMin - match
	CMP  R11, R13
	CSEL GT, R13, R11, R11       // min = max(...)

back4:
	SUB   R11, R12, R13
	CMP   $3, R13
	BLE   backTail
	ADD   R12, R3, R13
	MOVWU -4(R13), R13
	ADD   R12, R10, R17
	MOVWU -4(R17), R17
	EORW  R17, R13, R13
	CBNZW R13, backDiff
	SUB   $4, R12, R12
	JMP   back4

backDiff:
	CLZW R13, R13                // leading zeros
	LSRW $3, R13, R13
	SUB  R13, R12, R12
	JMP  backDone

backTail:
	CMP   R11, R12
	BLE   backDone
	ADD   R12, R3, R13
	MOVBU -1(R13), R13
	ADD   R12, R10, R17
	MOVBU -1(R17), R17
	CMPW  R13, R17
	BNE   backDone
	SUB   $1, R12, R12
	JMP   backTail

backDone:
	MOVD R12, R14

	// matchLength = MINMATCH + LZ4_count(ip+4, mp+4, iHighLimit) - back
	ADD $4, R3, R11
	ADD $4, R10, R12
	SUB $7, R5, R13

count8:
	CMP  R13, R11
	BHS  countTail
	MOVD (R11), R10
	MOVD (R12), R17
	EOR  R17, R10, R10
	CBNZ R10, countDiff
	ADD  $8, R11, R11
	ADD  $8, R12, R12
	JMP  count8

countDiff:
	RBIT R10, R10
	CLZ  R10, R10
	ADD  R10>>3, R11, R11
	JMP  countDone

countTail:
	SUB   $3, R5, R13
	CMP   R13, R11
	BHS   count2
	MOVWU (R11), R10
	MOVWU (R12), R17
	CMPW  R10, R17
	BNE   count2
	ADD   $4, R11, R11
	ADD   $4, R12, R12

count2:
	SUB   $1, R5, R13
	CMP   R13, R11
	BHS   count1
	MOVHU (R11), R10
	MOVHU (R12), R17
	CMPW  R10, R17
	BNE   count1
	ADD   $2, R11, R11
	ADD   $2, R12, R12

count1:
	CMP   R5, R11
	BHS   countDone
	MOVBU (R11), R10
	MOVBU (R12), R17
	CMPW  R10, R17
	BNE   countDone
	ADD   $1, R11, R11

countDone:
	SUB  R3, R11, R11            // MINMATCH + count
	SUB  R14, R11, R11           // - back
	MOVD R11, R15                // matchLength
	CMP  R9, R11
	BLE  afterCand
	MOVD R11, R9                 // longest
	SUBW R7, R21, R10
	MOVD R10, A_OFFSET(R0)       // ipIndex - matchIndex
	MOVD R14, A_SBACK(R0)

afterCand:
	// chainSwap: a longer match must also match at every position of this
	// one: jump to the chain going farthest back (LZ4HC_sparsestChain)
	CBZ  R25, follow
	CMP  R9, R15
	BNE  follow                  // matchLength != longest
	ADDW R9, R7, R10
	CMPW R21, R10
	BHI  follow                  // matchIndex + longest > ipIndex
	MOVD $1, R11                 // distanceToNextMatch
	MOVD ZR, R12                 // pos
	SUB  $3, R9, R14             // end = longest - MINMATCH + 1
	MOVD $16, R16                // accel = 1 << kTrigger

sparsest:
	CMP   R14, R12
	BGE   sparsestDone
	ADDW  R12, R7, R10
	ANDW  $0xFFFF, R10, R10
	MOVHU (R2)(R10<<1), R10      // candidateDist
	LSR   $4, R16, R13           // step = accel >> kTrigger
	ADD   $1, R16, R16           // accel++
	CMPW  R11, R10
	BLS   sparsestNext
	MOVD  R10, R11
	MOVD  R12, R26               // matchChainPos = pos
	MOVD  $16, R16

sparsestNext:
	ADD R13, R12, R12
	JMP sparsest

sparsestDone:
	CMPW $1, R11
	BLS  follow
	CMPW R7, R11
	BHI  done                    // avoid overflow
	SUBW R11, R7, R7
	JMP  loop

follow:
	ANDW  $0xFFFF, R7, R10
	MOVHU (R2)(R10<<1), R10      // DELTANEXTU16(chainTable, matchIndex)
	CBZ   R24, step
	CMPW  $1, R10
	BNE   step
	CBZ   R26, patternOut

step:
	CBZ   R26, stepSub
	ADDW  R26, R7, R10
	ANDW  $0xFFFF, R10, R10
	MOVHU (R2)(R10<<1), R10      // DELTANEXTU16(chainTable, matchIndex + matchChainPos)

stepSub:
	SUBW R10, R7, R7
	JMP  loop

done:
	MOVD ZR, ret+8(FP)
	JMP  out

extCandidate:
	MOVD $1, R10
	MOVD R10, ret+8(FP)
	JMP  out

patternOut:
	MOVD $2, R10
	MOVD R10, ret+8(FP)

out:
	MOVD R7, A_MINDEX(R0)
	MOVD R8, A_NBATT(R0)
	MOVD R9, A_LONGEST(R0)
	MOVD R26, A_CPOS(R0)
	RET
