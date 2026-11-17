package deflate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// hcmfHash is ldref dump-hcmf's FNV-1a 64 over hash3_tab, hash4_tab and
// next_tab as s16 LE bytes.
func hcmfHash(mf *hcMatchfinder) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, tab := range [][]mfPos{mf.hash3Tab[:], mf.hash4Tab[:], mf.nextTab[:]} {
		for _, v := range tab {
			h = (h ^ uint64(uint8(v))) * 0x100000001b3
			h = (h ^ uint64(uint8(uint16(v)>>8))) * 0x100000001b3
		}
	}
	return h
}

// hcmfDump serializes the matchfinder like dump-hcmf's 'c' op with full 1.
func hcmfDump(mf *hcMatchfinder) []byte {
	var b []byte
	for _, tab := range [][]mfPos{mf.hash3Tab[:], mf.hash4Tab[:], mf.nextTab[:]} {
		for _, v := range tab {
			b = binary.LittleEndian.AppendUint16(b, uint16(v))
		}
	}
	return b
}

// runHcmfScript runs an `ldref dump-hcmf` script (framing in ldref.c) on
// hc_matchfinder.go (and, for 'c', on a fresh Compressor) and returns the
// same output bytes.
func runHcmfScript(t testing.TB, script []byte) []byte {
	t.Helper()
	mf := new(hcMatchfinder)
	var buf []byte
	base := 0
	var nextHashes [2]uint32
	var out []byte
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(script[i:]) }
	for p := 0; p < len(script); {
		op := script[p]
		p++
		switch op {
		case 'b':
			n := int(u32(p))
			buf = script[p+4 : p+4+n]
			p += 4 + n
			mf.nextTab = [matchfinderWindowSize]mfPos{}
			mf.init()
			base = 0
			nextHashes = [2]uint32{}
		case 'f':
			l, off := mf.longestMatch(buf, &base, int(u32(p)), u32(p+4), u32(p+8), u32(p+12), u32(p+16), &nextHashes)
			p += 20
			out = binary.LittleEndian.AppendUint16(out, uint16(l))
			out = binary.LittleEndian.AppendUint16(out, uint16(off))
		case 'k':
			mf.skipBytes(buf, &base, int(u32(p)), len(buf), u32(p+4), &nextHashes)
			p += 8
		case 'h':
			out = binary.LittleEndian.AppendUint64(out, hcmfHash(mf))
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
				out = append(out, hcmfDump(&c.pg.hcMf)...)
			} else {
				out = binary.LittleEndian.AppendUint64(out, hcmfHash(&c.pg.hcMf))
			}
		default:
			t.Fatalf("bad script op %q at %d", op, p-1)
		}
	}
	return out
}

// hcData is a random input for the matchfinder scripts: structured data,
// tiny alphabets (long hash chains: every 4-byte group recurs, so the
// depth limit decides), random bytes, or "records" sharing a 4-byte prefix
// with tails that agree for random lengths (the longest match sits at a
// random chain depth, its length around the nice lengths).
func hcData(r *rand.Rand, n int) []byte {
	switch r.IntN(5) {
	case 0, 1:
		return structuredInput(r.Uint64(), n)
	case 2:
		k := 1 + r.IntN(4)
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a' + byte(r.IntN(k))
		}
		return b
	case 3:
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		return b
	default:
		return hcRecords(r, n, 4+r.IntN(30))
	}
}

// hcRecords: records "WXYZ" + 40-byte tail; each new tail copies the
// first L bytes of the tail of the record d records back (d < maxDepth,
// L around 3..40 incl. 5/6/9/10/11/13/14/15/29/30/31) and is random after.
func hcRecords(r *rand.Rand, n, maxDepth int) []byte {
	const tail = 40
	b := make([]byte, 0, n+tail+4)
	var starts []int
	lens := []int{1, 2, 3, 5, 6, 7, 9, 10, 11, 13, 14, 15, 26, 27, 29, 30, 31, 40}
	for len(b) < n {
		starts = append(starts, len(b))
		b = append(b, 'W', 'X', 'Y', 'Z')
		l := 0
		if len(starts) > 1 {
			d := 1 + r.IntN(min(maxDepth, len(starts)-1))
			src := starts[len(starts)-1-d] + 4
			l = lens[r.IntN(len(lens))]
			b = append(b, b[src:src+l]...)
		}
		for k := l; k < tail; k++ {
			b = append(b, byte(r.Uint32()))
		}
	}
	return b[:n]
}

