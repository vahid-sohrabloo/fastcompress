package deflate

import (
	"bytes"
	stdgzip "compress/gzip"
	stdzlib "compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/adler32"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// Tests of the gzip and zlib container layers (gzip.go, zlib.go,
// decompress_gzip.go, decompress_zlib.go): compressor byte-identity with
// libdeflate_{gzip,zlib}_compress, bounds, ports of the container cases of
// programs/test_trailing_bytes.c and the compress side of
// programs/test_litrunlen_overflow.c, CRC-32 (programs/test_checksums.c),
// allocations and decoding by the standard library (black box). The
// decompressor differential tests are in decompress_container_test.go.

// containerFormats are the two container formats under test.
var containerFormats = []string{creftest.LDGzip, creftest.LDZlib}

// wrapC builds libdeflate's container output from libdeflate's raw DEFLATE
// output raw of src at level, written here from the C source
// (libdeflate_{gzip,zlib}_compress) and checked against the real C container
// output (ldref compress -format) in TestContainerCompressMatchLibdeflate.
// The inner DEFLATE call gets the container's buffer minus the overhead, so
// for a bound-sized buffer it gets libdeflate_deflate_compress_bound bytes,
// as ldref compress-batch gives it.
func wrapC(format string, level int, raw, src []byte) []byte {
	var out []byte
	switch format {
	case creftest.LDGzip:
		xfl := byte(0)
		if level < 2 {
			xfl = 4
		} else if level >= 8 {
			xfl = 2
		}
		out = append(out, 0x1F, 0x8B, 8, 0, 0, 0, 0, 0, xfl, 255)
		out = append(out, raw...)
		out = binary.LittleEndian.AppendUint32(out, crc32.ChecksumIEEE(src))
		out = binary.LittleEndian.AppendUint32(out, uint32(len(src)))
	default:
		hint := 3
		switch {
		case level < 2:
			hint = 0
		case level < 6:
			hint = 1
		case level < 8:
			hint = 2
		}
		hdr := 0x7800 | hint<<6
		hdr |= 31 - hdr%31
		out = append(out, byte(hdr>>8), byte(hdr))
		out = append(out, raw...)
		out = binary.BigEndian.AppendUint32(out, adler32.Checksum(src))
	}
	return out
}

// goContainerCompress runs CompressGzip or CompressZlib into dst.
func goContainerCompress(c *Compressor, format string, dst, src []byte) int {
	if format == creftest.LDGzip {
		return c.CompressGzip(dst, src)
	}
	return c.CompressZlib(dst, src)
}

// goContainerBound is GzipCompressBound or ZlibCompressBound.
func goContainerBound(c *Compressor, format string, n int) int {
	if format == creftest.LDGzip {
		return c.GzipCompressBound(n)
	}
	return c.ZlibCompressBound(n)
}

// containerOverhead is GZIP_MIN_OVERHEAD or ZLIB_MIN_OVERHEAD.
func containerOverhead(format string) int {
	if format == creftest.LDGzip {
		return gzipMinOverhead
	}
	return zlibMinOverhead
}

// containerCompressInputs: Cases(true) (and, without -short, the rest of
// Cases(false)), plus a Silesia sample (-short: the first 1 MiB of xml, 256
// KiB at levels 10..12; else all of xml).
func containerCompressInputs(t *testing.T, sh *diffShared, level int) []l1Input {
	in := append([]l1Input(nil), sh.corpusCases()...)
	for _, f := range deflatetest.Silesia(t) {
		if f.Name != "xml" {
			continue
		}
		data := f.Load(t)
		if testing.Short() {
			n := 1 << 20
			if level >= 10 {
				n = 256 << 10
			}
			data = data[:n]
		}
		in = append(in, l1Input{"silesia/xml", data})
	}
	return in
}

// THE container identity check. Per implemented level, every input is
// compressed by libdeflate (raw, ldref compress-batch, bound-sized buffer)
// and by CompressGzip / CompressZlib into a Gzip/ZlibCompressBound buffer;
// Go must equal wrapC(C raw). Every 8th input and the Silesia sample are
// also compressed by ldref compress -format gzip|zlib (the real
// libdeflate_{gzip,zlib}_compress), which must equal wrapC(C raw) too.
// Inputs of at most 64 KiB are also compressed into a buffer of exactly the
// output size (same bytes) and one byte less (0; libdeflate's raw compressor
// returns 0 for the corresponding inner buffer, checked in the same batch).
func TestContainerCompressMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	sh := &diffShared{}
	for _, level := range deflatetest.Levels() {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			parallelCase(t)
			inputs := containerCompressInputs(t, sh, level)
			c, err := NewCompressor(level)
			if err != nil {
				t.Fatal(err)
			}
			const smallMax = 64 << 10
			recs := make([]ldBatchRec, 0, len(inputs))
			for _, in := range inputs {
				recs = append(recs, ldBatchRec{level, -1, in.data})
			}
			raws := ldCompressBatch(t, recs)
			// libdeflate raw with one byte less than its output: must be 0.
			var minus []ldBatchRec
			for i, in := range inputs {
				if len(in.data) <= smallMax {
					minus = append(minus, ldBatchRec{level, len(raws[i]) - 1, in.data})
				}
			}
			for i, r := range ldCompressBatch(t, minus) {
				if r != nil {
					t.Fatalf("libdeflate raw into %d bytes (output size - 1) returned %d", minus[i].capBytes, len(r))
				}
			}
			direct, total := 0, 0
			var dst []byte
			for i, in := range inputs {
				if raws[i] == nil {
					t.Fatalf("%s: libdeflate returned 0 for a bound-sized buffer", in.name)
				}
				doDirect := i%8 == 0 || strings.HasPrefix(in.name, "silesia/")
				for _, format := range containerFormats {
					want := wrapC(format, level, raws[i], in.data)
					if doDirect {
						if cw := creftest.LDCompress(t, in.data, level, format); !bytes.Equal(cw, want) {
							t.Fatalf("%s %s: ldref compress -format (%d bytes) != header+raw+trailer (%d bytes)",
								in.name, format, len(cw), len(want))
						}
						direct++
					}
					bound := goContainerBound(c, format, len(in.data))
					if cap(dst) < bound {
						dst = make([]byte, bound)
					}
					n := goContainerCompress(c, format, dst[:bound], in.data)
					if n != len(want) || !bytes.Equal(dst[:n], want) {
						t.Fatalf("%s %s: Go %d bytes, libdeflate %d bytes (first difference at %d)",
							in.name, format, n, len(want), firstDiff(dst[:max(n, 0)], want))
					}
					if len(in.data) <= smallMax {
						if n := goContainerCompress(c, format, dst[:len(want)], in.data); n != len(want) ||
							!bytes.Equal(dst[:n], want) {
							t.Fatalf("%s %s: exact-size buffer: %d bytes", in.name, format, n)
						}
						if n := goContainerCompress(c, format, dst[:len(want)-1], in.data); n != 0 {
							t.Fatalf("%s %s: buffer one byte short: returned %d, want 0", in.name, format, n)
						}
					}
					total += len(in.data)
				}
			}
			t.Logf("%d inputs, %d bytes, %d direct ldref container comparisons", len(inputs), total, direct)
		})
	}
}

