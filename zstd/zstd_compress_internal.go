package zstd

import (
	"encoding/binary"
	"errors"
	"math/bits"

	"github.com/vahid-sohrabloo/fastcompress/internal/huf"
)

// Internal errors, mirroring the C error codes returned at the same points.
var (
	errDstSizeTooSmall = errors.New("zstd: destination buffer too small") // ERROR(dstSize_tooSmall)
	errGeneric         = errors.New("zstd: generic error")                // ERROR(GENERIC)
)

// strategy is the match-finding strategy. C: zstd.h:ZSTD_strategy
type strategy int

const (
	zstdFast strategy = 1 + iota
	zstdDfast
	zstdGreedy
	zstdLazy
	zstdLazy2
	zstdBtlazy2
	zstdBtopt
	zstdBtultra
	zstdBtultra2
)

// paramSwitch. C: zstd.h:ZSTD_ParamSwitch_e
type paramSwitch int

const (
	psAuto    paramSwitch = 0 // let the library choose
	psEnable  paramSwitch = 1 // force-enable the feature
	psDisable paramSwitch = 2 // do not use the feature
)

// compressionParameters. C: zstd.h:ZSTD_compressionParameters
type compressionParameters struct {
	windowLog, chainLog, hashLog, searchLog, minMatch, targetLength uint32
	strategy                                                        strategy
}

// frameParameters. C: zstd.h:ZSTD_frameParameters
type frameParameters struct {
	contentSizeFlag bool // content size is written into the frame header when known
	checksumFlag    bool // a 32-bit XXH64 checksum of the content ends the frame
	noDictIDFlag    bool // no dictID is written into the frame header
}

// cctxParams holds the requested or applied compression parameters. C:
// zstd_compress_internal.h:ZSTD_CCtx_params_s. Only the fields the ported
// stages read are present; later stages add theirs. The fields C has but
// this port does not (format is always ZSTD_f_zstd1, no external sequence
// producer, no targetCBlockSize, no multithreading) keep their C defaults
// implicitly.
type cctxParams struct {
	cParams                compressionParameters
	fParams                frameParameters
	compressionLevel       int
	literalCompressionMode paramSwitch
	useRowMatchFinder      paramSwitch
	postBlockSplitter      paramSwitch
	preBlockSplitterLevel  int
	maxBlockSize           int // 0 = default (C: ZSTD_resolveMaxBlockSize)
	ldmParams              ldmParams

	// Multithreading (zstdmt_compress.go). C: ZSTD_c_nbWorkers (0 =
	// single-threaded), ZSTD_c_jobSize (0 = default), ZSTD_c_overlapLog
	// (0 = default).
	nbWorkers  int
	jobSize    int
	overlapLog int
	// forceWindow makes a loaded dictionary plain history, bounded by the
	// window like the input (C: ZSTD_c_forceMaxWindow); only zstdmt sets
	// it, for its jobs after the first.
	forceWindow bool
}

// fseRepeat. C: fse.h:FSE_repeat
type fseRepeat int

const (
	fseRepeatNone  fseRepeat = iota // cannot use the previous table
	fseRepeatCheck                  // can use the previous table but it must be checked
	fseRepeatValid                  // can use the previous table and it is assumed to be valid
)

// Entropy table sizes. C: zstd_compress_internal.h:ZSTD_hufCTables_t and
// ZSTD_fseCTables_t (HUF_CTABLE_SIZE_ST(255), FSE_CTABLE_SIZE_U32(log, max)).
const (
	hufCTableSize     = 255 + 2
	offcodeCTableSize = 1 + 1<<(offFSELog-1) + (maxOff+1)*2
	mlCTableSize      = 1 + 1<<(mlFSELog-1) + (maxML+1)*2
	llCTableSize      = 1 + 1<<(llFSELog-1) + (maxLL+1)*2
)

// hufCTables. C: zstd_compress_internal.h:ZSTD_hufCTables_t
type hufCTables struct {
	cTable     [hufCTableSize]huf.CElt
	repeatMode huf.Repeat
}

// fseCTables. C: zstd_compress_internal.h:ZSTD_fseCTables_t
type fseCTables struct {
	offcodeCTable         [offcodeCTableSize]uint32
	matchlengthCTable     [mlCTableSize]uint32
	litlengthCTable       [llCTableSize]uint32
	offcodeRepeatMode     fseRepeat
	matchlengthRepeatMode fseRepeat
	litlengthRepeatMode   fseRepeat
}

// entropyCTables. C: zstd_compress_internal.h:ZSTD_entropyCTables_t. The
// tables are arrays so that assignment copies them like C's memcpy.
type entropyCTables struct {
	huf hufCTables
	fse fseCTables
}

