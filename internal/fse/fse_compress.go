// Package fse ports the FSE (Finite State Entropy) encoder from libzstd v1.5.7
// lib/compress/fse_compress.c and the encoder-side inline helpers of
// lib/common/fse.h (Copyright (c) Meta Platforms, Inc. and affiliates;
// BSD/GPLv2 dual license).
package fse

import (
	"encoding/binary"
	"errors"
	"math/bits"

	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/hist"
)

// Table-size limits. C: fse.h (FSE_MAX_MEMORY_USAGE 14, FSE_DEFAULT_MEMORY_USAGE 13).
const (
	MinTableLog     = 5  // C: FSE_MIN_TABLELOG
	DefaultTableLog = 11 // C: FSE_DEFAULT_TABLELOG
	MaxTableLog     = 12 // C: FSE_MAX_TABLELOG
	nCountBound     = 512
)

// Errors, mirroring the C error codes returned at the same points.
var (
	ErrGeneric           = errors.New("fse: generic error")       // ERROR(GENERIC)
	ErrTableLogTooLarge  = errors.New("fse: tableLog too large")  // ERROR(tableLog_tooLarge)
	ErrDstSizeTooSmall   = errors.New("fse: dst size too small")  // ERROR(dstSize_tooSmall)
	ErrMaxSymbolTooSmall = errors.New("fse: maxSymbol too small") // ERROR(maxSymbolValue_tooSmall)
)

// CTable is C's FSE_CTable: a flat []uint32 laid out as
//
//	[0]                       header: low u16 = tableLog, high u16 = maxSymbol
//	[1 : 1+tableSize/2]       state table: tableSize u16 values, two per u32
//	                          (u16 i is the low half of word i/2 when i is even)
//	[1+tableSize/2 : ]        symbolTT: per symbol 2 u32 =
//	                          {deltaFindState (int32 bits), deltaNbBits}
//
// where tableSize = 1<<tableLog (for tableLog == 0, as built by
// BuildCTableRLE, the state table occupies one word). The u16 packing matches
// C on little-endian machines, which is what C's cast layout assumes.
// C: fse.h:FSE_CTable
type CTable []uint32

// CTableSizeU32 is the number of u32 words for a table of up to maxTableLog
// and maxSymbol. C: fse.h:FSE_CTABLE_SIZE_U32
func CTableSizeU32(maxTableLog, maxSymbol uint) int {
	return 1 + 1<<(maxTableLog-1) + int(maxSymbol+1)*2
}

// BuildCTableWorkspaceSize is the workspace size in bytes BuildCTable needs.
// C: fse.h:FSE_BUILD_CTABLE_WORKSPACE_SIZE
func BuildCTableWorkspaceSize(maxSymbol, tableLog uint) int {
	return 4 * ((int(maxSymbol)+2+1<<tableLog)/2 + 2)
}

// SymbolTT returns the symbol transformation table (2 words per symbol).
// C: fse.h:FSE_initCState (symbolTT = ct + 1 + (tableLog ? 1<<(tableLog-1) : 1))
func (ct CTable) SymbolTT() []uint32 {
	tl := ct[0] & 0xFFFF
	if tl == 0 {
		return ct[2:]
	}
	return ct[1+1<<(tl-1):]
}

func highbit32(v uint32) uint { return uint(bits.Len32(v)) - 1 } // C: bits.h:ZSTD_highbit32 (v != 0)