// firstDiff returns the index of the first differing byte of a and b.
func firstDiff(a, b []byte) int {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestContainerCompressSmallDst: every buffer size from 0 to the output size
// + 2 for small inputs at several levels, against libdeflate's container
// logic: 0 when the buffer is at most the overhead (18 / 6 bytes, note <=),
// else whatever libdeflate's raw compressor returns for the buffer minus the
// overhead (ldref compress-batch with that cap), wrapped.
func TestContainerCompressSmallDst(t *testing.T) {
	creftest.LDBin(t)
	r := rand.New(rand.NewPCG(7, 8))
	inputs := [][]byte{{}, {'a'}, []byte("hello, hello, hello, hello, hello, hello, hello, hello!"),
		bytes.Repeat([]byte{0}, 300), randBytes(r, 200), []byte(strings.Repeat("abcdefgh", 40))}
	for _, level := range []int{0, 1, 2, 6, 9, 12} {
		c, _ := NewCompressor(level)
		for _, format := range containerFormats {
			ov := containerOverhead(format)
			for _, src := range inputs {
				full := make([]byte, goContainerBound(c, format, len(src)))
				n := goContainerCompress(c, format, full, src)
				var recs []ldBatchRec
				for size := ov + 1; size <= n+2; size++ {
					recs = append(recs, ldBatchRec{level, size - ov, src})
				}
				raws := ldCompressBatch(t, recs)
				for size := 0; size <= n+2; size++ {
					var want []byte
					if size > ov {
						if raw := raws[size-ov-1]; raw != nil {
							want = wrapC(format, level, raw, src)
						}
					}
					dst := make([]byte, size)
					got := goContainerCompress(c, format, dst, src)
					if got != len(want) || !bytes.Equal(dst[:got], want) {
						t.Fatalf("level %d %s, %d-byte input, %d-byte buffer: Go %d bytes, libdeflate %d",
							level, format, len(src), size, got, len(want))
					}
				}
			}
		}
	}
}

// randBytes returns n random bytes from r.
func randBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// TestContainerBounds: Gzip/ZlibCompressBound equal C's definition
// (GZIP_MIN_OVERHEAD / ZLIB_MIN_OVERHEAD + libdeflate_deflate_compress_bound,
// the latter from ldref compress-bound) for a sweep of sizes at several
// levels; nil receivers work; the bound saturates at MaxInt; negative sizes
// panic; and incompressible data of many sizes compresses into a buffer of
// exactly the bound at every implemented level.
func TestContainerBounds(t *testing.T) {
	creftest.LDBin(t)
	var sizes []int
	for n := 0; n <= 12000; n++ {
		sizes = append(sizes, n)
	}
	for _, n := range []int{65535, 65536, 299999, 300000, 1 << 20, 1 << 30} {
		sizes = append(sizes, n-1, n, n+1)
	}
	if math.MaxInt > 1<<32 {
		big := uint64(1) << 40
		sizes = append(sizes, int(big), int(big)+12345)
	}
	var in []byte
	for _, n := range sizes {
		in = binary.LittleEndian.AppendUint64(in, uint64(n))
	}
	for _, level := range []int{0, 1, 6, 12} {
		out := creftest.LDRun(t, in, "compress-bound", "-l", fmt.Sprint(level))
		if len(out) != 8*len(sizes) {
			t.Fatalf("compress-bound: %d bytes for %d sizes", len(out), len(sizes))
		}
		c, _ := NewCompressor(level)
		for i, n := range sizes {
			raw := int(binary.LittleEndian.Uint64(out[8*i:]))
			if g := c.GzipCompressBound(n); g != raw+18 {
				t.Fatalf("level %d: GzipCompressBound(%d) = %d, C %d", level, n, g, raw+18)
			}
			if z := c.ZlibCompressBound(n); z != raw+6 {
				t.Fatalf("level %d: ZlibCompressBound(%d) = %d, C %d", level, n, z, raw+6)
			}
		}
	}
	var nilc *Compressor
	if nilc.GzipCompressBound(100) != 18+nilc.CompressBound(100) || nilc.ZlibCompressBound(100) != 6+nilc.CompressBound(100) {
		t.Fatal("nil receiver bound")
	}
	if nilc.GzipCompressBound(math.MaxInt) != math.MaxInt || nilc.ZlibCompressBound(math.MaxInt) != math.MaxInt {
		t.Fatal("bound does not saturate at MaxInt")
	}
	// The largest n with an unsaturated raw bound: the container bounds
	// saturate there too, rather than wrap.
	lo, hi := 0, math.MaxInt
	for lo < hi {
		mid := lo + (hi-lo)/2 + 1
		if nilc.CompressBound(mid) < math.MaxInt {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for _, n := range []int{lo, lo - 1, lo - 4000} {
		b := nilc.CompressBound(n)
		if g := nilc.GzipCompressBound(n); g < b || (b <= math.MaxInt-18 && g != b+18) {
			t.Fatalf("GzipCompressBound(%d) = %d, raw %d", n, g, b)
		}
		if z := nilc.ZlibCompressBound(n); z < b || (b <= math.MaxInt-6 && z != b+6) {
			t.Fatalf("ZlibCompressBound(%d) = %d, raw %d", n, z, b)
		}
	}
	for _, f := range []func(){func() { nilc.GzipCompressBound(-1) }, func() { nilc.ZlibCompressBound(-1) }} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("negative size did not panic")
				}
			}()
			f()
		}()
	}
	r := rand.New(rand.NewPCG(9, 10))
	data := randBytes(r, 300001)
	for _, level := range deflatetest.Levels() {
		c, _ := NewCompressor(level)
		for _, n := range []int{0, 1, 17, 18, 19, 100, 4999, 5000, 5001, 65535, 65536, 70000, 300001} {
			if testing.Short() && level >= 10 && n > 70000 {
				continue
			}
			for _, format := range containerFormats {
				b := goContainerBound(c, format, n)
				dst := make([]byte, b)
				if got := goContainerCompress(c, format, dst, data[:n]); got == 0 || got > b {
					t.Fatalf("level %d %s: %d random bytes: %d (bound %d)", level, format, n, got, b)
				}
			}
		}
	}
}

