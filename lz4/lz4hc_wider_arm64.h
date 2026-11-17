// The body of hcWider (lz4hc_parse_arm64.s), included after each variant's
// TEXT line; lz4hc_wider_amd64.h's prefix-only variants step for step:
// WIDER_PA adds the pattern analysis, WIDER_SWAP chainSwap
// (LZ4HC_sparsestChain; only with WIDER_PA, as the optimal parser uses
// both). With WIDER_SWAP lookBack is 0 (iLowLimit == ip, as C asserts), so
// there is no count back, and R4 holds the 2 bytes at ip + longest - 1
// (reloaded when longest changes). Frame slots are the caller's (BL leaves
// RSP as it is).
//
// In: R3 ip, R4 iLowLimit, R10 longest, R9 maxNbAttempts; R1 win (the
// prefix), R2 chainTable, R5 iHighLimit, R6 prefixIdx, R19 the hash
// multiplier, R22 iHighLimit - 7 (kept); W_HASH, W_NEXT (nextToUpdate, in
// and out). Out: R10 longest, R12 offset, R14 sBack. Clobbers R4, R7-R9,
// R11, R13, R16, R20, R23.
//
// Registers: R11 ipIndex, R7 lowestMatchIndex, R8 matchIndex, R9
// nbAttempts, R10 longest, R20 the pattern (ip's 4 bytes), R23 maxLength;
// R12-R14, R16 scratch.

	SUB  R1, R3, R11
	ADD  R6, R11, R11            // ipIndex
	MOVD R9, SL(W_NB)

	// LZ4HC_Insert(ip)
	MOVD SL(W_NEXT), R13
	MOVD SL(W_HASH), R9
	CMPW R11, R13
	BHS  insDone

insLoop:
	SUBW  R6, R13, R12
	MOVWU (R1)(R12), R12
	MULW  R19, R12, R12
	LSRW  $17, R12, R12          // LZ4HC_hashPtr
	MOVWU (R9)(R12<<2), R16
	SUBW  R16, R13, R14          // delta = idx - hashTable[h]
	MOVW  $65535, R7
	CMPW  R7, R14
	CSELW HI, R7, R14, R14
	ANDW  $0xFFFF, R13, R7
	MOVH  R14, (R2)(R7<<1)       // DELTANEXTU16(chainTable, idx) = delta
	MOVW  R13, (R9)(R12<<2)      // hashTable[h] = idx
	ADDW  $1, R13, R13
	CMPW  R11, R13
	BLO   insLoop

insDone:
	MOVD  R11, SL(W_NEXT)        // nextToUpdate = target
	MOVWU (R3), R20              // pattern
	MULW  R19, R20, R12
	LSRW  $17, R12, R12
	MOVWU (R9)(R12<<2), R8       // matchIndex = hashTable[LZ4HC_hashPtr(ip)]

	// lowestMatchIndex: lowLimit (= prefixIdx) within the first 64 KB
	MOVD R6, R7
	ADD  $65536, R6, R12
	CMP  R11, R12
	BHI  lowSet
	SUB  $65535, R11, R7

lowSet:
	MOVD R7, SL(W_LOWEST)
	SUB  R4, R5, R23             // maxLength = iHighLimit - iLowLimit
	MOVD SL(W_NB), R9            // nbAttempts
	MOVD ZR, SL(W_OFF)
	MOVD ZR, SL(W_SBACK)
#ifdef WIDER_PA
	MOVD ZR, SL(W_REPEAT)
#endif
#ifdef WIDER_SWAP
	MOVD ZR, SL(W_CPOS)
#endif

	// The loop runs while matchIndex >= lowestMatchIndex, nbAttempts > 0
	// and longest < maxLength (checked here and after each improvement).
	CMP  R23, R10
	BGE  wmDone
#ifdef WIDER_SWAP
	ADD   R10, R3, R16
	MOVHU -1(R16), R4            // ip + longest - 1
#endif

wmLoop:
	CMPW R7, R8
	BLO  wmDone                  // matchIndex < lowestMatchIndex
	SUBS $1, R9, R9
	BLT  wmDone                  // nbAttempts was 0
	SUBW R6, R8, R12
	ADD  R1, R12, R12            // mp
