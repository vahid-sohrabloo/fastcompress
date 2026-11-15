package lz4

import "unsafe"

// The fast compressor of lz4.c (lz4 v1.11.1, Copyright (c) Yann Collet,
// BSD 2-Clause): LZ4_compress_generic_validated and its callers.
//
// C works on pointers into the caller's buffers; this port works on
// slices. The input being compressed is win[srcStart:srcStart+inputSize].
// With a prefix (withPrefix64k) the history is win[srcStart-dictSize:srcStart];
// with an external dictionary (usingExtDict) or an attached one
// (usingDictCtx) it is a separate slice. Output is identical to C's for the
// same sequence of calls; see Stream for how C's pointer comparisons map to
// slices.

type tableType uint32

const (
	clearedTable tableType = iota
	byPtr                  // 32-bit C only; never used by this port
	byU32
	byU16
)

type dictDirective int

const (
	noDict dictDirective = iota
	withPrefix64k
	usingExtDict
	usingDictCtx
)

type dictIssue int

const (
	noDictIssue dictIssue = iota
	dictSmall
)

type outputDirective int

const (
	notLimited outputDirective = iota
	limitedOutput
	fillOutput
)

// streamInternal is C's LZ4_stream_t_internal. dictionary is C's pointer:
// only its start (and, for the prefix mode, its capacity) is meaningful;
// dictSize is the length of the history from that start.
type streamInternal struct {
	hashTable     [hashSizeU32]uint32
	dictionary    []byte
	dictCtx       *streamInternal
	currentOffset uint32
	tableType     tableType
	dictSize      uint32
}

// u16 is the hash table seen as byU16 entries (C casts the same memory).
func (c *streamInternal) u16() *[2 * hashSizeU32]uint16 {
	return (*[2 * hashSizeU32]uint16)(unsafe.Pointer(&c.hashTable))
}

// dictRegion returns the history C describes by dictionary and dictSize.
func (c *streamInternal) dictRegion() []byte {
	if c.dictionary == nil {
		return nil
	}
	return c.dictionary[:c.dictSize]
}

// resumeState is C's LZ4_resume_t: the end of a match of an extDict
// compression, which continues in prefix mode.
type resumeState struct {
	ip, op     int
	startIndex uint32
	set        bool
}

// C: LZ4_hash4
func hash4(sequence uint32, tt tableType) uint32 {
	hl := uint32(hashLog)
	if tt == byU16 {
		hl++
	}
	return (sequence * 2654435761) >> (minMatch*8 - hl)
}

// C: LZ4_hash5 (little endian)
func hash5(sequence uint64, tt tableType) uint32 {
	hl := uint32(hashLog)
	if tt == byU16 {
		hl++
	}
	return uint32(((sequence << 24) * 889523592379) >> (64 - hl))
}

// C: LZ4_hashPosition (64-bit: 5 bytes hashed, except for byU16)
func hashPosition(b []byte, p int, tt tableType) uint32 {
	if tt != byU16 {
		return hash5(le64(b[p:]), tt)
	}
	return hash4(le32(b[p:]), tt)
}

func (c *streamInternal) getIndexOnHash(h uint32, tt tableType) uint32 {
	if tt == byU32 {
		return c.hashTable[h]
	}
	return uint32(c.u16()[h])
}

func (c *streamInternal) putIndexOnHash(idx, h uint32, tt tableType) {
	if tt == byU32 {
		c.hashTable[h] = idx
		return
	}
	c.u16()[h] = uint16(idx)
}

func (c *streamInternal) clearHash(h uint32, tt tableType) {
	if tt == byU32 {
		c.hashTable[h] = 0
		return
	}
	c.u16()[h] = 0
}

// clearTableThreshold is C's LZ4_CLEAR_TABLE_THRESHOLD.
const clearTableThreshold = hashTableSize / 16

// C: LZ4_prepareTable
func (c *streamInternal) prepareTable(inputSize int, tt tableType) {
	if c.tableType != clearedTable {
		if c.tableType != tt ||
			(tt == byU16 && c.currentOffset+uint32(inputSize) >= 0xFFFF) ||
			(tt == byU32 && c.currentOffset > 1*gb) ||
			inputSize >= clearTableThreshold {
			c.hashTable = [hashSizeU32]uint32{}
			c.currentOffset = 0
			c.tableType = clearedTable
		}
	}
	if c.currentOffset != 0 && tt == byU32 {
		c.currentOffset += 64 * kb
	}
	c.dictCtx = nil
	c.dictionary = nil
	c.dictSize = 0
}

