package zstd

import (
	"encoding/binary"
	"math"
	"math/bits"
	"sync/atomic"

	"github.com/vahid-sohrabloo/fastcompress/internal/huf"
)

// Frame-level decoding. C: lib/decompress/zstd_decompress.c (v1.5.7),
// format ZSTD_f_zstd1 only (no magicless frames, no legacy formats).

// Decoder-side format constants. C: zstd.h, zstd_internal.h.
const (
	frameIDSize         = 4               // C: ZSTD_FRAMEIDSIZE
	magicSkippableMask  = 0xFFFFFFF0      // C: ZSTD_MAGIC_SKIPPABLE_MASK
	contentSizeError    = ^uint64(0) - 1  // C: ZSTD_CONTENTSIZE_ERROR
	startingInputLength = frameIDSize + 1 // C: ZSTD_startingInputLength(ZSTD_f_zstd1)
	btReserved          = 3               // C: zstd_internal.h:bt_reserved
)

// C: zstd_internal.h:ZSTD_did_fieldSize, ZSTD_fcs_fieldSize
var (
	didFieldSize = [4]int{0, 1, 2, 4}
	fcsFieldSize = [4]int{0, 2, 4, 8}
)

// frameType. C: zstd.h:ZSTD_FrameType_e
type frameType uint8

const (
	zstdFrame          frameType = iota // C: ZSTD_frame
	zstdSkippableFrame                  // C: ZSTD_skippableFrame
)

// frameHeader mirrors C's ZSTD_FrameHeader. For a skippable frame,
// frameContentSize is the payload size and dictID the magic variant (0-15).
type frameHeader struct {
	frameContentSize uint64 // contentSizeUnknown when the field is absent
	windowSize       uint64
	blockSizeMax     uint32
	frameType        frameType
	headerSize       uint32
	dictID           uint32
	checksumFlag     bool
	// singleSegment is the Single_Segment_flag.
	// C: ZSTD_FrameHeader has no such field — Header.SingleSegment needs
	// it, and windowSize == frameContentSize does not imply it.
	singleSegment bool
}

// isSkippableFrame. C: zstd_decompress.c:ZSTD_isSkippableFrame
func isSkippableFrame(src []byte) bool {
	return len(src) >= frameIDSize && binary.LittleEndian.Uint32(src)&magicSkippableMask == magicSkippableStart
}

// frameHeaderSizeInternal returns the size of the frame header starting
// src. C: zstd_decompress.c:ZSTD_frameHeaderSize_internal
func frameHeaderSizeInternal(src []byte) (int, dErr) {
	if len(src) < startingInputLength {
		return 0, dErrSrcSizeWrong
	}
	fhd := src[startingInputLength-1]
	dictID := fhd & 3
	singleSegment := (fhd >> 5) & 1
	fcsID := fhd >> 6
	n := startingInputLength + int(singleSegment^1) + didFieldSize[dictID] + fcsFieldSize[fcsID]
	if singleSegment == 1 && fcsID == 0 {
		n++
	}
	return n, 0
}

// getFrameHeaderAdvanced decodes the frame header at the start of src into
// zfh. need > 0 means src is too short and need bytes are wanted (zfh is
// then not filled). C: zstd_decompress.c:ZSTD_getFrameHeader_advanced
// (format ZSTD_f_zstd1)
func getFrameHeaderAdvanced(zfh *frameHeader, src []byte) (need int, err dErr) {
	if len(src) < startingInputLength {
		if len(src) > 0 {
			// Fewer than startingInputLength bytes: check they at least
			// begin a supported magic number, to error out early.
			var hbuf [4]byte
			binary.LittleEndian.PutUint32(hbuf[:], magicNumber)
			copy(hbuf[:], src)
			if binary.LittleEndian.Uint32(hbuf[:]) != magicNumber {
				binary.LittleEndian.PutUint32(hbuf[:], magicSkippableStart)
				copy(hbuf[:], src)
				if binary.LittleEndian.Uint32(hbuf[:])&magicSkippableMask != magicSkippableStart {
					return 0, dErrPrefixUnknown
				}
			}
		}
		return startingInputLength, 0
	}

	*zfh = frameHeader{}
	if magic := binary.LittleEndian.Uint32(src); magic != magicNumber {
		if magic&magicSkippableMask == magicSkippableStart {
			if len(src) < skippableHeaderSize {
				return skippableHeaderSize, 0 // magic number + frame length
			}
			zfh.frameType = zstdSkippableFrame
			zfh.dictID = magic - magicSkippableStart
			zfh.headerSize = skippableHeaderSize
			zfh.frameContentSize = uint64(binary.LittleEndian.Uint32(src[frameIDSize:]))
			return 0, 0
		}
		return 0, dErrPrefixUnknown
	}

	// ensure there is enough input to fully read the frame header
	fhsize, _ := frameHeaderSizeInternal(src)
	if len(src) < fhsize {
		return fhsize, 0
	}
	zfh.headerSize = uint32(fhsize)

	fhdByte := src[startingInputLength-1]
	pos := startingInputLength
	dictIDSizeCode := fhdByte & 3
	checksumFlag := (fhdByte>>2)&1 != 0
	singleSegment := (fhdByte>>5)&1 != 0
	fcsID := fhdByte >> 6
	var windowSize uint64
	var dictID uint32
	frameContentSize := contentSizeUnknown
	if fhdByte&0x08 != 0 {
		return 0, dErrFrameParameterUnsupported // reserved bits, must be zero
	}
	if !singleSegment {
		wlByte := src[pos]
		pos++
		windowLog := uint32(wlByte>>3) + windowLogAbsoluteMin
		// C: ZSTD_WINDOWLOG_MAX is 30 on 32-bit builds; this port uses 31 on
		// every GOARCH. On 386 such a window (up to ~4.0e9) overflows int:
		// callers cap it by MaxWindow/MaxMemory before converting.
		if windowLog > windowLogMax {
			return 0, dErrFrameParameterWindowTooLarge
		}
		windowSize = 1 << windowLog
		windowSize += (windowSize >> 3) * uint64(wlByte&7)
	}
	switch dictIDSizeCode {
	case 1:
		dictID = uint32(src[pos])
		pos++
	case 2:
		dictID = uint32(binary.LittleEndian.Uint16(src[pos:]))
		pos += 2
	case 3:
		dictID = binary.LittleEndian.Uint32(src[pos:])
		pos += 4
	}
	switch fcsID {
	case 0:
		if singleSegment {
			frameContentSize = uint64(src[pos])
		}
	case 1:
		frameContentSize = uint64(binary.LittleEndian.Uint16(src[pos:])) + 256
	case 2:
		frameContentSize = uint64(binary.LittleEndian.Uint32(src[pos:]))
	case 3:
		frameContentSize = binary.LittleEndian.Uint64(src[pos:])
	}
	if singleSegment {
		windowSize = frameContentSize
	}

	zfh.frameType = zstdFrame
	zfh.frameContentSize = frameContentSize
	zfh.windowSize = windowSize
	zfh.blockSizeMax = uint32(min(windowSize, blockSizeMax))
	zfh.dictID = dictID
	zfh.checksumFlag = checksumFlag
	zfh.singleSegment = singleSegment
	return 0, 0
}

// readSkippableFrameSize returns the size of the skippable frame starting
// src, header included. C: zstd_decompress.c:readSkippableFrameSize
func readSkippableFrameSize(src []byte) (int, dErr) {
	if len(src) < skippableHeaderSize {
		return 0, dErrSrcSizeWrong
	}
	sizeU32 := binary.LittleEndian.Uint32(src[frameIDSize:])
	if sizeU32+skippableHeaderSize < sizeU32 {
		return 0, dErrFrameParameterUnsupported
	}
	skippableSize := skippableHeaderSize + uint64(sizeU32)
	if skippableSize > uint64(len(src)) {
		return 0, dErrSrcSizeWrong
	}
	return int(skippableSize), 0
}

// readSkippableFrame copies the payload of the skippable frame starting
// src into dst and returns its size and the frame's magic variant.
// C: zstd_decompress.c:ZSTD_readSkippableFrame
func readSkippableFrame(dst, src []byte) (n int, magicVariant uint32, err dErr) {
	if len(src) < skippableHeaderSize {
		return 0, 0, dErrSrcSizeWrong
	}
	magic := binary.LittleEndian.Uint32(src)
	skippableFrameSize, err := readSkippableFrameSize(src)
	if !isSkippableFrame(src) {
		return 0, 0, dErrFrameParameterUnsupported
	}
	if err != 0 {
		// C: readSkippableFrameSize's error code, read as a size, fails
		// the "skippableFrameSize > srcSize" check: srcSize_wrong for
		// every error.
		return 0, 0, dErrSrcSizeWrong
	}
	contentSize := skippableFrameSize - skippableHeaderSize
	if contentSize > len(dst) {
		return 0, 0, dErrDstSizeTooSmall
	}
	copy(dst, src[skippableHeaderSize:skippableFrameSize])
	return contentSize, magic - magicSkippableStart, 0
}

