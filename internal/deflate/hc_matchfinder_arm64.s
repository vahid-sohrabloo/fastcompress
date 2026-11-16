//go:build arm64 && !appengine && !noasm && !purego && gc

// hc_matchfinder_longest_match of libdeflate v1.26 lib/hc_matchfinder.h
// (Copyright 2016 Eric Biggers, MIT; see NOTICE), the UNALIGNED_ACCESS_IS_FAST
// branch, with lz_extend (lib/matchfinder_common.h) and the window slide
// (hc_matchfinder_slide_window = matchfinder_rebase of the whole struct, NEON)
// written in: the HCSEARCH macro of hc_matchfinder_amd64.s step for step, as
// a Go-callable routine (the compressor loops stay in Go on arm64).
// Semantics are those of hcMatchfinder.longestMatch (the Go reference,
// statement for statement).
//
// Preemption: hcLongestMatchAsm is NOSPLIT assembly; one call walks at most
// max_search_depth nodes and slides the 256 KiB matchfinder at most once.

#include "textflag.h"

// struct hcMatchfinder byte offsets (asserted in hc_matchfinder_arm64.go).
#define HASH4_OFF 65536
#define NEXT_OFF 196608

// NEXTNODE(out): R10 = nextTab[node & 0x7FFF], ending at out when it is at
// or below the cutoff (a signed 16-bit mf_pos_t compare) or the depth is
// used up; falls through otherwise. Clobbers R14.
#define NEXTNODE(loop, out) \
	ANDW  $0x7FFF, R10, R14; \
	MOVHU (R20)(R14<<1), R10; \
	SXTH  R10, R14; \
	CMP   R3, R14; \
	BLE   out; \
	SUBSW $1, R4, R4; \
	BNE   loop; \
	JMP   out

// hcSearch: the search (hc_matchfinder_longest_match) with a register
// interface, for hcLongestMatchAsm and lazyLoopAsm. In: R0 mf, R1 strptr,
// R2 in_base, R4 max_search_depth, R5 best_len, R6 / R7 next_hashes[0] /
// [1] (masked to 15 / 16 bits), R15 max_len, R16 nice_len, R17 the lz_hash
// multiplier, R20 &nextTab, R21 &hash4Tab. Out: R5 best_len, R8
// best_matchptr (strptr if none), R2 (advanced by a slide), R6 / R7 the next
// hashes. Clobbers R3, R4, R9-R14, R19, V0-V5.
//
// BOUNDS: as hcLongestMatchAsm's (the same accesses).
TEXT hcSearch<>(SB), NOSPLIT|NOFRAME, $0
	SUB  R2, R1, R13             // cur_pos
	CMP  $0x8000, R13
	BNE  noslide
	// the slide: each int16 x becomes x - 0x8000 saturated (C:
	// vqsubq_s16), computed as umin(x, 0x8000) | 0x8000 on the bits
	MOVD  $0x8000, R14
	VDUP  R14, V5.H8
	MOVD  R0, R14
	MOVD  $4096, R19

slide:
	VLD1  (R14), [V0.H8, V1.H8, V2.H8, V3.H8]
	VUMIN V5.H8, V0.H8, V0.H8
	VUMIN V5.H8, V1.H8, V1.H8
	VUMIN V5.H8, V2.H8, V2.H8
	VUMIN V5.H8, V3.H8, V3.H8
	VORR  V5.B16, V0.B16, V0.B16
	VORR  V5.B16, V1.B16, V1.B16
	VORR  V5.B16, V2.B16, V2.B16
	VORR  V5.B16, V3.B16, V3.B16
	VST1.P [V0.H8, V1.H8, V2.H8, V3.H8], 64(R14)
	SUBS  $1, R19, R19
	BNE   slide
	ADD   $0x8000, R2, R2
	MOVD  ZR, R13

