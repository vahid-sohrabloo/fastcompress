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

// TestNormalizeCountAsmEquiv compares normalizeCountAsm with the Go body:
// the result, the error and every norm entry. The histograms cover RLE, the
// normalizeM2 fallback (many counts just above total>>tableLog), tiny
// totals, alphabets 2-256 and table logs 0-13.
func TestNormalizeCountAsmEquiv(t *testing.T) {
	if !useFSENormAsm {
		t.Skip("fse-norm assembly is off")
	}
	r := rand.New(rand.NewPCG(7, 8))
	var count [256]uint32
	var normA, normG [256]int16
	iters := 300000
	if testing.Short() {
		iters = 30000
	}
	m2 := 0
	for i := 0; i < iters; i++ {
		maxSym := []int{1, 2, 12, 35, 52, 255, 1 + r.IntN(255)}[r.IntN(7)]
		mode := r.IntN(9)
		total := 0
		for s := 0; s <= maxSym; s++ {
			var c int
			switch mode {
			case 0:
				c = r.IntN(4)
			case 1:
				c = r.IntN(1 << uint(1+r.IntN(16)))
			case 2: // near the low threshold: the normalizeM2 path
				c = 1 + r.IntN(3)
			case 3:
				c = 0
			case 4:
				c = []int{0, 1, 2, 1000}[r.IntN(4)]
			case 5:
				c = r.IntN(100)
			case 6: // one large count among ones
				c = 1
				if s == 0 {
					c = 1 << uint(8+r.IntN(12))
				}
			case 7: // all equal
				c = 3
			case 8: // ones and twos, a few large
				c = 1 + r.IntN(2)
				if r.IntN(16) == 0 {
					c = r.IntN(5000)
				}
			}
			count[s] = uint32(c)
			total += c
		}
		if mode == 3 { // RLE
			s := r.IntN(maxSym + 1)
			count[s] = 1 + uint32(r.IntN(1000))
			total = int(count[s])
		}
		if total == 0 {
			count[maxSym] = 1
			total = 1
		}
		tableLog := uint(r.IntN(14))
		lowProb := r.IntN(2) == 0
		for j := range normA {
			normA[j], normG[j] = int16(r.Uint32()), int16(r.Uint32())
		}
		gotG, errG := normalizeCountGeneric(normG[:maxSym+1], tableLog, count[:maxSym+1], total, uint(maxSym), lowProb)
		gotA, errA := normalizeCount(normA[:maxSym+1], tableLog, count[:maxSym+1], total, uint(maxSym), lowProb)
		if gotA != gotG || errA != errG {
			t.Fatalf("case %d (mode %d, maxSym %d, tableLog %d, total %d): asm %d %v, Go %d %v", i, mode, maxSym, tableLog, total, gotA, errA, gotG, errG)
		}
		if errG != nil || gotG == 0 {
			continue
		}
		if mode == 2 {
			m2++
		}
		for j := 0; j <= maxSym; j++ {
			if normA[j] != normG[j] {
				t.Fatalf("case %d (mode %d, maxSym %d, tableLog %d, total %d): norm[%d] asm %d, Go %d", i, mode, maxSym, tableLog, total, j, normA[j], normG[j])
			}
		}
	}
	if m2 == 0 {
		t.Fatal("no near-threshold case ran")
	}
}

// TestCompressWeightsAsmEquiv compares the assembly encoder with the Go
// body: size and bytes, for src lengths 1-700 (every length mod 4), table
// logs 5-12, random symbols up to ct's maxSymbol (also zero-probability
// ones, which cost tableLog+1 bits), and dst from 1 byte to past
// FSE_BLOCKBOUND (both flush kinds). Bytes past len(dst) must stay
// untouched.
func TestCompressWeightsAsmEquiv(t *testing.T) {
	if !useFSECompressAsm {
		t.Skip("fse-compress assembly is off")
	}
	r := rand.New(rand.NewPCG(13, 14))
	var count [256]uint32
	var norm [256]int16
	src := make([]byte, 700)
	var bufA, bufG [1000]byte
	iters := 200000
	if testing.Short() {
		iters = 20000
	}
	for i := 0; i < iters; i++ {
		maxSym := []int{1, 5, 12, 35, 255, 1 + r.IntN(255)}[r.IntN(6)]
		total := 0
		for s := 0; s <= maxSym; s++ {
			c := []int{0, r.IntN(3), r.IntN(100), r.IntN(3000)}[r.IntN(4)]
			count[s] = uint32(c)
			total += c
		}
		count[maxSym]++
		total++
		tl, err := NormalizeCount(norm[:], uint(MinTableLog+r.IntN(MaxTableLog-MinTableLog+1)), count[:maxSym+1], total, uint(maxSym), r.IntN(2) == 0)
		if err != nil || tl == 0 {
			continue
		}
		ct := make(CTable, CTableSizeU32(tl, uint(maxSym)))
		wksp := make([]byte, BuildCTableWorkspaceSize(uint(maxSym), tl)+8)
		if err := BuildCTable(ct, norm[:maxSym+1], uint(maxSym), tl, wksp); err != nil {
			t.Fatal(err)
		}
		n := 1 + r.IntN(len(src))
		for j := range src[:n] {
			src[j] = byte(r.IntN(maxSym + 1))
		}
		bound := n + n>>7 + 4 + 8
		d := []int{1, 8, 9, r.IntN(bound + 1), bound - 1, bound, bound + 20}[r.IntN(7)]
		d = max(d, 1)
		for j := range bufA {
			bufA[j], bufG[j] = 0xA5, 0xA5
		}
		gotG := compressUsingCTableGeneric(bufG[:d], src[:n], ct, d >= bound && tl <= 7)
		gotA, _ := CompressWeightsUsingCTable(bufA[:d], src[:n], ct)
		if gotA != gotG {
			t.Fatalf("case %d (maxSym %d, tableLog %d, src %d, dst %d): asm %d, Go %d", i, maxSym, tl, n, d, gotA, gotG)
		}
		for j := d; j < len(bufA); j++ {
			if bufA[j] != 0xA5 {
				t.Fatalf("case %d (src %d, dst %d): asm wrote byte %d", i, n, d, j)
			}
		}
		if string(bufA[:gotA]) != string(bufG[:gotG]) {
			t.Fatalf("case %d (maxSym %d, tableLog %d, src %d, dst %d): bytes differ", i, maxSym, tl, n, d)
		}
	}
}

