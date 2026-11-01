// Package huf ports the encoder side of lib/compress/huf_compress.c from
// libzstd v1.5.7 (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2
// dual license), with the pieces of lib/common/huf.h it needs.
//
// HUF_readCTable is here too (with HUF_readStats, in entropy_common.go), to
// load a dictionary's table; the Huffman decoder is not part of this
// package.
package huf

import (
	"encoding/binary"
	"errors"
	"math/bits"

	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
	"github.com/vahid-sohrabloo/fastzstd/internal/hist"
)

// Limits. C: huf.h
const (
	BlockSizeMax    = 128 * 1024 // C: HUF_BLOCKSIZE_MAX
	TableLogMax     = 12         // C: HUF_TABLELOG_MAX
	TableLogDefault = 11         // C: HUF_TABLELOG_DEFAULT
	SymbolValueMax  = 255        // C: HUF_SYMBOLVALUE_MAX

	// WorkspaceSize is C's HUF_WORKSPACE_SIZE. The Go port keeps its scratch
	// on the stack (C: huf_compress.c:HUF_compress_tables_t etc. live in the
	// caller's workspace; this cannot change output), so a wksp argument is
	// only length-checked, at C's error points, and never written.
	WorkspaceSize = (8 << 10) + 512

	// CTableWorkspaceSize is C's HUF_CTABLE_WORKSPACE_SIZE.
	CTableWorkspaceSize = (4*(SymbolValueMax+1) + 192) * 4

	// compressTablesSize is sizeof(HUF_compress_tables_t): count[256] +
	// CTable[257] + max(buildCTable tables, writeCTable wksp, hist wksp).
	compressTablesSize = 256*4 + (SymbolValueMax+2)*8 + CTableWorkspaceSize

	// SuspectIncompressibleSampleSize/Ratio. C: SUSPECT_INCOMPRESSIBLE_SAMPLE_*
	suspectIncompressibleSampleSize  = 4096
	suspectIncompressibleSampleRatio = 10
)

// Flags. C: huf.h:HUF_flags_e. FlagBMI2, FlagDisableAsm and FlagDisableFast
// are accepted and ignored: the encoder has a single Go body whose output
// does not depend on them.
const (
	FlagBMI2                  = 1 << 0
	FlagOptimalDepth          = 1 << 1
	FlagPreferRepeat          = 1 << 2
	FlagSuspectUncompressible = 1 << 3
	FlagDisableAsm            = 1 << 4
	FlagDisableFast           = 1 << 5
)

// Errors, mirroring the C error codes returned at the same points.
var (
	ErrGeneric           = errors.New("huf: generic error")       // ERROR(GENERIC)
	ErrDstSizeTooSmall   = errors.New("huf: dst size too small")  // ERROR(dstSize_tooSmall)
	ErrWorkspaceTooSmall = errors.New("huf: workspace too small") // ERROR(workSpace_tooSmall)
	ErrMaxSymbolTooLarge = errors.New("huf: maxSymbolValue too large")
	ErrMaxSymbolTooSmall = errors.New("huf: maxSymbolValue too small")
	ErrSrcSizeWrong      = errors.New("huf: srcSize wrong")
	ErrTableLogTooLarge  = errors.New("huf: tableLog too large")
)

// CElt is C's HUF_CElt (size_t). Elements 1.. hold, per symbol, nbBits in the
// low 8 bits and the code value in the top nbBits bits; element 0 holds the
// CTableHeader. C: huf.h:HUF_CElt
type CElt uint64

// CTable is a Huffman compression table: element 0 is the header, elements
// 1..256 are the symbols. It must have CTableSize(255) elements.
type CTable []CElt

// CTableSize is C's HUF_CTABLE_SIZE_ST(maxSymbolValue).
func CTableSize(maxSymbolValue int) int { return maxSymbolValue + 2 }

// CTableHeader is element 0 of a CTable. C: huf.h:HUF_CTableHeader
type CTableHeader struct {
	TableLog       uint8
	MaxSymbolValue uint8
}

// ReadCTableHeader. C: huf_compress.c:HUF_readCTableHeader
func ReadCTableHeader(ct CTable) CTableHeader {
	return CTableHeader{TableLog: uint8(ct[0]), MaxSymbolValue: uint8(ct[0] >> 8)}
}

// C: huf_compress.c:HUF_writeCTableHeader (the 6 unused bytes are zero)
func writeCTableHeader(ct CTable, tableLog, maxSymbolValue uint32) {
	ct[0] = CElt(uint8(tableLog)) | CElt(uint8(maxSymbolValue))<<8
}

func getNbBits(e CElt) uint { return uint(e & 0xFF) } // C: HUF_getNbBits

// C: HUF_setNbBits
func setNbBits(e *CElt, nbBits uint) { *e = CElt(nbBits) }

// C: HUF_setValue
func setValue(e *CElt, value uint) {
	// C skips nbBits 0; in Go a shift by 64 gives 0, so that case needs no branch.
	*e |= CElt(uint64(value) << (64 - getNbBits(*e)))
}

