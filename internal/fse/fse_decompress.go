// Port of libzstd v1.5.7 lib/common/fse_decompress.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license) and the decoder
// helpers of lib/common/fse.h: the byte-symbol FSE decoder, which the
// compressor needs only to read the Huffman weights of a dictionary
// (HUF_readStats).

package fse

import (
	"math/bits"

	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
)

// maxSymbolValue. C: fse.h:FSE_MAX_SYMBOL_VALUE
const maxSymbolValue = 255

// decodeEntry. C: fse.h:FSE_decode_t
type decodeEntry struct {
	newState uint16
	symbol   byte
	nbBits   byte
}

// dTable is a decoding table. C: fse.h:FSE_DTable with its
// FSE_DTableHeader (tableLog; fastMode is not kept: it only selects the
// faster, equivalent symbol reader).
type dTable struct {
	tableLog uint32
	table    []decodeEntry
}

// buildDTable. C: fse_decompress.c:FSE_buildDTable_internal. C lays the
// symbols down 8 bytes at a time through a spread buffer when there are no
// low-probability symbols; that fills the table in the same order as the
// plain loop used here for both cases.
func buildDTable(dt *dTable, norm []int16, maxSV, tableLog uint32) error {
	maxSV1 := maxSV + 1
	tableSize := uint32(1) << tableLog
	highThreshold := tableSize - 1
	var symbolNext [maxSymbolValue + 1]uint16

	// Sanity Checks
	if maxSV > maxSymbolValue {
		return ErrMaxSymbolTooLarge
	}
	if tableLog > MaxTableLog {
		return ErrTableLogTooLarge
	}
	if cap(dt.table) < int(tableSize) {
		dt.table = make([]decodeEntry, tableSize)
	}
	dt.table = dt.table[:tableSize]
	td := dt.table

	// Init, lay down lowprob symbols
	dt.tableLog = tableLog
	for s := uint32(0); s < maxSV1; s++ {
		if norm[s] == -1 {
			td[highThreshold].symbol = byte(s)
			highThreshold--
			symbolNext[s] = 1
		} else {
			symbolNext[s] = uint16(norm[s])
		}
	}

	// Spread symbols
	tableMask := tableSize - 1
	step := tableSize>>1 + tableSize>>3 + 3 // C: FSE_TABLESTEP
	var position uint32
	for s := uint32(0); s < maxSV1; s++ {
		for i := 0; i < int(norm[s]); i++ {
			td[position].symbol = byte(s)
			position = (position + step) & tableMask
			for position > highThreshold {
				position = (position + step) & tableMask // lowprob area
			}
		}
	}
	if position != 0 {
		return ErrGeneric // position must reach all cells once, otherwise normalizedCounter is incorrect
	}

	// Build Decoding table
	for u := range td {
		symbol := td[u].symbol
		nextState := uint32(symbolNext[symbol])
		symbolNext[symbol]++
		nb := tableLog - (uint32(bits.Len32(nextState)) - 1)
		td[u].nbBits = byte(nb)
		td[u].newState = uint16(nextState<<nb - tableSize)
	}
	return nil
}

// dState. C: fse.h:FSE_DState_t
type dState struct {
	state uint64
	table []decodeEntry
}

// C: fse.h:FSE_initDState (Go: returns the state, so that dt.table does
// not escape through a pointer and may live on the caller's stack)
func initDState(bitD *bitstream.DStream, dt *dTable) dState {
	state := bitD.ReadBits(dt.tableLog)
	bitD.Reload()
	return dState{state: state, table: dt.table}
}

// C: fse.h:FSE_decodeSymbol (FSE_decodeSymbolFast reads the same bits)
func (s *dState) decodeSymbol(bitD *bitstream.DStream) byte {
	dInfo := s.table[s.state]
	lowBits := bitD.ReadBits(uint32(dInfo.nbBits))
	s.state = uint64(dInfo.newState) + lowBits
	return dInfo.symbol
}

