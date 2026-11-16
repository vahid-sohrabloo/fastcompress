package lz4

import (
	"math/bits"
	"sync"
	"unsafe"
)

// The high compression levels of lz4hc.c (lz4 v1.11.1, Copyright (c) Yann
// Collet, BSD 2-Clause): LZ4MID (level 2), the hash chain parser (levels 3
// to 9) and the optimal parser (levels 10 to 12).
//
// C's context holds pointers (prefixStart, end, dictStart); here the prefix
// is a slice from prefixStart of length end-prefixStart, and the external
// dictionary a slice from dictStart of length dictLimit-lowLimit. During a
// compression the prefix includes the input (C: ctx->end += srcSize), and
// positions are indexes into that window ("win").

const (
	// Compression levels of LZ4HC (C: LZ4HC_CLEVEL_*).
	LevelHCMin     = 2
	LevelHCDefault = 9
	LevelHCOptMin  = 10
	LevelHCMax     = 12

	hcDictionaryLogSize = 16
	hcMaxD              = 1 << hcDictionaryLogSize // C: LZ4HC_MAXD
	hcHashLog           = 15
	hcHashTableSize     = 1 << hcHashLog
	hcHashSize          = 4 // C: LZ4HC_HASHSIZE

	optimalML = mlMask - 1 + minMatch // C: OPTIMAL_ML
	optNum    = 1 << 12               // C: LZ4_OPT_NUM

	midHashSize      = 8
	midHashLog       = hcHashLog - 1
	midHashTableSize = 1 << midHashLog
)

type hcStrat int

const (
	lz4mid hcStrat = iota
	lz4hc
	lz4opt
)

type hcParams struct {
	strat         hcStrat
	nbSearches    int
	targetLength  int
	nbSearchesSeq int
	dictCopyMin   int
}

var clTable = [LevelHCMax + 1]hcParams{
	{lz4mid, 2, 16, 0, 4 * kb},
	{lz4mid, 2, 16, 0, 4 * kb},
	{lz4mid, 2, 16, 0, 4 * kb},
	{lz4hc, 4, 16, 0, 4 * kb},
	{lz4hc, 8, 16, 0, 4 * kb},
	{lz4hc, 16, 16, 0, 4 * kb},
	{lz4hc, 32, 16, 0, 4 * kb},
	{lz4hc, 64, 16, 0, 4 * kb},
	{lz4hc, 128, 16, 0, 4 * kb},
	{lz4hc, 256, 16, 0, 4 * kb},
	{lz4opt, 96, 64, 1, 4 * kb},
	{lz4opt, 512, 128, 2, 2 * kb},
	{lz4opt, 16384, optNum, 64, 1 * kb},
}

// C: LZ4HC_getCLevelParams
func hcGetCLevelParams(level int) hcParams {
	if level < 1 {
		level = LevelHCDefault
	}
	return clTable[min(LevelHCMax, level)]
}

// hcChainArgs is the state shared with hcChainAsm (offsets in
// lz4hc_amd64.s).
//
// The pointers are uintptrs, so filling the struct (in the context, on the
// heap) needs no write barrier; the memory stays reachable through the
// context and win.
type hcChainArgs struct {
	win        uintptr
	chain      uintptr
	ip         int
	iLowLimit  int
	iHighLimit int
	prefixIdx  uint64
	lowest     uint64
	ipIndex    uint64
	pattern    uint64
	maxLength  int
	pa         int
	matchIndex uint64
	nbAttempts int
	longest    int
	offset     int
	sBack      int
	hashTable  uintptr
	next       uint64 // nextToUpdate (in and out)
	insert     int    // 1: run LZ4HC_Insert and the first lookup first
	chainSwap  int
	favor      int
	chainPos   uint64 // matchChainPos (in and out)
}

// hcParseArgs is the state of the assembly parsers hcHashChainAsm and
// hcOptimalAsm (offsets in lz4hc_parse_amd64.s); like hcChainArgs it holds
// uintptrs to memory kept reachable by the caller.
type hcParseArgs struct {
	win       uintptr
	hashTable uintptr
	chain     uintptr
	prefixIdx uint64
	ip        int // in and out (win indexes)
	anchor    int
	op        int // in and out (dst index)
	iend      int
	dst       uintptr
	dstLen    int
	olimit    int    // maxOutputSize, or -1 for notLimited
	next      uint64 // nextToUpdate (in and out)
	attempts  int
	pa        int
	stop      int // return (2) at the top of the main loop once ip > stop
	suff      int // optimal parser: sufficientLen
	opt       uintptr
	dict      uintptr // dictStart (pa&paExt)
	lowLimit  uint64
	mlen      int // out (status 1): the match of the sequence that did not fit
	moff      int
	nbSeq     int     // optimal parser with favorDecSpeed: nbSearchesSeq
	dhash     uintptr // the attached dictionary (pa&paDictCtx): its tables,
	dchain    uintptr
	dbase     uintptr // prefixStart - dictLimit,
	dend      uint64  // end index
	dstart    uintptr // and prefixStart
}

// hcParseArgs.pa bits
const (
	paPattern = 1 // the pattern analysis (hash chain parser)
	paExt     = 2 // an external dictionary
	paFavor   = 4 // favorDecSpeed (optimal parser)
	paDictCtx = 8 // an attached dictionary
)

// hcAsmChunk is the input the assembly parsers cover per call, so a block
// does not hold off the scheduler for its whole length.
const hcAsmChunk = 256 * kb

// parseAsmOK reports whether the assembly parsers can compress
// win[srcStart:] (the end of win) into dst: a prefix with or without an
// external dictionary (no attached one), dst holding the bound
// (notLimited) or maxOutputSize.
func (c *hcCtx) parseAsmOK(win []byte, srcStart, inputSize int, dst []byte, maxOutputSize int, limit outputDirective, usingDictCtx bool) bool {
	return useHCParseAsm && (hcParseFull || c.lowLimit == c.dictLimit && !usingDictCtx) && (!usingDictCtx || c.lowLimit == c.dictLimit && len(c.dictCtx.prefix) > 0) && inputSize >= minLength && c.lowLimit <= c.dictLimit &&
		len(c.dict) >= int(c.dictLimit-c.lowLimit) &&
		srcStart+inputSize == len(win) && c.nextToUpdate >= c.dictLimit && len(dst) >= maxOutputSize && maxOutputSize >= 1 &&
		(limit != notLimited || len(dst) >= CompressBound(inputSize)) && (limit != fillOutput || maxOutputSize > lastLiterals)
}

// newParseArgs fills the arguments of an assembly parser from the
// context; the caller sets the parser's own fields (and ORs its bits into
// pa). oend is the encoder's limit (maxOutputSize, less LASTLITERALS for
// fillOutput).
func (c *hcCtx) newParseArgs(win []byte, srcStart int, dst []byte, oend int, limit outputDirective, usingDictCtx bool) hcParseArgs {
	olimit := oend
	if limit == notLimited {
		olimit = -1
	}
	a := hcParseArgs{win: uintptr(unsafe.Pointer(&win[0])), hashTable: uintptr(unsafe.Pointer(&c.hashTable[0])),
		chain: uintptr(unsafe.Pointer(&c.chainTable[0])), prefixIdx: uint64(c.dictLimit),
		ip: srcStart, anchor: srcStart, iend: len(win), dst: uintptr(unsafe.Pointer(&dst[0])), dstLen: len(dst),
		olimit: olimit, next: uint64(c.nextToUpdate), lowLimit: uint64(c.lowLimit)}
	if c.lowLimit < c.dictLimit {
		a.pa = paExt
		a.dict = uintptr(unsafe.Pointer(&c.dict[0]))
	}
	if usingDictCtx {
		d := c.dictCtx
		a.pa = paDictCtx
		a.dhash, a.dchain = uintptr(unsafe.Pointer(&d.hashTable[0])), uintptr(unsafe.Pointer(&d.chainTable[0]))
		a.dstart = uintptr(unsafe.Pointer(&d.prefix[0]))
		a.dbase = a.dstart - uintptr(d.dictLimit)
		a.dend = uint64(len(d.prefix)) + uint64(d.dictLimit)
	}
	return a
}