// frameSizeInfo. C: zstd_internal.h:ZSTD_frameSizeInfo; err replaces C's
// error code in compressedSize (decompressedBound is then contentSizeError).
// On an error in the block walk, nbBlocks is the number of whole blocks
// before it (C leaves it unset); decompressMultiFrame sizes its output
// from it.
type frameSizeInfo struct {
	nbBlocks          int
	compressedSize    int
	decompressedBound uint64
	err               dErr
}

func errorFrameSizeInfo(err dErr) frameSizeInfo {
	return frameSizeInfo{decompressedBound: contentSizeError, err: err}
}

// findFrameSizeInfo walks the block headers of the frame starting src.
// C: zstd_decompress.c:ZSTD_findFrameSizeInfo (format ZSTD_f_zstd1)
func findFrameSizeInfo(src []byte) frameSizeInfo {
	if len(src) >= skippableHeaderSize && isSkippableFrame(src) {
		n, err := readSkippableFrameSize(src)
		if err != 0 {
			return frameSizeInfo{err: err} // C: decompressedBound stays 0 here
		}
		return frameSizeInfo{compressedSize: n}
	}

	var zfh frameHeader
	need, err := getFrameHeaderAdvanced(&zfh, src)
	if err != 0 {
		return errorFrameSizeInfo(err)
	}
	if need > 0 {
		return errorFrameSizeInfo(dErrSrcSizeWrong)
	}
	ip := int(zfh.headerSize)
	nbBlocks := 0
	walkError := func(err dErr) frameSizeInfo {
		fsi := errorFrameSizeInfo(err)
		fsi.nbBlocks = nbBlocks
		return fsi
	}
	for {
		cBlockSize, bp, err := getcBlockSize(src[ip:])
		if err != 0 {
			return walkError(err)
		}
		if blockHeaderSize+cBlockSize > len(src)-ip {
			return walkError(dErrSrcSizeWrong)
		}
		ip += blockHeaderSize + cBlockSize
		nbBlocks++
		if bp.lastBlock {
			break
		}
	}
	// Final frame content checksum
	if zfh.checksumFlag {
		if len(src)-ip < 4 {
			return walkError(dErrSrcSizeWrong)
		}
		ip += 4
	}
	fsi := frameSizeInfo{nbBlocks: nbBlocks, compressedSize: ip, decompressedBound: zfh.frameContentSize}
	if zfh.frameContentSize == contentSizeUnknown {
		fsi.decompressedBound = uint64(nbBlocks) * uint64(zfh.blockSizeMax)
	}
	return fsi
}

// decompressBound returns the most the frames in src can decompress to, or
// contentSizeError. C: zstd_decompress.c:ZSTD_decompressBound
func decompressBound(src []byte) uint64 {
	var bound uint64
	for len(src) > 0 {
		fsi := findFrameSizeInfo(src)
		if fsi.err != 0 || fsi.decompressedBound == contentSizeError {
			return contentSizeError
		}
		src = src[fsi.compressedSize:]
		bound += fsi.decompressedBound
	}
	return bound
}

// blockProperties. C: zstd_decompress_internal.h:blockProperties_t
type blockProperties struct {
	blockType int // btRaw, btRLE, btCompressed or btReserved
	lastBlock bool
	origSize  uint32 // only useful for RLE
}

// getcBlockSize decodes the block header at the start of src and returns
// the size of the block's content in src (1 for RLE).
// C: zstd_decompress_block.c:ZSTD_getcBlockSize
func getcBlockSize(src []byte) (cSize int, bp blockProperties, err dErr) {
	if len(src) < blockHeaderSize {
		return 0, bp, dErrSrcSizeWrong
	}
	cBlockHeader := uint32(src[0]) | uint32(src[1])<<8 | uint32(src[2])<<16
	cSize = int(cBlockHeader >> 3)
	bp.lastBlock = cBlockHeader&1 != 0
	bp.blockType = int((cBlockHeader >> 1) & 3)
	bp.origSize = uint32(cSize)
	switch bp.blockType {
	case btRLE:
		return 1, bp, 0
	case btReserved:
		return 0, bp, dErrReservedBlockType
	}
	return cSize, bp, 0
}

// decompressBegin resets d for a new frame.
// C: zstd_decompress.c:ZSTD_decompressBegin
func (d *dctx) decompressBegin() {
	d.expected = startingInputLength
	d.stage = dsGetFrameHeaderSize
	d.decodedSize = 0 // C also resets processedCSize, which only tracing reads
	d.previousDstEnd = 0
	d.prefixStart = 0
	d.virtualStart = 0
	d.extDict = nil
	d.dictEnd = nil
	d.prevDst = nil
	d.entropy.hufTable.Init(hufDTableCapacityLog) // C: hufTable[0] = ZSTD_HUFFDTABLE_CAPACITY_LOG * 0x1000001
	d.litEntropy, d.fseEntropy = false, false
	d.dictID = 0
	d.bType = btReserved
	d.isFrameDecompression = true
	d.entropy.rep = repStartValue // initial repcodes
	d.llTptr = d.entropy.llTable[:]
	d.mlTptr = d.entropy.mlTable[:]
	d.ofTptr = d.entropy.ofTable[:]
	d.hufPtr = &d.entropy.hufTable
}

// Decoder parameter defaults (no C counterparts).
const (
	// defaultMaxWindowSize is WithDecoderMaxWindow's default. C's
	// counterpart is ZSTD_MAXWINDOWSIZE_DEFAULT ((1<<27)+1); the doc text
	// sets MaxWindowSize (1<<29).
	defaultMaxWindowSize = MaxWindowSize
	// maxMemoryCap caps the memory limit in effect (Decoder.memLimit):
	// 1 GiB on 32-bit platforms, half of the int range, so that hostile
	// input cannot make DecodeAll reserve more than the address space can
	// give (a fatal runtime error, not an error value: a trusted frame
	// content size is reserved up front, see decompressFrames). On 64-bit
	// platforms it is 2^62 bytes, no cap in practice.
	maxMemoryCap = math.MaxInt/2 + 1
	// defaultMaxMemory is WithDecoderMaxMemory's default: 64 GiB as the
	// doc text says, 1 GiB (maxMemoryCap) on 32-bit platforms.
	defaultMaxMemory = min(64<<30, maxMemoryCap)
	// frameHeaderSizeMin. C: ZSTD_FRAMEHEADERSIZE_MIN(ZSTD_f_zstd1)
	frameHeaderSizeMin = 6
	// frameTrustMin and frameTrustRatio bound the output space
	// decompressMultiFrame reserves before decoding a frame (Go only).
	frameTrustMin   = 1 << 20
	frameTrustRatio = 64
	frameGuessRatio = 4
)

// newDCtx returns a decompression context with default parameters.
// C: zstd_decompress.c:ZSTD_createDCtx (ZSTD_initDCtx_internal)
func newDCtx() *dctx {
	d := new(dctx)
	d.reset(false)
	return d
}

// reset resets the session (and, unless sessionOnly, the parameters).
// C: zstd_decompress.c:ZSTD_DCtx_reset (ZSTD_reset_session_only, or
// ZSTD_reset_session_and_parameters)
func (d *dctx) reset(sessionOnly bool) {
	d.streamStage = zdssInit
	d.noForwardProgress = 0
	d.isFrameDecompression = true
	if !sessionOnly {
		d.ddict = nil  // C: ZSTD_clearDict
		d.ddicts = nil // C: refMultipleDDicts = ZSTD_rmd_refSingleDDict
		// C: ZSTD_DCtx_resetParameters
		d.maxWindowSize = defaultMaxWindowSize
		d.forceIgnoreChecksum = false
		d.maxMemory = defaultMaxMemory
	}
}

// setMaxWindow sets the largest window a frame may declare, in bytes.
// C: ZSTD_DCtx_setMaxWindowSize (ZSTD_d_windowLogMax at byte granularity);
// the option layer checks the bounds.
func (d *dctx) setMaxWindow(n uint64) { d.maxWindowSize = n }

// setIgnoreChecksum. C: ZSTD_DCtx_setParameter(ZSTD_d_forceIgnoreChecksum)
func (d *dctx) setIgnoreChecksum(b bool) { d.forceIgnoreChecksum = b }

