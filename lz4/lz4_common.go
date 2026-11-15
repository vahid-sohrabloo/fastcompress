package lz4

import (
	"encoding/binary"
	"math/bits"
)

// Constants of lz4.c and lz4.h (lz4 v1.11.1, default build:
// LZ4_MEMORY_USAGE 14, LZ4_DISTANCE_MAX 65535).
const (
	minMatch             = 4  // C: MINMATCH
	lastLiterals         = 5  // C: LASTLITERALS
	mfLimit              = 12 // C: MFLIMIT
	matchSafeguardDist   = 2*8 - minMatch
	fastloopSafeDistance = 64
	minLength            = mfLimit + 1 // C: LZ4_minLength
	distanceMax          = 65535       // C: LZ4_DISTANCE_MAX
	distanceAbsoluteMax  = 65535
	mlBits               = 4
	mlMask               = 1<<mlBits - 1
	runBits              = 8 - mlBits
	runMask              = 1<<runBits - 1

	memoryUsage   = 14
	hashLog       = memoryUsage - 2 // C: LZ4_HASHLOG
	hashTableSize = 1 << memoryUsage
	hashSizeU32   = 1 << hashLog // C: LZ4_HASH_SIZE_U32

	limit64K    = 64*1024 + (mfLimit - 1) // C: LZ4_64Klimit
	skipTrigger = 6                       // C: LZ4_skipTrigger

	// MaxInputSize is the largest input of a block (C: LZ4_MAX_INPUT_SIZE).
	MaxInputSize = 0x7E000000
	// AccelerationDefault and AccelerationMax bound the acceleration of
	// the fast compressor (C: LZ4_ACCELERATION_DEFAULT, LZ4_ACCELERATION_MAX).
	AccelerationDefault = 1
	AccelerationMax     = 65537

	kb = 1 << 10
	gb = 1 << 30
)

// CompressBound returns the largest compressed size of an input of n bytes
// (C: LZ4_compressBound): a destination that large never makes compression
// fail. It returns 0 when n is negative or above MaxInputSize.
func CompressBound(n int) int {
	if n < 0 || n > MaxInputSize {
		return 0
	}
	return n + n/255 + 16
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

// count returns the length of the common prefix of b[in:limit] and
// b[match:] (C: LZ4_count, 64-bit little endian).
func count(b []byte, in, match, limit int) int {
	start := in
	for in < limit-7 {
		if diff := le64(b[in:]) ^ le64(b[match:]); diff != 0 {
			return in - start + bits.TrailingZeros64(diff)>>3
		}
		in += 8
		match += 8
	}
	if in < limit-3 && le32(b[in:]) == le32(b[match:]) {
		in += 4
		match += 4
	}
	if in < limit-1 && le16(b[in:]) == le16(b[match:]) {
		in += 2
		match += 2
	}
	if in < limit && b[match] == b[in] {
		in++
	}
	return in - start
}

// count2 is count with the match in another buffer: the common prefix of
// a[in:limit] and m[match:].
func count2(a []byte, in int, m []byte, match, limit int) int {
	start := in
	for in < limit-7 {
		if diff := le64(a[in:]) ^ le64(m[match:]); diff != 0 {
			return in - start + bits.TrailingZeros64(diff)>>3
		}
		in += 8
		match += 8
	}
	if in < limit-3 && le32(a[in:]) == le32(m[match:]) {
		in += 4
		match += 4
	}
	if in < limit-1 && le16(a[in:]) == le16(m[match:]) {
		in += 2
		match += 2
	}
	if in < limit && m[match] == a[in] {
		in++
	}
	return in - start
}