// trailingBytesOriginal is the input of programs/test_trailing_bytes.c:
// original[i] = (i % 123) + (i % 1023), stored in a u8.
func trailingBytesOriginal() []byte {
	b := make([]byte, 32768)
	for i := range b {
		b[i] = byte((i % 123) + (i % 1023))
	}
	return b
}

// port of programs/test_trailing_bytes.c:tmain, the zlib and gzip codecs
// (the raw DEFLATE codec: TestPortTrailingBytes in decompress_stress_test.go
// for the decode side, TestPortTrailingBytesRaw below for the compress side):
// compress the 32768-byte input at level 6 into the first 30000 bytes of a
// zeroed 32768-byte buffer; decompress() and decompress_ex() of the stream
// alone and of the whole buffer (the stream plus zero bytes) must succeed with
// actual_in == the stream size and the original output. C's decompress()
// passes a non-NULL actual_out pointer, so it maps to the Ex call with nIn
// ignored; the exact-size call (DecompressGzip/Zlib, actual_out NULL) is
// checked as well. Go's compressed stream also equals libdeflate's, and
// containerDiff compares both modes with libdeflate (when ldref exists).
func TestPortTrailingBytesContainers(t *testing.T) {
	const originalNbytes = 32768
	const compressedNbytesTotal = 32768
	const compressedNbytesAvail = 30000
	original := trailingBytesOriginal()
	c, err := NewCompressor(6)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDecompressor()
	for _, format := range containerFormats {
		compressed := make([]byte, compressedNbytesTotal)
		n := goContainerCompress(c, format, compressed[:compressedNbytesAvail], original)
		if n <= 0 || n > compressedNbytesAvail {
			t.Fatalf("%s: compressed size %d", format, n)
		}
		if haveLD(t) {
			if want := creftest.LDCompress(t, original, 6, format); !bytes.Equal(compressed[:n], want) {
				t.Fatalf("%s: compressed stream differs from libdeflate's", format)
			}
		}
		for _, in := range [][]byte{compressed[:n], compressed} {
			decompressed := make([]byte, originalNbytes)
			// decompress(d, .., &actual_decompressed_nbytes): the actual-out
			// path (Ex, nIn ignored).
			var got int
			if format == creftest.LDGzip {
				_, got, err = d.DecompressGzipEx(decompressed, in)
			} else {
				_, got, err = d.DecompressZlibEx(decompressed, in)
			}
			if err != nil || got != originalNbytes || !bytes.Equal(decompressed, original) {
				t.Fatalf("%s: decompress of %d bytes: %d, %v", format, len(in), got, err)
			}
			// Also the exact-size form (actual_out NULL).
			clear(decompressed)
			if format == creftest.LDGzip {
				got, err = d.DecompressGzip(decompressed, in)
			} else {
				got, err = d.DecompressZlib(decompressed, in)
			}
			if err != nil || got != originalNbytes || !bytes.Equal(decompressed, original) {
				t.Fatalf("%s: exact decompress of %d bytes: %d, %v", format, len(in), got, err)
			}
			// decompress_ex()
			clear(decompressed)
			var aIn, aOut int
			if format == creftest.LDGzip {
				aIn, aOut, err = d.DecompressGzipEx(decompressed, in)
			} else {
				aIn, aOut, err = d.DecompressZlibEx(decompressed, in)
			}
			if err != nil || aIn != n || aOut != originalNbytes || !bytes.Equal(decompressed, original) {
				t.Fatalf("%s: decompress_ex of %d bytes: in %d out %d, %v", format, len(in), aIn, aOut, err)
			}
		}
		if haveLD(t) {
			containerDiff(t, format, []ldReq{
				{"stream", compressed[:n], originalNbytes, true},
				{"stream/ex", compressed[:n], originalNbytes, false},
				{"trailing", compressed, originalNbytes, true},
				{"trailing/ex", compressed, originalNbytes, false},
			})
		}
	}
}

