//go:build amd64 && !appengine && !noasm && !purego && gc

package lz4

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useFastAsm selects the assembly main loop of the fast compressor for
// noDict blocks (baseline amd64 instructions only).
var useFastAsm bool

func init() {
	cpu.RegisterAsm("compressFast", &useFastAsm, func() bool { return true })
}

//go:noescape
func compressFast32Asm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast32NoDictAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast32FillAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast16FillAsm(src, dst []byte, table *uint16, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

//go:noescape
func compressFast16Asm(src, dst []byte, table *uint16, startIndex, accel, olimit, lowIdx, clampIdx, prefixLen, ip0, anchor0, op0, resume int) (anchor, op, status int)

// compressNoDictAsm is compressGenericValidated for noDict and the byU32
// or byU16 table, with the main loop in assembly (inputConsumed is set for
// fillOutput). ok is false when the assembly cannot run (then nothing
// changed).
func (c *streamInternal) compressNoDictAsm(src, dst []byte, maxOutputSize int, inputConsumed *int, od outputDirective, tt tableType, di dictIssue, acceleration int) (int, bool) {
	inputSize := len(src)
	if od == fillOutput && (maxOutputSize < 1 || di != noDictIssue) {
		return 0, false
	}
	// noDictIssue: a fresh table (startIndex 0), where the assembly's
	// dictSmall checks are no-ops
	if !useFastAsm || (di == noDictIssue && c.currentOffset != 0) || uint64(c.currentOffset)+uint64(inputSize) >= 1<<31 || len(dst) < maxOutputSize ||
		(od == notLimited && len(dst) < CompressBound(inputSize)) {
		return 0, false
	}
	startIndex := c.currentOffset
	c.dictSize += uint32(inputSize)
	c.currentOffset += uint32(inputSize)
	c.tableType = tt
	anchor, op := 0, 0
	if inputSize >= minLength {
		c.putIndexOnHash(startIndex, hashPosition(src, 0, tt), tt)
		olimit := maxOutputSize
		if od == notLimited {
			olimit = -1
		}
		var st int
		si := int(startIndex)
		switch {
		case od == fillOutput && tt == byU32:
			anchor, op, st = compressFast32FillAsm(src, dst, &c.hashTable[0], si, acceleration, olimit, si, si, 0, 1, 0, 0, 0)
		case od == fillOutput:
			anchor, op, st = compressFast16FillAsm(src, dst, &c.u16()[0], si, acceleration, olimit, si, si, 0, 1, 0, 0, 0)
		case tt == byU32:
			anchor, op, st = compressFast32NoDictAsm(src, dst, &c.hashTable[0], si, acceleration, olimit, si, si, 0, 1, 0, 0, 0)
		default:
			anchor, op, st = compressFast16Asm(src, dst, &c.u16()[0], si, acceleration, olimit, si, si, 0, 1, 0, 0, 0)
		}
		if st != 0 {
			return 0, true
		}
	}
	if od == fillOutput {
		return fillLastLiterals(src, dst, anchor, op, maxOutputSize, inputConsumed), true
	}
	return lastLiteralsAsm(src, dst, anchor, op, maxOutputSize, od)
}

// fillLastLiterals is _last_literals for fillOutput: as many literals as
// fit; *inputConsumed is the input up to their end.
func fillLastLiterals(src, dst []byte, anchor, op, olimit int, inputConsumed *int) int {
	lastRun := len(src) - anchor
	if op+lastRun+1+(lastRun+255-runMask)/255 > olimit {
		lastRun = olimit - op - 1
		lastRun -= (lastRun + 256 - runMask) / 256
	}
	if lastRun >= runMask {
		dst[op] = runMask << mlBits
		op = putRun(dst, op+1, uint(lastRun-runMask))
	} else {
		dst[op] = byte(lastRun << mlBits)
		op++
	}
	copy(dst[op:op+lastRun], src[anchor:anchor+lastRun])
	*inputConsumed = anchor + lastRun
	return op + lastRun
}

// compressPrefixAsm is compressGenericValidated for withPrefix64k (the
// history is win[:srcStart]), byU32, notLimited or limitedOutput, without
// suspension, with the main loop in assembly. ok is false when the assembly
// cannot run (then nothing changed).
func (c *streamInternal) compressPrefixAsm(win []byte, srcStart int, dst []byte, maxOutputSize int, od outputDirective, di dictIssue, acceleration int) (int, bool) {
	src := win[srcStart:]
	inputSize := len(src)
	dictSize := c.dictSize
	if !useFastAsm || int(dictSize) > srcStart || uint64(c.currentOffset)+uint64(inputSize) >= 1<<31 ||
		len(dst) < maxOutputSize || (od == notLimited && len(dst) < CompressBound(inputSize)) ||
		(di == noDictIssue && dictSize < 64*kb && dictSize < c.currentOffset) {
		return 0, false
	}
	startIndex := c.currentOffset
	c.dictSize += uint32(inputSize)
	c.currentOffset += uint32(inputSize)
	c.tableType = byU32
	anchor, op := 0, 0
	if inputSize >= minLength {
		c.putIndexOnHash(startIndex, hashPosition(src, 0, byU32), byU32)
		olimit := maxOutputSize
		if od == notLimited {
			olimit = -1
		}
		lowIdx := 0
		if di == dictSmall {
			lowIdx = int(startIndex - dictSize)
		}
		var st int
		anchor, op, st = compressFast32Asm(src, dst, &c.hashTable[0], int(startIndex), acceleration, olimit, lowIdx, 0, int(dictSize), 1, 0, 0, 0)
		if st != 0 {
			return 0, true
		}
	}
	return lastLiteralsAsm(src, dst, anchor, op, maxOutputSize, od)
}

//go:noescape
func compressFastExtAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx int, dict []byte, seqLimit int) (anchor, op, status int)

