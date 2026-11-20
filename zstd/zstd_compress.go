// Port of the block entropy stage of libzstd v1.5.7
// lib/compress/zstd_compress.c (Copyright (c) Meta Platforms, Inc. and
// affiliates; BSD/GPLv2 dual license).

package zstd

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"slices"

	"github.com/vahid-sohrabloo/fastcompress/internal/fse"
	"github.com/vahid-sohrabloo/fastcompress/internal/hist"
	"github.com/vahid-sohrabloo/fastcompress/internal/xxhash"
)

// symbolEncodingTypeStats holds the finalized symbol encoding types and the
// size of the sequences statistics.
// C: zstd_compress.c:ZSTD_symbolEncodingTypeStats_t (the error that C puts
// in size is returned separately).
type symbolEncodingTypeStats struct {
	llType, offType, mlType symbolEncodingType
	size                    int
	lastCountSize           int // accounts for a bug in 1.3.4, see entropyCompressSeqStoreInternal
	longOffsets             bool
}

// buildSequencesStatistics selects the encoding type of the three sequence
// code tables, builds next's CTables and writes their descriptions to dst.
// nbSeq must be > 0. count is the histogram scratch.
// C: zstd_compress.c:ZSTD_buildSequencesStatistics
func buildSequencesStatistics(ss *seqStore, nbSeq int, prev, next *fseCTables,
	dst []byte, strat strategy, count *[256]uint32, wksp []byte) (symbolEncodingTypeStats, error) {
	op := 0
	ctLitLength := fse.CTable(next.litlengthCTable[:])
	ctOffsetBits := fse.CTable(next.offcodeCTable[:])
	ctMatchLength := fse.CTable(next.matchlengthCTable[:])
	ofCodeTable := ss.ofCode[:nbSeq]
	llCodeTable := ss.llCode[:nbSeq]
	mlCodeTable := ss.mlCode[:nbSeq]
	var stats symbolEncodingTypeStats

	// convert length/distances into codes
	stats.longOffsets = seqToCodes(ss)

	// build CTable for Literal Lengths
	{
		max := uint32(maxLL)
		mostFrequent := hist.CountFast(count, &max, llCodeTable) // can't fail
		next.litlengthRepeatMode = prev.litlengthRepeatMode
		stats.llType = selectEncodingType(&next.litlengthRepeatMode, count[:], max, int(mostFrequent), nbSeq,
			llFSELog, prev.litlengthCTable[:], llDefaultNorm[:], llDefaultNormLog, defaultAllowed, strat)
		countSize, err := buildCTable(dst[op:], ctLitLength, llFSELog, stats.llType,
			count[:], max, llCodeTable, nbSeq, llDefaultNorm[:], llDefaultNormLog, maxLL,
			prev.litlengthCTable[:], wksp)
		if err != nil {
			return stats, err
		}
		if stats.llType == setCompressed {
			stats.lastCountSize = countSize
		}
		op += countSize
	}
	// build CTable for Offsets
	{
		max := uint32(maxOff)
		mostFrequent := hist.CountFast(count, &max, ofCodeTable) // can't fail
		// We can only use the basic table if max <= DefaultMaxOff, otherwise the offsets are too large
		policy := defaultDisallowed
		if max <= defaultMaxOff {
			policy = defaultAllowed
		}
		next.offcodeRepeatMode = prev.offcodeRepeatMode
		stats.offType = selectEncodingType(&next.offcodeRepeatMode, count[:], max, int(mostFrequent), nbSeq,
			offFSELog, prev.offcodeCTable[:], ofDefaultNorm[:], ofDefaultNormLog, policy, strat)
		countSize, err := buildCTable(dst[op:], ctOffsetBits, offFSELog, stats.offType,
			count[:], max, ofCodeTable, nbSeq, ofDefaultNorm[:], ofDefaultNormLog, defaultMaxOff,
			prev.offcodeCTable[:], wksp)
		if err != nil {
			return stats, err
		}
		if stats.offType == setCompressed {
			stats.lastCountSize = countSize
		}
		op += countSize
	}
	// build CTable for MatchLengths
	{
		max := uint32(maxML)
		mostFrequent := hist.CountFast(count, &max, mlCodeTable) // can't fail
		next.matchlengthRepeatMode = prev.matchlengthRepeatMode
		stats.mlType = selectEncodingType(&next.matchlengthRepeatMode, count[:], max, int(mostFrequent), nbSeq,
			mlFSELog, prev.matchlengthCTable[:], mlDefaultNorm[:], mlDefaultNormLog, defaultAllowed, strat)
		countSize, err := buildCTable(dst[op:], ctMatchLength, mlFSELog, stats.mlType,
			count[:], max, mlCodeTable, nbSeq, mlDefaultNorm[:], mlDefaultNormLog, maxML,
			prev.matchlengthCTable[:], wksp)
		if err != nil {
			return stats, err
		}
		if stats.mlType == setCompressed {
			stats.lastCountSize = countSize
		}
		op += countSize
	}
	stats.size = op
	return stats, nil
}

// suspectUncompressibleLiteralRatio. C: zstd_compress.c:SUSPECT_UNCOMPRESSIBLE_LITERAL_RATIO
const suspectUncompressibleLiteralRatio = 20

// entropyCompressSeqStoreInternal compresses both literals and sequences and
// returns the compressed size of the block.
// C: zstd_compress.c:ZSTD_entropyCompressSeqStore_internal. Deviation: the
// histogram (C: the first MaxSeq+1 unsigned of entropyWorkspace) is a stack
// array; the rest of the workspace is still handed on as in C. This cannot
// change output.
func entropyCompressSeqStoreInternal(dst []byte, literals []byte, ss *seqStore,
	prev, next *entropyCTables, params *cctxParams, wksp []byte, bmi2 bool) (int, error) {
	strat := params.cParams.strategy
	var count [256]uint32
	ctLitLength := fse.CTable(next.fse.litlengthCTable[:])
	ctOffsetBits := fse.CTable(next.fse.offcodeCTable[:])
	ctMatchLength := fse.CTable(next.fse.matchlengthCTable[:])
	sequences := ss.sequences
	nbSeq := len(sequences)
	op := 0
	var lastCountSize int
	longOffsets := false

	wksp = wksp[(maxSeq+1)*4:] // C: entropyWorkspace = count + (MaxSeq + 1)

	// Compress literals
	{
		numSequences := nbSeq
		// Base suspicion of uncompressibility on ratio of literals to sequences
		suspectUncompressible := numSequences == 0 || len(literals)/numSequences >= suspectUncompressibleLiteralRatio
		cSize, err := compressLiterals(dst, literals, &prev.huf, &next.huf, strat,
			literalsCompressionIsDisabled(params), suspectUncompressible, bmi2, wksp)
		if err != nil {
			return 0, err
		}
		op += cSize
	}

	// Sequences Header
	if len(dst)-op < 3+1 { // max nbSeq Size + seqHead
		return 0, errDstSizeTooSmall // Can't fit seq hdr in output buf!
	}
	switch {
	case nbSeq < 128:
		dst[op] = byte(nbSeq)
		op++
	case nbSeq < longNbSeq:
		dst[op] = byte(nbSeq>>8 + 0x80)
		dst[op+1] = byte(nbSeq)
		op += 2
	default:
		dst[op] = 0xFF
		binary.LittleEndian.PutUint16(dst[op+1:], uint16(nbSeq-longNbSeq))
		op += 3
	}
	if nbSeq == 0 {
		// Copy the old tables over as if we repeated them
		next.fse = prev.fse
		return op, nil
	}
	{
		seqHead := op
		op++
		// build stats for sequences
		stats, err := buildSequencesStatistics(ss, nbSeq, &prev.fse, &next.fse, dst[op:], strat, &count, wksp)
		if err != nil {
			return 0, err
		}
		dst[seqHead] = byte(stats.llType<<6 + stats.offType<<4 + stats.mlType<<2)
		lastCountSize = stats.lastCountSize
		op += stats.size
		longOffsets = stats.longOffsets
	}

	{
		bitstreamSize, err := encodeSequences(dst[op:], ctMatchLength, ctOffsetBits, ctLitLength,
			sequences, ss.llCode, ss.mlCode, ss.ofCode, longOffsets, bmi2)
		if err != nil {
			return 0, err
		}
		op += bitstreamSize
		// zstd versions <= 1.3.4 mistakenly report corruption when
		// FSE_readNCount() receives a buffer < 4 bytes. Fixed by
		// https://github.com/facebook/zstd/pull/1146. This can happen when
		// the last set_compressed table present is 2 bytes and the bitstream
		// is only one byte. In this exceedingly rare case, we will simply
		// emit an uncompressed block, since it isn't worth optimizing.
		if lastCountSize != 0 && lastCountSize+bitstreamSize < 4 {
			return 0, nil
		}
	}
	return op, nil
}

// entropyCompressSeqStoreWExtLitBuffer.
// C: zstd_compress.c:ZSTD_entropyCompressSeqStore_wExtLitBuffer
func entropyCompressSeqStoreWExtLitBuffer(dst []byte, literals []byte, blockSize int, ss *seqStore,
	prev, next *entropyCTables, params *cctxParams, wksp []byte, bmi2 bool) (int, error) {
	cSize, err := entropyCompressSeqStoreInternal(dst, literals, ss, prev, next, params, wksp, bmi2)
	if err == nil && cSize == 0 {
		return 0, nil
	}
	// When srcSize <= dstCapacity, there is enough space to write a raw
	// uncompressed block. Since we ran out of space, block must be not
	// compressible, so fall back to raw uncompressed block.
	if err == errDstSizeTooSmall && blockSize <= len(dst) {
		return 0, nil // block not compressed
	}
	if err != nil {
		return 0, err
	}

	// Check compressibility. C computes this in size_t, which would wrap
	// for blockSize < minGain(blockSize) = 2 (blocks of 0 or 1 byte);
	// ZSTD_buildSeqStore never passes blocks that small here (it emits
	// blocks under 7 bytes raw), so the Go int cannot go negative.
	maxCSize := blockSize - minGain(blockSize, params.cParams.strategy)
	if cSize >= maxCSize {
		return 0, nil // block not compressed
	}
	return cSize, nil
}

