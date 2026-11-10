package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// btmfHash is ldref dump-btmf's FNV-1a 64 over hash3_tab, hash4_tab and
// child_tab as s16 LE bytes.
func btmfHash(mf *btMatchfinder) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, tab := range [][]mfPos{mf.hash3Tab[:], mf.hash4Tab[:], mf.childTab[:]} {
		for _, v := range tab {
			h = (h ^ uint64(uint8(v))) * 0x100000001b3
			h = (h ^ uint64(uint8(uint16(v)>>8))) * 0x100000001b3
		}
	}
	return h
}

// btmfDump serializes the matchfinder like dump-btmf's 'c' op with full 1.
func btmfDump(mf *btMatchfinder) []byte {
	var b []byte
	for _, tab := range [][]mfPos{mf.hash3Tab[:], mf.hash4Tab[:], mf.childTab[:]} {
		for _, v := range tab {
			b = binary.LittleEndian.AppendUint16(b, uint16(v))
		}
	}
	return b
}

// runBtmfScript runs an `ldref dump-btmf` script (framing in ldref.c) on
// bt_matchfinder.go (and, for 'c', on a fresh Compressor) and returns the
// same output bytes. Like ldref (and deflate_compress_near_optimal) it
// slides the window when a position is a full window past the base.
func runBtmfScript(t testing.TB, script []byte) []byte {
	t.Helper()
	mf := new(btMatchfinder)
	var buf []byte
	base := 0
	var nextHashes [2]uint32
	var out []byte
	var m [maxMatchesPerPos]lzMatch
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(script[i:]) }
	slide := func(pos int) int {
		if pos-base == matchfinderWindowSize {
			mf.slideWindow()
			base = pos
		}
		return pos - base
	}
	for p := 0; p < len(script); {
		op := script[p]
		p++
		switch op {
		case 'b':
			n := int(u32(p))
			buf = script[p+4 : p+4+n]
			p += 4 + n
			mf.childTab = [2 * matchfinderWindowSize]mfPos{}
			mf.init()
			base = 0
			nextHashes = [2]uint32{}
		case 'g':
			cur := slide(int(u32(p)))
			k := mf.getMatches(buf, base, cur, u32(p+4), u32(p+8), u32(p+12), &nextHashes, &m)
			p += 16
			out = binary.LittleEndian.AppendUint16(out, uint16(k))
			for _, x := range m[:k] {
				out = binary.LittleEndian.AppendUint16(out, x.length)
				out = binary.LittleEndian.AppendUint16(out, x.offset)
			}
		case 'k':
			cur := slide(int(u32(p)))
			mf.skipByte(buf, base, cur, u32(p+4), u32(p+8), &nextHashes)
			p += 12
		case 'h':
			out = binary.LittleEndian.AppendUint64(out, btmfHash(mf))
			out = binary.LittleEndian.AppendUint32(out, uint32(base))
			out = binary.LittleEndian.AppendUint32(out, nextHashes[0])
			out = binary.LittleEndian.AppendUint32(out, nextHashes[1])
		case 'c':
			c := mustCompressor(t, int(script[p]))
			full := script[p+1] == 1
			p += 2
			dst := make([]byte, c.CompressBound(len(buf)))
			if c.Compress(dst, buf) == 0 {
				t.Fatal("compress failed")
			}
			if full {
				out = append(out, btmfDump(&c.pnState.btMf)...)
			} else {
				out = binary.LittleEndian.AppendUint64(out, btmfHash(&c.pnState.btMf))
			}
		default:
			t.Fatalf("bad script op %q at %d", op, p-1)
		}
	}
	return out
}

// btData is a random input for the bt matchfinder scripts: hcData, long
// periodic data with sparse mutations (many long equal matches: nice_len
// returns and deep trees), or few-letter text with planted copies at
// distances near the 32 KiB window.
func btData(r *rand.Rand, n int) []byte {
	switch r.IntN(6) {
	case 0, 1, 2:
		return hcData(r, n)
	case 3:
		p := 1 + r.IntN([]int{4, 40, 300}[r.IntN(3)])
		b := make([]byte, n)
		for i := range b {
			if i < p {
				b[i] = byte(r.IntN(4))
			} else {
				b[i] = b[i-p]
			}
			if r.IntN(200) == 0 {
				b[i] ^= byte(1 + r.IntN(255))
			}
		}
		return b
	case 4:
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a' + byte(r.IntN(3))
		}
		return b
	default:
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		for k := 0; k < n/2000; k++ {
			d := []int{1, 2, 3, 4, 32766, 32767, 32768, 32769, 1 + r.IntN(32768)}[r.IntN(9)]
			l := 3 + r.IntN(300)
			s := d + r.IntN(max(1, n-d-l))
			for i := s; i < min(n, s+l); i++ {
				b[i] = b[i-d]
			}
		}
		return b
	}
}

