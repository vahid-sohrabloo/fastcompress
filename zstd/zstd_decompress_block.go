package zstd

import (
	"encoding/binary"
	"sync/atomic"

	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
	"github.com/vahid-sohrabloo/fastzstd/internal/huf"
)

// Compressed block decoding: literals, sequence tables and sequences.
// C: lib/decompress/zstd_decompress_block.c (v1.5.7)

// minSequencesSize. C: zstd_internal.h:MIN_SEQUENCES_SIZE
const minSequencesSize = 1

/*-*************************************************************
 *   Block decoding
 ***************************************************************/

// blockSizeMax. C: zstd_decompress_block.c:ZSTD_blockSizeMax
func (d *dctx) blockSizeMax() int {
	if d.isFrameDecompression {
		return int(d.fParams.blockSizeMax)
	}
	return blockSizeMax
}

// allocateLiteralsBuffer places the literals buffer: overlapping the
// current dst, split between dst and litExtraBuffer, or entirely within
// litExtraBuffer. C: zstd_decompress_block.c:ZSTD_allocateLiteralsBuffer
func (d *dctx) allocateLiteralsBuffer(dst []byte, dstCapacity, litSize int,
	streaming streamingOperation, expectedWriteSize int, splitImmediately bool) {
	blockSizeMax := d.blockSizeMax()
	if streaming == notStreaming && dstCapacity > blockSizeMax+wildcopyOverlength+litSize+wildcopyOverlength {
		// If we aren't streaming, we can just put the literals after the
		// output of the current block. We don't need to worry about
		// overwriting the extDict of our window, because it doesn't exist.
		// So if we have space after the end of the block, just put it there.
		d.litBuf = dst
		d.litBuffer = blockSizeMax + wildcopyOverlength
		d.litBufferEnd = d.litBuffer + litSize
		d.litBufferLocation = inDst
	} else if litSize <= litBufferExtraSize {
		// Literals fit entirely within the extra buffer, put them there to
		// avoid having to split the literals.
		d.litBuf = d.litExtraBuffer[:]
		d.litBuffer = 0
		d.litBufferEnd = d.litBuffer + litSize
		d.litBufferLocation = notInDst
	} else {
		// Literals must be split between the output block and the extra lit
		// buffer. We fill the extra lit buffer with the tail of the
		// literals, and put the rest of the literals at the end of the
		// block, with WILDCOPY_OVERLENGTH of buffer room to allow for
		// overreads. This MUST not write more than our maxBlockSize beyond
		// dst, because in streaming mode, that could overwrite part of our
		// extDict window.
		d.litBuf = dst
		if splitImmediately {
			// won't fit in litExtraBuffer, so it will be split between end of
			// dst and extra buffer
			d.litBuffer = expectedWriteSize - litSize + litBufferExtraSize - wildcopyOverlength
			d.litBufferEnd = d.litBuffer + litSize - litBufferExtraSize
		} else {
			// initially this will be stored entirely in dst during huffman
			// decoding, it will partially be shifted to litExtraBuffer after
			d.litBuffer = expectedWriteSize - litSize
			d.litBufferEnd = expectedWriteSize
		}
		d.litBufferLocation = split
	}
}

// decodeLiteralsBlock decodes the literals section at the start of src (a
// compressed block's content) and returns the number of bytes it read.
// dst[:dstCapacity] is the output space of the block; dst nil stands for
// C's NULL. Precondition (C's contract; a caller bug otherwise, which
// panics): cap(dst) >= dstCapacity, and dst == nil implies
// dstCapacity == 0. Where it is possible to do so without being stomped by
// the output during decompression, the literals are stored in dst: in full
// after the block's output when there is room, otherwise at the end of the
// block's output space with a part in litExtraBuffer to keep them "ahead"
// of the output. C: zstd_decompress_block.c:ZSTD_decodeLiteralsBlock
func (d *dctx) decodeLiteralsBlock(src []byte, dst []byte, dstCapacity int, streaming streamingOperation) (int, dErr) {
	srcSize := len(src)
	if srcSize < minCBlockSize {
		return 0, dErrBlockTooSmall
	}
	if dst != nil {
		dst = dst[:dstCapacity]
	}

	istart := src
	litEncType := symbolEncodingType(istart[0] & 3)
	blockSizeMax := d.blockSizeMax()

	switch litEncType {
	case setRepeat, setCompressed:
		if litEncType == setRepeat && !d.litEntropy {
			return 0, dErrDictionaryCorrupted
		}
		if srcSize < 5 {
			return 0, dErrBlockTooSmall // srcSize >= MIN_CBLOCK_SIZE == 2; here we need up to 5 for case 3
		}
		var lhSize, litSize, litCSize int
		singleStream := false
		lhlCode := (istart[0] >> 2) & 3
		lhc := binary.LittleEndian.Uint32(istart)
		expectedWriteSize := min(blockSizeMax, dstCapacity)
		const flags = 0 // C: HUF_flags_bmi2 / HUF_flags_disableAsm only pick code paths
		switch lhlCode {
		case 0, 1:
			// 2 - 2 - 10 - 10
			singleStream = lhlCode == 0
			lhSize = 3
			litSize = int((lhc >> 4) & 0x3FF)
			litCSize = int((lhc >> 14) & 0x3FF)
		case 2:
			// 2 - 2 - 14 - 14
			lhSize = 4
			litSize = int((lhc >> 4) & 0x3FFF)
			litCSize = int(lhc >> 18)
		default: // 3
			// 2 - 2 - 18 - 18
			lhSize = 5
			litSize = int((lhc >> 4) & 0x3FFFF)
			litCSize = int(lhc>>22) + int(istart[4])<<10
		}
		if litSize > 0 && dst == nil {
			return 0, dErrLitDstSizeTooSmall // NULL not handled
		}
		if litSize > blockSizeMax {
			return 0, dErrLitSizeTooLarge
		}
		if !singleStream && litSize < minLiteralsFor4Streams {
			return 0, dErrLiteralsHeaderWrong // not enough literals for the 4-streams mode
		}
		if litCSize+lhSize > srcSize {
			return 0, dErrBlockTooSmall
		}
		if expectedWriteSize < litSize {
			return 0, dErrLitDstSizeTooSmall
		}
		d.allocateLiteralsBuffer(dst, dstCapacity, litSize, streaming, expectedWriteSize, false)

		litDst := d.litBuf[d.litBuffer : d.litBuffer+litSize]
		hufSrc := istart[lhSize : lhSize+litCSize]
		var hufErr error
		if litEncType == setRepeat {
			if singleStream {
				hufErr = huf.Decompress1XUsingDTable(litDst, hufSrc, d.hufPtr, flags)
			} else {
				hufErr = huf.Decompress4XUsingDTable(litDst, hufSrc, d.hufPtr, flags)
			}
		} else {
			if singleStream {
				// C: HUF_decompress1X1_DCtx_wksp (HUF_FORCE_DECOMPRESS_X2 unset)
				hufErr = huf.Decompress1X1DCtxWksp(&d.entropy.hufTable, litDst, hufSrc, flags)
			} else {
				hufErr = huf.Decompress4XHufOnlyWksp(&d.entropy.hufTable, litDst, hufSrc, flags)
			}
		}
		if d.litBufferLocation == split {
			copy(d.litExtraBuffer[:litBufferExtraSize], d.litBuf[d.litBufferEnd-litBufferExtraSize:d.litBufferEnd])
			copy(d.litBuf[d.litBuffer+litBufferExtraSize-wildcopyOverlength:], d.litBuf[d.litBuffer:d.litBuffer+litSize-litBufferExtraSize])
			d.litBuffer += litBufferExtraSize - wildcopyOverlength
			d.litBufferEnd -= wildcopyOverlength
		}

		if hufErr != nil {
			return 0, dErrCorruptionDetected
		}

		d.litPtr = d.litBuffer
		d.litSize = litSize
		d.litEntropy = true
		if litEncType == setCompressed {
			d.hufPtr = &d.entropy.hufTable
		}
		return litCSize + lhSize, 0

	case setBasic:
		var litSize, lhSize int
		lhlCode := (istart[0] >> 2) & 3
		expectedWriteSize := min(blockSizeMax, dstCapacity)
		switch lhlCode {
		case 0, 2:
			lhSize = 1
			litSize = int(istart[0] >> 3)
		case 1:
			lhSize = 2
			litSize = int(binary.LittleEndian.Uint16(istart) >> 4)
		default: // 3
			lhSize = 3
			if srcSize < 3 {
				return 0, dErrBlockTooSmall // srcSize >= MIN_CBLOCK_SIZE == 2; here we need lhSize = 3
			}
			litSize = int(readLE24(istart) >> 4)
		}

		if litSize > 0 && dst == nil {
			return 0, dErrLitDstSizeTooSmall // NULL not handled
		}
		if litSize > blockSizeMax {
			return 0, dErrLitSizeTooLarge
		}
		if expectedWriteSize < litSize {
			return 0, dErrLitDstSizeTooSmall
		}
		d.allocateLiteralsBuffer(dst, dstCapacity, litSize, streaming, expectedWriteSize, true)
		if lhSize+litSize+wildcopyOverlength > srcSize { // risk reading beyond src buffer with wildcopy
			if litSize+lhSize > srcSize {
				return 0, dErrBlockTooSmall
			}
			if d.litBufferLocation == split {
				copy(d.litBuf[d.litBuffer:], istart[lhSize:lhSize+litSize-litBufferExtraSize])
				copy(d.litExtraBuffer[:], istart[lhSize+litSize-litBufferExtraSize:lhSize+litSize])
			} else {
				copy(d.litBuf[d.litBuffer:], istart[lhSize:lhSize+litSize])
			}
			d.litPtr = d.litBuffer
			d.litSize = litSize
			return lhSize + litSize, 0
		}
		// direct reference into compressed stream
		d.litBuf = src
		d.litPtr = lhSize
		d.litSize = litSize
		d.litBufferEnd = d.litPtr + litSize
		d.litBufferLocation = notInDst
		return lhSize + litSize, 0

	default: // setRLE
		lhlCode := (istart[0] >> 2) & 3
		var litSize, lhSize int
		expectedWriteSize := min(blockSizeMax, dstCapacity)
		switch lhlCode {
		case 0, 2:
			lhSize = 1
			litSize = int(istart[0] >> 3)
		case 1:
			lhSize = 2
			if srcSize < 3 {
				return 0, dErrBlockTooSmall // srcSize >= MIN_CBLOCK_SIZE == 2; here we need lhSize+1 = 3
			}
			litSize = int(binary.LittleEndian.Uint16(istart) >> 4)
		default: // 3
			lhSize = 3
			if srcSize < 4 {
				return 0, dErrBlockTooSmall // srcSize >= MIN_CBLOCK_SIZE == 2; here we need lhSize+1 = 4
			}
			litSize = int(readLE24(istart) >> 4)
		}
		if litSize > 0 && dst == nil {
			return 0, dErrLitDstSizeTooSmall // NULL not handled
		}
		if litSize > blockSizeMax {
			return 0, dErrLitSizeTooLarge
		}
		if expectedWriteSize < litSize {
			return 0, dErrLitDstSizeTooSmall
		}
		d.allocateLiteralsBuffer(dst, dstCapacity, litSize, streaming, expectedWriteSize, true)
		b := istart[lhSize]
		if d.litBufferLocation == split {
			memset(d.litBuf[d.litBuffer:d.litBuffer+litSize-litBufferExtraSize], b)
			memset(d.litExtraBuffer[:litBufferExtraSize], b)
		} else {
			memset(d.litBuf[d.litBuffer:d.litBuffer+litSize], b)
		}
		d.litPtr = d.litBuffer
		d.litSize = litSize
		return lhSize + 1, 0
	}
}

