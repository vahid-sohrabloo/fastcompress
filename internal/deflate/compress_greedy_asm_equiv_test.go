package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// Assembly equivalence of greedyLoopAsm (levels 2..4) and lazyLoopAsm
// (levels 5..9) with the Go code, as for level 1 (compress_fastest_asm_equiv_test.go): a CompressChunk of
// history+src, assembly off (heap state and input) and on, the latter with the
// compressor's greedyState (matchfinder and sequences) flush against a guard
// page (after its end, then before its start) and the input flush against
// one (after the end of src, then before the start of history). The output,
// the matchfinder tables, the sequences, the frequencies and the block split
// statistics must be identical. In builds without the assembly both runs are
// the Go code.

// greedyAsmEnv holds the guarded memory and the compressors of one level.
type greedyAsmEnv struct {
	pgAfter, pgBefore *greedyState
	inAfter, inBefore []byte
	maxIn             int
	refC, asmC        []*Compressor // index level-2
	refDst, asmDst    []byte
}

func newGreedyAsmEnv(t testing.TB, maxIn int) *greedyAsmEnv {
	size := int(unsafe.Sizeof(greedyState{}))
	a := guardmem.After(t, size)
	b := guardmem.Before(t, size)
	e := &greedyAsmEnv{
		pgAfter:  (*greedyState)(unsafe.Pointer(&a[0])),
		pgBefore: (*greedyState)(unsafe.Pointer(&b[0])),
		inAfter:  guardmem.After(t, maxIn),
		inBefore: guardmem.Before(t, maxIn),
		maxIn:    maxIn,
	}
	for level := 2; level <= 9; level++ {
		e.refC = append(e.refC, mustCompressor(t, level))
		e.asmC = append(e.asmC, mustCompressor(t, level))
	}
	e.refDst = make([]byte, e.refC[0].CompressChunkBound(maxIn))
	e.asmDst = make([]byte, len(e.refDst))
	return e
}

// checkGreedyAsm compresses history+data (history <= 32 KiB) at level 2..9
// with the assembly off and on, fails on any difference and returns the
// output.
func checkGreedyAsm(t testing.TB, e *greedyAsmEnv, level int, history, data []byte, final bool) []byte {
	t.Helper()
	n := len(history) + len(data)
	if n > e.maxIn {
		t.Fatalf("input of %d bytes > %d", n, e.maxIn)
	}
	h := len(history)
	refC, asmC := e.refC[level-2], e.asmC[level-2]
	ref := append(bytes.Clone(history), data...)
	var want int
	withDeflateAsm(false, func() { want = refC.CompressChunk(e.refDst, ref[:h], ref[h:], final) })
	heapPg := asmC.pg
	for i, l := range []struct {
		pg *greedyState
		in []byte
	}{
		{e.pgAfter, e.inAfter[e.maxIn-n:]},
		{e.pgBefore, e.inBefore[:n:n]},
	} {
		copy(l.in, ref)
		// The Go run's state as the stale state of a reused compressor (the
		// entries this run does not write then compare equal).
		*l.pg = *refC.pg
		asmC.pg = l.pg
		var got int
		withDeflateAsm(true, func() { got = asmC.CompressChunk(e.asmDst, l.in[:h], l.in[h:], final) })
		asmC.pg = heapPg
		switch {
		case got != want || !bytes.Equal(e.asmDst[:got], e.refDst[:want]):
			t.Fatalf("level %d, layout %d, history %d, %d bytes, final %v: output differs (%d vs %d bytes)", level,
				i, h, len(data), final, got, want)
		case l.pg.hcMf != refC.pg.hcMf:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: matchfinder tables differ", level, i, h, len(data))
		case l.pg.sequences != refC.pg.sequences:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: sequences differ", level, i, h, len(data))
		case asmC.freqs != refC.freqs || asmC.splitStats != refC.splitStats:
			t.Fatalf("level %d, layout %d, history %d, %d bytes: frequencies or split statistics differ", level, i,
				h, len(data))
		}
	}
	return e.refDst[:want]
}

