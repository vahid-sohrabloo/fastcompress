//go:build amd64 && !appengine && !noasm && !purego && gc

// The fast loop of libdeflate v1.26 lib/decompress_template.h (Copyright 2016
// Eric Biggers, MIT; see NOTICE) as compiled for deflate_decompress_bmi2
// (lib/x86/decompress_impl.h: target("bmi2"), EXTRACT_VARBITS8 = bzhi). It
// is the Go fast loop of decompress_generic.go step for step: the same
// refills, preloads, consumption order, reads and writes. The differences
// are the entry and exit (offsets turned into pointers and back, the state
// passed as arguments and results) and two match copies that move 16 bytes
// at a time over exactly the byte ranges of the Go loop's 8-byte words.

#include "textflag.h"

// Registers: BX bitbuf, R8 bitsleft (only the low byte is meaningful, as in
// C), SI in_next, DI out_next, DX entry, BP saved_bitbuf, R9 litlen table,
// R10 offset table, R11 litlen_tablebits, R12 in_fastloop_end, R13
// out_fastloop_end (the last four pointers or counts fixed for the call).
// AX, CX, R14, R15, X0 and X1 are scratch: in a match R14 is length, R15
// offset, AX dst and CX src. BP is free to use: the frame is non-empty, so
// the assembler saves BP in the prologue and restores it before RET.
//
// The loop head is 64-byte aligned and the copy loops 16-byte aligned
// (PCALIGN; gcc aligns the loops of deflate_decompress_bmi2 too). PCALIGN
// $64 also raises the function's own alignment to 64 (go tool objdump,
// go1.27), so the head sits at the same place in its cache line whatever
// is linked before it: with $32 the head moved between 0 and 32 mod 64 with
// unrelated code, and at 32 mod 64 a third to nearly all of the alignment
// gain was lost (median about half; dickens 96%, mozilla 88%, ooffice 80%,
// tar/16K 42%, nci 32%; i9-13900H).
// This is tuned on one microarchitecture and may not carry over. The two
// copy loops entered by falling through (copySmall, copy1Loop) are reached
// by a JMP over their padding, so the NOPs never execute (no measurable
// difference either way).

// Refill thresholds of the offset decode (decompress_fastloop_amd64.go
// checks them against the Go constants): OFFSET_MAXBITS + LITLEN_TABLEBITS
// - PRELOAD_SLACK and OFFSET_MAXFASTBITS + LITLEN_TABLEBITS - PRELOAD_SLACK.
#define OFFSET_SUB_REFILL  $38
#define OFFSET_FAST_REFILL $31

// REFILL_BITS_IN_FASTLOOP (REFILL_BITS_BRANCHLESS) with scratch t:
// bitbuf |= load64(in_next) << (u8)bitsleft; in_next += 7 - ((bitsleft >> 3)
// & 7); bitsleft |= MAX_BITSLEFT & ~7.
#define REFILL(t) \
	MOVQ  (SI), t; \
	SHLXQ R8, t, t; \
	ORQ   t, BX; \
	MOVL  R8, t; \
	SHRL  $3, t; \
	ANDL  $7, t; \
	SUBQ  t, SI; \
	ADDQ  $7, SI; \
	ORL   $56, R8

// entry = litlen_table[bitbuf & litlen_tablemask].
#define PRELOAD_LITLEN \
	BZHIQ R11, BX, DX; \
	MOVL  (R9)(DX*4), DX

// saved_bitbuf = bitbuf; bitbuf >>= (u8)entry; bitsleft -= entry.
#define CONSUME \
	MOVQ  BX, BP; \
	SHRXQ DX, BX, BX; \
	SUBL  DX, R8

// entry = table[(entry >> 16) + EXTRACT_VARBITS(bitbuf, (entry >> 8) &
// 0x3F)]: the subtable entry. Clobbers AX, CX.
#define SUBTABLE(table) \
	MOVL  DX, AX; \
	SHRL  $8, AX; \
	ANDL  $0x3F, AX; \
	BZHIQ AX, BX, AX; \
	MOVL  DX, CX; \
	SHRL  $16, CX; \
	ADDL  CX, AX; \
	MOVL  (table)(AX*4), DX

// dst = (entry >> 16) + (EXTRACT_VARBITS8(saved_bitbuf, entry) >> (u8)(entry
// >> 8)): a match length or offset. Clobbers AX, CX. BZHI takes the whole
// low byte of entry as the bit count (as C's EXTRACT_VARBITS8), where the Go
// loop masks with entry & 63: the same, because the low byte of every length
// and offset entry is at most 28 (decompress_generic.go:17-20).
#define VALUE(dst) \
	BZHIQ DX, BP, dst; \
	MOVL  DX, CX; \
	SHRL  $8, CX; \
	SHRXQ CX, dst, dst; \
	MOVL  DX, AX; \
	SHRL  $16, AX; \
	ADDQ  AX, dst

