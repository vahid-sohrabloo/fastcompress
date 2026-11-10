//go:build amd64 && !appengine && !noasm && !purego && gc

// hc_matchfinder_longest_match of libdeflate v1.26 lib/hc_matchfinder.h
// (Copyright 2016 Eric Biggers, MIT; see NOTICE), the UNALIGNED_ACCESS_IS_FAST
// branch, with lz_extend (lib/matchfinder_common.h) and the window slide
// (hc_matchfinder_slide_window = matchfinder_rebase of the whole struct,
// lib/x86/matchfinder_impl.h matchfinder_rebase_sse2) written in, as gcc -O2
// forceinlines them into deflate_compress_greedy and
// deflate_compress_lazy_generic, and the per-position loops of those two
// (greedyLoopAsm, lazyLoopAsm) around it. The prefetchw calls
// are PREFETCHT0, the instruction libdeflate's x86-64 build emits for them
// (no -mprfchw). Semantics are those of hcMatchfinder.longestMatch (the Go
// reference, statement for statement).
//
// BP: hcLongestMatchAsmForTest, hcLongestMatch258AsmForTest, greedyLoopAsm
// and lazyLoopAsm use BP as a general register (next_hashes[1]). Their
// frames make the assembler save the caller's BP on entry and restore it at
// RET, so Go code never sees a changed BP; but while one of them runs BP is
// not a frame pointer, and a frame-pointer unwinder (perf, the execution
// tracer) that samples inside it sees a bad stack.
//
// Preemption: the routines are NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside one. Bound per call: greedyLoopAsm
// and lazyLoopAsm return to Go after at most 2 * MIN_BLOCK_LENGTH + 512
// (about 10500) positions, in steady state after at most 512:
// ready_to_check_block holds once the block has MIN_BLOCK_LENGTH bytes, at
// least MIN_BLOCK_LENGTH bytes remain and NUM_OBSERVATIONS_PER_BLOCK_CHECK
// (512) observations, one per position, are new; within MIN_BLOCK_LENGTH of
// the input end they run to the last 258 bytes; lazyLoopAsm also returns at
// each min_len recalculation. Each position does at most three search
// walks of at most max_search_depth nodes and at most one window slide of the
// 256 KiB matchfinder.
//
// The search is the HCSEARCH macro (HCSEARCH258 in greedyLoopAsm), expanded
// in place at each search of the two loops and once in each of their
// Go-callable test entries, hcLongestMatchAsmForTest and
// hcLongestMatch258AsmForTest (no production caller). Its registers:
//   DI  mf                     BX  strptr (&in[inNext])
//   R13 &in[*inBase] (C in_base, advanced by a slide)
//   R15 cutoff (sign-extended mf_pos_t)
//   R12 depth_remaining        R10 best_len
//   R11 next_hashes[0]         R8  best_matchptr
//   BP  next_hashes[1]         SI  seq4 (first 4 bytes)
//   AX  cur_node4              DX  matchptr
//   R14 the 4 bytes at strptr+best_len-3
//   CX  lz_extend length       R9  scratch (lz_extend word, max_len)
// AX holds a node as a signed mf_pos_t: sign-extended from the hash4 bucket,
// in the chain walks the low 16 bits of the zero-extending MOVWLZX load of
// next_tab (gcc's movzwl; a sign-extending load is a cycle slower on the
// walk's load-to-load chain), compared with the cutoff in 16 bits (CMPW, as
// gcc's cmp %si,%ax) and sign-extended into DX for the match pointer.
// max_len and nice_len are memory operands (the macro arguments ML / NL; a
// constant 258 in HCSEARCH258). Keeping next_hashes in R11 / BP across the
// loops' positions, as gcc does in deflate_compress_greedy and
// deflate_compress_lazy_generic, instead of in the frame, made levels 2..9
// 1-9% faster, the MOVWLZX walk loads another 0-8% (T17.1).

#include "textflag.h"

// struct hcMatchfinder byte offsets (asserted in hc_matchfinder_amd64.go).
#define HASH4_OFF 65536
#define NEXT_OFF 196608

// HCSEARCH(ML, NL, ns, sl, c4, l4, hv, nb, l5, n5, cd, e8, e1, d24, d16, d8,
// dd, ed, ot): hc_matchfinder_longest_match written in where it is used (as
// gcc forceinlines it), with the registers listed above except that
// next_hashes[0] / next_hashes[1] are in R11 / BP (gcc keeps them in two
// registers across the compressors' loops) and max_len / nice_len are read
// at the operands ML / NL; R11 and BP are < 2^15 / < 2^16 on entry and are
// replaced by the next hashes (lz_hash results, so the same bounds hold) with
// no masking. It ends at the label ot. The other arguments are the labels
// of one expansion (unique per TEXT): ns noslide, sl the slide loop, c4 the
// length 4 check, l4 the length 4 chain walk, hv a known match of length >= 4,
// nb a new best length, l5 / n5 the chain walk for longer matches, cd a
// candidate, e8 / e1 lz_extend's 8-byte and 1-byte steps, d24 / d16 / d8 / dd
// the first differing word, ed the extended length. In order: the slide at
// cur_pos == MATCHFINDER_WINDOW_SIZE (PADDSW of 0x8000 over the struct), the
// cutoff, the max_len < 5 exit, the bucket insertion (hash3_tab, hash4_tab,
// next_tab), the next hashes with their prefetches, the length 3 check, the
// length 4 walk, then the walk for longer matches with lz_extend (32 bytes,
// then 8, then 1 at a time; its TZCNTQ runs only on a nonzero XOR word, for
// which TZCNT (REP BSF) and BSF give the same result, so the encoding is
// correct on CPUs without BMI1 too). The chain-walk loop heads are 32-byte
// aligned (PCALIGN). Its accesses are listed in the BOUNDS block of
// hcLongestMatchAsmForTest.
#define HCSEARCH(ML, NL, ns, sl, c4, l4, hv, nb, l5, n5, cd, e8, e1, d24, d16, d8, dd, ed, ot) \
	MOVQ BX, CX; \
	SUBQ R13, CX; \
	CMPL CX, $0x8000; \
	JNE  ns; \
	MOVL   $0x80008000, DX; \
	MOVQ   DX, X4; \
	PSHUFD $0, X4, X4; \
	MOVQ   DI, DX; \
	MOVL   $4096, CX; \
