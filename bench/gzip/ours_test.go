package main

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func stdStream(t *testing.T, in []byte, level int) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := flate.NewWriter(&b, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(in); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("../..", rel))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s missing (make -C ref/ldref)", rel)
	}
	return p
}

// checkExact: the Decompressor contract of adapter.go on one stream.
func checkExact(t *testing.T, name string, d Decompressor, in, z []byte) {
	t.Helper()
	if n, err := d.Decompress(make([]byte, len(in)), z); err != nil || n != len(in) {
		t.Errorf("%s exact: n=%d err=%v", name, n, err)
	}
	if _, err := d.Decompress(make([]byte, len(in)-1), z); err == nil {
		t.Errorf("%s: dst one byte short must error", name)
	}
	if _, err := d.Decompress(make([]byte, len(in)+1), z); err == nil {
		t.Errorf("%s: dst one byte long must error", name)
	}
	dst := make([]byte, len(in))
	if n, err := d.Decompress(dst, z); err != nil || n != len(in) || !bytes.Equal(dst, in) {
		t.Errorf("%s reuse after error: n=%d err=%v equal=%v", name, n, err, bytes.Equal(dst, in))
	}
}

// Levels outside libdeflate's 0..12 are unsupported (exit 4), formats other
// than raw and gzip not ported (exit 3).
func TestOursUnsupported(t *testing.T) {
	for _, level := range []int{-2, -1, 13} {
		if _, err := ours.NewCompressor(level, "raw"); !errors.Is(err, ErrUnsupportedLevel) {
			t.Errorf("NewCompressor(%d): err=%v, want ErrUnsupportedLevel", level, err)
		}
	}
	if _, err := ours.NewCompressor(6, "zlib"); !errors.Is(err, ErrNotPorted) {
		t.Errorf("NewCompressor(zlib): err=%v, want ErrNotPorted", err)
	}
	if _, err := ours.NewDecompressor("zlib"); !errors.Is(err, ErrNotPorted) {
		t.Errorf("NewDecompressor(zlib): err=%v, want ErrNotPorted", err)
	}
}

// Self-check of the compress adapter at every level gzbench runs (0..12),
// raw and gzip: the output is byte-identical to libdeflate's (ldref
// compress), libdeflate decodes it back to the input (ldref decompress
// -exact 1), and so does ours (the Decompressor contract); a too small dst
// is an error.
func TestOursCompressMatchesLibdeflate(t *testing.T) {
	ldref := repoFile(t, "ref/ldref/ldref")
	inputs := [][]byte{tinyInput(), bytes.Repeat(tinyInput(), 30)}
	for _, format := range []string{"raw", "gzip"} {
		d, err := ours.NewDecompressor(format)
		if err != nil {
			t.Fatal(err)
		}
		for level := 0; level <= 12; level++ {
			c, err := ours.NewCompressor(level, format)
			if err != nil {
				t.Fatalf("%s L%d: %v", format, level, err)
			}
			for _, in := range inputs {
				name := fmt.Sprintf("%s L%d %d bytes", format, level, len(in))
				dst := make([]byte, len(in)+len(in)/2+1024) // gzbench's block capacity
				n, err := c.Compress(dst, in)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				z := dst[:n]
				cmd := exec.Command(ldref, "compress", "-l", strconv.Itoa(level), "-format", format)
				cmd.Stdin = bytes.NewReader(in)
				want, err := cmd.Output()
				if err != nil {
					t.Fatalf("%s: ldref compress: %v", name, err)
				}
				if !bytes.Equal(z, want) {
					t.Errorf("%s: %d bytes, libdeflate %d bytes (not byte-identical)", name, len(z), len(want))
				}
				cmd = exec.Command(ldref, "decompress", "-format", format, "-out-size", strconv.Itoa(len(in)), "-exact", "1")
				cmd.Stdin = bytes.NewReader(z)
				back, err := cmd.Output()
				if err != nil || !bytes.Equal(back, in) {
					t.Errorf("%s: libdeflate decode: %v, equal %v", name, err, bytes.Equal(back, in))
				}
				checkExact(t, name, d, in, z)
				if _, err := c.Compress(make([]byte, n-1), in); err == nil {
					t.Errorf("%s: dst one byte short must error", name)
				}
			}
		}
	}
}

