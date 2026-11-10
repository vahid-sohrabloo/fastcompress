package zstd

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
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

// TestLLIncPriceInvariant checks llIncPrice's dynamic fast path against its
// definition, llPrice(L) - llPrice(L-1), for every L in [1, 256] (and
// beyond 64, where it takes the two-call form) at every point where the
// parser prices: after rescaleFreqs (first block, then the downscale of
// later blocks) and after the setBasePrices that follows each run of
// updateStats, with random sequences. The fast path relies on
// litLengthSumBasePrice >= bitWeight(litLengthFreq[c]) for every code
// (the sum of the frequencies is at least each one), so a change that
// breaks that invariant fails here.
func TestLLIncPriceInvariant(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	check := func(o *optState, when string) {
		t.Helper()
		for l := uint32(1); l <= 256; l++ {
			if got, want := llIncPrice(l, o), llPrice(l, o)-llPrice(l-1, o); got != want {
				t.Fatalf("%s: llIncPrice(%d) = %d, llPrice difference %d", when, l, got, want)
			}
		}
	}
	lits := make([]byte, 1<<17)
	for i := range lits {
		lits[i] = byte(r.IntN(64))
	}
	for range 20 {
		o := &optState{symbolCosts: &entropyCTables{}}
		for range 8 {
			rescaleFreqs(o, lits[:1000+r.IntN(len(lits)-1000)])
			if o.priceType == zopPredef {
				t.Fatal("expected dynamic prices")
			}
			check(o, "after rescaleFreqs")
			for range 1 + r.IntN(50) {
				for range 1 + r.IntN(20) {
					ll := uint32(r.IntN(1 << uint(r.IntN(17))))
					updateStats(o, ll, lits, uint32(1+r.IntN(1<<20)), uint32(3+r.IntN(1000)))
				}
				setBasePrices(o)
				check(o, "after updateStats")
			}
		}
	}
}
