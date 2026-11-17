// Port of the post-block splitter of libzstd v1.5.7
// lib/compress/zstd_compress.c (Copyright (c) Meta Platforms, Inc. and
// affiliates; BSD/GPLv2 dual license).

package zstd

import (
	"github.com/vahid-sohrabloo/fastcompress/internal/fse"
	"github.com/vahid-sohrabloo/fastcompress/internal/hist"
	"github.com/vahid-sohrabloo/fastcompress/internal/huf"
)

// hufSizeT is a HUF function's size_t result: the size, or C's error code
// (-code) for err. C: ZSTD_buildBlockEntropyStats_literals uses
// HUF_writeCTable_wksp's result unchecked in its size comparisons.
func hufSizeT(n int, err error) uint64 {
	switch err {
	case nil:
		return uint64(n)
	case huf.ErrGeneric:
		return ^uint64(0) // ERROR(GENERIC) = (size_t)-1
	default:
		return ^uint64(69) // ERROR(dstSize_tooSmall) = (size_t)-70, the only other error HUF_writeCTable_wksp returns here
	}
}

// buildBlockEntropyStatsLiterals builds the entropy of the literals: it
// stores the literals block type and the Huffman description in
// hufMetadata and returns the description's size.
// C: zstd_compress.c:ZSTD_buildBlockEntropyStats_literals. Deviation: the
// histogram (C: the start of the workspace) is a stack array; the node
// workspace is still the rest of wksp.
func buildBlockEntropyStatsLiterals(src []byte, prevHuf, nextHuf *hufCTables,
	hufMetadata *hufCTablesMetadata, literalsCompressionIsDisabled bool, wksp []byte, hufFlags int) (int, error) {
	srcSize := len(src)
	var countWksp [huf.SymbolValueMax + 1]uint32
	nodeWksp := wksp[(huf.SymbolValueMax+1)*4:]
	maxSymbolValue := uint32(huf.SymbolValueMax)
	huffLog := uint(litHufLog)
	repeat := prevHuf.repeatMode

	// Prepare nextEntropy assuming reusing the existing table
	*nextHuf = *prevHuf

	if literalsCompressionIsDisabled {
		hufMetadata.hType = setBasic
		return 0, nil
	}

	// small ? don't even attempt compression (speed opt)
	const compressLiteralsSizeMin = 63 // C: COMPRESS_LITERALS_SIZE_MIN, heuristic
	{
		minLitSize := compressLiteralsSizeMin
		if prevHuf.repeatMode == huf.RepeatValid {
			minLitSize = 6
		}
		if srcSize <= minLitSize {
			hufMetadata.hType = setBasic
			return 0, nil
		}
	}

	// Scan input and build symbol stats
	{
		largest := int(hist.Count(&countWksp, &maxSymbolValue, src)) // can't fail with maxSymbolValue 255
		if largest == srcSize {
			// only one literal symbol
			hufMetadata.hType = setRLE
			return 0, nil
		}
		if largest <= srcSize>>7+4 {
			// heuristic: likely not compressible
			hufMetadata.hType = setBasic
			return 0, nil
		}
	}

	// Validate the previous Huffman table
	if repeat == huf.RepeatCheck && !huf.ValidateCTable(prevHuf.cTable[:], &countWksp, uint(maxSymbolValue)) {
		repeat = huf.RepeatNone
	}

	// Build Huffman Tree
	clear(nextHuf.cTable[:])
	huffLog = huf.OptimalTableLog(huffLog, srcSize, uint(maxSymbolValue), nodeWksp, nextHuf.cTable[:], &countWksp, hufFlags)
	{
		maxBits, err := huf.BuildCTable(nextHuf.cTable[:], &countWksp, uint(maxSymbolValue), huffLog, nodeWksp)
		if err != nil {
			return 0, err
		}
		huffLog = maxBits
	}
	{
		// Build and write the CTable
		newCSize := uint64(huf.EstimateCompressedSize(nextHuf.cTable[:], &countWksp, uint(maxSymbolValue)))
		n, err := huf.WriteCTable(hufMetadata.hufDesBuffer[:], nextHuf.cTable[:], uint(maxSymbolValue), huffLog, nodeWksp)
		hSize := hufSizeT(n, err) // size_t arithmetic below, as in C
		// Check against repeating the previous CTable
		if repeat != huf.RepeatNone {
			oldCSize := uint64(huf.EstimateCompressedSize(prevHuf.cTable[:], &countWksp, uint(maxSymbolValue)))
			if oldCSize < uint64(srcSize) && (oldCSize <= hSize+newCSize || hSize+12 >= uint64(srcSize)) {
				*nextHuf = *prevHuf
				hufMetadata.hType = setRepeat
				return 0, nil
			}
		}
		if newCSize+hSize >= uint64(srcSize) {
			*nextHuf = *prevHuf
			hufMetadata.hType = setBasic
			return 0, nil
		}
		hufMetadata.hType = setCompressed
		nextHuf.repeatMode = huf.RepeatCheck
		if err != nil {
			return 0, err // C: the caller's FORWARD_IF_ERROR on the returned code
		}
		return n, nil
	}
}

