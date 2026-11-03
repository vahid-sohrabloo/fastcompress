package zstd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
	"github.com/vahid-sohrabloo/fastzstd/internal/huf"
	"github.com/vahid-sohrabloo/fastzstd/internal/xxhash"
)

// BuildDictOptions contains options used for creating a dictionary.
type BuildDictOptions struct {
	// Dictionary ID.
	ID uint32

	// Content to use to create dictionary tables.
	Contents [][]byte

	// History to use for all blocks.
	History []byte

	// Offsets to use.
	Offsets [3]int

	// CompatV155 will make the dictionary compatible with Zstd v1.5.5 and earlier.
	// See https://github.com/facebook/zstd/issues/3724
	CompatV155 bool

	// Use the specified encoder level.
	// The dictionary will be built using the specified encoder level,
	// which will reflect speed and make the dictionary tailored for that level.
	// If not set SpeedBestCompression will be used.
	Level EncoderLevel

	// DebugOut will write stats and other details here if set.
	DebugOut io.Writer
}

// BuildDict will build a dictionary from the provided options.
//
// The dictionary is C's ZDICT_finalizeDictionary output (libzstd v1.5.7)
// for History as the content, Contents as the samples, the C level of Level
// (SpeedBestCompression when unset) and ID (0: C's ID derived from the
// XXH64 of History). The output buffer has room for History plus 64 KiB,
// so History is never truncated.
//
// Deviation: non-zero Offsets replace C's starting repcodes {1, 4, 8} in
// the dictionary (C writes no others); every offset must then be positive,
// and History is zero-padded in front up to the largest one, as C pads it
// up to 8. All-zero Offsets give C's bytes.
//
// CompatV155 does not change the result: C's dictionaries already load in
// zstd v1.5.5 and earlier.
//
// DebugOut receives C's messages at notification level 4.
func BuildDict(o BuildDictOptions) ([]byte, error) {
	level := o.Level
	if level == levelUnset {
		level = SpeedBestCompression
	}
	if level < SpeedFastest || level > SpeedBestCompression {
		return nil, fmt.Errorf("zstd: unknown encoder level %d", int(o.Level))
	}
	reps := repStartValue
	if o.Offsets != [3]int{} {
		for i, off := range o.Offsets {
			if off <= 0 || uint64(off) > math.MaxUint32 {
				return nil, fmt.Errorf("zstd: BuildDict offset %d out of range", off)
			}
			reps[i] = uint32(off)
		}
	}
	return finalizeDictionary(o.History, o.Contents, zstdLevels[level], o.ID, reps, o.DebugOut)
}

// buildDictError is a ZDICT_finalizeDictionary (or trainer) failure: C's
// error code.
type buildDictError struct {
	code int    // C: ZSTD_ErrorCode
	err  string // the failing step's error, if it has a Go one
	// notPorted: C would go on, through a path that is not ported (LDM,
	// the btultra and btultra2 strategies)
	notPorted bool
	// undefined: C's behaviour is undefined here (it crashes)
	undefined bool
	op        string // the public function; "" is BuildDict
}

func (e *buildDictError) Error() string {
	name, ok := cErrorNames[e.code]
	if !ok {
		name = fmt.Sprintf("error code %d", e.code)
	}
	if e.err != "" {
		name += " (" + e.err + ")"
	}
	op := e.op
	if op == "" {
		op = "BuildDict"
	}
	return "zstd: " + op + ": " + name
}

// C error codes ZDICT_finalizeDictionary returns besides cErrorNames'.
const (
	cErrDictionaryCreationFailed = 34 // C: ZSTD_error_dictionaryCreation_failed
	cErrMemoryAllocation         = 64 // C: ZSTD_error_memory_allocation
)

// entropyStepError maps an error of a HUF or FSE step to C's error code.
func entropyStepError(err error) error {
	code := int(dErrGeneric)
	if errors.Is(err, huf.ErrDstSizeTooSmall) || errors.Is(err, fse.ErrDstSizeTooSmall) {
		code = int(dErrDstSizeTooSmall)
	}
	return &buildDictError{code: code, err: err.Error()}
}

// debugf writes a C DISPLAYLEVEL message to w (nil: none). Every message of
// zdict.c's finalize path has a level <= 4, the level DebugOut gets.
func debugf(w io.Writer, format string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, format, a...)
	}
}

