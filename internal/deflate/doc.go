// Package deflate is a port of libdeflate v1.26 (lib/deflate_decompress.c,
// lib/decompress_template.h, lib/deflate_compress.c; Copyright 2016 Eric
// Biggers, MIT, see NOTICE): a whole-buffer DEFLATE (RFC 1951) decompressor
// and compressor.
//
// The port is clean-room: its only implementation guide is the libdeflate C
// source in ref/libdeflate. Each ported function says which C function it
// ports ("port of <c_function>") and keeps the C algorithm, so that the
// intermediate results (decode tables, Huffman codes, block decisions) are
// bit-identical to libdeflate's; the tests compare them with ref/ldref.
//
// Decoder files (*decompress*.go, decode_table*.go, static_codes.go) use no
// unsafe and no recover; scripts/decodercheck.sh enforces it.
package deflate
