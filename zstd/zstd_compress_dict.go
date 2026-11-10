// Port of the dictionary parts of libzstd v1.5.7 lib/compress/zstd_compress.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual
// license): CDict creation, dictionary loading (entropy tables and content)
// and starting a frame from a CDict (attaching or copying it).

package zstd

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/vahid-sohrabloo/fastcompress/internal/fse"
	"github.com/vahid-sohrabloo/fastcompress/internal/huf"
)

// dictContentType. C: zstd.h:ZSTD_dictContentType_e
type dictContentType int

const (
	dctAuto       dictContentType = iota // C: ZSTD_dct_auto: a zstd dictionary if it starts with dictMagic, else raw content
	dctRawContent                        // C: ZSTD_dct_rawContent: the whole buffer is content
	dctFullDict                          // C: ZSTD_dct_fullDict: must be a zstd dictionary
)

// tableFillPurpose. C: zstd_compress_internal.h:ZSTD_tableFillPurpose_e
type tableFillPurpose int

const (
	tfpForCCtx  tableFillPurpose = iota // C: ZSTD_tfp_forCCtx
	tfpForCDict                         // C: ZSTD_tfp_forCDict
)

const (
	dictMagic = 0xEC30A437 // C: zstd.h:ZSTD_MAGIC_DICTIONARY

	// The "short cache" tags of CDict hash tables (fast and dfast): the
	// low shortCacheTagBits of an entry are a tag, the rest the index.
	// C: zstd_compress_internal.h:ZSTD_SHORT_CACHE_TAG_BITS/_MASK
	shortCacheTagBits = 8
	shortCacheTagMask = 1<<shortCacheTagBits - 1
)

// Dictionary errors. C: ERROR(dictionary_corrupted), ERROR(dictionary_wrong)
var (
	errDictionaryCorrupted = errors.New("zstd: dictionary is corrupted")
	errDictionaryWrong     = errors.New("zstd: dictionary is not a zstd dictionary")
)

// cdictHook and dictMatchStateHook, when set (by tests), are called when a
// frame starts from a CDict (with its strategy, and whether the CDict is
// attached or copied) and each time a dictMatchState block compressor runs
// (with its name, as extDictHook's). C has no equivalent.
var (
	cdictHook          func(strat strategy, attach bool)
	dictMatchStateHook func(name string)
)

// writeTaggedIndex unpacks hashAndTag into (hash, tag), then packs (index,
// tag) into hashTable[hash].
// C: zstd_compress_internal.h:ZSTD_writeTaggedIndex
func writeTaggedIndex(hashTable []uint32, hashAndTag uint32, index uint32) {
	hashTable[hashAndTag>>shortCacheTagBits] = index<<shortCacheTagBits | hashAndTag&shortCacheTagMask
}

// comparePackedTags reports whether the tags in the low bits of two packed
// values match. C: zstd_compress_internal.h:ZSTD_comparePackedTags
func comparePackedTags(packedTag1, packedTag2 uint32) bool {
	return packedTag1&shortCacheTagMask == packedTag2&shortCacheTagMask
}

// cdictIndicesAreTagged reports whether a CDict with these parameters has
// tagged hash and chain table entries. C: ZSTD_CDictIndicesAreTagged
func cdictIndicesAreTagged(cParams *compressionParameters) bool {
	return cParams.strategy == zstdFast || cParams.strategy == zstdDfast
}

// cdict is a digested dictionary, ready to start frames from: its entropy
// tables and repcodes, and its content indexed into match finder tables.
// It is read-only once created, so contexts can share it.
// C: zstd_compress_internal.h:ZSTD_CDict_s
type cdict struct {
	dictContent     []byte
	dictContentSize int // the size of the whole dictionary, header included
	dictContentType dictContentType
	cBlockState     compressedBlockState // entropy tables and repcodes
	matchState      matchState
	dictID          uint32
	// compressionLevel is always 0 (C: ZSTD_NO_CLEVEL): only
	// ZSTD_createCDict sets a level, and contexts here only build CDicts
	// through ZSTD_initLocalDict (ZSTD_createCDict_advanced2).
	compressionLevel  int
	useRowMatchFinder paramSwitch
	// repsInContent tells whether the starting repcodes stay within the
	// loaded content (see shouldAttachDict). Go only.
	repsInContent bool
}

