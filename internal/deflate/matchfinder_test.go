package deflate

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func TestLzHashGolden(t *testing.T) {
	// (seq * 0x1E35A7BD) mod 2^32 >> (32 - bits), by hand: 1 -> 0x1E35A7BD
	// >> 17 = 0xF1A; 2^31 -> (odd << 31) = 0x80000000 >> 17 = 0x4000;
	// 0xFFFFFFFF -> -0x1E35A7BD = 0xE1CA5843 >> 17 = 0x70E5.
	for _, tc := range []struct {
		seq  uint32
		bits uint
		want uint32
	}{
		{0, 15, 0}, {1, 15, 3866}, {1, 16, 7733}, {0x80000000, 15, 16384}, {0x80000000, 16, 32768},
		{0xFFFFFFFF, 15, 28901}, {0xFFFFFFFF, 16, 57802}, {0x64636261, 15, 22352}, {0x12345678, 15, 17906},
		{0x12345678, 16, 35812}, {0x12345678, 32, 0x8be41e98},
	} {
		if got := lzHash(tc.seq, tc.bits); got != tc.want {
			t.Errorf("lzHash(%#x, %d) = %d, want %d", tc.seq, tc.bits, got, tc.want)
		}
	}
	if got := loadLE32([]byte("xabcd"), 1); got != 0x64636261 {
		t.Errorf("loadLE32 = %#x", got)
	}
	// What ht_matchfinder hashes: the 4 bytes "abcd".
	if got := lzHash(loadLE32([]byte("abcd"), 0), htMatchfinderHashOrder); got != 22352 {
		t.Errorf("hash of abcd = %d", got)
	}
}

func TestMatchfinderInitRebase(t *testing.T) {
	if matchfinderWindowSize != 32768 || matchfinderInitval != -32768 || len(htMatchfinder{}.hashTab) != 65536 {
		t.Fatal("window / initval / table size")
	}
	data := make([]mfPos, 1024)
	for i := range data {
		data[i] = mfPos(i)
	}
	matchfinderInit(data)
	for i, v := range data {
		if v != -32768 {
			t.Fatalf("init: data[%d] = %d", i, v)
		}
	}
	// Subtract 32768 with signed saturation: non-negative values move one
	// window down, negative ones (already a full window or more back after
	// the subtraction) saturate at -32768.
	in := []mfPos{-32768, -32767, -1, 0, 1, 2, 16384, 32766, 32767}
	want := []mfPos{-32768, -32768, -32768, -32768, -32767, -32766, -16384, -2, -1}
	matchfinderRebase(in)
	for i := range in {
		if in[i] != want[i] {
			t.Errorf("rebase: entry %d = %d, want %d", i, in[i], want[i])
		}
	}
	// Exhaustively against the saturating definition.
	all := make([]mfPos, 65536)
	for i := range all {
		all[i] = mfPos(i - 32768)
	}
	matchfinderRebase(all)
	for i, v := range all {
		w := max(i-32768-32768, -32768)
		if int(v) != w {
			t.Fatalf("rebase(%d) = %d, want %d", i-32768, v, w)
		}
	}
	var mf htMatchfinder
	mf.init()
	mf.hashTab[5] = 100
	mf.slideWindow()
	if mf.hashTab[5] != 100-32768 || mf.hashTab[4] != -32768 || mf.hashTab[65535] != -32768 {
		t.Errorf("slideWindow: %d %d %d", mf.hashTab[5], mf.hashTab[4], mf.hashTab[65535])
	}
	if b := mf.bucket(2); &b[0] != &mf.hashTab[4] || &b[1] != &mf.hashTab[5] {
		t.Error("bucket(2) is not hash_tab[2] = entries 4, 5")
	}
	if b := mf.bucket(1<<15 - 1); &b[1] != &mf.hashTab[65535] {
		t.Error("last bucket")
	}
}

func naiveExtend(in []byte, s, m int, start, maxLen uint32) uint32 {
	n := start
	for n < maxLen && in[s+int(n)] == in[m+int(n)] {
		n++
	}
	return n
}

