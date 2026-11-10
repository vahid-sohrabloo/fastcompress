//go:build amd64 && !appengine && !noasm && !purego && gc

// The matchfinding loop of deflate_compress_near_optimal (libdeflate v1.26
// lib/deflate_compress.c, Copyright 2016 Eric Biggers, MIT; see NOTICE) with
// bt_matchfinder_get_matches and bt_matchfinder_skip_byte
// (bt_matchfinder_advance_one_byte, lib/bt_matchfinder.h) and lz_extend
// (lib/matchfinder_common.h) written in, as gcc -O2 forceinlines them into
// deflate_compress_near_optimal. The prefetchw calls are PREFETCHT0, the
// instruction libdeflate's x86-64 build emits for them (no -mprfchw).
// Semantics are those of the Go loop of deflateCompressNearOptimal with
// btMatchfinder.getMatches and skipByte (the Go reference, statement for
// statement).
//
// BP: btMatchLoopAsm uses BP as a general register (depth_remaining). Its
// frame makes the assembler save the caller's BP on entry and restore it at
// RET, so Go code never sees a changed BP; but while it runs BP is not a frame
// pointer, and a frame-pointer unwinder (perf, the execution tracer) that
// samples inside it sees a bad stack.
//
// Preemption: the routine is NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside it. Bound per call: positions
// start only below st.limit, which deflateCompressNearOptimal sets at most
// btMatchLoopMaxBytes (4096) input bytes past st.inNext, so a call advances
// in_next by at most 4096 + 257 bytes (the last position's skipped bytes),
// and each position or skipped byte walks at most max_search_depth tree nodes.
//
// Registers across positions: DI ns (the matchfinder at offset 0), BX in_next,
// R13 in_base (&in[in_cur_base]), R8 the match cache write pointer; the rest
// of the state is in the frame. Registers of the tree walk (the same in both
// copies):
//   R15 cutoff (cur_pos - MATCHFINDER_WINDOW_SIZE)   AX  cur_node (sign-ext.)
//   R14 pending_lt_ptr     R9  pending_gt_ptr        BP  depth_remaining
//   SI  best_lt_len        DX  best_gt_len           R11 len
//   R10 best_len (get_matches) or nice_len (skip_byte)
//   CX  matchptr           R12 scratch (lz_extend word, bytes)

#include "textflag.h"

// nearOptimalState byte offsets (asserted in bt_matchfinder_amd64.go): the
// matchfinder's hash4_tab and child_tab, match_cache[0] and
// match_cache[MATCH_CACHE_LENGTH], new_match_len_freqs. blockSplitStats:
// new_observations[NUM_LITERAL_OBSERVATION_TYPES] and num_new_observations.
#define HASH4_OFF 262144
#define CHILD_OFF 393216
#define CACHE_OFF 524288
#define CACHE_END 6524288
#define NMLF_OFF 9003532
#define OBS_MATCH 32
#define OBS_NUM 80

// LZEXT(END, labels...): lz_extend(in_next = BX, matchptr = CX, start_len =
// R11, max_len = END) into R11 (END an immediate or a register other than
// R11 / R12; clobbers R12): 32 bytes at a time while max_len - len >= 32,
// then 8, then 1; its TZCNTQ runs only on a nonzero XOR word, for which TZCNT
// (REP BSF) and BSF give the same result, so the encoding is correct on CPUs
// without BMI1 too. The labels are those of one expansion: e8 / e1 the
// 8-byte and 1-byte steps, d24 / d16 / d8 / dd the first differing word, ed
// the end. It reads 8 bytes at CX+n and BX+n only while n+8 <= END, then
// single bytes at n < END.
#define LZEXT(END, e8, e1, d24, d16, d8, dd, ed) \
	MOVL END, R12; \
	SUBL R11, R12; \
	CMPL R12, $32; \
	JB   e8; \
	MOVQ (CX)(R11*1), R12; \
	XORQ (BX)(R11*1), R12; \
	JNZ  dd; \
	MOVQ 8(CX)(R11*1), R12; \
	XORQ 8(BX)(R11*1), R12; \
	JNZ  d8; \
	MOVQ 16(CX)(R11*1), R12; \
	XORQ 16(BX)(R11*1), R12; \
	JNZ  d16; \
	MOVQ 24(CX)(R11*1), R12; \
	XORQ 24(BX)(R11*1), R12; \
	JNZ  d24; \
	ADDL $32, R11; \
