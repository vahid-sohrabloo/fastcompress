//go:build amd64 && !appengine && !noasm && !purego && gc

// The decoding loops of libzstd v1.5.7 lib/common/fse_decompress.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual
// license): FSE_decompress_usingDTable_generic's 4-symbol loop and its tail,
// as decompressUsingDTable has them. C has no assembly here; the Go loops
// keep the bit reader in memory and call its reload.

#include "textflag.h"

// Registers: bitContainer R8, bitsConsumed R9, ptr R10 (offset into src),
// src SI, table DI, mask R15, dst R11, op R12, olimit (len(dst) - 3) DX,
// the states R13 and R14; AX BX CX scratch.

// DECODE(s, off) is dState.decodeSymbol: the symbol of table[s] goes to
// dst[op+off], and s = cell.newState + DStream.ReadBits(cell.nbBits), masked
// to the table. A cell is newState (bits 0-15), symbol (16-23) and nbBits
// (24-31). Clobbers AX, BX, CX.
#define DECODE(s, off) \
	MOVL    (DI)(s*4), AX; \
	MOVL    AX, BX; \
	SHRL    $16, BX; \
	MOVB    BX, off(R11)(R12*1); \
	MOVL    AX, CX; \
	SHRL    $24, CX; \
	MOVQ    $64, BX; \
	SUBQ    R9, BX; \
	SUBQ    CX, BX; \
	SHRXQ   BX, R8, BX; \
	BZHIQ   CX, BX, BX; \
	ADDQ    CX, R9; \
	MOVWLZX AX, s; \
	ADDQ    BX, s; \
	ANDQ    R15, s

// RELOAD_CLAMPED is DStream.Reload for bitsConsumed <= 64 and len(src) >= 8:
// n = min(bitsConsumed >> 3, ptr) bytes back, bitsConsumed -= 8n, and the
// container is the 8 bytes at ptr (already those when ptr was 0). Clobbers
// AX.
#define RELOAD_CLAMPED \
	MOVQ    R9, AX; \
	SHRQ    $3, AX; \
	CMPQ    AX, R10; \
	CMOVQHI R10, AX; \
	SUBQ    AX, R10; \
	SHLQ    $3, AX; \
	SUBQ    AX, R9; \
	MOVQ    (SI)(R10*1), R8

// func decompressAsm(dst *byte, dstLen int, table *decodeEntry, mask uint, src *byte, ptr int, container uint64, consumed, state1, state2 uint) int
//
// BOUNDS:
// (1) Source reads are the 8 bytes at src + ptr. On entry 0 <= ptr <=
//     len(src) - 8 (precondition). The main loop lowers ptr by
//     bitsConsumed >> 3 only when that is at most ptr, and RELOAD_CLAMPED
//     by at most ptr, so 0 <= ptr <= len(src) - 8 at every read.
// (2) Table reads: table[s] with s masked by mask, on entry and at every
//     update; the table has mask + 1 cells (precondition).
// (3) Main loop writes: dst[op..op+3] with op < olimit = dstLen - 3
//     (signed), so op + 3 < dstLen; op starts at 0 and only grows.
// (4) Tail writes: before each pair, op <= dstLen - 2 (signed, else
//     tooSmall), so dst[op] and, at the stream's end, dst[op+1] are inside.
// (5) Frame $0-88: no locals, no calls.
TEXT ·decompressAsm(SB), NOSPLIT, $0-88
	MOVQ dst+0(FP), R11
	MOVQ dstLen+8(FP), DX
	SUBQ $3, DX
	MOVQ table+16(FP), DI
	MOVQ mask+24(FP), R15
	MOVQ src+32(FP), SI
	MOVQ ptr+40(FP), R10
	MOVQ container+48(FP), R8
	MOVQ consumed+56(FP), R9
	MOVQ state1+64(FP), R13
	MOVQ state2+72(FP), R14
	ANDQ R15, R13
	ANDQ R15, R14
	XORQ R12, R12

	// for ; Reload() == DStreamUnfinished && op < olimit; op += 4
loop:
	CMPQ  R9, $64
	JHI   tail       // overflow
	MOVQ  R9, AX
	SHRQ  $3, AX
	CMPQ  AX, R10
	JHI   last       // fewer bytes left than consumed: the last reload
	TESTQ R10, R10
	JEQ   tail       // at src[0] with no whole byte consumed
	SUBQ  AX, R10
	ANDQ  $7, R9
	MOVQ  (SI)(R10*1), R8
	CMPQ  R12, DX
	JGE   tail
	DECODE(R13, 0)
	DECODE(R14, 1)
	DECODE(R13, 2)
	DECODE(R14, 3)
	ADDQ  $4, R12
	JMP   loop

last:
	// Reload() that ends at src[0] (DStreamEndOfBuffer): the tail follows.
	RELOAD_CLAMPED

tail:
	LEAQ 1(DX), AX // dstLen - 2
	CMPQ R12, AX
	JGT  tooSmall
	DECODE(R13, 0)
	INCQ R12
	CMPQ R9, $64
	JHI  end2      // Reload() == DStreamOverflow
	RELOAD_CLAMPED
	LEAQ 1(DX), AX
	CMPQ R12, AX
	JGT  tooSmall
	DECODE(R14, 0)
	INCQ R12
	CMPQ R9, $64
	JHI  end1
	RELOAD_CLAMPED
	JMP  tail

end2:
	DECODE(R14, 0)
	INCQ R12
	JMP  done

end1:
	DECODE(R13, 0)
	INCQ R12

done:
	MOVQ R12, ret+80(FP)
	RET

tooSmall:
	MOVQ $-1, ret+80(FP)
	RET