// readLE24. C: mem.h:MEM_readLE24
func readLE24(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 }

// memset. C: ZSTD_memset
func memset(b []byte, c byte) {
	for i := range b {
		b[i] = c
	}
}

// Default FSE distribution tables.
// These are pre-calculated FSE decoding tables using default distributions
// as defined in specification :
// https://github.com/facebook/zstd/blob/release/doc/zstd_compression_format.md#default-distributions
// They were generated programmatically with following method :
//   - start from default distributions, present in /lib/common/zstd_internal.h
//   - generate tables normally, using ZSTD_buildFSETable()
//   - printout the content of tables
//   - prettify output, report below, test with fuzzer to ensure it's correct
//
// C: zstd_decompress_block.c:LL_defaultDTable, OF_defaultDTable,
// ML_defaultDTable (verbatim).

// Default FSE distribution table for Literal Lengths
var llDefaultDTable = [1<<llDefaultNormLog + 1]seqSymbol{
	{1, 1, 1, llDefaultNormLog}, // header : fastMode, tableLog
	// nextState, nbAddBits, nbBits, baseVal
	{0, 0, 4, 0}, {16, 0, 4, 0},
	{32, 0, 5, 1}, {0, 0, 5, 3},
	{0, 0, 5, 4}, {0, 0, 5, 6},
	{0, 0, 5, 7}, {0, 0, 5, 9},
	{0, 0, 5, 10}, {0, 0, 5, 12},
	{0, 0, 6, 14}, {0, 1, 5, 16},
	{0, 1, 5, 20}, {0, 1, 5, 22},
	{0, 2, 5, 28}, {0, 3, 5, 32},
	{0, 4, 5, 48}, {32, 6, 5, 64},
	{0, 7, 5, 128}, {0, 8, 6, 256},
	{0, 10, 6, 1024}, {0, 12, 6, 4096},
	{32, 0, 4, 0}, {0, 0, 4, 1},
	{0, 0, 5, 2}, {32, 0, 5, 4},
	{0, 0, 5, 5}, {32, 0, 5, 7},
	{0, 0, 5, 8}, {32, 0, 5, 10},
	{0, 0, 5, 11}, {0, 0, 6, 13},
	{32, 1, 5, 16}, {0, 1, 5, 18},
	{32, 1, 5, 22}, {0, 2, 5, 24},
	{32, 3, 5, 32}, {0, 3, 5, 40},
	{0, 6, 4, 64}, {16, 6, 4, 64},
	{32, 7, 5, 128}, {0, 9, 6, 512},
	{0, 11, 6, 2048}, {48, 0, 4, 0},
	{16, 0, 4, 1}, {32, 0, 5, 2},
	{32, 0, 5, 3}, {32, 0, 5, 5},
	{32, 0, 5, 6}, {32, 0, 5, 8},
	{32, 0, 5, 9}, {32, 0, 5, 11},
	{32, 0, 5, 12}, {0, 0, 6, 15},
	{32, 1, 5, 18}, {32, 1, 5, 20},
	{32, 2, 5, 24}, {32, 2, 5, 28},
	{32, 3, 5, 40}, {32, 4, 5, 48},
	{0, 16, 6, 65536}, {0, 15, 6, 32768},
	{0, 14, 6, 16384}, {0, 13, 6, 8192},
}

// Default FSE distribution table for Offset Codes
var ofDefaultDTable = [1<<ofDefaultNormLog + 1]seqSymbol{
	{1, 1, 1, ofDefaultNormLog}, // header : fastMode, tableLog
	// nextState, nbAddBits, nbBits, baseVal
	{0, 0, 5, 0}, {0, 6, 4, 61},
	{0, 9, 5, 509}, {0, 15, 5, 32765},
	{0, 21, 5, 2097149}, {0, 3, 5, 5},
	{0, 7, 4, 125}, {0, 12, 5, 4093},
	{0, 18, 5, 262141}, {0, 23, 5, 8388605},
	{0, 5, 5, 29}, {0, 8, 4, 253},
	{0, 14, 5, 16381}, {0, 20, 5, 1048573},
	{0, 2, 5, 1}, {16, 7, 4, 125},
	{0, 11, 5, 2045}, {0, 17, 5, 131069},
	{0, 22, 5, 4194301}, {0, 4, 5, 13},
	{16, 8, 4, 253}, {0, 13, 5, 8189},
	{0, 19, 5, 524285}, {0, 1, 5, 1},
	{16, 6, 4, 61}, {0, 10, 5, 1021},
	{0, 16, 5, 65533}, {0, 28, 5, 268435453},
	{0, 27, 5, 134217725}, {0, 26, 5, 67108861},
	{0, 25, 5, 33554429}, {0, 24, 5, 16777213},
}

// Default FSE distribution table for Match Lengths
var mlDefaultDTable = [1<<mlDefaultNormLog + 1]seqSymbol{
	{1, 1, 1, mlDefaultNormLog}, // header : fastMode, tableLog
	// nextState, nbAddBits, nbBits, baseVal
	{0, 0, 6, 3}, {0, 0, 4, 4},
	{32, 0, 5, 5}, {0, 0, 5, 6},
	{0, 0, 5, 8}, {0, 0, 5, 9},
	{0, 0, 5, 11}, {0, 0, 6, 13},
	{0, 0, 6, 16}, {0, 0, 6, 19},
	{0, 0, 6, 22}, {0, 0, 6, 25},
	{0, 0, 6, 28}, {0, 0, 6, 31},
	{0, 0, 6, 34}, {0, 1, 6, 37},
	{0, 1, 6, 41}, {0, 2, 6, 47},
	{0, 3, 6, 59}, {0, 4, 6, 83},
	{0, 7, 6, 131}, {0, 9, 6, 515},
	{16, 0, 4, 4}, {0, 0, 4, 5},
	{32, 0, 5, 6}, {0, 0, 5, 7},
	{32, 0, 5, 9}, {0, 0, 5, 10},
	{0, 0, 6, 12}, {0, 0, 6, 15},
	{0, 0, 6, 18}, {0, 0, 6, 21},
	{0, 0, 6, 24}, {0, 0, 6, 27},
	{0, 0, 6, 30}, {0, 0, 6, 33},
	{0, 1, 6, 35}, {0, 1, 6, 39},
	{0, 2, 6, 43}, {0, 3, 6, 51},
	{0, 4, 6, 67}, {0, 5, 6, 99},
	{0, 8, 6, 259}, {32, 0, 4, 4},
	{48, 0, 4, 4}, {16, 0, 4, 5},
	{32, 0, 5, 7}, {32, 0, 5, 8},
	{32, 0, 5, 10}, {32, 0, 5, 11},
	{0, 0, 6, 14}, {0, 0, 6, 17},
	{0, 0, 6, 20}, {0, 0, 6, 23},
	{0, 0, 6, 26}, {0, 0, 6, 29},
	{0, 0, 6, 32}, {0, 16, 6, 65539},
	{0, 15, 6, 32771}, {0, 14, 6, 16387},
	{0, 13, 6, 8195}, {0, 12, 6, 4099},
	{0, 11, 6, 2051}, {0, 10, 6, 1027},
}