// buildDummySequencesStatistics returns stats with all encoding types set
// to basic and updates nextEntropy's repeat modes.
// C: zstd_compress.c:ZSTD_buildDummySequencesStatistics
func buildDummySequencesStatistics(nextEntropy *fseCTables) symbolEncodingTypeStats {
	stats := symbolEncodingTypeStats{llType: setBasic, offType: setBasic, mlType: setBasic}
	nextEntropy.litlengthRepeatMode = fseRepeatNone
	nextEntropy.offcodeRepeatMode = fseRepeatNone
	nextEntropy.matchlengthRepeatMode = fseRepeatNone
	return stats
}

// buildBlockEntropyStatsSequences builds the entropy of the sequences: it
// stores the symbol compression modes and the FSE tables in fseMetadata
// and returns the tables' size.
// C: zstd_compress.c:ZSTD_buildBlockEntropyStats_sequences. Deviation: the
// histogram is a stack array (see entropyCompressSeqStoreInternal).
func buildBlockEntropyStatsSequences(ss *seqStore, prevEntropy, nextEntropy *fseCTables,
	params *cctxParams, fseMetadata *fseCTablesMetadata, wksp []byte) (int, error) {
	strat := params.cParams.strategy
	nbSeq := len(ss.sequences)
	var countWorkspace [256]uint32
	entropyWorkspace := wksp[(maxSeq+1)*4:]
	var stats symbolEncodingTypeStats
	if nbSeq != 0 {
		var err error
		stats, err = buildSequencesStatistics(ss, nbSeq, prevEntropy, nextEntropy, fseMetadata.fseTablesBuffer[:],
			strat, &countWorkspace, entropyWorkspace)
		if err != nil {
			return 0, err
		}
	} else {
		stats = buildDummySequencesStatistics(nextEntropy)
	}
	fseMetadata.llType = stats.llType
	fseMetadata.ofType = stats.offType
	fseMetadata.mlType = stats.mlType
	fseMetadata.lastCountSize = stats.lastCountSize
	return stats.size, nil
}

// buildBlockEntropyStats builds the entropy of the block into
// entropyMetadata (and nextEntropy). wksp must be at least
// entropyWorkspaceSize bytes. C: zstd_compress.c:ZSTD_buildBlockEntropyStats
func buildBlockEntropyStats(ss *seqStore, prevEntropy, nextEntropy *entropyCTables,
	params *cctxParams, entropyMetadata *entropyCTablesMetadata, wksp []byte) error {
	hufFlags := 0
	if params.cParams.strategy >= zstdBtultra { // C: HUF_OPTIMAL_DEPTH_THRESHOLD
		hufFlags = huf.FlagOptimalDepth
	}
	var err error
	entropyMetadata.hufMetadata.hufDesSize, err = buildBlockEntropyStatsLiterals(ss.literals,
		&prevEntropy.huf, &nextEntropy.huf, &entropyMetadata.hufMetadata,
		literalsCompressionIsDisabled(params), wksp, hufFlags)
	if err != nil {
		return err
	}
	entropyMetadata.fseMetadata.fseTablesSize, err = buildBlockEntropyStatsSequences(ss,
		&prevEntropy.fse, &nextEntropy.fse, params, &entropyMetadata.fseMetadata, wksp)
	return err
}

