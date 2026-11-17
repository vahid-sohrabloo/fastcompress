package deflate

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// Compress-side ports of programs/test_litrunlen_overflow.c and
// programs/test_trailing_bytes.c for the raw DEFLATE codec (the decode sides
// are TestPortLitrunlenOverflow / TestPortTrailingBytes in
// decompress_stress_test.go; the container forms are in container_test.go),
// and a check that every level 0..12 is implemented.

// port of programs/test_litrunlen_overflow.c:tmain, compress side, raw:
// libdeflate_deflate_compress(c, orig_data, data_size, compressed_data,
// data_size) at levels 3, 6 and 12 must give 0 < csize < data_size. Go's
// Compress into a buffer of exactly data_size bytes must return what
// libdeflate returns for the same out_nbytes_avail (ldref compress-batch with
// cap data_size: same size, same bytes), and decompress to the data.
func TestPortLitrunlenOverflowRaw(t *testing.T) {
	creftest.LDBin(t)
	data := litrunData(2)
	dataSize := len(data)
	if dataSize != 2*250*251 {
		t.Fatalf("data_size %d", dataSize)
	}
	levels := []int{3, 6, 12}
	recs := make([]ldBatchRec, len(levels))
	for i, level := range levels {
		recs[i] = ldBatchRec{level, dataSize, data}
	}
	want := ldCompressBatch(t, recs)
	d := NewDecompressor()
	for i, level := range levels {
		c, err := NewCompressor(level)
		if err != nil {
			t.Fatal(err)
		}
		comp := make([]byte, dataSize)
		csize := c.Compress(comp, data)
		if csize <= 0 || csize >= dataSize {
			t.Fatalf("level %d: csize %d, data_size %d", level, csize, dataSize)
		}
		if !bytes.Equal(comp[:csize], want[i]) {
			t.Fatalf("level %d: Go %d bytes, libdeflate %d bytes (cap data_size)", level, csize, len(want[i]))
		}
		out := make([]byte, dataSize)
		if _, err := d.Decompress(out, comp[:csize]); err != nil || !bytes.Equal(out, data) {
			t.Fatalf("level %d: decompress: %v", level, err)
		}
	}
}

// port of programs/test_trailing_bytes.c:tmain, compress side, raw DEFLATE
// codec: level 6, the 32768-byte input into the first 30000 bytes of a zeroed
// 32768-byte buffer: 0 < csize <= 30000, the same bytes as libdeflate for
// out_nbytes_avail = 30000, and decompress() / decompress_ex() of the stream
// with and without the trailing zero bytes succeed with actual_in == csize.
func TestPortTrailingBytesRaw(t *testing.T) {
	creftest.LDBin(t)
	const originalNbytes = 32768
	const compressedNbytesTotal = 32768
	const compressedNbytesAvail = 30000
	original := trailingBytesOriginal()
	c, err := NewCompressor(6)
	if err != nil {
		t.Fatal(err)
	}
	compressed := make([]byte, compressedNbytesTotal)
	n := c.Compress(compressed[:compressedNbytesAvail], original)
	if n <= 0 || n > compressedNbytesAvail {
		t.Fatalf("compressed size %d", n)
	}
	want := ldCompressBatch(t, []ldBatchRec{{6, compressedNbytesAvail, original}})[0]
	if !bytes.Equal(compressed[:n], want) {
		t.Fatalf("Go %d bytes, libdeflate %d bytes", n, len(want))
	}
	d := NewDecompressor()
	for _, in := range [][]byte{compressed[:n], compressed} {
		out := make([]byte, originalNbytes)
		// decompress() with &actual_out: the Ex path.
		aIn, aOut, err := d.DecompressEx(out, in)
		if err != nil || aIn != n || aOut != originalNbytes || !bytes.Equal(out, original) {
			t.Fatalf("decompress_ex of %d bytes: in %d out %d, %v", len(in), aIn, aOut, err)
		}
		clear(out)
		if got, err := d.Decompress(out, in); err != nil || got != originalNbytes || !bytes.Equal(out, original) {
			t.Fatalf("exact decompress of %d bytes: %d, %v", len(in), got, err)
		}
	}
}

// TestEveryLevelImplemented: every level 0..12 compresses a 1 MiB input
// (above every passthrough size) raw, gzip and zlib without panicking, and
// the output decodes to the input.
func TestEveryLevelImplemented(t *testing.T) {
	var src []byte
	for _, cs := range deflatetest.Cases(true) {
		if cs.Name == "size-text/1048576" {
			src = cs.Data
		}
	}
	if len(src) != 1<<20 {
		t.Fatal("size-text/1048576 not in Cases(true)")
	}
	if raceEnabled {
		src = src[:64<<10] // no goroutines here: a smaller input suffices under -race
	}
	d := NewDecompressor()
	out := make([]byte, len(src))
	for _, level := range deflatetest.Levels() {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			c, err := NewCompressor(level)
			if err != nil {
				t.Fatal(err)
			}
			dst := make([]byte, c.GzipCompressBound(len(src)))
			n := c.Compress(dst, src)
			if _, err := d.Decompress(out, dst[:n]); n == 0 || err != nil || !bytes.Equal(out, src) {
				t.Fatalf("raw: %d bytes, %v", n, err)
			}
			n = c.CompressGzip(dst, src)
			if _, err := d.DecompressGzip(out, dst[:n]); n == 0 || err != nil || !bytes.Equal(out, src) {
				t.Fatalf("gzip: %d bytes, %v", n, err)
			}
			n = c.CompressZlib(dst, src)
			if _, err := d.DecompressZlib(out, dst[:n]); n == 0 || err != nil || !bytes.Equal(out, src) {
				t.Fatalf("zlib: %d bytes, %v", n, err)
			}
		})
	}
}
