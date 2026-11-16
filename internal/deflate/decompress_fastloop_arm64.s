//go:build arm64 && !appengine && !noasm && !purego && gc

// The fast loop of libdeflate v1.26 lib/decompress_template.h (Copyright 2016
// Eric Biggers, MIT; see NOTICE) for arm64. C has no aarch64-specific
// decompressor (deflate_decompress_default is the template compiled for
// the baseline target); this follows the Go fast loop of decompress_generic.go
// step for step, as decompress_fastloop_amd64.s does: the same refills,
// preloads, consumption order, reads and writes. The differences are the
// entry and exit (offsets turned into pointers and back, the state passed
// as arguments and results) and the match copies for offset >= 16 and
// offset == 1, which move 16 bytes at a time with LDP/STP over exactly the
// byte ranges of the Go loop's 8-byte words.
//
// Only baseline ARMv8.0 instructions are used: variable shifts (LSLV/LSRV,
// which take the shift amount mod 64, as the Go loop's x&63) stand in for
// BMI2's SHLX/SHRX, and BIC with a shifted all-ones mask for BZHI. Unaligned
// 8-byte loads and stores and LDP/STP are architectural on arm64 (normal
// memory; Linux and Darwin user space do not enable strict alignment).
//
// Performance: this has been checked for equivalence with the Go loop under
// qemu-aarch64 only. Its speed has not been measured on arm64 hardware;
// whether it beats the Go loop there is unknown until it is benchmarked on
// real arm64 machines (no loop alignment or scheduling has been tuned).

#include "textflag.h"

// Registers:
//   R0  bitbuf             R1  bitsleft (only the low byte is meaningful, as
//                              in C; 32-bit ops keep the upper half zero)
//   R2  in_next            R3  out_next
//   R4  entry              R5  saved_bitbuf
//   R6  litlen table       R7  offset table
//   R8  litlen_tablebits (clamped to 11)
//   R9  in_fastloop_end    R10 out_fastloop_end
//   R11 out (base)         R12 in (base)
//   R13 length             R14 offset
//   R15 dst (copy)         R16 src (copy)
//   R22 litlen_tablemask   R23 constant -1   R24 constant 0x0101010101010101
//   R17, R19, R20, R21 scratch.
// R18 (platform register), R26 (closure context), R27 (REGTMP), R28 (g),
// R29 (FP) and R30 (LR) are not touched; nothing is written to the stack
// (NOFRAME: a leaf with no locals).

// Refill thresholds of the offset decode (decompress_fastloop_arm64.go
// checks them against the Go constants): OFFSET_MAXBITS + LITLEN_TABLEBITS
// - PRELOAD_SLACK and OFFSET_MAXFASTBITS + LITLEN_TABLEBITS - PRELOAD_SLACK.
#define OFFSET_SUB_REFILL  $38
#define OFFSET_FAST_REFILL $31

// REFILL_BITS_IN_FASTLOOP (REFILL_BITS_BRANCHLESS) with scratch t:
// bitbuf |= load64(in_next) << (bitsleft & 63); in_next += 7 - ((bitsleft
// >> 3) & 7); bitsleft |= MAX_BITSLEFT & ~7.
#define REFILL(t) \
	MOVD (R2), t;        \
	LSL  R1, t, t;       \
	ORR  t, R0, R0;      \
	UBFX $3, R1, $3, t;  \
	SUB  t, R2, R2;      \
	ADD  $7, R2, R2;     \
	ORRW $56, R1, R1

// entry = litlen_table[bitbuf & litlen_tablemask]. Clobbers R17.
#define PRELOAD_LITLEN \
	AND   R22, R0, R17; \
	MOVWU (R6)(R17<<2), R4

// saved_bitbuf = bitbuf; bitbuf >>= entry & 63; bitsleft -= entry.
#define CONSUME \
	MOVD R0, R5;     \
	LSR  R4, R0, R0; \
	SUBW R4, R1, R1

// entry = table[(entry >> 16) + (bitbuf & BITMASK((entry >> 8) & 0x3F))]:
// the subtable entry (32-bit index, as the Go loop's uint32). Clobbers R17,
// R19.
#define SUBTABLE(table) \
	UBFXW $8, R4, $6, R17;    \
	LSL   R17, R23, R17;      \
	BIC   R17, R0, R17;       \
	LSRW  $16, R4, R19;       \
	ADDW  R19, R17, R17;      \
	MOVWU (table)(R17<<2), R4

// dst = (entry >> 16) + ((saved_bitbuf & BITMASK(entry & 63)) >> ((entry >>
// 8) & 63)): a match length or offset (C: EXTRACT_VARBITS8(saved_bitbuf,
// entry) >> (u8)(entry >> 8)), as the Go loop's uint32 sum. Clobbers R17,
// R19.
#define VALUE(dst) \
	LSL  R4, R23, R17;   \
	BIC  R17, R5, R17;   \
	LSRW $8, R4, R19;    \
	LSR  R19, R17, R17;  \
	LSRW $16, R4, R19;   \
	ADDW R19, R17, dst