// decodeFrameHeader decodes the frame header src (exactly headerSize
// bytes) into d.fParams. C: zstd_decompress.c:ZSTD_decodeFrameHeader
func (d *dctx) decodeFrameHeader(src []byte) dErr {
	need, e := getFrameHeaderAdvanced(&d.fParams, src)
	if e != 0 {
		return e // invalid header
	}
	if need > 0 {
		return dErrSrcSizeWrong // headerSize too small
	}
	// C selects the frame's DDict here (ZSTD_DCtx_selectFrameDDict) when
	// referencing multiple DDicts; decompressFrames does that before
	// beginning the frame.
	if d.fParams.dictID != 0 && d.dictID != d.fParams.dictID {
		return dErrDictionaryWrong
	}
	d.validateChecksum = d.fParams.checksumFlag && !d.forceIgnoreChecksum
	if d.validateChecksum {
		d.xxhState.Reset()
	}
	return 0
}

// copyRawBlock. C: zstd_decompress.c:ZSTD_copyRawBlock
func copyRawBlock(dst, src []byte) (int, dErr) {
	if len(src) > len(dst) {
		return 0, dErrBlockDstSizeTooSmall
	}
	copy(dst, src)
	return len(src), 0
}

// setRLEBlock. C: zstd_decompress.c:ZSTD_setRleBlock
func setRLEBlock(dst []byte, b byte, regenSize int) (int, dErr) {
	if regenSize > len(dst) {
		return 0, dErrBlockDstSizeTooSmall
	}
	memset(dst[:regenSize], b)
	return regenSize, 0
}

// decompressFrame decodes the frame starting src into dst[:dstCapacity],
// the frame's output buffer from its start (as given to checkContinuity;
// len(dst) is ignored), and returns the output and the bytes of src
// consumed. dstCapacity is C's output space; the buffer itself may be
// shorter (cap(dst) < dstCapacity): decompressFrame then grows it before
// each block that needs more room, so the memory held follows the output,
// not the space a header or a run of empty blocks claims. On an error, the
// output holds the blocks decoded before it.
// C: zstd_decompress.c:ZSTD_decompressFrame
func (d *dctx) decompressFrame(dst []byte, dstCapacity int, src []byte) (out []byte, consumed int, err dErr) {
	out = dst[:0]
	oend := dstCapacity
	op, ip := 0, 0
	remainingSrcSize := len(src)

	// Go: makes room for min(n, oend-op) bytes at op. The offsets C keeps
	// as pointers stay valid when the buffer moves; prevDst follows it.
	reserve := func(n int) dErr {
		n = min(n, oend-op)
		if cap(out)-op < n {
			d.tryTight = false // the buffer is ours from now on
			// grow the capacity by at least cap(out) (no overflow on 386),
			// so honest frames copy their output O(1) times on average;
			// exactly, not with append's rounding (review M7), and to the
			// whole space when that is at most 25% more
			c := cap(out) + min(max(n, cap(out)), oend-cap(out))
			if c >= oend-oend/5 {
				c = oend
			}
			nb, e := d.growBuffer(out[:op], c-op)
			if e != 0 {
				return e
			}
			out = nb[:0:min(cap(nb), oend)]
			d.prevDst = out[:cap(out)]
		}
		return 0
	}

	// check
	if remainingSrcSize < frameHeaderSizeMin+blockHeaderSize {
		return out, 0, dErrSrcSizeWrong
	}

	// Frame Header
	frameHeaderSize, e := frameHeaderSizeInternal(src[:startingInputLength])
	if e != 0 {
		return out, 0, e
	}
	if remainingSrcSize < frameHeaderSize+blockHeaderSize {
		return out, 0, dErrSrcSizeWrong
	}
	if e := d.decodeFrameHeader(src[:frameHeaderSize]); e != 0 {
		return out, 0, e
	}
	ip += frameHeaderSize
	remainingSrcSize -= frameHeaderSize

	// Go: C's one-shot path does not check the window. DecodeAll applies
	// the streaming check (ZSTD_decompressStream, after
	// ZSTD_decodeFrameHeader) too: a deliberate deviation, see the error
	// mapping table.
	if max(d.fParams.windowSize, 1<<windowLogAbsoluteMin) > d.maxWindowSize {
		return out, ip, dErrFrameParameterWindowTooLarge
	}

	// C shrinks blockSizeMax to maxBlockSizeParam here; no option sets it.
	bsMax := int(d.fParams.blockSizeMax)

	// Loop on each block
	for {
		cBlockSize, bp, e := getcBlockSize(src[ip:])
		if e != 0 {
			return out[:op], ip, e
		}
		ip += blockHeaderSize
		remainingSrcSize -= blockHeaderSize
		if cBlockSize > remainingSrcSize {
			return out[:op], ip, dErrSrcSizeWrong
		}
		// C limits the output to ip when decompressing in place (src
		// inside dst); Go's src and dst are separate slices here.
		block := src[ip : ip+cBlockSize]

		var decodedSize int
		switch bp.blockType {
		case btCompressed:
			// Go: room for a whole block and for literals placed after it
			// (ZSTD_allocateLiteralsBuffer's in-dst test), so the
			// placement and the checks are those of the full output space.
			room := 2*bsMax + 2*wildcopyOverlength + 1
			if d.tryTight && cap(out)-op < min(room, oend-op) {
				// Go: the caller's spare capacity has less room left than
				// the full output space gives the block. Decode it in the
				// room left; on any error (the block not fitting among
				// them), restore the state the block started from and
				// decode it as below. A valid block regenerates the same
				// bytes wherever its literals are placed, and an invalid
				// one gets the full-room result, so the result never
				// depends on dst's capacity; at most one block per frame
				// is decoded twice (growth ends tryTight).
				d.saveBlockState()
				decodedSize, e = d.decompressBlockInternal(out[:op], cap(out)-op, block, notStreaming)
				// Over bsMax is not accepted here, so that such a block
				// gets the full room's result (the check below names it
				// the same way today; the guard keeps the tight path from
				// deciding an invalid block's error on its own).
				if e == 0 && decodedSize <= bsMax {
					break
				}
				d.restoreBlockState()
			}
			if e := reserve(room); e != 0 {
				return out[:op], ip, e
			}
			// Go: at most room, not all the buffer has: how far it has
			// grown depends on dst's capacity, and a block regenerating
			// more than room would fail on a check that depends on it
			// (run out of space, or go on to another check).
			dstCap := min(oend, cap(out), op+room) - op
			decodedSize, e = d.decompressBlockInternal(out[:op], dstCap, block, notStreaming)
			if e.cCode() == int(dErrDstSizeTooSmall) && dstCap > bsMax {
				// More than dstCap > blockSizeMax bytes: the check below
				// would have rejected the block. Go: named so whether the
				// room was the buffer's or the output space's, so that the
				// error does not depend on dst's capacity (which sets the
				// buffer); only a block short of a space of at most
				// blockSizeMax left is the space's error, as in streams.
				e = dErrDecodedBlockTooLarge
			}
		case btRaw:
			if e := reserve(cBlockSize); e != 0 {
				return out[:op], ip, e
			}
			decodedSize, e = copyRawBlock(out[op:min(oend, cap(out))], block)
		case btRLE:
			if e := reserve(int(bp.origSize)); e != 0 {
				return out[:op], ip, e
			}
			decodedSize, e = setRLEBlock(out[op:min(oend, cap(out))], block[0], int(bp.origSize))
		default: // btReserved: getcBlockSize has rejected it
			return out[:op], ip, dErrCorruptionDetected
		}
		if e != 0 {
			return out[:op], ip, e
		}
		// Go: streaming C's check (ZSTD_decompressContinue), which one-shot
		// C does not make: a block regenerating more than blockSizeMax.
		// DecodeAll and streaming then agree (a deliberate deviation, see
		// the error mapping table).
		if decodedSize > bsMax {
			return out[:op], ip, dErrDecodedBlockTooLarge
		}
		out = out[:op+decodedSize]
		if d.validateChecksum {
			d.xxhState.Write(out[op : op+decodedSize])
		}
		op += decodedSize
		ip += cBlockSize
		remainingSrcSize -= cBlockSize
		if bp.lastBlock {
			break
		}
	}

	if d.fParams.frameContentSize != contentSizeUnknown && uint64(op) != d.fParams.frameContentSize {
		return out, ip, dErrFrameSizeMismatch
	}
	if d.fParams.checksumFlag { // Frame content checksum verification
		if remainingSrcSize < 4 {
			// Go: C returns checksum_wrong here; streaming C waits for
			// more input. DecodeAll reports the cut input like streaming
			// (io.ErrUnexpectedEOF): a deliberate deviation.
			return out, ip, dErrSrcSizeWrong
		}
		if !d.forceIgnoreChecksum {
			checkCalc := uint32(d.xxhState.Sum64())
			checkRead := binary.LittleEndian.Uint32(src[ip:])
			if checkRead != checkCalc {
				return out, ip, dErrChecksumWrong
			}
		}
		ip += 4
	}
	return out, ip, 0
}

