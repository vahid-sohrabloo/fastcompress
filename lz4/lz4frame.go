package lz4

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/vahid-sohrabloo/fastcompress/internal/xxhash"
)

// The frame format of lz4frame.c (lz4 v1.11.1, Copyright (c) Yann Collet,
// BSD 2-Clause): frame compression (LZ4F_compressBegin,
// LZ4F_compressUpdate, LZ4F_flush, LZ4F_compressEnd, LZ4F_compressFrame and
// the dictionary variants), single-threaded or cut into jobs compressed by
// goroutines (nbWorkers >= 1, frame_mt.go).

// BlockSizeID selects the maximum block size of a frame (C:
// LZ4F_blockSizeID_t). 0 selects Block64KB.
type BlockSizeID uint8

// Maximum block sizes.
const (
	BlockDefault BlockSizeID = 0
	Block64KB    BlockSizeID = 4
	Block256KB   BlockSizeID = 5
	Block1MB     BlockSizeID = 6
	Block4MB     BlockSizeID = 7
)

// FrameInfo describes a frame (C: LZ4F_frameInfo_t). The zero value is C's
// default: 64 KB linked blocks, no checksums, no content size, no
// dictionary ID.
type FrameInfo struct {
	BlockSizeID      BlockSizeID
	BlockIndependent bool   // C: blockMode == LZ4F_blockIndependent
	ContentChecksum  bool   // XXH32 of the content after the end mark
	BlockChecksum    bool   // XXH32 of each block's data
	ContentSize      uint64 // 0: unknown, not written
	DictID           uint32 // 0: none, not written
	Skippable        bool   // decoder only: a skippable frame (C: frameType)
}

// Preferences are the frame compression parameters (C: LZ4F_preferences_t).
type Preferences struct {
	FrameInfo
	// CompressionLevel: below 2 (LevelHCMin) the fast compressor runs, with
	// acceleration 1 for 0 and 1, and -level+1 for negative levels; 2 to 12
	// are the LZ4HC levels (above 12: 12).
	CompressionLevel int
	AutoFlush        bool // compress each input at once, without buffering a block
	FavorDecSpeed    bool // levels 10 to 12: prefer faster decoding
	// NbWorkers: 0 compresses on the calling goroutine; from 1, the content
	// is cut into jobs (1 MB, or one block when larger) compressed by up to
	// NbWorkers goroutines (at most 1024) and emitted in order (C:
	// nbWorkers). The frame is C's for the same content and flush points
	// whatever NbWorkers (from 1) and the scheduling: with independent
	// blocks it is the single-threaded frame; with linked blocks each job
	// restarts with the previous 64 KB as dictionary (C's lz4 -T), which
	// differs slightly from the single-threaded frame. Output lags behind
	// input: Update, Flush and End emit the jobs completed, the dst bounds
	// (CompressBoundFrame) count them.
	NbWorkers int
}

// Frame errors (C: LZ4F_errorCodes).
var (
	ErrMaxBlockSize        = errors.New("lz4: invalid block size")     // maxBlockSize_invalid
	ErrFrameSize           = errors.New("lz4: wrong frame size")       // frameSize_wrong
	ErrStage               = errors.New("lz4: frame not started")      // compressionState_uninitialized
	ErrFrameType           = errors.New("lz4: unknown frame type")     // frameType_unknown
	ErrHeaderVersion       = errors.New("lz4: wrong frame version")    // headerVersion_wrong
	ErrReservedFlag        = errors.New("lz4: reserved flag set")      // reservedFlag_set
	ErrHeaderChecksum      = errors.New("lz4: bad header checksum")    // headerChecksum_invalid
	ErrBlockChecksum       = errors.New("lz4: bad block checksum")     // blockChecksum_invalid
	ErrContentChecksum     = errors.New("lz4: bad content checksum")   // contentChecksum_invalid
	ErrDecompressionFailed = errors.New("lz4: block decoding failed")  // decompressionFailed
	ErrDstTooSmallFrame    = errors.New("lz4: frame does not fit dst") // dstCapacity_tooSmall
)

