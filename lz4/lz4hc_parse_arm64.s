//go:build arm64 && !appengine && !noasm && !purego && gc

// The parsers of LZ4HC (lz4 v1.11.1 lib/lz4hc.c, Copyright (c) Yann
// Collet, BSD 2-Clause) for a prefix without dictionary (external or
// attached: those blocks run the Go parsers on arm64), notLimited,
// limitedOutput or fillOutput (destSize; the last sequence and literals are
// Go's): LZ4HC_compress_hashChain's main loop (levels 3 to 9) and
// LZ4HC_compress_optimal_internal's (levels 10 to 12, no favorDecSpeed),
// each with LZ4HC_InsertAndGetWiderMatch (hcWider) in one routine. They
// follow compressHashChain and compressOptimal in lz4hc.go and lz4hc_opt.go
// statement for statement (and lz4hc_parse_amd64.s's prefix-only routines
// step for step) and return before the last literals.
//
// Register mapping against lz4hc_parse_amd64.s: SI R1, R8 R2, R9 R3, R10
// R4, R11 R5, R12 R6, R13 R7, R14 R8, R15 R9, BX R10, DI R11, AX R12, CX
// R13, DX R14; R16, R17 scratch; R19 the LZ4HC hash multiplier, R21
// 0x80808081 (x / 255 = x * R21 >> 39), R22 iHighLimit - 7 (W_HLIM7). The
// frame slots are those of the amd64 file at SL(x) = x+8 from RSP; the
// helpers are NOFRAME leaves reached by BL, so they see the same slots.

#include "textflag.h"

#define SL(x) (x+8)(RSP)

// The wider-match area at the start of each parser's frame.
#define W_PAT    0
#define W_MAXLEN 8
#define W_LOWEST 16
#define W_OFF    24
#define W_SBACK  32
#define W_REPEAT 40
#define W_SRCPL  48
#define W_BACK   56
#define W_NB     64
#define W_CAND   72
#define W_MP     80
#define W_FWD    88
#define W_HASH   96
#define W_NEXT   104
#define W_PA     112
#define W_CPOS   136
#define W_END    160
#define W_SIZE   208

// hcParseArgs offsets (TestHCParseArgsLayout)
#define A_WIN     0
#define A_HASH    8
#define A_CHAIN   16
#define A_PREFIX  24
#define A_IP      32
#define A_ANCHOR  40
#define A_OP      48
#define A_IEND    56
#define A_DST     64
#define A_DSTLEN  72
#define A_OLIMIT  80
#define A_NEXT    88
#define A_ATT     96
#define A_PA      104
#define A_STOP    112
#define A_SUFF    120
#define A_OPT     128
#define A_MLEN    152
#define A_MOFF    160

// COUNTPAT(labels): R13 = the end of the repetition of the pattern in R20
// (32 bits, zero-extended) from R13, below R5 (LZ4HC_countPattern: the
// count is the end minus the start). Clobbers R7, R12, R14.
//
// BOUNDS: 8-byte loads at R13 while R13 < R5 - 7, then single bytes below
// R5.
#define COUNTPAT(l8, ldiff, ltail, ldone) \
	ORR   R20<<32, R20, R7; \
	SUB   $7, R5, R14; \
l8: \
	CMP   R14, R13; \
	BHS   ltail; \
	MOVD  (R13), R12; \
	EOR   R7, R12, R12; \
	CBNZ  R12, ldiff; \
	ADD   $8, R13, R13; \
	JMP   l8; \
ldiff: \
	RBIT  R12, R12; \
	CLZ   R12, R12; \
	ADD   R12>>3, R13, R13; \
	JMP   ldone; \
ltail: \
	CMP   R5, R13; \
	BHS   ldone; \
	MOVBU (R13), R12; \
	ANDW  $0xFF, R7, R14; \
	CMPW  R14, R12; \
	BNE   ldone; \
	ADD   $1, R13, R13; \
	LSR   $8, R7, R7; \
	JMP   ltail; \
ldone:

// hcWider: LZ4HC_InsertAndGetWiderMatch for a prefix without dictionary
// (every candidate is at or above lowestMatchIndex >= lowLimit = prefixIdx).
// Three variants (lz4hc_wider_arm64.h): hcWider (levels 3 to 8), hcWiderPA
// (the pattern analysis, level 9) and hcWiderOpt (the pattern analysis and
// chainSwap, levels 10 to 12).
//
// Preconditions (checked by the parsers' Go callers or their own
// arguments): 0 <= iLowLimit <= ip < iHighLimit <= the end of win; longest
// > ip - iLowLimit (lookBack); with chainSwap, iLowLimit == ip;
// nextToUpdate >= prefixIdx; table and chain entries reached from ipIndex
// are earlier positions (inserted ones have chain distances >= 1).
//
// BOUNDS:
// (1) Insert: positions nextToUpdate <= idx < ipIndex are read as 4 bytes
//     at win + idx - prefixIdx <= ip - 1, below iHighLimit; the hash is 15
//     bits (the table's 32768 entries); the chain is written at (uint16)idx.
// (2) A candidate is read only when lowestMatchIndex <= matchIndex
//     (lowestMatchIndex >= prefixIdx) and matchIndex < ipIndex (earlier
//     positions; the pattern analysis only moves matchIndex down, or to
//     prefixIdx), so mp = win + matchIndex - prefixIdx is in [win, ip).
// (3) The 2-byte probe at iLowLimit + longest - 1 ends at iLowLimit +
//     longest + 1 <= iLowLimit + maxLength = iHighLimit (the loop runs while
//     longest < maxLength). The one at mp - lookBack + longest - 1 is at
//     least mp (longest > lookBack) and ends before ip + longest - lookBack
//     <= iHighLimit.
// (4) countBack reads [ip+back-4, ip+back) and [mp+back-4, mp+back) while
//     back - 4 >= max(iLowLimit - ip, win - mp), then single bytes above
//     the same minimum. count reads ip+4.. below iHighLimit and mp+4.. at
//     the same distance behind.
// (5) The pattern analysis reads 4 bytes at a candidate mp in [win, ip)
//     (as in (2)), counts forward 8 bytes at a time while 8 bytes remain
//     below iHighLimit then bytewise below it, and backward 4 bytes at a
//     time while 4 bytes remain above win, then bytewise above it.
// (6) The chain is read at (uint16)(matchIndex + pos), within its 65536
//     entries.
TEXT hcWider<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_arm64.h"

// hcWiderPA: hcWider with the pattern analysis.
//
// BOUNDS: as hcWider's.
#define WIDER_PA
TEXT hcWiderPA<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_arm64.h"

// hcWiderOpt: hcWiderPA with chainSwap (lookBack 0).
//
// BOUNDS: as hcWider's.
#define WIDER_SWAP
TEXT hcWiderOpt<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_arm64.h"
#undef WIDER_SWAP
#undef WIDER_PA

// ENCODE(LEN, OFF, ...labels): LZ4HC_encodeSequence(ip, op, anchor; the
// frame's P_IP, P_OP and P_ANCHOR) with the match length and offset at
// SL(LEN) and SL(OFF); updates P_IP, P_ANCHOR and P_OP. Jumps to fail when
// the sequence does not fit the limit (limitedOutput), to fail2 when a write
// would pass the end of dst (cannot happen within the checked limits).
// Clobbers R3, R4, R8, R10-R14, R16.
//
// BOUNDS: sequences start at ip <= mflimit = iend - 12, so the literals
// copied 8 bytes at a time from anchor < ip end before iend; every dst
// write is checked against dstEnd.
#define ENCODE(LEN, OFF, fail, fail2, litRun, litRunLoop, litRunLast, copyLits, copyLits8, exactLits, offsetOut, mlRun, mlRunLoop, mlRunLast, mlDone) \
	MOVD  SL(P_IP), R3; \
	MOVD  SL(P_ANCHOR), R4; \
	MOVD  SL(P_OP), R11; \
	SUB   R4, R3, R13; \
	MUL   R21, R13, R12; \
	LSR   $39, R12, R12; \
	ADD   R13, R11, R14; \
	ADD   R12, R14, R14; \
	ADD   $9, R14, R14; \
	MOVD  SL(P_OLIM), R16; \
	CMP   R16, R14; \
	BHI   fail; \
	MOVD  R11, R8; \
	ADD   $1, R11, R11; \
	CMP   $15, R13; \
	BHS   litRun; \
	LSLW  $4, R13, R12; \
	MOVB  R12, (R8); \
	JMP   copyLits; \
