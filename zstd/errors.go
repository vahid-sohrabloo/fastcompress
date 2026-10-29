package zstd

import "errors"

var (
	// ErrEncoderClosed will be returned if the Encoder was used after
	// Close has been called.
	ErrEncoderClosed = errors.New("encoder used after Close")
)

// errStrategyNotPorted is EncodeAll's panic value for a level whose C
// strategy is not ported yet (levels 7 and 11 until the
// greedy/lazy ports land). EncodeAll has no error result.
var errStrategyNotPorted = errors.New("zstd: compression level not ported yet (only SpeedFastest and SpeedDefault are available)")