// localDict is a dictionary loaded into a context, digested into a CDict
// when the first frame starts. C: zstd_compress_internal.h:ZSTD_localDict
// (dictBuffer is not needed: the bytes are referenced, never freed).
type localDict struct {
	dict            []byte
	dictContentType dictContentType
	cdict           *cdict
}

// loadDictionary makes the context compress the next frames with dict
// (nil or empty: no dictionary). dict is referenced and must not change.
// C: zstd_compress.c:ZSTD_CCtx_loadDictionary_advanced with
// ZSTD_dlm_byRef (byCopy only adds a private copy).
func (c *cctx) loadDictionary(dict []byte, dct dictContentType) error {
	c.clearAllDicts() // erase any previously set dictionary
	if len(dict) == 0 {
		return nil // no dictionary
	}
	c.localDict = localDict{dict: dict, dictContentType: dct}
	return nil
}

// setLocalDict is loadDictionary followed by ZSTD_initLocalDict, with the
// CDict cd already created by createCDictAdvanced2 from this dictionary and
// parameters equal to this context's requested parameters (the Encoder
// creates it once and shares it between its contexts).
func (c *cctx) setLocalDict(dict []byte, dct dictContentType, cd *cdict) {
	c.localDict = localDict{dict: dict, dictContentType: dct, cdict: cd}
	c.cdict = cd
}

// clearAllDicts. C: zstd_compress.c:ZSTD_clearAllDicts
func (c *cctx) clearAllDicts() {
	c.localDict = localDict{}
	c.cdict = nil
}

// initLocalDict digests the local dictionary into a CDict, with the
// requested parameters, unless already done. It does not use the pledged
// source size, because the dictionary may be used for multiple
// compressions. C: zstd_compress.c:ZSTD_initLocalDict
func (c *cctx) initLocalDict() error {
	dl := &c.localDict
	if dl.dict == nil {
		return nil // No local dictionary.
	}
	if dl.cdict != nil {
		return nil // Local dictionary already initialized.
	}
	cd, err := createCDictAdvanced2(dl.dict, dl.dictContentType, &c.requestedParams)
	if err != nil {
		return err
	}
	dl.cdict = cd
	c.cdict = cd
	return nil
}

// createCDictAdvanced2 digests dict with the parameters params would give
// a source of unknown size and dict's size.
// C: zstd_compress.c:ZSTD_createCDict_advanced2 — dedicated dictionary
// search (ZSTD_c_enableDedicatedDictSearch) is off by default and never
// enabled here, so the DDS parameter selection is skipped.
func createCDictAdvanced2(dict []byte, dct dictContentType, originalParams *cctxParams) (*cdict, error) {
	params := *originalParams
	cParams := getCParamsFromCCtxParams(&params, contentSizeUnknown, uint64(len(dict)), cpmCreateCDict)
	params.cParams = cParams
	params.useRowMatchFinder = resolveRowMatchFinderMode(params.useRowMatchFinder, &cParams)

	// C: ZSTD_createCDict_advanced_internal
	cd := &cdict{
		compressionLevel:  0, // C: ZSTD_NO_CLEVEL, signals advanced API usage
		useRowMatchFinder: params.useRowMatchFinder,
	}
	if err := initCDictInternal(cd, dict, dct, params); err != nil {
		return nil, err
	}
	return cd, nil
}

// initCDictInternal. C: zstd_compress.c:ZSTD_initCDict_internal (dict is
// referenced, C's ZSTD_dlm_byRef)
func initCDictInternal(cd *cdict, dict []byte, dct dictContentType, params cctxParams) error {
	cd.matchState.cParams = params.cParams
	cd.dictContent = dict
	cd.dictContentSize = len(dict)
	cd.dictContentType = dct

	// Reset the state to no dictionary
	resetCompressedBlockState(&cd.cBlockState)
	resetMatchState(&cd.matchState, &params.cParams, params.useRowMatchFinder, true, resetTargetCDict)
	// (Maybe) load the dictionary. Skips loading the dictionary if it is
	// < 8 bytes.
	params.compressionLevel = clevelDefault
	params.fParams.contentSizeFlag = true
	dictID, err := compressInsertDictionary(&cd.cBlockState, &cd.matchState, &params, dict, dct, dtlmFull, tfpForCDict)
	if err != nil {
		return err
	}
	cd.dictID = dictID
	cdictLen := cd.matchState.window.nextSrc - cd.matchState.window.dictLimit
	cd.repsInContent = true
	for _, r := range cd.cBlockState.rep {
		if cdictLen != 0 && r > cdictLen {
			cd.repsInContent = false
		}
	}
	return nil
}

