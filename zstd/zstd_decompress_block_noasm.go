//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package zstd

// sequencesFast leaves every sequence to decompressSequencesBody's loop.
func sequencesFast(_ *seqState, _ *seqFast, _ []byte) dErr { return 0 }

// sequencesFastSplit leaves every sequence to
// decompressSequencesBodySplitLitBuffer's loops.
func sequencesFastSplit(_ *seqState, _ *seqFast, _ []byte) (seq, bool, dErr) { return seq{}, false, 0 }

// buildFSETableFast leaves every table to buildFSETable's Go body.
func buildFSETableFast(_ []seqSymbol, _ []int16, _ uint, _ []uint32, _ []uint8, _ uint, _ *buildFSETableWksp) bool {
	return false
}
