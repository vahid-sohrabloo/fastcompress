//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useRowAsm selects the assembly versions of the row match finder's inner
// loops (SSE2 and PREFETCHT0, which every amd64 CPU has).
var useRowAsm bool

func init() {
	cpu.RegisterAsm("row", &useRowAsm, func() bool { return true })
}

// rowUpdateCachedAsm is rowUpdateCachedGeneric (see it for the meaning) with
// the hash parameters precomputed: hash(p) = ((le64(b[p:]) << shift) *
// prime ^ saltAdj) >> hShift, as in hashPtrSalted. It also prefetches the
// rows of the new hashes (C: ZSTD_row_prefetch).
//
// Preconditions (checked by rowUpdateCached): idx < end; b holds the 8
// bytes at end-1-baseIdx+rowHashCacheSize; hashTable and tagTable hold
// every row (1<<(hBits-rowHashTagBits+rowLog) entries); rowMask is
// 1<<rowLog - 1 with rowLog in 4..6.
//
//go:noescape
func rowUpdateCachedAsm(hashTable *uint32, tagTable *byte, cache *[rowHashCacheSize]uint32, b *byte,
	idx, end, baseIdx, rowLog, rowMask uint32, shift, hShift, prime, saltAdj uint64)

// rowSearchAsm is rowSearchGeneric (see it for the meaning), computing the
// match mask with SSE2 like C's ZSTD_row_getSSEMask and prefetching each
// candidate (C: PREFETCH_L1(base + matchIndex)).
//
// Preconditions (checked by rowSearch): the row at relRow (1<<rowLog
// entries, rowLog in 4..6) lies within hashTable and tagTable; 0 < ip <
// iLimit <= len(b); lowLimit >= baseIdx. A candidate index at or after
// ip's makes it return ml = -1 instead of reading past ip (it cannot happen:
// the tables only hold earlier positions).
//
//go:noescape
func rowSearchAsm(hashTable *uint32, tagTable *byte, b *byte, ip, iLimit int,
	baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, nextToUpdate uint32) (ml int, best uint32)