// Copy one 8-byte word src -> dst and advance both by step.
#define WORD(step) \
	MOVQ (CX), R14; \
	MOVQ R14, (AX); \
	ADDQ step, CX; \
	ADDQ step, AX

// BOUNDS:
// (1) Entry: decompressDefault calls this only when in_next < in_fastloop_end
//     and out_next < out_fastloop_end, where in_fastloop_end = len(in) -
//     min(len(in), FASTLOOP_MAX_BYTES_READ = 25) and out_fastloop_end =
//     len(out) - min(len(out), FASTLOOP_MAX_BYTES_WRITTEN = 299), so both
//     slices are non-empty and in/out_base are valid. The out_fastloop_end
//     passed may be lower (out_next + fastloopChunk, the per-call output
//     bound), never higher. Every iteration re-tests the same condition
//     (label cond) before the next one.
// (2) Input reads are the 8-byte REFILL loads at in_next only, in the same
//     places as the Go fast loop: one before the loop and at most two per
//     iteration (after the offset decode and after the match; a literal
//     path has one). C's margin argument (decompress.go,
//     fastloopMaxBytesRead): with p0 = in_next at the loop test, an
//     iteration's reads end (exclusive) at most at p0 + 14 (the last load
//     is at most at p0 + 6): 11 bytes inside the 25-byte margin, so at or
//     before len(in) - 12.
//     The Go loop is bounds-checked on the same accesses, and
//     TestFastloopInputMargin plus FuzzDeflateAsmEquiv (in against a guard
//     page) check the assembly.
// (3) Output writes: literals at out_next, at most 3 per iteration (2 extra
//     + 1 in lieu of the item); a match writes from out_next to at most
//     out_next + length rounded up to its round size (40, 32 or 2 * offset
//     for offset < 8) - the word overrun of C. Worst case lit, lit, a
//     258-byte offset-1 match: 2 + 288 = 290 < 299 bytes from the out_next
//     of the loop test, 9 bytes of slack: every write ends at or before
//     len(out) - 10, below out + len(out). The 16-byte
//     moves (offset >= 16 and offset 1) store exactly the ranges of the Go
//     loop's words. TestFastloopOutputMargin and FuzzDeflateAsmEquiv (out
//     against a guard page) check it.
// (4) Match source reads start at src = out_next - offset with offset <=
//     out_next - out checked first (else exit with fastloopBadData before
//     any copy), so src >= out; they end below the write end (src < dst).
//     A 16-byte load at src + k (offset >= 16) only reads bytes before
//     dst + k, already final, as the Go words do.
// (5) Table reads: the litlen main index is bzhi(bitbuf, tablebits) <
//     2^11 <= LITLEN_ENOUGH (tablebits is clamped to 11 at entry; it is
//     always <= LITLEN_TABLEBITS = 11, buildDecodeTable); the offset main
//     index is the low 8 bits (OFFSET_TABLEBITS). TABLE INVARIANT: a
//     subtable pointer entry's start + 2^bits is within the table's ENOUGH
//     (buildDecodeTable lays the subtables out inside ENOUGH, C's bound);
//     the Go loop relies on the same entries (bounds-checked).
// (6) Only AX..R15, X0, X1 are used, BP saved and restored by the frame;
//     nothing is written to the stack.
TEXT ·decompressFastloopBMI2(SB), NOSPLIT, $8-160
	MOVQ in_base+0(FP), SI
	MOVQ out_base+24(FP), DI
	MOVQ litlenTable+48(FP), R9
	MOVQ offsetTable+56(FP), R10
	MOVL tablebits+64(FP), R11
	CMPL R11, $11
	JLS  tablebitsOK
	MOVL $11, R11

tablebitsOK:
	MOVQ inFastloopEnd+72(FP), R12
	ADDQ SI, R12
	MOVQ outFastloopEnd+80(FP), R13
	ADDQ DI, R13
	MOVQ bitbuf+88(FP), BX
	MOVL bitsleft+96(FP), R8
	ADDQ inNext+104(FP), SI
	ADDQ outNext+112(FP), DI

	// Before the loop: REFILL_BITS_IN_FASTLOOP and the first preload.
	REFILL(AX)
	PRELOAD_LITLEN

	PCALIGN $64

loop:
	// Consume the bits of the litlen entry, saving bitbuf for the extra
	// length bits.
	CONSUME
	TESTL DX, DX
	JNS   notLiteral // !(entry & HUFFDEC_LITERAL)

	// 1st extra fast literal.
	MOVL DX, R14
	SHRL $16, R14
	PRELOAD_LITLEN
	CONSUME
	MOVB R14, (DI)
	INCQ DI
	TESTL DX, DX
	JNS   notLiteral

	// 2nd extra fast literal.
	MOVL DX, R14
	SHRL $16, R14
	PRELOAD_LITLEN
	CONSUME
	MOVB R14, (DI)
	INCQ DI
	TESTL DX, DX
	JNS   notLiteral

	// Another fast literal, in lieu of the primary item.
	MOVL DX, R14
	SHRL $16, R14
	PRELOAD_LITLEN
	REFILL(AX)
	MOVB R14, (DI)
	INCQ DI
	JMP  cond

