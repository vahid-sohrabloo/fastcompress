//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the row match finder's assembly, shared by amd64 and arm64.

package zstd

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
		hShift:       uint64(64 - (ms.rowHashLog + rowHashTagBits)),
		saltAdj:      ms.hashSalt << saltedHashSaltShift[m],
		baseIdx:      ms.window.baseIdx,
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
}

// lazyRowRun runs compressBlockLazyGeneric's match loop (row finder, noDict)
// in assembly from ip until ilimit, storing into ss, and returns the new ip,
// anchor and repcodes. It returns ok = false, doing nothing, when the
// assembly is off or a precondition fails: ms.rowAsm set up for this block
// (tables hold every row, searched positions + 16 within the input),
// mls and rowLog in 4..6, 0 < ip, ilimit + 16 <= iend, and room in ss for
// every literal and a sequence per 4 input bytes (every match is >= 4), and
// ms.nextToUpdate <= ip's index (the row update starts there).
func lazyRowRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int, mls, rowLog uint32) (int, int, uint32, uint32, bool) {
	p := &ms.rowAsm
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	if !p.ok || mls < 4 || mls > 6 || rowLog < 4 || rowLog > 6 || ip <= 0 || anchor > ip || ilimit+16 > iend ||
		ms.nextToUpdate > uint32(ip)+p.baseIdx ||
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