type hcMatch struct {
	off, len, back int
}

// hcCtx is C's LZ4HC_CCtx_internal.
type hcCtx struct {
	hashTable        [hcHashTableSize]uint32
	chainTable       [hcMaxD]uint16
	prefix           []byte // prefixStart; len is end - prefixStart
	prefixSet        bool   // C: prefixStart != NULL
	dict             []byte // dictStart
	dictLimit        uint32
	lowLimit         uint32
	nextToUpdate     uint32
	compressionLevel int16
	favorDecSpeed    bool
	dictCtx          *hcCtx
	opt              []hcOptimal // workspace of levels 10 to 12
	args             hcChainArgs // hcChainAsm's state
}

func hcHashPtr(b []byte, p int) uint32 { return (le32(b[p:]) * 2654435761) >> (minMatch*8 - hcHashLog) }
func midHash4Ptr(b []byte, p int) uint32 {
	return (le32(b[p:]) * 2654435761) >> (32 - midHashLog)
}
func midHash8Ptr(b []byte, p int) uint32 {
	return uint32(((le64(b[p:]) << (64 - 56)) * 58295818150454627) >> (64 - midHashLog))
}

// C: LZ4HC_countBack, with ip in a and match in m
func hcCountBack(a []byte, ip int, m []byte, match int, iMin, mMin int) int {
	back := 0
	mn := max(iMin-ip, mMin-match)
	for back-mn > 3 {
		v := le32(a[ip+back-4:]) ^ le32(m[match+back-4:])
		if v != 0 {
			return back - bits.LeadingZeros32(v)>>3
		}
		back -= 4
	}
	for back > mn && a[ip+back-1] == m[match+back-1] {
		back--
	}
	return back
}

// C: LZ4HC_clearTables
func (c *hcCtx) clearTables() {
	c.hashTable = [hcHashTableSize]uint32{}
	for i := range c.chainTable {
		c.chainTable[i] = 0xFFFF
	}
}

// C: LZ4HC_init_internal
func (c *hcCtx) initInternal(start []byte) {
	bufferSize := uint64(len(c.prefix))
	newStartingOffset := bufferSize + uint64(c.dictLimit)
	if newStartingOffset > 1*gb {
		c.clearTables()
		newStartingOffset = 0
	}
	newStartingOffset += 64 * kb
	c.nextToUpdate = uint32(newStartingOffset)
	c.prefix = start[:0:cap(start)]
	c.prefixSet = true
	c.dict = start[:0:cap(start)]
	c.dictLimit = uint32(newStartingOffset)
	c.lowLimit = uint32(newStartingOffset)
}

// hcEnc carries C's UPDATABLE(ip, op, anchor).
type hcEnc struct {
	win    []byte
	dst    []byte
	ip, op int
	anchor int
}

// C: LZ4HC_encodeSequence; true when the output does not fit (ip, op and
// anchor are then unchanged).
func (e *hcEnc) encodeSequence(matchLength, offset int, limit outputDirective, oend int) bool {
	token := e.op
	op := e.op + 1
	litLen := e.ip - e.anchor
	if limit != notLimited && op+litLen/255+litLen+(2+1+lastLiterals) > oend {
		return true
	}
	dst := e.dst
	if litLen >= runMask {
		dst[token] = runMask << mlBits
		op = putRun(dst, op, uint(litLen-runMask))
	} else {
		dst[token] = byte(litLen << mlBits)
	}
	copy(dst[op:op+litLen], e.win[e.anchor:e.ip])
	op += litLen

	dst[op] = byte(offset)
	dst[op+1] = byte(offset >> 8)
	op += 2

	mlCode := matchLength - minMatch
	if limit != notLimited && op+mlCode/255+(1+lastLiterals) > oend {
		return true
	}
	if mlCode >= mlMask {
		dst[token] += mlMask
		op = putRun(dst, op, uint(mlCode-mlMask))
	} else {
		dst[token] += byte(mlCode)
	}
	e.op = op
	e.ip += matchLength
	e.anchor = e.ip
	return false
}

// C: LZ4HC_encodeLastLiterals; true when dst is too small (limitedOutput)
func (e *hcEnc) encodeLastLiterals(iend int, limit outputDirective, oend int) bool {
	dstEnd := oend
	if limit == fillOutput {
		dstEnd = oend + lastLiterals
	}
	lastRunSize := iend - e.anchor
	llAdd := (lastRunSize + 255 - runMask) / 255
	totalSize := 1 + llAdd + lastRunSize
	if limit != notLimited && e.op+totalSize > dstEnd {
		if limit == limitedOutput {
			return true
		}
		lastRunSize = dstEnd - e.op - 1
		llAdd = (lastRunSize + 256 - runMask) / 256
		lastRunSize -= llAdd
	}
	e.ip = e.anchor + lastRunSize
	op := e.op
	if lastRunSize >= runMask {
		e.dst[op] = runMask << mlBits
		op = putRun(e.dst, op+1, uint(lastRunSize-runMask))
	} else {
		e.dst[op] = byte(lastRunSize << mlBits)
		op++
	}
	copy(e.dst[op:op+lastRunSize], e.win[e.anchor:e.anchor+lastRunSize])
	e.op = op + lastRunSize
	return false
}

// C: LZ4HC_encodeLastSequence_fillOutput
func (e *hcEnc) encodeLastSequenceFillOutput(matchLength, offset, oend int) {
	dstEnd := oend + lastLiterals
	ll := e.ip - e.anchor
	llAddbytes := (ll + 240) / 255
	llTotalCost := 1 + llAddbytes + ll
	maxLitPos := oend - 3
	if e.op+llTotalCost <= maxLitPos {
		bytesLeftForMl := maxLitPos - (e.op + llTotalCost)
		maxMlSize := minMatch + (mlMask - 1) + bytesLeftForMl*255
		if matchLength > maxMlSize {
			matchLength = maxMlSize
		}
		if dstEnd-(e.op+llTotalCost+2)-1+matchLength >= mfLimit {
			e.encodeSequence(matchLength, offset, notLimited, oend)
		}
	}
}

