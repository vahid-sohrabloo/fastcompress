package xxhash

import (
	"encoding/binary"
	"math/bits"
)

// XXH32, ported from lz4 v1.11.1 lib/xxhash.c (xxHash, Copyright (c) Yann
// Collet, BSD-2-Clause), for the lz4 frame checksums.

const (
	prime32_1 uint32 = 2654435761
	prime32_2 uint32 = 2246822519
	prime32_3 uint32 = 3266489917
	prime32_4 uint32 = 668265263
	prime32_5 uint32 = 374761393
)

// C: XXH32_round
func round32(acc, input uint32) uint32 {
	acc += input * prime32_2
	acc = bits.RotateLeft32(acc, 13)
	return acc * prime32_1
}

// C: XXH32_finalize and XXH32_avalanche. len(p) < 16.
func finalize32(h uint32, p []byte) uint32 {
	for len(p) >= 4 {
		h += binary.LittleEndian.Uint32(p) * prime32_3
		h = bits.RotateLeft32(h, 17) * prime32_4
		p = p[4:]
	}
	for _, b := range p {
		h += uint32(b) * prime32_5
		h = bits.RotateLeft32(h, 11) * prime32_1
	}
	h ^= h >> 15
	h *= prime32_2
	h ^= h >> 13
	h *= prime32_3
	h ^= h >> 16
	return h
}

// stripes32 runs the 16-byte stripes of p and returns the remainder.
func stripes32(v *[4]uint32, p []byte) []byte {
	v0, v1, v2, v3 := v[0], v[1], v[2], v[3]
	for len(p) >= 16 {
		v0 = round32(v0, binary.LittleEndian.Uint32(p))
		v1 = round32(v1, binary.LittleEndian.Uint32(p[4:]))
		v2 = round32(v2, binary.LittleEndian.Uint32(p[8:]))
		v3 = round32(v3, binary.LittleEndian.Uint32(p[12:]))
		p = p[16:]
	}
	v[0], v[1], v[2], v[3] = v0, v1, v2, v3
	return p
}

// Sum32 returns the XXH32 hash of b with seed (C: XXH32).
func Sum32(b []byte, seed uint32) uint32 {
	var h uint32
	n := uint32(len(b))
	if len(b) >= 16 {
		v := [4]uint32{seed + prime32_1 + prime32_2, seed + prime32_2, seed, seed - prime32_1}
		b = stripes32(&v, b)
		h = bits.RotateLeft32(v[0], 1) + bits.RotateLeft32(v[1], 7) + bits.RotateLeft32(v[2], 12) + bits.RotateLeft32(v[3], 18)
	} else {
		h = seed + prime32_5
	}
	h += n
	return finalize32(h, b)
}

// Digest32 computes XXH32 incrementally (C: XXH32_state_t).
type Digest32 struct {
	v        [4]uint32
	mem      [16]byte
	memsize  int
	totalLen uint64
	seed     uint32
}

// Reset starts a new hash with seed (C: XXH32_reset).
func (d *Digest32) Reset(seed uint32) {
	*d = Digest32{seed: seed}
	d.v = [4]uint32{seed + prime32_1 + prime32_2, seed + prime32_2, seed, seed - prime32_1}
}

// Write adds p to the hash; it never fails (C: XXH32_update).
func (d *Digest32) Write(p []byte) (int, error) {
	n := len(p)
	d.totalLen += uint64(n)
	if d.memsize+n < 16 {
		copy(d.mem[d.memsize:], p)
		d.memsize += n
		return n, nil
	}
	if d.memsize > 0 {
		k := copy(d.mem[d.memsize:], p)
		stripes32(&d.v, d.mem[:])
		p = p[k:]
		d.memsize = 0
	}
	p = stripes32(&d.v, p)
	if len(p) > 0 {
		d.memsize = copy(d.mem[:], p)
	}
	return n, nil
}

// Sum32 returns the hash of everything written so far (C: XXH32_digest).
func (d *Digest32) Sum32() uint32 {
	var h uint32
	if d.totalLen >= 16 {
		h = bits.RotateLeft32(d.v[0], 1) + bits.RotateLeft32(d.v[1], 7) + bits.RotateLeft32(d.v[2], 12) + bits.RotateLeft32(d.v[3], 18)
	} else {
		h = d.seed + prime32_5
	}
	h += uint32(d.totalLen)
	return finalize32(h, d.mem[:d.memsize])
}
