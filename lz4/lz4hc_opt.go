package lz4

import "unsafe"

// The optimal parser of lz4hc.c (levels 10 to 12): LZ4HC_compress_optimal.

type hcOptimal struct {
	price  int32
	off    int32
	mlen   int32 // 1 means literal
	litlen int32
}

const (
	trailingLiterals = 3 // C: TRAILING_LITERALS
	priceScale       = 4 // C: LZ4HC_PRICE_SCALE
	seqPriceFDS      = 1 // C: LZ4HC_SEQ_PRICE_FDS
	seqSearchDist    = 4 // C: LZ4HC_SEQ_SEARCH_DIST
)

// C: LZ4HC_literalsPrice
func literalsPrice(litlen int) int {
	price := litlen
	if litlen >= runMask {
		price += 1 + (litlen-runMask)/255
	}
	return price * priceScale
}

// C: LZ4HC_sequencePrice
func sequencePrice(litlen, mlen int) int {
	price := 1 + 2
	if mlen >= mlMask+minMatch {
		price += 1 + (mlen-(mlMask+minMatch))/255
	}
	return price*priceScale + literalsPrice(litlen)
}

// C: LZ4HC_pricedLitlen
func pricedLitlen(llen int) int {
	const reduceFrom = runMask + 255
	if llen < reduceFrom {
		return llen
	}
	return reduceFrom + (llen-runMask)%255
}

// C: LZ4HC_FindLongestMatch
func (c *hcCtx) findLongestMatch(win []byte, ip, iHighLimit, nbSearches int, usingDictCtx, favorDecSpeed bool) hcMatch {
	md := c.insertAndGetWiderMatch(win, ip, ip, iHighLimit, minMatch-1, nbSearches, true, true, usingDictCtx, favorDecSpeed)
	if md.len < minMatch {
		return hcMatch{}
	}
	if favorDecSpeed && md.len > 18 && md.len <= 36 {
		md.len = 18
	}
	return md
}

func setLiteral(opt []hcOptimal, pos, litlen, price int) {
	opt[pos] = hcOptimal{price: int32(price), mlen: 1, litlen: int32(litlen)}
}

func setMatch(opt []hcOptimal, pos, mlen, off, price int) {
	opt[pos] = hcOptimal{price: int32(price), off: int32(off), mlen: int32(mlen)}
}

// C: LZ4HC_runStartPrice
func runStartPrice(opt []hcOptimal, pos int) int {
	ll := int(opt[pos].litlen)
	if ll <= pos {
		return int(opt[pos-ll].price)
	}
	return 0
}

// C: LZ4HC_addMatch
func addMatch(opt []hcOptimal, cur int, match hcMatch, lastPos int, favorDecSpeed bool) int {
	ll := int(opt[cur].litlen)
	fds := 0
	if favorDecSpeed {
		fds = 1
	}
	basePrice := runStartPrice(opt, cur) + fds*seqPriceFDS
	for ml := minMatch; ml <= match.len; ml++ {
		pos := cur + ml
		price := basePrice + sequencePrice(ll, ml)
		if pos > lastPos+trailingLiterals || price <= int(opt[pos].price)-fds {
			setMatch(opt, pos, ml, match.off, price)
			if ml == match.len && lastPos < pos {
				lastPos = pos
			}
		}
	}
	for addLit := 1; addLit <= trailingLiterals; addLit++ {
		setLiteral(opt, lastPos+addLit, addLit, int(opt[lastPos].price)+literalsPrice(addLit))
	}
	return lastPos
}