sl: \
	MOVOU  (DX), X0; \
	MOVOU  16(DX), X1; \
	MOVOU  32(DX), X2; \
	MOVOU  48(DX), X3; \
	PADDSW X4, X0; \
	PADDSW X4, X1; \
	PADDSW X4, X2; \
	PADDSW X4, X3; \
	MOVOU  X0, (DX); \
	MOVOU  X1, 16(DX); \
	MOVOU  X2, 32(DX); \
	MOVOU  X3, 48(DX); \
	ADDQ   $64, DX; \
	DECL   CX; \
	JNZ    sl; \
	ADDQ   $0x8000, R13; \
	XORL   CX, CX; \
ns: \
	MOVQ    BX, R8; \
	LEAL    -0x8000(CX), R15; \
	MOVWQSX R15, R15; \
	CMPL    ML, $5; \
	JB      ot; \
	MOVWQSX (DI)(R11*2), R14; \
	MOVWQSX HASH4_OFF(DI)(BP*2), AX; \
	MOVW    CX, (DI)(R11*2); \
	MOVW    CX, HASH4_OFF(DI)(BP*2); \
	ANDL    $0x7FFF, CX; \
	MOVW    AX, NEXT_OFF(DI)(CX*2); \
	MOVL    1(BX), CX; \
	MOVL    CX, R11; \
	ANDL    $0xFFFFFF, R11; \
	IMUL3L  $0x1E35A7BD, R11, R11; \
	IMUL3L  $0x1E35A7BD, CX, BP; \
	SHRL    $17, R11; \
	SHRL    $16, BP; \
	PREFETCHT0 (DI)(R11*2); \
	PREFETCHT0 HASH4_OFF(DI)(BP*2); \
	CMPL R10, $4; \
	JAE  hv; \
	CMPQ R14, R15; \
	JLE  ot; \
	MOVL (BX), SI; \
	CMPL R10, $3; \
	JAE  c4; \
	MOVL (R13)(R14*1), DX; \
	XORL SI, DX; \
	ANDL $0xFFFFFF, DX; \
	JNZ  c4; \
	MOVL $3, R10; \
	LEAQ (R13)(R14*1), R8; \
c4: \
	CMPQ AX, R15; \
	JLE  ot; \
	PCALIGN $32; \
l4: \
	MOVWQSX AX, DX; \
	ADDQ    R13, DX; \
	CMPL    (DX), SI; \
	JEQ     cd; \
	ANDL    $0x7FFF, AX; \
	MOVWLZX NEXT_OFF(DI)(AX*2), AX; \
	CMPW    AX, R15; \
	JLE     ot; \
	DECL    R12; \
	JNZ     l4; \
	JMP     ot; \
hv: \
	CMPQ AX, R15; \
	JLE  ot; \
	CMPL R10, NL; \
	JAE  ot; \
	MOVL (BX), SI; \
nb: \
	MOVL -3(BX)(R10*1), R14; \
	PCALIGN $32; \
l5: \
	MOVWQSX AX, DX; \
	ADDQ    R13, DX; \
	CMPL -3(DX)(R10*1), R14; \
	JNE  n5; \
	CMPL (DX), SI; \
	JEQ  cd; \
n5: \
	ANDL    $0x7FFF, AX; \
	MOVWLZX NEXT_OFF(DI)(AX*2), AX; \
	CMPW    AX, R15; \
	JLE     ot; \
	DECL    R12; \
	JNZ     l5; \
	JMP     ot; \
cd: \
	MOVL $4, CX; \
	MOVL ML, R9; \
	SUBL CX, R9; \
	CMPL R9, $32; \
	JB   e8; \
	MOVQ (DX)(CX*1), R9; \
	XORQ (BX)(CX*1), R9; \
	JNZ  dd; \
	MOVQ 8(DX)(CX*1), R9; \
	XORQ 8(BX)(CX*1), R9; \
	JNZ  d8; \
	MOVQ 16(DX)(CX*1), R9; \
	XORQ 16(BX)(CX*1), R9; \
	JNZ  d16; \
	MOVQ 24(DX)(CX*1), R9; \
	XORQ 24(BX)(CX*1), R9; \
	JNZ  d24; \
	ADDL $32, CX; \
e8: \
	LEAL 8(CX), R9; \
	CMPL R9, ML; \
	JA   e1; \
	MOVQ (DX)(CX*1), R9; \
	XORQ (BX)(CX*1), R9; \
	JNZ  dd; \
	ADDL $8, CX; \
	JMP  e8; \
e1: \
	CMPL    CX, ML; \
	JAE     ed; \
	MOVBLZX (DX)(CX*1), R9; \
	CMPB    R9, (BX)(CX*1); \
	JNE     ed; \
	INCL    CX; \
	JMP     e1; \
d24: \
	ADDL $8, CX; \
d16: \
	ADDL $8, CX; \
d8: \
	ADDL $8, CX; \
dd: \
	TZCNTQ R9, R9; \
	SHRL   $3, R9; \
	ADDL   R9, CX; \
ed: \
	CMPL CX, R10; \
	JBE  n5; \
	MOVL CX, R10; \
	MOVQ DX, R8; \
	CMPL R10, NL; \
	JAE  ot; \
	ANDL    $0x7FFF, AX; \
	MOVWLZX NEXT_OFF(DI)(AX*2), AX; \
	CMPW    AX, R15; \
	JLE     ot; \
	DECL    R12; \
	JNZ     nb; \
ot:

// HCSEARCH258(NL, labels...): HCSEARCH for max_len = 258 (greedyLoopAsm,
// where max_len is fixed), with next_hashes[0] / next_hashes[1] in R11 / BP
// (gcc keeps them in two registers across deflate_compress_greedy's loop) and
// nice_len at the operand NL; R11 and BP are < 2^15 / < 2^16 on entry and are
// replaced by the next hashes (lz_hash results, so the same bounds hold) with
// no masking. max_len is the constant 258 (no max_len < 5 exit, lz_extend
// always starts with the 32-byte step), as gcc folds it into
// deflate_compress_greedy. Same accesses as HCSEARCH.
#define HCSEARCH258(NL, ns, sl, c4, l4, hv, nb, l5, n5, cd, e8, e1, d24, d16, d8, dd, ed, ot) \
	MOVQ BX, CX; \
	SUBQ R13, CX; \
	CMPL CX, $0x8000; \
	JNE  ns; \
	MOVL   $0x80008000, DX; \
	MOVQ   DX, X4; \
	PSHUFD $0, X4, X4; \
	MOVQ   DI, DX; \
	MOVL   $4096, CX; \