noslide:
	MOVD  R1, R8                 // best_matchptr
	SUBW  $0x8000, R13, R3
	SXTH  R3, R3                 // cutoff
	CMPW  $5, R15
	BLO   out                    // max_len < 5
	MOVH  (R0)(R6<<1), R12       // cur_node3
	MOVH  (R21)(R7<<1), R10      // cur_node4
	MOVH  R13, (R0)(R6<<1)       // hash3Tab[hash3] = cur_pos
	MOVH  R13, (R21)(R7<<1)      // hash4Tab[hash4] = cur_pos
	ANDW  $0x7FFF, R13, R14
	MOVH  R10, (R20)(R14<<1)     // nextTab[cur_pos & 0x7FFF] = cur_node4
	MOVWU 1(R1), R14
	ANDW  $0xFFFFFF, R14, R6
	MULW  R17, R6, R6
	LSRW  $17, R6, R6            // next_hashes[0]
	MULW  R17, R14, R7
	LSRW  $16, R7, R7            // next_hashes[1]
	ADD   R6<<1, R0, R14
	PRFM  (R14), PSTL1KEEP
	ADD   R7<<1, R21, R14
	PRFM  (R14), PSTL1KEEP
	CMPW  $4, R5
	BHS   have4
	CMP   R3, R12
	BLE   out                    // cur_node3 <= cutoff
	MOVWU (R1), R9               // seq4
	CMPW  $3, R5
	BHS   check4
	MOVWU (R2)(R12), R14
	EORW  R9, R14, R14
	ANDW  $0xFFFFFF, R14, R14
	CBNZW R14, check4
	MOVD  $3, R5
	ADD   R2, R12, R8

check4:
	CMP   R3, R10
	BLE   out                    // cur_node4 <= cutoff
	PCALIGN $32

loop4:
	SXTH  R10, R11
	ADD   R2, R11, R11           // matchptr
	MOVWU (R11), R14
	CMPW  R9, R14
	BEQ   candidate
	NEXTNODE(loop4, out)

have4:
	CMP   R3, R10
	BLE   out
	CMPW  R16, R5
	BHS   out                    // best_len >= nice_len
	MOVWU (R1), R9

newBest:
	ADD   R5, R1, R14
	MOVWU -3(R14), R12           // the 4 bytes ending at strptr+best_len
	PCALIGN $32

loop5:
	SXTH  R10, R11
	ADD   R2, R11, R11           // matchptr
	ADD   R5, R11, R14
	MOVWU -3(R14), R14
	CMPW  R12, R14
	BNE   next5
	MOVWU (R11), R14
	CMPW  R9, R14
	BEQ   candidate

next5:
	NEXTNODE(loop5, out)

candidate:
	// R13 = lz_extend(strptr, matchptr, 4, max_len)
	MOVD $4, R13

ext8:
	ADD   $8, R13, R14
	CMPW  R15, R14
	BHI   ext1
	MOVD  (R11)(R13), R14
	MOVD  (R1)(R13), R19
	EOR   R19, R14, R14
	CBNZ  R14, extDiff
	ADD   $8, R13, R13
	JMP   ext8

ext1:
	CMPW  R15, R13
	BHS   extended
	MOVBU (R11)(R13), R14
	MOVBU (R1)(R13), R19
	CMPW  R14, R19
	BNE   extended
	ADD   $1, R13, R13
	JMP   ext1

extDiff:
	RBIT  R14, R14
	CLZ   R14, R14
	ADD   R14>>3, R13, R13

extended:
	CMPW  R5, R13
	BLS   next5                  // not longer
	MOVD  R13, R5                // the new longest match
	MOVD  R11, R8
	CMPW  R16, R5
	BHS   out                    // best_len >= nice_len
	NEXTNODE(newBest, out)

out:
	RET

