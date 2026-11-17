package zlib_test

import (
	"bytes"
	stdzlib "compress/zlib"
	"fmt"
	"io"
	"log"

	"github.com/vahid-sohrabloo/fastcompress/zlib"
)

// Stream a zlib stream and read it back with the standard library.
func ExampleWriter() {
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		log.Fatal(err)
	}
	for i := range 3 {
		fmt.Fprintf(w, "record %d\n", i)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := stdzlib.NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	fmt.Print(string(out))
	// Output:
	// record 0
	// record 1
	// record 2
}