// buildSeqTableRLE. C: zstd_decompress_block.c:ZSTD_buildSeqTable_rle
func buildSeqTableRLE(dt []seqSymbol, baseValue uint32, nbAddBits uint8) {
	dt[0] = seqSymbol{} // header: tableLog = 0, fastMode = 0
	dt[1] = seqSymbol{nbBits: 0, nextState: 0, nbAdditionalBits: nbAddBits, baseValue: baseValue}
}

// buildFSETable generates the FSE decoding table of one symbol type (ll,
// ml or off) into dt (header first). It cannot fail if its input is valid:
// all inputs are presumed validated at this stage (by fse.ReadNCount).
// C: zstd_decompress_block.c:ZSTD_buildFSETable (ZSTD_buildFSETable_body)
func buildFSETable(dt []seqSymbol, normalizedCounter []int16, maxSymbolValue uint,
	baseValue []uint32, nbAdditionalBits []uint8, tableLog uint, wksp *buildFSETableWksp) {
	tableDecode := dt[1:]
	maxSV1 := maxSymbolValue + 1
	tableSize := uint32(1) << tableLog

	symbolNext := wksp.symbolNext[:]
	spread := wksp.spread[:]
	highThreshold := tableSize - 1

	// Init, lay down lowprob symbols
	{
		fastMode := uint16(1)
		largeLimit := int16(1 << (tableLog - 1))
		for s := uint(0); s < maxSV1; s++ {
			if normalizedCounter[s] == -1 {
				tableDecode[highThreshold].baseValue = uint32(s)
				highThreshold--
				symbolNext[s] = 1
			} else {
				if normalizedCounter[s] >= largeLimit {
					fastMode = 0
				}
				symbolNext[s] = uint16(normalizedCounter[s])
			}
		}
		dt[0] = seqSymbol{nextState: fastMode, baseValue: uint32(tableLog)} // C: ZSTD_seqSymbol_header
	}

	// Spread symbols
	// Specialized symbol spreading for the case when there are no low
	// probability (-1 count) symbols. When compressing small blocks we
	// avoid low probability symbols to hit this case, since header decoding
	// speed matters more.
	if highThreshold == tableSize-1 {
		tableMask := tableSize - 1
		step := fseTableStep(tableSize)
		// First lay down the symbols in order. We use a uint64_t to lay down
		// 8 bytes at a time. This reduces branch misses since small blocks
		// generally have small table logs, so nearly all symbols have counts
		// <= 8. We ensure we have 8 bytes at the end of our buffer to handle
		// the over-write.
		{
			const add = 0x0101010101010101
			pos := 0
			var sv uint64
			for s := uint(0); s < maxSV1; s, sv = s+1, sv+add {
				n := int(normalizedCounter[s])
				binary.LittleEndian.PutUint64(spread[pos:], sv)
				for i := 8; i < n; i += 8 {
					binary.LittleEndian.PutUint64(spread[pos+i:], sv)
				}
				pos += n
			}
		}
		// Now we spread those positions across the table. The benefit of
		// doing it in two stages is that we avoid the variable size inner
		// loop, which caused lots of branch misses. Now we can run through
		// all the positions without any branch misses. We unroll the loop
		// twice, since that is what empirically worked best.
		{
			position := uint32(0)
			const unroll = 2
			for s := uint32(0); s < tableSize; s += unroll {
				for u := uint32(0); u < unroll; u++ {
					uPosition := (position + u*step) & tableMask
					tableDecode[uPosition].baseValue = uint32(spread[s+u])
				}
				position = (position + unroll*step) & tableMask
			}
		}
	} else {
		tableMask := tableSize - 1
		step := fseTableStep(tableSize)
		position := uint32(0)
		for s := uint(0); s < maxSV1; s++ {
			n := int(normalizedCounter[s])
			for i := 0; i < n; i++ {
				tableDecode[position].baseValue = uint32(s)
				position = (position + step) & tableMask
				for position > highThreshold { // lowprob area
					position = (position + step) & tableMask
				}
			}
		}
	}

	// Build Decoding table
	for u := uint32(0); u < tableSize; u++ {
		symbol := tableDecode[u].baseValue
		nextState := uint32(symbolNext[symbol])
		symbolNext[symbol]++
		nbBits := uint8(uint32(tableLog) - highbit32(nextState))
		tableDecode[u].nbBits = nbBits
		tableDecode[u].nextState = uint16((nextState << nbBits) - tableSize)
		tableDecode[u].nbAdditionalBits = nbAdditionalBits[symbol]
		tableDecode[u].baseValue = baseValue[symbol]
	}
}

// fseTableStep. C: fse.h:FSE_TABLESTEP
func fseTableStep(tableSize uint32) uint32 { return tableSize>>1 + tableSize>>3 + 3 }

// buildSeqTable sets *DTablePtr to the table of one symbol type described
// at the start of src, building it into DTableSpace when needed, and
// returns the number of bytes read from src.
// C: zstd_decompress_block.c:ZSTD_buildSeqTable (without the prefetch)
func buildSeqTable(DTableSpace []seqSymbol, DTablePtr *[]seqSymbol,
	typ symbolEncodingType, max uint, maxLog uint, src []byte,
	baseValue []uint32, nbAdditionalBits []uint8,
	defaultTable []seqSymbol, flagRepeatTable bool, wksp *buildFSETableWksp) (int, dErr) {
	switch typ {
	case setRLE:
		if len(src) == 0 {
			return 0, dErrBlockTooSmall // C: srcSize_wrong, reported as corruption_detected by the caller
		}
		if uint(src[0]) > max {
			return 0, dErrCorruptionDetected
		}
		symbol := src[0]
		baseline := baseValue[symbol]
		nbBits := nbAdditionalBits[symbol]
		buildSeqTableRLE(DTableSpace, baseline, nbBits)
		*DTablePtr = DTableSpace
		return 1, 0
	case setBasic:
		*DTablePtr = defaultTable
		return 0, 0
	case setRepeat:
		if !flagRepeatTable {
			return 0, dErrCorruptionDetected
		}
		return 0, 0
	default: // setCompressed
		var norm [maxSeq + 1]int16
		tableLog, headerSize, err := fse.ReadNCount(norm[:], &max, src)
		if err != nil {
			return 0, dErrCorruptionDetected
		}
		if tableLog > maxLog {
			return 0, dErrCorruptionDetected
		}
		buildFSETable(DTableSpace, norm[:], max, baseValue, nbAdditionalBits, tableLog, wksp)
		*DTablePtr = DTableSpace
		return headerSize, 0
	}
}

// decodeSeqHeaders decodes the sequences section header at the start of
// src (the block content after the literals) and builds the three tables.
// It returns the number of sequences and the header size.
// C: zstd_decompress_block.c:ZSTD_decodeSeqHeaders
func decodeSeqHeaders(d *dctx, src []byte) (nbSeq int, n int, err dErr) {
	iend := len(src)
	ip := 0

	// check
	if len(src) < minSequencesSize {
		return 0, 0, dErrSeqHeaderTooSmall
	}

	// SeqHead
	nbSeq = int(src[ip])
	ip++
	if nbSeq > 0x7F {
		if nbSeq == 0xFF {
			if ip+2 > iend {
				return 0, 0, dErrSeqHeaderTooSmall
			}
			nbSeq = int(binary.LittleEndian.Uint16(src[ip:])) + longNbSeq
			ip += 2
		} else {
			if ip >= iend {
				return 0, 0, dErrSeqHeaderTooSmall
			}
			nbSeq = (nbSeq-0x80)<<8 + int(src[ip])
			ip++
		}
	}

	if nbSeq == 0 {
		// No sequence : section ends immediately
		if ip != iend {
			return nbSeq, 0, dErrCorruptionDetected // extraneous data present in the Sequences section
		}
		return nbSeq, ip, 0
	}

	// FSE table descriptors
	if ip+1 > iend { // minimum possible size: 1 byte for symbol encoding types
		return nbSeq, 0, dErrSeqHeaderTooSmall
	}
	if src[ip]&3 != 0 { // The last field, Reserved, must be all-zeroes.
		return nbSeq, 0, dErrCorruptionDetected
	}
	llType := symbolEncodingType(src[ip] >> 6)
	ofType := symbolEncodingType((src[ip] >> 4) & 3)
	mlType := symbolEncodingType((src[ip] >> 2) & 3)
	ip++

	// Build DTables
	// C: every ZSTD_buildSeqTable error is reported as corruption_detected;
	// the refined codes keep that C code.
	llhSize, e := buildSeqTable(d.entropy.llTable[:], &d.llTptr, llType, maxLL, llFSELog,
		src[ip:], llBase[:], llBits[:], llDefaultDTable[:], d.fseEntropy, &d.entropy.workspace)
	if e != 0 {
		return nbSeq, 0, e
	}
	ip += llhSize

	ofhSize, e := buildSeqTable(d.entropy.ofTable[:], &d.ofTptr, ofType, maxOff, offFSELog,
		src[ip:], ofBase[:], ofBits[:], ofDefaultDTable[:], d.fseEntropy, &d.entropy.workspace)
	if e != 0 {
		return nbSeq, 0, e
	}
	ip += ofhSize

	mlhSize, e := buildSeqTable(d.entropy.mlTable[:], &d.mlTptr, mlType, maxML, mlFSELog,
		src[ip:], mlBase[:], mlBits[:], mlDefaultDTable[:], d.fseEntropy, &d.entropy.workspace)
	if e != 0 {
		return nbSeq, 0, e
	}
	ip += mlhSize

	return nbSeq, ip, 0
}