const (
	maxHufHeaderSize = 128 // C: ZSTD_MAX_HUF_HEADER_SIZE
	// C: ZSTD_MAX_FSE_HEADERS_SIZE
	maxFSEHeadersSize = ((maxML+1)*mlFSELog + (maxLL+1)*llFSELog + (maxOff+1)*offFSELog + 7) / 8
)

// hufCTablesMetadata. C: zstd_compress_internal.h:ZSTD_hufCTablesMetadata_t
type hufCTablesMetadata struct {
	hType        symbolEncodingType
	hufDesBuffer [maxHufHeaderSize]byte
	hufDesSize   int
}

// fseCTablesMetadata. C: zstd_compress_internal.h:ZSTD_fseCTablesMetadata_t
type fseCTablesMetadata struct {
	llType, ofType, mlType symbolEncodingType
	fseTablesBuffer        [maxFSEHeadersSize]byte
	fseTablesSize          int
	lastCountSize          int // accounts for a bug in 1.3.4, see entropyCompressSeqStoreInternal
}

// entropyCTablesMetadata. C: zstd_compress_internal.h:ZSTD_entropyCTablesMetadata_t
type entropyCTablesMetadata struct {
	hufMetadata hufCTablesMetadata
	fseMetadata fseCTablesMetadata
}

// maxNbBlockSplits. C: zstd_compress_internal.h:ZSTD_MAX_NB_BLOCK_SPLITS
const maxNbBlockSplits = 196

// blockSplitCtx is the post-block splitter's workspace.
// C: zstd_compress_internal.h:ZSTD_blockSplitCtx
type blockSplitCtx struct {
	fullSeqStoreChunk  seqStore
	firstHalfSeqStore  seqStore
	secondHalfSeqStore seqStore
	currSeqStore       seqStore
	nextSeqStore       seqStore
	partitions         [maxNbBlockSplits]uint32
	entropyMetadata    entropyCTablesMetadata
}

// Workspace sizes in bytes. C: zstd_compress_internal.h
const (
	compressSequencesWorkspaceSize = 4 * (maxSeq + 2)                                   // C: COMPRESS_SEQUENCES_WORKSPACE_SIZE
	entropyWorkspaceSize           = huf.WorkspaceSize + compressSequencesWorkspaceSize // C: ENTROPY_WORKSPACE_SIZE
)

// minGain is the minimum saving needed to emit a compressed block or
// literals section. C: zstd_compress_internal.h:ZSTD_minGain
func minGain(srcSize int, strat strategy) int {
	minlog := 6
	if strat >= zstdBtultra {
		minlog = int(strat) - 1
	}
	return srcSize>>minlog + 2
}

// literalsCompressionIsDisabled.
// C: zstd_compress_internal.h:ZSTD_literalsCompressionIsDisabled
func literalsCompressionIsDisabled(p *cctxParams) bool {
	switch p.literalCompressionMode {
	case psEnable:
		return false
	case psDisable:
		return true
	default: // psAuto
		return p.cParams.strategy == zstdFast && p.cParams.targetLength > 0
	}
}

// Format and parameter limits. C: zstd.h, zstd_internal.h and
// zstd_compress_internal.h. This port always uses the 64-bit values.
const (
	magicNumber          = 0xFD2FB528 // C: ZSTD_MAGICNUMBER
	blockSizeLogMax      = 17         // C: ZSTD_BLOCKSIZELOG_MAX
	blockSizeMax         = 1 << blockSizeLogMax
	blockHeaderSize      = 3  // C: ZSTD_blockHeaderSize
	minCBlockSize        = 2  // C: MIN_CBLOCK_SIZE (1 litCSize + 1 RLE or RAW)
	frameHeaderSizeMax   = 18 // C: ZSTD_FRAMEHEADERSIZE_MAX
	windowLogMax         = 31 // C: ZSTD_WINDOWLOG_MAX (64-bit)
	windowLogMin         = 10 // C: ZSTD_WINDOWLOG_MIN
	windowLogAbsoluteMin = 10 // C: ZSTD_WINDOWLOG_ABSOLUTEMIN
	hashLogMin           = 6  // C: ZSTD_HASHLOG_MIN
	targetLengthMax      = blockSizeMax
	hashLog3Max          = 17                 // C: ZSTD_HASHLOG3_MAX
	rowHashTagBits       = 8                  // C: ZSTD_ROW_HASH_TAG_BITS
	minCLevel            = -targetLengthMax   // C: ZSTD_minCLevel
	contentSizeUnknown   = ^uint64(0)         // C: ZSTD_CONTENTSIZE_UNKNOWN
	maxInputSize         = 0xFF00FF00FF00FF00 // C: ZSTD_MAX_INPUT_SIZE (64-bit)

	kSearchStrength = 8 // C: kSearchStrength
	hashReadSize    = 8 // C: HASH_READ_SIZE

	// windowStartIndex is the first valid index: indices below it are
	// reserved. C: ZSTD_WINDOW_START_INDEX
	windowStartIndex = 2
	// currentMax is the largest index allowed before overflow correction
	// (3500 MiB). C: ZSTD_CURRENT_MAX (64-bit)
	currentMax = 3500 << 20
	// chunkSizeMax is the most input that can be indexed without another
	// overflow check. C: ZSTD_CHUNKSIZE_MAX
	chunkSizeMax = 1<<32 - 1 - currentMax
	// indexOverflowMargin. C: zstd_compress.c:ZSTD_INDEXOVERFLOW_MARGIN
	indexOverflowMargin = 16 << 20
	// windowOverflowCorrectFrequently. C: ZSTD_WINDOW_OVERFLOW_CORRECT_FREQUENTLY
	// (0 outside fuzzing builds).
	windowOverflowCorrectFrequently = false
	// dubtUnsortedMark. C: ZSTD_DUBT_UNSORTED_MARK
	dubtUnsortedMark = 1
)