// port of programs/test_litrunlen_overflow.c:tmain, compress side, through
// the containers: the data compressed at levels 3, 6 and 12 into a
// data_size-byte buffer must give 0 < csize < data_size, the same bytes as
// libdeflate, and decompress to the data (Go, and the standard library as a
// black box).
func TestPortLitrunlenOverflowContainers(t *testing.T) {
	data := litrunData(2)
	dataSize := len(data)
	d := NewDecompressor()
	for _, level := range []int{3, 6, 12} {
		c, _ := NewCompressor(level)
		for _, format := range containerFormats {
			comp := make([]byte, dataSize)
			csize := goContainerCompress(c, format, comp, data)
			if csize <= 0 || csize >= dataSize {
				t.Fatalf("level %d %s: csize %d, data_size %d", level, format, csize, dataSize)
			}
			comp = comp[:csize]
			if haveLD(t) {
				if want := creftest.LDCompress(t, data, level, format); !bytes.Equal(comp, want) {
					t.Fatalf("level %d %s: differs from libdeflate", level, format)
				}
			}
			out := make([]byte, dataSize)
			var err error
			if format == creftest.LDGzip {
				_, err = d.DecompressGzip(out, comp)
			} else {
				_, err = d.DecompressZlib(out, comp)
			}
			if err != nil || !bytes.Equal(out, data) {
				t.Fatalf("level %d %s: Go decode: %v", level, format, err)
			}
			if got := stdDecode(t, format, comp); !bytes.Equal(got, data) {
				t.Fatalf("level %d %s: stdlib decode differs", level, format)
			}
		}
	}
}

