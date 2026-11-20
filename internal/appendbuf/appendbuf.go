// Package appendbuf implements the whole-buffer Append functions and the
// output limit shared by the Compressor and Decompressor types of packages
// flate, gzip and zlib, which differ only in the deflate method they call.
package appendbuf

import (
	"math"
	"slices"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflate"
)

// DefaultMaxOutput is the output limit of a zero Limit (1 GiB). It must
// equal flate.DefaultMaxOutput (checked there at compile time).
const DefaultMaxOutput = 1 << 30

// MaxExpansion bounds the DEFLATE expansion ratio: a match of length 258
// costs at least 2 bits (a 1-bit length code and a 1-bit distance code), so
// one input byte yields at most 4*258 output bytes.
const MaxExpansion = 4 * 258

// Limit is a Decompressor's AppendDecompress output limit; the zero value
// means DefaultMaxOutput.
type Limit int

// Set sets the limit to n; n <= 0 restores DefaultMaxOutput.
func (l *Limit) Set(n int) { *l = Limit(max(n, 0)) }

// Get returns the limit.
func (l Limit) Get() int {
	if l == 0 {
		return DefaultMaxOutput
	}
	return int(l)
}

// Format selects the deflate methods: raw DEFLATE, gzip or zlib.
//
// It is a value switched on, not a func parameter, so that the calls stay
// static: src (and dst) passed to a func value would escape to the heap,
// which the direct deflate calls do not.
type Format int

// The formats of packages flate, gzip and zlib.
const (
	Raw Format = iota
	Gzip
	Zlib
)

// compress calls the compress method of c for f.
func compress(c *deflate.Compressor, f Format, dst, src []byte) int {
	switch f {
	case Gzip:
		return c.CompressGzip(dst, src)
	case Zlib:
		return c.CompressZlib(dst, src)
	}
	return c.Compress(dst, src)
}

// decompressEx calls the DecompressEx method of d for f.
func decompressEx(d *deflate.Decompressor, f Format, dst, src []byte) (nIn, nOut int, err error) {
	switch f {
	case Gzip:
		return d.DecompressGzipEx(dst, src)
	case Zlib:
		return d.DecompressZlibEx(dst, src)
	}
	return d.DecompressEx(dst, src)
}

// Compress appends the compressed form of src in format f to dst: it makes
// room for bound bytes after len(dst) (growing dst only if its capacity is
// too small), compresses into that room and returns dst extended by the
// compressed size. bound must be the CompressBound of f for len(src), so
// compressing never fails.
func Compress(c *deflate.Compressor, f Format, dst, src []byte, bound int) []byte {
	dst = slices.Grow(dst, bound)
	n := compress(c, f, dst[len(dst):len(dst)+bound], src)
	return dst[:len(dst)+n]
}

// Decompress decodes the format f data at the start of src into the spare
// capacity of dst and returns dst extended by the output. sizeHint is the
// expected output size (<= 0: unknown). The first attempt gets sizeHint
// bytes (max(4*len(src), 1024) when unknown), clamped to
// MaxExpansion*len(src)+1024 and raised to the spare capacity of dst; while
// the decoder returns deflate.ErrInsufficientSpace the space is doubled and
// src decoded again from the start, up to min(maxOutput,
// math.MaxInt-len(dst)) bytes, past which errOutputLimit is returned.
// Growing allocates exactly len(dst)+size bytes, so the capacity never
// passes the limit. On error the returned slice has the length of dst and
// the error is the decoder's or errOutputLimit.
func Decompress(d *deflate.Decompressor, f Format, dst, src []byte, sizeHint, maxOutput int, errOutputLimit error) ([]byte, error) {
	// len(dst)+limit must not overflow.
	limit := min(maxOutput, math.MaxInt-len(dst))
	size := sizeHint
	if size <= 0 {
		size = max(min(len(src), limit/4)*4, 1024)
	}
	// No input of len(src) bytes decompresses to more than
	// MaxExpansion*len(src) bytes: a larger hint is wrong, so do not
	// allocate for it (the doubling below would still recover).
	if len(src) <= (math.MaxInt-1024)/MaxExpansion {
		size = min(size, MaxExpansion*len(src)+1024)
	}
	// Spare capacity costs nothing: use all of it.
	size = min(max(size, cap(dst)-len(dst)), limit)
	for {
		if cap(dst)-len(dst) < size {
			// Exact size, not slices.Grow: growslice rounding could
			// take the capacity past the output limit.
			nd := make([]byte, len(dst), len(dst)+size)
			copy(nd, dst)
			dst = nd
		}
		_, nOut, err := decompressEx(d, f, dst[len(dst):len(dst)+size], src)
		if err == nil {
			return dst[:len(dst)+nOut], nil
		}
		if err != deflate.ErrInsufficientSpace {
			return dst, err
		}
		if size == limit {
			return dst, errOutputLimit
		}
		if size > limit/2 {
			size = limit
		} else {
			size *= 2
		}
	}
}

// ScratchMax is the largest scratch buffer DecompressScratch keeps.
const ScratchMax = 1 << 20

// DecompressScratch is Decompress for a dst without spare capacity and an
// unknown output size, whose result is a new array: the sizes Decompress
// would try (from max(4*len(src), 1024), or the capacity of *scratch when
// larger, doubled up to the limit) are tried in *scratch, which grows to
// the size that works up to ScratchMax bytes; dst and the output are then
// copied to an array of exactly len(dst)+output bytes, so that the result
// has no spare capacity to carry. Past ScratchMax it continues as
// Decompress (decoding into the result's array, from the next size). On
// error it returns dst and the error Decompress would return.
func DecompressScratch(d *deflate.Decompressor, f Format, scratch *[]byte, dst, src []byte, maxOutput int, errOutputLimit error) ([]byte, error) {
	limit := min(maxOutput, math.MaxInt-len(dst))
	size := max(min(len(src), limit/4)*4, 1024)
	if len(src) <= (math.MaxInt-1024)/MaxExpansion {
		size = min(size, MaxExpansion*len(src)+1024)
	}
	size = min(max(size, cap(*scratch)), limit)
	// Scratch attempts never pass ScratchMax: a larger first size or a
	// doubling that jumps over it is cut to it, and only an output that
	// does not fit there goes on as Decompress.
	size = min(size, ScratchMax)
	for {
		if cap(*scratch) < size {
			*scratch = make([]byte, size)
		}
		_, nOut, err := decompressEx(d, f, (*scratch)[:size], src)
		if err == nil {
			s := (*scratch)[:nOut]
			if len(s) == 0 {
				return dst, nil
			}
			if len(dst) == 0 {
				out := make([]byte, len(s)) // (make and copy: not zeroed first)
				copy(out, s)
				return out, nil
			}
			out := make([]byte, len(dst)+len(s))
			copy(out, dst)
			copy(out[len(dst):], s)
			return out, nil
		}
		if err != deflate.ErrInsufficientSpace {
			return dst, err
		}
		if size == limit {
			return dst, errOutputLimit
		}
		next := size * 2
		if size > limit/2 {
			next = limit
		}
		if size >= ScratchMax {
			size = next
			break
		}
		size = min(next, ScratchMax)
	}
	return Decompress(d, f, dst, src, size, maxOutput, errOutputLimit)
}