// compressExtDictAsm is compressGenericValidated for usingExtDict, byU32 and
// limitedOutput with the main loop in assembly, suspending (suspend != nil,
// noDictIssue) where C does. ok is false when the assembly cannot run (then
// nothing changed).
func (c *streamInternal) compressExtDictAsm(src, dst []byte, dstCapacity int, od outputDirective, di dictIssue,
	acceleration int, suspend *resumeState) (int, bool) {
	inputSize := len(src)
	dict := c.dictRegion()
	if !useFastAsm || od != limitedOutput || inputSize < minLength || len(dst) < dstCapacity || len(dict) == 0 ||
		uint64(c.currentOffset)+uint64(inputSize) >= 1<<31 || c.dictSize > c.currentOffset ||
		(di == noDictIssue && c.dictSize < 64*kb && c.dictSize < c.currentOffset) {
		return 0, false
	}
	dictSize := c.dictSize
	startIndex := c.currentOffset
	c.dictSize += uint32(inputSize)
	c.currentOffset += uint32(inputSize)
	c.tableType = byU32
	c.putIndexOnHash(startIndex, hashPosition(src, 0, byU32), byU32)
	lowIdx := 0
	if di == dictSmall {
		lowIdx = int(startIndex - dictSize)
	}
	seqLimit := inputSize - mfLimit + 1
	if suspend != nil && inputSize > distanceMax+mfLimit-1 {
		seqLimit = distanceMax
	}
	anchor, op, st := compressFastExtAsm(src, dst, &c.hashTable[0], int(startIndex), acceleration, dstCapacity, lowIdx, dict, seqLimit)
	switch st {
	case 0:
		return lastLiteralsAsm(src, dst, anchor, op, dstCapacity, limitedOutput)
	case 3:
		suspend.ip, suspend.op, suspend.startIndex, suspend.set = anchor, op, startIndex, true
	}
	return 0, true
}

//go:noescape
func compressFastDictCtxAsm(src, dst []byte, table *uint32, startIndex, accel, olimit, lowIdx int, dict []byte, seqLimit int, dctx *uint32, dictDelta int) (anchor, op, status int)

// compressDictCtxAsm is compressGenericValidated for usingDictCtx
// (noDictIssue), byU32 and limitedOutput with the main loop in assembly. ok
// is false when the assembly cannot run (then nothing changed).
func (c *streamInternal) compressDictCtxAsm(src, dst []byte, dstCapacity int, od outputDirective, di dictIssue, acceleration int) (int, bool) {
	inputSize := len(src)
	dictCtx := c.dictCtx
	if !useFastAsm || dictCtx == nil || od != limitedOutput || di != noDictIssue || inputSize < minLength || len(dst) < dstCapacity ||
		dictCtx.tableType != byU32 || uint64(c.currentOffset)+uint64(inputSize) >= 1<<31 {
		return 0, false
	}
	dict := dictCtx.dictRegion()
	if len(dict) == 0 {
		return 0, false
	}
	startIndex := c.currentOffset
	dictDelta := startIndex - dictCtx.currentOffset
	c.dictCtx = nil
	c.dictSize = uint32(inputSize)
	c.currentOffset += uint32(inputSize)
	c.tableType = byU32
	c.putIndexOnHash(startIndex, hashPosition(src, 0, byU32), byU32)
	anchor, op, st := compressFastDictCtxAsm(src, dst, &c.hashTable[0], int(startIndex), acceleration, dstCapacity, 0, dict,
		inputSize-mfLimit+1, &dictCtx.hashTable[0], int(dictDelta))
	if st != 0 {
		return 0, true
	}
	return lastLiteralsAsm(src, dst, anchor, op, dstCapacity, limitedOutput)
}

// compressResumeAsm is C's LZ4_compress_resumeWithPrefix (the second part
// of an extDict compression, suspended at st) with the main loop in
// assembly: src is the whole input, the history is out of reach. ok is false
// when the assembly cannot run (then nothing changed).
func (c *streamInternal) compressResumeAsm(src, dst []byte, dstCapacity int, st *resumeState, acceleration int) (int, bool) {
	if !useFastAsm || len(src) < minLength || st.ip >= len(src)-11 || st.op > dstCapacity || len(dst) < dstCapacity ||
		uint64(st.startIndex)+uint64(len(src)) >= 1<<31 {
		return 0, false
	}
	anchor, op, s := compressFast32Asm(src, dst, &c.hashTable[0], int(st.startIndex), acceleration, dstCapacity,
		0, 0, 0, st.ip, st.ip, st.op, 1)
	if s != 0 {
		return 0, true
	}
	return lastLiteralsAsm(src, dst, anchor, op, dstCapacity, limitedOutput)
}

// lastLiteralsAsm is the end of compressGenericValidated (_last_literals)
// after the assembly main loop.
func lastLiteralsAsm(src, dst []byte, anchor, op, maxOutputSize int, od outputDirective) (int, bool) {
	lastRun := len(src) - anchor
	if od != notLimited && op+lastRun+1+(lastRun+255-runMask)/255 > maxOutputSize {
		return 0, true
	}
	if lastRun >= runMask {
		dst[op] = runMask << mlBits
		op = putRun(dst, op+1, uint(lastRun-runMask))
	} else {
		dst[op] = byte(lastRun << mlBits)
		op++
	}
	copy(dst[op:op+lastRun], src[anchor:])
	return op + lastRun, true
}