// dBlockState is the state a compressed block can change that the next
// block reads: the entropy tables and repcodes, the table pointers and the
// entropy flags (decompressFrame's tight decode, no C counterpart).
type dBlockState struct {
	entropy                             entropyDTables
	llTptr, mlTptr, ofTptr              []seqSymbol
	hufPtr                              *huf.DTable
	litEntropy, fseEntropy, ddictIsCold bool
}

// saveBlockState saves the block state in d.blockSnap (made once).
func (d *dctx) saveBlockState() {
	if d.blockSnap == nil {
		d.blockSnap = new(dBlockState)
	}
	b := d.blockSnap
	b.entropy.llTable, b.entropy.ofTable, b.entropy.mlTable = d.entropy.llTable, d.entropy.ofTable, d.entropy.mlTable
	b.entropy.hufTable, b.entropy.rep = d.entropy.hufTable, d.entropy.rep
	b.llTptr, b.mlTptr, b.ofTptr, b.hufPtr = d.llTptr, d.mlTptr, d.ofTptr, d.hufPtr
	b.litEntropy, b.fseEntropy, b.ddictIsCold = d.litEntropy, d.fseEntropy, d.ddictIsCold
}

// restoreBlockState restores what saveBlockState saved.
func (d *dctx) restoreBlockState() {
	b := d.blockSnap
	d.entropy.llTable, d.entropy.ofTable, d.entropy.mlTable = b.entropy.llTable, b.entropy.ofTable, b.entropy.mlTable
	d.entropy.hufTable, d.entropy.rep = b.entropy.hufTable, b.entropy.rep
	d.llTptr, d.mlTptr, d.ofTptr, d.hufPtr = b.llTptr, b.mlTptr, b.ofTptr, b.hufPtr
	d.litEntropy, d.fseEntropy, d.ddictIsCold = b.litEntropy, b.fseEntropy, b.ddictIsCold
}

// memBudget is a byte budget that concurrent decodes share (no C
// counterpart): a 32-bit Decoder's DecodeAll calls charge it for every
// buffer they allocate, so that together they cannot exhaust the address
// space (see Decoder).
type memBudget struct{ left atomic.Int64 }

// newMemBudget returns a budget of n bytes.
func newMemBudget(n int64) *memBudget {
	b := &memBudget{}
	b.left.Store(n)
	return b
}

// take takes n bytes from b, or reports that fewer are left.
func (b *memBudget) take(n int) bool {
	for {
		l := b.left.Load()
		if int64(n) > l {
			return false
		}
		if b.left.CompareAndSwap(l, l-int64(n)) {
			return true
		}
	}
}

// growBuffer returns b with room for n more bytes after len(b): b itself
// when it has it, else a copy of b[:len(b)] in an array of exactly
// len(b)+n bytes. With a budget, that array is charged to it first (and
// stays charged until releaseBudget, even once replaced):
// dErrDecoderSizeExceeded when the budget is spent.
func (d *dctx) growBuffer(b []byte, n int) ([]byte, dErr) {
	if cap(b)-len(b) >= n {
		return b, 0
	}
	c := len(b) + n
	if d.budget != nil {
		if !d.budget.take(c) {
			return b, dErrDecoderSizeExceeded
		}
		d.charged += c
	}
	nb := make([]byte, len(b), c)
	copy(nb, b)
	return nb, 0
}

// releaseBudget gives back what the decode charged to the budget.
func (d *dctx) releaseBudget() {
	if d.budget != nil {
		d.budget.left.Add(int64(d.charged))
	}
	d.charged = 0
}

// decompressMultiFrame decodes every frame of src (skipping skippable
// frames), appending the output to dst, which it grows as the allocation
// rule says: each frame gets an
// output space of min(frame content size, blocks × blockSizeMax) bytes,
// cut to d.maxMemory less the output so far, and memory as its blocks
// need it. dst[:len(dst)] is never modified.
// On an error the result holds the output decoded before it.
// C: zstd_decompress.c:ZSTD_decompressMultiFrame (via ZSTD_decompressDCtx)
func (d *dctx) decompressMultiFrame(dst, src []byte, dd *ddict) ([]byte, error) {
	out, e := d.decompressFrames(dst, src, dd)
	if e != 0 {
		return out, e.toError("decompress")
	}
	return out, nil
}

// newScratchMax is the largest output buffer a DecodeAll context keeps
// for decompressNew (Go only).
const newScratchMax = 1 << 20

// decompressNew is decompressMultiFrame into a new array (dst without
// capacity), for an input whose output size the allocation rule cannot
// reserve exactly (outputSizeUnsure): it decodes in d.newScratch, then
// copies the output to an array of exactly its size, so that the caller
// does not get a reservation's spare capacity. An output larger than
// newScratchMax is the array decompressMultiFrame grows; one up to
// newScratchMax makes the scratch grow for the next calls. The result
// and the error are decompressMultiFrame's (Go only).
func (d *dctx) decompressNew(src []byte) ([]byte, error) {
	s := d.newScratch[:0]
	out, err := d.decompressMultiFrame(s, src, nil)
	if len(out) == 0 {
		// No output: keep the reservation for the next call (a fresh
		// context has none) and hand out no capacity.
		if cap(s) == 0 && cap(out) > 0 && cap(out) <= newScratchMax {
			d.newScratch = out[:0]
		}
		return out[:0:0], err
	}
	if cap(s) > 0 && cap(out) > 0 && &out[:1][0] == &s[:1][0] {
		r := make([]byte, len(out))
		copy(r, out)
		return r, err
	}
	if n := len(out); n > cap(s) && n <= newScratchMax {
		d.newScratch = make([]byte, 0, min(1<<bits.Len(uint(n-1)), newScratchMax))
	}
	return out, err
}

// outputSizeUnsure reports whether src starts with a frame whose output
// size decompressFrames cannot reserve exactly: one without a content
// size (a frame with one, trusted or up to frameTrustMin, gets exactly
// that; see the allocation rule).
func outputSizeUnsure(src []byte) bool {
	var zfh frameHeader
	need, e := getFrameHeaderAdvanced(&zfh, src)
	return e == 0 && need == 0 && zfh.frameType == zstdFrame && zfh.frameContentSize == contentSizeUnknown
}