// maxDictContentLoaded is the most dictionary content any CDict keeps in
// its window: ZSTD_loadDictionaryContent keeps only the last
// currentMax - windowStartIndex bytes, and only the last
// 1<<(32-shortCacheTagBits) - windowStartIndex bytes for the fast and dfast
// strategies (short cache). Its other truncation (to what the tables can
// index) only limits the positions indexed, not the window.
const maxDictContentLoaded = min(currentMax-windowStartIndex, 1<<(32-shortCacheTagBits)-windowStartIndex)

// checkDictReps rejects a zstd dictionary whose repcodes reach beyond the
// content a CDict of some strategy keeps (dictContentSize is the size of
// the content after the header). C only checks them against the whole
// content (ZSTD_loadCEntropy); a larger repcode makes C's dictMatchState
// compressors read before the dictionary (C asserts it does not happen;
// the behaviour is undefined). No dictionary C handles correctly is
// rejected.
func checkDictReps(rep [repNum]uint32, dictContentSize int) error {
	limit := min(dictContentSize, maxDictContentLoaded)
	for _, r := range rep {
		if int(r) > limit {
			return fmt.Errorf("%w: repcode %d beyond the %d bytes of content a dictionary can use", errDictionaryCorrupted, r, limit)
		}
	}
	return nil
}

// compressInsertDictionary loads dict into bs and ms and returns its
// dictionary ID. C: zstd_compress.c:ZSTD_compress_insertDictionary (no LDM)
func compressInsertDictionary(bs *compressedBlockState, ms *matchState, params *cctxParams, dict []byte,
	dct dictContentType, dtlm dictTableLoadMethod, tfp tableFillPurpose) (uint32, error) {
	if len(dict) < 8 {
		if dct == dctFullDict {
			return 0, errDictionaryWrong
		}
		return 0, nil
	}

	resetCompressedBlockState(bs)

	// dict restricted modes
	if dct == dctRawContent {
		loadDictionaryContent(ms, params, dict, dtlm, tfp)
		return 0, nil
	}

	if binary.LittleEndian.Uint32(dict) != dictMagic {
		if dct == dctAuto {
			loadDictionaryContent(ms, params, dict, dtlm, tfp)
			return 0, nil
		}
		return 0, errDictionaryWrong
	}

	// dict as full zstd dictionary
	return loadZstdDictionary(bs, ms, params, dict, dtlm, tfp)
}

// loadZstdDictionary loads a zstd dictionary (magic number already
// checked, len(dict) >= 8) and returns its ID.
// C: zstd_compress.c:ZSTD_loadZstdDictionary
func loadZstdDictionary(bs *compressedBlockState, ms *matchState, params *cctxParams, dict []byte,
	dtlm dictTableLoadMethod, tfp tableFillPurpose) (uint32, error) {
	var dictID uint32
	if !params.fParams.noDictIDFlag {
		dictID = binary.LittleEndian.Uint32(dict[4:]) // skip magic number
	}
	eSize, err := loadCEntropy(bs, dict)
	if err != nil {
		return 0, err
	}
	loadDictionaryContent(ms, params, dict[eSize:], dtlm, tfp)
	return dictID, nil
}

// dictNCountRepeat tells whether an FSE table loaded from a dictionary can
// be assumed valid: only when it gives every symbol up to maxSymbolValue a
// non-zero probability. C: zstd_compress.c:ZSTD_dictNCountRepeat
func dictNCountRepeat(normalizedCounter []int16, dictMaxSymbolValue, maxSymbolValue uint) fseRepeat {
	if dictMaxSymbolValue < maxSymbolValue {
		return fseRepeatCheck
	}
	for s := uint(0); s <= maxSymbolValue; s++ {
		if normalizedCounter[s] == 0 {
			return fseRepeatCheck
		}
	}
	return fseRepeatValid
}