// func hcLongestMatchAsm(mf *hcMatchfinder, in []byte, inBase *int, inNext int,
//	bestLen, maxLen, niceLen, maxSearchDepth uint32, nextHashes *[2]uint32) (length, offset uint32)
//
// Registers: R0 mf, R1 strptr (&in[inNext]), R2 in_base (&in[*inBase],
// advanced by a slide), R3 cutoff (sign-extended mf_pos_t), R4
// depth_remaining, R5 best_len, R6 / R7 next_hashes[0] / [1], R8
// best_matchptr, R9 seq4 (the first 4 bytes), R10 cur_node4 (a node: its
// low 16 bits, sign-extended for the match pointer), R11 matchptr, R12
// cur_node3, then the 4 bytes at strptr+best_len-3, R13 cur_pos, then the
// lz_extend length, R15 max_len, R16 nice_len, R17 the lz_hash multiplier,
// R19 in, R20 &nextTab, R21 &hash4Tab; R14, R19 scratch.
//
// BOUNDS: preconditions (those under which the Go reference does not panic;
// the callers keep them by construction): max_len <= len(in) - inNext;
// nice_len <= max_len; 0 <= strptr - in_base <= 32768; every
// hash3Tab/hash4Tab/nextTab entry reachable from the buckets that is > cutoff
// is a position p (relative to in_base, after the slide) of this buffer with
// in <= in_base + p < strptr.
// Memory accesses:
// * mf (the 262144-byte struct): the slide reads and writes bytes [0, 262144)
//   (4096 iterations of 64 bytes); hash3Tab[R6] and hash4Tab[R7] (read and
//   written; R6 < 2^15 and R7 < 2^16, masked on entry), nextTab[curPos &
//   0x7FFF] (written), nextTab[node & 0x7FFF] (read). The next hashes are
//   lz_hash results (a 32-bit product shifted right by 17 / 16), so they are
//   < 2^15 / < 2^16. The two PRFM touch hash3Tab / hash4Tab at them; a
//   prefetch never faults anyway.
// * in, only when max_len >= 5: the 4 bytes at strptr+1 (<= strptr+max_len)
//   and at strptr (seq4); for a candidate m = in_base + node (node > cutoff,
//   so m < strptr by the precondition): 4 bytes at m (length 3 and 4 checks,
//   the 24-bit compare masks the 4th byte, m+3 < strptr+max_len), 4 bytes at
//   m+best_len-3 and at strptr+best_len-3 with 4 <= best_len < nice_len <=
//   max_len (so they end at most at +max_len); lz_extend reads 8 bytes at m+n
//   and strptr+n only while n+8 <= max_len, then single bytes at n < max_len.
//   So in is read only in [m, m+max_len) with m >= &in[0] and [strptr,
//   strptr+max_len).
// * nextHashes[0..1] and *inBase (read, written back); the 16-byte frame
//   (the saved link register).
// C: hc_matchfinder_longest_match (+ lz_extend, hc_matchfinder_slide_window)
TEXT ·hcLongestMatchAsm(SB), NOSPLIT, $16-80
	MOVD  mf+0(FP), R0
	MOVD  in_base+8(FP), R19
	MOVD  inBase+32(FP), R14
	MOVD  inNext+40(FP), R1
	ADD   R19, R1, R1
	MOVD  (R14), R2
	ADD   R19, R2, R2
	MOVWU bestLen+48(FP), R5
	MOVWU maxLen+52(FP), R15
	MOVWU niceLen+56(FP), R16
	MOVWU maxSearchDepth+60(FP), R4
	MOVD  nextHashes+64(FP), R14
	MOVWU 0(R14), R6
	ANDW  $0x7FFF, R6, R6
	MOVWU 4(R14), R7
	ANDW  $0xFFFF, R7, R7
	MOVW  $0x1E35A7BD, R17
	ADD   $NEXT_OFF, R0, R20
	ADD   $HASH4_OFF, R0, R21
	BL    hcSearch<>(SB)

	MOVD nextHashes+64(FP), R14
	MOVW R6, 0(R14)
	MOVW R7, 4(R14)
	MOVD in_base+8(FP), R19
	SUB  R19, R2, R2
	MOVD inBase+32(FP), R14
	MOVD R2, (R14)               // *inBase (changed only by a slide)
	SUB  R8, R1, R1              // offset = strptr - best_matchptr
	MOVW R5, length+72(FP)
	MOVW R1, offset+76(FP)
	RET

// greedyState byte offsets (asserted in hc_matchfinder_arm64.go):
// sequences[0] and sequences[SEQ_STORE_LENGTH]. deflateFreqs:
// litlen[DEFLATE_FIRST_LEN_SYM] and offset[0]. blockSplitStats:
// new_observations[NUM_LITERAL_OBSERVATION_TYPES] and num_new_observations.
#define GSEQ_OFF 262144
#define GSEQ_END 662144
#define FREQ_LEN 1028
#define FREQ_OFF 1152
#define OBS_MATCH 32
#define OBS_NUM 80