litRun: \
	MOVD  $0xF0, R12; \
	MOVB  R12, (R8); \
	SUB   $15, R13, R12; \
	ADD   R12, R11, R14; \
	MOVD  SL(P_DSTEND), R16; \
	CMP   R16, R14; \
	BHS   fail2; \
	MOVD  $255, R16; \
litRunLoop: \
	CMP   $255, R12; \
	BLO   litRunLast; \
	MOVB  R16, (R11); \
	ADD   $1, R11, R11; \
	SUB   $255, R12, R12; \
	JMP   litRunLoop; \
litRunLast: \
	MOVB  R12, (R11); \
	ADD   $1, R11, R11; \
copyLits: \
	ADD   R13, R11, R10; \
	MOVD  R4, R12; \
	ADD   $8, R10, R14; \
	MOVD  SL(P_DSTEND), R16; \
	CMP   R16, R14; \
	BHI   exactLits; \
copyLits8: \
	MOVD.P 8(R12), R14; \
	MOVD.P R14, 8(R11); \
	CMP   R10, R11; \
	BLO   copyLits8; \
	MOVD  R10, R11; \
	JMP   offsetOut; \
exactLits: \
	CMP   R10, R11; \
	BHS   offsetOut; \
	MOVBU.P 1(R12), R14; \
	MOVB.P R14, 1(R11); \
	JMP   exactLits; \
offsetOut: \
	ADD   $2, R11, R12; \
	MOVD  SL(P_DSTEND), R16; \
	CMP   R16, R12; \
	BHI   fail2; \
	MOVD  SL(OFF), R12; \
	MOVH  R12, (R11); \
	ADD   $2, R11, R11; \
	MOVD  SL(LEN), R13; \
	SUB   $4, R13, R13; \
	MUL   R21, R13, R12; \
	LSR   $39, R12, R12; \
	ADD   R12, R11, R14; \
	ADD   $6, R14, R14; \
	MOVD  SL(P_OLIM), R16; \
	CMP   R16, R14; \
	BHI   fail; \
	ADD   R12, R11, R14; \
	ADD   $2, R14, R14; \
	MOVD  SL(P_DSTEND), R16; \
	CMP   R16, R14; \
	BHI   fail2; \
	CMP   $15, R13; \
	BHS   mlRun; \
	MOVBU (R8), R12; \
	ADD   R13, R12, R12; \
	MOVB  R12, (R8); \
	JMP   mlDone; \
mlRun: \
	MOVBU (R8), R12; \
	ADD   $15, R12, R12; \
	MOVB  R12, (R8); \
	SUB   $15, R13, R13; \
	MOVD  $255, R16; \
mlRunLoop: \
	CMP   $255, R13; \
	BLO   mlRunLast; \
	MOVB  R16, (R11); \
	ADD   $1, R11, R11; \
	SUB   $255, R13, R13; \
	JMP   mlRunLoop; \
mlRunLast: \
	MOVB  R13, (R11); \
	ADD   $1, R11, R11; \
mlDone: \
	MOVD  SL(LEN), R16; \
	ADD   R16, R3, R3; \
	MOVD  R3, SL(P_IP); \
	MOVD  R3, SL(P_ANCHOR); \
	MOVD  R11, SL(P_OP)

// The hash chain parser's frame: the wider-match area, then
#define P_IP      (W_SIZE+0)
#define P_ANCHOR  (W_SIZE+8)
#define P_OP      (W_SIZE+16)
#define P_MFLIMIT (W_SIZE+24)
#define P_STOP    (W_SIZE+32)
#define P_OLIM    (W_SIZE+40)
#define P_DSTEND  (W_SIZE+48)
#define P_DSTBASE (W_SIZE+56)
#define P_MAXATT  (W_SIZE+64)
#define P_START0  (W_SIZE+72)
#define P_START2  (W_SIZE+80)
#define P_START3  (W_SIZE+88)
#define M0LEN     (W_SIZE+96)
#define M0OFF     (W_SIZE+104)
#define M1LEN     (W_SIZE+112)
#define M1OFF     (W_SIZE+120)
#define M2LEN     (W_SIZE+128)
#define M2OFF     (W_SIZE+136)
#define M3LEN     (W_SIZE+144)
#define M3OFF     (W_SIZE+152)
#define P_ARGS    (W_SIZE+160)

