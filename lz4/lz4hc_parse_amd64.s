//go:build amd64 && !appengine && !noasm && !purego && gc

// The parsers of LZ4HC (lz4 v1.11.1 lib/lz4hc.c, Copyright (c) Yann
// Collet, BSD 2-Clause) for a prefix without external or attached
// dictionary (lowLimit == dictLimit), notLimited or limitedOutput:
// LZ4HC_compress_hashChain's main loop (levels 3 to 9) and
// LZ4HC_compress_optimal_internal's (levels 10 to 12), each with
// LZ4HC_InsertAndGetWiderMatch (hcWider) inline in one routine. They follow
// compressHashChain and compressOptimal in lz4hc.go and lz4hc_opt.go
// statement for statement and return before the last literals.

#include "textflag.h"

// The wider-match area at the start of each parser's frame; hcWider runs
// one CALL deeper, where the same slots are 8 bytes farther from SP.
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
#define W_HLIM7  144
#define W_END    160
#define W_SIZE   168

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

// hcWider: LZ4HC_InsertAndGetWiderMatch for a prefix without dictionary
// (every candidate is at or above lowestMatchIndex >= lowLimit = prefixIdx).
//
// In: R9 ip, R10 iLowLimit, BX longest, R15 maxNbAttempts; SI win (the
// prefix), R8 chainTable, R11 iHighLimit, R12 prefixIdx (kept); the
// caller's frame (8(SP) here) holds W_HASH, W_NEXT (nextToUpdate, in and
// out) and W_HLIM7 = iHighLimit - 7. Three variants (lz4hc_wider_amd64.h):
// hcWider (levels 3 to 8), hcWiderPA (the pattern analysis, level 9) and
// hcWiderOpt (the pattern analysis and chainSwap, levels 10 to 12); no
// favorDecSpeed (the parsers' Go callers keep it in Go).
// Out: BX longest, AX offset, DX sBack. Clobbers CX, DI, R13, R14, R15.
//
// Registers: DI ipIndex, R13 lowestMatchIndex, R14 matchIndex, R15
// nbAttempts, BX longest; AX, CX, DX scratch.
//
// Preconditions (checked by the parsers' Go callers or their own
// arguments): 0 <= iLowLimit <= ip < iHighLimit <= the end of win; longest
// > ip - iLowLimit (lookBack); with chainSwap, iLowLimit == ip (C asserts
// lookBackLength == 0 there); nextToUpdate >= prefixIdx; table and chain
// entries reached from ipIndex are earlier positions (inserted ones have
// chain distances >= 1).
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
#include "lz4hc_wider_amd64.h"

// hcWiderPA: hcWider with the pattern analysis (level 9).
#define WIDER_PA
TEXT hcWiderPA<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

// hcWiderOpt: hcWider with the pattern analysis and chainSwap (levels 10
// to 12; iLowLimit == ip).
#define WIDER_SWAP
TEXT hcWiderOpt<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"
#undef WIDER_SWAP
#undef WIDER_PA

// hcCountPattern: CX = the end of the repetition of pattern (16(SP) here,
// the caller's W_PAT one CALL deeper) from CX, below R11 (LZ4HC_countPattern:
// the count is the end minus the start). Clobbers AX, DX, R13.
//
// BOUNDS: 8-byte loads at CX while CX < R11 - 7, then single bytes below
// R11.
TEXT hcCountPattern<>(SB), NOSPLIT|NOFRAME, $0
	MOVL (16+W_PAT)(SP), DX
	MOVQ DX, R13
	SHLQ $32, R13
	ORQ  DX, R13                 // pattern for 8-byte compares
	LEAQ -7(R11), DX

cp8:
	CMPQ CX, DX
	JHS  cpTail
	MOVQ (CX), AX
	XORQ R13, AX
	JNE  cpDiff
	ADDQ $8, CX
	JMP  cp8

cpDiff:
	BSFQ AX, AX
	SHRQ $3, AX
	ADDQ AX, CX
	RET

cpTail:
	CMPQ CX, R11
	JHS  cpDone
	MOVB (CX), AX
	CMPB AL, R13
	JNE  cpDone
	INCQ CX
	SHRQ $8, R13
	JMP  cpTail

cpDone:
	RET

