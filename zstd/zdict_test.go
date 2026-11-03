package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

var (
	buildDictSamplesOnce sync.Once
	buildDictSamplesData [][]byte
	buildDictSamplesErr  error
)

// buildDictSamples returns the 200 samples of 4 KiB that train.dict was
// trained on (Phase 1 Task 18's sample set; see dict_test.go): the output
// of `go run testdata/gentrain.go DIR`, in file name order.
func buildDictSamples(t testing.TB) [][]byte {
	t.Helper()
	buildDictSamplesOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gentrain")
		if err != nil {
			buildDictSamplesErr = err
			return
		}
		defer os.RemoveAll(dir)
		if out, err := exec.Command("go", "run", filepath.Join("testdata", "gentrain.go"), dir).CombinedOutput(); err != nil {
			buildDictSamplesErr = fmt.Errorf("gentrain: %v: %s", err, out)
			return
		}
		for i := range 200 {
			b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("s%03d", i)))
			if err != nil {
				buildDictSamplesErr = err
				return
			}
			buildDictSamplesData = append(buildDictSamplesData, b)
		}
	})
	if buildDictSamplesErr != nil {
		t.Fatal(buildDictSamplesErr)
	}
	return buildDictSamplesData
}

// buildDictHistory returns the first n bytes of dickens: from
// testdata/dickens-32k.raw up to 32 KiB. Above that the Silesia corpus's
// dickens is used when present (bench/corpus/silesia/dickens), and
// otherwise train.dict's bytes stand in for it (CI fetches no corpus).
func buildDictHistory(t testing.TB, n int) []byte {
	t.Helper()
	if raw := readTestdata(t, "dickens-32k.raw"); n <= len(raw) {
		return raw[:n]
	}
	if b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia", "dickens")); err == nil && len(b) >= n {
		return b[:n]
	}
	b := readTestdata(t, "train.dict")
	if len(b) < n {
		t.Fatalf("no %d-byte history", n)
	}
	return b[:n]
}

// splitSamples returns the samples as sub-slices of one buffer, as C's
// ZDICT functions take them (consecutive in memory).
func splitSamples(samples [][]byte) [][]byte {
	all := slices.Concat(samples...)
	out := make([][]byte, len(samples))
	pos := 0
	for i, s := range samples {
		out[i] = all[pos : pos+len(s) : pos+len(s)]
		pos += len(s)
	}
	return out
}

// sameDict fails unless got equals C's dictionary want.
func sameDict(t testing.TB, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		at := 0
		for at < min(len(got), len(want)) && got[at] == want[at] {
			at++
		}
		t.Fatalf("dictionary differs from C at byte %d (len %d, C %d)", at, len(got), len(want))
	}
}

func crefFinalize(t testing.TB, content []byte, samples [][]byte, level int, id uint32) []byte {
	t.Helper()
	want, cerr := creftest.FinalizeDictCRef(t, content, samples, level, id)
	if cerr != nil {
		t.Fatalf("cref finalize-dict: %v", cerr)
	}
	return want
}

// TestBuildDictCref requires finalizeDictionary (C levels 0, 1, 3, 7, 11)
// and BuildDict (every EncoderLevel, unset included) to equal
// `cref finalize-dict` byte for byte, for histories of 8 B, 1 KiB, 32 KiB
// and 110 KiB of dickens and dictionary IDs 0 (C's XXH64-derived ID) and
// 12345. The 8-byte history gives a CDict window so small (1 KiB) that C
// rejects every 4 KiB sample as larger than a block (no statistics, the
// flat literal distribution).
func TestBuildDictCref(t *testing.T) {
	samples := buildDictSamples(t)
	contig := splitSamples(samples)
	for _, hn := range []int{8, 1 << 10, 32 << 10, 110 << 10} {
		hist := buildDictHistory(t, hn)
		for _, id := range []uint32{0, 12345} {
			for _, lv := range []int{0, 1, 3, 7, 11} {
				t.Run(fmt.Sprintf("finalize/h%d/id%d/L%d", hn, id, lv), func(t *testing.T) {
					want := crefFinalize(t, hist, samples, lv, id)
					got, err := finalizeDictionary(hist, samples, lv, id, repStartValue, nil)
					if err != nil {
						t.Fatal(err)
					}
					sameDict(t, got, want)
				})
			}
			for _, el := range []EncoderLevel{levelUnset, SpeedFastest, SpeedDefault, SpeedBetterCompression, SpeedBestCompression} {
				cl := zstdLevels[SpeedBestCompression]
				if el != levelUnset {
					cl = zstdLevels[el]
				}
				t.Run(fmt.Sprintf("BuildDict/h%d/id%d/%s", hn, id, el), func(t *testing.T) {
					want := crefFinalize(t, hist, samples, cl, id)
					got, err := BuildDict(BuildDictOptions{ID: id, Contents: contig, History: hist, Level: el})
					if err != nil {
						t.Fatal(err)
					}
					sameDict(t, got, want)
				})
			}
		}
	}
}

