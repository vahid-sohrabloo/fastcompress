package huf

import (
	"bytes"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

const maxInput = 128 << 10

// crefHuf runs the C oracle (mode huf1x/huf4x) as documented in cref.c.
func crefHuf(t *testing.T, in []byte, streams int, tableLog uint, flags int) []byte {
	t.Helper()
	return creftest.Run(t, in, "huf"+strconv.Itoa(streams)+"x",
		"-tablelog", strconv.Itoa(int(tableLog)), "-flags", strconv.Itoa(flags))
}

// goHuf mirrors cmdHuf in cref.c: fresh table, RepeatNone, 255, cap = n+n/256+1024.
func goHuf(t *testing.T, in []byte, streams int, tableLog uint, flags int) []byte {
	t.Helper()
	dst := make([]byte, len(in)+len(in)>>8+1024)
	wksp := make([]byte, WorkspaceSize)
	table := make(CTable, CTableSize(255))
	rep := RepeatNone
	var n int
	var err error
	if streams == 1 {
		n, err = Compress1XRepeat(dst, in, 255, tableLog, wksp, table, &rep, flags)
	} else {
		n, err = Compress4XRepeat(dst, in, 255, tableLog, wksp, table, &rep, flags)
	}
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	return dst[:n]
}

// synthetic inputs that exercise deep trees (setMaxHeight), many distinct
// counts (sort buckets), tiny sizes and all unroll remainders.
func synthetic() map[string][]byte {
	m := map[string][]byte{}
	r := rand.New(rand.NewPCG(1, 2))
	// geometric distribution -> deep unlimited tree
	geo := make([]byte, 0, 100000)
	for s := 0; s < 40; s++ {
		cnt := 100000 >> min(s/2+1, 16)
		for i := 0; i < cnt/2+1 && len(geo) < cap(geo); i++ {
			geo = append(geo, byte(s))
		}
	}
	r.Shuffle(len(geo), func(i, j int) { geo[i], geo[j] = geo[j], geo[i] })
	m["geo"] = geo
	// fibonacci counts: maximally deep Huffman tree
	var fib []byte
	a, b := 1, 1
	for s := 0; s < 24 && len(fib) < maxInput-200000; s++ {
		for i := 0; i < a && len(fib) < maxInput; i++ {
			fib = append(fib, byte(s*7))
		}
		a, b = b, a+b
	}
	r.Shuffle(len(fib), func(i, j int) { fib[i], fib[j] = fib[j], fib[i] })
	m["fib"] = fib
	// many distinct counts: symbol s appears s+1 times
	var tri []byte
	for s := 0; s < 256; s++ {
		for i := 0; i <= s; i++ {
			tri = append(tri, byte(s))
		}
	}
	r.Shuffle(len(tri), func(i, j int) { tri[i], tri[j] = tri[j], tri[i] })
	m["tri"] = tri
	// large counts (log buckets) mixed with small
	var big []byte
	for s := 0; s < 200; s++ {
		for i := 0; i < 1+s*s/3+(s%5)*400; i++ {
			big = append(big, byte(s))
		}
	}
	r.Shuffle(len(big), func(i, j int) { big[i], big[j] = big[j], big[i] })
	m["bigcounts"] = big
	// many tied large counts (log-bucket quicksort path with equal keys)
	var ties []byte
	for s := 0; s < 120; s++ {
		for i := 0; i < 170+(s%7)*9+(s%3); i++ {
			ties = append(ties, byte(s*2))
		}
	}
	r.Shuffle(len(ties), func(i, j int) { ties[i], ties[j] = ties[j], ties[i] })
	m["ties"] = ties
	// tiny sizes of skewed text, covers every n % kUnroll and n % 2*kUnroll
	for n := 2; n <= 70; n++ {
		b := make([]byte, n)
		for i := range b {
			b[i] = "aaaaabbbcd"[r.IntN(10)]
		}
		m["tiny"+strconv.Itoa(n)] = b
	}
	for _, n := range []int{255, 256, 257, 1000, 4096, 40960, 41000, maxInput} {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.IntN(r.IntN(60) + 1))
		}
		m["skew"+strconv.Itoa(n)] = b
	}
	return m
}