// loadCEntropy loads the entropy tables and repcodes of the zstd
// dictionary dict into bs and returns the size of the dictionary header
// (magic, ID and tables). C: zstd_compress.c:ZSTD_loadCEntropy (the
// workspace is internal to fse.BuildCTable's callers here)
func loadCEntropy(bs *compressedBlockState, dict []byte) (int, error) {
	var offcodeNCount [maxOff + 1]int16
	offcodeMaxValue := uint(maxOff)
	dictPtr := 8 // skip magic num and dict ID
	dictEnd := len(dict)
	wksp := make([]byte, huf.WorkspaceSize)
	bs.entropy.huf.repeatMode = huf.RepeatCheck

	{
		maxSymbolValue := uint(255)
		hufHeaderSize, hasZeroWeights, err := huf.ReadCTable(bs.entropy.huf.cTable[:], &maxSymbolValue, dict[dictPtr:])

		// We only set the loaded table as valid if it contains all non-zero
		// weights. Otherwise, we set it to check
		if err == nil && !hasZeroWeights && maxSymbolValue == 255 {
			bs.entropy.huf.repeatMode = huf.RepeatValid
		}

		if err != nil {
			return 0, errDictionaryCorrupted
		}
		dictPtr += hufHeaderSize
	}

	{
		offcodeLog, offcodeHeaderSize, err := fse.ReadNCount(offcodeNCount[:], &offcodeMaxValue, dict[dictPtr:])
		if err != nil {
			return 0, errDictionaryCorrupted
		}
		if offcodeLog > offFSELog {
			return 0, errDictionaryCorrupted
		}
		// fill all offset symbols to avoid garbage at end of table
		if fse.BuildCTable(bs.entropy.fse.offcodeCTable[:], offcodeNCount[:], maxOff, offcodeLog, wksp) != nil {
			return 0, errDictionaryCorrupted
		}
		// Defer checking offcodeMaxValue because we need to know the size of the dictionary content
		dictPtr += offcodeHeaderSize
	}

	{
		var matchlengthNCount [maxML + 1]int16
		matchlengthMaxValue := uint(maxML)
		matchlengthLog, matchlengthHeaderSize, err := fse.ReadNCount(matchlengthNCount[:], &matchlengthMaxValue, dict[dictPtr:])
		if err != nil {
			return 0, errDictionaryCorrupted
		}
		if matchlengthLog > mlFSELog {
			return 0, errDictionaryCorrupted
		}
		if fse.BuildCTable(bs.entropy.fse.matchlengthCTable[:], matchlengthNCount[:], matchlengthMaxValue, matchlengthLog, wksp) != nil {
			return 0, errDictionaryCorrupted
		}
		bs.entropy.fse.matchlengthRepeatMode = dictNCountRepeat(matchlengthNCount[:], matchlengthMaxValue, maxML)
		dictPtr += matchlengthHeaderSize
	}

	{
		var litlengthNCount [maxLL + 1]int16
		litlengthMaxValue := uint(maxLL)
		litlengthLog, litlengthHeaderSize, err := fse.ReadNCount(litlengthNCount[:], &litlengthMaxValue, dict[dictPtr:])
		if err != nil {
			return 0, errDictionaryCorrupted
		}
		if litlengthLog > llFSELog {
			return 0, errDictionaryCorrupted
		}
		if fse.BuildCTable(bs.entropy.fse.litlengthCTable[:], litlengthNCount[:], litlengthMaxValue, litlengthLog, wksp) != nil {
			return 0, errDictionaryCorrupted
		}
		bs.entropy.fse.litlengthRepeatMode = dictNCountRepeat(litlengthNCount[:], litlengthMaxValue, maxLL)
		dictPtr += litlengthHeaderSize
	}

	if dictPtr+12 > dictEnd {
		return 0, errDictionaryCorrupted
	}
	bs.rep[0] = binary.LittleEndian.Uint32(dict[dictPtr:])
	bs.rep[1] = binary.LittleEndian.Uint32(dict[dictPtr+4:])
	bs.rep[2] = binary.LittleEndian.Uint32(dict[dictPtr+8:])
	dictPtr += 12

	{
		dictContentSize := uint64(dictEnd - dictPtr)
		offcodeMax := uint32(maxOff)
		if dictContentSize <= uint64(^uint32(0))-128<<10 {
			maxOffset := uint32(dictContentSize) + 128<<10 // The maximum offset that must be supported
			offcodeMax = highbit32(maxOffset)              // Calculate minimum offset code required to represent maxOffset
		}
		// All offset values <= dictContentSize + 128 KB must be representable for a valid table
		bs.entropy.fse.offcodeRepeatMode = dictNCountRepeat(offcodeNCount[:], offcodeMaxValue, uint(min(offcodeMax, maxOff)))

		// All repCodes must be <= dictContentSize and != 0
		for u := 0; u < 3; u++ {
			if bs.rep[u] == 0 || uint64(bs.rep[u]) > dictContentSize {
				return 0, errDictionaryCorrupted
			}
		}
	}
	return dictPtr, nil
}