func highbit32(v uint32) uint32 { return uint32(bits.Len32(v)) - 1 } // C: bits.h:ZSTD_highbit32 (v != 0)

// ---------------------------------------------------------------------------
// Table description (weights) compression
// ---------------------------------------------------------------------------

const maxFSETableLogForHuffHeader = 6 // C: MAX_FSE_TABLELOG_FOR_HUFF_HEADER

// compressWeights. C: huf_compress.c:HUF_compressWeights. It returns 0 for
// "not compressible", 1 for RLE (single symbol), else the compressed size.
func compressWeights(dst []byte, weightTable []byte) (int, error) {
	var (
		count   [256]uint32 // C: wksp->count (HUF_TABLELOG_MAX+1 used)
		norm    [TableLogMax + 1]int16
		ctBuf   [1 + 1<<(maxFSETableLogForHuffHeader-1) + (TableLogMax+1)*2]uint32 // FSE_CTABLE_SIZE_U32(6, 12)
		scratch [4 * ((TableLogMax+2+1<<maxFSETableLogForHuffHeader)/2 + 2)]byte   // FSE_BUILD_CTABLE_WORKSPACE_SIZE(12, 6)
	)
	maxSymbolValue := uint32(TableLogMax)
	wtSize := len(weightTable)

	if wtSize <= 1 {
		return 0, nil // not compressible
	}

	maxCount := hist.CountSimple(&count, &maxSymbolValue, weightTable) // never fails
	if int(maxCount) == wtSize {
		return 1, nil // only a single symbol in src : rle
	}
	if maxCount == 1 {
		return 0, nil // each symbol present maximum once => not compressible
	}

	tableLog := fse.OptimalTableLog(maxFSETableLogForHuffHeader, wtSize, uint(maxSymbolValue))
	if _, err := fse.NormalizeCount(norm[:], tableLog, count[:], wtSize, uint(maxSymbolValue), false); err != nil {
		return 0, err
	}

	op := 0
	hSize, err := fse.WriteNCount(dst, norm[:], uint(maxSymbolValue), tableLog)
	if err != nil {
		return 0, err
	}
	op += hSize

	ct := fse.CTable(ctBuf[:])
	if err := fse.BuildCTable(ct, norm[:], uint(maxSymbolValue), tableLog, scratch[:]); err != nil {
		return 0, err
	}
	cSize, err := fse.CompressUsingCTable(dst[op:], weightTable, ct)
	if err != nil {
		return 0, err
	}
	if cSize == 0 {
		return 0, nil // not enough space for compressed data
	}
	return op + cSize, nil
}

// WriteCTable writes the table description (header) for ct and returns its
// size. wksp must be at least CTableWorkspaceSize bytes (length-checked only).
// C: huf_compress.c:HUF_writeCTable_wksp
func WriteCTable(dst []byte, ct CTable, maxSymbol, huffLog uint, wksp []byte) (int, error) {
	if len(wksp) < writeCTableWkspSize {
		return 0, ErrGeneric
	}
	return writeCTable(dst, ct, maxSymbol, huffLog)
}

// sizeof(HUF_WriteCTableWksp): HUF_CompressWeightsWksp (236+164+52+26 padded
// to 480) + bitsToWeight[13] + huffWeight[255], padded to 4.
const writeCTableWkspSize = 748

func writeCTable(dst []byte, ct CTable, maxSymbolValue, huffLog uint) (int, error) {
	var bitsToWeight [TableLogMax + 1]byte
	var huffWeight [SymbolValueMax + 1]byte // C: [255]; one extra slot for the msan-safe zero below

	if maxSymbolValue > SymbolValueMax {
		return 0, ErrMaxSymbolTooLarge
	}

	// convert to weight
	bitsToWeight[0] = 0
	for n := uint(1); n < huffLog+1; n++ {
		bitsToWeight[n] = byte(huffLog + 1 - n)
	}
	for n := uint(0); n < maxSymbolValue; n++ {
		huffWeight[n] = bitsToWeight[getNbBits(ct[1+n])]
	}

	// attempt weights compression by FSE
	if len(dst) < 1 {
		return 0, ErrDstSizeTooSmall
	}
	hSize, err := compressWeights(dst[1:], huffWeight[:maxSymbolValue])
	if err != nil {
		return 0, err
	}
	if hSize > 1 && uint(hSize) < maxSymbolValue/2 { // FSE compressed
		dst[0] = byte(hSize)
		return hSize + 1, nil
	}

	// write raw values as 4-bits (max : 15)
	if maxSymbolValue > 256-128 {
		return 0, ErrGeneric // should not happen : likely means source cannot be compressed
	}
	if (maxSymbolValue+1)/2+1 > uint(len(dst)) {
		return 0, ErrDstSizeTooSmall // not enough space within dst buffer
	}
	dst[0] = byte(128 /*special case*/ + (maxSymbolValue - 1))
	huffWeight[maxSymbolValue] = 0 // to be sure it doesn't cause msan issue in final combination
	for n := uint(0); n < maxSymbolValue; n += 2 {
		dst[n/2+1] = huffWeight[n]<<4 + huffWeight[n+1]
	}
	return int((maxSymbolValue+1)/2 + 1), nil
}

