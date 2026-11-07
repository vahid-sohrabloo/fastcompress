//go:build amd64 && !appengine && !noasm && !purego && gc

#include "textflag.h"

// Fields of optAsmParams (see zstd_opt_amd64.go; TestOptAsmParamsLayout).
#define OP_BT 0
#define OP_O 8
#define OP_OPT 16
#define OP_MATCHES 24
#define OP_B 32
#define OP_REP 40
#define OP_IP 48
#define OP_ANCHOR 56
#define OP_IEND 64
#define OP_ILIMIT 72
#define OP_SUFF 80
#define OP_MINML 84
#define OP_STORESTART 88
#define OP_STOREEND 92

// Fields of optState (TestOptAsmParamsLayout).
#define OS_LLFREQ 1024
#define OS_MLFREQ 1168
#define OS_OFFREQ 1380
#define OS_LITBASE 1544
#define OS_LLBASE 1548
#define OS_MLBASE 1552
#define OS_OFFBASE 1556

// Fields of optimal (32 bytes).
#define O_PRICE 0
#define O_OFF 8
#define O_MLEN 12
#define O_LITLEN 16
#define O_REP 20

#define MAXPRICE $0x40000000

// Frame of optParseAsm: 0-39 the finder's arguments and results
// (btGetAllNoDictAsm: p 0, ip 8, rep 16, ll0 24, lengthToBeat 28, mnum 32,
// bad 36), 40 p, 48 ip, 56 anchor, 64 cur, 72 lastPos, 80 nbMatches, 84
// minML, 88 ll0Price (LL_PRICE(0)), 96 matchLengthSumBasePrice + 51, 100
// sufficientLen, 104 b+ip, 112-143 lastStretch, 144 ilimit-ip, 152
// iend-ip, 160 the match table, 168 rep.
// p (40), b+ip (104), the match table (160) and rep (168) are pointers
// held in untyped locals across CALL ·btGetAllNoDictAsm. That is safe only
// because both functions are NOSPLIT assembly: the runtime never
// async-preempts assembly and a NOSPLIT frame never grows the stack, so
// no stack scan or copy happens while they are live. If the callee became
// a Go function or lost NOSPLIT, this frame would need a stack map
// (NO_LOCAL_POINTERS is not enough once the stack can move).
#define F_P 40(SP)
#define F_IP 48(SP)
#define F_ANCHOR 56(SP)
#define F_CUR 64(SP)
#define F_LASTPOS 72(SP)
#define F_NB 80(SP)
#define F_MINML 84(SP)
#define F_LL0PRICE 88(SP)
#define F_MLK 96(SP)
#define F_SUFF 100(SP)
#define F_BIP 104(SP)
#define F_LS 112
#define F_LIMCUR 144(SP)
#define F_ENDCUR 152(SP)
#define F_MATCHES 160(SP)
#define F_REP 168(SP)

// MLPRICE: CX = the match length part of ZSTD_getMatchPrice (dynamic
// statistics) for the match length in reg: mlBits[mlc]*256 +
// matchLengthSumBasePrice - WEIGHT(matchLengthFreq[mlc]) + 51, with mlc =
// ZSTD_MLcode(reg - MINMATCH). R8 = mlCodeBits, SI = the optState; uses AX.
#define MLPRICE(reg, big, join, done) \
	LEAL    -3(reg), AX; \
	CMPL    AX, $127; \
	JHI     big; \
	MOVWLZX (R8)(AX*2), CX; \
	MOVBLZX CX, AX; \
	SHRL    $8, CX; \
join: \
	MOVL    OS_MLFREQ(SI)(AX*4), AX; \
	INCL    AX; \
	BSRL    AX, AX; \
	SUBL    AX, CX; \
	SHLL    $8, CX; \
	ADDL    F_MLK, CX; \
	JMP     done; \
big: \
	BSRL    AX, CX; \
	LEAL    36(CX), AX; \
	JMP     join; \
done:

// OFFPRICE: reg = the offset part of ZSTD_getMatchPrice (dynamic
// statistics, optLevel 0) for the offBase in off; uses AX, CX.
#define OFFPRICE(off, reg, done) \
	BSRL off, CX; \
	MOVL OS_OFFREQ(SI)(CX*4), AX; \
	INCL AX; \
	BSRL AX, AX; \
	MOVL CX, reg; \
	SUBL AX, reg; \
	SHLL $8, reg; \
	ADDL OS_OFFBASE(SI), reg; \
	CMPL CX, $20; \
	JB   done; \
	LEAL -19(CX), AX; \
	SHLL $9, AX; \
	ADDL AX, reg; \