func TestHUFCref(t *testing.T) {
	inputs := creftest.Inputs(t)
	for k, v := range synthetic() {
		inputs[k] = v
	}
	for name, in := range inputs {
		if len(in) > maxInput {
			in = in[:maxInput]
		}
		var seen [256]bool
		card := 0
		for _, c := range in {
			if !seen[c] {
				seen[c] = true
				card++
			}
		}
		for _, streams := range []int{1, 4} {
			for _, tl := range []uint{6, 11} {
				for _, flags := range []int{0, FlagOptimalDepth} {
					if flags == FlagOptimalDepth && card > 1<<tl {
						// C (v1.5.7) HUF_optimalTableLog with optimalDepth does not
						// raise maxTableLog to hold the alphabet: with more than
						// 2^tableLog symbols HUF_setMaxHeight has no valid tree and
						// the reference crashes (segfault). Not a valid configuration.
						continue
					}
					want := crefHuf(t, in, streams, tl, flags)
					got := goHuf(t, in, streams, tl, flags)
					if !bytes.Equal(got, want) {
						t.Errorf("%s %dX tl=%d flags=%d: Go %d bytes, C %d bytes (mismatch)",
							name, streams, tl, flags, len(got), len(want))
					}
				}
			}
		}
	}
	// tableLog 0 (default), 12 and 5 with the other flag bits: preferRepeat
	// and suspectUncompressible do not change a RepeatNone run's table.
	in := synthetic()["skew40960"] // alphabet < 60 symbols: valid for tableLog 6+
	for _, tl := range []uint{0, 6, 12} {
		for _, flags := range []int{0, FlagOptimalDepth, FlagSuspectUncompressible, FlagPreferRepeat | FlagBMI2} {
			for _, streams := range []int{1, 4} {
				want := crefHuf(t, in, streams, tl, flags)
				got := goHuf(t, in, streams, tl, flags)
				if !bytes.Equal(got, want) {
					t.Errorf("skew40960 %dX tl=%d flags=%d: mismatch (Go %d, C %d)", streams, tl, flags, len(got), len(want))
				}
			}
		}
	}
	// suspectUncompressible with a large random input: the sampling exit.
	rnd := make([]byte, 100000)
	rr := rand.New(rand.NewPCG(9, 9))
	for i := range rnd {
		rnd[i] = byte(rr.Uint32())
	}
	for _, streams := range []int{1, 4} {
		want := crefHuf(t, rnd, streams, 11, FlagSuspectUncompressible)
		got := goHuf(t, rnd, streams, 11, FlagSuspectUncompressible)
		if !bytes.Equal(got, want) {
			t.Errorf("random suspect %dX: mismatch", streams)
		}
	}
}

func TestCompressErrors(t *testing.T) {
	dst := make([]byte, 1<<18)
	wksp := make([]byte, WorkspaceSize)
	table := make(CTable, CTableSize(255))
	rep := RepeatNone
	src := bytes.Repeat([]byte("abcdefgh"), 100)

	if _, err := Compress1XRepeat(dst, make([]byte, maxInput+1), 255, 11, wksp, table, &rep, 0); err != ErrSrcSizeWrong {
		t.Errorf("srcSize > 128K: err=%v want ErrSrcSizeWrong", err)
	}
	if _, err := Compress1XRepeat(dst, src, 255, 13, wksp, table, &rep, 0); err != ErrTableLogTooLarge {
		t.Errorf("tableLog 13: err=%v want ErrTableLogTooLarge", err)
	}
	if _, err := Compress1XRepeat(dst, src, 256, 11, wksp, table, &rep, 0); err != ErrMaxSymbolTooLarge {
		t.Errorf("maxSymbol 256: err=%v want ErrMaxSymbolTooLarge", err)
	}
	if _, err := Compress1XRepeat(dst, src, 255, 11, wksp[:100], table, &rep, 0); err != ErrWorkspaceTooSmall {
		t.Errorf("small wksp: err=%v want ErrWorkspaceTooSmall", err)
	}
	// 0 means "not compressible", not an error.
	if n, err := Compress1XRepeat(dst, nil, 255, 11, wksp, table, &rep, 0); n != 0 || err != nil {
		t.Errorf("empty src: n=%d err=%v want 0,nil", n, err)
	}
	if n, err := Compress1XRepeat(dst[:0], src, 255, 11, wksp, table, &rep, 0); n != 0 || err != nil {
		t.Errorf("empty dst: n=%d err=%v want 0,nil", n, err)
	}
	// src holds symbol 'h' > maxSymbol 100.
	if _, err := Compress1XRepeat(dst, src, 100, 11, wksp, table, &rep, 0); err == nil {
		t.Error("symbol above maxSymbol: want error")
	}
	// single symbol: RLE
	if n, err := Compress4XRepeat(dst, bytes.Repeat([]byte{7}, 500), 255, 11, wksp, table, &rep, 0); n != 1 || err != nil || dst[0] != 7 {
		t.Errorf("rle: n=%d err=%v", n, err)
	}
}

