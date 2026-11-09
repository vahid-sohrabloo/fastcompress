package gzip_test

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/vahid-sohrabloo/fastzstd/gzip"
)

// Read a gzip member and its header fields.
func ExampleReader() {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = "notes.txt"
	w.Write([]byte("gzip streaming reader\n"))
	w.Close()

	r, err := gzip.NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("name:", r.Name)
	if _, err := io.Copy(os.Stdout, r); err != nil {
		log.Fatal(err)
	}
	if err := r.Close(); err != nil {
		log.Fatal(err)
	}
	// Output:
	// name: notes.txt
	// gzip streaming reader
}

// Read the members of a multi-member file one at a time.
func ExampleReader_Multistream() {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	for _, name := range []string{"one", "two"} {
		w.Reset(&buf)
		w.Name = name
		w.Write([]byte("data of " + name + "\n"))
		w.Close()
	}

	// bytes.Buffer is an io.ByteReader: each member is read exactly.
	r, err := gzip.NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	for {
		r.Multistream(false)
		fmt.Printf("member %q: ", r.Name)
		if _, err := io.Copy(os.Stdout, r); err != nil {
			log.Fatal(err)
		}
		err = r.Reset(&buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
	}
	// Output:
	// member "one": data of one
	// member "two": data of two
}