sl: \
	MOVOU  (DX), X0; \
	MOVOU  16(DX), X1; \
	MOVOU  32(DX), X2; \
	MOVOU  48(DX), X3; \
	PADDSW X4, X0; \
	PADDSW X4, X1; \
	PADDSW X4, X2; \
	PADDSW X4, X3; \
	MOVOU  X0, (DX); \
	MOVOU  X1, 16(DX); \
	MOVOU  X2, 32(DX); \
	MOVOU  X3, 48(DX); \
	ADDQ   $64, DX; \
	DECL   CX; \
	JNZ    sl; \
	ADDQ   $0x8000, R13; \
	XORL   CX, CX; \
ns: \
	MOVQ    BX, R8; \
	LEAL    -0x8000(CX), R15; \
	MOVWQSX R15, R15; \
	MOVWQSX (DI)(R11*2), R14; \
	MOVWQSX HASH4_OFF(DI)(BP*2), AX; \
	MOVW    CX, (DI)(R11*2); \
	MOVW    CX, HASH4_OFF(DI)(BP*2); \
	ANDL    $0x7FFF, CX; \
	MOVW    AX, NEXT_OFF(DI)(CX*2); \
	MOVL    1(BX), CX; \
	MOVL    CX, R11; \
	ANDL    $0xFFFFFF, R11; \
	IMUL3L  $0x1E35A7BD, R11, R11; \
	IMUL3L  $0x1E35A7BD, CX, BP; \
	SHRL    $17, R11; \
	SHRL    $16, BP; \
	PREFETCHT0 (DI)(R11*2); \
	PREFETCHT0 HASH4_OFF(DI)(BP*2); \
	CMPL R10, $4; \
	JAE  hv; \
	CMPQ R14, R15; \
	JLE  ot; \
	MOVL (BX), SI; \
	CMPL R10, $3; \
	JAE  c4; \
	MOVL (R13)(R14*1), DX; \
	XORL SI, DX; \
	ANDL $0xFFFFFF, DX; \
	JNZ  c4; \
	MOVL $3, R10; \
	LEAQ (R13)(R14*1), R8; \
c4: \
	CMPQ AX, R15; \
	JLE  ot; \
	PCALIGN $32; \
l4: \
	MOVWQSX AX, DX; \
	ADDQ    R13, DX; \
	CMPL    (DX), SI; \
	JEQ     cd; \
	ANDL    $0x7FFF, AX; \
	MOVWLZX NEXT_OFF(DI)(AX*2), AX; \
	CMPW    AX, R15; \
	JLE     ot; \
	DECL    R12; \
	JNZ     l4; \
	JMP     ot; \
hv: \
	CMPQ AX, R15; \
	JLE  ot; \
	CMPL R10, NL; \
	JAE  ot; \
	MOVL (BX), SI; \
nb: \
	MOVL -3(BX)(R10*1), R14; \
	PCALIGN $32; \
l5: \
	MOVWQSX AX, DX; \
	ADDQ    R13, DX; \
	CMPL -3(DX)(R10*1), R14; \
	JNE  n5; \
	CMPL (DX), SI; \
	JEQ  cd; \
n5: \
	ANDL    $0x7FFF, AX; \
	MOVWLZX NEXT_OFF(DI)(AX*2), AX; \
	CMPW    AX, R15; \
	JLE     ot; \
	DECL    R12; \
	JNZ     l5; \
	JMP     ot; \
cd: \
	MOVL $4, CX; \
	MOVQ (DX)(CX*1), R9; \
	XORQ (BX)(CX*1), R9; \
	JNZ  dd; \
	MOVQ 8(DX)(CX*1), R9; \
	XORQ 8(BX)(CX*1), R9; \
	JNZ  d8; \
	MOVQ 16(DX)(CX*1), R9; \
	XORQ 16(BX)(CX*1), R9; \
	JNZ  d16; \
	MOVQ 24(DX)(CX*1), R9; \
	XORQ 24(BX)(CX*1), R9; \
	JNZ  d24; \
	ADDL $32, CX; \
e8: \
	LEAL 8(CX), R9; \
	CMPL R9, $258; \
	JA   e1; \
	MOVQ (DX)(CX*1), R9; \
	XORQ (BX)(CX*1), R9; \
	JNZ  dd; \
	ADDL $8, CX; \
	JMP  e8; \
e1: \
	CMPL    CX, $258; \
	JAE     ed; \
	MOVBLZX (DX)(CX*1), R9; \
	CMPB    R9, (BX)(CX*1); \
	JNE     ed; \
	INCL    CX; \
	JMP     e1; \
d24: \
	ADDL $8, CX; \
d16: \
	ADDL $8, CX; \
d8: \
	ADDL $8, CX; \
dd: \
	TZCNTQ R9, R9; \
	SHRL   $3, R9; \
	ADDL   R9, CX; \
ed: \
	CMPL CX, R10; \
	JBE  n5; \
	MOVL CX, R10; \
	MOVQ DX, R8; \
	CMPL R10, NL; \
	JAE  ot; \
	ANDL    $0x7FFF, AX; \
	MOVWLZX NEXT_OFF(DI)(AX*2), AX; \
	CMPW    AX, R15; \
	JLE     ot; \
	DECL    R12; \
	JNZ     nb; \
ot:

// func hcLongestMatchAsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
//	bestLen, maxLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32)
//
// The HCSEARCH expansion of lazyLoopAsm, expanded here once: the next_hashes
// words are loaded into R11 / BP (masked to their tables, the macro's entry
// condition), max_len / nice_len are the maxLen / niceLen argument slots, and
// the next hashes are stored back. In: DI mf, BX strptr, R13 in_base, R10
// best_len, R12 max_search_depth; out R10 the length, R8 the best match
// pointer (strptr if none), R13 the in_base after a slide.
//
// BOUNDS: preconditions (those under which the Go reference does not panic;
// the callers keep them by construction): max_len <= len(in) - inNext;
// nice_len <= max_len; 0 <= strptr - in_base <= 32768; every
// hash3Tab/hash4Tab/nextTab entry reachable from the buckets that is > cutoff
// is a position p (relative to in_base, after the slide) of this buffer with
// in <= in_base + p < strptr.
// Memory accesses:
// * mf (the 262144-byte struct): the slide reads and writes bytes [0, 262144)
//   (4096 iterations of 64 bytes); hash3Tab[R11] and hash4Tab[BP] (read and
//   written; R11 < 2^15 and BP < 2^16 by the entry condition), nextTab[curPos
//   & 0x7FFF] (written), nextTab[node & 0x7FFF] (read). The next hashes are
//   lz_hash results (a 32-bit product shifted right by 17 / 16), so R11 <
//   2^15 and BP < 2^16 hold again at the end. The two PREFETCHT0 touch
//   hash3Tab[R11] / hash4Tab[BP] with those new values; a prefetch never
//   faults anyway.
// * in, only when max_len >= 5: the 4 bytes at strptr+1 (<= strptr+max_len)
//   and at strptr (seq4); for a candidate m = in_base + node (node > cutoff,
//   so m < strptr by the precondition): 4 bytes at m (length 3 and 4 checks,
//   the 24-bit compare masks the 4th byte, m+3 < strptr+max_len), 4 bytes at
//   m+best_len-3 and at strptr+best_len-3 with 4 <= best_len < nice_len <=
//   max_len (so they end at most at +max_len); lz_extend reads 8 bytes at m+n
//   and strptr+n only while n+8 <= max_len, then single bytes at n < max_len.
//   So in is read only in [m, m+max_len) with m >= &in[0] and [strptr,
//   strptr+max_len).
// Also (this entry) nextHashes[0..1] and *inBase (read, written back), the
// arguments and the 8-byte frame (the assembler's BP save: BP holds
// next_hashes[1], a general register, not a frame pointer, while inside: see
// the file header).
// C: hc_matchfinder_longest_match (+ lz_extend, hc_matchfinder_slide_window)
TEXT ·hcLongestMatchAsmForTest(SB), NOSPLIT, $8-80
	MOVQ mf+0(FP), DI
	MOVQ in_base+8(FP), R9
	MOVQ inBase+32(FP), AX
	MOVQ inNext+40(FP), BX
	ADDQ R9, BX
	MOVQ (AX), R13
	ADDQ R9, R13
	MOVL bestLen+48(FP), R10
	MOVL maxSearchDepth+60(FP), R12
	MOVQ nextHashes+64(FP), SI
	MOVL 0(SI), R11
	ANDL $0x7FFF, R11
	MOVL 4(SI), BP
	ANDL $0xFFFF, BP
	HCSEARCH(maxLen+52(FP), niceLen+56(FP), hns, hsl, hc4, hl4, hhv, hnb, hl5, hn5, hcd, he8, he1, hd24, hd16, hd8, hdd, hed, hout)
	MOVQ nextHashes+64(FP), SI
	MOVL R11, 0(SI)
	MOVL BP, 4(SI)
	MOVQ in_base+8(FP), R9
	SUBQ R9, R13
	MOVQ inBase+32(FP), AX
	MOVQ R13, (AX)           // *in_base_p (changed only by a slide)
	SUBQ R8, BX              // offset = in_next - best_matchptr
	MOVL R10, length+72(FP)
	MOVL BX, offset+76(FP)
	RET

// func hcLongestMatch258AsmForTest(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
//	bestLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32)
//
// The HCSEARCH258 expansion of greedyLoopAsm, expanded here once: the
// next_hashes words are loaded into R11 / BP (masked to their tables, the
// macro's entry condition), nice_len is the niceLen argument slot, and the
// next hashes are stored back.
//
// BOUNDS: hcLongestMatchAsmForTest's with max_len = 258 (so 258 <= len(in) -
// inNext and niceLen <= 258; in_base = &in[*inBase], strptr = &in[inNext]),
// plus nextHashes[0..1] and *inBase (read, written back), the arguments and
// the 8-byte frame (the assembler's BP save: BP holds next_hashes[1], a
// general register, not a frame pointer, while inside: see the file header).
// C: hc_matchfinder_longest_match (max_len 258, as in deflate_compress_greedy)
TEXT ·hcLongestMatch258AsmForTest(SB), NOSPLIT, $8-80
	MOVQ mf+0(FP), DI
	MOVQ in_base+8(FP), R9
	MOVQ inBase+32(FP), AX
	MOVQ inNext+40(FP), BX
	ADDQ R9, BX
	MOVQ (AX), R13
	ADDQ R9, R13
	MOVL bestLen+48(FP), R10
	MOVL maxSearchDepth+56(FP), R12
	MOVQ nextHashes+64(FP), SI
	MOVL 0(SI), R11
	ANDL $0x7FFF, R11
	MOVL 4(SI), BP
	ANDL $0xFFFF, BP
	HCSEARCH258(niceLen+52(FP), xns, xsl, xc4, xl4, xhv, xnb, xl5, xn5, xcd, xe8, xe1, xd24, xd16, xd8, xdd, xed, xout)
	MOVQ nextHashes+64(FP), SI
	MOVL R11, 0(SI)
	MOVL BP, 4(SI)
	MOVQ in_base+8(FP), R9
	SUBQ R9, R13
	MOVQ inBase+32(FP), AX
	MOVQ R13, (AX)           // *in_base_p (changed only by a slide)
	SUBQ R8, BX              // offset = in_next - best_matchptr
	MOVL R10, length+72(FP)
	MOVL BX, offset+76(FP)
	RET

// greedyState byte offsets (asserted in hc_matchfinder_amd64.go):
// sequences[0] and sequences[SEQ_STORE_LENGTH]. deflateFreqs:
// litlen[DEFLATE_FIRST_LEN_SYM] and offset[0]. blockSplitStats:
// new_observations[NUM_LITERAL_OBSERVATION_TYPES] and num_new_observations.
#define GSEQ_OFF 262144
#define GSEQ_END 662144
#define FREQ_LEN 1028
#define FREQ_OFF 1152
#define OBS_MATCH 32
#define OBS_NUM 80

// HCSLIDE rebases the whole hc_matchfinder at DI (as HCSEARCH's slide);
// clobbers DX, R9, X0-X4. lbl is a unique loop label.
#define HCSLIDE(lbl) \
	MOVL   $0x80008000, DX; \
	MOVQ   DX, X4; \
	PSHUFD $0, X4, X4; \
	MOVQ   DI, DX; \
	MOVL   $4096, R9; \
lbl: \
	MOVOU  (DX), X0; \
	MOVOU  16(DX), X1; \
	MOVOU  32(DX), X2; \
	MOVOU  48(DX), X3; \
	PADDSW X4, X0; \
	PADDSW X4, X1; \
	PADDSW X4, X2; \
	PADDSW X4, X3; \
	MOVOU  X0, (DX); \
	MOVOU  X1, 16(DX); \
	MOVOU  X2, 32(DX); \
	MOVOU  X3, 48(DX); \
	ADDQ   $64, DX; \
	DECL   R9; \
	JNZ    lbl

