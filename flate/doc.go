// Package flate compresses and decompresses raw DEFLATE data (RFC 1951):
// whole buffers in both directions, and streams in both directions (Writer,
// NewReader). It is a Go port of libdeflate v1.26 (Copyright 2016 Eric
// Biggers, MIT; see internal/deflate/NOTICE), without cgo; on amd64 some
// hot loops are assembly (see "Assembly and speed").
//
// Packages gzip and zlib of this module have the same API for the gzip (RFC
// 1952) and zlib (RFC 1950) containers.
//
// # Whole-buffer API
//
// A Compressor compresses one complete input into a caller-provided buffer
// (Compress) or appends to a slice (AppendCompress). A Decompressor
// decompresses one complete stream into a buffer of the exact decompressed
// size (Decompress), into a buffer of at most a given size (DecompressEx),
// or into a growing slice when the size is unknown (AppendDecompress).
// These calls have no streaming state: each one is independent, and
// nothing is read from or written to an io.Reader or io.Writer (the
// streaming Writer is described below).
//
// # Properties
//
//   - Compressed output is byte-identical to libdeflate's
//     libdeflate_deflate_compress at the same level (the tests compare every
//     level against the C library).
//   - Levels are libdeflate's: NoCompression (0, stored blocks), BestSpeed
//     (1) .. BestCompression (9), DefaultCompression (-1, which is level 6),
//     and the extensions Level10..Level12 (MaxLevel), libdeflate's slower
//     near-optimal parsing, which compress better than level 9. The output
//     differs from the standard library's at the same level number; both
//     are valid DEFLATE and decode with any decoder.
//   - The decompressor accepts and rejects exactly the streams libdeflate
//     does, with the same output and the same consumed input length.
//     Corrupt or hostile input returns ErrBadData, never a panic; the
//     decoder uses no unsafe code.
//   - Decompress needs the exact decompressed size (libdeflate semantics);
//     a stream that ends earlier gives ErrShortOutput, one that needs more
//     ErrInsufficientSpace. DecompressEx accepts a larger buffer and
//     reports the sizes. AppendDecompress handles an unknown size by
//     retrying with a doubled buffer, up to an output limit that guards
//     against decompression bombs (DefaultMaxOutput, 1 GiB, unless changed
//     with SetMaxOutput).
//   - Compress, AppendCompress (given enough capacity), Decompress,
//     DecompressEx and AppendDecompress (given enough capacity) do not
//     allocate.
//
// # When to use this package
//
// Use the whole-buffer API when the data is already in memory: HTTP bodies
// of known size, records in a database or archive, network messages. It
// avoids the per-stream buffering of an io.Reader/io.Writer and, with
// pooled Compressors and Decompressors, allocates nothing per call.
//
// Use a streaming API when the data does not fit in memory, arrives in
// pieces, or the decompressed size is unknown and potentially huge. For
// compression this package has Writer (NewWriter, NewWriterDict), a drop-in
// replacement for the standard library's compress/flate.Writer with preset
// dictionaries, Flush and HuffmanOnly; see its documentation for how it
// chunks the input and when its output is byte-identical to Compress. For
// decompression it has NewReader and NewReaderDict (see "Streaming Reader").
//
// A Writer holds about 2 MiB of buffers (grown on demand, kept across
// Reset) besides its level's compressor (see below). It always passes the
// compressor its history and input adjacent in one buffer, so the
// compressor's own copy buffer for non-adjacent histories is never
// allocated.
//
// # Streaming Reader
//
// NewReader and NewReaderDict return a drop-in replacement for the
// standard library's compress/flate reader (Resetter, CorruptInputError and
// the other error types are there too); packages gzip and zlib build their
// Readers on the same decoder. libdeflate has no streaming decompressor, so
// this is new design around its decoder:
//
//   - The stream is decoded with libdeflate's decoder: its fast loop (the
//     same code as Decompress) while enough input and output space are
//     buffered, and a resumable variant of its generic loop near the ends
//     of the buffers, which stops at a symbol boundary and continues when
//     more input arrives. Block headers are decoded whole, once all their
//     bytes are there. The Reader accepts exactly the streams the
//     whole-buffer decoder accepts, with the same output; a stream that
//     ends early is io.ErrUnexpectedEOF (libdeflate says ErrBadData).
//   - It keeps a window (the 32 KiB of history DEFLATE needs plus 96 KiB of
//     output decoded at a time) and an input buffer of up to 32 KiB, with
//     the decode tables about 173 KiB per Reader (the standard library's
//     is about 45 KiB). They start at a 16 KiB window and 4 KiB of input
//     (about 33 KiB in all) and grow to the full size once a stream
//     produces more than 16 KiB of output; they are kept across Reset
//     (reading a stream then allocates nothing).
//   - If the underlying reader implements io.ByteReader, no byte after the
//     end of the stream is consumed from it: a *bufio.Reader is read ahead
//     with Peek and only what was used is discarded; a *bytes.Buffer is
//     copied from and only what was used is taken out; an io.Seeker
//     (bytes.Reader, strings.Reader) is read in bulk and moved back over
//     the unused bytes at the end of the stream, so its position is exact
//     only once Read has returned io.EOF (before that, and after an error,
//     it is ahead; gzip and zlib also move it back after ErrChecksum); other
//     ByteReaders are read one byte at a time (slower, except for stored
//     blocks). Other readers are read in pieces of up to 32 KiB and may be
//     read past the end, as with the standard library.
//   - Errors are the standard library's: io.ErrUnexpectedEOF for a
//     truncated stream, CorruptInputError for invalid data (its offset is
//     an upper bound of the position, counted from the start of the
//     DEFLATE data, not the standard library's value), the underlying
//     reader's own error as it is; all are sticky. The data before the
//     invalid or missing part are returned before the error. An underlying
//     reader that keeps returning (0, nil) gives io.ErrNoProgress (as
//     with the standard library's flate reader; its gzip and zlib readers
//     loop forever instead, ours return io.ErrNoProgress too). Close
//     returns the stream's error, if any, and does not stop the reader.
//   - A Read returns as soon as the data before a block boundary is
//     decoded (so the data before a Flush of the writer can be read without
//     more input); inside a block it reads more input first.
//
// # Assembly and speed
//
// On amd64, unless built with the noasm or purego tag, assembly replaces
// the Go code of the decoder's fast loop (on CPUs with BMI2 only), the
// level 1 loop, the greedy, lazy and lazy2 loops of levels 2..9, the block
// flush's sequence writer, the match finder's table init and rebase, and,
// for the near-optimal levels 10..12, the binary-tree match finding loop
// and the backward min-cost-path pass. The rest of levels 10..12,
// Adler-32 and every other architecture (arm64 included) are Go only. The
// assembly produces the same output as the Go code (each routine has a
// fuzz target comparing the two).
//
// On the one amd64 CPU measured (an i9-13900H, Silesia corpus; see the
// repository README and bench/results), decompression is about as fast as
// libdeflate's (geometric mean 100.2-100.6% at levels 1, 6, 9 and 12, a
// margin that depends on code layout), and compression is as fast or
// faster: geometric means of 103.6% (level 1), 101.6% (level 3), 102.1%
// (level 6), 102.8% (level 9) and 119.8% (level 12) of libdeflate's
// speed, with every one of the 13 inputs per level at 99.6% or more
// (the lowest is x-ray at level 1, 99.6%; the slow level 12 rows carry
// about 2-3 points of rounding). The compression gate (geometric mean at
// least 100%, every row at least 95%, none below 90%) is met at every
// measured level. Results come from one CPU; expect other CPUs to differ.
// The pure-Go build is slower (not gated; arm64 has not been
// benchmarked).
//
// # Memory and reuse
//
// A Compressor holds the match finder of its level and is meant to be
// reused (a sync.Pool per level works well). Measured allocation of
// NewCompressor and NewDecompressor on amd64:
//
//	level 0                    8 KiB
//	level 1                  208 KiB
//	levels 2..9              656 KiB
//	levels 10..12            8.6 MiB
//	Decompressor              13 KiB
//
// Compressors and Decompressors are not safe for concurrent use; separate
// instances may be used concurrently. The zero value of a Decompressor is
// ready to use, but the zero value of a Compressor is not: create
// Compressors with NewCompressor.
//
// # Buffers must not overlap
//
// The output buffer of every call must not overlap the input: in-place
// compression or decompression is not supported, is not detected, and
// gives invalid compressed data, ErrBadData or wrong output. For
// AppendCompress and AppendDecompress the output buffer is the whole
// capacity of dst, not only dst[:len(dst)]; in particular, do not pass a
// src that lives in the spare capacity of dst (such as a compressed body
// stored after a header in the same buffer).
//
// # Size hints
//
// AppendDecompress allocates its sizeHint up front, clamped only by the
// output limit (and by the largest size the input could decompress to).
// Do not pass a hint read from untrusted data, such as a gzip ISIZE
// trailer, without lowering SetMaxOutput to an amount you are willing to
// allocate.
//
// # Errors
//
// The error variables (ErrBadData, ErrShortOutput, ErrInsufficientSpace,
// ErrShortBuffer, ErrInvalidLevel, ErrOutputLimit) are shared by packages
// flate, gzip and zlib: gzip.ErrBadData == flate.ErrBadData, so errors.Is
// works across them. The streaming readers return the standard library's
// error types instead (CorruptInputError, io.ErrUnexpectedEOF, and the
// checksum and header errors of gzip and zlib).
package flate
