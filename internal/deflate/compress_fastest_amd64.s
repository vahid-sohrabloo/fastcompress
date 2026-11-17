//go:build amd64 && !appengine && !noasm && !purego && gc

// The per-position loop of deflate_compress_fastest of libdeflate v1.26
// lib/deflate_compress.c (Copyright 2016 Eric Biggers, MIT; see NOTICE), with
// ht_matchfinder_longest_match, ht_matchfinder_skip_bytes
// (lib/ht_matchfinder.h, HT_MATCHFINDER_BUCKET_SIZE == 2), lz_extend
// (lib/matchfinder_common.h), deflate_choose_literal / deflate_choose_match
// (gather_split_stats = false), ht_matchfinder_slide_window (the SSE2
// matchfinder_rebase) written in, as gcc -O2 forceinlines them. It runs only
// while at least DEFLATE_MAX_MATCH_LEN bytes remain, so max_len is 258 and
// nice_len fixed; the Go loop (compress_fastest.go) does the rest. Semantics
// are those of that Go loop, statement for statement.
//
// Preemption: fastestLoopAsm is NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside it. It returns at the block end,
// so one call handles at most FAST_SOFT_MAX_BLOCK_LENGTH + MIN_BLOCK_LENGTH -
// 1 (70534) positions (choose_max_block_end extends the last block of the
// input up to that), usually at most 65535 or 8192 matches. BP is not used.
//
// Register use:
//   DI  pf (ht_mf at 0, sequences at SEQ_OFF)   R9  freqs (litlen, offset)
//   BX  in_next                                 R13 in_cur_base
//   R14 in_end - DEFLATE_MAX_MATCH_LEN          R8  seq (&sequences[i])
//   R10 next_hash                               R11 nice_len
//   R15 cutoff (search), scratch (match/skip)   CX  cur_pos / seq4 / pos
//   AX  hash (bucket index), scratch            DX  node, matchptr / best match
//   R12 second node, then its matchptr          SI  length
// Locals: blockend-8(SP) = &in[in_max_block_end], seqend-16(SP) =
// &sequences[FAST_SEQ_STORE_LENGTH].
// As gcc does, the buckets are indexed by the hash itself (no mask: it is <
// 2^15), bucket[0] is loaded zero-extended (MOVWLZX) and compared with the
// cutoff in 16 bits (CMPW: both are mf_pos_t), and only a node in range is
// sign-extended for the match pointer; with the mask, the LEAQ and a
// sign-extending load before that compare the loop was 4-6% slower (T17.1).

#include "textflag.h"

// fastestState byte offsets (asserted in compress_fastest_amd64.go).
#define SEQ_OFF 131072
#define SEQ_END 196608
// deflateFreqs byte offsets: litlen[DEFLATE_FIRST_LEN_SYM], offset[0].
#define FREQ_LEN 1028
#define FREQ_OFF 1152

// SLIDE rebases the 65536 hash_tab entries at DI (PADDSW of 0x8000, 64 bytes
// per iteration); clobbers AX, DX, X0-X4. lbl is a unique loop label.
#define SLIDE(lbl) \
	MOVL   $0x80008000, AX; \
	MOVQ   AX, X4; \
	PSHUFD $0, X4, X4; \
	MOVQ   DI, DX; \
	MOVL   $2048, AX; \
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
	DECL   AX; \
	JNZ    lbl

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
//   PREFETCHT0 of hash_tab[next_hash], next_hash = lz_hash >> 17 < 2^15 (inside
//   the table; a prefetch never faults anyway);
//   hash_tab buckets hash * 4 bytes + [0, 4) (read, written; hash < 2^15:
//   st.nextHash is masked on entry, every later hash is an lz_hash >> 17) and
//   the slide over bytes [0, 131072) (read, written); sequences[seq..seq+1]
//   (written; seq+1 <= 8192, the 8193-entry array's last index, because the
//   loop stops once seq reaches 8192).
// * freqs: litlen[0..255] and litlen[257+length_slot] (length_slot <= 28),
//   offset[offset_slot] (offset_slot <= 29) (read, written). The read-only
//   tables deflateLengthSlot[3..258] and deflateOffsetSlot[0..255].
// * st (read, written) and the 16-byte frame.
// C: deflate_compress_fastest (the inner loop of a block)
TEXT ·fastestLoopAsm(SB), NOSPLIT, $16-48
	MOVQ pf+0(FP), DI
	MOVQ freqs+8(FP), R9
	MOVQ in_base+16(FP), SI
	MOVQ in_len+24(FP), R14
	MOVQ st+40(FP), AX
	LEAQ -258(SI)(R14*1), R14
	MOVQ 0(AX), BX
	ADDQ SI, BX
	MOVQ 8(AX), R13
	ADDQ SI, R13
	MOVQ 16(AX), CX
	ADDQ SI, CX
	MOVQ CX, blockend-8(SP)
	MOVQ 24(AX), R8
	LEAQ SEQ_OFF(DI)(R8*8), R8
	LEAQ SEQ_END(DI), CX
	MOVQ CX, seqend-16(SP)
	MOVL 32(AX), R10
	ANDL $0x7FFF, R10         // next_hash < 2^15 (the identity for the lz_hash Go passes)
	MOVL 36(AX), R11