// loadDictionaryContent appends src to ms's window and indexes it into the
// match finder tables. C: zstd_compress.c:ZSTD_loadDictionaryContent (no
// LDM). C also calls ZSTD_overflowCorrectIfNeeded before filling the
// tables; with at most currentMax - windowStartIndex bytes loaded into a
// window that was just reset (every path here loads into a fresh CDict),
// the index cannot exceed currentMax, so it never corrects.
func loadDictionaryContent(ms *matchState, params *cctxParams, src []byte, dtlm dictTableLoadMethod, tfp tableFillPurpose) {
	{ // Ensure large dictionaries can't cause index overflow

		// Allow the dictionary to set indices up to exactly currentMax.
		// Dictionaries right at the edge will immediately trigger overflow
		// correction, but I don't want to insert extra constraints here.
		maxDictSize := uint32(currentMax - windowStartIndex)

		if cdictIndicesAreTagged(&params.cParams) && tfp == tfpForCDict {
			// Some dictionary matchfinders in zstd use "short cache",
			// which treats the lower shortCacheTagBits of each CDict
			// hashtable entry as a tag rather than as part of an index.
			// When short cache is used, we need to truncate the
			// dictionary so that its indices don't overlap with the tag.
			const shortCacheMaxDictSize = 1<<(32-shortCacheTagBits) - windowStartIndex
			maxDictSize = min(maxDictSize, shortCacheMaxDictSize)
		}

		// If the dictionary is too large, only load the suffix of the dictionary.
		if uint64(len(src)) > uint64(maxDictSize) {
			src = src[len(src)-int(maxDictSize):]
		}
	}

	windowUpdate(&ms.window, src, false)
	baseIdx := ms.window.baseIdx
	iend := len(ms.window.base) // src ends the window's prefix
	ip := iend - len(src)

	// If the dict is larger than we can reasonably index in our tables,
	// only load the suffix.
	{
		maxDictSize := 1 << min(max(params.cParams.hashLog+3, params.cParams.chainLog+1), 31)
		if len(src) > maxDictSize {
			ip = iend - maxDictSize
			src = src[len(src)-maxDictSize:]
		}
	}

	ms.nextToUpdate = uint32(ip) + baseIdx
	ms.loadedDictEnd = uint32(iend) + baseIdx // C: forceWindow ? 0 : iend - base (forceWindow is never set)
	ms.forceNonContiguous = false             // C: params->deterministicRefPrefix (never set)

	if len(src) <= hashReadSize {
		return
	}

	switch params.cParams.strategy {
	case zstdFast:
		fillHashTable(ms, uint32(iend)+baseIdx, dtlm, tfp)
	case zstdDfast:
		fillDoubleHashTable(ms, uint32(iend)+baseIdx, dtlm, tfp)
	case zstdGreedy, zstdLazy, zstdLazy2:
		// dedicated dictionary search is never enabled (see createCDictAdvanced2)
		if params.useRowMatchFinder == psEnable {
			tagTableSize := 1 << params.cParams.hashLog
			clear(ms.tagTable[:tagTableSize])
			rowUpdate(ms, iend-hashReadSize)
		} else {
			insertAndFindFirstIndex(ms, iend-hashReadSize)
		}
	case zstdBtlazy2, zstdBtopt, zstdBtultra, zstdBtultra2: // we want the dictionary table fully sorted
		updateTree(ms, iend-hashReadSize, iend)
	}

	ms.nextToUpdate = uint32(iend) + baseIdx
}

