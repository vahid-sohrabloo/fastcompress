package zstd

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"sort"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// compress2Case is one ZSTD_compress2 parameter set.
type compress2Case struct {
	level     int
	checksum  bool
	windowLog uint32 // 0 = level default
}

func (c compress2Case) String() string {
	s := "L" + strconv.Itoa(c.level)
	if c.checksum {
		s += "/c1"
	} else {
		s += "/c0"
	}
	if c.windowLog != 0 {
		s += "/w" + strconv.Itoa(int(c.windowLog))
	}
	return s
}

func (c compress2Case) crefArgs(mode string) []string {
	args := []string{mode, "-level", strconv.Itoa(c.level), "-checksum", "0", "-pledged", "1"}
	if c.checksum {
		args[4] = "1"
	}
	if c.windowLog != 0 {
		args = append(args, "-window-log", strconv.Itoa(int(c.windowLog)))
	}
	return args
}

func (c compress2Case) newCCtx(t *testing.T) *cctx {
	t.Helper()
	cc := newCCtx()
	cc.setLevel(c.level)
	cc.setChecksum(c.checksum)
	if err := cc.setWindowLog(c.windowLog); err != nil {
		t.Fatal(err)
	}
	return cc
}

func level1Cases() []compress2Case {
	var cases []compress2Case
	for _, lvl := range []int{1, 3} {
		for _, wl := range []uint32{0, 10, 17} {
			for _, cs := range []bool{false, true} {
				cases = append(cases, compress2Case{level: lvl, checksum: cs, windowLog: wl})
			}
		}
	}
	return cases
}

func sortedInputs(t *testing.T) ([]string, map[string][]byte) {
	inputs := creftest.Inputs(t)
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, inputs
}

// TestCompress2Cref requires cctx.compress2 to produce exactly the frame of
// C's ZSTD_compress2 at level 1, with and without checksum and with window
// logs 10 and 17, for every input, including multi-block ones that C
// pre-splits (inputs over 128 KiB).
func TestCompress2Cref(t *testing.T) {
	names, inputs := sortedInputs(t)
	for _, c := range level1Cases() {
		for _, name := range names {
			src := inputs[name]
			t.Run(c.String()+"/"+name, func(t *testing.T) {
				t.Parallel()
				cc := c.newCCtx(t)
				got, err := cc.compress2(nil, src)
				if err != nil {
					t.Fatalf("compress2: %v", err)
				}
				// A second frame from the same context must be identical.
				again, err := cc.compress2([]byte("prefix"), src)
				if err != nil {
					t.Fatalf("compress2 (reuse): %v", err)
				}
				if string(again[:6]) != "prefix" || !bytes.Equal(again[6:], got) {
					t.Fatalf("reused context produced a different frame")
				}
				want := creftest.Run(t, src, c.crefArgs("compress")...)
				if !bytes.Equal(got, want) {
					t.Fatalf("frame differs from C (Go %d bytes, C %d bytes)%s", len(got), len(want), firstDiff(got, want))
				}
			})
		}
	}
}

// TestCompress2Reuse compresses every single-block input with ONE context,
// alternating parameters, and requires each frame to equal C's from a fresh
// context: reusing a context (index continuation, tables not cleared) must
// not change the output.
func TestCompress2Reuse(t *testing.T) {
	names, inputs := sortedInputs(t)
	cases := level1Cases()
	cc := newCCtx()
	for i, name := range names {
		src := inputs[name]
		if len(src) > 128<<10 {
			continue
		}
		c := cases[i%len(cases)]
		cc.setLevel(c.level)
		cc.setChecksum(c.checksum)
		if err := cc.setWindowLog(c.windowLog); err != nil {
			t.Fatal(err)
		}
		got, err := cc.compress2(nil, src)
		if err != nil {
			t.Fatalf("%s/%s: compress2: %v", c, name, err)
		}
		want := creftest.Run(t, src, c.crefArgs("compress")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s/%s: frame differs from C (Go %d bytes, C %d bytes)%s", c, name, len(got), len(want), firstDiff(got, want))
		}
	}
}

// TestGetCParams checks parameter selection against clevels.h rows and the
// ZSTD_adjustCParams_internal downsizing for known source sizes.
func TestGetCParams(t *testing.T) {
	for _, tc := range []struct {
		level int
		size  uint64
		want  compressionParameters
	}{
		// > 256 KiB: table 0 row 1 unchanged.
		{1, 1 << 20, compressionParameters{19, 13, 14, 1, 7, 0, zstdFast}},
		// 128 KiB: table 2 row 1; windowLog 17 already fits.
		{1, 128 << 10, compressionParameters{17, 12, 13, 1, 6, 0, zstdFast}},
		// 100 bytes: table 3 row 1; windowLog -> srcLog 7 -> ABSOLUTEMIN 10
		// is applied after hashLog (8) and chainLog (7) were cut.
		{1, 100, compressionParameters{10, 7, 8, 1, 5, 0, zstdFast}},
		// 0 bytes means 0: HASHLOG_MIN srcLog 6.
		{1, 0, compressionParameters{10, 6, 7, 1, 5, 0, zstdFast}},
		// Level 11, 16 KiB: btopt (btScale reduces the cycle log).
		{11, 16 << 10, compressionParameters{14, 15, 14, 3, 4, 12, zstdBtopt}},
		// Unknown size: no downsizing.
		{3, contentSizeUnknown, compressionParameters{21, 16, 17, 1, 5, 0, zstdDfast}},
	} {
		if got := getCParams(tc.level, tc.size, 0); got != tc.want {
			t.Errorf("getCParams(%d, %d) = %+v, want %+v", tc.level, tc.size, got, tc.want)
		}
	}
}

