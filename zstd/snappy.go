package zstd

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

// Snappy framing format (google/snappy framing_format.txt) and block
// format (format_description.txt) constants.
const (
	snappyChunkCompressed   = 0x00
	snappyChunkUncompressed = 0x01
	snappyChunkStreamID     = 0xff
	snappyMaxBlock          = 65536 // most bytes a data chunk may decode to
	snappyChecksumSize      = 4
	// snappyMaxChunk bounds a valid compressed chunk: the checksum, a varint
	// of at most 5 bytes and elements of at most 6 bytes each (a 1-byte
	// literal with a 4-byte length), each giving at least 1 byte of
	// output. A longer chunk is corrupt, so no more is ever read into
	// memory.
	snappyMaxChunk = snappyChecksumSize + 5 + 6*snappyMaxBlock
	// snappyMaxSeq is the most sequences a block can hold: each covers at
	// least minMatch bytes.
	snappyMaxSeq = snappyMaxBlock / minMatch
	// snappyWindowLog: every offset stays inside its own chunk (at most
	// 65536 bytes), so a 64 KiB window covers them all.
	snappyWindowLog = 16
)

var snappyCastagnoli = crc32.MakeTable(crc32.Castagnoli)

// SnappyConverter can read SnappyConverter-compressed streams and convert
// them to zstd. Conversion is done by converting the stream directly from
// Snappy without intermediate full decoding. Therefore the compression ratio
// is much less than what can be done by a full decompression and compression,
// and a faulty Snappy stream may lead to a faulty Zstandard stream without
// any errors being generated. No CRC value is being generated and not all
// CRC values of the Snappy stream are checked. However, it provides really
// fast recompression of Snappy streams. The converter can be reused to avoid
// allocations, even after errors.
//
// This port decodes each chunk into a scratch buffer to validate it, and
// checks every CRC; a stream it converts without error is a valid zstd
// frame of the stream's content.
//
// Deprecated: Decode the stream with s2.NewReader, which reads Snappy streams,
// and compress the output with an Encoder. This validates the input and
// compresses much better.
type SnappyConverter struct {
	chunk      []byte // chunk data, cap snappyMaxChunk
	dec        []byte // decoded chunk, cap snappyMaxBlock
	out        []byte // frame header and one block
	ss         seqStore
	prev, next *compressedBlockState
	params     cctxParams
	wksp       []byte
}

// init allocates the buffers once and resets the entropy state.
func (r *SnappyConverter) init() {
	if r.chunk == nil {
		r.chunk = make([]byte, snappyMaxChunk)
		r.dec = make([]byte, 0, snappyMaxBlock)
		r.out = make([]byte, frameHeaderSizeMax+blockHeaderSize+snappyMaxBlock)
		r.ss = seqStore{
			sequences: make([]seqDef, 0, snappyMaxSeq),
			literals:  make([]byte, 0, snappyMaxBlock),
			llCode:    make([]byte, snappyMaxSeq),
			mlCode:    make([]byte, snappyMaxSeq),
			ofCode:    make([]byte, snappyMaxSeq),
		}
		r.prev, r.next = new(compressedBlockState), new(compressedBlockState)
		r.params = cctxParams{
			cParams:                compressionParameters{windowLog: snappyWindowLog, strategy: zstdFast},
			literalCompressionMode: psEnable,
		}
		r.wksp = make([]byte, entropyWorkspaceSize)
	}
	resetCompressedBlockState(r.prev)
}