const (
	zdictMaxRepOffset = 1024 // C: zdict.c:MAXREPOFFSET
	zdictOffcodeMax   = 30   // C: zdict.c:OFFCODE_MAX (only applicable to first block)
	zdictHBuffSize    = 256  // C: zdict.c:HBUFFSIZE, large enough for all entropy headers
	zdictDictSizeMin  = 256  // C: zdict.h:ZDICT_DICTSIZE_MIN
	// zdictCapacityExtra is the output room beyond the content, as
	// `cref finalize-dict` gives C.
	zdictCapacityExtra = 64 << 10
)

// entropyStats are the statistics countEStats gathers.
type entropyStats struct {
	countLit         [256]uint32
	offcodeCount     [maxOff + 1]uint32 // C: [OFFCODE_MAX+1]; one more slot keeps every offset code in bounds
	matchLengthCount [maxML + 1]uint32
	litLengthCount   [maxLL + 1]uint32
	repOffset        [zdictMaxRepOffset]uint32
}

// compressBeginUsingCDict starts a block-mode compression from cd (no
// frame parameters, unknown size).
// C: zstd_compress.c:ZSTD_compressBegin_usingCDict_deprecated, through
// ZSTD_compressBegin_usingCDict_internal (the CDict's parameters: the size
// is unknown) and ZSTD_compressBegin_internal. LDM stays disabled
// (ZSTD_resolveEnableLdm needs btopt or above with windowLog >= 27;
// analyzeEntropy refuses such parameters before getting here).
func (c *cctx) compressBeginUsingCDict(cd *cdict) {
	// C: ZSTD_CCtxParams_init_internal(&cctxParams, &params, ZSTD_NO_CLEVEL)
	var params cctxParams
	params.cParams = cd.matchState.cParams
	params.useRowMatchFinder = resolveRowMatchFinderMode(psAuto, &params.cParams)
	params.postBlockSplitter = resolveBlockSplitterMode(psAuto, &params.cParams)
	params.maxBlockSize = resolveMaxBlockSize(0)

	if cd.dictContentSize > 0 {
		c.resetCCtxUsingCDict(cd, &params, contentSizeUnknown, false)
		return
	}
	c.resetCCtxInternal(&params, contentSizeUnknown, 0, false)
	c.dictID = 0 // C: ZSTD_compress_insertDictionary of no content
}

// compressBlockDeprecated compresses src as one block without a header
// into dst and returns its size: 0 when it is not compressible.
// C: zstd_compress.c:ZSTD_compressBlock_deprecated, through
// ZSTD_compressContinue_internal in block mode (frame 0). The size is
// unknown, so C's pledged-size check never applies.
func (c *cctx) compressBlockDeprecated(dst, src []byte) (int, error) {
	// C: ZSTD_getBlockSize_deprecated
	if len(src) > min(c.appliedParams.maxBlockSize, 1<<c.appliedParams.cParams.windowLog) {
		return 0, errSrcSizeWrong // input is larger than a block
	}
	if len(src) == 0 {
		return 0, nil // do not generate an empty block if no input
	}
	ms := &c.blockState.matchState
	if !windowUpdate(&ms.window, src, ms.forceNonContiguous) {
		ms.forceNonContiguous = false
		ms.nextToUpdate = ms.window.dictLimit
	}
	// overflow check and correction for block mode; src ends at nextSrc
	srcIdx := ms.window.nextSrc - uint32(len(src))
	overflowCorrectIfNeeded(ms, &c.appliedParams, srcIdx, srcIdx+uint32(len(src)))
	srcIdx = ms.window.nextSrc - uint32(len(src))
	cSize, err := c.compressBlockInternal(dst, src, srcIdx, false)
	if err != nil {
		return 0, err
	}
	c.consumedSrcSize += uint64(len(src))
	c.producedCSize += uint64(cSize)
	return cSize, nil
}

