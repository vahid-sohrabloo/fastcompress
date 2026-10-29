// Port of libzstd v1.5.7 lib/compress/zstd_compress_sequences.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license).

package zstd

import (
	"errors"

	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
)

// errCost stands for a C size_t error code used as a cost: it is
// ERROR(GENERIC) == (size_t)-1, larger than any real cost.
const errCost = ^uint64(0)

// kInverseProbabilityLog256 is -log2(x / 256) for x in [0, 256): 0 if
// x == 0, else floor(-log2(x / 256) * 256).
// C: zstd_compress_sequences.c:kInverseProbabilityLog256
var kInverseProbabilityLog256 = [256]uint32{
	0, 2048, 1792, 1642, 1536, 1453, 1386, 1329, 1280, 1236, 1197, 1162,
	1130, 1100, 1073, 1047, 1024, 1001, 980, 960, 941, 923, 906, 889,
	874, 859, 844, 830, 817, 804, 791, 779, 768, 756, 745, 734,
	724, 714, 704, 694, 685, 676, 667, 658, 650, 642, 633, 626,
	618, 610, 603, 595, 588, 581, 574, 567, 561, 554, 548, 542,
	535, 529, 523, 517, 512, 506, 500, 495, 489, 484, 478, 473,
	468, 463, 458, 453, 448, 443, 438, 434, 429, 424, 420, 415,
	411, 407, 402, 398, 394, 390, 386, 382, 377, 373, 370, 366,
	362, 358, 354, 350, 347, 343, 339, 336, 332, 329, 325, 322,
	318, 315, 311, 308, 305, 302, 298, 295, 292, 289, 286, 282,
	279, 276, 273, 270, 267, 264, 261, 258, 256, 253, 250, 247,
	244, 241, 239, 236, 233, 230, 228, 225, 222, 220, 217, 215,
	212, 209, 207, 204, 202, 199, 197, 194, 192, 190, 187, 185,
	182, 180, 178, 175, 173, 171, 168, 166, 164, 162, 159, 157,
	155, 153, 151, 149, 146, 144, 142, 140, 138, 136, 134, 132,
	130, 128, 126, 123, 121, 119, 117, 115, 114, 112, 110, 108,
	106, 104, 102, 100, 98, 96, 94, 93, 91, 89, 87, 85,
	83, 82, 80, 78, 76, 74, 73, 71, 69, 67, 66, 64,
	62, 61, 59, 57, 55, 54, 52, 50, 49, 47, 46, 44,
	42, 41, 39, 37, 36, 34, 33, 31, 30, 28, 26, 25,
	23, 22, 20, 19, 17, 16, 14, 13, 11, 10, 8, 7,
	5, 4, 2, 1,
}

// C: zstd_compress_sequences.c:ZSTD_getFSEMaxSymbolValue
func getFSEMaxSymbolValue(ct fse.CTable) uint32 { return ct[0] >> 16 }

// useLowProbCount reports whether low-probability symbols get ncount -1
// (else 1). C: zstd_compress_sequences.c:ZSTD_useLowProbCount
func useLowProbCount(nbSeq int) bool {
	// Heuristic: This should cover most blocks <= 16K and start to fade out
	// after 16K to about 32K depending on compressibility.
	return nbSeq >= 2048
}

// nCountCost is the cost in bytes of the normalized count header.
// C: zstd_compress_sequences.c:ZSTD_NCountCost
func nCountCost(count []uint32, max uint32, nbSeq int, fseLog uint32) (int, error) {
	var wksp [512]byte // C: FSE_NCOUNTBOUND
	var norm [maxSeq + 1]int16
	tableLog := fse.OptimalTableLog(uint(fseLog), nbSeq, uint(max))
	if _, err := fse.NormalizeCount(norm[:], tableLog, count, nbSeq, uint(max), useLowProbCount(nbSeq)); err != nil {
		return 0, err
	}
	return fse.WriteNCount(wksp[:], norm[:], uint(max), tableLog)
}

// entropyCost is the cost in bits of count using the entropy bound.
// C: zstd_compress_sequences.c:ZSTD_entropyCost
func entropyCost(count []uint32, max uint32, total int) uint32 {
	var cost uint32
	for s := uint32(0); s <= max; s++ {
		norm := 256 * count[s] / uint32(total)
		if count[s] != 0 && norm == 0 {
			norm = 1
		}
		cost += count[s] * kInverseProbabilityLog256[norm]
	}
	return cost >> 8
}

// fseBitCost is the cost in bits of count using ct, or errCost if ct cannot
// represent every symbol in count. C: zstd_compress_sequences.c:ZSTD_fseBitCost
func fseBitCost(ct fse.CTable, count []uint32, max uint32) uint64 {
	const kAccuracyLog = 8
	var cost uint64
	tableLog := ct[0] & 0xFFFF // C: FSE_initCState's stateLog
	symbolTT := ct.SymbolTT()
	if getFSEMaxSymbolValue(ct) < max {
		return errCost
	}
	for s := uint32(0); s <= max; s++ {
		badCost := (tableLog + 1) << kAccuracyLog
		bitCost := fse.BitCost(symbolTT, tableLog, s, kAccuracyLog)
		if count[s] == 0 {
			continue
		}
		if bitCost >= badCost {
			return errCost
		}
		cost += uint64(count[s]) * uint64(bitCost)
	}
	return cost >> kAccuracyLog
}

// crossEntropyCost is the cost in bits of count using the table described by
// norm. C: zstd_compress_sequences.c:ZSTD_crossEntropyCost
func crossEntropyCost(norm []int16, accuracyLog uint32, count []uint32, max uint32) uint64 {
	shift := 8 - accuracyLog
	var cost uint64
	for s := uint32(0); s <= max; s++ {
		normAcc := uint32(1)
		if norm[s] != -1 {
			normAcc = uint32(norm[s])
		}
		norm256 := normAcc << shift
		cost += uint64(count[s]) * uint64(kInverseProbabilityLog256[norm256])
	}
	return cost >> 8
}

