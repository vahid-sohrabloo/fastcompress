//go:build arm64 && !appengine && !noasm && !purego && gc

// The per-position loop of deflate_compress_fastest of libdeflate v1.26
// lib/deflate_compress.c (Copyright 2016 Eric Biggers, MIT; see NOTICE), with
// ht_matchfinder_longest_match, ht_matchfinder_skip_bytes
// (lib/ht_matchfinder.h, HT_MATCHFINDER_BUCKET_SIZE == 2), lz_extend
// (lib/matchfinder_common.h), deflate_choose_literal / deflate_choose_match
// (gather_split_stats = false), ht_matchfinder_slide_window (NEON) written
// in: compress_fastest_amd64.s step for step. It runs only while at least
// DEFLATE_MAX_MATCH_LEN bytes remain, so max_len is 258 and nice_len fixed;
// the Go loop (compress_fastest.go) does the rest. Semantics are those of
// that Go loop, statement for statement.
//
// Preemption: fastestLoopAsm is NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside it. It returns at the block end,
// so one call handles at most FAST_SOFT_MAX_BLOCK_LENGTH + MIN_BLOCK_LENGTH -
// 1 (70534) positions.
//
// Register use:
//   R0  pf (ht_mf at 0, sequences at SEQ_OFF)   R1  freqs (litlen, offset)
//   R2  in_next                                 R3  in_cur_base
//   R4  in_end - DEFLATE_MAX_MATCH_LEN          R5  seq (&sequences[i])
//   R6  next_hash                               R7  nice_len
//   R8  cutoff (search, sign-extended), then the second length / scratch
//   R9  cur_pos / seq4 / pos                    R10 hash (bucket index), scratch
//   R11 node, matchptr / best match             R12 second node, then its matchptr
//   R13 length                                  R14 &in[in_max_block_end]
//   R15 &sequences[FAST_SEQ_STORE_LENGTH]       R16 deflateOffsetSlot
//   R17 deflateLengthSlot                       R19 the lz_hash multiplier
//   R20-R23 scratch
// The buckets are indexed by the hash itself (< 2^15), bucket[0] is loaded
// zero-extended and compared with the cutoff as a signed 16-bit value (both
// are mf_pos_t), bucket[1] is loaded sign-extended (MOVH).

#include "textflag.h"

// fastestState byte offsets (asserted in compress_fastest_arm64.go).
#define SEQ_OFF 131072
#define SEQ_END 196608
// deflateFreqs byte offsets: litlen[DEFLATE_FIRST_LEN_SYM], offset[0].
#define FREQ_LEN 1028
#define FREQ_OFF 1152

// SLIDE rebases the 65536 hash_tab entries at R0, 64 bytes per iteration:
// each int16 x becomes x - 0x8000 saturated (C: vqsubq_s16), computed as
// umin(x, 0x8000) | 0x8000 on the bits. Clobbers R20-R22, V0-V5. lbl is a
// unique loop label.
#define SLIDE(lbl) \
	MOVD  $0x8000, R20; \
	VDUP  R20, V5.H8; \
	MOVD  R0, R21; \
	MOVD  $2048, R22; \
lbl: \
	VLD1  (R21), [V0.H8, V1.H8, V2.H8, V3.H8]; \
	VUMIN V5.H8, V0.H8, V0.H8; \
	VUMIN V5.H8, V1.H8, V1.H8; \
	VUMIN V5.H8, V2.H8, V2.H8; \
	VUMIN V5.H8, V3.H8, V3.H8; \
	VORR  V5.B16, V0.B16, V0.B16; \
	VORR  V5.B16, V1.B16, V1.B16; \
	VORR  V5.B16, V2.B16, V2.B16; \
	VORR  V5.B16, V3.B16, V3.B16; \
	VST1.P [V0.H8, V1.H8, V2.H8, V3.H8], 64(R21); \
	SUBS  $1, R22, R22; \
	BNE   lbl