// entropyCompressSeqStore entropy-codes the block held in ss into dst. It
// returns the compressed size, or 0 when the block should be emitted
// uncompressed. next receives the entropy tables the block leaves behind;
// the caller makes them current only if it emits the compressed block.
// wksp must be at least entropyWorkspaceSize bytes.
// C: zstd_compress.c:ZSTD_entropyCompressSeqStore
func entropyCompressSeqStore(dst []byte, ss *seqStore, prev, next *entropyCTables,
	params *cctxParams, srcSize int, wksp []byte, bmi2 bool) (int, error) {
	return entropyCompressSeqStoreWExtLitBuffer(dst, ss.literals, srcSize, ss, prev, next, params, wksp, bmi2)
}

/*-*************************************
*  Parameters
***************************************/

// cParamMode tells how parameter adjustment treats the dictionary size.
// C: zstd_compress_internal.h:ZSTD_CParamMode_e
type cParamMode int

const (
	cpmNoAttachDict cParamMode = iota // C: ZSTD_cpm_noAttachDict
	cpmAttachDict                     // C: ZSTD_cpm_attachDict
	cpmCreateCDict                    // C: ZSTD_cpm_createCDict
	cpmUnknown                        // C: ZSTD_cpm_unknown
)

// rowMatchFinderSupported. C: zstd_compress.c:ZSTD_rowMatchFinderSupported
func rowMatchFinderSupported(strat strategy) bool {
	return strat >= zstdGreedy && strat <= zstdLazy2
}

// rowMatchFinderUsed. mode must be resolved (not psAuto).
// C: zstd_compress.c:ZSTD_rowMatchFinderUsed
func rowMatchFinderUsed(strat strategy, mode paramSwitch) bool {
	return rowMatchFinderSupported(strat) && mode == psEnable
}

// resolveRowMatchFinderMode. C: zstd_compress.c:ZSTD_resolveRowMatchFinderMode
func resolveRowMatchFinderMode(mode paramSwitch, cParams *compressionParameters) paramSwitch {
	if mode != psAuto {
		return mode
	}
	mode = psDisable
	if !rowMatchFinderSupported(cParams.strategy) {
		return mode
	}
	if cParams.windowLog > 14 {
		mode = psEnable
	}
	return mode
}

// resolveBlockSplitterMode. C: zstd_compress.c:ZSTD_resolveBlockSplitterMode
func resolveBlockSplitterMode(mode paramSwitch, cParams *compressionParameters) paramSwitch {
	if mode != psAuto {
		return mode
	}
	if cParams.strategy >= zstdBtopt && cParams.windowLog >= 17 {
		return psEnable
	}
	return psDisable
}

// allocateChainTable. C: zstd_compress.c:ZSTD_allocateChainTable
func allocateChainTable(strat strategy, useRowMatchFinder paramSwitch, forDDSDict bool) bool {
	return forDDSDict || (strat != zstdFast && !rowMatchFinderUsed(strat, useRowMatchFinder))
}

// resolveMaxBlockSize. C: zstd_compress.c:ZSTD_resolveMaxBlockSize
func resolveMaxBlockSize(maxBlockSize int) int {
	if maxBlockSize == 0 {
		return blockSizeMax
	}
	return maxBlockSize
}

// cycleLog. C: zstd_compress.c:ZSTD_cycleLog
func cycleLog(hashLog uint32, strat strategy) uint32 {
	var btScale uint32
	if strat >= zstdBtlazy2 {
		btScale = 1
	}
	return hashLog - btScale
}

// dictAndWindowLog returns a window log large enough to fit the source and
// the dictionary. srcSize must not be contentSizeUnknown.
// C: zstd_compress.c:ZSTD_dictAndWindowLog
func dictAndWindowLog(windowLog uint32, srcSize, dictSize uint64) uint32 {
	const maxWindowSize = uint64(1) << windowLogMax
	// No dictionary ==> No change
	if dictSize == 0 {
		return windowLog
	}
	windowSize := uint64(1) << windowLog
	dictAndWindowSize := dictSize + windowSize
	// If the window size is already large enough to fit both the source and
	// the dictionary then just use the window size. Otherwise adjust so
	// that it fits the dictionary and the window.
	switch {
	case windowSize >= dictSize+srcSize:
		return windowLog // Window size large enough already
	case dictAndWindowSize >= maxWindowSize:
		return windowLogMax // Larger than max window log
	default:
		return highbit32(uint32(dictAndWindowSize)-1) + 1
	}
}

// adjustCParamsInternal optimizes cPar for the given source and dictionary
// sizes: mostly downsizing to reduce memory and initialization latency.
// srcSize may be contentSizeUnknown; srcSize == 0 means 0. cPar is presumed
// valid. C: zstd_compress.c:ZSTD_adjustCParams_internal (no strategy is
// excluded from this build, so the cascade at its top is a no-op).
func adjustCParamsInternal(cPar compressionParameters, srcSize, dictSize uint64,
	mode cParamMode, useRowMatchFinder paramSwitch) compressionParameters {
	const minSrcSize = 513 // (1<<9) + 1
	const maxWindowResize = uint64(1) << (windowLogMax - 1)

	switch mode {
	case cpmUnknown, cpmNoAttachDict:
		// If we don't know the source size, don't make any assumptions
		// about it. We will already have selected smaller parameters if a
		// dictionary is in use.
	case cpmCreateCDict:
		// Assume a small source size when creating a dictionary with an
		// unknown source size.
		if dictSize != 0 && srcSize == contentSizeUnknown {
			srcSize = minSrcSize
		}
	case cpmAttachDict:
		// Dictionary has its own dedicated parameters which have already
		// been selected. We are selecting parameters for only the source.
		dictSize = 0
	}

	// resize windowLog if input is small enough, to use less memory
	if srcSize <= maxWindowResize && dictSize <= maxWindowResize {
		tSize := uint32(srcSize + dictSize)
		const hashSizeMin = 1 << hashLogMin
		srcLog := uint32(hashLogMin)
		if tSize >= hashSizeMin {
			srcLog = highbit32(tSize-1) + 1
		}
		if cPar.windowLog > srcLog {
			cPar.windowLog = srcLog
		}
	}
	if srcSize != contentSizeUnknown {
		dictAndWindowLog := dictAndWindowLog(cPar.windowLog, srcSize, dictSize)
		cycleLog := cycleLog(cPar.chainLog, cPar.strategy)
		if cPar.hashLog > dictAndWindowLog+1 {
			cPar.hashLog = dictAndWindowLog + 1
		}
		if cycleLog > dictAndWindowLog {
			cPar.chainLog -= cycleLog - dictAndWindowLog
		}
	}

	if cPar.windowLog < windowLogAbsoluteMin {
		cPar.windowLog = windowLogAbsoluteMin // minimum wlog required for valid frame header
	}

	// We can't use more than 32 bits of hash in total, so that means that we
	// require: (hashLog + 8) <= 32 && (chainLog + 8) <= 32
	if mode == cpmCreateCDict && (cPar.strategy == zstdFast || cPar.strategy == zstdDfast) {
		const maxShortCacheHashLog = 32 - 8 // C: 32 - ZSTD_SHORT_CACHE_TAG_BITS
		cPar.hashLog = min(cPar.hashLog, maxShortCacheHashLog)
		cPar.chainLog = min(cPar.chainLog, maxShortCacheHashLog)
	}

	// At this point, we aren't 100% sure if we are using the row match
	// finder. Unless it is explicitly disabled, conservatively assume that
	// it is enabled. In this case it will only be disabled for small
	// sources, so shrinking the hash log a little bit shouldn't result in
	// any ratio loss.
	if useRowMatchFinder == psAuto {
		useRowMatchFinder = psEnable
	}

	// We can't hash more than 32-bits in total. So that means that we
	// require: (hashLog - rowLog + 8) <= 32
	if rowMatchFinderUsed(cPar.strategy, useRowMatchFinder) {
		// Switch to 32-entry rows if searchLog is 5 (or more)
		rowLog := min(max(cPar.searchLog, 4), 6)
		const maxRowHashLog = 32 - rowHashTagBits
		maxHashLog := maxRowHashLog + rowLog
		if cPar.hashLog > maxHashLog {
			cPar.hashLog = maxHashLog
		}
	}
	return cPar
}

// getCParamRowSize. C: zstd_compress.c:ZSTD_getCParamRowSize
func getCParamRowSize(srcSizeHint uint64, dictSize uint64, mode cParamMode) uint64 {
	if mode == cpmAttachDict {
		dictSize = 0
	}
	unknown := srcSizeHint == contentSizeUnknown
	var addedSize uint64
	if unknown && dictSize > 0 {
		addedSize = 500
	}
	if unknown && dictSize == 0 {
		return contentSizeUnknown
	}
	return srcSizeHint + dictSize + addedSize
}

// getCParamsInternal returns the parameters of a compression level for a
// source and dictionary size. srcSizeHint 0 means 0; use contentSizeUnknown
// for unknown. C: zstd_compress.c:ZSTD_getCParams_internal
func getCParamsInternal(level int, srcSizeHint uint64, dictSize uint64, mode cParamMode) compressionParameters {
	rSize := getCParamRowSize(srcSizeHint, dictSize, mode)
	var tableID int
	for _, lim := range []uint64{256 << 10, 128 << 10, 16 << 10} {
		if rSize <= lim {
			tableID++
		}
	}
	var row int
	switch {
	case level == 0:
		row = clevelDefault // 0 == default
	case level < 0:
		row = 0 // entry 0 is baseline for fast mode
	case level > maxCLevel:
		row = maxCLevel
	default:
		row = level
	}
	cp := defaultCParameters[tableID][row]
	// acceleration factor
	if level < 0 {
		cp.targetLength = uint32(-max(minCLevel, level))
	}
	// refine parameters based on srcSize & dictSize
	return adjustCParamsInternal(cp, srcSizeHint, dictSize, mode, psAuto)
}