// hcScript builds one dump-hcmf script: a buffer, then a walk over its
// positions in order like a parser's (each position either searched by
// 'f' with random best_len / max_len / nice_len / depth, or covered by a
// 'k' after a search), state hashes along the way, and sometimes whole
// level 2..4 compressions of the buffer.
func hcScript(r *rand.Rand, short bool) []byte {
	var n int
	switch k := r.IntN(100); {
	case k < 90:
		n = 6 + r.IntN(2000)
	case k < 99:
		n = 2000 + r.IntN(18000)
	default:
		n = 33000 + r.IntN(70000)
	}
	if short && n > 40000 {
		n = 40000
	}
	return hcScriptFor(r, hcData(r, n))
}

// hcScriptFor builds the dump-hcmf script of hcScript for the buffer data.
func hcScriptFor(r *rand.Rand, data []byte) []byte {
	n := len(data)
	var b []byte
	u32 := func(vs ...uint32) {
		for _, v := range vs {
			b = binary.LittleEndian.AppendUint32(b, v)
		}
	}
	b = append(b, 'b')
	u32(uint32(n))
	b = append(b, data...)
	depths := []uint32{1, 2, 5, 6, 7, 11, 12, 13, 15, 16, 17, 35, 100}
	nices := []uint32{3, 4, 5, 9, 10, 11, 13, 14, 15, 29, 30, 31, 65, 258}
	jumped := false
	for p := 0; p < n; {
		rem := n - p
		maxLen := uint32(min(rem, deflateMaxMatchLen))
		if r.IntN(10) == 0 {
			maxLen = uint32(r.IntN(int(maxLen) + 1))
		}
		niceLen := min(nices[r.IntN(len(nices))], maxLen)
		if r.IntN(8) == 0 {
			niceLen = uint32(r.IntN(int(maxLen) + 1))
		}
		bestLen := uint32(r.IntN(9))
		if r.IntN(20) == 0 {
			bestLen = uint32(r.IntN(260))
		}
		b = append(b, 'f')
		u32(uint32(p), bestLen, maxLen, niceLen, depths[r.IntN(len(depths))])
		p++
		if p < n && !jumped && r.IntN(3) == 0 {
			count := 1 + r.IntN(min(n-p, [3]int{3, 40, 258}[r.IntN(3)]))
			jumped = count+5 > n-p // skip_bytes returns early: the rest is < 5 bytes
			b = append(b, 'k')
			u32(uint32(p), uint32(count))
			p += count
		}
		if r.IntN(500) == 0 {
			b = append(b, 'h')
		}
	}
	b = append(b, 'h')
	if n > 51 && r.IntN(20) == 0 {
		b = append(b, 'c', byte(2+r.IntN(3)), 0)
	}
	return b
}

// hc_matchfinder.go vs libdeflate's hc_matchfinder (ldref dump-hcmf) on
// 20000 random scripts (-short: 2000): every longest_match result (length
// and offset) and the full matchfinder state (tables, in_base,
// next_hashes) after each script and at random points.
func TestHcMatchfinderMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	n := 20000
	if testing.Short() {
		n = 2000
	}
	r := rand.New(rand.NewPCG(24, 7))
	var all []byte
	var starts []int
	flush := func() {
		want := creftest.LDRun(t, all, "dump-hcmf")
		if got := runHcmfScript(t, all); !bytes.Equal(got, want) {
			for i, s := range starts {
				e := len(all)
				if i+1 < len(starts) {
					e = starts[i+1]
				}
				w := creftest.LDRun(t, all[s:e], "dump-hcmf")
				if g := runHcmfScript(t, all[s:e]); !bytes.Equal(g, w) {
					d := firstByteDiff(g, w)
					t.Fatalf("script %d (%d bytes) differs at output byte %d: go % x, libdeflate % x", i, e-s, d,
						g[d:min(len(g), d+8)], w[d:min(len(w), d+8)])
				}
			}
			t.Fatal("outputs differ but no single script does")
		}
		all, starts = all[:0], starts[:0]
	}
	for range n {
		starts = append(starts, len(all))
		all = append(all, hcScript(r, testing.Short())...)
		if len(all) > 16<<20 {
			flush()
		}
	}
	flush()
}