// errSrcSizeWrong. C: ERROR(srcSize_wrong)
var errSrcSizeWrong = errors.New("zstd: src size is incorrect")

// repStartValue. C: zstd_internal.h:repStartValue
var repStartValue = [repNum]uint32{1, 4, 8}

// compressBound is the maximum compressed size of a single-pass frame of
// srcSize bytes. C: zstd.h:ZSTD_COMPRESSBOUND
func compressBound(srcSize int) int {
	if uint64(srcSize) >= maxInputSize {
		return 0
	}
	b := srcSize + srcSize>>8
	if srcSize < 128<<10 {
		b += (128<<10 - srcSize) >> 11
	}
	return b
}

// Offset-base helpers. C: zstd_compress_internal.h
const repcode1ToOffBase = 1 // C: REPCODE1_TO_OFFBASE

// offsetToOffBase. C: OFFSET_TO_OFFBASE
func offsetToOffBase(o uint32) uint32 { return o + repNum }

// compressedBlockState. C: zstd_compress_internal.h:ZSTD_compressedBlockState_t
type compressedBlockState struct {
	entropy entropyCTables
	rep     [repNum]uint32
}

// resetCompressedBlockState. C: zstd_compress.c:ZSTD_reset_compressedBlockState
func resetCompressedBlockState(bs *compressedBlockState) {
	bs.rep = repStartValue
	bs.entropy.huf.repeatMode = huf.RepeatNone
	bs.entropy.fse.offcodeRepeatMode = fseRepeatNone
	bs.entropy.fse.matchlengthRepeatMode = fseRepeatNone
	bs.entropy.fse.litlengthRepeatMode = fseRepeatNone
}

// window tracks the indexed history. C: zstd_compress_internal.h:ZSTD_window_t
//
// Indices are uint32, exactly as in C. C's pointer base (all regular indices
// are relative to it) may point before the buffer, which a Go slice cannot,
// so it is represented by the buffer holding the current prefix segment plus
// that buffer's first index: C's `base + idx` is base[idx-baseIdx], computed
// with uint32 wrap-around like C's pointer arithmetic. dictBase/dictBaseIdx
// represent C's dictBase the same way. nextSrc is C's nextSrc - base (the
// index one past the last byte of the prefix).
type window struct {
	base                  []byte
	baseIdx               uint32
	dictBase              []byte
	dictBaseIdx           uint32
	nextSrc               uint32
	dictLimit             uint32 // below that point, need extDict
	lowLimit              uint32 // below that point, no more valid data
	nbOverflowCorrections uint32
}

// at returns the position in w.base of index idx (C: base + idx).
func (w *window) at(idx uint32) int { return int(idx - w.baseIdx) }

// windowInit. C: zstd_compress_internal.h:ZSTD_window_init — C points base
// and dictBase at a dummy string; nil can never compare equal to an input.
func windowInit(w *window) {
	*w = window{
		dictLimit: windowStartIndex,
		lowLimit:  windowStartIndex,
		nextSrc:   windowStartIndex,
	}
}

// windowClear empties the history. C: ZSTD_window_clear
func windowClear(w *window) {
	end := w.nextSrc
	w.lowLimit = end
	w.dictLimit = end
}

// windowHasExtDict. C: ZSTD_window_hasExtDict
func windowHasExtDict(w *window) bool { return w.lowLimit < w.dictLimit }

