package flate_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// Stream data through a Writer and back through a Reader.
func ExampleNewReader() {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := io.Copy(w, strings.NewReader("hello, streaming reader\n")); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r := flate.NewReader(&buf)
	defer r.Close()
	if _, err := io.Copy(os.Stdout, r); err != nil {
		log.Fatal(err)
	}
	// Output: hello, streaming reader
}

// Reuse one reader for many streams with Reset (no allocations once
// warmed up); invalid data is a CorruptInputError.
func ExampleResetter() {
	r := flate.NewReader(nil)
	for _, s := range [][]byte{{0x01, 3, 0, 0xfc, 0xff, 'o', 'n', 'e'}, {0x07}} {
		if err := r.(flate.Resetter).Reset(bytes.NewReader(s), nil); err != nil {
			log.Fatal(err)
		}
		b, err := io.ReadAll(r)
		var ce flate.CorruptInputError
		fmt.Printf("%q corrupt=%v\n", b, errors.As(err, &ce))
	}
	// Output:
	// "one" corrupt=false
	// "" corrupt=true
}
