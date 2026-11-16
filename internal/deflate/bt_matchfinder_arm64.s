//go:build arm64 && !appengine && !noasm && !purego && gc

// The matchfinding loop of deflate_compress_near_optimal (libdeflate v1.26
// lib/deflate_compress.c, Copyright 2016 Eric Biggers, MIT; see NOTICE) with
// bt_matchfinder_get_matches and bt_matchfinder_skip_byte
// (bt_matchfinder_advance_one_byte, lib/bt_matchfinder.h) and lz_extend
// (lib/matchfinder_common.h) written in: bt_matchfinder_amd64.s step for
// step. The prefetchw calls are PRFM PSTL1KEEP. Semantics are those of the
// Go loop of deflateCompressNearOptimal with btMatchfinder.getMatches and
// skipByte (the Go reference, statement for statement).
//
// Preemption: the routine is NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside it. Bound per call: positions
// start only below st.limit, which deflateCompressNearOptimal sets at most
// btMatchLoopMaxBytes (4096) input bytes past st.inNext, so a call advances
// in_next by at most 4096 + 257 bytes (the last position's skipped bytes),
// and each position or skipped byte walks at most max_search_depth tree nodes.
//
// Registers across positions: R0 ns (the matchfinder at offset 0), R1
// in_next, R2 in_base (&in[in_cur_base]), R3 the match cache write pointer,
// R16 &childTab, R17 limit, R19 block begin, R20 block end, R21 the next
// observation position, R22 the end of the match cache, R23 / R24 the next
// hashes, R25 the lz_hash multiplier, R26 nice_len; the frame holds the
// position's first cache entry (8), the skip count (16), max_search_depth
// (24), min_len (32), stats (40) and in_end (48). Registers of the tree walk
// (the same in both copies):
//   R4  cutoff (cur_pos - MATCHFINDER_WINDOW_SIZE)   R5  cur_node (sign-ext.)
//   R6  pending_lt_ptr     R7  pending_gt_ptr        R8  depth_remaining
//   R9  best_lt_len        R10 best_gt_len           R11 len
//   R12 best_len (get_matches) or nice_len (skip_byte)
//   R13 matchptr           R14, R15 scratch

#include "textflag.h"

// nearOptimalState byte offsets (asserted in bt_matchfinder_arm64.go): the
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

// LZEXT(END, labels...): lz_extend(in_next = R1, matchptr = R13, start_len
// = R11, max_len = END) into R11 (END an immediate or R12): 8 bytes at a
// time while len + 8 <= END, then 1. Clobbers R14, R15. It reads 8 bytes at
// R13+n and R1+n only while n+8 <= END, then single bytes at n < END.
#define LZEXT(END, e8, e1, dd, ed) \
e8: \
	ADD   $8, R11, R14; \
	CMPW  END, R14; \
	BHI   e1; \
	MOVD  (R13)(R11), R14; \
	MOVD  (R1)(R11), R15; \
	EOR   R15, R14, R14; \
	CBNZ  R14, dd; \
	ADD   $8, R11, R11; \
	JMP   e8; \
e1: \
	CMPW  END, R11; \
	BHS   ed; \
	MOVBU (R13)(R11), R14; \
	MOVBU (R1)(R11), R15; \
	CMPW  R14, R15; \
	BNE   ed; \
	ADD   $1, R11, R11; \
	JMP   e1; \
dd: \
	RBIT  R14, R14; \
	CLZ   R14, R14; \
	ADD   R14>>3, R11, R11; \
ed:

// ADVANCE: the start of bt_matchfinder_advance_one_byte for the position
// at R1: cur_pos into R13, the cutoff into R4, hash3 / hash4 (the stored
// next hashes, masked to their tables) into R9 / R10, and the next
// position's hashes into R23 / R24 with their prefetches. Clobbers R7, R12,
// R14.
#define ADVANCE \
	SUB   R2, R1, R13; \
	SUB   $0x8000, R13, R4; \
	MOVWU 1(R1), R7; \
	ANDW  $0xFFFF, R23, R9; \
	ANDW  $0xFFFF, R24, R10; \
	ANDW  $0xFFFFFF, R7, R12; \
	MULW  R25, R12, R12; \
	MULW  R25, R7, R7; \
	LSRW  $16, R12, R23; \
	LSRW  $16, R7, R24; \
	ADD   R23<<2, R0, R14; \
	PRFM  (R14), PSTL1KEEP; \
	ADD   $HASH4_OFF, R0, R14; \
	ADD   R24<<1, R14, R14; \
	PRFM  (R14), PSTL1KEEP

