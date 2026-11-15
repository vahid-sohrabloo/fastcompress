// The body of hcWider (lz4hc_parse_amd64.s), included after each variant's
// TEXT line: WIDER_PA adds the pattern analysis, WIDER_SWAP chainSwap
// (LZ4HC_sparsestChain; only with WIDER_PA, as the optimal parser uses
// both). With WIDER_SWAP lookBack is 0 (iLowLimit == ip, as C asserts), so
// there is no count back, and R10 holds the 2 bytes at ip + longest - 1
// (reloaded when longest changes). WIDER_EXT adds the external dictionary
// (lowLimit < prefixIdx: W_LOWLIM, W_DICTB = dictStart - lowLimit), and
// WIDER_FAV favorDecSpeed (offsets below 8 skipped; with WIDER_SWAP),
// WIDER_DCTX the attached dictionary searched after the chain (dictCtx;
// not with WIDER_EXT: W_DHASH, W_DCHAIN, W_DBASE = its prefixStart -
// dictLimit, W_DEND its end index, W_DSTART its prefixStart).
// Frame slots are the caller's, 8 bytes up (one CALL deeper).

	MOVQ R9, DI
	SUBQ SI, DI
	ADDQ R12, DI                 // ipIndex
	MOVQ R15, (8+W_NB)(SP)

	// LZ4HC_Insert(ip)
	MOVQ (8+W_NEXT)(SP), CX
	MOVQ (8+W_HASH)(SP), R15
	CMPL CX, DI
	JHS  insDone

insLoop:
	MOVL CX, AX
	SUBL R12, AX
	MOVL (SI)(AX*1), AX
	IMUL3L $-1640531535, AX, AX
	SHRL $17, AX                 // LZ4HC_hashPtr
	MOVL CX, DX
	SUBL (R15)(AX*4), DX         // delta = idx - hashTable[h]
	MOVL $65535, R13
	CMPL DX, R13
	CMOVLHI R13, DX
	MOVWLZX CX, R13
	MOVW DX, (R8)(R13*2)         // DELTANEXTU16(chainTable, idx) = delta
	MOVL CX, (R15)(AX*4)         // hashTable[h] = idx
	INCL CX
	CMPL CX, DI
	JLO  insLoop

insDone:
	MOVQ DI, (8+W_NEXT)(SP)      // nextToUpdate = target
	MOVL (R9), AX
	MOVQ AX, (8+W_PAT)(SP)       // pattern
	IMUL3L $-1640531535, AX, AX
	SHRL $17, AX
	MOVL (R15)(AX*4), R14        // matchIndex = hashTable[LZ4HC_hashPtr(ip)]

	// lowestMatchIndex: lowLimit within the first 64 KB
#ifdef WIDER_EXT
	MOVQ (8+W_LOWLIM)(SP), R13
	LEAQ 65536(R13), AX
#else
	MOVQ R12, R13                // lowLimit = prefixIdx
	LEAQ 65536(R12), AX
#endif
	CMPQ AX, DI
	JHI  lowSet
	LEAQ -65535(DI), R13

lowSet:
	MOVQ R13, (8+W_LOWEST)(SP)
	MOVQ R11, AX
	SUBQ R10, AX
	MOVQ AX, (8+W_MAXLEN)(SP)    // maxLength = iHighLimit - iLowLimit
	MOVQ (8+W_NB)(SP), R15       // nbAttempts
	XORL AX, AX
	MOVQ AX, (8+W_OFF)(SP)
	MOVQ AX, (8+W_SBACK)(SP)
#ifdef WIDER_PA
	MOVQ AX, (8+W_REPEAT)(SP)
#endif
#ifdef WIDER_SWAP
	MOVQ AX, (8+W_CPOS)(SP)
#endif

	// The loop runs while matchIndex >= lowestMatchIndex, nbAttempts > 0
	// and longest < maxLength. longest only grows, and the steps after an
	// improvement (chainSwap, the chain) change nothing returned, so
	// longest < maxLength is checked here and after each improvement.
	CMPQ BX, (8+W_MAXLEN)(SP)
	JGE  wmDone
#ifdef WIDER_SWAP
	MOVWLZX -1(R9)(BX*1), R10    // ip + longest - 1
#endif