// ENCODE(LEN, OFF, ...labels): LZ4HC_encodeSequence(ip, op, anchor; the
// frame's P_IP, P_OP and P_ANCHOR) with the match length and offset at
// LEN(SP) and OFF(SP); updates P_IP, P_ANCHOR and P_OP. Jumps to fail when
// the sequence does not fit the limit (limitedOutput), to fail2 when a write
// would pass the end of dst (cannot happen within the checked limits).
// Clobbers AX, BX, CX, DX, DI, R9, R10, R14.
//
// BOUNDS: sequences start at ip <= mflimit = iend - 12, so the literals
// copied 8 bytes at a time from anchor < ip end before iend; every dst
// write is checked against dstEnd.
#define ENCODE(LEN, OFF, fail, fail2, litRun, litRunLoop, litRunLast, copyLits, copyLits8, exactLits, offsetOut, mlRun, mlRunLoop, mlRunLast, mlDone) \
	MOVQ P_IP(SP), R9; \
	MOVQ P_ANCHOR(SP), R10; \
	MOVQ P_OP(SP), DI; \
	MOVQ R9, CX; \
	SUBQ R10, CX; \
	MOVQ CX, AX; \
	MOVL $0x80808081, DX; \
	IMULQ DX, AX; \
	SHRQ $39, AX; \
	LEAQ 9(DI)(CX*1), DX; \
	ADDQ AX, DX; \
	CMPQ DX, P_OLIM(SP); \
	JHI  fail; \
	MOVQ DI, R14; \
	INCQ DI; \
	CMPQ CX, $15; \
	JHS  litRun; \
	MOVQ CX, AX; \
	SHLQ $4, AX; \
	MOVB AL, (R14); \
	JMP  copyLits; \
litRun: \
	MOVB $0xF0, (R14); \
	LEAQ -15(CX), AX; \
	LEAQ (DI)(AX*1), DX; \
	CMPQ DX, P_DSTEND(SP); \
	JHS  fail2; \
litRunLoop: \
	CMPQ AX, $255; \
	JLO  litRunLast; \
	MOVB $255, (DI); \
	INCQ DI; \
	SUBQ $255, AX; \
	JMP  litRunLoop; \
litRunLast: \
	MOVB AL, (DI); \
	INCQ DI; \
copyLits: \
	LEAQ (DI)(CX*1), BX; \
	MOVQ R10, AX; \
	LEAQ 8(BX), DX; \
	CMPQ DX, P_DSTEND(SP); \
	JHI  exactLits; \
copyLits8: \
	MOVQ (AX), DX; \
	MOVQ DX, (DI); \
	ADDQ $8, AX; \
	ADDQ $8, DI; \
	CMPQ DI, BX; \
	JLO  copyLits8; \
	MOVQ BX, DI; \
	JMP  offsetOut; \
exactLits: \
	CMPQ DI, BX; \
	JHS  offsetOut; \
	MOVB (AX), DX; \
	MOVB DX, (DI); \
	INCQ AX; \
	INCQ DI; \
	JMP  exactLits; \
offsetOut: \
	LEAQ 2(DI), AX; \
	CMPQ AX, P_DSTEND(SP); \
	JHI  fail2; \
	MOVQ OFF(SP), AX; \
	MOVW AX, (DI); \
	ADDQ $2, DI; \
	MOVQ LEN(SP), CX; \
	SUBQ $4, CX; \
	MOVQ CX, AX; \
	MOVL $0x80808081, DX; \
	IMULQ DX, AX; \
	SHRQ $39, AX; \
	LEAQ 6(DI)(AX*1), DX; \
	CMPQ DX, P_OLIM(SP); \
	JHI  fail; \
	LEAQ 2(DI)(AX*1), DX; \
	CMPQ DX, P_DSTEND(SP); \
	JHI  fail2; \
	CMPQ CX, $15; \
	JHS  mlRun; \
	ADDB CL, (R14); \
	JMP  mlDone; \
mlRun: \
	ADDB $15, (R14); \
	SUBQ $15, CX; \
mlRunLoop: \
	CMPQ CX, $255; \
	JLO  mlRunLast; \
	MOVB $255, (DI); \
	INCQ DI; \
	SUBQ $255, CX; \
	JMP  mlRunLoop; \
mlRunLast: \
	MOVB CL, (DI); \
	INCQ DI; \
