// The body of decodeFastAsm and decodeFastPrefix64kAsm
// (lz4_decompress_arm64.s), included after each TEXT line: DEC_PREFIX64K
// drops the checks that a match starts at or after lowPrefix, as C's
// withPrefix64k decoding does.

	MOVD src_base+0(FP), R4
	MOVD src_len+8(FP), R5
	ADD  R4, R5, R5              // iend
	MOVD out_base+24(FP), R6
	MOVD ip+48(FP), R0
	ADD  R4, R0, R0
	MOVD op+56(FP), R1
	ADD  R6, R1, R1
	MOVD oend+64(FP), R12
	ADD  R6, R12, R12
	SUB  $64, R12, R12           // oend-64
	MOVD lowPrefix+72(FP), R10
	ADD  R6, R10, R10
	SUB  $18, R5, R11
	SUB  $15, R5, R13
	SUB  $6, R5, R14
	SUB  $64, R5, R15

	PCALIGN $32

loop:
	// R0 is the token's position here
	MOVBU (R0), R2               // token
	CMP   R11, R0
	BHI   leave                  // ip+1 > iend-17, too close to input end: the safe loop restarts this sequence
	LSR   $4, R2, R3             // literal length
	CMP   $15, R3
	BEQ   longLit
	ADD   $1, R0, R0
	LDP   (R0), (R4, R5)
	STP   (R4, R5), (R1)
	ADD   R3, R0, R0
	ADD   R3, R1, R1

offset:
	MOVHU (R0), R8               // offset
	ADD   $2, R0, R0
	SUB   R8, R1, R9             // match
	AND   $15, R2, R3
	CMP   $15, R3
	BEQ   longML
	ADD   $4, R3, R3             // ml
	ADD   R1, R3, R2             // cpy
	CMP   R12, R2
	BHS   pending                // op+ml >= oend-64: safe_match_copy
	CMP   $8, R8
	BLO   general
#ifndef DEC_PREFIX64K
	CMP   R10, R9
	BLO   general
#endif

	// match length from the token, offset >= 8, within prefix: 18 bytes,
	// in order (the second 8 may read the first 8 just written)
	MOVD  (R9), R4
	MOVD  R4, (R1)
	MOVD  8(R9), R5
	MOVD  R5, 8(R1)
	MOVHU 16(R9), R6
	MOVH  R6, 16(R1)
	MOVD  R2, R1
	JMP   loop

general:
	// R2 = cpy
#ifndef DEC_PREFIX64K
	CMP R10, R9
	BLO offsetError              // checkOffset: match + 0 < lowPrefix
#endif
	CMP $16, R8
	BLO small
	CMP $64, R8
	BLO copy32

copy64:
	VLD1.P 64(R9), [V0.B16, V1.B16, V2.B16, V3.B16]
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R1)
	CMP    R2, R1
	BLO    copy64
	MOVD   R2, R1
	JMP    loop

copy32:
	// offset 16..63: each 16 bytes are read after the previous 16 are written
	LDP.P 16(R9), (R4, R5)
	STP.P (R4, R5), 16(R1)
	LDP.P 16(R9), (R6, R7)
	STP.P (R6, R7), 16(R1)
	CMP   R2, R1
	BLO   copy32
	MOVD  R2, R1
	JMP   loop

small:
	CBZ  R8, zeros
	CMP  $8, R8
	BHS  small8
	CMP  $1, R8
	BEQ  fill1
	CMP  $2, R8
	BEQ  fill2
	CMP  $4, R8
	BEQ  fill4
	MOVBU (R9), R4
	MOVB  R4, (R1)
	MOVBU 1(R9), R4
	MOVB  R4, 1(R1)
	MOVBU 2(R9), R4
	MOVB  R4, 2(R1)
	MOVBU 3(R9), R4
	MOVB  R4, 3(R1)
	MOVBU 4(R9), R4
	MOVB  R4, 4(R1)
	MOVBU 5(R9), R4
	MOVB  R4, 5(R1)
	MOVBU 6(R9), R4
	MOVB  R4, 6(R1)
	MOVBU 7(R9), R4
	MOVB  R4, 7(R1)
	MOVD  $lz4dist<>(SB), R4
	MOVD  (R4)(R8<<3), R8        // chunk distance, a multiple of the offset >= 8
	JMP   chunks