// seq is one decoded sequence. C: zstd_decompress_block.c:seq_t
//
// The fields are C's size_t values reinterpreted as int: on 64-bit
// targets the same bits as C's 64-bit build, on 32-bit ones those of its
// 32-bit build. offset can be a corrupt offset C decodes as a huge size_t
// (e.g. (size_t)-1 when a repeat offset is 0), which is negative here: the
// sequence executor must compare offsets as uint, as C compares size_t.
type seq struct {
	litLength   int
	matchLength int
	offset      int
}

// fseState. C: zstd_decompress_block.c:ZSTD_fseState
type fseState struct {
	state int
	table []seqSymbol // C: dt + 1 (the cells, header skipped)
}

// seqState. C: zstd_decompress_block.c:seqState_t
type seqState struct {
	dStream    bitstream.DStream
	stateLL    fseState
	stateOffb  fseState
	stateML    fseState
	prevOffset [repNum]int // C: size_t prevOffset[ZSTD_REP_NUM]; see seq
}

// initFseState. C: zstd_decompress_block.c:ZSTD_initFseState
func initFseState(dStatePtr *fseState, bitD *bitstream.DStream, dt []seqSymbol) {
	tableLog := dt[0].baseValue // C: ZSTD_seqSymbol_header.tableLog
	dStatePtr.state = int(bitD.ReadBits(tableLog))
	bitD.Reload()
	dStatePtr.table = dt[1:]
}

// updateFseStateWithDInfo. C: zstd_decompress_block.c:ZSTD_updateFseStateWithDInfo
func updateFseStateWithDInfo(dStatePtr *fseState, bitD *bitstream.DStream, nextState uint16, nbBits uint32) {
	lowBits := bitD.ReadBits(nbBits)
	dStatePtr.state = int(nextState) + int(lowBits)
}

// decodeSequence decodes the next sequence and, unless isLastSeq, updates
// the FSE states. This is C's 64-bit path on every GOARCH (the container
// is 64 bits wide everywhere), so longOffsets, which only matters to C's
// 32-bit path, is unused.
// C: zstd_decompress_block.c:ZSTD_decodeSequence
func decodeSequence(s *seqState, longOffsets bool, isLastSeq bool) seq {
	_ = longOffsets
	var sq seq
	llDInfo := &s.stateLL.table[s.stateLL.state]
	mlDInfo := &s.stateML.table[s.stateML.state]
	ofDInfo := &s.stateOffb.table[s.stateOffb.state]
	sq.matchLength = int(mlDInfo.baseValue)
	sq.litLength = int(llDInfo.baseValue)
	ofBase := ofDInfo.baseValue
	llBits := uint32(llDInfo.nbAdditionalBits)
	mlBits := uint32(mlDInfo.nbAdditionalBits)
	ofBits := uint32(ofDInfo.nbAdditionalBits)
	totalBits := llBits + mlBits + ofBits

	llNext := llDInfo.nextState
	mlNext := mlDInfo.nextState
	ofNext := ofDInfo.nextState
	llnbBits := uint32(llDInfo.nbBits)
	mlnbBits := uint32(mlDInfo.nbBits)
	ofnbBits := uint32(ofDInfo.nbBits)

	// sequence
	{
		var offset int
		if ofBits > 1 {
			offset = int(ofBase) + int(s.dStream.ReadBitsFast(ofBits)) // <= (ZSTD_WINDOWLOG_MAX-1) bits
			s.prevOffset[2] = s.prevOffset[1]
			s.prevOffset[1] = s.prevOffset[0]
			s.prevOffset[0] = offset
		} else {
			ll0 := 0
			if llDInfo.baseValue == 0 {
				ll0 = 1
			}
			if ofBits == 0 {
				offset = s.prevOffset[ll0]
				s.prevOffset[1] = s.prevOffset[ll0^1]
				s.prevOffset[0] = offset
			} else {
				offset = int(ofBase) + ll0 + int(s.dStream.ReadBitsFast(1))
				var temp int
				if offset == 3 {
					temp = s.prevOffset[0] - 1
				} else {
					temp = s.prevOffset[offset]
				}
				if temp == 0 { // 0 is not valid: input corrupted => force offset to -1 => corruption detected at execSequence
					temp--
				}
				if offset != 1 {
					s.prevOffset[2] = s.prevOffset[1]
				}
				s.prevOffset[1] = s.prevOffset[0]
				offset = temp
				s.prevOffset[0] = offset
			}
		}
		sq.offset = offset
	}

	if mlBits > 0 {
		sq.matchLength += int(s.dStream.ReadBitsFast(mlBits))
	}

	if totalBits >= streamAccumulatorMin-(llFSELog+mlFSELog+offFSELog) {
		s.dStream.Reload()
	}
	// Ensure there are enough bits to read the rest of data in 64-bit mode.
	// C: ZSTD_STATIC_ASSERT(16+LLFSELog+MLFSELog+OffFSELog < STREAM_ACCUMULATOR_MIN_64)

	if llBits > 0 {
		sq.litLength += int(s.dStream.ReadBitsFast(llBits))
	}

	if !isLastSeq {
		// don't update FSE state for last Sequence
		updateFseStateWithDInfo(&s.stateLL, &s.dStream, llNext, llnbBits)   // <=  9 bits
		updateFseStateWithDInfo(&s.stateML, &s.dStream, mlNext, mlnbBits)   // <=  9 bits
		updateFseStateWithDInfo(&s.stateOffb, &s.dStream, ofNext, ofnbBits) // <=  8 bits
		s.dStream.Reload()
	}

	return sq
}

/*-*************************************************************
 *   Sequence execution
 ***************************************************************/

// C: zstd_internal.h:WILDCOPY_VECLEN (WILDCOPY_OVERLENGTH is
// wildcopyOverlength, shared with the encoder).
const wildcopyVecLen = 16

// overlapType. C: zstd_internal.h:ZSTD_overlap_e
type overlapType uint8

const (
	noOverlap           overlapType = iota // C: ZSTD_no_overlap
	overlapSrcBeforeDst                    // C: ZSTD_overlap_src_before_dst
)

// copy16 copies 16 bytes from src[ip:] to dst[op:], as a memmove (the
// literal buffer can be within dst). C: zstd_internal.h:ZSTD_copy16
func copy16(dst []byte, op int, src []byte, ip int) {
	copy(dst[op:op+16], src[ip:ip+16])
}

// wildcopy copies length bytes from src[ip:] to dst[op:] in 8- or 16-byte
// chunks, reading and writing up to WILDCOPY_OVERLENGTH bytes past the end
// (all of it when length <= 0), exactly as C: with overlapSrcBeforeDst, src
// and dst are the same buffer, ip < op, at least 8 bytes apart; with
// noOverlap the two ranges are at least WILDCOPY_VECLEN bytes apart (or in
// different buffers). C: zstd_internal.h:ZSTD_wildcopy
func wildcopy(dst []byte, op int, src []byte, ip int, length int, ovtype overlapType) {
	diff := op - ip
	oend := op + length

	if ovtype == overlapSrcBeforeDst && diff < wildcopyVecLen {
		// Handle short offset copies.
		for {
			copy(dst[op:op+8], src[ip:ip+8]) // C: COPY8
			op += 8
			ip += 8
			if op >= oend {
				break
			}
		}
	} else {
		// Separate out the first COPY16() call because the copy length is
		// almost certain to be short, so the branches have different
		// probabilities. Since it is almost certain to be short, only do one
		// COPY16() in the first call. Then, do two calls per loop since at
		// that point it is more likely to have a high trip count.
		copy16(dst, op, src, ip)
		if 16 >= length {
			return
		}
		op += 16
		ip += 16
		for {
			copy16(dst, op, src, ip) // C: COPY16
			op += 16
			ip += 16
			copy16(dst, op, src, ip)
			op += 16
			ip += 16
			if op >= oend {
				break
			}
		}
	}
}

// C: ZSTD_overlapCopy8's dec32table (added) and dec64table (subtracted).
var (
	dec32table = [8]int{0, 1, 2, 1, 4, 4, 4, 4}
	dec64table = [8]int{8, 8, 8, 7, 8, 9, 10, 11}
)

// overlapCopy8 copies 8 bytes from b[ip:] to b[op:] and returns op and ip
// advanced, where ip <= op. If the offset is < 8 then the offset is spread
// to at least 8 bytes. Postcondition: op - ip >= 8.
// C: zstd_decompress_block.c:ZSTD_overlapCopy8
func overlapCopy8(b []byte, op, ip int, offset int) (int, int) {
	if offset < 8 {
		// close range match, overlap
		sub2 := dec64table[offset]
		b[op] = b[ip]
		b[op+1] = b[ip+1]
		b[op+2] = b[ip+2]
		b[op+3] = b[ip+3]
		ip += dec32table[offset]
		copy(b[op+4:op+8], b[ip:ip+4]) // C: ZSTD_copy4
		ip -= sub2
	} else {
		copy(b[op:op+8], b[ip:ip+8]) // C: ZSTD_copy8
	}
	return op + 8, ip + 8
}

