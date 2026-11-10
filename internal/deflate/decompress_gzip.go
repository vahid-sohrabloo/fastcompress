// Port of libdeflate v1.26 lib/gzip_decompress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): libdeflate_gzip_decompress{,_ex}. The format
// constants are in gzip.go. The CRC-32 is the standard library's hash/crc32
// (IEEE), the same function as libdeflate_crc32.

package deflate

import (
	"encoding/binary"
	"hash/crc32"
)

// gzipDecompress decodes the gzip member at the start of in into out. With
// wantOut false (C: actual_out_nbytes_ret == NULL) the member must
// decompress to exactly len(out) bytes. It returns the result and, on
// success, the size of the member (header, DEFLATE stream and trailer;
// trailing bytes are not counted) and the decompressed size.
//
// As in C: the header CRC (FHCRC) is skipped, not verified; the DEFLATE
// stream is given all input except the last 8 bytes (so it may read the
// trailer, and bytes after it, as stream data); the trailer is read right
// after the stream's last byte and must hold the CRC-32 and size of the
// output, else the result is BAD_DATA.
// port of libdeflate_gzip_decompress_ex
func (d *Decompressor) gzipDecompress(in, out []byte, wantOut bool) (libdeflateResult, int, int) {
	inEnd := len(in)

	if len(in) < gzipMinOverhead {
		return libdeflateBadData, 0, 0
	}

	// ID1
	if in[0] != gzipID1 {
		return libdeflateBadData, 0, 0
	}
	// ID2
	if in[1] != gzipID2 {
		return libdeflateBadData, 0, 0
	}
	// CM
	if in[2] != gzipCMDeflate {
		return libdeflateBadData, 0, 0
	}
	flg := in[3]
	// MTIME (4 bytes), XFL (1 byte) and OS (1 byte) are skipped.
	inNext := gzipMinHeaderSize

	if flg&gzipFRESERVED != 0 {
		return libdeflateBadData, 0, 0
	}

	// Extra field
	if flg&gzipFEXTRA != 0 {
		xlen := int(binary.LittleEndian.Uint16(in[inNext:]))
		inNext += 2

		if inEnd-inNext < xlen+gzipFooterSize {
			return libdeflateBadData, 0, 0
		}

		inNext += xlen
	}

	// Original file name (zero terminated). At least the 8 footer bytes
	// follow inNext here, so the loop reads at least one byte.
	if flg&gzipFNAME != 0 {
		inNext = gzipSkipString(in, inNext)
		if inEnd-inNext < gzipFooterSize {
			return libdeflateBadData, 0, 0
		}
	}

	// File comment (zero terminated)
	if flg&gzipFCOMMENT != 0 {
		inNext = gzipSkipString(in, inNext)
		if inEnd-inNext < gzipFooterSize {
			return libdeflateBadData, 0, 0
		}
	}

	// CRC16 for gzip header (not verified, as in C)
	if flg&gzipFHCRC != 0 {
		inNext += 2
		if inEnd-inNext < gzipFooterSize {
			return libdeflateBadData, 0, 0
		}
	}

	// Compressed data
	res, actualIn, actualOut := d.decompressImpl(in[inNext:inEnd-gzipFooterSize], out, true, wantOut)
	if res != libdeflateSuccess {
		return res, 0, 0
	}

	if !wantOut {
		actualOut = len(out)
	}

	inNext += actualIn

	// CRC32
	if crc32.ChecksumIEEE(out[:actualOut]) != binary.LittleEndian.Uint32(in[inNext:]) {
		return libdeflateBadData, 0, 0
	}
	inNext += 4

	// ISIZE
	if uint32(actualOut) != binary.LittleEndian.Uint32(in[inNext:]) {
		return libdeflateBadData, 0, 0
	}
	inNext += 4

	return libdeflateSuccess, inNext, actualOut
}

// gzipSkipString skips a zero-terminated header string starting at in[i]
// and returns the index after its terminator, or len(in) if the input ends
// first. in[i] must exist.
// C: while (*in_next++ != 0 && in_next != in_end) ;
func gzipSkipString(in []byte, i int) int {
	for {
		b := in[i]
		i++
		if b == 0 || i == len(in) {
			return i
		}
	}
}

// DecompressGzip decompresses the gzip member at the start of src into dst.
// The member must decompress to exactly len(dst) bytes: fewer gives
// ErrShortOutput, more ErrInsufficientSpace; a bad header, CRC-32 or size
// trailer gives ErrBadData. Bytes of src after the member are ignored (only
// the first member is decoded). On success nOut is len(dst); on error it is
// 0 and dst holds undefined data (dst bytes may be written even then).
// port of libdeflate_gzip_decompress(d, in, in_nbytes, out,
// out_nbytes_avail, NULL)
func (d *Decompressor) DecompressGzip(dst, src []byte) (nOut int, err error) {
	res, _, _ := d.gzipDecompress(src, dst, false)
	if res != libdeflateSuccess {
		return 0, res.err()
	}
	return len(dst), nil
}

// DecompressGzipEx decompresses the gzip member at the start of src into
// dst, which may be larger than needed. It returns the size of the member in
// src (nIn; trailing bytes are not counted) and the number of bytes written
// to dst (nOut). ErrShortOutput is never returned. On error nIn and nOut are
// 0 and dst holds undefined data; bytes of dst past nOut may be overwritten.
// port of libdeflate_gzip_decompress_ex(d, in, in_nbytes, out,
// out_nbytes_avail, &actual_in_nbytes, &actual_out_nbytes)
func (d *Decompressor) DecompressGzipEx(dst, src []byte) (nIn, nOut int, err error) {
	res, nIn, nOut := d.gzipDecompress(src, dst, true)
	if res != libdeflateSuccess {
		return 0, 0, res.err()
	}
	return nIn, nOut, nil
}
