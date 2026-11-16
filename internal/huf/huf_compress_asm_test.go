//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

package huf

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// hufVariant is one assembly stream writer (hufAsmVariants lists them per
// architecture).
type hufVariant struct {
	name string
	fn   func(dst *byte, dstLen int, src *byte, srcLen int, ct *CElt) int
	max  uint        // largest tableLog it accepts
	ok   func() bool // whether the CPU runs it
	// tight: only for dstLen >= srcLen*tableLog/8 + 8
	tight bool
}

// TestCompress1XAsmVariants checks every assembly stream writer against
// compress1XBodyGeneric, on every input length up to 300 and some larger
// ones, for each table log, with dst capacities around the tight bound
// (the tight variants' precondition) and around the size that just
// fails (so the end-mark overflow check is exercised).
func TestCompress1XAsmVariants(t *testing.T) {
	variants := hufAsmVariants()
	r := rand.New(rand.NewPCG(3, 4))
	wksp := make([]byte, WorkspaceSize)
	var lens []int
	for n := 0; n <= 300; n++ {
		lens = append(lens, n)
	}
	lens = append(lens, 1000, 4097, 40961, 128<<10)
	for _, tableLog := range []uint{5, 6, 7, 8, 9, 10, 11, 12} {
		for _, n := range lens {
			src := make([]byte, n)
			alpha := 2 + r.IntN(min(254, 1<<(tableLog-1)-2)) // symbols that fit in tableLog bits
			for i := range src {
				src[i] = byte(r.IntN(r.IntN(alpha) + 1))
			}
			if n > 0 {
				src[0] = byte(alpha - 1)
			}
			var count [256]uint32
			maxSym := uint32(255)
			for _, b := range src {
				count[b]++
			}
			for maxSym > 0 && count[maxSym] == 0 {
				maxSym--
			}
			if n < 2 {
				count[0], count[1], maxSym = 1, 1, 1
			}
			ct := make(CTable, CTableSize(255))
			got, err := BuildCTable(ct, &count, uint(maxSym), tableLog, wksp)
			if err != nil {
				t.Fatalf("BuildCTable(n=%d, log=%d): %v", n, tableLog, err)
			}
			tl := uint32(ReadCTableHeader(ct).TableLog)
			if uint(tl) != got {
				t.Fatalf("header tableLog %d, built %d", tl, got)
			}
			bound := n*int(tl)>>3 + 8
			want := make([]byte, bound+64)
			full := compress1XBodyGeneric(want, src, ct)
			caps := []int{9, 16, full - 1, full, full + 1, full + 8, bound, bound + 1, bound + 64}
			elts := (*[SymbolValueMax + 1]CElt)(ct[1:])
			for _, c := range caps {
				if c <= 8 || c > len(want) {
					continue
				}
				ref := make([]byte, c)
				wn := compress1XBodyGeneric(ref, src, ct)
				for _, v := range variants {
					if uint(tl) > v.max || !v.ok() || (v.tight && c < bound) {
						continue
					}
					dst := make([]byte, c)
					var sp *byte
					if n > 0 {
						sp = &src[0]
					}
					gn := v.fn(&dst[0], c, sp, n, &elts[0])
					if gn != wn || !bytes.Equal(dst[:gn], ref[:wn]) {
						t.Fatalf("%s: n=%d tableLog=%d cap=%d: got %d bytes, want %d", v.name, n, tl, c, gn, wn)
					}
				}
			}
		}
	}
}

// FuzzCompress1XAsmVariants compares the assembly stream writers with
// compress1XBodyGeneric on arbitrary input, with the table built for it at
// a table log taken from the first byte and a dst capacity from the second
// (around the tight bound).
func FuzzCompress1XAsmVariants(f *testing.F) {
	f.Add(uint8(11), uint8(0), []byte("abracadabra, abracadabra; the quick brown fox"))
	f.Add(uint8(6), uint8(3), bytes.Repeat([]byte{1, 2, 3, 1, 1, 0}, 100))
	wksp := make([]byte, WorkspaceSize)
	f.Fuzz(func(t *testing.T, logSel, capSel uint8, src []byte) {
		if len(src) < 2 || len(src) > 1<<17 {
			return
		}
		var count [256]uint32
		maxSym := uint32(0)
		for _, b := range src {
			count[b]++
			maxSym = max(maxSym, uint32(b))
		}
		nz := 0
		for _, c := range count[:maxSym+1] {
			if c > 0 {
				nz++
			}
		}
		if nz < 2 {
			return
		}
		tableLog := uint(5 + logSel%8)
		for 1<<(tableLog-1) < nz && tableLog < TableLogMax {
			tableLog++
		}
		ct := make(CTable, CTableSize(255))
		if _, err := BuildCTable(ct, &count, uint(maxSym), tableLog, wksp); err != nil {
			return
		}
		tl := int(ReadCTableHeader(ct).TableLog)
		bound := len(src)*tl>>3 + 8
		c := max(bound+int(capSel%16)-8, 9)
		ref := make([]byte, c)
		wn := compress1XBodyGeneric(ref, src, ct)
		elts := (*[SymbolValueMax + 1]CElt)(ct[1:])
		for _, v := range hufAsmVariants() {
			if uint(tl) > v.max || !v.ok() || (v.tight && c < bound) {
				continue
			}
			dst := make([]byte, c)
			gn := v.fn(&dst[0], c, &src[0], len(src), &elts[0])
			if gn != wn || !bytes.Equal(dst[:gn], ref[:wn]) {
				t.Fatalf("%s: n=%d tableLog=%d cap=%d: got %d bytes, want %d", v.name, len(src), tl, c, gn, wn)
			}
		}
	})
}
