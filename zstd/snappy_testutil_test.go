package zstd

// A test-only Snappy codec written from the public format text
// (google/snappy framing_format.txt, last revised 2013-10-25, and
// format_description.txt, last revised 2011-10-05): framing chunks with
// masked CRC-32C, and the block format's literal and copy elements. The
// encoder is deliberately varied (every element encoding, copies split
// below 3 bytes, chunk kinds) to exercise SnappyConverter, not to compress
// well.

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/rand/v2"
)

const (
	tsChunkCompressed   = 0x00
	tsChunkUncompressed = 0x01
	tsChunkPadding      = 0xfe
	tsChunkStreamID     = 0xff
	tsMaxBlock          = 65536
	// tsMaxChunk: no valid compressed chunk is longer (checksum, 5-byte
	// varint, elements of at most 6 bytes giving at least 1 byte each).
	tsMaxChunk = 4 + 5 + 6*tsMaxBlock
)

var tsCastagnoli = crc32.MakeTable(crc32.Castagnoli)

// tsMaskedCRC is the framing format's masked CRC-32C (section 3).
func tsMaskedCRC(b []byte) uint32 {
	c := crc32.Checksum(b, tsCastagnoli)
	return (c>>15 | c<<17) + 0xa282ead8
}

// tsChunk appends one chunk: type byte, 3-byte little-endian length, data.
func tsChunk(dst []byte, typ byte, data []byte) []byte {
	n := len(data)
	dst = append(dst, typ, byte(n), byte(n>>8), byte(n>>16))
	return append(dst, data...)
}

// tsStreamID appends the stream identifier chunk (section 4.1).
func tsStreamID(dst []byte) []byte { return tsChunk(dst, tsChunkStreamID, []byte("sNaPpY")) }

// tsDataChunk appends a compressed (typ 0x00) or uncompressed (0x01) chunk
// whose CRC covers raw and whose payload is body.
func tsDataChunk(dst []byte, typ byte, raw, body []byte) []byte {
	data := binary.LittleEndian.AppendUint32(nil, tsMaskedCRC(raw))
	return tsChunk(dst, typ, append(data, body...))
}

// tsEncodeOpts selects the stream shape.
type tsEncodeOpts struct {
	seed  uint64
	extra bool // padding and skippable chunks, a repeated stream identifier
}

// tsEncode writes src as one Snappy framed stream. Chunks hold 1..65536
// bytes of src (sizes drawn from the seed); each is compressed or stored.
func tsEncode(src []byte, o tsEncodeOpts) []byte {
	r := rand.New(rand.NewPCG(o.seed, o.seed^0x5a))
	out := tsStreamID(nil)
	for len(src) > 0 {
		n := min(len(src), tsMaxBlock)
		if r.IntN(3) == 0 {
			n = min(n, 1+r.IntN(tsMaxBlock))
		}
		chunk := src[:n]
		src = src[n:]
		if o.extra {
			switch r.IntN(6) {
			case 0:
				out = tsChunk(out, tsChunkPadding, make([]byte, r.IntN(20)))
			case 1:
				out = tsChunk(out, byte(0x80+r.IntN(0x7e)), []byte("skip me"))
			case 2:
				out = tsStreamID(out)
			}
		}
		if r.IntN(5) == 0 {
			out = tsDataChunk(out, tsChunkUncompressed, chunk, chunk)
		} else {
			out = tsDataChunk(out, tsChunkCompressed, chunk, tsEncodeBlock(nil, chunk, r))
		}
	}
	if o.extra {
		out = tsChunk(out, tsChunkPadding, nil)
	}
	return out
}

// tsEncodeBlock appends the Snappy block encoding of src (section 1 and 2):
// the varint length, then literals and copies found by a greedy 4-byte hash
// matcher. r varies the element encodings.
func tsEncodeBlock(dst, src []byte, r *rand.Rand) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(src)))
	var table [1 << 12]int32
	for i := range table {
		table[i] = -1
	}
	hash := func(i int) uint32 { return binary.LittleEndian.Uint32(src[i:]) * 0x1e35a7bd >> 20 }
	lit := 0
	i := 0
	for i+4 <= len(src) {
		h := hash(i)
		cand := int(table[h])
		table[h] = int32(i)
		if cand < 0 || binary.LittleEndian.Uint32(src[cand:]) != binary.LittleEndian.Uint32(src[i:]) {
			i++
			continue
		}
		l := 4
		for i+l < len(src) && src[cand+l] == src[i+l] {
			l++
		}
		dst = tsEmitLiteral(dst, src[lit:i], r)
		dst = tsEmitCopy(dst, i-cand, l, r)
		i += l
		lit = i
	}
	return tsEmitLiteral(dst, src[lit:], r)
}

// tsEmitLiteral appends lit as one or more literal elements (section 2.1),
// sometimes using a longer length form than needed.
func tsEmitLiteral(dst, lit []byte, r *rand.Rand) []byte {
	for len(lit) > 0 {
		n := len(lit)
		if r.IntN(4) == 0 {
			n = 1 + r.IntN(n)
		}
		v := uint32(n - 1)
		switch {
		case n <= 60 && r.IntN(4) != 0:
			dst = append(dst, byte(v<<2))
		default:
			nb := 1 // bytes needed for v
			for v>>(8*nb) != 0 {
				nb++
			}
			nb += r.IntN(5 - nb) // sometimes wider
			dst = append(dst, byte((59+nb)<<2))
			for k := range nb {
				dst = append(dst, byte(v>>(8*k)))
			}
		}
		dst = append(dst, lit[:n]...)
		lit = lit[n:]
	}
	return dst
}