// MOVM(a, b): match b = match a (length and offset)
#define MOVM(alen, aoff, blen, boff) \
	MOVD SL(alen), R12; \
	MOVD R12, SL(blen); \
	MOVD SL(aoff), R12; \
	MOVD R12, SL(boff)

// INCSLOT(s): SL(s)++
#define INCSLOT(s) \
	MOVD SL(s), R16; \
	ADD  $1, R16, R16; \
	MOVD R16, SL(s)

// PARSE_SETUP: the registers and frame slots both parsers share, from the
// hcParseArgs at a+0(FP).
#define PARSE_SETUP(limited, olimSet) \
	MOVD a+0(FP), R11; \
	MOVD R11, SL(P_ARGS); \
	MOVD A_WIN(R11), R1; \
	MOVD A_CHAIN(R11), R2; \
	MOVD A_PREFIX(R11), R6; \
	MOVD A_IEND(R11), R12; \
	ADD  R1, R12, R12; \
	SUB  $5, R12, R5; \
	SUB  $12, R12, R13; \
	MOVD R13, SL(P_MFLIMIT); \
	SUB  $7, R5, R22; \
	MOVD A_IP(R11), R12; \
	ADD  R1, R12, R12; \
	MOVD R12, SL(P_IP); \
	MOVD A_ANCHOR(R11), R12; \
	ADD  R1, R12, R12; \
	MOVD R12, SL(P_ANCHOR); \
	MOVD A_STOP(R11), R12; \
	ADD  R1, R12, R12; \
	MOVD R12, SL(P_STOP); \
	MOVD A_DST(R11), R10; \
	MOVD R10, SL(P_DSTBASE); \
	MOVD A_OP(R11), R12; \
	ADD  R10, R12, R12; \
	MOVD R12, SL(P_OP); \
	MOVD A_DSTLEN(R11), R12; \
	ADD  R10, R12, R12; \
	MOVD R12, SL(P_DSTEND); \
	MOVD A_OLIMIT(R11), R12; \
	CMP  $0, R12; \
	BGE  limited; \
	MOVD $-1, R12; \
	JMP  olimSet; \
limited: \
	ADD  R10, R12, R12; \
olimSet: \
	MOVD R12, SL(P_OLIM); \
	MOVD A_HASH(R11), R12; \
	MOVD R12, SL(W_HASH); \
	MOVD A_NEXT(R11), R12; \
	MOVD R12, SL(W_NEXT); \
	MOVD A_PA(R11), R12; \
	MOVD R12, SL(W_PA); \
	MOVD A_ATT(R11), R12; \
	MOVD R12, SL(P_MAXATT); \
	MOVW $2654435761, R19; \
	MOVD $0x80808081, R21

// WIDER_CALL: hcWider, or hcWiderPA with the pattern analysis.
#define WIDER_CALL(pa, done) \
	MOVD SL(W_PA), R12; \
	CBNZ R12, pa; \
	BL   hcWider<>(SB); \
	JMP  done; \
pa: \
	BL   hcWiderPA<>(SB); \
done:

// func hcHashChainAsm(a *hcParseArgs) int
//
// LZ4HC_compress_hashChain's main loop for a.win[a.ip:a.iend] (the end of
// win, a prefix at a.prefixIdx), from a.ip, a.anchor and a.op, with
// a.attempts searches and the pattern analysis if a.pa is paPattern (0
// otherwise: no dictionary). It returns 0 at the end of the loop (ip >
// mflimit), 1 when a sequence does not fit the limit (limitedOutput: C
// returns 0; fillOutput: a.mlen and a.moff are the sequence's match), 3
// when a write would pass the end of dst (cannot happen), and 2 at the top
// of the loop once ip > a.stop (call again to go on); a.ip, a.anchor, a.op
// and a.next (nextToUpdate) are updated. a.olimit < 0 is notLimited; with
// fillOutput it is oend (maxOutputSize - LASTLITERALS).
//
// Registers across the loop: R1 win, R2 chainTable, R5 matchlimit, R6
// prefixIdx, R19, R21, R22; everything else is in the frame.
//
// Preconditions (checked by compressHashChain): a.iend - a.ip >= LZ4_minLength,
// a.iend = len(win), a.next >= a.prefixIdx, a.lowLimit == a.prefixIdx;
// len(dst) >= a.olimit (limited) or LZ4_compressBound(a.iend - a.ip).
//
// BOUNDS: the searches as in hcWider, with ip <= mflimit and iLowLimit <=
// ip at each call (search 2 from start2 = ip + m1.len - 2 >= ip with
// iLowLimit ip, longest m1.len; search 3 from start3 = start2 + m2.len - 3
// with iLowLimit start2, longest m2.len), and both only when the start plus
// the current length is at most mflimit. Writes to dst as in ENCODE.
TEXT ·hcHashChainAsm(SB), NOSPLIT, $392-16
	PARSE_SETUP(limited, olimSet)