done:

// NEWREP: ZSTD_newRep of the repcodes at src+O_REP, for the offBase in BX,
// with ll0 = (the litlen at src+O_LITLEN == 0), written to dst[0..2]
// (dst may be src); uses AX, CX, DX, BX.
#define NEWREP(src, dst, full, noinc, rc1, rc3, same, store) \
	MOVL O_REP(src), AX; \
	MOVL O_REP+4(src), CX; \
	MOVL O_REP+8(src), DX; \
	CMPL BX, $3; \
	JHI  full; \
	CMPL O_LITLEN(src), $0; \
	JNE  noinc; \
	INCL BX; \
noinc: \
	DECL BX; \
	JEQ  same; \
	CMPL BX, $1; \
	JEQ  rc1; \
	CMPL BX, $3; \
	JEQ  rc3; \
	MOVL CX, 8(dst); \
	MOVL AX, 4(dst); \
	MOVL DX, 0(dst); \
	JMP  store; \
rc1: \
	MOVL DX, 8(dst); \
	MOVL AX, 4(dst); \
	MOVL CX, 0(dst); \
	JMP  store; \
rc3: \
	MOVL CX, 8(dst); \
	MOVL AX, 4(dst); \
	DECL AX; \
	MOVL AX, 0(dst); \
	JMP  store; \
full: \
	MOVL CX, 8(dst); \
	MOVL AX, 4(dst); \
	SUBL $3, BX; \
	MOVL BX, 0(dst); \
	JMP  store; \
same: \
	MOVL DX, 8(dst); \
	MOVL CX, 4(dst); \
	MOVL AX, 0(dst); \
	JMP  store

// func optParseAsm(p *optAsmParams) (status int)
//
// C: zstd_opt.c:ZSTD_compressBlock_opt_generic (optLevel 0, noDict, mls 4
// to 6, dynamic prices, compressed literals) from p.ip, up to the first
// segment to store: the forward pass (first matches, then each further
// position priced through a literal or the matches found there), then the
// backward traversal, which leaves the sequences in opt[storeStart ..
// storeEnd] and the new repcodes in *p.rep (status 1); C's "save sequences"
// loop runs in Go (optParseFast). Status 0: p.ip reached ilimit, with p.ip
// set. Status 2: the finder found the tree broken (impossible).
//
// Matches come from btGetAllNoDictAsm (skip check, tree update and
// search), called with its arguments at 0(SP). Bounds: optParseFast checks
// the finder's preconditions for every position searched (p.ip > 0,
// positions up to ilimit = iend - 8, baseIdx <= nextToUpdate, which only
// grows), and that every literal length is below blockSizeMax' price
// special case except LL_INCPRICE's, handled below. opt indices stay below
// optSize as in C: a first match longer than sufficientLen (< optNum) goes
// straight to the traversal, later ones end the pass when cur + length
// reaches optNum, so priced positions are < optNum and storeEnd = cur + 2
// < optSize.
TEXT ·optParseAsm(SB), NOSPLIT, $176-16
	MOVQ p+0(FP), AX
	MOVQ AX, F_P
	MOVQ OP_IP(AX), CX
	MOVQ CX, F_IP
	MOVQ OP_ANCHOR(AX), CX
	MOVQ CX, F_ANCHOR
	MOVL OP_MINML(AX), CX
	MOVL CX, F_MINML
	MOVL OP_SUFF(AX), CX
	MOVL CX, F_SUFF
	MOVQ OP_MATCHES(AX), CX
	MOVQ CX, F_MATCHES
	MOVQ OP_REP(AX), CX
	MOVQ CX, F_REP

	// the prices that stay fixed until the statistics change (after the
	// segment is stored): LL_PRICE(0) and the match length constant
	MOVQ OP_O(AX), SI
	MOVL OS_MLBASE(SI), CX
	ADDL $51, CX
	MOVL CX, F_MLK
	MOVL OS_LLFREQ(SI), AX
	INCL AX
	BSRL AX, AX
	SHLL $8, AX
	MOVL OS_LLBASE(SI), CX
	SUBL AX, CX
	MOVQ CX, F_LL0PRICE         // zero-extended, as int(uint32)