// defaultPolicy. C: zstd_compress_sequences.h:ZSTD_DefaultPolicy_e
type defaultPolicy int

const (
	defaultDisallowed defaultPolicy = 0
	defaultAllowed    defaultPolicy = 1
)

// selectEncodingType chooses the encoding mode of one sequence code table
// and updates *repeatMode. C: zstd_compress_sequences.c:ZSTD_selectEncodingType
func selectEncodingType(repeatMode *fseRepeat, count []uint32, max uint32, mostFrequent, nbSeq int,
	fseLog uint32, prevCTable fse.CTable, defaultNorm []int16, defaultNormLog uint32,
	isDefaultAllowed defaultPolicy, strat strategy) symbolEncodingType {
	if mostFrequent == nbSeq {
		*repeatMode = fseRepeatNone
		if isDefaultAllowed != defaultDisallowed && nbSeq <= 2 {
			// Prefer set_basic over set_rle when there are 2 or fewer
			// symbols, since RLE uses 1 byte, but set_basic uses 5-6 bits per
			// symbol. If basic encoding isn't possible, always choose RLE.
			return setBasic
		}
		return setRLE
	}
	if strat < zstdLazy {
		if isDefaultAllowed != defaultDisallowed {
			const staticFseNbSeqMax = 1000
			mult := 10 - int(strat)
			const baseLog = 3
			dynamicFseNbSeqMin := ((1 << defaultNormLog) * mult) >> baseLog // 28-36 for offset, 56-72 for lengths
			if *repeatMode == fseRepeatValid && nbSeq < staticFseNbSeqMax {
				return setRepeat
			}
			if nbSeq < dynamicFseNbSeqMin || mostFrequent < nbSeq>>(defaultNormLog-1) {
				// The format allows default tables to be repeated, but it
				// isn't useful. When using simple heuristics to select
				// encoding type, we don't want to confuse these tables with
				// dictionaries. When running more careful analysis, we don't
				// need to waste time checking both repeating tables and
				// default tables.
				*repeatMode = fseRepeatNone
				return setBasic
			}
		}
	} else {
		basicCost := errCost
		if isDefaultAllowed != defaultDisallowed {
			basicCost = crossEntropyCost(defaultNorm, defaultNormLog, count, max)
		}
		repeatCost := errCost
		if *repeatMode != fseRepeatNone {
			repeatCost = fseBitCost(prevCTable, count, max)
		}
		compressedCost := errCost
		if ncc, err := nCountCost(count, max, nbSeq, fseLog); err == nil {
			compressedCost = uint64(ncc)<<3 + uint64(entropyCost(count, max, nbSeq))
		}
		if basicCost <= repeatCost && basicCost <= compressedCost {
			*repeatMode = fseRepeatNone
			return setBasic
		}
		if repeatCost <= compressedCost {
			return setRepeat
		}
	}
	*repeatMode = fseRepeatCheck
	return setCompressed
}

// fseErr maps fse errors to the zstd error with the same C error code.
func fseErr(err error) error {
	if errors.Is(err, fse.ErrDstSizeTooSmall) {
		return errDstSizeTooSmall
	}
	return err
}

// buildCTable builds nextCTable for the chosen mode, writes the table
// description (if any) to dst and returns its size. count may be modified.
// wksp must be at least fse.BuildCTableWorkspaceSize(maxSeq, maxFSELog)
// bytes. C: zstd_compress_sequences.c:ZSTD_buildCTable (prevCTableSize is
// len(prevCTable)). Deviation: the normalized counts (C: the norm half of
// ZSTD_BuildCTableWksp) are a stack array; this cannot change output.
func buildCTable(dst []byte, nextCTable fse.CTable, fseLog uint32, typ symbolEncodingType,
	count []uint32, max uint32, codeTable []byte, nbSeq int,
	defaultNorm []int16, defaultNormLog, defaultMax uint32,
	prevCTable fse.CTable, wksp []byte) (int, error) {
	switch typ {
	case setRLE:
		fse.BuildCTableRLE(nextCTable, byte(max))
		if len(dst) == 0 {
			return 0, errDstSizeTooSmall // not enough space
		}
		dst[0] = codeTable[0]
		return 1, nil
	case setRepeat:
		copy(nextCTable, prevCTable)
		return 0, nil
	case setBasic:
		// note : could be pre-calculated
		if err := fse.BuildCTable(nextCTable, defaultNorm, uint(defaultMax), uint(defaultNormLog), wksp); err != nil {
			return 0, fseErr(err)
		}
		return 0, nil
	case setCompressed:
		var norm [maxSeq + 1]int16
		nbSeq1 := nbSeq
		tableLog := fse.OptimalTableLog(uint(fseLog), nbSeq, uint(max))
		if count[codeTable[nbSeq-1]] > 1 {
			count[codeTable[nbSeq-1]]--
			nbSeq1--
		}
		if _, err := fse.NormalizeCount(norm[:], tableLog, count, nbSeq1, uint(max), useLowProbCount(nbSeq1)); err != nil {
			return 0, fseErr(err)
		}
		nCountSize, err := fse.WriteNCount(dst, norm[:], uint(max), tableLog) // overflow protected
		if err != nil {
			return 0, fseErr(err)
		}
		if err := fse.BuildCTable(nextCTable, norm[:], uint(max), tableLog, wksp); err != nil {
			return 0, fseErr(err)
		}
		return nCountSize, nil
	}
	return 0, errGeneric // impossible to reach
}