// WALKSTEP(gt, nx, ed): the lt / gt step of the tree walk after the compare
// of matchptr[len] (R14) with in_next[len] (R15) (the flags of CMPW R15,
// R14): it re-links the node, moves to its child, updates best_lt_len /
// best_gt_len and len, then ends the walk at ed if the child is <= cutoff
// or the depth is used up (the flags of the depth decrement are left for
// the caller's BNE). gt and nx are labels of one expansion.
#define WALKSTEP(gt, nx, ed) \
	BHS   gt; \
	MOVH  R5, (R6); \
	ANDW  $0x7FFF, R5, R14; \
	ADD   R14<<2, R16, R6; \
	ADD   $2, R6, R6; \
	MOVH  (R6), R5; \
	MOVD  R11, R9; \
	CMPW  R11, R10; \
	CSELW LO, R10, R11, R11; \
	JMP   nx; \
gt: \
	MOVH  R5, (R7); \
	ANDW  $0x7FFF, R5, R14; \
	ADD   R14<<2, R16, R7; \
	MOVH  (R7), R5; \
	MOVD  R11, R10; \
	CMPW  R11, R9; \
	CSELW LO, R9, R11, R11; \
nx: \
	CMP   R4, R5; \
	BLE   ed; \
	SUBSW $1, R8, R8

// NICE(): the end of a walk at a match of nice_len: the node's children
// become the pending pointers' targets.
#define NICE \
	ANDW  $0x7FFF, R5, R14; \
	ADD   R14<<2, R16, R14; \
	MOVHU (R14), R15; \
	MOVH  R15, (R6); \
	MOVHU 2(R14), R15; \
	MOVH  R15, (R7)

// CLOSE: the pending pointers end the walk with no child.
#define CLOSE \
	MOVD $-32768, R14; \
	MOVH R14, (R6); \
	MOVH R14, (R7)

// INC32(base, idx, shift): (*uint32)(base + idx<<shift)++. Clobbers R15.
#define INC32(base, idx, shift) \
	MOVWU (base)(idx<<shift), R15; \
	ADDW  $1, R15, R15; \
	MOVW  R15, (base)(idx<<shift)

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
//   every index inside its table. The two PRFM touch hash3Tab[2h3] /
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
//   stats.new_observations[0..9] and num_new_observations; st; the 64-byte
//   frame.
// C: deflate_compress_near_optimal (the matchfinding loop of a block, with
// bt_matchfinder_get_matches, bt_matchfinder_skip_byte and lz_extend)
TEXT ·btMatchLoopAsm(SB), NOSPLIT, $64-48
	MOVD  ns+0(FP), R0
	MOVD  stats+8(FP), R14
	MOVD  R14, 40(RSP)
	MOVD  in_base+16(FP), R7
	MOVD  in_len+24(FP), R13
	ADD   R7, R13, R14
	MOVD  R14, 48(RSP)           // in_end
	MOVD  st+40(FP), R5
	MOVD  0(R5), R1
	ADD   R7, R1, R1
	MOVD  8(R5), R2
	ADD   R7, R2, R2
	MOVD  16(R5), R17
	ADD   R7, R17, R17           // limit
	MOVD  24(R5), R19
	ADD   R7, R19, R19           // block begin
	MOVD  32(R5), R20
	ADD   R7, R20, R20           // block end
	MOVD  40(R5), R21
	ADD   R7, R21, R21           // next observation
	MOVD  48(R5), R13
	ADD   $CACHE_OFF, R0, R3
	ADD   R13<<2, R3, R3
	MOVD  $CACHE_END, R22
	ADD   R0, R22, R22
	MOVWU 56(R5), R23
	MOVWU 60(R5), R24
	MOVWU 64(R5), R14
	MOVD  R14, 32(RSP)           // min_len
	MOVWU 68(R5), R26            // nice_len
	MOVWU 72(R5), R14
	MOVD  R14, 24(RSP)           // max_search_depth
	MOVW  $0x1E35A7BD, R25
	ADD   $CHILD_OFF, R0, R16

