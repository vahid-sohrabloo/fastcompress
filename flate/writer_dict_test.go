package flate_test

import (
	"bytes"
	stdflate "compress/flate"
	"errors"
	"io"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/flate"
)

// A preset dictionary (only its last 32 KiB) is referenced and the stream
// decodes with the standard library's NewReaderDict; Reset keeps it.
func TestWriterDict(t *testing.T) {
	for _, dl := range []int{0, 1, 1000, 32768, 50000} {
		dict := streamData(uint64(dl), dl)
		data := append(bytes.Clone(dict[max(0, dl-20000):]), streamData(5, 50000)...)
		for _, level := range []int{flate.HuffmanOnly, 0, 1, 6, 12} {
			var b bytes.Buffer
			z, err := flate.NewWriterDict(&b, level, dict)
			if err != nil {
				t.Fatal(err)
			}
			for round := range 2 {
				b.Reset()
				z.Reset(&b)
				z.Write(data)
				z.Close()
				got, err := io.ReadAll(stdflate.NewReaderDict(bytes.NewReader(b.Bytes()), dict))
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("dict %d level %d round %d: err %v", dl, level, round, err)
				}
				var plain bytes.Buffer
				pz, _ := flate.NewWriter(&plain, level)
				pz.Write(data)
				pz.Close()
				if dl >= 1000 && level >= 1 && b.Len() >= plain.Len() {
					t.Fatalf("dict %d level %d: %d bytes, without dictionary %d: dictionary not used", dl, level,
						b.Len(), plain.Len())
				}
			}
		}
	}
	if _, err := flate.NewWriterDict(io.Discard, 13, nil); !errors.Is(err, flate.ErrInvalidLevel) {
		t.Fatalf("level 13: %v", err)
	}
	// The dictionary is copied.
	dict := []byte("a dictionary of words")
	var b bytes.Buffer
	z, _ := flate.NewWriterDict(&b, 6, dict)
	copy(dict, "XXXXXXXXXXXXXXXXXXXXX")
	z.Write([]byte("a dictionary of words"))
	z.Close()
	got, err := io.ReadAll(stdflate.NewReaderDict(bytes.NewReader(b.Bytes()), []byte("a dictionary of words")))
	if err != nil || string(got) != "a dictionary of words" {
		t.Fatalf("dictionary not copied: %q %v", got, err)
	}
}
