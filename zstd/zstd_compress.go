// Port of the block entropy stage of libzstd v1.5.7
// lib/compress/zstd_compress.c (Copyright (c) Meta Platforms, Inc. and
// affiliates; BSD/GPLv2 dual license).

package zstd

import (
	"encoding/binary"

	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
	"github.com/vahid-sohrabloo/fastzstd/internal/hist"
)

// symbolEncodingTypeStats holds the finalized symbol encoding types and the
// size of the sequences statistics.
// C: zstd_compress.c:ZSTD_symbolEncodingTypeStats_t (the error that C puts
// in size is returned separately).
type symbolEncodingTypeStats struct {
	llType, offType, mlType symbolEncodingType
	size                    int
	lastCountSize           int // accounts for a bug in 1.3.4, see entropyCompressSeqStoreInternal
	longOffsets             bool
}

// buildSequencesStatistics selects the encoding type of the three sequence
// code tables, builds next's CTables and writes their descriptions to dst.
// nbSeq must be > 0. count is the histogram scratch.
// C: zstd_compress.c:ZSTD_buildSequencesStatistics
func buildSequencesStatistics(ss *seqStore, nbSeq int, prev, next *fseCTables,
	dst []byte, strat strategy, count *[256]uint32, wksp []byte) (symbolEncodingTypeStats, error) {
	op := 0
	ctLitLength := fse.CTable(next.litlengthCTable[:])
	ctOffsetBits := fse.CTable(next.offcodeCTable[:])
	ctMatchLength := fse.CTable(next.matchlengthCTable[:])
	ofCodeTable := ss.ofCode[:nbSeq]
	llCodeTable := ss.llCode[:nbSeq]
	mlCodeTable := ss.mlCode[:nbSeq]
	var stats symbolEncodingTypeStats

	// convert length/distances into codes
	stats.longOffsets = seqToCodes(ss)

	// build CTable for Literal Lengths
	{
		max := uint32(maxLL)
		mostFrequent := hist.CountFast(count, &max, llCodeTable) // can't fail
		next.litlengthRepeatMode = prev.litlengthRepeatMode
		stats.llType = selectEncodingType(&next.litlengthRepeatMode, count[:], max, int(mostFrequent), nbSeq,
			llFSELog, prev.litlengthCTable[:], llDefaultNorm[:], llDefaultNormLog, defaultAllowed, strat)
		countSize, err := buildCTable(dst[op:], ctLitLength, llFSELog, stats.llType,
			count[:], max, llCodeTable, nbSeq, llDefaultNorm[:], llDefaultNormLog, maxLL,
			prev.litlengthCTable[:], wksp)
		if err != nil {
			return stats, err
		}
		if stats.llType == setCompressed {
			stats.lastCountSize = countSize
		}
		op += countSize
	}
	// build CTable for Offsets
	{
		max := uint32(maxOff)
		mostFrequent := hist.CountFast(count, &max, ofCodeTable) // can't fail
		// We can only use the basic table if max <= DefaultMaxOff, otherwise the offsets are too large
		policy := defaultDisallowed
		if max <= defaultMaxOff {
			policy = defaultAllowed
		}
		next.offcodeRepeatMode = prev.offcodeRepeatMode
		stats.offType = selectEncodingType(&next.offcodeRepeatMode, count[:], max, int(mostFrequent), nbSeq,
			offFSELog, prev.offcodeCTable[:], ofDefaultNorm[:], ofDefaultNormLog, policy, strat)
		countSize, err := buildCTable(dst[op:], ctOffsetBits, offFSELog, stats.offType,
			count[:], max, ofCodeTable, nbSeq, ofDefaultNorm[:], ofDefaultNormLog, defaultMaxOff,
			prev.offcodeCTable[:], wksp)
		if err != nil {
			return stats, err
		}
		if stats.offType == setCompressed {
			stats.lastCountSize = countSize
		}
		op += countSize
	}
	// build CTable for MatchLengths
	{
		max := uint32(maxML)
		mostFrequent := hist.CountFast(count, &max, mlCodeTable) // can't fail
		next.matchlengthRepeatMode = prev.matchlengthRepeatMode
		stats.mlType = selectEncodingType(&next.matchlengthRepeatMode, count[:], max, int(mostFrequent), nbSeq,
			mlFSELog, prev.matchlengthCTable[:], mlDefaultNorm[:], mlDefaultNormLog, defaultAllowed, strat)
		countSize, err := buildCTable(dst[op:], ctMatchLength, mlFSELog, stats.mlType,
			count[:], max, mlCodeTable, nbSeq, mlDefaultNorm[:], mlDefaultNormLog, maxML,
			prev.matchlengthCTable[:], wksp)
		if err != nil {
			return stats, err
		}
		if stats.mlType == setCompressed {
			stats.lastCountSize = countSize
		}
		op += countSize
	}
	stats.size = op
	return stats, nil
}

// suspectUncompressibleLiteralRatio. C: zstd_compress.c:SUSPECT_UNCOMPRESSIBLE_LITERAL_RATIO
const suspectUncompressibleLiteralRatio = 20