// stdDecode decodes a gzip or zlib stream with the standard library.
func stdDecode(t testing.TB, format string, comp []byte) []byte {
	t.Helper()
	var rd io.Reader
	var err error
	if format == creftest.LDGzip {
		var zr *stdgzip.Reader
		zr, err = stdgzip.NewReader(bytes.NewReader(comp))
		if err == nil {
			zr.Multistream(false)
			if zr.Name != "" || zr.Comment != "" || zr.Extra != nil || !zr.ModTime.IsZero() || zr.OS != 255 {
				t.Fatalf("unexpected gzip header %+v", zr.Header)
			}
		}
		rd = zr
	} else {
		rd, err = stdzlib.NewReader(bytes.NewReader(comp))
	}
	if err != nil {
		t.Fatalf("stdlib %s reader: %v", format, err)
	}
	out, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("stdlib %s decode: %v", format, err)
	}
	return out
}

// TestContainerStdlibDecode: Go's container output at every implemented
// level, for a selection of the corpus, decodes with compress/gzip and
// compress/zlib (black boxes) to the input.
func TestContainerStdlibDecode(t *testing.T) {
	var inputs []deflatetest.Case
	for i, cs := range deflatetest.Cases(true) {
		// Under -race (no goroutines here) a small-input smoke suffices.
		if i%5 == 0 && len(cs.Data) <= 1<<20 && (!raceEnabled || len(cs.Data) <= 20000) {
			inputs = append(inputs, cs)
		}
	}
	for _, level := range deflatetest.Levels() {
		c, _ := NewCompressor(level)
		for _, cs := range inputs {
			if testing.Short() && level >= 10 && len(cs.Data) > 128<<10 {
				continue
			}
			for _, format := range containerFormats {
				dst := make([]byte, goContainerBound(c, format, len(cs.Data)))
				n := goContainerCompress(c, format, dst, cs.Data)
				if got := stdDecode(t, format, dst[:n]); !bytes.Equal(got, cs.Data) {
					t.Fatalf("level %d %s %s: stdlib decode differs", level, format, cs.Name)
				}
			}
		}
	}
}