const (
	frameMagic         = 0x184D2204 // C: LZ4F_MAGICNUMBER
	skippableMagic     = 0x184D2A50 // C: LZ4F_MAGIC_SKIPPABLE_START
	minFHSize          = 7
	maxFHSize          = 19
	bhSize             = 4 // block header
	bfSize             = 4 // block checksum
	blockUncompressed  = 0x80000000
	blockSizeIDDefault = Block64KB
)

// BlockSize returns the maximum block size of id (C: LZ4F_getBlockSize).
func BlockSize(id BlockSizeID) (int, error) {
	if id == 0 {
		id = blockSizeIDDefault
	}
	if id < Block64KB || id > Block4MB {
		return 0, ErrMaxBlockSize
	}
	return 64 * kb << (2 * (id - Block64KB)), nil
}

func headerChecksum(h []byte) byte { return byte(xxhash.Sum32(h, 0) >> 8) }

// C: LZ4F_optimalBSID
func optimalBSID(requested BlockSizeID, srcSize int) BlockSizeID {
	proposed := Block64KB
	maxBlockSize := 64 * kb
	for requested > proposed {
		if srcSize <= maxBlockSize {
			return proposed
		}
		proposed++
		maxBlockSize <<= 2
	}
	return requested
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// C: LZ4F_compressBound_autoFlush
func compressBoundAutoFlush(srcSize int, p *Preferences) int {
	blockSize, _ := BlockSize(p.BlockSizeID)
	nbFullBlocks := srcSize / blockSize
	lastBlockSize := srcSize & (blockSize - 1)
	nbBlocks := nbFullBlocks + b2i(lastBlockSize > 0)
	blockCRCSize := bfSize * b2i(p.BlockChecksum)
	frameEnd := bhSize + b2i(p.ContentChecksum)*bfSize
	return (bhSize+blockCRCSize)*nbBlocks + blockSize*nbFullBlocks + lastBlockSize + frameEnd
}

// CompressFrameBound returns the largest frame CompressFrame produces for
// srcSize bytes (C: LZ4F_compressFrameBound). p may be nil.
func CompressFrameBound(srcSize int, p *Preferences) int {
	var prefs Preferences
	if p != nil {
		prefs = *p
	}
	prefs.AutoFlush = true
	if _, err := BlockSize(prefs.BlockSizeID); err != nil {
		prefs.BlockSizeID = Block4MB
	}
	return maxFHSize + compressBoundAutoFlush(srcSize, &prefs)
}

// CompressBoundFrame returns the largest output of one Writer step
// compressing srcSize bytes, flush and end included (C: LZ4F_compressBound;
// with NbWorkers and without AutoFlush the jobs srcSize starts, plus every
// job in flight, which C emits over several calls).
func CompressBoundFrame(srcSize int, p *Preferences) int {
	if p != nil && !p.AutoFlush && mtEligible(p) {
		return mtCompressBound(srcSize, p)
	}
	if p != nil && p.AutoFlush {
		pp := *p
		if _, err := BlockSize(pp.BlockSizeID); err != nil {
			pp.BlockSizeID = Block4MB
		}
		return compressBoundAutoFlush(srcSize, &pp) + b2i(srcSize == 0)
	}
	blockCRCSize, contentCRCSize := bfSize, bfSize
	id := BlockDefault
	if p != nil {
		blockCRCSize = bfSize * b2i(p.BlockChecksum)
		contentCRCSize = bfSize * b2i(p.ContentChecksum)
		id = p.BlockSizeID
	}
	blockSize, err := BlockSize(id)
	if err != nil {
		blockSize = 4 << 20
	}
	nbBlocks := srcSize/blockSize + 1
	return nbBlocks*(blockSize+bhSize+blockCRCSize) + bhSize + contentCRCSize
}

// CDict is a dictionary digested for frame compression (C: LZ4F_CDict).
// It can be shared by concurrent compressions.
type CDict struct {
	content []byte
	fast    *Stream
	hc      *StreamHC
}

// NewCDict digests the last 64 KB of dict (copied) for CompressFrameUsingCDict
// and WithCDict (C: LZ4F_createCDict).
func NewCDict(dict []byte) *CDict {
	if len(dict) > 64*kb {
		dict = dict[len(dict)-64*kb:]
	}
	d := &CDict{content: append([]byte(nil), dict...), fast: new(Stream), hc: new(StreamHC)}
	d.fast.LoadDictSlow(d.content)
	d.hc.Reset(LevelHCDefault)
	d.hc.LoadDict(d.content)
	return d
}

type ctxType uint8

const (
	ctxNone ctxType = iota
	ctxFast
	ctxHC
)

type blockCompressMode uint8

const (
	compressedMode blockCompressMode = iota
	uncompressedMode
)

// frameCCtx is C's LZ4F_cctx (single-threaded).
type frameCCtx struct {
	prefs        Preferences
	cStage       int
	cdict        *CDict
	maxBlockSize int
	tmpBuff      []byte // len is C's tmpBuffCapacity
	tmpIn        int    // offset of tmpIn within tmpBuff
	tmpInSize    int
	totalInSize  uint64
	xxh          xxhash.Digest32
	fast         *Stream
	hc           *StreamHC
	ctxType      ctxType
	mode         blockCompressMode
	rawDict      CDict
	rawDictFast  *Stream
	rawDictHC    *StreamHC
	mt           *frameMT // multi-threaded frames (kept across frames)
	mtActive     bool     // the current frame is cut into jobs
}

// C: LZ4F_initStream
func (c *frameCCtx) initStream(cdict *CDict, level int, independent bool) {
	if level < LevelHCMin {
		if cdict != nil {
			c.fast.ResetFast()
			c.fast.AttachDictionary(cdict.fast)
		} else if !independent {
			c.fast.Reset()
		}
	} else {
		c.hc.ResetFast(level)
		if cdict != nil {
			c.hc.AttachDictionary(cdict.hc)
		}
	}
}

// C: LZ4F_startStream
func (c *frameCCtx) startStream(cdict *CDict, dict []byte) {
	level := c.prefs.CompressionLevel
	if !c.prefs.BlockIndependent {
		c.initStream(cdict, level, false)
	}
	if level >= LevelHCMin {
		c.hc.FavorDecompressionSpeed(c.prefs.FavorDecSpeed)
	}
	if dict != nil {
		if level < LevelHCMin {
			c.fast.LoadDict(dict)
		} else {
			c.hc.LoadDict(dict)
		}
	}
}

// C: LZ4F_digestRawDict
func (c *frameCCtx) digestRawDict(dict []byte) *CDict {
	if len(dict) > 64*kb {
		dict = dict[len(dict)-64*kb:]
	}
	c.rawDict = CDict{}
	if c.prefs.CompressionLevel < LevelHCMin {
		if c.rawDictFast == nil {
			c.rawDictFast = new(Stream)
		}
		c.rawDictFast.Reset()
		c.rawDictFast.LoadDict(dict)
		c.rawDict.fast = c.rawDictFast
	} else {
		if c.rawDictHC == nil {
			c.rawDictHC = new(StreamHC)
		}
		c.rawDictHC.Reset(c.prefs.CompressionLevel)
		c.rawDictHC.LoadDict(dict)
		c.rawDict.hc = c.rawDictHC
	}
	return &c.rawDict
}

// begin is C's LZ4F_compressBegin_internal: it writes the frame header
// into dst (at least maxFHSize bytes) and returns its size.
func (c *frameCCtx) begin(dst []byte, dict []byte, cdict *CDict, p *Preferences) (int, error) {
	var prefs Preferences
	if p != nil {
		prefs = *p
	}
	headerSize := minFHSize + 8*b2i(prefs.ContentSize != 0) + 4*b2i(prefs.DictID != 0)
	if len(dst) < headerSize {
		return 0, ErrDstTooSmallFrame
	}
	if _, err := BlockSize(prefs.BlockSizeID); err != nil {
		return 0, err
	}
	if c.mtActive {
		c.mt.abort() // jobs of an abandoned frame
		c.mtActive = false
	}
	c.cStage = 0
	c.prefs = prefs
	mtFrame := mtEligible(&prefs)

	want := ctxFast
	if prefs.CompressionLevel >= LevelHCMin {
		want = ctxHC
	}
	if c.ctxType != want {
		if want == ctxFast {
			if c.fast == nil {
				c.fast = new(Stream)
			}
			c.fast.Reset()
		} else {
			if c.hc == nil {
				c.hc = new(StreamHC)
			}
			c.hc.Reset(prefs.CompressionLevel)
		}
		c.ctxType = want
	}

	// a raw dictionary is a CDict for independent blocks and for jobs
	if dict != nil && (prefs.BlockIndependent || mtFrame) {
		cdict = c.digestRawDict(dict)
		dict = nil
	}

	if c.prefs.BlockSizeID == 0 {
		c.prefs.BlockSizeID = blockSizeIDDefault
	}
	c.maxBlockSize, _ = BlockSize(c.prefs.BlockSizeID)
	{
		linked := 0
		if !c.prefs.BlockIndependent {
			linked = 128 * kb
		}
		required := linked
		if !prefs.AutoFlush {
			required = c.maxBlockSize + linked
		}
		if mtFrame {
			required = 0 // jobs have their own buffers
		}
		if len(c.tmpBuff) < required {
			c.tmpBuff = make([]byte, required)
		}
	}
	c.tmpIn = 0
	c.tmpInSize = 0
	if dict != nil {
		if len(dict) > 64*kb {
			dict = dict[len(dict)-64*kb:]
		}
		copy(c.tmpBuff, dict)
		c.tmpIn = len(dict)
		dict = c.tmpBuff[:len(dict)]
	}
	c.xxh.Reset(0)

	c.cdict = cdict
	c.mode = compressedMode
	c.startStream(cdict, dict)

	binary.LittleEndian.PutUint32(dst, frameMagic)
	op := 4
	dst[op] = byte(1<<6 | b2i(c.prefs.BlockIndependent)<<5 | b2i(c.prefs.BlockChecksum)<<4 |
		b2i(c.prefs.ContentSize > 0)<<3 | b2i(c.prefs.ContentChecksum)<<2 | b2i(c.prefs.DictID > 0))
	dst[op+1] = byte((c.prefs.BlockSizeID & 7) << 4)
	op += 2
	if c.prefs.ContentSize != 0 {
		binary.LittleEndian.PutUint64(dst[op:], c.prefs.ContentSize)
		op += 8
		c.totalInSize = 0
	}
	if c.prefs.DictID != 0 {
		binary.LittleEndian.PutUint32(dst[op:], c.prefs.DictID)
		op += 4
	}
	dst[op] = headerChecksum(dst[4:op])
	op++
	if mtFrame {
		if c.mt == nil {
			c.mt = new(frameMT)
		}
		c.mt.begin(c)
		c.mtActive = true
	}
	c.cStage = 1
	return op, nil
}

// dstEmitter appends job output to dst (the dst-based API).
type dstEmitter struct {
	dst []byte
	op  int
}

func (e *dstEmitter) emit(b []byte) error {
	if len(e.dst)-e.op < len(b) {
		return ErrDstTooSmallFrame
	}
	e.op += copy(e.dst[e.op:], b)
	return nil
}

// mtFail ends a frame whose job or output failed (C: LZ4F_invalidateFrame).
func (c *frameCCtx) mtFail(err error) error {
	c.mt.abort()
	c.mtActive = false
	c.cStage = 0
	return err
}

// mtUpdate is LZ4F_compressUpdate for a frame cut into jobs, emitting
// output through emit.
func (c *frameCCtx) mtUpdate(src []byte, stable bool, mode blockCompressMode, emit func([]byte) error) error {
	if c.cStage != 1 {
		return ErrStage
	}
	if err := c.mt.update(src, stable, mode, emit); err != nil {
		return c.mtFail(err)
	}
	if c.prefs.ContentChecksum {
		c.xxh.Write(src)
	}
	c.totalInSize += uint64(len(src))
	return nil
}

// mtFlush is LZ4F_flush for a frame cut into jobs: every job is emitted.
func (c *frameCCtx) mtFlush(emit func([]byte) error) error {
	if c.cStage != 1 {
		return ErrStage
	}
	if err := c.mt.flush(emit); err != nil {
		return c.mtFail(err)
	}
	return nil
}

// mtEnd is LZ4F_compressEnd for a frame cut into jobs: the jobs through
// emit, then the end mark and checksum into tail (8 bytes).
func (c *frameCCtx) mtEnd(tail []byte, emit func([]byte) error) (int, error) {
	if c.cStage != 1 {
		return 0, ErrStage
	}
	if c.prefs.ContentSize != 0 && c.prefs.ContentSize != c.totalInSize {
		return 0, c.mtFail(ErrFrameSize)
	}
	if err := c.mt.flush(emit); err != nil {
		return 0, c.mtFail(err)
	}
	binary.LittleEndian.PutUint32(tail, 0)
	op := 4
	if c.prefs.ContentChecksum {
		binary.LittleEndian.PutUint32(tail[op:], c.xxh.Sum32())
		op += 4
	}
	c.mtActive = false
	c.cStage = 0
	return op, nil
}

// C: LZ4F_levelToAcceleration
func levelToAcceleration(level int) int {
	if level >= 0 {
		return 1
	}
	if level < 1-AccelerationMax {
		return AccelerationMax
	}
	return -level + 1
}

// compressBlockFunc is C's compressFunc_t selected by LZ4F_selectCompression.
func (c *frameCCtx) compress(src, dst []byte) int {
	if c.mode == uncompressedMode {
		return 0
	}
	level := c.prefs.CompressionLevel
	cdict := c.cdict
	if level < LevelHCMin {
		acceleration := levelToAcceleration(level)
		if c.prefs.BlockIndependent {
			c.initStream(cdict, level, true)
			if cdict != nil {
				return c.fast.CompressFastContinue(dst, src, acceleration)
			}
			return c.fast.CompressFast(dst, src, acceleration)
		}
		return c.fast.CompressFastContinue(dst, src, acceleration)
	}
	if c.prefs.BlockIndependent {
		c.initStream(cdict, level, true)
		if cdict != nil {
			return c.hc.CompressContinue(dst, src)
		}
		return c.hc.Compress(dst, src, level)
	}
	return c.hc.CompressContinue(dst, src)
}

// makeBlock is C's LZ4F_makeBlock: one block with its header and checksum.
func (c *frameCCtx) makeBlock(dst, src []byte) (int, error) {
	srcSize := len(src)
	crcFlag := c.prefs.BlockChecksum
	overhead := bhSize + bfSize*b2i(crcFlag)
	cCapacity := 1
	if srcSize > 1 {
		cCapacity = srcSize - 1
	}
	if len(dst) <= overhead {
		return 0, ErrDstTooSmallFrame
	}
	if len(dst)-overhead < cCapacity {
		cCapacity = len(dst) - overhead
	}
	cSize := c.compress(src, dst[bhSize:bhSize+cCapacity])
	if cSize == 0 || cSize >= srcSize {
		if len(dst)-overhead < srcSize {
			return 0, ErrDstTooSmallFrame
		}
		cSize = srcSize
		binary.LittleEndian.PutUint32(dst, uint32(cSize)|blockUncompressed)
		copy(dst[bhSize:], src)
	} else {
		binary.LittleEndian.PutUint32(dst, uint32(cSize))
	}
	if crcFlag {
		binary.LittleEndian.PutUint32(dst[bhSize+cSize:], xxhash.Sum32(dst[bhSize:bhSize+cSize], 0))
	}
	return bhSize + cSize + bfSize*b2i(crcFlag), nil
}

// C: LZ4F_compressBlocks_internal
func (c *frameCCtx) compressBlocks(dst, src []byte) (int, error) {
	op := 0
	for ip := 0; ip < len(src); {
		bSize := min(c.maxBlockSize, len(src)-ip)
		n, err := c.makeBlock(dst[op:], src[ip:ip+bSize])
		if err != nil {
			return 0, err
		}
		ip += bSize
		op += n
	}
	return op, nil
}

// C: LZ4F_localSaveDict
func (c *frameCCtx) localSaveDict() {
	var dictSize int
	if c.prefs.CompressionLevel < LevelHCMin {
		dictSize = c.fast.SaveDict(c.tmpBuff[:64*kb])
	} else {
		dictSize = c.hc.SaveDict(c.tmpBuff[:64*kb])
	}
	c.tmpIn = dictSize
}

// tmpBlock returns tmpBuff[tmpIn:tmpIn+n] with the capacity of tmpBuff, so
// the next block compressed from tmpBuff continues it.
func (c *frameCCtx) tmpBlock(n int) []byte { return c.tmpBuff[c.tmpIn : c.tmpIn+n] }

// C: LZ4F_flushBlock
func (c *frameCCtx) flushBlock(dst []byte) (int, error) {
	if c.tmpInSize == 0 {
		return 0, nil
	}
	n, err := c.makeBlock(dst, c.tmpBlock(c.tmpInSize))
	if err != nil {
		c.cStage = 0
		return 0, err
	}
	if !c.prefs.BlockIndependent {
		c.tmpIn += c.tmpInSize
	}
	c.tmpInSize = 0
	if c.tmpIn+c.maxBlockSize > len(c.tmpBuff) {
		c.localSaveDict()
	}
	return n, nil
}

// update is C's LZ4F_compressUpdate (LZ4F_st_update, or the jobs); dst
// must hold CompressBoundFrame(len(src)).
func (c *frameCCtx) update(dst, src []byte, stableSrc bool, mode blockCompressMode) (int, error) {
	if c.cStage != 1 {
		return 0, ErrStage
	}
	if c.mtActive {
		e := dstEmitter{dst: dst}
		err := c.mtUpdate(src, stableSrc, mode, e.emit)
		return e.op, err
	}
	blockSize := c.maxBlockSize
	op := 0
	srcPtr := 0
	const (
		notDone = iota
		fromTmpBuffer
		fromSrcBuffer
	)
	lastBlock := notDone
	linked := !c.prefs.BlockIndependent

	if c.mode != mode {
		n, err := c.flushBlock(dst[op:])
		if err != nil {
			return 0, err
		}
		op += n
		c.mode = mode
		if mode == uncompressedMode && linked {
			c.startStream(nil, nil)
		}
	}

	if c.tmpInSize > 0 {
		sizeToCopy := blockSize - c.tmpInSize
		if sizeToCopy > len(src) {
			copy(c.tmpBuff[c.tmpIn+c.tmpInSize:], src)
			srcPtr = len(src)
			c.tmpInSize += len(src)
		} else {
			lastBlock = fromTmpBuffer
			copy(c.tmpBuff[c.tmpIn+c.tmpInSize:], src[:sizeToCopy])
			srcPtr += sizeToCopy
			n, err := c.makeBlock(dst[op:], c.tmpBlock(blockSize))
			if err != nil {
				c.cStage = 0
				return 0, err
			}
			op += n
			if linked {
				c.tmpIn += blockSize
			}
			c.tmpInSize = 0
		}
	}

	{
		remaining := len(src) - srcPtr
		toCompress := remaining
		if !c.prefs.AutoFlush {
			toCompress = remaining - remaining%blockSize
		}
		viaTmp := c.prefs.AutoFlush && linked && !stableSrc && mode == compressedMode && toCompress > 0 && toCompress < 64*kb
		if viaTmp {
			if c.tmpIn+toCompress > len(c.tmpBuff) {
				c.localSaveDict()
			}
			copy(c.tmpBuff[c.tmpIn:], src[srcPtr:srcPtr+toCompress])
			lastBlock = fromTmpBuffer
			n, err := c.makeBlock(dst[op:], c.tmpBlock(toCompress))
			if err != nil {
				c.cStage = 0
				return 0, err
			}
			op += n
			srcPtr += toCompress
			c.tmpIn += toCompress
		} else if toCompress > 0 {
			lastBlock = fromSrcBuffer
			n, err := c.compressBlocks(dst[op:], src[srcPtr:srcPtr+toCompress])
			if err != nil {
				c.cStage = 0
				return 0, err
			}
			op += n
			srcPtr += toCompress
		}
	}

	if linked && lastBlock == fromSrcBuffer {
		if stableSrc {
			c.tmpIn = 0
		} else {
			c.localSaveDict()
		}
	}

	if !c.prefs.AutoFlush && c.tmpIn+blockSize > len(c.tmpBuff) {
		c.localSaveDict()
	}

	if srcPtr < len(src) {
		n := copy(c.tmpBuff[c.tmpIn:], src[srcPtr:])
		c.tmpInSize = n
	}

	if c.prefs.ContentChecksum {
		c.xxh.Write(src)
	}
	c.totalInSize += uint64(len(src))
	return op, nil
}

// flush is C's LZ4F_flush with a large enough dst.
func (c *frameCCtx) flush(dst []byte) (int, error) {
	if c.cStage != 1 {
		return 0, ErrStage
	}
	if c.mtActive {
		e := dstEmitter{dst: dst}
		err := c.mtFlush(e.emit)
		return e.op, err
	}
	return c.flushBlock(dst)
}

// end is C's LZ4F_compressEnd with a large enough dst.
func (c *frameCCtx) end(dst []byte) (int, error) {
	if c.cStage != 1 {
		return 0, ErrStage
	}
	if c.mtActive {
		if len(dst) < 8 {
			return 0, c.mtFail(ErrDstTooSmallFrame)
		}
		e := dstEmitter{dst: dst[:len(dst)-8]}
		var tail [8]byte
		n, err := c.mtEnd(tail[:], e.emit)
		if err != nil {
			return 0, err
		}
		return e.op + copy(dst[e.op:], tail[:n]), nil
	}
	if c.prefs.ContentSize != 0 && c.prefs.ContentSize != c.totalInSize {
		c.cStage = 0
		return 0, ErrFrameSize
	}
	op, err := c.flushBlock(dst)
	if err != nil {
		return 0, err
	}
	binary.LittleEndian.PutUint32(dst[op:], 0)
	op += 4
	if c.prefs.ContentChecksum {
		binary.LittleEndian.PutUint32(dst[op:], c.xxh.Sum32())
		op += 4
	}
	c.cStage = 0
	return op, nil
}

// compressFrame is C's LZ4F_compressFrame_usingCDict (and, with a raw
// dictionary, LZ4F_compressBegin_usingDict + one update + end).
func (c *frameCCtx) compressFrame(dst, src []byte, dict []byte, cdict *CDict, p *Preferences) (int, error) {
	var prefs Preferences
	if p != nil {
		prefs = *p
	}
	if prefs.ContentSize != 0 {
		prefs.ContentSize = uint64(len(src))
	}
	prefs.BlockSizeID = optimalBSID(prefs.BlockSizeID, len(src))
	prefs.AutoFlush = true
	if bs, err := BlockSize(prefs.BlockSizeID); err == nil && len(src) <= bs {
		prefs.BlockIndependent = true
	}
	if mtEligible(&prefs) {
		// a single job is the single-threaded frame, without the setup
		if bs, err := BlockSize(prefs.BlockSizeID); err != nil || len(src) <= mtJobSize(bs) {
			prefs.NbWorkers = 0
		}
	}
	if len(dst) < CompressFrameBound(len(src), &prefs) {
		return 0, ErrDstTooSmallFrame
	}
	op, err := c.begin(dst, dict, cdict, &prefs)
	if err != nil {
		return 0, err
	}
	n, err := c.update(dst[op:], src, true, compressedMode)
	if err != nil {
		return 0, err
	}
	op += n
	n, err = c.end(dst[op:])
	if err != nil {
		return 0, err
	}
	return op + n, nil
}

// CompressFrame compresses src into dst as one frame (C:
// LZ4F_compressFrame). p may be nil (C's defaults). dst must hold
// CompressFrameBound(len(src), p) bytes. It returns the frame's size.
func CompressFrame(dst, src []byte, p *Preferences) (int, error) {
	return compressFramePooled(dst, src, nil, p)
}

// CompressFrameUsingCDict is CompressFrame with a digested dictionary (C:
// LZ4F_compressFrame_usingCDict). Set p.DictID so the decoder can tell
// which dictionary it needs.
func CompressFrameUsingCDict(dst, src []byte, cdict *CDict, p *Preferences) (int, error) {
	return compressFramePooled(dst, src, cdict, p)
}

// framePool holds the contexts of CompressFrame and
// CompressFrameUsingCDict (C: LZ4F_compressFrame's context, on its stack
// or allocated per call): a reused context gives the frame a fresh one
// does (see FrameCompressor), without allocating its tables per call.
var framePool = sync.Pool{New: func() any { return new(frameCCtx) }}

func compressFramePooled(dst, src []byte, cdict *CDict, p *Preferences) (int, error) {
	c := framePool.Get().(*frameCCtx)
	n, err := c.compressFrame(dst, src, nil, cdict, p)
	c.dropRefs()
	framePool.Put(c)
	return n, err
}

// dropRefs drops the references of an idle context (and of its job
// contexts) to the caller's input and dictionary, so that a pooled
// context does not keep them alive. It changes only state the next frame
// resets first: the fast stream's dictionary fields, which every fast
// path clears (C: LZ4_prepareTable) or resets with the table, and the HC
// stream's prefix, which every HC path drops first (C:
// LZ4_resetStreamHC_fast, done here instead; the dictionary start is set
// again when the prefix is).
func (c *frameCCtx) dropRefs() {
	c.cdict = nil
	if c.fast != nil {
		c.fast.c.dictionary, c.fast.c.dictCtx, c.fast.c.dictSize = nil, nil, 0
	}
	if c.hc != nil {
		c.hc.c.resetFast(int(c.hc.c.compressionLevel))
		c.hc.c.dict = nil
	}
	if c.mt != nil && !c.mtActive {
		c.mt.cdict, c.mt.stablePrev = nil, nil
		for _, w := range c.mt.workers {
			w.dropRefs()
		}
	}
}

// AppendFrame appends the frame of src to dst (CompressFrame with the
// allocation done).
//
// When dst lacks the room for CompressFrameBound(len(src), p) bytes, up
// to appendScratchMax of them, the frame is made in a pooled buffer and
// appended to dst in an array of exactly the bytes of dst and the frame,
// rather than of the bound.
func AppendFrame(dst, src []byte, p *Preferences) ([]byte, error) {
	n0 := len(dst)
	bound := CompressFrameBound(len(src), p)
	if cap(dst)-n0 < bound && bound <= appendScratchMax {
		bp, _ := appendScratch.Get().(*[]byte)
		if bp == nil || len(*bp) < bound {
			b := make([]byte, max(bound, 64*kb))
			bp = &b
		}
		n, err := CompressFrame(*bp, src, p)
		frame := (*bp)[:n]
		if err == nil {
			if n0 == 0 {
				dst = make([]byte, len(frame)) // (make and copy: not zeroed first)
				copy(dst, frame)
			} else {
				nd := make([]byte, n0+len(frame))
				copy(nd, dst)
				copy(nd[n0:], frame)
				dst = nd
			}
		}
		appendScratch.Put(bp)
		return dst, err
	}
	dst = growLen(dst, bound)
	n, err := CompressFrame(dst[n0:], src, p)
	if err != nil {
		return dst[:n0], err
	}
	return dst[:n0+n], nil
}

// appendScratchMax is the largest frame bound AppendFrame compresses in
// a pooled buffer (appendScratch).
const appendScratchMax = 4 << 20

var appendScratch sync.Pool

// growLen extends dst by n bytes, reallocating when the capacity is short.
func growLen(dst []byte, n int) []byte {
	if cap(dst)-len(dst) < n {
		nd := make([]byte, len(dst), len(dst)+n)
		copy(nd, dst)
		dst = nd
	}
	return dst[:len(dst)+n]
}
