package lz4

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/vahid-sohrabloo/fastcompress/internal/xxhash"
)

// Frame decoding (C: LZ4F_decompress and LZ4F_decodeHeader in lz4frame.c,
// lz4 v1.11.1). The header checks run in C's order, so a malformed frame
// gets C's error. This file is a decoder file: no unsafe, no recover.
//
// History semantics are C's: with linked blocks each block may reference
// the dictionary followed by everything decoded so far in the frame
// (uncompressed blocks included); with independent blocks only the
// dictionary.

// frameHeader parses a frame header from b. need > 0 asks for that many
// bytes in b (input too short to decide).
func frameHeader(b []byte) (info FrameInfo, maxBlockSize, hSize, need int, err error) {
	if len(b) < minFHSize {
		return info, 0, 0, minFHSize, nil
	}
	magic := binary.LittleEndian.Uint32(b)
	if magic&0xFFFFFFF0 == skippableMagic {
		if len(b) < 8 {
			return info, 0, 0, 8, nil
		}
		info.Skippable = true
		info.ContentSize = uint64(binary.LittleEndian.Uint32(b[4:]))
		return info, 0, 8, 0, nil
	}
	if magic != frameMagic {
		return info, 0, 0, 0, ErrFrameType
	}
	flg := b[4]
	if (flg>>1)&1 != 0 {
		return info, 0, 0, 0, ErrReservedFlag
	}
	if (flg>>6)&3 != 1 {
		return info, 0, 0, 0, ErrHeaderVersion
	}
	contentSizeFlag := (flg >> 3) & 1
	dictIDFlag := flg & 1
	hSize = minFHSize + 8*int(contentSizeFlag) + 4*int(dictIDFlag)
	if len(b) < hSize {
		return info, 0, 0, hSize, nil
	}
	bd := b[5]
	blockSizeID := (bd >> 4) & 7
	if (bd>>7)&1 != 0 {
		return info, 0, 0, 0, ErrReservedFlag
	}
	if blockSizeID < 4 {
		return info, 0, 0, 0, ErrMaxBlockSize
	}
	if bd&0x0F != 0 {
		return info, 0, 0, 0, ErrReservedFlag
	}
	if headerChecksum(b[4:hSize-1]) != b[hSize-1] {
		return info, 0, 0, 0, ErrHeaderChecksum
	}
	info.BlockIndependent = (flg>>5)&1 != 0
	info.BlockChecksum = (flg>>4)&1 != 0
	info.ContentChecksum = (flg>>2)&1 != 0
	info.BlockSizeID = BlockSizeID(blockSizeID)
	maxBlockSize, _ = BlockSize(info.BlockSizeID)
	if contentSizeFlag != 0 {
		info.ContentSize = binary.LittleEndian.Uint64(b[6:])
	}
	if dictIDFlag != 0 {
		info.DictID = binary.LittleEndian.Uint32(b[hSize-5:])
	}
	return info, maxBlockSize, hSize, 0, nil
}

// decodeFrameBlock decodes the compressed block blk into out[pos:pos+max]
// with the history out[lowPrefix:pos] preceded by dict.
func decodeFrameBlock(out []byte, pos, maxBlockSize, lowPrefix int, dict, blk []byte) (int, error) {
	dd := noDict
	if len(dict) > 0 {
		dd = usingExtDict
	}
	n, err := decompressBlock(blk, out, pos, maxBlockSize, dd, lowPrefix, dict)
	if err != nil {
		return 0, ErrDecompressionFailed
	}
	return n, nil
}

var scratchPool sync.Pool

// decodeViaScratch decodes blk with the history dict followed by prefix
// (its last 64 KB) into a temporary buffer and passes the output to emit.
func decodeViaScratch(prefix, dict, blk []byte, maxBlockSize int, emit func([]byte)) (int, error) {
	need := 64*kb + maxBlockSize
	bp, _ := scratchPool.Get().(*[]byte)
	if bp == nil || len(*bp) < need {
		b := make([]byte, need)
		bp = &b
	}
	buf := *bp
	h := 0
	if len(prefix) >= 64*kb {
		h = copy(buf, prefix[len(prefix)-64*kb:])
	} else {
		d := min(len(dict), 64*kb-len(prefix))
		h = copy(buf, dict[len(dict)-d:])
		h += copy(buf[h:], prefix)
	}
	n, err := decodeFrameBlock(buf, h, maxBlockSize, 0, nil, blk)
	if err == nil {
		emit(buf[h : h+n])
	}
	scratchPool.Put(bp)
	return n, err
}