e8: \
	LEAL 8(R11), R12; \
	CMPL R12, END; \
	JA   e1; \
	MOVQ (CX)(R11*1), R12; \
	XORQ (BX)(R11*1), R12; \
	JNZ  dd; \
	ADDL $8, R11; \
	JMP  e8; \
e1: \
	CMPL    R11, END; \
	JAE     ed; \
	MOVBLZX (CX)(R11*1), R12; \
	CMPB    R12, (BX)(R11*1); \
	JNE     ed; \
	INCL    R11; \
	JMP     e1; \
d24: \
	ADDL $8, R11; \
d16: \
	ADDL $8, R11; \
d8: \
	ADDL $8, R11; \
dd: \
	TZCNTQ R12, R12; \
	SHRL   $3, R12; \
	ADDL   R12, R11; \
ed:

// ADVANCE: the start of bt_matchfinder_advance_one_byte for the position
// at BX: cur_pos into CX, the cutoff into R15, hash3 / hash4 (the stored next
// hashes, masked to their tables) into SI / DX, and the next position's
// hashes into the frame with their prefetches. Clobbers R9, R10.
#define ADVANCE \
	MOVQ   BX, CX; \
	SUBQ   R13, CX; \
	LEAQ   -0x8000(CX), R15; \
	MOVL   1(BX), R9; \
	MOVL   h3-68(SP), SI; \
	ANDL   $0xFFFF, SI; \
	MOVL   h4-72(SP), DX; \
	ANDL   $0xFFFF, DX; \
	MOVL   R9, R10; \
	ANDL   $0xFFFFFF, R10; \
	IMUL3L $0x1E35A7BD, R10, R10; \
	IMUL3L $0x1E35A7BD, R9, R9; \
	SHRL   $16, R10; \
	SHRL   $16, R9; \
	MOVL   R10, h3-68(SP); \
	MOVL   R9, h4-72(SP); \
	PREFETCHT0 (DI)(R10*4); \
	PREFETCHT0 HASH4_OFF(DI)(R9*2)

// WALKSTEP(gt, nx, ed): the lt / gt step of the tree walk after the compare
// of matchptr[len] (R12) with in_next[len] (the flags of that CMPB): it
// re-links the node, moves to its child, updates best_lt_len / best_gt_len and
// len, then ends the walk at ed if the child is <= cutoff or the depth is
// used up. gt and nx are labels of one expansion.
#define WALKSTEP(gt, nx, ed) \
	JAE     gt; \
	MOVW    AX, (R14); \
	ANDL    $0x7FFF, AX; \
	LEAQ    CHILD_OFF+2(DI)(AX*4), R14; \
	MOVWQSX (R14), AX; \
	MOVL    R11, SI; \
	CMPL    DX, R11; \
	CMOVLCS DX, R11; \
	JMP     nx; \
gt: \
	MOVW    AX, (R9); \
	ANDL    $0x7FFF, AX; \
	LEAQ    CHILD_OFF(DI)(AX*4), R9; \
	MOVWQSX (R9), AX; \
	MOVL    R11, DX; \
	CMPL    SI, R11; \
	CMOVLCS SI, R11; \
nx: \
	CMPQ AX, R15; \
	JLE  ed; \
	DECL BP

