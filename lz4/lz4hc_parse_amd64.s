//go:build amd64 && !appengine && !noasm && !purego && gc

// The parsers of LZ4HC (lz4 v1.11.1 lib/lz4hc.c, Copyright (c) Yann
// Collet, BSD 2-Clause) for a prefix with or without an external
// dictionary (no attached one), notLimited, limitedOutput or fillOutput
// (destSize; the last sequence and literals are Go's):
// LZ4HC_compress_hashChain's main loop (levels 3 to 9) and
// LZ4HC_compress_optimal_internal's (levels 10 to 12, favorDecSpeed too),
// each with LZ4HC_InsertAndGetWiderMatch (hcWider) inline in one routine.
// They follow compressHashChain and compressOptimal in lz4hc.go and
// lz4hc_opt.go statement for statement and return before the last
// literals.

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
#define W_LOWLIM 120
#define W_DICTB  128
#define W_CPOS   136
#define W_HLIM7  144
#define W_VLIM   152
#define W_END    160
#define W_DHASH  168
#define W_DCHAIN 176
#define W_DBASE  184
#define W_DEND   192
#define W_DSTART 200
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
#define A_DICT    136
#define A_LOWLIM  144
#define A_MLEN    152
#define A_MOFF    160
#define A_NBSEQ   168
#define A_DHASH   176
#define A_DCHAIN  184
#define A_DBASE   192
#define A_DEND    200
#define A_DSTART  208

// a.pa (W_PA) bits: the pattern analysis (hash chain), the external
// dictionary, favorDecSpeed (optimal)
#define PA_PA  1
#define PA_EXT 2
#define PA_FAV 4
#define PA_DCTX 8

// hcWider: LZ4HC_InsertAndGetWiderMatch for a prefix without dictionary
// (every candidate is at or above lowestMatchIndex >= lowLimit = prefixIdx);
// the X variants add the external dictionary, F favorDecSpeed, D the
// attached dictionary.
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
// chain distances >= 1). X variants: lowLimit < prefixIdx, and the
// dictionary's prefixIdx - lowLimit bytes at W_DICTB + lowLimit.
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
// (7) X variants: a dictionary candidate (lowLimit <= lowestMatchIndex <=
//     matchIndex <= prefixIdx - 4) is read at mp = dictStart + matchIndex -
//     lowLimit, 4 bytes within the dictionary; the count compares ip+4.. with
//     mp+4.. below vLimit = min(ip + prefixIdx - matchIndex, iHighLimit),
//     so mp's side ends at or before dictEnd, then (at vLimit) win from
//     prefixStart, below iHighLimit - (vLimit - prefixStart)... as in (4) for
//     the prefix; countBack stops at dictStart. The pattern analysis counts
//     a dictionary run forward below dictEnd and back above dictStart, and
//     continues across the boundary on prefixStart.. below iHighLimit or
//     dictEnd down to dictStart.
// (8) D variants: an attached dictionary's candidate dmi is read only when
//     ipIndex - (dmi + lowestMatchIndex - dEnd) <= 65535, 4 bytes at dBase
//     + dmi, then counted below vLimit = min(ip + dEnd - dmi, iHighLimit)
//     (its side ends at or before the dictionary's end) and back down to
//     its prefixStart; its entries are its own positions or end the search
//     by distance (as C's).
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

// hcWiderOptF: hcWiderOpt with favorDecSpeed.
#define WIDER_FAV
TEXT hcWiderOptF<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"
#undef WIDER_FAV
#undef WIDER_SWAP
#undef WIDER_PA

// The same four with the external dictionary.
#define WIDER_EXT
TEXT hcWiderX<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

#define WIDER_PA
TEXT hcWiderXPA<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

#define WIDER_SWAP
TEXT hcWiderXOpt<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

#define WIDER_FAV
TEXT hcWiderXOptF<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"
#undef WIDER_FAV
#undef WIDER_SWAP
#undef WIDER_PA
#undef WIDER_EXT

// The first four with the attached dictionary.
#define WIDER_DCTX
TEXT hcWiderD<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

#define WIDER_PA
TEXT hcWiderDPA<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

#define WIDER_SWAP
TEXT hcWiderDOpt<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"

