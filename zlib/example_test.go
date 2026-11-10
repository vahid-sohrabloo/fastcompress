package zlib_test

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/vahid-sohrabloo/fastcompress/zlib"
)

// Compress at levels 1 and 6 and decompress into a buffer of the known size.
func Example() {
	var src []byte
	for i := range 300 {
		src = fmt.Appendf(src, "line %d: %d squared is %d\n", i, i, i*i)
	}
	for _, level := range []int{zlib.BestSpeed, 6} {
		c, err := zlib.NewCompressor(level)
		if err != nil {
			log.Fatal(err)
		}
		dst := make([]byte, c.CompressBound(len(src)))
		n, err := c.Compress(dst, src)
		if err != nil {
			log.Fatal(err)
		}
		compressed := dst[:n]

		out := make([]byte, len(src)) // the exact decompressed size
		if _, err := zlib.NewDecompressor().Decompress(out, compressed); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("level %d: %d -> %d bytes, round trip ok: %v\n", level, len(src), n, bytes.Equal(out, src))
	}
	// Output:
	// level 1: 8934 -> 2412 bytes, round trip ok: true
	// level 6: 8934 -> 2275 bytes, round trip ok: true
}

// Compressors are cheap to reuse but expensive to create (see the memory
// table in the package documentation), and not safe for concurrent use: a
// sync.Pool per level shares them between goroutines.
func Example_pool() {
	var pool = sync.Pool{New: func() any {
		c, err := zlib.NewCompressor(zlib.DefaultCompression)
		if err != nil {
			panic(err)
		}
		return c
	}}
	compress := func(dst, src []byte) []byte {
		c := pool.Get().(*zlib.Compressor)
		defer pool.Put(c)
		dst, _ = c.AppendCompress(dst, src) // AppendCompress cannot fail
		return dst
	}

	var wg sync.WaitGroup
	sizes := make([]int, 4)
	for i := range sizes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sizes[i] = len(compress(nil, bytes.Repeat([]byte{byte('a' + i)}, 1000*(i+1))))
		}()
	}
	wg.Wait()
	fmt.Println(sizes)
	// Output:
	// [17 23 27 28]
}

// AppendDecompress decodes data of unknown size; SetMaxOutput bounds the
// output (and the memory) so that a small hostile input cannot expand to
// gigabytes.
func ExampleDecompressor_AppendDecompress() {
	c, _ := zlib.NewCompressor(zlib.BestSpeed)
	bomb, _ := c.AppendCompress(nil, make([]byte, 10<<20)) // 10 MiB of zeros
	fmt.Printf("%d bytes compressed\n", len(bomb))

	d := zlib.NewDecompressor()
	d.SetMaxOutput(1 << 20) // accept at most 1 MiB
	_, err := d.AppendDecompress(nil, bomb, 0)
	fmt.Println("over the limit:", errors.Is(err, zlib.ErrOutputLimit))

	d.SetMaxOutput(16 << 20)
	out, err := d.AppendDecompress([]byte("prefix:"), bomb, 0)
	fmt.Println(string(out[:7]), len(out)-7, err)
	// Output:
	// 12171 bytes compressed
	// over the limit: true
	// prefix: 10485760 <nil>
}

// The errors are sentinel values shared by packages flate, gzip and zlib;
// test them with errors.Is.
func Example_errors() {
	c, _ := zlib.NewCompressor(6)
	src := []byte("some data to compress, some data to compress")
	compressed, _ := c.AppendCompress(nil, src)

	_, err := c.Compress(make([]byte, 4), src)
	fmt.Println("small dst:", errors.Is(err, zlib.ErrShortBuffer))

	d := zlib.NewDecompressor()
	_, err = d.Decompress(make([]byte, len(src)+1), compressed)
	fmt.Println("dst too large:", errors.Is(err, zlib.ErrShortOutput))
	_, err = d.Decompress(make([]byte, len(src)-1), compressed)
	fmt.Println("dst too small:", errors.Is(err, zlib.ErrInsufficientSpace))
	_, err = d.Decompress(make([]byte, len(src)), compressed[:len(compressed)/2])
	fmt.Println("truncated:", errors.Is(err, zlib.ErrBadData))

	_, err = zlib.NewCompressor(13)
	fmt.Println("level 13:", errors.Is(err, zlib.ErrInvalidLevel))
	// Output:
	// small dst: true
	// dst too large: true
	// dst too small: true
	// truncated: true
	// level 13: true
}