// GetNbBitsFromCTable. C: huf_compress.c:HUF_getNbBitsFromCTable
func GetNbBitsFromCTable(ct CTable, symbolValue uint32) uint32 {
	if symbolValue > uint32(ReadCTableHeader(ct).MaxSymbolValue) {
		return 0
	}
	return uint32(getNbBits(ct[1+symbolValue]))
}

// ---------------------------------------------------------------------------
// Tree construction
// ---------------------------------------------------------------------------

// nodeElt. C: huf_compress.c:nodeElt
type nodeElt struct {
	count  uint32
	parent uint16
	byte   uint8
	nbBits uint8
}

// setMaxHeight enforces targetNbBits on the sorted tree. C:
// huf_compress.c:HUF_setMaxHeight
func setMaxHeight(huffNode []nodeElt, lastNonNull, targetNbBits uint32) uint32 {
	largestBits := uint32(huffNode[lastNonNull].nbBits)
	// early exit : no elt > targetNbBits, so the tree is already valid.
	if largestBits <= targetNbBits {
		return largestBits
	}

	// there are several too large elements (at least >= 2)
	totalCost := int32(0)
	baseCost := uint32(1) << (largestBits - targetNbBits)
	n := int(lastNonNull)

	// Adjust any ranks > targetNbBits to targetNbBits.
	for uint32(huffNode[n].nbBits) > targetNbBits {
		totalCost += int32(baseCost) - int32(1)<<(largestBits-uint32(huffNode[n].nbBits))
		huffNode[n].nbBits = uint8(targetNbBits)
		n--
	}
	// n end at index of smallest symbol using < targetNbBits
	for uint32(huffNode[n].nbBits) == targetNbBits {
		n--
	}

	// renorm totalCost from 2^largestBits to 2^targetNbBits
	totalCost >>= largestBits - targetNbBits

	// repay normalized cost
	const noSymbol = 0xF0F0F0F0
	var rankLast [TableLogMax + 2]uint32

	// Get pos of last (smallest = lowest cum. count) symbol per rank
	for i := range rankLast {
		rankLast[i] = noSymbol
	}
	{
		currentNbBits := targetNbBits
		for pos := n; pos >= 0; pos-- {
			if uint32(huffNode[pos].nbBits) >= currentNbBits {
				continue
			}
			currentNbBits = uint32(huffNode[pos].nbBits) // < targetNbBits
			rankLast[targetNbBits-currentNbBits] = uint32(pos)
		}
	}

	for totalCost > 0 {
		// Try to reduce the next power of 2 above totalCost because we
		// gain back half the rank.
		nBitsToDecrease := highbit32(uint32(totalCost)) + 1
		for ; nBitsToDecrease > 1; nBitsToDecrease-- {
			highPos := rankLast[nBitsToDecrease]
			lowPos := rankLast[nBitsToDecrease-1]
			if highPos == noSymbol {
				continue
			}
			// Decrease highPos if no symbols of lowPos or if it is
			// not cheaper to remove 2 lowPos than highPos.
			if lowPos == noSymbol {
				break
			}
			highTotal := huffNode[highPos].count
			lowTotal := 2 * huffNode[lowPos].count
			if highTotal <= lowTotal {
				break
			}
		}
		// only triggered when no more rank 1 symbol left => find closest one
		for nBitsToDecrease <= TableLogMax && rankLast[nBitsToDecrease] == noSymbol {
			nBitsToDecrease++
		}
		// Increase the number of bits to gain back half the rank cost.
		totalCost -= 1 << (nBitsToDecrease - 1)
		huffNode[rankLast[nBitsToDecrease]].nbBits++

		// Fix up the new rank.
		if rankLast[nBitsToDecrease-1] == noSymbol {
			rankLast[nBitsToDecrease-1] = rankLast[nBitsToDecrease]
		}
		// Fix up the old rank.
		if rankLast[nBitsToDecrease] == 0 { // special case, reached largest symbol
			rankLast[nBitsToDecrease] = noSymbol
		} else {
			rankLast[nBitsToDecrease]--
			if uint32(huffNode[rankLast[nBitsToDecrease]].nbBits) != targetNbBits-nBitsToDecrease {
				rankLast[nBitsToDecrease] = noSymbol // this rank is now empty
			}
		}
	} // while (totalCost > 0)

	// If we've removed too much weight, then we have to add it back.
	for totalCost < 0 { // Sometimes, cost correction overshoot
		// special case : no rank 1 symbol (using targetNbBits-1);
		// let's create one from largest rank 0 (using targetNbBits).
		if rankLast[1] == noSymbol {
			for uint32(huffNode[n].nbBits) == targetNbBits {
				n--
			}
			huffNode[n+1].nbBits--
			rankLast[1] = uint32(n + 1)
			totalCost++
			continue
		}
		huffNode[rankLast[1]+1].nbBits--
		rankLast[1]++
		totalCost++
	}
	return targetNbBits
}