mlDone: \
	ADDQ LEN(SP), R9; \
	MOVQ R9, P_IP(SP); \
	MOVQ R9, P_ANCHOR(SP); \
	MOVQ DI, P_OP(SP)

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
	MOVQ alen(SP), AX; \
	MOVQ AX, blen(SP); \
	MOVQ aoff(SP), AX; \
	MOVQ AX, boff(SP)

// PARSE_SETUP: the registers and frame slots both parsers share, from the
// hcParseArgs at a+0(FP).
#define PARSE_SETUP(limited, olimSet) \
	MOVQ a+0(FP), DI; \
	MOVQ DI, P_ARGS(SP); \
	MOVQ A_WIN(DI), SI; \
	MOVQ A_CHAIN(DI), R8; \
	MOVQ A_PREFIX(DI), R12; \
	MOVQ A_IEND(DI), AX; \
	ADDQ SI, AX; \
	LEAQ -5(AX), R11; \
	LEAQ -12(AX), CX; \
	MOVQ CX, P_MFLIMIT(SP); \
	LEAQ -7(R11), CX; \
	MOVQ CX, W_HLIM7(SP); \
	MOVQ A_IP(DI), AX; \
	ADDQ SI, AX; \
	MOVQ AX, P_IP(SP); \
	MOVQ A_ANCHOR(DI), AX; \
	ADDQ SI, AX; \
	MOVQ AX, P_ANCHOR(SP); \
	MOVQ A_STOP(DI), AX; \
	ADDQ SI, AX; \
	MOVQ AX, P_STOP(SP); \
	MOVQ A_DST(DI), BX; \
	MOVQ BX, P_DSTBASE(SP); \
	MOVQ A_OP(DI), AX; \
	ADDQ BX, AX; \
	MOVQ AX, P_OP(SP); \
	MOVQ A_DSTLEN(DI), AX; \
	ADDQ BX, AX; \
	MOVQ AX, P_DSTEND(SP); \
	MOVQ A_OLIMIT(DI), AX; \
	TESTQ AX, AX; \
	JGE  limited; \
	MOVQ $-1, AX; \
	JMP  olimSet; \
limited: \
	ADDQ BX, AX; \
olimSet: \
	MOVQ AX, P_OLIM(SP); \
	MOVQ A_HASH(DI), AX; \
	MOVQ AX, W_HASH(SP); \
	MOVQ A_NEXT(DI), AX; \
	MOVQ AX, W_NEXT(SP); \
	MOVQ A_PA(DI), AX; \
	MOVQ AX, W_PA(SP); \
	MOVQ A_ATT(DI), AX; \
	MOVQ AX, P_MAXATT(SP)

// WIDER_CALL: hcWider, or hcWiderPA with the pattern analysis.
#define WIDER_CALL(pa, done) \
	CMPQ W_PA(SP), $0; \
	JNE  pa; \
	CALL hcWider<>(SB); \
	JMP  done; \
pa: \
	CALL hcWiderPA<>(SB); \
done:

// func hcHashChainAsm(a *hcParseArgs) int
//
// LZ4HC_compress_hashChain's main loop for a.win[a.ip:a.iend] (the end of
// win, a prefix without dictionary at a.prefixIdx), from a.ip, a.anchor and
// a.op, with a.attempts searches and the pattern analysis if a.pa. It
// returns 0 at the end of the loop (ip > mflimit), 1 when a sequence does
// not fit the limit (limitedOutput: C returns 0), 3 when a write would pass
// the end of dst (cannot happen), and 2 at the top of the loop once ip >
// a.stop (call again to go on); a.ip, a.anchor, a.op and a.next
// (nextToUpdate) are updated. a.olimit < 0 is notLimited.
//
// Registers across the loop: SI win, R8 chainTable, R11 matchlimit, R12
// prefixIdx; everything else is in the frame.
//
// Preconditions (checked by compressHashChain): a.iend - a.ip >= LZ4_minLength,
// a.iend = len(win), a.next >= a.prefixIdx, lowLimit == dictLimit;
// len(dst) >= a.olimit (limited) or LZ4_compressBound(a.iend - a.ip).
//
// BOUNDS: the searches as in hcWider, with ip <= mflimit and iLowLimit <=
// ip at each call (search 2 from start2 = ip + m1.len - 2 >= ip with
// iLowLimit ip, longest m1.len; search 3 from start3 = start2 + m2.len - 3
// with iLowLimit start2, longest m2.len), and both only when the start plus
// the current length is at most mflimit. Writes to dst as in ENCODE.
TEXT ·hcHashChainAsm(SB), NOSPLIT, $344-16
	PARSE_SETUP(limited, olimSet)