// attachDictSizeCutoffs are the approximate sizes for each strategy past
// which copying the dictionary tables into the working context is faster
// than using them in-place. C: zstd_compress.c:attachDictSizeCutoffs
var attachDictSizeCutoffs = [zstdBtultra2 + 1]uint64{
	8 << 10,  // unused
	8 << 10,  // zstdFast
	16 << 10, // zstdDfast
	32 << 10, // zstdGreedy
	32 << 10, // zstdLazy
	32 << 10, // zstdLazy2
	32 << 10, // zstdBtlazy2
	32 << 10, // zstdBtopt
	8 << 10,  // zstdBtultra
	8 << 10,  // zstdBtultra2
}

// shouldAttachDict chooses between referencing the CDict's tables
// (dictMatchState) and copying them into the context.
// C: zstd_compress.c:ZSTD_shouldAttachDict — dedicatedDictSearch is never
// set, and attachDictPref (ZSTD_dictDefaultAttach) and forceWindow (0) keep
// their defaults.
//
// C: same function — Go also copies (never attaches) a CDict whose starting
// repcodes reach beyond its loaded content (repsInContent): attached, C's
// dictMatchState compressors would read before the dictionary (undefined
// behaviour, asserted against in C), while the extDict compressors of a
// copy validate repcodes. The Encoder's options reject such dictionaries
// (checkDictReps), so this only guards contexts loaded directly; for every
// other dictionary it does not change the choice.
func shouldAttachDict(cd *cdict, pledgedSrcSize uint64) bool {
	cutoff := attachDictSizeCutoffs[cd.matchState.cParams.strategy]
	return (pledgedSrcSize <= cutoff || pledgedSrcSize == contentSizeUnknown) && cd.repsInContent
}

// getCParamMode. C: zstd_compress.c:ZSTD_getCParamMode
func getCParamMode(cd *cdict, pledgedSrcSize uint64) cParamMode {
	if cd != nil && shouldAttachDict(cd, pledgedSrcSize) {
		return cpmAttachDict
	}
	return cpmNoAttachDict
}

// resetCCtxUsingCDict starts a frame from cd, attaching or copying it.
// C: zstd_compress.c:ZSTD_resetCCtx_usingCDict
func (c *cctx) resetCCtxUsingCDict(cd *cdict, params *cctxParams, pledgedSrcSize uint64, buffered bool) {
	attach := shouldAttachDict(cd, pledgedSrcSize)
	if cdictHook != nil {
		cdictHook(cd.matchState.cParams.strategy, attach)
	}
	if attach {
		c.resetCCtxByAttachingCDict(cd, *params, pledgedSrcSize, buffered)
	} else {
		c.resetCCtxByCopyingCDict(cd, *params, pledgedSrcSize, buffered)
	}
}