// The lazy loop's frame slots (offsets from RSP).
#define L_BLOCKBEGIN 8
#define L_RECALC 16
#define L_SEQEND 24
#define L_MINLEN 32
#define L_DEPTH 40
#define L_LAZY2 48
#define L_CURLEN 56
#define L_CUROFF 64
#define L_CNT 72

// INC32(base, idx): (*uint32)(base + idx*4)++. Clobbers R19.
#define INC32(base, idx) \
	MOVWU (base)(idx<<2), R19; \
	ADDW  $1, R19, R19; \
	MOVW  R19, (base)(idx<<2)

// INCOBS: stats.num_new_observations++. Clobbers R19.
#define INCOBS \
	MOVWU OBS_NUM(R23), R19; \
	ADDW  $1, R19, R19; \
	MOVW  R19, OBS_NUM(R23)

// LADJUST: adjust_max_and_nice_len(max_len, nice_len, in_end - in_next).
// Clobbers R13.
#define LADJUST(lbl) \
	SUB  R1, R25, R13; \
	CMP  $258, R13; \
	BGE  lbl; \
	MOVD R13, R15; \
	CMPW R13, R16; \
	BLS  lbl; \
	MOVD R13, R16; \
lbl:

// LLIT(off): deflate_choose_literal(c, in_next[off], true, seq). Clobbers
// R12-R14, R19.
#define LLIT(off) \
	MOVBU off(R1), R12; \
	INC32(R22, R12); \
	LSRW  $5, R12, R13; \
	ANDW  $6, R13, R13; \
	ANDW  $1, R12, R12; \
	ORRW  R12, R13, R13; \
	INC32(R23, R13); \
	INCOBS; \
	MOVWU (R24), R14; \
	ADDW  $1, R14, R14; \
	MOVW  R14, (R24)

// LMATCH: deflate_choose_match(c, R5 length, R12 offset, true, &seq).
// Clobbers R3, R13, R14, R19.
#define LMATCH \
	MOVW  $256, R13; \
	SUBW  R12, R13, R13; \
	LSRW  $29, R13, R13; \
	SUBW  $1, R12, R14; \
	LSRW  R13, R14, R14; \
	MOVD  $·deflateOffsetSlot(SB), R3; \
	MOVBU (R3)(R14), R14; \
	ADDW  R13<<1, R14, R14; \
	MOVD  $·deflateLengthSlot(SB), R13; \
	MOVBU (R13)(R5), R13; \
	ADD   $FREQ_LEN, R22, R3; \
	INC32(R3, R13); \
	ADD   $FREQ_OFF, R22, R3; \
	INC32(R3, R14); \
	CMPW  $9, R5; \
	CSETW HS, R13; \
	ADD   $OBS_MATCH, R23, R3; \
	INC32(R3, R13); \
	INCOBS; \
	LSLW  $23, R5, R13; \
	MOVWU (R24), R3; \
	ORRW  R13, R3, R3; \
	MOVW  R3, (R24); \
	MOVH  R12, 4(R24); \
	MOVH  R14, 6(R24); \
	ADD   $8, R24, R24; \
	MOVW  ZR, (R24)

// LBETTER(thr, nl): falls through if the match (R5 length, R12 offset)
// found after the current one (curlen, curoff) is better: next_len >=
// cur_len && 4 * (next_len - cur_len) + (bsr32(cur_offset) -
// bsr32(next_offset)) > thr (the offsets are not 0 then); else jumps to nl.
// Clobbers R3, R13, R14, R19.
#define LBETTER(thr, nl) \
	MOVD  L_CURLEN(RSP), R13; \
	CMPW  R13, R5; \
	BLO   nl; \
	SUBW  R13, R5, R14; \
	LSLW  $2, R14, R14; \
	MOVD  L_CUROFF(RSP), R3; \
	CLZW  R3, R3; \
	CLZW  R12, R19; \
	ADDW  R19, R14, R14; \
	SUBW  R3, R14, R14; \
	CMPW  $thr, R14; \
	BLE   nl