// TestRepeat covers the repeat/preferRepeat/validity policy of
// HUF_compress_internal (C: huf_compress.c:HUF_compress_internal).
func TestRepeat(t *testing.T) {
	wksp := make([]byte, WorkspaceSize)
	dst := make([]byte, 1<<16)
	r := rand.New(rand.NewPCG(5, 6))
	gen := func(n, alpha int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.IntN(r.IntN(alpha) + 1))
		}
		return b
	}
	a := gen(20000, 40)
	b := gen(300, 30) // alphabet within a's, small block

	for _, streams := range []int{1, 4} {
		comp := func(src []byte, table CTable, rep *Repeat, flags int) (int, error) {
			if streams == 1 {
				return Compress1XRepeat(dst, src, 255, 11, wksp, table, rep, flags)
			}
			return Compress4XRepeat(dst, src, 255, 11, wksp, table, rep, flags)
		}
		using := func(src []byte, table CTable) []byte {
			out := make([]byte, len(dst))
			var n int
			if streams == 1 {
				n = Compress1XUsingCTable(out, src, table, 0)
			} else {
				n = Compress4XUsingCTable(out, src, table, 0)
			}
			return out[:n]
		}

		// Block A with a fresh table: saves its table, sets repeat none.
		table := make(CTable, CTableSize(255))
		rep := RepeatNone
		n, err := comp(a, table, &rep, 0)
		if err != nil || n <= 1 {
			t.Fatalf("A: n=%d err=%v", n, err)
		}
		if rep != RepeatNone {
			t.Fatalf("A: repeat=%d want none", rep)
		}
		saved := append(CTable(nil), table...)

		// preferRepeat + valid: old table, no header, table/repeat untouched.
		rep = RepeatValid
		n, err = comp(b, table, &rep, FlagPreferRepeat)
		if err != nil {
			t.Fatal(err)
		}
		want := using(b, saved)
		if n == 0 || !bytes.Equal(dst[:n], want) {
			t.Errorf("%dX preferRepeat valid: got %d bytes, want %d (UsingCTable)", streams, n, len(want))
		}
		if rep != RepeatValid || !equalCT(table, saved) {
			t.Errorf("%dX preferRepeat valid: table/repeat modified", streams)
		}

		// preferRepeat + check with a block holding a symbol the old table
		// cannot code: validation fails -> repeat none, new table written.
		bad := append(gen(300, 30), 200)
		rep = RepeatCheck
		n, err = comp(bad, table, &rep, FlagPreferRepeat)
		if err != nil {
			t.Fatal(err)
		}
		if rep != RepeatNone {
			t.Errorf("%dX check invalid: repeat=%d want none", streams, rep)
		}
		if n > 0 && equalCT(table, saved) {
			t.Errorf("%dX check invalid: table not replaced", streams)
		}

		// preferRepeat + check with a block the table can code: stays check, reused.
		copy(table, saved)
		rep = RepeatCheck
		n, err = comp(b, table, &rep, FlagPreferRepeat)
		if err != nil || n == 0 || !bytes.Equal(dst[:n], want) || rep != RepeatCheck {
			t.Errorf("%dX check valid: n=%d err=%v rep=%d", streams, n, err, rep)
		}

		// Without preferRepeat, valid repeat: whichever of old/new is cheaper;
		// a block identical in distribution to A picks the old table.
		copy(table, saved)
		rep = RepeatValid
		a2 := gen(20000, 40)
		n, err = comp(a2, table, &rep, 0)
		if err != nil || n <= 1 {
			t.Fatalf("A2: n=%d err=%v", n, err)
		}
		if rep == RepeatValid && !bytes.Equal(dst[:n], using(a2, saved)) {
			t.Errorf("%dX reuse: output is not old-table encoding", streams)
		}
		if rep == RepeatNone && equalCT(table, saved) {
			t.Errorf("%dX new table chosen but hufTable not updated", streams)
		}
	}
}

func equalCT(a, b CTable) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWriteCTableRoundHeader(t *testing.T) {
	var count [256]uint32
	src := bytes.Repeat([]byte("aaaaaaaabbbbccd"), 40)
	for _, c := range src {
		count[c]++
	}
	ct := make(CTable, CTableSize(255))
	wksp := make([]byte, WorkspaceSize)
	maxBits, err := BuildCTable(ct, &count, 'd', 11, wksp)
	if err != nil {
		t.Fatal(err)
	}
	hdr := ReadCTableHeader(ct)
	if hdr.MaxSymbolValue != 'd' || uint(hdr.TableLog) != maxBits {
		t.Fatalf("header %+v maxBits=%d", hdr, maxBits)
	}
	out := make([]byte, 200)
	n, err := WriteCTable(out, ct, 'd', maxBits, wksp)
	if err != nil || n <= 1 {
		t.Fatalf("WriteCTable n=%d err=%v", n, err)
	}
	if _, err := WriteCTable(out[:0], ct, 'd', maxBits, wksp); err != ErrDstSizeTooSmall {
		t.Errorf("empty dst: err=%v", err)
	}
	if _, err := BuildCTable(ct, &count, 256, 11, wksp); err != ErrMaxSymbolTooLarge {
		t.Errorf("maxSymbol 256: err=%v", err)
	}
	if _, err := BuildCTable(ct, &count, 'd', 11, wksp[:10]); err != ErrWorkspaceTooSmall {
		t.Errorf("small wksp: err=%v", err)
	}
}

// TestBuildCTableAllZeroPanics: an all-zero histogram must panic, not loop
// forever on buildTree's masked indices.
func TestBuildCTableAllZeroPanics(t *testing.T) {
	var count [256]uint32
	ct := make(CTable, CTableSize(255))
	wksp := make([]byte, CTableWorkspaceSize)
	defer func() {
		if recover() == nil {
			t.Fatal("BuildCTable returned for an all-zero histogram")
		}
	}()
	BuildCTable(ct, &count, 255, 11, wksp)
}