// decompressFrames is decompressMultiFrame returning the dErr.
func (d *dctx) decompressFrames(dst, src []byte, dd *ddict) ([]byte, dErr) {
	start := len(dst)
	for len(src) >= startingInputLength {
		magic := binary.LittleEndian.Uint32(src)
		if magic&magicSkippableMask == magicSkippableStart {
			// skippable frame detected: skip it
			skippableSize, e := readSkippableFrameSize(src)
			if e != 0 {
				return dst, e // invalid skippable frame
			}
			src = src[skippableSize:]
			continue // check next frame
		}
		// Go: C reaches prefix_unknown through ZSTD_decompressFrame, after
		// a srcSize_wrong check for fewer than 9 bytes, and turns it into
		// srcSize_wrong once a frame has completed. DecodeAll follows
		// streaming C instead: bytes that start no frame are
		// prefix_unknown however few they are (a deliberate deviation, see
		// the error mapping table).
		if magic != magicNumber {
			return dst, dErrPrefixUnknown
		}

		// C: ZSTD_decompressBegin_usingDDict. With a set of dictionaries
		// the frame's ID picks one first: C does that in
		// ZSTD_decodeFrameHeader, which streaming C runs before beginning
		// the frame with the chosen DDict (one-shot C begins with the DDict
		// referenced last, then checks the ID against the chosen one).
		var zfh frameHeader
		need, he := getFrameHeaderAdvanced(&zfh, src)
		headerOK := he == 0 && need == 0
		fdd := dd
		if d.ddicts != nil && headerOK {
			fdd = d.ddicts.selectFrameDDict(zfh.dictID)
		}
		d.decompressBeginUsingDDict(fdd)

		// Go: the allocation rule. The frame's output space: its frame
		// content size, but at most what its blocks can regenerate
		// (ZSTD_decompressBound's nbBlocks × blockSizeMax; the blocks
		// before the walk failed when it did) and the memory left. A
		// frame whose header fails, or declares a window too large, gets
		// none: decompressFrame reports that before any output, as it does
		// a dictionary ID that is not the dictionary's. Memory is
		// reserved up front for a plausible frame content size (at most
		// frameTrustRatio times the frame's compressed size); otherwise for
		// frameGuessRatio times the compressed size, at least
		// frameTrustMin. decompressFrame grows the buffer as blocks need it.
		limit := min(d.maxMemory-min(d.maxMemory, uint64(len(dst)-start)), uint64(math.MaxInt-len(dst)))
		var frameCap, reserve uint64
		byFCS, byLimit, trusted := false, false, false
		if headerOK && (zfh.dictID == 0 || zfh.dictID == d.dictID) &&
			max(zfh.windowSize, 1<<windowLogAbsoluteMin) <= d.maxWindowSize {
			fsi := findFrameSizeInfo(src)
			frameCap = uint64(fsi.nbBlocks) * uint64(zfh.blockSizeMax)
			if zfh.frameContentSize < frameCap { // contentSizeUnknown never is
				frameCap, byFCS = zfh.frameContentSize, true
				if frameCap > limit {
					return dst, dErrDecoderSizeExceeded
				}
			}
			if frameCap > limit {
				frameCap, byLimit = limit, true
			}
			// When the walk fails, the frame's end is unknown: the rest of
			// the input (later frames, junk) stands in for its compressed
			// size, so the reservation is still at most frameTrustRatio ×
			// the input.
			compressedSize := uint64(len(src))
			if fsi.err == 0 {
				compressedSize = uint64(fsi.compressedSize)
			}
			reserve = min(frameCap, max(frameTrustMin, frameGuessRatio*compressedSize))
			if byFCS && frameCap/frameTrustRatio <= compressedSize {
				reserve, trusted = frameCap, true
			}
		}
		// Spare capacity dst already has is not an untrusted reservation:
		// the frame decodes into it (up to its output space) before any
		// growth.
		// Without a trusted content size, the spare capacity replaces the
		// guess: the frame decodes in it, and decompressFrame decodes a
		// compressed block that lacks the full-space room there into the
		// room left first (tryTight), so a dst the caller sized for the
		// output is used without growth.
		spare := min(frameCap, uint64(cap(dst)-len(dst)))
		d.tryTight = !trusted && spare > 0 && spare < frameCap
		if d.tryTight {
			reserve = spare
		} else {
			reserve = max(reserve, spare)
		}
		ge := dErr(0)
		if dst, ge = d.growBuffer(dst, int(reserve)); ge != 0 {
			return dst, ge
		}
		buf := dst[len(dst) : len(dst) : len(dst)+int(reserve)]
		if frameCap == 0 {
			// Go: no output space is C's NULL dst, whether or not the
			// caller's dst is nil, so that the result does not depend on
			// it (decodeLiteralsBlock's "NULL not handled" check).
			buf = nil
		}

		// C passes dstCapacity, the frame's output space; the buffer's
		// capacity may be smaller (decompressFrame grows it). Both are 0
		// only together.
		d.checkContinuity(buf, int(frameCap))
		out, consumed, e := d.decompressFrame(buf, int(frameCap), src)
		if len(out) > 0 && cap(dst) > len(dst) && &out[0] == &dst[:len(dst)+1][len(dst)] {
			dst = dst[:len(dst)+len(out)] // decoded in place
		} else if len(dst) == 0 {
			dst = out // the frame outgrew its reservation: its buffer is the result
		} else {
			// The frame outgrew its reservation: append it, growing dst by
			// at least a quarter (as append does) so frames after it
			// append in amortized O(1).
			if dst, ge = d.growBuffer(dst, max(len(out), len(dst)/4)); ge != 0 {
				return dst, ge
			}
			dst = append(dst, out...)
		}
		if e != 0 {
			if e.cCode() == int(dErrDstSizeTooSmall) && e != dErrDecoderSizeExceeded {
				// Go: name what limited the output space.
				switch {
				case byLimit:
					e = dErrDecoderSizeExceeded
				case byFCS:
					e = dErrFrameSizeExceeded
				}
			}
			return dst, e
		}
		src = src[consumed:]
	}
	if len(src) > 0 {
		// C: srcSize_wrong ("input not entirely consumed"). Go: a tail that
		// cannot start a frame is prefix_unknown (see above).
		var zfh frameHeader
		if _, e := getFrameHeaderAdvanced(&zfh, src); e != 0 {
			return dst, e
		}
		return dst, dErrSrcSizeWrong
	}
	return dst, 0
}

/*-**************************************
*   Advanced Streaming Decompression API
*   Bufferless and synchronous
****************************************/

// nextSrcSizeToDecompress. C: zstd_decompress.c:ZSTD_nextSrcSizeToDecompress
func (d *dctx) nextSrcSizeToDecompress() uint { return d.expected }

// nextSrcSizeToDecompressWithInputSize is nextSrcSizeToDecompress, but a
// raw block, which can be streamed, may be taken partially: the input the
// caller has (inputSize), at least 1 byte and at most what the block has
// left. C: zstd_decompress.c:ZSTD_nextSrcSizeToDecompressWithInputSize
func (d *dctx) nextSrcSizeToDecompressWithInputSize(inputSize uint) uint {
	if !(d.stage == dsDecompressBlock || d.stage == dsDecompressLastBlock) {
		return d.expected
	}
	if d.bType != btRaw {
		return d.expected
	}
	return max(1, min(inputSize, d.expected))
}

// inputType is the kind of input the bufferless decoder expects next.
// C: zstd.h:ZSTD_nextInputType_e
type inputType uint8

const (
	nitFrameHeader    inputType = iota // C: ZSTDnit_frameHeader
	nitBlockHeader                     // C: ZSTDnit_blockHeader
	nitBlock                           // C: ZSTDnit_block
	nitLastBlock                       // C: ZSTDnit_lastBlock
	nitChecksum                        // C: ZSTDnit_checksum
	nitSkippableFrame                  // C: ZSTDnit_skippableFrame
)

// nextInputType. C: zstd_decompress.c:ZSTD_nextInputType
func (d *dctx) nextInputType() inputType {
	switch d.stage {
	case dsDecodeBlockHeader:
		return nitBlockHeader
	case dsDecompressBlock:
		return nitBlock
	case dsDecompressLastBlock:
		return nitLastBlock
	case dsCheckChecksum:
		return nitChecksum
	case dsDecodeSkippableHeader, dsSkipFrame:
		return nitSkippableFrame
	}
	return nitFrameHeader // dsGetFrameHeaderSize, dsDecodeFrameHeader
}

// isSkipFrame. C: zstd_decompress.c:ZSTD_isSkipFrame
func (d *dctx) isSkipFrame() bool { return d.stage == dsSkipFrame }