small8:
	MOVD (R9), R4
	MOVD R4, (R1)

chunks:
	ADD $8, R1, R1
	CMP R2, R1
	BHS smallDone

chunkLoop:
	SUB  R8, R1, R4
	MOVD (R4), R4
	MOVD R4, (R1)
	ADD  $8, R1, R1
	CMP  R2, R1
	BLO  chunkLoop

smallDone:
	MOVD R2, R1
	JMP  loop

	// offsets 1, 2 and 4 (C: LZ4_memcpy_using_offset): the 8-byte pattern
	// of the repeated bytes, stored up to cpy 16 bytes at a time
fill1:
	MOVBU (R9), R4
	MOVD  $0x0101010101010101, R5
	MUL   R5, R4, R4
	JMP   fill

fill2:
	MOVHU (R9), R4
	MOVD  $0x0001000100010001, R5
	MUL   R5, R4, R4
	JMP   fill

fill4:
	MOVWU (R9), R4
	ORR   R4<<32, R4, R4

fill:
	STP.P (R4, R4), 16(R1)
	CMP   R2, R1
	BLO   fill
	MOVD  R2, R1
	JMP   loop

zeros:
	// offset 0 (invalid): C writes zeros
	STP.P (ZR, ZR), 16(R1)
	CMP   R2, R1
	BLO   zeros
	MOVD  R2, R1
	JMP   loop

longLit:
	MOVD R0, R7                  // token position
	ADD  $1, R0, R0
	CMP  R13, R0
	BHS  corrupt
	MOVBU.P 1(R0), R4
	ADD  R4, R3, R3
	CMP  $255, R4
	BNE  litLen

litLenLoop:
	CMP  R13, R0
	BHS  corrupt
	MOVBU.P 1(R0), R4
	ADD  R4, R3, R3
	CMP  $255, R4
	BEQ  litLenLoop

litLen:
	SUB  R1, R12, R4
	CMP  R4, R3
	BGT  restart                 // too close to output end for a wild copy
	SUB  R0, R15, R4
	CMP  R4, R3
	BGT  restart                 // too close to input end for a wild copy
	MOVD R0, R5
	ADD  R1, R3, R9              // cpy
	ADD  R3, R0, R0

litLoop:
	VLD1.P 64(R5), [V0.B16, V1.B16, V2.B16, V3.B16]
	VST1.P [V0.B16, V1.B16, V2.B16, V3.B16], 64(R1)
	CMP    R9, R1
	BLO    litLoop
	MOVD   R9, R1
	JMP    offset

longML:
	CMP  R14, R0
	BHS  corrupt
	MOVBU.P 1(R0), R4
	ADD  R4, R3, R3
	CMP  $255, R4
	BEQ  longML
	ADD  $4, R3, R3              // ml
	ADD  R1, R3, R2              // cpy
	CMP  R12, R2
	BHS  pending                 // op+ml >= oend-64: safe_match_copy
	JMP  general

restart:
	MOVD R7, R0

leave:
	MOVD ZR, status+112(FP)
	JMP  exit

pending:
	MOVD R8, offset+96(FP)
	MOVD R3, ml+104(FP)
	MOVD $1, R4
	MOVD R4, status+112(FP)
	JMP  exit

corrupt:
	MOVD $2, R4
	MOVD R4, status+112(FP)
	JMP  exit

offsetError:
	MOVD $3, R4
	MOVD R4, status+112(FP)

exit:
	MOVD src_base+0(FP), R4
	SUB  R4, R0, R0
	MOVD R0, nip+80(FP)
	MOVD out_base+24(FP), R4
	SUB  R4, R1, R1
	MOVD R1, nop+88(FP)
	RET