// getCParams returns the parameters of a compression level for a source of
// srcSizeHint bytes (contentSizeUnknown if unknown) and a dictionary of
// dictSize bytes, as a context without an attached dictionary uses them.
// C: zstd_compress.c:ZSTD_getCParams_internal with ZSTD_cpm_noAttachDict.
func getCParams(level int, srcSizeHint uint64, dictSize int) compressionParameters {
	return getCParamsInternal(level, srcSizeHint, uint64(dictSize), cpmNoAttachDict)
}

// overrideCParams. C: zstd_compress.c:ZSTD_overrideCParams
func overrideCParams(cParams *compressionParameters, overrides *compressionParameters) {
	if overrides.windowLog != 0 {
		cParams.windowLog = overrides.windowLog
	}
	if overrides.hashLog != 0 {
		cParams.hashLog = overrides.hashLog
	}
	if overrides.chainLog != 0 {
		cParams.chainLog = overrides.chainLog
	}
	if overrides.searchLog != 0 {
		cParams.searchLog = overrides.searchLog
	}
	if overrides.minMatch != 0 {
		cParams.minMatch = overrides.minMatch
	}
	if overrides.targetLength != 0 {
		cParams.targetLength = overrides.targetLength
	}
	if overrides.strategy != 0 {
		cParams.strategy = overrides.strategy
	}
}

// getCParamsFromCCtxParams. srcSizeHint == 0 means 0.
// C: zstd_compress.c:ZSTD_getCParamsFromCCtxParams (no srcSizeHint
// parameter: it keeps its C default, 0).
func getCParamsFromCCtxParams(p *cctxParams, srcSizeHint uint64, dictSize uint64, mode cParamMode) compressionParameters {
	cParams := getCParamsInternal(p.compressionLevel, srcSizeHint, dictSize, mode)
	if p.ldmParams.enableLdm == psEnable {
		cParams.windowLog = ldmDefaultWindowLog
	}
	overrideCParams(&cParams, &p.cParams)
	return adjustCParamsInternal(cParams, srcSizeHint, dictSize, mode, p.useRowMatchFinder)
}

// cctxParamsInit. C: zstd_compress.c:ZSTD_CCtxParams_init
func cctxParamsInit(p *cctxParams, level int) {
	*p = cctxParams{compressionLevel: level}
	p.fParams.contentSizeFlag = true
}

// maxNbSeq. C: zstd_compress.c:ZSTD_maxNbSeq (no external sequence producer)
func maxNbSeq(blockSize int, minMatch uint32) int {
	divider := 4
	if minMatch == 3 {
		divider = 3
	}
	return blockSize / divider
}

/*-*************************************
*  Context
***************************************/

// compressionStage. C: zstd_compress_internal.h:ZSTD_compressionStage_e
type compressionStage int

const (
	csCreated compressionStage = iota
	csInit
	csOngoing
	csEnding
)

// blockState. C: zstd_compress_internal.h:ZSTD_blockState_t
type blockState struct {
	prevCBlock *compressedBlockState
	nextCBlock *compressedBlockState
	matchState matchState
}

// cctx is a compression context. C: zstd_compress_internal.h:ZSTD_CCtx_s
//
// C carves every table and buffer out of one workspace (ZSTD_cwksp). Here
// they are plain slices kept across compressions and regrown when too small.
// That cannot change output: C's tables are either zeroed or hold indices
// from earlier compressions, which are all below the new window's lowLimit
// and so rejected exactly like zeros (see resetMatchState). Every table the
// matchState owns goes through resizeTable, which keeps that true for
// storage beyond a table's current length too.
type cctx struct {
	requestedParams cctxParams
	appliedParams   cctxParams

	pledgedSrcSizePlusOne uint64 // this way, 0 (default) == unknown
	consumedSrcSize       uint64
	producedCSize         uint64
	blockSizeMax          int
	stage                 compressionStage
	isFirstBlock          bool
	initialized           bool
	dictID                uint32

	xxhState *xxhash.Digest

	// Dictionary. C: localDict and cdict (no prefix dictionary: the
	// referenced-prefix API is not exposed).
	localDict localDict
	cdict     *cdict

	blockState blockState
	cBlocks    [2]compressedBlockState
	seqStore   seqStore

	// Long-distance matching. C: ldmState, ldmSequences, maxNbLdmSequences
	ldmState          ldmState // long distance matching state
	ldmSequences      []rawSeq // storage for the ldm output sequences
	maxNbLdmSequences int
	// mt is the multithreaded compressor, created on the first frame
	// with nbWorkers >= 1 (C: mtctx).
	mt *mtCCtx
	// externSeqStore holds the long-distance matches zstdmt's serial stage
	// found for a job (C: externSeqStore, ZSTD_referenceExternalSequences);
	// empty otherwise.
	externSeqStore rawSeqStore

	tmpWorkspace []byte         // C: tmpWorkspace, used for entropy
	splitStats   *fpStats       // pre-block-splitter workspace (C: tmpWorkspace too)
	blockSplit   *blockSplitCtx // post-block-splitter workspace (C: blockSplitCtx)
	// splitBlocks counts the blocks the post-block splitter cut into more
	// than one partition (a test statistic; C has no equivalent).
	splitBlocks int
	bmi2        bool
	hugePages   bool // WithHugePages: back the large match tables with huge pages

	// Streaming. C's inBufferMode and outBufferMode are always
	// ZSTD_bm_buffered here (the stable modes are not exposed).
	inBuff             []byte // C: inBuff; cap may exceed inBuffSize
	inBuffSize         int
	inToCompress       int
	inBuffPos          int
	inBuffTarget       int
	outBuff            []byte // C: outBuff; cap may exceed outBuffSize
	outBuffSize        int
	outBuffContentSize int
	outBuffFlushedSize int
	streamStage        streamStage
	frameEnded         bool
}

// streamStage. C: zstd_compress_internal.h:ZSTD_cStreamStage
type streamStage int

const (
	zcssInit  streamStage = iota // C: zcss_init
	zcssLoad                     // C: zcss_load
	zcssFlush                    // C: zcss_flush
)

// endDirective. C: zstd.h:ZSTD_EndDirective
type endDirective int

const (
	eContinue endDirective = iota // C: ZSTD_e_continue: collect more data, the encoder decides when to output
	eFlush                        // C: ZSTD_e_flush: flush any data provided so far
	eEnd                          // C: ZSTD_e_end: flush any remaining data and close the current frame
)

// newCCtx returns a context with default parameters (level 3, content size
// flag on, no checksum). C: zstd_compress.c:ZSTD_createCCtx
func newCCtx() *cctx {
	c := &cctx{xxhState: xxhash.New(0)}
	cctxParamsInit(&c.requestedParams, clevelDefault)
	c.blockState.prevCBlock = &c.cBlocks[0]
	c.blockState.nextCBlock = &c.cBlocks[1]
	return c
}

// setLevel sets the compression level; 0 means the default (3) and values
// are clamped to [minCLevel, maxCLevel].
// C: zstd_compress.c:ZSTD_CCtxParams_setParameter(ZSTD_c_compressionLevel)
func (c *cctx) setLevel(level int) {
	level = min(max(level, minCLevel), maxCLevel)
	if level == 0 {
		level = clevelDefault
	}
	c.requestedParams.compressionLevel = level
}

// setChecksum. C: ZSTD_CCtxParams_setParameter(ZSTD_c_checksumFlag)
func (c *cctx) setChecksum(on bool) { c.requestedParams.fParams.checksumFlag = on }

// setWindowLog overrides the level's window log; 0 restores the default.
// C: ZSTD_CCtxParams_setParameter(ZSTD_c_windowLog)
func (c *cctx) setWindowLog(windowLog uint32) error {
	if windowLog != 0 && (windowLog < windowLogMin || windowLog > windowLogMax) {
		return errors.New("zstd: window log out of range")
	}
	c.requestedParams.cParams.windowLog = windowLog
	return nil
}

// setPledgedSrcSize declares the size of the next frame's content.
// compress2 ignores it: like C's ZSTD_compress2 it pledges len(src).
// C: zstd_compress.c:ZSTD_CCtx_setPledgedSrcSize
func (c *cctx) setPledgedSrcSize(n uint64) { c.pledgedSrcSizePlusOne = n + 1 }

// reset abandons the current frame, keeping the parameters.
// C: zstd_compress.c:ZSTD_CCtx_reset(ZSTD_reset_session_only)
func (c *cctx) reset() {
	c.streamStage = zcssInit
	c.pledgedSrcSizePlusOne = 0
}