#ifdef WIDER_SWAP
	ADD   R10, R12, R14
	MOVHU -1(R14), R14
	CMPW  R4, R14
	BNE   wmNoMatch
	MOVWU (R12), R13
	CMPW  R20, R13
	BNE   wmNoMatch
	MOVD  ZR, R14                // back = 0
#else
	ADD   R10, R4, R13
	MOVHU -1(R13), R13           // iLowLimit + longest - 1
	SUB   R3, R12, R14
	ADD   R4, R14, R14           // mp - lookBack
	ADD   R10, R14, R14
	MOVHU -1(R14), R14
	CMPW  R13, R14
	BNE   wmNoMatch
	MOVWU (R12), R13
	CMPW  R20, R13
	BNE   wmNoMatch

	// back = LZ4HC_countBack(ip, mp, iLowLimit, prefixPtr), if lookBack
	MOVD ZR, R14
	CMP  R4, R3
	BEQ  backDone
	SUB  R3, R4, R13             // iMin - ip
	SUB  R12, R1, R7             // mMin - match
	CMP  R13, R7
	CSEL GT, R7, R13, R13        // min = max(...)

back4:
	SUB   R13, R14, R7
	CMP   $3, R7
	BLE   backTail
	ADD   R14, R3, R7
	MOVWU -4(R7), R7
	ADD   R14, R12, R16
	MOVWU -4(R16), R16
	EORW  R16, R7, R7
	CBNZW R7, backDiff
	SUB   $4, R14, R14
	JMP   back4

backDiff:
	CLZW R7, R7                  // leading zeros
	LSRW $3, R7, R7
	SUB  R7, R14, R14
	JMP  backRestore

backTail:
	CMP   R13, R14
	BLE   backRestore
	ADD   R14, R3, R7
	MOVBU -1(R7), R7
	ADD   R14, R12, R16
	MOVBU -1(R16), R16
	CMPW  R7, R16
	BNE   backRestore
	SUB   $1, R14, R14
	JMP   backTail

backRestore:
	MOVD SL(W_LOWEST), R7
#endif

backDone:
	MOVD R14, SL(W_BACK)

	// matchLength = MINMATCH + LZ4_count(ip+4, mp+4, iHighLimit) - back
	ADD $4, R3, R13
	ADD $4, R12, R14

count8:
	CMP  R22, R13
	BHS  countTail
	MOVD (R13), R12
	MOVD (R14), R16
	EOR  R16, R12, R12
	CBNZ R12, countDiff
	ADD  $8, R13, R13
	ADD  $8, R14, R14
	JMP  count8

countDiff:
	RBIT R12, R12
	CLZ  R12, R12
	ADD  R12>>3, R13, R13
	JMP  countDone

countTail:
	SUB   $3, R5, R12
	CMP   R12, R13
	BHS   count2
	MOVWU (R13), R12
	MOVWU (R14), R16
	CMPW  R12, R16
	BNE   count2
	ADD   $4, R13, R13
	ADD   $4, R14, R14

count2:
	SUB   $1, R5, R12
	CMP   R12, R13
	BHS   count1
	MOVHU (R13), R12
	MOVHU (R14), R16
	CMPW  R12, R16
	BNE   count1
	ADD   $2, R13, R13
	ADD   $2, R14, R14

count1:
	CMP   R5, R13
	BHS   countDone
	MOVBU (R13), R12
	MOVBU (R14), R16
	CMPW  R12, R16
	BNE   countDone
	ADD   $1, R13, R13

countDone:
	SUB  R3, R13, R13            // MINMATCH + count
	MOVD SL(W_BACK), R14
	SUB  R14, R13, R13           // matchLength
	CMP  R10, R13
	BLE  wmNext
	MOVD R13, R10                // longest
	SUBW R8, R11, R12
	MOVD R12, SL(W_OFF)          // ipIndex - matchIndex
	MOVD R14, SL(W_SBACK)
	CMP  R23, R10
	BGE  wmDone
#ifdef WIDER_SWAP
	ADD   R10, R3, R16
	MOVHU -1(R16), R4
	JMP   wmNext

wmNoMatch:
	MOVD ZR, R13                 // matchLength = 0

