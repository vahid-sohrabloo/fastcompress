package deflate

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// fuzzLens maps fuzz bytes to one table-build input. data[0] selects:
//
//	bit 0: 0 = precode (19 symbols, max length 7), 1 = litlen+offset with
//	    num_litlen_syms = LE16(data[1:3]) mod 289 and num_offset_syms =
//	    data[3] mod 33 (max length 15).
//	bit 1: 0 = raw lengths: the following bytes are the lengths, each mod 8
//	    (precode) or mod 16 (litlen, then offset);
//	    1 = complete code: the following bytes drive fuzzComplete, which
//	    always builds a complete code (then perturbs it with probability
//	    3/8), so most inputs of this mode give accepted tables, with
//	    subtables for long codewords.
//
// The lengths start at data[1] (precode) or data[4] (litlen). Missing bytes
// read as 0.
func fuzzLens(data []byte) (precode bool, pre [deflateNumPrecodeSyms]uint8, rec litlenRec) {
	pos := 0
	next := func() byte {
		b := byte(0)
		if pos < len(data) {
			b = data[pos]
		}
		pos++
		return b
	}
	mode := next()
	gen := func(l []uint8, maxLen int) {
		if mode&2 != 0 {
			fuzzComplete(next, l, maxLen)
			return
		}
		for i := range l {
			l[i] = next() % uint8(maxLen+1)
		}
	}
	if mode&1 == 0 {
		gen(pre[:], deflateMaxPreCodewordLen)
		return true, pre, rec
	}
	nl := (int(next()) | int(next())<<8) % 289
	no := int(next()) % 33
	rec.lit = make([]uint8, nl)
	rec.off = make([]uint8, no)
	gen(rec.lit, deflateMaxLitlenCodewordLen)
	gen(rec.off, deflateMaxOffsetCodewordLen)
	return false, pre, rec
}

// fuzzComplete fills l (all zero on entry) with a complete code whose shape
// comes from next(): a leaf count k in [2, len(l)], k-1 leaf splits (each
// byte picks the leaf to split; leaves already at maxLen are skipped, so the
// Kraft sum stays exactly 1), a symbol per leaf (byte mod len(l), probing to
// the next free symbol), then one perturbation byte: mod 8 = 5 drops a
// codeword (incomplete), 6 lengthens one (incomplete), 7 shortens one
// (over-subscribed), otherwise (including the 0 of a missing byte) the code
// is left complete. len(l) == 1 gives
// the single 1-bit codeword, len(l) == 0 nothing.
func fuzzComplete(next func() byte, l []uint8, maxLen int) {
	n := len(l)
	if n < 2 {
		if n == 1 {
			l[0] = 1
		}
		return
	}
	k := 2 + (int(next())|int(next())<<8)%(n-1)
	depth := make([]uint8, 1, k)
	for len(depth) < k {
		i := int(next()) % len(depth)
		j := 0
		for ; j < len(depth) && int(depth[(i+j)%len(depth)]) == maxLen; j++ {
		}
		if j == len(depth) {
			break // every leaf is at maxLen: the tree is full
		}
		i = (i + j) % len(depth)
		depth[i]++
		depth = append(depth, depth[i])
	}
	syms := make([]int, len(depth))
	for i, d := range depth {
		s := int(next()) % n
		for l[s] != 0 {
			s = (s + 1) % n
		}
		l[s] = d
		syms[i] = s
	}
	p := next()
	s := syms[int(p>>3)%len(syms)]
	switch p % 8 {
	case 5:
		l[s] = 0
	case 6:
		if int(l[s]) < maxLen {
			l[s]++
		}
	case 7:
		if l[s] > 1 {
			l[s]--
		}
	}
}

// fuzzEncode is the inverse of fuzzLens, for seeds.
func fuzzEncode(precode bool, pre [deflateNumPrecodeSyms]uint8, rec litlenRec) []byte {
	if precode {
		return append([]byte{0}, pre[:]...)
	}
	b := []byte{1, byte(len(rec.lit)), byte(len(rec.lit) >> 8), byte(len(rec.off))}
	b = append(b, rec.lit...)
	return append(b, rec.off...)
}

// FuzzBuildDecodeTables builds the decode tables for arbitrary length
// vectors and compares the ok flag, litlen tablebits and every table entry
// with libdeflate (ref/ldref dump-tables), then checks accepted tables with
// the canonical-code oracle. Skipped without ldref, fails under CI.
func FuzzBuildDecodeTables(f *testing.F) {
	for _, c := range unitCases() {
		f.Add(fuzzEncode(c.precode, c.pre, c.rec))
	}
	f.Add(fuzzEncode(false, [deflateNumPrecodeSyms]uint8{}, staticLens()))
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 16; i++ {
		// Complete-code mode seeds (precode and litlen+offset).
		b := make([]byte, 1+r.IntN(700))
		for j := range b {
			b[j] = byte(r.Uint32())
		}
		b[0] = 2 | byte(i&1)
		f.Add(b)
		var pre [deflateNumPrecodeSyms]uint8
		copy(pre[:], genLens(r, deflateNumPrecodeSyms, deflateMaxPreCodewordLen))
		f.Add(fuzzEncode(true, pre, litlenRec{}))
		f.Add(fuzzEncode(false, pre, genLitlenRec(r)))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		creftest.LDBin(t)
		precode, pre, rec := fuzzLens(data)
		if precode {
			name := fmt.Sprintf("precode %v", pre)
			g := goPrecode(&pre)
			comparePrecode(t, name, g, ldPrecode(t, [][deflateNumPrecodeSyms]uint8{pre})[0])
			checkPrecode(t, name, pre[:], g)
			return
		}
		name := fmt.Sprintf("litlen %v offset %v", rec.lit, rec.off)
		g := goLitlen(rec)
		compareLitlen(t, name, g, ldLitlen(t, []litlenRec{rec})[0])
		checkLitlen(t, name, rec, g)
	})
}