// btScript builds one dump-btmf script: a buffer, then a walk over its
// positions in order like the near-optimal parser's (each position either
// searched by 'g' with random max_len / nice_len / depth, or skipped by 'k'
// as the parser skips the positions covered by a long match), state hashes
// along the way, and sometimes a whole level 10..12 compression.
func btScript(r *rand.Rand, short bool, withCompress bool) []byte {
	var n int
	switch k := r.IntN(100); {
	case k < 88:
		n = 6 + r.IntN(2000)
	case k < 98:
		n = 2000 + r.IntN(18000)
	default:
		n = 33000 + r.IntN(70000)
	}
	if short && n > 40000 {
		n = 40000
	}
	data := btData(r, n)
	var b []byte
	u32 := func(vs ...uint32) {
		for _, v := range vs {
			b = binary.LittleEndian.AppendUint32(b, v)
		}
	}
	b = append(b, 'b')
	u32(uint32(n))
	b = append(b, data...)
	depths := []uint32{1, 2, 3, 8, 34, 35, 36, 99, 100, 101, 299, 300, 301}
	nices := []uint32{1, 3, 4, 5, 6, 74, 75, 76, 149, 150, 151, 257, 258}
	for p := 0; n-p >= btMatchfinderRequiredNbytes; {
		rem := uint32(n - p)
		maxLen := min(rem, deflateMaxMatchLen)
		if r.IntN(10) == 0 {
			maxLen = 5 + uint32(r.IntN(int(maxLen)-4))
		}
		niceLen := min(nices[r.IntN(len(nices))], maxLen)
		if r.IntN(8) == 0 {
			niceLen = 1 + uint32(r.IntN(int(maxLen)))
		}
		depth := depths[r.IntN(len(depths))]
		b = append(b, 'g')
		u32(uint32(p), maxLen, niceLen, depth)
		p++
		if r.IntN(4) == 0 {
			for count := 1 + r.IntN([3]int{3, 40, 258}[r.IntN(3)]); count > 0 && n-p >= 5; count-- {
				nice := max(5, min(niceLen, uint32(n-p)))
				if r.IntN(10) == 0 {
					nice = 5 + uint32(r.IntN(min(n-p, deflateMaxMatchLen)-4))
				}
				b = append(b, 'k')
				u32(uint32(p), nice, depth)
				p++
			}
		}
		if r.IntN(500) == 0 {
			b = append(b, 'h')
		}
	}
	b = append(b, 'h')
	if withCompress && n > 15 && r.IntN(20) == 0 {
		b = append(b, 'c', byte(10+r.IntN(3)), 0)
	}
	return b
}

// diffBtmfScripts compares runBtmfScript with ldref dump-btmf on count
// random scripts, narrowing a mismatch down to the script and output byte.
func diffBtmfScripts(t *testing.T, r *rand.Rand, count int, withCompress bool) {
	var all []byte
	var starts []int
	flush := func() {
		want := creftest.LDRun(t, all, "dump-btmf")
		if got := runBtmfScript(t, all); !bytes.Equal(got, want) {
			for i, s := range starts {
				e := len(all)
				if i+1 < len(starts) {
					e = starts[i+1]
				}
				w := creftest.LDRun(t, all[s:e], "dump-btmf")
				if g := runBtmfScript(t, all[s:e]); !bytes.Equal(g, w) {
					d := firstByteDiff(g, w)
					t.Fatalf("script %d (%d bytes) differs at output byte %d: go % x, libdeflate % x", i, e-s, d,
						g[d:min(len(g), d+8)], w[d:min(len(w), d+8)])
				}
			}
			t.Fatal("outputs differ but no single script does")
		}
		all, starts = all[:0], starts[:0]
	}
	for range count {
		starts = append(starts, len(all))
		all = append(all, btScript(r, testing.Short(), withCompress)...)
		if len(all) > 16<<20 {
			flush()
		}
	}
	flush()
}

// bt_matchfinder.go vs libdeflate's bt_matchfinder (ldref dump-btmf) on
// 20000 random scripts (-short: 2000): every get_matches list (lengths
// and offsets), the full matchfinder state (tables, in_base, next_hashes)
// after each script and at random points, across window slides; 1 in 20
// scripts adds a whole level 10..12 compression's final state.
func TestBtMatchfinderMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	diffBtmfScripts(t, rand.New(rand.NewPCG(26, 1)), n, true)
}