mainLoop:
	MOVQ P_IP(SP), R9
	CMPQ R9, P_MFLIMIT(SP)
	JHI  endLoop
	CMPQ R9, P_STOP(SP)
	JHI  stopped
	MOVQ R9, R10
	MOVL $3, BX                  // longest = MINMATCH-1
	MOVQ P_MAXATT(SP), R15
	WIDER_CALL(w1pa, w1done)
	CMPQ BX, $4
	JHS  m1Found
	INCQ P_IP(SP)
	JMP  mainLoop

m1Found:
	MOVQ BX, M1LEN(SP)
	MOVQ AX, M1OFF(SP)
	MOVQ P_IP(SP), CX
	MOVQ CX, P_START0(SP)        // start0 = ip, m0 = m1
	MOVQ BX, M0LEN(SP)
	MOVQ AX, M0OFF(SP)

search2:
	// m1 at ip; search a longer m2 starting before its end
	MOVQ P_IP(SP), R10
	MOVQ M1LEN(SP), BX
	LEAQ (R10)(BX*1), R9
	CMPQ R9, P_MFLIMIT(SP)
	JHI  noM2
	SUBQ $2, R9                  // start2 = ip + m1.len - 2
	MOVQ R9, P_START2(SP)
	MOVQ P_MAXATT(SP), R15
	WIDER_CALL(w2pa, w2done)
	ADDQ DX, P_START2(SP)        // start2 += m2.back
	MOVQ BX, M2LEN(SP)
	MOVQ AX, M2OFF(SP)
	JMP  m2Done

noM2:
	MOVQ $0, M2LEN(SP)
	MOVQ $0, M2OFF(SP)

m2Done:
	MOVQ M2LEN(SP), AX
	CMPQ AX, M1LEN(SP)
	JGT  m2Better
	ENCODE(M1LEN, M1OFF, fail, fail2, e1a, e1b, e1c, e1d, e1e, e1f, e1g, e1h, e1i, e1j, e1k)
	JMP  mainLoop

m2Better:
	MOVQ P_START0(SP), AX
	CMPQ AX, P_IP(SP)
	JHS  noRestore
	MOVQ P_IP(SP), CX
	ADDQ M0LEN(SP), CX
	CMPQ P_START2(SP), CX
	JHS  noRestore
	MOVQ AX, P_IP(SP)            // ip = start0, m1 = m0
	MOVM(M0LEN, M0OFF, M1LEN, M1OFF)

noRestore:
	MOVQ P_START2(SP), AX
	SUBQ P_IP(SP), AX
	CMPQ AX, $3
	JGE  search3
	MOVQ P_START2(SP), AX        // m1 too short before m2: removed
	MOVQ AX, P_IP(SP)
	MOVM(M2LEN, M2OFF, M1LEN, M1OFF)
	JMP  search2

search3:
	// m1 is cut where m2 starts, but keeps up to OPTIMAL_ML bytes
	MOVQ P_START2(SP), CX
	SUBQ P_IP(SP), CX            // gap
	CMPQ CX, $18
	JGE  noShift
	MOVQ M1LEN(SP), AX
	MOVQ $18, DX
	CMPQ AX, DX
	CMOVQGT DX, AX               // newML = min(m1.len, OPTIMAL_ML)
	MOVQ M2LEN(SP), DX
	LEAQ -4(CX)(DX*1), DX        // gap + m2.len - MINMATCH
	CMPQ AX, DX
	CMOVQGT DX, AX
	SUBQ CX, AX                  // newML - gap
	JLE  noShift
	ADDQ AX, P_START2(SP)
	SUBQ AX, M2LEN(SP)

noShift:
	MOVQ P_START2(SP), R10
	MOVQ M2LEN(SP), BX
	LEAQ (R10)(BX*1), R9
	CMPQ R9, P_MFLIMIT(SP)
	JHI  noM3
	SUBQ $3, R9                  // start3 = start2 + m2.len - 3
	MOVQ R9, P_START3(SP)
	MOVQ P_MAXATT(SP), R15
	WIDER_CALL(w3pa, w3done)
	ADDQ DX, P_START3(SP)        // start3 += m3.back
	MOVQ BX, M3LEN(SP)
	MOVQ AX, M3OFF(SP)
	JMP  m3Done