mloop:
	MOVQ F_P, AX
	MOVQ F_IP, R8
	CMPQ R8, OP_ILIMIT(AX)
	JGE  done

	// find first match
	MOVQ OP_BT(AX), DX
	MOVQ DX, 0(SP)
	MOVQ R8, 8(SP)
	MOVQ F_REP, DX
	MOVQ DX, 16(SP)
	MOVQ R8, CX
	SUBQ F_ANCHOR, CX           // litlen
	XORL DX, DX
	TESTQ CX, CX
	SETEQ DX
	MOVL DX, 24(SP)
	MOVL F_MINML, DX
	MOVL DX, 28(SP)
	CALL ·btGetAllNoDictAsm(SB)
	CMPB 36(SP), $0
	JNE  bad
	MOVL 32(SP), CX
	TESTL CX, CX
	JNE  first
	INCQ F_IP
	JMP  mloop

first:
	MOVL CX, F_NB
	MOVQ F_P, AX
	MOVQ OP_OPT(AX), DI
	MOVQ OP_O(AX), SI
	MOVQ F_IP, R11
	MOVQ OP_B(AX), DX
	ADDQ R11, DX
	MOVQ DX, F_BIP
	MOVQ OP_ILIMIT(AX), DX
	SUBQ R11, DX
	MOVQ DX, F_LIMCUR
	MOVQ OP_IEND(AX), DX
	SUBQ R11, DX
	MOVQ DX, F_ENDCUR
	SUBQ F_ANCHOR, R11          // litlen (< blockSizeMax - 8)

	// opt[0]: mlen 0, litlen, price LL_PRICE(litlen), rep
	MOVL $0, O_MLEN(DI)
	MOVL R11, O_LITLEN(DI)
	CMPL R11, $63
	JHI  llbig
	LEAQ ·llCodeBits(SB), AX
	MOVWLZX (AX)(R11*2), CX
	MOVBLZX CX, AX
	SHRL $8, CX

lljoin:
	MOVL OS_LLFREQ(SI)(AX*4), AX
	INCL AX
	BSRL AX, AX
	SUBL AX, CX
	SHLL $8, CX
	ADDL OS_LLBASE(SI), CX
	MOVQ CX, O_PRICE(DI)
	MOVQ F_REP, DX
	MOVQ (DX), AX
	MOVQ AX, O_REP(DI)
	MOVL 8(DX), AX
	MOVL AX, O_REP+8(DI)

	// large match -> immediate encoding
	MOVQ F_MATCHES, BX
	MOVL F_NB, CX
	MOVL -8(BX)(CX*8), DX       // maxOffBase
	MOVL -4(BX)(CX*8), R10      // maxML
	CMPL R10, F_SUFF
	JLS  firstprices
	// lastStretch = {litlen 0, maxML, maxOffBase}; its price and rep are
	// never read (the Go save loop reads litlen, mlen and off), zeroed so
	// that the opt[storeEnd] copy below holds no stale stack bytes
	PXOR   X0, X0
	MOVUPS X0, F_LS(SP)
	MOVUPS X0, F_LS+16(SP)
	MOVL DX, F_LS+O_OFF(SP)
	MOVL R10, F_LS+O_MLEN(SP)
	MOVQ $0, F_CUR
	MOVQ R10, F_LASTPOS
	JMP  shortest

llbig:
	BSRL R11, CX
	LEAL 19(CX), AX
	JMP  lljoin

firstprices:
	// positions 1 .. minML-1: price maxPrice, mlen 0, litlen litlen+pos
	MOVL $1, R9
	MOVL F_MINML, R12

fp1:
	CMPL R9, R12
	JAE  fp1done
	MOVL R9, AX
	SHLQ $5, AX
	ADDQ DI, AX
	MOVQ MAXPRICE, O_PRICE(AX)
	MOVL $0, O_MLEN(AX)
	LEAL (R11)(R9*1), CX
	MOVL CX, O_LITLEN(AX)
	INCL R9
	JMP  fp1

fp1done:
	LEAQ ·mlCodeBits(SB), R8
	MOVQ O_PRICE(DI), R15
	ADDQ F_LL0PRICE, R15        // opt[0].price + LL_PRICE(0)
	XORL BX, BX