wmLoop:
	CMPL R14, R13
	JLO  wmDone                  // matchIndex < lowestMatchIndex
	DECQ R15
	JLT  wmDone                  // nbAttempts was 0
#ifdef WIDER_FAV
	MOVL DI, AX
	SUBL R14, AX
	CMPL AX, $8
	JLO  wmNoMatch               // favorDecSpeed skips offsets below 8
#endif
#ifdef WIDER_EXT
	CMPL R14, R12
	JLO  wmDict                  // in the external dictionary
#endif

	MOVL R14, AX
	SUBL R12, AX
	ADDQ SI, AX                  // mp
#ifdef WIDER_SWAP
	MOVWLZX -1(AX)(BX*1), DX
	CMPL R10, DX
	JNE  wmNoMatch
	MOVL (AX), CX
	CMPL CX, (8+W_PAT)(SP)
	JNE  wmNoMatch
	XORL DX, DX                  // back = 0
#else
	MOVWLZX -1(R10)(BX*1), CX    // iLowLimit + longest - 1
	MOVQ AX, DX
	SUBQ R9, DX
	ADDQ R10, DX                 // mp - lookBack
	MOVWLZX -1(DX)(BX*1), DX
	CMPL CX, DX
	JNE  wmNoMatch
	MOVL (AX), CX
	CMPL CX, (8+W_PAT)(SP)
	JNE  wmNoMatch

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
	JMP  backRestore

backTail:
	CMPQ DX, CX
	JLE  backRestore
	MOVB -1(R9)(DX*1), R13
	CMPB R13, -1(AX)(DX*1)
	JNE  backRestore
	DECQ DX
	JMP  backTail

backRestore:
	MOVQ (8+W_LOWEST)(SP), R13
#endif

backDone:
	MOVQ DX, (8+W_BACK)(SP)

	// matchLength = MINMATCH + LZ4_count(ip+4, mp+4, iHighLimit) - back
	LEAQ 4(R9), CX
	LEAQ 4(AX), DX

count8:
	CMPQ CX, (8+W_HLIM7)(SP)
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
	LEAQ -3(R11), AX
	CMPQ CX, AX
	JHS  count2
	MOVL (CX), AX
	CMPL AX, (DX)
	JNE  count2
	ADDQ $4, CX
	ADDQ $4, DX

count2:
	LEAQ -1(R11), AX
	CMPQ CX, AX
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
	MOVQ (8+W_BACK)(SP), DX
	SUBQ DX, CX                  // matchLength
	CMPQ CX, BX
	JLE  wmNext
	MOVQ CX, BX                  // longest
	MOVL DI, AX
	SUBL R14, AX
	MOVQ AX, (8+W_OFF)(SP)       // ipIndex - matchIndex
	MOVQ DX, (8+W_SBACK)(SP)
	CMPQ BX, (8+W_MAXLEN)(SP)
	JGE  wmDone
#ifdef WIDER_SWAP
	MOVWLZX -1(R9)(BX*1), R10
	JMP  wmNext

wmNoMatch:
	XORL CX, CX                  // matchLength = 0

wmNext:
	// chainSwap: a longer match must also match at every position of this
	// one: jump to the chain going farthest back (LZ4HC_sparsestChain)
	CMPQ CX, BX
	JNE  wmFollow                // matchLength != longest
	MOVL R14, AX
	ADDL BX, AX
	CMPL AX, DI
	JHI  wmFollow                // matchIndex + longest > ipIndex
	MOVL $1, CX                  // distanceToNextMatch
	XORL DX, DX                  // pos
	LEAQ -3(BX), AX
	MOVQ AX, (8+W_END)(SP)       // end = longest - MINMATCH + 1
	MOVL $16, R10                // accel = 1 << kTrigger (iLowLimit == ip)

sparsest:
	CMPQ DX, (8+W_END)(SP)
	JGE  sparsestDone
	LEAL (R14)(DX*1), AX
	MOVWLZX AX, AX
	MOVWLZX (R8)(AX*2), AX       // candidateDist
	MOVQ R10, R13
	SHRQ $4, R13                 // step = accel >> kTrigger
	INCQ R10                     // accel++
	CMPL AX, CX
	JLS  sparsestNext
	MOVL AX, CX
	MOVQ DX, (8+W_CPOS)(SP)      // matchChainPos = pos
	MOVL $16, R10

