// The body of decodeExecSeqsAsm and decodeExecSeqsSplitAsm
// (zstd_decompress_block_arm64.s, which has their BOUNDS and includes this
// in each TEXT symbol; labels are local to each symbol), as
// zstd_decompress_block_seqloop_amd64.h: the same branches in the same
// order, with the same hand-backs. With SEQ_SPLIT defined it adds
// ZSTD_execSequenceSplitLitBuffer's oMatchEnd > oend_w hand-back. On entry
// R23 is the argument a; the including TEXT supplies the pending and exit
// labels (SEQ_PENDING, SEQ_EXIT). No a+0(FP) here: go vet's asmdecl does
// not read #included files, so every FP reference stays in the .s, where
// it checks them.

	MOVD ARGS_BC(R23), R0
	MOVD ARGS_CONSUMED(R23), R1
	MOVD ARGS_SRC(R23), R24
	MOVD ARGS_PTR(R23), R2
	ADD  R24, R2, R2
	ADD  $16, R24, R22

	MOVD ARGS_TLL(R23), R14
	MOVD ARGS_STLL(R23), R24
	MOVD R24, STLL(RSP)
	MOVD (R14)(R24<<3), R5
	MOVD ARGS_TML(R23), R15
	MOVD ARGS_STML(R23), R24
	MOVD R24, STML(RSP)
	MOVD (R15)(R24<<3), R6
	MOVD ARGS_TOF(R23), R16
	MOVD ARGS_STOF(R23), R24
	MOVD R24, STOF(RSP)
	MOVD (R16)(R24<<3), R7

	MOVD ARGS_REP+0(R23), R8
	MOVD ARGS_REP+8(R23), R9
	MOVD ARGS_REP+16(R23), R10

	MOVD ARGS_OUT(R23), R24
	MOVD ARGS_OP(R23), R3
	ADD  R24, R3, R3
	MOVD ARGS_OENDW(R23), R19
	ADD  R24, R19, R19
	MOVD ARGS_PREFIX(R23), R21
	ADD  R24, R21, R21
	MOVD ARGS_LITS(R23), R24
	MOVD ARGS_LITPTR(R23), R4
	ADD  R24, R4, R4
	MOVD ARGS_LITEND(R23), R20
	ADD  R24, R20, R20
	MOVD ARGS_NBSEQ(R23), R17
	MOVD ZR, ARGS_PENDING(R23)

loop:
	// for ; nbSeq > 1; nbSeq-- (the last sequence is Go's), while ptr >= 16.
	CMP $1, R17
	BLE exit
	CMP R22, R2
	BLO exit

	// decodeSequence, from the three cells.
	LSR  $32, R5, R11       // litLength = llDInfo.baseValue
	LSR  $32, R6, R12       // matchLength = mlDInfo.baseValue
	UBFX $16, R7, $8, R24   // ofBits
	CMP  $1, R24
	BHI  ofLong
	BLO  ofRepeat

	// ofBits == 1: offset = ofBase + ll0 + ReadBitsFast(1).
	LSR  $32, R7, R23
	CMP  $1, R23
	BNE  exit               // TABLE INVARIANT broken: nothing consumed yet
	LSL  R1, R0, R23
	LSR  $63, R23, R23
	ADD  $1, R1, R1
	CMP  $0, R11
	CSET EQ, R13            // ll0
	ADD  R23, R13, R13
	ADD  $1, R13, R13
	CMP  $2, R13
	BEQ  ofRep2
	BHI  ofRep0Minus1
	// offset 1: temp = prevOffset[1].
	CMP  $1, R9
	SBC  ZR, R9, R13        // temp -= !temp
	MOVD R8, R9
	MOVD R13, R8
	B    ofDone

ofRep2:
	// offset 2: temp = prevOffset[2].
	CMP  $1, R10
	SBC  ZR, R10, R13
	MOVD R9, R10
	MOVD R8, R9
	MOVD R13, R8
	B    ofDone

ofRep0Minus1:
	// offset 3: temp = prevOffset[0] - 1.
	SUB  $1, R8, R13
	CMP  $1, R13
	SBC  ZR, R13, R13
	MOVD R9, R10
	MOVD R8, R9
	MOVD R13, R8
	B    ofDone

ofRepeat:
	// ofBits == 0: offset = prevOffset[ll0], and prevOffset[1] =
	// prevOffset[!ll0].
	MOVD R8, R13
	CBNZ R11, ofDone
	MOVD R9, R13
	MOVD R8, R9
	MOVD R13, R8
	B    ofDone

ofLong:
	// ofBits > 1: offset = ofBase + ReadBitsFast(ofBits).
	READ_BITS_FAST(R24, R23, R25)
	LSR  $32, R7, R13
	ADD  R23, R13, R13
	MOVD R9, R10
	MOVD R8, R9
	MOVD R13, R8

ofDone:
	UBFX $16, R6, $8, R24   // mlBits
	CBZ  R24, mlDone
	READ_BITS_FAST(R24, R23, R25)
	ADD  R23, R12, R12