fpm:
	MOVQ F_MATCHES, AX
	MOVL (AX)(BX*8), R13        // offBase
	MOVL 4(AX)(BX*8), R12       // end
	OFFPRICE(R13, R14, fpoff)

fpl:
	CMPL R9, R12
	JA   fpnext
	MLPRICE(R9, fpbig, fpjoin, fpdone)
	ADDL R14, CX
	ADDQ R15, CX
	MOVL R9, AX
	SHLQ $5, AX
	ADDQ DI, AX
	MOVQ CX, O_PRICE(AX)
	MOVL R13, O_OFF(AX)
	MOVL R9, O_MLEN(AX)
	MOVL $0, O_LITLEN(AX)
	INCL R9
	JMP  fpl

fpnext:
	INCL BX
	CMPL BX, F_NB
	JB   fpm
	LEAL -1(R9), R10            // lastPos
	SHLQ $5, R9
	MOVQ MAXPRICE, O_PRICE(DI)(R9*1)

	// check further positions
	MOVL $1, R9

cloop:
	CMPL R9, R10
	JA   cdone
	MOVL R9, R13
	SHLQ $5, R13
	ADDQ DI, R13                // &opt[cur]
	LEAQ -32(R13), R12          // &opt[cur-1]

	// Fix current position with one literal if cheaper
	MOVL    O_LITLEN(R12), R11
	INCL    R11                 // litlen
	MOVQ    F_BIP, AX
	MOVBLZX -1(AX)(R9*1), AX
	MOVL    (SI)(AX*4), AX      // litFreq
	INCL    AX
	BSRL    AX, AX
	SHLL    $8, AX
	MOVL    OS_LITBASE(SI), CX
	LEAL    -256(CX), DX        // litPriceMax
	CMPL    AX, DX
	CMOVLHI DX, AX
	SUBL    AX, CX              // LIT_PRICE
	ADDQ    O_PRICE(R12), CX

	// LL_INCPRICE(litlen)
	CMPL    R11, $63
	JHI     incbig
	LEAQ    ·llCodeBits(SB), DX
	MOVWLZX (DX)(R11*2), AX
	MOVWLZX -2(DX)(R11*2), DX
	CMPL    AX, DX
	JEQ     incdone

incdiff:
	// (llBits[c1] - highbit(freq[c1]+1)) - (llBits[c0] - highbit(freq[c0]+1)), * 256
	MOVQ    CX, R14
	MOVBLZX AX, CX
	SHRL    $8, AX
	MOVL    OS_LLFREQ(SI)(CX*4), CX
	INCL    CX
	BSRL    CX, CX
	SUBL    CX, AX
	MOVBLZX DX, CX
	SHRL    $8, DX
	MOVL    OS_LLFREQ(SI)(CX*4), CX
	INCL    CX
	BSRL    CX, CX
	SUBL    CX, DX
	SUBL    DX, AX
	SHLL    $8, AX
	MOVLQSX AX, AX
	LEAQ    (R14)(AX*1), CX

incdone:
	CMPQ   CX, O_PRICE(R13)
	JGT    nolit
	MOVUPS (R12), X0
	MOVUPS 16(R12), X1
	MOVUPS X0, (R13)
	MOVUPS X1, 16(R13)
	MOVL   R11, O_LITLEN(R13)
	MOVQ   CX, O_PRICE(R13)
	JMP    afterrep            // litlen != 0: no offset history change

nolit:
	// Offset history is not updated during match comparison. Do it here,
	// now that the match is selected and confirmed.
	CMPL O_LITLEN(R13), $0
	JNE  afterrep
	MOVL O_MLEN(R13), AX
	SHLQ $5, AX
	MOVQ R13, R12
	SUBQ AX, R12                // &opt[cur - mlen]
	MOVL O_OFF(R13), BX
	LEAQ O_REP(R13), R14
	NEWREP(R12, R14, nrfull, nrnoinc, nrrc1, nrrc3, nrsame, afterrep)