// windowCanOverflowCorrect. C: ZSTD_window_canOverflowCorrect. srcIdx is
// C's src - window.base.
func windowCanOverflowCorrect(w *window, cycleLog, maxDist, loadedDictEnd, srcIdx uint32) bool {
	cycleSize := uint32(1) << cycleLog
	curr := srcIdx
	minIndexToOverflowCorrect := cycleSize + max(maxDist, cycleSize) + windowStartIndex
	// Adjust the min index to backoff the overflow correction frequency, so
	// we don't waste too much CPU in overflow correction. If this
	// computation overflows we don't really care, we just need to make sure
	// it is at least minIndexToOverflowCorrect.
	adjustment := w.nbOverflowCorrections + 1
	adjustedIndex := max(minIndexToOverflowCorrect*adjustment, minIndexToOverflowCorrect)
	indexLargeEnough := curr > adjustedIndex
	// Only overflow correct early if the dictionary is invalidated already,
	// so we don't hurt compression ratio.
	dictionaryInvalidated := curr > maxDist+loadedDictEnd
	return indexLargeEnough && dictionaryInvalidated
}

// windowNeedOverflowCorrection. C: ZSTD_window_needOverflowCorrection.
// srcIdx and srcEndIdx are C's src - base and srcEnd - base.
func windowNeedOverflowCorrection(w *window, cycleLog, maxDist, loadedDictEnd, srcIdx, srcEndIdx uint32) bool {
	curr := srcEndIdx
	if windowOverflowCorrectFrequently {
		if windowCanOverflowCorrect(w, cycleLog, maxDist, loadedDictEnd, srcIdx) {
			return true
		}
	}
	return curr > currentMax
}

// windowCorrectOverflow reduces the indices to protect from index overflow
// and returns the correction, which must be applied to every stored index.
// The least significant cycleLog bits of the indices remain the same; every
// index up to maxDist in the past remains valid. srcIdx is C's src - base.
// C: ZSTD_window_correctOverflow
func windowCorrectOverflow(w *window, cycleLog, maxDist, srcIdx uint32) uint32 {
	cycleSize := uint32(1) << cycleLog
	cycleMask := cycleSize - 1
	curr := srcIdx
	currentCycle := curr & cycleMask
	// Ensure newCurrent - maxDist >= windowStartIndex.
	var currentCycleCorrection uint32
	if currentCycle < windowStartIndex {
		currentCycleCorrection = max(cycleSize, windowStartIndex)
	}
	newCurrent := currentCycle + currentCycleCorrection + max(maxDist, cycleSize)
	correction := curr - newCurrent

	// C: base += correction; dictBase += correction.
	w.baseIdx -= correction
	w.dictBaseIdx -= correction
	w.nextSrc -= correction // C's nextSrc pointer is unchanged; its index moves
	if w.lowLimit < correction+windowStartIndex {
		w.lowLimit = windowStartIndex
	} else {
		w.lowLimit -= correction
	}
	if w.dictLimit < correction+windowStartIndex {
		w.dictLimit = windowStartIndex
	} else {
		w.dictLimit -= correction
	}
	w.nbOverflowCorrections++
	return correction
}

// windowEnforceMaxDist updates lowLimit so that blockEndIdx - lowLimit <=
// maxDist + loadedDictEnd, and invalidates the dictionary once the input has
// progressed beyond the window. loadedDictEnd and dms may be nil (no
// dictionary). blockEndIdx is C's blockEnd - base.
// C: ZSTD_window_enforceMaxDist
func windowEnforceMaxDist(w *window, blockEndIdx, maxDist uint32, loadedDictEnd *uint32, dms **matchState) {
	var lde uint32
	if loadedDictEnd != nil {
		lde = *loadedDictEnd
	}
	if blockEndIdx > maxDist+lde {
		newLowLimit := blockEndIdx - maxDist
		if w.lowLimit < newLowLimit {
			w.lowLimit = newLowLimit
		}
		if w.dictLimit < w.lowLimit {
			w.dictLimit = w.lowLimit
		}
		// On reaching window size, dictionaries are invalidated
		if loadedDictEnd != nil {
			*loadedDictEnd = 0
		}
		if dms != nil {
			*dms = nil
		}
	}
}

// checkDictValidity invalidates the dictionary when the input progresses
// beyond the window size or the window is no longer contiguous with it.
// C: ZSTD_checkDictValidity
func checkDictValidity(w *window, blockEndIdx, maxDist uint32, loadedDictEnd *uint32, dms **matchState) {
	if blockEndIdx > *loadedDictEnd+maxDist || *loadedDictEnd != w.dictLimit {
		// On reaching window size, dictionaries are invalidated. For
		// simplification, if window size is reached anywhere within next
		// block, the dictionary is invalidated for the full block.
		//
		// We also have to invalidate the dictionary if windowUpdate() has
		// detected non-contiguous segments, which means that
		// loadedDictEnd != window.dictLimit.
		*loadedDictEnd = 0
		*dms = nil
	}
}