// compress2 compresses src into one frame appended to dst, with the
// requested parameters and the content size pledged.
// C: zstd_compress.c:ZSTD_compress2, via ZSTD_compressStream2(ZSTD_e_end):
// ZSTD_CCtx_init_compressStream2, then ZSTD_compressStream_generic's
// shortcut that compresses straight into the output buffer (or, with
// nbWorkers >= 1 and more than 512 KiB of input, zstdmt). Its capacity is
// ZSTD_compressBound(len(src)), what a caller sizing dst with
// ZSTD_compressBound gives C (output can depend on it only through
// dstSize_tooSmall fallbacks, which this capacity rules out except where C
// would hit them too).
func (c *cctx) compress2(dst, src []byte) ([]byte, error) {
	c.reset()
	if err := c.initCompressStream2(eEnd, len(src), false); err != nil {
		return dst, err
	}
	n0 := len(dst)
	if bound := compressBound(len(src)); c.appliedParams.nbWorkers == 0 && cap(dst)-n0 < bound && bound <= maxPooledOut {
		// dst has no room for the worst case (typically dst == nil).
		// Growing it by compressBound would allocate about len(src) bytes
		// for a result a fraction of that, which the caller keeps: garbage
		// and pinned memory (about 80% more bytes allocated than needed on
		// restic's blobs). Compress into a pooled scratch buffer and append
		// only the frame. The output is the same: C's capacity for it is
		// still compressBound.
		p := getPooledOut(bound)
		n, err := c.compressEnd((*p)[:bound], src)
		c.reset()
		if err == nil {
			dst = append(dst, (*p)[:n]...)
		}
		putPooledOut(p)
		return dst, err
	}
	dst = slices.Grow(dst, compressBound(len(src)))
	if c.appliedParams.nbWorkers > 0 {
		// C: ZSTD_compressStream2(ZSTD_e_end) delegating to zstdmt; the
		// output buffer is the same.
		out, _, remaining, err := c.compressStreamMT(dst[n0:n0:n0+compressBound(len(src))], src, eEnd)
		if err == nil && remaining != 0 {
			err = errDstSizeTooSmall // compression not completed, due to lack of output space
		}
		if err != nil {
			c.reset()
			return dst[:n0], err
		}
		return dst[:n0+len(out)], nil
	}
	out := dst[n0 : n0+compressBound(len(src))]
	n, err := c.compressEnd(out, src)
	c.reset()
	if err != nil {
		return dst[:n0], err
	}
	return dst[:n0+n], nil
}

// initCompressStream2 resolves the parameters for the pledged size and
// starts a frame. With endOp eEnd, inSize becomes the pledged size.
// buffered selects the streaming buffers (C: ZSTD_bm_buffered in and out
// buffer modes); compress2 compresses straight from and into the caller's
// buffers (C: ZSTD_compress2 sets both modes to ZSTD_bm_stable).
// C: zstd_compress.c:ZSTD_CCtx_init_compressStream2 (no prefix
// dictionary); with nbWorkers >= 1 and a pledged size above 512 KiB (or
// unknown) it starts the multithreaded compressor instead. The CDict is always the local dictionary's, so C's
// override of the level by the CDict's does not apply.
func (c *cctx) initCompressStream2(endOp endDirective, inSize int, buffered bool) error {
	params := c.requestedParams
	if err := c.initLocalDict(); err != nil { // Init the local dict if present.
		return err
	}
	if endOp == eEnd {
		c.pledgedSrcSizePlusOne = uint64(inSize) + 1 // auto-determine pledgedSrcSize
	}
	pledgedSrcSize := c.pledgedSrcSizePlusOne - 1
	{
		var dictSize uint64
		if c.cdict != nil {
			dictSize = uint64(c.cdict.dictContentSize)
		}
		mode := getCParamMode(c.cdict, pledgedSrcSize)
		params.cParams = getCParamsFromCCtxParams(&params, pledgedSrcSize, dictSize, mode)
	}
	params.postBlockSplitter = resolveBlockSplitterMode(params.postBlockSplitter, &params.cParams)
	params.ldmParams.enableLdm = resolveEnableLdm(params.ldmParams.enableLdm, &params.cParams)
	params.useRowMatchFinder = resolveRowMatchFinderMode(params.useRowMatchFinder, &params.cParams)
	params.maxBlockSize = resolveMaxBlockSize(params.maxBlockSize)
	if pledgedSrcSize <= mtJobSizeMin {
		params.nbWorkers = 0 // do not invoke multi-threading when src size is too small
	}
	if params.nbWorkers > 0 {
		if c.mt == nil {
			c.mt = newMTCCtx(params.nbWorkers)
		}
		if err := c.mt.initCStream(c.cdict, params, pledgedSrcSize, c.hugePages); err != nil {
			return err
		}
		c.consumedSrcSize = 0
		c.producedCSize = 0
		c.streamStage = zcssLoad
		c.appliedParams = params
		return nil
	}
	if err := c.compressBeginInternal(&params, pledgedSrcSize, buffered); err != nil {
		return err
	}
	c.inToCompress = 0
	c.inBuffPos = 0
	if buffered {
		// for small input: avoid automatic flush on reaching end of block,
		// since it would require to add a 3-bytes null block to end frame
		c.inBuffTarget = c.blockSizeMax
		if uint64(c.blockSizeMax) == pledgedSrcSize {
			c.inBuffTarget++
		}
	} else {
		c.inBuffTarget = 0
	}
	c.outBuffContentSize, c.outBuffFlushedSize = 0, 0
	c.streamStage = zcssLoad
	c.frameEnded = false
	return nil
}

// compressBeginInternal starts a frame, from c.cdict if set.
// C: zstd_compress.c:ZSTD_compressBegin_internal with the context's cdict
// and no raw dict. C starts from the CDict (ZSTD_resetCCtx_usingCDict)
// when its content is not empty and the source is small or of unknown size
// or the CDict has no compression level. CDicts here always come from
// ZSTD_initLocalDict, whose CDicts have no level (ZSTD_NO_CLEVEL), and are
// only created for non-empty dictionaries, so every frame with a dictionary
// starts from the CDict: C's other path, loading the dictionary content
// into the context (ZSTD_dtlm_fast, ZSTD_tfp_forCCtx), is unreachable.
func (c *cctx) compressBeginInternal(params *cctxParams, pledgedSrcSize uint64, buffered bool) error {
	if cd := c.cdict; cd != nil {
		c.resetCCtxUsingCDict(cd, params, pledgedSrcSize, buffered)
		return nil
	}
	c.resetCCtxInternal(params, pledgedSrcSize, 0, buffered)
	c.dictID = 0 // C: ZSTD_compress_insertDictionary of no dictionary
	return nil
}

// indexTooCloseToMax prefers clearing the tables over reducing indices when
// reusing a context for many small inputs.
// C: zstd_compress.c:ZSTD_indexTooCloseToMax
func indexTooCloseToMax(w *window) bool {
	return w.nextSrc > currentMax-indexOverflowMargin
}

// dictTooBig. C: zstd_compress.c:ZSTD_dictTooBig
func dictTooBig(loadedDictSize int) bool { return loadedDictSize > chunkSizeMax }

// resetCCtxInternal prepares the context for a new frame. params are
// assumed fully validated. C: zstd_compress.c:ZSTD_resetCCtx_internal with
// ZSTDcrp_makeClean.
//
// Deviation: C also restarts the indices (ZSTDirp_reset) whenever it
// reallocates its workspace (too small, or oversized for too long). This
// port grows its slices independently and restarts only for the other
// reasons. That cannot change output: restarting or continuing shifts every
// index of the new frame uniformly, all window arithmetic is relative, and
// old table entries stay below lowLimit either way.
//
// The binary tree (btlazy2) also compares indices with btLow = curr -
// btMask, clamped to 0 when curr <= btMask. C's restarted indices start
// at lowLimit = windowStartIndex, so where C clamps the continued btLow is
// below lowLimit; every candidate is above windowLow >= lowLimit, so both
// sides see "within the tree" (and unsortLimit = max(btLow, windowLow) is
// windowLow on both). Otherwise btLow shifts uniformly with the indices.
// The load-bearing invariant: every binary-tree read is guarded by
// `matchIndex > unsortLimit` (dubtFindBestMatch's unsorted walk and its
// nullify step) or `matchIndex > windowLow` (insertDUBT1 and the
// longest-match walk), and both limits are >= lowLimit. So an entry is read
// only for a position inserted in this frame (updateDUBT writes both of its
// entries), and stale entries, including stale unsorted marks, are never
// read; stale
// values reached through hashTable are below lowLimit and rejected like C's
// zeros. Overflow correction may then trigger at other blocks than in C;
// it is output-neutral too: it keeps the low cycleLog (= btLog) bits of
// every index, every index within the window, and the unsorted marks
// (reduceTable's preserveMark), as TestCompress2OverflowCorrectionBtlazy2
// checks.
//
// btopt's tree (insertBt1, insertBtAndGetAllMatches) and hashTable3 follow
// the same argument: btLow is clamped or shifted as above, and every read
// is guarded by `matchIndex >= windowLow` or `>= matchLow`, both >=
// lowLimit, so stale entries are never followed (positions skipped after a
// long match are never referenced either). TestCompress2OverflowCorrectionBtopt
// checks overflow correction.
func (c *cctx) resetCCtxInternal(params *cctxParams, pledgedSrcSize uint64, loadedDictSize int, buffered bool) {
	c.isFirstBlock = true
	// Set applied params early so we can modify them for LDM, and point
	// params at the applied params.
	c.appliedParams = *params
	params = &c.appliedParams
	if params.ldmParams.enableLdm == psEnable {
		// Adjust long distance matching parameters
		ldmAdjustParameters(&c.appliedParams.ldmParams, &params.cParams)
	}

	windowSize := max(1, min(uint64(1)<<params.cParams.windowLog, pledgedSrcSize))
	blockSize := min(params.maxBlockSize, int(windowSize))
	nbSeq := maxNbSeq(blockSize, params.cParams.minMatch)
	maxNbLdmSeq := ldmGetMaxNbSeq(params.ldmParams, blockSize)

	needsIndexReset := indexTooCloseToMax(&c.blockState.matchState.window) || dictTooBig(loadedDictSize) || !c.initialized

	// init params
	c.blockState.matchState.cParams = params.cParams
	c.pledgedSrcSizePlusOne = pledgedSrcSize + 1
	c.consumedSrcSize = 0
	c.producedCSize = 0
	if pledgedSrcSize == contentSizeUnknown {
		c.appliedParams.fParams.contentSizeFlag = false
	}
	c.blockSizeMax = blockSize

	c.xxhState.Reset()
	c.stage = csInit
	c.dictID = 0

	resetCompressedBlockState(c.blockState.prevCBlock)
	if c.hugePages && c.blockState.matchState.huge == nil {
		c.blockState.matchState.huge = newHugeTables(c)
	}
	resetMatchState(&c.blockState.matchState, &params.cParams, params.useRowMatchFinder, needsIndexReset, resetTargetCCtx)

	// ldm hash table and bucketOffsets table
	if params.ldmParams.enableLdm == psEnable {
		ls := &c.ldmState
		ldmHSize := 1 << params.ldmParams.hashLog
		ls.hashTable = resizeHugeLdmTable(c.blockState.matchState.huge, ls.hashTable, ldmHSize)
		clear(ls.hashTable)
		if cap(c.ldmSequences) < maxNbLdmSeq {
			c.ldmSequences = make([]rawSeq, maxNbLdmSeq)
		}
		c.ldmSequences = c.ldmSequences[:maxNbLdmSeq]
		c.maxNbLdmSequences = maxNbLdmSeq

		windowInit(&ls.window)
		ls.loadedDictEnd = 0

		numBuckets := 1 << (params.ldmParams.hashLog - params.ldmParams.bucketSizeLog)
		if cap(ls.bucketOffsets) < numBuckets {
			ls.bucketOffsets = make([]byte, numBuckets)
		}
		ls.bucketOffsets = ls.bucketOffsets[:numBuckets]
		clear(ls.bucketOffsets)
	}

	// sequences storage
	c.externSeqStore = rawSeqStore{} // C: ZSTD_referenceExternalSequences(zc, NULL, 0)
	ss := &c.seqStore
	if cap(ss.sequences) < nbSeq {
		ss.sequences = make([]seqDef, 0, nbSeq)
		ss.llCode = make([]byte, nbSeq)
		ss.mlCode = make([]byte, nbSeq)
		ss.ofCode = make([]byte, nbSeq)
	}
	if cap(ss.literals) < blockSize+wildcopyOverlength {
		ss.literals = make([]byte, 0, blockSize+wildcopyOverlength)
	}
	if c.tmpWorkspace == nil {
		c.tmpWorkspace = make([]byte, entropyWorkspaceSize)
	}

	// streaming buffers. C carves them out of the workspace; their content
	// from an earlier frame is never read (the window was cleared above).
	c.inBuffSize, c.outBuffSize = 0, 0
	if buffered {
		c.inBuffSize = int(windowSize) + blockSize
		c.outBuffSize = compressBound(blockSize) + 1
		if cap(c.inBuff) < c.inBuffSize {
			c.inBuff = make([]byte, c.inBuffSize)
		}
		if cap(c.outBuff) < c.outBuffSize {
			c.outBuff = make([]byte, c.outBuffSize)
		}
		c.inBuff = c.inBuff[:c.inBuffSize]
		c.outBuff = c.outBuff[:c.outBuffSize]
	}
	c.initialized = true
}

