//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASHL(pos, dst): long hash, (le64(b[pos:]) * prime8bytes) >> hShiftL.
// HASHS(pos, dst): short hash, (le64(b[pos:]) * (primeS << shiftS)) >> hShiftS,
// which equals ((le64 << shiftS) * primeS) >> hShiftS mod 2^64.
#define HASHL(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R23, dst, dst; \
	LSR  R24, dst, dst

#define HASHS(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R25, dst, dst; \
	LSR  R26, dst, dst

// Frame local.
#define IDXS0 8(RSP)

// func compressBlockDoubleFastAsm(hashLong, hashSmall *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip, istart, iend, prefixLowest int, prefixLowestIndex, baseIdx, rep1, rep2 uint32,
//	hShiftL, shiftS, hShiftS, primeS uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockDoubleFastNoDictGeneric's outer loop (C:
// zstd_double_fast.c:ZSTD_compressBlock_doubleFast_noDict_generic), as
// zstd_double_fast_amd64.s.
//
// Registers: R0 b, R1 hashLong, R2 hashSmall, R3 ip, R4 ip1, R5 step,
// R6 nextStep, R7 hl0, R8 idxl0, R9 offset1, R10 curr, R11 hl1, R12 idxl1,
// R13 ilimit, R21 baseIdx, R22 prefixLowestIndex, R23 prime8bytes,
// R24 hShiftL, R25 primeS << shiftS, R26 hShiftS; R14-R17, R19, R20
// scratch (the helpers clobber them). offset2, anchor, nSeq and nLits live
// in their argument/result slots. For a match: R3 ip, R6 match position,
// R7 mLength, R8 offset (then offBase); store takes R4 litLen and R5 the
// return selector.
// LONGCAND compares the 8 bytes at idxl0 (R8) with ip's, going to
// longmatch if they match (a miss falls through to nolong, which follows
// it); SHORTCAND the 4 bytes at idxs0 (R20), going to searchlong if they
// match. Both leave the match position in R14; every
// entry is below curr (R10), else bad. The variants are those of
// zstd_double_fast_amd64.s: compressBlockDoubleFastAsmStale reads ip itself
// for an index below prefixLowestIndex (CSELW) and increments the value
// (CINC), so the compare fails.
#define LONGCAND \
	CMPW  R22, R8; \
	BLO   nolong; \
	CMPW  R10, R8; \
	BHS   bad; \
	SUBW  R21, R8, R14; \
	MOVD  (R0)(R14), R15; \
	MOVD  (R0)(R3), R16; \
	CMP   R15, R16; \
	BEQ   longmatch

#define SHORTCAND \
	CMPW  R22, R20; \
	BLO   noshort; \
	CMPW  R10, R20; \
	BHS   bad; \
	SUBW  R21, R20, R14; \
	MOVWU (R0)(R14), R15; \
	MOVWU (R0)(R3), R16; \
	CMPW  R15, R16; \
	BEQ   searchlong

TEXT ·compressBlockDoubleFastAsm(SB), NOSPLIT, $16-160
#include "zstd_double_fast_arm64.h"

#undef LONGCAND
#undef SHORTCAND
#define LONGCAND \
	CMPW  R10, R8; \
	BHS   bad; \
	CMPW  R22, R8; \
	CSELW LO, R10, R8, R8; \
	SUBW  R21, R8, R14; \
	MOVD  (R0)(R14), R15; \
	CINC  LO, R15, R15; \
	MOVD  (R0)(R3), R16; \
	CMP   R15, R16; \
	BEQ   longmatch

#define SHORTCAND \
	CMPW  R10, R20; \
	BHS   bad; \
	CMPW  R22, R20; \
	CSELW LO, R10, R20, R20; \
	SUBW  R21, R20, R14; \
	MOVWU (R0)(R14), R15; \
	CINCW LO, R15, R15; \
	MOVWU (R0)(R3), R16; \
	CMPW  R15, R16; \
	BEQ   searchlong

TEXT ·compressBlockDoubleFastAsmStale(SB), NOSPLIT, $16-160
#include "zstd_double_fast_arm64.h"
