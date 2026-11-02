package zstd

import (
	"encoding/binary"

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
