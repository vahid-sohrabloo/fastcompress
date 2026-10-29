package zstd

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"sync"
)

// Encoder provides encoding to Zstandard.
// An Encoder can be used for either compressing a stream via the
// io.WriteCloser interface supported by the Encoder or as multiple independent
// tasks via the EncodeAll function.
// Smaller encodes are encouraged to use the EncodeAll function.
// Use NewWriter to create a new instance.
type Encoder struct {
	o     encoderOptions
	cctxs sync.Pool // *cctx configured from o; one per concurrent EncodeAll
}

// NewWriter will create a new Zstandard encoder.
// If the encoder will be used for encoding blocks a nil writer can be used.
func NewWriter(w io.Writer, opts ...EOption) (*Encoder, error) {
	// w is for the streaming Writer, which is not available yet.
	e := &Encoder{}
	e.o.setDefault()
	for _, o := range opts {
		if err := o(&e.o); err != nil {
			return nil, err
		}
	}
	e.cctxs.New = func() any { return e.newCCtx() }
	return e, nil
}

// newCCtx returns a compression context with the Encoder's C parameters.
func (e *Encoder) newCCtx() *cctx {
	c := newCCtx()
	c.setLevel(zstdLevels[e.o.level])
	c.setChecksum(e.o.crc)
	_ = c.setWindowLog(e.o.windowLog) // in range: validated by WithWindowSize
	c.requestedParams.literalCompressionMode = e.o.literalCompressionMode()
	return c
}

// EncodeAll will encode all input in src and append it to dst.
// This function can be called concurrently, but each call will only run on a single goroutine.
// If empty input is given, nothing is returned, unless WithZeroFrames is specified.
// Encoded blocks can be concatenated and the result will be the combined input stream.
// Data compressed with EncodeAll can be decoded with the Decoder,
// using either a stream or DecodeAll.
//
// The frame is C's ZSTD_compress2 output for the mapped parameters (see
// EOption). Calls share a pool of compression contexts; as in C, reusing a
// context does not change the output (at SpeedBetterCompression and
// SpeedBestCompression C's row match finder re-salts its hash on every
// reset, which this port mirrors, but the salt only relabels hash rows), so
// the frame does not depend on which pooled context serves the call.
func (e *Encoder) EncodeAll(src, dst []byte) []byte {
	if len(src) == 0 && !e.o.zeroFrames {
		return dst
	}
	c := e.cctxs.Get().(*cctx)
	dst, err := c.compress2(dst, src)
	if err != nil {
		panic(err) // unreachable with validated options
	}
	e.cctxs.Put(c)
	if e.o.pad > 1 {
		dst = addPadding(dst, e.o.pad)
	}
	return dst
}

// MaxEncodedSize returns the expected maximum size of an encoded block or stream.
func (e *Encoder) MaxEncodedSize(size int) int {
	n := compressBound(size)
	if e.o.pad > 1 {
		n += e.o.pad + skippableHeaderSize
	}
	return n
}

// Skippable frame format. C: zstd.h ZSTD_MAGIC_SKIPPABLE_START,
// ZSTD_SKIPPABLEHEADERSIZE.
const (
	magicSkippableStart = 0x184D2A50
	skippableHeaderSize = 8
)

// addPadding appends a skippable frame of random content so that len(b)
// becomes a multiple of n. A skippable frame is at least its 8-byte header,
// so a shorter gap is widened by multiples of n.
//
// C: no equivalent (klauspost's WithEncoderPadding).
func addPadding(b []byte, n int) []byte {
	add := (n - len(b)%n) % n
	if add == 0 {
		return b
	}
	for add < skippableHeaderSize {
		add += n
	}
	b = binary.LittleEndian.AppendUint32(b, magicSkippableStart)
	b = binary.LittleEndian.AppendUint32(b, uint32(add-skippableHeaderSize))
	n0 := len(b)
	b = append(b, make([]byte, add-skippableHeaderSize)...)
	rand.Read(b[n0:])
	return b
}