// C: huf_compress.c:rankPos
type rankPos struct {
	base uint16
	curr uint16
}

const (
	rankPositionTableSize           = 192 // C: RANK_POSITION_TABLE_SIZE
	rankPositionMaxCountLog         = 32  // C: RANK_POSITION_MAX_COUNT_LOG
	rankPositionLogBucketsBegin     = (rankPositionTableSize - 1) - rankPositionMaxCountLog - 1
	rankPositionDistinctCountCutoff = rankPositionLogBucketsBegin + 7 // + ZSTD_highbit32(158) == 7
)

// getIndex. C: huf_compress.c:HUF_getIndex
func getIndex(count uint32) uint32 {
	if count < rankPositionDistinctCountCutoff {
		return count
	}
	return highbit32(count) + rankPositionLogBucketsBegin
}

// insertionSort sorts by descending count. C: HUF_insertionSort
func insertionSort(huffNode []nodeElt, low, high int) {
	size := high - low + 1
	huffNode = huffNode[low:]
	for i := 1; i < size; i++ {
		key := huffNode[i]
		j := i - 1
		for j >= 0 && huffNode[j].count < key.count {
			huffNode[j+1] = huffNode[j]
			j--
		}
		huffNode[j+1] = key
	}
}

// quickSortPartition. C: HUF_quickSortPartition (pivot = rightmost element)
func quickSortPartition(arr []nodeElt, low, high int) int {
	pivot := arr[high].count
	i := low - 1
	for j := low; j < high; j++ {
		if arr[j].count > pivot {
			i++
			arr[i], arr[j] = arr[j], arr[i]
		}
	}
	arr[i+1], arr[high] = arr[high], arr[i+1]
	return i + 1
}

// simpleQuickSort. C: HUF_simpleQuickSort
func simpleQuickSort(arr []nodeElt, low, high int) {
	const kInsertionSortThreshold = 8
	if high-low < kInsertionSortThreshold {
		insertionSort(arr, low, high)
		return
	}
	for low < high {
		idx := quickSortPartition(arr, low, high)
		if idx-low < high-idx {
			simpleQuickSort(arr, low, idx-1)
			low = idx + 1
		} else {
			simpleQuickSort(arr, idx+1, high)
			high = idx - 1
		}
	}
}

// sortSymbols sorts the symbols [0, maxSymbolValue] by decreasing count.
// C: huf_compress.c:HUF_sort
func sortSymbols(huffNode []nodeElt, count *[256]uint32, maxSymbolValue uint32, rankPosition *[rankPositionTableSize]rankPos) {
	maxSymbolValue1 := maxSymbolValue + 1

	*rankPosition = [rankPositionTableSize]rankPos{}
	for n := uint32(0); n < maxSymbolValue1; n++ {
		lowerRank := getIndex(count[n])
		rankPosition[lowerRank].base++
	}

	// Set up the rankPosition table (the running sum stays in a register)
	sum := rankPosition[rankPositionTableSize-1].base
	for n := rankPositionTableSize - 1; n > 0; n-- {
		sum += rankPosition[n-1].base
		rankPosition[n-1].base = sum
		rankPosition[n-1].curr = sum
	}

	// Insert each symbol into their appropriate bucket
	for n := uint32(0); n < maxSymbolValue1; n++ {
		c := count[n]
		r := getIndex(c) + 1
		pos := rankPosition[r].curr
		rankPosition[r].curr++
		huffNode[pos].count = c
		huffNode[pos].byte = uint8(n)
	}

	// Sort each bucket.
	for n := rankPositionDistinctCountCutoff; n < rankPositionTableSize-1; n++ {
		bucketSize := int(rankPosition[n].curr) - int(rankPosition[n].base)
		bucketStartIdx := int(rankPosition[n].base)
		if bucketSize > 1 {
			simpleQuickSort(huffNode[bucketStartIdx:], 0, bucketSize-1)
		}
	}
}

const startNode = SymbolValueMax + 1 // C: STARTNODE