// Streams produced by compress/flate (black box), all levels incl. stored (0)
// and Huffman-only (-2).
func TestOursDecompressStdlibStreams(t *testing.T) {
	in := tinyInput()
	d, err := ours.NewDecompressor("raw")
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []int{-2, 0, 1, 6, 9} {
		checkExact(t, "stdlib L"+strconv.Itoa(level), d, in, stdStream(t, in, level))
	}
}

// Streams produced by ldref (libdeflate), levels 0..12.
func TestOursDecompressLdrefStreams(t *testing.T) {
	ldref := repoFile(t, "ref/ldref/ldref")
	in := tinyInput()
	d, err := ours.NewDecompressor("raw")
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []int{0, 1, 6, 9, 12} {
		cmd := exec.Command(ldref, "compress", "-l", strconv.Itoa(level))
		cmd.Stdin = bytes.NewReader(in)
		z, err := cmd.Output()
		if err != nil {
			t.Fatalf("ldref compress -l %d: %v", level, err)
		}
		checkExact(t, "ldref L"+strconv.Itoa(level), d, in, z)
	}
}

// -zin path: prepare takes the recorded streams instead of compressing, with
// no compressor (as for a decode-only adapter), and rejects malformed record files.
func TestPrepareZin(t *testing.T) {
	in := tinyInput()
	const block = 1000
	var zin []byte
	for off := 0; off < len(in); off += block {
		end := min(off+block, len(in))
		z := stdStream(t, in[off:end], 6)
		zin = binary.LittleEndian.AppendUint64(zin, uint64(len(z)))
		zin = append(zin, z...)
	}
	b, err := prepare(ours, in, 6, block, "raw", zin)
	if err != nil {
		t.Fatal(err)
	}
	if b.comp != nil || b.zn >= len(in) {
		t.Errorf("comp=%v zn=%d", b.comp, b.zn)
	}
	if _, _, _, err := run(ours, in, 6, true, block, 0.1, 1, "raw", zin, nil); err != nil {
		t.Errorf("run -mode d with zin: %v", err)
	}
	for name, bad := range map[string][]byte{
		"truncated header": zin[:4],
		"short record":     zin[:len(zin)-1],
		"extra record":     append(append([]byte{}, zin...), zin...),
		"missing record":   zin[:8+int(binary.LittleEndian.Uint64(zin))],
	} {
		if _, err := prepare(ours, in, 6, block, "raw", bad); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// wrong data: a record that decodes to the wrong length must fail the round trip
	if _, err := prepare(ours, in[:len(in)-1], 6, block, "raw", zin); err == nil {
		t.Error("input shorter than the recorded streams: want error")
	}
	// without zin ours compresses the blocks itself
	if b, err := prepare(ours, in, 6, block, "raw", nil); err != nil || b.comp == nil {
		t.Errorf("no zin: err=%v", err)
	}
}

// ldbench -dumpz output is what gzbench -zin reads: decode it with ours.
func TestLdbenchDumpzRoundTrip(t *testing.T) {
	ldbench := repoFile(t, "ref/ldref/ldbench")
	in := tinyInput()
	dir := t.TempDir()
	f, z := filepath.Join(dir, "in"), filepath.Join(dir, "z")
	if err := os.WriteFile(f, in, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, level := range []int{1, 9, 12} {
		if out, err := exec.Command(ldbench, "-level", strconv.Itoa(level), "-block", "1000", "-dumpz", z, f).CombinedOutput(); err != nil {
			t.Fatalf("ldbench -dumpz: %v\n%s", err, out)
		}
		zin, err := os.ReadFile(z)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := run(ours, in, level, true, 1000, 0.1, 1, "raw", zin, nil); err != nil {
			t.Errorf("L%d: %v", level, err)
		}
	}
}

func TestGateExpectedLevelsMissing(t *testing.T) {
	var out bytes.Buffer
	WriteSummary(&out, []Row{ok("d", "6", "a/whole", 110)}, SummaryOpts{Levels: []string{"1", "6", "6", "12"}})
	s := out.String()
	for _, sub := range []string{"| 1 | 0 | n/a | n/a | FAIL (missing rows) |", "| 12 | 0 | n/a | n/a | FAIL (missing rows) |", "| 6 | 1 |", "level 1: no rows"} {
		if !strings.Contains(s, sub) {
			t.Errorf("summary missing %q:\n%s", sub, s)
		}
	}
	if strings.Count(s, "| 6 | 1 |") != 1 {
		t.Errorf("level 6 must appear once:\n%s", s)
	}
	if strings.Contains(s, "### Compress") {
		t.Errorf("a mode without rows is not listed:\n%s", s)
	}
	if i1, i6, i12 := strings.Index(s, "| 1 |"), strings.Index(s, "| 6 |"), strings.Index(s, "| 12 |"); !(i1 < i6 && i6 < i12) {
		t.Errorf("levels not in -l order:\n%s", s)
	}
	out.Reset()
	WriteSummary(&out, []Row{ok("d", "6", "a/whole", 110)}, SummaryOpts{})
	if strings.Contains(out.String(), "FAIL (missing rows)") {
		t.Errorf("no expected levels: nothing to add:\n%s", out.String())
	}
}

func summaryOf(rows []Row, o SummaryOpts) string {
	var b bytes.Buffer
	WriteSummary(&b, rows, o)
	return b.String()
}

// Every combination of requested modes x evaluated modes: a gate that was not
// evaluated is always announced, and OVERALL never reads as a pass.
func TestSummaryNotEvaluated(t *testing.T) {
	why := map[string]string{"c": "ours not ported for -mode c -format raw; no ours-vs-C rows"}
	lv := []string{"1", "6"}
	pass := func(m string) []Row { return []Row{ok(m, "1", "a", 120), ok(m, "6", "a", 120)} }
	fail := func(m string) []Row { return []Row{ok(m, "1", "a", 80), ok(m, "6", "a", 120)} }
	cases := []struct {
		name  string
		rows  []Row
		o     SummaryOpts
		want  []string
		avoid []string
	}{
		{"both requested, only d (pass)", pass("d"), SummaryOpts{Levels: lv, Modes: []string{"c", "d"}, Why: why}, []string{
			"**ONLY the Decompress gate was evaluated; there is NO compression result in this file.**",
			"### Compress gate: NOT EVALUATED (ours not ported for -mode c -format raw; no ours-vs-C rows)",
			"| 1 | 0 | n/a | n/a | NOT EVALUATED |", "| 6 | 0 | n/a | n/a | NOT EVALUATED |",
			"### Decompress gate", "OVERALL: INCOMPLETE (compress not evaluated)"}, []string{"OVERALL: PASS"}},
		{"both requested, only d (fail)", fail("d"), SummaryOpts{Levels: lv, Modes: []string{"c", "d"}, Why: why}, []string{
			"OVERALL: FAIL; INCOMPLETE (compress not evaluated)"}, []string{"OVERALL: PASS"}},
		{"only c requested, no rows", nil, SummaryOpts{Levels: lv, Modes: []string{"c"}, Why: why}, []string{
			"**NO gate was evaluated; there is NO compression result in this file.**",
			"### Compress gate: NOT EVALUATED (ours not ported", "OVERALL: INCOMPLETE (compress not evaluated)"}, []string{"OVERALL: PASS", "Decompress"}},
		{"only d requested, no rows, default reason", nil, SummaryOpts{Levels: lv, Modes: []string{"d"}}, []string{
			"### Decompress gate: NOT EVALUATED (no ours-vs-C rows)", "OVERALL: INCOMPLETE (decompress not evaluated)"}, []string{"OVERALL: PASS"}},
		{"both requested, none", nil, SummaryOpts{Levels: lv, Modes: []string{"c", "d"}}, []string{
			"**NO gate was evaluated; there is NO compression or decompression result in this file.**",
			"OVERALL: INCOMPLETE (compress, decompress not evaluated)"}, []string{"OVERALL: PASS"}},
		{"both requested, both pass", append(pass("c"), pass("d")...), SummaryOpts{Levels: lv, Modes: []string{"c", "d"}}, []string{
			"**Gates evaluated: Compress, Decompress.**", "OVERALL: PASS"}, []string{"NOT EVALUATED", "INCOMPLETE"}},
		{"both requested, c fails", append(fail("c"), pass("d")...), SummaryOpts{Modes: []string{"c", "d"}}, []string{"OVERALL: FAIL"}, []string{"INCOMPLETE", "OVERALL: PASS"}},
		{"only d requested, d passes", pass("d"), SummaryOpts{Modes: []string{"d"}}, []string{"**Gates evaluated: Decompress.**", "OVERALL: PASS"}, []string{"Compress"}},
		{"modes unset = modes with rows", pass("d"), SummaryOpts{}, []string{"OVERALL: PASS"}, []string{"NOT EVALUATED"}},
		{"noasm, only d", pass("d"), SummaryOpts{Info: true, Levels: lv, Modes: []string{"c", "d"}}, []string{
			"### Compress: NOT EVALUATED", "REPORTED ONLY (noasm)", "OVERALL: REPORTED ONLY (noasm), not a gate; INCOMPLETE (compress not evaluated)"}, []string{"PASS", "FAIL"}},
	}
	for _, c := range cases {
		s := summaryOf(c.rows, c.o)
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: missing %q:\n%s", c.name, w, s)
			}
		}
		for _, w := range c.avoid {
			if strings.Contains(s, w) {
				t.Errorf("%s: must not contain %q:\n%s", c.name, w, s)
			}
		}
	}
}