// EXTEND(ptr, n, w, b, d, done): n = lz_extend(in_next, ptr, n, 258): 8-byte
// words while n+8 <= 258, then bytes, ending at done. Clobbers R10, R21.
#define EXTEND(ptr, n, w, b, d, done) \
w: \
	ADD   $8, n, R10; \
	CMP   $258, R10; \
	BHI   b; \
	MOVD  (ptr)(n), R10; \
	MOVD  (R2)(n), R21; \
	EOR   R21, R10, R10; \
	CBNZ  R10, d; \
	ADD   $8, n, n; \
	JMP   w; \
b: \
	CMP   $258, n; \
	BHS   done; \
	MOVBU (ptr)(n), R10; \
	MOVBU (R2)(n), R21; \
	CMPW  R10, R21; \
	BNE   done; \
	ADD   $1, n, n; \
	JMP   b; \
d: \
	RBIT  R10, R10; \
	CLZ   R10, R10; \
	ADD   R10>>3, n, n

// func fastestLoopAsm(pf *fastestState, freqs *deflateFreqs, in []byte, st *fastestLoopState)
//
// BOUNDS: preconditions (kept by deflateCompressFastest): len(in) - st.inNext
// >= 258; 0 <= st.inNext - st.inCurBase <= 32768; st.inNext < st.inMaxBlockEnd
// <= len(in); 0 <= st.seq < 8192; st.niceLen <= 258; every hash_tab entry > the
// cutoff of the current position is a position p (relative to st.inCurBase) of
// this buffer with 0 <= inCurBase + p < inNext. The loop runs only while
// inNext <= len(in) - 258 (checked before every position).
// Memory accesses:
// * in: per position the 4 bytes at inNext and at inNext+1 (< inNext+258); for
//   a candidate m = inCurBase + node (0 <= m < inNext): 4 bytes at m, 8 bytes
//   at m+4 / inNext+4, lz_extend's 8-byte words at m+n / inNext+n only while
//   n+8 <= 258 and single bytes at n < 258, and for the second candidate the 4
//   bytes at m+len-3 / inNext+len-3 with 4 <= len < nice_len <= 258: so only
//   [m, m+258) and [inNext, inNext+258), inside in. A literal reads in[inNext].
//   A skip (only if inNext+len+5 <= len(in)) reads the 4 bytes at inNext+2+i,
//   i < len-1, all before inNext+len+4 < len(in).
// * pf: C's two prefetchw (ht_matchfinder_longest_match: the next position's
//   bucket; ht_matchfinder_skip_bytes: the bucket after the skip) as
//   PRFM PSTL1KEEP of hash_tab[next_hash], next_hash = lz_hash >> 17 < 2^15
//   (inside the table; a prefetch never faults anyway);
//   hash_tab buckets hash * 4 bytes + [0, 4) (read, written; hash < 2^15:
//   st.nextHash is masked on entry, every later hash is an lz_hash >> 17) and
//   the slide over bytes [0, 131072) (read, written); sequences[seq..seq+1]
//   (written; seq+1 <= 8192, the 8193-entry array's last index, because the
//   loop stops once seq reaches 8192).
// * freqs: litlen[0..255] and litlen[257+length_slot] (length_slot <= 28),
//   offset[offset_slot] (offset_slot <= 29) (read, written). The read-only
//   tables deflateLengthSlot[3..258] and deflateOffsetSlot[0..255].
// * st (read, written); no frame.
// C: deflate_compress_fastest (the inner loop of a block)
TEXT ·fastestLoopAsm(SB), NOSPLIT, $0-48
	MOVD  pf+0(FP), R0
	MOVD  freqs+8(FP), R1
	MOVD  in_base+16(FP), R20
	MOVD  in_len+24(FP), R4
	MOVD  st+40(FP), R10
	ADD   R20, R4, R4
	SUB   $258, R4, R4
	MOVD  0(R10), R2
	ADD   R20, R2, R2
	MOVD  8(R10), R3
	ADD   R20, R3, R3
	MOVD  16(R10), R14
	ADD   R20, R14, R14           // &in[in_max_block_end]
	MOVD  24(R10), R5
	ADD   $SEQ_OFF, R0, R21
	ADD   R5<<3, R21, R5
	ADD   $SEQ_END, R0, R15
	MOVWU 32(R10), R6
	ANDW  $0x7FFF, R6, R6         // next_hash < 2^15 (the identity for the lz_hash Go passes)
	MOVWU 36(R10), R7
	MOVD  $·deflateOffsetSlot(SB), R16
	MOVD  $·deflateLengthSlot(SB), R17
	MOVW  $0x1E35A7BD, R19