// C: LZ4HC_compress_optimal_internal
func (c *hcCtx) compressOptimal(win []byte, srcStart int, srcSizePtr *int, dst []byte, dstCapacity,
	nbSearches, nbSearchesSeq, targetLength int, limit outputDirective, usingDictCtx, favorDecSpeed bool) int {
	if c.opt == nil {
		c.opt = make([]hcOptimal, optNum+trailingLiterals)
	}
	opt := c.opt
	e := hcEnc{win: win, dst: dst, ip: srcStart, anchor: srcStart}
	inputSize := *srcSizePtr
	iend := srcStart + inputSize
	oend := dstCapacity
	if limit == fillOutput {
		oend -= lastLiterals
	}
	sufficientLen := min(targetLength, optNum-1)

	*srcSizePtr = 0
	if (hcParseFull || !favorDecSpeed) && c.parseAsmOK(win, srcStart, inputSize, dst, dstCapacity, limit, usingDictCtx) {
		a := c.newParseArgs(win, srcStart, dst, oend, limit, usingDictCtx)
		a.attempts = nbSearches
		a.nbSeq = nbSearchesSeq
		a.pa |= b2i(favorDecSpeed) * paFavor
		a.suff = sufficientLen
		a.opt = uintptr(unsafe.Pointer(&opt[0]))
		parse := hcOptimalAsm
		if favorDecSpeed {
			parse = hcOptimalFavAsm
		}
		st := 2
		for st == 2 {
			a.stop = a.ip + hcAsmChunk
			st = parse(&a)
		}
		c.nextToUpdate = uint32(a.next)
		if st != 0 && (st != 1 || limit != fillOutput) {
			return 0
		}
		e.ip, e.anchor, e.op = a.ip, a.anchor, a.op
		if st == 1 {
			e.encodeLastSequenceFillOutput(a.mlen, a.moff, oend)
		}
	} else if inputSize >= minLength {
		mflimit := iend - mfLimit
		matchlimit := iend - lastLiterals
	mainLoop:
		for e.ip <= mflimit {
			llen := pricedLitlen(e.ip - e.anchor)
			var lastPos, lastSeqPos int
			var lastSeq hcMatch

			firstMatch := c.findLongestMatch(win, e.ip, matchlimit, nbSearches, usingDictCtx, favorDecSpeed)
			if firstMatch.len == 0 {
				e.ip++
				continue
			}
			if firstMatch.len > sufficientLen {
				lastSeqPos = 0
				lastSeq = firstMatch
			} else {
				for rPos := 0; rPos < minMatch; rPos++ {
					setLiteral(opt, rPos, llen+rPos, literalsPrice(llen+rPos))
				}
				lastPos = addMatch(opt, 0, firstMatch, 0, favorDecSpeed)
				immediate := false
				for cur := 1; cur < lastPos; cur++ {
					curPtr := e.ip + cur
					nbS := nbSearches
					if curPtr > mflimit {
						break
					}
					if opt[cur+1].price <= opt[cur].price {
						continue
					}
					if favorDecSpeed && opt[cur+1].price < opt[cur].price+priceScale {
						if lastPos-cur > seqSearchDist {
							continue
						}
						nbS = nbSearchesSeq
					}
					newMatch := c.findLongestMatch(win, curPtr, matchlimit, nbS, usingDictCtx, favorDecSpeed)
					if newMatch.len == 0 {
						continue
					}
					if newMatch.len > sufficientLen || newMatch.len+cur >= optNum {
						lastSeqPos = cur
						lastSeq = newMatch
						immediate = true
						break
					}
					lastPos = addMatch(opt, cur, newMatch, lastPos, favorDecSpeed)
				}
				if !immediate {
					lastSeq.len = int(opt[lastPos].mlen)
					lastSeq.off = int(opt[lastPos].off)
					lastSeqPos = lastPos - lastSeq.len
				}
			}

			// reverse traversal
			{
				pos := lastSeqPos
				mlen := int32(lastSeq.len)
				off := int32(lastSeq.off)
				for pos > 0 {
					prevMlen := opt[pos].mlen
					prevOff := opt[pos].off
					opt[pos].mlen = mlen
					opt[pos].off = off
					mlen = prevMlen
					off = prevOff
					pos -= int(mlen)
				}
				opt[0].mlen = mlen
				opt[0].off = off
			}

			// encode all recorded sequences in order
			for rPos := 0; rPos <= lastSeqPos; {
				ml := int(opt[rPos].mlen)
				offset := int(opt[rPos].off)
				if ml == 1 {
					e.ip++
					rPos++
					continue
				}
				rPos += ml
				if e.encodeSequence(ml, offset, limit, oend) {
					if limit != fillOutput {
						return 0
					}
					e.encodeLastSequenceFillOutput(ml, offset, oend)
					break mainLoop
				}
			}
		}
	}

	if e.encodeLastLiterals(iend, limit, oend) {
		return 0
	}
	*srcSizePtr = e.ip - srcStart
	return e.op
}