// BuildCTable builds the compression table from a normalized count. wksp must
// be at least BuildCTableWorkspaceSize bytes.
// C: fse_compress.c:FSE_buildCTable_wksp. Deviation: the cumul array is a
// local stack array instead of part of the workspace (cannot change output).
func BuildCTable(ct CTable, norm []int16, maxSymbol, tableLog uint, wksp []byte) error {
	tableSize := uint32(1) << tableLog
	tableMask := tableSize - 1
	step := tableSize>>1 + tableSize>>3 + 3 // C: FSE_TABLESTEP
	maxSV1 := maxSymbol + 1

	var cumul [258]uint16 // size maxSV1+1 <= 257
	tableSymbol := wksp
	highThreshold := tableSize - 1

	if BuildCTableWorkspaceSize(maxSymbol, tableLog) > len(wksp) {
		return ErrTableLogTooLarge
	}
	// header
	ct[0] = uint32(uint16(tableLog)) | uint32(uint16(maxSymbol))<<16
	stateTable := ct[1:]
	symbolTT := ct[1+tableSize>>1:]

	// symbol start positions
	for u := uint(1); u <= maxSV1; u++ {
		if norm[u-1] == -1 { // low proba symbol
			cumul[u] = cumul[u-1] + 1
			tableSymbol[highThreshold] = byte(u - 1)
			highThreshold--
		} else {
			cumul[u] = cumul[u-1] + uint16(norm[u-1])
		}
	}
	cumul[maxSV1] = uint16(tableSize + 1)

	// Spread symbols
	if highThreshold == tableSize-1 {
		// No low prob symbols: lay down 8 bytes at a time.
		spread := tableSymbol[tableSize:] // size tableSize+8
		const add = 0x0101010101010101
		pos := 0
		var sv uint64
		for s := uint(0); s < maxSV1; s, sv = s+1, sv+add {
			n := int(norm[s])
			binary.LittleEndian.PutUint64(spread[pos:], sv)
			for i := 8; i < n; i += 8 {
				binary.LittleEndian.PutUint64(spread[pos+i:], sv)
			}
			pos += n
		}
		position := uint32(0)
		for s := uint32(0); s < tableSize; s += 2 {
			for u := uint32(0); u < 2; u++ {
				tableSymbol[(position+u*step)&tableMask] = spread[s+u]
			}
			position = (position + 2*step) & tableMask
		}
	} else {
		position := uint32(0)
		for symbol := uint(0); symbol < maxSV1; symbol++ {
			freq := int(norm[symbol])
			for range freq {
				tableSymbol[position] = byte(symbol)
				position = (position + step) & tableMask
				for position > highThreshold {
					position = (position + step) & tableMask // low proba area
				}
			}
		}
	}

	// Build table: u16 sorted by symbol order, gives next state value.
	for u := uint32(0); u < tableSize; u++ {
		s := tableSymbol[u]
		i := cumul[s]
		cumul[s]++
		v := tableSize + u
		if i&1 == 0 {
			stateTable[i>>1] = stateTable[i>>1]&0xFFFF0000 | v
		} else {
			stateTable[i>>1] = stateTable[i>>1]&0x0000FFFF | v<<16
		}
	}

	// Build symbol transformation table. Entry s: [2s]=deltaFindState, [2s+1]=deltaNbBits.
	total := uint32(0)
	for s := uint(0); s <= maxSymbol; s++ {
		switch n := norm[s]; n {
		case 0:
			// filling nonetheless, for compatibility with GetMaxNbBits
			symbolTT[2*s+1] = uint32(tableLog+1)<<16 - 1<<tableLog
		case -1, 1:
			symbolTT[2*s+1] = uint32(tableLog)<<16 - 1<<tableLog
			symbolTT[2*s] = total - 1
			total++
		default:
			maxBitsOut := uint32(tableLog) - uint32(highbit32(uint32(n)-1))
			minStatePlus := uint32(n) << maxBitsOut
			symbolTT[2*s+1] = maxBitsOut<<16 - minStatePlus
			symbolTT[2*s] = total - uint32(n)
			total += uint32(n)
		}
	}
	return nil
}

// NCountWriteBound is the worst-case header size.
// C: fse_compress.c:FSE_NCountWriteBound
func NCountWriteBound(maxSymbol, tableLog uint) int {
	if maxSymbol == 0 {
		return nCountBound
	}
	return int((maxSymbol+1)*tableLog+4+2)/8 + 1 + 2
}