// C: LZ4_match8
func match8(a []byte, ai int, b []byte, bi int) bool { return le64(a[ai:]) == le64(b[bi:]) }

// putRun writes the literal or match length extension of n (already
// reduced by the token's 15) as C does: 255s, then the remainder.
func putRun(dst []byte, op int, n uint) int {
	for ; n >= 255; n -= 255 {
		dst[op] = 255
		op++
	}
	dst[op] = byte(n)
	return op + 1
}

// compressGenericValidated is C's LZ4_compress_generic_validated with the
// directives as arguments. dst[:maxOutputSize] is the output (for
// notLimited, C presumes LZ4_compressBound room: dst must be that large).
func (c *streamInternal) compressGenericValidated(win []byte, srcStart, inputSize int, inputConsumed *int,
	dst []byte, maxOutputSize int, od outputDirective, tt tableType, dd dictDirective, di dictIssue,
	acceleration int, suspend *resumeState, resume *resumeState) int {

	ip := srcStart
	startIndex := c.currentOffset
	if resume != nil {
		ip = resume.ip
		startIndex = resume.startIndex
	}
	base := srcStart - int(startIndex) // win index of table index 0

	dictCtx := c.dictCtx
	var dictionary []byte
	var dictSize uint32
	var dictDelta uint32
	if dd == usingDictCtx {
		dictionary = dictCtx.dictRegion()
		dictSize = dictCtx.dictSize
		dictDelta = startIndex - dictCtx.currentOffset
	} else {
		dictSize = c.dictSize
		if dd == usingExtDict {
			dictionary = c.dictRegion()
		}
	}
	maybeExtMem := dd == usingExtDict || dd == usingDictCtx
	prefixIdxLimit := startIndex - dictSize
	// dictBase: index into dictionary of table index i is i - dictBaseOff
	var dictBaseOff int
	if dd == usingDictCtx {
		dictBaseOff = int(dictCtx.currentOffset) - int(dictSize)
	} else {
		dictBaseOff = int(startIndex) - int(dictSize)
	}
	anchor := ip
	iend := srcStart + inputSize
	var mflimitPlusOne, matchlimit, seqLimit int

	op := 0
	if resume != nil {
		op = resume.op
	}
	olimit := maxOutputSize
	var offset uint32
	var forwardH uint32
	filledIp := -1

	if od == fillOutput && maxOutputSize < 1 {
		return 0
	}

	// lowLimit: the dictionary's start when the match is in the dictionary
	// (mInDict), otherwise the win index lowLimitWin.
	lowLimitWin := srcStart
	if dd == withPrefix64k && resume == nil {
		lowLimitWin = srcStart - int(dictSize)
	}

	// the current match: in win at mIdx, or (mInDict) in dictionary at mIdx
	var mIdx int
	mInDict := false
	var token int

	if resume == nil {
		if dd == usingDictCtx {
			c.dictCtx = nil
			c.dictSize = uint32(inputSize)
		} else {
			c.dictSize += uint32(inputSize)
		}
		c.currentOffset += uint32(inputSize)
		c.tableType = tt
		if inputSize < minLength {
			goto lastLiterals
		}
	}
	mflimitPlusOne = iend - mfLimit + 1
	matchlimit = iend - lastLiterals
	seqLimit = mflimitPlusOne
	if suspend != nil && inputSize > distanceMax+mfLimit-1 {
		seqLimit = srcStart + distanceMax
	}
	if resume == nil {
		// First Byte
		c.putIndexOnHash(uint32(ip-base), hashPosition(win, ip, tt), tt)
		ip++
		forwardH = hashPosition(win, ip, tt)
	}

	for resuming := resume != nil; ; {
		if resuming {
			resuming = false
			goto resumeLabel
		}
		// Find a match (byU32, byU16)
		{
			forwardIp := ip
			step := 1
			searchMatchNb := acceleration << skipTrigger
			for {
				h := forwardH
				current := uint32(forwardIp - base)
				matchIndex := c.getIndexOnHash(h, tt)
				ip = forwardIp
				forwardIp += step
				step = searchMatchNb >> skipTrigger
				searchMatchNb++

				if forwardIp > mflimitPlusOne {
					goto lastLiterals
				}

				switch dd {
				case usingDictCtx:
					if matchIndex < startIndex {
						matchIndex = dictCtx.hashTable[h]
						mIdx = int(matchIndex) - dictBaseOff
						mInDict = true
						matchIndex += dictDelta
					} else {
						mIdx = base + int(matchIndex)
						mInDict = false
						lowLimitWin = srcStart
					}
				case usingExtDict:
					relIdx := int(matchIndex) - int(startIndex)
					if relIdx < 0 {
						mIdx = int(dictSize) + relIdx
						mInDict = true
					} else {
						mIdx = srcStart + relIdx
						mInDict = false
					}
				default:
					mIdx = base + int(matchIndex)
				}
				forwardH = hashPosition(win, forwardIp, tt)
				c.putIndexOnHash(current, h, tt)

				if di == dictSmall && matchIndex < prefixIdxLimit {
					continue
				}
				if dd != noDict && matchIndex+distanceMax < current {
					continue
				}
				var mv uint32
				if mInDict {
					mv = le32(dictionary[mIdx:])
				} else {
					mv = le32(win[mIdx:])
				}
				if mv == le32(win[ip:]) {
					if dd == noDict && matchIndex+distanceMax < current {
						continue
					}
					if maybeExtMem {
						offset = current - matchIndex
					}
					if dd == usingExtDict && matchIndex >= startIndex {
						lowLimitWin = srcStart
					}
					break
				}
			}
		}

		// Catch up
		filledIp = ip
		if mInDict {
			if mIdx > 0 && win[ip-1] == dictionary[mIdx-1] {
				for {
					ip--
					mIdx--
					if !(ip > anchor && mIdx > 0 && win[ip-1] == dictionary[mIdx-1]) {
						break
					}
				}
			}
		} else if mIdx > lowLimitWin && win[ip-1] == win[mIdx-1] {
			for {
				ip--
				mIdx--
				if !(ip > anchor && mIdx > lowLimitWin && win[ip-1] == win[mIdx-1]) {
					break
				}
			}
		}

		// Encode Literals
		{
			litLength := ip - anchor
			token = op
			op++
			if od == limitedOutput && op+litLength+(2+1+lastLiterals)+litLength/255 > olimit {
				return 0
			}
			if od == fillOutput && op+(litLength+240)/255+litLength+2+1+mfLimit-minMatch > olimit {
				op--
				goto lastLiterals
			}
			if litLength >= runMask {
				dst[token] = runMask << mlBits
				op = putRun(dst, op, uint(litLength-runMask))
			} else {
				dst[token] = byte(litLength << mlBits)
			}
			copy(dst[op:op+litLength], win[anchor:ip])
			op += litLength
		}

	nextMatch:
		if od == fillOutput && op+2+1+mfLimit-minMatch > olimit {
			op = token
			goto lastLiterals
		}

		// Encode Offset
		if maybeExtMem {
			dst[op] = byte(offset)
			dst[op+1] = byte(offset >> 8)
		} else {
			d := ip - mIdx
			dst[op] = byte(d)
			dst[op+1] = byte(d >> 8)
		}
		op += 2

		// Encode MatchLength
		{
			var matchCode int
			if maybeExtMem && mInDict {
				limit := ip + (int(dictSize) - mIdx)
				if limit > matchlimit {
					limit = matchlimit
				}
				matchCode = count2(win, ip+minMatch, dictionary, mIdx+minMatch, limit)
				ip += matchCode + minMatch
				if ip == limit {
					more := count(win, limit, srcStart, matchlimit)
					matchCode += more
					ip += more
				}
			} else {
				matchCode = count(win, ip+minMatch, mIdx+minMatch, matchlimit)
				ip += matchCode + minMatch
			}

			if od != notLimited && op+(1+lastLiterals)+(matchCode+240)/255 > olimit {
				if od == fillOutput {
					newMatchCode := 15 - 1 + (olimit-op-1-lastLiterals)*255
					ip -= matchCode - newMatchCode
					matchCode = newMatchCode
					if ip <= filledIp {
						for p := ip; p <= filledIp; p++ {
							c.clearHash(hashPosition(win, p, tt), tt)
						}
					}
				} else {
					return 0
				}
			}
			if matchCode >= mlMask {
				dst[token] += mlMask
				op = putRun(dst, op, uint(matchCode-mlMask))
			} else {
				dst[token] += byte(matchCode)
			}
		}

		anchor = ip

		// Test end of chunk
		if ip >= seqLimit {
			if suspend == nil || ip >= mflimitPlusOne {
				break
			}
			suspend.ip = ip
			suspend.op = op
			suspend.startIndex = startIndex
			suspend.set = true
			return 0
		}

	resumeLabel:
		// Fill table, and test next position (byU32, byU16)
		{
			h := hashPosition(win, ip, tt)
			current := uint32(ip - base)
			hPrev := hashPosition(win, ip-1, tt)
			prevIndex := c.getIndexOnHash(hPrev, tt)
			var matchIndex uint32
			c.putIndexOnHash(current-1, hPrev, tt)
			if dd == usingDictCtx && prevIndex < startIndex {
				prevIndex = dictCtx.hashTable[hPrev] + dictDelta
			}
			{
				usedTable := dd == noDict && di == dictSmall
				ctxIndex := prevIndex
				if usedTable && prevIndex < startIndex {
					ctxIndex = startIndex
				}
				ctxIndex++
				inDict := (dd == usingExtDict || dd == usingDictCtx) && ctxIndex < startIndex
				ok := true
				if di == dictSmall && ctxIndex < prefixIdxLimit {
					ok = false
				}
				if ok && ctxIndex+distanceMax < current {
					ok = false
				}
				if ok && inDict && ctxIndex+8 > startIndex {
					ok = false
				}
				if ok {
					if inDict {
						ok = match8(dictionary, int(ctxIndex-dictDelta)-dictBaseOff, win, ip)
					} else {
						ok = match8(win, base+int(ctxIndex), win, ip)
					}
				}
				if ok {
					matchIndex = ctxIndex
				} else {
					matchIndex = c.getIndexOnHash(h, tt)
					if dd == usingDictCtx && matchIndex < startIndex {
						matchIndex = dictCtx.hashTable[h] + dictDelta
					}
				}
			}
			switch {
			case dd == usingExtDict:
				relIdx := int(matchIndex) - int(startIndex)
				if relIdx < 0 {
					mIdx = int(dictSize) + relIdx
					mInDict = true
				} else {
					mIdx = srcStart + relIdx
					mInDict = false
				}
			case dd == usingDictCtx && matchIndex < startIndex:
				mIdx = int(matchIndex-dictDelta) - dictBaseOff
				mInDict = true
			default:
				mIdx = base + int(matchIndex)
				mInDict = false
				if dd == usingDictCtx {
					lowLimitWin = srcStart
				}
			}
			c.putIndexOnHash(current, h, tt)
			if (di != dictSmall || matchIndex >= prefixIdxLimit) && matchIndex+distanceMax >= current {
				var mv uint32
				if mInDict {
					mv = le32(dictionary[mIdx:])
				} else {
					mv = le32(win[mIdx:])
				}
				if mv == le32(win[ip:]) {
					token = op
					op++
					dst[token] = 0
					if maybeExtMem {
						offset = current - matchIndex
					}
					if dd == usingExtDict && matchIndex >= startIndex {
						lowLimitWin = srcStart
					}
					goto nextMatch
				}
			}
		}

		// Prepare next loop
		ip++
		forwardH = hashPosition(win, ip, tt)
	}

lastLiterals:
	{
		lastRun := iend - anchor
		if od != notLimited && op+lastRun+1+(lastRun+255-runMask)/255 > olimit {
			if od == fillOutput {
				lastRun = olimit - op - 1
				lastRun -= (lastRun + 256 - runMask) / 256
			} else {
				return 0
			}
		}
		if lastRun >= runMask {
			dst[op] = runMask << mlBits
			op = putRun(dst, op+1, uint(lastRun-runMask))
		} else {
			dst[op] = byte(lastRun << mlBits)
			op++
		}
		copy(dst[op:op+lastRun], win[anchor:anchor+lastRun])
		ip = anchor + lastRun
		op += lastRun
	}
	if od == fillOutput {
		*inputConsumed = ip - srcStart
	}
	return op
}

