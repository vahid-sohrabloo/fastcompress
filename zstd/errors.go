package zstd

import "errors"

var (
	// ErrEncoderClosed will be returned if the Encoder was used after
	// Close has been called.
	ErrEncoderClosed = errors.New("encoder used after Close")
)
