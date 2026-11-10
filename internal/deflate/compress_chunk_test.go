package deflate

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// chunkLevels are the levels CompressChunk is tested at: HuffmanOnly and
// every libdeflate level.
var chunkLevels = []int{HuffmanOnly, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

func newChunkCompressor(t testing.TB, level int) *Compressor {
	t.Helper()
	if level == HuffmanOnly {
		return NewHuffmanOnlyCompressor()
	}
	return mustCompressor(t, level)
}

// CompressChunk(dst, nil, src, true) is Compress (and so libdeflate, which
// the existing differential tests pin Compress to) on the whole corpus at
// every level, including the exact-size and one-byte-short buffers.
func TestCompressChunkFinalMatchesCompress(t *testing.T) {
	cases := deflatetest.Cases(true)
	for l := 0; l <= 12; l++ {
		c := mustCompressor(t, l)
		cc := mustCompressor(t, l)
		for _, cs := range cases {
			if testing.Short() && l >= 10 && len(cs.Data) > 300000 {
				continue
			}
			// The race detector slows the parsers ~10x; they run no
			// goroutines, so small inputs suffice there.
			if raceEnabled && len(cs.Data) > 20000 {
				continue
			}
			dst := make([]byte, c.CompressBound(len(cs.Data)))
			n := c.Compress(dst, cs.Data)
			want := dst[:n]
			got := make([]byte, cc.CompressChunkBound(len(cs.Data)))
			m := cc.CompressChunk(got, nil, cs.Data, true)
			if !bytes.Equal(got[:m], want) {
				t.Fatalf("level %d %s: CompressChunk %d bytes, Compress %d", l, cs.Name, m, n)
			}
			// An empty, non-nil history is no history.
			if m2 := cc.CompressChunk(got, cs.Data[:0], cs.Data, true); m2 != n || !bytes.Equal(got[:m2], want) {
				t.Fatalf("level %d %s: empty history differs", l, cs.Name)
			}
			if m2 := cc.CompressChunk(got[:n], nil, cs.Data, true); m2 != n {
				t.Fatalf("level %d %s: exact-size buffer gave %d, want %d", l, cs.Name, m2, n)
			}
			if m2 := cc.CompressChunk(got[:n-1], nil, cs.Data, true); m2 != 0 {
				t.Fatalf("level %d %s: one byte short gave %d, want 0", l, cs.Name, m2)
			}
		}
	}
}

// chunkData generates n bytes that reference themselves at distances up to
// 32768 (incl. exactly 32767 and 32768), so chunks find matches in their
// history.
func chunkData(r *rand.Rand, n int) []byte {
	b := make([]byte, 0, n+300)
	alpha := 2 + r.IntN(250)
	for len(b) < n {
		switch op := r.IntN(10); {
		case op < 4 || len(b) < 300:
			for k := 1 + r.IntN(40); k > 0; k-- {
				b = append(b, byte(r.IntN(alpha)))
			}
		default:
			var d int
			switch r.IntN(6) {
			case 0:
				d = 32768
			case 1:
				d = 32767
			case 2:
				d = 1 + r.IntN(8)
			default:
				d = 1 + r.IntN(32768)
			}
			d = min(d, len(b))
			for k := 3 + r.IntN(256); k > 0; k-- {
				b = append(b, b[len(b)-d])
			}
		}
	}
	return b[:n]
}

// chunkSizes returns random chunk sizes covering empty, tiny (below the
// matchfinders' 5 bytes and the passthrough sizes), sub-MIN_BLOCK_LENGTH and
// multi-block chunks.
func chunkSize(r *rand.Rand) int {
	switch r.IntN(12) {
	case 0:
		return 0
	case 1:
		return 1 + r.IntN(8)
	case 2:
		return 9 + r.IntN(60)
	case 3:
		return minBlockLength - 1 + r.IntN(3)
	case 4:
		return 100 + r.IntN(minBlockLength)
	case 5:
		if r.IntN(4) == 0 {
			return softMaxBlockLength + r.IntN(100000)
		}
		return 65535 + r.IntN(3)
	default:
		return r.IntN(70000)
	}
}

// historyLen picks how much of the preceding data is passed as history.
func historyLen(r *rand.Rand, pos int) int {
	var h int
	switch r.IntN(6) {
	case 0:
		h = 0
	case 1:
		h = matchfinderWindowSize
	case 2:
		h = matchfinderWindowSize + 1 + r.IntN(1000) // truncated to the last 32 KiB
	case 3:
		h = 1 + r.IntN(8)
	default:
		h = r.IntN(matchfinderWindowSize + 1)
	}
	return min(h, pos)
}

// chunkStream compresses data as consecutive chunks (sizes from r, the last
// one final) with c and returns the concatenated stream. Histories are
// passed adjacent (a slice of data) or, every other chunk, as a copy.
func chunkStream(t testing.TB, c *Compressor, r *rand.Rand, data []byte) []byte {
	t.Helper()
	var out []byte
	pos := 0
	for i := 0; ; i++ {
		n := min(chunkSize(r), len(data)-pos)
		final := pos+n == len(data) && r.IntN(3) != 0
		h := historyLen(r, pos)
		hist := data[pos-h : pos]
		if i%2 == 1 && h > 0 {
			hist = bytes.Clone(hist)
		}
		src := data[pos : pos+n]
		dst := make([]byte, c.CompressChunkBound(n))
		m := c.CompressChunk(dst, hist, src, final)
		if m == 0 {
			t.Fatalf("chunk %d (%d bytes, history %d, final %v): bound too small", i, n, h, final)
		}
		if !final && !bytes.HasSuffix(dst[:m], []byte{0, 0, 0xFF, 0xFF}) {
			t.Fatalf("chunk %d: non-final chunk does not end with a sync flush: % x", i, dst[max(0, m-6):m])
		}
		// Exact size fits, one byte less does not.
		if m2 := c.CompressChunk(make([]byte, m), hist, src, final); m2 != m {
			t.Fatalf("chunk %d: exact buffer gave %d, want %d", i, m2, m)
		}
		if m2 := c.CompressChunk(make([]byte, m-1), hist, src, final); m2 != 0 {
			t.Fatalf("chunk %d: one byte short gave %d", i, m2)
		}
		out = append(out, dst[:m]...)
		pos += n
		if final {
			return out
		}
	}
}

// Concatenated chunks decode, with our decoder, compress/flate and
// libdeflate, to the input at every level.
func TestCompressChunkRoundTrip(t *testing.T) {
	seeds, size := 12, 400000
	if testing.Short() {
		seeds, size = 5, 200000
	}
	if raceEnabled {
		seeds, size = 3, 60000
	}
	for _, l := range chunkLevels {
		c := newChunkCompressor(t, l)
		var reqs []ldReq
		var wants [][]byte
		for s := range seeds {
			r := rand.New(rand.NewPCG(uint64(l+100), uint64(s)))
			n := r.IntN(size)
			if s == 0 {
				n = 0
			}
			data := chunkData(r, n)
			stream := chunkStream(t, c, r, data)
			if err := checkDecode(t, stream, data); err != nil {
				t.Fatalf("level %d seed %d (%d bytes): %v", l, s, n, err)
			}
			reqs = append(reqs, ldReq{name: fmt.Sprint(s), in: stream, outSize: len(data), exact: true})
			wants = append(wants, data)
		}
		for i, res := range ldBatch(t, reqs) {
			if res.res != libdeflateSuccess || res.nIn != len(reqs[i].in) || !bytes.Equal(res.out, wants[i]) {
				t.Fatalf("level %d seed %d: libdeflate result %d, in %d/%d, out equal %v", l, i, res.res,
					res.nIn, len(reqs[i].in), bytes.Equal(res.out, wants[i]))
			}
		}
	}
}

// Matches reach into the history, also a full 32 KiB one, and a chunk
// whose only matches are at the window's far end still decodes.
func TestCompressChunkUsesHistory(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	rnd := make([]byte, 40000)
	for i := range rnd {
		rnd[i] = byte(r.Uint32())
	}
	type tc struct {
		name       string
		hist, src  []byte
		mustShrink bool
	}
	cases := []tc{
		// src repeats the history at distance 32767 (history: 1 byte
		// then 32767 bytes, all 32768 used).
		{"dist32767", rnd[:32768], rnd[1:32768], true},
		// A short history, src repeats it.
		{"short", rnd[:6000], rnd[:6000], true},
		// src repeats the history at exactly 32768: libdeflate's
		// matchfinders only find offsets < 32768, so this only has to
		// decode.
		{"dist32768", rnd[:32768], rnd[:32768], false},
		// Longer than the window: only the last 32 KiB are history.
		{"long", rnd[:40000], rnd[40000-20000 : 40000], true},
	}
	for _, l := range chunkLevels {
		c := newChunkCompressor(t, l)
		for _, k := range cases {
			for _, final := range []bool{true, false} {
				dst := make([]byte, c.CompressChunkBound(len(k.src)))
				n := c.CompressChunk(dst, k.hist, k.src, final)
				stream := dst[:n]
				if !final {
					stream = append(stream, 3, 0) // a final empty static block
				}
				// Decode history+src as one stream: stored history first.
				whole := append(c.storedPrefix(k.hist), stream...)
				want := append(bytes.Clone(k.hist), k.src...)
				if err := checkDecode(t, whole, want); err != nil {
					t.Fatalf("level %d %s final=%v: %v", l, k.name, final, err)
				}
				if k.mustShrink && l >= 1 && n > len(k.src)/4 {
					t.Fatalf("level %d %s: %d bytes for %d: history not used", l, k.name, n, len(k.src))
				}
				if (l <= 0) && n < len(k.src) {
					t.Fatalf("level %d %s: %d bytes for %d: matches without a matchfinder", l, k.name, n,
						len(k.src))
				}
			}
		}
	}
}

// storedPrefix outputs b as non-final stored blocks (a stream prefix whose
// decoded output is b, so a chunk compressed with history b can follow).
func (c *Compressor) storedPrefix(b []byte) []byte {
	dst := make([]byte, len(b)+5*(len(b)/math.MaxUint16+1)+syncMarkerMaxLen)
	n := c.compressNoneNonFinal(dst, b)
	return dst[:n]
}

// Exact outputs of the edge cases: empty chunks, and tiny ones.
func TestCompressChunkEdges(t *testing.T) {
	for _, l := range chunkLevels {
		c := newChunkCompressor(t, l)
		dst := make([]byte, 64)
		hist := []byte("some history bytes")
		if n := c.CompressChunk(dst, hist, nil, false); !bytes.Equal(dst[:n], []byte{0, 0, 0, 0xFF, 0xFF}) {
			t.Fatalf("level %d: empty non-final chunk = % x", l, dst[:n])
		}
		if n := c.CompressChunk(dst, hist, nil, true); !bytes.Equal(dst[:n], []byte{1, 0, 0, 0xFF, 0xFF}) {
			t.Fatalf("level %d: empty final chunk = % x", l, dst[:n])
		}
		if n := c.CompressChunk(dst[:4], nil, nil, false); n != 0 {
			t.Fatalf("level %d: empty non-final chunk in 4 bytes = %d", l, n)
		}
		if n := c.CompressChunk(dst, hist, []byte("ab"), false); !bytes.Equal(dst[:n],
			[]byte{0, 2, 0, 0xFD, 0xFF, 'a', 'b', 0, 0, 0, 0xFF, 0xFF}) {
			t.Fatalf("level %d: 2-byte non-final chunk = % x", l, dst[:n])
		}
		if n := c.CompressChunk(dst[:11], hist, []byte("ab"), false); n != 0 {
			t.Fatalf("level %d: 2-byte non-final chunk in 11 bytes = %d", l, n)
		}
		if n := c.CompressChunk(dst[:1], hist, []byte("ab"), false); n != 0 {
			t.Fatalf("level %d: 2-byte non-final chunk in 1 byte = %d", l, n)
		}
	}
	// Multi-block stored non-final chunk (level 0): 3 blocks + marker.
	c := mustCompressor(t, 0)
	src := randData(9, 2*math.MaxUint16+10)
	dst := make([]byte, c.CompressChunkBound(len(src)))
	n := c.CompressChunk(dst, nil, src, false)
	if n != len(src)+3*5+5 {
		t.Fatalf("level 0, %d bytes: %d", len(src), n)
	}
	if err := checkDecode(t, append(dst[:n:n], 3, 0), src); err != nil {
		t.Fatal(err)
	}
	if m := c.CompressChunk(dst[:n-1], nil, src, false); m != 0 {
		t.Fatalf("one byte short: %d", m)
	}
}

// Every value of the bit count before the sync marker (0..7, so both the
// 5- and the 6-byte marker) is reached, and the marker's size check is
// exact.
func TestCompressChunkMarkerBits(t *testing.T) {
	seen := map[uint]bool{}
	c := mustCompressor(t, 1)
	for s := uint64(0); s < 2000 && len(seen) < 8; s++ {
		r := rand.New(rand.NewPCG(s, 77))
		src := chunkData(r, 60+r.IntN(3000))
		dst := make([]byte, c.CompressChunkBound(len(src)))
		fin := c.CompressChunk(dst, nil, src, true)
		// The same blocks end the final stream: its bit count is the one
		// the non-final chunk's marker starts at.
		bitcount := c.os.bitcount
		seen[bitcount] = true
		n := c.CompressChunk(dst, nil, src, false)
		if want := fin - b2i(bitcount != 0) + (int(bitcount)+3+7)/8 + 4; n != want {
			t.Fatalf("seed %d bitcount %d: non-final %d bytes, want %d", s, bitcount, n, want)
		}
		if err := checkDecode(t, append(dst[:n:n], 3, 0), src); err != nil {
			t.Fatalf("seed %d: %v", s, err)
		}
		if m := c.CompressChunk(dst[:n-1], nil, src, false); m != 0 {
			t.Fatalf("seed %d: one byte short gave %d", s, m)
		}
	}
	if len(seen) < 8 {
		t.Fatalf("bit counts seen: %v", seen)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A chunk's output depends only on its arguments, not on earlier calls:
// a series of chunks through one compressor equals each chunk through a
// fresh one.
func TestCompressChunkDeterministic(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	var parts [][]byte
	maxPart := 130000
	if raceEnabled {
		maxPart = 30000
	}
	for range 6 {
		parts = append(parts, chunkData(r, 20000+r.IntN(maxPart)))
	}
	// Parts whose statistics change mid-chunk, so the near-optimal parser
	// ends its first block at a detected change (the rewind path).
	low := make([]byte, 60000)
	for i := range low {
		low[i] = 'a' + byte(r.IntN(4))
	}
	parts = append(parts, randData(3, 50000),
		append(bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 900), parts[0]...),
		append(bytes.Clone(low), randData(4, 60000)...), append(randData(5, 60000), low...))
	for _, l := range chunkLevels {
		used := newChunkCompressor(t, l)
		var hist []byte
		for i, p := range parts {
			final := i%2 == 1
			want := make([]byte, used.CompressChunkBound(len(p)))
			n := newChunkCompressor(t, l).CompressChunk(want, hist, p, final)
			got := make([]byte, len(want))
			m := used.CompressChunk(got, hist, p, final)
			if !bytes.Equal(got[:m], want[:n]) {
				t.Fatalf("level %d part %d: output depends on an earlier call (%d vs %d bytes)", l, i, m, n)
			}
			hist = p[len(p)-min(len(p), 30000):]
		}
	}
}

func TestCompressChunkBound(t *testing.T) {
	var c *Compressor
	for _, n := range []int{0, 1, 5000, 1 << 20} {
		if got, want := c.CompressChunkBound(n), c.CompressBound(n)+5; got != want {
			t.Errorf("CompressChunkBound(%d) = %d, want %d", n, got, want)
		}
	}
	if got := c.CompressChunkBound(math.MaxInt); got != math.MaxInt {
		t.Errorf("CompressChunkBound(MaxInt) = %d", got)
	}
	if got := c.CompressChunkBound(math.MaxInt - 4); got != math.MaxInt {
		t.Errorf("CompressChunkBound(MaxInt-4) = %d", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("CompressChunkBound(-1) did not panic")
		}
	}()
	c.CompressChunkBound(-1)
}

// HuffmanOnly uses no matches and round-trips (no libdeflate counterpart).
func TestHuffmanOnly(t *testing.T) {
	c := NewHuffmanOnlyCompressor()
	if c.Level() != HuffmanOnly {
		t.Fatalf("Level() = %d", c.Level())
	}
	for _, cs := range deflatetest.Cases(true) {
		dst := make([]byte, c.CompressBound(len(cs.Data)))
		n := c.Compress(dst, cs.Data)
		if err := checkDecode(t, dst[:n], cs.Data); err != nil {
			t.Fatalf("%s: %v", cs.Name, err)
		}
	}
	// All-same bytes: at least 1 bit per literal (a match would be tiny).
	src := bytes.Repeat([]byte{'a'}, 100000)
	dst := make([]byte, c.CompressBound(len(src)))
	if n := c.Compress(dst, src); n < len(src)/8 {
		t.Fatalf("%d bytes for %d equal bytes: matches were used", n, len(src))
	}
	// Multi-block: > SOFT_MAX_BLOCK_LENGTH.
	big := randData(4, 2*softMaxBlockLength+minBlockLength)
	for i := range big {
		big[i] &= 7
	}
	dst = make([]byte, c.CompressBound(len(big)))
	n := c.Compress(dst, big)
	if err := checkDecode(t, dst[:n], big); err != nil {
		t.Fatal(err)
	}
	if n >= len(big)/2 || c.Compress(dst[:n-1], big) != 0 {
		t.Fatalf("HuffmanOnly: %d bytes for %d 3-bit symbols / short buffer accepted", n, len(big))
	}
	// Blocks end at SOFT_MAX_BLOCK_LENGTH: two alphabets of 4 symbols, one
	// per 300000-byte block, cost 2.25 bits per byte (the end-of-block
	// symbol takes a 3-bit codeword from one literal); one block, over 3.
	two := make([]byte, 2*softMaxBlockLength+minBlockLength)
	for i := range two {
		two[i] = big[i] & 3
		if i >= softMaxBlockLength {
			two[i] += 4
		}
	}
	dst = make([]byte, c.CompressBound(len(two)))
	n = c.Compress(dst, two)
	if err := checkDecode(t, dst[:n], two); err != nil {
		t.Fatal(err)
	}
	if n > len(two)*26/80 {
		t.Fatalf("HuffmanOnly: %d bytes for %d 2-bit-per-block symbols: blocks not split", n, len(two))
	}
}

func TestCompressChunkNoAlloc(t *testing.T) {
	// Allocation counts on large inputs, single goroutine: nothing for -race to find.
	if raceEnabled {
		t.Skip("allocation counts: no goroutines, runs in the normal lane")
	}
	r := rand.New(rand.NewPCG(8, 8))
	buf := chunkData(r, 32768+100000)
	hist, src := buf[:32768], buf[32768:]
	histCopy := bytes.Clone(hist)
	dst := make([]byte, 200000)
	for _, l := range []int{HuffmanOnly, 0, 1, 6, 12} {
		c := newChunkCompressor(t, l)
		c.CompressChunk(dst, histCopy, src, false) // allocates the copy buffer once
		for _, tc := range []struct {
			hist  []byte
			n     int
			final bool
		}{{hist, len(src), false}, {hist, len(src), true}, {histCopy, len(src), false}, {nil, 3, false},
			{hist, 0, false}, {nil, len(src), true}} {
			allocs := testing.AllocsPerRun(5, func() {
				if c.CompressChunk(dst, tc.hist, src[:tc.n], tc.final) == 0 {
					t.Fatal("compress failed")
				}
			})
			if allocs != 0 {
				t.Errorf("level %d n=%d final=%v: %v allocs", l, tc.n, tc.final, allocs)
			}
		}
	}
}

// FuzzCompressChunk: data[0] picks the level, data[1:9] seeds the chunk
// split; the stream must decode (our decoder and compress/flate).
func FuzzCompressChunk(f *testing.F) {
	f.Add([]byte{0})
	f.Add(append([]byte{7, 1, 2, 3, 4, 5, 6, 7, 8}, bytes.Repeat([]byte("abcdabcxabcd"), 2000)...))
	f.Add(append([]byte{13, 9, 9, 9, 9, 9, 9, 9, 9}, structuredInput(1, 70000)...))
	f.Add(append([]byte{0, 1, 0, 0, 0, 0, 0, 0, 0}, structuredInput(2, 3000)...))
	f.Add(append([]byte{2, 5, 0, 0, 0, 0, 0, 0, 0}, chunkData(rand.New(rand.NewPCG(1, 2)), 90000)...))
	cs := make([]*Compressor, len(chunkLevels))
	for i, l := range chunkLevels {
		cs[i] = newChunkCompressor(f, l)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 9 {
			return
		}
		c := cs[int(data[0])%len(cs)]
		var seed uint64
		for _, b := range data[1:9] {
			seed = seed<<8 | uint64(b)
		}
		in := data[9:]
		stream := chunkStream(t, c, rand.New(rand.NewPCG(seed, 1)), in)
		if err := checkDecode(t, stream, in); err != nil {
			t.Fatalf("level %d, %d bytes: %v", c.Level(), len(in), err)
		}
	})
}

// A CompressChunk leaves no state behind: Compress, and a history-less
// final CompressChunk, after a chunk with history equal a fresh
// compressor's output.
func TestCompressChunkNoStateLeak(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 21))
	data := chunkData(r, 120000)
	hist, src := data[:30000], data[30000:]
	for _, l := range chunkLevels {
		want := make([]byte, chunkBoundOf(len(src)))
		n := newChunkCompressor(t, l).Compress(want, src)
		for _, final := range []bool{false, true} {
			c := newChunkCompressor(t, l)
			tmp := make([]byte, c.CompressChunkBound(len(src)))
			c.CompressChunk(tmp, hist, src, final)
			got := make([]byte, len(want))
			if m := c.Compress(got, src); !bytes.Equal(got[:m], want[:n]) {
				t.Fatalf("level %d final=%v: Compress after a chunk differs (%d vs %d bytes)", l, final, m, n)
			}
			c.CompressChunk(tmp, hist, src, final)
			if m := c.CompressChunk(got, nil, src, true); !bytes.Equal(got[:m], want[:n]) {
				t.Fatalf("level %d final=%v: history-less chunk after a chunk differs", l, final)
			}
		}
	}
}

