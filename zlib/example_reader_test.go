package zlib_test

import (
	"bytes"
	"io"
	"log"
	"os"

	"github.com/vahid-sohrabloo/fastzstd/zlib"
)

// Decompress a zlib stream written with a preset dictionary.
func ExampleNewReaderDict() {
	dict := []byte("streaming reader dictionary")
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevelDict(&buf, zlib.DefaultCompression, dict)
	if err != nil {
		log.Fatal(err)
	}
	w.Write([]byte("a streaming reader with a dictionary\n"))
	w.Close()

	r, err := zlib.NewReaderDict(&buf, dict)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	if _, err := io.Copy(os.Stdout, r); err != nil {
		log.Fatal(err)
	}
	// Output: a streaming reader with a dictionary
}