func TestLzExtend(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	src := make([]byte, 600)
	for i := range src {
		src[i] = byte(r.Uint32())
	}
	for trial := range 4000 {
		in := bytes.Clone(src)
		d := 1 + r.IntN(300)
		s := d + r.IntN(20)
		maxLen := uint32(4 + r.IntN(255))
		same := r.IntN(int(maxLen) + 1) // in[s:s+same] equals in[s-d:...]
		for k := 0; k < same; k++ {
			in[s+k] = in[s-d+k]
		}
		if same < int(maxLen) && in[s+same] == in[s-d+same] {
			in[s+same] ^= 1
		}
		start := uint32(min(same, 4))
		if same >= 4 {
			start = uint32(r.IntN(5)) // any start <= the matched length
		}
		got := lzExtend(in, s, s-d, start, maxLen)
		if want := naiveExtend(in, s, s-d, start, maxLen); got != want || got != uint32(same) {
			t.Fatalf("trial %d: lzExtend(d=%d, start %d, max %d) = %d, want %d (same %d)", trial, d, start, maxLen,
				got, want, same)
		}
	}
	// The unrolled 4-word step (max - start >= 32), the single words and
	// the byte tail: a run of equal bytes of every length.
	in := bytes.Repeat([]byte{'z'}, 600)
	for l := 4; l <= 258; l++ {
		in[300+l] = 'y'
		if got := lzExtend(in, 300, 299, 4, 258); got != uint32(l) {
			t.Fatalf("run %d: got %d", l, got)
		}
		if got := lzExtend(in, 300, 299, 4, uint32(l)); got != uint32(l) {
			t.Fatalf("run %d at max: got %d", l, got)
		}
		in[300+l] = 'z'
	}
}

// htSeq is a little helper running the ht_matchfinder over in as
// deflate_compress_fastest does (longest match at a position; skip the rest
// of a match), recording what each call returned.
type htStep struct {
	pos            int
	length, offset uint32
}

func htParse(mf *htMatchfinder, in []byte, niceLen uint32) []htStep {
	var steps []htStep
	base := 0
	nextHash := uint32(0)
	mf.init()
	for p := 0; p+htMatchfinderRequiredNbytes <= len(in); {
		maxLen := min(uint32(len(in)-p), deflateMaxMatchLen)
		l, o := mf.longestMatch(in, &base, p, maxLen, min(niceLen, maxLen), &nextHash)
		steps = append(steps, htStep{p, l, o})
		if l == 0 {
			p++
			continue
		}
		mf.skipBytes(in, &base, p+1, len(in), l-1, &nextHash)
		p += int(l)
	}
	return steps
}

