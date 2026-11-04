//go:build !amd64 || appengine || noasm || purego || !gc

package zstd

// sequencesFast leaves every sequence to decompressSequencesBody's loop.
func sequencesFast(_ *seqState, _ *seqFast, _ []byte) dErr { return 0 }