// countEStats compresses one sample as a block with the dictionary cd and
// adds its literals, codes and first two offsets to st. Samples above a
// block (min(128 KiB, 1 << windowLog), windowLog from ZSTD_getParams) are
// cut to one; samples the context rejects as larger than its own block size
// are skipped. C: zdict.c:ZDICT_countEStats
//
// It fails only for a strategy this port does not have (btultra and
// btultra2, C levels 13 and up for small samples), where C would go on.
func countEStats(c *cctx, cd *cdict, windowLog uint32, st *entropyStats, src, work []byte, debug io.Writer) error {
	if maxSize := min(blockSizeMax, 1<<windowLog); len(src) > maxSize {
		src = src[:maxSize] // protection vs large samples
	}
	c.compressBeginUsingCDict(cd)
	cSize, err := c.compressBlockDeprecated(work, src)
	if errors.Is(err, errStrategyUnsupported) {
		return &buildDictError{code: int(dErrGeneric), err: err.Error(), notPorted: true}
	}
	if err != nil {
		debugf(debug, "warning : could not compress sample size %d \n", len(src))
		return nil
	}
	if cSize == 0 {
		return nil // block is not compressible
	}
	ss := &c.seqStore

	// literals stats
	for _, b := range ss.literals {
		st.countLit[b]++
	}

	// seqStats
	nbSeq := len(ss.sequences)
	seqToCodes(ss)
	for _, code := range ss.ofCode[:nbSeq] {
		st.offcodeCount[code]++
	}
	for _, code := range ss.mlCode[:nbSeq] {
		st.matchLengthCount[code]++
	}
	for _, code := range ss.llCode[:nbSeq] {
		st.litLengthCount[code]++
	}

	if nbSeq >= 2 { // rep offsets
		offset1 := ss.sequences[0].offBase - repNum
		offset2 := ss.sequences[1].offBase - repNum
		if offset1 >= zdictMaxRepOffset {
			offset1 = 0
		}
		if offset2 >= zdictMaxRepOffset {
			offset2 = 0
		}
		st.repOffset[offset1] += 3
		st.repOffset[offset2]++
	}
	return nil
}

// offsetCount. C: zdict.c:offsetCount_t
type offsetCount struct{ offset, count uint32 }

// insertSortCount inserts (val, count) into table, which keeps the repNum
// largest counts in decreasing order. C: zdict.c:ZDICT_insertSortCount
func insertSortCount(table *[repNum + 1]offsetCount, val, count uint32) {
	table[repNum] = offsetCount{val, count}
	for u := repNum; u > 0; u-- {
		if table[u-1].count >= table[u].count {
			break
		}
		table[u-1], table[u] = table[u], table[u-1]
	}
}

// flatLit rewrites countLit into a mostly flat but still compressible
// distribution of literals, which HUF_writeCTable can encode (a
// non-compressible one it cannot). C: zdict.c:ZDICT_flatLit
func flatLit(countLit *[256]uint32) {
	for u := 1; u < 256; u++ {
		countLit[u] = 2
	}
	countLit[0] = 4
	countLit[253] = 1
	countLit[254] = 1
}

// offcodeMaxFor is the largest offset code a dictionary of dictSize bytes
// allows for a first block. C: zdict.c:ZDICT_analyzeEntropy's offcodeMax
// (computed in 32 bits, as C's (U32) cast; highbit32(0) is out of range).
func offcodeMaxFor(dictSize int) uint32 {
	return highbit32(uint32(dictSize + blockSizeMax))
}

