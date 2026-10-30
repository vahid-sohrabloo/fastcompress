// Port of libzstd v1.5.7 lib/compress/zstd_compress_sequences.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license).
//
// The Go body of ZSTD_encodeSequences, always compiled;
// zstd_compress_sequences_amd64.s and _arm64.s replace its main loop.

package zstd

import (
	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
)

// encodeSequencesGeneric writes the sequences bitstream and returns its size.
// C: zstd_compress_sequences.c:ZSTD_encodeSequences (and
// ZSTD_encodeSequences_body). The MEM_32bits() flushes are omitted: the bit
// container is 64 bits, as in C on 64-bit targets. bmi2 selects the same
// body in C and does not change output.
func encodeSequencesGeneric(dst []byte, ctML, ctOF, ctLL fse.CTable, seqs []seqDef,
	llCodes, mlCodes, ofCodes []byte, longOffsets bool, bmi2 bool) (int, error) {
	var blockStream bitstream.CStream
	var stateMatchLength, stateOffsetBits, stateLitLength fse.CState
	nbSeq := len(seqs)

	if blockStream.Init(dst) != nil {
		return 0, errDstSizeTooSmall // not enough space remaining
	}

	// first symbols
	last := nbSeq - 1
	stateMatchLength.InitCState2(ctML, uint32(mlCodes[last]))
	stateOffsetBits.InitCState2(ctOF, uint32(ofCodes[last]))
	stateLitLength.InitCState2(ctLL, uint32(llCodes[last]))
	blockStream.AddBits(uint64(seqs[last].litLength), uint(llBits[llCodes[last]]))
	blockStream.AddBits(uint64(seqs[last].mlBase), uint(mlBits[mlCodes[last]]))
	if longOffsets {
		ofBits := uint(ofCodes[last])
		extraBits := ofBits - min(ofBits, streamAccumulatorMin-1)
		if extraBits != 0 {
			blockStream.AddBits(uint64(seqs[last].offBase), extraBits)
			blockStream.FlushBits()
		}
		blockStream.AddBits(uint64(seqs[last].offBase>>extraBits), ofBits-extraBits)
	} else {
		blockStream.AddBits(uint64(seqs[last].offBase), uint(ofCodes[last]))
	}
	blockStream.FlushBits()

	for n := nbSeq - 2; n >= 0; n-- {
		llCode := llCodes[n]
		ofCode := ofCodes[n]
		mlCode := mlCodes[n]
		llBitsN := uint(llBits[llCode])
		ofBits := uint(ofCode)
		mlBitsN := uint(mlBits[mlCode])
		fse.EncodeSymbol(&blockStream, &stateOffsetBits, uint32(ofCode))  // 15
		fse.EncodeSymbol(&blockStream, &stateMatchLength, uint32(mlCode)) // 24
		fse.EncodeSymbol(&blockStream, &stateLitLength, uint32(llCode))   // 33
		if ofBits+mlBitsN+llBitsN >= 64-7-(llFSELog+mlFSELog+offFSELog) {
			blockStream.FlushBits() // (7)
		}
		blockStream.AddBits(uint64(seqs[n].litLength), llBitsN)
		blockStream.AddBits(uint64(seqs[n].mlBase), mlBitsN)
		if ofBits+mlBitsN+llBitsN > 56 {
			blockStream.FlushBits()
		}
		if longOffsets {
			extraBits := ofBits - min(ofBits, streamAccumulatorMin-1)
			if extraBits != 0 {
				blockStream.AddBits(uint64(seqs[n].offBase), extraBits)
				blockStream.FlushBits() // (7)
			}
			blockStream.AddBits(uint64(seqs[n].offBase>>extraBits), ofBits-extraBits) // 31
		} else {
			blockStream.AddBits(uint64(seqs[n].offBase), ofBits) // 31
		}
		blockStream.FlushBits() // (7)
	}

	fse.FlushCState(&blockStream, &stateMatchLength)
	fse.FlushCState(&blockStream, &stateOffsetBits)
	fse.FlushCState(&blockStream, &stateLitLength)

	streamSize := blockStream.Close()
	if streamSize == 0 {
		return 0, errDstSizeTooSmall // not enough space
	}
	return streamSize, nil
}
