package zlib_test

import (
	stdzlib "compress/zlib"
	"reflect"

	pkg "github.com/vahid-sohrabloo/fastcompress/zlib"
)

// Package-specific parts of the shared API parity tests in apiparity_test.go.

// stdPath is the standard library package this package replaces.
const stdPath = "compress/zlib"

// ourPath is the import path of this package.
const ourPath = "github.com/vahid-sohrabloo/fastcompress/zlib"

// apiExtras lists every exported identifier ("Name") and member ("Type.Name")
// of this package that the standard library's zlib does not have, with the
// reason it exists. Anything exported that is neither in the standard
// library nor here fails TestAPIParity.
var apiExtras = map[string]string{
	// libdeflate's levels above the standard library's 9 (near-optimal
	// parsing) and the largest accepted level.
	"Level10":  "libdeflate level 10 (near-optimal parsing)",
	"Level11":  "libdeflate level 11 (near-optimal parsing)",
	"Level12":  "libdeflate level 12 (near-optimal parsing)",
	"MaxLevel": "largest accepted level (Level12)",

	// Whole-buffer API (libdeflate_compress / libdeflate_decompress style).
	"CompressBound":                 "libdeflate_*_compress_bound: worst-case compressed size",
	"Compressor":                    "whole-buffer compressor (libdeflate_compressor)",
	"NewCompressor":                 "libdeflate_alloc_compressor",
	"Compressor.AppendCompress":     "whole-buffer compress appending to a slice",
	"Compressor.Compress":           "libdeflate_*_compress into a caller buffer",
	"Compressor.CompressBound":      "per-level compress bound",
	"Compressor.Level":              "the Compressor's level",
	"Decompressor":                  "whole-buffer decompressor (libdeflate_decompressor)",
	"NewDecompressor":               "libdeflate_alloc_decompressor",
	"Decompressor.AppendDecompress": "whole-buffer decompress into a growing slice (unknown size)",
	"Decompressor.Decompress":       "libdeflate_*_decompress with the exact size",
	"Decompressor.DecompressEx":     "libdeflate_*_decompress_ex (consumed and produced sizes)",
	"Decompressor.MaxOutput":        "AppendDecompress output limit (decompression-bomb guard)",
	"Decompressor.SetMaxOutput":     "sets the AppendDecompress output limit",
	"DefaultMaxOutput":              "default AppendDecompress output limit",

	// Whole-buffer API errors (libdeflate result codes), shared by flate,
	// gzip and zlib.
	"ErrBadData":           "LIBDEFLATE_BAD_DATA",
	"ErrShortOutput":       "LIBDEFLATE_SHORT_OUTPUT",
	"ErrInsufficientSpace": "LIBDEFLATE_INSUFFICIENT_SPACE",
	"ErrShortBuffer":       "Compress: dst smaller than the compressed data",
	"ErrInvalidLevel":      "NewCompressor: level outside -1..12",
	"ErrOutputLimit":       "AppendDecompress: output limit exceeded",
}

// reflectPairs maps each exported function, variable and type of the
// standard library's zlib to {standard library, ours}: functions as values,
// variables as pointers, types as reflect.Type.
var reflectPairs = map[string][2]any{
	"NewReader":          {stdzlib.NewReader, pkg.NewReader},
	"NewReaderDict":      {stdzlib.NewReaderDict, pkg.NewReaderDict},
	"NewWriter":          {stdzlib.NewWriter, pkg.NewWriter},
	"NewWriterLevel":     {stdzlib.NewWriterLevel, pkg.NewWriterLevel},
	"NewWriterLevelDict": {stdzlib.NewWriterLevelDict, pkg.NewWriterLevelDict},
	"ErrChecksum":        {&stdzlib.ErrChecksum, &pkg.ErrChecksum},
	"ErrDictionary":      {&stdzlib.ErrDictionary, &pkg.ErrDictionary},
	"ErrHeader":          {&stdzlib.ErrHeader, &pkg.ErrHeader},
	"Resetter":           {reflect.TypeFor[stdzlib.Resetter](), reflect.TypeFor[pkg.Resetter]()},
	"Writer":             {reflect.TypeFor[stdzlib.Writer](), reflect.TypeFor[pkg.Writer]()},
}