// LSKIP(lp, sl, ns, dn): hc_matchfinder_skip_bytes(in_next, R4 count >= 1)
// (nothing if count + 5 > in_end - in_next), then in_next += count.
// Clobbers R3, R4, R12-R14, R19, V0-V5.
#define LSKIP(lp, sl, ns, dn) \
	MOVD  R4, L_CNT(RSP); \
	ADD   R4, R1, R13; \
	ADD   $5, R13, R13; \
	CMP   R25, R13; \
	BHI   dn; \
	SUB   R2, R1, R13; \
	ADD   $1, R1, R3; \
lp: \
	CMPW  $0x8000, R13; \
	BNE   ns; \
	MOVD  $0x8000, R14; \
	VDUP  R14, V5.H8; \
	MOVD  R0, R14; \
	MOVD  $4096, R19; \
sl: \
	VLD1  (R14), [V0.H8, V1.H8, V2.H8, V3.H8]; \
	VUMIN V5.H8, V0.H8, V0.H8; \
	VUMIN V5.H8, V1.H8, V1.H8; \
	VUMIN V5.H8, V2.H8, V2.H8; \
	VUMIN V5.H8, V3.H8, V3.H8; \
	VORR  V5.B16, V0.B16, V0.B16; \
	VORR  V5.B16, V1.B16, V1.B16; \
	VORR  V5.B16, V2.B16, V2.B16; \
	VORR  V5.B16, V3.B16, V3.B16; \
	VST1.P [V0.H8, V1.H8, V2.H8, V3.H8], 64(R14); \
	SUBS  $1, R19, R19; \
	BNE   sl; \
	ADD   $0x8000, R2, R2; \
	MOVD  ZR, R13; \
ns: \
	MOVH  R13, (R0)(R6<<1); \
	MOVHU (R21)(R7<<1), R12; \
	ANDW  $0x7FFF, R13, R14; \
	MOVH  R12, (R20)(R14<<1); \
	MOVH  R13, (R21)(R7<<1); \
	MOVWU (R3), R14; \
	ADD   $1, R3, R3; \
	MOVD  R14, R7; \
	ANDW  $0xFFFFFF, R14, R14; \
	MULW  R17, R14, R6; \
	MULW  R17, R7, R7; \
	LSRW  $17, R6, R6; \
	LSRW  $16, R7, R7; \
	ADDW  $1, R13, R13; \
	SUBSW $1, R4, R4; \
	BNE   lp; \
dn: \
	MOVD  L_CNT(RSP), R13; \
	ADD   R13, R1, R1