noM3:
	MOVQ $0, M3LEN(SP)
	MOVQ $0, M3OFF(SP)

m3Done:
	MOVQ M3LEN(SP), AX
	CMPQ AX, M2LEN(SP)
	JLE  encodeCut
	MOVQ P_IP(SP), CX
	ADDQ M1LEN(SP), CX           // ip + m1.len
	LEAQ 3(CX), DX
	CMPQ P_START3(SP), DX
	JHS  encodeCut               // room for m2 between m1 and m3
	CMPQ P_START3(SP), CX
	JLO  m3ReplacesM2

	// m3 starts after m1: encode m1, m3 becomes m1, m2 (moved after m1)
	// the fallback m0
	MOVQ P_START2(SP), DX
	CMPQ DX, CX
	JHS  noCorrection
	SUBQ DX, CX                  // correction
	ADDQ CX, P_START2(SP)
	SUBQ CX, M2LEN(SP)
	CMPQ M2LEN(SP), $4
	JGE  noCorrection
	MOVQ P_START3(SP), AX
	MOVQ AX, P_START2(SP)
	MOVM(M3LEN, M3OFF, M2LEN, M2OFF)

noCorrection:
	ENCODE(M1LEN, M1OFF, fail, fail2, e2a, e2b, e2c, e2d, e2e, e2f, e2g, e2h, e2i, e2j, e2k)
	MOVQ P_START3(SP), AX
	MOVQ AX, P_IP(SP)
	MOVM(M3LEN, M3OFF, M1LEN, M1OFF)
	MOVQ P_START2(SP), AX
	MOVQ AX, P_START0(SP)
	MOVM(M2LEN, M2OFF, M0LEN, M0OFF)
	JMP  search2

m3ReplacesM2:
	MOVQ P_START3(SP), AX
	MOVQ AX, P_START2(SP)
	MOVM(M3LEN, M3OFF, M2LEN, M2OFF)
	JMP  search3

encodeCut:
	// encode m1, cut where m2 starts
	MOVQ P_IP(SP), CX
	MOVQ P_START2(SP), AX
	SUBQ CX, AX                  // start2 - ip
	CMPQ AX, M1LEN(SP)
	JGE  noCut
	MOVQ AX, M1LEN(SP)

noCut:
	ENCODE(M1LEN, M1OFF, fail, fail2, e3a, e3b, e3c, e3d, e3e, e3f, e3g, e3h, e3i, e3j, e3k)
	MOVQ P_START2(SP), AX        // m2 becomes m1
	MOVQ AX, P_IP(SP)
	MOVM(M2LEN, M2OFF, M1LEN, M1OFF)
	MOVQ M3LEN(SP), AX
	CMPQ AX, M1LEN(SP)
	JGT  m3BecomesM2
	ENCODE(M1LEN, M1OFF, fail, fail2, e4a, e4b, e4c, e4d, e4e, e4f, e4g, e4h, e4i, e4j, e4k)
	JMP  mainLoop

m3BecomesM2:
	MOVQ P_START3(SP), AX
	MOVQ AX, P_START2(SP)
	MOVM(M3LEN, M3OFF, M2LEN, M2OFF)
	JMP  search3

endLoop:
	MOVQ $0, BX
	JMP  out

stopped:
	MOVQ $2, BX

out:
	MOVQ P_ARGS(SP), DI
	MOVQ P_IP(SP), AX
	SUBQ SI, AX
	MOVQ AX, A_IP(DI)
	MOVQ P_ANCHOR(SP), AX
	SUBQ SI, AX
	MOVQ AX, A_ANCHOR(DI)
	MOVQ P_OP(SP), AX
	SUBQ P_DSTBASE(SP), AX
	MOVQ AX, A_OP(DI)
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ BX, ret+8(FP)
	RET

fail:
	MOVQ P_ARGS(SP), DI
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ $1, ret+8(FP)
	RET

fail2:
	MOVQ P_ARGS(SP), DI
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ $3, ret+8(FP)
	RET

