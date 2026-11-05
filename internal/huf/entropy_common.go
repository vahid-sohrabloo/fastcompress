// Port of the Huffman part of libzstd v1.5.7 lib/common/entropy_common.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual
// license): the reader of a Huffman tree description.

package huf

import (
	"errors"
	"math/bits"

	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
)

// ErrCorruptionDetected. C: ERROR(corruption_detected)
var ErrCorruptionDetected = errors.New("huf: corruption detected")

// ReadStats reads a compact Huffman tree description, as saved by
// WriteCTable, into huffWeight (hwSize = len(huffWeight) entries) and
// rankStats (at least TableLogMax+1 entries). It returns the number of
// symbols, the table log and the number of bytes read from src.
// C: entropy_common.c:HUF_readStats (HUF_readStats_body; the bmi2 variant
// computes the same, and the workspace is internal to fse.DecompressWksp)
func ReadStats(huffWeight []byte, rankStats []uint32, src []byte) (nbSymbols, tableLog uint32, size int, err error) {
	hwSize := len(huffWeight)
	var oSize int

	if len(src) == 0 {
		return 0, 0, 0, ErrSrcSizeWrong
	}
	iSize := int(src[0])

	if iSize >= 128 { // special header
		oSize = iSize - 127
		iSize = (oSize + 1) / 2
		if iSize+1 > len(src) {
			return 0, 0, 0, ErrSrcSizeWrong
		}
		if oSize >= hwSize {
			return 0, 0, 0, ErrCorruptionDetected
		}
		ip := src[1:]
		for n := 0; n < oSize; n += 2 {
			huffWeight[n] = ip[n/2] >> 4
			huffWeight[n+1] = ip[n/2] & 15
		}
	} else { // header compressed with FSE (normal case)
		if iSize+1 > len(src) {
			return 0, 0, 0, ErrSrcSizeWrong
		}
		// max (hwSize-1) values decoded, as last one is implied
		// C: the workspace is HUF_READ_STATS_WORKSPACE_SIZE_U32 =
		// FSE_DECOMPRESS_WKSP_SIZE_U32(6, HUF_TABLELOG_MAX-1)
		oSize, err = fse.DecompressWksp(huffWeight[:hwSize-1], src[1:1+iSize], maxFSETableLogForHuffHeader, fse.DecompressWkspSizeU32(maxFSETableLogForHuffHeader, TableLogMax-1))
		if err != nil {
			return 0, 0, 0, err
		}
	}

	// collect weight stats
	clear(rankStats[:TableLogMax+1])
	var weightTotal uint32
	for n := 0; n < oSize; n++ {
		if huffWeight[n] > TableLogMax {
			return 0, 0, 0, ErrCorruptionDetected
		}
		rankStats[huffWeight[n]]++
		weightTotal += (1 << huffWeight[n]) >> 1
	}
	if weightTotal == 0 {
		return 0, 0, 0, ErrCorruptionDetected
	}

	// get last non-null symbol weight (implied, total must be 2^n)
	tableLog = uint32(bits.Len32(weightTotal)) // C: ZSTD_highbit32(weightTotal) + 1
	if tableLog > TableLogMax {
		return 0, 0, 0, ErrCorruptionDetected
	}
	// determine last weight
	total := uint32(1) << tableLog
	rest := total - weightTotal
	verif := uint32(1) << highbit32(rest)
	lastWeight := highbit32(rest) + 1
	if verif != rest {
		return 0, 0, 0, ErrCorruptionDetected // last value must be a clean power of 2
	}
	huffWeight[oSize] = byte(lastWeight)
	rankStats[lastWeight]++

	// check tree construction validity
	if rankStats[1] < 2 || rankStats[1]&1 != 0 {
		return 0, 0, 0, ErrCorruptionDetected // by construction : at least 2 elts of rank 1, must be even
	}

	// results
	return uint32(oSize + 1), tableLog, iSize + 1, nil
}