// buildTree builds an unlimited-depth Huffman tree in the sorted array and
// returns the smallest node. tbl is C's huffNode0; C's huffNode is tbl[1:] and
// index -1 (the barrier) is tbl[0]. The t-prefixed indices below are tbl
// indices, C's index plus 1, and parent holds a tbl index too (only this
// function reads it). Every index is below len(tbl), a power of 2, so the
// masks change no index and drop the bounds checks.
// C: huf_compress.c:HUF_buildTree
func buildTree(tbl *[2 * (SymbolValueMax + 1)]nodeElt, maxSymbolValue uint32) int {
	const mask = len(tbl) - 1
	tNonNull := int(maxSymbolValue) + 1
	for tbl[tNonNull&mask].count == 0 {
		tNonNull--
	}
	tNodeNb := startNode + 1
	tLowS := tNonNull
	tNodeRoot := tNodeNb + tLowS - 2 // C: nodeRoot = nodeNb + lowS - 1
	tLowN := tNodeNb
	tbl[tNodeNb&mask].count = tbl[tLowS&mask].count + tbl[(tLowS-1)&mask].count
	tbl[tLowS&mask].parent = uint16(tNodeNb)
	tbl[(tLowS-1)&mask].parent = uint16(tNodeNb)
	tNodeNb++
	tLowS -= 2
	for t := tNodeNb; t <= tNodeRoot; t++ {
		tbl[t&mask].count = 1 << 30
	}
	tbl[0].count = 1 << 31 // fake entry, strong barrier

	// create parents (the counts already loaded are reused, as gcc does)
	for tNodeNb <= tNodeRoot {
		var t1, t2 int
		var c1, c2 uint32
		cS, cN := tbl[tLowS&mask].count, tbl[tLowN&mask].count
		if cS < cN {
			t1, c1 = tLowS, cS
			tLowS--
			cS = tbl[tLowS&mask].count
		} else {
			t1, c1 = tLowN, cN
			tLowN++
			cN = tbl[tLowN&mask].count
		}
		if cS < cN {
			t2, c2 = tLowS, cS
			tLowS--
		} else {
			t2, c2 = tLowN, cN
			tLowN++
		}
		tbl[tNodeNb&mask].count = c1 + c2
		tbl[t1&mask].parent = uint16(tNodeNb)
		tbl[t2&mask].parent = uint16(tNodeNb)
		tNodeNb++
	}

	// distribute weights (unlimited tree height)
	tbl[tNodeRoot&mask].nbBits = 0
	for t := tNodeRoot - 1; t >= startNode+1; t-- {
		tbl[t&mask].nbBits = tbl[int(tbl[t&mask].parent)&mask].nbBits + 1
	}
	for t := 1; t <= tNonNull; t++ {
		tbl[t&mask].nbBits = tbl[int(tbl[t&mask].parent)&mask].nbBits + 1
	}
	return tNonNull - 1
}

// buildCTableFromTree. C: huf_compress.c:HUF_buildCTableFromTree
func buildCTableFromTree(ct CTable, huffNode []nodeElt, nonNullRank int, maxSymbolValue, maxNbBits uint32) {
	// Every nbBits here is at most maxNbBits <= TableLogMax < 16, so the &15
	// masks change no index and drop the bounds checks.
	var nbPerRank, valPerRank [16]uint16
	alphabetSize := int(maxSymbolValue + 1)
	for _, e := range huffNode[:nonNullRank+1] {
		nbPerRank[e.nbBits&15]++
	}
	// determine starting value per rank
	var min uint16
	for n := int(maxNbBits); n > 0; n-- {
		valPerRank[n&15] = min // get starting value within each rank
		min += nbPerRank[n&15]
		min >>= 1
	}
	elts := ct[1 : alphabetSize+1]
	for _, e := range huffNode[:alphabetSize] {
		setNbBits(&elts[e.byte], uint(e.nbBits)) // push nbBits per symbol, symbol order
	}
	for n := range elts { // assign value within rank, symbol order
		nb := getNbBits(elts[n]) & 15
		setValue(&elts[n], uint(valPerRank[nb]))
		valPerRank[nb]++
	}
	writeCTableHeader(ct, maxNbBits, maxSymbolValue)
}

// BuildCTable builds the Huffman table from a histogram and returns the
// maximum number of bits actually used (<= maxNbBits). maxNbBits 0 means
// TableLogDefault. wksp must be at least CTableWorkspaceSize bytes
// (length-checked only). C: huf_compress.c:HUF_buildCTable_wksp
func BuildCTable(ct CTable, count *[256]uint32, maxSymbol, maxNbBits uint, wksp []byte) (uint, error) {
	if len(wksp) < CTableWorkspaceSize {
		return 0, ErrWorkspaceTooSmall
	}
	n, err := buildCTable(ct, count, uint32(maxSymbol), uint32(maxNbBits))
	return uint(n), err
}

func buildCTable(ct CTable, count *[256]uint32, maxSymbolValue, maxNbBits uint32) (uint32, error) {
	var (
		huffNode0    [2 * (SymbolValueMax + 1)]nodeElt
		rankPosition [rankPositionTableSize]rankPos
	)
	if maxNbBits == 0 {
		maxNbBits = TableLogDefault
	}
	if maxSymbolValue > SymbolValueMax {
		return 0, ErrMaxSymbolTooLarge
	}

	// sort, decreasing order
	sortSymbols(huffNode0[1:], count, maxSymbolValue, &rankPosition)

	// build tree
	nonNullRank := buildTree(&huffNode0, maxSymbolValue)

	// determine and enforce maxTableLog
	maxNbBits = setMaxHeight(huffNode0[1:], uint32(nonNullRank), maxNbBits)
	if maxNbBits > TableLogMax {
		return 0, ErrGeneric // check fit into table
	}

	buildCTableFromTree(ct, huffNode0[1:], nonNullRank, maxSymbolValue, maxNbBits)
	return maxNbBits, nil
}