// decompressUsingDTable. C: fse_decompress.c:FSE_decompress_usingDTable_generic
// (64-bit container: the static reload tests inside the 4-symbol loop,
// FSE_MAX_TABLELOG*2+7 > 64 and FSE_MAX_TABLELOG*4+7 > 64, are false).
func decompressUsingDTable(dst, src []byte, dt *dTable) (int, error) {
	op := 0
	omax := len(dst)
	olimit := omax - 3
	var bitD bitstream.DStream

	// Init
	if _, err := bitD.Init(src); err != nil {
		return 0, err
	}
	state1 := initDState(&bitD, dt)
	state2 := initDState(&bitD, dt)
	if bitD.Reload() == bitstream.DStreamOverflow {
		return 0, ErrCorruptionDetected
	}

	// 4 symbols per loop
	for ; bitD.Reload() == bitstream.DStreamUnfinished && op < olimit; op += 4 {
		dst[op] = state1.decodeSymbol(&bitD)
		dst[op+1] = state2.decodeSymbol(&bitD)
		dst[op+2] = state1.decodeSymbol(&bitD)
		dst[op+3] = state2.decodeSymbol(&bitD)
	}

	// tail
	// note : Reload() >= DStreamEndOfBuffer; Ends at exactly DStreamCompleted
	for {
		if op > omax-2 {
			return 0, ErrDstSizeTooSmall
		}
		dst[op] = state1.decodeSymbol(&bitD)
		op++
		if bitD.Reload() == bitstream.DStreamOverflow {
			dst[op] = state2.decodeSymbol(&bitD)
			op++
			break
		}

		if op > omax-2 {
			return 0, ErrDstSizeTooSmall
		}
		dst[op] = state2.decodeSymbol(&bitD)
		op++
		if bitD.Reload() == bitstream.DStreamOverflow {
			dst[op] = state1.decodeSymbol(&bitD)
			op++
			break
		}
	}
	return op, nil
}

// DecompressWkspSizeU32 is the workspace size, in U32, that
// FSE_decompress_wksp needs for a table of maxTableLog and maxSymbolValue.
// C: fse.h:FSE_DECOMPRESS_WKSP_SIZE_U32
func DecompressWkspSizeU32(maxTableLog, maxSymbolValue uint) uint {
	dtableSizeU32 := 1 + uint(1)<<maxTableLog                          // C: FSE_DTABLE_SIZE_U32
	buildDTableWksp := 2*(maxSymbolValue+1) + uint(1)<<maxTableLog + 8 // C: FSE_BUILD_DTABLE_WKSP_SIZE
	return dtableSizeU32 + 1 + (buildDTableWksp+3)/4 + (255+1)/2 + 1
}

// DecompressWksp decodes an FSE stream (NCount header || payload) of byte
// symbols into dst and returns the number of bytes written. maxLog is the
// largest table log accepted. wkspSizeU32 is the size of C's caller
// workspace: the workspace itself is internal here, but C rejects a table
// that would not fit in it, so the budget is checked as in C.
// C: fse_decompress.c:FSE_decompress_wksp_bmi2 (FSE_decompress_wksp_body)
func DecompressWksp(dst, src []byte, maxLog, wkspSizeU32 uint) (int, error) {
	var ncount [maxSymbolValue + 1]int16
	maxSV := uint(maxSymbolValue)

	// normal FSE decoding mode
	tableLog, nCountLength, err := ReadNCount(ncount[:], &maxSV, src)
	if err != nil {
		return 0, err
	}
	if tableLog > maxLog {
		return 0, ErrTableLogTooLarge
	}
	src = src[nCountLength:]

	if DecompressWkspSizeU32(tableLog, maxSV) > wkspSizeU32 {
		return 0, ErrTableLogTooLarge
	}

	// C builds the DTable in the caller's workspace, with no allocation.
	// tableBuf holds every table of the Huffman weight reader (maxLog
	// MaxTableLogForHuffHeader, HUF_readStats) on the stack; buildDTable allocates only larger ones.
	var tableBuf [1 << MaxTableLogForHuffHeader]decodeEntry
	dt := dTable{table: tableBuf[:0]}
	if err := buildDTable(&dt, ncount[:], uint32(maxSV), uint32(tableLog)); err != nil {
		return 0, err
	}
	return decompressUsingDTable(dst, src, &dt)
}
