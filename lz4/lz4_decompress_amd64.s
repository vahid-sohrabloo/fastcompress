//go:build amd64 && !appengine && !noasm && !purego && gc

// The fast decode loop of lz4 v1.11.1 lib/lz4.c (Copyright (c) Yann Collet,
// BSD 2-Clause): LZ4_decompress_generic's loop under LZ4_FAST_DEC_LOOP, for
// a block without external dictionary (noDict, history in the same buffer
// before op). It follows the Go port in lz4_decompress.go statement for
// statement and stops where C leaves the fast loop: at a sequence start for
// the safe loop (status 0), or with a decoded match for safe_match_copy
// (status 1). Matches are copied with LZ77 semantics; the bytes written past
// the end of a literal run or a match lie below oend and are overwritten by
// later output, as C's wild copies.

#include "textflag.h"

// distance of the 8-byte chunk copy that follows the first 8 bytes of a
// match with offset 1..7 (the smallest multiple of the offset >= 8)
DATA lz4dist<>+0(SB)/8, $0
DATA lz4dist<>+8(SB)/8, $8
DATA lz4dist<>+16(SB)/8, $8
DATA lz4dist<>+24(SB)/8, $9
DATA lz4dist<>+32(SB)/8, $8
DATA lz4dist<>+40(SB)/8, $10
DATA lz4dist<>+48(SB)/8, $12
DATA lz4dist<>+56(SB)/8, $14
GLOBL lz4dist<>(SB), RODATA|NOPTR, $64

// func decodeFastAsm(src, out []byte, ip, op, oend, lowPrefix int) (nip, nop, offset, ml, status int)
//
// Registers: AX ip, BX op (pointers), CX token, DX literal / match length,
// R8 offset, R9 match, R10 lowPrefix, R11 iend-17, R12 oend-64,
// R13 iend-15 (RUN_MASK), R14 iend-6, R15 iend-64; SI, DI, X0-X3 scratch.
//
// Preconditions (checked by decompressGeneric): 0 <= ip < len(src);
// 0 <= lowPrefix <= op; op + 64 <= oend <= len(out).
//
// Status: 0 leave for the safe loop at the token at nip (op = nop);
// 1 safe_match_copy with offset and ml decoded (literals copied, nip after
// the match length); 2 corrupt input (C -1); 3 offset before lowPrefix (C -3).
//
// BOUNDS:
// (1) Loop invariant: ip < iend and op <= oend-64. It holds on entry. A
//     sequence ends (2) with op' = op+ll+ml < oend-64 (else status 1 before
//     any write of the match), and ip' <= iend-1: a short literal run leaves
//     ip <= iend-17+14, then the offset, so ip' <= iend-1; a long one leaves
//     ip+ll <= iend-64; the match length extension reads only below iend-6.
// (2) Reads of src: the token at ip < iend; the 16-byte literal copy at
//     ip <= iend-17 (checked before) reads below iend-1; length extension
//     bytes below iend-15 (literals) or iend-6 (match), checked before each
//     byte; a long literal run is copied in 64-byte chunks only when
//     ll <= iend-64-ip, so its reads end below ip+ll+64 <= iend; the 2-byte
//     offset at ip <= iend-2.
// (3) Writes of literals: 16 bytes at op <= oend-64; a long run in 64-byte
//     chunks only when ll <= oend-64-op, so below op+ll+64 <= oend.
// (4) Matches: after the literals op <= oend-64+14 (short) or <= oend-64
//     (long), and ml < oend-64-op (else status 1), so cpy = op+ml <
//     oend-64. Every match copy reads at match = op-offset >= lowPrefix >= 0
//     (else status 3) and below its own write position (offset >= 1;
//     offset 0 writes zeros without reading), and writes below cpy+64 <=
//     oend: the 18-byte copy below op+18 <= oend-32; the 64-, 32- and 8-byte
//     chunk loops stop at the first chunk ending at or beyond cpy, so they
//     write below cpy+64, cpy+32 and cpy+8.
TEXT ·decodeFastAsm(SB), NOSPLIT, $0-120
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
	LEAQ -17(R8), R11
	LEAQ -15(R8), R13
	LEAQ -6(R8), R14
	LEAQ -64(R8), R15

loop:
	MOVBQZX (AX), CX             // token
	INCQ    AX
	CMPQ    AX, R11
	JHI     backToken            // too close to input end: the safe loop restarts this sequence
	MOVQ    CX, DX
	SHRQ    $4, DX               // literal length
	CMPQ    DX, $15
	JEQ     longLit
	MOVOU   (AX), X0
	MOVOU   X0, (BX)
	ADDQ    DX, AX
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

mlDone:
	ADDQ $4, DX                  // ml
	MOVQ R12, SI
	SUBQ BX, SI
	CMPQ DX, SI
	JGE  pending                 // op+ml >= oend-64: safe_match_copy
	ANDQ $15, CX
	CMPQ CX, $15
	JEQ  general
	CMPQ R8, $8
	JLO  general
	CMPQ R9, R10
	JLO  general

	// match length from the token, offset >= 8, within prefix: 18 bytes
	MOVQ (R9), SI
	MOVQ SI, (BX)
	MOVQ 8(R9), SI
	MOVQ SI, 8(BX)
	MOVW 16(R9), SI
	MOVW SI, 16(BX)
	ADDQ DX, BX
	JMP  loop

general:
	CMPQ R9, R10
	JLO  offsetError             // checkOffset: match + 0 < lowPrefix
	LEAQ (BX)(DX*1), CX          // cpy
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

zeros:
	// offset 0 (invalid): C writes zeros
	MOVQ $0, (BX)
	ADDQ $8, BX
	CMPQ BX, CX
	JLO  zeros
	MOVQ CX, BX
	JMP  loop

longLit:
	LEAQ -1(AX), DI              // token position
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
	JMP  mlDone

restart:
	MOVQ DI, AX
	JMP  leave

backToken:
	DECQ AX

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