// compressGeneric is C's LZ4_compress_generic: empty and oversized inputs,
// and the extDict compression that resumes in prefix mode.
func (c *streamInternal) compressGeneric(win []byte, srcStart, srcSize int, inputConsumed *int,
	dst []byte, dstCapacity int, od outputDirective, tt tableType, dd dictDirective, di dictIssue, acceleration int) int {
	if uint32(srcSize) > MaxInputSize || srcSize < 0 {
		return 0
	}
	if srcSize == 0 {
		if od != notLimited && dstCapacity <= 0 {
			return 0
		}
		dst[0] = 0
		if od == fillOutput {
			*inputConsumed = 0
		}
		return 1
	}
	if dd == usingExtDict && di == noDictIssue {
		var state resumeState
		result, ok := 0, false
		if srcStart == 0 && len(win) == srcSize {
			result, ok = c.compressExtDictAsm(win, dst, dstCapacity, od, noDictIssue, acceleration, &state)
		}
		if !ok {
			result = c.compressGenericValidated(win, srcStart, srcSize, inputConsumed, dst, dstCapacity, od,
				tt, usingExtDict, noDictIssue, acceleration, &state, nil)
		}
		if !state.set {
			return result
		}
		capa := dstCapacity
		if od == notLimited {
			capa = CompressBound(srcSize)
		}
		if srcStart == 0 && len(win) == srcSize {
			if n, ok := c.compressResumeAsm(win, dst, capa, &state, acceleration); ok {
				return n
			}
		}
		return c.compressGenericValidated(win, srcStart, srcSize, nil, dst, capa, limitedOutput,
			byU32, withPrefix64k, noDictIssue, acceleration, nil, &state)
	}
	if dd == noDict && od != fillOutput && srcStart == 0 && len(win) == srcSize {
		if n, ok := c.compressNoDictAsm(win, dst, dstCapacity, od, tt, di, acceleration); ok {
			return n
		}
	}
	if dd == usingExtDict && tt == byU32 && srcStart == 0 && len(win) == srcSize {
		if n, ok := c.compressExtDictAsm(win, dst, dstCapacity, od, di, acceleration, nil); ok {
			return n
		}
	}
	if dd == withPrefix64k && tt == byU32 && od != fillOutput && len(win) == srcStart+srcSize {
		if n, ok := c.compressPrefixAsm(win, srcStart, dst, dstCapacity, od, di, acceleration); ok {
			return n
		}
	}
	return c.compressGenericValidated(win, srcStart, srcSize, inputConsumed, dst, dstCapacity, od,
		tt, dd, di, acceleration, nil, nil)
}