// decompressContinue decodes the next srcSize bytes of input (src, which
// must be exactly nextSrcSizeToDecompress bytes, or for a raw block at
// least 1 of them), writing output into dst[len(dst):len(dst)+dstCapacity],
// and returns the number of bytes written. dst[:len(dst)] is the output
// buffer before them (as for checkContinuity); cap(dst) >=
// len(dst)+dstCapacity. In the dsSkipFrame stage, C does not read src: it
// may then be nil, srcSize still giving its size.
// C: zstd_decompress.c:ZSTD_decompressContinue
func (d *dctx) decompressContinue(dst []byte, dstCapacity int, src []byte, srcSize uint) (int, dErr) {
	// Sanity check
	if srcSize != d.nextSrcSizeToDecompressWithInputSize(srcSize) {
		return 0, dErrSrcSizeWrong // not allowed
	}
	d.checkContinuity(dst, dstCapacity)

	switch d.stage {
	case dsGetFrameHeaderSize:
		// format ZSTD_f_zstd1: allows header; srcSize >= frameIDSize to
		// read the skippable magic number
		if binary.LittleEndian.Uint32(src)&magicSkippableMask == magicSkippableStart { // skippable frame
			copy(d.headerBuffer[:], src)
			d.expected = skippableHeaderSize - srcSize // remaining to load to get full skippable frame header
			d.stage = dsDecodeSkippableHeader
			return 0, 0
		}
		headerSize, e := frameHeaderSizeInternal(src)
		if e != 0 {
			return 0, e
		}
		d.headerSize = headerSize
		copy(d.headerBuffer[:], src)
		d.expected = uint(d.headerSize) - srcSize
		d.stage = dsDecodeFrameHeader
		return 0, 0

	case dsDecodeFrameHeader:
		copy(d.headerBuffer[uint(d.headerSize)-srcSize:], src)
		// Go: with a set of dictionaries, C selects the frame's here
		// (ZSTD_decodeFrameHeader); the caller of the bufferless API
		// begins the frame with its dictionary instead.
		if e := d.decodeFrameHeader(d.headerBuffer[:d.headerSize]); e != 0 {
			return 0, e
		}
		d.expected = blockHeaderSize
		d.stage = dsDecodeBlockHeader
		return 0, 0

	case dsDecodeBlockHeader:
		cBlockSize, bp, e := getcBlockSize(src[:blockHeaderSize])
		if e != 0 {
			return 0, e
		}
		// Go: an RLE block's size here is 1 (its byte), which one-shot
		// decoding does not check: its run length is checked when the
		// block is decoded, as there (deviation, so that a frame with
		// blockSizeMax 0 and an empty run decodes as with DecodeAll).
		if cBlockSize > int(d.fParams.blockSizeMax) && bp.blockType != btRLE {
			// Block Size Exceeds Maximum. Go: refined by block type, as
			// one-shot decoding reports the same block (see the dErrs).
			if bp.blockType == btCompressed {
				return 0, dErrCBlockSizeTooLarge
			}
			return 0, dErrDecodedBlockTooLarge
		}
		d.expected = uint(cBlockSize)
		d.bType = bp.blockType
		d.rleSize = bp.origSize
		if cBlockSize != 0 {
			d.stage = dsDecompressBlock
			if bp.lastBlock {
				d.stage = dsDecompressLastBlock
			}
			return 0, 0
		}
		// empty block
		if bp.blockType == btCompressed {
			// Go: C skips it as empty; one-shot decoding rejects a
			// compressed block too small for a literals header
			// (ZSTD_decodeLiteralsBlock). Deviation, so that the stream
			// and DecodeAll agree.
			return 0, dErrBlockTooSmall
		}
		if bp.lastBlock {
			// Go: C checks the frame content size only after a non-empty
			// last block; one-shot decoding checks it at every frame end.
			// Deviation, as above.
			if d.fParams.frameContentSize != contentSizeUnknown && d.decodedSize != d.fParams.frameContentSize {
				return 0, dErrFrameSizeMismatch
			}
			if d.fParams.checksumFlag {
				d.expected = 4
				d.stage = dsCheckChecksum
			} else {
				d.expected = 0 // end of frame
				d.stage = dsGetFrameHeaderSize
			}
		} else {
			d.expected = blockHeaderSize // jump to next header
			d.stage = dsDecodeBlockHeader
		}
		return 0, 0

	case dsDecompressLastBlock, dsDecompressBlock:
		var rSize int
		e := dErr(0)
		out := dst[len(dst) : len(dst)+dstCapacity]
		switch d.bType {
		case btCompressed:
			if d.pipe != nil {
				rSize, e = d.pipeBlock(dst, dstCapacity, src) // Go: the stream pipeline (decoder_mt.go)
			} else {
				rSize, e = d.decompressBlockInternal(dst, dstCapacity, src, isStreaming)
			}
			d.expected = 0 // Streaming not supported
		case btRaw:
			// Go: a block that takes the frame past its content size is
			// rejected before any of it is written, as one-shot decoding
			// does (C checks the size at the frame's end). Deviation, as
			// above; d.expected is the rest of the block.
			if d.fParams.frameContentSize != contentSizeUnknown && d.decodedSize+uint64(d.expected) > d.fParams.frameContentSize {
				return 0, dErrFrameSizeExceeded
			}
			rSize, e = copyRawBlock(out, src)
			if e != 0 {
				return 0, e
			}
			d.expected -= uint(rSize)
		case btRLE:
			rSize, e = setRLEBlock(out, src[0], int(d.rleSize))
			d.expected = 0 // Streaming not supported
		default: // btReserved: should never happen
			return 0, dErrCorruptionDetected // invalid block type
		}
		if e != 0 {
			return 0, e
		}
		if rSize > int(d.fParams.blockSizeMax) {
			return 0, dErrDecodedBlockTooLarge // Decompressed Block Size Exceeds Maximum
		}
		if d.fParams.frameContentSize != contentSizeUnknown && d.decodedSize+uint64(rSize) > d.fParams.frameContentSize {
			return 0, dErrFrameSizeExceeded // Go: as for a raw block, once the block is decoded
		}
		d.decodedSize += uint64(rSize)
		if d.validateChecksum {
			d.xxhState.Write(out[:rSize])
		}
		if rSize > 0 {
			// C sets previousDstEnd = dst + rSize unconditionally: see
			// decompressBlock.
			d.previousDstEnd = len(dst) + rSize
		}

		// Stay on the same stage until we are finished streaming the block.
		if d.expected > 0 {
			return rSize, 0
		}

		if d.stage == dsDecompressLastBlock { // end of frame
			if d.fParams.frameContentSize != contentSizeUnknown && d.decodedSize != d.fParams.frameContentSize {
				// Go: with the size of the block written, so that the
				// caller can deliver it, as one-shot decoding does.
				return rSize, dErrFrameSizeMismatch
			}
			if d.fParams.checksumFlag { // another round for frame checksum
				d.expected = 4
				d.stage = dsCheckChecksum
			} else {
				d.expected = 0 // ends here
				d.stage = dsGetFrameHeaderSize
			}
		} else {
			d.stage = dsDecodeBlockHeader
			d.expected = blockHeaderSize
		}
		return rSize, 0

	case dsCheckChecksum:
		// srcSize == 4, guaranteed by d.expected
		if d.validateChecksum {
			h32 := uint32(d.xxhState.Sum64())
			check32 := binary.LittleEndian.Uint32(src)
			if check32 != h32 {
				return 0, dErrChecksumWrong
			}
		}
		d.expected = 0
		d.stage = dsGetFrameHeaderSize
		return 0, 0

	case dsDecodeSkippableHeader:
		copy(d.headerBuffer[skippableHeaderSize-srcSize:], src) // complete skippable header
		if _, e := readSkippableFrameSize(d.headerBuffer[:]); e == dErrFrameParameterUnsupported {
			return 0, e // Go: as one-shot decoding (deviation, see decompressStream)
		}
		// note: expected can grow seriously large, beyond local buffer size
		d.expected = uint(binary.LittleEndian.Uint32(d.headerBuffer[frameIDSize:]))
		d.stage = dsSkipFrame
		return 0, 0

	case dsSkipFrame:
		d.expected = 0
		d.stage = dsGetFrameHeaderSize
		return 0, 0
	}
	return 0, dErrGeneric // impossible to reach
}

// Streaming decompression buffers. C: zstd.h
const (
	dStreamInSize  = blockSizeMax + blockHeaderSize // C: ZSTD_DStreamInSize()
	dStreamOutSize = blockSizeMax                   // C: ZSTD_DStreamOutSize()

	// noForwardProgressMax is the number of calls in a row without
	// progress decompressStream accepts. C: ZSTD_NO_FORWARD_PROGRESS_MAX
	noForwardProgressMax = 16
	// C: zstd_internal.h:ZSTD_WORKSPACETOOLARGE_FACTOR and
	// ZSTD_WORKSPACETOOLARGE_MAXDURATION
	workspaceTooLargeFactor      = 3
	workspaceTooLargeMaxDuration = 128
)

// inBuffer. C: zstd.h:ZSTD_inBuffer (size is len(src))
type inBuffer struct {
	src []byte
	pos int
}

// outBuffer. C: zstd.h:ZSTD_outBuffer (size is len(dst))
type outBuffer struct {
	dst []byte
	pos int
}

// decodingBufferSizeInternal returns the size of the round buffer a frame
// needs. C: zstd_decompress.c:ZSTD_decodingBufferSize_internal
func decodingBufferSizeInternal(windowSize, frameContentSize uint64, maxBlockSize int) (int, dErr) {
	blockSize := min(windowSize, blockSizeMax, uint64(maxBlockSize))
	// We need blockSize + WILDCOPY_OVERLENGTH worth of buffer so that if a
	// block ends at windowSize + WILDCOPY_OVERLENGTH + 1 bytes, we can start
	// writing the block at the beginning of the output buffer, and maintain
	// a full window.
	//
	// We need another blockSize worth of buffer so that we can store split
	// literals at the end of the block without overwriting the extDict
	// window.
	neededRBSize := windowSize + blockSize*2 + wildcopyOverlength*2
	neededSize := min(frameContentSize, neededRBSize)
	// C: the size must fit size_t; Go's buffers are indexed by int.
	if neededSize > math.MaxInt {
		return 0, dErrFrameParameterWindowTooLarge
	}
	return int(neededSize), 0
}