// analyzeEntropy writes the entropy tables and repcodes of a dictionary
// with content dict, from the statistics of samples compressed with it at
// level, into dst and returns their size. C: zdict.c:ZDICT_analyzeEntropy
// (which writes repStartValue; reps is written instead, see BuildDict).
func analyzeEntropy(dst []byte, level int, samples [][]byte, dict []byte, reps [repNum]uint32, debug io.Writer) (int, error) {
	// Go guard: from 4 GiB - 128 KiB C's (U32) cast in offcodeMax wraps
	// and lets the history through, after which C writes offcodeCount
	// out of bounds (undefined behaviour). It is rejected with the code C
	// gives the sizes below the wrap, before anything is allocated.
	if uint64(len(dict))+blockSizeMax >= 1<<32 {
		return 0, &buildDictError{code: cErrDictionaryCreationFailed} // too large dictionary
	}
	offcodeMax := offcodeMaxFor(len(dict))
	if offcodeMax > zdictOffcodeMax {
		return 0, &buildDictError{code: cErrDictionaryCreationFailed} // too large dictionary
	}

	// init
	st := new(entropyStats)
	for u := range st.countLit {
		st.countLit[u] = 1 // any character must be described
	}
	for u := range st.offcodeCount[:offcodeMax+1] {
		st.offcodeCount[u] = 1
	}
	for u := range st.matchLengthCount {
		st.matchLengthCount[u] = 1
	}
	for u := range st.litLengthCount {
		st.litLengthCount[u] = 1
	}
	st.repOffset[1], st.repOffset[4], st.repOffset[8] = 1, 1, 1
	if level == 0 {
		level = clevelDefault
	}
	var totalSrcSize uint64
	for _, s := range samples {
		totalSrcSize += uint64(len(s))
	}
	averageSampleSize := totalSrcSize / uint64(max(len(samples), 1))
	// C: ZSTD_getParams: a size hint of 0 means unknown
	srcSizeHint := averageSampleSize
	if srcSizeHint == 0 {
		srcSizeHint = contentSizeUnknown
	}
	cParams := getCParamsInternal(level, srcSizeHint, uint64(len(dict)), cpmUnknown)

	// C: ZSTD_createCDict_advanced(dict, ZSTD_dlm_byRef, ZSTD_dct_rawContent, cParams)
	var cdParams cctxParams
	cctxParamsInit(&cdParams, 0)
	cdParams.cParams = cParams
	// compressBeginUsingCDict does not port C's LDM, which
	// ZSTD_resolveEnableLdm turns on for btopt and above with windowLog >=
	// 27 (C level 22 with a history of 64 MiB or more). Refuse rather than
	// differ from C, before building the CDict (whose parameters these
	// are); BuildDict's levels (<= 11) never get here.
	if cp := getCParamsFromCCtxParams(&cdParams, contentSizeUnknown, uint64(len(dict)), cpmCreateCDict); cp.strategy >= zstdBtopt && cp.windowLog >= 27 {
		return 0, &buildDictError{code: int(dErrGeneric), err: fmt.Sprintf("level %d needs long-distance matching, which is not ported", level), notPorted: true}
	}
	cd, err := createCDictAdvanced2(dict, dctRawContent, &cdParams)
	if err != nil {
		debugf(debug, "Not enough memory \n")
		return 0, &buildDictError{code: cErrMemoryAllocation, err: err.Error()}
	}
	c := newCCtx()
	work := make([]byte, blockSizeMax)

	// collect stats on all samples
	for _, s := range samples {
		if err := countEStats(c, cd, cParams.windowLog, st, s, work, debug); err != nil {
			return 0, err
		}
	}

	if debug != nil {
		// writeStats
		debugf(debug, "Offset Code Frequencies : \n")
		for u := range offcodeMax + 1 {
			debugf(debug, "%2d :%7d \n", u, st.offcodeCount[u])
		}
	}

	// analyze, build stats, starting with literals
	hufTable := make(huf.CTable, huf.CTableSize(255))
	wksp := make([]byte, huf.CTableWorkspaceSize)
	maxNbBits, err := huf.BuildCTable(hufTable, &st.countLit, 255, litHufLog, wksp)
	if err != nil {
		debugf(debug, " HUF_buildCTable error \n")
		return 0, entropyStepError(err)
	}
	if maxNbBits == 8 { // not compressible: will fail on HUF_writeCTable()
		debugf(debug, "warning : pathological dataset : literals are not compressible : samples are noisy or too regular \n")
		flatLit(&st.countLit)
		maxNbBits, err = huf.BuildCTable(hufTable, &st.countLit, 255, litHufLog, wksp) // 9 bits (C asserts it)
		if err != nil {
			return 0, entropyStepError(err)
		}
	}
	huffLog := maxNbBits

	// looking for most common first offsets
	var bestRepOffset [repNum + 1]offsetCount
	for offset := uint32(1); offset < zdictMaxRepOffset; offset++ {
		insertSortCount(&bestRepOffset, offset, st.repOffset[offset])
	}
	// note: the result of this phase should be used to better appreciate
	// the impact on statistics (C does not use it either)

	var offcodeNCount [zdictOffcodeMax + 1]int16
	var matchLengthNCount [maxML + 1]int16
	var litLengthNCount [maxLL + 1]int16
	normalize := func(norm []int16, tableLog uint, count []uint32, what string) (uint, error) {
		var total uint32 // C: U32
		for _, n := range count {
			total += n
		}
		log, err := fse.NormalizeCount(norm, tableLog, count, int(total), uint(len(count)-1), true)
		if err != nil {
			debugf(debug, "FSE_normalizeCount error with %s \n", what)
			return 0, entropyStepError(err)
		}
		return log, nil
	}
	offLog, err := normalize(offcodeNCount[:], offFSELog, st.offcodeCount[:offcodeMax+1], "offcodeCount")
	if err != nil {
		return 0, err
	}
	mlLog, err := normalize(matchLengthNCount[:], mlFSELog, st.matchLengthCount[:], "matchLengthCount")
	if err != nil {
		return 0, err
	}
	llLog, err := normalize(litLengthNCount[:], llFSELog, st.litLengthCount[:], "litLengthCount")
	if err != nil {
		return 0, err
	}

	// write result to buffer
	pos, err := huf.WriteCTable(dst, hufTable, 255, huffLog, wksp)
	if err != nil {
		debugf(debug, "HUF_writeCTable error \n")
		return 0, entropyStepError(err)
	}
	for _, t := range []struct {
		norm     []int16
		maxSym   uint
		tableLog uint
		what     string
	}{
		{offcodeNCount[:], zdictOffcodeMax, offLog, "offcodeNCount"},
		{matchLengthNCount[:], maxML, mlLog, "matchLengthNCount"},
		{litLengthNCount[:], maxLL, llLog, "litlengthNCount"},
	} {
		n, err := fse.WriteNCount(dst[pos:], t.norm, t.maxSym, t.tableLog)
		if err != nil {
			debugf(debug, "FSE_writeNCount error with %s \n", t.what)
			return 0, entropyStepError(err)
		}
		pos += n
	}

	if len(dst)-pos < 12 {
		debugf(debug, "not enough space to write RepOffsets \n")
		return 0, &buildDictError{code: int(dErrDstSizeTooSmall)}
	}
	for i, r := range reps {
		binary.LittleEndian.PutUint32(dst[pos+4*i:], r)
	}
	return pos + 12, nil
}