// C: fse_compress.c:FSE_writeNCount_generic
func writeNCountGeneric(dst []byte, norm []int16, maxSymbol, tableLog uint, writeIsSafe bool) (int, error) {
	out := 0
	oend := len(dst)
	tableSize := 1 << tableLog
	var bitStream uint32
	bitCount := 0
	symbol := uint(0)
	alphabetSize := maxSymbol + 1
	previousIs0 := false

	// Table Size
	bitStream += uint32(tableLog-MinTableLog) << bitCount
	bitCount += 4

	remaining := tableSize + 1 // +1 for extra accuracy
	threshold := tableSize
	nbBits := int(tableLog) + 1

	for symbol < alphabetSize && remaining > 1 { // stops at 1
		if previousIs0 {
			start := symbol
			for symbol < alphabetSize && norm[symbol] == 0 {
				symbol++
			}
			if symbol == alphabetSize {
				break // incorrect distribution
			}
			for symbol >= start+24 {
				start += 24
				bitStream += 0xFFFF << bitCount
				if !writeIsSafe && out > oend-2 {
					return 0, ErrDstSizeTooSmall
				}
				dst[out] = byte(bitStream)
				dst[out+1] = byte(bitStream >> 8)
				out += 2
				bitStream >>= 16
			}
			for symbol >= start+3 {
				start += 3
				bitStream += 3 << bitCount
				bitCount += 2
			}
			bitStream += uint32(symbol-start) << bitCount
			bitCount += 2
			if bitCount > 16 {
				if !writeIsSafe && out > oend-2 {
					return 0, ErrDstSizeTooSmall
				}
				dst[out] = byte(bitStream)
				dst[out+1] = byte(bitStream >> 8)
				out += 2
				bitStream >>= 16
				bitCount -= 16
			}
		}
		{
			count := int(norm[symbol])
			symbol++
			max := (2*threshold - 1) - remaining
			if count < 0 {
				remaining -= -count
			} else {
				remaining -= count
			}
			count++ // +1 for extra accuracy
			if count >= threshold {
				count += max // [0..max[ [max..threshold[ (...) [threshold+max 2*threshold[
			}
			bitStream += uint32(count) << bitCount
			bitCount += nbBits
			if count < max {
				bitCount--
			}
			previousIs0 = count == 1
			if remaining < 1 {
				return 0, ErrGeneric
			}
			for remaining < threshold {
				nbBits--
				threshold >>= 1
			}
		}
		if bitCount > 16 {
			if !writeIsSafe && out > oend-2 {
				return 0, ErrDstSizeTooSmall
			}
			dst[out] = byte(bitStream)
			dst[out+1] = byte(bitStream >> 8)
			out += 2
			bitStream >>= 16
			bitCount -= 16
		}
	}

	if remaining != 1 {
		return 0, ErrGeneric // incorrect normalized distribution
	}

	// flush remaining bitStream
	if !writeIsSafe && out > oend-2 {
		return 0, ErrDstSizeTooSmall
	}
	dst[out] = byte(bitStream)
	dst[out+1] = byte(bitStream >> 8)
	out += (bitCount + 7) / 8
	return out, nil
}

// WriteNCount writes the normalized-count header to dst and returns its size.
// C: fse_compress.c:FSE_writeNCount
func WriteNCount(dst []byte, norm []int16, maxSymbol, tableLog uint) (int, error) {
	if tableLog > MaxTableLog {
		return 0, ErrTableLogTooLarge
	}
	if tableLog < MinTableLog {
		return 0, ErrGeneric
	}
	return writeNCountGeneric(dst, norm, maxSymbol, tableLog, len(dst) >= NCountWriteBound(maxSymbol, tableLog))
}

// minTableLog is the minimum tableLog that safely represents a distribution.
// C: fse_compress.c:FSE_minTableLog. srcSize must be > 1.
func minTableLog(srcSize int, maxSymbol uint) uint {
	minBitsSrc := highbit32(uint32(srcSize)) + 1
	minBitsSymbols := highbit32(uint32(maxSymbol)) + 2
	return min(minBitsSrc, minBitsSymbols)
}

// OptimalTableLogInternal. srcSize must be > 1 (RLE otherwise).
// C: fse_compress.c:FSE_optimalTableLog_internal
func OptimalTableLogInternal(maxTableLog uint, srcSize int, maxSymbol, minus uint) uint {
	maxBitsSrc := highbit32(uint32(srcSize-1)) - minus
	tableLog := maxTableLog
	minBits := minTableLog(srcSize, maxSymbol)
	if tableLog == 0 {
		tableLog = DefaultTableLog
	}
	if maxBitsSrc < tableLog {
		tableLog = maxBitsSrc // accuracy can be reduced
	}
	if minBits > tableLog {
		tableLog = minBits // need a minimum to safely represent all symbols
	}
	tableLog = max(tableLog, MinTableLog)
	tableLog = min(tableLog, MaxTableLog)
	return tableLog
}

// OptimalTableLog. C: fse_compress.c:FSE_optimalTableLog
func OptimalTableLog(maxTableLog uint, srcSize int, maxSymbol uint) uint {
	return OptimalTableLogInternal(maxTableLog, srcSize, maxSymbol, 2)
}

