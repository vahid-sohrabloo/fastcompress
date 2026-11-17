//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Bit writer: R8 container, R9 bitPos, R10 write pointer, R11 its clamp.

// ADDBITS(val, n): container |= (val & (1<<n - 1)) << bitPos; bitPos += n
// (C: BIT_addBits). val is clobbered, as are AX and CX; n < 32.
#define ADDBITS(val, n) \
	MOVQ n, CX; \
	MOVL $1, AX; \
	SHLQ CX, AX; \
	DECQ AX; \
	ANDQ AX, val; \
	MOVQ R9, CX; \
	SHLQ CX, val; \
	ORQ  val, R8; \
	ADDQ n, R9

// FLUSH: C's BIT_flushBits (bitPos < 64 here, see encodeSequences).
// Clobbers CX.
#define FLUSH \
	MOVQ    R8, (R10); \
	MOVQ    R9, CX; \
	SHRQ    $3, CX; \
	ADDQ    CX, R10; \
	CMPQ    R10, R11; \
	CMOVQHI R11, R10; \
	ANDQ    $7, R9; \
	SHLQ    $3, CX; \
	SHRQ    CX, R8

// ENC(st, tt, stab, sym): C's FSE_encodeSymbol for state register st, with
// symbolTT tt and u16 state table stab (memory operands), symbol in sym.
// Clobbers AX, CX, DX, R15.
#define ENC(st, tt, stab, sym) \
	MOVQ    tt, AX; \
	MOVL    4(AX)(sym*8), DX; \
	MOVL    (AX)(sym*8), R15; \
	LEAL    (st)(DX*1), CX; \
	SHRL    $16, CX; \
	MOVL    $1, AX; \
	SHLQ    CX, AX; \
	DECQ    AX; \
	ANDQ    st, AX; \
	MOVL    st, DX; \
	SHRL    CX, DX; \
	ADDL    R15, DX; \
	MOVQ    CX, R15; \
	MOVQ    R9, CX; \
	SHLQ    CX, AX; \
	ORQ     AX, R8; \
	ADDQ    R15, R9; \
	MOVQ    stab, AX; \
	MOVWLZX (AX)(DX*2), st

// func encodeSequencesAsm(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
//	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
//	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
//	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32)
//
// C: zstd_compress_sequences.c:ZSTD_encodeSequences_body, the loop over
// n = nbSeq-2 .. 0 with longOffsets = 0.
//
// Registers: R8-R11 bit writer, R12 stML, R13 stOF, R14 stLL, SI seqs,
// DI n, BX/DX scratch codes and bit counts; the sum of the extra bits is
// kept at 0(SP).
TEXT ·encodeSequencesAsm(SB), NOSPLIT, $8-204
	MOVQ seqs+0(FP), SI
	MOVQ n+32(FP), DI
	MOVQ buf+104(FP), R10
	MOVQ R10, R11
	ADDQ ptr+112(FP), R10
	ADDQ endPtr+120(FP), R11
	MOVQ container+128(FP), R8
	MOVQ bitPos+136(FP), R9
	MOVL stML+144(FP), R12
	MOVL stOF+148(FP), R13
	MOVL stLL+152(FP), R14

loop:
	DECQ DI
	JLT  done

	MOVQ    ofCodes+24(FP), AX
	MOVBLZX (AX)(DI*1), BX
	CMPL    BX, ofMax+160(FP)
	JHI     badcode
	ENC(R13, ofTT+64(FP), ofStateTable+56(FP), BX)
	MOVQ    mlCodes+16(FP), AX
	MOVBLZX (AX)(DI*1), BX
	CMPL    BX, mlMax+156(FP)
	JHI     badcode
	ENC(R12, mlTT+48(FP), mlStateTable+40(FP), BX)
	MOVQ    llCodes+8(FP), AX
	MOVBLZX (AX)(DI*1), BX
	CMPL    BX, llMax+164(FP)
	JHI     badcode
	ENC(R14, llTT+80(FP), llStateTable+72(FP), BX)

	// BX = llBits, DX = mlBits, 0(SP) = ofBits + mlBits + llBits
	MOVQ    llBitsTab+88(FP), AX
	MOVBLZX (AX)(BX*1), BX
	MOVQ    mlCodes+16(FP), AX
	MOVBLZX (AX)(DI*1), DX
	MOVQ    mlBitsTab+96(FP), AX
	MOVBLZX (AX)(DX*1), DX
	MOVQ    ofCodes+24(FP), AX
	MOVBLZX (AX)(DI*1), R15
	LEAQ    (BX)(DX*1), AX
	ADDQ    R15, AX
	MOVQ    AX, 0(SP)
	CMPQ    AX, $31             // 64-7-(LLFSELog+MLFSELog+OffFSELog)
	JLT     noflush1
	FLUSH

noflush1:
	MOVWLZX 4(SI)(DI*8), R15    // litLength
	ADDBITS(R15, BX)
	MOVWLZX 6(SI)(DI*8), R15    // mlBase
	ADDBITS(R15, DX)
	CMPQ    0(SP), $56
	JLE     noflush2
	FLUSH

noflush2:
	MOVQ    ofCodes+24(FP), AX
	MOVBLZX (AX)(DI*1), DX      // ofBits
	MOVL    (SI)(DI*8), R15     // offBase
	ADDBITS(R15, DX)
	FLUSH
	JMP     loop