// estimateBlockSizeLiteral returns the size estimate of the literals
// section (header + content) of a block.
// C: zstd_compress.c:ZSTD_estimateBlockSize_literal
func estimateBlockSizeLiteral(literals []byte, h *hufCTables, hufMetadata *hufCTablesMetadata, writeEntropy bool) int {
	litSize := len(literals)
	var countWksp [256]uint32
	maxSymbolValue := uint32(huf.SymbolValueMax)
	literalSectionHeaderSize := 3 + b2i(litSize >= 1<<10) + b2i(litSize >= 16<<10)
	singleStream := litSize < 256

	switch hufMetadata.hType {
	case setBasic:
		return litSize
	case setRLE:
		return 1
	case setCompressed, setRepeat:
		hist.Count(&countWksp, &maxSymbolValue, literals) // can't fail with maxSymbolValue 255
		cLitSizeEstimate := int(huf.EstimateCompressedSize(h.cTable[:], &countWksp, uint(maxSymbolValue)))
		if writeEntropy {
			cLitSizeEstimate += hufMetadata.hufDesSize
		}
		if !singleStream {
			cLitSizeEstimate += 6 // multi-stream huffman uses 6-byte jump table
		}
		return cLitSizeEstimate + literalSectionHeaderSize
	}
	return 0 // impossible
}

// estimateBlockSizeSymbolType returns the size estimate of the
// FSE-compressed symbols (of, ml or ll) of a block. additionalBits nil
// means the code is also the number of additional bits (offsets).
// C: zstd_compress.c:ZSTD_estimateBlockSize_symbolType
func estimateBlockSizeSymbolType(typ symbolEncodingType, codeTable []byte, maxCode uint32,
	fseCTable []uint32, additionalBits []uint8, defaultNorm []int16, defaultNormLog uint32) int {
	nbSeq := len(codeTable)
	var countWksp [256]uint32
	var cSymbolTypeSizeEstimateInBits uint64
	max := maxCode

	hist.CountFast(&countWksp, &max, codeTable) // can't fail
	switch typ {
	case setBasic:
		// We selected this encoding type, so it must be valid.
		cSymbolTypeSizeEstimateInBits = crossEntropyCost(defaultNorm, defaultNormLog, countWksp[:], max)
	case setRLE:
		cSymbolTypeSizeEstimateInBits = 0
	case setCompressed, setRepeat:
		cSymbolTypeSizeEstimateInBits = fseBitCost(fse.CTable(fseCTable), countWksp[:], max)
	}
	if cSymbolTypeSizeEstimateInBits == errCost {
		return nbSeq * 10
	}
	for _, c := range codeTable {
		if additionalBits != nil {
			cSymbolTypeSizeEstimateInBits += uint64(additionalBits[c])
		} else {
			cSymbolTypeSizeEstimateInBits += uint64(c) // for offset, offset code is also the number of additional bits
		}
	}
	return int(cSymbolTypeSizeEstimateInBits >> 3)
}

// estimateBlockSizeSequences returns the size estimate of the sequences
// section (header + content) of a block.
// C: zstd_compress.c:ZSTD_estimateBlockSize_sequences
func estimateBlockSizeSequences(ofCodeTable, llCodeTable, mlCodeTable []byte, nbSeq int,
	fseTables *fseCTables, fseMetadata *fseCTablesMetadata, writeEntropy bool) int {
	sequencesSectionHeaderSize := 1 /* seqHead */ + 1 /* min seqSize size */ + b2i(nbSeq >= 128) + b2i(nbSeq >= longNbSeq)
	cSeqSizeEstimate := 0
	cSeqSizeEstimate += estimateBlockSizeSymbolType(fseMetadata.ofType, ofCodeTable[:nbSeq], maxOff,
		fseTables.offcodeCTable[:], nil, ofDefaultNorm[:], ofDefaultNormLog)
	cSeqSizeEstimate += estimateBlockSizeSymbolType(fseMetadata.llType, llCodeTable[:nbSeq], maxLL,
		fseTables.litlengthCTable[:], llBits[:], llDefaultNorm[:], llDefaultNormLog)
	cSeqSizeEstimate += estimateBlockSizeSymbolType(fseMetadata.mlType, mlCodeTable[:nbSeq], maxML,
		fseTables.matchlengthCTable[:], mlBits[:], mlDefaultNorm[:], mlDefaultNormLog)
	if writeEntropy {
		cSeqSizeEstimate += fseMetadata.fseTablesSize
	}
	return cSeqSizeEstimate + sequencesSectionHeaderSize
}