sparsestNext:
	ADDQ R13, DX
	JMP  sparsest

sparsestDone:
	MOVWLZX -1(R9)(BX*1), R10
	MOVQ (8+W_LOWEST)(SP), R13
	CMPL CX, $1
	JLS  wmFollow
	CMPL CX, R14
	JHI  wmDone                  // avoid overflow
	SUBL CX, R14
	JMP  wmLoop

wmFollow:
	MOVQ (8+W_CPOS)(SP), CX
	TESTQ CX, CX
	JNE  wmStepPos
	MOVWLZX R14, AX
	MOVWLZX (R8)(AX*2), AX       // distNextMatch = DELTANEXTU16(chainTable, matchIndex)
	CMPL AX, $1
	JEQ  wmPA                    // matchChainPos == 0: the pattern analysis
	SUBL AX, R14
	JMP  wmLoop

wmStepPos:
	LEAL (R14)(CX*1), AX
	MOVWLZX AX, AX
	MOVWLZX (R8)(AX*2), AX       // DELTANEXTU16(chainTable, matchIndex + matchChainPos)

wmStepSub:
	SUBL AX, R14
	JMP  wmLoop
#else

wmNoMatch:
wmNext:
	MOVWLZX R14, AX
	MOVWLZX (R8)(AX*2), AX       // DELTANEXTU16(chainTable, matchIndex)
#ifdef WIDER_PA
	CMPL AX, $1
	JEQ  wmPA                    // distNextMatch == 1: the pattern analysis
#endif

wmStepSub:
	SUBL AX, R14
	JMP  wmLoop
#endif

wmDone:
#ifdef WIDER_DCTX
	// the attached dictionary, if attempts remain, longest < maxLength and
	// ip is within 64 KB of lowLimit (= prefixIdx)
	TESTQ R15, R15
	JLE  wdRet
	CMPQ BX, (8+W_MAXLEN)(SP)
	JGE  wdRet
	LEAQ 65536(R12), AX
	CMPQ AX, DI
	JLS  wdRet
	MOVL (8+W_PAT)(SP), AX
	IMUL3L $-1640531535, AX, AX
	SHRL $17, AX
	MOVQ (8+W_DHASH)(SP), CX
	MOVL (CX)(AX*4), R14         // dictMatchIndex

wdLoop:
	MOVL R14, AX
	ADDL (8+W_LOWEST)(SP), AX
	SUBL (8+W_DEND)(SP), AX      // matchIndex
	MOVL DI, CX
	SUBL AX, CX                  // ipIndex - matchIndex
	CMPL CX, $65535
	JHI  wdRet
	CMPQ BX, (8+W_MAXLEN)(SP)
	JGE  wdRet
	DECQ R15
	JLT  wdRet
	MOVQ CX, (8+W_CAND)(SP)
	MOVL R14, AX
	ADDQ (8+W_DBASE)(SP), AX     // matchPtr
	MOVL (AX), CX
	CMPL CX, (8+W_PAT)(SP)
	JNE  wdNext
	MOVL (8+W_DEND)(SP), CX
	SUBL R14, CX
	ADDQ R9, CX                  // vLimit = ip + dictEndOffset - dictMatchIndex
	CMPQ CX, R11
	CMOVQHI R11, CX
	MOVQ CX, (8+W_VLIM)(SP)
	XORL DX, DX
#ifndef WIDER_SWAP
	CMPQ R9, R10
	JEQ  ybDone
	MOVQ R10, CX
	SUBQ R9, CX                  // iMin - ip
	MOVQ (8+W_DSTART)(SP), R13
	SUBQ AX, R13                 // the dictionary's prefixStart - match
	CMPQ R13, CX
	CMOVQGT R13, CX

yb4:
	MOVQ DX, R13
	SUBQ CX, R13
	CMPQ R13, $3
	JLE  ybTail
	MOVL -4(R9)(DX*1), R13
	XORL -4(AX)(DX*1), R13
	JNE  ybDiff
	SUBQ $4, DX
	JMP  yb4

ybDiff:
	BSRL R13, R13
	XORL $31, R13
	SHRL $3, R13
	SUBQ R13, DX
	JMP  ybDone

