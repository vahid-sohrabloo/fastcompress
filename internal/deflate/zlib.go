// Port of libdeflate v1.26 lib/zlib_compress.c and lib/zlib_constants.h
// (Copyright 2016 Eric Biggers, MIT; see NOTICE): libdeflate_zlib_compress,
// libdeflate_zlib_compress_bound and the zlib format constants. The zlib
// decompressor is in decompress_zlib.go.

package deflate

import (
	"encoding/binary"
	"math"

	"github.com/vahid-sohrabloo/fastzstd/internal/adler32"
)

// zlib format constants. C: zlib_constants.h
const (
	zlibMinHeaderSize = 2
	zlibFooterSize    = 4
	zlibMinOverhead   = zlibMinHeaderSize + zlibFooterSize

	zlibCMDeflate = 8

	zlibCINFO32KWindow = 7

	zlibFastestCompression = 0
	zlibFastCompression    = 1
	zlibDefaultCompression = 2
	zlibSlowestCompression = 3
)

// CompressZlib compresses src into dst as a zlib stream (RFC 1950: the
// 2-byte CMF/FLG header with a 32 KiB window, the compression level hint and
// no dictionary, the DEFLATE stream of Compress, then the big-endian
// Adler-32 of src) and returns the compressed size, or 0 if dst is too small
// (dst bytes may have been written even then). The output is byte-identical
// to libdeflate's. A buffer of ZlibCompressBound(len(src)) bytes is always
// large enough. CompressZlib does not allocate.
// port of libdeflate_zlib_compress
func (c *Compressor) CompressZlib(dst, src []byte) int {
	if len(dst) <= zlibMinOverhead {
		return 0
	}

	// 2 byte header: CMF and FLG
	hdr := uint16((zlibCMDeflate << 8) | (zlibCINFO32KWindow << 12))
	var levelHint uint16
	compressionLevel := c.Level()
	if compressionLevel < 2 {
		levelHint = zlibFastestCompression
	} else if compressionLevel < 6 {
		levelHint = zlibFastCompression
	} else if compressionLevel < 8 {
		levelHint = zlibDefaultCompression
	} else {
		levelHint = zlibSlowestCompression
	}
	hdr |= levelHint << 6
	hdr |= 31 - (hdr % 31)

	binary.BigEndian.PutUint16(dst[0:2], hdr)

	// Compressed data
	deflateSize := c.Compress(dst[zlibMinHeaderSize:len(dst)-zlibFooterSize], src)
	if deflateSize == 0 {
		return 0
	}
	outNext := zlibMinHeaderSize + deflateSize

	// ADLER32
	binary.BigEndian.PutUint32(dst[outNext:], adler32.Checksum(src))
	outNext += 4

	return outNext
}

// ZlibCompressBound returns an upper bound on the CompressZlib size of any
// input of at most n bytes: CompressBound(n) plus the 6 bytes of header and
// trailer. Like CompressBound it may be called on a nil Compressor, panics if
// n < 0 and saturates at math.MaxInt.
// port of libdeflate_zlib_compress_bound
func (c *Compressor) ZlibCompressBound(n int) int {
	b := c.CompressBound(n)
	if b > math.MaxInt-zlibMinOverhead {
		return math.MaxInt
	}
	return zlibMinOverhead + b
}
