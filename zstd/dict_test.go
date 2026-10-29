package zstd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// Test dictionaries (zstd/testdata):
//
// train.dict (112640 bytes, sha256 e9bdb330278e5323918bde0c1dc8cc0e4cef4a4365fc3f262a9f3ffc5dddf6dc)
// was trained with zstd v1.5.7 on 200 pieces of 4 KiB of the Silesia
// corpus, from the repository root:
//
//	D=$(mktemp -d)
//	for i in $(seq 0 199); do
//	  dd if=bench/corpus/silesia.tar of=$D/s$(printf %03d $i) bs=4096 skip=$((i*258)) count=1 status=none
//	done
//	ref/zstd/programs/zstd --train $D/* -o zstd/testdata/train.dict
//
// train-12k.dict (12288 bytes, sha256 1344064d77cd31542a3b965d37665f2fcc77708a7e9968302b673b41c3c6c438)
// is trained on the same samples with --maxdict=12288:
//
//	ref/zstd/programs/zstd --train --maxdict=12288 $D/* -o zstd/testdata/train-12k.dict
//
// dickens-32k.raw is `head -c 32768 bench/corpus/silesia/dickens`.

func readTestdata(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// dictCase is a dictionary as the Encoder option gives it.
type dictCase struct {
	name string
	dict []byte
	raw  bool   // WithEncoderDictRaw (cref -raw-dict 1)
	id   uint32 // raw: the dictionary ID written into the frame header
}

func (d dictCase) option() EOption {
	if d.raw {
		return WithEncoderDictRaw(d.id, d.dict)
	}
	return WithEncoderDict(d.dict)
}

// crefArgs returns the cref options loading the dictionary from a file.
func (d dictCase) crefArgs(t testing.TB) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dict")
	if err := os.WriteFile(p, d.dict, 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"-dict", p}
	if d.raw {
		args = append(args, "-raw-dict", "1")
	}
	return args
}

// testDicts are the dictionaries of TestDictCref: the trained ones and the
// first 32 KiB and 8 KiB of dickens as raw content. Their CDicts use the
// <= 128 KiB parameters (train, raw32k: fast, dfast, row lazy2, btlazy2 at
// levels 1, 3, 7, 11) or the <= 16 KiB ones (train12k, raw8k: hash-chain
// lazy2 at level 7, btopt at level 11). The trained dictionaries' Huffman
// tables cover every byte value, so their first blocks reuse them
// (HUF_repeat_valid) and btopt prices from them.
func testDicts(t testing.TB) []dictCase {
	raw := readTestdata(t, "dickens-32k.raw")
	return []dictCase{
		{name: "train", dict: readTestdata(t, "train.dict")},
		{name: "train12k", dict: readTestdata(t, "train-12k.dict")},
		{name: "raw32k", dict: raw, raw: true, id: 300},
		{name: "raw8k", dict: raw[:8<<10], raw: true, id: 0x12345678},
	}
}

// stripDictID returns frame without the dictionary ID field of its header
// (and its flag), and the ID. C cannot write an ID for raw content (C's
// rawContent dictionaries have ID 0) while WithEncoderDictRaw writes the
// given one; everything after the field must still equal C's frame.
func stripDictID(t testing.TB, frame []byte) ([]byte, uint32) {
	t.Helper()
	if len(frame) < 6 || binary.LittleEndian.Uint32(frame) != magicNumber {
		t.Fatalf("not a frame: %x", frame[:min(len(frame), 8)])
	}
	fhd := frame[4]
	pos := 5
	if fhd&(1<<5) == 0 { // no single segment: window descriptor
		pos++
	}
	n := [4]int{0, 1, 2, 4}[fhd&3]
	var id uint32
	for i := n - 1; i >= 0; i-- {
		id = id<<8 | uint32(frame[pos+i])
	}
	out := slices.Concat(frame[:4], []byte{fhd &^ 3}, frame[5:pos], frame[pos+n:])
	return out, id
}

