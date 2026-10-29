//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import (
	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
)

// useSeqAsm selects the assembly loop of encodeSequences (baseline amd64).
var useSeqAsm bool

func init() {
	cpu.RegisterAsm("sequences", &useSeqAsm, func() bool { return true })
}

// encodeSequencesAsm encodes seqs[n-1] down to seqs[0] (C: the loop of
// ZSTD_encodeSequences_body with longOffsets = 0), continuing from the given
// bit writer state (buf, ptr, endPtr, container, bitPos) and FSE states,
// and returns the updated ones. stateTables are the CTables' u16 state
// tables (ct[1:]), tts their symbolTT, and mlMax/ofMax/llMax the tables'
// maxSymbolValue: a code above it (impossible: the tables are built from
// these codes' histograms) stops the loop with bitPosOut = 64 instead of
// reading outside the symbolTT. The state transitions of a valid FSE table
// stay inside its state table.
//
// Preconditions (checked by encodeSequences): the code slices and seqs hold
// n entries; 0 <= ptr <= endPtr = len(buf) - 8 (each flush writes 8 bytes at ptr and
// clamps ptr to endPtr, as BIT_flushBits); bitPos < 8.
//
//go:noescape
func encodeSequencesAsm(seqs *seqDef, llCodes, mlCodes, ofCodes *byte, n int,
	mlStateTable, mlTT, ofStateTable, ofTT, llStateTable, llTT *uint32, llBitsTab, mlBitsTab *uint8,
	buf *byte, ptr, endPtr int, container, bitPos uint64, stML, stOF, stLL uint32,
	mlMax, ofMax, llMax uint32) (containerOut, bitPosOut uint64, ptrOut int, stMLOut, stOFOut, stLLOut uint32)

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
	container, bitPos64, ptr, vML, vOF, vLL := encodeSequencesAsm(&seqs[0], &llCodes[0], &mlCodes[0], &ofCodes[0], last,
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