// TestBuildDictDegenerate compares degenerate inputs with C: no samples,
// empty samples, an empty history, histories shorter than the largest
// repcode (zero padding), and samples above 128 KiB (cut to a block).
// Histories below about 1.5 KiB give the CDict a window under 4 KiB
// (ZSTD_cpm_createCDict assumes a 513-byte source), so C skips every
// 4 KiB sample as larger than a block.
func TestBuildDictDegenerate(t *testing.T) {
	samples := buildDictSamples(t)
	hist := buildDictHistory(t, 32<<10)
	big := slices.Concat(samples...)[:300<<10]
	cases := []struct {
		name    string
		hist    []byte
		samples [][]byte
	}{
		{"no-samples", hist, nil},
		{"empty-samples", hist, [][]byte{{}, {}, nil}},
		{"empty-history", nil, samples[:20]},
		{"history-1", hist[:1], samples[:20]},
		{"history-7", hist[:7], samples[:20]},
		{"history-9", hist[:9], samples[:20]},
		{"tiny-samples", hist, [][]byte{{1}, {2, 3}, {4, 5, 6, 7, 8, 9}, hist[:7], hist[100:130]}},
		{"big-sample", hist, [][]byte{big, samples[0]}},
		// An empty history keeps the level's window: a 300 KiB sample is
		// cut to 128 KiB; one 20 KiB sample selects the <= 128 KiB
		// parameters (the average sample size counts no empty slot).
		{"empty-history-big-sample", nil, [][]byte{big}},
		{"empty-history-one-sample", nil, [][]byte{big[:20<<10]}},
		{"110k-history-big-sample", buildDictHistory(t, 110<<10), [][]byte{big, samples[0]}},
		{"rle-samples", hist, [][]byte{bytes.Repeat([]byte{'a'}, 5000), bytes.Repeat([]byte{0}, 20000)}},
	}
	for _, c := range cases {
		for _, lv := range []int{1, 3, 7, 11} {
			t.Run(fmt.Sprintf("%s/L%d", c.name, lv), func(t *testing.T) {
				want := crefFinalize(t, c.hist, c.samples, lv, 7)
				got, err := finalizeDictionary(c.hist, c.samples, lv, 7, repStartValue, nil)
				if err != nil {
					t.Fatal(err)
				}
				sameDict(t, got, want)
			})
		}
	}
}

// TestBuildDictOptionErrors checks BuildDict's own argument checks.
func TestBuildDictOptionErrors(t *testing.T) {
	hist := buildDictHistory(t, 1<<10)
	for _, o := range []BuildDictOptions{
		{History: hist, Level: SpeedBestCompression + 1},
		{History: hist, Level: -1},
		{History: hist, Offsets: [3]int{0, 4, 8}},
		{History: hist, Offsets: [3]int{1, -4, 8}},
		{History: hist, Offsets: [3]int{1, 4, math.MaxInt}}, // 64-bit: above uint32; 32-bit: beyond the capacity
	} {
		if _, err := BuildDict(o); err == nil {
			t.Errorf("BuildDict(level %d, offsets %v): no error", o.Level, o.Offsets)
		}
	}
}

