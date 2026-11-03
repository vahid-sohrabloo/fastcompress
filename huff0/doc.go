// Package huff0 holds the Huffman table type of the zstd package's API
// (zstd.InspectDictionary's LitEncoder) for drop-in compatibility with
// github.com/klauspost/compress/huff0.
//
// Only the Scratch type exists, and it is opaque: code that reads
// Scratch's fields or calls klauspost's huff0 functions does not compile
// against this package.
package huff0