// refCRC32 is the bitwise reflected CRC-32 (polynomial 0xEDB88320, initial
// and final inversion), libdeflate_crc32's function.
func refCRC32(crc uint32, p []byte) uint32 {
	crc = ^crc
	for _, b := range p {
		crc ^= uint32(b)
		for k := 0; k < 8; k++ {
			crc = crc>>1 ^ (0xEDB88320 & -(crc & 1))
		}
	}
	return ^crc
}

// port of programs/test_checksums.c (CRC-32 part): hash/crc32 (what the
// containers use) against a bitwise reference with random and zero initial
// values, sizes and alignments, and two-part computations; initial value 0.
func TestCRC32MatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	if crc32.ChecksumIEEE(nil) != 0 || crc32.Update(0, crc32.IEEETable, nil) != 0 {
		t.Fatal("initial CRC-32 is not 0")
	}
	buf := randBytes(r, 262144)
	for _, c := range []struct{ limit, iters int }{{256, 5000}, {1024, 500}, {32768, 50}, {262144, 20}} {
		for i := 0; i < c.iters; i++ {
			start := r.IntN(c.limit)
			n := r.IntN(c.limit - start)
			c0 := uint32(0)
			if r.IntN(2) == 1 {
				c0 = r.Uint32()
			}
			p := buf[start : start+n]
			got := crc32.Update(c0, crc32.IEEETable, p)
			if want := refCRC32(c0, p); got != want {
				t.Fatalf("crc32(%#x, %d bytes) = %#x, reference %#x", c0, n, got, want)
			}
			div := r.IntN(n + 1)
			if v := crc32.Update(crc32.Update(c0, crc32.IEEETable, p[:div]), crc32.IEEETable, p[div:]); v != got {
				t.Fatal("multipart CRC-32 differs")
			}
		}
	}
}

// TestContainerAllocs: the container compress and decompress calls do not
// allocate in steady state.
func TestContainerAllocs(t *testing.T) {
	src := deflatetest.Cases(true)[0].Data
	for _, cs := range deflatetest.Cases(true) {
		if strings.HasPrefix(cs.Name, "size-text/65536") {
			src = cs.Data
		}
	}
	d := NewDecompressor()
	out := make([]byte, len(src)+10)
	for _, level := range []int{0, 1, 2, 5, 8, 10, 12} {
		c, _ := NewCompressor(level)
		for _, format := range containerFormats {
			dst := make([]byte, goContainerBound(c, format, len(src)))
			n := goContainerCompress(c, format, dst, src)
			if a := testing.AllocsPerRun(3, func() { goContainerCompress(c, format, dst, src) }); a != 0 {
				t.Errorf("level %d %s compress: %v allocations", level, format, a)
			}
			comp := dst[:n]
			var f, g func()
			if format == creftest.LDGzip {
				f = func() { _, _, _ = d.DecompressGzipEx(out, comp) }
				g = func() { _, _ = d.DecompressGzip(out[:len(src)], comp) }
			} else {
				f = func() { _, _, _ = d.DecompressZlibEx(out, comp) }
				g = func() { _, _ = d.DecompressZlib(out[:len(src)], comp) }
			}
			if a := testing.AllocsPerRun(5, f); a != 0 {
				t.Errorf("level %d %s decompress ex: %v allocations", level, format, a)
			}
			if a := testing.AllocsPerRun(5, g); a != 0 {
				t.Errorf("level %d %s decompress: %v allocations", level, format, a)
			}
		}
	}
}