// estimateBlockSize returns the size estimate of a block of the given
// literals and codes. C: zstd_compress.c:ZSTD_estimateBlockSize
func estimateBlockSize(literals []byte, ofCodeTable, llCodeTable, mlCodeTable []byte, nbSeq int,
	entropy *entropyCTables, entropyMetadata *entropyCTablesMetadata, writeLitEntropy, writeSeqEntropy bool) int {
	literalsSize := estimateBlockSizeLiteral(literals, &entropy.huf, &entropyMetadata.hufMetadata, writeLitEntropy)
	seqSize := estimateBlockSizeSequences(ofCodeTable, llCodeTable, mlCodeTable, nbSeq,
		&entropy.fse, &entropyMetadata.fseMetadata, writeSeqEntropy)
	return seqSize + literalsSize + blockHeaderSize
}

// buildEntropyStatisticsAndEstimateSubBlockSize builds the entropy
// statistics of ss and returns its estimated compressed size.
// C: zstd_compress.c:ZSTD_buildEntropyStatisticsAndEstimateSubBlockSize
func (c *cctx) buildEntropyStatisticsAndEstimateSubBlockSize(ss *seqStore) (int, error) {
	entropyMetadata := &c.blockSplit.entropyMetadata
	if err := buildBlockEntropyStats(ss, &c.blockState.prevCBlock.entropy, &c.blockState.nextCBlock.entropy,
		&c.appliedParams, entropyMetadata, c.tmpWorkspace); err != nil {
		return 0, err
	}
	return estimateBlockSize(ss.literals, ss.ofCode, ss.llCode, ss.mlCode, len(ss.sequences),
		&c.blockState.nextCBlock.entropy, entropyMetadata,
		entropyMetadata.hufMetadata.hType == setCompressed, true), nil
}

// countSeqStoreLiteralsBytes returns the literal bytes represented in ss.
// C: zstd_compress.c:ZSTD_countSeqStoreLiteralsBytes
func countSeqStoreLiteralsBytes(ss *seqStore) int {
	literalsBytes := 0
	for i, seq := range ss.sequences {
		literalsBytes += int(seq.litLength)
		if uint32(i) == ss.longLengthPos && ss.longLengthType == lltLiteralLength {
			literalsBytes += 0x10000
		}
	}
	return literalsBytes
}

// countSeqStoreMatchBytes returns the match bytes represented in ss.
// C: zstd_compress.c:ZSTD_countSeqStoreMatchBytes
func countSeqStoreMatchBytes(ss *seqStore) int {
	matchBytes := 0
	for i, seq := range ss.sequences {
		matchBytes += int(seq.mlBase) + minMatch
		if uint32(i) == ss.longLengthPos && ss.longLengthType == lltMatchLength {
			matchBytes += 0x10000
		}
	}
	return matchBytes
}

// deriveSeqStoreChunk makes result the chunk [startIdx, endIdx) of the
// sequences of orig. The chunk shares orig's sequences, literals and code
// tables, as C's pointers do. C: zstd_compress.c:ZSTD_deriveSeqStoreChunk
func deriveSeqStoreChunk(result, orig *seqStore, startIdx, endIdx int) {
	*result = *orig
	litStart := 0
	if startIdx > 0 {
		result.sequences = orig.sequences[:startIdx]
		litStart = countSeqStoreLiteralsBytes(result)
	}

	// Move longLengthPos into the correct position if necessary
	if orig.longLengthType != lltNone {
		if orig.longLengthPos < uint32(startIdx) || orig.longLengthPos > uint32(endIdx) {
			result.longLengthType = lltNone
		} else {
			result.longLengthPos -= uint32(startIdx)
		}
	}
	result.sequences = orig.sequences[startIdx:endIdx]
	litEnd := len(orig.literals)
	if endIdx != len(orig.sequences) {
		litEnd = litStart + countSeqStoreLiteralsBytes(result)
	}
	// else: this accounts for possible last literals if the derived chunk
	// reaches the end of the block
	result.literals = orig.literals[litStart:litEnd]
	result.llCode = orig.llCode[startIdx:]
	result.mlCode = orig.mlCode[startIdx:]
	result.ofCode = orig.ofCode[startIdx:]
}