// isOverflow. C: zstd_decompress.c:ZSTD_DCtx_isOverflow (in uint64, as on
// 64-bit C)
func (d *dctx) isOverflow(neededInBuffSize, neededOutBuffSize int) bool {
	return uint64(d.inBuffSize)+uint64(d.outBuffSize) >= (uint64(neededInBuffSize)+uint64(neededOutBuffSize))*workspaceTooLargeFactor
}

// updateOversizedDuration. C: zstd_decompress.c:ZSTD_DCtx_updateOversizedDuration
func (d *dctx) updateOversizedDuration(neededInBuffSize, neededOutBuffSize int) {
	if d.isOverflow(neededInBuffSize, neededOutBuffSize) {
		d.oversizedDuration++
	} else {
		d.oversizedDuration = 0
	}
}

// isOversizedTooLong. C: zstd_decompress.c:ZSTD_DCtx_isOversizedTooLong
func (d *dctx) isOversizedTooLong() bool {
	return d.oversizedDuration >= workspaceTooLargeMaxDuration
}

// growBuf returns b if it has n bytes or more, else a buffer of
// max(n, 2×len(b)) bytes, at most limit (n <= limit), starting with b's
// bytes (Go: the stream buffers grow as the data needs them).
func growBuf(b []byte, n, limit int) []byte {
	if n <= len(b) {
		return b
	}
	nb := make([]byte, min(max(n, 2*len(b)), limit))
	copy(nb, b)
	return nb
}

// growOutBuff makes outBuff at least n bytes long (n <= outBuffSize). The
// output in it moves with it: when the current segment is in outBuff,
// prevDst follows (the offsets C keeps as pointers stay valid). Once the
// buffer has wrapped it is never grown again (decompressStream grows it to
// outBuffSize first), so the extDict is never left in an old copy.
func (d *dctx) growOutBuff(n int) {
	if n <= len(d.outBuff) {
		return
	}
	old := d.outBuff
	d.outBuff = growBuf(old, n, d.outBuffSize)
	if len(old) > 0 && cap(d.prevDst) > 0 && &d.prevDst[:1][0] == &old[0] {
		d.prevDst = d.outBuff
	}
}

// decompressContinueStream calls decompressContinue with the right
// parameters for decompressStream and updates the stage and the output
// buffer state (C's buffered output mode; ZSTD_bm_stable is not ported).
// C: zstd_decompress.c:ZSTD_decompressContinueStream
func (d *dctx) decompressContinueStream(src []byte, srcSize uint) dErr {
	isSkipFrame := d.isSkipFrame()
	dstSize := 0
	if !isSkipFrame {
		dstSize = d.outBuffSize - d.outStart
	}
	// Go: make room in outBuff for what this input can write (C's outBuff
	// has dstSize bytes there): a raw block's bytes, an RLE block's size,
	// and for a compressed block, a whole block and literals placed after
	// it (more than ZSTD_decompressBlock_internal can write before it
	// fails). dstCap is what the buffer has.
	dstCap := 0
	if dstSize > 0 {
		need := 1
		if d.stage == dsDecompressBlock || d.stage == dsDecompressLastBlock {
			switch d.bType {
			case btRaw:
				need = int(srcSize)
			case btRLE:
				// Go: a run longer than blockSizeMax fails whatever room
				// it has (C writes it first, then rejects it), so none is
				// made for it: it fails on the room, with the error C's
				// check gives (see below).
				need = int(d.rleSize)
				if need > int(d.fParams.blockSizeMax) {
					need = 1
				}
			case btCompressed:
				need = 2*int(d.fParams.blockSizeMax) + 2*wildcopyOverlength + 1
			}
		}
		d.growOutBuff(d.outStart + min(dstSize, max(need, 1)))
		dstCap = min(dstSize, len(d.outBuff)-d.outStart)
	}
	decodedSize, e := d.decompressContinue(d.outBuff[:d.outStart:d.outStart+dstCap], dstCap, src, srcSize)
	if e == dErrFrameSizeMismatch && decodedSize > 0 {
		// Go: the last block is delivered before the error (see endErr).
		d.endErr, e = e, 0
	}
	if e != 0 {
		if e.cCode() == int(dErrDstSizeTooSmall) && e != dErrFrameSizeExceeded {
			switch {
			case dstCap < dstSize && !(d.bType == btRLE && int(d.rleSize) > dstSize):
				// More than dstCap > blockSizeMax bytes: C fails the block
				// too, with "Decompressed Block Size Exceeds Maximum"
				// unless it also runs out of its larger buffer (which an
				// RLE block's size tells).
				e = dErrDecodedBlockTooLarge
			case uint64(d.outBuffSize) == d.fParams.frameContentSize && dstSize < int(d.fParams.blockSizeMax):
				// Go: the buffer (not round: no wrap) is the frame content
				// size, and less of it is left than a block may regenerate:
				// the frame regenerates more than its content size. One-shot
				// decoding names that error so too (its output space is
				// then the content size); a block failing with more room
				// left is over blockSizeMax, ErrUnexpectedBlockSize.
				e = dErrFrameSizeExceeded
			}
		}
		return e
	}
	if decodedSize == 0 && !isSkipFrame {
		d.streamStage = zdssRead
	} else {
		d.outEnd = d.outStart + decodedSize
		d.streamStage = zdssFlush
	}
	return 0
}

// decompressStream decodes input from in into out, advancing in.pos and
// out.pos, and returns a hint for the input size of the next call: 0 when
// a frame is completely decoded and flushed (also with no input at a frame
// boundary). On an error, in.pos and out.pos report the input consumed and
// the output written before it (C leaves them as they were: a deviation);
// the dctx must then be reset (reset(true)) before reuse.
// C: zstd_decompress.c:ZSTD_decompressStream
func (d *dctx) decompressStream(out *outBuffer, in *inBuffer) (hint int, err error) {
	hint, e := d.decompressStreamCode(out, in)
	if e != 0 {
		return 0, e.toError("stream")
	}
	return hint, nil
}