// Convert the Snappy stream supplied in 'in' and write the zStandard stream
// to 'w'. If any error is detected on the Snappy stream it is returned.
// The number of bytes written is returned.
//
// The output is one zstd frame (window 64 KiB, unknown content size, no
// checksum) with one block per Snappy data chunk. Empty input writes
// nothing.
func (r *SnappyConverter) Convert(in io.Reader, w io.Writer) (int64, error) {
	r.init()
	var written int64
	write := func(b []byte) error {
		n, err := w.Write(b)
		written += int64(n)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
		return err
	}
	started := false
	var hdr [4]byte
	for {
		if n, err := io.ReadFull(in, hdr[:]); err != nil {
			if err == io.EOF && n == 0 {
				break
			}
			return written, snappyReadErr(err)
		}
		typ := hdr[0]
		n := int(hdr[1]) | int(hdr[2])<<8 | int(hdr[3])<<16
		if !started && typ != snappyChunkStreamID {
			return written, ErrSnappyCorrupt // the stream identifier comes first
		}
		var block []byte
		switch {
		case typ == snappyChunkStreamID:
			if n != 6 {
				return written, ErrSnappyCorrupt
			}
			if _, err := io.ReadFull(in, r.chunk[:6]); err != nil {
				return written, snappyReadErr(err)
			}
			if string(r.chunk[:6]) != "sNaPpY" {
				return written, ErrSnappyCorrupt
			}
			if !started {
				started = true
				fh := frameHeader{frameContentSize: contentSizeUnknown, windowSize: 1 << snappyWindowLog}
				if err := write(writeFrameHeaderFields(r.out[:0], fh)); err != nil {
					return written, err
				}
			}
			continue
		case typ == snappyChunkUncompressed:
			if n > snappyChecksumSize+snappyMaxBlock {
				return written, ErrSnappyTooLarge
			}
			if n < snappyChecksumSize {
				return written, ErrSnappyCorrupt
			}
			if _, err := io.ReadFull(in, r.chunk[:n]); err != nil {
				return written, snappyReadErr(err)
			}
			raw := r.chunk[snappyChecksumSize:n]
			if snappyMaskedCRC(raw) != binary.LittleEndian.Uint32(r.chunk) {
				return written, ErrSnappyCorrupt
			}
			block = r.rawBlock(raw)
		case typ == snappyChunkCompressed:
			if n < snappyChecksumSize {
				return written, ErrSnappyCorrupt
			}
			m := min(n, snappyMaxChunk)
			if _, err := io.ReadFull(in, r.chunk[:m]); err != nil {
				return written, snappyReadErr(err)
			}
			var err error
			if block, err = r.convertChunk(r.chunk[:m], n > m); err != nil {
				return written, err
			}
		case typ <= 0x7f:
			return written, ErrSnappyUnsupported // reserved unskippable
		default: // padding and reserved skippable chunks
			if c, err := io.CopyN(io.Discard, in, int64(n)); err != nil {
				if err == io.EOF && c < int64(n) {
					err = io.ErrUnexpectedEOF
				}
				return written, snappyReadErr(err)
			}
			continue
		}
		if len(block) > 0 {
			if err := write(block); err != nil {
				return written, err
			}
		}
	}
	if started {
		// The last block is empty: the end is only known at EOF.
		if err := write([]byte{1, 0, 0}); err != nil { // last, raw, size 0
			return written, err
		}
	}
	return written, nil
}

// snappyReadErr maps a read that ended early to ErrSnappyCorrupt (a
// truncated stream); other reader errors are returned as they are.
func snappyReadErr(err error) error {
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return ErrSnappyCorrupt
	}
	return err
}

// snappyMaskedCRC is the framing format's masked CRC-32C.
func snappyMaskedCRC(b []byte) uint32 {
	c := crc32.Checksum(b, snappyCastagnoli)
	return (c>>15 | c<<17) + 0xa282ead8
}

// rawBlock returns raw as a non-last raw zstd block in r.out, or nil when
// raw is empty.
func (r *SnappyConverter) rawBlock(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	n, _ := noCompressBlock(r.out, raw, 0) // r.out holds any chunk
	return r.out[:n]
}

