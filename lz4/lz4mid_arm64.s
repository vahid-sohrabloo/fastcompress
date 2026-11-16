//go:build arm64 && !appengine && !noasm && !purego && gc

// The main loop of LZ4MID_compress (level 2; lz4 v1.11.1 lib/lz4hc.c,
// Copyright (c) Yann Collet, BSD 2-Clause) for a prefix without dictionary
// (external or attached: those blocks run the Go loop on arm64),
// notLimited, limitedOutput or fillOutput (the last sequence is Go's). It
// follows midCompress in lz4hc.go statement for statement (and
// lz4mid_amd64.h's prefix-only routine step for step) and returns before
// the last literals.

#include "textflag.h"

// LZ4MID_hash8Ptr: ((le64 << 8) * 58295818150454627) >> (64-14), with the
// shift folded into the multiplier (equal mod 2^64, held in R19)
#define HASH8(ptr, dst) \
	MOVD (ptr), dst; \
	MUL  R19, dst, dst; \
	LSR  $50, dst, dst

// LZ4MID_hash4Ptr: (le32 * 2654435761) >> (32-14), the multiplier in R20
#define HASH4(ptr, dst) \
	MOVWU (ptr), dst; \
	MULW  R20, dst, dst; \
	LSRW  $18, dst, dst

// MIDCOUNT(a, b, out, t1, t2, t3, t4, ...labels): out = LZ4_count(a, b,
// matchlimit R6): the common prefix of the bytes at a (below R6) and at b.
// a is kept; b and t1-t4 are clobbered.
#define MIDCOUNT(a, b, out, t1, t2, t3, t4, l16, l16b, ldiff, l8, ltail, l2, l1, ldone) \
	MOVD a, out; \
l16: \
	SUB  $15, R6, t1; \
	CMP  t1, out; \
	BHS  l8; \
	LDP  (out), (t1, t2); \
	LDP  (b), (t3, t4); \
	EOR  t3, t1, t1; \
	CBNZ t1, ldiff; \
	EOR  t4, t2, t2; \
	CBNZ t2, l16b; \
	ADD  $16, out, out; \
	ADD  $16, b, b; \
	JMP  l16; \
l16b: \
	ADD  $8, out, out; \
	MOVD t2, t1; \
ldiff: \
	RBIT t1, t1; \
	CLZ  t1, t1; \
	ADD  t1>>3, out, out; \
	JMP  ldone; \
l8: \
	SUB  $7, R6, t1; \
	CMP  t1, out; \
	BHS  ltail; \
	MOVD (out), t1; \
	MOVD (b), t2; \
	EOR  t2, t1, t1; \
	CBNZ t1, ldiff; \
	ADD  $8, out, out; \
	ADD  $8, b, b; \
	JMP  l8; \
ltail: \
	SUB   $3, R6, t1; \
	CMP   t1, out; \
	BHS   l2; \
	MOVWU (out), t1; \
	MOVWU (b), t2; \
	CMPW  t1, t2; \
	BNE   l2; \
	ADD   $4, out, out; \
	ADD   $4, b, b; \
l2: \
	SUB   $1, R6, t1; \
	CMP   t1, out; \
	BHS   l1; \
	MOVHU (out), t1; \
	MOVHU (b), t2; \
	CMPW  t1, t2; \
	BNE   l1; \
	ADD   $2, out, out; \
	ADD   $2, b, b; \
l1: \
	CMP   R6, out; \
	BHS   ldone; \
	MOVBU (out), t1; \
	MOVBU (b), t2; \
	CMPW  t1, t2; \
	BNE   ldone; \
	ADD   $1, out, out; \
ldone: \
	SUB   a, out, out

// COUNTMATCH(ipreg, ipIndexReg, posReg, out, ...labels): LZ4MID_countMatch
// without external dictionary: 0 if ipIndex - pos > LZ4_DISTANCE_MAX or pos
// < prefixIdx, else LZ4_count(ip, win + pos - prefixIdx, matchlimit). The
// first 8 bytes are compared here (most candidates differ within them),
// the rest by MIDCOUNT. Clobbers R9, R10, R11, R13, R17 (posReg may be
// one of them).
#define COUNTMATCH(ipreg, ipIndexReg, posReg, out, slow, l16, l16b, ldiff, l8, ltail, l2, l1, lmc, done) \
	MOVD  ZR, out; \
	SUBW  posReg, ipIndexReg, R9; \
	LSRW  $16, R9, R9; \
	CBNZW R9, done; \
	CMPW  R7, posReg; \
	BLO   done; \
	SUBW  R7, posReg, R10; \
	ADD   R3, R10, R10; \
	SUB   $7, R6, R9; \
	CMP   R9, ipreg; \
	BHS   slow; \
	MOVD  (ipreg), R9; \
	MOVD  (R10), R11; \
	EOR   R11, R9, R9; \
	CBZ   R9, slow; \
	RBIT  R9, R9; \
	CLZ   R9, out; \
	LSR   $3, out, out; \
	JMP   done; \
