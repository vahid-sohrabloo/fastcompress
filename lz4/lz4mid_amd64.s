//go:build amd64 && !appengine && !noasm && !purego && gc

// The main loop of LZ4MID_compress (level 2; lz4 v1.11.1 lib/lz4hc.c,
// Copyright (c) Yann Collet, BSD 2-Clause) for a prefix with or without an
// external dictionary (no attached one), notLimited, limitedOutput or
// fillOutput (the last sequence is Go's). It follows midCompress in
// lz4hc.go statement for statement and returns before the last literals.

#include "textflag.h"

// LZ4MID_hash8Ptr: ((le64 << 8) * 58295818150454627) >> (64-14), with the
// shift folded into the multiplier (equal mod 2^64); R15 scratch
#define HASH8(ptr, dst) \
	MOVQ  (ptr), dst; \
	MOVQ  $0xcf1bbcdcbfa56300, R15; \
	IMULQ R15, dst; \
	SHRQ  $50, dst

// LZ4MID_hash4Ptr: (le32 * 2654435761) >> (32-14)
#define HASH4(ptr, dst) \
	MOVL   (ptr), dst; \
	IMUL3L $-1640531535, dst, dst; \
	SHRL   $18, dst

// COUNTMATCH(ipreg, ipIndexReg, posReg, out): LZ4MID_countMatch without
// external dictionary: 0 if ipIndex - pos > LZ4_DISTANCE_MAX or pos <
// prefixIdx, else LZ4_count(ip, win + pos - prefixIdx, matchlimit). The
// first 8 bytes are compared here (most candidates differ within them),
// the rest by midCount. Clobbers AX, BX, CX, DX.
#define COUNTMATCH(ipreg, ipIndexReg, posReg, out, slow, done) \
	XORL  out, out; \
	MOVL  ipIndexReg, AX; \
	SUBL  posReg, AX; \
	CMPL  AX, $65535; \
	JHI   done; \
	CMPL  posReg, R13; \
	JLO   done; \
	MOVL  posReg, BX; \
	SUBL  R13, BX; \
	ADDQ  R9, BX; \
	LEAQ  -7(R12), AX; \
	CMPQ  ipreg, AX; \
	JHS   slow; \
	MOVQ  (ipreg), AX; \
	XORQ  (BX), AX; \
	JEQ   slow; \
	BSFQ  AX, out; \
	SHRQ  $3, out; \
	JMP   done; \
slow: \
	MOVQ  ipreg, AX; \
	CALL  midCount<>(SB); \
	MOVQ  CX, out; \
done:

// Frame slots
#define F_OLIMIT 0
#define F_DSTEND 8
#define F_LOWLIM 88  // lowLimit (= prefixIdx without dictionary)
#define F_DICTB  96  // dictStart - lowLimit
#define F_R12    104 // matchlimit, while midCount runs with a nearer limit
#define F_DHASH  112 // attached dictionary (0: none): its hash table,
#define F_DCHAIN 120 // chain table (HC format),
#define F_DBASE  128 // prefixStart - dictLimit,
#define F_DEND   136 // end index (dictLimit + its prefix's length)
#define F_DMID   144 // and format (1: LZ4MID tables)
#define F_NB     152 // dictionary search: attempts left,
#define F_DOFF   160 // the candidate's offset,
#define F_DMI    168 // and dictMatchIndex

// midDictCtx offsets (TestMidDictCtxLayout)
#define D_HASH  0
#define D_CHAIN 8
#define D_BASE  16
#define D_END   24
#define D_MID   32

// DICTCOUNT(ipreg, posReg, out, tmp, done): LZ4MID_countMatch's external
// dictionary branch, pos < prefixIdx: 0 if pos < lowLimit, else
// LZ4_count(ip, dictStart + pos - lowLimit, ip + min(prefixIdx - pos,
// matchlimit - ip)). Clobbers AX, BX, CX, DX and tmp.
#define DICTCOUNT(ipreg, posReg, out, tmp, done) \
	CMPL  posReg, F_LOWLIM(SP); \
	JLO   done; \
	MOVL  R13, tmp; \
	SUBL  posReg, tmp; \
	ADDQ  ipreg, tmp; \
	CMPQ  tmp, R12; \
	CMOVQHI R12, tmp; \
	MOVL  posReg, BX; \
	ADDQ  F_DICTB(SP), BX; \
	MOVQ  R12, F_R12(SP); \
	MOVQ  tmp, R12; \
	MOVQ  ipreg, AX; \
	CALL  midCount<>(SB); \
	MOVQ  F_R12(SP), R12; \
	MOVQ  CX, out; \
	JMP   done