step:
	CMP  R4, R2
	BHI  done                     // fewer than DEFLATE_MAX_MATCH_LEN bytes remain
	SUB  R3, R2, R9               // cur_pos
	CMP  $0x8000, R9
	BNE  noslide
	SLIDE(slide1)
	ADD  $0x8000, R3, R3          // in_cur_base += MATCHFINDER_WINDOW_SIZE
	MOVD ZR, R9

noslide:
	// ht_matchfinder_longest_match
	SUBW  $0x8000, R9, R8
	SXTH  R8, R8                  // cutoff
	MOVD  R6, R10                 // hash = next_hash (< 2^15)
	MOVWU 1(R2), R6
	MULW  R19, R6, R6
	LSRW  $17, R6, R6             // next_hash = lz_hash(in_next + 1)
	ADD   R6<<2, R0, R20
	PRFM  (R20), PSTL1KEEP        // prefetchw(&mf->hash_tab[next_hash])
	ADD   R10<<2, R0, R20         // the bucket
	MOVHU (R20), R11              // cur_node = bucket[0]
	MOVH  R9, (R20)               // bucket[0] = cur_pos
	SXTH  R11, R21
	CMP   R8, R21
	BLE   literal                 // a 16-bit mf_pos_t compare
	MOVH  2(R20), R12             // cur_node = bucket[1] (sign-extended)
	MOVH  R11, 2(R20)             // bucket[1] = to_insert
	ADD   R3, R21, R11            // matchptr
	MOVWU (R2), R9                // seq4
	MOVWU (R11), R21
	CMPW  R9, R21
	BNE   firstmiss

	// lz_extend(in_next, matchptr, 4, 258), its first word written out.
	MOVD 4(R11), R13
	MOVD 4(R2), R21
	EOR  R21, R13, R13
	CBZ  R13, ext12
	RBIT R13, R13
	CLZ  R13, R13
	LSR  $3, R13, R13
	ADD  $4, R13, R13
	JMP  second

ext12:
	MOVD $12, R13
	EXTEND(R11, R13, e1w, e1b, e1d, second)

second:
	// The second candidate, if in range and length < nice_len: its first
	// 4 bytes and the 4 bytes ending at offset length must match.
	CMP   R8, R12
	BLE   match
	CMPW  R7, R13
	BHS   match
	ADD   R3, R12, R12            // matchptr
	MOVWU (R12), R21
	CMPW  R9, R21
	BNE   match
	ADD   R13, R12, R21
	MOVWU -3(R21), R21
	ADD   R13, R2, R22
	MOVWU -3(R22), R22
	CMPW  R21, R22
	BNE   match
	JMP   ext4

firstmiss:
	MOVD  ZR, R13                 // length = 0
	CMP   R8, R12
	BLE   literal
	ADD   R3, R12, R12            // matchptr
	MOVWU (R12), R21
	CMPW  R9, R21
	BNE   literal

ext4:
	// R8 = lz_extend(in_next, matchptr, 4, 258); taken if > length.
	MOVD $4, R8
	EXTEND(R12, R8, e2w, e2b, e2d, e2done)

e2done:
	CMPW R13, R8
	BLS  match
	MOVD R8, R13
	MOVD R12, R11

