//go:build amd64 && !appengine && !noasm && !purego && gc

// The sequence output of deflate_flush_block of libdeflate v1.26
// lib/deflate_compress.c (Copyright 2016 Eric Biggers, MIT; see NOTICE): the
// literal runs (4 literals per FLUSH_BITS, then 1..3) and WRITE_MATCH, with
// ADD_BITS / FLUSH_BITS (UNALIGNED_ACCESS_IS_FAST: a whole word while
// out_next < out_fast_end, else a byte at a time) on a 64-bit bit buffer kept
// in registers for the whole list, as gcc -O2 keeps C's locals. Semantics are
// those of writeSequences (flush_block.go), statement for statement.
//
// Preemption: writeSequencesAsm is NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside it. One call writes one block's
// sequences: at most MAX_BLOCK_LENGTH (300 KiB) literals or SEQ_STORE_LENGTH
// (50000) sequences, a few hundred microseconds. BP is not used.
//
// Register use:
//   AX  bitbuf                       CX  bitcount (CL: the ADD_BITS shift)
//   DI  out_next                     R8  out_fast_end
//   SI  in_next                      BX  seq
//   R9  codes                        R10 full (c.o.length)
//   R11 litrunlen, then offset_slot  R12 length
//   R14 &deflateOffsetSlotBase       R15 &deflateExtraOffsetBits
//   DX, R13 scratch

#include "textflag.h"

// deflateCodes byte offsets: codewords.offset[0], lens[0] (litlen),
// lens[lensOffsetStart] (offset); deflateLengthCodes: lens[0]. Asserted in
// flush_block_amd64.go.
#define CW_OFF 1152
#define LENS 1280
#define LENS_OFF 1568
#define FULL_LENS 1036

// ADDLIT(i): ADD_BITS(codes->codewords.litlen[lit], codes->lens.litlen[lit])
// for lit = in_next[i]; clobbers DX, R13.
#define ADDLIT(i) \
	MOVBLZX i(SI), DX; \
	MOVL    (R9)(DX*4), R13; \
	MOVBLZX LENS(R9)(DX*1), DX; \
	SHLQ    CX, R13; \
	ORQ     R13, AX; \
	ADDL    DX, CX

// ADDB(cw, n): ADD_BITS(cw, n) with cw in R13 and n in DX.
#define ADDB \
	SHLQ CX, R13; \
	ORQ  R13, AX; \
	ADDL DX, CX

// FLUSH(slow, back): FLUSH_BITS. The whole-word branch is inline; the
// byte-at-a-time branch is at slow, which jumps back to back. Clobbers DX.
#define FLUSH(slow, back) \
	CMPQ DI, R8; \
	JAE  slow; \
	MOVQ AX, (DI); \
	MOVL CX, DX; \
	ANDL $56, CX; \
	SHRQ CX, AX; \
	SHRL $3, CX; \
	ADDQ CX, DI; \
	ANDL $7, DX; \
	MOVL DX, CX; \
back:

// FLUSHB(slow, back): the byte-at-a-time branch of FLUSH(slow, back). Each
// byte store is checked against out_base + out_len, the counterpart of C's
// ASSERT(out_next < os->end) in FLUSH_BITS (debug builds only; a release
// build relies on its caller's space check alone): a violated cost
// precondition goes to overrun instead of writing past the slice. Clobbers
// DX.
#define FLUSHB(slow, back) \
slow: \
	CMPL CX, $8; \
	JB   back; \
	MOVQ out_base+8(FP), DX; \
	ADDQ out_len+16(FP), DX; \
	CMPQ DI, DX; \
	JAE  overrun; \
	MOVB AX, (DI); \
	INCQ DI; \
	SUBL $8, CX; \
	SHRQ $8, AX; \
	JMP  slow