// finalizeDictionary builds a zstd dictionary from content: header (magic,
// dictID, the entropy tables analyzeEntropy derives from samples at level,
// and reps), zero padding up to the largest repcode, then content. dictID 0
// means C's ID derived from content. The capacity is len(content) + 64 KiB,
// as `cref finalize-dict` gives C, so content is never truncated.
// C: zdict.c:ZDICT_finalizeDictionary (C's minContentSize is
// ZDICT_maxRep(repStartValue); it is the largest of reps here)
//
// Precondition: level must not select C's long-distance matcher (btopt or
// above with windowLog >= 27, reachable from C level 22 with a 64 MiB
// history): it is not ported, and analyzeEntropy returns an error instead.
// So it does for levels that select btultra or btultra2 (not ported either)
// when a sample is compressed.
func finalizeDictionary(content []byte, samples [][]byte, level int, dictID uint32, reps [repNum]uint32, debug io.Writer) ([]byte, error) {
	return finalizeDictionaryCap(content, samples, level, dictID, reps, uint64(len(content))+zdictCapacityExtra, debug)
}

// finalizeDictionaryCap is finalizeDictionary into a dictionary of at most
// capacity bytes (C's dictBufferCapacity): content that does not fit after
// the header is cut at its end, as C does.
func finalizeDictionaryCap(content []byte, samples [][]byte, level int, dictID uint32, reps [repNum]uint32, capacity uint64, debug io.Writer) ([]byte, error) {
	var header [zdictHBuffSize]byte
	if level == 0 {
		level = clevelDefault
	}
	// The final dictionary content must be at least as large as the
	// largest repcode.
	minContentSize := uint64(max(reps[0], reps[1], reps[2]))

	// check conditions
	if capacity < uint64(len(content)) || capacity < zdictDictSizeMin {
		return nil, &buildDictError{code: int(dErrDstSizeTooSmall)}
	}

	// dictionary header
	binary.LittleEndian.PutUint32(header[:], dictMagic)
	if dictID == 0 {
		randomID := xxhash.Sum64(content, 0)
		dictID = uint32(randomID%((1<<31)-32768)) + 32768 // compliant ID
	}
	binary.LittleEndian.PutUint32(header[4:], dictID)
	hSize := 8

	// entropy tables
	debugf(debug, "\r%70s\r", "") // clean display line
	debugf(debug, "statistics ... \n")
	eSize, err := analyzeEntropy(header[hSize:], level, samples, content, reps, debug)
	if err != nil {
		return nil, err
	}
	hSize += eSize

	// Shrink the content size if it doesn't fit in the buffer (C keeps
	// its start)
	if uint64(hSize)+uint64(len(content)) > capacity {
		content = content[:capacity-uint64(hSize)]
	}

	// Pad the dictionary content with zeros if it is too small
	var paddingSize int
	if uint64(len(content)) < minContentSize {
		if uint64(hSize)+minContentSize > capacity {
			return nil, &buildDictError{code: int(dErrDstSizeTooSmall), err: "capacity too small to fit max repcode"}
		}
		paddingSize = int(minContentSize) - len(content)
	}

	// The padding comes before the content because the "best" position in
	// the dictionary is the last byte.
	out := make([]byte, hSize+paddingSize+len(content))
	copy(out, header[:hSize])
	copy(out[hSize+paddingSize:], content)
	return out, nil
}
