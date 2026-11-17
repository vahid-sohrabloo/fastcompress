// Port of libdeflate v1.26 lib/zlib_decompress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): libdeflate_zlib_decompress{,_ex}. The format
// constants are in zlib.go.

package deflate

import (
	"encoding/binary"

	"github.com/vahid-sohrabloo/fastcompress/internal/adler32"
)

// zlibDecompress decodes the zlib stream at the start of in into out. With
// wantOut false (C: actual_out_nbytes_ret == NULL) the stream must
// decompress to exactly len(out) bytes. It returns the result and, on
// success, the size of the zlib stream (trailing bytes are not counted) and
// the decompressed size.
//
// As in C: the DEFLATE stream is given all input except the last 4 bytes;
// the Adler-32 is read right after the stream's last byte; a preset
// dictionary (FDICT) is BAD_DATA; the level hint (FLEVEL) is ignored.
// port of libdeflate_zlib_decompress_ex
func (d *Decompressor) zlibDecompress(in, out []byte, wantOut bool) (libdeflateResult, int, int) {
	if len(in) < zlibMinOverhead {
		return libdeflateBadData, 0, 0
	}

	// 2 byte header: CMF and FLG
	hdr := binary.BigEndian.Uint16(in)
	inNext := zlibMinHeaderSize

	// FCHECK
	if hdr%31 != 0 {
		return libdeflateBadData, 0, 0
	}

	// CM
	if (hdr>>8)&0xF != zlibCMDeflate {
		return libdeflateBadData, 0, 0
	}

	// CINFO
	if hdr>>12 > zlibCINFO32KWindow {
		return libdeflateBadData, 0, 0
	}

	// FDICT
	if (hdr>>5)&1 != 0 {
		return libdeflateBadData, 0, 0
	}

	// Compressed data
	res, actualIn, actualOut := d.decompressImpl(in[inNext:len(in)-zlibFooterSize], out, true, wantOut)
	if res != libdeflateSuccess {
		return res, 0, 0
	}

	if !wantOut {
		actualOut = len(out)
	}

	inNext += actualIn

	// ADLER32
	if adler32.Checksum(out[:actualOut]) != binary.BigEndian.Uint32(in[inNext:]) {
		return libdeflateBadData, 0, 0
	}
	inNext += 4

	return libdeflateSuccess, inNext, actualOut
}

// DecompressZlib decompresses the zlib stream at the start of src into dst.
// The stream must decompress to exactly len(dst) bytes: fewer gives
// ErrShortOutput, more ErrInsufficientSpace; a bad header or Adler-32 gives
// ErrBadData. Bytes of src after the stream are ignored. On success nOut is
// len(dst); on error it is 0 and dst holds undefined data (dst bytes may be
// written even then).
// port of libdeflate_zlib_decompress(d, in, in_nbytes, out,
// out_nbytes_avail, NULL)
func (d *Decompressor) DecompressZlib(dst, src []byte) (nOut int, err error) {
	res, _, _ := d.zlibDecompress(src, dst, false)
	if res != libdeflateSuccess {
		return 0, res.err()
	}
	return len(dst), nil
}

// DecompressZlibEx decompresses the zlib stream at the start of src into
// dst, which may be larger than needed. It returns the size of the stream in
// src (nIn; trailing bytes are not counted) and the number of bytes written
// to dst (nOut). ErrShortOutput is never returned. On error nIn and nOut are
// 0 and dst holds undefined data; bytes of dst past nOut may be overwritten.
// port of libdeflate_zlib_decompress_ex(d, in, in_nbytes, out,
// out_nbytes_avail, &actual_in_nbytes, &actual_out_nbytes)
func (d *Decompressor) DecompressZlibEx(dst, src []byte) (nIn, nOut int, err error) {
	res, nIn, nOut := d.zlibDecompress(src, dst, true)
	if res != libdeflateSuccess {
		return 0, 0, res.err()
	}
	return nIn, nOut, nil
}