// TestBuildDictCapacity checks the output capacity C has under cref
// (content + 64 KiB): repcodes reaching beyond it fail with dstSize_tooSmall
// as in C, so padding stays bounded.
func TestBuildDictCapacity(t *testing.T) {
	hist := buildDictHistory(t, 100)
	samples := buildDictSamples(t)[:10]
	_, err := BuildDict(BuildDictOptions{History: hist, Contents: samples, Offsets: [3]int{1, 4, 100 + 64<<10}})
	var ce *buildDictError
	if !errors.As(err, &ce) || ce.code != 70 {
		t.Fatalf("err = %v, want C error 70 (dstSize_tooSmall)", err)
	}
	if _, err := BuildDict(BuildDictOptions{History: hist, Contents: samples, Offsets: [3]int{1, 4, 100 + 60<<10}}); err != nil {
		t.Fatal(err)
	}
}

// dictCLI runs the C zstd program at path with the dictionary dict.
func dictCLI(t testing.TB, path string, dict, in []byte, args ...string) ([]byte, error) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "dict")
	if err := os.WriteFile(f, dict, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, append(args, "-c", "-D", f)...)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", path, args, err, stderr.String())
	}
	return out.Bytes(), nil
}

// roundTrips checks dict both ways with the C program at cli: frames our
// Encoder writes decode with it, frames it writes decode with our Decoder.
func roundTrips(t *testing.T, cli string, dict []byte, el EncoderLevel, srcs [][]byte) {
	t.Helper()
	e := newTestEncoder(t, WithEncoderLevel(el), WithEncoderDict(dict))
	d := newTestDecoder(t, WithDecoderDicts(dict))
	for i, src := range srcs {
		frame := e.EncodeAll(src, nil)
		dec, err := dictCLI(t, cli, dict, frame, "-d")
		if err != nil || !bytes.Equal(dec, src) {
			t.Fatalf("src %d: C decode of our frame: %v", i, err)
		}
		cframe, err := dictCLI(t, cli, dict, src, "-"+strconv.Itoa(zstdLevels[el]))
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.DecodeAll(cframe, nil)
		if err != nil || !bytes.Equal(got, src) {
			t.Fatalf("src %d: our decode of C's frame: %v", i, err)
		}
	}
}

// usableSrcs are inputs for the dictionaries built from buildDictSamples:
// unseen gentrain-like text (the last samples, held out) and dickens.
func usableSrcs(t testing.TB) [][]byte {
	samples := buildDictSamples(t)
	dick := buildDictHistory(t, 32<<10)
	return [][]byte{samples[199], samples[198][:1000], slices.Concat(samples[190:197]...), dick[5000:6000], dick[20000:]}
}

// TestBuildDictUsable builds dictionaries (32 KiB history) and checks that
// our EncodeAll with them equals `cref compress -dict`, and that they work
// both ways with the zstd program built from ref/zstd (`-D`).
func TestBuildDictUsable(t *testing.T) {
	samples := buildDictSamples(t)[:190]
	hist := buildDictHistory(t, 32<<10)
	cli := creftest.ZstdCLI(t)
	for _, lv := range encoderLevels {
		t.Run(lv.l.String(), func(t *testing.T) {
			dict, err := BuildDict(BuildDictOptions{Contents: samples, History: hist, Level: lv.l})
			if err != nil {
				t.Fatal(err)
			}
			e := newTestEncoder(t, WithEncoderLevel(lv.l), WithEncoderDict(dict))
			dc := dictCase{dict: dict}
			for i, src := range usableSrcs(t) {
				got := e.EncodeAll(src, nil)
				want := creftest.Run(t, src, append([]string{"compress", "-level", strconv.Itoa(lv.c), "-checksum", "1", "-pledged", "1"}, dc.crefArgs(t)...)...)
				if !bytes.Equal(got, want) {
					t.Fatalf("src %d: EncodeAll differs from cref compress -dict", i)
				}
			}
			roundTrips(t, cli, dict, lv.l, usableSrcs(t))
		})
	}
}