ybTail:
	CMPQ DX, CX
	JLE  ybDone
	MOVB -1(R9)(DX*1), R13
	CMPB R13, -1(AX)(DX*1)
	JNE  ybDone
	DECQ DX
	JMP  ybTail

ybDone:
#endif
	MOVQ DX, (8+W_BACK)(SP)
	LEAQ 4(R9), CX
	LEAQ 4(AX), DX
	MOVQ (8+W_VLIM)(SP), R13
	SUBQ $7, R13

yc8:
	CMPQ CX, R13
	JHS  ycTail
	MOVQ (CX), AX
	XORQ (DX), AX
	JNE  ycDiff
	ADDQ $8, CX
	ADDQ $8, DX
	JMP  yc8

ycDiff:
	BSFQ AX, AX
	SHRQ $3, AX
	ADDQ AX, CX
	JMP  ycDone

ycTail:
	ADDQ $7, R13
	LEAQ -3(R13), AX
	CMPQ CX, AX
	JHS  yc2
	MOVL (CX), AX
	CMPL AX, (DX)
	JNE  yc2
	ADDQ $4, CX
	ADDQ $4, DX

yc2:
	LEAQ -1(R13), AX
	CMPQ CX, AX
	JHS  yc1
	MOVWLZX (CX), AX
	CMPW AX, (DX)
	JNE  yc1
	ADDQ $2, CX
	ADDQ $2, DX

yc1:
	CMPQ CX, R13
	JHS  ycDone
	MOVB (CX), AX
	CMPB AL, (DX)
	JNE  ycDone
	INCQ CX

ycDone:
	SUBQ R9, CX                  // MINMATCH + count
	SUBQ (8+W_BACK)(SP), CX      // mlt
	CMPQ CX, BX
	JLE  wdNext
	MOVQ CX, BX
	MOVQ (8+W_CAND)(SP), CX
	MOVQ CX, (8+W_OFF)(SP)
	MOVQ (8+W_BACK)(SP), CX
	MOVQ CX, (8+W_SBACK)(SP)

wdNext:
	MOVQ (8+W_DCHAIN)(SP), CX
	MOVWLZX R14, AX
	MOVWLZX (CX)(AX*2), AX
	SUBL AX, R14                 // dictMatchIndex -= DELTANEXTU16
	JMP  wdLoop

wdRet:
#endif
	MOVQ (8+W_OFF)(SP), AX
	MOVQ (8+W_SBACK)(SP), DX
	RET

#ifdef WIDER_EXT
	// a candidate in the external dictionary (lowestMatchIndex <=
	// matchIndex < prefixIdx): matchIndex <= prefixIdx - 4 and the 4-byte
	// pattern, the count back down to dictStart, the count up to the end of
	// the dictionary (vLimit) and on from prefixStart if it gets there
wmDict:
	LEAL 4(R14), AX
	CMPL AX, R12
	JHI  wmNoMatch               // matchIndex > prefixIdx - 4
	MOVQ (8+W_DICTB)(SP), AX
	ADDQ R14, AX                 // mp = dictStart + matchIndex - lowLimit
	MOVL (AX), CX
	CMPL CX, (8+W_PAT)(SP)
	JNE  wmNoMatch
	MOVL R12, CX
	SUBL R14, CX
	ADDQ R9, CX                  // vLimit = ip + prefixIdx - matchIndex
	CMPQ CX, R11
	CMOVQHI R11, CX              // at most iHighLimit
	MOVQ CX, (8+W_VLIM)(SP)
	XORL DX, DX
#ifndef WIDER_SWAP
	// back = LZ4HC_countBack(ip, mp, iLowLimit, dictStart), if lookBack
	CMPQ R9, R10
	JEQ  xbDone
	MOVQ R10, CX
	SUBQ R9, CX                  // iMin - ip
	MOVQ (8+W_DICTB)(SP), R13
	ADDQ (8+W_LOWLIM)(SP), R13
	SUBQ AX, R13                 // dictStart - match
	CMPQ R13, CX
	CMOVQGT R13, CX              // min = max(...)