bpos:
	CMP  R17, R1
	BHS  bdone
	MOVD R3, 8(RSP)

	// bt_matchfinder_get_matches(..., in_next - in_base, 258, nice_len, ...)
	ADVANCE
	ADD   R9<<2, R0, R14
	MOVH  (R14), R5              // cur_node = hash3_tab[hash3][0]
	MOVH  2(R14), R11            // cur_node_2 = hash3_tab[hash3][1]
	MOVH  R13, (R14)
	MOVH  R5, 2(R14)
	MOVD  $3, R12                // best_len
	CMP   R4, R5
	BLE   b3done
	MOVWU (R1), R7
	ANDW  $0xFFFFFF, R7, R7      // seq3
	MOVWU (R2)(R5), R14
	ANDW  $0xFFFFFF, R14, R14
	CMPW  R7, R14
	BNE   b3second
	SUBW  R5, R13, R14
	JMP   b3rec

b3second:
	CMP   R4, R11
	BLE   b3done
	MOVWU (R2)(R11), R14
	ANDW  $0xFFFFFF, R14, R14
	CMPW  R7, R14
	BNE   b3done
	SUBW  R11, R13, R14

b3rec:
	LSLW  $16, R14, R14
	ORRW  $3, R14, R14
	MOVW  R14, (R3)              // {3, cur_pos - node}
	ADD   $4, R3, R3

b3done:
	ADD   $HASH4_OFF, R0, R14
	ADD   R10<<1, R14, R14
	MOVH  (R14), R5              // cur_node = hash4_tab[hash4]
	MOVH  R13, (R14)
	ANDW  $0x7FFF, R13, R13
	ADD   R13<<2, R16, R6        // pending_lt_ptr
	ADD   $2, R6, R7             // pending_gt_ptr
	CMP   R4, R5
	BGT   bwalk
	CLOSE
	JMP   bfound

bwalk:
	MOVD ZR, R9                  // best_lt_len
	MOVD ZR, R10                 // best_gt_len
	MOVD ZR, R11                 // len
	MOVD 24(RSP), R8
	PCALIGN $32

bnode:
	ADD   R2, R5, R13            // matchptr
	MOVBU (R13)(R11), R14
	MOVBU (R1)(R11), R15
	CMPW  R15, R14
	BNE   bcmp
	ADD   $1, R11, R11
	LZEXT($258, be8, be1, bdd, bed)
	CMPW  R12, R11
	BLS   bcmpld
	MOVD  R11, R12               // best_len = len
	SUB   R13, R1, R14
	LSLW  $16, R14, R14
	ORRW  R11, R14, R14
	MOVW  R14, (R3)              // {len, in_next - matchptr}
	ADD   $4, R3, R3
	CMPW  R26, R11
	BHS   bnice

bcmpld:
	MOVBU (R13)(R11), R14
	MOVBU (R1)(R11), R15
	CMPW  R15, R14

bcmp:
	WALKSTEP(bgt, bnx, bend)
	BNE bnode

bend:
	CLOSE
	JMP bfound

bnice:
	NICE

bfound:
	// best_len = the length of the last match found, 0 if none
	MOVD 8(RSP), R13
	CMP  R13, R3
	BNE  bhave
	MOVD ZR, R12

bhave:
	CMP   R21, R1
	BLO   bhdr
	MOVD  40(RSP), R7
	MOVD  32(RSP), R14
	CMPW  R14, R12
	BLO   bobslit
	CMPW  $9, R12
	CSETW HS, R11                // length >= 9
	ADD   $OBS_MATCH, R7, R14
	INC32(R14, R11, 2)           // observe_match
	MOVWU OBS_NUM(R7), R15
	ADDW  $1, R15, R15
	MOVW  R15, OBS_NUM(R7)
	ADD   R12, R1, R21
	MOVD  $NMLF_OFF, R14
	ADD   R0, R14, R14
	INC32(R14, R12, 2)           // new_match_len_freqs[best_len]++
	JMP   bhdr