match:
	// deflate_choose_match(c, length, offset, false, &seq)
	SUB   R11, R2, R10            // offset
	MOVW  $256, R9
	SUBW  R10, R9, R9
	LSRW  $29, R9, R9             // n = (256 - offset) >> 29
	SUBW  $1, R10, R12
	LSRW  R9, R12, R12
	MOVBU (R16)(R12), R12
	ADDW  R9<<1, R12, R12         // offset_slot
	MOVBU (R17)(R13), R8          // length_slot
	ADD   $FREQ_LEN, R1, R21
	MOVWU (R21)(R8<<2), R22
	ADDW  $1, R22, R22
	MOVW  R22, (R21)(R8<<2)       // litlen[DEFLATE_FIRST_LEN_SYM + length_slot]++
	ADD   $FREQ_OFF, R1, R21
	MOVWU (R21)(R12<<2), R22
	ADDW  $1, R22, R22
	MOVW  R22, (R21)(R12<<2)      // offset[offset_slot]++
	LSLW  $23, R13, R8
	MOVWU (R5), R21
	ORRW  R8, R21, R21
	MOVW  R21, (R5)               // litrunlen_and_length |= length << SEQ_LENGTH_SHIFT
	MOVH  R10, 4(R5)
	MOVH  R12, 6(R5)
	ADD   $8, R5, R5              // seq++
	MOVW  ZR, (R5)                // seq->litrunlen_and_length = 0

	// ht_matchfinder_skip_bytes(in_next + 1, length - 1)
	ADD  R13, R2, R9
	ADD  $5, R9, R9
	ADD  $258, R4, R8             // in_end
	CMP  R8, R9
	BHI  skipdone                 // count + 5 > in_end - (in_next + 1)
	ADD  $1, R2, R9
	SUB  R3, R9, R9               // pos
	ADD  R13, R9, R8
	SUB  $2, R8, R8
	CMP  $0x8000, R8
	BLT  skipnoslide              // pos + count - 1 < MATCHFINDER_WINDOW_SIZE
	SLIDE(slide2)
	ADD  $0x8000, R3, R3
	SUB  $0x8000, R9, R9

skipnoslide:
	ADD  $2, R2, R8               // the bytes hashed next
	SUBW $1, R13, R12             // count

skiploop:
	ADD   R6<<2, R0, R20
	MOVHU (R20), R11
	MOVH  R11, 2(R20)             // bucket[1] = bucket[0]
	MOVH  R9, (R20)               // bucket[0] = pos
	MOVWU (R8), R6
	MULW  R19, R6, R6
	LSRW  $17, R6, R6
	ADD   $1, R8, R8
	ADDW  $1, R9, R9
	SUBSW $1, R12, R12
	BNE   skiploop
	ADD   R6<<2, R0, R20
	PRFM  (R20), PSTL1KEEP        // prefetchw(&mf->hash_tab[hash])

skipdone:
	ADD R13, R2, R2               // in_next += length
	JMP endcheck

literal:
	// deflate_choose_literal(c, *in_next++, false, seq)
	MOVBU (R2), R10
	MOVWU (R1)(R10<<2), R21
	ADDW  $1, R21, R21
	MOVW  R21, (R1)(R10<<2)
	MOVWU (R5), R21
	ADDW  $1, R21, R21
	MOVW  R21, (R5)
	ADD   $1, R2, R2

endcheck:
	// in_next < in_max_block_end && seq < &sequences[FAST_SEQ_STORE_LENGTH]
	CMP R14, R2
	BHS done
	CMP R15, R5
	BLO step

done:
	MOVD in_base+16(FP), R20
	MOVD st+40(FP), R10
	SUB  R20, R2, R2
	MOVD R2, 0(R10)
	SUB  R20, R3, R3
	MOVD R3, 8(R10)
	ADD  $SEQ_OFF, R0, R21
	SUB  R21, R5, R5
	LSR  $3, R5, R5
	MOVD R5, 24(R10)
	MOVW R6, 32(R10)
	RET