// TestGreedyLoopAsmEquiv: levels 2..9; every length 0..600 at levels 2, 6, 8
// and 9 (buffer ends near the 258-byte limit of the assembly loop), random
// inputs up to 300 KiB (window slides inside searches and skips, block splits
// by the split statistics) with history 0..32 KiB, final and not final,
// periodic inputs (long matches, the nice-length exits, 16-bit offsets),
// dense 3-byte matches (blocks ended by SEQ_STORE_LENGTH) and 400 KiB of
// stationary data (a block ended by SOFT_MAX_BLOCK_LENGTH).
func TestGreedyLoopAsmEquiv(t *testing.T) {
	const maxIn = 400<<10 + matchfinderWindowSize
	e := newGreedyAsmEnv(t, maxIn)
	r := rand.New(rand.NewPCG(91, 3))
	for n := 0; n <= 600; n++ {
		for _, level := range []int{2, 6, 8, 9} {
			checkGreedyAsm(t, e, level, nil, fastestAsmData(r, n), true)
		}
	}
	rounds := 160
	if testing.Short() {
		rounds = 40
	}
	for k := range rounds {
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
		checkGreedyAsm(t, e, 2+k%8, hist, data, r.IntN(4) != 0)
	}
	// Length 3 matches at offsets 4095, 4096 and 4097 in random bytes (the
	// greedy choice takes them up to 4096).
	for _, off := range []int{4095, 4096, 4097} {
		b := make([]byte, 40000)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		for p := off + 10; p+4 < len(b); p += 97 {
			copy(b[p:p+3], b[p-off:])
			b[p+3] = b[p-off+3] + 1
		}
		for level := 2; level <= 4; level++ {
			checkGreedyAsm(t, e, level, nil, b, true)
		}
	}
	// The same at 8191, 8192 and 8193 (the lazy compressors take them up
	// to 8192).
	for _, off := range []int{8191, 8192, 8193} {
		b := make([]byte, 60000)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		for p := off + 10; p+4 < len(b); p += 97 {
			copy(b[p:p+3], b[p-off:])
			b[p+3] = b[p-off+3] + 1
		}
		for level := 5; level <= 9; level++ {
			checkGreedyAsm(t, e, level, nil, b, true)
		}
	}
	// Every byte value once in the first 4 KiB of 4-letter data: min_len
	// starts at 3 (calculate_min_match_len sees 256 literals) and the
	// recalculation after 10000 bytes raises it (the single ones fall under
	// its cutoff), at varying positions relative to the matches.
	for k := range 40 {
		b := make([]byte, 30000+k*37)
		for i := range b {
			b[i] = "ACGT"[r.IntN(4)]
		}
		for i := range 256 {
			b[16*i+k%16] = byte(i)
		}
		checkGreedyAsm(t, e, 5+k%5, nil, b, true)
	}
	for p := 1; p <= 8; p++ {
		b := make([]byte, 100<<10)
		for i := range b {
			b[i] = byte(i % p)
		}
		checkGreedyAsm(t, e, 2+p%3, nil, b, true)
		checkGreedyAsm(t, e, 5+p%5, nil, b, true)
	}
	// Dense 3-byte matches (a random literal and a copy from at most 4095
	// back, as in TestCompressGreedyBlockEnds; 8191 at the lazy levels, as in
	// TestCompressLazyBlockEnds): blocks end at SEQ_STORE_LENGTH matches, so
	// the assembly writes the last entry of sequences, sequences[50000], the
	// last 8 bytes before the guard page after the state.
	for level := 2; level <= 9; level++ {
		maxD := 4096
		if level >= 5 {
			maxD = 8192
		}
		dense := make([]byte, 0, 300000)
		for range maxD {
			dense = append(dense, byte(r.Uint32()))
		}
		for len(dense) < 300000 {
			dense = append(dense, byte(r.Uint32()))
			d := 4 + r.IntN(maxD-4)
			for range 3 {
				dense = append(dense, dense[len(dense)-d])
			}
		}
		bs, err := walkBlocks(checkGreedyAsm(t, e, level, nil, dense, true))
		full := 0
		for _, b := range bs {
			if b.nmatch == seqStoreLength {
				full++
			}
		}
		if err != nil || full == 0 {
			t.Fatalf("level %d dense: no block ended by %d matches (%v): %v", level, seqStoreLength, err, bs)
		}
	}
	// 400 KiB (>= SOFT_MAX_BLOCK_LENGTH + MIN_BLOCK_LENGTH bytes) of
	// stationary data, a random letter or a copy of 3..32 bytes from up to
	// 32768 back, which the split statistics do not split, at levels 2..4: the
	// first block ends at in_max_block_end with more than 258 bytes left, i.e.
	// at greedyLoopAsm's in_max_block_end exit.
	// Its own generator, so that the data does not depend on -short (the
	// statistics of an unlucky draw can still split the block before the soft
	// maximum, which the check below would report).
	for level := 2; level <= 4; level++ {
		sr := rand.New(rand.NewPCG(92, uint64(level)))
		b := make([]byte, 0, 400<<10+32)
		for len(b) < 400<<10 {
			if len(b) < 8 || sr.IntN(3) == 0 {
				b = append(b, byte('a'+sr.IntN(26)))
				continue
			}
			d := 1 + sr.IntN(min(len(b), matchfinderWindowSize))
			for range 3 + sr.IntN(30) {
				b = append(b, b[len(b)-d])
			}
		}
		bs, err := walkBlocks(checkGreedyAsm(t, e, level, nil, b[:400<<10], true))
		soft := 0
		for _, b := range bs {
			if b.ulen >= softMaxBlockLength && b.ulen < softMaxBlockLength+deflateMaxMatchLen {
				soft++
			}
		}
		if err != nil || soft == 0 {
			t.Fatalf("level %d stationary: no block ended at the soft maximum length (%v): %v", level, err, bs)
		}
	}
}