done:
	MOVQ R8, containerOut+168(FP)
	MOVQ R9, bitPosOut+176(FP)
	SUBQ buf+104(FP), R10
	MOVQ R10, ptrOut+184(FP)
	MOVL R12, stMLOut+192(FP)
	MOVL R13, stOFOut+196(FP)
	MOVL R14, stLLOut+200(FP)
	RET

badcode:
	MOVQ $64, bitPosOut+176(FP)
	RET

// BMI2 forms of ADDBITS, FLUSH and ENC (same clobbers).
#define ADDBITSX(val, n) \
	BZHIQ n, val, val; \
	SHLXQ R9, val, val; \
	ORQ   val, R8; \
	ADDQ  n, R9

#define FLUSHX \
	MOVQ    R8, (R10); \
	MOVQ    R9, CX; \
	SHRQ    $3, CX; \
	ADDQ    CX, R10; \
	CMPQ    R10, R11; \
	CMOVQHI R11, R10; \
	ANDQ    $7, R9; \
	SHLQ    $3, CX; \
	SHRXQ   CX, R8, R8

#define ENCX(st, tt, stab, sym) \
	MOVQ    tt, AX; \
	MOVL    4(AX)(sym*8), DX; \
	MOVL    (AX)(sym*8), R15; \
	LEAL    (st)(DX*1), CX; \
	SHRL    $16, CX; \
	BZHIQ   CX, st, AX; \
	SHLXQ   R9, AX, AX; \
	ORQ     AX, R8; \
	SHRXL   CX, st, DX; \
	ADDL    R15, DX; \
	ADDQ    CX, R9; \
	MOVQ    stab, AX; \
	MOVWLZX (AX)(DX*2), st

// func encodeSequencesAsmBMI2(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
//	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
//	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
//	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32)
//
// encodeSequencesAsm with BMI2 (BZHI, SHLX, SHRX); C: ZSTD_encodeSequences_bmi2.
// C: zstd_compress_sequences.c:ZSTD_encodeSequences_body, the loop over
// n = nbSeq-2 .. 0 with longOffsets = 0.
//
// Registers: R8-R11 bit writer, R12 stML, R13 stOF, R14 stLL, SI seqs,
// DI n, BX/DX scratch codes and bit counts; the sum of the extra bits is
// kept at 0(SP).
TEXT ·encodeSequencesAsmBMI2(SB), NOSPLIT, $8-204
	MOVQ seqs+0(FP), SI
	MOVQ n+32(FP), DI
	MOVQ buf+104(FP), R10
	MOVQ R10, R11
	ADDQ ptr+112(FP), R10
	ADDQ endPtr+120(FP), R11
	MOVQ container+128(FP), R8
	MOVQ bitPos+136(FP), R9
	MOVL stML+144(FP), R12
	MOVL stOF+148(FP), R13
	MOVL stLL+152(FP), R14

xloop:
	DECQ DI
	JLT  xdone

	MOVQ    ofCodes+24(FP), AX
	MOVBLZX (AX)(DI*1), BX
	CMPL    BX, ofMax+160(FP)
	JHI     xbadcode
	ENCX(R13, ofTT+64(FP), ofStateTable+56(FP), BX)
	MOVQ    mlCodes+16(FP), AX
	MOVBLZX (AX)(DI*1), BX
	CMPL    BX, mlMax+156(FP)
	JHI     xbadcode
	ENCX(R12, mlTT+48(FP), mlStateTable+40(FP), BX)
	MOVQ    llCodes+8(FP), AX
	MOVBLZX (AX)(DI*1), BX
	CMPL    BX, llMax+164(FP)
	JHI     xbadcode
	ENCX(R14, llTT+80(FP), llStateTable+72(FP), BX)

	// BX = llBits, DX = mlBits, 0(SP) = ofBits + mlBits + llBits
	MOVQ    llBitsTab+88(FP), AX
	MOVBLZX (AX)(BX*1), BX
	MOVQ    mlCodes+16(FP), AX
	MOVBLZX (AX)(DI*1), DX
	MOVQ    mlBitsTab+96(FP), AX
	MOVBLZX (AX)(DX*1), DX
	MOVQ    ofCodes+24(FP), AX
	MOVBLZX (AX)(DI*1), R15
	LEAQ    (BX)(DX*1), AX
	ADDQ    R15, AX
	MOVQ    AX, 0(SP)
	CMPQ    AX, $31             // 64-7-(LLFSELog+MLFSELog+OffFSELog)
	JLT     xnoflush1
	FLUSHX

xnoflush1:
	MOVWLZX 4(SI)(DI*8), R15    // litLength
	ADDBITSX(R15, BX)
	MOVWLZX 6(SI)(DI*8), R15    // mlBase
	ADDBITSX(R15, DX)
	CMPQ    0(SP), $56
	JLE     xnoflush2
	FLUSHX

xnoflush2:
	MOVQ    ofCodes+24(FP), AX
	MOVBLZX (AX)(DI*1), DX      // ofBits
	MOVL    (SI)(DI*8), R15     // offBase
	ADDBITSX(R15, DX)
	FLUSHX
	JMP     xloop

xdone:
	MOVQ R8, containerOut+168(FP)
	MOVQ R9, bitPosOut+176(FP)
	SUBQ buf+104(FP), R10
	MOVQ R10, ptrOut+184(FP)
	MOVL R12, stMLOut+192(FP)
	MOVL R13, stOFOut+196(FP)
	MOVL R14, stLLOut+200(FP)
	RET

xbadcode:
	MOVQ $64, bitPosOut+176(FP)
	RET
