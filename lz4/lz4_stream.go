package lz4

import (
	"sync"
	"unsafe"
)

// Stream is the state of the fast compressor (C: LZ4_stream_t), for
// streaming compression with LZ4_compress_fast_continue semantics and for
// reusing the hash table across one-shot compressions. The zero value is
// ready to use (C: LZ4_initStream). A Stream must not be copied after use
// and is not safe for concurrent use.
//
// C decides between its prefix and external-dictionary modes by comparing
// pointers: the input continues the history when it starts where the
// history ends. This port does the same, with one restriction: the history
// and the input count as contiguous only when they are parts of one slice
// (the history's capacity reaches the input's end). Two separate
// allocations that happen to be adjacent in memory are compressed as an
// external dictionary, where C would use the prefix mode. Both modes produce
// valid blocks; the output differs from C's only in that case.
//
// A Stream keeps references to the history (the dictionary and the last
// inputs): they must stay unmodified, as in C, until SaveDict copies the
// history or the stream is reset.
type Stream struct {
	c streamInternal
}

// addr returns the address of b's first element (0 for a nil slice).
func addr(b []byte) uintptr { return uintptr(unsafe.Pointer(unsafe.SliceData(b))) }

// Reset clears the stream (C: LZ4_initStream, LZ4_resetStream).
func (s *Stream) Reset() { s.c = streamInternal{} }

// ResetFast prepares the stream for a new independent compression, keeping
// its hash table when that is faster than clearing it (C:
// LZ4_resetStream_fast).
func (s *Stream) ResetFast() { s.c.prepareTable(0, byU32) }

// C: LZ4_loadDict_internal
func (s *Stream) loadDict(dictionary []byte, slow bool) int {
	const tt = byU32
	dict := &s.c
	*dict = streamInternal{}
	dict.currentOffset += 64 * kb
	dict.tableType = tt
	if len(dictionary) < 8 {
		return 0
	}
	if len(dictionary) > 64*kb {
		dictionary = dictionary[len(dictionary)-64*kb:]
	}
	dict.dictionary = dictionary
	dict.dictSize = uint32(len(dictionary))
	idx32 := dict.currentOffset - dict.dictSize
	end := len(dictionary) - 8
	for p := 0; p <= end; p += 3 {
		dict.putIndexOnHash(idx32, hashPosition(dictionary, p, tt), tt)
		idx32 += 3
	}
	if slow {
		idx32 = dict.currentOffset - dict.dictSize
		limit := dict.currentOffset - 64*kb
		for p := 0; p <= end; p++ {
			h := hashPosition(dictionary, p, tt)
			if dict.getIndexOnHash(h, tt) <= limit {
				dict.putIndexOnHash(idx32, h, tt)
			}
			idx32++
		}
	}
	return int(dict.dictSize)
}

// LoadDict resets the stream and loads dictionary (its last 64 KB) as the
// history of the next block (C: LZ4_loadDict). It returns the size loaded.
func (s *Stream) LoadDict(dictionary []byte) int { return s.loadDict(dictionary, false) }

// LoadDictSlow is LoadDict with more references into the dictionary, for a
// better ratio (C: LZ4_loadDictSlow).
func (s *Stream) LoadDictSlow(dictionary []byte) int { return s.loadDict(dictionary, true) }

// AttachDictionary makes dict, a stream that loaded a dictionary, the
// history of the next block without copying its tables (C:
// LZ4_attach_dictionary). dict must not change while it is attached.
func (s *Stream) AttachDictionary(dict *Stream) {
	var dictCtx *streamInternal
	if dict != nil {
		dictCtx = &dict.c
		if s.c.currentOffset == 0 {
			s.c.currentOffset = 64 * kb
			s.c.tableType = byU32
		}
		if dictCtx.dictSize == 0 {
			dictCtx = nil
		}
	}
	s.c.dictCtx = dictCtx
}

