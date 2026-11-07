//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the seqToCodes assembly, shared by amd64 and arm64.

package zstd

// seqCodes is seqCodesGeneric, in assembly when it is on.
func seqCodes(seqs []seqDef, llCodes, ofCodes, mlCodes []byte) {
	n := len(seqs)
	if !useSeqCodesAsm || n == 0 || len(llCodes) < n || len(ofCodes) < n || len(mlCodes) < n {
		seqCodesGeneric(seqs, llCodes, ofCodes, mlCodes)
		return
	}
	seqCodesAsm(&seqs[0], n, &llCodes[0], &ofCodes[0], &mlCodes[0])
}
