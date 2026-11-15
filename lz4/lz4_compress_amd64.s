//go:build amd64 && !appengine && !noasm && !purego && gc

// The fast compressor's main loop (lz4_compress_amd64.h) for the byU32 and
// byU16 hash tables.

#include "textflag.h"

// byU32: 4096 entries, LZ4_hash5 of the 8 bytes at ptr (64-bit C):
// ((v << 24) * 889523592379) >> 52, with the shift folded into the
// multiplier (equal mod 2^64), as gcc compiles it
#define TABLE_LOAD(h, dst) \
	MOVL (R10)(h*4), dst

#define TABLE_STORE(val, h) \
	MOVL val, (R10)(h*4)

#define HASH(ptr, dst) \
	MOVQ  (ptr), dst; \
	MOVQ  $0xcf1bbcdcbb000000, R12; \
	IMULQ R12, dst; \
	SHRQ  $52, dst

// func compressFast32Asm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)
//
// Runs LZ4_compress_generic_validated from the second byte (Go put the first
// one) to _last_literals with a byU32 table, for noDict (prefixLen 0, lowIdx
// = clampIdx = startIndex) or withPrefix64k (the prefixLen bytes before src
// are the history, lowIdx = startIndex - prefixLen for dictSmall else 0,
// clampIdx 0), returning the final anchor and op, status 0; status 1 when
// the block does not fit (C returns 0 for limitedOutput), 2 on a failed
// internal bound check (cannot happen, see BOUNDS). olimit < 0 is
// notLimited. It starts at ip0 = 1, anchor0 = op0 = 0, or with resume != 0
// at C's _resume label with ip = anchor (ip0 = anchor0) and op0: the second
// part of an extDict compression (LZ4_compress_resumeWithPrefix), whose
// candidates are within src (prefixLen 0) or out of reach.
//
// Preconditions (checked by compressNoDictAsm and compressPrefixAsm):
// len(src) >= LZ4_minLength (13); with resume, ip0 < len(src) - 11 and op0 <=
// olimit (as the extDict part left them); the prefixLen bytes before src belong to
// the same buffer; every table entry is an earlier position (an index
// relative to src - startIndex, below the current one); an entry below
// startIndex - prefixLen is either below lowIdx or more than
// LZ4_DISTANCE_MAX behind every position of src (noDictIssue with a prefix
// of 64 KB, or entries all >= startIndex - prefixLen); startIndex +
// len(src) < 2 GB; len(dst) >= olimit (limited) or LZ4_compressBound
// (not limited).
//
// BOUNDS:
// (1) src reads: hashes read 8 bytes at forwardIp <= mflimitPlusOne =
//     iend-11 (checked first), at ip and ip-1 with ip < mflimitPlusOne
//     (afterML), all below iend-3. Match candidates are read only when
//     matchIndex >= lowIdx and matchIndex + LZ4_DISTANCE_MAX >= current
//     (both checked first), and matchIndex < current (table entries are
//     earlier positions), so by the preconditions base+matchIndex lies in
//     [src - prefixLen, ip): 4 bytes at match < ip, 8 bytes at
//     base+ctxIndex <= ip (ctxIndex <= current, checked the same way). The
//     catch up reads ip[-1] > anchor >= src and match[-1] > src -
//     prefixLen. LZ4_count reads below matchlimit = iend-5 on the ip side
//     and at the same distance behind on the match side. Literals are
//     copied in 8-byte chunks from anchor, so below ip+8 <= iend-3.
// (2) dst writes: before the literals (token, run bytes, literals in 8-byte
//     chunks) op + litLength + 8 + litLength/255 <= olimit (limited) is
//     checked, and the chunk copy runs only when op+litLength+8 <= dstEnd,
//     otherwise byte by byte; the run bytes stop below op+litLength-15+1
//     <= dstEnd (checked). The offset is written after op+2 <= dstEnd is
//     checked. The match run (4-byte stores and the last byte) is written
//     below op + matchCode/255 + 5 <= dstEnd (checked). With notLimited
//     and len(dst) >= LZ4_compressBound(len(src)) C's own bound proof gives
//     the same: the checks cannot fail (status 2).
TEXT ·compressFast32Asm(SB), NOSPLIT, $48-160
#include "lz4_compress_amd64.h"