// func lazyLoopAsm(pg *greedyState, freqs *deflateFreqs, stats *blockSplitStats, in []byte,
//	st *lazyLoopState)
//
// The per-position loop of deflate_compress_lazy_generic (lazy and lazy2),
// lazyLoopAsm of hc_matchfinder_amd64.s step for step, with the searches
// through hcSearch: the search at the current position (best_len min_len -
// 1, max_search_depth), the literal choice (no match, or a length 3 match
// farther than 8192), and with a match the have_cur_match loop: nice-length
// matches taken at once, else the look-ahead searches (depth >> 1, and >> 2
// for lazy2) with the "better match" rule, literals for the skipped
// positions, the chosen match and the skip (length - 1, - 2 or - 3).
// adjust_max_and_nice_len before every search (st.maxLen / st.niceLen are
// updated). It returns at the start of a position with in_next >=
// st.nextRecalcMinLen (the Go loop recalculates min_len) and after the first
// position at which in_next >= in_max_block_end, seq reaches
// SEQ_STORE_LENGTH or ready_to_check_block holds (the Go loop then evaluates
// should_end_block). Semantics are those of the Go loop of
// deflateCompressLazyGeneric. Registers across positions: R0 pg, R1
// in_next, R2 in_cur_base, R6 / R7 next_hashes[0] / [1], R15 max_len, R16
// nice_len, R17 the lz_hash multiplier, R20 &nextTab, R21 &hash4Tab, R22
// freqs, R23 stats, R24 seq, R25 in_end, R26 in_max_block_end; the rest of
// the state is in the frame.
//
// BOUNDS: preconditions (kept by deflateCompressLazyGeneric): st.inNext <
// st.nextRecalcMinLen; 0 <= st.inNext - st.inCurBase <= 32768;
// st.inBlockBegin <= st.inNext < st.inMaxBlockEnd <= len(in); 0 <= st.seq <
// 50000; 1 <= st.minLen; st.niceLen <= st.maxLen <= 258; st.maxLen <=
// len(in) - st.inNext or st.maxLen = 258; the matchfinder invariant of
// hcLongestMatchAsm's BOUNDS; st.nextHash3 / st.nextHash4 are masked on
// entry. Each search runs after LADJUST, so max_len <= len(in) - in_next and
// nice_len <= max_len (the search's preconditions; hcLongestMatchAsm's
// BOUNDS give its accesses). A look-ahead search is at most at in_next+2
// with a current match of length >= 3 <= max_len before it, so in_next <
// len(in) there. Other accesses: a literal reads in[in_next-3 .. in_next]
// (positions already passed); a skip of count bytes (only if in_next + count
// + 5 <= len(in)) reads the 4 bytes at in_next+1+i, i < count, all before
// in_next+count+4 < len(in), and writes hash3Tab/hash4Tab/nextTab at masked
// indexes (a slide rebases bytes [0, 262144) of pg); sequences[seq] and
// [seq+1] (seq+1 <= 50000, the last index of the 50001-entry array: the loop
// stops once seq reaches 50000; one position adds at most one match);
// freqs.litlen[0..255], litlen[257+length_slot <= 285], offset[offset_slot
// <= 29]; stats.new_observations[0..9] and num_new_observations; the slot
// tables; st; the 80-byte frame.
// C: deflate_compress_lazy_generic (the inner loop of a block)
TEXT ·lazyLoopAsm(SB), NOSPLIT, $80-56
	MOVD  pg+0(FP), R0
	MOVD  freqs+8(FP), R22
	MOVD  stats+16(FP), R23
	MOVD  in_base+24(FP), R3
	MOVD  in_len+32(FP), R13
	ADD   R3, R13, R25           // in_end
	MOVD  st+48(FP), R12
	MOVD  0(R12), R1
	ADD   R3, R1, R1
	MOVD  8(R12), R2
	ADD   R3, R2, R2
	MOVD  16(R12), R13
	ADD   R3, R13, R13
	MOVD  R13, L_BLOCKBEGIN(RSP)
	MOVD  24(R12), R26
	ADD   R3, R26, R26           // in_max_block_end
	MOVD  32(R12), R13
	ADD   R3, R13, R13
	MOVD  R13, L_RECALC(RSP)
	MOVD  40(R12), R13
	ADD   $GSEQ_OFF, R0, R24
	ADD   R13<<3, R24, R24       // seq
	MOVD  $GSEQ_END, R13
	ADD   R0, R13, R13
	MOVD  R13, L_SEQEND(RSP)
	MOVWU 48(R12), R6
	ANDW  $0x7FFF, R6, R6        // next_hashes[0] (masked: hcSearch's entry condition)
	MOVWU 52(R12), R7
	ANDW  $0xFFFF, R7, R7        // next_hashes[1]
	MOVWU 56(R12), R13
	MOVD  R13, L_MINLEN(RSP)
	MOVWU 60(R12), R15           // max_len
	MOVWU 64(R12), R16           // nice_len
	MOVWU 68(R12), R13
	MOVD  R13, L_DEPTH(RSP)
	MOVWU 72(R12), R13
	MOVD  R13, L_LAZY2(RSP)
	MOVW  $0x1E35A7BD, R17
	ADD   $NEXT_OFF, R0, R20
	ADD   $HASH4_OFF, R0, R21