notLiteral:
	TESTL $0x8000, DX // HUFFDEC_EXCEPTIONAL
	JZ    length
	TESTL $0x2000, DX // HUFFDEC_END_OF_BLOCK
	JNZ   endOfBlock

	// Litlen subtable: load and consume the subtable entry.
	SUBTABLE(R9)
	CONSUME
	TESTL DX, DX
	JNS   subNotLiteral

	// A literal that required a subtable.
	MOVL DX, R14
	SHRL $16, R14
	PRELOAD_LITLEN
	REFILL(AX)
	MOVB R14, (DI)
	INCQ DI
	JMP  cond

subNotLiteral:
	TESTL $0x2000, DX // HUFFDEC_END_OF_BLOCK
	JNZ   endOfBlock

	// Else, a length that required a subtable.

length:
	// Match length (the extra bits were consumed with the entry).
	VALUE(R14)

	// Offset entry: offset_table[bitbuf & BITMASK(OFFSET_TABLEBITS)].
	MOVBQZX BX, AX
	MOVL    (R10)(AX*4), DX
	TESTL   $0x8000, DX // HUFFDEC_EXCEPTIONAL
	JZ      offsetFast

	// Offset subtable.
	CMPB R8, OFFSET_SUB_REFILL
	JAE  offsetSubRefilled
	REFILL(AX)

offsetSubRefilled:
	SHRQ $8, BX // OFFSET_TABLEBITS
	SUBL $8, R8
	SUBTABLE(R10)
	JMP  offsetEntry

offsetFast:
	CMPB R8, OFFSET_FAST_REFILL
	JAE  offsetEntry
	REFILL(AX)

offsetEntry:
	CONSUME
	VALUE(R15)

	// SAFETY_CHECK(offset <= out_next - (const u8 *)out).
	MOVQ DI, AX
	SUBQ out_base+24(FP), AX
	CMPQ R15, AX
	JHI  badData

	MOVQ DI, AX  // dst
	MOVQ DI, CX
	SUBQ R15, CX // src
	ADDQ R14, DI // out_next += length

	// Preload the next litlen entry and refill before the copy.
	PRELOAD_LITLEN
	REFILL(R14)

	CMPQ R15, $16
	JAE  copy16
	CMPQ R15, $8
	JAE  copy8
	CMPQ R15, $1
	JEQ  copy1

	// 2 <= offset < 8: words stepping by offset, two at first, then two
	// per round while dst < out_next.
	WORD(R15)
	WORD(R15)
	JMP copySmall

	PCALIGN $16

copySmall:
	WORD(R15)
	WORD(R15)
	CMPQ AX, DI
	JCS  copySmall
	JMP  cond

	PCALIGN $16

copy16:
	// offset >= 16: 40-byte rounds as 16 + 16 + 8 bytes (the Go loop's 5
	// words), the first unconditionally.
	MOVOU (CX), X0
	MOVOU X0, (AX)
	MOVOU 16(CX), X1
	MOVOU X1, 16(AX)
	MOVQ  32(CX), R14
	MOVQ  R14, 32(AX)
	ADDQ  $40, CX
	ADDQ  $40, AX
	CMPQ  AX, DI
	JCS   copy16
	JMP   cond

	PCALIGN $16

copy8:
	// 8 <= offset < 16: five words per round, the first unconditionally.
	WORD($8)
	WORD($8)
	WORD($8)
	WORD($8)
	WORD($8)
	CMPQ AX, DI
	JCS  copy8
	JMP  cond

copy1:
	// offset == 1: the byte repeated, 32 bytes per round, the first
	// unconditionally.
	MOVBQZX    (CX), R14
	MOVQ       $0x0101010101010101, CX
	IMULQ      CX, R14
	MOVQ       R14, X0
	PUNPCKLQDQ X0, X0
	JMP        copy1Loop

	PCALIGN $16

copy1Loop:
	MOVOU X0, (AX)
	MOVOU X0, 16(AX)
	ADDQ  $32, AX
	CMPQ  AX, DI
	JCS   copy1Loop

cond:
	// while (in_next < in_fastloop_end && out_next < out_fastloop_end)
	CMPQ SI, R12
	JCC  done
	CMPQ DI, R13
	JCS  loop

done:
	MOVQ $0, R14 // fastloopContinue
	JMP  exit

endOfBlock:
	MOVQ $1, R14 // fastloopEndOfBlock
	JMP  exit

badData:
	MOVQ $2, R14 // fastloopBadData

exit:
	SUBQ in_base+0(FP), SI
	SUBQ out_base+24(FP), DI
	MOVQ BX, bitbufRet+120(FP)
	MOVL R8, bitsleftRet+128(FP)
	MOVQ SI, inNextRet+136(FP)
	MOVQ DI, outNextRet+144(FP)
	MOVQ R14, status+152(FP)
	RET