// arrayEnd returns the address of the last element of b's backing array, which
// identifies the array for slices that were not cut with a 3-index slice
// expression.
func arrayEnd(b []byte) *byte {
	if cap(b) == 0 {
		return nil
	}
	return &b[:cap(b)][cap(b)-1]
}

// windowUpdate appends src to the window. If src does not directly follow
// the prefix, the prefix becomes the extDict and the old extDict is
// forgotten. It reports whether src was contiguous. C: ZSTD_window_update
//
// C compares raw pointers. Here src continues the prefix when it starts at
// the byte after the prefix in the same backing array; the overlap check
// below likewise only applies to slices of one array (two arrays never
// overlap, and w.dictBase keeps its array alive, so its memory cannot be
// reused for src).
//
// Streaming (compressStreamGeneric) always passes slices of the context's
// own inBuff, cut with full capacity, so contiguity and overlap are decided
// exactly as C's pointers decide them, including when inBuff wraps within
// a frame (the new block overwrites the start of the old prefix, now the
// extDict). Only compress2 passes caller slices, which may be cut with a
// 3-index slice expression and then look non-contiguous where C's pointers
// are contiguous; compress2 makes one update per frame, right after the
// window was cleared, where either outcome leaves an empty extDict, so the
// output is the same.
func windowUpdate(w *window, src []byte, forceNonContiguous bool) bool {
	if len(src) == 0 {
		return true
	}
	contiguous := true
	// next is nextSrc's position in w.base (w.at), kept unsigned and
	// compared in 64 bits: when base is not contiguous with nextSrc it may
	// exceed cap(w.base), and math.MaxInt32 on 32-bit platforms.
	next := w.nextSrc - w.baseIdx
	if forceNonContiguous || uint64(cap(w.base)) < uint64(next)+uint64(len(src)) || &w.base[:next+1][next] != &src[0] {
		// not contiguous
		distanceFromBase := w.nextSrc
		w.lowLimit = w.dictLimit
		w.dictLimit = distanceFromBase
		w.dictBase, w.dictBaseIdx = w.base, w.baseIdx
		w.base, w.baseIdx = src, distanceFromBase // C: base = ip - distanceFromBase
		if w.dictLimit-w.lowLimit < hashReadSize {
			w.lowLimit = w.dictLimit // too small extDict
		}
		contiguous = false
	} else {
		w.base = w.base[:int(next)+len(src)]
	}
	w.nextSrc += uint32(len(src))
	// if input and dictionary overlap : reduce dictionary (area presumed modified by input)
	if e := arrayEnd(src); e != nil && e == arrayEnd(w.dictBase) {
		// Positions relative to the end of the shared array. dictAt is only
		// taken at lowLimit and dictLimit, which lie at or after the first
		// byte of w.dictBase (index dictBaseIdx), so idx-dictBaseIdx does
		// not wrap.
		ip := -cap(src)
		dictAt := func(idx uint32) int { return -cap(w.dictBase) + int(idx-w.dictBaseIdx) }
		dictEnd := dictAt(w.dictLimit)
		if ip+len(src) > dictAt(w.lowLimit) && ip < dictEnd {
			// C: zstd_compress_internal.h:ZSTD_window_update — C computes
			// highInputIdx = (ip + srcSize) - dictBase; its dictBase (index
			// 0) may lie before the array, where this port's uint32 index
			// arithmetic wraps, so the same value is taken relative to
			// dictLimit (min(highInputIdx, dictLimit) is all C uses).
			lowLimitMax := w.dictLimit
			if hi := ip + len(src); hi <= dictEnd {
				lowLimitMax = w.dictLimit - uint32(dictEnd-hi)
			}
			w.lowLimit = lowLimitMax
		}
	}
	return contiguous
}

// extDictHook, when set (by tests), is called each time an extDict block
// compressor runs its extDict search, with the compressor's name: "fast",
// "dfast", "greedy-hc", "greedy-row", "lazy-hc", "lazy-row", "lazy2-hc",
// "lazy2-row", "btlazy2" or "btopt". C has no equivalent.
var extDictHook func(name string)

// dictMode. C: zstd_compress_internal.h:ZSTD_dictMode_e
type dictMode int

const (
	noDict dictMode = iota
	extDict
	dictMatchState
	dedicatedDictSearch
)

