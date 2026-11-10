package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// Assembly equivalence of fastestLoopAsm (compress_fastest_amd64.s) with the
// Go loop of deflateCompressFastest: a level 1 CompressChunk of history+src,
// assembly off (heap state and input) and on, the latter with the compressor's
// fastestState flush against a guard page (after its end, then before its
// start) and the input flush against one (after the end of src, then before
// the start of history), so that an access past the state or the input
// faults. The output, the matchfinder table, the sequences, the frequencies
// and the end state (endInCurBase, endNextHash) must be identical. In builds
// without the assembly both runs are the Go loop.

// fastestAsmEnv holds the guarded memory of the assembly runs, mapped once.
type fastestAsmEnv struct {
	pfAfter, pfBefore    *fastestState
	inAfter, inBefore    []byte
	maxIn                int
	refC, asmC           *Compressor
	refDst, asmDst       []byte
	refHeapPf, asmHeapPf *fastestState
}

func newFastestAsmEnv(t testing.TB, maxIn int) *fastestAsmEnv {
	size := int(unsafe.Sizeof(fastestState{}))
	a := guardmem.After(t, size)
	b := guardmem.Before(t, size)
	e := &fastestAsmEnv{
		pfAfter:  (*fastestState)(unsafe.Pointer(&a[0])),
		pfBefore: (*fastestState)(unsafe.Pointer(&b[0])),
		inAfter:  guardmem.After(t, maxIn),
		inBefore: guardmem.Before(t, maxIn),
		maxIn:    maxIn,
		refC:     mustCompressor(t, 1),
		asmC:     mustCompressor(t, 1),
	}
	e.refHeapPf, e.asmHeapPf = e.refC.pf, e.asmC.pf
	e.refDst = make([]byte, e.refC.CompressChunkBound(maxIn))
	e.asmDst = make([]byte, len(e.refDst))
	return e
}

// checkFastestAsm compresses history+data (history <= 32 KiB) with the
// assembly off and on, fails on any difference and returns the output.
func checkFastestAsm(t testing.TB, e *fastestAsmEnv, history, data []byte, final bool) []byte {
	t.Helper()
	n := len(history) + len(data)
	if n > e.maxIn {
		t.Fatalf("input of %d bytes > %d", n, e.maxIn)
	}
	h := len(history)
	ref := append(bytes.Clone(history), data...)
	var want int
	withDeflateAsm(false, func() { want = e.refC.CompressChunk(e.refDst, ref[:h], ref[h:], final) })
	for i, l := range []struct {
		pf *fastestState
		in []byte
	}{
		{e.pfAfter, e.inAfter[e.maxIn-n:]},
		{e.pfBefore, e.inBefore[:n:n]},
	} {
		copy(l.in, ref)
		// The Go run's state as the stale state of a reused compressor (the
		// entries this run does not write then compare equal).
		*l.pf = *e.refC.pf
		e.asmC.pf = l.pf
		var got int
		withDeflateAsm(true, func() { got = e.asmC.CompressChunk(e.asmDst, l.in[:h], l.in[h:], final) })
		e.asmC.pf = e.asmHeapPf
		switch {
		case got != want || !bytes.Equal(e.asmDst[:got], e.refDst[:want]):
			t.Fatalf("layout %d, history %d, %d bytes, final %v: output differs (%d vs %d bytes)", i, h, len(data),
				final, got, want)
		case l.pf.htMf != e.refC.pf.htMf:
			t.Fatalf("layout %d, history %d, %d bytes: matchfinder tables differ", i, h, len(data))
		case l.pf.sequences != e.refC.pf.sequences:
			t.Fatalf("layout %d, history %d, %d bytes: sequences differ", i, h, len(data))
		case l.pf.endInCurBase != e.refC.pf.endInCurBase || l.pf.endNextHash != e.refC.pf.endNextHash:
			t.Fatalf("layout %d, history %d, %d bytes: end state (%d, %d), Go (%d, %d)", i, h, len(data),
				l.pf.endInCurBase, l.pf.endNextHash, e.refC.pf.endInCurBase, e.refC.pf.endNextHash)
		case e.asmC.freqs != e.refC.freqs:
			t.Fatalf("layout %d, history %d, %d bytes: frequencies differ", i, h, len(data))
		}
	}
	return e.refDst[:want]
}