// normalizeM2 is the secondary normalization method, used when the primary
// one fails. C: fse_compress.c:FSE_normalizeM2
func normalizeM2(norm []int16, tableLog uint, count []uint32, total uint64, maxSymbol uint, lowProbCount int16) error {
	const notYetAssigned = int16(-2)
	var distributed uint32

	lowThreshold := uint32(total >> tableLog)
	lowOne := uint32((total * 3) >> (tableLog + 1))

	for s := uint(0); s <= maxSymbol; s++ {
		if count[s] == 0 {
			norm[s] = 0
			continue
		}
		if count[s] <= lowThreshold {
			norm[s] = lowProbCount
			distributed++
			total -= uint64(count[s])
			continue
		}
		if count[s] <= lowOne {
			norm[s] = 1
			distributed++
			total -= uint64(count[s])
			continue
		}
		norm[s] = notYetAssigned
	}
	toDistribute := uint32(1)<<tableLog - distributed

	if toDistribute == 0 {
		return nil
	}

	if total/uint64(toDistribute) > uint64(lowOne) {
		// risk of rounding to zero
		lowOne = uint32((total * 3) / uint64(toDistribute*2))
		for s := uint(0); s <= maxSymbol; s++ {
			if norm[s] == notYetAssigned && count[s] <= lowOne {
				norm[s] = 1
				distributed++
				total -= uint64(count[s])
			}
		}
		toDistribute = uint32(1)<<tableLog - distributed
	}

	if distributed == uint32(maxSymbol)+1 {
		// all values are pretty poor; probably incompressible data. Find max,
		// then give all remaining points to max.
		var maxV uint
		var maxC uint32
		for s := uint(0); s <= maxSymbol; s++ {
			if count[s] > maxC {
				maxV, maxC = s, count[s]
			}
		}
		norm[maxV] += int16(toDistribute)
		return nil
	}

	if total == 0 {
		// all of the symbols were low enough for the lowOne or lowThreshold
		for s := uint(0); toDistribute > 0; s = (s + 1) % (maxSymbol + 1) {
			if norm[s] > 0 {
				toDistribute--
				norm[s]++
			}
		}
		return nil
	}

	vStepLog := 62 - uint64(tableLog)
	mid := uint64(1)<<(vStepLog-1) - 1
	rStep := (uint64(1)<<vStepLog*uint64(toDistribute) + mid) / uint64(uint32(total)) // scale on remaining
	tmpTotal := mid
	for s := uint(0); s <= maxSymbol; s++ {
		if norm[s] == notYetAssigned {
			end := tmpTotal + uint64(count[s])*rStep
			sStart := uint32(tmpTotal >> vStepLog)
			sEnd := uint32(end >> vStepLog)
			weight := sEnd - sStart
			if weight < 1 {
				return ErrGeneric
			}
			norm[s] = int16(weight)
			tmpTotal = end
		}
	}
	return nil
}

// NormalizeCount normalizes count (total = sum of counts) into norm so that
// the magnitudes sum to 1<<tableLog. It returns the tableLog actually used,
// or 0 for the RLE special case (one symbol holds every count).
// C: fse_compress.c:FSE_normalizeCount
func NormalizeCount(norm []int16, tableLog uint, count []uint32, total int, maxSymbol uint, useLowProbCount bool) (uint, error) {
	if tableLog == 0 {
		tableLog = DefaultTableLog
	}
	if tableLog < MinTableLog {
		return 0, ErrGeneric
	}
	if tableLog > MaxTableLog {
		return 0, ErrTableLogTooLarge
	}
	if tableLog < minTableLog(total, maxSymbol) {
		return 0, ErrGeneric // too small tableLog, compression potentially impossible
	}

	rtbTable := [...]uint32{0, 473195, 504333, 520860, 550000, 700000, 750000, 830000}
	lowProbCount := int16(1)
	if useLowProbCount {
		lowProbCount = -1
	}
	scale := uint64(62 - tableLog)
	step := (uint64(1) << 62) / uint64(uint32(total)) // one division
	vStep := uint64(1) << (scale - 20)
	stillToDistribute := 1 << tableLog
	var largest uint
	var largestP int16
	lowThreshold := uint32(total >> tableLog)

	for s := uint(0); s <= maxSymbol; s++ {
		if count[s] == uint32(total) {
			return 0, nil // rle special case
		}
		if count[s] == 0 {
			norm[s] = 0
			continue
		}
		if count[s] <= lowThreshold {
			norm[s] = lowProbCount
			stillToDistribute--
		} else {
			proba := int16((uint64(count[s]) * step) >> scale)
			if proba < 8 {
				restToBeat := vStep * uint64(rtbTable[proba])
				if uint64(count[s])*step-uint64(proba)<<scale > restToBeat {
					proba++
				}
			}
			if proba > largestP {
				largestP, largest = proba, s
			}
			norm[s] = proba
			stillToDistribute -= int(proba)
		}
	}
	if -stillToDistribute >= int(norm[largest]>>1) {
		// corner case, need another normalization method
		if err := normalizeM2(norm, tableLog, count, uint64(total), maxSymbol, lowProbCount); err != nil {
			return 0, err
		}
	} else {
		norm[largest] += int16(stillToDistribute)
	}
	return tableLog, nil
}

