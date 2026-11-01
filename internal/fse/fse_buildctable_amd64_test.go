//go:build amd64 && !appengine && !noasm && !purego && gc

package fse

import (
	"math/rand/v2"
	"testing"
)

// TestBuildCTableAsmEquiv compares buildCTableAsm with the Go builder on
// normalized counts of random histograms: with and without low-probability
// (-1) symbols, zero holes, every table log, and alphabets up to 256.
func TestBuildCTableAsmEquiv(t *testing.T) {
	if !useFSEBuildAsm {
		t.Skip("fse-build assembly is off")
	}
	r := rand.New(rand.NewPCG(3, 4))
	var count [256]uint32
	var norm [256]int16
	iters := 100000
	if testing.Short() {
		iters = 10000
	}
	for i := 0; i < iters; i++ {
		maxSym := []int{1, 12, 28, 35, 52, 255, r.IntN(256)}[r.IntN(7)]
		total := 0
		for s := 0; s <= maxSym; s++ {
			c := 0
			switch r.IntN(4) {
			case 0:
				c = r.IntN(3) // rare symbols: -1 norms
			case 1:
				c = r.IntN(50)
			case 2:
				c = r.IntN(2000)
			}
			count[s] = uint32(c)
			total += c
		}
		extra := 1 + r.IntN(10)
		count[maxSym] += uint32(extra)
		total += extra
		if total < 2 {
			continue
		}
		tableLog := uint(MinTableLog + r.IntN(MaxTableLog-MinTableLog+1))
		tl, err := NormalizeCount(norm[:], tableLog, count[:maxSym+1], total, uint(maxSym), r.IntN(2) == 0)
		if err != nil || tl == 0 {
			continue
		}
		{
			sum := 0
			for _, n := range norm[:maxSym+1] {
				sum += max(int(n), -int(n))
			}
			if sum != 1<<tl {
				t.Fatalf("case %d: NormalizeCount(tableLog %d, total %d, maxSym %d) sums to %d", i, tableLog, total, maxSym, sum)
			}
		}
		size := CTableSizeU32(tl, uint(maxSym))
		ctA, ctG := make(CTable, size), make(CTable, size)
		wksp := make([]byte, BuildCTableWorkspaceSize(uint(maxSym), tl)+8)
		if errG := buildCTableGeneric(ctG, norm[:maxSym+1], uint(maxSym), tl, wksp); errG != nil {
			t.Fatalf("case %d: Go: %v", i, errG)
		}
		if maxSym >= 1<<tl {
			continue // buildCTable runs the Go body
		}
		if r := buildCTableAsm(&ctA[0], &norm[0], uint(maxSym), tl, &wksp[0], uint(len(wksp))); r != 0 {
			t.Fatalf("case %d (maxSym %d, tableLog %d): asm returned %d", i, maxSym, tl, r)
		}
		for j := range ctA {
			if ctA[j] != ctG[j] {
				t.Fatalf("case %d (maxSym %d, tableLog %d): ct[%d] asm %#x, Go %#x", i, maxSym, tl, j, ctA[j], ctG[j])
			}
		}
	}
}

// TestBuildCTableAsmBadNorm: a norm that does not sum to the table size, or
// holds a value below -1, makes the assembly return -1 instead of writing
// out of bounds.
func TestBuildCTableAsmBadNorm(t *testing.T) {
	if !useFSEBuildAsm {
		t.Skip("fse-build assembly is off")
	}
	const tl = 6
	ct := make(CTable, CTableSizeU32(tl, 3))
	wksp := make([]byte, BuildCTableWorkspaceSize(3, tl)+8)
	for _, norm := range [][]int16{
		{30, 30, 2, 3},   // 65
		{30, 30, 2, 1},   // 63
		{70, -1, -5, -1}, // 64 with -5 read as 0xfffb
		{-1, -1, -1, 61}, // valid: 64
	} {
		want := int64(-1)
		if norm[3] == 61 {
			want = 0
		}
		if r := buildCTableAsm(&ct[0], &norm[0], 3, tl, &wksp[0], uint(len(wksp))); r != want {
			t.Errorf("norm %v: asm returned %d, want %d", norm, r, want)
		}
	}
}