// resizeTable returns t with n entries, reusing its storage when possible.
//
// Invariant (C: ZSTD_cwksp's tableValidEnd): only t[:len(t)] is kept
// current — zeroed on an index reset, reduced on overflow correction —
// so entries in t[len(t):cap(t)] may hold indices from before a reset,
// which could lie ahead of the new window. Growing within capacity
// therefore zeroes the newly exposed part, as C cleans any table area
// beyond its previously valid end. Entries kept in t[:len(t)] are stale
// indices below the window, rejected like zeros.
func resizeTable(t []uint32, n int) []uint32 {
	if cap(t) < n {
		return make([]uint32, n)
	}
	if n > len(t) {
		clear(t[len(t):n])
	}
	return t[:n]
}

// resetTarget. C: zstd_compress.c:ZSTD_resetTarget_e
type resetTarget int

const (
	resetTargetCDict resetTarget = iota // C: ZSTD_resetTarget_CDict
	resetTargetCCtx                     // C: ZSTD_resetTarget_CCtx
)

// resetMatchState. C: zstd_compress.c:ZSTD_reset_matchState with
// ZSTDcrp_makeClean (C's ZSTDcrp_leaveDirty, for a context the CDict's
// tables are then copied into, only skips cleaning). C zeroes the tables
// when the indices restart (ZSTDirp_reset marks them dirty, then cleans
// them) and otherwise leaves whatever earlier compressions stored; so does
// this port. For a context, the row match finder's tag table is never
// cleaned (see resizeTagTable) and its hash salt advances on every reset;
// for a CDict, the tag table is zeroed and the salt is 0. The optimal
// parser's tables are allocated for contexts with strategies >= btopt and
// its statistics restart on every reset.
func resetMatchState(ms *matchState, cParams *compressionParameters, useRowMatchFinder paramSwitch, forceResetIndex bool, forWho resetTarget) {
	// disable chain table allocation for fast or row-based strategies
	var chainSize int
	if allocateChainTable(cParams.strategy, useRowMatchFinder, false) { // dedicated dictionary search is never enabled
		chainSize = 1 << cParams.chainLog
	}
	hSize := 1 << cParams.hashLog
	var hashLog3 uint32
	if forWho == resetTargetCCtx && cParams.minMatch == 3 {
		hashLog3 = min(hashLog3Max, cParams.windowLog)
	}
	var h3Size int
	if hashLog3 != 0 {
		h3Size = 1 << hashLog3
	}

	if forceResetIndex {
		windowInit(&ms.window)
	}
	ms.hashLog3 = hashLog3
	ms.lazySkipping = false

	// C: ZSTD_invalidateMatchState
	windowClear(&ms.window)
	ms.frameStart = ms.window.dictLimit
	ms.nextToUpdate = ms.window.dictLimit
	ms.loadedDictEnd = 0
	ms.opt.litLengthSum = 0 // force reset of btopt stats
	ms.dictMatchState = nil

	ms.hashTable = resizeHugeTable(ms.huge, hugeHash, ms.hashTable, hSize)
	ms.chainTable = resizeHugeTable(ms.huge, hugeChain, ms.chainTable, chainSize)
	ms.hashTable3 = resizeHugeTable(ms.huge, hugeHash3, ms.hashTable3, h3Size)
	if forceResetIndex {
		clear(ms.hashTable)
		clear(ms.chainTable)
		clear(ms.hashTable3)
	}

	if rowMatchFinderUsed(cParams.strategy, useRowMatchFinder) {
		// Row match finder needs an additional table of hashes ("tags").
		// We want to generate a new salt in case we reset a Cctx, but we
		// always want to use 0 when we reset a Cdict.
		if forWho == resetTargetCCtx {
			ms.tagTable = resizeHugeTagTable(ms.huge, ms.tagTable, hSize)
			advanceHashSalt(ms)
		} else {
			// When we are not salting we want to always memset the memory
			ms.tagTable = make([]byte, hSize)
			ms.hashSalt = 0
		}
		// Switch to 32-entry rows if searchLog is 5 (or more)
		rowLog := min(max(cParams.searchLog, 4), 6)
		ms.rowHashLog = cParams.hashLog - rowLog
	}

	// opt parser space
	if forWho == resetTargetCCtx && cParams.strategy >= zstdBtopt && ms.opt.priceTable == nil {
		ms.opt.matchTable = new([optSize]optMatch)
		ms.opt.priceTable = new([optSize]optimal)
	}
	ms.cParams = *cParams
}

/*-*************************************
*  Index reduction
***************************************/

// reduceTable reduces table indexes by reducerValue, or squashes them to
// zero. preserveMark preserves the "unsorted mark" of btlazy2.
// C: zstd_compress.c:ZSTD_reduceTable_internal
func reduceTable(table []uint32, reducerValue uint32, preserveMark bool) {
	// Protect special index values < windowStartIndex.
	reducerThreshold := reducerValue + windowStartIndex
	for i, v := range table {
		switch {
		case preserveMark && v == dubtUnsortedMark:
			// keep
		case v < reducerThreshold:
			table[i] = 0
		default:
			table[i] = v - reducerValue
		}
	}
}

// reduceIndex rescales all indexes to avoid future overflow.
// C: zstd_compress.c:ZSTD_reduceIndex
func reduceIndex(ms *matchState, params *cctxParams, reducerValue uint32) {
	reduceTable(ms.hashTable, reducerValue, false)
	if allocateChainTable(params.cParams.strategy, params.useRowMatchFinder, false) {
		reduceTable(ms.chainTable, reducerValue, params.cParams.strategy == zstdBtlazy2)
	}
	if ms.hashLog3 != 0 {
		reduceTable(ms.hashTable3, reducerValue, false)
	}
}

// overflowCorrectIfNeeded. ipIdx and iendIdx are C's ip - base and iend -
// base. C: zstd_compress.c:ZSTD_overflowCorrectIfNeeded
func overflowCorrectIfNeeded(ms *matchState, params *cctxParams, ipIdx, iendIdx uint32) {
	cycleLog := cycleLog(params.cParams.chainLog, params.cParams.strategy)
	maxDist := uint32(1) << params.cParams.windowLog
	if windowNeedOverflowCorrection(&ms.window, cycleLog, maxDist, ms.loadedDictEnd, ipIdx, iendIdx) {
		correction := windowCorrectOverflow(&ms.window, cycleLog, maxDist, ipIdx)
		reduceIndex(ms, params, correction)
		if ms.nextToUpdate < correction {
			ms.nextToUpdate = 0
		} else {
			ms.nextToUpdate -= correction
		}
		ms.frameStart -= min(ms.frameStart, correction)
		// invalidate dictionaries on overflow correction
		ms.loadedDictEnd = 0
		ms.dictMatchState = nil
	}
}