// TestBuildDictCompatV155 checks both CompatV155 values against the system
// zstd v1.5.5 (/usr/bin/zstd): the same bytes (C's), loaded by v1.5.5 both
// ways.
func TestBuildDictCompatV155(t *testing.T) {
	const cli = "/usr/bin/zstd"
	v, err := exec.Command(cli, "-V").Output()
	if err != nil || !strings.Contains(string(v), "v1.5.5") {
		t.Skipf("%s is not zstd v1.5.5 (%q, %v)", cli, v, err)
	}
	samples := buildDictSamples(t)[:190]
	var dicts [2][]byte
	for i, compat := range []bool{false, true} {
		for _, hn := range []int{8, 32 << 10} {
			dict, err := BuildDict(BuildDictOptions{Contents: samples, History: buildDictHistory(t, hn), CompatV155: compat})
			if err != nil {
				t.Fatal(err)
			}
			t.Run(fmt.Sprintf("compat=%v/h%d", compat, hn), func(t *testing.T) {
				roundTrips(t, cli, dict, SpeedBestCompression, usableSrcs(t))
				roundTrips(t, cli, dict, SpeedFastest, usableSrcs(t))
			})
			if hn == 32<<10 {
				dicts[i] = dict
			}
		}
	}
	if !bytes.Equal(dicts[0], dicts[1]) {
		t.Fatal("CompatV155 changes the dictionary")
	}
}

// TestBuildDictOffsets checks custom Offsets: they replace C's repcodes
// (and nothing else: the rest equals C's dictionary), a history shorter
// than the largest gets zero padding in front, and frames using the
// dictionary decode with C and with our Decoder.
func TestBuildDictOffsets(t *testing.T) {
	samples := buildDictSamples(t)[:190]
	cli := creftest.ZstdCLI(t)
	for _, c := range []struct {
		hn   int
		offs [3]int
	}{
		{32 << 10, [3]int{11, 5, 1000}},
		{32 << 10, [3]int{1, 1, 32 << 10}},
		{8, [3]int{100, 2, 3}},
		{1 << 10, [3]int{4, 8, 3000}},
	} {
		t.Run(fmt.Sprintf("h%d/%v", c.hn, c.offs), func(t *testing.T) {
			hist := buildDictHistory(t, c.hn)
			dict, err := BuildDict(BuildDictOptions{ID: 99, Contents: samples, History: hist, Offsets: c.offs, Level: SpeedDefault})
			if err != nil {
				t.Fatal(err)
			}
			info, err := InspectDictionary(dict)
			if err != nil {
				t.Fatal(err)
			}
			if info.Offsets() != c.offs || info.ID() != 99 {
				t.Fatalf("offsets %v ID %d, want %v 99", info.Offsets(), info.ID(), c.offs)
			}
			content := info.Content()
			pad := max(c.offs[0], c.offs[1], c.offs[2], 8) - c.hn
			if pad < 0 {
				pad = 0
			}
			if len(content) != c.hn+pad || !bytes.Equal(content[pad:], hist) || !bytes.Equal(content[:pad], make([]byte, pad)) {
				t.Fatalf("content: %d bytes, want %d zero bytes then the history", len(content), pad)
			}
			// Everything but the repcodes is C's dictionary.
			want := crefFinalize(t, hist, samples, 3, 99)
			hdr := len(dict) - len(content)
			wantHdr := len(want) - max(c.hn, 8)
			if hdr != wantHdr {
				t.Fatalf("header %d bytes, C %d", hdr, wantHdr)
			}
			for i, r := range c.offs {
				binary.LittleEndian.PutUint32(want[hdr-12+4*i:], uint32(r))
			}
			sameDict(t, dict[:hdr], want[:hdr])
			roundTrips(t, cli, dict, SpeedDefault, usableSrcs(t))
			roundTrips(t, cli, dict, SpeedBestCompression, usableSrcs(t))
		})
	}
}

