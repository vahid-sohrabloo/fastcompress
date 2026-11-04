// Package bitstream ports the BIT_CStream_t encoder and the BIT_DStream_t
// decoder from libzstd v1.5.7
// lib/common/bitstream.h (Copyright (c) Meta Platforms, Inc. and affiliates;
// BSD/GPLv2 dual license). The container is 64 bits (C: BitContainerType on
// 64-bit systems), bits are added LSB-first and flushed little-endian.
package bitstream

import (
	"encoding/binary"
	"errors"
	"math/bits"
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

// Raw returns the writer's state for an assembly encoding loop: the bit
// container, its bit count, and the write offset and its clamp (byte
// offsets into Buf()).
func (c *CStream) Raw() (container uint64, bitPos uint, ptr, endPtr int) {
	return c.bitContainer, c.bitPos, c.ptr, c.endPtr
}

// SetRaw restores the state an assembly loop returned (see Raw).
func (c *CStream) SetRaw(container uint64, bitPos uint, ptr int) {
	c.bitContainer, c.bitPos, c.ptr = container, bitPos, ptr
}

// Buf returns the destination buffer passed to Init.
func (c *CStream) Buf() []byte { return c.buf }

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

// DStatus is the result of DStream.Reload. C: bitstream.h:BIT_DStream_status
type DStatus int

const (
	DStreamUnfinished  DStatus = 0 // fully refilled
	DStreamEndOfBuffer DStatus = 1 // still some bits left in bitstream
	DStreamCompleted   DStatus = 2 // bitstream entirely consumed, bit-exact
	DStreamOverflow    DStatus = 3 // user requested more bits than present in bitstream
)

// Errors of DStream.Init, mirroring the C error codes.
var (
	ErrSrcSizeWrong       = errors.New("bitstream: src size wrong")      // ERROR(srcSize_wrong)
	ErrGeneric            = errors.New("bitstream: generic error")       // ERROR(GENERIC)
	ErrCorruptionDetected = errors.New("bitstream: corruption detected") // ERROR(corruption_detected)
)

// DStream is the backward bit reader. C: bitstream.h:BIT_DStream_t. ptr is
// a byte offset into src; C's start is offset 0 and its limitPtr is start +
// 8, i.e. offset 8.
//
// C: bitstream.h:BIT_reloadDStream — on overflow C keeps bitContainer and
// points ptr at a static zero word; here ptr stays where it is. No reader
// can tell: a stream stays in overflow until reset (Reload returns before
// touching ptr), and EndOfDStream is false either way (bitsConsumed > 64).
// The container is kept as in C.
type DStream struct {
	bitContainer uint64
	bitsConsumed uint32
	src          []byte
	ptr          int
}

// Init starts reading src, which must end with the end mark. It returns
// len(src). C: bitstream.h:BIT_initDStream
func (d *DStream) Init(src []byte) (int, error) {
	if len(src) < 1 {
		*d = DStream{}
		return 0, ErrSrcSizeWrong
	}
	d.src = src
	if len(src) >= 8 { // normal case
		d.ptr = len(src) - 8
		d.bitContainer = binary.LittleEndian.Uint64(src[d.ptr:])
		lastByte := src[len(src)-1]
		d.bitsConsumed = 0
		if lastByte != 0 {
			d.bitsConsumed = 8 - uint32(bits.Len8(lastByte)-1) // ensures bitsConsumed is always set
		}
		if lastByte == 0 {
			return 0, ErrGeneric // endMark not present
		}
	} else {
		d.ptr = 0
		d.bitContainer = 0
		for i := len(src) - 1; i >= 0; i-- {
			d.bitContainer += uint64(src[i]) << (8 * uint(i))
		}
		lastByte := src[len(src)-1]
		d.bitsConsumed = 0
		if lastByte != 0 {
			d.bitsConsumed = 8 - uint32(bits.Len8(lastByte)-1)
		}
		if lastByte == 0 {
			return 0, ErrCorruptionDetected // endMark not present
		}
		d.bitsConsumed += uint32(8-len(src)) * 8
	}
	return len(src), nil
}

// LookBits returns the next nbBits bits (nbBits < 32) without consuming
// them. C: bitstream.h:BIT_lookBits (BIT_getMiddleBits, x86-64 form)
func (d *DStream) LookBits(nbBits uint32) uint64 {
	const regMask = 63
	start := 64 - d.bitsConsumed - nbBits
	return (d.bitContainer >> (start & regMask)) & (1<<nbBits - 1)
}

// ReadBits reads and consumes nbBits bits (nbBits < 32).
// C: bitstream.h:BIT_readBits
func (d *DStream) ReadBits(nbBits uint32) uint64 {
	v := d.LookBits(nbBits)
	d.bitsConsumed += nbBits
	return v
}

// Reload refills the container from memory. C: bitstream.h:BIT_reloadDStream
func (d *DStream) Reload() DStatus {
	// note : once in overflow mode, a bitstream remains in this mode until it's reset
	if d.bitsConsumed > 64 {
		return DStreamOverflow // C: ptr = &zeroFilled; overflow detected, no update
	}
	if d.ptr >= 8 { // C: ptr >= limitPtr
		d.ptr -= int(d.bitsConsumed >> 3)
		d.bitsConsumed &= 7
		d.bitContainer = binary.LittleEndian.Uint64(d.src[d.ptr:])
		return DStreamUnfinished
	}
	if d.ptr == 0 {
		// reached end of bitStream => no update
		if d.bitsConsumed < 64 {
			return DStreamEndOfBuffer
		}
		return DStreamCompleted
	}
	// start < ptr < limitPtr => cautious update
	nbBytes := int(d.bitsConsumed >> 3)
	result := DStreamUnfinished
	if d.ptr-nbBytes < 0 {
		nbBytes = d.ptr // ptr > start
		result = DStreamEndOfBuffer
	}
	d.ptr -= nbBytes
	d.bitsConsumed -= uint32(nbBytes) * 8
	d.bitContainer = binary.LittleEndian.Uint64(d.src[d.ptr:]) // reminder : srcSize > 8, otherwise ptr == start
	return result
}

// LookBitsFast is LookBits for nbBits >= 1 (C's form differs for
// bitsConsumed+nbBits > 64). C: bitstream.h:BIT_lookBitsFast
func (d *DStream) LookBitsFast(nbBits uint32) uint64 {
	const regMask = 63
	return (d.bitContainer << (d.bitsConsumed & regMask)) >> ((regMask + 1 - nbBits) & regMask)
}

// ReadBitsFast is ReadBits for nbBits >= 1. C: bitstream.h:BIT_readBitsFast
func (d *DStream) ReadBitsFast(nbBits uint32) uint64 {
	v := d.LookBitsFast(nbBits)
	d.SkipBits(nbBits)
	return v
}

// SkipBits consumes nbBits bits. C: bitstream.h:BIT_skipBits
func (d *DStream) SkipBits(nbBits uint32) { d.bitsConsumed += nbBits }

// ReloadFast is Reload for bitsConsumed <= 64; it returns DStreamOverflow
// once ptr is below limitPtr, where Reload must be used instead.
// C: bitstream.h:BIT_reloadDStreamFast (BIT_reloadDStream_internal)
func (d *DStream) ReloadFast() DStatus {
	if d.ptr < 8 { // C: ptr < limitPtr
		return DStreamOverflow
	}
	d.ptr -= int(d.bitsConsumed >> 3)
	d.bitsConsumed &= 7
	d.bitContainer = binary.LittleEndian.Uint64(d.src[d.ptr:])
	return DStreamUnfinished
}

// EndOfDStream reports whether the stream has exactly reached its end (all
// bits consumed). C: bitstream.h:BIT_endOfDStream
func (d *DStream) EndOfDStream() bool {
	return d.ptr == 0 && d.bitsConsumed == 64
}

// BitsConsumed returns the number of consumed bits of the container.
// C: BIT_DStream_t.bitsConsumed (read directly by HUF_decodeLastSymbolX2)
func (d *DStream) BitsConsumed() uint32 { return d.bitsConsumed }

// SetBitsConsumed sets the number of consumed bits of the container.
// C: BIT_DStream_t.bitsConsumed (set directly by HUF_decodeLastSymbolX2)
func (d *DStream) SetBitsConsumed(n uint32) { d.bitsConsumed = n }

// Raw returns the reader's state, as SetRaw takes it. It is how the zstd
// sequence decoder hands a stream to its assembly loop.
func (d *DStream) Raw() (src []byte, ptr int, container uint64, bitsConsumed uint32) {
	return d.src, d.ptr, d.bitContainer, d.bitsConsumed
}

// SetRaw sets the reader's state directly: src (C: start = src), the read
// offset ptr (0 <= ptr <= len(src)-8), the container and the consumed bit
// count. It is how the HUF fast decoders hand a stream back to the generic
// loop. C: huf_decompress.c:HUF_initRemainingDStream (the field stores)
func (d *DStream) SetRaw(src []byte, ptr int, container uint64, bitsConsumed uint32) {
	d.src, d.ptr, d.bitContainer, d.bitsConsumed = src, ptr, container, bitsConsumed
}
