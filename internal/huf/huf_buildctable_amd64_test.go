//go:build amd64 && !appengine && !noasm && !purego && gc

package huf

import (
	"math/rand/v2"
	"testing"
)

// histogram fills count[0..maxSym] in one of the shapes the Task 20i review
// compared, with zero holes, and returns the number of nonzero counts.
func histogram(r *rand.Rand, count *[256]uint32, maxSym int, mode int) int {
	*count = [256]uint32{}
	for s := 0; s <= maxSym; s++ {
		var c uint32
		switch mode {
		case 0: // small counts: many ties and zeros
			c = uint32(r.IntN(6))
		case 1: // all equal
			c = 7
		case 2: // skewed powers of two
			c = 1 << r.IntN(17)
		case 3: // Fibonacci-like: deep trees
			a, b := uint32(1), uint32(1)
			for i := 0; i < s%25; i++ {
				a, b = b, a+b
			}
			c = a
		case 4: // wide, one log2 bucket: the bucket quicksort
			c = 1<<10 + uint32(r.IntN(1<<10))
		case 5: // few distinct values, tie heavy, above the distinct cutoff
			c = 200 * uint32(1+r.IntN(3))
		case 6: // random totals up to 2^17
			c = uint32(r.IntN(1 << uint(r.IntN(10)+1)))
		}
		if mode != 1 && mode != 4 && r.IntN(8) == 0 {
			c = 0 // hole
		}
		count[s] = c
	}
	n := 0
	for s := 0; s <= maxSym; s++ {
		if count[s] != 0 {
			n++
		}
	}
	return n
}

// TestBuildCTableAsmEquiv compares buildCTableAsm with the Go builder: the
// result, the error and every table word.
func TestBuildCTableAsmEquiv(t *testing.T) {
	if !useHufBuildAsm {
		t.Skip("huf-build assembly is off")
	}
	r := rand.New(rand.NewPCG(1, 2))
	var count [256]uint32
	ctA := make(CTable, CTableSize(255))
	ctG := make(CTable, CTableSize(255))
	iters := 200000
	if testing.Short() {
		iters = 20000
	}
	for i := 0; i < iters; i++ {
		maxSym := []int{1, 2, 254, 255, 255, r.IntN(256)}[r.IntN(6)]
		mode := r.IntN(7)
		n := histogram(r, &count, maxSym, mode)
		if n == 0 {
			count[r.IntN(maxSym+1)] = 1 + uint32(r.IntN(100))
		}
		// maxNbBits 0 is the default (11); a limit needs 2^maxNbBits >= n
		// (C's callers guarantee it, and both bodies index out of range
		// without it).
		maxNbBits := uint32([]int{0, 5, 6, 8, 9, 10, 11, 12}[r.IntN(8)])
		for maxNbBits != 0 && 1<<maxNbBits < n {
			maxNbBits++
		}
		for j := range ctA {
			ctA[j], ctG[j] = CElt(r.Uint64()), CElt(r.Uint64())
		}
		nG, errG := buildCTableGeneric(ctG, &count, uint32(maxSym), maxNbBits)
		nA := buildCTableAsm(&ctA[0], &count, uint32(maxSym), maxNbBits)
		if errG != nil {
			if int64(nA) != -1 {
				t.Fatalf("case %d (mode %d, maxSym %d, maxNbBits %d): asm %d, Go error %v", i, mode, maxSym, maxNbBits, int64(nA), errG)
			}
			continue
		}
		if uint64(nG) != nA {
			t.Fatalf("case %d (mode %d, maxSym %d, maxNbBits %d): asm %d, Go %d", i, mode, maxSym, maxNbBits, int64(nA), nG)
		}
		for j := 0; j <= maxSym+1; j++ {
			if ctA[j] != ctG[j] {
				t.Fatalf("case %d (mode %d, maxSym %d, maxNbBits %d): ct[%d] asm %#x, Go %#x", i, mode, maxSym, maxNbBits, j, ctA[j], ctG[j])
			}
		}
	}
}

// TestBuildCTableAsmErrors checks the assembly's C error returns and the
// wrapper's fallbacks.
func TestBuildCTableAsmErrors(t *testing.T) {
	if !useHufBuildAsm {
		t.Skip("huf-build assembly is off")
	}
	var count [256]uint32
	ct := make(CTable, CTableSize(255))
	count[3] = 5
	if r := int64(buildCTableAsm(&ct[0], &count, 256, 11)); r != -46 {
		t.Errorf("maxSymbolValue 256: %d, want -46", r)
	}
	count = [256]uint32{}
	if r := int64(buildCTableAsm(&ct[0], &count, 255, 11)); r != -2 {
		t.Errorf("all-zero histogram: %d, want -2", r)
	}
}
