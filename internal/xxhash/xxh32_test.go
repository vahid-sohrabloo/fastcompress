package xxhash

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// TestSum32Cref compares Sum32 and Digest32 with C's XXH32 (lz4ref).
func TestSum32Cref(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	buf := make([]byte, 5000)
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	for _, n := range []int{0, 1, 3, 4, 15, 16, 17, 31, 32, 33, 100, 4999} {
		for _, seed := range []uint32{0, 2654435761} {
			want := strings.TrimSpace(string(creftest.LZ4Run(t, buf[:n], "xxh32", fmt.Sprintf("seed=%d", seed))))
			if got := fmt.Sprintf("%08x", Sum32(buf[:n], seed)); got != want {
				t.Errorf("Sum32(%d bytes, %d) = %s, C %s", n, seed, got, want)
			}
			for _, step := range []int{1, 3, 16, 50} {
				var d Digest32
				d.Reset(seed)
				for i := 0; i < n; i += step {
					d.Write(buf[i:min(i+step, n)])
				}
				if got := fmt.Sprintf("%08x", d.Sum32()); got != want {
					t.Errorf("Digest32(%d bytes, %d, step %d) = %s, C %s", n, seed, step, got, want)
				}
			}
		}
	}
}