// func greedyLoopAsm(pg *greedyState, freqs *deflateFreqs, stats *blockSplitStats, in []byte,
//	st *greedyLoopState)
//
// The per-position loop of deflate_compress_greedy (lib/deflate_compress.c):
// hc_matchfinder_longest_match (HCSEARCH258, best_len min_len - 1, max_len 258,
// nice_len, max_search_depth), the literal / match choice,
// deflate_choose_literal / deflate_choose_match with gather_split_stats
// (observe_literal / observe_match) and hc_matchfinder_skip_bytes, while at
// least DEFLATE_MAX_MATCH_LEN bytes remain (so max_len and nice_len are
// fixed). It returns after the first position at which in_next >=
// in_max_block_end, seq reaches SEQ_STORE_LENGTH or ready_to_check_block holds
// (the Go loop then runs should_end_block's do_end_block_check), or when fewer
// bytes remain. Semantics are those of the Go loop of deflateCompressGreedy.
// Registers across positions: DI pg, BX in_next, R13 in_cur_base, R11 / BP
// next_hashes[0] / [1] (as gcc keeps them; BP as a general register, not a
// frame pointer: see the file header); the rest of the state is in the frame
// (C keeps most of it in memory too), and the search clobbers every other
// register.
//
// BOUNDS: preconditions (kept by deflateCompressGreedy): len(in) - st.inNext >=
// 258; 0 <= st.inNext - st.inCurBase <= 32768; st.inBlockBegin <= st.inNext <
// st.inMaxBlockEnd <= len(in); 0 <= st.seq < 50000; 1 <= st.minLen <= 258;
// st.niceLen <= 258; the matchfinder invariant of hcLongestMatchAsmForTest's
// BOUNDS. st.nextHash3 / st.nextHash4 are masked to 15 / 16 bits on entry
// (HCSEARCH258's entry condition; the identity for the lz_hash values Go
// passes). The loop runs only while in_next <= len(in) - 258 (checked before
// each position), so the search (HCSEARCH258, max_len 258) reads only inside
// in (hcLongestMatchAsmForTest's BOUNDS).
// Other accesses: a literal reads in[in_next]; a skip (only if in_next +
// length + 5 <= len(in)) reads the 4 bytes at in_next+2+i, i < length-1, all
// before in_next+length+4 < len(in), and writes hash3Tab/hash4Tab/nextTab at
// masked indexes (a slide rebases bytes [0, 262144) of pg); sequences[seq]
// and [seq+1] (seq+1 <= 50000, the last index of the 50001-entry array: the
// loop stops once seq reaches 50000); freqs.litlen[0..255],
// litlen[257+length_slot <= 285], offset[offset_slot <= 29];
// stats.new_observations[0..9] and num_new_observations; the slot tables
// deflateLengthSlot[3..258], deflateOffsetSlot[0..255]; st; the 96-byte frame.
// C: deflate_compress_greedy (the inner loop of a block)
TEXT ·greedyLoopAsm(SB), NOSPLIT, $96-56
	MOVQ pg+0(FP), DI
	MOVQ freqs+8(FP), AX
	MOVQ AX, freqs-16(SP)
	MOVQ stats+16(FP), AX
	MOVQ AX, stats-24(SP)
	MOVQ in_base+24(FP), R9
	MOVQ in_len+32(FP), CX
	LEAQ (R9)(CX*1), DX
	MOVQ DX, inend-88(SP)
	SUBQ $258, DX
	MOVQ DX, limit-32(SP)
	MOVQ st+48(FP), AX
	MOVQ 0(AX), BX
	ADDQ R9, BX
	MOVQ 8(AX), R13
	ADDQ R9, R13
	MOVQ 16(AX), CX
	ADDQ R9, CX
	MOVQ CX, blockbegin-80(SP)
	MOVQ 24(AX), CX
	ADDQ R9, CX
	MOVQ CX, blockend-40(SP)
	MOVQ 32(AX), CX
	LEAQ GSEQ_OFF(DI)(CX*8), CX
	MOVQ CX, seqp-8(SP)
	LEAQ GSEQ_END(DI), CX
	MOVQ CX, seqend-48(SP)
	MOVL 40(AX), R11
	ANDL $0x7FFF, R11         // next_hashes[0] (masked: HCSEARCH258's entry condition)
	MOVL 44(AX), BP
	ANDL $0xFFFF, BP          // next_hashes[1]
	MOVL 48(AX), CX
	MOVL CX, minlen-56(SP)
	MOVL 52(AX), CX
	MOVL CX, nicelen-64(SP)
	MOVL 56(AX), CX
	MOVL CX, depth-60(SP)

gstep:
	CMPQ BX, limit-32(SP)
	JHI  gdone                // fewer than DEFLATE_MAX_MATCH_LEN bytes remain
	MOVL minlen-56(SP), R10
	DECL R10                  // best_len = min_len - 1
	MOVL depth-60(SP), R12
	HCSEARCH258(nicelen-64(SP), gns, gsl, gc4, gl4, ghv, gnb, gl5, gn5, gcd, ge8, ge1, gd24, gd16, gd8, gdd, ged, got)

	// length >= min_len && (length > DEFLATE_MIN_MATCH_LEN || offset <= 4096)
	CMPL R10, minlen-56(SP)
	JB   gliteral
	MOVQ BX, AX
	SUBQ R8, AX               // offset
	CMPL R10, $3
	JA   gmatch
	CMPQ AX, $4096
	JA   gliteral

gmatch:
	// deflate_choose_match(c, length, offset, true, &seq)
	MOVL    $256, CX
	SUBL    AX, CX
	SHRL    $29, CX           // n = (256 - offset) >> 29
	LEAL    -1(AX), DX
	SHRL    CX, DX
	LEAQ    ·deflateOffsetSlot(SB), R9
	MOVBLZX (R9)(DX*1), DX
	LEAL    (DX)(CX*2), DX    // offset_slot
	MOVQ    freqs-16(SP), R9
	LEAQ    ·deflateLengthSlot(SB), CX
	MOVBLZX (CX)(R10*1), CX
	INCL    FREQ_LEN(R9)(CX*4) // litlen[DEFLATE_FIRST_LEN_SYM + length_slot]++
	INCL    FREQ_OFF(R9)(DX*4) // offset[offset_slot]++
	MOVQ    stats-24(SP), R9
	CMPL    R10, $9
	SBBL    CX, CX
	INCL    CX                // length >= 9
	INCL    OBS_MATCH(R9)(CX*4) // observe_match
	INCL    OBS_NUM(R9)
	MOVQ    seqp-8(SP), R9
	MOVL    R10, CX
	SHLL    $23, CX
	ORL     CX, (R9)          // litrunlen_and_length |= length << SEQ_LENGTH_SHIFT
	MOVW    AX, 4(R9)
	MOVW    DX, 6(R9)
	ADDQ    $8, R9            // seq++
	MOVL    $0, (R9)
	MOVQ    R9, seqp-8(SP)

	// hc_matchfinder_skip_bytes(in_next + 1, length - 1)
	LEAQ 5(BX)(R10*1), CX
	CMPQ CX, inend-88(SP)
	JHI  gskipdone            // count + 5 > in_end - (in_next + 1)
	LEAQ 1(BX), CX
	SUBQ R13, CX              // cur_pos
	LEAQ 2(BX), SI            // the bytes hashed next
	LEAL -1(R10), R12         // count