// C: LZ4_renormDictT
func (c *streamInternal) renormDictT(nextSize int) {
	if uint64(c.currentOffset)+uint64(nextSize) > 0x80000000 {
		delta := c.currentOffset - 64*kb
		for i := range c.hashTable {
			if c.hashTable[i] < delta {
				c.hashTable[i] = 0
			} else {
				c.hashTable[i] -= delta
			}
		}
		c.currentOffset = 64 * kb
		if c.dictSize > 64*kb {
			if c.dictionary != nil {
				c.dictionary = c.dictionary[c.dictSize-64*kb:]
			}
			c.dictSize = 64 * kb
		}
	}
}

// C: LZ4_isDictSmall
func (c *streamInternal) isDictSmall() bool {
	return c.dictSize < 64*kb && c.dictSize < c.currentOffset
}

// dictCtxCopyThreshold is C's LZ4_DICTCTX_COPY_THRESHOLD.
const dictCtxCopyThreshold = min(hashTableSize/8, 4*kb)

// CompressFastContinue compresses src as the next block of the stream,
// referencing the previous blocks (up to 64 KB) and the dictionary (C:
// LZ4_compress_fast_continue). dst's length is the capacity; it returns the
// compressed size, or 0 if the block does not fit.
func (s *Stream) CompressFastContinue(dst, src []byte, acceleration int) int {
	const tt = byU32
	c := &s.c
	inputSize := len(src)
	var dictEnd uintptr
	if c.dictSize != 0 {
		dictEnd = addr(c.dictionary) + uintptr(c.dictSize)
	}
	if c.dictSize == 0 && c.dictCtx == nil && inputSize >= clearTableThreshold {
		c.prepareTable(inputSize, byU32)
	}
	c.renormDictT(inputSize)
	acceleration = clampAcceleration(acceleration)
	source := addr(src)

	// invalidate tiny dictionaries
	if c.dictSize < 4 && dictEnd != source && inputSize > 0 && c.dictCtx == nil {
		c.dictSize = 0
		c.dictionary = src
		dictEnd = source
	}

	// overlapping input/dictionary space
	{
		sourceEnd := source + uintptr(inputSize)
		dictStart := addr(c.dictionary)
		if sourceEnd > dictStart && source < dictEnd {
			oldSize := uint32(dictEnd - dictStart)
			var n uint32
			if sourceEnd < dictEnd {
				n = uint32(dictEnd - sourceEnd)
			}
			if n > 64*kb {
				n = 64 * kb
			}
			if n < 4 {
				n = 0
			}
			c.dictionary = c.dictionary[oldSize-n:]
			c.dictSize = n
		}
	}

	// prefix mode: src follows the history within one slice
	if dictEnd == source && cap(c.dictionary) >= int(c.dictSize)+inputSize {
		win := c.dictionary[:int(c.dictSize)+inputSize]
		if c.isDictSmall() {
			return c.compressGeneric(win, int(c.dictSize), inputSize, nil, dst, len(dst), limitedOutput, tt, withPrefix64k, dictSmall, acceleration)
		}
		return c.compressGeneric(win, int(c.dictSize), inputSize, nil, dst, len(dst), limitedOutput, tt, withPrefix64k, noDictIssue, acceleration)
	}

	// external dictionary mode
	var result int
	if c.dictCtx != nil {
		if inputSize >= dictCtxCopyThreshold {
			*c = *c.dictCtx
			result = c.compressGeneric(src, 0, inputSize, nil, dst, len(dst), limitedOutput, tt, usingExtDict, noDictIssue, acceleration)
		} else {
			result = c.compressGeneric(src, 0, inputSize, nil, dst, len(dst), limitedOutput, tt, usingDictCtx, noDictIssue, acceleration)
		}
	} else if c.isDictSmall() {
		result = c.compressGeneric(src, 0, inputSize, nil, dst, len(dst), limitedOutput, tt, usingExtDict, dictSmall, acceleration)
	} else {
		result = c.compressGeneric(src, 0, inputSize, nil, dst, len(dst), limitedOutput, tt, usingExtDict, noDictIssue, acceleration)
	}
	c.dictionary = src
	c.dictSize = uint32(inputSize)
	return result
}