slow: \
	MIDCOUNT(ipreg, R10, out, R9, R11, R13, R17, l16, l16b, ldiff, l8, ltail, l2, l1, lmc); \
done:

// func midCompressAsm(win []byte, ip, iend, prefixIdx int, t *uint32, dst []byte, olimit int, dict *byte, lowLimit int, d *midDictCtx) (nip, anchor, op, status, ml, md int)
//
// win is the prefix (prefixPtr = &win[0], index i is position prefixIdx + i);
// the input is win[ip:iend]; t is the LZ4MID tables (hash4 then hash8, 16384
// entries each); olimit < 0 is notLimited (with fillOutput, oend). dict,
// lowLimit and d are unused: the caller passes no dictionary (lowLimit =
// prefixIdx, d nil). It returns ip, anchor and op at the end of the main
// loop (status 0), or status 1 when a sequence does not fit (limitedOutput:
// C returns 0; fillOutput: ip, anchor and op before the sequence, ml and md
// its match length and distance).
//
// Registers: R0 ip, R1 op, R2 anchor, R3 win, R4 hash4 table, R5 mflimit,
// R6 matchlimit, R7 prefixIdx, R8 ipIndex (then the token), R19 and R20
// the hash multipliers, R21 olimit, R22 dstEnd, R23 ilimitIdx, R24
// matchLength, R25 matchDistance, R26 hash8 table, R13 and R14 the hash
// and the candidate at ip+1, R15 and R16 the hash4 and its entry; R9-R12,
// R17 scratch.
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
//     match by MIDCOUNT, which reads 16 bytes while 16 remain below
//     matchlimit, then 8, 4, 2 and 1 as they fit, on the ip side and at the
//     same distance behind on the match side. The catch back reads ip[-1]
//     (ip > anchor >= 0) and ip[-dist-1] with ip > dist.
// (2) dst writes: before a sequence, op + litLen/255 + litLen + 8 <= olimit
//     (limited) is checked; with notLimited dst holds the bound. Every
//     write is also checked against dstEnd (status 2, which cannot happen
//     within these limits): the literal run bytes, the offset, the match run
//     below op + mlCode/255 + 2; the literals are copied in 8-byte chunks
//     when op+litLen+8 <= dstEnd, else byte by byte.
TEXT ·midCompressAsm(SB), NOSPLIT, $0-160
	MOVD win_base+0(FP), R3
	MOVD ip+24(FP), R0
	ADD  R3, R0, R0
	MOVD R0, R2                  // anchor
	MOVD iend+32(FP), R9
	ADD  R3, R9, R9              // iend
	SUB  $12, R9, R5             // mflimit
	SUB  $5, R9, R6              // matchlimit
	SUB  $8, R9, R10             // ilimit
	SUB  R3, R10, R10
	MOVD prefixIdx+40(FP), R7
	ADDW R7, R10, R23            // ilimitIdx
	MOVD t+48(FP), R4
	ADD  $65536, R4, R26         // hash8 table
	MOVD dst_base+56(FP), R1
	MOVD dst_len+64(FP), R22
	ADD  R1, R22, R22            // dstEnd
	MOVD olimit+80(FP), R21
	CMP  $0, R21
	BGE  limited
	MOVD $-1, R21
	JMP  olimitSet

limited:
	ADD R1, R21, R21

olimitSet:
	MOVD $0xcf1bbcdcbfa56300, R19
	MOVW $2654435761, R20