lstep:
	MOVD L_RECALC(RSP), R13
	CMP  R13, R1
	BHS  ldone                   // time to recalculate min_len (in Go)
	LADJUST(ladj1)
	MOVD L_MINLEN(RSP), R5
	SUBW $1, R5, R5              // best_len = min_len - 1
	MOVD L_DEPTH(RSP), R4
	BL   hcSearch<>(SB)
	MOVD L_MINLEN(RSP), R13
	CMPW R13, R5
	BLO  llit                    // cur_len < min_len
	SUB  R8, R1, R12             // cur_offset
	CMPW $3, R5
	BNE  lhave
	CMP  $8192, R12
	BHI  llit                    // cur_len == DEFLATE_MIN_MATCH_LEN && cur_offset > 8192

lhave:
	MOVD R5, L_CURLEN(RSP)
	MOVD R12, L_CUROFF(RSP)
	ADD  $1, R1, R1

lhloop:
	// have_cur_match: the current match (curlen, curoff) is at in_next - 1.
	MOVD L_CURLEN(RSP), R5
	CMPW R16, R5
	BHS  lnice                   // very long: choose it immediately
	LADJUST(ladj2)
	MOVD L_CURLEN(RSP), R5
	SUBW $1, R5, R5
	MOVD L_DEPTH(RSP), R4
	LSRW $1, R4, R4              // max_search_depth >> 1
	BL   hcSearch<>(SB)
	SUB  R8, R1, R12             // next_offset
	ADD  $1, R1, R1
	LBETTER(2, lnot1)
	MOVD R5, L_CURLEN(RSP)            // the next match becomes the current one
	MOVD R12, L_CUROFF(RSP)
	LLIT(-2)                     // a better match at the next position
	JMP  lhloop

lnot1:
	MOVD L_LAZY2(RSP), R13
	CBZ  R13, lnolazy2
	LADJUST(ladj3)
	MOVD L_CURLEN(RSP), R5
	SUBW $1, R5, R5
	MOVD L_DEPTH(RSP), R4
	LSRW $2, R4, R4              // max_search_depth >> 2
	BL   hcSearch<>(SB)
	SUB  R8, R1, R12
	ADD  $1, R1, R1
	LBETTER(6, lnot2)
	MOVD R5, L_CURLEN(RSP)
	MOVD R12, L_CUROFF(RSP)
	LLIT(-3)                     // a much better match two positions ahead
	LLIT(-2)
	JMP  lhloop

lnot2:
	MOVD L_CURLEN(RSP), R5
	MOVD L_CUROFF(RSP), R12
	LMATCH
	CMPW $3, R5
	BLS  lend
	SUBW $3, R5, R4
	LSKIP(lsk1, lsl1, lns1, ldn1)
	JMP  lend

lnolazy2:
	MOVD L_CURLEN(RSP), R5
	MOVD L_CUROFF(RSP), R12
	LMATCH
	SUBW $2, R5, R4
	LSKIP(lsk2, lsl2, lns2, ldn2)
	JMP  lend

lnice:
	MOVD L_CUROFF(RSP), R12
	LMATCH
	SUBW $1, R5, R4
	LSKIP(lsk3, lsl3, lns3, ldn3)
	JMP  lend

llit:
	LLIT(0)
	ADD  $1, R1, R1

lend:
	CMP   R26, R1
	BHS   ldone
	MOVD  L_SEQEND(RSP), R13
	CMP   R13, R24
	BHS   ldone
	// ready_to_check_block(&c->split_stats, in_block_begin, in_next, in_end)
	MOVWU OBS_NUM(R23), R13
	CMPW  $512, R13
	BLO   lstep
	MOVD  L_BLOCKBEGIN(RSP), R13
	SUB   R13, R1, R13
	CMP   $5000, R13
	BLT   lstep
	SUB   R1, R25, R13
	CMP   $5000, R13
	BLT   lstep

ldone:
	MOVD in_base+24(FP), R3
	MOVD st+48(FP), R12
	SUB  R3, R1, R1
	MOVD R1, 0(R12)
	SUB  R3, R2, R2
	MOVD R2, 8(R12)
	ADD  $GSEQ_OFF, R0, R13
	SUB  R13, R24, R24
	LSR  $3, R24, R24
	MOVD R24, 40(R12)
	MOVW R6, 48(R12)
	MOVW R7, 52(R12)
	MOVW R15, 60(R12)
	MOVW R16, 64(R12)
	RET
