// Port of libdeflate v1.26 lib/adler32.c (Copyright 2016 Eric Biggers, MIT;
// see internal/deflate/NOTICE): adler32_generic (with its ADLER32_CHUNK
// macro) and libdeflate_adler32. The architecture-specific variants of
// lib/x86 and lib/arm compute the same function and are not ported (yet).

// Package adler32 computes the Adler-32 checksum (RFC 1950) the way
// libdeflate's portable implementation does.
package adler32

// Init is the Adler-32 of the empty string, the value to start Update from.
// C: libdeflate_adler32(..., NULL, ...) returns it ("Return initial value").
const Init uint32 = 1

const (
	// The largest prime that is less than 2^16. C: DIVISOR
	divisor = 65521

	// MAX_CHUNK_LEN is the most bytes that can be processed without the
	// possibility of s2 overflowing when it is represented as an unsigned
	// 32-bit integer. This value was computed using the following Python
	// script:
	//
	//	divisor = 65521
	//	count = 0
	//	s1 = divisor - 1
	//	s2 = divisor - 1
	//	while True:
	//		s1 += 0xFF
	//		s2 += s1
	//		if s2 > 0xFFFFFFFF:
	//			break
	//		count += 1
	//	print(count)
	//
	// Note that to get the correct worst-case value, we must assume that
	// every byte has value 0xFF and that s1 and s2 started with the highest
	// possible values modulo the divisor. C: MAX_CHUNK_LEN
	maxChunkLen = 5552
)

// Checksum returns the Adler-32 checksum of p (Update(Init, p)).
func Checksum(p []byte) uint32 { return Update(Init, p) }

// Update returns the Adler-32 checksum of the data whose checksum is adler
// followed by p. As in C, adler is not reduced first: its two halves must be
// below 65521 (as every Adler-32 value is) for the result to be the
// checksum. An empty (or nil) p returns adler unchanged. Unlike C, a nil p
// does not return Init: Go has no NULL buffer distinct from an empty one.
// Update does not allocate.
// port of libdeflate_adler32 / adler32_generic
func Update(adler uint32, p []byte) uint32 {
	s1 := adler & 0xFFFF
	s2 := adler >> 16

	for len(p) != 0 {
		n := min(len(p), maxChunkLen&^3)
		s1, s2 = chunk(s1, s2, p[:n])
		p = p[n:]
	}

	return (s2 << 16) | s1
}

// chunk adds p (at most MAX_CHUNK_LEN bytes) to (s1, s2) and returns them
// reduced modulo DIVISOR. Groups of 4 bytes are summed with s1 added once
// per group (s1_sum) and the per-position byte sums weighted at the end.
// port of ADLER32_CHUNK
func chunk(s1, s2 uint32, p []byte) (uint32, uint32) {
	if len(p) >= 4 {
		var s1Sum, byte0Sum, byte1Sum, byte2Sum, byte3Sum uint32

		for len(p) >= 4 {
			b0, b1, b2, b3 := uint32(p[0]), uint32(p[1]), uint32(p[2]), uint32(p[3])
			s1Sum += s1
			s1 += b0 + b1 + b2 + b3
			byte0Sum += b0
			byte1Sum += b1
			byte2Sum += b2
			byte3Sum += b3
			p = p[4:]
		}
		s2 += (4 * (s1Sum + byte0Sum)) + (3 * byte1Sum) +
			(2 * byte2Sum) + byte3Sum
	}
	for _, b := range p {
		s1 += uint32(b)
		s2 += s1
	}
	s1 %= divisor
	s2 %= divisor
	return s1, s2
}
