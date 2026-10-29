package xxhash

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

var seeds = []uint64{0, 1, 0x9E3779B185EBCA87}

func TestXXH64Cref(t *testing.T) {
	for name, in := range creftest.Inputs(t) {
		for _, seed := range seeds {
			want := string(creftest.Run(t, in, "xxh64", "-seed", strconv.FormatUint(seed, 10)))
			if got := fmt.Sprintf("%016x\n", Sum64(in, seed)); got != want {
				t.Errorf("%s seed=%#x: Sum64 = %q, want %q", name, seed, got, want)
			}
			for _, piece := range []int{1, 7, 64, 4096} {
				d := New(seed)
				for b := in; len(b) > 0; {
					n := min(piece, len(b))
					if _, err := d.Write(b[:n]); err != nil {
						t.Fatal(err)
					}
					b = b[n:]
				}
				if got := fmt.Sprintf("%016x\n", d.Sum64()); got != want {
					t.Errorf("%s seed=%#x piece=%d: streaming = %q, want %q", name, seed, piece, got, want)
				}
			}
		}
	}
}

func TestXXH64Known(t *testing.T) {
	// Published XXH64 vector: empty input, seed 0.
	if got := Sum64(nil, 0); got != 0xEF46DB3751D8E999 {
		t.Fatalf("empty = %#x", got)
	}
	d := New(0)
	if d.Sum64() != 0xEF46DB3751D8E999 {
		t.Fatal("empty digest mismatch")
	}
	d.Write([]byte("abc"))
	d.Reset()
	if d.Sum64() != 0xEF46DB3751D8E999 {
		t.Fatal("Reset did not clear state")
	}
}

func TestXXH64NoAlloc(t *testing.T) {
	b := make([]byte, 1000)
	d := New(0)
	if n := testing.AllocsPerRun(100, func() {
		d.Reset()
		d.Write(b)
		_ = d.Sum64()
		_ = Sum64(b, 0)
	}); n != 0 {
		t.Fatalf("allocs = %v", n)
	}
}

func BenchmarkSum64(b *testing.B) {
	buf := make([]byte, 128<<10)
	b.SetBytes(int64(len(buf)))
	for i := 0; i < b.N; i++ {
		Sum64(buf, 0)
	}
}
