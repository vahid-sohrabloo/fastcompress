// Port of libzstd v1.5.7 lib/compress/zstd_compress_literals.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license).

package zstd

import (
	"encoding/binary"

	"github.com/vahid-sohrabloo/fastcompress/internal/huf"
)

// noCompressLiterals writes a raw literals section.
// C: zstd_compress_literals.c:ZSTD_noCompressLiterals
func noCompressLiterals(dst, src []byte) (int, error) {
	srcSize := len(src)
	flSize := 1 + b2i(srcSize > 31) + b2i(srcSize > 4095)
	if srcSize+flSize > len(dst) {
		return 0, errDstSizeTooSmall
	}
	switch flSize {
	case 1: // 2 - 1 - 5
		dst[0] = byte(uint32(setBasic) + uint32(srcSize)<<3)
	case 2: // 2 - 2 - 12
		binary.LittleEndian.PutUint16(dst, uint16(uint32(setBasic)+1<<2+uint32(srcSize)<<4))
	case 3: // 2 - 2 - 20
		binary.LittleEndian.PutUint32(dst, uint32(setBasic)+3<<2+uint32(srcSize)<<4)
	}
	copy(dst[flSize:], src)
	return srcSize + flSize, nil
}

// C: zstd_compress_literals.c:allBytesIdentical (len(src) >= 1)
func allBytesIdentical(src []byte) bool {
	b := src[0]
	for _, c := range src[1:] {
		if c != b {
			return false
		}
	}
	return true
}

// compressRleLiteralsBlock writes an RLE literals section. len(dst) >= 4 and
// every byte of src is identical.
// C: zstd_compress_literals.c:ZSTD_compressRleLiteralsBlock
func compressRleLiteralsBlock(dst, src []byte) int {
	srcSize := len(src)
	flSize := 1 + b2i(srcSize > 31) + b2i(srcSize > 4095)
	switch flSize {
	case 1: // 2 - 1 - 5
		dst[0] = byte(uint32(setRLE) + uint32(srcSize)<<3)
	case 2: // 2 - 2 - 12
		binary.LittleEndian.PutUint16(dst, uint16(uint32(setRLE)+1<<2+uint32(srcSize)<<4))
	case 3: // 2 - 2 - 20
		binary.LittleEndian.PutUint32(dst, uint32(setRLE)+3<<2+uint32(srcSize)<<4)
	}
	dst[flSize] = src[0]
	return flSize + 1
}

// minLiteralsToCompress is the minimal number of literals for compression to
// even be attempted; the minimum is tighter for stronger strategies.
// C: zstd_compress_literals.c:ZSTD_minLiteralsToCompress
func minLiteralsToCompress(strat strategy, hufRepeat huf.Repeat) int {
	shift := min(9-int(strat), 3)
	if hufRepeat == huf.RepeatValid {
		return 6
	}
	return 8 << shift
}

// compressLiterals writes the literals section for src into dst and returns
// its size. nextHuf receives the table the section leaves for the next block.
// wksp must be at least huf.WorkspaceSize bytes.
// C: zstd_compress_literals.c:ZSTD_compressLiterals
func compressLiterals(dst []byte, src []byte, prevHuf *hufCTables, nextHuf *hufCTables,
	strat strategy, disableLiteralCompression bool, suspectUncompressible bool, bmi2 bool, wksp []byte) (int, error) {
	srcSize := len(src)
	lhSize := 3 + b2i(srcSize >= 1<<10) + b2i(srcSize >= 16<<10)
	singleStream := srcSize < 256
	hType := setCompressed
	var cLitSize int
	var err error

	// Prepare nextEntropy assuming reusing the existing table
	*nextHuf = *prevHuf

	if disableLiteralCompression {
		return noCompressLiterals(dst, src)
	}

	// if too small, don't even attempt compression (speed opt)
	if srcSize < minLiteralsToCompress(strat, prevHuf.repeatMode) {
		return noCompressLiterals(dst, src)
	}

	if len(dst) < lhSize+1 {
		return 0, errDstSizeTooSmall // not enough space for compression
	}
	{
		repeat := prevHuf.repeatMode
		flags := 0
		if bmi2 {
			flags |= huf.FlagBMI2
		}
		if strat < zstdLazy && srcSize <= 1024 {
			flags |= huf.FlagPreferRepeat
		}
		if strat >= zstdBtultra { // C: HUF_OPTIMAL_DEPTH_THRESHOLD
			flags |= huf.FlagOptimalDepth
		}
		if suspectUncompressible {
			flags |= huf.FlagSuspectUncompressible
		}

		if repeat == huf.RepeatValid && lhSize == 3 {
			singleStream = true
		}
		// Both are called directly: through a function variable (the
		// compiler before Go 1.26 cannot see through it) &repeat escapes,
		// and every block allocates.
		if singleStream {
			cLitSize, err = huf.Compress1XRepeat(dst[lhSize:], src, huf.SymbolValueMax, litHufLog, wksp, nextHuf.cTable[:], &repeat, flags)
		} else {
			cLitSize, err = huf.Compress4XRepeat(dst[lhSize:], src, huf.SymbolValueMax, litHufLog, wksp, nextHuf.cTable[:], &repeat, flags)
		}
		if repeat != huf.RepeatNone {
			// reused the existing table
			hType = setRepeat
		}
	}

	if cLitSize == 0 || cLitSize >= srcSize-minGain(srcSize, strat) || err != nil {
		*nextHuf = *prevHuf
		return noCompressLiterals(dst, src)
	}
	if cLitSize == 1 {
		// A return value of 1 signals that the alphabet consists of a single
		// symbol. However, in some rare circumstances, it could be the
		// compressed size (a single byte). For that outcome to have a chance
		// to happen, it's necessary that srcSize < 8 (it's also necessary to
		// not generate statistics). Therefore, in such a case, actively check
		// that all bytes are identical.
		if srcSize >= 8 || allBytesIdentical(src) {
			*nextHuf = *prevHuf
			return compressRleLiteralsBlock(dst, src), nil
		}
	}

	if hType == setCompressed {
		// using a newly constructed table
		nextHuf.repeatMode = huf.RepeatCheck
	}

	// Build header
	switch lhSize {
	case 3: // 2 - 2 - 10 - 10
		lhc := uint32(hType) + uint32(b2i(!singleStream))<<2 + uint32(srcSize)<<4 + uint32(cLitSize)<<14
		dst[0], dst[1], dst[2] = byte(lhc), byte(lhc>>8), byte(lhc>>16)
	case 4: // 2 - 2 - 14 - 14
		lhc := uint32(hType) + 2<<2 + uint32(srcSize)<<4 + uint32(cLitSize)<<18
		binary.LittleEndian.PutUint32(dst, lhc)
	case 5: // 2 - 2 - 18 - 18
		lhc := uint32(hType) + 3<<2 + uint32(srcSize)<<4 + uint32(cLitSize)<<22
		binary.LittleEndian.PutUint32(dst, lhc)
		dst[4] = byte(cLitSize >> 10)
	}
	return lhSize + cLitSize, nil
}

// b2i converts a C boolean expression result to 0 or 1.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