step:
	CMPQ BX, R14
	JHI  done                 // fewer than DEFLATE_MAX_MATCH_LEN bytes remain
	MOVQ BX, CX
	SUBQ R13, CX              // cur_pos
	CMPL CX, $0x8000
	JNE  noslide
	SLIDE(slide1)
	ADDQ $0x8000, R13         // in_cur_base += MATCHFINDER_WINDOW_SIZE
	XORL CX, CX

noslide:
	// ht_matchfinder_longest_match
	LEAL    -0x8000(CX), R15
	MOVWQSX R15, R15          // cutoff
	MOVL    R10, AX           // hash = next_hash (< 2^15)
	MOVL    1(BX), R10
	IMUL3L  $0x1E35A7BD, R10, R10
	SHRL    $17, R10          // next_hash = lz_hash(in_next + 1)
	PREFETCHT0 (DI)(R10*4)    // prefetchw(&mf->hash_tab[next_hash])
	MOVWLZX (DI)(AX*4), DX    // cur_node = bucket[0] (zero-extended: gcc's movzwl)
	MOVW    CX, (DI)(AX*4)    // bucket[0] = cur_pos
	CMPW    DX, R15           // 16-bit mf_pos_t compare (gcc's cmp %r8w,%dx)
	JLE     literal
	MOVWQSX 2(DI)(AX*4), R12  // cur_node = bucket[1]
	MOVW    DX, 2(DI)(AX*4)   // bucket[1] = to_insert
	MOVWQSX DX, DX
	ADDQ    R13, DX           // matchptr
	MOVL    (BX), CX          // seq4
	CMPL    (DX), CX
	JNE     firstmiss

	// lz_extend(in_next, matchptr, 4, 258), its first word written out.
	MOVQ   4(DX), SI
	XORQ   4(BX), SI
	JZ     ext12
	TZCNTQ SI, SI             // nonzero: TZCNT (REP BSF) = BSF
	SHRL   $3, SI
	ADDL   $4, SI
	JMP    second

ext12:
	MOVL $12, SI              // 258 - 12 >= 32: four words, then the loop
	MOVQ (DX)(SI*1), AX
	XORQ (BX)(SI*1), AX
	JNZ  e1d0
	MOVQ 8(DX)(SI*1), AX
	XORQ 8(BX)(SI*1), AX
	JNZ  e1d8
	MOVQ 16(DX)(SI*1), AX
	XORQ 16(BX)(SI*1), AX
	JNZ  e1d16
	MOVQ 24(DX)(SI*1), AX
	XORQ 24(BX)(SI*1), AX
	JNZ  e1d24
	ADDL $32, SI

e1w:
	LEAL 8(SI), AX
	CMPL AX, $258
	JA   e1b
	MOVQ (DX)(SI*1), AX
	XORQ (BX)(SI*1), AX
	JNZ  e1d0
	ADDL $8, SI
	JMP  e1w

e1b:
	CMPL    SI, $258
	JAE     second
	MOVBLZX (DX)(SI*1), AX
	CMPB    AX, (BX)(SI*1)
	JNE     second
	INCL    SI
	JMP     e1b

e1d24:
	ADDL $8, SI

e1d16:
	ADDL $8, SI

e1d8:
	ADDL $8, SI

e1d0:
	TZCNTQ AX, AX
	SHRL   $3, AX
	ADDL   AX, SI

second:
	// The second candidate, if in range and length < nice_len: its first
	// 4 bytes and the 4 bytes ending at offset length must match.
	CMPQ R12, R15
	JLE  match
	CMPL SI, R11
	JAE  match
	ADDQ R13, R12             // matchptr
	CMPL (R12), CX
	JNE  match
	MOVL -3(R12)(SI*1), AX
	CMPL AX, -3(BX)(SI*1)
	JNE  match
	JMP  ext4

firstmiss:
	XORL SI, SI               // length = 0
	CMPQ R12, R15
	JLE  literal
	ADDQ R13, R12             // matchptr
	CMPL (R12), CX
	JNE  literal