// TestBuildDictDebugOut checks DebugOut: C's notification-level-4 messages,
// and the same dictionary as without it.
func TestBuildDictDebugOut(t *testing.T) {
	samples := buildDictSamples(t)
	hist := buildDictHistory(t, 32<<10)
	var buf bytes.Buffer
	got, err := BuildDict(BuildDictOptions{Contents: samples, History: hist, DebugOut: &buf})
	if err != nil {
		t.Fatal(err)
	}
	want, err := BuildDict(BuildDictOptions{Contents: samples, History: hist})
	if err != nil {
		t.Fatal(err)
	}
	sameDict(t, got, want)
	out := buf.String()
	head := "\r" + strings.Repeat(" ", 70) + "\r" + "statistics ... \n" + "Offset Code Frequencies : \n" + " 0 :"
	if !strings.HasPrefix(out, head) {
		t.Fatalf("DebugOut starts %q, want %q", out[:min(len(out), len(head)+10)], head)
	}
	// offcodeMax = highbit32(32 KiB + 128 KiB) = 17: 18 lines.
	if n := strings.Count(out, " :"); n != 18+1 {
		t.Fatalf("%d frequency lines, want 18", n-1)
	}
	// Samples rejected as larger than a block (8-byte history: 1 KiB
	// blocks) are reported at level 3, the flat literals at level 2.
	if strings.Contains(out, "warning") {
		t.Fatalf("unexpected warning:\n%s", out)
	}
	buf.Reset()
	if _, err := BuildDict(BuildDictOptions{Contents: samples[:2], History: hist[:8], DebugOut: &buf}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"warning : could not compress sample size 4096 \n", "warning : pathological dataset : literals are not compressible : samples are noisy or too regular \n"} {
		if !strings.Contains(buf.String(), s) {
			t.Fatalf("DebugOut lacks %q:\n%s", s, buf.String())
		}
	}
}

// TestInsertSortCount pins ZDICT_insertSortCount: the table keeps the
// ZSTD_REP_NUM largest counts in decreasing order, earlier offsets first on
// ties (C computes it, but does not write it to the dictionary).
func TestInsertSortCount(t *testing.T) {
	var table [repNum + 1]offsetCount
	for off, cnt := range []uint32{0, 5, 3, 9, 5, 1, 9, 2} {
		insertSortCount(&table, uint32(off), cnt)
	}
	// The last slot is insertion scratch.
	want := [repNum]offsetCount{{3, 9}, {6, 9}, {1, 5}}
	if [repNum]offsetCount(table[:repNum]) != want {
		t.Fatalf("table %v, want %v", table, want)
	}
}

// FuzzBuildDict builds a dictionary from an arbitrary history and samples
// (split from one buffer at the cut lengths in cuts) at the C level and ID
// that mode picks. Nothing may panic; the result (dictionary or C error
// code) must equal `cref finalize-dict`'s, and BuildDict must not panic.
func FuzzBuildDict(f *testing.F) {
	samples := buildDictSamples(f)
	dick := readTestdata(f, "dickens-32k.raw")
	f.Add(uint8(0), dick[:1000], slices.Concat(samples[:8]...), []byte{16, 16, 16, 16, 16, 16, 16})
	f.Add(uint8(3), dick[:8], slices.Concat(samples[:3]...), []byte{})
	f.Add(uint8(9), dick, slices.Concat(samples[10:30]...), []byte{1, 2, 3, 200, 0, 0, 40})
	f.Add(uint8(4), []byte{}, []byte{}, []byte{})
	f.Add(uint8(14), dick[:300], bytes.Repeat([]byte{'x'}, 3000), []byte{5, 30})
	f.Fuzz(func(t *testing.T, mode uint8, hist, blob, cuts []byte) {
		if len(hist) > 128<<10 || len(blob) > 256<<10 || len(cuts) > 256 {
			return
		}
		var parts [][]byte
		for _, c := range cuts {
			n := min(int(c)*64, len(blob))
			parts = append(parts, blob[:n:n])
			blob = blob[n:]
		}
		if len(blob) > 0 {
			parts = append(parts, blob)
		}
		lv := []int{0, 1, 3, 7, 11, -1, 2, 5}[mode&7]
		var id uint32
		if mode&8 != 0 {
			id = uint32(len(hist)) * 2654435761
		}
		got, err := finalizeDictionary(hist, parts, lv, id, repStartValue, nil)
		want, cerr := creftest.FinalizeDictCRef(t, hist, parts, lv, id)
		switch {
		case err != nil || cerr != nil:
			var ce *buildDictError
			if err == nil || cerr == nil || !errors.As(err, &ce) || ce.code != cerr.Code {
				t.Fatalf("err %v, C %v", err, cerr)
			}
		default:
			sameDict(t, got, want)
		}
		el := EncoderLevel(mode>>4) % 5
		offs := [3]int{int(mode), 4, 8}
		if mode&16 == 0 {
			offs = [3]int{}
		}
		_, _ = BuildDict(BuildDictOptions{ID: id, Contents: parts, History: hist, Level: el, Offsets: offs})
	})
}