// The final hc_matchfinder state after a whole Compress at levels 2, 3, 4
// equals libdeflate's c->p.g.hc_mf (dump-hcmf 'c' with full tables).
func TestHcMatchfinderStateMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	inputs := [][]byte{structuredInput(1, 52), structuredInput(2, 100000), hcRecords(rand.New(rand.NewPCG(1, 2)), 70000, 20)}
	for _, n := range []int{32767, 32768, 32769, 65536 + 3} {
		inputs = append(inputs, structuredInput(uint64(n), n))
	}
	for _, in := range inputs {
		for _, level := range []int{2, 3, 4} {
			var s []byte
			s = append(s, 'b')
			s = binary.LittleEndian.AppendUint32(s, uint32(len(in)))
			s = append(s, in...)
			s = append(s, 'c', byte(level), 1)
			want := creftest.LDRun(t, s, "dump-hcmf")
			got := runHcmfScript(t, s)
			if len(want) != 2*(len(hcMatchfinder{}.hash3Tab)+len(hcMatchfinder{}.hash4Tab)+matchfinderWindowSize) {
				t.Fatalf("dump-hcmf gave %d bytes", len(want))
			}
			if i := firstByteDiff(got, want); i != len(want) {
				t.Fatalf("level %d, %d bytes: table entry %d differs", level, len(in), i/2)
			}
		}
	}
}

// hcWalk feeds in[from:] positions to longestMatch one by one (as
// literals: no skipping) with the given parameters, from a fresh
// matchfinder, and returns the result at position at.
func hcWalk(in []byte, at int, bestLen, niceLen, depth uint32) (uint32, uint32, *hcMatchfinder) {
	mf := new(hcMatchfinder)
	mf.init()
	base := 0
	var nh [2]uint32
	var l, off uint32
	for p := 0; p <= at; p++ {
		maxLen := uint32(min(len(in)-p, deflateMaxMatchLen))
		l, off = mf.longestMatch(in, &base, p, bestLen, maxLen, min(niceLen, maxLen), depth, &nh)
	}
	return l, off, mf
}

// Every crafted input starts with a filler byte: position 0 is always
// inserted under hash 0 (next_hashes starts as {0, 0}, as in C), so a
// sequence at position 0 is never found through its own hash.
func TestHcLongestMatchCrafted(t *testing.T) {
	// Length 3 match through hash3, none of length 4.
	in := []byte("#abcXYZWVUabcQ......")
	if l, off, _ := hcWalk(in, 10, 2, 258, 16); l != 3 || off != 9 {
		t.Errorf("len3: %d/%d", l, off)
	}
	// best_len 3 (min_len 4): the hash3 candidate is not used.
	if l, off, _ := hcWalk(in, 10, 3, 258, 16); l != 3 || off != 0 {
		t.Errorf("len3 not wanted: %d/%d", l, off)
	}
	// A length 4+ match beats the length 3 one.
	in = []byte("#abcdefXYabcQZZabcdefgh.....")
	if l, off, _ := hcWalk(in, 15, 2, 258, 16); l != 6 || off != 14 {
		t.Errorf("len6: %d/%d", l, off)
	}

	// Chain depth: n records "WXYZ" + tail; the record at depth d (d
	// records back) shares 12 tail bytes, the others 0.
	for _, d := range []int{1, 5, 6, 7, 12, 13} {
		b := []byte{'#'}
		r := rand.New(rand.NewPCG(uint64(d), 3))
		var starts []int
		for i := 0; i < 20; i++ {
			starts = append(starts, len(b))
			b = append(b, 'W', 'X', 'Y', 'Z')
			for range 20 {
				b = append(b, byte(r.Uint32()))
			}
		}
		at := len(b)
		b = append(b, 'W', 'X', 'Y', 'Z')
		src := starts[len(starts)-d] + 4
		b = append(b, b[src:src+12]...)
		b = append(b, bytes.Repeat([]byte{0xEE}, 30)...)
		for _, depth := range []uint32{uint32(d) - 1, uint32(d)} {
			if depth == 0 {
				continue
			}
			l, off, _ := hcWalk(b, at, 2, 258, depth)
			if wantL := map[bool]uint32{true: 16, false: 4}[depth >= uint32(d)]; l != wantL {
				t.Errorf("record depth %d, max_search_depth %d: length %d (offset %d), want %d", d, depth, l, off, wantL)
			}
		}
	}

	// nice_len: the nearer candidate is 10 bytes long, a deeper one 20:
	// nice 10 stops at the nearer one, nice 11 finds the longer one.
	far := []byte("WXYZ0123456789abcdefghij")
	near := []byte("WXYZ012345--------------")
	b := append(append(append([]byte("#"), far...), near...), []byte("WXYZ0123456789abcdefgh!!!!!!!!")...)
	at := 1 + len(far) + len(near)
	if l, off, _ := hcWalk(b, at, 2, 10, 16); l != 10 || off != uint32(len(near)) {
		t.Errorf("nice 10: %d/%d", l, off)
	}
	if l, off, _ := hcWalk(b, at, 2, 11, 16); l != 22 || off != uint32(at-1) {
		t.Errorf("nice 11: %d/%d", l, off)
	}
	// best_len >= 4 on entry (min_len 5..9): only longer matches count;
	// best_len >= nice_len searches nothing.
	if l, off, _ := hcWalk(b, at, 10, 258, 16); l != 22 || off != uint32(at-1) {
		t.Errorf("best 10: %d/%d", l, off)
	}
	if l, off, _ := hcWalk(b, at, 22, 258, 16); l != 22 || off != 0 {
		t.Errorf("best 22: %d/%d", l, off)
	}
	if l, off, _ := hcWalk(b, at, 12, 12, 16); l != 12 || off != 0 {
		t.Errorf("best 12 = nice: %d/%d", l, off)
	}

	// max_len < 5: nothing searched, inserted or hashed.
	mf := new(hcMatchfinder)
	mf.init()
	before := *mf
	base := 0
	nh := [2]uint32{7, 9}
	if l, off := mf.longestMatch([]byte("aaaa"), &base, 0, 2, 4, 4, 16, &nh); l != 2 || off != 0 || *mf != before ||
		nh != [2]uint32{7, 9} {
		t.Errorf("max_len 4: %d/%d, state changed %v, hashes %v", l, off, *mf != before, nh)
	}
}

