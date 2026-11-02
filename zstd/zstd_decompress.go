package zstd

import (
	"encoding/binary"
	"math"
	"slices"
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

// decompressBegin resets d for a new frame. Only the fields dctx has so
// far are ported; the frame stage fields come with the frame decoder.
// C: zstd_decompress.c:ZSTD_decompressBegin
func (d *dctx) decompressBegin() {
	d.previousDstEnd = 0
	d.prefixStart = 0
	d.virtualStart = 0
	d.extDict = nil
	d.prevDst = nil
	d.entropy.hufTable.Init(hufDTableCapacityLog) // C: hufTable[0] = ZSTD_HUFFDTABLE_CAPACITY_LOG * 0x1000001
	d.litEntropy, d.fseEntropy = false, false
	d.dictID = 0
	d.isFrameDecompression = true
	d.entropy.rep = repStartValue // initial repcodes
	d.llTptr = d.entropy.llTable[:]
	d.mlTptr = d.entropy.mlTable[:]
	d.ofTptr = d.entropy.ofTable[:]
	d.hufPtr = &d.entropy.hufTable
}

// Decoder parameter defaults (no C counterparts: the klauspost doc text
// decides them, see docs/superpowers/phase2-api-decisions.md).
const (
	// defaultMaxWindowSize is WithDecoderMaxWindow's default, MaxWindowSize
	// (C: ZSTD_MAXWINDOWSIZE_DEFAULT, (1<<27)+1).
	defaultMaxWindowSize = MaxWindowSize
	// defaultMaxMemory is WithDecoderMaxMemory's default, 64 GiB.
	defaultMaxMemory = 64 << 30
	// frameHeaderSizeMin. C: ZSTD_FRAMEHEADERSIZE_MIN(ZSTD_f_zstd1)
	frameHeaderSizeMin = 6
)

// ddict is a digested decoder dictionary. C: zstd_ddict.c:ZSTD_DDict.
// Placeholder: the dictionary task gives it its fields, in zstd_ddict.go.
type ddict struct{}

// newDCtx returns a decompression context with default parameters.
// C: zstd_decompress.c:ZSTD_createDCtx (ZSTD_initDCtx_internal)
func newDCtx() *dctx {
	d := new(dctx)
	d.reset(false)
	return d
}

// reset resets the session (and, unless sessionOnly, the parameters).
// The streaming and dictionary state come with the tasks that port them.
// C: zstd_decompress.c:ZSTD_DCtx_reset (ZSTD_reset_session_only, or
// ZSTD_reset_session_and_parameters)
func (d *dctx) reset(sessionOnly bool) {
	d.isFrameDecompression = true
	if !sessionOnly {
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
	// C: ZSTD_DCtx_selectFrameDDict when referencing multiple DDicts (the
	// dictionary task).
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

// decompressFrame decodes the frame starting src into dst[:cap(dst)], the
// frame's output buffer from its start (as given to checkContinuity), and
// returns the bytes written and the bytes of src consumed. On an error,
// written is the output of the blocks decoded before it.
// C: zstd_decompress.c:ZSTD_decompressFrame
func (d *dctx) decompressFrame(dst, src []byte) (written, consumed int, err dErr) {
	out := dst[:cap(dst)]
	oend := len(out)
	op, ip := 0, 0
	remainingSrcSize := len(src)

	// check
	if remainingSrcSize < frameHeaderSizeMin+blockHeaderSize {
		return 0, 0, dErrSrcSizeWrong
	}

	// Frame Header
	frameHeaderSize, e := frameHeaderSizeInternal(src[:startingInputLength])
	if e != 0 {
		return 0, 0, e
	}
	if remainingSrcSize < frameHeaderSize+blockHeaderSize {
		return 0, 0, dErrSrcSizeWrong
	}
	if e := d.decodeFrameHeader(src[:frameHeaderSize]); e != 0 {
		return 0, 0, e
	}
	ip += frameHeaderSize
	remainingSrcSize -= frameHeaderSize

	// Go: C's one-shot path does not check the window. DecodeAll applies
	// the streaming check (ZSTD_decompressStream, after
	// ZSTD_decodeFrameHeader) too: a deliberate deviation, see the error
	// mapping table.
	if max(d.fParams.windowSize, 1<<windowLogAbsoluteMin) > d.maxWindowSize {
		return 0, ip, dErrFrameParameterWindowTooLarge
	}

	// C shrinks blockSizeMax to maxBlockSizeParam here; no option sets it.

	// Loop on each block
	for {
		cBlockSize, bp, e := getcBlockSize(src[ip:])
		if e != 0 {
			return op, ip, e
		}
		ip += blockHeaderSize
		remainingSrcSize -= blockHeaderSize
		if cBlockSize > remainingSrcSize {
			return op, ip, dErrSrcSizeWrong
		}
		// C limits the output to ip when decompressing in place (src
		// inside dst); Go's src and dst are separate slices here.
		block := src[ip : ip+cBlockSize]

		var decodedSize int
		switch bp.blockType {
		case btCompressed:
			decodedSize, e = d.decompressBlockInternal(out[:op], oend-op, block, notStreaming)
		case btRaw:
			decodedSize, e = copyRawBlock(out[op:oend], block)
		case btRLE:
			decodedSize, e = setRLEBlock(out[op:oend], block[0], int(bp.origSize))
		default: // btReserved: getcBlockSize has rejected it
			return op, ip, dErrCorruptionDetected
		}
		if e != 0 {
			return op, ip, e
		}
		// Go: streaming C's check (ZSTD_decompressContinue), which one-shot
		// C does not make: a block regenerating more than blockSizeMax.
		// DecodeAll and streaming then agree (a deliberate deviation, see
		// the error mapping table).
		if decodedSize > int(d.fParams.blockSizeMax) {
			return op, ip, dErrDecodedBlockTooLarge
		}
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
		return op, ip, dErrFrameSizeMismatch
	}
	if d.fParams.checksumFlag { // Frame content checksum verification
		if remainingSrcSize < 4 {
			// Go: C returns checksum_wrong here; streaming C waits for
			// more input. DecodeAll reports the cut input like streaming
			// (io.ErrUnexpectedEOF): a deliberate deviation.
			return op, ip, dErrSrcSizeWrong
		}
		if !d.forceIgnoreChecksum {
			checkCalc := uint32(d.xxhState.Sum64())
			checkRead := binary.LittleEndian.Uint32(src[ip:])
			if checkRead != checkCalc {
				return op, ip, dErrChecksumWrong
			}
		}
		ip += 4
	}
	return op, ip, 0
}

// decompressMultiFrame decodes every frame of src (skipping skippable
// frames), appending the output to dst, which it grows as the allocation
// rule says (docs/superpowers/phase2-api-decisions.md): each frame gets
// min(frame content size, blocks × blockSizeMax) bytes, cut to
// d.maxMemory less the output so far. dst[:len(dst)] is never modified.
// On an error the result holds the output decoded before it.
// C: zstd_decompress.c:ZSTD_decompressMultiFrame (via ZSTD_decompressDCtx)
func (d *dctx) decompressMultiFrame(dst, src []byte, dd *ddict) ([]byte, error) {
	out, e := d.decompressFrames(dst, src, dd)
	if e != 0 {
		return out, e.toError("decompress")
	}
	return out, nil
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

		// C: ZSTD_decompressBegin_usingDDict when dd is set (the
		// dictionary task).
		d.decompressBegin()

		// Go: the allocation rule. The frame's output space: its frame
		// content size, but at most what its blocks can regenerate
		// (ZSTD_decompressBound's nbBlocks × blockSizeMax; the blocks
		// before the walk failed when it did) and the memory left. A
		// frame whose header fails, or declares a window too large, gets
		// none: decompressFrame reports that before any output.
		limit := min(d.maxMemory-min(d.maxMemory, uint64(len(dst)-start)), uint64(math.MaxInt-len(dst)))
		var frameCap uint64
		byFCS, byLimit := false, false
		var zfh frameHeader
		if need, e := getFrameHeaderAdvanced(&zfh, src); e == 0 && need == 0 &&
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
		}
		dst = slices.Grow(dst, int(frameCap))
		buf := dst[len(dst) : len(dst) : len(dst)+int(frameCap)]

		d.checkContinuity(buf, len(buf))
		n, consumed, e := d.decompressFrame(buf, src)
		dst = dst[:len(dst)+n]
		if e != 0 {
			if e.cCode() == int(dErrDstSizeTooSmall) {
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