wmNext:
	// chainSwap: a longer match must also match at every position of this
	// one: jump to the chain going farthest back (LZ4HC_sparsestChain)
	CMP  R10, R13
	BNE  wmFollow                // matchLength != longest
	ADDW R10, R8, R12
	CMPW R11, R12
	BHI  wmFollow                // matchIndex + longest > ipIndex
	MOVD $1, R13                 // distanceToNextMatch
	MOVD ZR, R14                 // pos
	SUB  $3, R10, R12
	MOVD R12, SL(W_END)          // end = longest - MINMATCH + 1
	MOVD $16, R4                 // accel = 1 << kTrigger (iLowLimit == ip)

sparsest:
	MOVD  SL(W_END), R16
	CMP   R16, R14
	BGE   sparsestDone
	ADDW  R14, R8, R12
	ANDW  $0xFFFF, R12, R12
	MOVHU (R2)(R12<<1), R12      // candidateDist
	LSR   $4, R4, R7             // step = accel >> kTrigger
	ADD   $1, R4, R4             // accel++
	CMPW  R13, R12
	BLS   sparsestNext
	MOVD  R12, R13
	MOVD  R14, SL(W_CPOS)        // matchChainPos = pos
	MOVD  $16, R4

sparsestNext:
	ADD R7, R14, R14
	JMP sparsest

sparsestDone:
	ADD   R10, R3, R16
	MOVHU -1(R16), R4
	MOVD  SL(W_LOWEST), R7
	CMPW  $1, R13
	BLS   wmFollow
	CMPW  R8, R13
	BHI   wmDone                 // avoid overflow
	SUBW  R13, R8, R8
	JMP   wmLoop

wmFollow:
	MOVD  SL(W_CPOS), R13
	CBNZ  R13, wmStepPos
	ANDW  $0xFFFF, R8, R12
	MOVHU (R2)(R12<<1), R12      // distNextMatch = DELTANEXTU16(chainTable, matchIndex)
	CMPW  $1, R12
	BEQ   wmPA                   // matchChainPos == 0: the pattern analysis
	SUBW  R12, R8, R8
	JMP   wmLoop

wmStepPos:
	ADDW  R13, R8, R12
	ANDW  $0xFFFF, R12, R12
	MOVHU (R2)(R12<<1), R12      // DELTANEXTU16(chainTable, matchIndex + matchChainPos)

wmStepSub:
	SUBW R12, R8, R8
	JMP  wmLoop
#else

wmNoMatch:
wmNext:
	ANDW  $0xFFFF, R8, R12
	MOVHU (R2)(R12<<1), R12      // DELTANEXTU16(chainTable, matchIndex)
#ifdef WIDER_PA
	CMPW  $1, R12
	BEQ   wmPA                   // distNextMatch == 1: the pattern analysis
#endif

wmStepSub:
	SUBW R12, R8, R8
	JMP  wmLoop
#endif

wmDone:
	MOVD SL(W_OFF), R12
	MOVD SL(W_SBACK), R14
	RET

#ifdef WIDER_PA
	// the pattern analysis: distNextMatch == 1, matchChainPos == 0
wmPA:
	MOVD  R9, SL(W_NB)
	SUBW  $1, R8, R13
	MOVD  R13, SL(W_CAND)        // matchCandidateIdx
	MOVD  SL(W_REPEAT), R16
	CBNZ  R16, paRep
	LSRW  $16, R20, R12
	ANDW  $0xFFFF, R20, R9
	CMPW  R9, R12
	BNE   paNot                  // (pattern & 0xFFFF) != (pattern >> 16)
	LSRW  $24, R20, R12
	ANDW  $0xFF, R20, R9
	CMPW  R9, R12
	BNE   paNot                  // (pattern & 0xFF) != (pattern >> 24)
	MOVD  $2, R16
	MOVD  R16, SL(W_REPEAT)      // rep_confirmed
	ADD   $4, R3, R13
	COUNTPAT(pc1a, pc1b, pc1c, pc1d)
	SUB   R3, R13, R13
	MOVD  R13, SL(W_SRCPL)       // srcPatternLength = countPattern(ip+4) + 4
	JMP   paRep

paNot:
	MOVD $1, R16
	MOVD R16, SL(W_REPEAT)       // rep_not
	JMP  paOut