mainLoop:
	MOVD SL(P_IP), R3
	MOVD SL(P_MFLIMIT), R16
	CMP  R16, R3
	BHI  endLoop
	MOVD SL(P_STOP), R16
	CMP  R16, R3
	BHI  stopped
	MOVD R3, R4
	MOVD $3, R10                 // longest = MINMATCH-1
	MOVD SL(P_MAXATT), R9
	WIDER_CALL(w1pa, w1done)
	CMP  $4, R10
	BHS  m1Found
	INCSLOT(P_IP)
	JMP  mainLoop

m1Found:
	MOVD R10, SL(M1LEN)
	MOVD R12, SL(M1OFF)
	MOVD SL(P_IP), R13
	MOVD R13, SL(P_START0)       // start0 = ip, m0 = m1
	MOVD R10, SL(M0LEN)
	MOVD R12, SL(M0OFF)

search2:
	// m1 at ip; search a longer m2 starting before its end
	MOVD SL(P_IP), R4
	MOVD SL(M1LEN), R10
	ADD  R10, R4, R3
	MOVD SL(P_MFLIMIT), R16
	CMP  R16, R3
	BHI  noM2
	SUB  $2, R3, R3              // start2 = ip + m1.len - 2
	MOVD R3, SL(P_START2)
	MOVD SL(P_MAXATT), R9
	WIDER_CALL(w2pa, w2done)
	MOVD SL(P_START2), R16
	ADD  R14, R16, R16
	MOVD R16, SL(P_START2)       // start2 += m2.back
	MOVD R10, SL(M2LEN)
	MOVD R12, SL(M2OFF)
	JMP  m2Done

noM2:
	MOVD ZR, SL(M2LEN)
	MOVD ZR, SL(M2OFF)

m2Done:
	MOVD SL(M2LEN), R12
	MOVD SL(M1LEN), R16
	CMP  R16, R12
	BGT  m2Better
	ENCODE(M1LEN, M1OFF, fail, fail2, e1a, e1b, e1c, e1d, e1e, e1f, e1g, e1h, e1i, e1j, e1k)
	JMP  mainLoop

m2Better:
	MOVD SL(P_START0), R12
	MOVD SL(P_IP), R16
	CMP  R16, R12
	BHS  noRestore
	MOVD SL(M0LEN), R13
	ADD  R16, R13, R13
	MOVD SL(P_START2), R16
	CMP  R13, R16
	BHS  noRestore
	MOVD R12, SL(P_IP)           // ip = start0, m1 = m0
	MOVM(M0LEN, M0OFF, M1LEN, M1OFF)

noRestore:
	MOVD SL(P_START2), R12
	MOVD SL(P_IP), R16
	SUB  R16, R12, R12
	CMP  $3, R12
	BGE  search3
	MOVD SL(P_START2), R12       // m1 too short before m2: removed
	MOVD R12, SL(P_IP)
	MOVM(M2LEN, M2OFF, M1LEN, M1OFF)
	JMP  search2

search3:
	// m1 is cut where m2 starts, but keeps up to OPTIMAL_ML bytes
	MOVD SL(P_START2), R13
	MOVD SL(P_IP), R16
	SUB  R16, R13, R13           // gap
	CMP  $18, R13
	BGE  noShift
	MOVD SL(M1LEN), R12
	MOVD $18, R14
	CMP  R14, R12
	CSEL GT, R14, R12, R12       // newML = min(m1.len, OPTIMAL_ML)
	MOVD SL(M2LEN), R14
	ADD  R13, R14, R14
	SUB  $4, R14, R14            // gap + m2.len - MINMATCH
	CMP  R14, R12
	CSEL GT, R14, R12, R12
	SUBS R13, R12, R12           // newML - gap
	BLE  noShift
	MOVD SL(P_START2), R16
	ADD  R12, R16, R16
	MOVD R16, SL(P_START2)
	MOVD SL(M2LEN), R16
	SUB  R12, R16, R16
	MOVD R16, SL(M2LEN)