xb4:
	MOVQ DX, R13
	SUBQ CX, R13
	CMPQ R13, $3
	JLE  xbTail
	MOVL -4(R9)(DX*1), R13
	XORL -4(AX)(DX*1), R13
	JNE  xbDiff
	SUBQ $4, DX
	JMP  xb4

xbDiff:
	BSRL R13, R13
	XORL $31, R13
	SHRL $3, R13
	SUBQ R13, DX
	JMP  xbDone

xbTail:
	CMPQ DX, CX
	JLE  xbDone
	MOVB -1(R9)(DX*1), R13
	CMPB R13, -1(AX)(DX*1)
	JNE  xbDone
	DECQ DX
	JMP  xbTail

xbDone:
#endif
	MOVQ DX, (8+W_BACK)(SP)

	// MINMATCH + LZ4_count(ip+4, mp+4, vLimit)
	LEAQ 4(R9), CX
	LEAQ 4(AX), DX
	MOVQ (8+W_VLIM)(SP), R13
	SUBQ $7, R13

xc8:
	CMPQ CX, R13
	JHS  xcTail
	MOVQ (CX), AX
	XORQ (DX), AX
	JNE  xcDiff
	ADDQ $8, CX
	ADDQ $8, DX
	JMP  xc8

xcDiff:
	BSFQ AX, AX
	SHRQ $3, AX
	ADDQ AX, CX
	JMP  xcDone

xcTail:
	ADDQ $7, R13                 // vLimit
	LEAQ -3(R13), AX
	CMPQ CX, AX
	JHS  xc2
	MOVL (CX), AX
	CMPL AX, (DX)
	JNE  xc2
	ADDQ $4, CX
	ADDQ $4, DX

xc2:
	LEAQ -1(R13), AX
	CMPQ CX, AX
	JHS  xc1
	MOVWLZX (CX), AX
	CMPW AX, (DX)
	JNE  xc1
	ADDQ $2, CX
	ADDQ $2, DX

xc1:
	CMPQ CX, R13
	JHS  xcEnd
	MOVB (CX), AX
	CMPB AL, (DX)
	JNE  xcDone
	INCQ CX

xcEnd:
	// at vLimit below iHighLimit: + LZ4_count(ip + matchLength, prefixStart, iHighLimit)
	CMPQ CX, R13
	JNE  xcDone
	CMPQ R13, R11
	JHS  xcDone
	MOVQ (8+W_LOWEST)(SP), R13
	MOVQ SI, DX
	JMP  count8

xcDone:
	MOVQ (8+W_LOWEST)(SP), R13
	JMP  countDone
#endif

#ifdef WIDER_PA
	// the pattern analysis: distNextMatch == 1, matchChainPos == 0
wmPA:
	MOVQ R15, (8+W_NB)(SP)
	LEAL -1(R14), CX
	MOVQ CX, (8+W_CAND)(SP)      // matchCandidateIdx
	CMPQ (8+W_REPEAT)(SP), $0
	JNE  paRep
	MOVL (8+W_PAT)(SP), DX
	MOVL DX, AX
	SHRL $16, AX
	MOVWLZX DX, R15
	CMPL AX, R15
	JNE  paNot                   // (pattern & 0xFFFF) != (pattern >> 16)
	MOVL DX, AX
	SHRL $24, AX
	MOVBLZX DX, R15
	CMPL AX, R15
	JNE  paNot                   // (pattern & 0xFF) != (pattern >> 24)
	MOVQ $2, (8+W_REPEAT)(SP)    // rep_confirmed
	LEAQ 4(R9), CX
	CALL hcCountPattern<>(SB)
	SUBQ R9, CX
	MOVQ CX, (8+W_SRCPL)(SP)     // srcPatternLength = countPattern(ip+4) + 4
	JMP  paRep

paNot:
	MOVQ $1, (8+W_REPEAT)(SP)    // rep_not
	JMP  paOut

paRep:
	CMPQ (8+W_REPEAT)(SP), $2
	JNE  paOut
	MOVQ (8+W_LOWEST)(SP), R13   // (hcCountPattern clobbers R13)
	MOVQ (8+W_CAND)(SP), CX
	CMPL CX, R13
	JLO  paOut                   // matchCandidateIdx < lowestMatchIndex
	MOVL R12, AX
	SUBL CX, AX
	DECL AX
	CMPL AX, $3
	JLO  paOut                   // !LZ4HC_protectDictEnd(prefixIdx, matchCandidateIdx)

	// LZ4HC_patternSegment (prefix candidate)