// TestBuildCTableSmallTableLog: at tableLog 1-4 C's step can be a multiple
// of the table size (tableLog 1 and 3), so its spread leaves tableSymbol
// mostly unwritten and the build then indexes cumul and the state table
// with stale workspace bytes. BuildCTable must not write past ct or wksp
// there (the Go body runs those logs; it may panic, recoverably).
func TestBuildCTableSmallTableLog(t *testing.T) {
	for tl := uint(1); tl <= 4; tl++ {
		maxSym := uint(1)<<tl - 1
		norm := make([]int16, maxSym+1)
		for s := range norm {
			norm[s] = 1
		}
		ctSize := CTableSizeU32(tl, maxSym)
		wkspSize := BuildCTableWorkspaceSize(maxSym, tl) + 8
		ct := make([]uint32, ctSize+4096)
		for j := range ct {
			ct[j] = 0xDEADBEEF
		}
		wksp := make([]byte, wkspSize+4096)
		for j := range wksp {
			wksp[j] = 0xC3 // stale workspace: symbol 0xC3
		}
		func() {
			defer func() { _ = recover() }()
			_ = BuildCTable(CTable(ct[:ctSize]), norm, maxSym, tl, wksp[:wkspSize])
		}()
		for j := ctSize; j < len(ct); j++ {
			if ct[j] != 0xDEADBEEF {
				t.Fatalf("tableLog %d: word %d past ct written", tl, j-ctSize)
			}
		}
		for j := wkspSize; j < len(wksp); j++ {
			if wksp[j] != 0xC3 {
				t.Fatalf("tableLog %d: byte %d past wksp written", tl, j-wkspSize)
			}
		}
	}
}

// TestNormalizeCountAsmLowCounts compares normalizeCount's assembly with
// the Go body on histograms of ones (and a few twos) whose total is close
// to the table size: each proba rounds up, the overshoot sends them to
// normalizeM2, and there they reach the all-low branch (no zero holes) and
// the total == 0 round robin (with holes). (Small nbSeq can do this in
// ZSTD_NCountCost.)
func TestNormalizeCountAsmLowCounts(t *testing.T) {
	if !useFSENormAsm {
		t.Skip("fse-norm assembly is off")
	}
	r := rand.New(rand.NewPCG(15, 16))
	var count [256]uint32
	var normA, normG [256]int16
	for tl := uint(MinTableLog); tl <= MaxTableLog; tl++ {
		ts := 1 << tl
		for n := ts / 3; n < ts && n <= 256; n++ {
			for holes := 0; holes < 2; holes++ {
				for twos := 0; twos < 3; twos++ {
					count = [256]uint32{}
					maxSym, total, placed := 0, 0, 0
					for s := 0; s < 256 && placed < n; s++ {
						if holes == 1 && s%3 == 1 && 255-s > n-placed {
							continue
						}
						c := uint32(1)
						if twos > 0 && r.IntN(4*twos) == 0 {
							c = 2
						}
						count[s] = c
						total += int(c)
						placed++
						maxSym = s
					}
					if maxSym == 0 {
						continue
					}
					for _, low := range []bool{false, true} {
						gotG, errG := normalizeCountGeneric(normG[:maxSym+1], tl, count[:maxSym+1], total, uint(maxSym), low)
						gotA, errA := normalizeCount(normA[:maxSym+1], tl, count[:maxSym+1], total, uint(maxSym), low)
						if gotA != gotG || errA != errG || (errG == nil && normA != normG) {
							t.Fatalf("tableLog %d, %d symbols, holes %d, twos %d, total %d: asm %d %v, Go %d %v", tl, n, holes, twos, total, gotA, errA, gotG, errG)
						}
					}
				}
			}
		}
	}
}