// fastestAsmData is a test input: the matchfinder inputs of hcData, long
// runs (matches of 258 and the nice-length exits), or text with a few
// literals between copies at every distance up to the window.
func fastestAsmData(r *rand.Rand, n int) []byte {
	switch r.IntN(4) {
	case 0:
		b := make([]byte, n)
		c := byte(r.Uint32())
		for i := range b {
			if r.IntN(1000) == 0 {
				c = byte(r.Uint32())
			}
			b[i] = c
		}
		return b
	case 1:
		b := make([]byte, 0, n)
		for len(b) < n {
			if len(b) > 8 && r.IntN(3) != 0 {
				d := 1 + r.IntN(min(len(b), matchfinderWindowSize+100))
				l := 3 + r.IntN(300)
				for k := 0; k < l && len(b) < n; k++ {
					b = append(b, b[len(b)-d])
				}
			} else {
				b = append(b, byte('a'+r.IntN(26)))
			}
		}
		return b
	default:
		return hcData(r, n)
	}
}

// TestFastestLoopAsmEquiv: every length 0..600 (block and buffer ends near
// the 258-byte limit of the assembly loop), random inputs up to 300 KiB
// (window slides inside searches and skips, several blocks; without -short at
// least one of them ends at the 8192-sequence limit) with history 0..32 KiB, final
// and not final, periodic inputs and deflatetest's dense4 (blocks ended by
// FAST_SEQ_STORE_LENGTH, in every run).
func TestFastestLoopAsmEquiv(t *testing.T) {
	const maxIn = 300<<10 + matchfinderWindowSize
	e := newFastestAsmEnv(t, maxIn)
	r := rand.New(rand.NewPCG(91, 2))
	for n := 0; n <= 600; n++ {
		checkFastestAsm(t, e, nil, fastestAsmData(r, n), true)
	}
	rounds := 120
	if testing.Short() {
		rounds = 30
	}
	for range rounds {
		var n int
		switch k := r.IntN(10); {
		case k < 6:
			n = r.IntN(5000)
		case k < 9:
			n = 5000 + r.IntN(100000)
		default:
			n = 100000 + r.IntN(200000)
		}
		data := fastestAsmData(r, n)
		var hist []byte
		if r.IntN(3) == 0 {
			hist = fastestAsmData(r, r.IntN(matchfinderWindowSize+1))
		}
		checkFastestAsm(t, e, hist, data, r.IntN(4) != 0)
	}
	// 16-bit offsets: a 100 KiB byte run (blocks of
	// FAST_SOFT_MAX_BLOCK_LENGTH bytes, about 254 matches of 258 bytes each)
	// and every 1..8 byte period.
	for p := 1; p <= 8; p++ {
		b := make([]byte, 100<<10)
		for i := range b {
			b[i] = byte(i % p)
		}
		checkFastestAsm(t, e, nil, b, true)
	}
	// dense4 (320000 bytes, a match every 5 bytes): its blocks end at
	// FAST_SEQ_STORE_LENGTH matches, so the assembly writes the last entry of
	// sequences, sequences[8192], 16 bytes before the end of the guarded state
	// (only endInCurBase and endNextHash follow it).
	var dense4 []byte
	for _, cs := range deflatetest.Cases(true) {
		if cs.Name == "dense4" {
			dense4 = cs.Data
		}
	}
	bs, err := walkBlocks(checkFastestAsm(t, e, nil, dense4, true))
	full := 0
	for _, b := range bs {
		if b.nmatch == fastSeqStoreLength {
			full++
		}
	}
	if dense4 == nil || err != nil || full == 0 {
		t.Fatalf("dense4 (%d bytes): no block ended by %d matches (%v): %v", len(dense4), fastSeqStoreLength, err, bs)
	}
}

// FuzzDeflateFastestLoopAsmEquiv: the fuzz input after its first 2 bytes
// (little-endian: the history length, at most the input) as history+src.
func FuzzDeflateFastestLoopAsmEquiv(f *testing.F) {
	f.Add([]byte{})
	f.Add(append([]byte{0, 0}, bytes.Repeat([]byte("abcdefgh"), 100)...))
	f.Add(append([]byte{0x10, 0}, bytes.Repeat([]byte("WXYZhello, world WXYZhello, there "), 40)...))
	r := rand.New(rand.NewPCG(5, 6))
	f.Add(append([]byte{0, 1}, fastestAsmData(r, 3000)...))
	const maxIn = 1 << 17
	e := newFastestAsmEnv(f, maxIn)
	f.Fuzz(func(t *testing.T, data []byte) {
		var h int
		if len(data) >= 2 {
			h = int(binary.LittleEndian.Uint16(data))
			data = data[2:]
		}
		data = data[:min(len(data), maxIn)]
		h = min(h, len(data))
		checkFastestAsm(t, e, data[:h], data[h:], h%2 == 0)
	})
}