// COUNTMATCHX(ipreg, ipIndexReg, posReg, out): LZ4MID_countMatch: 0 if
// ipIndex - pos > LZ4_DISTANCE_MAX, DICTCOUNT if pos < prefixIdx, else
// LZ4_count(ip, win + pos - prefixIdx, matchlimit). The first 8 bytes are
// compared here (most candidates differ within them), the rest by
// midCount. Clobbers AX, BX, CX, DX.
#define COUNTMATCHX(ipreg, ipIndexReg, posReg, out, slow, dict, done) \
	XORL  out, out; \
	MOVL  ipIndexReg, AX; \
	SUBL  posReg, AX; \
	CMPL  AX, $65535; \
	JHI   done; \
	CMPL  posReg, R13; \
	JLO   dict; \
	MOVL  posReg, BX; \
	SUBL  R13, BX; \
	ADDQ  R9, BX; \
	LEAQ  -7(R12), AX; \
	CMPQ  ipreg, AX; \
	JHS   slow; \
	MOVQ  (ipreg), AX; \
	XORQ  (BX), AX; \
	JEQ   slow; \
	BSFQ  AX, out; \
	SHRQ  $3, out; \
	JMP   done; \
dict: \
	DICTCOUNT(ipreg, posReg, out, AX, done); \
slow: \
	MOVQ  ipreg, AX; \
	CALL  midCount<>(SB); \
	MOVQ  CX, out; \
done:

// func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int)
//
// win is the prefix (prefixPtr = &win[0], index i is position prefixIdx + i);
// the input is win[ip:iend]; t is the LZ4MID tables (hash4 then hash8, 16384
// entries each); olimit < 0 is notLimited (with fillOutput, oend). The
// external dictionary, if lowLimit < prefixIdx, is the prefixIdx - lowLimit
// bytes at dict; d, if not nil, is an attached dictionary (dictCtx, in HC or
// LZ4MID format: LZ4HC_searchExtDict with 2 attempts or
// LZ4MID_searchExtDict where no match was found, within 64 KB - 8 of the
// dictionary's end). It returns ip, anchor and op at the end of the main loop
// (status 0), or status 1 when a sequence does not fit (limitedOutput: C
// returns 0; fillOutput: ip, anchor and op before the sequence, ml and md
// its match length and distance).
//
// Registers: SI ip, DI op, R8 anchor, R9 win, R10 hash4 table, R11 mflimit,
// R12 matchlimit, R13 prefixIdx, R14 ipIndex, R15 hash scratch; AX, BX, CX,
// DX scratch; locals 0 olimit, 8 dstEnd, 16 ilimitIdx, 24 matchLength,
// 32 matchDistance, 40 hash8 table, 48 dst, 56 and 64 the hash and the
// candidate at ip+1, 72 and 80 the hash4 and its entry, 88 to 104 the
// F_ slots.
//
// Preconditions (checked by midCompress): iend - ip >= LZ4_minLength,
// iend <= len(win), and the input is the end of win (iend = len(win));
// every table entry is a position below the current one (prefixIdx +
// index); len(dst) >= olimit (limited) or LZ4_compressBound(iend - ip).
//
// BOUNDS:
// (1) win reads: ip <= mflimit = iend-12 in the loop; hashes read 8 bytes at
//     ip, ip+1 and ip+2 (catch back moves ip down only), and at ip-5, ip-3,
//     ip-2 and 4 bytes at ip-1 after a match whose end is below ilimit+2,
//     i.e. ip-2 < iend-8, so these reads end below iend; ip-5 is read only
//     when ip > 5 (C's check), ip-3 > anchor >= 0 after a match of at least
//     4 bytes. Candidates are read only at positions >= prefixIdx (index
//     >= 0) and below ip (table entries are earlier positions): 8 bytes at
//     ip and at the candidate when ip < matchlimit - 7 (so below matchlimit
//     = iend-5, and the candidate below ip + 8), else and past a full 8-byte
//     match by midCount, which reads below matchlimit on the ip side and at
//     the same distance behind on the match side. A dictionary candidate
//     (lowLimit <= pos < prefixIdx) is counted by midCount with the limit
//     ip + min(prefixIdx - pos, matchlimit - ip), so its side ends at or
//     before dictEnd. An attached dictionary's candidate dmi is read only
//     when ipIndex - (dmi + lowLimit - dEnd) <= 65535, and (HC format) its
//     4 bytes at dBase + dmi match ip's, then counted by midCount below
//     ip + min(dEnd - dmi, matchlimit - ip), i.e. not past the
//     dictionary's end (LZ4MID format: from ip, the same limit); the
//     dictionary's entries are its own positions, at or above its
//     dictLimit, or end the search by distance (as C's). The catch back reads
//     ip[-1] (ip > anchor >= 0) and ip[-dist-1] with ip > dist.
// (2) dst writes: before a sequence, op + litLen/255 + litLen + 8 <= olimit
//     (limited) is checked; with notLimited dst holds the bound. Every
//     write is also checked against dstEnd (status 2, which cannot happen
//     within these limits): the literal run bytes, the offset, the match run
//     below op + mlCode/255 + 2; the literals are copied in 8-byte chunks
//     when op+litLen+8 <= dstEnd, else byte by byte.
TEXT ·midCompressAsm(SB), NOSPLIT, $176-160
#include "lz4mid_amd64.h"