// C: LZ4HC_searchExtDict (an attached dictionary, HC format)
func (c *hcCtx) searchExtDict(win []byte, ip int, ipIndex uint32, iLowLimit, iHighLimit int,
	dictCtx *hcCtx, gDictEndIndex uint32, currentBestML, nbAttempts int) hcMatch {
	dbuf := dictCtx.prefix
	lDictEndIndex := len(dictCtx.prefix) + int(dictCtx.dictLimit)
	lDictMatchIndex := dictCtx.hashTable[hcHashPtr(win, ip)]
	matchIndex := lDictMatchIndex + gDictEndIndex - uint32(lDictEndIndex)
	offset, sBack := 0, 0
	for ipIndex-matchIndex <= distanceMax && nbAttempts > 0 {
		nbAttempts--
		mp := int(lDictMatchIndex) - int(dictCtx.dictLimit)
		if le32(dbuf[mp:]) == le32(win[ip:]) {
			vLimit := ip + (lDictEndIndex - int(lDictMatchIndex))
			if vLimit > iHighLimit {
				vLimit = iHighLimit
			}
			mlt := count2(win, ip+minMatch, dbuf, mp+minMatch, vLimit) + minMatch
			back := 0
			if ip > iLowLimit {
				back = hcCountBack(win, ip, dbuf, mp, iLowLimit, 0)
			}
			mlt -= back
			if mlt > currentBestML {
				currentBestML = mlt
				offset = int(ipIndex - matchIndex)
				sBack = back
			}
		}
		nextOffset := uint32(dictCtx.chainTable[uint16(lDictMatchIndex)])
		lDictMatchIndex -= nextOffset
		matchIndex -= nextOffset
	}
	return hcMatch{off: offset, len: currentBestML, back: sBack}
}

// C: LZ4MID_searchExtDict (an attached dictionary, LZ4MID format)
func midSearchExtDict(win []byte, ip int, ipIndex uint32, iHighLimit int, dictCtx *hcCtx, gDictEndIndex uint32) hcMatch {
	dbuf := dictCtx.prefix
	lDictEndIndex := len(dictCtx.prefix) + int(dictCtx.dictLimit)
	hash4Table := dictCtx.hashTable[:midHashTableSize]
	hash8Table := dictCtx.hashTable[midHashTableSize:]
	{
		l8 := hash8Table[midHash8Ptr(win, ip)]
		m8Index := l8 + gDictEndIndex - uint32(lDictEndIndex)
		if ipIndex-m8Index <= distanceMax {
			mp := int(l8) - int(dictCtx.dictLimit)
			safeLen := min(lDictEndIndex-int(l8), iHighLimit-ip)
			mlt := count2(win, ip, dbuf, mp, ip+safeLen)
			if mlt >= minMatch {
				return hcMatch{len: mlt, off: int(ipIndex - m8Index)}
			}
		}
	}
	{
		l4 := hash4Table[midHash4Ptr(win, ip)]
		m4Index := l4 + gDictEndIndex - uint32(lDictEndIndex)
		if ipIndex-m4Index <= distanceMax {
			mp := int(l4) - int(dictCtx.dictLimit)
			safeLen := min(lDictEndIndex-int(l4), iHighLimit-ip)
			mlt := count2(win, ip, dbuf, mp, ip+safeLen)
			if mlt >= minMatch {
				return hcMatch{len: mlt, off: int(ipIndex - m4Index)}
			}
		}
	}
	return hcMatch{}
}

// C: LZ4HC_dropPositions; start and end are win indexes
func (c *hcCtx) dropPositions(win []byte, start, end int, strat hcStrat) {
	startIdx := c.dictLimit + uint32(start)
	if end-start > hcHashTableSize {
		for i, v := range c.hashTable {
			if v >= startIdx {
				c.hashTable[i] = 0
			}
		}
		return
	}
	idx := startIdx
	for p := start; p < end; p++ {
		if strat == lz4mid {
			h4 := midHash4Ptr(win, p)
			h8 := midHashTableSize + midHash8Ptr(win, p)
			if c.hashTable[h4] == idx {
				c.hashTable[h4] = 0
			}
			if c.hashTable[h8] == idx {
				c.hashTable[h8] = 0
			}
		} else {
			h := hcHashPtr(win, p)
			if c.hashTable[h] == idx {
				c.hashTable[h] = 0
			}
		}
		idx++
	}
}

// C: LZ4MID_fillHTable (the dictionary is the prefix)
func (c *hcCtx) midFillHTable(dict []byte) {
	size := len(dict)
	hash4Table := c.hashTable[:midHashTableSize]
	hash8Table := c.hashTable[midHashTableSize:]
	prefixIdx := c.dictLimit
	if size <= midHashSize {
		return
	}
	target := prefixIdx + uint32(size) - midHashSize
	idx := c.nextToUpdate
	for ; idx < target; idx += 3 {
		hash4Table[midHash4Ptr(dict, int(idx-prefixIdx))] = idx
		hash8Table[midHash8Ptr(dict, int(idx+1-prefixIdx))] = idx + 1
	}
	idx = c.nextToUpdate
	if size > 32*kb+midHashSize {
		idx = target - 32*kb
	}
	for ; idx < target; idx++ {
		hash8Table[midHash8Ptr(dict, int(idx-prefixIdx))] = idx
	}
	c.nextToUpdate = target
}

// C: LZ4MID_countMatch
func midCountMatch(win []byte, ip int, ipIndex, pos uint32, matchlimit int, prefixIdx uint32, dict []byte, dictIdx uint32) int {
	if ipIndex-pos > distanceMax {
		return 0
	}
	if pos >= prefixIdx {
		m := int(pos - prefixIdx)
		if ip+8 <= len(win) && m < ip {
			// most candidates differ within 8 bytes
			if diff := le64(win[ip:]) ^ le64(win[m:]); diff != 0 {
				n := bits.TrailingZeros64(diff) >> 3
				return min(n, matchlimit-ip)
			}
		}
		return count(win, ip, m, matchlimit)
	}
	if pos >= dictIdx {
		mp := int(pos - dictIdx)
		safeLen := min(int(prefixIdx-pos), matchlimit-ip)
		return count2(win, ip, dict, mp, ip+safeLen)
	}
	return 0
}

// midDictCtx is an attached dictionary for midCompressAsm (offsets in
// lz4mid_amd64.s): its tables, prefixStart - dictLimit, end index and
// format (1: LZ4MID). The pointers are uintptrs to memory the context
// keeps reachable.
type midDictCtx struct {
	hash, chain, base uintptr
	end               uint64
	mid               int
}

