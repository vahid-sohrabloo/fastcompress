// Package gzip compresses and decompresses gzip data (RFC 1952): whole buffers
// in both directions, and streams in both directions (Writer, Reader). It is
// a Go port of libdeflate v1.26's gzip functions (Copyright 2016 Eric
// Biggers, MIT; see internal/deflate/NOTICE), without cgo (amd64 assembly
// in the shared core; see package flate).
//
// The API is the same as package flate's (see its documentation for the
// levels, the properties of the whole-buffer API, when to use it instead of
// a streaming API, and the memory used per level); this page lists what is
// specific to gzip.
//
// # Compression
//
// Compress writes one complete gzip member, byte-identical to libdeflate's
// libdeflate_gzip_compress at the same level: a 10-byte header with no
// optional fields (no file name, comment, extra field or header CRC), MTIME
// 0 (no time stamp), XFL 4 at levels 0 and 1 and 2 at levels 8 and above,
// OS 255 ("unknown"); the raw DEFLATE data of package flate; the CRC-32 and
// the input size modulo 2^32. Its CompressBound is flate's plus 18 bytes.
//
// # Decompression
//
// Decompress, DecompressEx and AppendDecompress decode the first member of
// src only, as libdeflate does; bytes after it are ignored, and
// DecompressEx's nIn (header, data and trailer) tells where the next member
// starts, so concatenated members can be decoded by calling it again on
// src[nIn:]. The header's optional fields (FEXTRA, FNAME, FCOMMENT) are
// skipped and not returned; the header CRC (FHCRC) is skipped without being
// checked, as libdeflate does. The CRC-32 and size trailer are checked: a
// mismatch is ErrBadData. FTEXT, MTIME, XFL and OS are ignored.
//
// The streaming Reader (below) returns the header fields and reads all
// members.
//
// # Streaming compression
//
// Writer (NewWriter, NewWriterLevel) is a drop-in replacement for the
// standard library's gzip.Writer, with the same Header fields, built on
// flate.Writer (see its documentation for chunking, Flush and errors).
//
// # Streaming decompression
//
// Reader (NewReader) is a drop-in replacement for the standard library's
// gzip.Reader, built on package flate's streaming decoder (see "Streaming
// Reader" there): Header fields of the first member, Multistream, Reset,
// ErrChecksum and ErrHeader. As the standard library's (and unlike
// Decompress): the header CRC (FHCRC) is verified, reserved FLG bits are
// ignored, a name or comment longer than 511 bytes is ErrHeader, and bytes
// after a member are read as the next member (Multistream(false) stops
// after one, leaving an io.ByteReader positioned just after it once Read
// has returned io.EOF). Unlike the standard library's, an underlying
// reader that keeps returning (0, nil) gives io.ErrNoProgress instead of
// an endless loop. A Reader holds about 33 KiB, growing to about 173 KiB
// for members with more than 16 KiB of output.
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
// gzip.ErrBadData == flate.ErrBadData and errors.Is works across the
// packages. The streaming Reader returns the standard library's errors
// instead: ErrChecksum and ErrHeader of this package (the standard
// library's texts), flate.CorruptInputError, io.ErrUnexpectedEOF.
package gzip