// safecopy is a memcpy() that may read up to WILDCOPY_OVERLENGTH bytes past
// the input and write up to 16 bytes past oendW (op >= oendW is allowed).
// It is only called in the uncommon case where the sequence is near the end
// of the block. With noOverlap the source and destination are at least
// WILDCOPY_VECLEN bytes apart (or in different buffers); with
// overlapSrcBeforeDst src is out and ip <= op, any distance apart.
// C: zstd_decompress_block.c:ZSTD_safecopy
func safecopy(out []byte, op, oendW int, src []byte, ip int, length int, ovtype overlapType) {
	diff := op - ip
	oend := op + length

	if length < 8 {
		// Handle short lengths.
		for op < oend {
			out[op] = src[ip]
			op++
			ip++
		}
		return
	}
	if ovtype == overlapSrcBeforeDst {
		// Copy 8 bytes and ensure the offset >= 8 when there can be overlap.
		op, ip = overlapCopy8(out, op, ip, diff)
		length -= 8
	}

	if oend <= oendW {
		// No risk of overwrite.
		wildcopy(out, op, src, ip, length, ovtype)
		return
	}
	if op <= oendW {
		// Wildcopy until we get close to the end.
		wildcopy(out, op, src, ip, oendW-op, ovtype)
		ip += oendW - op
		op = oendW
	}
	// Handle the leftovers.
	for op < oend {
		out[op] = src[ip]
		op++
		ip++
	}
}

// safecopyDstBeforeSrc allows overlap with dst before src, or handles the
// non-overlap case with dst after src, within one buffer b.
// C: zstd_decompress_block.c:ZSTD_safecopyDstBeforeSrc
func safecopyDstBeforeSrc(b []byte, op, ip int, length int) {
	diff := op - ip
	oend := op + length

	if length < 8 || diff > -8 {
		// Handle short lengths, close overlaps, and dst not before src.
		for op < oend {
			b[op] = b[ip]
			op++
			ip++
		}
		return
	}

	if op <= oend-wildcopyOverlength && diff < -wildcopyVecLen {
		n := oend - wildcopyOverlength - op
		wildcopy(b, op, b, ip, n, noOverlap)
		ip += n
		op += n
	}

	// Handle the leftovers.
	for op < oend {
		b[op] = b[ip]
		op++
		ip++
	}
}

// The sequence executors write the sequence at out[op:] and return its
// size. Pointers become offsets: op, oend, oendW and prefixStart index out
// (the output buffer, with the current segment from prefixStart), litPtr and
// litLimit index lits, and the match source is out or extDict (C's
// virtualStart..dictEnd, ending where the prefix starts). Offsets and
// lengths compare as uint where C compares size_t, so an offset holding
// C's huge size_t bits (negative here) is rejected on every GOARCH.
//
// Bounds: C reads and writes up to WILDCOPY_OVERLENGTH bytes past a copy,
// within limits its callers guarantee, and so do these: writes stay below
// oend (the fast paths need oMatchEnd <= oend - WILDCOPY_OVERLENGTH), and
// literal reads below litLimit + WILDCOPY_OVERLENGTH, which every literal
// buffer placement leaves room for (ZSTD_allocateLiteralsBuffer, and raw
// literals referenced in src only with WILDCOPY_OVERLENGTH bytes after).

// execSequenceEnd handles the cases near the end of the output buffer,
// with more careful checks against overflow. It needs to be fast for a
// single long sequence, but not for many small sequences, since those fall
// into execSequence. C: zstd_decompress_block.c:ZSTD_execSequenceEnd
func execSequenceEnd(out []byte, op, oend int, sequence seq,
	lits []byte, litPtr *int, litLimit int, prefixStart int, extDict []byte) (int, dErr) {
	oLitEnd := op + sequence.litLength
	sequenceLength := sequence.litLength + sequence.matchLength
	iLitEnd := *litPtr + sequence.litLength
	oendW := oend - wildcopyOverlength
	virtualStart := prefixStart - len(extDict)

	// bounds checks : careful of address space overflow in 32-bit mode
	if uint(sequenceLength) > uint(oend-op) {
		return 0, dErrBlockDstSizeTooSmall // last match must fit within dstBuffer
	}
	if uint(sequence.litLength) > uint(litLimit-*litPtr) {
		return 0, dErrCorruptionDetected // try to read beyond literal buffer
	}

	// copy literals
	safecopy(out, op, oendW, lits, *litPtr, sequence.litLength, noOverlap)
	op = oLitEnd
	*litPtr = iLitEnd

	// copy Match
	match := oLitEnd - sequence.offset
	if uint(sequence.offset) > uint(oLitEnd-prefixStart) {
		// offset beyond prefix
		if uint(sequence.offset) > uint(oLitEnd-virtualStart) {
			return 0, dErrOffsetBeyondHistory
		}
		dmatch := len(extDict) - (prefixStart - match) // C: dictEnd - (prefixStart - match)
		if dmatch+sequence.matchLength <= len(extDict) {
			copy(out[oLitEnd:oLitEnd+sequence.matchLength], extDict[dmatch:]) // C: ZSTD_memmove
			return sequenceLength, 0
		}
		// span extDict & currentPrefixSegment
		length1 := len(extDict) - dmatch
		copy(out[oLitEnd:oLitEnd+length1], extDict[dmatch:])
		op = oLitEnd + length1
		sequence.matchLength -= length1
		match = prefixStart
	}
	safecopy(out, op, oendW, out, match, sequence.matchLength, overlapSrcBeforeDst)
	return sequenceLength, 0
}

// execSequenceEndSplitLitBuffer is execSequenceEnd while the literal
// buffer is still split: the literals are in out, ahead of op.
// C: zstd_decompress_block.c:ZSTD_execSequenceEndSplitLitBuffer
func execSequenceEndSplitLitBuffer(out []byte, op, oend, oendW int, sequence seq,
	litPtr *int, litLimit int, prefixStart int, extDict []byte) (int, dErr) {
	oLitEnd := op + sequence.litLength
	sequenceLength := sequence.litLength + sequence.matchLength
	iLitEnd := *litPtr + sequence.litLength
	virtualStart := prefixStart - len(extDict)

	// bounds checks : careful of address space overflow in 32-bit mode
	if uint(sequenceLength) > uint(oend-op) {
		return 0, dErrBlockDstSizeTooSmall // last match must fit within dstBuffer
	}
	if uint(sequence.litLength) > uint(litLimit-*litPtr) {
		return 0, dErrCorruptionDetected // try to read beyond literal buffer
	}

	// copy literals
	if op > *litPtr && op < *litPtr+sequence.litLength {
		return 0, dErrBlockDstSizeTooSmall // output should not catch up to and overwrite literal buffer
	}
	safecopyDstBeforeSrc(out, op, *litPtr, sequence.litLength)
	op = oLitEnd
	*litPtr = iLitEnd

	// copy Match
	match := oLitEnd - sequence.offset
	if uint(sequence.offset) > uint(oLitEnd-prefixStart) {
		// offset beyond prefix
		if uint(sequence.offset) > uint(oLitEnd-virtualStart) {
			return 0, dErrOffsetBeyondHistory
		}
		dmatch := len(extDict) - (prefixStart - match) // C: dictEnd - (prefixStart - match)
		if dmatch+sequence.matchLength <= len(extDict) {
			copy(out[oLitEnd:oLitEnd+sequence.matchLength], extDict[dmatch:]) // C: ZSTD_memmove
			return sequenceLength, 0
		}
		// span extDict & currentPrefixSegment
		length1 := len(extDict) - dmatch
		copy(out[oLitEnd:oLitEnd+length1], extDict[dmatch:])
		op = oLitEnd + length1
		sequence.matchLength -= length1
		match = prefixStart
	}
	safecopy(out, op, oendW, out, match, sequence.matchLength, overlapSrcBeforeDst)
	return sequenceLength, 0
}