paRep:
	MOVD  SL(W_REPEAT), R16
	CMP   $2, R16
	BNE   paOut
	MOVD  SL(W_LOWEST), R7
	MOVD  SL(W_CAND), R13
	CMPW  R7, R13
	BLO   paOut                  // matchCandidateIdx < lowestMatchIndex
	SUBW  R13, R6, R12
	SUBW  $1, R12, R12
	CMPW  $3, R12
	BLO   paOut                  // !LZ4HC_protectDictEnd(prefixIdx, matchCandidateIdx)

	// LZ4HC_patternSegment (prefix candidate)
	SUBW  R6, R13, R12
	ADD   R1, R12, R12           // mp
	MOVWU (R12), R14
	CMPW  R20, R14
	BNE   paOut
	MOVD  R12, SL(W_MP)
	ADD   $4, R12, R13
	COUNTPAT(pc2a, pc2b, pc2c, pc2d)
	MOVD  SL(W_MP), R16
	SUB   R16, R13, R13
	MOVD  R13, SL(W_FWD)         // forwardPatternLength
	MOVD  SL(W_MP), R13
	MOVD  R20, R14
	ADD   $4, R1, R12

paRev4:
	CMP   R12, R13
	BLO   paRevTail
	MOVWU -4(R13), R16
	CMPW  R14, R16
	BNE   paRevTail
	SUB   $4, R13, R13
	JMP   paRev4

paRevTail:
	CMP   R1, R13
	BLS   paRevDone
	LSRW  $24, R14, R12
	MOVBU -1(R13), R16
	CMPW  R12, R16
	BNE   paRevDone
	SUB   $1, R13, R13
	LSLW  $8, R14, R14
	JMP   paRevTail

paRevDone:
	MOVD  SL(W_MP), R12
	SUB   R13, R12, R12          // back = reverseCountPattern(mp, prefixPtr)
	MOVD  SL(W_CAND), R13
	MOVD  SL(W_LOWEST), R7
	SUBW  R12, R13, R14          // matchCandidateIdx - back
	CMPW  R7, R14
	CSELW LO, R7, R14, R14       // max(..., lowestMatchIndex)
	SUBW  R14, R13, R12          // backLength
	MOVD  SL(W_FWD), R14
	ADD   R14, R12, R7           // currentSegmentLength
	MOVD  SL(W_SRCPL), R9
	CMP   R9, R7
	BLO   paNotFull
	CMP   R9, R14
	BHI   paNotFull

	// fullPatternFits: matchCandidateIdx + forward - srcPatternLength
	ADDW  R14, R13, R8
	SUBW  R9, R8, R8
	SUBW  R8, R6, R12
	SUBW  $1, R12, R12
	CMPW  $3, R12
	BLO   paProtect
	JMP   paCont

paNotFull:
	SUBW  R12, R13, R8           // matchCandidateIdx - backLength
	SUBW  R8, R6, R12
	SUBW  $1, R12, R12
	CMPW  $3, R12
	BLO   paProtect
#ifndef WIDER_SWAP
	CMP   R4, R3
	BNE   paCont                 // lookBackLength != 0
#endif
	CMP   R9, R7
	CSEL  HI, R9, R7, R7         // maxML = min(currentSegmentLength, srcPatternLength)
	CMP   R7, R10
	BHS   paNext
	SUBW  R8, R11, R12
	MOVW  $65535, R16
	CMPW  R16, R12
	BHI   paBreak
	MOVD  R7, R10                // longest = maxML
	MOVD  R12, SL(W_OFF)
	CMP   R23, R10
	BGE   paBreak
#ifdef WIDER_SWAP
	ADD   R10, R3, R16
	MOVHU -1(R16), R4
#endif

paNext:
	ANDW  $0xFFFF, R8, R12
	MOVHU (R2)(R12<<1), R12      // distToNextPattern
	CMPW  R8, R12
	BHI   paBreak                // avoid overflow
	SUBW  R12, R8, R8

paCont:
	MOVD SL(W_LOWEST), R7
	MOVD SL(W_NB), R9
	JMP  wmLoop

paProtect:
	MOVD R6, R8                  // matchIndex = prefixIdx
	JMP  paCont

paOut:
	MOVD SL(W_LOWEST), R7
	MOVD SL(W_NB), R9
	MOVD $1, R12
	JMP  wmStepSub

paBreak:
	MOVD SL(W_LOWEST), R7
	MOVD SL(W_NB), R9
	JMP  wmDone
#endif