/*-*************************************
*  Block compression
***************************************/

// blockCompressor finds the sequences of the block src, whose first byte is
// at index srcIdx of ms.window, stores them into ss and returns the number
// of trailing literals. rep holds the repcodes in and out.
// C: zstd_compress_internal.h:ZSTD_BlockCompressor_f
type blockCompressor func(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32) (lastLLSize int)

// selectBlockCompressor returns the match finder for a strategy, or nil for
// strategies this port does not have yet.
// C: zstd_compress.c:ZSTD_selectBlockCompressor
func selectBlockCompressor(strat strategy, rowMatchfinder bool, mode dictMode) blockCompressor {
	if rowMatchfinder {
		switch mode {
		case noDict:
			switch strat {
			case zstdGreedy:
				return compressBlockGreedyRow
			case zstdLazy:
				return compressBlockLazyRow
			case zstdLazy2:
				return compressBlockLazy2Row
			}
		case extDict:
			switch strat {
			case zstdGreedy:
				return compressBlockGreedyExtDictRow
			case zstdLazy:
				return compressBlockLazyExtDictRow
			case zstdLazy2:
				return compressBlockLazy2ExtDictRow
			}
		case dictMatchState:
			switch strat {
			case zstdGreedy:
				return compressBlockGreedyDictMatchStateRow
			case zstdLazy:
				return compressBlockLazyDictMatchStateRow
			case zstdLazy2:
				return compressBlockLazy2DictMatchStateRow
			}
		}
		return nil
	}
	if mode == dictMatchState {
		switch strat {
		case zstdFast:
			return compressBlockFastDictMatchState
		case zstdDfast:
			return compressBlockDoubleFastDictMatchState
		case zstdGreedy:
			return compressBlockGreedyDictMatchState
		case zstdLazy:
			return compressBlockLazyDictMatchState
		case zstdLazy2:
			return compressBlockLazy2DictMatchState
		case zstdBtlazy2:
			return compressBlockBtlazy2DictMatchState
		case zstdBtopt:
			return compressBlockBtoptDictMatchState
		case zstdBtultra, zstdBtultra2:
			return compressBlockBtultraDictMatchState
		}
		return nil
	}
	if mode == extDict {
		switch strat {
		case zstdFast:
			return compressBlockFastExtDict
		case zstdDfast:
			return compressBlockDoubleFastExtDict
		case zstdGreedy:
			return compressBlockGreedyExtDict
		case zstdLazy:
			return compressBlockLazyExtDict
		case zstdLazy2:
			return compressBlockLazy2ExtDict
		case zstdBtlazy2:
			return compressBlockBtlazy2ExtDict
		case zstdBtopt:
			return compressBlockBtoptExtDict
		case zstdBtultra, zstdBtultra2:
			return compressBlockBtultraExtDict
		}
		return nil
	}
	if mode == noDict {
		switch strat {
		case zstdFast:
			return compressBlockFast
		case zstdDfast:
			return compressBlockDoubleFast
		case zstdGreedy:
			return compressBlockGreedy
		case zstdLazy:
			return compressBlockLazy
		case zstdLazy2:
			return compressBlockLazy2
		case zstdBtlazy2:
			return compressBlockBtlazy2
		case zstdBtopt:
			return compressBlockBtopt
		case zstdBtultra:
			return compressBlockBtultra
		case zstdBtultra2:
			return compressBlockBtultra2
		}
	}
	return nil
}

var errStrategyUnsupported = errors.New("zstd: compression strategy not implemented")

// buildSeqStore result. C: zstd_compress.c:ZSTD_buildSeqStore_e
const (
	bssCompress   = iota // C: ZSTDbss_compress
	bssNoCompress        // C: ZSTDbss_noCompress
)

// buildSeqStore runs the match finder over the block src at srcIdx.
// C: zstd_compress.c:ZSTD_buildSeqStore (externSeqStore is only filled by
// zstdmt's jobs, see referenceExternalSequences)
func (c *cctx) buildSeqStore(src []byte, srcIdx uint32) (int, error) {
	ms := &c.blockState.matchState
	// See C issue 3090. MIN_CBLOCK_SIZE was reduced from 3 to 2, so 1 is
	// added to compensate. This guard also keeps the entropy stage's
	// maxCSize positive (see entropyCompressSeqStoreWExtLitBuffer).
	if len(src) < minCBlockSize+blockHeaderSize+1+1 {
		if c.appliedParams.cParams.strategy >= zstdBtopt {
			ldmSkipRawSeqStoreBytes(&c.externSeqStore, len(src))
		} else {
			ldmSkipSequences(&c.externSeqStore, len(src), c.appliedParams.cParams.minMatch)
		}
		return bssNoCompress, nil // don't even attempt compression below a certain srcSize
	}
	resetSeqStore(&c.seqStore)
	// required for optimal parser to read stats from dictionary
	ms.opt.symbolCosts = &c.blockState.prevCBlock.entropy
	// tell the optimal parser how we expect to compress literals
	ms.opt.literalCompressionMode = c.appliedParams.literalCompressionMode

	// limited update after a very long match
	curr := srcIdx
	if curr > ms.nextToUpdate+384 {
		ms.nextToUpdate = curr - min(192, curr-ms.nextToUpdate-384)
	}

	// select and store sequences
	mode := matchStateDictMode(ms)
	c.blockState.nextCBlock.rep = c.blockState.prevCBlock.rep
	var lastLLSize int
	if c.externSeqStore.pos < c.externSeqStore.size {
		// Updates externSeqStore.pos
		var err error
		lastLLSize, err = ldmBlockCompress(&c.externSeqStore, ms, &c.seqStore, &c.blockState.nextCBlock.rep,
			c.appliedParams.useRowMatchFinder, src, srcIdx)
		if err != nil {
			return 0, err
		}
	} else if c.appliedParams.ldmParams.enableLdm == psEnable {
		ldmSeqStore := rawSeqStore{seq: c.ldmSequences, capacity: c.maxNbLdmSequences}
		// src's index in the LDM window: both windows end where the chunk
		// given to compressContinueInternal ends (ms.window.nextSrc).
		ldmSrcIdx := c.ldmState.window.nextSrc - (ms.window.nextSrc - srcIdx)
		// Updates ldmSeqStore.size
		if err := ldmGenerateSequences(&c.ldmState, &ldmSeqStore, &c.appliedParams.ldmParams, ldmSrcIdx, len(src)); err != nil {
			return 0, err
		}
		// Updates ldmSeqStore.pos
		var err error
		lastLLSize, err = ldmBlockCompress(&ldmSeqStore, ms, &c.seqStore, &c.blockState.nextCBlock.rep,
			c.appliedParams.useRowMatchFinder, src, srcIdx)
		if err != nil {
			return 0, err
		}
	} else { // not long range mode
		compressor := selectBlockCompressor(c.appliedParams.cParams.strategy,
			rowMatchFinderUsed(c.appliedParams.cParams.strategy, c.appliedParams.useRowMatchFinder), mode)
		if compressor == nil {
			return 0, errStrategyUnsupported
		}
		ms.ldmSeqStore = nil
		lastLLSize = compressor(ms, &c.seqStore, &c.blockState.nextCBlock.rep, src, srcIdx)
	}
	// C: ZSTD_storeLastLiterals
	c.seqStore.literals = append(c.seqStore.literals, src[len(src)-lastLLSize:]...)
	return bssCompress, nil
}

// isRLE reports whether all bytes of src are equal. C: zstd_compress.c:ZSTD_isRLE
func isRLE(src []byte) bool {
	v := src[0]
	for _, b := range src[1:] {
		if b != v {
			return false
		}
	}
	return true
}

// confirmRepcodesAndEntropyTables makes the tables and repcodes the block
// left behind current. C: ZSTD_blockState_confirmRepcodesAndEntropyTables
func (bs *blockState) confirmRepcodesAndEntropyTables() {
	bs.prevCBlock, bs.nextCBlock = bs.nextCBlock, bs.prevCBlock
}

// compressBlockInternal compresses the block src (at index srcIdx) into dst
// without a block header. It returns 0 when the block should be stored raw
// and 1 (dst[0] = the byte) for an RLE block.
// C: zstd_compress.c:ZSTD_compressBlock_internal (no sequence collector)
func (c *cctx) compressBlockInternal(dst, src []byte, srcIdx uint32, frame bool) (int, error) {
	// This is an estimated upper bound for the length of an rle block. This
	// isn't the actual upper bound. Finding the real threshold needs
	// further investigation.
	const rleMaxLength = 25
	var cSize int

	bss, err := c.buildSeqStore(src, srcIdx)
	if err != nil {
		return 0, err
	}
	if bss == bssCompress {
		// encode sequences and literals
		cSize, err = entropyCompressSeqStore(dst, &c.seqStore,
			&c.blockState.prevCBlock.entropy, &c.blockState.nextCBlock.entropy,
			&c.appliedParams, len(src), c.tmpWorkspace, c.bmi2)
		if err != nil {
			return 0, err
		}
		// We don't want to emit our first block as a RLE even if it
		// qualifies because doing so will cause the decoder (cli only) to
		// throw a "should consume all input error." This is only an issue
		// for zstd <= v1.4.3
		if frame && !c.isFirstBlock && cSize < rleMaxLength && isRLE(src) {
			cSize = 1
			dst[0] = src[0]
		}
	}

	// C: out:
	if cSize > 1 {
		c.blockState.confirmRepcodesAndEntropyTables()
	}
	// We check that dictionaries have offset codes available for the first
	// block. After the first block, the offcode table might not have large
	// enough codes to represent the offsets in the data.
	if c.blockState.prevCBlock.entropy.fse.offcodeRepeatMode == fseRepeatValid {
		c.blockState.prevCBlock.entropy.fse.offcodeRepeatMode = fseRepeatCheck
	}
	return cSize, nil
}