// hcAddMatch: LZ4HC_addMatch without favorDecSpeed.
//
// In: DI opt, CX cur, BX match.len, AX match.off, DX lastPos. Out: DX
// lastPos. Clobbers AX, R9, R10, R13, R14, R15; R11 and R12 are kept (saved
// in the caller's W_MP and W_FWD slots, unused between searches).
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
	MOVQ R11, (8+W_MP)(SP)
	MOVQ R12, (8+W_FWD)(SP)
	MOVQ CX, R10
	SHLQ $4, R10
	ADDQ DI, R10                 // &opt[cur]
	MOVLQSX 12(R10), R9          // ll = opt[cur].litlen
	XORL R13, R13
	CMPQ R9, CX
	JGT  amBase
	MOVQ CX, R14
	SUBQ R9, R14
	SHLQ $4, R14
	MOVLQSX (DI)(R14*1), R13     // LZ4HC_runStartPrice: opt[cur-ll].price

amBase:
	// + LZ4HC_literalsPrice(ll)
	MOVQ R9, R14
	CMPQ R9, $15
	JLT  amLit
	LEAQ -15(R9), R11
	MOVL $0x80808081, R12
	IMULQ R12, R11
	SHRQ $39, R11
	LEAQ 1(R9)(R11*1), R14

amLit:
	LEAQ (R13)(R14*4), R13       // basePrice + literals price
	SHLQ $32, AX                 // the offset, in the high half of opt[pos]'s first 8 bytes
	// price = basePrice + LZ4HC_sequencePrice(ll, ml): + 12 below ml 19,
	// + 4 more at ml = 19, 19+255, ...
	LEAQ 12(R13), R9
	MOVL $19, R11                // the next ml where the price grows
	MOVQ DX, R15
	ADDQ $3, R15
	SUBQ CX, R15                 // pos > lastPos + 3  <=>  ml > lastPos + 3 - cur
	MOVQ R15, R12
	CMPQ R12, BX
	CMOVQGT BX, R12              // the last ml compared with opt[pos].price
	MOVL $4, R14                 // ml
	ADDQ $64, R10                // &opt[cur + 4]

amCmp:
	CMPQ R14, R12
	JGT  amFill
	CMPQ R14, R11
	JNE  amCmpPrice
	ADDQ $4, R9
	ADDQ $255, R11

amCmpPrice:
	MOVLQSX (R10), R13
	CMPQ R9, R13
	JGT  amCmpNext               // price > opt[pos].price
	MOVL R9, R13                 // LZ4HC_setMatch: price, off, mlen = ml, litlen 0
	ORQ  AX, R13
	MOVQ R13, (R10)
	MOVQ R14, 8(R10)
	CMPQ R14, BX
	JNE  amCmpNext
	LEAQ (CX)(BX*1), R13         // pos of the whole match
	CMPQ DX, R13
	CMOVQLT R13, DX              // lastPos = pos

amCmpNext:
	INCQ R14
	ADDQ $16, R10
	JMP  amCmp

amFill:
	// pos > lastPos + 3: every length is set
	CMPQ R14, BX
	JGT  amTail

amFillLoop:
	CMPQ R14, R11
	JNE  amFillSet
	ADDQ $4, R9
	ADDQ $255, R11

amFillSet:
	MOVL R9, R13
	ORQ  AX, R13
	MOVQ R13, (R10)
	MOVQ R14, 8(R10)
	INCQ R14
	ADDQ $16, R10
	CMPQ R14, BX
	JLE  amFillLoop
	LEAQ (CX)(BX*1), DX          // lastPos = cur + len (> lastPos + 3)

