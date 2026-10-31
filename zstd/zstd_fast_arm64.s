//go:build arm64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASH(pos, dst): dst = (le64(b[pos:]) * (prime << mlsShift)) >> hShift,
// which equals ((le64 << mlsShift) * prime) >> hShift mod 2^64.
#define HASH(pos, dst) \
	MOVD (R0)(pos), dst; \
	MUL  R24, dst, dst; \
	LSR  R25, dst, dst

// func compressBlockFastAsm(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
//	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockFastNoDictGeneric's loop (C: zstd_fast.c:
// ZSTD_compressBlock_fast_noDict_generic, _start to _cleanup), as
// zstd_fast_amd64.s.
//
// Registers: R0 b, R1 hashTable, R2 ip0, R3 ip1, R4 ip2 (ip3 = ip2 + 1),
// R5 hash0, R6 hash1, R7 matchIdx, R8 rep1, R9 step, R10 nextStep,
// R11 rep2, R12 anchor, R13 current0, R21 ilimit, R22 baseIdx,
// R23 prefixStartIndex, R24 prime << mlsShift, R25 hShift; R14-R17, R19,
// R20 scratch (the helpers clobber them). nSeq and nLits live in their
// result slots. After a match: R2 ip0, R5 match0, R6 mLength, R7 offBase;
// store takes R3 litLen and R10 the return selector.
// CANDIDATE(hit, miss): compare the 4 bytes at matchIdx (R7) with ip0's,
// going to hit if they match; a miss falls through to the label miss,
// which must follow. matchIdx < current0 (R13), else bad. The two
// variants are those of zstd_fast_amd64.s: compressBlockFastAsmStale reads
// ip0 itself for matchIdx < prefixStartIndex (CSELW) and increments the
// value (CINCW), so the compare fails.
#define CANDIDATE(hit, miss) \
	CMPW  R23, R7; \
	BLO   miss; \
	CMPW  R13, R7; \
	BHS   bad; \
	SUBW  R22, R7, R14; \
	MOVWU (R0)(R14), R15; \
	MOVWU (R0)(R2), R16; \
	CMPW  R15, R16; \
	BEQ   hit

TEXT ·compressBlockFastAsm(SB), NOSPLIT, $8-152
#include "zstd_fast_arm64.h"

#undef CANDIDATE
#define CANDIDATE(hit, miss) \
	CMPW  R13, R7; \
	BHS   bad; \
	CMPW  R23, R7; \
	CSELW LO, R13, R7, R7; \
	SUBW  R22, R7, R14; \
	MOVWU (R0)(R14), R15; \
	CINCW LO, R15, R15; \
	MOVWU (R0)(R2), R16; \
	CMPW  R15, R16; \
	BEQ   hit

TEXT ·compressBlockFastAsmStale(SB), NOSPLIT, $8-152
#include "zstd_fast_arm64.h"

// fastCopyLits copies R16 (> 0) bytes from R15 to R14 without reading or
// writing outside either range. Clobbers R17, R19, V0.
TEXT ·fastCopyLits(SB), NOSPLIT|NOFRAME, $0-0
	CMP  $16, R16
	BLO  small
	MOVD ZR, R17

loop16:
	ADD  R17, R15, R19
	VLD1 (R19), [V0.B16]
	ADD  R17, R14, R19
	VST1 [V0.B16], (R19)
	ADD  $16, R17
	ADD  $16, R17, R19
	CMP  R16, R19
	BLS  loop16
	ADD  R16, R15, R19            // last 16 bytes (may overlap)
	SUB  $16, R19
	VLD1 (R19), [V0.B16]
	ADD  R16, R14, R19
	SUB  $16, R19
	VST1 [V0.B16], (R19)
	RET

small:
	CMP  $8, R16
	BLO  lt8
	MOVD (R15), R17
	MOVD R17, (R14)
	ADD  R16, R15, R19
	MOVD -8(R19), R17
	ADD  R16, R14, R19
	MOVD R17, -8(R19)
	RET

lt8:
	CMP   $4, R16
	BLO   lt4
	MOVWU (R15), R17
	MOVW  R17, (R14)
	ADD   R16, R15, R19
	MOVWU -4(R19), R17
	ADD   R16, R14, R19
	MOVW  R17, -4(R19)
	RET

lt4:
	MOVBU (R15), R17
	MOVB  R17, (R14)
	CMP   $2, R16
	BLO   done
	ADD   R16, R15, R19
	MOVHU -2(R19), R17
	ADD   R16, R14, R19
	MOVH  R17, -2(R19)

done:
	RET

// fastCount returns in R17 the length of the common prefix of R14[0:R16]
// and R15[0:R16] (C: ZSTD_count). Reads only within those ranges.
// Clobbers R14-R16, R19, R20.
TEXT ·fastCount(SB), NOSPLIT|NOFRAME, $0-0
	MOVD ZR, R17

cnt8:
	CMP    $8, R16
	BLT    tail
	MOVD.P 8(R14), R19
	MOVD.P 8(R15), R20
	EOR    R19, R20, R20
	CBNZ   R20, diff
	ADD    $8, R17
	SUB    $8, R16
	B      cnt8

diff:
	RBIT R20, R20
	CLZ  R20, R20
	ADD  R20>>3, R17, R17
	RET

tail:
	CMP     $0, R16
	BLE     out
	MOVBU.P 1(R14), R19
	MOVBU.P 1(R15), R20
	CMPW    R19, R20
	BNE     out
	ADD     $1, R17
	SUB     $1, R16
	B       tail

out:
	RET