// func midCompressDictAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int)
//
// midCompressAsm with the external dictionary (lowLimit < prefixIdx) or
// the attached one (d), whose candidates the prefix-only routine skips.
//
// BOUNDS: as midCompressAsm's (the dictionary paths included).
#define MID_DICT
TEXT ·midCompressDictAsm(SB), NOSPLIT, $176-160
#include "lz4mid_amd64.h"
#undef MID_DICT

// midCount: CX = LZ4_count(AX, BX, R12): the common prefix of the bytes at
// AX (below matchlimit R12) and at BX. Clobbers AX, BX, DX.
//
// BOUNDS: 16-byte loads at AX while AX < R12-15, 8-byte loads while AX <
// R12-7, then 4, 2 and 1 bytes while they fit below R12; BX advances with
// AX and lies behind it.
TEXT midCount<>(SB), NOSPLIT, $0
	MOVQ AX, CX

mc16:
	LEAQ -15(R12), DX
	CMPQ CX, DX
	JHS  mc8
	MOVOU (CX), X0
	MOVOU (BX), X1
	PCMPEQB X1, X0
	PMOVMSKB X0, DX
	XORL $0xFFFF, DX
	JNE  mc16Diff
	ADDQ $16, CX
	ADDQ $16, BX
	JMP  mc16

mc16Diff:
	BSFL DX, DX
	ADDQ DX, CX
	SUBQ AX, CX
	RET

mc8:
	LEAQ -7(R12), DX
	CMPQ CX, DX
	JHS  mcTail
	MOVQ (CX), DX
	XORQ (BX), DX
	JNE  mcDiff
	ADDQ $8, CX
	ADDQ $8, BX
	JMP  mc8

mcDiff:
	BSFQ DX, DX
	SHRQ $3, DX
	ADDQ DX, CX
	SUBQ AX, CX
	RET

mcTail:
	LEAQ -3(R12), DX
	CMPQ CX, DX
	JHS  mc2
	MOVL (CX), DX
	CMPL DX, (BX)
	JNE  mc2
	ADDQ $4, CX
	ADDQ $4, BX

mc2:
	LEAQ -1(R12), DX
	CMPQ CX, DX
	JHS  mc1
	MOVWLZX (CX), DX
	CMPW DX, (BX)
	JNE  mc1
	ADDQ $2, CX
	ADDQ $2, BX

mc1:
	CMPQ CX, R12
	JHS  mcDone
	MOVB (CX), DX
	CMPB DL, (BX)
	JNE  mcDone
	INCQ CX

mcDone:
	SUBQ AX, CX
	RET
