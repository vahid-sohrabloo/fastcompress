// Package xxhash is a port of XXH64 from libzstd v1.5.7 lib/common/xxhash.h
// (xxHash, Copyright (c) Yann Collet, BSD-2-Clause). Only XXH64 is provided.
package xxhash

import (
	"encoding/binary"
	"math/bits"
)

const (
	prime64_1 uint64 = 0x9E3779B185EBCA87
	prime64_2 uint64 = 0xC2B2AE3D27D4EB4F
	prime64_3 uint64 = 0x165667B19E3779F9
	prime64_4 uint64 = 0x85EBCA77C2B2AE63
	prime64_5 uint64 = 0x27D4EB2F165667C5
)

// C: xxhash.h:XXH64_round
func round(acc, input uint64) uint64 {
	acc += input * prime64_2
	acc = bits.RotateLeft64(acc, 31)
	acc *= prime64_1
	return acc
}

// C: xxhash.h:XXH64_mergeRound
func mergeRound(acc, val uint64) uint64 {
	val = round(0, val)
	acc ^= val
	return acc*prime64_1 + prime64_4
}

// C: xxhash.h:XXH64_avalanche
func avalanche(h uint64) uint64 {
	h ^= h >> 33
	h *= prime64_2
	h ^= h >> 29
	h *= prime64_3
	h ^= h >> 32
	return h
}

// C: xxhash.h:XXH64_finalize. p holds the trailing bytes; len(p) is already
// reduced modulo 32 by the caller (C does len &= 31).
func finalize(h uint64, p []byte) uint64 {
	p = p[:len(p)&31]
	for len(p) >= 8 {
		k1 := round(0, binary.LittleEndian.Uint64(p))
		p = p[8:]
		h ^= k1
		h = bits.RotateLeft64(h, 27)*prime64_1 + prime64_4
	}
	if len(p) >= 4 {
		h ^= uint64(binary.LittleEndian.Uint32(p)) * prime64_1
		p = p[4:]
		h = bits.RotateLeft64(h, 23)*prime64_2 + prime64_3
	}
	for _, b := range p {
		h ^= uint64(b) * prime64_5
		h = bits.RotateLeft64(h, 11) * prime64_1
	}
	return avalanche(h)
}

// Sum64 returns XXH64(b, seed).
// C: xxhash.h:XXH64_endian_align (unaligned variant; Go reads are alignment-agnostic).
func Sum64(b []byte, seed uint64) uint64 {
	n := len(b)
	var h uint64
	if n >= 32 {
		v1 := seed + prime64_1 + prime64_2
		v2 := seed + prime64_2
		v3 := seed
		v4 := seed - prime64_1
		for len(b) >= 32 {
			v1 = round(v1, binary.LittleEndian.Uint64(b))
			v2 = round(v2, binary.LittleEndian.Uint64(b[8:]))
			v3 = round(v3, binary.LittleEndian.Uint64(b[16:]))
			v4 = round(v4, binary.LittleEndian.Uint64(b[24:]))
			b = b[32:]
		}
		h = bits.RotateLeft64(v1, 1) + bits.RotateLeft64(v2, 7) + bits.RotateLeft64(v3, 12) + bits.RotateLeft64(v4, 18)
		h = mergeRound(h, v1)
		h = mergeRound(h, v2)
		h = mergeRound(h, v3)
		h = mergeRound(h, v4)
	} else {
		h = seed + prime64_5
	}
	h += uint64(n)
	return finalize(h, b)
}

// Digest is a streaming XXH64 state. C: xxhash.h:XXH64_state_t
type Digest struct {
	v        [4]uint64
	totalLen uint64
	mem      [32]byte
	memsize  int
}

// New returns a Digest seeded with seed.
func New(seed uint64) *Digest {
	d := &Digest{}
	d.init(seed)
	return d
}

// C: xxhash.h:XXH64_reset
func (d *Digest) init(seed uint64) {
	*d = Digest{}
	d.v[0] = seed + prime64_1 + prime64_2
	d.v[1] = seed + prime64_2
	d.v[2] = seed
	d.v[3] = seed - prime64_1
}

// Reset restores d to the state New(0) returns.
func (d *Digest) Reset() { d.init(0) }

// Write adds p to the hash; it never fails. C: XXH64_update
func (d *Digest) Write(p []byte) (int, error) {
	n := len(p)
	d.totalLen += uint64(n)
	if d.memsize+n < 32 {
		copy(d.mem[d.memsize:], p)
		d.memsize += n
		return n, nil
	}
	if d.memsize > 0 {
		k := copy(d.mem[d.memsize:], p)
		d.v[0] = round(d.v[0], binary.LittleEndian.Uint64(d.mem[0:]))
		d.v[1] = round(d.v[1], binary.LittleEndian.Uint64(d.mem[8:]))
		d.v[2] = round(d.v[2], binary.LittleEndian.Uint64(d.mem[16:]))
		d.v[3] = round(d.v[3], binary.LittleEndian.Uint64(d.mem[24:]))
		p = p[k:]
		d.memsize = 0
	}
	if len(p) >= 32 {
		v0, v1, v2, v3 := d.v[0], d.v[1], d.v[2], d.v[3]
		for len(p) >= 32 {
			v0 = round(v0, binary.LittleEndian.Uint64(p))
			v1 = round(v1, binary.LittleEndian.Uint64(p[8:]))
			v2 = round(v2, binary.LittleEndian.Uint64(p[16:]))
			v3 = round(v3, binary.LittleEndian.Uint64(p[24:]))
			p = p[32:]
		}
		d.v[0], d.v[1], d.v[2], d.v[3] = v0, v1, v2, v3
	}
	if len(p) > 0 {
		copy(d.mem[:], p)
		d.memsize = len(p)
	}
	return n, nil
}

// Sum64 returns the hash of everything written so far without changing d.
// C: XXH64_digest
func (d *Digest) Sum64() uint64 {
	var h uint64
	if d.totalLen >= 32 {
		h = bits.RotateLeft64(d.v[0], 1) + bits.RotateLeft64(d.v[1], 7) + bits.RotateLeft64(d.v[2], 12) + bits.RotateLeft64(d.v[3], 18)
		h = mergeRound(h, d.v[0])
		h = mergeRound(h, d.v[1])
		h = mergeRound(h, d.v[2])
		h = mergeRound(h, d.v[3])
	} else {
		h = d.v[2] + prime64_5 // v[2] holds the seed until 32 bytes are consumed
	}
	h += d.totalLen
	return finalize(h, d.mem[:d.memsize])
}
