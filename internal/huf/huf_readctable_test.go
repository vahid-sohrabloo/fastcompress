package huf

import (
	"math/rand/v2"
	"testing"
)

// TestReadCTableRoundTrip reads back what WriteCTable writes: the table
// read by ReadCTable equals the encoder's table (header, nbBits and code
// values), whether the weights are FSE-compressed or stored raw.
func TestReadCTableRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	wksp := make([]byte, CTableWorkspaceSize)
	var sawRaw, sawFSE, sawZero bool
	for i := 0; i < 3000; i++ {
		var count [256]uint32
		maxSymbol := uint(1 + r.IntN(255))
		for s := uint(0); s <= maxSymbol; s++ {
			if r.IntN(5) != 0 {
				count[s] = uint32(1 + r.IntN(1<<uint(r.IntN(14))))
			}
		}
		count[0]++
		count[maxSymbol]++
		ct := make(CTable, CTableSize(255))
		maxBits, err := BuildCTable(ct, &count, maxSymbol, uint(9+r.IntN(3)), wksp) // >= log2(alphabet)
		if err != nil {
			t.Fatal(err)
		}
		hdr := make([]byte, 300)
		n, err := WriteCTable(hdr, ct, maxSymbol, maxBits, wksp)
		if err != nil {
			t.Fatal(err)
		}
		if hdr[0] >= 128 {
			sawRaw = true
		} else {
			sawFSE = true
		}
		got := make(CTable, CTableSize(255))
		gotMax := uint(255)
		size, hasZeroWeights, err := ReadCTable(got, &gotMax, hdr[:n+r.IntN(4)])
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if size != n || gotMax != maxSymbol {
			t.Fatalf("case %d: size %d maxSymbol %d, want %d %d", i, size, gotMax, n, maxSymbol)
		}
		zero := false
		for s := uint(0); s <= maxSymbol; s++ {
			zero = zero || count[s] == 0
		}
		sawZero = sawZero || zero
		if hasZeroWeights != zero {
			t.Fatalf("case %d: hasZeroWeights %v, want %v", i, hasZeroWeights, zero)
		}
		for s := 0; s <= int(maxSymbol)+1; s++ {
			if got[s] != ct[s] {
				t.Fatalf("case %d: element %d = %#x, want %#x", i, s, got[s], ct[s])
			}
		}
		// A too small maxSymbolValue is an error.
		small := maxSymbol - 1
		if _, _, err := ReadCTable(got, &small, hdr[:n]); err == nil {
			t.Fatalf("case %d: maxSymbolValue %d accepted", i, small)
		}
	}
	if !sawRaw || !sawFSE || !sawZero {
		t.Fatalf("coverage: raw %v fse %v zero weights %v", sawRaw, sawFSE, sawZero)
	}
	got := make(CTable, CTableSize(255))
	m := uint(255)
	if _, _, err := ReadCTable(got, &m, nil); err == nil {
		t.Fatal("empty header accepted")
	}
}