// resolveRepcodeToRawOffset returns the raw offset that the repcode offBase
// selects with ll0 and the history rep.
// C: zstd_compress.c:ZSTD_resolveRepcodeToRawOffset
func resolveRepcodeToRawOffset(rep *[repNum]uint32, offBase uint32, ll0 uint32) uint32 {
	adjustedRepCode := offBase - 1 + ll0 // [ 0 - 3 ]
	if adjustedRepCode == repNum {
		// litlength == 0 and offCode == 2 implies selection of first
		// repcode - 1. This is only valid if it results in a valid offset
		// value, aka > 0. rep[0] may be 1 in exceptional circumstances;
		// the resulting 0 is compared and discarded by
		// seqStoreResolveOffCodes.
		return rep[0] - 1
	}
	return rep[adjustedRepCode]
}

// seqStoreResolveOffCodes reconciles the offset history divergences that
// RLE or raw partitions cause and replaces the repcodes of ss that became
// invalid. dRep follows the decoder; cRep follows ss exactly.
// C: zstd_compress.c:ZSTD_seqStore_resolveOffCodes
func seqStoreResolveOffCodes(dRep, cRep *[repNum]uint32, ss *seqStore, nbSeq int) {
	longLitLenIdx := uint32(nbSeq)
	if ss.longLengthType == lltLiteralLength {
		longLitLenIdx = ss.longLengthPos
	}
	for idx := range nbSeq {
		seq := &ss.sequences[idx]
		ll0 := seq.litLength == 0 && uint32(idx) != longLitLenIdx
		offBase := seq.offBase
		if offBase <= repNum { // C: OFFBASE_IS_REPCODE
			dRawOffset := resolveRepcodeToRawOffset(dRep, offBase, uint32(b2i(ll0)))
			cRawOffset := resolveRepcodeToRawOffset(cRep, offBase, uint32(b2i(ll0)))
			// Adjust simulated decompression repcode history if we come
			// across a mismatch. Replace the repcode with the offset it
			// actually references, determined by the compression repcode
			// history.
			if dRawOffset != cRawOffset {
				seq.offBase = offsetToOffBase(cRawOffset)
			}
		}
		// Compression repcode history is always updated with values
		// directly from the unmodified seqStore. Decompression repcode
		// history may use modified seq.offBase value taken from
		// compression repcode history.
		updateRep(dRep, seq.offBase, ll0)
		updateRep(cRep, offBase, ll0)
	}
}

// rleCompressBlock writes an RLE block. C: zstd_compress_internal.h:ZSTD_rleCompressBlock
func rleCompressBlock(dst []byte, src byte, srcSize int, lastBlock uint32) (int, error) {
	if len(dst) < 4 {
		return 0, errDstSizeTooSmall
	}
	writeLE24(dst, lastBlock+btRLE<<1+uint32(srcSize)<<3)
	dst[3] = src
	return 4, nil
}

// compressSeqStoreSingleBlock compresses ss into one block with its header
// and returns the block's size. C: zstd_compress.c:ZSTD_compressSeqStore_singleBlock
// (no sequence collector)
func (c *cctx) compressSeqStoreSingleBlock(ss *seqStore, dRep, cRep *[repNum]uint32,
	dst, src []byte, lastBlock uint32, isPartition bool) (int, error) {
	const rleMaxLength = 25
	var cSize int

	// In case of an RLE or raw block, the simulated decompression repcode
	// history must be reset
	dRepOriginal := *dRep
	if isPartition {
		seqStoreResolveOffCodes(dRep, cRep, ss, len(ss.sequences))
	}

	if len(dst) < blockHeaderSize {
		return 0, errDstSizeTooSmall // Block header doesn't fit
	}
	cSeqsSize, err := entropyCompressSeqStore(dst[blockHeaderSize:], ss,
		&c.blockState.prevCBlock.entropy, &c.blockState.nextCBlock.entropy,
		&c.appliedParams, len(src), c.tmpWorkspace, c.bmi2)
	if err != nil {
		return 0, err
	}

	if !c.isFirstBlock && cSeqsSize < rleMaxLength && isRLE(src) {
		// We don't want to emit our first block as a RLE even if it
		// qualifies because doing so will cause the decoder (cli only) to
		// throw a "should consume all input error." This is only an issue
		// for zstd <= v1.4.3
		cSeqsSize = 1
	}

	switch cSeqsSize {
	case 0:
		cSize, err = noCompressBlock(dst, src, lastBlock)
		if err != nil {
			return 0, err
		}
		*dRep = dRepOriginal // reset simulated decompression repcode history
	case 1:
		cSize, err = rleCompressBlock(dst, src[0], len(src), lastBlock)
		if err != nil {
			return 0, err
		}
		*dRep = dRepOriginal // reset simulated decompression repcode history
	default:
		c.blockState.confirmRepcodesAndEntropyTables()
		writeLE24(dst, lastBlock+btCompressed<<1+uint32(cSeqsSize)<<3) // C: writeBlockHeader
		cSize = blockHeaderSize + cSeqsSize
	}

	if c.blockState.prevCBlock.entropy.fse.offcodeRepeatMode == fseRepeatValid {
		c.blockState.prevCBlock.entropy.fse.offcodeRepeatMode = fseRepeatCheck
	}
	return cSize, nil
}