amTail:
	// LZ4HC_setLiteral(opt, lastPos+addLit, addLit, opt[lastPos].price + 4*addLit)
	MOVQ DX, R10
	SHLQ $4, R10
	ADDQ DI, R10
	MOVL (R10), R9
	LEAL 4(R9), R11
	MOVQ R11, 16(R10)            // price, off 0
	MOVQ $0x100000001, R11
	MOVQ R11, 24(R10)            // mlen 1, litlen 1
	LEAL 8(R9), R11
	MOVQ R11, 32(R10)
	MOVQ $0x200000001, R11
	MOVQ R11, 40(R10)
	LEAL 12(R9), R11
	MOVQ R11, 48(R10)
	MOVQ $0x300000001, R11
	MOVQ R11, 56(R10)
	MOVQ (8+W_MP)(SP), R11
	MOVQ (8+W_FWD)(SP), R12
	RET

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
// LZ4HC_compress_optimal_internal's main loop without favorDecSpeed, for
// the same inputs as hcHashChainAsm, with a.attempts searches (nbSearches),
// a.suff (sufficientLen = min(targetLength, LZ4_OPT_NUM-1)) and a.opt (the
// LZ4_OPT_NUM + TRAILING_LITERALS entries); the same results.
//
// Preconditions: those of hcHashChainAsm, and a.suff < LZ4_OPT_NUM.
//
// BOUNDS: the searches as in hcWider, at curPtr = ip + cur <= mflimit with
// iLowLimit = curPtr (lookBack 0). opt[] as in hcAddMatch: the first prices
// at 0..3; the loop reads opt[cur] and opt[cur+1] for cur < lastPos <
// LZ4_OPT_NUM; a match with len > sufficientLen or cur + len >=
// LZ4_OPT_NUM is encoded at once, so hcAddMatch only sees cur + len <
// LZ4_OPT_NUM. The reverse traversal and the encoding read opt[0..lastSeqPos]
// with lastSeqPos < LZ4_OPT_NUM, moving by mlen >= 1. Writes to dst as in
// ENCODE.
TEXT ·hcOptimalAsm(SB), NOSPLIT, $344-16
	PARSE_SETUP(limited, olimSet)
	MOVQ A_SUFF(DI), AX
	MOVQ AX, O_SUFF(SP)
	MOVQ A_OPT(DI), AX
	MOVQ AX, O_OPT(SP)

optMain:
	MOVQ P_IP(SP), R9
	CMPQ R9, P_MFLIMIT(SP)
	JHI  endLoop
	CMPQ R9, P_STOP(SP)
	JHI  stopped
	MOVQ R9, R10
	MOVL $3, BX
	MOVQ P_MAXATT(SP), R15
	CALL hcWiderOpt<>(SB)        // LZ4HC_FindLongestMatch
	CMPQ BX, $4
	JHS  firstFound
	INCQ P_IP(SP)
	JMP  optMain

firstFound:
	MOVQ BX, O_SEQLEN(SP)
	MOVQ AX, O_SEQOFF(SP)
	CMPQ BX, O_SUFF(SP)
	JLE  firstPrices
	MOVQ $0, O_SEQPOS(SP)        // good enough: immediate encoding
	JMP  optEncode

firstPrices:
	// llen = LZ4HC_pricedLitlen(ip - anchor)
	MOVQ P_IP(SP), CX
	SUBQ P_ANCHOR(SP), CX
	CMPQ CX, $270
	JLT  llenSet
	LEAQ -15(CX), AX
	XORL DX, DX
	MOVL $255, R9
	DIVQ R9
	LEAQ 270(DX), CX             // reduceFrom + (llen - RUN_MASK) % 255

llenSet:
	// LZ4HC_setLiteral(opt, rPos, llen + rPos, LZ4HC_literalsPrice(llen + rPos))
	MOVQ O_OPT(SP), DI
	XORL R10, R10

firstLits:
	LEAQ (CX)(R10*1), R9         // litlen
	MOVQ R9, R14
	CMPQ R9, $15
	JLT  firstLitPrice
	LEAQ -15(R9), AX
	MOVL $0x80808081, DX
	IMULQ DX, AX
	SHRQ $39, AX
	LEAQ 1(R9)(AX*1), R14

firstLitPrice:
	SHLQ $2, R14
	MOVQ R10, AX
	SHLQ $4, AX
	MOVL R14, (DI)(AX*1)
	MOVL $0, 4(DI)(AX*1)
	MOVL $1, 8(DI)(AX*1)
	MOVL R9, 12(DI)(AX*1)
	INCQ R10
	CMPQ R10, $4
	JLO  firstLits

	XORL CX, CX                  // cur 0
	MOVQ O_SEQLEN(SP), BX
	MOVQ O_SEQOFF(SP), AX
	XORL DX, DX                  // lastPos 0
	CALL hcAddMatch<>(SB)
	MOVQ DX, O_LASTPOS(SP)
	MOVQ $1, O_CUR(SP)