afterrep:
	// last match must start at a minimum distance of 8 from oend
	CMPQ R9, F_LIMCUR
	JGT  cnext
	CMPL R9, R10
	JEQ  cdone
	MOVQ O_PRICE(R13), AX
	ADDQ $128, AX
	CMPQ 32+O_PRICE(R13), AX
	JLE  cnext                  // skip unpromising positions

	MOVL R9, F_CUR
	MOVL R10, F_LASTPOS
	MOVQ F_P, AX
	MOVQ OP_BT(AX), DX
	MOVQ DX, 0(SP)
	MOVQ F_IP, DX
	ADDQ R9, DX
	MOVQ DX, 8(SP)
	LEAQ O_REP(R13), DX
	MOVQ DX, 16(SP)
	XORL DX, DX
	CMPL O_LITLEN(R13), $0
	SETEQ DX
	MOVL DX, 24(SP)
	MOVL F_MINML, DX
	MOVL DX, 28(SP)
	CALL ·btGetAllNoDictAsm(SB)
	CMPB 36(SP), $0
	JNE  bad
	MOVQ F_P, AX
	MOVQ OP_OPT(AX), DI
	MOVQ OP_O(AX), SI
	LEAQ ·mlCodeBits(SB), R8
	MOVL F_CUR, R9
	MOVL F_LASTPOS, R10
	MOVL 32(SP), CX
	TESTL CX, CX
	JEQ  cnext
	MOVL CX, F_NB
	MOVL R9, R13
	SHLQ $5, R13
	MOVQ O_PRICE(DI)(R13*1), R15
	ADDQ F_LL0PRICE, R15        // basePrice

	MOVQ F_MATCHES, BX
	MOVL -4(BX)(CX*8), DX       // longestML
	CMPL DX, F_SUFF
	JHI  cstop
	LEAL (R9)(DX*1), AX
	CMPL AX, $4096              // optNum
	JAE  cstop
	CMPQ AX, F_ENDCUR
	JGE  cstop

	// set prices using matches found at position == cur
	XORL BX, BX

cm:
	MOVQ F_MATCHES, AX
	MOVL (AX)(BX*8), R13        // offBase
	MOVL 4(AX)(BX*8), R11       // lastML
	MOVL F_MINML, R12
	TESTL BX, BX
	JEQ  cmstart
	MOVL -4(AX)(BX*8), R12
	INCL R12                    // startML

cmstart:
	OFFPRICE(R13, R14, cmoff)

cml:
	CMPL R11, R12
	JB   cmnext
	LEAL (R9)(R11*1), DX        // pos
	MLPRICE(R11, cmbig, cmjoin, cmdone)
	ADDL R14, CX
	ADDQ R15, CX                // price
	MOVL DX, AX
	SHLQ $5, AX
	CMPL DX, R10
	JA   cmfill
	CMPQ CX, O_PRICE(DI)(AX*1)
	JGE  cmnext                 // early update abort

cmstore:
	ADDQ DI, AX
	MOVQ CX, O_PRICE(AX)
	MOVL R13, O_OFF(AX)
	MOVL R11, O_MLEN(AX)
	MOVL $0, O_LITLEN(AX)
	DECL R11
	JMP  cml

cmfill:
	// fill empty positions, for future comparisons
	INCL R10
	MOVL R10, AX
	SHLQ $5, AX
	MOVQ MAXPRICE, O_PRICE(DI)(AX*1)
	MOVL $1, O_LITLEN(DI)(AX*1)
	CMPL R10, DX
	JB   cmfill
	JMP  cmstore

cmnext:
	INCL BX
	CMPL BX, F_NB
	JB   cm
	LEAL 1(R10), AX
	SHLQ $5, AX
	MOVQ MAXPRICE, O_PRICE(DI)(AX*1)

cnext:
	INCL R9
	JMP  cloop

incbig:
	// litlen > 63: codes highbit + 19 with highbit extra bits, except
	// llCode(63) for litlen 64; blockSizeMax costs one bit more than
	// blockSizeMax - 1 (C: ZSTD_litLengthPrice)
	CMPL R11, $131072
	JEQ  inc256
	BSRL R11, AX
	LEAL -1(R11), DX
	CMPL DX, $63
	JHI  incbig2
	LEAQ ·llCodeBits(SB), DX
	MOVWLZX 126(DX), DX
	JMP  incmk1

incbig2:
	BSRL DX, DX
	CMPL AX, DX
	JEQ  incdone
	MOVL DX, R14
	SHLL $8, R14
	LEAL 19(DX)(R14*1), DX

incmk1:
	MOVL AX, R14
	SHLL $8, R14
	LEAL 19(AX)(R14*1), AX
	JMP  incdiff

inc256:
	ADDQ $256, CX
	JMP  incdone