loop:
	CMP  R5, R0
	BHI  done                    // ip > mflimit
	SUB  R3, R0, R8
	ADDW R7, R8, R8              // ipIndex

	// the short match's entry, loaded early (another table)
	HASH4(R0, R15)
	MOVWU (R4)(R15<<2), R16

	// search long match
	HASH8(R0, R12)
	MOVWU (R26)(R12<<2), R11     // pos8
	MOVW  R8, (R26)(R12<<2)
	MOVD  R11, R25
	COUNTMATCH(R0, R8, R11, R12, cm8slow, cm8a, cm8b, cm8c, cm8d, cm8e, cm8f, cm8g, cm8h, cm8)
	MOVD R12, R24
	SUBW R25, R8, R25            // matchDistance = ipIndex - pos8
	CMP  $4, R12
	BHS  found

	// search short match
	MOVW R8, (R4)(R15<<2)
	MOVD R16, R11                // pos4
	MOVD R16, R25
	COUNTMATCH(R0, R8, R11, R12, cm4slow, cm4a, cm4b, cm4c, cm4d, cm4e, cm4f, cm4g, cm4h, cm4)
	MOVD R12, R24
	SUBW R25, R8, R25            // matchDistance = ipIndex - pos4
	CMP  $4, R12
	BHS  found

	// no match: skip faster over incompressible data
	SUB R2, R0, R9
	LSR $9, R9, R9
	ADD R9, R0, R0
	ADD $1, R0, R0
	JMP loop

found:
	CMP   $8, R24
	BHS   encode
	CMP   R5, R0
	BHS   encode                 // ip < mflimit only
	ADD   $1, R0, R12            // ip+1
	HASH8(R12, R13)              // h8 at ip+1
	MOVWU (R26)(R13<<2), R14     // pos8 at ip+1
	ADDW  $1, R8, R15
	// ml2 = LZ4MID_countMatch(ip+1, ipIndex+1, pos8)
	MOVD  ZR, R11
	SUBW  R14, R15, R10
	LSRW  $16, R10, R9
	CBNZW R9, ml2done
	CMPW  R7, R14
	BLO   ml2done
	SUBW  R7, R14, R10
	ADD   R3, R10, R10
	SUB   $7, R6, R9
	CMP   R9, R12
	BHS   ml2slow
	MOVD  (R12), R9
	MOVD  (R10), R16
	EOR   R16, R9, R9
	CBZ   R9, ml2slow
	RBIT  R9, R9
	CLZ   R9, R11
	LSR   $3, R11, R11
	JMP   ml2done

ml2slow:
	MIDCOUNT(R12, R10, R11, R9, R16, R17, R15, ml2a, ml2b, ml2c, ml2d, ml2e, ml2f, ml2g, ml2h)

ml2done:
	CMP  R24, R11
	BLS  encode                  // ml2 <= matchLength
	CMPW R8, R14
	BEQ  encode                  // pos8 == ipIndex
	ADDW $1, R8, R15
	MOVW R15, (R26)(R13<<2)
	ADD  $1, R0, R0
	MOVD R11, R24
	SUBW R14, R15, R25           // matchDistance = ipIndex + 1 - pos8

encode:
	// catch back
	MOVD R25, R11                // matchDistance
	MOVD R24, R12                // matchLength

catchBack:
	CMP   R2, R0
	BLS   catchDone
	SUB   R3, R0, R9
	CMP   R11, R9
	BLS   catchDone              // (ip - prefixPtr) <= matchDistance
	SUB   R11, R0, R9
	MOVBU -1(R9), R10
	MOVBU -1(R0), R9
	CMPW  R9, R10
	BNE   catchDone
	SUB   $1, R0, R0
	ADD   $1, R12, R12
	JMP   catchBack

catchDone:
	MOVD R12, R24

	// fill table with beginning of match
	SUB  R3, R0, R8
	ADDW R7, R8, R8              // startMatchIdx
	ADD  $1, R0, R9
	HASH8(R9, R12)
	ADDW $1, R8, R11
	MOVW R11, (R26)(R12<<2)
	ADD  $2, R0, R9
	HASH8(R9, R12)
	ADDW $2, R8, R11
	MOVW R11, (R26)(R12<<2)
	ADD  $1, R0, R9
	HASH4(R9, R12)
	ADDW $1, R8, R11
	MOVW R11, (R4)(R12<<2)

	// LZ4HC_encodeSequence(matchLength, matchDistance)
	SUB  R2, R0, R11             // litLen
	MOVD $0x80808081, R17
	MUL  R17, R11, R9
	LSR  $39, R9, R9             // litLen / 255
	ADD  R11, R1, R12
	ADD  R9, R12, R12
	ADD  $9, R12, R12            // op+1 (token) + litLen + 2+1+LASTLITERALS
	CMP  R21, R12
	BHI  fail
	MOVD R1, R8                  // token
	ADD  $1, R1, R1
	CMP  $15, R11
	BHS  litRun
	LSLW $4, R11, R9
	MOVB R9, (R8)
	JMP  copyLits

litRun:
	MOVD $0xF0, R9
	MOVB R9, (R8)
	SUB  $15, R11, R9
	ADD  R9, R1, R12
	CMP  R22, R12
	BHS  fail2
	MOVD $255, R10