bobslit:
	MOVBU (R1), R11
	LSRW  $5, R11, R14
	ANDW  $6, R14, R14
	ANDW  $1, R11, R11
	ORRW  R11, R14, R14          // ((lit >> 5) & 0x6) | (lit & 1)
	INC32(R7, R14, 2)            // observe_literal
	MOVWU OBS_NUM(R7), R15
	ADDW  $1, R15, R15
	MOVW  R15, OBS_NUM(R7)
	ADD   $1, R1, R21

bhdr:
	SUB   R13, R3, R11
	LSR   $2, R11, R11           // the number of matches
	MOVBU (R1), R14
	LSLW  $16, R14, R14
	ORRW  R11, R14, R14
	MOVW  R14, (R3)              // {num_matches, *in_next}
	ADD   $4, R3, R3
	ADD   $1, R1, R1

	// best_len >= DEFLATE_MIN_MATCH_LEN && best_len >= nice_len: skip the
	// best_len - 1 bytes it covers
	CMPW $3, R12
	BLO  bcheck
	CMPW R26, R12
	BLO  bcheck
	SUBW $1, R12, R12
	MOVD R12, 16(RSP)

bskip:
	// bt_matchfinder_skip_byte(..., in_next - in_base, nice_len, ...)
	ADVANCE
	ADD   R9<<2, R0, R14
	MOVHU (R14), R15
	MOVH  R15, 2(R14)            // hash3_tab[hash3][1] = hash3_tab[hash3][0]
	MOVH  R13, (R14)
	ADD   $HASH4_OFF, R0, R14
	ADD   R10<<1, R14, R14
	MOVH  (R14), R5
	MOVH  R13, (R14)
	ANDW  $0x7FFF, R13, R13
	ADD   R13<<2, R16, R6
	ADD   $2, R6, R7
	CMP   R4, R5
	BGT   swalk
	CLOSE
	JMP   snext

swalk:
	MOVD ZR, R9
	MOVD ZR, R10
	MOVD ZR, R11
	MOVD 24(RSP), R8
	MOVD R26, R12
	PCALIGN $32

snode:
	ADD   R2, R5, R13
	MOVBU (R13)(R11), R14
	MOVBU (R1)(R11), R15
	CMPW  R15, R14
	BNE   scmp
	ADD   $1, R11, R11
	LZEXT(R12, se8, se1, sdd, sed)
	CMPW  R12, R11
	BHS   snice
	MOVBU (R13)(R11), R14
	MOVBU (R1)(R11), R15
	CMPW  R15, R14

scmp:
	WALKSTEP(sgt, snx, send)
	BNE snode

send:
	CLOSE
	JMP snext

snice:
	NICE

snext:
	MOVBU (R1), R14
	LSLW  $16, R14, R14
	MOVW  R14, (R3)              // {0, *in_next}
	ADD   $4, R3, R3
	ADD   $1, R1, R1
	MOVD  16(RSP), R14
	SUBS  $1, R14, R14
	MOVD  R14, 16(RSP)
	BNE   bskip

bcheck:
	CMP   R20, R1
	BHS   bdone
	CMP   R22, R3
	BHS   bdone
	// ready_to_check_block(&c->split_stats, in_block_begin, in_next, in_end)
	MOVD  40(RSP), R7
	MOVWU OBS_NUM(R7), R14
	CMPW  $512, R14
	BLO   bpos
	SUB   R19, R1, R14
	CMP   $5000, R14
	BLT   bpos
	MOVD  48(RSP), R14
	SUB   R1, R14, R14
	CMP   $5000, R14
	BLT   bpos

bdone:
	MOVD in_base+16(FP), R7
	MOVD st+40(FP), R5
	SUB  R7, R1, R1
	MOVD R1, 0(R5)
	SUB  R7, R21, R21
	MOVD R21, 40(R5)
	ADD  $CACHE_OFF, R0, R13
	SUB  R13, R3, R3
	LSR  $2, R3, R3
	MOVD R3, 48(R5)
	MOVW R23, 56(R5)
	MOVW R24, 60(R5)
	RET
