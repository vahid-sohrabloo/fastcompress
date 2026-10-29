package zstd

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// TestBtoptFewLiteralsCref: inputs of 16 KiB or less whose literals
// section holds 32 to 63 bytes, where btopt's ZSTD_minLiteralsToCompress
// (8 << min(9-strategy, 3) = 32) first differs from the lazy strategies'
// (64): the literals must be Huffman-compressed as in C. Level 11 (btopt,
// minMatch 4) and level 12 (btopt, minMatch 3).
func TestBtoptFewLiteralsCref(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	for _, n := range []int{33, 40, 48, 63} {
		lits := make([]byte, n)
		for i := range lits {
			lits[i] = 'a' + byte(r.IntN(8)) // compressible literals
		}
		src := bytes.Repeat(lits, 4096/n)
		for _, level := range []int{11, 12} {
			c := compress2Case{level: level}
			if getCParams(level, uint64(len(src)), 0).strategy != zstdBtopt {
				t.Fatalf("level %d is not btopt for %d bytes", level, len(src))
			}
			got, err := c.newCCtx(t).compress2(nil, src)
			if err != nil {
				t.Fatal(err)
			}
			if want := creftest.Run(t, src, c.crefArgs("compress")...); !bytes.Equal(got, want) {
				t.Fatalf("L%d/%d literals: frame differs from C (Go %d bytes, C %d bytes)%s", level, n, len(got), len(want), firstDiff(got, want))
			}
		}
	}
}
