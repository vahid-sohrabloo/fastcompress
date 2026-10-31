//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// HASHF(pos, dst): dst = (le64(b[pos:]) * (prime << mlsShift)) >> CX,
// which is ((le64 << mlsShift) * prime) >> hShift mod 2^64 with CX = hShift.
#define HASHF(pos, dst) \
	MOVQ  (SI)(pos*1), dst; \
	IMULQ 32(SP), dst; \
	SHRQ  CX, dst

// HASH(pos, dst) is HASHF loading CX = hShift first.
#define HASH(pos, dst) \
	MOVQ hShift+96(FP), CX; \
	HASHF(pos, dst)

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

// func compressBlockFastAsm(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
//	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
//	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
//
// Mirrors compressBlockFastNoDictGeneric's loop (C: zstd_fast.c:
// ZSTD_compressBlock_fast_noDict_generic, _start to _cleanup).
//
// Registers in the search loop: SI b, DI hashTable, R8 ip0, R9 ip1, R10 ip2
// (ip3 = ip2 + 1), R11 hash0, R12 hash1, R13 matchIdx, R14 rep1,
// BX baseIdx, R15 b - baseIdx (so an index addresses b directly; no calls
// in the loop, so the GC never sees it), CX hShift, AX/DX scratch. BX, R15
// and CX are reloaded at start. rep2, anchor, nSeq and nLits live in their
// argument and result slots; the frame holds current0 at 0(SP), ilimit at
// 8(SP), step at 16(SP), nextStep at 24(SP) and prime << mlsShift at 32(SP).
// After a match: R8 ip0, DX match0, AX mLength, R12 offBase.
// CANDIDATE(hit, miss): compare the 4 bytes at matchIdx (R13) with ip0's,
// going to hit if they match; a miss falls through to the label miss,
// which must follow. matchIdx < current0 (AX), else bad; with
// prefixStartIndex <= matchIdx, R15 + matchIdx lies in b[prefixStart:ip0]
// (the Go caller checks prefixStartIndex >= baseIdx).
//
// compressBlockFastAsm branches on matchIdx >= prefixStartIndex, which
// predicts well once the frame has filled the table. compressBlockFastAsmStale
// does not branch on it (C: zstd_fast.c's mval = idx >= prefixStartIndex ?
// read32(match) : read32(ip0) ^ 1): below prefixStartIndex matchIdx becomes
// current0, so ip0 itself is read, and the carry from the index compare is
// added, so the compare fails (matchIdx is not used again). That is faster
// while the table still holds many entries from before the window (a young
// frame in a reused context), where the branch mispredicts.
#define CANDIDATE(hit, miss) \
	CMPL R13, prefixStartIndex+64(FP); \
	JB   miss; \
	CMPL R13, AX; \
	JAE  bad; \
	MOVL (R15)(R13*1), DX; \
	CMPL DX, (SI)(R8*1); \
	JEQ  hit

TEXT ·compressBlockFastAsm(SB), NOSPLIT, $40-152
#include "zstd_fast_amd64.h"

#undef CANDIDATE
#define CANDIDATE(hit, miss) \
	CMPL    R13, AX; \
	JAE     bad; \
	CMPL    R13, prefixStartIndex+64(FP); \
	CMOVLCS AX, R13; \
	MOVL    (R15)(R13*1), DX; \
	ADCL    $0, DX; \
	CMPL    DX, (SI)(R8*1); \
	JEQ     hit

// func compressBlockFastAsmStale: compressBlockFastAsm without a branch on
// the candidate's index (see CANDIDATE).
TEXT ·compressBlockFastAsmStale(SB), NOSPLIT, $40-152
#include "zstd_fast_amd64.h"

// fastCopyLits copies R9 (> 0) bytes from R11 to R10 without reading or
// writing outside either range. Clobbers CX, R13, X0.
TEXT ·fastCopyLits(SB), NOSPLIT, $0-0
	CMPQ R9, $16
	JB   small
	XORL R13, R13

loop16:
	MOVOU (R11)(R13*1), X0
	MOVOU X0, (R10)(R13*1)
	ADDQ  $16, R13
	LEAQ  16(R13), CX
	CMPQ  CX, R9
	JLS   loop16
	MOVOU -16(R11)(R9*1), X0   // last 16 bytes (may overlap)
	MOVOU X0, -16(R10)(R9*1)
	RET

small:
	CMPQ R9, $8
	JB   lt8
	MOVQ (R11), CX
	MOVQ CX, (R10)
	MOVQ -8(R11)(R9*1), CX
	MOVQ CX, -8(R10)(R9*1)
	RET

lt8:
	CMPQ R9, $4
	JB   lt4
	MOVL (R11), CX
	MOVL CX, (R10)
	MOVL -4(R11)(R9*1), CX
	MOVL CX, -4(R10)(R9*1)
	RET

lt4:
	MOVB (R11), CX
	MOVB CX, (R10)
	CMPQ R9, $2
	JB   done
	MOVW -2(R11)(R9*1), CX
	MOVW CX, -2(R10)(R9*1)

done:
	RET

// fastCount returns in R11 the length of the common prefix of R9[0:R13]
// and R10[0:R13] (C: ZSTD_count). Reads only within those ranges.
// Once the first 8 bytes match it compares 32 bytes per step with AVX2
// when useAVX2 is set. Clobbers CX, BX, Y0.
TEXT ·fastCount(SB), NOSPLIT, $0-0
	XORL R11, R11
	CMPQ R13, $8
	JLT  tail
	MOVQ (R10), CX
	XORQ (R9), CX
	JNE  diff
	MOVL $8, R11
	CMPB ·useAVX2(SB), $0
	JEQ  cnt8

cnt32:
	LEAQ 32(R11), CX
	CMPQ CX, R13
	JGT  cnt32end
	VMOVDQU   (R10)(R11*1), Y0
	VPCMPEQB  (R9)(R11*1), Y0, Y0
	VPMOVMSKB Y0, CX
	INCL      CX               // all equal: 0xFFFFFFFF + 1 = 0; else the lowest set bit is the first mismatch
	JNE       cnt32diff
	ADDQ      $32, R11
	JMP       cnt32

cnt32diff:
	VZEROUPPER
	BSFL CX, CX
	ADDQ CX, R11
	RET

cnt32end:
	VZEROUPPER

cnt8:
	LEAQ 8(R11), CX
	CMPQ CX, R13
	JGT  tail
	MOVQ (R10)(R11*1), CX
	XORQ (R9)(R11*1), CX
	JNE  diff
	ADDQ $8, R11
	JMP  cnt8

diff:
	BSFQ CX, CX
	SHRQ $3, CX
	ADDQ CX, R11
	RET

tail:
	CMPQ    R11, R13
	JGE     out
	MOVBLZX (R10)(R11*1), CX
	MOVBLZX (R9)(R11*1), BX
	CMPL    CX, BX
	JNE     out
	INCQ    R11
	JMP     tail

out:
	RET
