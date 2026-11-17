package huf

import (
	"math/rand/v2"
	"testing"
)

// FuzzReadCTable feeds arbitrary table descriptions to ReadCTable (and so
// ReadStats and the FSE weight decoder): no panic, no read beyond the
// input, and a table that WriteCTable can write reads back identically.
func FuzzReadCTable(f *testing.F) {
	f.Add(uint8(255), []byte{})
	f.Add(uint8(255), []byte{0x80, 0x11})
	f.Add(uint8(3), []byte{0x81, 0x12, 0x30})
	r := rand.New(rand.NewPCG(7, 8))
	wksp := make([]byte, CTableWorkspaceSize)
	for i := 0; i < 8; i++ {
		var count [256]uint32
		ms := uint(1 + r.IntN(255))
		for s := uint(0); s <= ms; s++ {
			count[s] = uint32(r.IntN(1000))
		}
		count[0]++
		count[ms]++
		ct := make(CTable, CTableSize(255))
		mb, err := BuildCTable(ct, &count, ms, 11, wksp)
		if err != nil {
			continue
		}
		hdr := make([]byte, 300)
		if n, err := WriteCTable(hdr, ct, ms, mb, wksp); err == nil {
			f.Add(uint8(255), hdr[:n])
		}
	}
	f.Fuzz(func(t *testing.T, maxSym uint8, src []byte) {
		ct := make(CTable, CTableSize(255))
		m := uint(maxSym)
		size, _, err := ReadCTable(ct, &m, src)
		if err != nil {
			return
		}
		if size > len(src) {
			t.Fatalf("read %d bytes of %d", size, len(src))
		}
		h := ReadCTableHeader(ct)
		if uint(h.MaxSymbolValue) != m || m > uint(maxSym) || h.TableLog > TableLogMax {
			t.Fatalf("header %+v, maxSymbol %d (limit %d)", h, m, maxSym)
		}
		if m == 0 {
			return
		}
		hdr := make([]byte, 300)
		n, err := WriteCTable(hdr, ct, m, uint(h.TableLog), make([]byte, CTableWorkspaceSize))
		if err != nil {
			return
		}
		again := make(CTable, CTableSize(255))
		m2 := uint(255)
		if _, _, err := ReadCTable(again, &m2, hdr[:n]); err != nil || m2 != m {
			t.Fatalf("rewritten table: %v, maxSymbol %d want %d", err, m2, m)
		}
		for s := 0; s <= int(m)+1; s++ {
			if again[s] != ct[s] {
				t.Fatalf("element %d = %#x, want %#x", s, again[s], ct[s])
			}
		}
	})
}