// convertChunk decodes the data of a compressed chunk (checksum, then a
// Snappy block) into r.dec, building the zstd sequences from its elements
// as it goes, and returns the zstd block. tooLong reports that the chunk
// was longer than snappyMaxChunk (data holds only its start).
//
// Each copy of at least minMatch bytes becomes a sequence with offBase =
// offset + repNum (no repcodes); shorter copies become literals. Offsets
// never reach before the chunk, so the 64 KiB window always holds them.
func (r *SnappyConverter) convertChunk(data []byte, tooLong bool) ([]byte, error) {
	src := data[snappyChecksumSize:]
	v, k := binary.Uvarint(src)
	if k <= 0 || k > 5 || v > 1<<32-1 { // at most 5 bytes, 32 bits
		return nil, ErrSnappyCorrupt
	}
	if v > snappyMaxBlock {
		return nil, ErrSnappyTooLarge
	}
	if tooLong {
		return nil, ErrSnappyCorrupt
	}
	src = src[k:]
	dLen := int(v)
	dst := r.dec[:0]
	ss := &r.ss
	resetSeqStore(ss)
	litStart := 0 // first byte of dst not yet in a sequence
	for len(src) > 0 {
		tag := src[0]
		var l, off uint64
		switch tag & 3 {
		case 0: // literal
			l = uint64(tag >> 2)
			src = src[1:]
			if l >= 60 {
				nb := int(l - 59)
				if len(src) < nb {
					return nil, ErrSnappyCorrupt
				}
				l = 0
				for j := range nb {
					l |= uint64(src[j]) << (8 * j)
				}
				src = src[nb:]
			}
			l++
			if l > uint64(len(src)) || l > uint64(dLen-len(dst)) {
				return nil, ErrSnappyCorrupt
			}
			dst = append(dst, src[:l]...)
			src = src[l:]
			continue
		case 1: // copy, 1-byte offset
			if len(src) < 2 {
				return nil, ErrSnappyCorrupt
			}
			l, off = 4+uint64(tag>>2&7), uint64(tag>>5)<<8|uint64(src[1])
			src = src[2:]
		case 2: // copy, 2-byte offset
			if len(src) < 3 {
				return nil, ErrSnappyCorrupt
			}
			l, off = 1+uint64(tag>>2), uint64(binary.LittleEndian.Uint16(src[1:]))
			src = src[3:]
		case 3: // copy, 4-byte offset
			if len(src) < 5 {
				return nil, ErrSnappyCorrupt
			}
			l, off = 1+uint64(tag>>2), uint64(binary.LittleEndian.Uint32(src[1:]))
			src = src[5:]
		}
		pos := len(dst)
		if off == 0 || off > uint64(pos) || l > uint64(dLen-pos) {
			return nil, ErrSnappyCorrupt
		}
		for i := range int(l) {
			dst = append(dst, dst[pos+i-int(off)])
		}
		if l >= minMatch {
			ss.literals = append(ss.literals, dst[litStart:pos]...)
			ss.sequences = append(ss.sequences, seqDef{
				offBase:   offsetToOffBase(uint32(off)),
				litLength: uint16(pos - litStart), // < snappyMaxBlock - minMatch
				mlBase:    uint16(l - minMatch),   // l <= 64
			})
			litStart = len(dst)
		}
	}
	if len(dst) != dLen {
		return nil, ErrSnappyCorrupt
	}
	if snappyMaskedCRC(dst) != binary.LittleEndian.Uint32(data) {
		return nil, ErrSnappyCorrupt
	}
	ss.literals = append(ss.literals, dst[litStart:]...)
	r.dec = dst
	return r.compressBlock(dst)
}

// compressBlock entropy-codes r.ss (the sequences of src) into a non-last
// zstd block in r.out, falling back to a raw block as the encoder does.
// C: zstd_compress.c:ZSTD_compressBlock_internal, without RLE blocks.
func (r *SnappyConverter) compressBlock(src []byte) ([]byte, error) {
	// C: ZSTD_buildSeqStore stores blocks this small raw.
	if len(src) < minCBlockSize+blockHeaderSize+1+1 {
		return r.rawBlock(src), nil
	}
	cSize, err := entropyCompressSeqStore(r.out[blockHeaderSize:], &r.ss, &r.prev.entropy, &r.next.entropy,
		&r.params, len(src), r.wksp, false)
	if err != nil {
		return nil, err
	}
	if cSize > 1 {
		r.prev, r.next = r.next, r.prev // C: confirmRepcodesAndEntropyTables
	}
	if r.prev.entropy.fse.offcodeRepeatMode == fseRepeatValid {
		r.prev.entropy.fse.offcodeRepeatMode = fseRepeatCheck
	}
	if cSize == 0 {
		return r.rawBlock(src), nil
	}
	writeLE24(r.out, btCompressed<<1+uint32(cSize)<<3)
	return r.out[:blockHeaderSize+cSize], nil
}