// writeBlockHeader24 writes a 3-byte little-endian block header.
func writeLE24(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
}

// Block types. C: zstd_internal.h:blockType_e
const (
	btRaw        = 0
	btRLE        = 1
	btCompressed = 2
)

// noCompressBlock writes a raw block. C: zstd_compress_internal.h:ZSTD_noCompressBlock
func noCompressBlock(dst, src []byte, lastBlock uint32) (int, error) {
	if len(src)+blockHeaderSize > len(dst) {
		return 0, errDstSizeTooSmall // dst buf too small for uncompressed block
	}
	writeLE24(dst, lastBlock+btRaw<<1+uint32(len(src))<<3)
	copy(dst[blockHeaderSize:], src)
	return blockHeaderSize + len(src), nil
}

// splitLevels is the pre-split level per compression strategy, from fast to
// btultra2 (indexed by strategy; entry 0 is unused). C: zstd_compress.c:ZSTD_optimalBlockSize
var splitLevels = [...]int{0, 0, 1, 2, 2, 3, 3, 4, 4, 4}

// optimalBlockSize returns the size of the next block; src is the remaining
// input. C: zstd_compress.c:ZSTD_optimalBlockSize
func (c *cctx) optimalBlockSize(src []byte, blockSizeMax int, splitLevel int, strat strategy, savings int64) int {
	// note: conservatively only split full blocks (128 KB) currently.
	if len(src) < 128<<10 || blockSizeMax < 128<<10 {
		return min(len(src), blockSizeMax)
	}
	// do not split incompressible data though: require verified savings to
	// allow pre-splitting. Note: as a consequence, the first full block is
	// not split.
	if savings < 3 {
		return 128 << 10
	}
	// apply splitLevel, or use the default value (which depends on strat).
	if splitLevel == 1 {
		return 128 << 10
	}
	if splitLevel == 0 {
		splitLevel = splitLevels[strat]
	} else {
		splitLevel -= 2
	}
	if c.splitStats == nil { // C: cctx->tmpWorkspace
		c.splitStats = new(fpStats)
	}
	return splitBlock(src, blockSizeMax, splitLevel, c.splitStats)
}

// compressFrameChunk compresses src into one or more blocks, consuming all
// of it. The frame header is already written.
// C: zstd_compress.c:ZSTD_compress_frameChunk (no targetCBlockSize)
func (c *cctx) compressFrameChunk(dst, src []byte, lastFrameChunk bool) (int, error) {
	blockSizeMax := c.blockSizeMax
	remaining := len(src)
	ip := 0
	op := 0
	maxDist := uint32(1) << c.appliedParams.cParams.windowLog
	savings := int64(c.consumedSrcSize) - int64(c.producedCSize)

	if c.appliedParams.fParams.checksumFlag && len(src) > 0 {
		c.xxhState.Write(src)
	}

	for remaining > 0 {
		ms := &c.blockState.matchState
		blockSize := c.optimalBlockSize(src[ip:], blockSizeMax, c.appliedParams.preBlockSplitterLevel, c.appliedParams.cParams.strategy, savings)
		var lastBlock uint32
		if lastFrameChunk && blockSize == remaining {
			lastBlock = 1
		}

		// See C issue 3090: 1 is added to MIN_CBLOCK_SIZE.
		if len(dst)-op < blockHeaderSize+minCBlockSize+1 {
			return 0, errDstSizeTooSmall // not enough space to store compressed block
		}

		// src ends at the window's nextSrc.
		ipIdx := ms.window.nextSrc - uint32(remaining)
		overflowCorrectIfNeeded(ms, &c.appliedParams, ipIdx, ipIdx+uint32(blockSize))
		ipIdx = ms.window.nextSrc - uint32(remaining)
		checkDictValidity(&ms.window, ipIdx+uint32(blockSize), maxDist, &ms.loadedDictEnd, &ms.dictMatchState)
		windowEnforceMaxDist(&ms.window, ipIdx, maxDist, &ms.loadedDictEnd, &ms.dictMatchState)

		// Ensure hash/chain table insertion resumes no sooner than lowlimit
		if ms.nextToUpdate < ms.window.lowLimit {
			ms.nextToUpdate = ms.window.lowLimit
		}

		block := src[ip : ip+blockSize]
		var cSize int
		if c.appliedParams.postBlockSplitter == psEnable { // C: ZSTD_blockSplitterEnabled
			var err error
			cSize, err = c.compressBlockSplitBlock(dst[op:], block, ipIdx, lastBlock)
			if err != nil {
				return 0, err
			}
		} else {
			var err error
			cSize, err = c.compressBlockInternal(dst[op+blockHeaderSize:], block, ipIdx, true)
			if err != nil {
				return 0, err
			}
			if cSize == 0 { // block is not compressible
				cSize, err = noCompressBlock(dst[op:], block, lastBlock)
				if err != nil {
					return 0, err
				}
			} else {
				var cBlockHeader uint32
				if cSize == 1 {
					cBlockHeader = lastBlock + btRLE<<1 + uint32(blockSize)<<3
				} else {
					cBlockHeader = lastBlock + btCompressed<<1 + uint32(cSize)<<3
				}
				writeLE24(dst[op:], cBlockHeader)
				cSize += blockHeaderSize
			}
		}

		// savings ensures that splitting doesn't worsen expansion of
		// incompressible data.
		savings += int64(blockSize) - int64(cSize)

		ip += blockSize
		remaining -= blockSize
		op += cSize
		c.isFirstBlock = false
	}

	if lastFrameChunk && op > 0 {
		c.stage = csEnding
	}
	return op, nil
}

// writeFrameHeader. C: zstd_compress.c:ZSTD_writeFrameHeader (format
// ZSTD_f_zstd1)
func writeFrameHeader(dst []byte, params *cctxParams, pledgedSrcSize uint64, dictID uint32) (int, error) {
	if params.fParams.noDictIDFlag {
		dictID = 0
	}
	windowSize := uint64(1) << params.cParams.windowLog
	fh := frameHeader{
		frameContentSize: contentSizeUnknown,
		windowSize:       windowSize,
		dictID:           dictID,
		checksumFlag:     params.fParams.checksumFlag,
		singleSegment:    params.fParams.contentSizeFlag && windowSize >= pledgedSrcSize,
	}
	// contentSizeFlag implies a known pledgedSrcSize (resetCCtxInternal
	// clears it otherwise); unknown here would drop the FCS field.
	if params.fParams.contentSizeFlag {
		fh.frameContentSize = pledgedSrcSize
	}
	if len(dst) < frameHeaderSizeMax {
		return 0, errDstSizeTooSmall // dst buf is too small to fit worst-case frame header size.
	}
	return len(writeFrameHeaderFields(dst[:0], fh)), nil
}

// writeFrameHeaderFields appends the header of a frame described by fh
// (zstd or skippable) to dst. The encoder's choices are C's: the shortest
// Dictionary_ID field, no window descriptor in single-segment mode, and the
// Frame_Content_Size field size C picks for the value.
// C: zstd_compress.c:ZSTD_writeFrameHeader, ZSTD_writeSkippableFrame —
// C writes from compression parameters, this from header fields, so that
// Header.AppendTo can share it. Two cases C's encoder never produces are
// written so that they decode back to fh: a known FCS below 256 without
// single-segment mode uses the 4-byte field (C's 0 code would mean
// absent), and single-segment mode without an FCS writes the 8-byte
// all-ones value C reads as unknown. The window descriptor encodes any
// windowSize a header can hold (exponent and mantissa).
func writeFrameHeaderFields(dst []byte, fh frameHeader) []byte {
	if fh.frameType == zstdSkippableFrame {
		// The variant is masked so the magic stays skippable (C's
		// ZSTD_writeSkippableFrame rejects variants above 15).
		dst = binary.LittleEndian.AppendUint32(dst, magicSkippableStart+fh.dictID&0xF)
		return binary.LittleEndian.AppendUint32(dst, uint32(fh.frameContentSize))
	}
	var dictIDSizeCode byte
	for _, lim := range []uint32{0, 255, 65535} {
		if fh.dictID > lim {
			dictIDSizeCode++
		}
	}
	hasFCS := fh.frameContentSize != contentSizeUnknown || fh.singleSegment
	var fcsCode byte
	if hasFCS {
		for _, lim := range []uint64{256, 65536 + 256, 0xFFFFFFFF} {
			if fh.frameContentSize >= lim {
				fcsCode++
			}
		}
		if fcsCode == 0 && !fh.singleSegment {
			fcsCode = 2
		}
	}
	fhd := dictIDSizeCode | fcsCode<<6
	if fh.checksumFlag {
		fhd |= 1 << 2
	}
	if fh.singleSegment {
		fhd |= 1 << 5
	}

	dst = binary.LittleEndian.AppendUint32(dst, magicNumber)
	dst = append(dst, fhd)
	if !fh.singleSegment {
		dst = append(dst, windowDescriptor(fh.windowSize))
	}
	switch dictIDSizeCode {
	case 1:
		dst = append(dst, byte(fh.dictID))
	case 2:
		dst = binary.LittleEndian.AppendUint16(dst, uint16(fh.dictID))
	case 3:
		dst = binary.LittleEndian.AppendUint32(dst, fh.dictID)
	}
	switch fcsCode {
	case 0:
		if fh.singleSegment {
			dst = append(dst, byte(fh.frameContentSize))
		}
	case 1:
		dst = binary.LittleEndian.AppendUint16(dst, uint16(fh.frameContentSize-256))
	case 2:
		dst = binary.LittleEndian.AppendUint32(dst, uint32(fh.frameContentSize))
	case 3:
		dst = binary.LittleEndian.AppendUint64(dst, fh.frameContentSize)
	}
	return dst
}

