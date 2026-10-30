//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Bit writer: R13 container, R14 bitPos, R15 write pointer, R16 its clamp.

// ADDBITS(val, n, t): container |= (val & (1<<n - 1)) << bitPos; bitPos += n
// (C: BIT_addBits). val and t are clobbered; n < 32.
#define ADDBITS(val, n, t) \
	MOVD $1, t; \
	LSL  n, t, t; \
	SUB  $1, t; \
	AND  t, val, val; \
	LSL  R14, val, val; \
	ORR  val, R13, R13; \
	ADD  n, R14, R14

// FLUSH(t): C's BIT_flushBits (bitPos < 64 here, see encodeSequences).
// Clobbers t.
#define FLUSH(t) \
	MOVD R13, (R15); \
	LSR  $3, R14, t; \
	ADD  t, R15, R15; \
	CMP  R16, R15; \
	CSEL HI, R16, R15, R15; \
	AND  $7, R14, R14; \
	LSL  $3, t, t; \
	LSR  t, R13, R13

// ENC(st, tt, stab, sym): C's FSE_encodeSymbol for state register st, with
// symbolTT tt and u16 state table stab, symbol in sym. Clobbers R24-R26.
#define ENC(st, tt, stab, sym) \
	ADD   sym<<3, tt, R24; \
	MOVWU 4(R24), R25; \
	MOVWU (R24), R24; \
	ADDW  st, R25, R25; \
	LSRW  $16, R25, R25; \
	MOVD  $1, R26; \
	LSL   R25, R26, R26; \
	SUB   $1, R26; \
	AND   st, R26, R26; \
	LSL   R14, R26, R26; \
	ORR   R26, R13, R13; \
	ADD   R25, R14, R14; \
	LSRW  R25, st, R26; \
	ADDW  R24, R26, R26; \
	MOVHU (stab)(R26<<1), st

// func encodeSequencesAsm(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
//	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
//	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
//	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32)
//
// C: zstd_compress_sequences.c:ZSTD_encodeSequences_body, the loop over
// n = nbSeq-2 .. 0 with longOffsets = 0, as zstd_compress_sequences_amd64.s.
//
// Registers: R0 seqs, R1 llCodes, R2 mlCodes, R3 ofCodes, R4 n,
// R5/R6 ML state table and symbolTT, R7/R8 OF, R9/R10 LL, R11 llBitsTab,
// R12 mlBitsTab, R13-R16 bit writer, R17 stML, R19 stOF, R20 stLL;
// R21-R26 scratch (R21 the code, R26 the sum of the extra bits).
TEXT ·encodeSequencesAsm(SB), NOSPLIT|NOFRAME, $0-204
	MOVD  seqs+0(FP), R0
	MOVD  llCodes+8(FP), R1
	MOVD  mlCodes+16(FP), R2
	MOVD  ofCodes+24(FP), R3
	MOVD  n+32(FP), R4
	MOVD  mlStateTable+40(FP), R5
	MOVD  mlTT+48(FP), R6
	MOVD  ofStateTable+56(FP), R7
	MOVD  ofTT+64(FP), R8
	MOVD  llStateTable+72(FP), R9
	MOVD  llTT+80(FP), R10
	MOVD  llBitsTab+88(FP), R11
	MOVD  mlBitsTab+96(FP), R12
	MOVD  buf+104(FP), R15
	MOVD  ptr+112(FP), R16
	ADD   R16, R15, R16
	MOVD  endPtr+120(FP), R21
	ADD   R21, R15, R21
	MOVD  R16, R15
	MOVD  R21, R16
	MOVD  container+128(FP), R13
	MOVD  bitPos+136(FP), R14
	MOVWU stML+144(FP), R17
	MOVWU stOF+148(FP), R19
	MOVWU stLL+152(FP), R20

loop:
	SUBS $1, R4, R4
	BMI  done

	MOVBU (R3)(R4), R21
	MOVWU ofMax+160(FP), R22
	CMPW  R22, R21
	BHI   badcode
	ENC(R19, R8, R7, R21)
	MOVBU (R2)(R4), R21
	MOVWU mlMax+156(FP), R22
	CMPW  R22, R21
	BHI   badcode
	ENC(R17, R6, R5, R21)
	MOVBU (R1)(R4), R21
	MOVWU llMax+164(FP), R22
	CMPW  R22, R21
	BHI   badcode
	ENC(R20, R10, R9, R21)

	// R21 = llBits, R22 = mlBits, R26 = ofBits + mlBits + llBits
	MOVBU (R11)(R21), R21
	MOVBU (R2)(R4), R22
	MOVBU (R12)(R22), R22
	MOVBU (R3)(R4), R23
	ADD   R21, R22, R26
	ADD   R23, R26
	CMP   $31, R26              // 64-7-(LLFSELog+MLFSELog+OffFSELog)
	BLT   noflush1
	FLUSH(R25)

noflush1:
	ADD   R4<<3, R0, R23        // &seqs[n]
	MOVHU 4(R23), R24           // litLength
	ADDBITS(R24, R21, R25)
	MOVHU 6(R23), R24           // mlBase
	ADDBITS(R24, R22, R25)
	CMP   $56, R26
	BLE   noflush2
	FLUSH(R25)

noflush2:
	MOVBU (R3)(R4), R22         // ofBits
	MOVWU (R23), R24            // offBase
	ADDBITS(R24, R22, R25)
	FLUSH(R25)
	B     loop

done:
	MOVD R13, containerOut+168(FP)
	MOVD R14, bitPosOut+176(FP)
	MOVD buf+104(FP), R24
	SUB  R24, R15, R15
	MOVD R15, ptrOut+184(FP)
	MOVW R17, stMLOut+192(FP)
	MOVW R19, stOFOut+196(FP)
	MOVW R20, stLLOut+200(FP)
	RET

badcode:
	MOVD $64, R24
	MOVD R24, bitPosOut+176(FP)
	RET