// func compressFast32NoDictAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)
//
// compressFast32Asm for noDict only (prefixLen 0, lowIdx = startIndex, no
// resume), which compares a candidate's 4 bytes before its distance.
//
// BOUNDS: as compressFast32Asm's; a candidate is read when matchIndex >=
// lowIdx = startIndex (checked first), so at or above src, and below ip.
#define LZ4_NODICT
TEXT ·compressFast32NoDictAsm(SB), NOSPLIT, $48-160
#include "lz4_compress_amd64.h"
#undef LZ4_NODICT

// func compressFastExtAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx int, dict []byte, seqLimit int) (anchor, op, status int)
//
// The main loop of compressGenericValidated for usingExtDict (dict is the
// history, the input src is elsewhere), byU32 and limitedOutput, from the
// second byte to _last_literals (status 0), or to the suspension at
// seqLimit (src index; status 3: anchor is the ip to resume at, op the
// output so far). Status 1: the block does not fit; 2: a failed internal
// bound check (cannot happen). lowIdx is C's prefixIdxLimit for dictSmall,
// else 0.
//
// Preconditions (checked by compressExtDictAsm): len(src) >= LZ4_minLength;
// table entries are earlier positions (indexes relative to src -
// startIndex), those below startIndex in the dictionary: an entry is below
// lowIdx, more than LZ4_DISTANCE_MAX behind every position of src, or within
// [startIndex - len(dict), startIndex - 4] (noDictIssue: a 64 KB dictionary
// or one covering every index; C inserts dictionary positions up to its end
// minus 8 (LZ4_loadDict) or minus 12 (a compressed block)); seqLimit is
// len(src) - 11 or LZ4_DISTANCE_MAX below it; startIndex + len(src) < 2 GB;
// len(dst) >= olimit.
//
// BOUNDS: as compressFast32Asm's, with these differences. (1) A candidate
// below startIndex is read at dictBase + index: 4 bytes only when index >=
// lowIdx and index + LZ4_DISTANCE_MAX >= current (both checked first), so,
// by the preconditions, within [dict, dictEnd); the 8 bytes of LZ4_match8
// only when ctxIndex + 8 <= startIndex (checked), below dictEnd. The catch
// up stops at the dictionary's start. A match in the dictionary is counted
// below min(ip + (dictEnd - match), matchlimit) on the ip side, so below
// dictEnd on the match side, then from the start of src as a match in src.
TEXT ·compressFastExtAsm(SB), NOSPLIT, $96-144
#include "lz4_compress_ext_amd64.h"

#undef TABLE_LOAD
#undef TABLE_STORE
#undef HASH

// byU16: 8192 entries, LZ4_hash4 of the 4 bytes at ptr
#define TABLE_LOAD(h, dst) \
	MOVWLZX (R10)(h*2), dst

#define TABLE_STORE(val, h) \
	MOVW val, (R10)(h*2)

#define HASH(ptr, dst) \
	MOVL   (ptr), dst; \
	IMUL3L $-1640531535, dst, dst; \
	SHRL   $19, dst

// func compressFast16Asm(src, dst []byte, table *uint16, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)
//
// compressFast32Asm for a byU16 table and noDict (inputs below
// LZ4_64Klimit, and startIndex + len(src) < 0xFFFF as LZ4_prepareTable
// ensures).
//
// BOUNDS: as compressFast32NoDictAsm's; the hash is 13 bits (8192 entries
// of 2 bytes, the table's 16 KB).
#define LZ4_NODICT
#define LZ4_U16
TEXT ·compressFast16Asm(SB), NOSPLIT, $48-160
#include "lz4_compress_amd64.h"
#undef LZ4_U16
#undef LZ4_NODICT