// FuzzDeflateGreedyLoopAsmEquiv (levels 2..4, greedyLoopAsm) and
// FuzzDeflateLazyLoopAsmEquiv (levels 5..9, lazyLoopAsm): the fuzz input's
// first byte selects the level, the next 2 (little-endian) the history length
// (at most the rest); the rest is history+src.
func FuzzDeflateGreedyLoopAsmEquiv(f *testing.F) { fuzzHcLoopAsm(f, 2, 3) }

func FuzzDeflateLazyLoopAsmEquiv(f *testing.F) { fuzzHcLoopAsm(f, 5, 5) }

func fuzzHcLoopAsm(f *testing.F, firstLevel, levels int) {
	f.Add([]byte{})
	f.Add(append([]byte{0, 0, 0}, bytes.Repeat([]byte("abcdefgh"), 100)...))
	f.Add(append([]byte{4, 0x10, 0}, bytes.Repeat([]byte("WXYZhello, world WXYZhello, there "), 40)...))
	r := rand.New(rand.NewPCG(7, 8))
	f.Add(append([]byte{1, 0, 1}, fastestAsmData(r, 3000)...))
	f.Add(append([]byte{2, 0, 0}, hcRecords(r, 20000, 20)...))
	const maxIn = 1 << 17
	e := newGreedyAsmEnv(f, maxIn)
	f.Fuzz(func(t *testing.T, data []byte) {
		level, h := firstLevel, 0
		if len(data) >= 3 {
			level = firstLevel + int(data[0])%levels
			h = int(binary.LittleEndian.Uint16(data[1:]))
			data = data[3:]
		}
		data = data[:min(len(data), maxIn)]
		h = min(h, len(data))
		checkGreedyAsm(t, e, level, data[:h], data[h:], h%2 == 0)
	})
}
