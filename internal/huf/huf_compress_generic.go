// Port of libzstd v1.5.7 lib/compress/huf_compress.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the Huffman
// stream writer, always compiled; huf_compress_amd64.s and
// huf_compress_arm64.s have an equivalent encoder.

package huf

import "encoding/binary"

// ---------------------------------------------------------------------------
// Bit stream
// ---------------------------------------------------------------------------

// cstream is HUF_CStream_t: two bit containers built from the top bits down.
// C: huf_compress.c:HUF_CStream_t. Pointers are byte offsets into buf.
type cstream struct {
	container [2]uint64
	pos       [2]uint64 // only the low 8 bits are meaningful, as in C
	buf       []byte
	ptr       int
	endPtr    int
}

// init. C: HUF_initCStream. Returns false where C returns an error.
func (b *cstream) init(dst []byte) bool {
	*b = cstream{}
	b.buf = dst
	b.endPtr = len(dst) - 8
	return len(dst) > 8
}

// addBits adds elt to container idx. C: HUF_addBits with kFast = 0.
func (b *cstream) addBits(elt CElt, idx int) {
	i := idx & 1
	b.container[i] = b.container[i]>>(uint(elt)&63) | uint64(elt&^0xFF)
	b.pos[i] += uint64(elt)
}

// addBitsFast is addBits for a bitstream guaranteed to have at least 4 unused
// bits afterwards: the low 4 bits of elt are or'd in as noise exactly as C
// does. C: HUF_addBits with kFast = 1.
func (b *cstream) addBitsFast(elt CElt, idx int) {
	i := idx & 1
	b.container[i] = b.container[i]>>(uint(elt)&63) | uint64(elt)
	b.pos[i] += uint64(elt)
}

// C: HUF_zeroIndex1
func (b *cstream) zeroIndex1() {
	b.container[1] = 0
	b.pos[1] = 0
}

// mergeIndex1 merges container 1 into container 0. C: HUF_mergeIndex1
func (b *cstream) mergeIndex1() {
	b.container[0] >>= b.pos[1] & 0xFF & 63
	b.container[0] |= b.container[1]
	b.pos[0] += b.pos[1]
}

// flushBits writes the whole bytes of container 0; ptr is clamped to endPtr on
// overflow (revealed by close). C: HUF_flushBits with kFast = 0.
func (b *cstream) flushBits() {
	nbBits := uint(b.pos[0] & 0xFF)
	nbBytes := int(nbBits >> 3)
	bitContainer := b.container[0] >> ((64 - nbBits) & 63)
	b.pos[0] &= 7
	binary.LittleEndian.PutUint64(b.buf[b.ptr:], bitContainer)
	b.ptr += nbBytes
	if b.ptr > b.endPtr {
		b.ptr = b.endPtr
	}
}

// flushBitsFast is flushBits when overflow is known impossible.
// C: HUF_flushBits with kFast = 1.
func (b *cstream) flushBitsFast() {
	nbBits := uint(b.pos[0] & 0xFF)
	nbBytes := int(nbBits >> 3)
	bitContainer := b.container[0] >> ((64 - nbBits) & 63)
	b.pos[0] &= 7
	binary.LittleEndian.PutUint64(b.buf[b.ptr:], bitContainer)
	b.ptr += nbBytes
}

// endMark is a 1-bit value 1. C: HUF_endMark
const endMark = CElt(1) | CElt(1)<<63

// close returns the stream size, or 0 if it did not fit. C: HUF_closeCStream
func (b *cstream) close() int {
	b.addBits(endMark, 0)
	b.flushBits()
	nbBits := b.pos[0] & 0xFF
	if b.ptr >= b.endPtr {
		return 0 // overflow detected
	}
	n := b.ptr
	if nbBits > 0 {
		n++
	}
	return n
}

// compress1XLoop encodes ip backwards into bitC. C:
// HUF_compress1X_usingCTable_internal_body_loop. kUnroll, kFastFlush and
// kLastFast are C template parameters; here they are plain arguments.
func compress1XLoop(bitC *cstream, ip []byte, ct *[SymbolValueMax + 1]CElt, kUnroll int, kFastFlush, kLastFast bool) {
	flush := func() {
		if kFastFlush {
			bitC.flushBitsFast()
		} else {
			bitC.flushBits()
		}
	}
	last := func(elt CElt, idx int) {
		if kLastFast {
			bitC.addBitsFast(elt, idx)
		} else {
			bitC.addBits(elt, idx)
		}
	}

	// Join to kUnroll
	n := len(ip)
	if rem := n % kUnroll; rem > 0 {
		for ; rem > 0; rem-- {
			n--
			bitC.addBits(ct[ip[n]], 0)
		}
		flush()
	}

	// Join to 2 * kUnroll
	if n%(2*kUnroll) != 0 {
		for u := 1; u < kUnroll; u++ {
			bitC.addBitsFast(ct[ip[n-u]], 0)
		}
		last(ct[ip[n-kUnroll]], 0)
		flush()
		n -= kUnroll
	}

	for ; n > 0; n -= 2 * kUnroll {
		// Encode kUnroll symbols into the bitstream @ index 0.
		for u := 1; u < kUnroll; u++ {
			bitC.addBitsFast(ct[ip[n-u]], 0)
		}
		last(ct[ip[n-kUnroll]], 0)
		flush()
		// Encode kUnroll symbols into the bitstream @ index 1. This allows
		// us to start filling the bit container without data dependencies.
		bitC.zeroIndex1()
		for u := 1; u < kUnroll; u++ {
			bitC.addBitsFast(ct[ip[n-kUnroll-u]], 1)
		}
		last(ct[ip[n-kUnroll-kUnroll]], 1)
		// Merge bitstream @ index 1 into the bitstream @ index 0
		bitC.mergeIndex1()
		flush()
	}
}

// tightCompressBound: upper bound on the output needed so no bounds checks
// are needed while encoding. C: HUF_tightCompressBound
func tightCompressBound(srcSize int, tableLog uint) int {
	return (srcSize*int(tableLog))>>3 + 8
}

// compress1XBodyGeneric. C: HUF_compress1X_usingCTable_internal_body (64-bit
// branch: MEM_32bits() is false).
func compress1XBodyGeneric(dst, src []byte, ct CTable) int {
	tableLog := uint(ReadCTableHeader(ct).TableLog)
	elts := (*[SymbolValueMax + 1]CElt)(ct[1:])
	var bitC cstream

	// init
	if len(dst) < 8 {
		return 0 // not enough space to compress
	}
	if !bitC.init(dst) {
		return 0
	}

	if len(dst) < tightCompressBound(len(src), tableLog) || tableLog > 11 {
		compress1XLoop(&bitC, src, elts, 4, false, false)
	} else {
		switch tableLog {
		case 11:
			compress1XLoop(&bitC, src, elts, 5, true, false)
		case 10:
			compress1XLoop(&bitC, src, elts, 5, true, true)
		case 9:
			compress1XLoop(&bitC, src, elts, 6, true, false)
		case 8:
			compress1XLoop(&bitC, src, elts, 7, true, false)
		case 7:
			compress1XLoop(&bitC, src, elts, 8, true, false)
		default: // 6 and below
			compress1XLoop(&bitC, src, elts, 9, true, true)
		}
	}
	return bitC.close()
}
