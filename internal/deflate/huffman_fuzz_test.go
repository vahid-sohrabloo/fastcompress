package deflate

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// fuzzFreqs maps fuzz bytes to one dump-codes record. data[0]: kind =
// value mod 4 (hkLitlen, hkOffset, hkPrecode, hkPair); bits 2-3 pick the
// frequency encoding of the following bytes: 0 = one byte per symbol,
// 1 = 4 bytes LE per symbol (full u32 range, wrapping sums), 2 = 1 <<
// (byte mod 32) or 0 when the byte's top bit is set, 3 = Fibonacci term
// (byte mod 48, 0 for byte >= 240). data[1:3] (LE) mod the kind's maximum
// - 1, plus 2, is the alphabet size (hkPair: always 320). Missing bytes are
// 0; for n == 2 with both frequencies 0 the second becomes 1 (C's
// precondition, see sortSymbols).
func fuzzFreqs(data []byte) hRec {
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
	kind := int(mode % 4)
	enc := mode >> 2 & 3
	sz := int(next()) | int(next())<<8
	n := deflateNumLitlenSyms + deflateNumOffsetSyms
	if kind != hkPair {
		n = 2 + sz%(hkMaxN[kind]-1)
	}
	fib := fibFreqs(48)
	f := make([]uint32, n)
	for i := range f {
		switch b := next(); enc {
		case 0:
			f[i] = uint32(b)
		case 1:
			f[i] = uint32(b) | uint32(next())<<8 | uint32(next())<<16 | uint32(next())<<24
		case 2:
			if b < 0x80 {
				f[i] = 1 << (b % 32)
			}
		default:
			if b < 240 {
				f[i] = fib[b%48]
			}
		}
	}
	if n == 2 && f[0] == 0 && f[1] == 0 {
		f[1] = 1
	}
	return hRec{kind: kind, freqs: f}
}

func fuzzEncodeFreqs(r hRec) []byte {
	b := []byte{byte(r.kind) | 1<<2}
	n := len(r.freqs)
	if r.kind != hkPair {
		n -= 2
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(n))
	for _, f := range r.freqs {
		b = binary.LittleEndian.AppendUint32(b, f)
	}
	return b
}

// FuzzMakeHuffmanCode compares deflateMakeHuffmanCode (and, for the pair
// kind, deflateMakeHuffmanCodes + computeFullLenCodewords, then
// precomputeHuffmanHeader on the resulting lens) with ldref dump-codes /
// dump-header, and checks the independent code properties. It skips
// without ldref locally and fails under CI (creftest.LDBin).
func FuzzMakeHuffmanCode(f *testing.F) {
	f.Add(fuzzEncodeFreqs(hRec{hkOffset, []uint32{1, 1, 1}}))
	f.Add(fuzzEncodeFreqs(hRec{hkOffset, make([]uint32, 32)}))
	f.Add(fuzzEncodeFreqs(hRec{hkPrecode, fibFreqs(19)}))
	f.Add(fuzzEncodeFreqs(hRec{hkLitlen, append(fibFreqs(25), ones(263)...)}))
	f.Add(fuzzEncodeFreqs(hRec{hkOffset, fillU32(32, 0x80000000)}))
	r := rand.New(rand.NewPCG(21, 200))
	for i := range 24 {
		kind := i % 3
		f.Add(fuzzEncodeFreqs(hRec{kind, genFreqs(r, genN(r, kind))}))
	}
	for range 4 {
		f.Add(fuzzEncodeFreqs(genPair(r)))
	}
	f.Add([]byte{3 | 3<<2})
	f.Fuzz(func(t *testing.T, data []byte) {
		creftest.LDBin(t)
		rec := fuzzFreqs(data)
		g := goCodes(rec)
		if err := compareCodes(rec, g, ldCodes(t, []hRec{rec})[0]); err != nil {
			t.Fatalf("kind %d freqs %v: %v", rec.kind, rec.freqs, err)
		}
		if rec.kind != hkPair {
			if err := checkCodeProps(rec.freqs, g.lens, g.cw, hkMaxLen[rec.kind]); err != nil {
				t.Fatalf("kind %d freqs %v: %v", rec.kind, rec.freqs, err)
			}
			return
		}
		var in [deflateNumLitlenSyms + deflateNumOffsetSyms]uint8
		copy(in[:], g.lens)
		diffHeader(t, [][deflateNumLitlenSyms + deflateNumOffsetSyms]uint8{in})
	})
}