// fakeDecoder wraps a real decoder and corrupts its result.
type fakeDecoder struct {
	real Decompressor
	mode string // "short": right data, length-1; "garbage": right length, content untouched (zeros)
}

func (f fakeDecoder) Decompress(dst, src []byte) (int, error) {
	switch f.mode {
	case "short":
		n, err := f.real.Decompress(dst, src)
		return n - 1, err
	default:
		return len(dst), nil
	}
}

type fakeAdapter struct{ mode string }

func (a fakeAdapter) NewCompressor(int, string) (Compressor, error) { return nil, ErrNotPorted }
func (a fakeAdapter) NewDecompressor(format string) (Decompressor, error) {
	d, err := ours.NewDecompressor(format)
	return fakeDecoder{d, a.mode}, err
}

// prepare must reject a decoder that reports the wrong length, and one that
// reports the right length without producing the input.
func TestPrepareRejectsBadDecoders(t *testing.T) {
	in := tinyInput()
	z := stdStream(t, in, 6)
	zin := binary.LittleEndian.AppendUint64(nil, uint64(len(z)))
	zin = append(zin, z...)
	for _, mode := range []string{"short", "garbage"} {
		if _, err := prepare(fakeAdapter{mode}, in, 6, 0, "raw", zin); err == nil {
			t.Errorf("%s decoder accepted by prepare", mode)
		}
	}
	if _, err := prepare(fakeAdapter{"real"}, in, 6, 0, "raw", zin); err == nil {
		t.Error("garbage (default) decoder accepted by prepare")
	}
}

// The timed path must not allocate: ours.Decompress on a real stream, reusing
// dst, produces output and zero allocations per call.
func TestOursDecompressNoAllocs(t *testing.T) {
	in := tinyInput()
	z := stdStream(t, in, 6)
	d, err := ours.NewDecompressor("raw")
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, len(in))
	n, err := d.Decompress(dst, z)
	if err != nil || n == 0 || n != len(in) || !bytes.Equal(dst, in) {
		t.Fatalf("n=%d err=%v equal=%v", n, err, bytes.Equal(dst, in))
	}
	if a := testing.AllocsPerRun(50, func() {
		if n, err := d.Decompress(dst, z); err != nil || n == 0 {
			t.Fatal(n, err)
		}
	}); a != 0 {
		t.Errorf("Decompress allocates %v times per call", a)
	}
}