// BuildCTableRLE builds a fake table that always encodes symbol. ct needs
// CTableSizeU32(0, symbol) words: header, one state word, then symbolTT.
// C: fse_compress.c:FSE_buildCTable_rle
func BuildCTableRLE(ct CTable, symbol byte) {
	ct[0] = uint32(symbol) << 16 // tableLog 0, maxSymbol = symbol
	ct[1] = 0                    // state table u16[0], u16[1]
	ct[2+2*int(symbol)] = 0      // deltaFindState
	ct[3+2*int(symbol)] = 0      // deltaNbBits
}

// CState is the FSE encoder state. C: fse.h:FSE_CState_t. stateTable is
// ct[1:] (two u16 per word), symbolTT is ct.SymbolTT().
type CState struct {
	value      uint32 // C: ptrdiff_t; a state fits 32 bits
	stateTable []uint32
	symbolTT   []uint32
	stateLog   uint
}

// InitCState. C: fse.h:FSE_initCState
func (s *CState) InitCState(ct CTable) {
	tableLog := uint(ct[0] & 0xFFFF)
	s.value = 1 << tableLog
	s.stateTable = ct[1:]
	s.symbolTT = ct.SymbolTT()
	s.stateLog = tableLog
}

// Value returns the state value, for an assembly encoding loop.
func (s *CState) Value() uint32 { return s.value }

// SetValue sets the state value an assembly loop returned.
func (s *CState) SetValue(v uint32) { s.value = v }

func stateAt(t []uint32, i uint32) uint32 { return t[i>>1] >> ((i & 1) * 16) & 0xFFFF }

// InitCState2 is InitCState but the first symbol to include (the last to be
// read) uses the smallest state value possible, saving its cost.
// C: fse.h:FSE_initCState2
func (s *CState) InitCState2(ct CTable, symbol uint32) {
	s.InitCState(ct)
	deltaFindState := int32(s.symbolTT[2*symbol])
	deltaNbBits := s.symbolTT[2*symbol+1]
	nbBitsOut := (deltaNbBits + 1<<15) >> 16
	s.value = nbBitsOut<<16 - deltaNbBits
	s.value = stateAt(s.stateTable, s.value>>nbBitsOut+uint32(deltaFindState))
}

// EncodeSymbol. C: fse.h:FSE_encodeSymbol
func EncodeSymbol(bs *bitstream.CStream, s *CState, sym uint32) {
	tt := s.symbolTT[2*sym : 2*sym+2]
	nbBitsOut := (s.value + tt[1]) >> 16
	bs.AddBits(uint64(s.value), uint(nbBitsOut))
	s.value = stateAt(s.stateTable, s.value>>nbBitsOut+tt[0])
}

// FlushCState. C: fse.h:FSE_flushCState
func FlushCState(bs *bitstream.CStream, s *CState) {
	bs.AddBits(uint64(s.value), s.stateLog)
	bs.FlushBits()
}

// GetMaxNbBits is the approximate maximum cost of a symbol in bits (a symbol
// with frequency 0 costs a fake tableLog+1). symbolTT is ct.SymbolTT().
// C: fse.h:FSE_getMaxNbBits
func GetMaxNbBits(symbolTT []uint32, symbol uint32) uint32 {
	return (symbolTT[2*symbol+1] + (1<<16 - 1)) >> 16
}

// BitCost is the approximate symbol cost as a fixed-point value with
// accuracyLog fractional bits. C: fse.h:FSE_bitCost
func BitCost(symbolTT []uint32, tableLog, symbol, accuracyLog uint32) uint32 {
	deltaNbBits := symbolTT[2*symbol+1]
	minNbBits := deltaNbBits >> 16
	threshold := (minNbBits + 1) << 16
	tableSize := uint32(1) << tableLog
	deltaFromThreshold := threshold - (deltaNbBits + tableSize)
	normalizedDeltaFromThreshold := (deltaFromThreshold << accuracyLog) >> tableLog // linear interpolation (very approximate)
	bitMultiplier := uint32(1) << accuracyLog
	return (minNbBits+1)*bitMultiplier - normalizedDeltaFromThreshold
}