mlDone:
	// if totalBits >= STREAM_ACCUMULATOR_MIN_64 - (LLFSELog+MLFSELog+OffFSELog)
	UBFX $16, R5, $8, R23   // llBits
	UBFX $16, R7, $8, R25
	ADD  R24, R25, R25
	ADD  R23, R25, R25
	CMP  $31, R25
	BLO  reload1Done
	RELOAD(reload1Done)

reload1Done:
	CBZ R23, llDone
	READ_BITS_FAST(R23, R24, R25)
	ADD R24, R11, R11

llDone:
	UPDATE_STATE(R5, R14, STLL)
	UPDATE_STATE(R6, R15, STML)
	UPDATE_STATE(R7, R16, STOF)
	RELOAD(reload2Done)

reload2Done:
	// execSequence: hand back the sequences for its slow paths.
	ADD R11, R3, R23        // oLitEnd
	ADD R11, R4, R25        // iLitEnd
	CMP R20, R25
	BHI pending
	ADD R12, R23, R24       // oMatchEnd
	CMP R19, R24
	BHI pending
#ifdef SEQ_SPLIT
	// ZSTD_execSequenceSplitLitBuffer: oMatchEnd > oend_w, where the
	// caller passes oend_w = litPtr + litLength - WILDCOPY_OVERLENGTH.
	SUB $32, R25, R25
	CMP R25, R24
	BHI pending
#endif
	SUB R21, R23, R25
	CMP R25, R13            // uint(offset) > uint(oLitEnd - prefixStart)
	BHI pending

	// Copy literals: copy16, then wildcopy(op+16, litPtr+16, litLength-16),
	// 16 bytes at a time, each load before its store.
	VLD1 (R4), [V0.B16]
	VST1 [V0.B16], (R3)
	CMP  $16, R11
	BLS  litDone
	ADD  $16, R4, R25
	ADD  $16, R3, R26
	VLD1 (R25), [V0.B16]
	VST1 [V0.B16], (R26)
	CMP  $32, R11
	BLS  litDone
	ADD  $16, R25, R25
	ADD  $16, R26, R26

litLoop:
	VLD1.P 16(R25), [V0.B16]
	VST1.P [V0.B16], 16(R26)
	VLD1.P 16(R25), [V0.B16]
	VST1.P [V0.B16], 16(R26)
	CMP    R23, R26
	BLO    litLoop

litDone:
	MOVD R23, R3            // op = oLitEnd
	ADD  R11, R4, R4        // litPtr = iLitEnd

	// Copy match.
	SUB R13, R3, R23        // match = oLitEnd - offset
	CMP $16, R13
	BLO matchShort

	// offset >= WILDCOPY_VECLEN: wildcopy(op, match, matchLength, noOverlap).
	VLD1 (R23), [V0.B16]
	VST1 [V0.B16], (R3)
	CMP  $16, R12
	BLS  seqDone
	ADD  $16, R3, R26
	ADD  $16, R23, R23

matchLoop:
	VLD1.P 16(R23), [V0.B16]
	VST1.P [V0.B16], 16(R26)
	VLD1.P 16(R23), [V0.B16]
	VST1.P [V0.B16], 16(R26)
	CMP    R24, R26
	BLO    matchLoop
	B      seqDone

matchShort:
	// overlapCopy8(out, op, match, offset).
	CMP   $8, R13
	BHS   matchCopy8
	MOVBU (R23), R26        // offset < 8: four bytes one by one,
	MOVB  R26, (R3)
	MOVBU 1(R23), R26
	MOVB  R26, 1(R3)
	MOVBU 2(R23), R26
	MOVB  R26, 2(R3)
	MOVBU 3(R23), R26
	MOVB  R26, 3(R3)
	LSL   $3, R13, R25
	MOVD  $0x0404040401020100, R26 // dec32table
	LSR   R25, R26, R26
	AND   $0xFF, R26, R26
	ADD   R26, R23, R23     // match += dec32table[offset]
	MOVWU (R23), R26        // ZSTD_copy4
	MOVW  R26, 4(R3)
	MOVD  $0x0B0A090807080808, R26 // dec64table
	LSR   R25, R26, R26
	AND   $0xFF, R26, R26
	SUB   R26, R23, R23     // match -= dec64table[offset]
	B     matchShortRest

matchCopy8:
	MOVD (R23), R26         // ZSTD_copy8
	MOVD R26, (R3)

matchShortRest:
	// op += 8, match += 8; now op - match is in 8..15, so wildcopy(op,
	// match, matchLength-8, overlapSrcBeforeDst) is its COPY8 loop.
	ADD $8, R23, R23
	ADD $8, R3, R26
	CMP $8, R12
	BLS seqDone

copy8Loop:
	MOVD.P 8(R23), R25
	MOVD.P R25, 8(R26)
	CMP    R24, R26
	BLO    copy8Loop

seqDone:
	MOVD R24, R3            // op += sequenceLength
	SUB  $1, R17, R17
	B    loop
