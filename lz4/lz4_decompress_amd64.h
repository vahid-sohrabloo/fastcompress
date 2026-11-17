// The body of decodeFastAsm and decodeFastPrefix64kAsm
// (lz4_decompress_amd64.s), included after each TEXT line: DEC_PREFIX64K
// drops the checks that a match starts at or after lowPrefix, as C's
// withPrefix64k decoding does.

	MOVQ src_base+0(FP), SI
	MOVQ src_len+8(FP), R8
	ADDQ SI, R8                  // iend
	MOVQ out_base+24(FP), DI
	MOVQ ip+48(FP), AX
	ADDQ SI, AX
	MOVQ op+56(FP), BX
	ADDQ DI, BX
	MOVQ oend+64(FP), R12
	LEAQ -64(DI)(R12*1), R12     // oend-64
	MOVQ lowPrefix+72(FP), R10
	ADDQ DI, R10
	LEAQ -18(R8), R11
	LEAQ -15(R8), R13
	LEAQ -6(R8), R14
	LEAQ -64(R8), R15

	PCALIGN $32

loop:
	// AX is the token's position here
	MOVBQZX (AX), CX             // token
	CMPQ    AX, R11
	JHI     leave                // ip+1 > iend-17, too close to input end: the safe loop restarts this sequence
	MOVQ    CX, DX
	SHRQ    $4, DX               // literal length
	CMPQ    DX, $15
	JEQ     longLit
	MOVOU   1(AX), X0
	MOVOU   X0, (BX)
	LEAQ    1(AX)(DX*1), AX
	ADDQ    DX, BX

offset:
	MOVWQZX (AX), R8             // offset
	ADDQ    $2, AX
	MOVQ    BX, R9
	SUBQ    R8, R9               // match
	MOVQ    CX, DX
	ANDQ    $15, DX
	CMPQ    DX, $15
	JEQ     longML
	ADDQ    $4, DX               // ml
	LEAQ    (BX)(DX*1), CX       // cpy
	CMPQ    CX, R12
	JHS     pending              // op+ml >= oend-64: safe_match_copy
	CMPQ    R8, $8
	JLO     general
#ifndef DEC_PREFIX64K
	CMPQ    R9, R10
	JLO     general
#endif

	// match length from the token, offset >= 8, within prefix: 18 bytes
	MOVQ (R9), SI
	MOVQ SI, (BX)
	MOVQ 8(R9), SI
	MOVQ SI, 8(BX)
	MOVW 16(R9), SI
	MOVW SI, 16(BX)
	MOVQ CX, BX
	JMP  loop

general:
	// CX = cpy
#ifndef DEC_PREFIX64K
	CMPQ R9, R10
	JLO  offsetError             // checkOffset: match + 0 < lowPrefix
#endif
	CMPQ R8, $16
	JLO  small
	CMPQ R8, $64
	JLO  copy32

copy64:
	MOVOU (R9), X0
	MOVOU 16(R9), X1
	MOVOU 32(R9), X2
	MOVOU 48(R9), X3
	MOVOU X0, (BX)
	MOVOU X1, 16(BX)
	MOVOU X2, 32(BX)
	MOVOU X3, 48(BX)
	ADDQ  $64, R9
	ADDQ  $64, BX
	CMPQ  BX, CX
	JLO   copy64
	MOVQ  CX, BX
	JMP   loop

copy32:
	MOVOU (R9), X0
	MOVOU X0, (BX)
	MOVOU 16(R9), X1
	MOVOU X1, 16(BX)
	ADDQ  $32, R9
	ADDQ  $32, BX
	CMPQ  BX, CX
	JLO   copy32
	MOVQ  CX, BX
	JMP   loop

small:
	TESTQ R8, R8
	JEQ   zeros
	CMPQ  R8, $8
	JHS   small8
	CMPQ  R8, $1
	JEQ   fill1
	CMPQ  R8, $2
	JEQ   fill2
	CMPQ  R8, $4
	JEQ   fill4
	MOVB  (R9), SI
	MOVB  SI, (BX)
	MOVB  1(R9), SI
	MOVB  SI, 1(BX)
	MOVB  2(R9), SI
	MOVB  SI, 2(BX)
	MOVB  3(R9), SI
	MOVB  SI, 3(BX)
	MOVB  4(R9), SI
	MOVB  SI, 4(BX)
	MOVB  5(R9), SI
	MOVB  SI, 5(BX)
	MOVB  6(R9), SI
	MOVB  SI, 6(BX)
	MOVB  7(R9), SI
	MOVB  SI, 7(BX)
	LEAQ  lz4dist<>(SB), SI
	MOVQ  (SI)(R8*8), R8         // chunk distance, a multiple of the offset >= 8
	JMP   chunks