// collidingWords returns n distinct 4-byte groups with one ht hash.
func collidingWords(n int) [][]byte {
	target := lzHash(0x01020304, htMatchfinderHashOrder)
	var out [][]byte
	for v := uint32(0x01020304); len(out) < n; v++ {
		if lzHash(v, htMatchfinderHashOrder) == target {
			out = append(out, []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
		}
	}
	return out
}

func TestHtLongestMatchCrafted(t *testing.T) {
	var mf htMatchfinder

	// Empty table: no match; position 0 goes to bucket 0 (C starts with
	// next_hash = 0), and next_hash becomes the hash of in[1:5].
	mf.init()
	base, nh := 0, uint32(0)
	in := []byte("abcdefgh")
	if l, o := mf.longestMatch(in, &base, 0, 8, 8, &nh); l != 0 || o != 0 {
		t.Fatalf("empty: %d %d", l, o)
	}
	if b := mf.bucket(0); b[0] != 0 || b[1] != -32768 {
		t.Fatalf("bucket 0 = %v", *b)
	}
	if nh != lzHash(loadLE32(in, 1), htMatchfinderHashOrder) {
		t.Fatal("next_hash")
	}

	// A repeat at distance 5: "abcdX" + "abcdY". (Every input starts with
	// a '-': position 0 always goes to bucket 0, see above, so it is never
	// found again.)
	steps := htParse(&mf, []byte("-abcdXabcdYzzzz"), 32)
	if steps[6] != (htStep{6, 4, 5}) || steps[1] != (htStep{1, 0, 0}) {
		t.Fatalf("repeat: %v", steps)
	}
	// Position 0 really is lost: "abcd" at 0 is not found at 5.
	for _, s := range htParse(&mf, []byte("abcdXabcdYzzzz"), 32) {
		if s.length != 0 {
			t.Fatalf("position 0 found: %v", s)
		}
	}

	// Bucket order: "abcdefQ" ... "abcdQ" ... "abcdef?": the newest entry
	// (length 4) is tried first, then the older, longer one (length 6)
	// unless nice_len was reached on the first.
	in = []byte("-abcdefQ1234abcdQ5678abcdefZ9")
	at := 21
	for _, tc := range []struct {
		nice   uint32
		length uint32
		offset uint32
	}{{32, 6, 20}, {5, 6, 20}, {4, 4, 9}} {
		var got htStep
		for _, s := range htParse(&mf, in, tc.nice) {
			if s.pos == at {
				got = s
			}
		}
		if got != (htStep{at, tc.length, tc.offset}) {
			t.Errorf("nice %d: %v, want length %d offset %d", tc.nice, got, tc.length, tc.offset)
		}
	}

	// Equal hash, different sequences: w0 w1 then w0 again. At the second
	// w0 the newest entry (w1) fails the sequence check, so the older one
	// (the else branch) is used.
	w := collidingWords(3)
	in = bytes.Join([][]byte{{'-'}, w[0], {'.'}, w[1], {'.'}, w[0], {'!', '#', '$', '%'}}, nil)
	var got htStep
	for _, s := range htParse(&mf, in, 32) {
		if s.pos == 11 {
			got = s
		}
	}
	if got != (htStep{11, 4, 10}) {
		t.Errorf("collision, else branch: %v", got)
	}
	// Both entries collide with different sequences: no match.
	in = bytes.Join([][]byte{{'-'}, w[0], {'.'}, w[1], {'.'}, w[2], {'!', '#', '$', '%'}}, nil)
	for _, s := range htParse(&mf, in, 32) {
		if s.length != 0 {
			t.Errorf("collision without equal sequence matched: %v", s)
		}
	}
	// First entry collides with the same sequence but the second entry was
	// never set (initval): one match, from the first.
	in = bytes.Join([][]byte{{'-'}, w[0], {'.', '.', '.'}, w[0], {'!', '#', '$', '%'}}, nil)
	for _, s := range htParse(&mf, in, 32) {
		if s.pos == 8 && s != (htStep{8, 4, 7}) {
			t.Errorf("single entry: %v", s)
		}
	}
	// Second candidate shares the 4-byte prefix but differs at the bytes
	// ending at best_len (the second load check) -> not taken.
	in = []byte("-abcdXX..abcdefgYY..abcdefZ")
	for _, s := range htParse(&mf, in, 32) {
		if s.pos == 20 && s != (htStep{20, 6, 11}) {
			t.Errorf("second load check: %v", s)
		}
	}
}

// The window cutoff: an entry exactly MATCHFINDER_WINDOW_SIZE back is out
// of range (cur_node <= cutoff), one less is in range; and the slide at
// cur_pos == 32768.
func TestHtLongestMatchWindow(t *testing.T) {
	var mf htMatchfinder
	in := make([]byte, 70000)
	r := rand.New(rand.NewPCG(5, 6))
	for i := range in {
		in[i] = byte(r.Uint32())
	}
	copy(in[40000:40008], in[40000-32768:])
	copy(in[50000:50008], in[50000-32767:])
	hashAt := func(p int) uint32 { return lzHash(loadLE32(in, p), htMatchfinderHashOrder) }

	for _, tc := range []struct {
		pos     int
		wantLen uint32
	}{{40000, 0}, {50000, 8}} {
		mf.init()
		src := tc.pos - int(map[int]int{40000: 32768, 50000: 32767}[tc.pos])
		base := 32768 // window base after one slide; src - base is the entry
		b := mf.bucket(hashAt(tc.pos))
		b[0] = mfPos(src - base)
		nh := hashAt(tc.pos)
		l, o := mf.longestMatch(in, &base, tc.pos, 258, 258, &nh)
		if l != tc.wantLen || (l != 0 && o != uint32(tc.pos-src)) {
			t.Errorf("pos %d: length %d offset %d, want length %d", tc.pos, l, o, tc.wantLen)
		}
	}

	// cur_pos == 32768: the table slides (entries -32768) and the base
	// moves by one window before the search.
	mf.init()
	base := 0
	copy(in[32768:32776], in[32768-100:])
	nh := hashAt(32768)
	mf.bucket(nh)[0] = 32768 - 100
	mf.hashTab[7] = 5
	l, o := mf.longestMatch(in, &base, 32768, 258, 258, &nh)
	if base != 32768 || l != 8 || o != 100 || mf.hashTab[7] != 5-32768 {
		t.Errorf("slide in longest_match: base %d length %d offset %d entry %d", base, l, o, mf.hashTab[7])
	}
	if b := mf.bucket(hashAt(32768)); b[0] != 0 || b[1] != -100 {
		t.Errorf("after slide, bucket = %v", *b)
	}
}

func TestHtSkipBytes(t *testing.T) {
	var mf htMatchfinder
	in := make([]byte, 40000)
	r := rand.New(rand.NewPCG(8, 9))
	for i := range in {
		in[i] = byte(r.Uint32())
	}
	hashAt := func(p int) uint32 { return lzHash(loadLE32(in, p), htMatchfinderHashOrder) }

	// Too close to the end: nothing happens, next_hash untouched.
	mf.init()
	base, nh := 0, uint32(12345)
	mf.skipBytes(in, &base, len(in)-10, len(in), 6, &nh)
	if nh != 12345 || base != 0 {
		t.Fatal("skip near the end changed state")
	}
	for i, v := range mf.hashTab {
		if v != -32768 {
			t.Fatalf("skip near the end wrote entry %d", i)
		}
	}
	// Exactly enough room (count + 5 == in_end - in_next): inserted.
	mf.skipBytes(in, &base, len(in)-11, len(in), 6, &nh)
	if nh != hashAt(len(in)-5) {
		t.Fatal("skip with exactly enough room")
	}

	// Positions p..p+count-1 are inserted under the hashes of their 4-byte
	// groups (the first under the incoming next_hash), shifting buckets.
	mf.init()
	base = 0
	p := 1000
	nh = hashAt(p)
	mf.bucket(hashAt(p))[0] = 77
	mf.skipBytes(in, &base, p, len(in), 10, &nh)
	if nh != hashAt(p+10) {
		t.Fatal("next_hash after skip")
	}
	if b := mf.bucket(hashAt(p)); b[0] != mfPos(p) || b[1] != 77 {
		t.Errorf("first bucket %v", *b)
	}
	for k := 1; k < 10; k++ {
		if b := mf.bucket(hashAt(p + k)); b[0] != mfPos(p+k) {
			t.Errorf("position %d not inserted: %v", p+k, *b)
		}
	}

	// The window slides when the last skipped position is >= 32768 past
	// the base: cur_pos + count - 1 >= 32768.
	for _, tc := range []struct {
		start, count int
		slide        bool
	}{{32760, 8, false}, {32760, 9, true}, {32768, 3, true}, {32767, 1, false}, {32767, 2, true}} {
		mf.init()
		base = 0
		nh = hashAt(tc.start)
		mf.skipBytes(in, &base, tc.start, len(in), uint32(tc.count), &nh)
		wantBase := 0
		if tc.slide {
			wantBase = 32768
		}
		last := tc.start + tc.count - 1
		if base != wantBase || mf.bucket(hashAt(last))[0] != mfPos(last-wantBase) {
			t.Errorf("skip %d+%d: base %d (want %d), last entry %d", tc.start, tc.count, base, wantBase,
				mf.bucket(hashAt(last))[0])
		}
	}
}
