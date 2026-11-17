// Not a port: the chunked-compression and Huffman-only extensions used by
// the streaming Writers of flate/, gzip/ and zlib/ (plan decisions D1, D6).
// libdeflate is one-shot; everything here is new design built on the ported
// parsers, which take part through the two Compressor fields histLen and
// nonFinal (each use is marked "deviation from C" in the parser files).
//
// Design:
//   - A chunk is compressed by the level's own parser over one contiguous
//     buffer history+src. The parser first runs the history through its
//     matchfinder without output ("priming": hc/ht skip_bytes, bt
//     skip_byte, from position 0 so C's window/rebase arithmetic is
//     unchanged), then parses src. Matches may reach into the history.
//     Nothing else carries over between calls, so a chunk's output depends
//     only on (level, history, src, final).
//   - A non-final chunk sets BFINAL on no block and ends with an empty
//     stored block (a sync flush: 3 header bits, padding to a byte, 00 00 FF
//     FF), so every chunk output starts and ends on a byte boundary and
//     chunk outputs are simply concatenated.
//   - With no history and final == true the code path is exactly Compress's,
//     so the output is byte-identical to libdeflate.

package deflate

import "math"

// HuffmanOnly is the Level of a NewHuffmanOnlyCompressor (the value of
// compress/flate's HuffmanOnly constant).
const HuffmanOnly = -2

// syncMarkerMaxLen is the size of the empty stored block CompressChunk
// appends to a non-final chunk: at most one byte to finish the partial
// byte with the 3 header bits, one more if they do not fit, and LEN/NLEN.
const syncMarkerMaxLen = 5

// CompressChunk compresses src as the continuation of a raw DEFLATE stream
// and returns the number of bytes written to dst, or 0 if dst is too small
// (dst may have been written then). history holds the uncompressed bytes
// that precede src in the stream (only its last 32 KiB are used; nil for
// none): matches may reference it, it is not output.
//
// With final == true the last block is marked final and the output padded
// to a byte; CompressChunk(dst, nil, src, true) is byte-identical to
// Compress(dst, src). With final == false no block is marked final and the
// output ends with an empty stored block (sync flush), so it ends on a byte
// boundary and the next chunk's output can simply be appended; for an empty
// src only that block (00 00 00 FF FF) is output. An empty src with
// final == true gives one final empty stored block, like Compress.
//
// A dst of CompressChunkBound(len(src)) bytes is always large enough.
// history and src may be adjacent in one buffer (history directly followed
// by src), which avoids a copy; otherwise they are copied into a buffer
// kept by the Compressor (allocated on first use, reused afterwards). dst
// must not overlap either. CompressChunk does not otherwise allocate.
func (c *Compressor) CompressChunk(dst, history, src []byte, final bool) int {
	if len(history) > matchfinderWindowSize {
		history = history[len(history)-matchfinderWindowSize:]
	}

	// For extremely short inputs, or for compression level 0, just output
	// uncompressed blocks (Compress's rule; the history is not needed).
	if len(src) <= c.maxPassthroughSize {
		if final {
			return deflateCompressNone(src, dst)
		}
		return c.compressNoneNonFinal(dst, src)
	}

	in := c.chunkInput(history, src)
	os := &c.os
	os.init(dst)
	c.histLen = len(history)
	c.nonFinal = !final
	// Deferred so that not even a panic leaves Compress a stale state.
	defer c.resetChunkState()
	// The capacity is clamped so that the matchfinders' bounded loads
	// (loadLE32) cannot read past the input.
	c.impl(c, in[:len(in):len(in)], os)

	n := c.finishChunk(final)
	os.out = nil
	return n
}

// resetChunkState restores the zero values that make the impls run C's
// whole-buffer code.
func (c *Compressor) resetChunkState() {
	c.histLen = 0
	c.nonFinal = false
}

