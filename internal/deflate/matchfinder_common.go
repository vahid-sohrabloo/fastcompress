// Port of libdeflate v1.26 lib/matchfinder_common.h (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): MATCHFINDER_WINDOW_ORDER / _SIZE / _INITVAL,
// mf_pos_t, matchfinder_init, matchfinder_rebase, lz_hash and lz_extend,
// with the portable semantics of lib/x86/matchfinder_impl.h and
// lib/arm/matchfinder_impl.h (their SIMD init/rebase compute the same
// values: a fill, and a subtraction of the window size with signed
// saturation).

package deflate

import (
	"encoding/binary"
	"math/bits"
)

// Matchfinder window. C: MATCHFINDER_WINDOW_ORDER (= DEFLATE_WINDOW_ORDER,
// set by deflate_compress.c), MATCHFINDER_WINDOW_SIZE
const (
	matchfinderWindowOrder = deflateWindowOrder
	matchfinderWindowSize  = 1 << matchfinderWindowOrder
)

// mfPos is a matchfinder position: an offset relative to the current window
// base (in_base), in -MATCHFINDER_WINDOW_SIZE..MATCHFINDER_WINDOW_SIZE-1.
// C: mf_pos_t (s16)
type mfPos = int16

// The "no position" value: one full window before the base, so always out
// of range. C: MATCHFINDER_INITVAL ((mf_pos_t)-MATCHFINDER_WINDOW_SIZE)
const matchfinderInitval mfPos = -matchfinderWindowSize

// C's MATCHFINDER_MEM_ALIGNMENT (32: the AVX-2 init/rebase need 32-byte
// aligned buffers) and MATCHFINDER_SIZE_ALIGNMENT (1024: every matchfinder
// buffer size is a multiple of it, so the SIMD loops need no tail) have no
// effect on the output; the port keeps only the size property as a
// compile-time check of the hash table sizes (see ht_matchfinder.go).
const matchfinderSizeAlignment = 1024

// Every matchfinder table has a multiple of 32 entries, so the SSE2
// init/rebase (32 entries per iteration) cover each one entirely and the Go
// tail loops in matchfinderInit and matchfinderRebase do nothing for them.
const (
	_ = uint(0 - len(htMatchfinder{}.hashTab)%32)
	_ = uint(0 - len(hcMatchfinder{}.hash3Tab)%32)
	_ = uint(0 - len(hcMatchfinder{}.hash4Tab)%32)
	_ = uint(0 - len(hcMatchfinder{}.nextTab)%32)
	_ = uint(0 - len(btMatchfinder{}.hash3Tab)%32)
	_ = uint(0 - len(btMatchfinder{}.hash4Tab)%32)
	_ = uint(0 - len(btMatchfinder{}.childTab)%32)
)

// matchfinderInit initializes the hash table portion of a matchfinder:
// every entry is set to MATCHFINDER_INITVAL. With the SSE2 assembly
// (matchfinder_amd64.s) all but the last len(data)%32 entries are set by
// it. port of matchfinder_init
func matchfinderInit(data []mfPos) {
	if useMatchfinderSSE2 {
		n := len(data) &^ 31
		matchfinderInitSSE2(data[:n])
		data = data[n:]
	}
	for i := range data {
		data[i] = matchfinderInitval
	}
}

// matchfinderRebase slides the matchfinder by MATCHFINDER_WINDOW_SIZE
// bytes. It must be called just after each MATCHFINDER_WINDOW_SIZE bytes
// have been run through the matchfinder.
//
// It subtracts MATCHFINDER_WINDOW_SIZE from each entry, making the entries
// relative to the current position rather than the position
// MATCHFINDER_WINDOW_SIZE bytes prior. To avoid integer underflows, entries
// that would become less than -MATCHFINDER_WINDOW_SIZE stay at
// -MATCHFINDER_WINDOW_SIZE, keeping them permanently out of bounds.
//
// C's branchless version for 32768-byte windows: clear all bits if the
// value was already negative, then set the sign bit, which is subtracting
// 32768 with signed saturation (what the x86 PADDSW / ARM VQADDQ versions
// compute). With the SSE2 assembly (matchfinder_amd64.s: PADDSW, as C's
// x86 build) all but the last len(data)%32 entries are rebased by it.
// port of matchfinder_rebase
func matchfinderRebase(data []mfPos) {
	if useMatchfinderSSE2 {
		n := len(data) &^ 31
		matchfinderRebaseSSE2(data[:n])
		data = data[n:]
	}
	for i, v := range data {
		data[i] = mfPos(uint16(v&^(v>>15)) | 0x8000)
	}
}

// The window size the branchless rebase assumes. C: the
// 'MATCHFINDER_WINDOW_SIZE == 32768' branch of matchfinder_rebase
const _ = uint(matchfinderWindowSize-32768) + uint(32768-matchfinderWindowSize)

// lzHash is the hash function: given a sequence prefix held in the
// low-order bits of a 32-bit value, multiply by a carefully-chosen large
// constant. Discard any bits of the product that don't fit in a 32-bit
// value, but take the next-highest numBits bits of the product as the hash
// value, as those have the most randomness. port of lz_hash
func lzHash(seq uint32, numBits uint) uint32 {
	return (seq * 0x1E35A7BD) >> (32 - numBits)
}

// loadLE32 loads the 4 bytes at in[i:] as a little-endian value. The
// matchfinders compare such values only for equality, so this is also C's
// load_u32_unaligned (native order). C: get_unaligned_le32,
// load_u32_unaligned
//
// The bounded slice in[i:i+4] costs two compares and no pointer masking
// (in[i:] costs four compares plus the masking), but it is checked against
// cap(in), not len(in): the compressors run on an input whose capacity is
// clamped to its length (Compress and CompressChunk), so a read past the
// input still panics there.
func loadLE32(in []byte, i int) uint32 {
	return binary.LittleEndian.Uint32(in[i : i+4])
}

// loadLE64 is loadLE32 for the 8 bytes at in[i:] (C: load_word_unaligned;
// the same capacity note applies).
func loadLE64(in []byte, i int) uint64 {
	return binary.LittleEndian.Uint64(in[i : i+8])
}

// lzExtend returns the number of bytes at in[matchptr:] that match the
// bytes at in[strptr:], up to a maximum of maxLen; startLen bytes are
// already known to match (startLen <= maxLen, matchptr < strptr,
// strptr+maxLen <= len(in)).
//
// C compares a machine word at a time (4 words unrolled when at least
// 4*WORDBYTES bytes may match, then single words) and locates the first
// differing byte with bsfw on little-endian CPUs; this port always uses
// 64-bit words (the result does not depend on the word size). Like C it
// advances the two positions instead of re-slicing the input.
// port of lz_extend
func lzExtend(in []byte, strptr, matchptr int, startLen, maxLen uint32) uint32 {
	n := int(startLen)
	end := int(maxLen)

	if end-n >= 4*wordBytes {
		for range 4 {
			if v := loadLE64(in, matchptr+n) ^ loadLE64(in, strptr+n); v != 0 {
				return uint32(n + bits.TrailingZeros64(v)>>3) // C: word_differs
			}
			n += wordBytes
		}
	}
	for n+wordBytes <= end {
		if v := loadLE64(in, matchptr+n) ^ loadLE64(in, strptr+n); v != 0 {
			return uint32(n + bits.TrailingZeros64(v)>>3) // C: word_differs
		}
		n += wordBytes
	}
	for n < end && in[matchptr+n] == in[strptr+n] {
		n++
	}
	return uint32(n)
}