func chunkBoundOf(n int) int { return (*Compressor)(nil).CompressChunkBound(n) }

// The last history byte is primed: src is a run of a byte that occurs in
// the history only as its last byte, so the chunk's first item must be a
// match (offset 1, into the history) at every level with a parser.
func TestCompressChunkPrimesLastHistoryByte(t *testing.T) {
	r := rand.New(rand.NewPCG(22, 22))
	hist := make([]byte, 3000)
	for i := range hist {
		hist[i] = 'a' + byte(r.IntN(20))
	}
	hist[len(hist)-1] = 'Z'
	src := append(bytes.Repeat([]byte{'Z'}, 300), hist[:200]...)
	for _, hl := range []int{len(hist), matchfinderWindowSize} {
		h := hist
		if hl == matchfinderWindowSize {
			// A full window: 'Z' only at its end, too.
			h = append(bytes.Repeat(hist[:len(hist)-1], 11)[:matchfinderWindowSize-1], 'Z')
		}
		for l := 1; l <= 12; l++ {
			c := mustCompressor(t, l)
			dst := make([]byte, c.CompressChunkBound(len(src)))
			n := c.CompressChunk(dst, h, src, true)
			var firstIsMatch bool
			switch {
			case l == 1:
				firstIsMatch = c.pf.sequences[0].litrunlenAndLength&seqLitrunlenMask == 0
			case l <= 9:
				firstIsMatch = c.pg.sequences[0].litrunlenAndLength&seqLitrunlenMask == 0
			default:
				firstIsMatch = c.pnState.optimumNodes[0].item&optimumLenMask > 1
			}
			if !firstIsMatch {
				t.Fatalf("level %d history %d: src[0] is a literal: the last history byte was not primed", l, hl)
			}
			if err := checkDecode(t, append(c.storedPrefix(h), dst[:n]...), append(bytes.Clone(h), src...)); err != nil {
				t.Fatalf("level %d history %d: %v", l, hl, err)
			}
		}
	}
}

// Only the last 32 KiB of a longer history are used: the output equals
// that of exactly those 32768 bytes.
func TestCompressChunkHistoryTruncation(t *testing.T) {
	r := rand.New(rand.NewPCG(23, 23))
	data := chunkData(r, 1<<20+60000)
	src := data[1<<20:]
	for _, l := range chunkLevels {
		c := newChunkCompressor(t, l)
		want := make([]byte, c.CompressChunkBound(len(src)))
		n := c.CompressChunk(want, data[1<<20-matchfinderWindowSize:1<<20], src, false)
		got := make([]byte, len(want))
		for _, hl := range []int{matchfinderWindowSize + 1, 40000, 1 << 20} {
			for _, adjacent := range []bool{true, false} {
				h := data[1<<20-hl : 1<<20]
				if !adjacent {
					h = bytes.Clone(h)
				}
				if m := c.CompressChunk(got, h, src, false); !bytes.Equal(got[:m], want[:n]) {
					t.Fatalf("level %d history %d adjacent=%v: differs from its last 32 KiB", l, hl, adjacent)
				}
			}
		}
	}
}
