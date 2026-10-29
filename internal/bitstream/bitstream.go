// Package bitstream ports the BIT_CStream_t encoder from libzstd v1.5.7
// lib/common/bitstream.h (Copyright (c) Meta Platforms, Inc. and affiliates;
// BSD/GPLv2 dual license). The container is 64 bits (C: BitContainerType on
// 64-bit systems), bits are added LSB-first and flushed little-endian.
package bitstream

import (
	"encoding/binary"
	"errors"
)

// ErrDstSizeTooSmall mirrors ERROR(dstSize_tooSmall).
var ErrDstSizeTooSmall = errors.New("bitstream: dst size too small")

// CStream is the forward bit writer. C: bitstream.h:BIT_CStream_t. ptr and
// endPtr are byte offsets into buf instead of pointers.
type CStream struct {
	bitContainer uint64
	bitPos       uint
	buf          []byte
	ptr          int
	endPtr       int
}

// Init starts a stream writing into dst. dst must be longer than 8 bytes.
// C: bitstream.h:BIT_initCStream
func (c *CStream) Init(dst []byte) error {
	c.bitContainer = 0
	c.bitPos = 0
	c.buf = dst
	c.ptr = 0
	c.endPtr = len(dst) - 8
	if len(dst) <= 8 {
		return ErrDstSizeTooSmall
	}
	return nil
}

// AddBits adds the low n bits of v (n < 32). Does not check register
// overflow. C: bitstream.h:BIT_addBits
func (c *CStream) AddBits(v uint64, n uint) {
	c.bitContainer |= (v & (1<<n - 1)) << c.bitPos
	c.bitPos += n
}

// AddBitsFast is AddBits for a clean v (no bits set above n).
// C: bitstream.h:BIT_addBitsFast
func (c *CStream) AddBitsFast(v uint64, n uint) {
	c.bitContainer |= v << c.bitPos
	c.bitPos += n
}

// FlushBitsFast writes whole bytes to buf without the overflow clamp.
// C: bitstream.h:BIT_flushBitsFast
func (c *CStream) FlushBitsFast() {
	nbBytes := int(c.bitPos >> 3)
	binary.LittleEndian.PutUint64(c.buf[c.ptr:], c.bitContainer)
	c.ptr += nbBytes
	c.bitPos &= 7
	c.bitContainer >>= uint(nbBytes) * 8
}

// FlushBits writes whole bytes to buf, clamping ptr to endPtr on overflow.
// Overflow is revealed later by Close. C: bitstream.h:BIT_flushBits
func (c *CStream) FlushBits() {
	nbBytes := int(c.bitPos >> 3)
	binary.LittleEndian.PutUint64(c.buf[c.ptr:], c.bitContainer)
	c.ptr += nbBytes
	if c.ptr > c.endPtr {
		c.ptr = c.endPtr
	}
	c.bitPos &= 7
	c.bitContainer >>= uint(nbBytes) * 8
}

// Close appends the end mark, flushes and returns the stream size in bytes,
// or 0 if it did not fit. C: bitstream.h:BIT_closeCStream
func (c *CStream) Close() int {
	c.AddBitsFast(1, 1)
	c.FlushBits()
	if c.ptr >= c.endPtr {
		return 0
	}
	n := c.ptr
	if c.bitPos > 0 {
		n++
	}
	return n
}