gskiploop:
	CMPL CX, $0x8000
	JNE  gskipns
	HCSLIDE(gslide)
	ADDQ $0x8000, R13
	XORL CX, CX

gskipns:
	MOVW   CX, (DI)(R11*2)    // hash3_tab[hash3] = cur_pos
	MOVW   HASH4_OFF(DI)(BP*2), AX
	MOVL   CX, DX
	ANDL   $0x7FFF, DX
	MOVW   AX, NEXT_OFF(DI)(DX*2) // next_tab[cur_pos] = hash4_tab[hash4]
	MOVW   CX, HASH4_OFF(DI)(BP*2) // hash4_tab[hash4] = cur_pos
	MOVL   (SI), R14
	INCQ   SI
	MOVL   R14, BP
	ANDL   $0xFFFFFF, R14
	IMUL3L $0x1E35A7BD, R14, R11
	IMUL3L $0x1E35A7BD, BP, BP
	SHRL   $17, R11
	SHRL   $16, BP
	INCL   CX
	DECL   R12
	JNZ    gskiploop

gskipdone:
	ADDQ R10, BX              // in_next += length
	JMP  gendcheck

gliteral:
	// deflate_choose_literal(c, *in_next++, true, seq)
	MOVBLZX (BX), AX
	MOVQ    freqs-16(SP), R9
	INCL    (R9)(AX*4)
	MOVL    AX, CX
	SHRL    $5, CX
	ANDL    $6, CX
	ANDL    $1, AX
	ORL     AX, CX            // ((lit >> 5) & 0x6) | (lit & 1)
	MOVQ    stats-24(SP), R9
	INCL    (R9)(CX*4)        // observe_literal
	INCL    OBS_NUM(R9)
	MOVQ    seqp-8(SP), R9
	INCL    (R9)
	INCQ    BX

gendcheck:
	CMPQ BX, blockend-40(SP)
	JAE  gdone
	MOVQ seqp-8(SP), R9
	CMPQ R9, seqend-48(SP)
	JAE  gdone
	// ready_to_check_block(&c->split_stats, in_block_begin, in_next, in_end)
	MOVQ stats-24(SP), R9
	CMPL OBS_NUM(R9), $512
	JB   gstep
	MOVQ BX, CX
	SUBQ blockbegin-80(SP), CX
	CMPQ CX, $5000
	JLT  gstep
	MOVQ inend-88(SP), CX
	SUBQ BX, CX
	CMPQ CX, $5000
	JLT  gstep

gdone:
	MOVQ in_base+24(FP), R9
	MOVQ st+48(FP), AX
	SUBQ R9, BX
	MOVQ BX, 0(AX)
	SUBQ R9, R13
	MOVQ R13, 8(AX)
	MOVQ seqp-8(SP), CX
	LEAQ GSEQ_OFF(DI), DX
	SUBQ DX, CX
	SHRQ $3, CX
	MOVQ CX, 32(AX)
	MOVL R11, 40(AX)
	MOVL BP, 44(AX)
	RET

// The lazy loop's frame slots (seqp, freqs and stats at the greedy loop's
// offsets, which the L* macros use).
// LSEARCH(labels...): the search (HCSEARCH, its label arguments) with max_len,
// nice_len and next_hashes from the frame (R10 best_len and R12 depth set by
// the caller).
#define LSEARCH(ns, sl, c4, l4, hv, nb, l5, n5, cd, e8, e1, d24, d16, d8, dd, ed, ot) \
	HCSEARCH(maxlen-80(SP), nicelen-84(SP), ns, sl, c4, l4, hv, nb, l5, n5, cd, e8, e1, d24, d16, d8, dd, ed, ot)

// LADJUST: adjust_max_and_nice_len(max_len, nice_len, in_end - in_next).
#define LADJUST(lbl) \
	MOVQ inend-32(SP), CX; \
	SUBQ BX, CX; \
	CMPQ CX, $258; \
	JGE  lbl; \
	MOVL CX, maxlen-80(SP); \
	CMPL nicelen-84(SP), CX; \
	JBE  lbl; \
	MOVL CX, nicelen-84(SP); \
lbl:

// LLIT(off): deflate_choose_literal(c, in_next[off], true, seq); clobbers AX,
// CX, R9.
#define LLIT(off) \
	MOVBLZX off(BX), AX; \
	MOVQ    freqs-16(SP), R9; \
	INCL    (R9)(AX*4); \
	MOVL    AX, CX; \
	SHRL    $5, CX; \
	ANDL    $6, CX; \
	ANDL    $1, AX; \
	ORL     AX, CX; \
	MOVQ    stats-24(SP), R9; \
	INCL    (R9)(CX*4); \
	INCL    OBS_NUM(R9); \
	MOVQ    seqp-8(SP), R9; \
	INCL    (R9)

// LMATCH: deflate_choose_match(c, R10 length, AX offset, true, &seq);
// clobbers CX, DX, R9.
#define LMATCH \
	MOVL    $256, CX; \
	SUBL    AX, CX; \
	SHRL    $29, CX; \
	LEAL    -1(AX), DX; \
	SHRL    CX, DX; \
	LEAQ    ·deflateOffsetSlot(SB), R9; \
	MOVBLZX (R9)(DX*1), DX; \
	LEAL    (DX)(CX*2), DX; \
	MOVQ    freqs-16(SP), R9; \
	LEAQ    ·deflateLengthSlot(SB), CX; \
	MOVBLZX (CX)(R10*1), CX; \
	INCL    FREQ_LEN(R9)(CX*4); \
	INCL    FREQ_OFF(R9)(DX*4); \
	MOVQ    stats-24(SP), R9; \
	CMPL    R10, $9; \
	SBBL    CX, CX; \
	INCL    CX; \
	INCL    OBS_MATCH(R9)(CX*4); \
	INCL    OBS_NUM(R9); \
	MOVQ    seqp-8(SP), R9; \
	MOVL    R10, CX; \
	SHLL    $23, CX; \
	ORL     CX, (R9); \
	MOVW    AX, 4(R9); \
	MOVW    DX, 6(R9); \
	ADDQ    $8, R9; \
	MOVL    $0, (R9); \
	MOVQ    R9, seqp-8(SP)