// TestHashPtr checks the C hash functions and the branch-free form the fast
// strategy uses.
func TestHashPtr(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, 64)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	for mls := uint32(4); mls <= 8; mls++ {
		for hBits := uint32(6); hBits <= 30; hBits++ {
			for i := 0; i+8 <= len(b); i++ {
				var want uint32
				u := binary.LittleEndian.Uint64(b[i:])
				switch mls {
				case 4:
					want = (uint32(u) * 2654435761) >> (32 - hBits)
				case 8:
					want = uint32((u * 0xCF1BBCDCB7A56463) >> (64 - hBits))
				default:
					p := map[uint32]uint64{5: 889523592379, 6: 227718039650203, 7: 58295818150454627}[mls]
					want = uint32(((u << (64 - 8*mls)) * p) >> (64 - hBits))
				}
				if got := hashPtr(b, i, hBits, mls); got != want {
					t.Fatalf("hashPtr(mls=%d, hBits=%d) = %d, want %d", mls, hBits, got, want)
				}
				if mls <= 7 {
					sh, prime := fastHashParams(mls)
					if got := uint32(((u << sh) * prime) >> (64 - hBits)); got != want {
						t.Fatalf("fast hash (mls=%d, hBits=%d) = %d, want %d", mls, hBits, got, want)
					}
				}
			}
		}
	}
}

func TestCount(t *testing.T) {
	b := []byte("abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz012345678X")
	for iEnd := 36; iEnd <= len(b); iEnd++ {
		want := 0
		for 36+want < iEnd && b[36+want] == b[want] {
			want++
		}
		if got := count(b, 36, 0, iEnd); got != want {
			t.Fatalf("count(iEnd=%d) = %d, want %d", iEnd, got, want)
		}
	}
}

// TestCompress2IndexResetTables shrinks the hash table across an index
// reset and grows it again: entries beyond the shrunk length still hold
// pre-reset indices, which lie ahead of the new window unless growing the
// table zeroes them (C: ZSTD_cwksp cleans tables beyond tableValidEnd).
func TestCompress2IndexResetTables(t *testing.T) {
	_, inputs := sortedInputs(t)
	big := inputs["text16KiB"] // level 1: hashLog 15
	small := inputs["text16KiB"][:1024]
	// Level 3 (dfast) owns two tables (hash and chain), both resized.
	for _, lvl := range []int{1, 3} {
		c := compress2Case{level: lvl}
		cc := c.newCCtx(t)
		if _, err := cc.compress2(nil, big); err != nil {
			t.Fatal(err)
		}
		// Force ZSTD_indexTooCloseToMax: the next frame restarts the indices.
		cc.blockState.matchState.window.nextSrc = currentMax
		if _, err := cc.compress2(nil, small); err != nil { // hashLog 11
			t.Fatal(err)
		}
		got, err := cc.compress2(nil, big)
		if err != nil {
			t.Fatal(err)
		}
		if want := creftest.Run(t, big, c.crefArgs("compress")...); !bytes.Equal(got, want) {
			t.Fatalf("%s: frame after index reset differs from C (Go %d bytes, C %d bytes)%s", c, len(got), len(want), firstDiff(got, want))
		}
	}
}

// TestCompress2ReuseMultiBlock is TestCompress2Reuse for inputs larger
// than one block, at window log 10 (1 KiB blocks, never pre-split by C) and
// at the default window log (pre-split blocks).
func TestCompress2ReuseMultiBlock(t *testing.T) {
	names, inputs := sortedInputs(t)
	cc := newCCtx()
	for i, name := range names {
		src := inputs[name]
		if len(src) <= 128<<10 {
			continue
		}
		c := compress2Case{level: 1 + 2*(i/2%2), checksum: i%2 == 1, windowLog: 10}
		if i%3 == 0 {
			c.windowLog = 0 // default window log: C pre-splits full blocks
		}
		cc.setLevel(c.level)
		cc.setChecksum(c.checksum)
		if err := cc.setWindowLog(c.windowLog); err != nil {
			t.Fatal(err)
		}
		got, err := cc.compress2(nil, src)
		if err != nil {
			t.Fatalf("%s/%s: compress2: %v", c, name, err)
		}
		want := creftest.Run(t, src, c.crefArgs("compress")...)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s/%s: frame differs from C (Go %d bytes, C %d bytes)%s", c, name, len(got), len(want), firstDiff(got, want))
		}
	}
}