// optNum is the most positions the optimal parser prices ahead.
// C: zstd_internal.h:ZSTD_OPT_NUM
const optNum = 1 << 12

// optSize is the size of the optimal parser's tables.
// C: zstd_compress_internal.h:ZSTD_OPT_SIZE
const optSize = optNum + 3

// maxLit is the largest literal value. C: zstd_internal.h:MaxLit
const maxLit = 1<<8 - 1

// optMatch is a match candidate. C: zstd_compress_internal.h:ZSTD_match_t
type optMatch struct {
	off uint32 // offset sumtype code for the match, using storeSeq format
	len uint32 // raw length of match
}

// optimal is one position tracked by the optimal parser.
// C: zstd_compress_internal.h:ZSTD_optimal_t — price is C's int; this port
// computes it in a wider int, which cannot change a comparison since C's
// prices stay far below 2^31 (C asserts price < 1e9).
type optimal struct {
	price  int            // price from beginning of segment to this position
	off    uint32         // offset of previous match
	mlen   uint32         // length of previous match
	litlen uint32         // nb of literals since previous match
	rep    [repNum]uint32 // offset history after previous match
}

// optPrice. C: zstd_compress_internal.h:ZSTD_OptPrice_e
type optPrice int

const (
	zopDynamic optPrice = iota
	zopPredef
)

// optState is the optimal parser state.
// C: zstd_compress_internal.h:optState_t — the frequency tables are arrays
// here; matchTable and priceTable are allocated by resetMatchState for
// strategies >= btopt, like C's workspace reservations.
type optState struct {
	litFreq         [maxLit + 1]uint32 // table of literals statistics
	litLengthFreq   [maxLL + 1]uint32  // table of litLength statistics
	matchLengthFreq [maxML + 1]uint32  // table of matchLength statistics
	offCodeFreq     [maxOff + 1]uint32 // table of offCode statistics
	matchTable      *[optSize]optMatch // list of found matches
	priceTable      *[optSize]optimal  // all positions tracked by optimal parser

	litSum                  uint32 // nb of literals
	litLengthSum            uint32 // nb of litLength codes
	matchLengthSum          uint32 // nb of matchLength codes
	offCodeSum              uint32 // nb of offset codes
	litSumBasePrice         uint32 // to compare to log2(litfreq)
	litLengthSumBasePrice   uint32 // to compare to log2(llfreq)
	matchLengthSumBasePrice uint32 // to compare to log2(mlfreq)
	offCodeSumBasePrice     uint32 // to compare to log2(offreq)
	priceType               optPrice
	// symbolCosts are the previous block's entropy tables (pre-calculated
	// dictionary statistics on a frame's first block).
	symbolCosts            *entropyCTables
	literalCompressionMode paramSwitch
}

// matchState. C: zstd_compress_internal.h:ZSTD_MatchState_t (the fields the
// ported strategies use; later strategies and dictionaries add theirs).
type matchState struct {
	window window
	// loadedDictEnd is the index of the end of the dictionary, within the
	// context's referential; 0 when no dictionary is in use.
	loadedDictEnd uint32
	// nextToUpdate is the index from which to continue table updates.
	nextToUpdate uint32
	// nextToUpdate3 is compressBlockOptGeneric's nextToUpdate3 (C: a local
	// of ZSTD_compressBlock_opt_generic, set to nextToUpdate at the start
	// of each block), kept here so the assembly reaches it through btAsm.
	nextToUpdate3 uint32
	hashLog3      uint32 // dispatch table for matches of len==3 : larger == faster, more memory

	hashTable  []uint32
	hashTable3 []uint32
	chainTable []uint32
	huge       *hugeTables // backs the large tables with huge pages (WithHugePages); nil = Go heap

	// Row-based match finder state.
	rowHashLog      uint32                   // hashLog - rowLog
	tagTable        []byte                   // a tag byte per hashTable entry; entry 0 of each row is the row's head
	hashCache       [rowHashCacheSize]uint32 // hashes of the next positions to insert
	hashSalt        uint64                   // salts the hash; changes on every context reset
	hashSaltEntropy uint32                   // collects entropy for the next salt
	lazySkipping    bool                     // the lazy parser only inserts the positions it searches
	frameStart      uint32                   // index of the current frame's first byte (set on reset; picks the fast/dfast loop, see staleTables)
	rowAsm          rowAsmParams             // set up for the row finder's assembly during a block
	btAsm           btAsmParams              // set up for the optimal parser's assembly btGetAllMatches during a block
	asmCalls        int                      // calls of lazyHcRun, btGetAllNoDict and dubtFindBestMatchFast that ran the assembly (tests check it)
	asmLoopRuns     int                      // blocks whose match loop ran in assembly: fast, double-fast, lazyHcRun, lazyRowRun, lazyBtRun, optParseFast (tests check it)

	// staleHint: the last fast or double-fast assembly block's sequences had
	// few literals (see staleTables), so the next block runs the Stale loop.
	// Speed only: it never affects output. Never reset; it carries across
	// blocks, frames, context resets and level changes, and the fast and
	// double-fast finders share it (each frame's first blocks run the Stale
	// loop by frameStart anyway).
	staleHint bool

	// forceNonContiguous makes the next windowUpdate start a new segment.
	forceNonContiguous bool

	// dictMatchState is the attached CDict's match state, searched in
	// place (read-only), or nil.
	dictMatchState *matchState

	// ldmSeqStore holds the block's long-distance matches, which the
	// optimal parser takes as candidates (set by ldmBlockCompress), or nil.
	ldmSeqStore *rawSeqStore

	opt optState // optimal parser state

	cParams compressionParameters
}