// LBETTER(thr, nl): falls through if the match (R10 length, AX offset) found
// after the current one (curlen, curoff) is better: next_len >= cur_len &&
// 4 * (next_len - cur_len) + (bsr32(cur_offset) - bsr32(next_offset)) > thr
// (the offsets are not 0 then); else jumps to nl. Clobbers CX, DX, R9.
#define LBETTER(thr, nl) \
	MOVL curlen-96(SP), CX; \
	CMPL R10, CX; \
	JB   nl; \
	MOVL R10, DX; \
	SUBL CX, DX; \
	SHLL $2, DX; \
	MOVQ curoff-104(SP), R9; \
	BSRL R9, R9; \
	ADDL R9, DX; \
	BSRL AX, R9; \
	SUBL R9, DX; \
	CMPL DX, $thr; \
	JLE  nl

// LSKIP(lp, sl, ns, dn): hc_matchfinder_skip_bytes(in_next, R12 count >= 1)
// (nothing if count + 5 > in_end - in_next), then in_next += count; clobbers
// AX, CX, DX, SI, R9, R12, R14, R15, X0-X4.
#define LSKIP(lp, sl, ns, dn) \
	MOVL   R12, cnt-108(SP); \
	LEAQ   5(BX)(R12*1), CX; \
	CMPQ   CX, inend-32(SP); \
	JHI    dn; \
	MOVQ   BX, CX; \
	SUBQ   R13, CX; \
	LEAQ   1(BX), SI; \
lp: \
	CMPL   CX, $0x8000; \
	JNE    ns; \
	MOVL   $0x80008000, DX; \
	MOVQ   DX, X4; \
	PSHUFD $0, X4, X4; \
	MOVQ   DI, DX; \
	MOVL   $4096, R9; \
sl: \
	MOVOU  (DX), X0; \
	MOVOU  16(DX), X1; \
	MOVOU  32(DX), X2; \
	MOVOU  48(DX), X3; \
	PADDSW X4, X0; \
	PADDSW X4, X1; \
	PADDSW X4, X2; \
	PADDSW X4, X3; \
	MOVOU  X0, (DX); \
	MOVOU  X1, 16(DX); \
	MOVOU  X2, 32(DX); \
	MOVOU  X3, 48(DX); \
	ADDQ   $64, DX; \
	DECL   R9; \
	JNZ    sl; \
	ADDQ   $0x8000, R13; \
	XORL   CX, CX; \
ns: \
	MOVW   CX, (DI)(R11*2); \
	MOVW   HASH4_OFF(DI)(BP*2), AX; \
	MOVL   CX, DX; \
	ANDL   $0x7FFF, DX; \
	MOVW   AX, NEXT_OFF(DI)(DX*2); \
	MOVW   CX, HASH4_OFF(DI)(BP*2); \
	MOVL   (SI), R14; \
	INCQ   SI; \
	MOVL   R14, BP; \
	ANDL   $0xFFFFFF, R14; \
	IMUL3L $0x1E35A7BD, R14, R11; \
	IMUL3L $0x1E35A7BD, BP, BP; \
	SHRL   $17, R11; \
	SHRL   $16, BP; \
	INCL   CX; \
	DECL   R12; \
	JNZ    lp; \
dn: \
	MOVL   cnt-108(SP), CX; \
	ADDQ   CX, BX

// func lazyLoopAsm(pg *greedyState, freqs *deflateFreqs, stats *blockSplitStats, in []byte,
//	st *lazyLoopState)
//
// The per-position loop of deflate_compress_lazy_generic (lazy and lazy2):
// the search at the current position (best_len min_len - 1, max_search_depth),
// the literal choice (no match, or a length 3 match farther than 8192), and
// with a match the have_cur_match loop: nice-length matches taken at once,
// else the look-ahead searches (depth >> 1, and >> 2 for lazy2) with the
// "better match" rule, literals for the skipped positions, the chosen match
// and the skip (length - 1, - 2 or - 3). adjust_max_and_nice_len before every
// search (st.maxLen / st.niceLen are updated). It returns at the start of a
// position with in_next >= st.nextRecalcMinLen (the Go loop recalculates
// min_len) and after the first position at which in_next >= in_max_block_end,
// seq reaches SEQ_STORE_LENGTH or ready_to_check_block holds (the Go loop
// then evaluates should_end_block). Semantics are those of the Go loop of
// deflateCompressLazyGeneric. Registers across positions: DI pg, BX in_next,
// R13 in_cur_base, R11 / BP next_hashes[0] / [1] (BP a general register,
// not a frame pointer: see the file header); the rest of the state is in the
// frame (max_len / nice_len are the searches' memory operands).
//
// BOUNDS: preconditions (kept by deflateCompressLazyGeneric): st.inNext <
// st.nextRecalcMinLen; 0 <= st.inNext - st.inCurBase <= 32768;
// st.inBlockBegin <= st.inNext < st.inMaxBlockEnd <= len(in); 0 <= st.seq <
// 50000; 1 <= st.minLen; st.niceLen <= st.maxLen <= 258; st.maxLen <=
// len(in) - st.inNext or st.maxLen = 258; the matchfinder invariant of
// hcLongestMatchAsmForTest's BOUNDS; st.nextHash3 / st.nextHash4 are masked
// on entry as in greedyLoopAsm. Each search runs after LADJUST, so max_len <=
// len(in) - in_next and nice_len <= max_len (the search's preconditions;
// hcLongestMatchAsmForTest's BOUNDS give the accesses of each LSEARCH
// expansion). A look-ahead search is at most at in_next+2 with a current
// match of length >= 3 <= max_len before it, so in_next < len(in) there.
// Other accesses: a literal reads in[in_next-3 .. in_next] (positions already
// passed); a skip of count bytes (only if in_next + count + 5 <= len(in))
// reads the 4 bytes at in_next+1+i, i < count, all before in_next+count+4 <
// len(in), and writes hash3Tab/hash4Tab/nextTab at masked indexes (a slide
// rebases bytes [0, 262144) of pg); sequences[seq] and [seq+1] (seq+1 <=
// 50000, the last index of the 50001-entry array: the loop stops once seq
// reaches 50000; one position adds at most one match); freqs.litlen[0..255],
// litlen[257+length_slot <= 285], offset[offset_slot <= 29];
// stats.new_observations[0..9] and num_new_observations; the slot tables; st;
// the 112-byte frame.
// C: deflate_compress_lazy_generic (the inner loop of a block)
TEXT ·lazyLoopAsm(SB), NOSPLIT, $112-56
	MOVQ pg+0(FP), DI
	MOVQ freqs+8(FP), AX
	MOVQ AX, freqs-16(SP)
	MOVQ stats+16(FP), AX
	MOVQ AX, stats-24(SP)
	MOVQ in_base+24(FP), R9
	MOVQ in_len+32(FP), CX
	LEAQ (R9)(CX*1), DX
	MOVQ DX, inend-32(SP)
	MOVQ st+48(FP), AX
	MOVQ 0(AX), BX
	ADDQ R9, BX
	MOVQ 8(AX), R13
	ADDQ R9, R13
	MOVQ 16(AX), CX
	ADDQ R9, CX
	MOVQ CX, blockbegin-56(SP)
	MOVQ 24(AX), CX
	ADDQ R9, CX
	MOVQ CX, blockend-40(SP)
	MOVQ 32(AX), CX
	ADDQ R9, CX
	MOVQ CX, recalc-64(SP)
	MOVQ 40(AX), CX
	LEAQ GSEQ_OFF(DI)(CX*8), CX
	MOVQ CX, seqp-8(SP)
	LEAQ GSEQ_END(DI), CX
	MOVQ CX, seqend-48(SP)
	MOVL 48(AX), R11
	ANDL $0x7FFF, R11         // next_hashes[0] (masked: HCSEARCH's entry condition)
	MOVL 52(AX), BP
	ANDL $0xFFFF, BP          // next_hashes[1]
	MOVL 56(AX), CX
	MOVL CX, minlen-76(SP)
	MOVL 60(AX), CX
	MOVL CX, maxlen-80(SP)
	MOVL 64(AX), CX
	MOVL CX, nicelen-84(SP)
	MOVL 68(AX), CX
	MOVL CX, depth-88(SP)
	MOVL 72(AX), CX
	MOVL CX, lazy2-92(SP)