// chunkInput returns history followed by src as one slice: the caller's
// buffer when src directly follows history in it, else a copy in
// c.chunkBuf.
func (c *Compressor) chunkInput(history, src []byte) []byte {
	if len(history) == 0 {
		return src
	}
	h := len(history)
	if cap(history)-h >= len(src) && &history[:h+1][h] == &src[0] {
		return history[:h+len(src)]
	}
	need := h + len(src)
	if cap(c.chunkBuf) < need {
		c.chunkBuf = make([]byte, need)
	}
	in := c.chunkBuf[:need]
	copy(in, history)
	copy(in[h:], src)
	return in
}

// finishChunk completes c.os after the impl ran: 0 on overflow, else the
// final partial byte (final) or the sync-flush block (non-final) is
// written and the output size returned.
func (c *Compressor) finishChunk(final bool) int {
	os := &c.os
	if os.overflow {
		return 0
	}
	if final {
		// As Compress: flushBlock checked that the partial byte fits.
		if os.bitcount != 0 {
			os.out[os.next] = byte(os.bitbuf)
			os.next++
		}
		return os.next
	}
	// The 3 header bits after os.bitcount bits, rounded up to a byte, then
	// LEN and NLEN.
	if len(os.out)-os.next < (int(os.bitcount)+3+7)/8+4 {
		return 0
	}
	c.writeUncompressedBlocks(os, nil, false)
	return os.next
}

// compressNoneNonFinal is deflateCompressNone with no block marked final,
// followed by the sync-flush block (src may be empty: only that block).
func (c *Compressor) compressNoneNonFinal(dst, src []byte) int {
	blocks := (len(src) + math.MaxUint16 - 1) / math.MaxUint16
	if len(dst) < len(src)+5*blocks+syncMarkerMaxLen {
		return 0
	}
	os := &c.os
	os.init(dst)
	if len(src) > 0 {
		c.writeUncompressedBlocks(os, src, false)
	}
	c.writeUncompressedBlocks(os, nil, false)
	n := os.next
	os.out = nil
	return n
}

// CompressChunkBound returns an upper bound on the CompressChunk output for
// any src of at most n bytes, final or not, for every level:
// CompressBound(n) plus the sync-flush block (math.MaxInt if that
// overflows). Only the last block of a chunk can be shorter than
// MIN_BLOCK_LENGTH, as in a Compress stream, so CompressBound's argument
// holds for the blocks. It panics if n < 0; the receiver may be nil.
func (c *Compressor) CompressChunkBound(n int) int {
	b := c.CompressBound(n)
	if b > math.MaxInt-syncMarkerMaxLen {
		return math.MaxInt
	}
	return b + syncMarkerMaxLen
}

// NewHuffmanOnlyCompressor returns a compressor that uses no matches: every
// block holds literals only (Huffman-coded with the block's own dynamic
// code, the static code or stored, whichever flushBlock finds cheapest), as
// compress/flate's HuffmanOnly level. Blocks end at SOFT_MAX_BLOCK_LENGTH
// (choose_max_block_end); inputs of up to 55 bytes are stored (C's
// passthrough formula at level 0). Not in libdeflate, so there is no byte
// identity to compare against. Level reports HuffmanOnly (-2).
func NewHuffmanOnlyCompressor() *Compressor {
	c := new(Compressor)
	c.huffmanOnly = true
	c.maxPassthroughSize = 55
	c.impl = deflateCompressHuffmanOnly
	c.initStaticCodes()
	return c
}

// deflateCompressHuffmanOnly is the compress() implementation of
// NewHuffmanOnlyCompressor: all-literal blocks through the ported
// deflate_choose_all_literals and deflate_flush_block. The history (if
// any) is skipped: there are no matches to reach into it.
func deflateCompressHuffmanOnly(c *Compressor, in []byte, os *outputBitstream) {
	inNext := c.histLen
	inEnd := len(in)
	for {
		inBlockBegin := inNext
		inNext = chooseMaxBlockEnd(inBlockBegin, inEnd, softMaxBlockLength)
		block := in[inBlockBegin:inNext]
		c.chooseAllLiterals(block)
		c.huffOnlySeq[0].litrunlenAndLength = uint32(len(block))
		c.flushBlock(os, block, c.huffOnlySeq[:], inNext == inEnd && !c.nonFinal)
		if inNext == inEnd || os.overflow {
			break
		}
	}
}