// decompressStreamCode is decompressStream returning the dErr.
func (d *dctx) decompressStreamCode(out *outBuffer, in *inBuffer) (int, dErr) {
	if in.pos > len(in.src) {
		return 0, dErrSrcSizeWrong // forbidden
	}
	if out.pos > len(out.dst) {
		return 0, dErrDstSizeTooSmall // forbidden
	}
	istart, iend := in.pos, len(in.src)
	ip := istart
	ostart, oend := out.pos, len(out.dst)
	op := ostart
	someMoreWork := true
	// Go: on an error, the input consumed and the output written so far
	// are reported (C returns before updating in.pos and out.pos):
	// deviation, so that the output before an error is the same however
	// the input is cut, and DecodeAll's.
	fail := func(e dErr) (int, dErr) {
		in.pos, out.pos = ip, op
		return 0, e
	}

	for someMoreWork {
		switch d.streamStage {
		case zdssInit: // transparent reset
			d.streamStage = zdssLoadHeader
			d.lhSize, d.inPos, d.outStart, d.outEnd = 0, 0, 0, 0
			d.hostageByte = false
			d.endErr = 0
			fallthrough

		case zdssLoadHeader:
			hSize, e := getFrameHeaderAdvanced(&d.fParams, d.headerBuffer[:d.lhSize])
			// C selects the frame's dictionary here (ZSTD_DCtx_selectFrameDDict,
			// with ZSTD_d_refMultipleDDicts): see fdd below.
			if e != 0 {
				return fail(e)
			}
			if hSize != 0 { // need more input
				toLoad := hSize - d.lhSize // if hSize != 0, hSize > lhSize
				remainingInput := iend - ip
				if toLoad > remainingInput { // not enough input to load full header
					copy(d.headerBuffer[d.lhSize:], in.src[ip:iend])
					d.lhSize += remainingInput
					ip = iend
					in.pos = len(in.src)
					// check first few bytes
					if _, e := getFrameHeaderAdvanced(&d.fParams, d.headerBuffer[:d.lhSize]); e != 0 {
						return fail(e) // First few bytes detected incorrect
					}
					// return hint input size: remaining header bytes + next block header
					return max(frameHeaderSizeMin, hSize) - d.lhSize + blockHeaderSize, 0
				}
				copy(d.headerBuffer[d.lhSize:], in.src[ip:ip+toLoad])
				d.lhSize = hSize
				ip += toLoad
				break
			}

			// Go: the frame's dictionary, chosen as decompressFrames does
			// (selectFrameDDict; see the deviations from C there).
			fdd := d.ddict
			if d.ddicts != nil {
				fdd = d.ddicts.selectFrameDDict(d.fParams.dictID)
			}

			// check for single-pass mode opportunity
			// Go: only when the frame starts at istart, i.e. its whole
			// header came in this call. C also tries it when the header
			// began in an earlier call, from istart, then in the middle of
			// the frame, and may decode what follows there as a frame
			// (deviation: the result must not depend on how the input is
			// cut).
			// Go: not when a pipeline feeds the stream (its compressed
			// blocks are S2's to decode). The result is the stream's
			// without the shortcut, which differs from the shortcut's in
			// the documented size pair only: a block regenerating too much
			// is ErrUnexpectedBlockSize here, where the shortcut can name
			// it ErrFrameSizeExceeded, or fails another check here where
			// the shortcut names it ErrUnexpectedBlockSize.
			if d.pipe == nil && d.fParams.frameContentSize != contentSizeUnknown &&
				d.fParams.frameType != zstdSkippableFrame &&
				uint64(oend-op) >= d.fParams.frameContentSize && ip-istart == d.lhSize {
				// C: ZSTD_findFrameCompressedSize_advanced from istart
				fsi := findFrameSizeInfo(in.src[istart:iend])
				if fsi.err == 0 && fsi.compressedSize <= iend-istart {
					// shortcut: using single-pass mode. C:
					// ZSTD_decompress_usingDDict (ZSTD_decompressMultiFrame
					// on the frame). Go: DecodeAll's decompressFrames, with
					// its output space and checks, so that the result is
					// the one the frame gives without the shortcut and with
					// DecodeAll (deviation; see the error mapping table). It
					// decodes in place: its output space is at most the
					// frame content size.
					res, e := d.decompressFrames(out.dst[op:op:oend], in.src[istart:istart+fsi.compressedSize], d.ddict)
					op += copy(out.dst[op:oend], res)
					if e != 0 {
						return fail(e)
					}
					ip = istart + fsi.compressedSize
					d.expected = 0
					d.streamStage = zdssInit
					someMoreWork = false
					break
				}
			}

			// Consume header (see dsDecodeFrameHeader)
			d.decompressBeginUsingDDict(fdd)

			if binary.LittleEndian.Uint32(d.headerBuffer[:])&magicSkippableMask == magicSkippableStart { // skippable frame
				// Go: a size that overflows with the header is rejected, as
				// one-shot decoding does (readSkippableFrameSize); C waits
				// for the content. Deviation, as above.
				if _, e := readSkippableFrameSize(d.headerBuffer[:d.lhSize]); e == dErrFrameParameterUnsupported {
					return fail(e)
				}
				d.expected = uint(binary.LittleEndian.Uint32(d.headerBuffer[frameIDSize:]))
				d.stage = dsSkipFrame
			} else {
				if e := d.decodeFrameHeader(d.headerBuffer[:d.lhSize]); e != 0 {
					return fail(e)
				}
				d.expected = blockHeaderSize
				d.stage = dsDecodeBlockHeader
			}

			// control buffer memory usage
			d.fParams.windowSize = max(d.fParams.windowSize, 1<<windowLogAbsoluteMin)
			if d.fParams.windowSize > d.maxWindowSize {
				return fail(dErrFrameParameterWindowTooLarge)
			}
			// C shrinks blockSizeMax to maxBlockSizeParam here; no option
			// sets it.

			// Adapt buffer sizes to frame header instructions
			neededInBuffSize := max(int(d.fParams.blockSizeMax), 4) // 4: frame checksum
			neededOutBuffSize, e := decodingBufferSizeInternal(d.fParams.windowSize, d.fParams.frameContentSize, int(d.fParams.blockSizeMax))
			if e != 0 {
				return fail(e)
			}
			d.updateOversizedDuration(neededInBuffSize, neededOutBuffSize)
			tooSmall := d.inBuffSize < neededInBuffSize || d.outBuffSize < neededOutBuffSize
			tooLarge := d.isOversizedTooLong()
			if tooSmall || tooLarge {
				// C frees the buffers and allocates them at the new sizes.
				// Go drops them; they grow as the frame's data needs.
				d.inBuff, d.outBuff = nil, nil
				d.inBuffSize = neededInBuffSize
				d.outBuffSize = neededOutBuffSize
			}
			d.streamStage = zdssRead
			fallthrough

		case zdssRead:
			neededInSize := d.nextSrcSizeToDecompressWithInputSize(uint(iend - ip))
			if neededInSize == 0 { // end of frame
				d.streamStage = zdssInit
				someMoreWork = false
				break
			}
			if uint(iend-ip) >= neededInSize { // decode directly from src
				if e := d.decompressContinueStream(in.src[ip:ip+int(neededInSize)], neededInSize); e != 0 {
					return fail(e)
				}
				ip += int(neededInSize)
				// Function modifies the stage so we must break
				break
			}
			if ip == iend { // no more input
				someMoreWork = false
				break
			}
			d.streamStage = zdssLoad
			fallthrough

		case zdssLoad:
			neededInSize := d.nextSrcSizeToDecompress()
			toLoad := neededInSize - d.inPos
			isSkipFrame := d.isSkipFrame()
			// At this point we shouldn't be decompressing a block that we can stream.
			var loadedSize int
			if isSkipFrame {
				loadedSize = int(min(toLoad, uint(iend-ip)))
			} else {
				if toLoad > uint(d.inBuffSize)-d.inPos {
					return fail(dErrCorruptionDetected) // should never happen
				}
				loadedSize = int(min(toLoad, uint(iend-ip))) // C: ZSTD_limitCopy
				d.inBuff = growBuf(d.inBuff, int(d.inPos)+loadedSize, d.inBuffSize)
				copy(d.inBuff[d.inPos:], in.src[ip:ip+loadedSize])
			}
			ip += loadedSize
			d.inPos += uint(loadedSize)
			if uint(loadedSize) < toLoad { // not enough input, wait for more
				someMoreWork = false
				break
			}

			// decode loaded input
			d.inPos = 0 // input is consumed
			var src []byte
			if !isSkipFrame {
				src = d.inBuff[:neededInSize]
			}
			if e := d.decompressContinueStream(src, neededInSize); e != 0 {
				return fail(e)
			}
			// Function modifies the stage so we must break

		case zdssFlush:
			toFlushSize := d.outEnd - d.outStart
			flushedSize := copy(out.dst[op:oend], d.outBuff[d.outStart:d.outEnd]) // C: ZSTD_limitCopy
			op += flushedSize
			d.outStart += flushedSize
			if flushedSize == toFlushSize { // flush completed
				if d.endErr != 0 {
					if d.hostageByte && ip < iend {
						ip++ // the hostage byte (the frame's last) was consumed
					}
					return fail(d.endErr)
				}
				d.streamStage = zdssRead
				if uint64(d.outBuffSize) < d.fParams.frameContentSize &&
					d.outStart+int(d.fParams.blockSizeMax) > d.outBuffSize {
					// restart filling outBuff from beginning. Go: the
					// buffer gets its full size first, so it does not
					// move while the extDict is in it.
					d.growOutBuff(d.outBuffSize)
					d.outStart, d.outEnd = 0, 0
				}
				break
			}
			// cannot complete flush
			someMoreWork = false

		default:
			return fail(dErrGeneric) // impossible to reach
		}
	}

	// result
	in.pos = ip
	out.pos = op

	if ip == istart && op == ostart { // no forward progress
		d.noForwardProgress++
		if d.noForwardProgress >= noForwardProgressMax {
			if op == oend {
				return 0, dErrNoForwardProgressDestFull
			}
			if ip == iend {
				return 0, dErrNoForwardProgressInputEmpty
			}
		}
	} else {
		d.noForwardProgress = 0
	}
	nextSrcSizeHint := d.nextSrcSizeToDecompress()
	if nextSrcSizeHint == 0 { // frame fully decoded
		if d.outEnd == d.outStart { // output fully flushed
			if d.hostageByte {
				if in.pos >= len(in.src) {
					// can't release hostage (not present)
					d.streamStage = zdssRead
					return 1, 0
				}
				in.pos++ // release hostage
			}
			return 0, 0
		}
		if !d.hostageByte {
			// output not fully flushed; keep last byte as hostage; will be
			// released when all output is flushed
			in.pos-- // note: pos > 0, otherwise, impossible to finish reading last block
			d.hostageByte = true
		}
		return 1, 0
	}
	if d.nextInputType() == nitBlock {
		nextSrcSizeHint += blockHeaderSize // preload header of next block
	}
	nextSrcSizeHint -= d.inPos // part already loaded
	// Go: a skippable frame's hint can exceed int on 32-bit platforms.
	return int(min(nextSrcSizeHint, math.MaxInt)), 0
}