// resetCCtxByAttachingCDict starts a frame that searches cd's tables in
// place (dictMatchState). C: zstd_compress.c:ZSTD_resetCCtx_byAttachingCDict
//
// The dictionary's indices are translated to sit just below the prefix
// (dictIndexDelta), so where this port continues the context's indices
// and C restarts them (see resetCCtxInternal), every index of the frame,
// dictionary ones included, shifts uniformly: output is unchanged. The
// context's own table entries from earlier frames are below the new
// prefix, which starts at or after the previous frame's end, and are
// rejected as without a dictionary.
func (c *cctx) resetCCtxByAttachingCDict(cd *cdict, params cctxParams, pledgedSrcSize uint64, buffered bool) {
	{
		adjustedCDictCParams := cd.matchState.cParams
		windowLog := params.cParams.windowLog
		// Resize working context table params for input only, since the
		// dict has its own tables.
		// pledgedSrcSize == 0 means 0!
		params.cParams = adjustCParamsInternal(adjustedCDictCParams, pledgedSrcSize,
			uint64(cd.dictContentSize), cpmAttachDict, params.useRowMatchFinder)
		params.cParams.windowLog = windowLog
		params.useRowMatchFinder = cd.useRowMatchFinder // cdict overrides
		c.resetCCtxInternal(&params, pledgedSrcSize, 0, buffered)
	}

	{
		ms := &c.blockState.matchState
		cdictEnd := cd.matchState.window.nextSrc
		cdictLen := cdictEnd - cd.matchState.window.dictLimit
		if cdictLen != 0 { // don't even attach dictionaries with no contents
			ms.dictMatchState = &cd.matchState

			// prep working match state so dict matches never have
			// negative indices when they are translated to the working
			// context's index space.
			if ms.window.dictLimit < cdictEnd {
				ms.window.nextSrc = cdictEnd
				windowClear(&ms.window)
			}
			// loadedDictEnd is expressed within the referential of the active context
			ms.loadedDictEnd = ms.window.dictLimit
		}
	}

	c.dictID = cd.dictID

	// copy block state
	*c.blockState.prevCBlock = cd.cBlockState
}

// copyCDictTableIntoCCtx copies a CDict table, removing the short cache
// tags if present (see shortCacheTagBits).
// C: zstd_compress.c:ZSTD_copyCDictTableIntoCCtx
func copyCDictTableIntoCCtx(dst, src []uint32, cParams *compressionParameters) {
	if cdictIndicesAreTagged(cParams) {
		for i, taggedIndex := range src {
			dst[i] = taggedIndex >> shortCacheTagBits
		}
	} else {
		copy(dst, src)
	}
}

// resetCCtxByCopyingCDict starts a frame from a copy of cd's tables and
// window: cd's content becomes the frame's extDict.
// C: zstd_compress.c:ZSTD_resetCCtx_byCopyingCDict — C resets the context
// with ZSTDcrp_leaveDirty; here the reset may clean tables that are then
// overwritten anyway (hashTable3, which the CDict never fills, is zeroed).
func (c *cctx) resetCCtxByCopyingCDict(cd *cdict, params cctxParams, pledgedSrcSize uint64, buffered bool) {
	cdictCParams := &cd.matchState.cParams

	{
		windowLog := params.cParams.windowLog
		// Copy only compression parameters related to tables.
		params.cParams = *cdictCParams
		params.cParams.windowLog = windowLog
		params.useRowMatchFinder = cd.useRowMatchFinder
		c.resetCCtxInternal(&params, pledgedSrcSize, 0, buffered)
	}

	ms := &c.blockState.matchState
	dms := &cd.matchState

	// copy tables
	{
		var chainSize int
		if allocateChainTable(cdictCParams.strategy, cd.useRowMatchFinder, false) { // DDS guaranteed disabled
			chainSize = 1 << cdictCParams.chainLog
		}
		hSize := 1 << cdictCParams.hashLog

		copyCDictTableIntoCCtx(ms.hashTable[:hSize], dms.hashTable[:hSize], cdictCParams)

		// Do not copy cdict's chainTable if cctx has parameters such that
		// it would not use chainTable
		if allocateChainTable(c.appliedParams.cParams.strategy, c.appliedParams.useRowMatchFinder, false) {
			copyCDictTableIntoCCtx(ms.chainTable[:chainSize], dms.chainTable[:chainSize], cdictCParams)
		}
		// copy tag table
		if rowMatchFinderUsed(cdictCParams.strategy, cd.useRowMatchFinder) {
			copy(ms.tagTable[:hSize], dms.tagTable[:hSize])
			ms.hashSalt = dms.hashSalt
		}
	}

	// Zero the hashTable3, since the cdict never fills it
	clear(ms.hashTable3)

	// copy dictionary offsets
	ms.window = dms.window
	ms.nextToUpdate = dms.nextToUpdate
	ms.loadedDictEnd = dms.loadedDictEnd

	c.dictID = cd.dictID

	// copy block state
	*c.blockState.prevCBlock = cd.cBlockState
}