// C: LZ4MID_compress. src is win[srcStart:srcStart+*srcSizePtr].
func (c *hcCtx) midCompress(win []byte, srcStart int, srcSizePtr *int, dst []byte, maxOutputSize int,
	limit outputDirective, usingDictCtx bool) int {
	hash4Table := (*[midHashTableSize]uint32)(c.hashTable[:midHashTableSize])
	hash8Table := (*[midHashTableSize]uint32)(c.hashTable[midHashTableSize:])
	e := hcEnc{win: win, dst: dst, ip: srcStart, anchor: srcStart}
	iend := srcStart + *srcSizePtr
	oend := maxOutputSize
	if limit == fillOutput {
		oend -= lastLiterals
	}
	prefixIdx := c.dictLimit
	dict := c.dict
	dictIdx := c.lowLimit
	gDictEndIndex := c.lowLimit
	var searchIntoDict func(win []byte, ip int, ipIndex uint32, iHighLimit int, dictCtx *hcCtx, gDictEndIndex uint32) hcMatch
	if usingDictCtx && c.dictCtx != nil {
		if hcGetCLevelParams(int(c.dictCtx.compressionLevel)).strat == lz4mid {
			searchIntoDict = midSearchExtDict
		} else {
			searchIntoDict = func(win []byte, ip int, ipIndex uint32, iHighLimit int, dictCtx *hcCtx, gDictEndIndex uint32) hcMatch {
				return c.searchExtDict(win, ip, ipIndex, ip, iHighLimit, dictCtx, gDictEndIndex, minMatch-1, 2)
			}
		}
	}
	refEnd := e.ip

	var dctx *midDictCtx
	if searchIntoDict != nil && len(c.dictCtx.prefix) > 0 {
		d := c.dictCtx
		dctx = &midDictCtx{hash: uintptr(unsafe.Pointer(&d.hashTable[0])), chain: uintptr(unsafe.Pointer(&d.chainTable[0])),
			base: uintptr(unsafe.Pointer(&d.prefix[0])) - uintptr(d.dictLimit), end: uint64(len(d.prefix)) + uint64(d.dictLimit),
			mid: b2i(hcGetCLevelParams(int(d.compressionLevel)).strat == lz4mid)}
	}
	if *srcSizePtr >= minLength && useMidAsm && (searchIntoDict == nil || dctx != nil) && dictIdx <= prefixIdx &&
		(midDictAsm || dictIdx == prefixIdx && dctx == nil) && len(dict) >= int(prefixIdx-dictIdx) &&
		iend == len(win) && len(dst) >= maxOutputSize && maxOutputSize >= 1 && (limit != fillOutput || oend > 0) &&
		(limit != notLimited || len(dst) >= CompressBound(*srcSizePtr)) {
		olimit := oend
		if limit == notLimited {
			olimit = -1
		}
		var dp *byte
		if dictIdx < prefixIdx {
			dp = &dict[0]
		}
		mid := midCompressAsm
		if dp != nil || dctx != nil {
			mid = midCompressDictAsm
		}
		nip, anchor, op, st, ml, md := mid(win, e.ip, iend, int(prefixIdx), &c.hashTable[0], dst, olimit, dp, int(dictIdx), dctx)
		if st != 0 && (st != 1 || limit != fillOutput) {
			return 0
		}
		e.ip, e.anchor, e.op = nip, anchor, op
		if st == 1 {
			refEnd = e.ip + ml
			e.encodeLastSequenceFillOutput(ml, md, oend)
		}
	} else if *srcSizePtr >= minLength {
		mflimit := iend - mfLimit
		matchlimit := iend - lastLiterals
		ilimit := iend - midHashSize
		ilimitIdx := uint32(ilimit) + prefixIdx

		for e.ip <= mflimit {
			ip := e.ip
			ipIndex := uint32(ip) + prefixIdx
			var matchLength, matchDistance int
			{
				h8 := midHash8Ptr(win, ip)
				pos8 := hash8Table[h8]
				hash8Table[h8] = ipIndex
				matchLength = midCountMatch(win, ip, ipIndex, pos8, matchlimit, prefixIdx, dict, dictIdx)
				matchDistance = int(ipIndex - pos8)
			}
			if matchLength < minMatch {
				h4 := midHash4Ptr(win, ip)
				pos4 := hash4Table[h4]
				hash4Table[h4] = ipIndex
				matchLength = midCountMatch(win, ip, ipIndex, pos4, matchlimit, prefixIdx, dict, dictIdx)
				matchDistance = int(ipIndex - pos4)
			}
			found := false
			if matchLength >= minMatch {
				if matchLength < 8 && ip < mflimit {
					h8 := midHash8Ptr(win, ip+1)
					pos8 := hash8Table[h8]
					ml2 := midCountMatch(win, ip+1, ipIndex+1, pos8, matchlimit, prefixIdx, dict, dictIdx)
					if ml2 > matchLength && pos8 != ipIndex {
						hash8Table[h8] = ipIndex + 1
						ip++
						matchLength = ml2
						matchDistance = int(ipIndex + 1 - pos8)
					}
				}
				found = true
			} else if usingDictCtx && searchIntoDict != nil && ipIndex-gDictEndIndex < distanceMax-8 {
				dm := searchIntoDict(win, ip, ipIndex, matchlimit, c.dictCtx, gDictEndIndex)
				matchLength = dm.len
				matchDistance = dm.off
				found = matchLength >= minMatch
			}
			if !found {
				e.ip += 1 + (e.ip-e.anchor)>>9
				continue
			}

			// catch back
			for ip > e.anchor && ip > matchDistance && win[ip-1] == win[ip-matchDistance-1] {
				ip--
				matchLength++
			}
			e.ip = ip
			{
				startMatchIdx := uint32(ip) + prefixIdx
				hash8Table[midHash8Ptr(win, ip+1)] = startMatchIdx + 1
				hash8Table[midHash8Ptr(win, ip+2)] = startMatchIdx + 2
				hash4Table[midHash4Ptr(win, ip+1)] = startMatchIdx + 1
			}
			if e.encodeSequence(matchLength, matchDistance, limit, oend) {
				if limit != fillOutput {
					return 0
				}
				refEnd = e.ip + matchLength
				e.encodeLastSequenceFillOutput(matchLength, matchDistance, oend)
				break
			}
			{
				endMatchIdx := uint32(e.ip) + prefixIdx
				posM2 := endMatchIdx - 2
				if posM2 < ilimitIdx {
					if e.ip > 5 {
						hash8Table[midHash8Ptr(win, e.ip-5)] = endMatchIdx - 5
					}
					hash8Table[midHash8Ptr(win, e.ip-3)] = endMatchIdx - 3
					hash8Table[midHash8Ptr(win, e.ip-2)] = endMatchIdx - 2
					hash4Table[midHash4Ptr(win, e.ip-2)] = endMatchIdx - 2
					hash4Table[midHash4Ptr(win, e.ip-1)] = endMatchIdx - 1
				}
			}
		}
	}

	if refEnd < e.ip {
		refEnd = e.ip
	}
	if e.encodeLastLiterals(iend, limit, oend) {
		return 0
	}
	if e.ip < refEnd {
		c.dropPositions(win, e.ip, min(refEnd, iend-midHashSize), lz4mid)
	}
	*srcSizePtr = e.ip - srcStart
	return e.op
}

// C: LZ4HC_Insert; target is a win index
func (c *hcCtx) insert(win []byte, target int) {
	prefixIdx := c.dictLimit
	t := uint32(target) + prefixIdx
	idx := c.nextToUpdate
	for idx < t {
		h := hcHashPtr(win, int(idx-prefixIdx))
		delta := idx - c.hashTable[h]
		if delta > distanceMax {
			delta = distanceMax
		}
		c.chainTable[uint16(idx)] = uint16(delta)
		c.hashTable[h] = idx
		idx++
	}
	c.nextToUpdate = t
}

// C: LZ4HC_rotatePattern
func hcRotatePattern(rotate uint, pattern uint32) uint32 {
	bitsToRotate := (rotate & 3) << 3
	if bitsToRotate == 0 {
		return pattern
	}
	return bits.RotateLeft32(pattern, int(bitsToRotate))
}

// C: LZ4HC_countPattern
func hcCountPattern(b []byte, ip, iEnd int, pattern32 uint32) int {
	iStart := ip
	pattern := uint64(pattern32) + uint64(pattern32)<<32
	for ip < iEnd-7 {
		diff := le64(b[ip:]) ^ pattern
		if diff == 0 {
			ip += 8
			continue
		}
		ip += bits.TrailingZeros64(diff) >> 3
		return ip - iStart
	}
	pb := pattern
	for ip < iEnd && b[ip] == byte(pb) {
		ip++
		pb >>= 8
	}
	return ip - iStart
}

