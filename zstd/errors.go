package zstd

import "errors"

var (
	// ErrEncoderClosed will be returned if the Encoder was used after
	// Close has been called.
	ErrEncoderClosed = errors.New("encoder used after Close")
)

// errStrategyNotPorted is EncodeAll's panic value for an input whose C
// strategy is not ported yet: SpeedBetterCompression for inputs of 16 KiB or
// less (hash-chain lazy2) and SpeedBestCompression for inputs of 256 KiB or
// less (btlazy2, btopt), until Tasks 14-16. EncodeAll has no error result.
var errStrategyNotPorted = errors.New("zstd: compression strategy for this level and input size not ported yet")