curLoop:
	MOVQ O_CUR(SP), CX
	CMPQ CX, O_LASTPOS(SP)
	JGE  curDone
	MOVQ P_IP(SP), R9
	ADDQ CX, R9                  // curPtr
	CMPQ R9, P_MFLIMIT(SP)
	JHI  curDone
	MOVQ CX, AX
	SHLQ $4, AX
	ADDQ O_OPT(SP), AX
	MOVL 16(AX), DX
	CMPL DX, (AX)
	JLE  curNext                 // opt[cur+1].price <= opt[cur].price
	MOVQ R9, R10
	MOVL $3, BX
	MOVQ P_MAXATT(SP), R15
	CALL hcWiderOpt<>(SB)
	CMPQ BX, $4
	JLO  curNext
	MOVQ O_CUR(SP), CX
	CMPQ BX, O_SUFF(SP)
	JGT  curImmediate
	LEAQ (BX)(CX*1), DX
	CMPQ DX, $4096
	JGE  curImmediate
	MOVQ O_OPT(SP), DI
	MOVQ O_LASTPOS(SP), DX
	CALL hcAddMatch<>(SB)
	MOVQ DX, O_LASTPOS(SP)

curNext:
	INCQ O_CUR(SP)
	JMP  curLoop

curImmediate:
	MOVQ CX, O_SEQPOS(SP)
	MOVQ BX, O_SEQLEN(SP)
	MOVQ AX, O_SEQOFF(SP)
	JMP  optEncode

curDone:
	MOVQ O_LASTPOS(SP), CX
	MOVQ CX, AX
	SHLQ $4, AX
	ADDQ O_OPT(SP), AX
	MOVLQSX 8(AX), DX            // lastSeq.len = opt[lastPos].mlen
	MOVQ DX, O_SEQLEN(SP)
	MOVLQSX 4(AX), BX
	MOVQ BX, O_SEQOFF(SP)
	SUBQ DX, CX
	MOVQ CX, O_SEQPOS(SP)        // lastSeqPos = lastPos - lastSeq.len

optEncode:
	// reverse traversal: move each step to its starting position
	MOVQ O_OPT(SP), DI
	MOVQ O_SEQPOS(SP), CX        // pos
	MOVQ O_SEQLEN(SP), BX        // mlen
	MOVQ O_SEQOFF(SP), DX        // off

revLoop:
	TESTQ CX, CX
	JLE  revDone
	MOVQ CX, AX
	SHLQ $4, AX
	MOVLQSX 8(DI)(AX*1), R9      // prevMlen
	MOVLQSX 4(DI)(AX*1), R10     // prevOff
	MOVL BX, 8(DI)(AX*1)
	MOVL DX, 4(DI)(AX*1)
	MOVQ R9, BX
	MOVQ R10, DX
	SUBQ BX, CX
	JMP  revLoop

revDone:
	MOVL BX, 8(DI)
	MOVL DX, 4(DI)
	MOVQ $0, O_RPOS(SP)

encLoop:
	MOVQ O_RPOS(SP), CX
	CMPQ CX, O_SEQPOS(SP)
	JGT  optMain
	MOVQ CX, AX
	SHLQ $4, AX
	ADDQ O_OPT(SP), AX
	MOVLQSX 8(AX), DX            // ml
	CMPQ DX, $1
	JNE  encSeq
	INCQ P_IP(SP)                // literal
	INCQ O_RPOS(SP)
	JMP  encLoop

encSeq:
	ADDQ DX, O_RPOS(SP)
	MOVQ DX, O_ML(SP)
	MOVLQSX 4(AX), DX
	MOVQ DX, O_OFFE(SP)
	ENCODE(O_ML, O_OFFE, fail, fail2, e1a, e1b, e1c, e1d, e1e, e1f, e1g, e1h, e1i, e1j, e1k)
	JMP  encLoop

endLoop:
	MOVQ $0, BX
	JMP  out

stopped:
	MOVQ $2, BX

out:
	MOVQ P_ARGS(SP), DI
	MOVQ P_IP(SP), AX
	SUBQ SI, AX
	MOVQ AX, A_IP(DI)
	MOVQ P_ANCHOR(SP), AX
	SUBQ SI, AX
	MOVQ AX, A_ANCHOR(DI)
	MOVQ P_OP(SP), AX
	SUBQ P_DSTBASE(SP), AX
	MOVQ AX, A_OP(DI)
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ BX, ret+8(FP)
	RET

fail:
	MOVQ P_ARGS(SP), DI
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ $1, ret+8(FP)
	RET

fail2:
	MOVQ P_ARGS(SP), DI
	MOVQ W_NEXT(SP), AX
	MOVQ AX, A_NEXT(DI)
	MOVQ $3, ret+8(FP)
	RET