// seqStoreSplits keeps track of where the recursive search is.
// C: zstd_compress.c:seqStoreSplits
type seqStoreSplits struct {
	splitLocations []uint32 // array of split indices
	idx            int      // the current index within splitLocations being worked on
}

// minSequencesBlockSplitting. C: zstd_compress.c:MIN_SEQUENCES_BLOCK_SPLITTING
const minSequencesBlockSplitting = 300

// deriveBlockSplitsHelper estimates the cost of the sequences
// [startIdx, endIdx) of orig as one block and as two halves, and recurses
// into the halves when splitting is estimated to pay. The recursion depth
// is capped by minSequencesBlockSplitting, the number of splits by
// maxNbBlockSplits. C: zstd_compress.c:ZSTD_deriveBlockSplitsHelper
func (c *cctx) deriveBlockSplitsHelper(splits *seqStoreSplits, startIdx, endIdx int, orig *seqStore) {
	bs := c.blockSplit
	fullSeqStoreChunk := &bs.fullSeqStoreChunk
	firstHalfSeqStore := &bs.firstHalfSeqStore
	secondHalfSeqStore := &bs.secondHalfSeqStore
	midIdx := (startIdx + endIdx) / 2

	if endIdx-startIdx < minSequencesBlockSplitting || splits.idx >= maxNbBlockSplits {
		return // too few sequences
	}
	deriveSeqStoreChunk(fullSeqStoreChunk, orig, startIdx, endIdx)
	deriveSeqStoreChunk(firstHalfSeqStore, orig, startIdx, midIdx)
	deriveSeqStoreChunk(secondHalfSeqStore, orig, midIdx, endIdx)
	estimatedOriginalSize, err1 := c.buildEntropyStatisticsAndEstimateSubBlockSize(fullSeqStoreChunk)
	estimatedFirstHalfSize, err2 := c.buildEntropyStatisticsAndEstimateSubBlockSize(firstHalfSeqStore)
	estimatedSecondHalfSize, err3 := c.buildEntropyStatisticsAndEstimateSubBlockSize(secondHalfSeqStore)
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	if estimatedFirstHalfSize+estimatedSecondHalfSize < estimatedOriginalSize {
		c.deriveBlockSplitsHelper(splits, startIdx, midIdx, orig)
		splits.splitLocations[splits.idx] = uint32(midIdx)
		splits.idx++
		c.deriveBlockSplitsHelper(splits, midIdx, endIdx, orig)
	}
}

// deriveBlockSplits fills partitions with the intra-block partition
// indices that should improve the compression ratio and returns the number
// of splits (the partition count minus 1).
// C: zstd_compress.c:ZSTD_deriveBlockSplits
func (c *cctx) deriveBlockSplits(partitions []uint32, nbSeq int) int {
	splits := seqStoreSplits{splitLocations: partitions}
	if nbSeq <= 4 {
		return 0 // refuse to try and split anything with less than 4 sequences
	}
	c.deriveBlockSplitsHelper(&splits, 0, nbSeq, &c.seqStore)
	splits.splitLocations[splits.idx] = uint32(nbSeq)
	return splits.idx
}