// EstimateCompressedSize. C: huf_compress.c:HUF_estimateCompressedSize
func EstimateCompressedSize(ct CTable, count *[256]uint32, maxSymbolValue uint) uint {
	var nbBits uint
	for s := uint(0); s <= maxSymbolValue; s++ {
		nbBits += getNbBits(ct[1+s]) * uint(count[s])
	}
	return nbBits >> 3
}

// ValidateCTable reports whether ct can encode every symbol present in count.
// C: huf_compress.c:HUF_validateCTable
func ValidateCTable(ct CTable, count *[256]uint32, maxSymbolValue uint) bool {
	if uint(ReadCTableHeader(ct).MaxSymbolValue) < maxSymbolValue {
		return false
	}
	bad := false
	for s := uint(0); s <= maxSymbolValue; s++ {
		bad = bad || (count[s] != 0 && getNbBits(ct[1+s]) == 0)
	}
	return !bad
}

// CompressBound. C: huf.h:HUF_COMPRESSBOUND
func CompressBound(size int) int { return 129 + size + size>>8 + 8 }

// Compress1XUsingCTable encodes src with ct. It returns 0 when the output did
// not fit in dst. flags is accepted for C parity; only the bits that change
// output matter, and none do here. C: huf_compress.c:HUF_compress1X_usingCTable
func Compress1XUsingCTable(dst, src []byte, ct CTable, flags int) int {
	_ = flags // C: HUF_flags_bmi2 selects a compiler target, not a different output
	return compress1XBody(dst, src, ct)
}

// Compress4XUsingCTable encodes src as four streams behind a 6-byte jump
// table. It returns 0 when src is too small or the output did not fit.
// C: huf_compress.c:HUF_compress4X_usingCTable_internal
func Compress4XUsingCTable(dst, src []byte, ct CTable, flags int) int {
	segmentSize := (len(src) + 3) / 4 // first 3 segments
	op := 0

	if len(dst) < 6+1+1+1+8 {
		return 0 // minimum space to compress successfully
	}
	if len(src) < 12 {
		return 0 // no saving possible : too small input
	}
	op += 6 // jumpTable

	ip := 0
	for i := 0; i < 4; i++ {
		seg := src[ip:min(ip+segmentSize, len(src))]
		cSize := Compress1XUsingCTable(dst[op:], seg, ct, flags)
		if cSize == 0 || cSize > 65535 {
			return 0
		}
		if i < 3 {
			binary.LittleEndian.PutUint16(dst[2*i:], uint16(cSize))
		}
		op += cSize
		ip += segmentSize
	}
	return op
}

// ---------------------------------------------------------------------------
// Block compression with table reuse
// ---------------------------------------------------------------------------

// Repeat tells whether the previous table may be reused.
// C: huf.h:HUF_repeat
type Repeat int

const (
	RepeatNone  Repeat = iota // Cannot use the previous table
	RepeatCheck               // Can use the previous table but it must be checked
	RepeatValid               // Can use the previous table and it is assumed valid
)

// compressCTable encodes src with ct after an already written header of hdr
// bytes. C: huf_compress.c:HUF_compressCTable_internal (ostart = dst[0], op =
// dst[hdr:]). It returns the total size, or 0 if uncompressible.
func compressCTable(dst []byte, hdr int, src []byte, fourStreams bool, ct CTable, flags int) int {
	var cSize int
	if fourStreams {
		cSize = Compress4XUsingCTable(dst[hdr:], src, ct, flags)
	} else {
		cSize = Compress1XUsingCTable(dst[hdr:], src, ct, flags)
	}
	if cSize == 0 {
		return 0 // uncompressible
	}
	// check compressibility
	if hdr+cSize >= len(src)-1 {
		return 0
	}
	return hdr + cSize
}

// Cardinality counts the symbols with a nonzero count. C: HUF_cardinality
func Cardinality(count *[256]uint32, maxSymbolValue uint) uint {
	var c uint
	for i := uint(0); i < maxSymbolValue+1; i++ {
		if count[i] != 0 {
			c++
		}
	}
	return c
}

// MinTableLog. C: HUF_minTableLog
func MinTableLog(symbolCardinality uint) uint {
	return uint(highbit32(uint32(symbolCardinality))) + 1
}