// execSequence. C: zstd_decompress_block.c:ZSTD_execSequence
func execSequence(out []byte, op, oend int, sequence seq,
	lits []byte, litPtr *int, litLimit int, prefixStart int, extDict []byte) (int, dErr) {
	oLitEnd := op + sequence.litLength
	sequenceLength := sequence.litLength + sequence.matchLength
	oMatchEnd := op + sequenceLength
	oendW := oend - wildcopyOverlength
	iLitEnd := *litPtr + sequence.litLength
	virtualStart := prefixStart - len(extDict)

	// C (aarch64): PREFETCH_L1(match); prefetching is Task 20's.

	// Handle edge cases in a slow path:
	//   - Read beyond end of literals
	//   - Match end is within WILDCOPY_OVERLIMIT of oend
	// C's third case (32-bit mode and the match length overflows) cannot
	// happen with int offsets: lengths are below 2^18.
	if iLitEnd > litLimit || oMatchEnd > oendW {
		return execSequenceEnd(out, op, oend, sequence, lits, litPtr, litLimit, prefixStart, extDict)
	}

	// Copy Literals:
	// Split out litLength <= 16 since it is nearly always true. +1.6% on
	// gcc-9. We likely don't need the full 32-byte wildcopy.
	copy16(out, op, lits, *litPtr)
	if sequence.litLength > 16 {
		wildcopy(out, op+16, lits, *litPtr+16, sequence.litLength-16, noOverlap)
	}
	op = oLitEnd
	*litPtr = iLitEnd // update for next sequence

	// Copy Match
	match := oLitEnd - sequence.offset
	if uint(sequence.offset) > uint(oLitEnd-prefixStart) {
		// offset beyond prefix -> go into extDict
		if uint(sequence.offset) > uint(oLitEnd-virtualStart) {
			return 0, dErrOffsetBeyondHistory
		}
		dmatch := len(extDict) + (match - prefixStart) // C: dictEnd + (match - prefixStart)
		if dmatch+sequence.matchLength <= len(extDict) {
			copy(out[oLitEnd:oLitEnd+sequence.matchLength], extDict[dmatch:]) // C: ZSTD_memmove
			return sequenceLength, 0
		}
		// span extDict & currentPrefixSegment
		length1 := len(extDict) - dmatch
		copy(out[oLitEnd:oLitEnd+length1], extDict[dmatch:])
		op = oLitEnd + length1
		sequence.matchLength -= length1
		match = prefixStart
	}
	// Match within prefix of 1 or more bytes

	// Nearly all offsets are >= WILDCOPY_VECLEN bytes, which means we can
	// use wildcopy without overlap checking.
	if uint(sequence.offset) >= wildcopyVecLen {
		// We bet on a full wildcopy for matches, since we expect matches to
		// be longer than literals (in general). In silesia, ~10% of matches
		// are longer than 16 bytes.
		wildcopy(out, op, out, match, sequence.matchLength, noOverlap)
		return sequenceLength, 0
	}

	// Copy 8 bytes and spread the offset to be >= 8.
	op, match = overlapCopy8(out, op, match, sequence.offset)

	// If the match length is > 8 bytes, then continue with the wildcopy.
	if sequence.matchLength > 8 {
		wildcopy(out, op, out, match, sequence.matchLength-8, overlapSrcBeforeDst)
	}
	return sequenceLength, 0
}

// execSequenceSplitLitBuffer is execSequence while the literal buffer is
// still split: the literals are in out, ahead of op, and oendW is the
// caller's limit (C: litPtr + litLength - WILDCOPY_OVERLENGTH) that keeps
// the wildcopy writes behind them.
// C: zstd_decompress_block.c:ZSTD_execSequenceSplitLitBuffer
func execSequenceSplitLitBuffer(out []byte, op, oend, oendW int, sequence seq,
	litPtr *int, litLimit int, prefixStart int, extDict []byte) (int, dErr) {
	oLitEnd := op + sequence.litLength
	sequenceLength := sequence.litLength + sequence.matchLength
	oMatchEnd := op + sequenceLength
	iLitEnd := *litPtr + sequence.litLength
	virtualStart := prefixStart - len(extDict)

	// Handle edge cases in a slow path:
	//   - Read beyond end of literals
	//   - Match end is within WILDCOPY_OVERLIMIT of oend
	// C's 32-bit overflow case cannot happen here (see execSequence).
	if iLitEnd > litLimit || oMatchEnd > oendW {
		return execSequenceEndSplitLitBuffer(out, op, oend, oendW, sequence, litPtr, litLimit, prefixStart, extDict)
	}

	// Copy Literals:
	// Split out litLength <= 16 since it is nearly always true. +1.6% on
	// gcc-9. We likely don't need the full 32-byte wildcopy.
	copy16(out, op, out, *litPtr)
	if sequence.litLength > 16 {
		wildcopy(out, op+16, out, *litPtr+16, sequence.litLength-16, noOverlap)
	}
	op = oLitEnd
	*litPtr = iLitEnd // update for next sequence

	// Copy Match
	match := oLitEnd - sequence.offset
	if uint(sequence.offset) > uint(oLitEnd-prefixStart) {
		// offset beyond prefix -> go into extDict
		if uint(sequence.offset) > uint(oLitEnd-virtualStart) {
			return 0, dErrOffsetBeyondHistory
		}
		dmatch := len(extDict) + (match - prefixStart) // C: dictEnd + (match - prefixStart)
		if dmatch+sequence.matchLength <= len(extDict) {
			copy(out[oLitEnd:oLitEnd+sequence.matchLength], extDict[dmatch:]) // C: ZSTD_memmove
			return sequenceLength, 0
		}
		// span extDict & currentPrefixSegment
		length1 := len(extDict) - dmatch
		copy(out[oLitEnd:oLitEnd+length1], extDict[dmatch:])
		op = oLitEnd + length1
		sequence.matchLength -= length1
		match = prefixStart
	}
	// Match within prefix of 1 or more bytes

	// Nearly all offsets are >= WILDCOPY_VECLEN bytes, which means we can
	// use wildcopy without overlap checking.
	if uint(sequence.offset) >= wildcopyVecLen {
		// We bet on a full wildcopy for matches, since we expect matches to
		// be longer than literals (in general). In silesia, ~10% of matches
		// are longer than 16 bytes.
		wildcopy(out, op, out, match, sequence.matchLength, noOverlap)
		return sequenceLength, 0
	}

	// Copy 8 bytes and spread the offset to be >= 8.
	op, match = overlapCopy8(out, op, match, sequence.offset)

	// If the match length is > 8 bytes, then continue with the wildcopy.
	if sequence.matchLength > 8 {
		wildcopy(out, op, out, match, sequence.matchLength-8, overlapSrcBeforeDst)
	}
	return sequenceLength, 0
}

/*-*************************************************************
 *   Sequence loops
 ***************************************************************/

// The sequence loops decode nbSeq sequences from seqSrc (the rest of the
// block after the sequences header) and execute them, with the literals
// decodeLiteralsBlock left, into out[ostart:ostart+maxDstSize]. They return
// the block's decoded size.

// litBufferStart returns the buffer the literals are in and the offset of
// d.litBuf within it: out itself when they are in dst (inDst, split),
// since decodeLiteralsBlock was given out[ostart:].
func (d *dctx) litBufferStart(out []byte, ostart int) ([]byte, int) {
	if d.litBufferLocation == notInDst {
		return d.litBuf, 0
	}
	return out, ostart
}

// initSeqState starts the sequence bitstream and the FSE states, with the
// repeat offsets of the previous block (the prologue the C loops share).
func (d *dctx) initSeqState(st *seqState, seqSrc []byte) dErr {
	d.fseEntropy = true
	for i := range st.prevOffset {
		st.prevOffset[i] = int(d.entropy.rep[i])
	}
	if _, err := st.dStream.Init(seqSrc); err != nil {
		return dErrCorruptionDetected
	}
	initFseState(&st.stateLL, &st.dStream, d.llTptr)
	initFseState(&st.stateOffb, &st.dStream, d.ofTptr)
	initFseState(&st.stateML, &st.dStream, d.mlTptr)
	return 0
}

// saveReps saves the repeat offsets for the next block (as U32, like C).
func (d *dctx) saveReps(st *seqState) {
	for i := range st.prevOffset {
		d.entropy.rep[i] = uint32(st.prevOffset[i])
	}
}

