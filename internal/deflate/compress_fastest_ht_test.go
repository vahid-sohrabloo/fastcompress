package deflate

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
)

// The matchfinder code written out in deflateCompressFastest against the
// reference htMatchfinder methods (longestMatch / skipBytes): both run over
// the same input, and the hash table, the window base (inCurBase) and
// next_hash must be identical. The compressor's state is observable only at
// the end of a call (fastestState.endInCurBase / endNextHash), so it is
// compared after the last step of every prefix of the input: each prefix
// ends the parse at a different step, which checks every step with the
// shorter maxLen / niceLen of the input's end, and the full input checks
// the whole parse. Go only (no C).

// htReferenceParse runs the matchfinder steps of deflate_compress_fastest
// over in with the htMatchfinder methods: the CompressChunk history priming
// (histLen bytes), then the longest match at each position, skipping the
// rest of a chosen match. Block ends do not touch the matchfinder, so the
// block loop is left out. It returns the final window base and next_hash.
func htReferenceParse(mf *htMatchfinder, in []byte, histLen int, nice uint32) (base int, nextHash uint32) {
	inEnd := len(in)
	maxLen := uint32(deflateMaxMatchLen)
	niceLen := min(nice, maxLen)
	mf.init()
	if histLen > 0 && inEnd-histLen >= htMatchfinderRequiredNbytes {
		nextHash = lzHash(loadLE32(in, 0), htMatchfinderHashOrder)
		mf.skipBytes(in, &base, 0, inEnd, uint32(histLen), &nextHash)
	}
	for p := histLen; p < inEnd; {
		if remaining := inEnd - p; remaining < deflateMaxMatchLen {
			maxLen = uint32(remaining)
			if maxLen < htMatchfinderRequiredNbytes {
				break // the rest are literals
			}
			niceLen = min(niceLen, maxLen)
		}
		length, _ := mf.longestMatch(in, &base, p, maxLen, niceLen, &nextHash)
		if length == 0 {
			p++
			continue
		}
		mf.skipBytes(in, &base, p+1, inEnd, length-1, &nextHash)
		p += int(length)
	}
	return base, nextHash
}

// checkFastestHt compresses history+src at level 1 (niceMatchLength nice)
// and compares the compressor's matchfinder state with htReferenceParse.
func checkFastestHt(t *testing.T, c *Compressor, ref *htMatchfinder, history, src []byte, nice uint32,
	dst []byte) {
	t.Helper()
	c.niceMatchLength = nice
	var n int
	if len(history) == 0 {
		n = c.Compress(dst, src)
	} else {
		n = c.CompressChunk(dst, history, src, true)
	}
	if n == 0 {
		t.Fatalf("hist %d, src %d: compress failed", len(history), len(src))
	}
	in := append(slices.Clone(history), src...)
	base, nextHash := htReferenceParse(ref, in, len(history), nice)
	pf := c.pf
	if pf.endInCurBase != base || pf.endNextHash != nextHash || pf.htMf.hashTab != ref.hashTab {
		i := 0
		for i < len(ref.hashTab) && pf.htMf.hashTab[i] == ref.hashTab[i] {
			i++
		}
		t.Fatalf("hist %d, src %d, nice %d: inCurBase %d want %d, nextHash %d want %d, first table "+
			"difference at %d (of %d)", len(history), len(src), nice, pf.endInCurBase, base, pf.endNextHash,
			nextHash, i, len(ref.hashTab))
	}
}

// fastestHtInputs returns small inputs (every prefix is checked) and large
// ones (prefixes around the window slides and a stride of them).
func fastestHtInputs() (small, large [][]byte) {
	// Distinct 4-byte groups of one hash: both bucket entries, the second
	// entry checked after a miss on the first, and the bestLen-3 test.
	var coll []byte
	words := collidingWords(3)
	for i := range 120 {
		coll = append(coll, words[i%3]...)
		coll = append(coll, words[(i*7)%3][:1+i%4]...)
	}
	small = [][]byte{
		coll,
		bytes.Repeat([]byte("abcdabceabcdabcf"), 40),
		bytes.Repeat([]byte{'z'}, 700), // matches longer than niceLen and maxLen
		randData(11, 400),
		structuredInput(3, 900),
	}
	large = [][]byte{
		structuredInput(5, 140000),
		append(randData(12, 40000), bytes.Repeat([]byte("0123456789"), 10000)...), // a slide inside a skip
		bytes.Repeat([]byte{'q'}, 100000),
		structuredInput(6, 70000),
	}
	return small, large
}

// largePrefixes returns the prefix lengths checked for an input of n bytes
// (after hist history bytes): every length near the window slides, a
// stride and n.
func largePrefixes(n, hist int, short bool) []int {
	var ks []int
	near := 100
	stride := 997
	if short {
		near, stride = 40, 7919
	}
	for _, edge := range []int{matchfinderWindowSize, 2 * matchfinderWindowSize, 3 * matchfinderWindowSize} {
		for k := edge - hist - near; k <= edge-hist+near; k++ {
			if k > 0 && k < n {
				ks = append(ks, k)
			}
		}
	}
	for k := stride; k < n; k += stride {
		ks = append(ks, k)
	}
	return append(ks, n)
}

func TestCompressFastestHtEquiv(t *testing.T) {
	c := mustCompressor(t, 1)
	ref := new(htMatchfinder)
	small, large := fastestHtInputs()
	dst := make([]byte, c.CompressBound(300000))
	nices := []uint32{32, 5, 258}
	for i, in := range small {
		for _, nice := range nices {
			t.Run(fmt.Sprintf("small%d/nice%d", i, nice), func(t *testing.T) {
				for k := c.maxPassthroughSize + 1; k <= len(in); k++ {
					checkFastestHt(t, c, ref, nil, in[:k], nice, dst)
				}
			})
		}
	}
	for i, in := range large {
		for _, hist := range []int{0, 1, 4096, 32768} {
			nice := nices[(i+hist)%len(nices)]
			t.Run(fmt.Sprintf("large%d/hist%d/nice%d", i, hist, nice), func(t *testing.T) {
				history, src := in[:hist], in[hist:]
				for _, k := range largePrefixes(len(src), hist, testing.Short()) {
					if k > c.maxPassthroughSize {
						checkFastestHt(t, c, ref, history, src[:k], nice, dst)
					}
				}
			})
		}
	}
}