// windowDescriptor returns the Window_Descriptor byte (exponent and
// mantissa) of windowSize, rounded down to a representable size; sizes
// below the 1 KiB minimum give 0. For 1<<windowLog it is C's
// (windowLog - ZSTD_WINDOWLOG_ABSOLUTEMIN) << 3.
func windowDescriptor(windowSize uint64) byte {
	if windowSize < 1<<windowLogAbsoluteMin {
		return 0
	}
	exp := bits.Len64(windowSize) - 1
	mantissa := (windowSize - 1<<exp) >> (exp - 3)
	// No range check (Header.AppendTo does none): a window of 2^42 or more
	// wraps the exponent bits.
	return byte((exp-windowLogAbsoluteMin)<<3) | byte(mantissa)
}

// compressContinueInternal. C: zstd_compress.c:ZSTD_compressContinue_internal
// (frame mode only)
func (c *cctx) compressContinueInternal(dst, src []byte, lastFrameChunk bool) (int, error) {
	ms := &c.blockState.matchState
	fhSize := 0
	if c.stage == csCreated {
		return 0, errors.New("zstd: missing init") // ERROR(stage_wrong)
	}
	if c.stage == csInit {
		var err error
		fhSize, err = writeFrameHeader(dst, &c.appliedParams, c.pledgedSrcSizePlusOne-1, c.dictID)
		if err != nil {
			return 0, err
		}
		dst = dst[fhSize:]
		c.stage = csOngoing
	}

	if len(src) == 0 {
		return fhSize, nil // do not generate an empty block if no input
	}

	if !windowUpdate(&ms.window, src, ms.forceNonContiguous) {
		ms.forceNonContiguous = false
		ms.nextToUpdate = ms.window.dictLimit
	}
	if c.appliedParams.ldmParams.enableLdm == psEnable {
		windowUpdate(&c.ldmState.window, src, false)
	}

	cSize, err := c.compressFrameChunk(dst, src, lastFrameChunk)
	if err != nil {
		return 0, err
	}
	c.consumedSrcSize += uint64(len(src))
	c.producedCSize += uint64(cSize + fhSize)
	if c.pledgedSrcSizePlusOne != 0 { // control src size
		if c.consumedSrcSize+1 > c.pledgedSrcSizePlusOne {
			return 0, errSrcSizeWrong
		}
	}
	return cSize + fhSize, nil
}

// writeEpilogue ends the frame: an empty last block if none was written,
// then the checksum. C: zstd_compress.c:ZSTD_writeEpilogue
func (c *cctx) writeEpilogue(dst []byte) (int, error) {
	op := 0
	if c.stage == csCreated {
		return 0, errors.New("zstd: init missing") // ERROR(stage_wrong)
	}

	// special case : empty frame
	if c.stage == csInit {
		fhSize, err := writeFrameHeader(dst, &c.appliedParams, 0, 0)
		if err != nil {
			return 0, err
		}
		op += fhSize
		c.stage = csOngoing
	}

	if c.stage != csEnding {
		// write one last empty block, make it the "last" block
		if len(dst)-op < 3 {
			return 0, errDstSizeTooSmall // no room for epilogue
		}
		writeLE24(dst[op:], 1+btRaw<<1)
		op += blockHeaderSize
	}

	if c.appliedParams.fParams.checksumFlag {
		checksum := uint32(c.xxhState.Sum64())
		if len(dst)-op < 4 {
			return 0, errDstSizeTooSmall // no room for checksum
		}
		binary.LittleEndian.PutUint32(dst[op:], checksum)
		op += 4
	}

	c.stage = csCreated // return to "created but no init" status
	return op, nil
}

// compressEnd compresses the last chunk of a frame and ends it.
// C: zstd_compress.c:ZSTD_compressEnd_public
func (c *cctx) compressEnd(dst, src []byte) (int, error) {
	cSize, err := c.compressContinueInternal(dst, src, true)
	if err != nil {
		return 0, err
	}
	endResult, err := c.writeEpilogue(dst[cSize:])
	if err != nil {
		return 0, err
	}
	if c.pledgedSrcSizePlusOne != 0 { // control src size
		if c.pledgedSrcSizePlusOne != c.consumedSrcSize+1 {
			return 0, errSrcSizeWrong
		}
	}
	return cSize + endResult, nil
}

/*-*************************************
*  Streaming
***************************************/

// cStreamOutSize is the recommended size of a streaming output buffer: it
// holds one compressed block, its header and a checksum.
// C: zstd_compress.c:ZSTD_CStreamOutSize
var cStreamOutSize = compressBound(blockSizeMax) + blockHeaderSize + 4

var errInitMissing = errors.New("zstd: stream not initialized") // ERROR(init_missing)

// compressStream2 compresses in with directive op. Output is appended to
// dst within its capacity: dst[len(dst):cap(dst)] is C's free output space,
// so cap(dst) - len(dst) must be > 0 for progress. It returns dst extended
// by the output, the number of bytes of in consumed, and the number of
// bytes still to flush: with eFlush or eEnd the call must be repeated (with
// free output space) until it is 0; 0 after eEnd means the frame is
// complete. With eContinue the call must be repeated until in is consumed.
// C: zstd_compress.c:ZSTD_compressStream2 (buffered in/out modes, so no
// stable-buffer checks; nbWorkers >= 1 goes to compressStreamMT).
func (c *cctx) compressStream2(dst []byte, in []byte, op endDirective) ([]byte, int, int, error) {
	// transparent initialization stage
	if c.streamStage == zcssInit {
		if err := c.initCompressStream2(op, len(in), true); err != nil {
			return dst, 0, 0, err
		}
	}
	// compression stage
	if c.appliedParams.nbWorkers > 0 {
		return c.compressStreamMT(dst, in, op)
	}
	dst, consumed, err := c.compressStreamGeneric(dst, in, op)
	if err != nil {
		return dst, consumed, 0, err
	}
	return dst, consumed, c.outBuffContentSize - c.outBuffFlushedSize, nil // remaining to flush
}

// compressStreamGeneric. C: zstd_compress.c:ZSTD_compressStream_generic
// with inBufferMode and outBufferMode ZSTD_bm_buffered. Output goes to
// dst[len(dst):cap(dst)]; it returns dst extended and the number of bytes
// of in consumed. C's return value (the next input size hint) is unused.
func (c *cctx) compressStreamGeneric(dst []byte, in []byte, flushMode endDirective) ([]byte, int, error) {
	ip := 0
	iend := len(in)
	for someMoreWork := true; someMoreWork; {
		switch c.streamStage {
		case zcssInit:
			return dst, ip, errInitMissing // call ZSTD_initCStream() first!
		case zcssLoad:
			if flushMode == eEnd && cap(dst)-len(dst) >= compressBound(iend-ip) && // Enough output space
				c.inBuffPos == 0 {
				// shortcut to compression pass directly into output buffer
				cSize, err := c.compressEnd(dst[len(dst):cap(dst)], in[ip:iend])
				if err != nil {
					return dst, ip, err
				}
				ip = iend
				dst = dst[:len(dst)+cSize]
				c.frameEnded = true
				c.reset()
				someMoreWork = false
				continue
			}
			// complete loading into inBuffer in buffered mode
			loaded := copy(c.inBuff[c.inBuffPos:c.inBuffTarget], in[ip:iend]) // C: ZSTD_limitCopy
			c.inBuffPos += loaded
			ip += loaded
			if flushMode == eContinue && c.inBuffPos < c.inBuffTarget {
				// not enough input to fill full block : stop here
				someMoreWork = false
				continue
			}
			if flushMode == eFlush && c.inBuffPos == c.inToCompress {
				// empty
				someMoreWork = false
				continue
			}
			// compress current block (note : this stage cannot be stopped in the middle)
			{
				oSize := cap(dst) - len(dst)
				iSize := c.inBuffPos - c.inToCompress
				var cDst []byte
				direct := oSize >= compressBound(iSize)
				if direct {
					cDst = dst[len(dst):cap(dst)] // compress into output buffer, to skip flush stage
				} else {
					cDst = c.outBuff
				}
				lastBlock := flushMode == eEnd && ip == iend
				src := c.inBuff[c.inToCompress:c.inBuffPos]
				var cSize int
				var err error
				if lastBlock {
					cSize, err = c.compressEnd(cDst, src)
				} else {
					cSize, err = c.compressContinueInternal(cDst, src, false)
				}
				if err != nil {
					return dst, ip, err
				}
				c.frameEnded = lastBlock
				// prepare next block
				c.inBuffTarget = c.inBuffPos + c.blockSizeMax
				if c.inBuffTarget > c.inBuffSize {
					c.inBuffPos, c.inBuffTarget = 0, c.blockSizeMax
				}
				c.inToCompress = c.inBuffPos
				if direct { // no need to flush
					dst = dst[:len(dst)+cSize]
					if c.frameEnded {
						someMoreWork = false
						c.reset()
					}
					continue
				}
				c.outBuffContentSize = cSize
				c.outBuffFlushedSize = 0
				c.streamStage = zcssFlush // pass-through to flush stage
			}
			fallthrough
		case zcssFlush:
			toFlush := c.outBuffContentSize - c.outBuffFlushedSize
			flushed := copy(dst[len(dst):cap(dst)], c.outBuff[c.outBuffFlushedSize:c.outBuffContentSize]) // C: ZSTD_limitCopy
			dst = dst[:len(dst)+flushed]
			c.outBuffFlushedSize += flushed
			if toFlush != flushed {
				// flush not fully completed, presumably because dst is too small
				someMoreWork = false
				continue
			}
			c.outBuffContentSize, c.outBuffFlushedSize = 0, 0
			if c.frameEnded {
				someMoreWork = false
				c.reset()
				continue
			}
			c.streamStage = zcssLoad
		}
	}
	return dst, ip, nil
}