// entropyCompressSeqStoreInternal compresses both literals and sequences and
// returns the compressed size of the block.
// C: zstd_compress.c:ZSTD_entropyCompressSeqStore_internal. Deviation: the
// histogram (C: the first MaxSeq+1 unsigned of entropyWorkspace) is a stack
// array; the rest of the workspace is still handed on as in C. This cannot
// change output.
func entropyCompressSeqStoreInternal(dst []byte, literals []byte, ss *seqStore,
	prev, next *entropyCTables, params *cctxParams, wksp []byte, bmi2 bool) (int, error) {
	strat := params.cParams.strategy
	var count [256]uint32
	ctLitLength := fse.CTable(next.fse.litlengthCTable[:])
	ctOffsetBits := fse.CTable(next.fse.offcodeCTable[:])
	ctMatchLength := fse.CTable(next.fse.matchlengthCTable[:])
	sequences := ss.sequences
	nbSeq := len(sequences)
	op := 0
	var lastCountSize int
	longOffsets := false

	wksp = wksp[(maxSeq+1)*4:] // C: entropyWorkspace = count + (MaxSeq + 1)

	// Compress literals
	{
		numSequences := nbSeq
		// Base suspicion of uncompressibility on ratio of literals to sequences
		suspectUncompressible := numSequences == 0 || len(literals)/numSequences >= suspectUncompressibleLiteralRatio
		cSize, err := compressLiterals(dst, literals, &prev.huf, &next.huf, strat,
			literalsCompressionIsDisabled(params), suspectUncompressible, bmi2, wksp)
		if err != nil {
			return 0, err
		}
		op += cSize
	}

	// Sequences Header
	if len(dst)-op < 3+1 { // max nbSeq Size + seqHead
		return 0, errDstSizeTooSmall // Can't fit seq hdr in output buf!
	}
	switch {
	case nbSeq < 128:
		dst[op] = byte(nbSeq)
		op++
	case nbSeq < longNbSeq:
		dst[op] = byte(nbSeq>>8 + 0x80)
		dst[op+1] = byte(nbSeq)
		op += 2
	default:
		dst[op] = 0xFF
		binary.LittleEndian.PutUint16(dst[op+1:], uint16(nbSeq-longNbSeq))
		op += 3
	}
	if nbSeq == 0 {
		// Copy the old tables over as if we repeated them
		next.fse = prev.fse
		return op, nil
	}
	{
		seqHead := op
		op++
		// build stats for sequences
		stats, err := buildSequencesStatistics(ss, nbSeq, &prev.fse, &next.fse, dst[op:], strat, &count, wksp)
		if err != nil {
			return 0, err
		}
		dst[seqHead] = byte(stats.llType<<6 + stats.offType<<4 + stats.mlType<<2)
		lastCountSize = stats.lastCountSize
		op += stats.size
		longOffsets = stats.longOffsets
	}

	{
		bitstreamSize, err := encodeSequences(dst[op:], ctMatchLength, ctOffsetBits, ctLitLength,
			sequences, ss.llCode, ss.mlCode, ss.ofCode, longOffsets, bmi2)
		if err != nil {
			return 0, err
		}
		op += bitstreamSize
		// zstd versions <= 1.3.4 mistakenly report corruption when
		// FSE_readNCount() receives a buffer < 4 bytes. Fixed by
		// https://github.com/facebook/zstd/pull/1146. This can happen when
		// the last set_compressed table present is 2 bytes and the bitstream
		// is only one byte. In this exceedingly rare case, we will simply
		// emit an uncompressed block, since it isn't worth optimizing.
		if lastCountSize != 0 && lastCountSize+bitstreamSize < 4 {
			return 0, nil
		}
	}
	return op, nil
}

// entropyCompressSeqStoreWExtLitBuffer.
// C: zstd_compress.c:ZSTD_entropyCompressSeqStore_wExtLitBuffer
func entropyCompressSeqStoreWExtLitBuffer(dst []byte, literals []byte, blockSize int, ss *seqStore,
	prev, next *entropyCTables, params *cctxParams, wksp []byte, bmi2 bool) (int, error) {
	cSize, err := entropyCompressSeqStoreInternal(dst, literals, ss, prev, next, params, wksp, bmi2)
	if err == nil && cSize == 0 {
		return 0, nil
	}
	// When srcSize <= dstCapacity, there is enough space to write a raw
	// uncompressed block. Since we ran out of space, block must be not
	// compressible, so fall back to raw uncompressed block.
	if err == errDstSizeTooSmall && blockSize <= len(dst) {
		return 0, nil // block not compressed
	}
	if err != nil {
		return 0, err
	}

	// Check compressibility
	maxCSize := blockSize - minGain(blockSize, params.cParams.strategy)
	if cSize >= maxCSize {
		return 0, nil // block not compressed
	}
	return cSize, nil
}

// entropyCompressSeqStore entropy-codes the block held in ss into dst. It
// returns the compressed size, or 0 when the block should be emitted
// uncompressed. next receives the entropy tables the block leaves behind;
// the caller makes them current only if it emits the compressed block.
// wksp must be at least entropyWorkspaceSize bytes.
// C: zstd_compress.c:ZSTD_entropyCompressSeqStore
func entropyCompressSeqStore(dst []byte, ss *seqStore, prev, next *entropyCTables,
	params *cctxParams, srcSize int, wksp []byte, bmi2 bool) (int, error) {
	return entropyCompressSeqStoreWExtLitBuffer(dst, ss.literals, srcSize, ss, prev, next, params, wksp, bmi2)
}