// Copy one 8-byte word src -> dst and advance both by step. Clobbers R17.
#define WORD(step) \
	MOVD (R16), R17;  \
	MOVD R17, (R15);  \
	ADD  step, R16;   \
	ADD  step, R15

// port of the fast loop of FUNCNAME (decompress_template.h)
//
// BOUNDS:
// (1) Entry: decompressDefault and decompressStream call this only when
//     in_next < in_fastloop_end and out_next < out_fastloop_end, where
//     in_fastloop_end = len(in) - min(len(in), FASTLOOP_MAX_BYTES_READ = 25)
//     and out_fastloop_end = len(out) - min(len(out),
//     FASTLOOP_MAX_BYTES_WRITTEN = 299), so both slices are non-empty and
//     in/out bases are valid. The out_fastloop_end passed may be lower
//     (out_next + fastloopChunk, the per-call output bound), never higher.
//     Every iteration re-tests the same condition (label cond) before the
//     next one.
// (2) Input reads are the 8-byte REFILL loads at in_next only, in the same
//     places as the Go fast loop: one at entry and at most two per
//     iteration (after the offset decode and after the match; a literal
//     path has one). C's margin argument (decompress.go,
//     fastloopMaxBytesRead): with p0 = in_next at the loop test, an
//     iteration's reads end (exclusive) at most at p0 + 14 (the last load
//     is at most at p0 + 6): 11 bytes inside the 25-byte margin, so at or
//     before len(in) - 12. TestFastloopInputMargin and FuzzDeflateAsmEquiv
//     (in against a guard page) check it.
// (3) Output writes: literals at out_next, at most 3 per iteration (2 extra
//     + 1 in lieu of the item); a match writes from out_next to at most
//     out_next + length rounded up to its round size (40, 32 or 2 * offset
//     for offset < 8) - the word overrun of C. Worst case lit, lit, a
//     258-byte offset-1 match: 2 + 288 = 290 < 299 bytes from the out_next
//     of the loop test: every write ends at or before len(out) - 10. The
//     LDP/STP pairs (offset >= 16 and offset 1) store exactly the ranges of
//     the Go loop's words. TestFastloopOutputMargin and FuzzDeflateAsmEquiv
//     (out against a guard page) check it.
// (4) Match source reads start at src = out_next - offset with offset <=
//     out_next - out checked first (else exit with fastloopBadData before
//     any copy), so src >= out; they end below the write end (src < dst).
//     A 16-byte LDP at src + k (offset >= 16) only reads bytes before
//     dst + k, already final, as the Go words do; for 8 <= offset < 16 and
//     offset < 8 the copies are single words, each loaded after the
//     previous one is stored, as in the Go loop.
// (5) Table reads: the litlen main index is bitbuf & (2^tablebits - 1) <
//     2^11 <= LITLEN_ENOUGH (tablebits is clamped to 11 at entry; it is
//     always <= LITLEN_TABLEBITS = 11, buildDecodeTable); the offset main
//     index is the low 8 bits (OFFSET_TABLEBITS). TABLE INVARIANT: a
//     subtable pointer entry's start + 2^bits is within the table's ENOUGH
//     (buildDecodeTable lays the subtables out inside ENOUGH, C's bound);
//     the Go loop relies on the same entries (bounds-checked).
// (6) Only R0-R17 and R19-R24 are used; the frame is empty (NOFRAME) and
//     nothing is written to the stack.
TEXT ·decompressFastloopAsm(SB), NOSPLIT|NOFRAME, $0-160
	MOVD  in_base+0(FP), R12
	MOVD  out_base+24(FP), R11
	MOVD  litlenTable+48(FP), R6
	MOVD  offsetTable+56(FP), R7
	MOVWU tablebits+64(FP), R8
	CMPW  $11, R8
	BLS   tablebitsOK
	MOVW  $11, R8

tablebitsOK:
	MOVD  $-1, R23
	LSL   R8, R23, R22
	MVN   R22, R22 // litlen_tablemask = 2^tablebits - 1
	MOVD  $0x0101010101010101, R24
	MOVD  inFastloopEnd+72(FP), R9
	ADD   R12, R9, R9
	MOVD  outFastloopEnd+80(FP), R10
	ADD   R11, R10, R10
	MOVD  bitbuf+88(FP), R0
	MOVWU bitsleft+96(FP), R1
	MOVD  inNext+104(FP), R2
	ADD   R12, R2, R2
	MOVD  outNext+112(FP), R3
	ADD   R11, R3, R3

	// Before the loop: REFILL_BITS_IN_FASTLOOP and the first preload.
	REFILL(R17)
	PRELOAD_LITLEN

