// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): deflate_compress_none, the level 0 (and
// passthrough) "compressor".

package deflate

import (
	"encoding/binary"
	"math"
)

// deflateCompressNone outputs in as uncompressed blocks of at most
// UINT16_MAX bytes each (the last one final) and returns the output size,
// or 0 if out is too small (out may have been partly written then). An
// empty input still needs one (empty, final) block to be a valid stream.
// port of deflate_compress_none
func deflateCompressNone(in, out []byte) int {
	// If the input is zero-length, we still must output a block in order
	// for the output to be a valid DEFLATE stream.
	if len(in) == 0 {
		if len(out) < 5 {
			return 0
		}
		// BFINAL and BTYPE
		out[0] = 1 | (deflateBlocktypeUncompressed << 1)
		// LEN and NLEN
		binary.LittleEndian.PutUint32(out[1:], 0xFFFF0000)
		return 5
	}

	inNext, outNext := 0, 0
	for {
		bfinal := byte(0)
		ln := math.MaxUint16

		if len(in)-inNext <= math.MaxUint16 {
			bfinal = 1
			ln = len(in) - inNext
		}
		if len(out)-outNext < 5+ln {
			return 0
		}
		// Output BFINAL and BTYPE. The stream is already byte-aligned
		// here, so this step always requires outputting exactly 1 byte.
		out[outNext] = bfinal | (deflateBlocktypeUncompressed << 1)

		// Output LEN and NLEN, then the data itself.
		binary.LittleEndian.PutUint16(out[outNext+1:], uint16(ln))
		binary.LittleEndian.PutUint16(out[outNext+3:], ^uint16(ln))
		copy(out[outNext+5:outNext+5+ln], in[inNext:inNext+ln])
		outNext += 5 + ln
		inNext += ln
		if inNext == len(in) {
			break
		}
	}
	return outNext
}
