//go:build arm64 && !appengine && !noasm && !purego && gc

// The fast decode loop of lz4 v1.11.1 lib/lz4.c (Copyright (c) Yann Collet,
// BSD 2-Clause): LZ4_decompress_generic's loop under LZ4_FAST_DEC_LOOP, for
// a block without external dictionary (noDict, history in the same buffer
// before op). It follows lz4_decompress_amd64.s (and so the Go port in
// lz4_decompress.go) step for step and stops where C leaves the fast loop:
// at a sequence start for the safe loop (status 0), or with a decoded match
// for safe_match_copy (status 1). Matches are copied with LZ77 semantics;
// the bytes written past the end of a literal run or a match lie below oend
// and are overwritten by later output, as C's wild copies. Baseline ARMv8.0
// only (LDP/STP and NEON VLD1/VST1 for the wide copies).

#include "textflag.h"

// distance of the 8-byte chunk copy that follows the first 8 bytes of a
// match with offset 1..7 (the smallest multiple of the offset >= 8)
DATA lz4dist<>+0(SB)/8, $0
DATA lz4dist<>+8(SB)/8, $8
DATA lz4dist<>+16(SB)/8, $8
DATA lz4dist<>+24(SB)/8, $9
DATA lz4dist<>+32(SB)/8, $8
DATA lz4dist<>+40(SB)/8, $10
DATA lz4dist<>+48(SB)/8, $12
DATA lz4dist<>+56(SB)/8, $14
GLOBL lz4dist<>(SB), RODATA|NOPTR, $64

// func decodeFastAsm(src, out []byte, ip, op, oend, lowPrefix int) (nip, nop, offset, ml, status int)
//
// Registers: R0 ip, R1 op (pointers), R2 token, then cpy (op+ml), R3
// literal / match length, R8 offset, R9 match, R10 lowPrefix, R11
// iend-18, R12 oend-64, R13 iend-15 (RUN_MASK), R14 iend-6, R15 iend-64;
// R4-R7, V0-V3 scratch.
//
// Preconditions (checked by decompressGeneric): 0 <= ip < len(src);
// 0 <= lowPrefix <= op; op + 64 <= oend <= len(out).
//
// Status: 0 leave for the safe loop at the token at nip (op = nop);
// 1 safe_match_copy with offset and ml decoded (literals copied, nip after
// the match length); 2 corrupt input (C -1); 3 offset before lowPrefix (C -3).
//
// BOUNDS:
// (1) Loop invariant: ip < iend and op <= oend-64. It holds on entry. A
//     sequence ends (2) with op' = op+ll+ml < oend-64 (else status 1 before
//     any write of the match), and ip' <= iend-1: a short literal run leaves
//     ip <= iend-17+14, then the offset, so ip' <= iend-1; a long one leaves
//     ip+ll <= iend-64; the match length extension reads only below iend-6.
// (2) Reads of src: the token at ip < iend; the 16-byte literal copy at
//     ip+1 <= iend-17 (checked before) reads below iend-1; length extension
//     bytes below iend-15 (literals) or iend-6 (match), checked before each
//     byte; a long literal run is copied in 64-byte chunks only when
//     ll <= iend-64-ip, so its reads end below ip+ll+64 <= iend; the 2-byte
//     offset at ip <= iend-2.
// (3) Writes of literals: 16 bytes at op <= oend-64; a long run in 64-byte
//     chunks only when ll <= oend-64-op, so below op+ll+64 <= oend.
// (4) Matches: after the literals op <= oend-64+14 (short) or <= oend-64
//     (long), and ml < oend-64-op (else status 1), so cpy = op+ml <
//     oend-64. Every match copy reads at match = op-offset >= lowPrefix >= 0
//     (else status 3) and below its own write position (offset >= 1;
//     offset 0 writes zeros without reading), and writes below cpy+64 <=
//     oend: the 18-byte copy below op+18 <= oend-32; the 64-, 32- and 8-byte
//     chunk loops stop at the first chunk ending at or beyond cpy, so they
//     write below cpy+64, cpy+32 and cpy+8; the 16-byte pattern stores of
//     offsets 0, 1, 2 and 4 (which read only the offset's bytes at match)
//     stop likewise, below cpy+16.
TEXT ·decodeFastAsm(SB), NOSPLIT, $0-120
#include "lz4_decompress_arm64.h"

#define DEC_PREFIX64K

// func decodeFastPrefix64kAsm(src, out []byte, ip, op, oend, lowPrefix int) (nip, nop, offset, ml, status int)
//
// decodeFastAsm for at least 64 KB of history: op - lowPrefix >= 65535 on
// entry (C: withPrefix64k, no checkOffset).
//
// BOUNDS: as decodeFastAsm's; a match starts at op - offset >= op0 - 65535
// >= lowPrefix >= 0 (op only grows, offset <= 65535), so status 3 cannot
// occur and the dropped checks could not fail.
TEXT ·decodeFastPrefix64kAsm(SB), NOSPLIT, $0-120
#include "lz4_decompress_arm64.h"
#undef DEC_PREFIX64K