// The window: a match at distance 32767 is found, at 32768 not (it is at
// the cutoff); the slide happens when the position is a full window past
// the base, in longestMatch and in skipBytes.
func TestHcWindow(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	for _, dist := range []int{32767, 32768} {
		b := make([]byte, 40000)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		at := 33000
		copy(b[at:at+20], b[at-dist:])
		mf := new(hcMatchfinder)
		mf.init()
		base := 0
		var nh [2]uint32
		var l, off uint32
		for p := 0; p <= at; p++ {
			l, off = mf.longestMatch(b, &base, p, 2, 258, 258, 16, &nh)
		}
		if base != matchfinderWindowSize {
			t.Fatalf("base %d after %d positions", base, at)
		}
		want := map[int]uint32{32767: 20, 32768: 2}[dist]
		if l != want || (want == 20 && off != uint32(dist)) {
			t.Errorf("distance %d: length %d offset %d", dist, l, off)
		}
	}

	// skipBytes slides at position 32768 inside its loop.
	b := make([]byte, 70000)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	mf := new(hcMatchfinder)
	mf.init()
	base := 0
	var nh [2]uint32
	mf.longestMatch(b, &base, 0, 2, 258, 258, 16, &nh)
	mf.skipBytes(b, &base, 1, len(b), 32767, &nh)
	if base != 0 {
		t.Fatalf("slid early: base %d", base)
	}
	mf.skipBytes(b, &base, 32768, len(b), 1, &nh)
	if base != matchfinderWindowSize || mf.hash4Tab[lzHash(loadLE32(b, 32768), 16)] != 0 {
		t.Fatalf("skip at 32768: base %d", base)
	}
	// Near the end skipBytes does nothing.
	before := *mf
	nh0 := nh
	mf.skipBytes(b, &base, len(b)-10, len(b), 6, &nh)
	if *mf != before || nh != nh0 {
		t.Fatal("skipBytes near the end changed the state")
	}
	mf.skipBytes(b, &base, len(b)-10, len(b), 5, &nh)
	if *mf == before {
		t.Fatal("skipBytes with exactly count + 5 bytes left did nothing")
	}
}

func TestHcInitSlide(t *testing.T) {
	mf := new(hcMatchfinder)
	for i := range mf.nextTab {
		mf.nextTab[i] = 123
	}
	mf.init()
	for i, v := range mf.hash3Tab {
		if v != matchfinderInitval {
			t.Fatalf("hash3Tab[%d] = %d", i, v)
		}
	}
	for i, v := range mf.hash4Tab {
		if v != matchfinderInitval {
			t.Fatalf("hash4Tab[%d] = %d", i, v)
		}
	}
	if mf.nextTab[5] != 123 {
		t.Fatal("init touched next_tab (C does not)")
	}
	mf.hash3Tab[1], mf.hash4Tab[2], mf.nextTab[3] = 32767, 0, -5
	mf.slideWindow()
	if mf.hash3Tab[1] != -1 || mf.hash4Tab[2] != -32768 || mf.nextTab[3] != -32768 || mf.nextTab[5] != 123-32768 ||
		mf.hash3Tab[0] != -32768 {
		t.Fatalf("slide: %d %d %d %d", mf.hash3Tab[1], mf.hash4Tab[2], mf.nextTab[3], mf.nextTab[5])
	}
	if s := fmt.Sprint(hcMatchfinderTotalHashSize); s != "196608" {
		t.Fatalf("total hash size %s", s)
	}
}