// sameFrames checks Go's frames against C's: equal for a structured or
// ID-0 raw dictionary, equal after stripping the ID otherwise.
func sameFrames(t testing.TB, d dictCase, got, want []byte) {
	t.Helper()
	if d.raw && d.id != 0 && len(got) > 0 {
		g, id := stripDictID(t, got)
		if id != d.id {
			t.Fatalf("dictionary ID %d, want %d", id, d.id)
		}
		got = g
	}
	if !bytes.Equal(got, want) {
		at := 0
		for at < min(len(got), len(want)) && got[at] == want[at] {
			at++
		}
		t.Fatalf("frame differs from C at byte %d (len %d, C %d)", at, len(got), len(want))
	}
}

// dictInputs returns the inputs of TestDictCref, in order: 100 B, 4 KiB,
// 64 KiB and 1 MiB of text (from the Silesia tar when present, outside the
// training samples, else the zstd format spec), sizes either side of the
// attach cutoffs (8, 16 and 32 KiB) and creftest.Inputs without the
// silesia files.
func dictInputs(t *testing.T) ([]string, map[string][]byte) {
	t.Helper()
	m := map[string][]byte{}
	var names []string
	add := func(name string, b []byte) {
		if _, dup := m[name]; !dup {
			names = append(names, name)
		}
		m[name] = b
	}
	var text []byte
	if b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia.tar")); err == nil {
		text = b[150<<20 : 150<<20+1<<20+1]
	} else {
		md := creftest.Inputs(t)["zstd-format.md"]
		for len(text) <= 1<<20 {
			text = append(text, md...)
		}
	}
	for _, n := range []int{100, 4 << 10, 64 << 10, 1 << 20, 8 << 10, 8<<10 + 1, 16 << 10, 16<<10 + 1, 32 << 10, 32<<10 + 1} {
		add("text"+strconv.Itoa(n), text[len(text)-n:])
	}
	edge := creftest.Inputs(t)
	var keys []string
	for k := range edge {
		if !strings.HasPrefix(k, "silesia/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		add(k, edge[k])
	}
	return names, m
}

// dictRuns records which CDict paths ran (attach or copy, per strategy)
// and which dictMatchState compressors ran, through the test hooks.
type dictRuns struct {
	mu   sync.Mutex
	seen map[string]int
}

func (r *dictRuns) install(t *testing.T) {
	r.seen = map[string]int{}
	note := func(s string) {
		r.mu.Lock()
		r.seen[s]++
		r.mu.Unlock()
	}
	cdictHook = func(strat strategy, attach bool) {
		how := "copy"
		if attach {
			how = "attach"
		}
		note(how + "/" + strconv.Itoa(int(strat)))
	}
	dictMatchStateHook = func(name string) { note("dms/" + name) }
	t.Cleanup(func() { cdictHook, dictMatchStateHook = nil, nil })
}

func (r *dictRuns) require(t *testing.T, keys ...string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range keys {
		if r.seen[k] == 0 {
			t.Errorf("%s never ran (seen: %v)", k, r.seen)
		}
	}
}