// SaveDict copies the last len(buf) bytes of history (at most 64 KB) into
// buf, so the stream no longer references the previous inputs (C:
// LZ4_saveDict). It returns the size saved.
func (s *Stream) SaveDict(buf []byte) int {
	c := &s.c
	dictSize := len(buf)
	if dictSize > 64*kb {
		dictSize = 64 * kb
	}
	if uint32(dictSize) > c.dictSize {
		dictSize = int(c.dictSize)
	}
	if dictSize > 0 {
		hist := c.dictionary[:c.dictSize]
		copy(buf[:dictSize], hist[len(hist)-dictSize:])
	}
	c.dictionary = buf
	c.dictSize = uint32(dictSize)
	return dictSize
}

// CompressFast compresses src into dst as an independent block, reusing the
// stream's tables (C: LZ4_compress_fast_extState_fastReset). The stream must
// not hold an unfinished streaming compression.
func (s *Stream) CompressFast(dst, src []byte, acceleration int) int {
	return s.c.compressFastExtStateFastReset(src, dst, acceleration)
}

// CompressDestSize compresses as much of src as fits into dst (C:
// LZ4_compress_destSize_extState). It returns the compressed size and the
// number of bytes of src consumed. The stream is reset.
func (s *Stream) CompressDestSize(dst, src []byte, acceleration int) (n, consumed int) {
	n, consumed = s.c.compressDestSize(src, dst, acceleration)
	s.c = streamInternal{}
	return n, consumed
}

// C: LZ4_compress_destSize_extState_internal
func (c *streamInternal) compressDestSize(src, dst []byte, acceleration int) (int, int) {
	*c = streamInternal{}
	acceleration = clampAcceleration(acceleration)
	if len(dst) >= CompressBound(len(src)) {
		return c.compressFastExtStateFastReset(src, dst, acceleration), len(src)
	}
	consumed := len(src)
	var r int
	if len(src) < limit64K {
		r = c.compressGeneric(src, 0, len(src), &consumed, dst, len(dst), fillOutput, byU16, noDict, noDictIssue, acceleration)
	} else {
		r = c.compressGeneric(src, 0, len(src), &consumed, dst, len(dst), fillOutput, byU32, noDict, noDictIssue, acceleration)
	}
	return r, consumed
}

var streamPool = sync.Pool{New: func() any { return new(streamInternal) }}

// CompressFast compresses src into dst as an independent block, at
// acceleration a (C: LZ4_compress_fast; values below 1 select 1, larger
// values are faster with a lower ratio). dst's length is the capacity:
// with CompressBound(len(src)) bytes compression never fails. It returns the
// compressed size, or 0 if the block does not fit.
func CompressFast(dst, src []byte, acceleration int) int {
	c := streamPool.Get().(*streamInternal)
	*c = streamInternal{}
	n := c.compressFastExtStateFastReset(src, dst, acceleration)
	c.dictionary = nil
	streamPool.Put(c)
	return n
}

// CompressDefault is CompressFast at acceleration 1 (C:
// LZ4_compress_default).
func CompressDefault(dst, src []byte) int { return CompressFast(dst, src, 1) }

// CompressDestSize compresses as much of src as fits into dst, at
// acceleration 1 (C: LZ4_compress_destSize). It returns the compressed size
// and the number of bytes of src consumed.
func CompressDestSize(dst, src []byte) (n, consumed int) {
	c := streamPool.Get().(*streamInternal)
	n, consumed = c.compressDestSize(src, dst, 1)
	c.dictionary = nil
	streamPool.Put(c)
	return n, consumed
}