// func writeSequencesAsm(r *bitRegs, out []byte, codes *deflateCodes, full *deflateLengthCodes, block []byte,
//	seqs *deflateSequence)
//
// BOUNDS: preconditions (kept by flushBlock, which calls it only after its
// space check, with the sequences of the block): r.bitcount <= 7; r.next
// may exceed r.fastEnd (flushBlock computes fastEnd before the block header
// advances next), and r.fastEnd <= len(out) - 7 whenever r.fastEnd > r.next
// (setFastEnd: fastEnd = len(out) - min(7, len(out) - next) for an earlier
// or the same next); the whole block's bits fit in out[r.next:] (the cost
// check of flushBlock: at most 8 * (len(out) - r.next) - r.bitcount bits
// are added in total, so a byte-at-a-time flush writes only bytes before
// len(out); each byte store is also checked against len(out) and faults at
// address 0 instead, see overrun); the sequences from seqs on are a valid
// list of the block: each litrunlen <= MAX_BLOCK_LENGTH, each length 0 or
// 3..258, offset_slot 0..29, the sum of all literal runs and lengths up to
// and including the first sequence with length 0 equals len(block), and
// that sequence is inside the array; full holds codes' full length
// codewords (every codeword and its extra bits fit the lengths, so at most
// 14 + 5 + 15 + 13 bits per match and 4 * 14 per literal group are added
// between flushes: the CAN_BUFFER checks of flush_block.go).
// Memory accesses:
// * r (read, written: bitbuf, bitcount, next; fastEnd read).
// * out: an 8-byte store at out_next only while out_next < out_fast_end, so
//   [out_next, out_next+8) <= len(out); single bytes at out_next while
//   bitcount >= 8 otherwise (inside out by the cost precondition, and only
//   after out_next < out_base + out_len is checked).
// * out_base, out_len argument slots (read in the byte-at-a-time branch).
// * overrun: a 1-byte store to address 0 (a fault by design, never memory).
// * block: in_next[0..3] in a group of 4 literals (in_next + 4 <= the end of
//   the run) and in_next[0] for each of the last 1..3 (inside the run); in_next
//   then moves over the match; all inside block by the sum precondition.
// * seqs: 8 bytes per sequence, up to the one with length 0 (inside the array).
// * codes: codewords.litlen[lit] and lens[lit] (lit < 256 < 288),
//   codewords.offset[offset_slot] and lens[288 + offset_slot] (offset_slot <=
//   29 < 32); full: codewords[length], lens[length] (3 <= length <= 258 < 259);
//   the read-only tables deflateOffsetSlotBase[offset_slot] (uint32 [30]) and
//   deflateExtraOffsetBits[offset_slot] (uint8 [30]).
// * The 0-byte frame (only the argument slots).
// port of deflate_flush_block (output of the sequences list, with WRITE_MATCH)
TEXT ·writeSequencesAsm(SB), NOSPLIT, $0-80
	MOVQ r+0(FP), DX
	MOVQ out_base+8(FP), DI
	MOVQ codes+32(FP), R9
	MOVQ full+40(FP), R10
	MOVQ block_base+48(FP), SI
	MOVQ seqs+72(FP), BX
	MOVQ 24(DX), R8
	ADDQ DI, R8                  // out_fast_end
	ADDQ 16(DX), DI              // out_next
	MOVQ 0(DX), AX               // bitbuf
	MOVQ 8(DX), CX               // bitcount
	LEAQ ·deflateOffsetSlotBase(SB), R14
	LEAQ ·deflateExtraOffsetBits(SB), R15

seqloop:
	MOVL (BX), R11
	MOVL R11, R12
	ANDL $0x7FFFFF, R11          // litrunlen
	SHRL $23, R12                // length
	CMPL R11, $4
	JB   tail

	// Output a run of literals, 4 per flush.
lit4:
	ADDLIT(0)
	ADDLIT(1)
	ADDLIT(2)
	ADDLIT(3)
	ADDQ $4, SI
	FLUSH(slow1, back1)
	SUBL $4, R11
	CMPL R11, $4
	JAE  lit4

tail:
	TESTL R11, R11
	JZ    nolits

lit1:
	// 1..3 literals, then one flush.
	ADDLIT(0)
	INCQ SI
	DECL R11
	JNZ  lit1
	FLUSH(slow2, back2)

nolits:
	TESTL R12, R12
	JZ    end                    // the last sequence

	// WRITE_MATCH. Litlen symbol and extra length bits:
	MOVL    (R10)(R12*4), R13
	MOVBLZX FULL_LENS(R10)(R12*1), DX
	ADDB
	// Offset symbol:
	MOVWLZX 6(BX), R11           // offset_slot
	MOVL    CW_OFF(R9)(R11*4), R13
	MOVBLZX LENS_OFF(R9)(R11*1), DX
	ADDB
	// Extra offset bits:
	MOVWLZX 4(BX), R13
	SUBL    (R14)(R11*4), R13    // offset - deflate_offset_slot_base[offset_slot]
	MOVBLZX (R15)(R11*1), DX
	ADDB
	FLUSH(slow3, back3)
	ADDQ R12, SI                 // in_next += length
	ADDQ $8, BX                  // seq++
	JMP  seqloop

end:
	MOVQ r+0(FP), DX
	MOVQ AX, 0(DX)
	MOVQ CX, 8(DX)
	SUBQ out_base+8(FP), DI
	MOVQ DI, 16(DX)
	RET

	FLUSHB(slow1, back1)
	FLUSHB(slow2, back2)
	FLUSHB(slow3, back3)

	// A byte flush at out_next >= len(out): unreachable under the BOUNDS
	// preconditions. Fault (a store to address 0: the runtime turns it into
	// a nil pointer dereference panic) rather than write past the slice.
overrun:
	MOVQ $0, DX
	MOVB AX, (DX)
	JMP  overrun