lstep:
	CMPQ BX, recalc-64(SP)
	JAE  ldone                // time to recalculate min_len (in Go)
	LADJUST(ladj1)
	MOVL minlen-76(SP), R10
	DECL R10                  // best_len = min_len - 1
	MOVL depth-88(SP), R12
	LSEARCH(s1ns, s1sl, s1c4, s1l4, s1hv, s1nb, s1l5, s1n5, s1cd, s1e8, s1e1, s1d24, s1d16, s1d8, s1dd, s1ed, s1ot)
	CMPL R10, minlen-76(SP)
	JB   llit                 // cur_len < min_len
	MOVQ BX, AX
	SUBQ R8, AX               // cur_offset
	CMPL R10, $3
	JNE  lhave
	CMPQ AX, $8192
	JA   llit                 // cur_len == DEFLATE_MIN_MATCH_LEN && cur_offset > 8192

lhave:
	MOVL R10, curlen-96(SP)
	MOVQ AX, curoff-104(SP)
	INCQ BX

lhloop:
	// have_cur_match: the current match (curlen, curoff) is at in_next - 1.
	MOVL curlen-96(SP), R10
	CMPL R10, nicelen-84(SP)
	JAE  lnice                // very long: choose it immediately
	LADJUST(ladj2)
	MOVL curlen-96(SP), R10
	DECL R10
	MOVL depth-88(SP), R12
	SHRL $1, R12              // max_search_depth >> 1
	LSEARCH(s2ns, s2sl, s2c4, s2l4, s2hv, s2nb, s2l5, s2n5, s2cd, s2e8, s2e1, s2d24, s2d16, s2d8, s2dd, s2ed, s2ot)
	MOVQ BX, AX
	SUBQ R8, AX               // next_offset
	INCQ BX
	LBETTER(2, lnot1)
	MOVL R10, curlen-96(SP)   // the next match becomes the current one
	MOVQ AX, curoff-104(SP)
	LLIT(-2)                  // a better match at the next position
	JMP  lhloop

lnot1:
	CMPL lazy2-92(SP), $0
	JEQ  lnolazy2
	LADJUST(ladj3)
	MOVL curlen-96(SP), R10
	DECL R10
	MOVL depth-88(SP), R12
	SHRL $2, R12              // max_search_depth >> 2
	LSEARCH(s3ns, s3sl, s3c4, s3l4, s3hv, s3nb, s3l5, s3n5, s3cd, s3e8, s3e1, s3d24, s3d16, s3d8, s3dd, s3ed, s3ot)
	MOVQ BX, AX
	SUBQ R8, AX
	INCQ BX
	LBETTER(6, lnot2)
	MOVL R10, curlen-96(SP)
	MOVQ AX, curoff-104(SP)
	LLIT(-3)                  // a much better match two positions ahead
	LLIT(-2)
	JMP  lhloop

lnot2:
	MOVL curlen-96(SP), R10
	MOVQ curoff-104(SP), AX
	LMATCH
	CMPL R10, $3
	JBE  lend
	LEAL -3(R10), R12
	LSKIP(lsk1, lsl1, lns1, ldn1)
	JMP  lend

lnolazy2:
	MOVL curlen-96(SP), R10
	MOVQ curoff-104(SP), AX
	LMATCH
	LEAL -2(R10), R12
	LSKIP(lsk2, lsl2, lns2, ldn2)
	JMP  lend

lnice:
	MOVQ curoff-104(SP), AX
	LMATCH
	LEAL -1(R10), R12
	LSKIP(lsk3, lsl3, lns3, ldn3)
	JMP  lend

llit:
	LLIT(0)
	INCQ BX

lend:
	CMPQ BX, blockend-40(SP)
	JAE  ldone
	MOVQ seqp-8(SP), R9
	CMPQ R9, seqend-48(SP)
	JAE  ldone
	// ready_to_check_block(&c->split_stats, in_block_begin, in_next, in_end)
	MOVQ stats-24(SP), R9
	CMPL OBS_NUM(R9), $512
	JB   lstep
	MOVQ BX, CX
	SUBQ blockbegin-56(SP), CX
	CMPQ CX, $5000
	JLT  lstep
	MOVQ inend-32(SP), CX
	SUBQ BX, CX
	CMPQ CX, $5000
	JLT  lstep

ldone:
	MOVQ in_base+24(FP), R9
	MOVQ st+48(FP), AX
	SUBQ R9, BX
	MOVQ BX, 0(AX)
	SUBQ R9, R13
	MOVQ R13, 8(AX)
	MOVQ seqp-8(SP), CX
	LEAQ GSEQ_OFF(DI), DX
	SUBQ DX, CX
	SHRQ $3, CX
	MOVQ CX, 40(AX)
	MOVL R11, 48(AX)
	MOVL BP, 52(AX)
	MOVL maxlen-80(SP), CX
	MOVL CX, 60(AX)
	MOVL nicelen-84(SP), CX
	MOVL CX, 64(AX)
	RET