// C: LZ4HC_reverseCountPattern
func hcReverseCountPattern(b []byte, ip, iLow int, pattern uint32) int {
	iStart := ip
	for ip >= iLow+4 {
		if le32(b[ip-4:]) != pattern {
			break
		}
		ip -= 4
	}
	shift := 24
	for ip > iLow {
		if b[ip-1] != byte(pattern>>uint(shift)) {
			break
		}
		ip--
		shift -= 8
	}
	return iStart - ip
}

// C: LZ4HC_protectDictEnd
func hcProtectDictEnd(dictLimit, matchIndex uint32) bool {
	return (dictLimit - 1 - matchIndex) >= 3
}

// C: LZ4HC_sparsestChain
func hcSparsestChain(chainTable *[hcMaxD]uint16, matchIndex uint32, longest int, matchChainPos *uint32) uint32 {
	const kTrigger = 4
	distanceToNextMatch := uint32(1)
	end := longest - minMatch + 1
	step := 1
	accel := 1 << kTrigger
	for pos := 0; pos < end; pos += step {
		candidateDist := uint32(chainTable[uint16(matchIndex+uint32(pos))])
		step = accel >> kTrigger
		accel++
		if candidateDist > distanceToNextMatch {
			distanceToNextMatch = candidateDist
			*matchChainPos = uint32(pos)
			accel = 1 << kTrigger
		}
	}
	return distanceToNextMatch
}

// C: LZ4HC_patternSegment
func (c *hcCtx) patternSegment(win []byte, candidateIdx, pattern uint32, iHighLimit int, lowestMatchIndex uint32) (forward, back int, ok bool) {
	prefixIdx := c.dictLimit
	dict := c.dict
	dictIdx := c.lowLimit
	dictEnd := int(prefixIdx - dictIdx) // in dict
	extDict := candidateIdx < prefixIdx
	if extDict {
		mp := int(candidateIdx - dictIdx)
		if le32(dict[mp:]) != pattern {
			return 0, 0, false
		}
		forward = hcCountPattern(dict, mp+4, dictEnd, pattern) + 4
		if mp+forward == dictEnd {
			rotated := hcRotatePattern(uint(forward), pattern)
			forward += hcCountPattern(win, 0, iHighLimit, rotated)
		}
		back = hcReverseCountPattern(dict, mp, 0, pattern)
	} else {
		mp := int(candidateIdx - prefixIdx)
		if le32(win[mp:]) != pattern {
			return 0, 0, false
		}
		forward = hcCountPattern(win, mp+4, iHighLimit, pattern) + 4
		back = hcReverseCountPattern(win, mp, 0, pattern)
		if mp-back == 0 && dictIdx < prefixIdx {
			rotated := hcRotatePattern(uint(uint32(-int32(back))), pattern)
			back += hcReverseCountPattern(dict, dictEnd, 0, rotated)
		}
	}
	back = int(candidateIdx - max(candidateIdx-uint32(back), lowestMatchIndex))
	return forward, back, true
}

const (
	repUntested = iota
	repNot
	repConfirmed
)

// C: LZ4HC_InsertAndGetWiderMatch; ip, iLowLimit and iHighLimit are win
// indexes.
func (c *hcCtx) insertAndGetWiderMatch(win []byte, ip, iLowLimit, iHighLimit, longest, maxNbAttempts int,
	patternAnalysis, chainSwap, usingDictCtx, favorDecSpeed bool) hcMatch {
	chainTable := &c.chainTable
	hashTable := &c.hashTable
	dictCtx := c.dictCtx
	prefixIdx := c.dictLimit
	ipIndex := uint32(ip) + prefixIdx
	withinStartDistance := c.lowLimit+(distanceMax+1) > ipIndex
	lowestMatchIndex := ipIndex - distanceMax
	if withinStartDistance {
		lowestMatchIndex = c.lowLimit
	}
	dict := c.dict
	dictIdx := c.lowLimit
	lookBackLength := ip - iLowLimit
	maxLength := iHighLimit - iLowLimit
	nbAttempts := maxNbAttempts
	matchChainPos := uint32(0)
	pattern := le32(win[ip:])
	repeat := repUntested
	srcPatternLength := 0
	offset, sBack := 0, 0

	useAsm := useHCAsm && longest > lookBackLength && iHighLimit <= len(win) && c.nextToUpdate >= prefixIdx
	var matchIndex uint32
	a := &c.args
	if useAsm {
		*a = hcChainArgs{win: uintptr(unsafe.Pointer(&win[0])), chain: uintptr(unsafe.Pointer(&chainTable[0])),
			ip: ip, iLowLimit: iLowLimit, iHighLimit: iHighLimit,
			prefixIdx: uint64(prefixIdx), lowest: uint64(lowestMatchIndex), ipIndex: uint64(ipIndex),
			pattern: uint64(pattern), maxLength: maxLength, pa: b2i(patternAnalysis),
			nbAttempts: nbAttempts, longest: longest, hashTable: uintptr(unsafe.Pointer(&hashTable[0])),
			next: uint64(c.nextToUpdate), insert: 1, chainSwap: b2i(chainSwap), favor: b2i(favorDecSpeed)}
	} else {
		c.insert(win, ip)
		matchIndex = hashTable[hcHashPtr(win, ip)]
	}

	for useAsm || (matchIndex >= lowestMatchIndex && nbAttempts > 0 && longest < maxLength) {
		matchLength := 0
		if useAsm {
			if a.insert == 0 {
				a.matchIndex, a.nbAttempts, a.longest, a.offset, a.sBack = uint64(matchIndex), nbAttempts, longest, offset, sBack
				a.chainPos = uint64(matchChainPos)
			}
			r := hcChainAsm(a)
			if a.insert != 0 {
				a.insert = 0
				c.nextToUpdate = uint32(a.next)
			}
			matchIndex, nbAttempts, longest, offset, sBack = uint32(a.matchIndex), a.nbAttempts, a.longest, a.offset, a.sBack
			matchChainPos = uint32(a.chainPos)
			if r == 0 {
				break
			}
			if r == 2 {
				// candidate done: on to the pattern analysis
				goto patternAnalysisStep
			}
		}
		nbAttempts--
		if favorDecSpeed && ipIndex-matchIndex < 8 {
			// favorDecSpeed skips matches with offset < 8
		} else if matchIndex >= prefixIdx {
			mp := int(matchIndex - prefixIdx)
			if le16(win[iLowLimit+longest-1:]) == le16(win[mp-lookBackLength+longest-1:]) {
				if le32(win[mp:]) == pattern {
					back := 0
					if lookBackLength != 0 {
						back = hcCountBack(win, ip, win, mp, iLowLimit, 0)
					}
					matchLength = minMatch + count(win, ip+minMatch, mp+minMatch, iHighLimit)
					matchLength -= back
					if matchLength > longest {
						longest = matchLength
						offset = int(ipIndex - matchIndex)
						sBack = back
					}
				}
			}
		} else {
			mp := int(matchIndex - dictIdx)
			if matchIndex <= prefixIdx-4 && le32(dict[mp:]) == pattern {
				vLimit := ip + int(prefixIdx-matchIndex)
				if vLimit > iHighLimit {
					vLimit = iHighLimit
				}
				matchLength = count2(win, ip+minMatch, dict, mp+minMatch, vLimit) + minMatch
				if ip+matchLength == vLimit && vLimit < iHighLimit {
					matchLength += count(win, ip+matchLength, 0, iHighLimit)
				}
				back := 0
				if lookBackLength != 0 {
					back = hcCountBack(win, ip, dict, mp, iLowLimit, 0)
				}
				matchLength -= back
				if matchLength > longest {
					longest = matchLength
					offset = int(ipIndex - matchIndex)
					sBack = back
				}
			}
		}

		if chainSwap && matchLength == longest && matchIndex+uint32(longest) <= ipIndex {
			distanceToNextMatch := hcSparsestChain(chainTable, matchIndex, longest, &matchChainPos)
			if distanceToNextMatch > 1 {
				if distanceToNextMatch > matchIndex {
					break
				}
				matchIndex -= distanceToNextMatch
				continue
			}
		}

	patternAnalysisStep:
		{
			distNextMatch := uint32(chainTable[uint16(matchIndex)])
			if patternAnalysis && distNextMatch == 1 && matchChainPos == 0 {
				matchCandidateIdx := matchIndex - 1
				if repeat == repUntested {
					if (pattern&0xFFFF) == (pattern>>16) && (pattern&0xFF) == (pattern>>24) {
						repeat = repConfirmed
						srcPatternLength = hcCountPattern(win, ip+4, iHighLimit, pattern) + 4
					} else {
						repeat = repNot
					}
				}
				if repeat == repConfirmed && matchCandidateIdx >= lowestMatchIndex &&
					hcProtectDictEnd(prefixIdx, matchCandidateIdx) {
					forwardPatternLength, backLength, ok := c.patternSegment(win, matchCandidateIdx, pattern, iHighLimit, lowestMatchIndex)
					if ok {
						currentSegmentLength := backLength + forwardPatternLength
						fullPatternFits := currentSegmentLength >= srcPatternLength && forwardPatternLength <= srcPatternLength
						var newMatchIndex uint32
						if fullPatternFits {
							newMatchIndex = matchCandidateIdx + uint32(forwardPatternLength) - uint32(srcPatternLength)
						} else {
							newMatchIndex = matchCandidateIdx - uint32(backLength)
						}
						if !hcProtectDictEnd(prefixIdx, newMatchIndex) {
							matchIndex = prefixIdx
							continue
						}
						matchIndex = newMatchIndex
						if !fullPatternFits && lookBackLength == 0 {
							maxML := min(currentSegmentLength, srcPatternLength)
							if longest < maxML {
								if ip+int(prefixIdx)-int(matchIndex) > distanceMax {
									break
								}
								longest = maxML
								offset = int(ipIndex - matchIndex)
							}
							distToNextPattern := uint32(chainTable[uint16(matchIndex)])
							if distToNextPattern > matchIndex {
								break
							}
							matchIndex -= distToNextPattern
						}
						continue
					}
				}
			}
		}

		matchIndex -= uint32(chainTable[uint16(matchIndex+matchChainPos)])
	}

	if usingDictCtx && nbAttempts > 0 && longest < maxLength && withinStartDistance {
		dbuf := dictCtx.prefix
		dictEndOffset := len(dictCtx.prefix) + int(dictCtx.dictLimit)
		dictMatchIndex := dictCtx.hashTable[hcHashPtr(win, ip)]
		matchIndex = dictMatchIndex + lowestMatchIndex - uint32(dictEndOffset)
		for ipIndex-matchIndex <= distanceMax && longest < maxLength && nbAttempts > 0 {
			nbAttempts--
			mp := int(dictMatchIndex) - int(dictCtx.dictLimit)
			if le32(dbuf[mp:]) == pattern {
				vLimit := ip + (dictEndOffset - int(dictMatchIndex))
				if vLimit > iHighLimit {
					vLimit = iHighLimit
				}
				mlt := count2(win, ip+minMatch, dbuf, mp+minMatch, vLimit) + minMatch
				back := 0
				if lookBackLength != 0 {
					back = hcCountBack(win, ip, dbuf, mp, iLowLimit, 0)
				}
				mlt -= back
				if mlt > longest {
					longest = mlt
					offset = int(ipIndex - matchIndex)
					sBack = back
				}
			}
			nextOffset := uint32(dictCtx.chainTable[uint16(dictMatchIndex)])
			dictMatchIndex -= nextOffset
			matchIndex -= nextOffset
		}
	}
	return hcMatch{len: longest, off: offset, back: sBack}
}

