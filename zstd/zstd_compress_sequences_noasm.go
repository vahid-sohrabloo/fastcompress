//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/fse"

func encodeSequences(dst []byte, ctML, ctOF, ctLL fse.CTable, seqs []seqDef,
	llCodes, mlCodes, ofCodes []byte, longOffsets bool, bmi2 bool) (int, error) {
	return encodeSequencesGeneric(dst, ctML, ctOF, ctLL, seqs, llCodes, mlCodes, ofCodes, longOffsets, bmi2)
}
