// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): struct deflate_output_bitstream and the
// ADD_BITS / FLUSH_BITS macros, plus the bitstream setup of
// libdeflate_deflate_compress and the out_fast_end computation of
// deflate_flush_block.

package deflate

import "encoding/binary"

// outputBitstream keeps track of the bits being sent to the compressed
// output buffer. C: struct deflate_output_bitstream, with C's
// deflate_flush_block locals (bitbuf, bitcount, out_next, out_fast_end)
// folded in: C copies the struct into locals for each block because its
// compilers cannot prove that writes through os->next do not alias os; Go
// callers do the same by copying the value into a local variable for the
// duration of a block and storing it back (the methods are small and
// inline, so the fields stay in registers/stack slots).
//
// The output buffer is out; C's next/end pointers are the index next and
// len(out). The writer itself never writes at or beyond len(out): like C,
// it relies on the caller (deflate_flush_block, T2.2) having checked that
// the whole block fits before writing it, and that check is what sets
// overflow (the writer never sets it). A flushBits with too little space
// panics on the slice bound (C: ASSERT(out_next < os->end), undefined
// behaviour in a release build).
type outputBitstream struct {
	// Bits that haven't yet been written to the output buffer. C: bitbuf
	bitbuf uint64

	// Number of bits currently held in bitbuf: 0..outBitbufNbits in
	// general, 0..7 after a flush. C: bitcount
	bitcount uint

	// Index in out at which the next byte is written. C: next / out_next
	next int

	// Whole 8-byte words may be stored at out[next:] while next < fastEnd.
	// C: deflate_flush_block's
	// out_fast_end = os->end - MIN(WORDBYTES - 1, os->end - out_next),
	// computed once per block by setFastEnd.
	fastEnd int

	// The output buffer; its end is C's os->end.
	out []byte

	// True if the output buffer ran out of space. C: overflow
	overflow bool
}

// init sets up the bitstream for writing out from the start, as
// libdeflate_deflate_compress does (bitbuf 0, bitcount 0, next = out,
// end = out + len, overflow false), and computes fastEnd.
func (w *outputBitstream) init(out []byte) {
	*w = outputBitstream{out: out}
	w.setFastEnd()
}

// setFastEnd computes the fast-flush limit for the current next, as
// deflate_flush_block does on entry (C: out_fast_end). It must be called
// again whenever next was moved other than by flushBits (e.g. after
// uncompressed-block bytes were copied), like C recomputes it per block.
func (w *outputBitstream) setFastEnd() {
	end := len(w.out)
	w.fastEnd = end - min(wordBytes-1, end-w.next)
}

// addBits adds the low n bits of bits to the bit buffer. Like C, bits is
// not masked: bits above n must be 0. The caller must ensure that
// bitcount + n <= outBitbufNbits by calling flushBits often enough
// (canBuffer). C: ADD_BITS
func (w *outputBitstream) addBits(bits uint32, n uint) {
	// bitcount <= outBitbufNbits (63), so the mask changes nothing; it
	// only spares Go's guard for shift counts >= 64.
	w.bitbuf |= uint64(bits) << (w.bitcount & 63)
	w.bitcount += n
}

// flushBits writes the whole bytes of the bit buffer to the output; at most
// 7 bits remain. When a whole word fits (next < fastEnd) it is stored
// branchlessly (an unaligned 8-byte little-endian store, of which only
// bitcount>>3 bytes count; the rest is overwritten later); otherwise bytes
// are written one at a time. C: FLUSH_BITS (UNALIGNED_ACCESS_IS_FAST == 1)
func (w *outputBitstream) flushBits() {
	if w.next < w.fastEnd {
		// Flush a whole word (branchlessly).
		binary.LittleEndian.PutUint64(w.out[w.next:], w.bitbuf)
		w.bitbuf >>= w.bitcount & 56 // bitcount <= 63: C's bitcount & ~7
		w.next += int(w.bitcount >> 3)
		w.bitcount &= 7
	} else {
		w.flushBytes()
	}
}

// bitRegs is the part of outputBitstream that the per-symbol output loops
// (writeSequences, writeOptimumPath) update: C's deflate_flush_block locals
// bitbuf, bitcount, out_next and out_fast_end. outputBitstream itself is too
// large for the Go compiler to keep in registers, and so is a local copy of
// it whose methods take its address; this 4-word value with value-receiver
// methods is (each call returns the updated state), like C's ADD_BITS and
// FLUSH_BITS macros on locals. Load it with regs and store it back with
// setRegs.
type bitRegs struct {
	bitbuf   uint64
	bitcount uint
	next     int
	fastEnd  int
}

// regs returns the current bit writer state as a bitRegs.
func (w *outputBitstream) regs() bitRegs {
	return bitRegs{bitbuf: w.bitbuf, bitcount: w.bitcount, next: w.next, fastEnd: w.fastEnd}
}

// setRegs stores r back into w.
func (w *outputBitstream) setRegs(r bitRegs) {
	w.bitbuf, w.bitcount, w.next = r.bitbuf, r.bitcount, r.next
}

// addBits is outputBitstream.addBits on r. C: ADD_BITS
func (r bitRegs) addBits(bits uint32, n uint) bitRegs {
	r.bitbuf |= uint64(bits) << (r.bitcount & 63)
	r.bitcount += n
	return r
}

// flushBits is outputBitstream.flushBits on r, writing to out (the
// bitstream's out). C: FLUSH_BITS
func (r bitRegs) flushBits(out []byte) bitRegs {
	if r.next < r.fastEnd {
		binary.LittleEndian.PutUint64(out[r.next:], r.bitbuf)
		r.bitbuf >>= r.bitcount & 56
		r.next += int(r.bitcount >> 3)
		r.bitcount &= 7
		return r
	}
	return r.flushBytes(out)
}

// flushBytes is outputBitstream.flushBytes on r, kept out of line so that
// flushBits inlines.
func (r bitRegs) flushBytes(out []byte) bitRegs {
	for r.bitcount >= 8 {
		out[r.next] = byte(r.bitbuf)
		r.next++
		r.bitcount -= 8
		r.bitbuf >>= 8
	}
	return r
}

// flushBytes is the byte-at-a-time branch of FLUSH_BITS, kept out of line
// so that flushBits inlines. C: FLUSH_BITS, "Flush a byte at a time"
func (w *outputBitstream) flushBytes() {
	for w.bitcount >= 8 {
		w.out[w.next] = byte(w.bitbuf)
		w.next++
		w.bitcount -= 8
		w.bitbuf >>= 8
	}
}