// pos0Hash4ZeroInput builds the input that makes C's hash3 empty-slot early
// return observable (hc_matchfinder.h: `if (cur_node3 <= cutoff) goto out`,
// which also skips the length 4+ search). Position 0 is inserted into
// bucket 0 of both hash tables (next_hashes start as {0, 0}). With a first
// 4-byte group v whose hash4 is 0 but whose hash3 is not, a later copy of
// it finds node 0 at the head of its hash4 chain while its own hash3 slot
// was never written: C returns no match there. The input is v,
// "QJ#@!~VKZ", 3000 bytes of a 200-symbol alphabet (0x20..0xE7, so > 80
// distinct literals: min_len 3 at every level), F7 F8 F9 FA, the same 13
// bytes again (at position pos0CopyAt), then 3000 more alphabet bytes. It
// returns the input and v.
func pos0Hash4ZeroInput() ([]byte, uint32) {
	var v uint32
	for v = 1; lzHash(v, hcMatchfinderHash4Order) != 0 || lzHash(v&0xFFFFFF, hcMatchfinderHash3Order) == 0; v++ {
	}
	r := rand.New(rand.NewPCG(7, 7))
	alpha := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = 0x20 + byte(r.IntN(200))
		}
		return b
	}
	head := binary.LittleEndian.AppendUint32(nil, v)
	head = append(head, "QJ#@!~VKZ"...)
	in := append(bytes.Clone(head), alpha(3000)...)
	in = append(in, 0xF7, 0xF8, 0xF9, 0xFA)
	in = append(in, head...)
	return append(in, alpha(3000)...), v
}

const pos0CopyAt = 13 + 3000 + 4

// Golden libdeflate (ldref compress -l N) outputs for pos0Hash4ZeroInput,
// levels 2..9, as "size sha256", so the test asserts without ldref; with
// ldref the bytes are compared too. (The input is one block and the parse
// is the same at every level: a 6030-byte dynamic block.)
const pos0Golden = "5826 d78d426616b9aef55d11ab94c44464fad361ebf87de1dea2988833141c72a040"

var pos0Hash4ZeroGolden = map[int]string{
	2: pos0Golden, 3: pos0Golden, 4: pos0Golden, 5: pos0Golden, 6: pos0Golden, 7: pos0Golden, 8: pos0Golden, 9: pos0Golden,
}

func sizeSum(b []byte) string { return fmt.Sprintf("%d %x", len(b), sha256.Sum256(b)) }

func TestHcPos0Hash4Zero(t *testing.T) {
	in, v := pos0Hash4ZeroInput()
	if v != 0x01002e5a || len(in) != 2*13+3000+4+3000 || !bytes.Equal(in[pos0CopyAt:pos0CopyAt+13], in[:13]) {
		t.Fatalf("construction changed: v %#x, %d bytes", v, len(in))
	}
	// Matchfinder level (C semantics, no ldref): every position searched in
	// order (no skipping) with best_len 2. At the copy, hash4 leads to node
	// 0 but the hash3 slot of v is empty: no match at all. One byte later
	// the rest of the copy (12 bytes) is found at the same distance.
	if l, off, _ := hcWalk(in, pos0CopyAt, 2, 258, 16); l != 2 || off != 0 {
		t.Errorf("at the copy of position 0: length %d offset %d, want no match (C's hash3 early return)", l, off)
	}
	if l, off, _ := hcWalk(in, pos0CopyAt+1, 2, 258, 16); l != 12 || off != pos0CopyAt {
		t.Errorf("one byte later: length %d offset %d, want 12 at %d", l, off, pos0CopyAt)
	}
	for level := 2; level <= 9; level++ {
		got := compressAt(t, mustCompressor(t, level), in)
		if err := checkDecode(t, got, in); err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if g, w := sizeSum(got), pos0Hash4ZeroGolden[level]; g != w {
			t.Errorf("level %d: %s, libdeflate's golden %q", level, g, w)
		}
	}
	// The same against ldref itself (skips without it locally, fails under CI).
	t.Run("ldref", func(t *testing.T) {
		creftest.LDBin(t)
		for level := 2; level <= 9; level++ {
			got := compressAt(t, mustCompressor(t, level), in)
			want := ldCompressBatch(t, []ldBatchRec{{level, -1, in}})[0]
			if !bytes.Equal(got, want) {
				t.Errorf("level %d: go %d bytes, libdeflate %d; %s", level, len(got), len(want), firstBlockDiff(got, want))
			}
			t.Logf("%d: %q,", level, sizeSum(want))
		}
	})
}
