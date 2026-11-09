package zlib

import (
	"fmt"

	"github.com/vahid-sohrabloo/fastzstd/flate"
	"github.com/vahid-sohrabloo/fastzstd/internal/appendbuf"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflate"
)

// Compression levels, the same as package flate's (and the standard
// library's compress/zlib for NoCompression..DefaultCompression).
const (
	NoCompression      = flate.NoCompression
	BestSpeed          = flate.BestSpeed
	BestCompression    = flate.BestCompression
	DefaultCompression = flate.DefaultCompression

	Level10  = flate.Level10
	Level11  = flate.Level11
	Level12  = flate.Level12
	MaxLevel = flate.MaxLevel
)

// DefaultMaxOutput is the output limit of AppendDecompress on a new
// Decompressor (1 GiB); see Decompressor.SetMaxOutput.
const DefaultMaxOutput = flate.DefaultMaxOutput

// The errors are package flate's (the same values, so errors.Is matches
// across the three packages).
var (
	// ErrInvalidLevel is returned by NewCompressor for a level outside
	// DefaultCompression..MaxLevel (-1..12). The returned error wraps it.
	ErrInvalidLevel = flate.ErrInvalidLevel

	// ErrShortBuffer is returned by Compress when dst is too small for the
	// compressed data.
	ErrShortBuffer = flate.ErrShortBuffer

	// ErrBadData is returned when the data is not a valid zlib stream: a bad
	// header, invalid DEFLATE data or a wrong Adler-32.
	ErrBadData = flate.ErrBadData

	// ErrShortOutput is returned by Decompress when the data decompresses
	// to fewer than len(dst) bytes.
	ErrShortOutput = flate.ErrShortOutput

	// ErrInsufficientSpace is returned when the data decompresses to more
	// than len(dst) bytes.
	ErrInsufficientSpace = flate.ErrInsufficientSpace

	// ErrOutputLimit is returned by AppendDecompress when the data
	// decompresses to more bytes than the Decompressor's output limit
	// (SetMaxOutput). It wraps ErrInsufficientSpace.
	ErrOutputLimit = flate.ErrOutputLimit
)

// CompressBound returns an upper bound on the compressed size of any input
// of at most n bytes, for every level: a dst of that size never gives
// ErrShortBuffer. It panics if n < 0.
func CompressBound(n int) int {
	return (*deflate.Compressor)(nil).ZlibCompressBound(n)
}

// A Compressor compresses whole buffers to zlib streams (RFC 1950) at one
// level: a 2-byte header (32 KiB window, no dictionary, the level hint),
// the raw DEFLATE data of package flate, then the big-endian Adler-32. Its
// output is byte-identical to libdeflate v1.26's libdeflate_zlib_compress at
// the same level.
//
// The zero value is not usable (its methods panic); create Compressors
// with NewCompressor. A Compressor is not safe for concurrent use; use one
// per goroutine, or a sync.Pool. It holds the level's match-finder state
// (see the package documentation for the size per level) and is meant to
// be reused.
//
// The output buffer must not overlap src: in-place compression is not
// supported and silently produces invalid data.
type Compressor struct {
	c *deflate.Compressor
}

// NewCompressor returns a Compressor for level, which must be in
// DefaultCompression..MaxLevel (-1..12). DefaultCompression is level 6.
// Any other level returns an error wrapping ErrInvalidLevel.
func NewCompressor(level int) (*Compressor, error) {
	c, err := deflate.NewCompressor(level)
	if err != nil {
		return nil, fmt.Errorf("%w %d (want %d..%d)", err, level, DefaultCompression, MaxLevel)
	}
	return &Compressor{c: c}, nil
}

// Level returns the effective compression level: 6 for a Compressor created
// with DefaultCompression, else the level it was created with.
func (c *Compressor) Level() int { return c.c.Level() }

// Compress compresses src into dst as a zlib stream and returns the
// compressed size. If dst is too small it returns 0 and ErrShortBuffer; a
// dst of CompressBound(len(src)) bytes is always large enough. Bytes of dst
// past the returned size (all of dst on error) may have been overwritten.
// dst must not overlap src. Compress does not allocate.
func (c *Compressor) Compress(dst, src []byte) (n int, err error) {
	n = c.c.CompressZlib(dst, src)
	if n == 0 {
		return 0, ErrShortBuffer
	}
	return n, nil
}

// CompressBound returns CompressBound(n) (the bound is the same for every
// level).
func (c *Compressor) CompressBound(n int) int { return CompressBound(n) }