#define WIDER_FAV
TEXT hcWiderDOptF<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_wider_amd64.h"
#undef WIDER_FAV
#undef WIDER_SWAP
#undef WIDER_PA
#undef WIDER_DCTX

// hcCountPattern: CX = the end of the repetition of pattern (16(SP) here,
// the caller's W_PAT one CALL deeper) from CX, below R11 (LZ4HC_countPattern:
// the count is the end minus the start). Clobbers AX, DX, R13.
TEXT hcCountPattern<>(SB), NOSPLIT|NOFRAME, $0
	MOVL (16+W_PAT)(SP), DX
	JMP  hcCountPatternR<>(SB)

// hcCountPatternR: hcCountPattern of the pattern in DX (32 bits).
//
// BOUNDS: 8-byte loads at CX while CX < R11 - 7, then single bytes below
// R11.
TEXT hcCountPatternR<>(SB), NOSPLIT|NOFRAME, $0
	MOVL DX, DX
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
#define O_NBSEQ   (W_SIZE+168)

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
	MOVQ AX, P_MAXATT(SP); \
	MOVQ A_LOWLIM(DI), AX; \
	MOVQ AX, W_LOWLIM(SP); \
	MOVQ A_DICT(DI), CX; \
	SUBQ AX, CX; \
	MOVQ CX, W_DICTB(SP); \
	MOVQ A_DHASH(DI), AX; \
	MOVQ AX, W_DHASH(SP); \
	MOVQ A_DCHAIN(DI), AX; \
	MOVQ AX, W_DCHAIN(SP); \
	MOVQ A_DBASE(DI), AX; \
	MOVQ AX, W_DBASE(SP); \
	MOVQ A_DEND(DI), AX; \
	MOVQ AX, W_DEND(SP); \
	MOVQ A_DSTART(DI), AX; \
	MOVQ AX, W_DSTART(SP)

// WIDER_CALL: hcWider, hcWiderPA with the pattern analysis, and their X
// and D variants with the external or attached dictionary.
#define WIDER_CALL(pa, ext, extpa, dctx, dctxpa, done) \
	MOVQ W_PA(SP), AX; \
	CMPQ AX, $PA_PA; \
	JEQ  pa; \
	JHI  ext; \
	CALL hcWider<>(SB); \
	JMP  done; \
pa: \
	CALL hcWiderPA<>(SB); \
	JMP  done; \
ext: \
	CMPQ AX, $PA_DCTX; \
	JHS  dctx; \
	CMPQ AX, $(PA_EXT|PA_PA); \
	JEQ  extpa; \
	CALL hcWiderX<>(SB); \
	JMP  done; \
extpa: \
	CALL hcWiderXPA<>(SB); \
	JMP  done; \
dctx: \
	CMPQ AX, $(PA_DCTX|PA_PA); \
	JEQ  dctxpa; \
	CALL hcWiderD<>(SB); \
	JMP  done; \
dctxpa: \
	CALL hcWiderDPA<>(SB); \
done:

// func hcHashChainAsm(a *hcParseArgs) int
//
// LZ4HC_compress_hashChain's main loop for a.win[a.ip:a.iend] (the end of
// win, a prefix at a.prefixIdx, with an external dictionary at a.dict from
// a.lowLimit if PA_EXT), from a.ip, a.anchor and a.op, with a.attempts
// searches and the pattern analysis if PA_PA. It returns 0 at the end of
// the loop (ip > mflimit), 1 when a sequence does not fit the limit
// (limitedOutput: C returns 0; fillOutput: a.mlen and a.moff are the
// sequence's match), 3 when a write would pass the end of dst (cannot
// happen), and 2 at the top of the loop once ip > a.stop (call again to go
// on); a.ip, a.anchor, a.op and a.next (nextToUpdate) are updated.
// a.olimit < 0 is notLimited; with fillOutput it is oend (maxOutputSize -
// LASTLITERALS).
//
// Registers across the loop: SI win, R8 chainTable, R11 matchlimit, R12
// prefixIdx; everything else is in the frame.
//
// Preconditions (checked by compressHashChain): a.iend - a.ip >= LZ4_minLength,
// a.iend = len(win), a.next >= a.prefixIdx, a.lowLimit == a.prefixIdx
// without PA_EXT, below it with (the dictionary's a.prefixIdx - a.lowLimit
// bytes at a.dict); len(dst) >= a.olimit (limited) or
// LZ4_compressBound(a.iend - a.ip).
//
// BOUNDS: the searches as in hcWider, with ip <= mflimit and iLowLimit <=
// ip at each call (search 2 from start2 = ip + m1.len - 2 >= ip with
// iLowLimit ip, longest m1.len; search 3 from start3 = start2 + m2.len - 3
// with iLowLimit start2, longest m2.len), and both only when the start plus
// the current length is at most mflimit. Writes to dst as in ENCODE.
TEXT ·hcHashChainAsm(SB), NOSPLIT, $384-16
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
	WIDER_CALL(w1pa, w1x, w1xpa, w1d, w1dpa, w1done)
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
	WIDER_CALL(w2pa, w2x, w2xpa, w2d, w2dpa, w2done)
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
	WIDER_CALL(w3pa, w3x, w3xpa, w3d, w3dpa, w3done)
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
	MOVQ M1LEN(SP), AX
	MOVQ M1OFF(SP), DX
	MOVQ $1, BX
	JMP  failOut

fail2:
	MOVQ $3, BX
	JMP  out

failOut:
	// the sequence that did not fit (for fillOutput)
	MOVQ P_ARGS(SP), DI
	MOVQ AX, A_MLEN(DI)
	MOVQ DX, A_MOFF(DI)
	JMP  out

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
#include "lz4hc_addmatch_amd64.h"

// hcAddMatchF: hcAddMatch with favorDecSpeed (basePrice + 1, and a price
// replaces opt[pos]'s only when lower).
#define ADD_FAV
TEXT hcAddMatchF<>(SB), NOSPLIT|NOFRAME, $0
#include "lz4hc_addmatch_amd64.h"
#undef ADD_FAV

// OPT_CALL: hcWiderOpt, or its X or D variant (external or attached
// dictionary); OPT_CALLF the same with favorDecSpeed (F variants), then
// its cut of lengths 19 to 36 to 18 (LZ4HC_FindLongestMatch).
#define OPT_CALL(x, d, done) \
	MOVQ W_PA(SP), AX; \
	TESTQ AX, AX; \
	JNE  x; \
	CALL hcWiderOpt<>(SB); \
	JMP  done; \
x: \
	CMPQ AX, $PA_EXT; \
	JNE  d; \
	CALL hcWiderXOpt<>(SB); \
	JMP  done; \
d: \
	CALL hcWiderDOpt<>(SB); \
done:

#define OPT_CALLF(x, d, called, done) \
	MOVQ W_PA(SP), AX; \
	CMPQ AX, $PA_FAV; \
	JNE  x; \
	CALL hcWiderOptF<>(SB); \
	JMP  called; \
x: \
	CMPQ AX, $(PA_EXT|PA_FAV); \
	JNE  d; \
	CALL hcWiderXOptF<>(SB); \
	JMP  called; \
d: \
	CALL hcWiderDOptF<>(SB); \
called: \
	LEAQ -19(BX), CX; \
	CMPQ CX, $(36-19); \
	JHI  done; \
	MOVL $18, BX; \
done:

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
// LZ4HC_compress_optimal_internal's main loop, for the same inputs as
// hcHashChainAsm, with a.attempts searches (nbSearches; a.nbSeq,
// nbSearchesSeq, where favorDecSpeed (PA_FAV) searches with fewer), a.suff
// (sufficientLen = min(targetLength, LZ4_OPT_NUM-1)) and a.opt (the
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
TEXT ·hcOptimalAsm(SB), NOSPLIT, $384-16
#include "lz4hc_optimal_amd64.h"

// func hcOptimalFavAsm(a *hcParseArgs) int
//
// hcOptimalAsm with favorDecSpeed (a.pa has PA_FAV).
//
// BOUNDS: as hcOptimalAsm's.
#define OPT_FAV
TEXT ·hcOptimalFavAsm(SB), NOSPLIT, $384-16
#include "lz4hc_optimal_amd64.h"
#undef OPT_FAV