func rowUpdateCached(ms *matchState, idx, end, mls, rowLog uint32) {
	if !useRowAsm || idx >= end {
		rowUpdateCachedGeneric(ms, idx, end, mls, rowLog)
		return
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	hBits := ms.rowHashLog + rowHashTagBits
	rows := 1 << (ms.rowHashLog + rowLog)
	if rowLog < 4 || rowLog > 6 || len(ms.hashTable) < rows || len(ms.tagTable) < rows ||
		int(end-1-baseIdx)+rowHashCacheSize+8 > len(b) {
		panic("zstd: rowUpdateCached preconditions")
	}
	m := mls & 15
	rowUpdateCachedAsm(&ms.hashTable[0], &ms.tagTable[0], &ms.hashCache, &b[0],
		idx, end, baseIdx, rowLog, 1<<rowLog-1,
		uint64(saltedHashShift[m]), uint64(64-hBits), saltedHashPrime[m], ms.hashSalt<<saltedHashSaltShift[m])
}

func rowSearch(ms *matchState, ip, iLimit int, relRow, tag, rowLog, nbAttempts, lowLimit uint32) (int, uint32) {
	if !useRowAsm {
		return rowSearchGeneric(ms, ip, iLimit, relRow, tag, rowLog, nbAttempts, lowLimit)
	}
	b := ms.window.base
	end := int(relRow) + 1<<rowLog
	if rowLog < 4 || rowLog > 6 || end > len(ms.hashTable) || end > len(ms.tagTable) ||
		ip <= 0 || ip >= iLimit || iLimit > len(b) {
		panic("zstd: rowSearch preconditions")
	}
	baseIdx := ms.window.baseIdx
	if lowLimit < baseIdx {
		panic("zstd: rowSearch lowLimit below the window base")
	}
	ml, best := rowSearchAsm(&ms.hashTable[0], &ms.tagTable[0], &b[0], ip, iLimit,
		baseIdx, relRow, tag, rowLog, nbAttempts, lowLimit, ms.nextToUpdate)
	if ml < 0 {
		panic("zstd: row table holds an index at or after the current position")
	}
	return ml, best
}

// rowFindBestMatchAsm is rowFindBestMatch (noDict) for the block set up in
// p: the table update with the hash cache (skipping long matches' middles),
// the search and the insertion of ip, prefetching as C does. It returns
// the match length (below 4: none) and offBase (offBaseNotFound if none),
// or ml = -1 if a row entry pointed at or after ip (impossible).
//
// Preconditions (checked by rowAsmSetup and rowFindBestMatchAsmCall): the
// tables hold every row; 0 < ip < iLimit <= len(b) and ip+16 <= len(b)
// (the hash cache reads 8 bytes at ip+8); lowestValid >= baseIdx;
// *nextToUpdate <= ip+baseIdx.
//
//go:noescape
func rowFindBestMatchAsm(p *rowAsmParams, ip, iLimit int, lazySkipping bool) (ml int, offBase uint32)

// rowUpdateAsmHelper is rowFindBestMatchAsm's register-convention update
// loop (never called from Go).
func rowUpdateAsmHelper()

// rowAsmSetup prepares ms.rowAsm for compressBlockLazyGeneric's row
// searches over a block ending at iend (a position in ms.window.base).
func rowAsmSetup(ms *matchState, mls, rowLog uint32, iend int) {
	p := &ms.rowAsm
	p.ok = false
	b := ms.window.base
	rows := 1 << (ms.rowHashLog + rowLog)
	if !useRowAsm || rowLog < 4 || rowLog > 6 || len(ms.hashTable) < rows || len(ms.tagTable) < rows ||
		iend > len(b) || len(b) == 0 || ms.window.lowLimit < ms.window.baseIdx {
		return
	}
	m := mls & 15
	cParams := &ms.cParams
	*p = rowAsmParams{
		hashTable:    &ms.hashTable[0],
		tagTable:     &ms.tagTable[0],
		b:            &b[0],
		cache:        &ms.hashCache,
		nextToUpdate: &ms.nextToUpdate,
		entropy:      &ms.hashSaltEntropy,
		shift:        uint64(saltedHashShift[m]),
		hShift:       uint64(64 - (ms.rowHashLog + rowHashTagBits)),
		prime:        saltedHashPrime[m],
		saltAdj:      ms.hashSalt << saltedHashSaltShift[m],
		baseIdx:      ms.window.baseIdx,
		rowLog:       rowLog,
		rowMask:      1<<rowLog - 1,
		nbAttempts:   1 << min(cParams.searchLog, rowLog),
		lowestValid:  ms.window.lowLimit,
		maxDistance:  1 << cParams.windowLog,
		ok:           true,
		bLen:         iend,
		iLimit:       iend,
	}
	if ms.loadedDictEnd != 0 {
		p.isDictionary = 1
	}
	if rowLog >= 5 {
		p.pfHash2 = 64
	}
	if rowLog == 6 {
		p.pfTag2 = 32
	}
}

func rowFindBestMatchAsmCall(ms *matchState, ip, iLimit int) (int, uint32) {
	p := &ms.rowAsm
	if ip <= 0 || ip >= iLimit || iLimit > p.bLen || ip+16 > p.bLen || ms.nextToUpdate > uint32(ip)+p.baseIdx {
		return rowFindBestMatch(ms, ip, iLimit, min(max(ms.cParams.minMatch, 4), 6), p.rowLog)
	}
	ml, offBase := rowFindBestMatchAsm(p, ip, iLimit, ms.lazySkipping)
	if ml < 0 {
		panic("zstd: row table holds an index at or after the current position")
	}
	return ml, offBase
}

// The lazy parser and row match finder specialised per (mls, rowLog), in
// row_lazy_amd64.s (generated by gen_row_amd64.go). lazyRowMR runs
// compressBlockLazyGeneric's match loop for the block set up in p (see
// lazyRowRun for the preconditions); rowSearchMR and rowUpdMR are its
// register-convention helpers (never called from Go).
//
//go:generate go run gen_row_amd64.go

//go:noescape
func lazyRow44(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch44()
func rowUpd44()

//go:noescape
func lazyRow45(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch45()
func rowUpd45()

//go:noescape
func lazyRow46(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch46()
func rowUpd46()

//go:noescape
func lazyRow54(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch54()
func rowUpd54()

//go:noescape
func lazyRow55(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch55()
func rowUpd55()

//go:noescape
func lazyRow56(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch56()
func rowUpd56()

//go:noescape
func lazyRow64(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch64()
func rowUpd64()

//go:noescape
func lazyRow65(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch65()
func rowUpd65()

//go:noescape
func lazyRow66(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)
func rowSearch66()
func rowUpd66()

// lazyRowRun runs compressBlockLazyGeneric's match loop (row finder, noDict)
// in assembly from ip until ilimit, storing into ss, and returns the new ip,
// anchor and repcodes. It returns ok = false, doing nothing, when the
// assembly is off or a precondition fails: ms.rowAsm set up for this block
// (tables hold every row, searched positions + 16 within the input),
// mls and rowLog in 4..6, 0 < ip, ilimit + 16 <= iend, and room in ss for
// every literal and a sequence per 4 input bytes (every match is >= 4).
func lazyRowRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int, mls, rowLog uint32) (int, int, uint32, uint32, bool) {
	p := &ms.rowAsm
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	if !p.ok || mls < 4 || mls > 6 || rowLog < 4 || rowLog > 6 || ip <= 0 || anchor > ip || ilimit+16 > iend ||
		iend > p.bLen || prefixLowest < 0 || depth < 0 || depth > 2 ||
		cap(ss.literals)-nLits < iend-anchor || cap(ss.sequences)-nSeqs < (iend-anchor)/4 {
		return ip, anchor, offset1, offset2, false
	}
	seqs := ss.sequences[nSeqs:cap(ss.sequences)]
	lits := ss.literals[nLits:cap(ss.literals)]
	var seqPtr *seqDef
	if len(seqs) > 0 {
		seqPtr = &seqs[0]
	}
	var litPtr *byte
	if len(lits) > 0 {
		litPtr = &lits[0]
	}
	p.skip = 0
	if ms.lazySkipping {
		p.skip = 1
	}
	f := lazyRowFuncs[(mls-4)*3+rowLog-4]
	ipOut, anchorOut, nSeq, nLit, r1, r2, llType, llPos := f(p, seqPtr, litPtr, ip, anchor, iend, ilimit, prefixLowest, offset1, offset2, depth)
	if nSeq < 0 {
		panic("zstd: row table holds an index at or after the current position")
	}
	ms.lazySkipping = p.skip != 0
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	return ipOut, anchorOut, r1, r2, true
}

var lazyRowFuncs = [9]func(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32){
	lazyRow44, lazyRow45, lazyRow46, lazyRow54, lazyRow55, lazyRow56, lazyRow64, lazyRow65, lazyRow66,
}