noShift:
	MOVD SL(P_START2), R4
	MOVD SL(M2LEN), R10
	ADD  R10, R4, R3
	MOVD SL(P_MFLIMIT), R16
	CMP  R16, R3
	BHI  noM3
	SUB  $3, R3, R3              // start3 = start2 + m2.len - 3
	MOVD R3, SL(P_START3)
	MOVD SL(P_MAXATT), R9
	WIDER_CALL(w3pa, w3done)
	MOVD SL(P_START3), R16
	ADD  R14, R16, R16
	MOVD R16, SL(P_START3)       // start3 += m3.back
	MOVD R10, SL(M3LEN)
	MOVD R12, SL(M3OFF)
	JMP  m3Done

noM3:
	MOVD ZR, SL(M3LEN)
	MOVD ZR, SL(M3OFF)

m3Done:
	MOVD SL(M3LEN), R12
	MOVD SL(M2LEN), R16
	CMP  R16, R12
	BLE  encodeCut
	MOVD SL(P_IP), R13
	MOVD SL(M1LEN), R16
	ADD  R16, R13, R13           // ip + m1.len
	ADD  $3, R13, R14
	MOVD SL(P_START3), R16
	CMP  R14, R16
	BHS  encodeCut               // room for m2 between m1 and m3
	CMP  R13, R16
	BLO  m3ReplacesM2

	// m3 starts after m1: encode m1, m3 becomes m1, m2 (moved after m1)
	// the fallback m0
	MOVD SL(P_START2), R14
	CMP  R13, R14
	BHS  noCorrection
	SUB  R14, R13, R13           // correction
	ADD  R13, R14, R14
	MOVD R14, SL(P_START2)
	MOVD SL(M2LEN), R16
	SUB  R13, R16, R16
	MOVD R16, SL(M2LEN)
	CMP  $4, R16
	BGE  noCorrection
	MOVD SL(P_START3), R12
	MOVD R12, SL(P_START2)
	MOVM(M3LEN, M3OFF, M2LEN, M2OFF)

noCorrection:
	ENCODE(M1LEN, M1OFF, fail, fail2, e2a, e2b, e2c, e2d, e2e, e2f, e2g, e2h, e2i, e2j, e2k)
	MOVD SL(P_START3), R12
	MOVD R12, SL(P_IP)
	MOVM(M3LEN, M3OFF, M1LEN, M1OFF)
	MOVD SL(P_START2), R12
	MOVD R12, SL(P_START0)
	MOVM(M2LEN, M2OFF, M0LEN, M0OFF)
	JMP  search2

m3ReplacesM2:
	MOVD SL(P_START3), R12
	MOVD R12, SL(P_START2)
	MOVM(M3LEN, M3OFF, M2LEN, M2OFF)
	JMP  search3

encodeCut:
	// encode m1, cut where m2 starts
	MOVD SL(P_IP), R13
	MOVD SL(P_START2), R12
	SUB  R13, R12, R12           // start2 - ip
	MOVD SL(M1LEN), R16
	CMP  R16, R12
	BGE  noCut
	MOVD R12, SL(M1LEN)

noCut:
	ENCODE(M1LEN, M1OFF, fail, fail2, e3a, e3b, e3c, e3d, e3e, e3f, e3g, e3h, e3i, e3j, e3k)
	MOVD SL(P_START2), R12       // m2 becomes m1
	MOVD R12, SL(P_IP)
	MOVM(M2LEN, M2OFF, M1LEN, M1OFF)
	MOVD SL(M3LEN), R12
	MOVD SL(M1LEN), R16
	CMP  R16, R12
	BGT  m3BecomesM2
	ENCODE(M1LEN, M1OFF, fail, fail2, e4a, e4b, e4c, e4d, e4e, e4f, e4g, e4h, e4i, e4j, e4k)
	JMP  mainLoop

