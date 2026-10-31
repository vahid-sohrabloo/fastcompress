//go:build !amd64 || appengine || noasm || purego || !gc

package zstd

func seqCodes(seqs []seqDef, llCodes, ofCodes, mlCodes []byte) {
	seqCodesGeneric(seqs, llCodes, ofCodes, mlCodes)
}
