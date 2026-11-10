package gzip_test

import (
	"bytes"
	stdgzip "compress/gzip"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/gzip"
)

// Write a gzip member with header fields and read it back with the
// standard library.
func ExampleWriter() {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = "notes.txt"
	w.Comment = "an example"
	w.ModTime = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if _, err := io.WriteString(w, "hello, gzip\n"); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := stdgzip.NewReader(&buf)
	if err != nil {
		log.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s %q %s %q\n", r.Name, r.Comment, r.ModTime.UTC().Format(time.DateOnly), out)
	// Output:
	// notes.txt "an example" 2026-11-05 "hello, gzip\n"
}

// Reuse one Writer for several members with Reset (no allocation once its
// buffers have grown).
func ExampleWriter_Reset() {
	w := gzip.NewWriter(nil)
	for _, s := range []string{"one", "two"} {
		var buf bytes.Buffer
		w.Reset(&buf)
		io.WriteString(w, s)
		w.Close()
		r, _ := stdgzip.NewReader(&buf)
		out, _ := io.ReadAll(r)
		fmt.Println(string(out))
	}
	// Output:
	// one
	// two
}