m3BecomesM2:
	MOVD SL(P_START3), R12
	MOVD R12, SL(P_START2)
	MOVM(M3LEN, M3OFF, M2LEN, M2OFF)
	JMP  search3

endLoop:
	MOVD ZR, R10
	JMP  out

stopped:
	MOVD $2, R10

out:
	MOVD SL(P_ARGS), R11
	MOVD SL(P_IP), R12
	SUB  R1, R12, R12
	MOVD R12, A_IP(R11)
	MOVD SL(P_ANCHOR), R12
	SUB  R1, R12, R12
	MOVD R12, A_ANCHOR(R11)
	MOVD SL(P_OP), R12
	MOVD SL(P_DSTBASE), R16
	SUB  R16, R12, R12
	MOVD R12, A_OP(R11)
	MOVD SL(W_NEXT), R12
	MOVD R12, A_NEXT(R11)
	MOVD R10, ret+8(FP)
	RET

fail:
	MOVD SL(M1LEN), R12
	MOVD SL(M1OFF), R14
	MOVD $1, R10
	JMP  failOut

fail2:
	MOVD $3, R10
	JMP  out

failOut:
	// the sequence that did not fit (for fillOutput)
	MOVD SL(P_ARGS), R11
	MOVD R12, A_MLEN(R11)
	MOVD R14, A_MOFF(R11)
	JMP  out

// hcAddMatch: LZ4HC_addMatch without favorDecSpeed.
//
// In: R11 opt, R13 cur, R10 match.len, R12 match.off, R14 lastPos. Out: R14
// lastPos. Clobbers R3, R4, R7-R9, R12, R16, R17.
//
// Preconditions (compressOptimal's loop): 0 <= cur < lastPos (or 0),
// cur + match.len < LZ4_OPT_NUM and lastPos + TRAILING_LITERALS <
// LZ4_OPT_NUM + TRAILING_LITERALS, so every opt[] entry written or read
// (pos <= cur + match.len, lastPos + 1..3) is within the LZ4_OPT_NUM + 3
// entries; opt[cur].litlen <= cur or the run starts before the window
// (then the start price is 0, as in C).
//
// BOUNDS: opt[cur - ll] is read only when ll <= cur; opt[cur + ml] for ml in
// [4, match.len]; opt[lastPos + 1..3] after the loop, with the final
// lastPos <= max(lastPos, cur + match.len).
TEXT hcAddMatch<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_addmatch_arm64.h"

// The optimal parser's slots (the hash chain parser's, renamed)
#define O_SUFF    P_START0
#define O_OPT     P_START2
#define O_LASTPOS P_START3
#define O_CUR     M0LEN
#define O_SEQLEN  M1LEN
#define O_SEQOFF  M1OFF
#define O_SEQPOS  M2LEN
#define O_RPOS    M2OFF
#define O_ML      M3LEN
#define O_OFFE    M3OFF

// func hcOptimalAsm(a *hcParseArgs) int
//
// LZ4HC_compress_optimal_internal's main loop (no favorDecSpeed), for the
// same inputs as hcHashChainAsm, with a.attempts searches (nbSearches),
// a.suff (sufficientLen = min(targetLength, LZ4_OPT_NUM-1)) and a.opt (the
// LZ4_OPT_NUM + TRAILING_LITERALS entries); the same results.
//
// Preconditions: those of hcHashChainAsm, a.pa == 0 and a.suff <
// LZ4_OPT_NUM.
//
// BOUNDS: the searches as in hcWider, at curPtr = ip + cur <= mflimit with
// iLowLimit = curPtr (lookBack 0). opt[] as in hcAddMatch: the first prices
// at 0..3; the loop reads opt[cur] and opt[cur+1] for cur < lastPos <
// LZ4_OPT_NUM; a match with len > sufficientLen or cur + len >=
// LZ4_OPT_NUM is encoded at once, so hcAddMatch only sees cur + len <
// LZ4_OPT_NUM. The reverse traversal and the encoding read opt[0..lastSeqPos]
// with lastSeqPos < LZ4_OPT_NUM, moving by mlen >= 1. Writes to dst as in
// ENCODE.
TEXT ·hcOptimalAsm(SB), NOSPLIT, $392-16
#include "lz4hc_optimal_arm64.h"