// compressBlockSplitBlockInternal compresses the block src, whose
// sequences are in c.seqStore, as one or more blocks with headers and
// returns their combined size.
// C: zstd_compress.c:ZSTD_compressBlock_splitBlock_internal
func (c *cctx) compressBlockSplitBlockInternal(dst, src []byte, lastBlock uint32, nbSeq int) (int, error) {
	cSize := 0
	ip := 0
	op := 0
	srcBytesTotal := 0
	bs := c.blockSplit
	partitions := bs.partitions[:] // size == maxNbBlockSplits
	nextSeqStore := &bs.nextSeqStore
	currSeqStore := &bs.currSeqStore
	numSplits := c.deriveBlockSplits(partitions, nbSeq)

	// If a block is split and some partitions are emitted as
	// RLE/uncompressed, then repcode history may become invalid. In order to
	// reconcile potentially invalid repcodes, we keep track of two separate
	// repcode histories that simulate repcode history on compression and
	// decompression side, and use the histories to determine whether we
	// must replace a particular repcode with its raw offset.
	//
	// 1) cRep gets updated for each partition, regardless of whether the
	//    block was emitted as uncompressed or RLE. This allows us to
	//    retrieve the offset value that an invalid repcode references
	//    within a nocompress/RLE block.
	// 2) dRep gets updated only for compressed partitions, and when a
	//    repcode gets replaced, will use the replacement offset value rather
	//    than the original repcode to update the repcode history. dRep also
	//    will be the final repcode history sent to the next block.
	//
	// See seqStoreResolveOffCodes for more details.
	dRep := c.blockState.prevCBlock.rep
	cRep := c.blockState.prevCBlock.rep
	*nextSeqStore = seqStore{}

	if numSplits == 0 {
		return c.compressSeqStoreSingleBlock(&c.seqStore, &dRep, &cRep, dst, src, lastBlock, false)
	}
	c.splitBlocks++

	deriveSeqStoreChunk(currSeqStore, &c.seqStore, 0, int(partitions[0]))
	for i := 0; i <= numSplits; i++ {
		lastPartition := i == numSplits
		var lastBlockEntireSrc uint32

		srcBytes := countSeqStoreLiteralsBytes(currSeqStore) + countSeqStoreMatchBytes(currSeqStore)
		srcBytesTotal += srcBytes
		if lastPartition {
			// This is the final partition, need to account for possible
			// last literals
			srcBytes += len(src) - srcBytesTotal
			lastBlockEntireSrc = lastBlock
		} else {
			deriveSeqStoreChunk(nextSeqStore, &c.seqStore, int(partitions[i]), int(partitions[i+1]))
		}

		cSizeChunk, err := c.compressSeqStoreSingleBlock(currSeqStore, &dRep, &cRep,
			dst[op:], src[ip:ip+srcBytes], lastBlockEntireSrc, true)
		if err != nil {
			return 0, err
		}

		ip += srcBytes
		op += cSizeChunk
		cSize += cSizeChunk
		*currSeqStore = *nextSeqStore
	}
	// cRep and dRep may have diverged during the compression. If so, we use
	// the dRep repcodes for the next block.
	c.blockState.prevCBlock.rep = dRep
	return cSize, nil
}

// compressBlockSplitBlock compresses the block src (at index srcIdx),
// possibly split into several blocks, and returns the size written to dst
// (headers included). C: zstd_compress.c:ZSTD_compressBlock_splitBlock (no
// sequence collector)
func (c *cctx) compressBlockSplitBlock(dst, src []byte, srcIdx uint32, lastBlock uint32) (int, error) {
	if c.blockSplit == nil { // C: part of the cctx
		c.blockSplit = new(blockSplitCtx)
	}
	bss, err := c.buildSeqStore(src, srcIdx)
	if err != nil {
		return 0, err
	}
	if bss == bssNoCompress {
		if c.blockState.prevCBlock.entropy.fse.offcodeRepeatMode == fseRepeatValid {
			c.blockState.prevCBlock.entropy.fse.offcodeRepeatMode = fseRepeatCheck
		}
		return noCompressBlock(dst, src, lastBlock)
	}
	nbSeq := len(c.seqStore.sequences)
	return c.compressBlockSplitBlockInternal(dst, src, lastBlock, nbSeq)
}