#ifdef WIDER_EXT
	CMPL CX, R12
	JLO  paDictCand
#endif
	MOVL CX, AX
	SUBL R12, AX
	ADDQ SI, AX                  // mp
	MOVL (AX), DX
	CMPL DX, (8+W_PAT)(SP)
	JNE  paOut
	MOVQ AX, (8+W_MP)(SP)
	LEAQ 4(AX), CX
	CALL hcCountPattern<>(SB)
	SUBQ (8+W_MP)(SP), CX
	MOVQ CX, (8+W_FWD)(SP)       // forwardPatternLength
	MOVQ (8+W_MP)(SP), CX
	MOVL (8+W_PAT)(SP), DX
	LEAQ 4(SI), AX

paRev4:
	CMPQ CX, AX
	JLO  paRevTail
	CMPL -4(CX), DX
	JNE  paRevTail
	SUBQ $4, CX
	JMP  paRev4

paRevTail:
	CMPQ CX, SI
	JLS  paRevDone
	MOVL DX, AX
	SHRL $24, AX
	CMPB AL, -1(CX)
	JNE  paRevDone
	DECQ CX
	SHLL $8, DX
	JMP  paRevTail

paRevDone:
	MOVQ (8+W_MP)(SP), AX
	SUBQ CX, AX                  // back = reverseCountPattern(mp, prefixPtr)
#ifdef WIDER_EXT
	CMPQ CX, SI
	JEQ  paIntoDict              // back to prefixStart: on into the dictionary

paBackSet:
#endif
	MOVQ (8+W_CAND)(SP), CX
	MOVQ (8+W_LOWEST)(SP), R13
	MOVL CX, DX
	SUBL AX, DX                  // matchCandidateIdx - back
	CMPL DX, R13
	CMOVLCS R13, DX              // max(..., lowestMatchIndex)
	MOVL CX, AX
	SUBL DX, AX                  // backLength
	MOVQ (8+W_FWD)(SP), DX
	LEAQ (AX)(DX*1), R13         // currentSegmentLength
	MOVQ (8+W_SRCPL)(SP), R15
	CMPQ R13, R15
	JLO  paNotFull
	CMPQ DX, R15
	JHI  paNotFull

	// fullPatternFits: matchCandidateIdx + forward - srcPatternLength
	MOVL CX, R14
	ADDL DX, R14
	SUBL R15, R14
	MOVL R12, AX
	SUBL R14, AX
	DECL AX
	CMPL AX, $3
	JLO  paProtect
	JMP  paCont

paNotFull:
	MOVL CX, R14
	SUBL AX, R14                 // matchCandidateIdx - backLength
	MOVL R12, AX
	SUBL R14, AX
	DECL AX
	CMPL AX, $3
	JLO  paProtect
#ifndef WIDER_SWAP
	CMPQ R9, R10
	JNE  paCont                  // lookBackLength != 0
#endif
	CMPQ R13, R15
	CMOVQHI R15, R13             // maxML = min(currentSegmentLength, srcPatternLength)
	CMPQ BX, R13
	JHS  paNext
	MOVL DI, AX
	SUBL R14, AX
	CMPL AX, $65535
	JHI  paBreak
	MOVQ R13, BX                 // longest = maxML
	MOVQ AX, (8+W_OFF)(SP)
	CMPQ BX, (8+W_MAXLEN)(SP)
	JGE  paBreak
#ifdef WIDER_SWAP
	MOVWLZX -1(R9)(BX*1), R10
#endif

paNext:
	MOVWLZX R14, AX
	MOVWLZX (R8)(AX*2), AX       // distToNextPattern
	CMPL AX, R14
	JHI  paBreak                 // avoid overflow
	SUBL AX, R14

paCont:
	MOVQ (8+W_LOWEST)(SP), R13
	MOVQ (8+W_NB)(SP), R15
	JMP  wmLoop

paProtect:
	MOVL R12, R14                // matchIndex = prefixIdx
	JMP  paCont

paOut:
	MOVQ (8+W_LOWEST)(SP), R13
	MOVQ (8+W_NB)(SP), R15
	MOVL $1, AX
	JMP  wmStepSub