// OptimalTableLog picks the table depth: a cheap FSE-based estimate, or with
// FlagOptimalDepth a search for the depth minimizing header plus encoded size.
// table is scratch (it is overwritten); wksp is length-checked only.
// C: huf_compress.c:HUF_optimalTableLog
func OptimalTableLog(maxTableLog uint, srcSize int, maxSymbol uint, wksp []byte, table CTable, count *[256]uint32, flags int) uint {
	_ = wksp
	if flags&FlagOptimalDepth == 0 {
		// cheap evaluation, based on FSE
		return fse.OptimalTableLogInternal(maxTableLog, srcSize, maxSymbol, 1)
	}

	// C writes the header to workSpace + sizeof(HUF_WriteCTableWksp) with the
	// rest of the workspace as capacity.
	var dst [CTableWorkspaceSize - writeCTableWkspSize]byte
	symbolCardinality := Cardinality(count, maxSymbol)
	minTableLog := MinTableLog(symbolCardinality)
	optSize := ^uint(0) - 1
	optLog := maxTableLog

	// Search until size increases
	for optLogGuess := minTableLog; optLogGuess <= maxTableLog; optLogGuess++ {
		maxBits, err := buildCTable(table, count, uint32(maxSymbol), uint32(optLogGuess))
		if err != nil {
			continue
		}
		if uint(maxBits) < optLogGuess && optLogGuess > minTableLog {
			break
		}
		hSize, err := writeCTable(dst[:], table, maxSymbol, uint(maxBits))
		if err != nil {
			continue
		}
		newSize := EstimateCompressedSize(table, count, maxSymbol) + uint(hSize)
		if newSize > optSize+1 {
			break
		}
		if newSize < optSize {
			optSize = newSize
			optLog = optLogGuess
		}
	}
	return optLog
}

// compressInternal. C: huf_compress.c:HUF_compress_internal. It returns 0 for
// "not compressible", 1 for RLE (dst[0] = the symbol), else the size.
func compressInternal(dst, src []byte, maxSymbolValue, huffLog uint, fourStreams bool,
	wksp []byte, oldHufTable CTable, repeat *Repeat, flags int) (int, error) {
	var count [256]uint32
	var ctable [SymbolValueMax + 2]CElt
	table := CTable(ctable[:])

	// checks & inits
	if len(wksp) < compressTablesSize {
		return 0, ErrWorkspaceTooSmall
	}
	if len(src) == 0 {
		return 0, nil // Uncompressed
	}
	if len(dst) == 0 {
		return 0, nil // cannot fit anything within dst budget
	}
	if len(src) > BlockSizeMax {
		return 0, ErrSrcSizeWrong // current block size limit
	}
	if huffLog > TableLogMax {
		return 0, ErrTableLogTooLarge
	}
	if maxSymbolValue > SymbolValueMax {
		return 0, ErrMaxSymbolTooLarge
	}
	if maxSymbolValue == 0 {
		maxSymbolValue = SymbolValueMax
	}
	if huffLog == 0 {
		huffLog = TableLogDefault
	}

	// Heuristic : If old table is valid, use it for small inputs
	if flags&FlagPreferRepeat != 0 && repeat != nil && *repeat == RepeatValid {
		return compressCTable(dst, 0, src, fourStreams, oldHufTable, flags), nil
	}

	// If uncompressible data is suspected, do a smaller sampling first
	if flags&FlagSuspectUncompressible != 0 && len(src) >= suspectIncompressibleSampleSize*suspectIncompressibleSampleRatio {
		var largestTotal uint32
		ms := uint32(maxSymbolValue)
		largestTotal += hist.CountSimple(&count, &ms, src[:suspectIncompressibleSampleSize])
		ms = uint32(maxSymbolValue)
		largestTotal += hist.CountSimple(&count, &ms, src[len(src)-suspectIncompressibleSampleSize:])
		if largestTotal <= (2*suspectIncompressibleSampleSize)>>7+4 {
			return 0, nil // heuristic : probably not compressible enough
		}
	}

	// Scan input and build symbol stats
	{
		ms := uint32(maxSymbolValue)
		largest := hist.Count(&count, &ms, src)
		if largest == hist.MaxSymbolTooSmall {
			return 0, ErrMaxSymbolTooSmall
		}
		maxSymbolValue = uint(ms)
		if int(largest) == len(src) { // single symbol, rle
			dst[0] = src[0]
			return 1, nil
		}
		if int(largest) <= len(src)>>7+4 {
			return 0, nil // heuristic : probably not compressible enough
		}
	}

	// Check validity of previous table
	if repeat != nil && *repeat == RepeatCheck && !ValidateCTable(oldHufTable, &count, maxSymbolValue) {
		*repeat = RepeatNone
	}
	// Heuristic : use existing table for small inputs
	if flags&FlagPreferRepeat != 0 && repeat != nil && *repeat != RepeatNone {
		return compressCTable(dst, 0, src, fourStreams, oldHufTable, flags), nil
	}

	// Build Huffman Tree
	huffLog = OptimalTableLog(huffLog, len(src), maxSymbolValue, wksp, table, &count, flags)
	maxBits, err := buildCTable(table, &count, uint32(maxSymbolValue), uint32(huffLog))
	if err != nil {
		return 0, err
	}
	huffLog = uint(maxBits)

	// Write table description header
	hSize, err := writeCTable(dst, table, maxSymbolValue, huffLog)
	if err != nil {
		return 0, err
	}
	// Check if using previous huffman table is beneficial
	if repeat != nil && *repeat != RepeatNone {
		oldSize := EstimateCompressedSize(oldHufTable, &count, maxSymbolValue)
		newSize := EstimateCompressedSize(table, &count, maxSymbolValue)
		if oldSize <= uint(hSize)+newSize || hSize+12 >= len(src) {
			return compressCTable(dst, 0, src, fourStreams, oldHufTable, flags), nil
		}
	}

	// Use the new huffman table
	if hSize+12 >= len(src) {
		return 0, nil
	}
	if repeat != nil {
		*repeat = RepeatNone
	}
	if oldHufTable != nil {
		copy(oldHufTable, table) // Save new table
	}
	return compressCTable(dst, hSize, src, fourStreams, table, flags), nil
}