// C: LZ4HC_compress_hashChain
func (c *hcCtx) compressHashChain(win []byte, srcStart int, srcSizePtr *int, dst []byte, maxOutputSize, maxNbAttempts int,
	limit outputDirective, usingDictCtx bool) int {
	inputSize := *srcSizePtr
	patternAnalysis := maxNbAttempts > 128
	e := hcEnc{win: win, dst: dst, ip: srcStart, anchor: srcStart}
	iend := srcStart + inputSize
	oend := maxOutputSize
	if limit == fillOutput {
		oend -= lastLiterals
	}
	var start0, start2, start3 int
	var m0, m1, m2, m3 hcMatch

	*srcSizePtr = 0
	if inputSize < minLength {
		goto lastLiterals
	}
	if c.parseAsmOK(win, srcStart, inputSize, dst, maxOutputSize, limit, usingDictCtx) {
		a := c.newParseArgs(win, srcStart, dst, oend, limit, usingDictCtx)
		a.attempts = maxNbAttempts
		a.pa |= b2i(patternAnalysis) * paPattern
		st := 2
		for st == 2 {
			a.stop = a.ip + hcAsmChunk
			st = hcHashChainAsm(&a)
		}
		c.nextToUpdate = uint32(a.next)
		if st != 0 && (st != 1 || limit != fillOutput) {
			return 0
		}
		e.ip, e.anchor, e.op = a.ip, a.anchor, a.op
		if st == 1 {
			e.encodeLastSequenceFillOutput(a.mlen, a.moff, oend)
		}
		goto lastLiterals
	}
	{
		mflimit := iend - mfLimit
		matchlimit := iend - lastLiterals
		for e.ip <= mflimit {
			m1 = c.insertAndGetWiderMatch(win, e.ip, e.ip, matchlimit, minMatch-1, maxNbAttempts, patternAnalysis, false, usingDictCtx, false)
			if m1.len < minMatch {
				e.ip++
				continue
			}
			start0 = e.ip
			m0 = m1

		search2:
			if e.ip+m1.len <= mflimit {
				start2 = e.ip + m1.len - 2
				m2 = c.insertAndGetWiderMatch(win, start2, e.ip, matchlimit, m1.len, maxNbAttempts, patternAnalysis, false, usingDictCtx, false)
				start2 += m2.back
			} else {
				m2 = hcMatch{}
			}

			if m2.len <= m1.len {
				if e.encodeSequence(m1.len, m1.off, limit, oend) {
					goto destOverflow
				}
				continue
			}

			if start0 < e.ip && start2 < e.ip+m0.len {
				e.ip = start0
				m1 = m0
			}

			if start2-e.ip < 3 {
				e.ip = start2
				m1 = m2
				goto search2
			}

			for {
				if start2-e.ip < optimalML {
					gap := start2 - e.ip
					newML := min(m1.len, optimalML)
					if newML > gap+m2.len-minMatch {
						newML = gap + m2.len - minMatch
					}
					if newML > gap {
						start2 += newML - gap
						m2.len -= newML - gap
					}
				}

				if start2+m2.len <= mflimit {
					start3 = start2 + m2.len - 3
					m3 = c.insertAndGetWiderMatch(win, start3, start2, matchlimit, m2.len, maxNbAttempts, patternAnalysis, false, usingDictCtx, false)
					start3 += m3.back
				} else {
					m3 = hcMatch{}
				}

				if m3.len > m2.len && start3 < e.ip+m1.len+3 {
					if start3 >= e.ip+m1.len {
						if start2 < e.ip+m1.len {
							correction := e.ip + m1.len - start2
							start2 += correction
							m2.len -= correction
							if m2.len < minMatch {
								start2 = start3
								m2 = m3
							}
						}
						if e.encodeSequence(m1.len, m1.off, limit, oend) {
							goto destOverflow
						}
						e.ip = start3
						m1 = m3
						start0 = start2
						m0 = m2
						goto search2
					}
					start2 = start3
					m2 = m3
					continue
				}

				if start2 < e.ip+m1.len {
					m1.len = start2 - e.ip
				}
				if e.encodeSequence(m1.len, m1.off, limit, oend) {
					goto destOverflow
				}
				e.ip = start2
				m1 = m2
				if m3.len <= m1.len {
					if e.encodeSequence(m1.len, m1.off, limit, oend) {
						goto destOverflow
					}
					break
				}
				start2 = start3
				m2 = m3
			}
		}
	}

lastLiterals:
	if e.encodeLastLiterals(iend, limit, oend) {
		return 0
	}
	*srcSizePtr = e.ip - srcStart
	return e.op

destOverflow:
	if limit != fillOutput {
		return 0
	}
	e.encodeLastSequenceFillOutput(m1.len, m1.off, oend)
	goto lastLiterals
}