cstop:
	// longest match ends the forward pass: lastStretch = {litlen 0, its
	// mlen and offBase}, lastPos = cur + longestML
	// (price and rep zeroed as for the immediate encoding)
	PXOR   X0, X0
	MOVUPS X0, F_LS(SP)
	MOVUPS X0, F_LS+16(SP)
	MOVL F_NB, CX
	MOVL -8(BX)(CX*8), AX
	MOVL AX, F_LS+O_OFF(SP)
	MOVL DX, F_LS+O_MLEN(SP)
	ADDL R9, DX
	MOVL R9, F_CUR
	MOVL DX, F_LASTPOS
	JMP  shortest

cdone:
	// lastStretch = opt[lastPos]; cur = lastPos - lastStretch.mlen
	MOVL   R10, F_LASTPOS
	MOVL   R10, AX
	SHLQ   $5, AX
	MOVUPS (DI)(AX*1), X0
	MOVUPS 16(DI)(AX*1), X1
	MOVUPS X0, F_LS(SP)
	MOVUPS X1, F_LS+16(SP)
	MOVL   R10, AX
	SUBL   F_LS+O_MLEN(SP), AX
	MOVL   AX, F_CUR

shortest:
	// cur, lastPos, lastStretch have to be set
	MOVQ F_P, AX
	MOVQ OP_OPT(AX), DI
	MOVL F_CUR, R9
	CMPL F_LS+O_MLEN(SP), $0
	JNE  update
	// no solution: all matches have been converted into literals
	MOVL F_LASTPOS, AX
	ADDQ AX, F_IP
	JMP  mloop

update:
	// Update offset history
	MOVL F_LS+O_LITLEN(SP), CX
	TESTL CX, CX
	JNE  lsrep
	MOVL R9, R12
	SHLQ $5, R12
	ADDQ DI, R12                // &opt[cur]
	MOVL F_LS+O_OFF(SP), BX
	MOVQ F_REP, R14
	NEWREP(R12, R14, srfull, srnoinc, srrc1, srrc3, srsame, reverse)

lsrep:
	MOVQ F_REP, DX
	MOVQ F_LS+O_REP(SP), AX
	MOVQ AX, (DX)
	MOVL F_LS+O_REP+8(SP), AX
	MOVL AX, 8(DX)
	SUBL CX, R9                 // cur -= lastStretch.litlen

reverse:
	// Let's write the shortest path solution, in reverse order from
	// storeEnd = cur + 2 (opt now stores sequences: literals then a match).
	LEAL   2(R9), R10           // storeEnd
	MOVL   R10, AX
	SHLQ   $5, AX
	ADDQ   DI, AX               // &opt[storeEnd]
	MOVUPS F_LS(SP), X0
	MOVUPS F_LS+16(SP), X1
	CMPL   F_LS+O_LITLEN(SP), $0
	JEQ    rlast
	// last "sequence" is unfinished: just a bunch of literals (C stores it,
	// then the block after overwrites opt[storeEnd])
	MOVUPS X0, -32(AX)
	MOVUPS X1, -16(AX)

rlast:
	MOVUPS X0, (AX)
	MOVUPS X1, 16(AX)
	MOVL   R10, R11             // storeStart

rloop:
	MOVL   R9, CX
	SHLQ   $5, CX
	MOVUPS (DI)(CX*1), X0       // nextStretch = opt[stretchPos]
	MOVUPS 16(DI)(CX*1), X1
	MOVL   O_LITLEN(DI)(CX*1), DX
	MOVL   O_MLEN(DI)(CX*1), BX
	MOVL   R11, AX
	SHLQ   $5, AX
	MOVL   DX, O_LITLEN(DI)(AX*1)
	TESTL  BX, BX
	JEQ    rdone                // reaching beginning of segment
	DECL   R11
	MOVUPS X0, -32(DI)(AX*1)
	MOVUPS X1, -16(DI)(AX*1)
	SUBL   DX, R9
	SUBL   BX, R9               // stretchPos -= litlen + mlen
	JMP    rloop

rdone:
	MOVQ F_P, AX
	MOVL R11, OP_STORESTART(AX)
	MOVL R10, OP_STOREEND(AX)
	MOVQ F_IP, CX
	MOVQ CX, OP_IP(AX)
	MOVQ $1, status+8(FP)
	RET

done:
	MOVQ R8, OP_IP(AX)
	MOVQ $0, status+8(FP)
	RET

bad:
	MOVQ $2, status+8(FP)
	RET