// decompressSequencesBodySplitLitBuffer decodes the sequences while the
// literals are split between dst and litExtraBuffer.
// C: zstd_decompress_block.c:ZSTD_decompressSequences_bodySplitLitBuffer
func (d *dctx) decompressSequencesBodySplitLitBuffer(out []byte, ostart, maxDstSize int,
	seqSrc []byte, nbSeq int, isLongOffset bool) (int, dErr) {
	oend := ostart + maxDstSize
	op := ostart
	lits := out // literals are split between internal buffer & output buffer
	litPtr := ostart + d.litPtr
	dctxLitBufferEnd := ostart + d.litBufferEnd // C: dctx->litBufferEnd
	litBufferEnd := dctxLitBufferEnd
	prefixStart := d.prefixStart
	extDict := d.extDict

	if nbSeq != 0 {
		var seqState seqState
		if e := d.initSeqState(&seqState, seqSrc); e != 0 {
			return 0, e
		}

		// decompress without overrunning litPtr begins
		{
			var sequence seq
			// Handle the initial state where litBuffer is currently split
			// between dst and litExtraBuffer.
			for ; nbSeq != 0; nbSeq-- {
				sequence = decodeSequence(&seqState, isLongOffset, nbSeq == 1)
				if litPtr+sequence.litLength > dctxLitBufferEnd {
					break
				}
				oneSeqSize, e := execSequenceSplitLitBuffer(out, op, oend, litPtr+sequence.litLength-wildcopyOverlength,
					sequence, &litPtr, litBufferEnd, prefixStart, extDict)
				if e != 0 {
					return 0, e
				}
				op += oneSeqSize
			}

			// If there are more sequences, they will need to read literals
			// from litExtraBuffer; copy over the remainder from dst and update
			// litPtr and litEnd.
			if nbSeq > 0 {
				leftoverLit := dctxLitBufferEnd - litPtr
				if leftoverLit != 0 {
					if uint(leftoverLit) > uint(oend-op) {
						return 0, dErrBlockDstSizeTooSmall // remaining lit must fit within dstBuffer
					}
					safecopyDstBeforeSrc(out, op, litPtr, leftoverLit)
					sequence.litLength -= leftoverLit
					op += leftoverLit
				}
				lits = d.litExtraBuffer[:]
				litPtr = 0
				litBufferEnd = litBufferExtraSize
				d.litBufferLocation = notInDst
				oneSeqSize, e := execSequence(out, op, oend, sequence, lits, &litPtr, litBufferEnd, prefixStart, extDict)
				if e != 0 {
					return 0, e
				}
				op += oneSeqSize
				nbSeq--
			}
		}

		if nbSeq > 0 {
			// there is remaining lit from extra buffer
			for ; nbSeq != 0; nbSeq-- {
				sequence := decodeSequence(&seqState, isLongOffset, nbSeq == 1)
				oneSeqSize, e := execSequence(out, op, oend, sequence, lits, &litPtr, litBufferEnd, prefixStart, extDict)
				if e != 0 {
					return 0, e
				}
				op += oneSeqSize
			}
		}

		// check if reached exact end
		if nbSeq != 0 {
			return 0, dErrCorruptionDetected
		}
		if !seqState.dStream.EndOfDStream() {
			return 0, dErrCorruptionDetected
		}
		// save reps for next block
		d.saveReps(&seqState)
	}

	// last literal segment
	if d.litBufferLocation == split {
		// split hasn't been reached yet, first get dst then copy litExtraBuffer
		lastLLSize := litBufferEnd - litPtr
		if uint(lastLLSize) > uint(oend-op) {
			return 0, dErrBlockDstSizeTooSmall
		}
		copy(out[op:op+lastLLSize], out[litPtr:litBufferEnd]) // C: ZSTD_memmove
		op += lastLLSize
		lits = d.litExtraBuffer[:]
		litPtr = 0
		litBufferEnd = litBufferExtraSize
		d.litBufferLocation = notInDst
	}
	// copy last literals from internal buffer
	{
		lastLLSize := litBufferEnd - litPtr
		if uint(lastLLSize) > uint(oend-op) {
			return 0, dErrBlockDstSizeTooSmall
		}
		copy(out[op:op+lastLLSize], lits[litPtr:litBufferEnd])
		op += lastLLSize
	}

	return op - ostart, 0
}

// decompressSequencesBody decodes the sequences when the literals are not
// split (in dst after the block's output, or elsewhere).
// C: zstd_decompress_block.c:ZSTD_decompressSequences_body
func (d *dctx) decompressSequencesBody(out []byte, ostart, maxDstSize int,
	seqSrc []byte, nbSeq int, isLongOffset bool) (int, dErr) {
	oend := ostart + d.litBuffer
	if d.litBufferLocation == notInDst {
		oend = ostart + maxDstSize
	}
	op := ostart
	lits, litBase := d.litBufferStart(out, ostart)
	litPtr := litBase + d.litPtr
	litEnd := litPtr + d.litSize
	prefixStart := d.prefixStart
	extDict := d.extDict

	// Regen sequences
	if nbSeq != 0 {
		var seqState seqState
		if e := d.initSeqState(&seqState, seqSrc); e != 0 {
			return 0, e
		}

		for ; nbSeq != 0; nbSeq-- {
			sequence := decodeSequence(&seqState, isLongOffset, nbSeq == 1)
			oneSeqSize, e := execSequence(out, op, oend, sequence, lits, &litPtr, litEnd, prefixStart, extDict)
			if e != 0 {
				return 0, e
			}
			op += oneSeqSize
		}

		// check if reached exact end
		if !seqState.dStream.EndOfDStream() {
			return 0, dErrCorruptionDetected
		}
		// save reps for next block
		d.saveReps(&seqState)
	}

	// last literal segment
	{
		lastLLSize := litEnd - litPtr
		if uint(lastLLSize) > uint(oend-op) {
			return 0, dErrBlockDstSizeTooSmall
		}
		copy(out[op:op+lastLLSize], lits[litPtr:litEnd])
		op += lastLLSize
	}

	return op - ostart, 0
}

// prefetchMatch returns the position (relative to prefixStart) after the
// sequence. C prefetches the sequence's match here; in Go it is a no-op
// until Task 20 (PREFETCHT0 in asm).
// C: zstd_decompress_block.c:ZSTD_prefetchMatch
func prefetchMatch(prefetchPos int, sequence seq) int {
	prefetchPos += sequence.litLength
	return prefetchPos + sequence.matchLength
}

// C: zstd_decompress_block.c:STORED_SEQS, STORED_SEQS_MASK, ADVANCED_SEQS
const (
	storedSeqs     = 8
	storedSeqsMask = storedSeqs - 1
	advancedSeqs   = storedSeqs
)

// seqLongBodyBlocks counts the blocks decoded by decompressSequencesLongBody
// (test-only: it shows tests that a frame took the prefetch path).
var seqLongBodyBlocks atomic.Int64

// decompressSequencesLongBody decodes the sequences ADVANCED_SEQS ahead of
// their execution, to prefetch their matches (employed when a block has a
// significant portion of long-distance matches, or with a "cold"
// dictionary). C: zstd_decompress_block.c:ZSTD_decompressSequencesLong_body
func (d *dctx) decompressSequencesLongBody(out []byte, ostart, maxDstSize int,
	seqSrc []byte, nbSeq int, isLongOffset bool) (int, dErr) {
	seqLongBodyBlocks.Add(1)
	oend := ostart + maxDstSize
	if d.litBufferLocation == inDst {
		oend = ostart + d.litBuffer
	}
	op := ostart
	lits, litBase := d.litBufferStart(out, ostart)
	litPtr := litBase + d.litPtr
	dctxLitBufferEnd := litBase + d.litBufferEnd // C: dctx->litBufferEnd
	litBufferEnd := dctxLitBufferEnd
	prefixStart := d.prefixStart
	extDict := d.extDict

	// exec runs one sequence, switching the literals to litExtraBuffer first
	// when they reach the split point (C repeats this block twice).
	exec := func(sequence *seq) (int, dErr) {
		if d.litBufferLocation == split && litPtr+sequence.litLength > dctxLitBufferEnd {
			// lit buffer is reaching split point, empty out the first buffer
			// and transition to litExtraBuffer
			leftoverLit := dctxLitBufferEnd - litPtr
			if leftoverLit != 0 {
				if uint(leftoverLit) > uint(oend-op) {
					return 0, dErrBlockDstSizeTooSmall // remaining lit must fit within dstBuffer
				}
				safecopyDstBeforeSrc(out, op, litPtr, leftoverLit)
				sequence.litLength -= leftoverLit
				op += leftoverLit
			}
			lits = d.litExtraBuffer[:]
			litPtr = 0
			litBufferEnd = litBufferExtraSize
			d.litBufferLocation = notInDst
			return execSequence(out, op, oend, *sequence, lits, &litPtr, litBufferEnd, prefixStart, extDict)
		}
		// lit buffer is either wholly contained in first or second split,
		// or not split at all
		if d.litBufferLocation == split {
			return execSequenceSplitLitBuffer(out, op, oend, litPtr+sequence.litLength-wildcopyOverlength,
				*sequence, &litPtr, litBufferEnd, prefixStart, extDict)
		}
		return execSequence(out, op, oend, *sequence, lits, &litPtr, litBufferEnd, prefixStart, extDict)
	}

	// Regen sequences
	if nbSeq != 0 {
		var sequences [storedSeqs]seq
		seqAdvance := min(nbSeq, advancedSeqs)
		var seqState seqState
		prefetchPos := op - prefixStart // track position relative to prefixStart
		if e := d.initSeqState(&seqState, seqSrc); e != 0 {
			return 0, e
		}

		// prepare in advance
		seqNb := 0
		for ; seqNb < seqAdvance; seqNb++ {
			sequence := decodeSequence(&seqState, isLongOffset, seqNb == nbSeq-1)
			prefetchPos = prefetchMatch(prefetchPos, sequence)
			sequences[seqNb] = sequence
		}

		// decompress without stomping litBuffer
		for ; seqNb < nbSeq; seqNb++ {
			sequence := decodeSequence(&seqState, isLongOffset, seqNb == nbSeq-1)
			oneSeqSize, e := exec(&sequences[(seqNb-advancedSeqs)&storedSeqsMask])
			if e != 0 {
				return 0, e
			}
			prefetchPos = prefetchMatch(prefetchPos, sequence)
			sequences[seqNb&storedSeqsMask] = sequence
			op += oneSeqSize
		}
		if !seqState.dStream.EndOfDStream() {
			return 0, dErrCorruptionDetected
		}

		// finish queue
		seqNb -= seqAdvance
		for ; seqNb < nbSeq; seqNb++ {
			oneSeqSize, e := exec(&sequences[seqNb&storedSeqsMask])
			if e != 0 {
				return 0, e
			}
			op += oneSeqSize
		}

		// save reps for next block
		d.saveReps(&seqState)
	}

	// last literal segment
	if d.litBufferLocation == split {
		// first deplete literal buffer in dst, then copy litExtraBuffer
		lastLLSize := litBufferEnd - litPtr
		if uint(lastLLSize) > uint(oend-op) {
			return 0, dErrBlockDstSizeTooSmall
		}
		copy(out[op:op+lastLLSize], out[litPtr:litBufferEnd]) // C: ZSTD_memmove
		op += lastLLSize
		lits = d.litExtraBuffer[:]
		litPtr = 0
		litBufferEnd = litBufferExtraSize
	}
	{
		lastLLSize := litBufferEnd - litPtr
		if uint(lastLLSize) > uint(oend-op) {
			return 0, dErrBlockDstSizeTooSmall
		}
		copy(out[op:op+lastLLSize], lits[litPtr:litBufferEnd]) // C: ZSTD_memmove
		op += lastLLSize
	}

	return op - ostart, 0
}