litRunLoop:
	CMP  $255, R9
	BLO  litRunLast
	MOVB R10, (R1)
	ADD  $1, R1, R1
	SUB  $255, R9, R9
	JMP  litRunLoop

litRunLast:
	MOVB R9, (R1)
	ADD  $1, R1, R1

copyLits:
	ADD  R11, R1, R10            // end of the literals in dst
	MOVD R2, R9
	ADD  $8, R10, R12
	CMP  R22, R12
	BHI  exactLits

copyLits8:
	MOVD.P 8(R9), R12
	MOVD.P R12, 8(R1)
	CMP    R10, R1
	BLO    copyLits8
	MOVD   R10, R1
	JMP    offsetOut

exactLits:
	CMP     R10, R1
	BHS     offsetOut
	MOVBU.P 1(R9), R12
	MOVB.P  R12, 1(R1)
	JMP     exactLits

offsetOut:
	// offset
	ADD  $2, R1, R9
	CMP  R22, R9
	BHI  fail2
	MOVH R25, (R1)
	ADD  $2, R1, R1

	// match length
	SUB  $4, R24, R11            // mlCode
	MUL  R17, R11, R9
	LSR  $39, R9, R9             // mlCode / 255
	ADD  R9, R1, R12
	ADD  $6, R12, R12            // op + mlCode/255 + 1+LASTLITERALS
	CMP  R21, R12
	BHI  failML
	ADD  R9, R1, R12
	ADD  $2, R12, R12
	CMP  R22, R12
	BHI  fail2
	CMP  $15, R11
	BHS  mlRun
	MOVBU (R8), R9
	ADD  R11, R9, R9
	MOVB R9, (R8)
	JMP  mlDone

mlRun:
	MOVBU (R8), R9
	ADD  $15, R9, R9
	MOVB R9, (R8)
	SUB  $15, R11, R11
	MOVD $255, R10

mlRunLoop:
	CMP  $255, R11
	BLO  mlRunLast
	MOVB R10, (R1)
	ADD  $1, R1, R1
	SUB  $255, R11, R11
	JMP  mlRunLoop

mlRunLast:
	MOVB R11, (R1)
	ADD  $1, R1, R1

mlDone:
	ADD  R24, R0, R0             // ip += matchLength
	MOVD R0, R2                  // anchor = ip

	// fill table with end of match
	SUB  R3, R0, R8
	ADDW R7, R8, R8              // endMatchIdx
	SUBW $2, R8, R9
	CMPW R23, R9
	BHS  loop                    // pos_m2 >= ilimitIdx
	SUB  R3, R0, R9
	CMP  $5, R9
	BLE  noM5
	SUB  $5, R0, R9
	HASH8(R9, R12)
	SUBW $5, R8, R11
	MOVW R11, (R26)(R12<<2)

noM5:
	SUB  $3, R0, R9
	HASH8(R9, R12)
	SUBW $3, R8, R11
	MOVW R11, (R26)(R12<<2)
	SUB  $2, R0, R9
	HASH8(R9, R12)
	SUBW $2, R8, R11
	MOVW R11, (R26)(R12<<2)
	SUB  $2, R0, R9
	HASH4(R9, R12)
	SUBW $2, R8, R11
	MOVW R11, (R4)(R12<<2)
	SUB  $1, R0, R9
	HASH4(R9, R12)
	SUBW $1, R8, R11
	MOVW R11, (R4)(R12<<2)
	JMP  loop

done:
	MOVD win_base+0(FP), R9
	SUB  R9, R0, R0
	MOVD R0, nip+112(FP)
	SUB  R9, R2, R2
	MOVD R2, anchor+120(FP)
	MOVD dst_base+56(FP), R9
	SUB  R9, R1, R1
	MOVD R1, op+128(FP)
	MOVD ZR, status+136(FP)
	RET

failML:
	MOVD R8, R1                  // op before the sequence

fail:
	MOVD win_base+0(FP), R9
	SUB  R9, R0, R0
	MOVD R0, nip+112(FP)
	SUB  R9, R2, R2
	MOVD R2, anchor+120(FP)
	MOVD dst_base+56(FP), R9
	SUB  R9, R1, R1
	MOVD R1, op+128(FP)
	MOVD R24, ml+144(FP)
	MOVD R25, md+152(FP)
	MOVD $1, R9
	MOVD R9, status+136(FP)
	RET

fail2:
	MOVD $2, R9
	MOVD R9, status+136(FP)
	RET