// AppendDecompressFrame decodes all the frames of src (concatenated frames,
// skippable frames skipped) and appends the content to dst (C: a loop of
// LZ4F_decompress_usingDict over src). dict is the dictionary the frames
// were compressed with, or nil. Corrupt or truncated input returns an
// error (io.ErrUnexpectedEOF when src ends within a frame) with dst holding
// the content decoded so far.
func AppendDecompressFrame(dst, src, dict []byte) ([]byte, error) {
	var xxh xxhash.Digest32
	for len(src) > 0 {
		info, maxBlockSize, hSize, need, err := frameHeader(src)
		if err != nil {
			return dst, err
		}
		if need > 0 {
			return dst, io.ErrUnexpectedEOF
		}
		src = src[hSize:]
		if info.Skippable {
			if uint64(len(src)) < info.ContentSize {
				return dst, io.ErrUnexpectedEOF
			}
			src = src[info.ContentSize:]
			continue
		}
		frameStart := len(dst)
		remaining := info.ContentSize
		if info.ContentSize > 0 && info.ContentSize <= 1<<30 && cap(dst)-len(dst) < int(info.ContentSize) {
			nd := make([]byte, len(dst), len(dst)+int(info.ContentSize)+maxBlockSize)
			copy(nd, dst)
			dst = nd
		}
		if info.ContentChecksum {
			xxh.Reset(0)
		}
		crcSize := bfSize * b2i(info.BlockChecksum)
		for {
			if len(src) < bhSize {
				return dst, io.ErrUnexpectedEOF
			}
			bh := binary.LittleEndian.Uint32(src)
			src = src[bhSize:]
			if bh == 0 {
				break
			}
			size := int(bh & 0x7FFFFFFF)
			if size > maxBlockSize {
				return dst, ErrMaxBlockSize
			}
			if len(src) < size+crcSize {
				if bh&blockUncompressed != 0 {
					// C streams uncompressed blocks: the bytes present are output
					n := min(size, len(src))
					dst = append(dst, src[:n]...)
				}
				return dst, io.ErrUnexpectedEOF
			}
			blk := src[:size]
			if info.BlockChecksum && xxhash.Sum32(blk, 0) != binary.LittleEndian.Uint32(src[size:]) {
				if bh&blockUncompressed != 0 {
					dst = append(dst, blk...)
				}
				return dst, ErrBlockChecksum
			}
			src = src[size+crcSize:]
			var decoded []byte
			if bh&blockUncompressed != 0 {
				pos := len(dst)
				dst = append(dst, blk...)
				decoded = dst[pos:]
			} else {
				pos := len(dst)
				lowPrefix := frameStart
				if info.BlockIndependent {
					lowPrefix = pos
				}
				if cap(dst)-pos >= maxBlockSize {
					n, err := decodeFrameBlock(dst[:cap(dst)], pos, maxBlockSize, lowPrefix, dict, blk)
					if err != nil {
						return dst, err
					}
					dst = dst[:pos+n]
				} else {
					// not enough room for a whole block: as C (dstage_flushOut),
					// decode into a buffer after a copy of the history, then append
					n, err := decodeViaScratch(dst[lowPrefix:pos], dict, blk, maxBlockSize, func(b []byte) { dst = append(dst, b...) })
					if err != nil {
						return dst, err
					}
					dst = dst[:pos+n]
				}
				decoded = dst[pos:]
			}
			if info.ContentChecksum {
				xxh.Write(decoded)
			}
			if info.ContentSize != 0 {
				remaining -= uint64(len(decoded))
			}
		}
		if remaining != 0 {
			return dst, ErrFrameSize
		}
		if info.ContentChecksum {
			if len(src) < 4 {
				return dst, io.ErrUnexpectedEOF
			}
			if binary.LittleEndian.Uint32(src) != xxh.Sum32() {
				return dst, ErrContentChecksum
			}
			src = src[4:]
		}
	}
	return dst, nil
}

// Reader decompresses a stream of frames (C: LZ4F_decompress driven by an
// io.Reader). Concatenated frames are decoded in sequence and skippable
// frames skipped.
type Reader struct {
	r    io.Reader
	dict []byte
	err  error

	inFrame      bool
	info         FrameInfo
	maxBlockSize int
	remaining    uint64
	xxh          xxhash.Digest32

	buf      []byte // history (last 64 KB at most) then the current block
	pos      int    // end of decoded data in buf
	histLen  int    // decoded bytes of the frame available as history before pos, dictionary included
	outStart int    // start of the data not yet returned
	cbuf     []byte // compressed block
	hdr      [maxFHSize]byte
}

// NewReader returns a Reader decoding the frames read from r.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// NewReaderDict is NewReader for frames compressed with dict.
func NewReaderDict(r io.Reader, dict []byte) *Reader {
	if len(dict) > 64*kb {
		dict = dict[len(dict)-64*kb:]
	}
	return &Reader{r: r, dict: dict}
}

// Reset makes z read from r, keeping its dictionary and buffers.
func (z *Reader) Reset(r io.Reader) {
	*z = Reader{r: r, dict: z.dict, buf: z.buf, cbuf: z.cbuf}
}

// FrameInfo returns the parameters of the current frame (valid after the
// first Read of the frame).
func (z *Reader) FrameInfo() FrameInfo { return z.info }