/*-*************************************************************
 *   Block decoder
 ***************************************************************/

// totalHistorySize returns the total size of the history referenceable by
// zstd, including both the prefix and the extDict. At op any offset larger
// than this is invalid. C: zstd_decompress_block.c:ZSTD_totalHistorySize
func totalHistorySize(op, virtualStart int) int {
	return op - virtualStart
}

// offsetInfo. C: zstd_decompress_block.c:ZSTD_OffsetInfo
type offsetInfo struct {
	longOffsetShare     uint32
	maxNbAdditionalBits uint32
}

// getOffsetInfo returns the "share" of long offsets (arbitrarily defined as
// > (1<<23)) compared to maximum possible of (1<<OffFSELog), as well as the
// maximum number of additional bits required. offTable must be valid.
// C: zstd_decompress_block.c:ZSTD_getOffsetInfo
func getOffsetInfo(offTable []seqSymbol, nbSeq int) offsetInfo {
	var info offsetInfo
	// If nbSeq == 0, then the offTable is uninitialized, but we have no
	// sequences, so both values should be 0.
	if nbSeq != 0 {
		tableLog := offTable[0].baseValue // C: ZSTD_seqSymbol_header.tableLog
		table := offTable[1:]
		maxU := uint32(1) << tableLog
		for u := uint32(0); u < maxU; u++ {
			info.maxNbAdditionalBits = max(info.maxNbAdditionalBits, uint32(table[u].nbAdditionalBits))
			if table[u].nbAdditionalBits > 22 {
				info.longOffsetShare++
			}
		}
		info.longOffsetShare <<= offFSELog - tableLog // scale to OffFSELog
	}
	return info
}

// mem32Bits is C's MEM_32bits() for the sequence decoder: false on every
// GOARCH, since decodeSequence uses C's 64-bit path everywhere (the bit
// container is 64 bits wide). So 386 makes C's 64-bit choices too
// (isLongOffset false, prefetch share 7).
const mem32Bits = false

// maxShortOffset returns the maximum offset we can decode in one read of our
// bitstream, without reloading more bits in the middle of the offset bits
// read. Any offsets larger than this must use the long offset decoder.
// C: zstd_decompress_block.c:ZSTD_maxShortOffset (the MEM_64bits() branch:
// we can decode any offset without reloading bits)
func maxShortOffset() uint {
	return ^uint(0)
}

// decompressBlockInternal decodes the compressed block src (its content,
// without the block header) into dst[len(dst):len(dst)+dstCap] and returns
// the decoded size. dst[:len(dst)] is the output buffer before the block
// (the offsets prefixStart, virtualStart and previousDstEnd index it, after
// checkContinuity). Precondition: cap(dst) >= len(dst)+dstCap.
// C: zstd_decompress_block.c:ZSTD_decompressBlock_internal
func (d *dctx) decompressBlockInternal(dst []byte, dstCap int, src []byte, streaming streamingOperation) (int, dErr) {
	// blockType == blockCompressed
	ip := 0
	ostart := len(dst)
	out := dst[:ostart+dstCap]

	// Note : the wording of the specification allows compressed block to be
	// sized exactly ZSTD_blockSizeMax(dctx). This generally does not happen,
	// as it makes little sense, since an uncompressed block would feature
	// same size and have no decompression cost. Also, note that decoder from
	// reference libzstd before < v1.5.4 would consider this edge case as an
	// error. As a consequence, avoid generating compressed blocks of size
	// ZSTD_blockSizeMax(dctx) for broader compatibility with the deployed
	// ecosystem of zstd decoders.
	if len(src) > d.blockSizeMax() {
		return 0, dErrCompressedSizeTooBig
	}

	// Decode literals section
	{
		litCSize, e := d.decodeLiteralsBlock(src, out[ostart:], dstCap, streaming)
		if e != 0 {
			return 0, e
		}
		ip += litCSize
	}

	// Build Decoding Tables
	{
		// Compute the maximum block size, which must also work when !frame
		// and fParams are unset. Additionally, take the min with dstCapacity
		// to ensure that the totalHistorySize fits in a size_t.
		blockSizeMax := min(dstCap, d.blockSizeMax())
		totalHistorySize := totalHistorySize(ostart+blockSizeMax, d.virtualStart)
		// isLongOffset must be true if there are long offsets. Offsets are
		// long if they are larger than ZSTD_maxShortOffset(). We don't
		// expect that to be the case in 64-bit mode.
		//
		// We check here to see if our history is large enough to allow long
		// offsets. If it isn't, then we can't possible have (valid) long
		// offsets. If the offset is invalid, then it is okay to read it
		// incorrectly.
		//
		// If isLongOffsets is true, then we will later check our decoding
		// table to see if it is even possible to generate long offsets.
		isLongOffset := mem32Bits && uint(totalHistorySize) > maxShortOffset()
		usePrefetchDecoder := d.ddictIsCold

		nbSeq, seqHSize, e := decodeSeqHeaders(d, src[ip:])
		if e != 0 {
			return 0, e
		}
		ip += seqHSize

		if dstCap == 0 && nbSeq > 0 {
			return 0, dErrBlockDstSizeTooSmall // NULL not handled
		}
		// C's "invalid dst" check (dst within 1 MiB of the top of the
		// address space) has no Go counterpart.

		// If we could potentially have long offsets, or we might want to use
		// the prefetch decoder, compute information about the share of long
		// offsets, and the maximum nbAdditionalBits.
		// NOTE: could probably use a larger nbSeq limit
		if isLongOffset || (!usePrefetchDecoder && totalHistorySize > 1<<24 && nbSeq > 8) {
			info := getOffsetInfo(d.ofTptr, nbSeq)
			if isLongOffset && info.maxNbAdditionalBits <= streamAccumulatorMin {
				// If isLongOffset, but the maximum number of additional bits
				// that we see in our table is small enough, then we know it
				// is impossible to have too long an offset in this block, so
				// we can use the regular offset decoder.
				isLongOffset = false
			}
			if !usePrefetchDecoder {
				const minShare = 7 // heuristic values, correspond to 2.73% and 7.81% (C: 20 on 32-bit, see mem32Bits)
				usePrefetchDecoder = info.longOffsetShare >= minShare
			}
		}

		d.ddictIsCold = false

		if usePrefetchDecoder {
			return d.decompressSequencesLongBody(out, ostart, dstCap, src[ip:], nbSeq, isLongOffset)
		}

		// else
		if d.litBufferLocation == split {
			return d.decompressSequencesBodySplitLitBuffer(out, ostart, dstCap, src[ip:], nbSeq, isLongOffset)
		}
		return d.decompressSequencesBody(out, ostart, dstCap, src[ip:], nbSeq, isLongOffset)
	}
}

// checkContinuity makes the previous segment the extDict when the output
// position len(dst) of the buffer dst is not where the previous output
// ended, and dstSize > 0. C: zstd_decompress_block.c:ZSTD_checkContinuity
func (d *dctx) checkContinuity(dst []byte, dstSize int) {
	contiguous := cap(dst) > 0 && cap(d.prevDst) > 0 && &dst[:1][0] == &d.prevDst[:1][0] &&
		len(dst) == d.previousDstEnd // C: dst == dctx->previousDstEnd
	if !contiguous && dstSize > 0 {
		d.extDict = d.prevDst[d.prefixStart:d.previousDstEnd] // C: dictEnd = previousDstEnd
		d.virtualStart = len(dst) - len(d.extDict)
		d.prefixStart = len(dst)
		d.previousDstEnd = len(dst)
		d.prevDst = dst[:cap(dst)]
	}
}

// decompressBlock decodes the compressed block src (outside a frame:
// blockSizeMax is ZSTD_BLOCKSIZE_MAX) into dst[len(dst):cap(dst)] and
// returns its size; dst[:len(dst)] is the history. Raw and RLE blocks are
// the caller's to copy (and to tell checkContinuity about).
// C: zstd_decompress_block.c:ZSTD_decompressBlock (ZSTD_decompressBlock_deprecated)
func (d *dctx) decompressBlock(dst, src []byte) (int, dErr) {
	dstCap := cap(dst) - len(dst)
	d.isFrameDecompression = false
	d.checkContinuity(dst, dstCap)
	dSize, e := d.decompressBlockInternal(dst, dstCap, src, notStreaming)
	if e != 0 {
		return 0, e
	}
	if dSize > 0 {
		// C sets previousDstEnd = dst + dSize unconditionally; with dSize 0
		// and dst not contiguous (dstCap 0, so checkContinuity kept the
		// previous buffer) that pointer is in another buffer, which Go's
		// offsets cannot express. It changes nothing when contiguous.
		d.previousDstEnd = len(dst) + dSize
	}
	return dSize, 0
}
