// Port of the FSE part of libzstd v1.5.7 lib/common/entropy_common.c
// (Copyright (c) Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual
// license): the normalized-count header reader.

package fse

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// Decoder-side errors, mirroring the C error codes returned at the same points.
var (
	ErrCorruptionDetected = errors.New("fse: corruption detected") // ERROR(corruption_detected)
	ErrMaxSymbolTooLarge  = errors.New("fse: maxSymbol too large") // ERROR(maxSymbolValue_tooLarge)
)

// tableLogAbsoluteMax. C: fse.h:FSE_TABLELOG_ABSOLUTE_MAX
const tableLogAbsoluteMax = 15

// ReadNCount reads a normalized-count header (written by WriteNCount) from
// hb into norm. On entry *maxSV is the largest symbol norm can hold; on
// return the header's largest symbol. It returns the table log and the
// header size. norm[0..*maxSV entry] is zeroed first, so symbols the header
// does not mention have a count of 0.
// C: entropy_common.c:FSE_readNCount (FSE_readNCount_body; the bmi2
// variant computes the same)
func ReadNCount(norm []int16, maxSV *uint, hb []byte) (tableLog uint, size int, err error) {
	if len(hb) < 8 {
		// This function only works when hbSize >= 8
		var buffer [8]byte
		copy(buffer[:], hb)
		tableLog, size, err = ReadNCount(norm, maxSV, buffer[:])
		if err != nil {
			return 0, 0, err
		}
		if size > len(hb) {
			return 0, 0, ErrCorruptionDetected
		}
		return tableLog, size, nil
	}

	iend := len(hb)
	ip := 0
	var charnum uint
	maxSV1 := *maxSV + 1
	previous0 := false
	read := func(p int) uint32 { return binary.LittleEndian.Uint32(hb[p:]) }

	// init
	clear(norm[:*maxSV+1]) // all symbols not present in NCount have a frequency of 0
	bitStream := read(ip)
	nbBits := int(bitStream&0xF) + MinTableLog // extract tableLog
	if nbBits > tableLogAbsoluteMax {
		return 0, 0, ErrTableLogTooLarge
	}
	bitStream >>= 4
	bitCount := 4
	tableLog = uint(nbBits)
	remaining := 1<<nbBits + 1
	threshold := 1 << nbBits
	nbBits++

	for {
		if previous0 {
			// Count the number of repeats. Each time the 2-bit repeat code
			// is 0b11 there is another repeat. Avoid UB by setting the
			// high bit to 1.
			repeats := bits.TrailingZeros32(^bitStream|0x80000000) >> 1
			for repeats >= 12 {
				charnum += 3 * 12
				if ip <= iend-7 {
					ip += 3
				} else {
					bitCount -= 8 * (iend - 7 - ip)
					bitCount &= 31
					ip = iend - 4
				}
				bitStream = read(ip) >> bitCount
				repeats = bits.TrailingZeros32(^bitStream|0x80000000) >> 1
			}
			charnum += uint(3 * repeats)
			bitStream >>= 2 * repeats
			bitCount += 2 * repeats

			// Add the final repeat which isn't 0b11.
			charnum += uint(bitStream & 3)
			bitCount += 2

			// This is an error, but break and return an error at the end,
			// because returning out of a loop makes it harder for the
			// compiler to optimize.
			if charnum >= maxSV1 {
				break
			}

			// We don't need to set the normalized count to 0 because we
			// already memset the whole buffer to 0.

			if ip <= iend-7 || ip+bitCount>>3 <= iend-4 {
				ip += bitCount >> 3
				bitCount &= 7
			} else {
				bitCount -= 8 * (iend - 4 - ip)
				bitCount &= 31
				ip = iend - 4
			}
			bitStream = read(ip) >> bitCount
		}
		{
			maxV := (2*threshold - 1) - remaining
			var count int
			if int(bitStream&uint32(threshold-1)) < maxV {
				count = int(bitStream & uint32(threshold-1))
				bitCount += nbBits - 1
			} else {
				count = int(bitStream & uint32(2*threshold-1))
				if count >= threshold {
					count -= maxV
				}
				bitCount += nbBits
			}

			count-- // extra accuracy
			// When it matters (small blocks), this is a predictable
			// branch, because we don't use -1.
			if count >= 0 {
				remaining -= count
			} else {
				remaining += count
			}
			norm[charnum] = int16(count)
			charnum++
			previous0 = count == 0

			if remaining < threshold {
				// This branch can be folded into the threshold update
				// condition because we know that threshold > 1.
				if remaining <= 1 {
					break
				}
				nbBits = bits.Len32(uint32(remaining)) // C: ZSTD_highbit32(remaining) + 1
				threshold = 1 << (nbBits - 1)
			}
			if charnum >= maxSV1 {
				break
			}

			if ip <= iend-7 || ip+bitCount>>3 <= iend-4 {
				ip += bitCount >> 3
				bitCount &= 7
			} else {
				bitCount -= 8 * (iend - 4 - ip)
				bitCount &= 31
				ip = iend - 4
			}
			bitStream = read(ip) >> bitCount
		}
	}
	if remaining != 1 {
		return 0, 0, ErrCorruptionDetected
	}
	// Only possible when there are too many zeros.
	if charnum > maxSV1 {
		return 0, 0, ErrMaxSymbolTooSmall
	}
	if bitCount > 32 {
		return 0, 0, ErrCorruptionDetected
	}
	*maxSV = charnum - 1

	ip += (bitCount + 7) >> 3
	return tableLog, ip, nil
}