// TestDictCref checks EncodeAll and the Writer with a dictionary against C
// (ZSTD_CCtx_loadDictionary_advanced, ZSTD_dlm_byCopy, ZSTD_dct_auto or
// ZSTD_dct_rawContent) at levels 1, 3, 7 and 11. For each level and
// dictionary one Encoder serves every input, so the dictionary is attached
// or copied again for each frame of a reused context: EncodeAll is compared
// with `cref compress-seq` (one reused ZSTD_CCtx, ZSTD_compress2 per input),
// the Writer with `cref stream-pipe` (unknown size: always attached) and,
// with ResetContentSize, `cref stream-pipe -pledged-size` (copied above the
// cutoff).
func TestDictCref(t *testing.T) {
	names, inputs := dictInputs(t)
	var runs dictRuns
	runs.install(t)
	for _, d := range testDicts(t) {
		dargs := d.crefArgs(t)
		for _, lv := range encoderLevels {
			t.Run(d.name+"/L"+strconv.Itoa(lv.c), func(t *testing.T) {
				t.Parallel()
				e := newTestEncoder(t, WithEncoderLevel(lv.l), d.option())

				// EncodeAll, one reused context, vs compress-seq.
				var all []byte
				var sizes []string
				var got [][]byte
				for _, name := range names {
					src := inputs[name]
					all = append(all, src...)
					sizes = append(sizes, strconv.Itoa(len(src)))
					got = append(got, e.EncodeAll(src, nil))
				}
				out := creftest.Run(t, all, append([]string{"compress-seq", "-sizes", strings.Join(sizes, ","),
					"-level", strconv.Itoa(lv.c), "-checksum", "1"}, dargs...)...)
				for i, name := range names {
					n := int(binary.LittleEndian.Uint32(out))
					want := out[4 : 4+n]
					out = out[4+n:]
					if len(inputs[name]) == 0 {
						if len(got[i]) != 0 {
							t.Fatalf("EncodeAll %s: output for empty input", name)
						}
						continue
					}
					t.Run("EncodeAll/"+name, func(t *testing.T) { sameFrames(t, d, got[i], want) })
				}

				// Writer, reused, unknown size and pledged. An empty stream
				// writes nothing (no WithZeroFrames), unlike cref.
				for _, name := range names {
					src := inputs[name]
					if len(src) == 0 {
						continue
					}
					for _, pledged := range []bool{false, true} {
						sc := streamCase{level: lv.c, checksum: true, chunk: 1000}
						if pledged {
							sc.pledged = int64(len(src))
						}
						t.Run("Writer/"+sc.String()+"/"+name, func(t *testing.T) {
							gotS, err := writerStream(e, sc, src)
							if err != nil {
								t.Fatal(err)
							}
							want := creftest.Run(t, src, append(sc.crefArgs(len(src)), dargs...)...)
							sameFrames(t, d, gotS, want)
						})
					}
				}
			})
		}
	}
	t.Cleanup(func() {
		// Attach and copy for every strategy reached, and every
		// dictMatchState compressor of levels 1, 3, 7 and 11.
		runs.require(t,
			"attach/1", "copy/1", "attach/2", "copy/2", "attach/5", "copy/5",
			"attach/6", "copy/6", "attach/7", "copy/7",
			"dms/fast", "dms/dfast", "dms/lazy2-row", "dms/lazy2-hc", "dms/btlazy2", "dms/btopt")
	})
}

// TestDictCCtxLevels runs the context with a dictionary at levels 2 to 12
// (the strategies levels 1, 3, 7 and 11 do not reach with the test
// dictionaries: greedy and lazy, hash-chain and row; from level 13 C's
// strategies or post-block splitter are not ported) against `cref
// compress`, for inputs either side of the attach cutoffs.
func TestDictCCtxLevels(t *testing.T) {
	names, inputs := dictInputs(t)
	var keep []string
	for _, n := range names {
		if strings.HasPrefix(n, "text") && len(inputs[n]) <= 64<<10 {
			keep = append(keep, n)
		}
	}
	var runs dictRuns
	runs.install(t)
	for _, d := range testDicts(t) {
		dargs := d.crefArgs(t)
		dct := dctFullDict
		if d.raw {
			dct = dctRawContent
		}
		for level := 2; level <= 12; level++ {
			t.Run(d.name+"/L"+strconv.Itoa(level), func(t *testing.T) {
				t.Parallel()
				c := newCCtx()
				c.setLevel(level)
				c.setChecksum(true)
				if err := c.loadDictionary(d.dict, dct); err != nil {
					t.Fatal(err)
				}
				for _, name := range keep {
					src := inputs[name]
					got, err := c.compress2(nil, src)
					if err != nil {
						t.Fatal(err)
					}
					want := creftest.Run(t, src, append([]string{"compress", "-level", strconv.Itoa(level), "-checksum", "1", "-pledged", "1"}, dargs...)...)
					if !bytes.Equal(got, want) { // the context writes C's ID 0 for raw content
						t.Fatalf("%s: frame differs from C", name)
					}
				}
			})
		}
	}
	t.Cleanup(func() {
		runs.require(t, "dms/greedy-hc", "dms/lazy-hc", "dms/lazy2-hc", "dms/greedy-row", "dms/lazy-row",
			"dms/lazy2-row", "attach/3", "copy/3", "attach/4", "copy/4")
	})
}