loop:
	// Consume the bits of the litlen entry, saving bitbuf for the extra
	// length bits.
	CONSUME
	TBZ $31, R4, notLiteral // !(entry & HUFFDEC_LITERAL)

	// 1st extra fast literal.
	LSRW $16, R4, R13
	PRELOAD_LITLEN
	CONSUME
	MOVB R13, (R3)
	ADD  $1, R3
	TBZ  $31, R4, notLiteral

	// 2nd extra fast literal.
	LSRW $16, R4, R13
	PRELOAD_LITLEN
	CONSUME
	MOVB R13, (R3)
	ADD  $1, R3
	TBZ  $31, R4, notLiteral

	// Another fast literal, in lieu of the primary item.
	LSRW $16, R4, R13
	PRELOAD_LITLEN
	REFILL(R17)
	MOVB R13, (R3)
	ADD  $1, R3
	B    cond

notLiteral:
	TBZ  $15, R4, length    // !(entry & HUFFDEC_EXCEPTIONAL)
	TBNZ $13, R4, endOfBlock // HUFFDEC_END_OF_BLOCK

	// Litlen subtable: load and consume the subtable entry.
	SUBTABLE(R6)
	CONSUME
	TBZ $31, R4, subNotLiteral

	// A literal that required a subtable.
	LSRW $16, R4, R13
	PRELOAD_LITLEN
	REFILL(R17)
	MOVB R13, (R3)
	ADD  $1, R3
	B    cond

subNotLiteral:
	TBNZ $13, R4, endOfBlock // HUFFDEC_END_OF_BLOCK

	// Else, a length that required a subtable.

length:
	// Match length (the extra bits were consumed with the entry).
	VALUE(R13)

	// Offset entry: offset_table[bitbuf & BITMASK(OFFSET_TABLEBITS)].
	AND   $0xff, R0, R17
	MOVWU (R7)(R17<<2), R4
	TBZ   $15, R4, offsetFast // !(entry & HUFFDEC_EXCEPTIONAL)

	// Offset subtable.
	ANDW $0xff, R1, R17
	CMPW OFFSET_SUB_REFILL, R17
	BHS  offsetSubRefilled
	REFILL(R17)

offsetSubRefilled:
	LSR  $8, R0, R0 // OFFSET_TABLEBITS
	SUBW $8, R1, R1
	SUBTABLE(R7)
	B    offsetEntry

offsetFast:
	ANDW $0xff, R1, R17
	CMPW OFFSET_FAST_REFILL, R17
	BHS  offsetEntry
	REFILL(R17)

offsetEntry:
	CONSUME
	VALUE(R14)

	// SAFETY_CHECK(offset <= out_next - (const u8 *)out).
	SUB R11, R3, R17
	CMP R17, R14
	BHI badData

	MOVD R3, R15       // dst
	SUB  R14, R3, R16  // src
	ADD  R13, R3, R3   // out_next += length

	// Preload the next litlen entry and refill before the copy.
	PRELOAD_LITLEN
	REFILL(R17)

	CMP $16, R14
	BHS copy16
	CMP $8, R14
	BHS copy8
	CMP $1, R14
	BEQ copy1

	// 2 <= offset < 8: words stepping by offset, two at first, then two
	// per round while dst < out_next.
	WORD(R14)
	WORD(R14)

copySmall:
	WORD(R14)
	WORD(R14)
	CMP R3, R15
	BLO copySmall
	B   cond

copy16:
	// offset >= 16: 40-byte rounds as 16 + 16 + 8 bytes (the Go loop's 5
	// words), the first unconditionally.
	LDP  (R16), (R17, R19)
	STP  (R17, R19), (R15)
	LDP  16(R16), (R20, R21)
	STP  (R20, R21), 16(R15)
	MOVD 32(R16), R17
	MOVD R17, 32(R15)
	ADD  $40, R16
	ADD  $40, R15
	CMP  R3, R15
	BLO  copy16
	B    cond

copy8:
	// 8 <= offset < 16: five words per round, the first unconditionally.
	WORD($8)
	WORD($8)
	WORD($8)
	WORD($8)
	WORD($8)
	CMP R3, R15
	BLO copy8
	B   cond

copy1:
	// offset == 1: the byte repeated, 32 bytes per round, the first
	// unconditionally.
	MOVBU (R16), R17
	MUL   R24, R17, R17

copy1Loop:
	STP (R17, R17), (R15)
	STP (R17, R17), 16(R15)
	ADD $32, R15
	CMP R3, R15
	BLO copy1Loop

cond:
	// while (in_next < in_fastloop_end && out_next < out_fastloop_end)
	CMP R9, R2
	BHS done
	CMP R10, R3
	BLO loop

done:
	MOVD $0, R17 // fastloopContinue
	B    exit

endOfBlock:
	MOVD $1, R17 // fastloopEndOfBlock
	B    exit

badData:
	MOVD $2, R17 // fastloopBadData

exit:
	SUB  R12, R2, R2
	SUB  R11, R3, R3
	MOVD R0, bitbufRet+120(FP)
	MOVW R1, bitsleftRet+128(FP)
	MOVD R2, inNextRet+136(FP)
	MOVD R3, outNextRet+144(FP)
	MOVD R17, status+152(FP)
	RET