paBreak:
	MOVQ (8+W_LOWEST)(SP), R13
	MOVQ (8+W_NB)(SP), R15
	JMP  wmDone

#ifdef WIDER_EXT
	// a prefix candidate whose run reaches prefixStart: back +=
	// reverseCountPattern(dictEnd, dictStart, rotatePattern(-back, pattern))
paIntoDict:
	MOVQ AX, (8+W_VLIM)(SP)      // back within the prefix
	MOVL AX, CX
	NEGL CX
	ANDL $3, CX
	SHLL $3, CX
	MOVL (8+W_PAT)(SP), DX
	ROLL CX, DX
	MOVQ (8+W_DICTB)(SP), R13
	MOVL R12, CX
	ADDQ R13, CX                 // dictEnd
	ADDQ (8+W_LOWLIM)(SP), R13   // dictStart
	MOVQ CX, (8+W_MP)(SP)

pxRev4:
	LEAQ 4(R13), AX
	CMPQ CX, AX
	JLO  pxRevTail
	CMPL -4(CX), DX
	JNE  pxRevTail
	SUBQ $4, CX
	JMP  pxRev4

pxRevTail:
	CMPQ CX, R13
	JLS  pxRevDone
	MOVL DX, AX
	SHRL $24, AX
	CMPB AL, -1(CX)
	JNE  pxRevDone
	DECQ CX
	SHLL $8, DX
	JMP  pxRevTail

pxRevDone:
	MOVQ (8+W_MP)(SP), AX
	SUBQ CX, AX
	ADDQ (8+W_VLIM)(SP), AX
	JMP  paBackSet

	// LZ4HC_patternSegment, a candidate in the dictionary: forward up to
	// dictEnd and on from prefixStart (rotated pattern) if it gets there,
	// back down to dictStart
paDictCand:
	MOVQ (8+W_DICTB)(SP), AX
	ADDQ CX, AX                  // mp
	MOVL (AX), DX
	CMPL DX, (8+W_PAT)(SP)
	JNE  paOut
	MOVQ AX, (8+W_MP)(SP)
	MOVQ R11, (8+W_VLIM)(SP)
	MOVL R12, R11
	ADDQ (8+W_DICTB)(SP), R11    // dictEnd as the limit
	LEAQ 4(AX), CX
	MOVL (8+W_PAT)(SP), DX
	CALL hcCountPatternR<>(SB)
	CMPQ CX, R11
	MOVQ (8+W_VLIM)(SP), R11     // iHighLimit again
	JNE  pdFwd
	MOVQ CX, AX
	SUBQ (8+W_MP)(SP), AX
	MOVQ AX, (8+W_FWD)(SP)       // forward up to dictEnd
	MOVL AX, CX
	ANDL $3, CX
	SHLL $3, CX
	MOVL (8+W_PAT)(SP), DX
	ROLL CX, DX                  // LZ4HC_rotatePattern(forward, pattern)
	MOVQ SI, CX
	CALL hcCountPatternR<>(SB)
	SUBQ SI, CX
	ADDQ CX, (8+W_FWD)(SP)
	JMP  pdBack

pdFwd:
	SUBQ (8+W_MP)(SP), CX
	MOVQ CX, (8+W_FWD)(SP)

pdBack:
	MOVQ (8+W_MP)(SP), CX
	MOVL (8+W_PAT)(SP), DX
	MOVQ (8+W_DICTB)(SP), R13
	ADDQ (8+W_LOWLIM)(SP), R13   // dictStart

pdRev4:
	LEAQ 4(R13), AX
	CMPQ CX, AX
	JLO  pdRevTail
	CMPL -4(CX), DX
	JNE  pdRevTail
	SUBQ $4, CX
	JMP  pdRev4

pdRevTail:
	CMPQ CX, R13
	JLS  pdRevDone
	MOVL DX, AX
	SHRL $24, AX
	CMPB AL, -1(CX)
	JNE  pdRevDone
	DECQ CX
	SHLL $8, DX
	JMP  pdRevTail

pdRevDone:
	MOVQ (8+W_MP)(SP), AX
	SUBQ CX, AX
	JMP  paBackSet
#endif
#endif