func (z *Reader) readFull(b []byte) error {
	_, err := io.ReadFull(z.r, b)
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// startFrame reads the next frame header; io.EOF at a frame boundary.
func (z *Reader) startFrame() error {
	n, err := io.ReadFull(z.r, z.hdr[:minFHSize])
	if err == io.EOF && n == 0 {
		return io.EOF
	}
	if err != nil {
		return io.ErrUnexpectedEOF
	}
	have := minFHSize
	for {
		info, maxBlockSize, hSize, need, err := frameHeader(z.hdr[:have])
		if err != nil {
			return err
		}
		if need > 0 {
			if err := z.readFull(z.hdr[have:need]); err != nil {
				return err
			}
			have = need
			continue
		}
		_ = hSize
		if info.Skippable {
			if _, err := io.CopyN(io.Discard, z.r, int64(info.ContentSize)); err != nil {
				if err == io.EOF {
					return io.ErrUnexpectedEOF
				}
				return err
			}
			return z.startFrame()
		}
		z.info = info
		z.maxBlockSize = maxBlockSize
		z.remaining = info.ContentSize
		z.xxh.Reset(0)
		z.inFrame = true
		need = 64*kb + 2*maxBlockSize
		if len(z.buf) < need {
			z.buf = make([]byte, need)
		}
		if len(z.cbuf) < maxBlockSize+bfSize {
			z.cbuf = make([]byte, maxBlockSize+bfSize)
		}
		z.pos = copy(z.buf, z.dict)
		z.histLen = z.pos
		z.outStart = z.pos
		return nil
	}
}

// nextBlock decodes the next block into buf (or ends the frame).
func (z *Reader) nextBlock() error {
	var bh [4]byte
	if err := z.readFull(bh[:]); err != nil {
		return err
	}
	h := binary.LittleEndian.Uint32(bh[:])
	if h == 0 {
		z.inFrame = false
		if z.remaining != 0 {
			return ErrFrameSize
		}
		if z.info.ContentChecksum {
			if err := z.readFull(bh[:]); err != nil {
				return err
			}
			if binary.LittleEndian.Uint32(bh[:]) != z.xxh.Sum32() {
				return ErrContentChecksum
			}
		}
		return nil
	}
	size := int(h & 0x7FFFFFFF)
	if size > z.maxBlockSize {
		return ErrMaxBlockSize
	}
	crcSize := bfSize * b2i(z.info.BlockChecksum)
	blk := z.cbuf[:size+crcSize]
	if err := z.readFull(blk); err != nil {
		return err
	}
	if z.info.BlockChecksum && xxhash.Sum32(blk[:size], 0) != binary.LittleEndian.Uint32(blk[size:]) {
		return ErrBlockChecksum
	}
	blk = blk[:size]

	// keep up to 64 KB of history before the block
	if z.info.BlockIndependent {
		z.pos = copy(z.buf, z.dict)
		z.histLen = z.pos
	} else if z.pos+z.maxBlockSize > len(z.buf) {
		keep := min(z.pos, 64*kb)
		copy(z.buf, z.buf[z.pos-keep:z.pos])
		z.pos = keep
		z.histLen = min(z.histLen, keep)
	}
	var n int
	if h&blockUncompressed != 0 {
		n = copy(z.buf[z.pos:], blk)
	} else {
		var err error
		n, err = decodeFrameBlock(z.buf, z.pos, z.maxBlockSize, z.pos-z.histLen, nil, blk)
		if err != nil {
			return err
		}
	}
	z.outStart = z.pos
	z.pos += n
	z.histLen += n
	if z.info.ContentChecksum {
		z.xxh.Write(z.buf[z.outStart:z.pos])
	}
	if z.info.ContentSize != 0 {
		z.remaining -= uint64(n)
	}
	return nil
}

// Read implements io.Reader.
func (z *Reader) Read(p []byte) (int, error) {
	for {
		if z.outStart < z.pos {
			n := copy(p, z.buf[z.outStart:z.pos])
			z.outStart += n
			return n, nil
		}
		if z.err != nil {
			return 0, z.err
		}
		if !z.inFrame {
			z.err = z.startFrame()
		} else {
			z.err = z.nextBlock()
		}
		if z.err != nil && z.outStart < z.pos {
			continue
		}
	}
}

// WriteTo implements io.WriterTo.
func (z *Reader) WriteTo(w io.Writer) (int64, error) {
	var total int64
	for {
		if z.outStart < z.pos {
			n, err := w.Write(z.buf[z.outStart:z.pos])
			total += int64(n)
			z.outStart += n
			if err != nil {
				return total, err
			}
		}
		if z.err != nil {
			if z.err == io.EOF {
				return total, nil
			}
			return total, z.err
		}
		if !z.inFrame {
			z.err = z.startFrame()
		} else {
			z.err = z.nextBlock()
		}
	}
}