// C: LZ4_clampAcceleration
func clampAcceleration(a int) int {
	if a < 1 {
		return AccelerationDefault
	}
	if a > AccelerationMax {
		return AccelerationMax
	}
	return a
}

// C: LZ4_compress_fastReset_generic (64-bit: byU32 beyond the byU16 limit)
func (c *streamInternal) compressFastResetGeneric(src, dst []byte, dstCapacity int, od outputDirective, acceleration int) int {
	srcSize := len(src)
	if srcSize < limit64K {
		c.prepareTable(srcSize, byU16)
		if c.currentOffset != 0 {
			return c.compressGeneric(src, 0, srcSize, nil, dst, dstCapacity, od, byU16, noDict, dictSmall, acceleration)
		}
		return c.compressGeneric(src, 0, srcSize, nil, dst, dstCapacity, od, byU16, noDict, noDictIssue, acceleration)
	}
	c.prepareTable(srcSize, byU32)
	return c.compressGeneric(src, 0, srcSize, nil, dst, dstCapacity, od, byU32, noDict, noDictIssue, acceleration)
}

// C: LZ4_compress_fast_extState_fastReset
func (c *streamInternal) compressFastExtStateFastReset(src, dst []byte, acceleration int) int {
	acceleration = clampAcceleration(acceleration)
	if len(dst) >= CompressBound(len(src)) {
		return c.compressFastResetGeneric(src, dst, 0, notLimited, acceleration)
	}
	return c.compressFastResetGeneric(src, dst, len(dst), limitedOutput, acceleration)
}