// C: LZ4HC_compress_generic_internal. The input is src, which continues the
// prefix (c.prefix's capacity reaches its end).
func (c *hcCtx) compressGenericInternal(src []byte, srcSizePtr *int, dst []byte, dstCapacity, cLevel int,
	limit outputDirective, usingDictCtx bool) int {
	if uint32(*srcSizePtr) > MaxInputSize || *srcSizePtr < 0 {
		return 0
	}
	if dstCapacity < 1 {
		return 0
	}
	if *srcSizePtr == 0 {
		dst[0] = 0
		return 1
	}
	srcStart := len(c.prefix)
	win := c.prefix[:srcStart+*srcSizePtr]
	c.prefix = win
	cParam := hcGetCLevelParams(cLevel)
	srcSize := *srcSizePtr
	var result int
	switch cParam.strat {
	case lz4mid:
		result = c.midCompress(win, srcStart, srcSizePtr, dst, dstCapacity, limit, usingDictCtx)
	case lz4hc:
		result = c.compressHashChain(win, srcStart, srcSizePtr, dst, dstCapacity, cParam.nbSearches, limit, usingDictCtx)
	default:
		result = c.compressOptimal(win, srcStart, srcSizePtr, dst, dstCapacity, cParam.nbSearches, cParam.nbSearchesSeq,
			cParam.targetLength, limit, usingDictCtx, c.favorDecSpeed)
	}
	if limit == fillOutput && result > 0 && *srcSizePtr < srcSize {
		consumedEnd := srcStart + *srcSizePtr
		consumedIdx := c.dictLimit + uint32(consumedEnd)
		if cParam.strat != lz4mid && c.nextToUpdate > consumedIdx {
			c.dropPositions(win, consumedEnd, consumedEnd+int(c.nextToUpdate-consumedIdx), cParam.strat)
			c.nextToUpdate = consumedIdx
		}
		c.prefix = win[:consumedEnd]
	}
	return result
}

// C: isStateCompatible
func hcIsStateCompatible(a, b *hcCtx) bool {
	isMid1 := hcGetCLevelParams(int(a.compressionLevel)).strat == lz4mid
	isMid2 := hcGetCLevelParams(int(b.compressionLevel)).strat == lz4mid
	return isMid1 == isMid2
}

// C: LZ4HC_compress_generic (with _noDictCtx and _dictCtx)
func (c *hcCtx) compressGeneric(src []byte, srcSizePtr *int, dst []byte, dstCapacity, cLevel int, limit outputDirective) int {
	if c.dictCtx == nil {
		return c.compressGenericInternal(src, srcSizePtr, dst, dstCapacity, cLevel, limit, false)
	}
	position := len(c.prefix) + int(c.dictLimit-c.lowLimit)
	if position >= 64*kb {
		c.dictCtx = nil
		return c.compressGenericInternal(src, srcSizePtr, dst, dstCapacity, cLevel, limit, false)
	}
	if position == 0 && *srcSizePtr >= hcGetCLevelParams(cLevel).dictCopyMin && hcIsStateCompatible(c, c.dictCtx) {
		favorDecSpeed := c.favorDecSpeed
		opt := c.opt
		*c = *c.dictCtx
		c.opt = opt
		c.setExternalDict(src)
		c.compressionLevel = int16(cLevel)
		c.favorDecSpeed = favorDecSpeed
		return c.compressGenericInternal(src, srcSizePtr, dst, dstCapacity, cLevel, limit, false)
	}
	return c.compressGenericInternal(src, srcSizePtr, dst, dstCapacity, cLevel, limit, true)
}

// C: LZ4_resetStreamHC_fast
func (c *hcCtx) resetFast(level int) {
	c.dictLimit += uint32(len(c.prefix))
	c.prefix = nil
	c.prefixSet = false
	c.dictCtx = nil
	c.setCompressionLevel(level)
}

// C: LZ4_setCompressionLevel
func (c *hcCtx) setCompressionLevel(level int) {
	if level < 1 {
		level = LevelHCDefault
	}
	if level > LevelHCMax {
		level = LevelHCMax
	}
	c.compressionLevel = int16(level)
}

// C: LZ4_initStreamHC
func (c *hcCtx) init() {
	opt := c.opt
	*c = hcCtx{opt: opt}
	c.setCompressionLevel(LevelHCDefault)
}

// C: LZ4_compress_HC_extStateHC_fastReset
func (c *hcCtx) compressExtStateFastReset(src, dst []byte, level int) int {
	c.resetFast(level)
	c.initInternal(src)
	n := len(src)
	if len(dst) < CompressBound(len(src)) {
		return c.compressGeneric(src, &n, dst, len(dst), level, limitedOutput)
	}
	return c.compressGeneric(src, &n, dst, len(dst), level, notLimited)
}

// C: LZ4HC_setExternalDict
func (c *hcCtx) setExternalDict(newBlock []byte) {
	if hcGetCLevelParams(int(c.compressionLevel)).strat != lz4mid {
		endIdx := c.dictLimit + uint32(len(c.prefix))
		if len(c.prefix) >= 4 {
			c.insert(c.prefix, len(c.prefix)-3)
		}
		for idx := c.nextToUpdate; idx < endIdx; idx++ {
			c.chainTable[uint16(idx)] = 0
		}
	}
	c.lowLimit = c.dictLimit
	c.dict = c.prefix
	c.dictLimit += uint32(len(c.prefix))
	c.prefix = newBlock[:0:cap(newBlock)]
	c.prefixSet = true
	c.nextToUpdate = c.dictLimit
	c.dictCtx = nil
}

// C: LZ4_loadDictHC
func (c *hcCtx) loadDict(dictionary []byte) int {
	if len(dictionary) > 64*kb {
		dictionary = dictionary[len(dictionary)-64*kb:]
	}
	cLevel := int(c.compressionLevel)
	favorDecSpeed := c.favorDecSpeed
	c.init()
	c.setCompressionLevel(cLevel)
	c.favorDecSpeed = favorDecSpeed
	cp := hcGetCLevelParams(cLevel)
	c.initInternal(dictionary)
	c.prefix = dictionary
	if cp.strat == lz4mid {
		c.midFillHTable(dictionary)
	} else if len(dictionary) >= hcHashSize {
		c.insert(dictionary, len(dictionary)-3)
	}
	return len(dictionary)
}