// func btMatchLoopAsm(ns *nearOptimalState, stats *blockSplitStats, in []byte, st *btMatchLoopState)
//
// The matchfinding loop of a block of deflate_compress_near_optimal: per
// position bt_matchfinder_get_matches (max_len 258, nice_len, max_search_depth)
// into the match cache, the literal / match observation with the minimum
// match length heuristic, the cache header, and bt_matchfinder_skip_byte for
// the bytes covered by a match of at least nice_len (each with a header of no
// matches); it returns after the first position at which in_next >=
// in_max_block_end, the cache reaches MATCH_CACHE_LENGTH or
// ready_to_check_block holds (the Go loop then runs do_end_block_check), or
// at st.limit (checked before each position). Semantics are those of the Go
// loop of deflateCompressNearOptimal.
//
// BOUNDS: preconditions (kept by deflateCompressNearOptimal): st.inNext <
// st.limit <= len(in) - 514 (so every position and skipped byte p of a call
// has len(in) - p >= 258, max_len = 258 and nice_len unchanged), st.limit
// <= st.inCurBase + 32768 - 257 (so 0 <= cur_pos < 32768 at every position and
// skipped byte: no window slide), 0 <= st.inCurBase <= st.inNext;
// st.inBlockBegin <= st.inNext < st.inMaxBlockEnd; 0 <= st.cachePtr <
// MATCH_CACHE_LENGTH; 3 <= st.niceLen <= 258; st.minLen <= 259;
// st.maxSearchDepth >= 1; every hash3Tab/hash4Tab/childTab entry reachable
// from the buckets that is > cutoff is a position p (relative to in_base) of
// this buffer with in <= in_base + p < in_next.
// Memory accesses:
// * ns.btMf: hash3Tab[2h], [2h+1] and hash4Tab[h] (read, written) with h the
//   stored next hashes masked to 16 bits; childTab[2 (cur_pos & 0x7FFF)] and
//   [+1] (written), childTab[2 (node & 0x7FFF)] and [+1] (read, written):
//   every index inside its table. The two PREFETCHT0 touch hash3Tab[2h3] /
//   hash4Tab[h4] with h3, h4 < 2^16 (lz_hash results); a prefetch never
//   faults anyway.
// * in: the 4 bytes at in_next+1 (< in_next+258); 4 bytes at in_next and at
//   a hash3 candidate m (m < in_next); for a tree candidate m = in_base + node
//   (node > cutoff, so in <= m < in_next): bytes m+len and in_next+len with
//   len < nice_len <= 258, lz_extend's words at m+n and in_next+n with n+8 <=
//   max_len (258, or nice_len in skip_byte) and bytes at n < max_len; a
//   literal / header reads in[in_next]. So in is read only in [in, in_next +
//   258) inside in.
// * ns.matchCache: from match_cache[st.cachePtr] on, at most 256 matches and
//   a header per position, one header per skipped byte (at most 257), and the
//   loop stops once MATCH_CACHE_LENGTH is reached: the last entry written is
//   below MATCH_CACHE_LENGTH + MAX_MATCHES_PER_POS + DEFLATE_MAX_MATCH_LEN - 1,
//   the array's length.
// * ns.newMatchLenFreqs[best_len] (best_len <= 258, 259 entries);
//   stats.new_observations[0..9] and num_new_observations; st; the 96-byte
//   frame.
// C: deflate_compress_near_optimal (the matchfinding loop of a block, with
// bt_matchfinder_get_matches, bt_matchfinder_skip_byte and lz_extend)
TEXT ·btMatchLoopAsm(SB), NOSPLIT, $96-48
	MOVQ ns+0(FP), DI
	MOVQ stats+8(FP), AX
	MOVQ AX, stats-64(SP)
	MOVQ in_base+16(FP), R9
	MOVQ in_len+24(FP), CX
	LEAQ (R9)(CX*1), DX
	MOVQ DX, inend-48(SP)
	MOVQ st+40(FP), AX
	MOVQ 0(AX), BX
	ADDQ R9, BX
	MOVQ 8(AX), R13
	ADDQ R9, R13
	MOVQ 16(AX), CX
	ADDQ R9, CX
	MOVQ CX, limit-24(SP)
	MOVQ 24(AX), CX
	ADDQ R9, CX
	MOVQ CX, blockbegin-40(SP)
	MOVQ 32(AX), CX
	ADDQ R9, CX
	MOVQ CX, blockend-32(SP)
	MOVQ 40(AX), CX
	ADDQ R9, CX
	MOVQ CX, nextobs-16(SP)
	MOVQ 48(AX), CX
	LEAQ CACHE_OFF(DI)(CX*4), R8
	LEAQ CACHE_END(DI), CX
	MOVQ CX, cacheend-56(SP)
	MOVL 56(AX), CX
	MOVL CX, h3-68(SP)
	MOVL 60(AX), CX
	MOVL CX, h4-72(SP)
	MOVL 64(AX), CX
	MOVL CX, minlen-84(SP)
	MOVL 68(AX), CX
	MOVL CX, nicelen-76(SP)
	MOVL 72(AX), CX
	MOVL CX, depth-80(SP)