// Compress1XRepeat compresses src as one stream, considering oldHufTable when
// *repeat != RepeatNone. If it reuses the table it modifies neither the table
// nor *repeat; otherwise it sets *repeat = RepeatNone and saves the table used
// in hufTable. It returns 0 if src is not compressible, 1 if it is a single
// repeated symbol (dst[0]), else the compressed size. maxSymbol 0 means 255,
// huffLog 0 means TableLogDefault. wksp must be at least WorkspaceSize bytes.
// hufTable, if not nil, must have CTableSize(255) elements.
// C: huf_compress.c:HUF_compress1X_repeat
func Compress1XRepeat(dst, src []byte, maxSymbol, huffLog uint, wksp []byte, hufTable CTable, repeat *Repeat, flags int) (int, error) {
	return compressInternal(dst, src, maxSymbol, huffLog, false, wksp, hufTable, repeat, flags)
}

// Compress4XRepeat is Compress1XRepeat with four streams.
// C: huf_compress.c:HUF_compress4X_repeat
func Compress4XRepeat(dst, src []byte, maxSymbol, huffLog uint, wksp []byte, hufTable CTable, repeat *Repeat, flags int) (int, error) {
	return compressInternal(dst, src, maxSymbol, huffLog, true, wksp, hufTable, repeat, flags)
}

// ---------------------------------------------------------------------------
// Table description reading
// ---------------------------------------------------------------------------

// tableLogAbsoluteMax. C: huf.h:HUF_TABLELOG_ABSOLUTEMAX
const tableLogAbsoluteMax = 12

// ReadCTable rebuilds the compression table described by src (as written
// by WriteCTable) into ct. On entry *maxSymbolValue is the largest symbol
// accepted; on return the table's largest symbol. It returns the number of
// bytes read and whether some symbol up to it has weight 0 (no code).
// Elements of ct beyond the table's largest symbol are left as they were.
// C: huf_compress.c:HUF_readCTable
func ReadCTable(ct CTable, maxSymbolValue *uint, src []byte) (size int, hasZeroWeights bool, err error) {
	var huffWeight [SymbolValueMax + 1]byte
	var rankVal [tableLogAbsoluteMax + 1]uint32 // large enough for values from 0 to 16
	elts := ct[1:]

	// get symbol weights
	nbSymbols, tableLog, readSize, err := ReadStats(huffWeight[:], rankVal[:], src)
	if err != nil {
		return 0, false, err
	}
	hasZeroWeights = rankVal[0] > 0

	// check result
	if tableLog > TableLogMax {
		return 0, false, ErrTableLogTooLarge
	}
	if uint(nbSymbols) > *maxSymbolValue+1 {
		return 0, false, ErrMaxSymbolTooSmall
	}

	*maxSymbolValue = uint(nbSymbols) - 1

	writeCTableHeader(ct, tableLog, uint32(*maxSymbolValue))

	// Prepare base value per rank
	var nextRankStart uint32
	for n := uint32(1); n <= tableLog; n++ {
		curr := nextRankStart
		nextRankStart += rankVal[n] << (n - 1)
		rankVal[n] = curr
	}

	// fill nbBits
	for n := uint32(0); n < nbSymbols; n++ {
		w := uint32(huffWeight[n])
		var nb uint32
		if w != 0 {
			nb = tableLog + 1 - w
		}
		setNbBits(&elts[n], uint(byte(nb)))
	}

	// fill val
	var nbPerRank, valPerRank [TableLogMax + 2]uint16 // support w=0=>n=tableLog+1
	for n := uint32(0); n < nbSymbols; n++ {
		nbPerRank[getNbBits(elts[n])]++
	}
	// determine stating value per rank
	valPerRank[tableLog+1] = 0 // for w==0
	var minV uint16
	for n := tableLog; n > 0; n-- { // start at n=tablelog <-> w=1
		valPerRank[n] = minV // get starting value within each rank
		minV += nbPerRank[n]
		minV >>= 1
	}
	// assign value within rank, symbol order
	for n := uint32(0); n < nbSymbols; n++ {
		nb := getNbBits(elts[n])
		setValue(&elts[n], uint(valPerRank[nb]))
		valPerRank[nb]++
	}
	return readSize, hasZeroWeights, nil
}
