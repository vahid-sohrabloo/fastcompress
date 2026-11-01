//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASHL(pos, dst): long hash, (le64(b[pos:]) * prime8bytes) >> hShiftL.
// HASHS(pos, dst): short hash, ((le64(b[pos:]) << shiftS) * primeS) >> hShiftS,
// computed as (le64 * PRIMES) >> hShiftS with PRIMES = primeS << shiftS.
// Both clobber CX.
#define HASHL(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	MOVQ  $0xCF1BBCDCB7A56463, CX; \
	IMULQ CX, dst; \
	MOVQ  hShiftL+88(FP), CX; \
	SHRQ  CX, dst

#define HASHS(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	IMULQ PRIMES, dst; \
	MOVQ  hShiftS+104(FP), CX; \
	SHRQ  CX, dst

// Frame locals (hardware SP).
#define CURR 0(SP)
#define HL1 8(SP)
#define IDXS0 16(SP)
#define IDXL1 24(SP)
#define IP1 32(SP)
#define STEP 40(SP)
#define ILIMIT 56(SP)
#define MLEN 64(SP)
#define MATCHL1 72(SP)
#define PRIMES 80(SP)
#define NEXTSTEP 88(SP)
#define PRIMEL 96(SP)

// COUNTI(call, done): R11 = fastCount (the common prefix length of
// R9[0:R13] and R10[0:R13]), with the first 8 bytes compared here: most
// matches end within them, without the call. Clobbers CX (and, through
// the call, BX and Y0).
#define COUNTI(call, done) \
	CMPQ R13, $8; \
	JLT  call; \
	MOVQ (R10), CX; \
	XORQ (R9), CX; \
	JEQ  call; \
	BSFQ CX, R11; \
	SHRQ $3, R11; \
	JMP  done; \
call: \
	CALL ·fastCount(SB); \
done:

// func compressBlockDoubleFastAsm(hashLong, hashSmall *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip, istart, iend, prefixLowest int, prefixLowestIndex, baseIdx, rep1, rep2 uint32,
//	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockDoubleFastNoDictGeneric's outer loop (C:
// zstd_double_fast.c:ZSTD_compressBlock_doubleFast_noDict_generic).
//
// Registers: SI b, DI hashLong, R15 hashSmall, R8 ip, R9 ip1,
// R10 b - baseIdx (so an index addresses b directly; reloaded at outer, as
// the helpers clobber it; no calls in the search loop, so the GC never sees
// it), step in STEP,
// R12 hl0, R13 idxl0, R14 offset1, AX curr (copied to CX before the
// candidates, which use AX as scratch), DX idxs0, R11 read64(ip), BX
// scratch then read64(ip1); nextStep in NEXTSTEP.
// offset2, anchor, nSeq and nLits live in their argument/result slots.
// For a match: R8 ip, DX match position, AX mLength, R12 offBase.
// LONGCAND compares the 8 bytes at idxl0 (R13) with ip's (R11), going to
// longmatch if they match (a miss falls through to nolong, which follows
// it); SHORTCAND the 4 bytes at idxs0 (DX), going to searchlong if they
// match. CX = curr. Every entry is below curr (else
// bad), so with idx >= prefixLowestIndex >= baseIdx (the Go caller checks
// the latter), R10 + idx lies in b[:ip].
//
// compressBlockDoubleFastAsm branches on idx >= prefixLowestIndex, which
// predicts well once the frame has filled the tables.
// compressBlockDoubleFastAsmStale does not branch on it (C selects the
// address with ZSTD_selectAddr): below prefixLowestIndex idx becomes curr,
// so ip itself is read, and the carry from the index compare is added, so
// the compare fails (C's zstd_fast.c uses read32(ip0) ^ 1 the same way).
// That is faster while the tables still hold many entries from before the
// window (a young frame in a reused context), where the branch mispredicts.
#define LONGCAND \
	CMPL R13, prefixLowestIndex+72(FP); \
	JB   nolong; \
	CMPL R13, CX; \
	JAE  bad; \
	MOVQ (R10)(R13*1), AX; \
	CMPQ AX, R11; \
	JEQ  longmatch

#define SHORTCAND \
	CMPL DX, prefixLowestIndex+72(FP); \
	JB   noshort; \
	CMPL DX, CX; \
	JAE  bad; \
	MOVL (R10)(DX*1), AX; \
	CMPL AX, R11; \
	JEQ  searchlong

TEXT ·compressBlockDoubleFastAsm(SB), NOSPLIT, $104-160
#include "zstd_double_fast_amd64.h"

#undef LONGCAND
#undef SHORTCAND
#define LONGCAND \
	CMPL    R13, CX; \
	JAE     bad; \
	CMPL    R13, prefixLowestIndex+72(FP); \
	CMOVLCS CX, R13; \
	MOVQ    (R10)(R13*1), AX; \
	ADCQ    $0, AX; \
	CMPQ    AX, R11; \
	JEQ     longmatch

#define SHORTCAND \
	CMPL    DX, CX; \
	JAE     bad; \
	CMPL    DX, prefixLowestIndex+72(FP); \
	CMOVLCS CX, DX; \
	MOVL    (R10)(DX*1), AX; \
	ADCL    $0, AX; \
	CMPL    AX, R11; \
	JEQ     searchlong

// func compressBlockDoubleFastAsmStale: compressBlockDoubleFastAsm without
// a branch on the candidates' indices (see LONGCAND).
TEXT ·compressBlockDoubleFastAsmStale(SB), NOSPLIT, $104-160
#include "zstd_double_fast_amd64.h"
