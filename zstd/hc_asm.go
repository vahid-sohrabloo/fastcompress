//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the hash-chain lazy parser's assembly, shared by amd64 and
// arm64.

package zstd

// hcAsmSetup prepares ms.rowAsm for compressBlockLazyGeneric's hash-chain
// searches over a block ending at iend (a position in ms.window.base).
func hcAsmSetup(ms *matchState, iend int) {
	p := &ms.rowAsm
	p.ok = false
	b := ms.window.base
	cParams := &ms.cParams
	if !useHcAsm || len(ms.hashTable) < 1<<cParams.hashLog || len(ms.chainTable) < 1<<cParams.chainLog ||
		iend > len(b) || len(b) == 0 || ms.window.lowLimit < ms.window.baseIdx {
		return
	}
	*p = rowAsmParams{
		hashTable:    &ms.hashTable[0],
		b:            &b[0],
		nextToUpdate: &ms.nextToUpdate,
		hShift:       uint64(64 - cParams.hashLog),
		baseIdx:      ms.window.baseIdx,
		nbAttempts:   1 << cParams.searchLog,
		lowestValid:  ms.window.lowLimit,
		maxDistance:  1 << cParams.windowLog,
		ok:           true,
		bLen:         iend,
		iLimit:       iend,
		chainTable:   &ms.chainTable[0],
		chainMask:    1<<cParams.chainLog - 1,
	}
	if ms.loadedDictEnd != 0 {
		p.isDictionary = 1
	}
}

// lazyHcRun runs compressBlockLazyGeneric's match loop (hash-chain finder,
// noDict) in assembly from ip until ilimit, storing into ss, and returns the
// new ip, anchor and repcodes. It returns ok = false, doing nothing, when
// the assembly is off or a precondition fails: ms.rowAsm set up by
// hcAsmSetup for this block (tables hold every entry), mls in 4..6, 0 < ip,
// ilimit + 8 <= iend (every searched position has 8 bytes to hash), room in
// ss for every literal and a sequence per 4 input bytes (every match is
// >= 4), baseIdx <= ms.nextToUpdate <= ip's index (the inserts read
// positions from there), and the window's lowLimit <= ip's index (so
// lowLimit <= curr at every searched position, which the assembly's
// candidate bounds rely on).
func lazyHcRun(ms *matchState, ss *seqStore, ip, anchor, iend, ilimit, prefixLowest int,
	offset1, offset2 uint32, depth int, mls uint32) (int, int, uint32, uint32, bool) {
	p := &ms.rowAsm
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	if !p.ok || p.chainTable == nil || mls < 4 || mls > 6 || ip <= 0 || anchor > ip || ilimit+8 > iend ||
		ms.nextToUpdate > uint32(ip)+p.baseIdx || ms.nextToUpdate < p.baseIdx || p.lowestValid > uint32(ip)+p.baseIdx ||
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
	f := lazyHcFuncs[mls-4]
	ipOut, anchorOut, nSeq, nLit, r1, r2, llType, llPos := f(p, seqPtr, litPtr, ip, anchor, iend, ilimit, prefixLowest, offset1, offset2, depth)
	if nSeq < 0 {
		panic("zstd: hash chain holds an index at or after the current position")
	}
	ms.lazySkipping = p.skip != 0
	ms.asmCalls++
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	return ipOut, anchorOut, r1, r2, true
}

var lazyHcFuncs = [3]func(p *rowAsmParams, seqs *seqDef, lits *byte, ip, anchor, iend, ilimit, prefixLowest int,
	rep1, rep2 uint32, depth int) (ipOut, anchorOut, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32){
	lazyHc4, lazyHc5, lazyHc6,
}