bpos:
	CMPQ BX, limit-24(SP)
	JAE  bdone
	MOVQ R8, mstart-8(SP)

	// bt_matchfinder_get_matches(..., in_next - in_base, 258, nice_len, ...)
	ADVANCE
	MOVWQSX (DI)(SI*4), AX    // cur_node = hash3_tab[hash3][0]
	MOVWQSX 2(DI)(SI*4), R11  // cur_node_2 = hash3_tab[hash3][1]
	MOVW    CX, (DI)(SI*4)
	MOVW    AX, 2(DI)(SI*4)
	MOVL    $3, R10           // best_len
	CMPQ    AX, R15
	JLE     b3done
	MOVL    (BX), R9
	ANDL    $0xFFFFFF, R9     // seq3
	MOVL    (R13)(AX*1), R12
	ANDL    $0xFFFFFF, R12
	CMPL    R12, R9
	JNE     b3second
	MOVL    CX, R12
	SUBL    AX, R12
	JMP     b3rec

b3second:
	CMPQ R11, R15
	JLE  b3done
	MOVL (R13)(R11*1), R12
	ANDL $0xFFFFFF, R12
	CMPL R12, R9
	JNE  b3done
	MOVL CX, R12
	SUBL R11, R12

b3rec:
	SHLL $16, R12
	ORL  $3, R12
	MOVL R12, (R8)            // {3, cur_pos - node}
	ADDQ $4, R8

b3done:
	MOVWQSX HASH4_OFF(DI)(DX*2), AX // cur_node = hash4_tab[hash4]
	MOVW    CX, HASH4_OFF(DI)(DX*2)
	ANDL    $0x7FFF, CX
	LEAQ    CHILD_OFF(DI)(CX*4), R14 // pending_lt_ptr
	LEAQ    2(R14), R9               // pending_gt_ptr
	CMPQ    AX, R15
	JG      bwalk
	MOVW    $-32768, (R14)
	MOVW    $-32768, (R9)
	JMP     bfound

bwalk:
	XORL SI, SI               // best_lt_len
	XORL DX, DX               // best_gt_len
	XORL R11, R11             // len
	MOVL depth-80(SP), BP
	PCALIGN $32

bnode:
	LEAQ    (R13)(AX*1), CX   // matchptr
	MOVBLZX (CX)(R11*1), R12
	CMPB    R12, (BX)(R11*1)
	JNE     bcmp
	INCL    R11
	LZEXT($258, be8, be1, bd24, bd16, bd8, bdd, bed)
	CMPL    R11, R10
	JBE     bcmpld
	MOVL    R11, R10          // best_len = len
	MOVQ    BX, R12
	SUBQ    CX, R12
	SHLL    $16, R12
	ORL     R11, R12
	MOVL    R12, (R8)         // {len, in_next - matchptr}
	ADDQ    $4, R8
	CMPL    R11, nicelen-76(SP)
	JAE     bnice

bcmpld:
	MOVBLZX (CX)(R11*1), R12
	CMPB    R12, (BX)(R11*1)

bcmp:
	WALKSTEP(bgt, bnx, bend)
	JNZ bnode

bend:
	MOVW $-32768, (R14)
	MOVW $-32768, (R9)
	JMP  bfound

bnice:
	ANDL $0x7FFF, AX
	MOVW CHILD_OFF(DI)(AX*4), R12
	MOVW R12, (R14)
	MOVW CHILD_OFF+2(DI)(AX*4), R12
	MOVW R12, (R9)

bfound:
	// best_len = the length of the last match found, 0 if none
	MOVQ mstart-8(SP), CX
	CMPQ R8, CX
	JNE  bhave
	XORL R10, R10

bhave:
	CMPQ BX, nextobs-16(SP)
	JB   bhdr
	MOVQ stats-64(SP), R9
	CMPL R10, minlen-84(SP)
	JB   bobslit
	CMPL R10, $9
	SBBL R11, R11
	INCL R11                  // length >= 9
	INCL OBS_MATCH(R9)(R11*4) // observe_match
	INCL OBS_NUM(R9)
	LEAQ (BX)(R10*1), R11
	MOVQ R11, nextobs-16(SP)
	INCL NMLF_OFF(DI)(R10*4)  // new_match_len_freqs[best_len]++
	JMP  bhdr