// AppendCompress appends the compressed form of src to dst and returns the
// extended slice. It makes room for CompressBound(len(src)) bytes after
// len(dst) (growing dst only if its capacity is too small), so it never
// returns ErrShortBuffer; the bytes of dst before len(dst) are not
// modified. The whole capacity of dst, not only dst[:len(dst)], must not
// overlap src: the compressed data is written into the spare capacity.
// The returned error is always nil; it is there so that the signature
// matches AppendDecompress.
func (c *Compressor) AppendCompress(dst, src []byte) ([]byte, error) {
	return appendbuf.Compress(c.c, appendbuf.Zlib, dst, src, CompressBound(len(src))), nil
}

// A Decompressor decompresses whole-buffer zlib streams (RFC 1950). Streams
// that need a preset dictionary (FDICT) are rejected with ErrBadData, as
// libdeflate does; the Adler-32 trailer is verified. It accepts and rejects
// exactly the inputs libdeflate v1.26's libdeflate_zlib_decompress{,_ex}
// does, with the same output and the same consumed input.
//
// Unlike a Compressor's, the zero value is ready to use (with
// DefaultMaxOutput as the AppendDecompress limit), as is NewDecompressor's
// result. A Decompressor is not safe for concurrent use; it holds about
// 13 KiB of decode tables and is meant to be reused.
//
// The output buffer must not overlap src: in-place decompression is not
// supported and fails with ErrBadData or silently produces wrong output.
type Decompressor struct {
	d     deflate.Decompressor
	limit appendbuf.Limit // AppendDecompress output limit; 0 means DefaultMaxOutput
}

// NewDecompressor returns a new Decompressor.
func NewDecompressor() *Decompressor { return new(Decompressor) }

// SetMaxOutput sets the largest output, in bytes, AppendDecompress will
// produce (and allocate for) in one call; more fails with ErrOutputLimit.
// It protects against decompression bombs: a small input can decompress to
// about 1032 times its size. n <= 0 restores DefaultMaxOutput. The limit
// does not apply to Decompress and DecompressEx, whose output is bounded by
// len(dst).
func (d *Decompressor) SetMaxOutput(n int) {
	d.limit.Set(n)
}

// MaxOutput returns the AppendDecompress output limit.
func (d *Decompressor) MaxOutput() int {
	return d.limit.Get()
}

// Decompress decompresses the zlib stream at the start of src into
// dst. The stream must decompress to exactly len(dst) bytes: fewer gives
// ErrShortOutput, more ErrInsufficientSpace; invalid data gives ErrBadData.
// Bytes of src after the end of the stream are ignored. On success n is
// len(dst). On error n is 0 and dst may have been overwritten. dst must
// not overlap src. Decompress does not allocate.
func (d *Decompressor) Decompress(dst, src []byte) (n int, err error) {
	return d.d.DecompressZlib(dst, src)
}

// DecompressEx decompresses the zlib stream at the start of src into dst,
// which may be larger than needed. It returns the length of the stream in
// src (nIn: header, DEFLATE data and Adler-32; bytes after it are not
// counted) and the decompressed size (nOut). Output longer than len(dst)
// gives ErrInsufficientSpace, invalid data ErrBadData; ErrShortOutput is
// never returned. On error nIn and nOut are 0. Bytes of dst past nOut may be
// overwritten even on success. dst must not overlap src. DecompressEx does
// not allocate.
func (d *Decompressor) DecompressEx(dst, src []byte) (nIn, nOut int, err error) {
	return d.d.DecompressZlibEx(dst, src)
}

// AppendDecompress decompresses the zlib stream at the start of src,
// whose decompressed size need not be known, appends the output to dst and
// returns the extended slice. sizeHint is the expected decompressed size
// (<= 0: unknown, max(4*len(src), 1024) is tried first). The stream is
// decoded into the spare capacity of dst, grown to at least sizeHint bytes;
// if that is too small, the space is doubled and the stream decoded again
// from the start, up to the output limit (SetMaxOutput, DefaultMaxOutput by
// default), past which ErrOutputLimit is returned. An exact sizeHint saves
// both time and memory. The bytes of dst before len(dst) are not modified.
// On error the returned slice has the length of dst (its capacity may have
// grown and its spare capacity been written) and the error is ErrBadData or
// ErrOutputLimit.
//
// sizeHint is trusted: that many bytes are allocated before anything is
// decoded, clamped only by the output limit and by 1032 times len(src)
// plus 1 KiB, more than any input of len(src) bytes can decompress to. Do
// not pass an untrusted value, such as a gzip ISIZE trailer or a length
// field from the network, without lowering SetMaxOutput to what you are
// willing to allocate.
//
// The whole capacity of dst, not only dst[:len(dst)], must not overlap
// src: the output is decoded into the spare capacity, so a src stored
// there (for example after a header in the same buffer) is overwritten
// while it is read, giving ErrBadData or wrong output.
func (d *Decompressor) AppendDecompress(dst, src []byte, sizeHint int) ([]byte, error) {
	return appendbuf.Decompress(&d.d, appendbuf.Zlib, dst, src, sizeHint, d.MaxOutput(), ErrOutputLimit)
}