// C: fse_compress.c:FSE_compress_usingCTable_generic
func compressUsingCTableGeneric(dst, src []byte, ct CTable, fast bool) int {
	ip := len(src)
	var bitC bitstream.CStream
	var c1, c2 CState

	if len(src) <= 2 {
		return 0
	}
	if bitC.Init(dst) != nil {
		return 0 // not enough space available to write a bitstream
	}
	flush := func() {
		if fast {
			bitC.FlushBitsFast()
		} else {
			bitC.FlushBits()
		}
	}

	if len(src)&1 != 0 {
		ip--
		c1.InitCState2(ct, uint32(src[ip]))
		ip--
		c2.InitCState2(ct, uint32(src[ip]))
		ip--
		EncodeSymbol(&bitC, &c1, uint32(src[ip]))
		flush()
	} else {
		ip--
		c2.InitCState2(ct, uint32(src[ip]))
		ip--
		c1.InitCState2(ct, uint32(src[ip]))
	}

	// join to mod 4 (64-bit container: 64 > MaxTableLog*4+7)
	if (len(src)-2)&2 != 0 {
		ip--
		EncodeSymbol(&bitC, &c2, uint32(src[ip]))
		ip--
		EncodeSymbol(&bitC, &c1, uint32(src[ip]))
		flush()
	}

	// 4 encodings per loop
	for ip > 0 {
		ip--
		EncodeSymbol(&bitC, &c2, uint32(src[ip]))
		ip--
		EncodeSymbol(&bitC, &c1, uint32(src[ip]))
		ip--
		EncodeSymbol(&bitC, &c2, uint32(src[ip]))
		ip--
		EncodeSymbol(&bitC, &c1, uint32(src[ip]))
		flush()
	}

	FlushCState(&bitC, &c2)
	FlushCState(&bitC, &c1)
	return bitC.Close()
}

// CompressUsingCTable encodes src with ct into dst. It returns 0 (and no
// error) when dst is too small, as C does.
// C: fse_compress.c:FSE_compress_usingCTable
func CompressUsingCTable(dst, src []byte, ct CTable) (int, error) {
	fast := len(dst) >= len(src)+len(src)>>7+4+8 // C: FSE_BLOCKBOUND
	return compressUsingCTableGeneric(dst, src, ct, fast), nil
}

// Compress2 is a TEST ORACLE WRAPPER, not part of the zstd compressor. C 1.5.7
// has no FSE_compress2; this follows the re-assembly documented in the header
// of ref/cref/cref.c (mode "fse"), which mirrors the removed
// FSE_compress_wksp. The zstd compressor (ZSTD_buildCTable, Task 7) uses the
// building blocks above directly, not this wrapper's heuristics.
//
// It returns 0 when src is not compressible, 1 for RLE (dst[0] = the symbol),
// otherwise n = NCount header || payload. tableLog 0 means DefaultTableLog.
func Compress2(dst, src []byte, maxSymbol, tableLog uint) (int, error) {
	if len(src) <= 1 {
		return 0, nil
	}
	if tableLog == 0 {
		tableLog = DefaultTableLog
	}
	var count [256]uint32
	var norm [256]int16
	ms := uint32(maxSymbol)
	maxCount := hist.Count(&count, &ms, src)
	if maxCount == hist.MaxSymbolTooSmall {
		return 0, ErrMaxSymbolTooSmall
	}
	maxSymbol = uint(ms)
	if int(maxCount) == len(src) {
		dst[0] = src[0]
		return 1, nil
	}
	if maxCount == 1 {
		return 0, nil
	}
	if int(maxCount) < len(src)>>7 {
		return 0, nil
	}
	tableLog = OptimalTableLog(tableLog, len(src), maxSymbol)
	if _, err := NormalizeCount(norm[:], tableLog, count[:], len(src), maxSymbol, len(src) >= 2048); err != nil {
		return 0, err
	}
	nc, err := WriteNCount(dst, norm[:], maxSymbol, tableLog)
	if err != nil {
		return 0, err
	}
	ct := make(CTable, CTableSizeU32(MaxTableLog, 255))
	wksp := make([]byte, BuildCTableWorkspaceSize(255, MaxTableLog))
	if err := BuildCTable(ct, norm[:], maxSymbol, tableLog, wksp); err != nil {
		return 0, err
	}
	cs, err := CompressUsingCTable(dst[nc:], src, ct)
	if err != nil {
		return 0, err
	}
	if cs == 0 || nc+cs >= len(src)-1 {
		return 0, nil
	}
	return nc + cs, nil
}