bobslit:
	MOVBLZX (BX), R11
	MOVL    R11, R12
	SHRL    $5, R12
	ANDL    $6, R12
	ANDL    $1, R11
	ORL     R11, R12          // ((lit >> 5) & 0x6) | (lit & 1)
	INCL    (R9)(R12*4)       // observe_literal
	INCL    OBS_NUM(R9)
	LEAQ    1(BX), R11
	MOVQ    R11, nextobs-16(SP)

bhdr:
	MOVQ    R8, R11
	SUBQ    CX, R11
	SHRQ    $2, R11           // the number of matches
	MOVBLZX (BX), R12
	SHLL    $16, R12
	ORL     R11, R12
	MOVL    R12, (R8)         // {num_matches, *in_next}
	ADDQ    $4, R8
	INCQ    BX

	// best_len >= DEFLATE_MIN_MATCH_LEN && best_len >= nice_len: skip the
	// best_len - 1 bytes it covers
	CMPL R10, $3
	JB   bcheck
	CMPL R10, nicelen-76(SP)
	JB   bcheck
	DECL R10
	MOVL R10, skipcnt-88(SP)

bskip:
	// bt_matchfinder_skip_byte(..., in_next - in_base, nice_len, ...)
	ADVANCE
	MOVW    (DI)(SI*4), R12
	MOVW    R12, 2(DI)(SI*4)  // hash3_tab[hash3][1] = hash3_tab[hash3][0]
	MOVW    CX, (DI)(SI*4)
	MOVWQSX HASH4_OFF(DI)(DX*2), AX
	MOVW    CX, HASH4_OFF(DI)(DX*2)
	ANDL    $0x7FFF, CX
	LEAQ    CHILD_OFF(DI)(CX*4), R14
	LEAQ    2(R14), R9
	CMPQ    AX, R15
	JG      swalk
	MOVW    $-32768, (R14)
	MOVW    $-32768, (R9)
	JMP     snext

swalk:
	XORL SI, SI
	XORL DX, DX
	XORL R11, R11
	MOVL depth-80(SP), BP
	MOVL nicelen-76(SP), R10
	PCALIGN $32

snode:
	LEAQ    (R13)(AX*1), CX
	MOVBLZX (CX)(R11*1), R12
	CMPB    R12, (BX)(R11*1)
	JNE     scmp
	INCL    R11
	LZEXT(R10, se8, se1, sd24, sd16, sd8, sdd, sed)
	CMPL    R11, R10
	JAE     snice
	MOVBLZX (CX)(R11*1), R12
	CMPB    R12, (BX)(R11*1)

scmp:
	WALKSTEP(sgt, snx, send)
	JNZ snode

send:
	MOVW $-32768, (R14)
	MOVW $-32768, (R9)
	JMP  snext

snice:
	ANDL $0x7FFF, AX
	MOVW CHILD_OFF(DI)(AX*4), R12
	MOVW R12, (R14)
	MOVW CHILD_OFF+2(DI)(AX*4), R12
	MOVW R12, (R9)

snext:
	MOVBLZX (BX), R12
	SHLL    $16, R12
	MOVL    R12, (R8)         // {0, *in_next}
	ADDQ    $4, R8
	INCQ    BX
	DECL    skipcnt-88(SP)
	JNZ     bskip

bcheck:
	CMPQ BX, blockend-32(SP)
	JAE  bdone
	CMPQ R8, cacheend-56(SP)
	JAE  bdone
	// ready_to_check_block(&c->split_stats, in_block_begin, in_next, in_end)
	MOVQ stats-64(SP), R9
	CMPL OBS_NUM(R9), $512
	JB   bpos
	MOVQ BX, R11
	SUBQ blockbegin-40(SP), R11
	CMPQ R11, $5000
	JLT  bpos
	MOVQ inend-48(SP), R11
	SUBQ BX, R11
	CMPQ R11, $5000
	JLT  bpos

bdone:
	MOVQ in_base+16(FP), R9
	MOVQ st+40(FP), AX
	SUBQ R9, BX
	MOVQ BX, 0(AX)
	MOVQ nextobs-16(SP), CX
	SUBQ R9, CX
	MOVQ CX, 40(AX)
	LEAQ CACHE_OFF(DI), CX
	SUBQ CX, R8
	SHRQ $2, R8
	MOVQ R8, 48(AX)
	MOVL h3-68(SP), CX
	MOVL CX, 56(AX)
	MOVL h4-72(SP), CX
	MOVL CX, 60(AX)
	RET