// matchStateDictMode. C: ZSTD_matchState_dictMode (dedicated dictionary
// search is never enabled).
func matchStateDictMode(ms *matchState) dictMode {
	if windowHasExtDict(&ms.window) {
		return extDict
	}
	if ms.dictMatchState != nil {
		return dictMatchState
	}
	return noDict
}

// getLowestMatchIndex returns the lowest allowed match index. It may either
// be in a dictionary or in the prefix. C: ZSTD_getLowestMatchIndex
func getLowestMatchIndex(ms *matchState, curr uint32, windowLog uint32) uint32 {
	maxDistance := uint32(1) << windowLog
	lowestValid := ms.window.lowLimit
	withinWindow := lowestValid
	if curr-lowestValid > maxDistance {
		withinWindow = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	// When using a dictionary the entire dictionary is valid if a single
	// byte of the dictionary is within the window.
	if isDictionary {
		return lowestValid
	}
	return withinWindow
}

// getLowestPrefixIndex returns the lowest allowed match index in the prefix.
// C: ZSTD_getLowestPrefixIndex
func getLowestPrefixIndex(ms *matchState, curr uint32, windowLog uint32) uint32 {
	maxDistance := uint32(1) << windowLog
	lowestValid := ms.window.dictLimit
	withinWindow := lowestValid
	if curr-lowestValid > maxDistance {
		withinWindow = curr - maxDistance
	}
	isDictionary := ms.loadedDictEnd != 0
	// When computing the lowest prefix index we need to take the dictionary
	// into account to handle the edge case where the dictionary and the
	// source are contiguous in memory.
	if isDictionary {
		return lowestValid
	}
	return withinWindow
}

// updateRep updates rep in place for a sequence with offBase (storeSeq's
// numeric representation); ll0 tells whether its literal length is 0.
// C: zstd_compress_internal.h:ZSTD_updateRep
func updateRep(rep *[repNum]uint32, offBase uint32, ll0 bool) {
	if offBase > repNum { // full offset
		rep[2] = rep[1]
		rep[1] = rep[0]
		rep[0] = offBase - repNum
		return
	}
	repCode := offBase - 1 // repcode
	if ll0 {
		repCode++
	}
	if repCode > 0 { // note : if repCode==0, no change
		currentOffset := rep[0] - 1
		if repCode != repNum {
			currentOffset = rep[repCode]
		}
		if repCode >= 2 {
			rep[2] = rep[1]
		}
		rep[1] = rep[0]
		rep[0] = currentOffset
	}
}

// newRep returns rep updated for a sequence with offBase.
// C: zstd_compress_internal.h:ZSTD_newRep
func newRep(rep [repNum]uint32, offBase uint32, ll0 bool) [repNum]uint32 {
	updateRep(&rep, offBase, ll0)
	return rep
}

// count returns the length of the common prefix of b[ip:] and b[match:],
// stopping at iEnd (match < ip). C: zstd_compress_internal.h:ZSTD_count
// (the result does not depend on C's word-at-a-time strategy).
func count(b []byte, ip, match, iEnd int) int {
	start := ip
	for ip+8 <= iEnd {
		diff := binary.LittleEndian.Uint64(b[match:]) ^ binary.LittleEndian.Uint64(b[ip:])
		if diff != 0 {
			return ip - start + bits.TrailingZeros64(diff)>>3
		}
		ip += 8
		match += 8
	}
	for ip < iEnd && b[match] == b[ip] {
		ip++
		match++
	}
	return ip - start
}

// count2 is count with ip in a and match in m: the length of the common
// prefix of a[ip:] and m[match:], stopping at iEnd (a position in a); m
// must hold iEnd - ip bytes from match.
func count2(a []byte, ip int, m []byte, match, iEnd int) int {
	start := ip
	for ip+8 <= iEnd {
		diff := binary.LittleEndian.Uint64(m[match:]) ^ binary.LittleEndian.Uint64(a[ip:])
		if diff != 0 {
			return ip - start + bits.TrailingZeros64(diff)>>3
		}
		ip += 8
		match += 8
	}
	for ip < iEnd && m[match] == a[ip] {
		ip++
		match++
	}
	return ip - start
}

// count2Segments counts the match length with ip in b (the prefix
// segment) and match in m, its own segment ending at mEnd: on reaching
// mEnd, the match continues at iStart, the start of the prefix in b.
// C: zstd_compress_internal.h:ZSTD_count_2segments
func count2Segments(b []byte, ip int, m []byte, match, iEnd, mEnd, iStart int) int {
	vEnd := min(ip+(mEnd-match), iEnd)
	matchLength := count2(b, ip, m, match, vEnd)
	if match+matchLength != mEnd {
		return matchLength
	}
	return matchLength + count(b, ip+matchLength, iStart, iEnd)
}

// indexOverlapCheck reports whether a repcode at repIndex can be read
// without straddling prefixLowestIndex: it is false for the 3 indices just
// below it and for prefixLowestIndex itself.
// C: zstd_compress_internal.h:ZSTD_index_overlap_check
func indexOverlapCheck(prefixLowestIndex, repIndex uint32) bool {
	return (prefixLowestIndex-1)-repIndex >= 3
}

// Hash primes. C: zstd_compress_internal.h
const (
	prime3bytes = 506832829
	prime4bytes = 2654435761
	prime5bytes = 889523592379
	prime6bytes = 227718039650203
	prime7bytes = 58295818150454627
	prime8bytes = 0xCF1BBCDCB7A56463
)

// le32 is binary.LittleEndian.Uint32(b[i:]) without making the subslice
// (whose pointer arithmetic costs several instructions in a hot loop).
func le32(b []byte, i int) uint32 {
	_ = b[i+3]
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
}

// hash3Ptr hashes the first 3 bytes at b[i:] to h bits (4 bytes are read).
// C: zstd_compress_internal.h:ZSTD_hash3Ptr
func hash3Ptr(b []byte, i int, h uint32) uint32 {
	return ((binary.LittleEndian.Uint32(b[i:]) << (32 - 24)) * prime3bytes) >> (32 - h)
}

// hashPtr hashes the first mls bytes at b[i:] to hBits bits; mls other
// than 5..8 hashes 4 bytes (and reads only those).
// C: zstd_compress_internal.h:ZSTD_hashPtr. Mls 5..8 share one form,
// ((le64 << (64 - 8*mls)) * prime) >> (64 - hBits), taken from a table so
// the function is small enough to inline (a switch is not).
func hashPtr(b []byte, i int, hBits, mls uint32) uint32 {
	if mls-5 > 3 {
		return (binary.LittleEndian.Uint32(b[i:]) * prime4bytes) >> (32 - hBits)
	}
	return uint32(((binary.LittleEndian.Uint64(b[i:]) << hashPtrShift[mls&7]) * hashPtrPrime[mls&7]) >> (64 - hBits))
}

// Parameters of hashPtr for mls 5..8, indexed by mls & 7.
var (
	hashPtrShift = [8]uint8{5: 64 - 40, 6: 64 - 48, 7: 64 - 56, 0: 0}
	hashPtrPrime = [8]uint64{5: prime5bytes, 6: prime6bytes, 7: prime7bytes, 0: prime8bytes}
)

// storeSeq stores a sequence (litLen literals from lits, then a match of
// matchLength bytes at offBase) into ss. offBase is a repcode 1..3 or an
// offset + repNum. matchLength must be >= minMatch.
// C: zstd_compress_internal.h:ZSTD_storeSeq (+ ZSTD_storeSeqOnly)
func storeSeq(ss *seqStore, litLen int, lits []byte, offBase uint32, matchLength int) {
	ss.literals = append(ss.literals, lits[:litLen]...)
	n := uint32(len(ss.sequences))
	if litLen > 0xFFFF {
		ss.longLengthType = lltLiteralLength
		ss.longLengthPos = n
	}
	mlBase := matchLength - minMatch
	if mlBase > 0xFFFF {
		ss.longLengthType = lltMatchLength
		ss.longLengthPos = n
	}
	ss.sequences = append(ss.sequences, seqDef{offBase: offBase, litLength: uint16(litLen), mlBase: uint16(mlBase)})
}