small8:
	MOVQ (R9), SI
	MOVQ SI, (BX)

chunks:
	ADDQ $8, BX
	CMPQ BX, CX
	JHS  smallDone

chunkLoop:
	MOVQ BX, SI
	SUBQ R8, SI
	MOVQ (SI), SI
	MOVQ SI, (BX)
	ADDQ $8, BX
	CMPQ BX, CX
	JLO  chunkLoop

smallDone:
	MOVQ CX, BX
	JMP  loop

	// offsets 1, 2 and 4 (C: LZ4_memcpy_using_offset): the 8-byte pattern
	// of the repeated bytes, stored up to cpy
fill1:
	MOVBQZX (R9), SI
	MOVQ  $0x0101010101010101, DI
	IMULQ DI, SI
	JMP   fill

fill2:
	MOVWQZX (R9), SI
	MOVQ  $0x0001000100010001, DI
	IMULQ DI, SI
	JMP   fill

fill4:
	MOVL  (R9), SI
	MOVQ  SI, DI
	SHLQ  $32, DI
	ORQ   DI, SI

fill:
	MOVQ SI, (BX)
	ADDQ $8, BX
	CMPQ BX, CX
	JLO  fill
	MOVQ CX, BX
	JMP  loop

zeros:
	// offset 0 (invalid): C writes zeros
	MOVQ $0, (BX)
	ADDQ $8, BX
	CMPQ BX, CX
	JLO  zeros
	MOVQ CX, BX
	JMP  loop

longLit:
	MOVQ AX, DI                  // token position
	INCQ AX
	CMPQ AX, R13
	JHS  corrupt
	MOVBQZX (AX), SI
	INCQ AX
	ADDQ SI, DX
	CMPQ SI, $255
	JNE  litLen

litLenLoop:
	CMPQ AX, R13
	JHS  corrupt
	MOVBQZX (AX), SI
	INCQ AX
	ADDQ SI, DX
	CMPQ SI, $255
	JEQ  litLenLoop

litLen:
	MOVQ R12, SI
	SUBQ BX, SI
	CMPQ DX, SI
	JGT  restart                 // too close to output end for a wild copy
	MOVQ R15, SI
	SUBQ AX, SI
	CMPQ DX, SI
	JGT  restart                 // too close to input end for a wild copy
	MOVQ AX, SI
	LEAQ (BX)(DX*1), R9          // cpy
	ADDQ DX, AX

litLoop:
	MOVOU (SI), X0
	MOVOU 16(SI), X1
	MOVOU 32(SI), X2
	MOVOU 48(SI), X3
	MOVOU X0, (BX)
	MOVOU X1, 16(BX)
	MOVOU X2, 32(BX)
	MOVOU X3, 48(BX)
	ADDQ  $64, SI
	ADDQ  $64, BX
	CMPQ  BX, R9
	JLO   litLoop
	MOVQ  R9, BX
	JMP   offset

longML:
	CMPQ AX, R14
	JHS  corrupt
	MOVBQZX (AX), SI
	INCQ AX
	ADDQ SI, DX
	CMPQ SI, $255
	JEQ  longML
	ADDQ $4, DX                  // ml
	LEAQ (BX)(DX*1), CX          // cpy
	CMPQ CX, R12
	JHS  pending                 // op+ml >= oend-64: safe_match_copy
	JMP  general

restart:
	MOVQ DI, AX
	JMP  leave

leave:
	MOVQ $0, status+112(FP)
	JMP  exit

pending:
	MOVQ R8, offset+96(FP)
	MOVQ DX, ml+104(FP)
	MOVQ $1, status+112(FP)
	JMP  exit

corrupt:
	MOVQ $2, status+112(FP)
	JMP  exit

offsetError:
	MOVQ $3, status+112(FP)

exit:
	MOVQ src_base+0(FP), SI
	SUBQ SI, AX
	MOVQ AX, nip+80(FP)
	MOVQ out_base+24(FP), DI
	SUBQ DI, BX
	MOVQ BX, nop+88(FP)
	RET
