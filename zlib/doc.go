// Package zlib compresses and decompresses zlib data (RFC 1950): whole buffers
// in both directions, and streams in both directions (Writer, NewReader). It
// is a Go port of libdeflate v1.26's zlib functions (Copyright 2016 Eric
// Biggers, MIT; see internal/deflate/NOTICE), without cgo (amd64 assembly
// in the shared core; see package flate).
//
// The API is the same as package flate's (see its documentation for the
// levels, the properties of the whole-buffer API, when to use it instead of
// a streaming API, and the memory used per level); this page lists what is
// specific to zlib.
//
// # Compression
//
// Compress writes one complete zlib stream, byte-identical to libdeflate's
// libdeflate_zlib_compress at the same level: a 2-byte header (deflate
// with a 32 KiB window, no preset dictionary, FLEVEL 0 at levels 0 and 1, 1
// at 2..5, 2 at 6 and 7, 3 at 8 and above), the raw DEFLATE data of package
// flate, and the big-endian Adler-32 of the input. Its CompressBound is
// flate's plus 6 bytes.
//
// # Streaming compression
//
// Writer (NewWriter, NewWriterLevel, NewWriterLevelDict) is a drop-in
// replacement for the standard library's zlib.Writer, preset dictionaries
// included, built on flate.Writer (see its documentation for chunking,
// Flush and errors).
//
// # Decompression
//
// Decompress, DecompressEx and AppendDecompress check the header (FCHECK,
// method 8, window size at most 32 KiB) and the Adler-32 trailer; any
// mismatch is ErrBadData. Streams that need a preset dictionary (FDICT set)
// are rejected with ErrBadData, as libdeflate does; the streaming
// NewReaderDict reads those. Bytes after the end of
// the stream are ignored; DecompressEx's nIn (header, data and trailer)
// tells where the stream ends.
//
// # Streaming decompression
//
// NewReader and NewReaderDict return a drop-in replacement for the standard
// library's zlib reader (with Resetter, ErrChecksum, ErrDictionary and
// ErrHeader), built on package flate's streaming decoder (see "Streaming
// Reader" there). As the standard library's: a preset dictionary is only
// used if the header asks for one (FDICT), and then its Adler-32 must match
// (else ErrDictionary); the Adler-32 trailer is verified (ErrChecksum); an
// io.ByteReader is left positioned just after the stream once Read has
// returned io.EOF. Unlike the standard library's, an underlying reader that
// keeps returning (0, nil) gives io.ErrNoProgress instead of an endless
// loop. A reader holds about 33 KiB, growing to about 173 KiB for streams
// with more than 16 KiB of output.
//
// # Caveats
//
// As in package flate: the zero value of a Compressor is not usable
// (create one with NewCompressor), while a zero Decompressor is ready to
// use. The output buffer must not overlap the input, and for
// AppendCompress and AppendDecompress the output buffer is the whole
// capacity of dst; in-place use is not detected and gives invalid
// compressed data, ErrBadData or wrong output. AppendDecompress allocates
// its sizeHint up front (clamped only by the output limit and the largest
// size the input could decompress to), so do not pass a hint read from
// untrusted data, such as a gzip ISIZE trailer, without lowering
// SetMaxOutput.
//
// # Errors
//
// The error variables of the whole-buffer API are package flate's, so
// zlib.ErrBadData == flate.ErrBadData and errors.Is works across the
// packages. The streaming reader returns the standard library's errors
// instead: ErrChecksum, ErrDictionary and ErrHeader of this package (the
// standard library's texts), flate.CorruptInputError, io.ErrUnexpectedEOF.
package zlib
