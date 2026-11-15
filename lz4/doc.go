// Package lz4 is a pure-Go port of lz4 v1.11.1 (lz4.c, lz4hc.c and
// lz4frame.c, by Yann Collet; BSD 2-Clause): the LZ4 block format, the fast
// compressor, the high compression levels LZ4HC 2 to 12 and the LZ4 frame
// format.
//
// Every compressor produces C's output byte for byte, for the same calls in
// the same order: CompressFast is LZ4_compress_fast, CompressHC is
// LZ4_compress_HC, Stream and StreamHC are LZ4_stream_t and LZ4_streamHC_t
// with their streaming and dictionary functions, and CompressFrame and
// Writer are LZ4F_compressFrame and LZ4F_compressBegin/Update/Flush/End
// (single-threaded). The output matches C's on 64-bit little-endian
// platforms; this port produces the same bytes on every platform.
//
// Block functions take dst as a buffer whose length is the capacity, like
// C's dstCapacity, and return the number of bytes written. Compression
// returns 0 when the block does not fit; CompressBound(len(src)) bytes are
// always enough. Decompression returns an error for any malformed input,
// never panics and never reads or writes outside its buffers.
//
// Frames: CompressFrame / AppendFrame compress a buffer into one frame,
// Writer compresses a stream; AppendDecompressFrame and Reader decode
// concatenated frames, skipping skippable frames, with an optional
// dictionary.
package lz4
