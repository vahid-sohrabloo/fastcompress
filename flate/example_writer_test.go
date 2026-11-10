package flate_test

import (
	"bytes"
	stdflate "compress/flate"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// Stream data through a Writer, flushing after the first message so a
// reader could decode it before the rest arrives.
func ExampleWriter() {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		log.Fatal(err)
	}
	io.WriteString(w, "first message\n")
	if err := w.Flush(); err != nil { // everything so far is decodable
		log.Fatal(err)
	}
	io.WriteString(w, strings.Repeat("more data\n", 100))
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	out, err := io.ReadAll(stdflate.NewReader(&buf))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d bytes, first line %q\n", len(out), strings.SplitN(string(out), "\n", 2)[0])
	// Output:
	// 1014 bytes, first line "first message"
}

// A preset dictionary: the decompressor must use the same one.
func ExampleNewWriterDict() {
	dict := []byte("GET /index.html HTTP/1.1\r\nHost: example.com\r\n")
	var buf bytes.Buffer
	w, err := flate.NewWriterDict(&buf, flate.DefaultCompression, dict)
	if err != nil {
		log.Fatal(err)
	}
	io.WriteString(w, "GET /index.html HTTP/1.1\r\nHost: example.com\r\n\r\n")
	w.Close()

	out, err := io.ReadAll(stdflate.NewReaderDict(&buf, dict))
	fmt.Printf("%q %v\n", out[:3], err)
	// Output:
	// "GET" <nil>
}
