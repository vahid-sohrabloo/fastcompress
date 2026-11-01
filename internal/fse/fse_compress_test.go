package fse

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

const maxInput = 128 << 10

func TestFSECref(t *testing.T) {
	for name, in := range creftest.Inputs(t) {
		if len(in) > maxInput {
			in = in[:maxInput]
		}
		for _, tl := range []uint{5, 9, 11, 12} {
			want := creftest.Run(t, in, "fse", "-tablelog", strconv.Itoa(int(tl)))
			dst := make([]byte, len(in)+len(in)>>7+1024)
			n, err := Compress2(dst, in, 255, tl)
			if err != nil {
				t.Fatalf("%s tl=%d: %v", name, tl, err)
			}
			if !bytes.Equal(dst[:n], want) {
				t.Errorf("%s tl=%d: Go %d bytes, C %d bytes (mismatch)", name, tl, n, len(want))
			}
		}
	}
}

func TestNormalizeCountSumsToTableSize(t *testing.T) {
	var count [256]uint32
	src := []byte("abracadabra abracadabra, the quick brown fox jumps over the lazy dog")
	for _, b := range src {
		count[b]++
	}
	for _, lowProb := range []bool{false, true} {
		var norm [256]int16
		tl, err := NormalizeCount(norm[:], 9, count[:], len(src), 255, lowProb)
		if err != nil || tl != 9 {
			t.Fatalf("NormalizeCount: tl=%d err=%v", tl, err)
		}
		sum := 0
		for _, v := range norm {
			if v < 0 {
				v = -v
			}
			sum += int(v)
		}
		if sum != 1<<9 {
			t.Errorf("lowProb=%v: sum=%d want 512", lowProb, sum)
		}
	}
}

func TestWriteNCountErrors(t *testing.T) {
	var norm [256]int16
	if _, err := WriteNCount(make([]byte, 600), norm[:], 255, 13); err == nil {
		t.Error("tableLog 13: want error")
	}
	if _, err := WriteNCount(make([]byte, 600), norm[:], 255, 4); err == nil {
		t.Error("tableLog 4: want error")
	}
}

func TestBuildCTableWorkspaceTooSmall(t *testing.T) {
	norm := make([]int16, 256)
	norm[0], norm[1] = 16, 16
	ct := make(CTable, CTableSizeU32(5, 1))
	if err := BuildCTable(ct, norm, 1, 5, make([]byte, 4)); err == nil {
		t.Error("want tableLog_tooLarge error for small workspace")
	}
}

// A norm that does not sum to the table size must panic, not write past the
// state table into symbolTT. With a low-probability symbol 0, symbol 2's
// spread overwrites symbol 1's slots, so cumul[2] (21) plus its 20 slots
// runs past tableSize 32.
func TestBuildCTableInvalidNormPanics(t *testing.T) {
	norm := []int16{-1, 20, 20}
	ct := make(CTable, CTableSizeU32(5, 2))
	wksp := make([]byte, BuildCTableWorkspaceSize(2, 5))
	defer func() {
		if recover() == nil {
			t.Error("BuildCTable with an invalid norm did not panic")
		}
	}()
	_ = BuildCTable(ct, norm, 2, 5, wksp)
}

func TestCTableLayout(t *testing.T) {
	norm := make([]int16, 256)
	norm[0], norm[1], norm[2] = 16, 8, 8
	ct := make(CTable, CTableSizeU32(5, 2))
	wksp := make([]byte, BuildCTableWorkspaceSize(2, 5))
	if err := BuildCTable(ct, norm, 2, 5, wksp); err != nil {
		t.Fatal(err)
	}
	if ct[0] != 5|2<<16 {
		t.Errorf("header = %#x, want tableLog 5 | maxSymbol 2<<16", ct[0])
	}
	if len(ct.SymbolTT()) != 2*3 {
		t.Errorf("symbolTT len = %d, want 6", len(ct.SymbolTT()))
	}
	// maxBitsOut = tableLog - highbit32(norm-1): norm 16 -> 5-3 = 2, norm 8 -> 5-2 = 3.
	if g := GetMaxNbBits(ct.SymbolTT(), 0); g != 2 {
		t.Errorf("GetMaxNbBits(0) = %d, want 2", g)
	}
	if g := GetMaxNbBits(ct.SymbolTT(), 1); g != 3 {
		t.Errorf("GetMaxNbBits(1) = %d, want 3", g)
	}
}