ext4:
	// R15 = lz_extend(in_next, matchptr, 4, 258); taken if > length.
	MOVL $4, R15
	MOVQ (R12)(R15*1), AX
	XORQ (BX)(R15*1), AX
	JNZ  e2d0
	MOVQ 8(R12)(R15*1), AX
	XORQ 8(BX)(R15*1), AX
	JNZ  e2d8
	MOVQ 16(R12)(R15*1), AX
	XORQ 16(BX)(R15*1), AX
	JNZ  e2d16
	MOVQ 24(R12)(R15*1), AX
	XORQ 24(BX)(R15*1), AX
	JNZ  e2d24
	ADDL $32, R15

e2w:
	LEAL 8(R15), AX
	CMPL AX, $258
	JA   e2b
	MOVQ (R12)(R15*1), AX
	XORQ (BX)(R15*1), AX
	JNZ  e2d0
	ADDL $8, R15
	JMP  e2w

e2b:
	CMPL    R15, $258
	JAE     e2done
	MOVBLZX (R12)(R15*1), AX
	CMPB    AX, (BX)(R15*1)
	JNE     e2done
	INCL    R15
	JMP     e2b

e2d24:
	ADDL $8, R15

e2d16:
	ADDL $8, R15

e2d8:
	ADDL $8, R15

e2d0:
	TZCNTQ AX, AX
	SHRL   $3, AX
	ADDL   AX, R15

e2done:
	CMPL R15, SI
	JBE  match
	MOVL R15, SI
	MOVQ R12, DX

match:
	// deflate_choose_match(c, length, offset, false, &seq)
	MOVQ    BX, AX
	SUBQ    DX, AX            // offset
	MOVL    $256, CX
	SUBL    AX, CX
	SHRL    $29, CX           // n = (256 - offset) >> 29
	LEAL    -1(AX), R12
	SHRL    CX, R12
	LEAQ    ·deflateOffsetSlot(SB), R15
	MOVBLZX (R15)(R12*1), R12
	LEAL    (R12)(CX*2), R12  // offset_slot
	LEAQ    ·deflateLengthSlot(SB), R15
	MOVBLZX (R15)(SI*1), R15
	INCL    FREQ_LEN(R9)(R15*4) // litlen[DEFLATE_FIRST_LEN_SYM + length_slot]++
	INCL    FREQ_OFF(R9)(R12*4) // offset[offset_slot]++
	MOVL    SI, R15
	SHLL    $23, R15
	ORL     R15, (R8)         // litrunlen_and_length |= length << SEQ_LENGTH_SHIFT
	MOVW    AX, 4(R8)
	MOVW    R12, 6(R8)
	ADDQ    $8, R8            // seq++
	MOVL    $0, (R8)          // seq->litrunlen_and_length = 0

	// ht_matchfinder_skip_bytes(in_next + 1, length - 1)
	LEAQ 5(BX)(SI*1), CX
	LEAQ 258(R14), R15        // in_end
	CMPQ CX, R15
	JHI  skipdone             // count + 5 > in_end - (in_next + 1)
	LEAQ 1(BX), CX
	SUBQ R13, CX              // pos
	LEAQ -2(CX)(SI*1), R15
	CMPQ R15, $0x8000
	JLT  skipnoslide          // pos + count - 1 < MATCHFINDER_WINDOW_SIZE
	SLIDE(slide2)
	ADDQ $0x8000, R13
	SUBQ $0x8000, CX

skipnoslide:
	LEAQ 2(BX), R15           // the bytes hashed next
	LEAL -1(SI), R12          // count

skiploop:
	MOVW   (DI)(R10*4), DX
	MOVW   DX, 2(DI)(R10*4)   // bucket[1] = bucket[0]
	MOVW   CX, (DI)(R10*4)    // bucket[0] = pos
	MOVL   (R15), R10
	IMUL3L $0x1E35A7BD, R10, R10
	SHRL   $17, R10
	INCQ   R15
	INCL   CX
	DECL   R12
	JNZ    skiploop
	PREFETCHT0 (DI)(R10*4)    // prefetchw(&mf->hash_tab[hash])

skipdone:
	ADDQ SI, BX               // in_next += length
	JMP  endcheck

literal:
	// deflate_choose_literal(c, *in_next++, false, seq)
	MOVBLZX (BX), AX
	INCL    (R9)(AX*4)
	INCL    (R8)
	INCQ    BX

endcheck:
	// in_next < in_max_block_end && seq < &sequences[FAST_SEQ_STORE_LENGTH]
	CMPQ BX, blockend-8(SP)
	JAE  done
	CMPQ R8, seqend-16(SP)
	JB   step

done:
	MOVQ in_base+16(FP), SI
	MOVQ st+40(FP), AX
	SUBQ SI, BX
	MOVQ BX, 0(AX)
	SUBQ SI, R13
	MOVQ R13, 8(AX)
	LEAQ SEQ_OFF(DI), CX
	SUBQ CX, R8
	SHRQ $3, R8
	MOVQ R8, 24(AX)
	MOVL R10, 32(AX)
	RET