// TestDictRawIDHeader checks the frame header of WithEncoderDictRaw: ID 0
// gives C's frame exactly; other IDs take 1, 2 or 4 bytes as the format
// requires and leave the rest of the frame equal to C's.
func TestDictRawIDHeader(t *testing.T) {
	raw := readTestdata(t, "dickens-32k.raw")
	src := raw[100:5000]
	want := creftest.Run(t, src, append([]string{"compress", "-level", "3", "-checksum", "1", "-pledged", "1"},
		dictCase{dict: raw, raw: true}.crefArgs(t)...)...)
	for _, c := range []struct {
		id    uint32
		field int
	}{{0, 0}, {1, 1}, {255, 1}, {256, 2}, {65535, 2}, {65536, 4}, {1 << 31, 4}} {
		e := newTestEncoder(t, WithEncoderDictRaw(c.id, raw))
		got := e.EncodeAll(src, nil)
		if len(got)-len(want) != c.field || got[4]&3 != byte([]int{0, 1, 2, 0, 3}[c.field]) {
			t.Fatalf("id %d: header field %d bytes, flag %d", c.id, len(got)-len(want), got[4]&3)
		}
		sameFrames(t, dictCase{raw: true, id: c.id}, got, want)
	}
}

// TestDictOptionErrors checks that invalid dictionaries are rejected.
func TestDictOptionErrors(t *testing.T) {
	dict := readTestdata(t, "train.dict")
	bad := map[string]EOption{
		"nil":          WithEncoderDict(nil),
		"short":        WithEncoderDict(dict[:7]),
		"no magic":     WithEncoderDict(readTestdata(t, "dickens-32k.raw")),
		"truncated":    WithEncoderDict(dict[:40]),
		"corrupt huf":  WithEncoderDict(slices.Concat(dict[:8], []byte{0xFF, 0xFF}, dict[10:])),
		"zero repcode": WithEncoderDict(zeroRep(dict)),
		"raw empty":    WithEncoderDictRaw(1, nil),
	}
	for name, opt := range bad {
		if _, err := NewWriter(nil, opt); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// zeroRep returns dict with its first repcode set to 0, which C rejects.
func zeroRep(dict []byte) []byte {
	var bs compressedBlockState
	eSize, err := loadCEntropy(&bs, dict)
	if err != nil {
		panic(err)
	}
	b := slices.Clone(dict)
	binary.LittleEndian.PutUint32(b[eSize-12:], 0)
	return b
}

// TestDictResetWithOptions changes the dictionary with ResetWithOptions,
// for EncodeAll and the Writer, and checks that WithLowerEncoderMem cannot
// be changed.
func TestDictResetWithOptions(t *testing.T) {
	ds := testDicts(t)
	src := readTestdata(t, "dickens-32k.raw")[1000:9000]
	e := newTestEncoder(t, WithEncoderLevel(SpeedDefault))
	for _, d := range ds {
		if err := e.ResetWithOptions(nil, d.option()); err != nil {
			t.Fatal(err)
		}
		want := creftest.Run(t, src, append([]string{"compress", "-level", "3", "-checksum", "1", "-pledged", "1"}, d.crefArgs(t)...)...)
		sameFrames(t, d, e.EncodeAll(src, nil), want)
		sc := streamCase{level: 3, checksum: true, chunk: 1000}
		got, err := writerStream(e, sc, src)
		if err != nil {
			t.Fatal(err)
		}
		sameFrames(t, d, got, creftest.Run(t, src, append(sc.crefArgs(len(src)), d.crefArgs(t)...)...))
	}
	if err := e.ResetWithOptions(nil, WithLowerEncoderMem(true)); err == nil {
		t.Fatal("WithLowerEncoderMem changed by ResetWithOptions")
	}
	if err := e.ResetWithOptions(nil, WithLowerEncoderMem(false)); err != nil {
		t.Fatalf("WithLowerEncoderMem unchanged: %v", err)
	}
}

// TestDictTinyRaw checks raw content shorter than 8 bytes, which C keeps as
// a dictionary but does not load (no history, ID 0): the frames equal C's.
func TestDictTinyRaw(t *testing.T) {
	src := readTestdata(t, "dickens-32k.raw")[:3000]
	for _, n := range []int{1, 7, 8} {
		d := dictCase{dict: []byte("abcdefgh")[:n], raw: true}
		for _, lv := range encoderLevels {
			e := newTestEncoder(t, WithEncoderLevel(lv.l), d.option())
			want := creftest.Run(t, src, append([]string{"compress", "-level", strconv.Itoa(lv.c), "-checksum", "1", "-pledged", "1"}, d.crefArgs(t)...)...)
			sameFrames(t, d, e.EncodeAll(src, nil), want)
		}
	}
}

// TestDictRowSaltCref follows the row match finder's salt state of one
// context through frames that attach and copy a CDict (row lazy2 at level
// 7): copying sets the context's salt to the CDict's (0) and the next reset
// advances it from there, as in C. The frames equal C's reused context's.
func TestDictRowSaltCref(t *testing.T) {
	dict := readTestdata(t, "train.dict")
	text := readTestdata(t, "dickens-32k.raw")
	text = append(text[16<<10:], text...) // 48 KiB
	sizes := []int{1000, 32<<10 + 1, 5000, 32 << 10, 100, 40000}
	saltFile := filepath.Join(t.TempDir(), "salts")
	var in []byte
	var ss []string
	for i, n := range sizes {
		in = append(in, text[i*7:i*7+n]...)
		ss = append(ss, strconv.Itoa(n))
	}
	out := creftest.Run(t, in, append([]string{"compress-seq", "-sizes", strings.Join(ss, ","), "-level", "7",
		"-salt-out", saltFile}, dictCase{dict: dict}.crefArgs(t)...)...)
	b, err := os.ReadFile(saltFile)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSpace(string(b)), "\n")
	var runs dictRuns
	runs.install(t)
	cc := newCCtx()
	cc.setLevel(7)
	if err := cc.loadDictionary(dict, dctFullDict); err != nil {
		t.Fatal(err)
	}
	pos := 0
	for i, n := range sizes {
		got, err := cc.compress2(nil, in[pos:pos+n])
		if err != nil {
			t.Fatal(err)
		}
		pos += n
		fl := int(binary.LittleEndian.Uint32(out))
		if !bytes.Equal(got, out[4:4+fl]) {
			t.Fatalf("step %d: frame differs from C", i)
		}
		out = out[4+fl:]
		ms := &cc.blockState.matchState
		if g := fmt.Sprintf("%016x %d", ms.hashSalt, ms.hashSaltEntropy); g != want[i] {
			t.Fatalf("step %d: salt state %q, C %q", i, g, want[i])
		}
	}
	runs.require(t, "attach/5", "copy/5")
}

// TestDictEncodeAllConcurrent runs EncodeAll from several goroutines on one
// Encoder, whose contexts share one CDict (read-only), and checks the
// frames against a sequential run; with -race it checks the sharing.
func TestDictEncodeAllConcurrent(t *testing.T) {
	text := readTestdata(t, "dickens-32k.raw")
	for _, d := range testDicts(t) {
		for _, lv := range encoderLevels {
			e := newTestEncoder(t, WithEncoderLevel(lv.l), d.option())
			srcs := [][]byte{text[:500], text[1000:9000], text[2000:], text[:20000]}
			want := make([][]byte, len(srcs))
			for i, src := range srcs {
				want[i] = e.EncodeAll(src, nil)
			}
			var wg sync.WaitGroup
			for g := 0; g < 4; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range srcs {
						j := (i + g) % len(srcs)
						if got := e.EncodeAll(srcs[j], nil); !bytes.Equal(got, want[j]) {
							t.Errorf("%s/L%d: input %d differs", d.name, lv.c, j)
						}
					}
				}()
			}
			wg.Wait()
		}
	}
}
