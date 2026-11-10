// Port of libdeflate v1.26 lib/gzip_compress.c and lib/gzip_constants.h
// (Copyright 2016 Eric Biggers, MIT; see NOTICE): libdeflate_gzip_compress,
// libdeflate_gzip_compress_bound and the gzip format constants. The gzip
// decompressor is in decompress_gzip.go. The CRC-32 is the standard library's
// hash/crc32 (IEEE), the same function as libdeflate_crc32.

package deflate

import (
	"encoding/binary"
	"hash/crc32"
	"math"
)

// gzip format constants. C: gzip_constants.h
const (
	gzipMinHeaderSize = 10
	gzipFooterSize    = 8
	gzipMinOverhead   = gzipMinHeaderSize + gzipFooterSize

	gzipID1 = 0x1F
	gzipID2 = 0x8B

	gzipCMDeflate = 8

	gzipFTEXT     = 0x01
	gzipFHCRC     = 0x02
	gzipFEXTRA    = 0x04
	gzipFNAME     = 0x08
	gzipFCOMMENT  = 0x10
	gzipFRESERVED = 0xE0

	gzipMTimeUnavailable = 0

	gzipXFLSlowestCompression = 0x02
	gzipXFLFastestCompression = 0x04

	gzipOSUnknown = 255
)

// CompressGzip compresses src into dst as a gzip member (RFC 1952: a 10-byte
// header with no optional fields, MTIME 0 and OS 255, the DEFLATE stream of
// Compress, then the CRC-32 and the size of src modulo 2^32) and returns the
// compressed size, or 0 if dst is too small (dst bytes may have been written
// even then). The output is byte-identical to libdeflate's. A buffer of
// GzipCompressBound(len(src)) bytes is always large enough. CompressGzip does
// not allocate.
// port of libdeflate_gzip_compress
func (c *Compressor) CompressGzip(dst, src []byte) int {
	if len(dst) <= gzipMinOverhead {
		return 0
	}

	// ID1
	dst[0] = gzipID1
	// ID2
	dst[1] = gzipID2
	// CM
	dst[2] = gzipCMDeflate
	// FLG
	dst[3] = 0
	// MTIME
	binary.LittleEndian.PutUint32(dst[4:8], gzipMTimeUnavailable)
	// XFL
	xfl := byte(0)
	compressionLevel := c.Level()
	if compressionLevel < 2 {
		xfl |= gzipXFLFastestCompression
	} else if compressionLevel >= 8 {
		xfl |= gzipXFLSlowestCompression
	}
	dst[8] = xfl
	// OS
	dst[9] = gzipOSUnknown

	// Compressed data
	deflateSize := c.Compress(dst[gzipMinHeaderSize:len(dst)-gzipFooterSize], src)
	if deflateSize == 0 {
		return 0
	}
	outNext := gzipMinHeaderSize + deflateSize

	// CRC32
	binary.LittleEndian.PutUint32(dst[outNext:], crc32.ChecksumIEEE(src))
	outNext += 4

	// ISIZE
	binary.LittleEndian.PutUint32(dst[outNext:], uint32(len(src)))
	outNext += 4

	return outNext
}

// GzipCompressBound returns an upper bound on the CompressGzip size of any
// input of at most n bytes: CompressBound(n) plus the 18 bytes of header and
// trailer. Like CompressBound it may be called on a nil Compressor, panics if
// n < 0 and saturates at math.MaxInt.
// port of libdeflate_gzip_compress_bound
func (c *Compressor) GzipCompressBound(n int) int {
	b := c.CompressBound(n)
	if b > math.MaxInt-gzipMinOverhead {
		return math.MaxInt
	}
	return gzipMinOverhead + b
}
