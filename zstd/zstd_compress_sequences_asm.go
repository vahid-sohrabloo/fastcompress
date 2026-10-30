//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the sequence encoder's assembly, shared by amd64 and arm64.

package zstd

import (
	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
)

// encodeSequences is encodeSequencesGeneric with its main loop in assembly.
func encodeSequences(dst []byte, ctML, ctOF, ctLL fse.CTable, seqs []seqDef,
	llCodes, mlCodes, ofCodes []byte, longOffsets bool, bmi2 bool) (int, error) {
	nbSeq := len(seqs)
	if !useSeqAsm || longOffsets || nbSeq < 2 || len(llCodes) < nbSeq || len(mlCodes) < nbSeq || len(ofCodes) < nbSeq {
		return encodeSequencesGeneric(dst, ctML, ctOF, ctLL, seqs, llCodes, mlCodes, ofCodes, longOffsets, bmi2)
	}
	var blockStream bitstream.CStream
	var stateMatchLength, stateOffsetBits, stateLitLength fse.CState
	if blockStream.Init(dst) != nil {
		return 0, errDstSizeTooSmall // not enough space remaining
	}

	// first symbols (as encodeSequencesGeneric, longOffsets false)
	last := nbSeq - 1
	stateMatchLength.InitCState2(ctML, uint32(mlCodes[last]))
	stateOffsetBits.InitCState2(ctOF, uint32(ofCodes[last]))
	stateLitLength.InitCState2(ctLL, uint32(llCodes[last]))
	blockStream.AddBits(uint64(seqs[last].litLength), uint(llBits[llCodes[last]]))
	blockStream.AddBits(uint64(seqs[last].mlBase), uint(mlBits[mlCodes[last]]))
	blockStream.AddBits(uint64(seqs[last].offBase), uint(ofCodes[last]))
	blockStream.FlushBits()

	container, bitPos, ptr, endPtr := blockStream.Raw()
	buf := blockStream.Buf()
	if endPtr != len(buf)-8 || ptr < 0 || ptr > endPtr || bitPos >= 8 {
		panic("zstd: encodeSequences preconditions")
	}
	var bitPos64 uint64
	var vML, vOF, vLL uint32
	container, bitPos64, ptr, vML, vOF, vLL = encodeSequencesAsmVariant(&seqs[0], &llCodes[0], &mlCodes[0], &ofCodes[0], last,
		&ctML[1], &ctML.SymbolTT()[0], &ctOF[1], &ctOF.SymbolTT()[0], &ctLL[1], &ctLL.SymbolTT()[0], &llBits[0], &mlBits[0],
		&buf[0], ptr, endPtr, container, uint64(bitPos),
		stateMatchLength.Value(), stateOffsetBits.Value(), stateLitLength.Value(),
		getFSEMaxSymbolValue(ctML), getFSEMaxSymbolValue(ctOF), getFSEMaxSymbolValue(ctLL))
	if bitPos64 > 63 {
		panic("zstd: sequence code beyond its FSE table")
	}
	blockStream.SetRaw(container, uint(bitPos64), ptr)
	stateMatchLength.SetValue(vML)
	stateOffsetBits.SetValue(vOF)
	stateLitLength.SetValue(vLL)

	fse.FlushCState(&blockStream, &stateMatchLength)
	fse.FlushCState(&blockStream, &stateOffsetBits)
	fse.FlushCState(&blockStream, &stateLitLength)

	streamSize := blockStream.Close()
	if streamSize == 0 {
		return 0, errDstSizeTooSmall // not enough space
	}
	return streamSize, nil
}
