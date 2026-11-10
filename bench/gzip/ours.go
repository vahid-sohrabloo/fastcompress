package main

import (
	"fmt"

	"github.com/vahid-sohrabloo/fastzstd/flate"
	"github.com/vahid-sohrabloo/fastzstd/gzip"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflate"
)

// ours is the adapter for this repository's gzip/DEFLATE port: the
// whole-buffer Compressors of packages flate ("raw") and gzip ("gzip") at
// libdeflate's levels 0..12 (byte-identical to libdeflate v1.26), and the
// whole-buffer Decompressors (exact decoded size, as the Adapter contract
// asks). Another format reports ErrNotPorted (gzbench exit 3).
var ours Adapter = oursAdapter{}

type oursAdapter struct{}

func (oursAdapter) NewCompressor(level int, format string) (Compressor, error) {
	if level < 0 || level > flate.MaxLevel {
		return nil, fmt.Errorf("level %d: %w", level, ErrUnsupportedLevel)
	}
	switch format {
	case "raw":
		return flate.NewCompressor(level)
	case "gzip":
		return gzip.NewCompressor(level)
	}
	return nil, ErrNotPorted
}

func (oursAdapter) NewDecompressor(format string) (Decompressor, error) {
	switch format {
	case "raw":
		return deflate.NewDecompressor(), nil
	case "gzip":
		return gzip.NewDecompressor(), nil
	}
	return nil, ErrNotPorted
}