// C: LZ4_compressHC_continue_generic
func (c *hcCtx) continueGeneric(src []byte, srcSizePtr *int, dst []byte, dstCapacity int, limit outputDirective) int {
	if !c.prefixSet {
		c.initInternal(src)
	}
	if uint64(len(c.prefix))+uint64(c.dictLimit) > 2*gb {
		dictSize := min(len(c.prefix), 64*kb)
		c.loadDict(c.prefix[len(c.prefix)-dictSize:])
	}
	// blocks follow each other within one slice
	end := addr(c.prefix) + uintptr(len(c.prefix))
	if addr(src) != end || cap(c.prefix) < len(c.prefix)+len(src) {
		c.setExternalDict(src)
	}
	{
		sourceEnd := addr(src) + uintptr(*srcSizePtr)
		dictBegin := addr(c.dict)
		dictEnd := dictBegin + uintptr(c.dictLimit-c.lowLimit)
		if sourceEnd > dictBegin && addr(src) < dictEnd {
			if sourceEnd > dictEnd {
				sourceEnd = dictEnd
			}
			k := uint32(sourceEnd - dictBegin)
			c.lowLimit += k
			c.dict = c.dict[k:]
			if c.dictLimit-c.lowLimit < hcHashSize {
				c.lowLimit = c.dictLimit
				c.dict = c.prefix
			}
		}
	}
	return c.compressGeneric(src[:*srcSizePtr], srcSizePtr, dst, dstCapacity, int(c.compressionLevel), limit)
}

// C: LZ4_saveDictHC
func (c *hcCtx) saveDict(safeBuffer []byte) int {
	dictSize := len(safeBuffer)
	prefixSize := len(c.prefix)
	if !c.prefixSet {
		return 0
	}
	if dictSize > 64*kb {
		dictSize = 64 * kb
	}
	if dictSize < 4 {
		dictSize = 0
	}
	if dictSize > prefixSize {
		dictSize = prefixSize
	}
	if dictSize > 0 {
		copy(safeBuffer[:dictSize], c.prefix[prefixSize-dictSize:])
	}
	endIndex := uint32(prefixSize) + c.dictLimit
	c.prefix = safeBuffer[:dictSize]
	c.prefixSet = safeBuffer != nil
	c.dictLimit = endIndex - uint32(dictSize)
	c.lowLimit = endIndex - uint32(dictSize)
	c.dict = c.prefix
	if c.nextToUpdate < c.dictLimit {
		c.nextToUpdate = c.dictLimit
	}
	return dictSize
}

// StreamHC is the state of the high compression levels (C: LZ4_streamHC_t),
// for streaming compression (LZ4_compress_HC_continue semantics) and for
// reusing its tables (about 256 KB) across one-shot compressions. The zero
// value is not ready: use NewStreamHC or Reset. Blocks count as contiguous
// as described for Stream.
type StreamHC struct {
	c hcCtx
}

// NewStreamHC returns a stream at compression level (C:
// LZ4_createStreamHC + LZ4_setCompressionLevel).
func NewStreamHC(level int) *StreamHC {
	s := new(StreamHC)
	s.Reset(level)
	return s
}

// Reset clears the stream and sets its compression level (C:
// LZ4_resetStreamHC).
func (s *StreamHC) Reset(level int) {
	s.c.init()
	s.c.setCompressionLevel(level)
}

// ResetFast starts a new independent stream at level, without clearing the
// tables (C: LZ4_resetStreamHC_fast).
func (s *StreamHC) ResetFast(level int) { s.c.resetFast(level) }

// SetCompressionLevel changes the level of the next blocks (C:
// LZ4_setCompressionLevel): values below 1 select LevelHCDefault, larger
// than LevelHCMax select LevelHCMax.
func (s *StreamHC) SetCompressionLevel(level int) { s.c.setCompressionLevel(level) }

// FavorDecompressionSpeed makes the optimal parser (levels 10 to 12)
// prefer output that decodes faster (C: LZ4_favorDecompressionSpeed).
func (s *StreamHC) FavorDecompressionSpeed(favor bool) { s.c.favorDecSpeed = favor }

// LoadDict loads dictionary (its last 64 KB) as the history of the next
// block, keeping the level (C: LZ4_loadDictHC). It returns the size loaded.
func (s *StreamHC) LoadDict(dictionary []byte) int { return s.c.loadDict(dictionary) }

// AttachDictionary makes dict, a stream that loaded a dictionary, the
// history of the next block (C: LZ4_attach_HC_dictionary).
func (s *StreamHC) AttachDictionary(dict *StreamHC) {
	if dict == nil {
		s.c.dictCtx = nil
		return
	}
	s.c.dictCtx = &dict.c
}

// CompressContinue compresses src as the next block of the stream (C:
// LZ4_compress_HC_continue). It returns the compressed size, or 0 if the
// block does not fit into dst (its length is the capacity).
func (s *StreamHC) CompressContinue(dst, src []byte) int {
	n := len(src)
	if len(dst) < CompressBound(n) {
		return s.c.continueGeneric(src, &n, dst, len(dst), limitedOutput)
	}
	return s.c.continueGeneric(src, &n, dst, len(dst), notLimited)
}

// CompressContinueDestSize compresses as much of src as fits into dst as the
// next block (C: LZ4_compress_HC_continue_destSize). It returns the
// compressed size and the bytes of src consumed.
func (s *StreamHC) CompressContinueDestSize(dst, src []byte) (n, consumed int) {
	consumed = len(src)
	n = s.c.continueGeneric(src, &consumed, dst, len(dst), fillOutput)
	return n, consumed
}

// SaveDict copies the last len(buf) bytes of history (at most 64 KB) into
// buf (C: LZ4_saveDictHC). It returns the size saved.
func (s *StreamHC) SaveDict(buf []byte) int { return s.c.saveDict(buf) }

// Compress compresses src into dst as an independent block at level,
// reusing the stream's tables (C: LZ4_compress_HC_extStateHC_fastReset).
func (s *StreamHC) Compress(dst, src []byte, level int) int {
	return s.c.compressExtStateFastReset(src, dst, level)
}

var hcPool = sync.Pool{New: func() any { return new(hcCtx) }}

// CompressHC compresses src into dst as an independent block at level
// (C: LZ4_compress_HC): levels 2 to 12 (below 1: LevelHCDefault; 1 behaves
// as 2; above 12: 12). dst's length is the capacity. It returns the
// compressed size, or 0 if the block does not fit.
func CompressHC(dst, src []byte, level int) int {
	c := hcPool.Get().(*hcCtx)
	c.init()
	n := c.compressExtStateFastReset(src, dst, level)
	c.prefix, c.dict = nil, nil
	hcPool.Put(c)
	return n
}

// CompressHCDestSize compresses as much of src as fits into dst at level
// (C: LZ4_compress_HC_destSize). It returns the compressed size and the
// bytes of src consumed.
func CompressHCDestSize(dst, src []byte, level int) (n, consumed int) {
	c := hcPool.Get().(*hcCtx)
	c.init()
	c.initInternal(src)
	c.setCompressionLevel(level)
	consumed = len(src)
	n = c.compressGeneric(src, &consumed, dst, len(dst), level, fillOutput)
	c.prefix, c.dict = nil, nil
	hcPool.Put(c)
	return n, consumed
}