// tsEmitCopy appends a copy of length l at offset off as copy elements
// (section 2.2), split into random pieces (including 1- and 2-byte
// copies) and random encodings.
func tsEmitCopy(dst []byte, off, l int, r *rand.Rand) []byte {
	for l > 0 {
		n := min(l, 64)
		if r.IntN(3) == 0 {
			n = 1 + r.IntN(n)
		}
		switch {
		case n >= 4 && n <= 11 && off < 2048 && r.IntN(2) == 0:
			dst = append(dst, byte(1|(n-4)<<2|(off>>8)<<5), byte(off))
		case off <= 0xFFFF && r.IntN(3) != 0:
			dst = append(dst, byte(2|(n-1)<<2), byte(off), byte(off>>8))
		default:
			dst = append(dst, byte(3|(n-1)<<2))
			dst = binary.LittleEndian.AppendUint32(dst, uint32(off))
		}
		l -= n
	}
	return dst
}

var (
	errTSCorrupt     = errors.New("ts: corrupt")
	errTSTooLarge    = errors.New("ts: too large")
	errTSUnsupported = errors.New("ts: unsupported")
)

// tsDecode decodes a Snappy framed stream; it is the reference for
// SnappyConverter's accept/reject decisions. Empty input is an empty
// stream; anything else must start with the stream identifier.
func tsDecode(src []byte) ([]byte, error) {
	var out []byte
	first := true
	for len(src) > 0 {
		if len(src) < 4 {
			return out, errTSCorrupt
		}
		typ := src[0]
		n := int(src[1]) | int(src[2])<<8 | int(src[3])<<16
		src = src[4:]
		if first && typ != tsChunkStreamID {
			return out, errTSCorrupt
		}
		first = false
		switch {
		case typ >= 0x02 && typ <= 0x7f:
			return out, errTSUnsupported
		case typ == tsChunkUncompressed && n > 4+tsMaxBlock:
			return out, errTSTooLarge
		case typ == tsChunkCompressed && n > tsMaxChunk && len(src) >= tsMaxChunk:
			// Too long to be valid; only its start is looked at, so a
			// too-large length wins over the truncation.
			if _, err := tsDecodeBlock(src[4:tsMaxChunk]); err == errTSTooLarge {
				return out, err
			}
			return out, errTSCorrupt
		}
		if len(src) < n {
			return out, errTSCorrupt
		}
		data := src[:n]
		src = src[n:]
		switch typ {
		case tsChunkStreamID:
			if string(data) != "sNaPpY" {
				return out, errTSCorrupt
			}
		case tsChunkCompressed, tsChunkUncompressed:
			if n < 4 {
				return out, errTSCorrupt
			}
			raw := data[4:]
			if typ == tsChunkCompressed {
				var err error
				if raw, err = tsDecodeBlock(raw); err != nil {
					return out, err
				}
			}
			if tsMaskedCRC(raw) != binary.LittleEndian.Uint32(data) {
				return out, errTSCorrupt
			}
			out = append(out, raw...)
		}
	}
	return out, nil
}

// tsDecodeBlock decodes one Snappy block of at most 65536 bytes.
func tsDecodeBlock(src []byte) ([]byte, error) {
	v, k := binary.Uvarint(src)
	if k <= 0 || k > 5 || v > 1<<32-1 {
		return nil, errTSCorrupt
	}
	if v > tsMaxBlock {
		return nil, errTSTooLarge
	}
	src = src[k:]
	dst := make([]byte, 0, v)
	for len(src) > 0 {
		tag := src[0]
		var l, off uint64
		switch tag & 3 {
		case 0:
			l = uint64(tag >> 2)
			src = src[1:]
			if l >= 60 {
				nb := int(l - 59)
				if len(src) < nb {
					return nil, errTSCorrupt
				}
				l = 0
				for j := range nb {
					l |= uint64(src[j]) << (8 * j)
				}
				src = src[nb:]
			}
			l++
			if l > uint64(len(src)) || l > v-uint64(len(dst)) {
				return nil, errTSCorrupt
			}
			dst = append(dst, src[:l]...)
			src = src[l:]
			continue
		case 1:
			if len(src) < 2 {
				return nil, errTSCorrupt
			}
			l, off = 4+uint64(tag>>2&7), uint64(tag>>5)<<8|uint64(src[1])
			src = src[2:]
		case 2:
			if len(src) < 3 {
				return nil, errTSCorrupt
			}
			l, off = 1+uint64(tag>>2), uint64(binary.LittleEndian.Uint16(src[1:]))
			src = src[3:]
		case 3:
			if len(src) < 5 {
				return nil, errTSCorrupt
			}
			l, off = 1+uint64(tag>>2), uint64(binary.LittleEndian.Uint32(src[1:]))
			src = src[5:]
		}
		if off == 0 || off > uint64(len(dst)) || l > v-uint64(len(dst)) {
			return nil, errTSCorrupt
		}
		for range l {
			dst = append(dst, dst[uint64(len(dst))-off])
		}
	}
	if uint64(len(dst)) != v {
		return nil, errTSCorrupt
	}
	return dst, nil
}
