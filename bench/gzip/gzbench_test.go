package main

import (
	"bytes"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClock advances only when the fake workload "runs".
type fakeClock struct{ t float64 }

func (c *fakeClock) now() float64 { return c.t }

func TestMeasureFakeWorkload(t *testing.T) {
	c := &fakeClock{}
	// Each pass takes 0.1 s except while `slow` (windows of the first pass
	// are 0.1 s, discarded as < 0.5 s on the first iteration).
	const perPass = 0.1
	passes := 0
	run := func(loops int) { passes += loops; c.t += perPass * float64(loops) }
	// 1e6 bytes per pass at 0.1 s/pass = 10 MB/s.
	got := Measure(run, 1e6, 2, 1, c.now)
	if math.Abs(got-10) > 0.5 {
		t.Fatalf("Measure = %v, want ~10", got)
	}
	if c.t < 2 {
		t.Fatalf("stopped after %v s, want >= 2", c.t)
	}
}

func TestMeasureDiscardsShortWindowsAndPicksBest(t *testing.T) {
	c := &fakeClock{}
	// Pass cost shrinks with each call: window 1 (1 pass) = 0.01 s (< 0.5,
	// discarded), later windows get faster, so the best is the last counted.
	cost := []float64{0.01, 0.1, 0.1, 0.05, 0.05, 0.05, 0.05, 0.05, 0.05, 0.05, 0.05}
	i := 0
	run := func(loops int) {
		k := i
		if k >= len(cost) {
			k = len(cost) - 1
		}
		i++
		c.t += cost[k] * float64(loops)
	}
	got := Measure(run, 1e6, 3, 2, c.now)
	// Window 1 would be 100 MB/s but lasts 0.01 s so it must be ignored; the
	// best counted pass costs 0.05 s => 20 MB/s.
	if got > 25 || got < 9 {
		t.Fatalf("Measure = %v, want within counted windows only (<=20)", got)
	}
}

func TestMeasureNoCountedWindow(t *testing.T) {
	c := &fakeClock{}
	// Always-0.01 s windows, loops resize to keep ~1 s, so force failure with
	// a clock that never reaches 0.5 s per window: loops fixed by dt<0.02.
	run := func(loops int) { c.t += 0.001 }
	if got := Measure(run, 1e6, 0.05, 0, c.now); got != 0 {
		t.Fatalf("Measure = %v, want 0 (no window >= 0.5 s)", got)
	}
}

func TestMedianGeomean(t *testing.T) {
	if got := Median([]float64{3, 1, 2}); got != 2 {
		t.Errorf("median odd = %v", got)
	}
	if got := Median([]float64{4, 1, 3, 2}); got != 2 { // lower middle, like compare.sh
		t.Errorf("median even = %v", got)
	}
	if got := Geomean([]float64{100, 100}); math.Abs(got-100) > 1e-9 {
		t.Errorf("geomean = %v", got)
	}
	if got := Geomean([]float64{50, 200}); math.Abs(got-100) > 1e-9 {
		t.Errorf("geomean = %v", got)
	}
}

func ok(mode, level, label string, pct float64) Row { return Row{mode, level, label, pct, StatusOK} }

func verdict(t *testing.T, rows []Row) GateResult {
	t.Helper()
	res := Gate(rows)
	if len(res) != 1 {
		t.Fatalf("want 1 group, got %d", len(res))
	}
	return res[0]
}

func TestGate(t *testing.T) {
	rows := []Row{
		ok("c", "6", "a/whole", 110), ok("c", "6", "b/whole", 96), // pass: geomean>100, min 96
		ok("c", "9", "a/whole", 120), ok("c", "9", "b/whole", 93), // fail: row < 95
		ok("c", "12", "a/whole", 150), ok("c", "12", "b/whole", 85), // fail: hard floor
		ok("d", "6", "a/whole", 99), ok("d", "6", "b/whole", 99), // fail: geomean < 100
	}
	want := map[string]bool{"c6": true, "c9": false, "c12": false, "d6": false}
	for _, g := range Gate(rows) {
		if g.Pass != want[g.Mode+g.Level] {
			t.Errorf("%s%s Pass=%v want %v", g.Mode, g.Level, g.Pass, !g.Pass)
		}
	}
	var out bytes.Buffer
	WriteSummary(&out, rows, SummaryOpts{})
	s := out.String()
	for _, sub := range []string{"Compress gate", "Decompress gate", "b/whole 93.0% < 95%", "b/whole 85.0% < 90% HARD FLOOR", "FAIL (below hard floor)", "level 6: geomean 99.0% < 100%"} {
		if !strings.Contains(s, sub) {
			t.Errorf("summary missing %q:\n%s", sub, s)
		}
	}
}

func TestGateBoundaries(t *testing.T) {
	// Exactly 100 geomean, exactly 95 row: pass. 94.9 row: fail.
	if g := verdict(t, []Row{ok("c", "1", "a", 100), ok("c", "1", "b", 100)}); !g.Pass {
		t.Error("geomean exactly 100 must pass")
	}
	if g := verdict(t, []Row{ok("c", "1", "a", 110), ok("c", "1", "b", 95)}); !g.Pass {
		t.Errorf("row exactly 95 must pass: %+v", g)
	}
	if g := verdict(t, []Row{ok("c", "1", "a", 110), ok("c", "1", "b", 94.9)}); g.Pass || len(g.Below95) != 1 || len(g.Below90) != 0 {
		t.Errorf("row 94.9 must fail without hard floor: %+v", g)
	}
	if g := verdict(t, []Row{ok("c", "1", "a", 200), ok("c", "1", "b", 90)}); len(g.Below90) != 0 || g.Pass {
		t.Errorf("row exactly 90 is not below the floor (but fails the 95 rule): %+v", g)
	}
	if g := verdict(t, []Row{ok("c", "1", "a", 200), ok("c", "1", "b", 89.9)}); len(g.Below90) != 1 {
		t.Errorf("row 89.9 is below the hard floor: %+v", g)
	}
}

func TestGateMismatchNeverPasses(t *testing.T) {
	// A fast-but-wrong row at 120% must neither pass nor lift the geomean.
	rows := []Row{ok("c", "6", "a/whole", 99.9), {"c", "6", "b/whole", 120, StatusMismatch}}
	g := verdict(t, rows)
	if g.Pass || len(g.Mismatch) != 1 {
		t.Fatalf("mismatch must fail: %+v", g)
	}
	if math.Abs(g.Geomean-99.9) > 1e-9 {
		t.Errorf("mismatch row must be excluded from geomean, got %v", g.Geomean)
	}
	// Only a mismatch row: geomean n/a, still FAIL.
	g = verdict(t, []Row{{"c", "6", "x", 150, StatusMismatch}})
	if g.Pass || !math.IsNaN(g.Geomean) {
		t.Errorf("only-mismatch group: %+v", g)
	}
	// Mismatch in one direction only: the other direction's level still passes.
	res := Gate([]Row{{"c", "6", "x", 120, StatusMismatch}, ok("d", "6", "x", 120)})
	if res[0].Pass || !res[1].Pass {
		t.Errorf("per-direction: %+v", res)
	}
	var out bytes.Buffer
	WriteSummary(&out, []Row{ok("c", "6", "a", 110), {"c", "6", "b", 120, StatusMismatch}}, SummaryOpts{})
	for _, sub := range []string{"FAIL (MISMATCH)", "b MISMATCH"} {
		if !strings.Contains(out.String(), sub) {
			t.Errorf("summary missing %q:\n%s", sub, out.String())
		}
	}
	if strings.Contains(out.String(), "| PASS |") {
		t.Errorf("mismatch level printed PASS:\n%s", out.String())
	}
}

func TestGateMissingAndInvalid(t *testing.T) {
	for name, bad := range map[string]Row{
		"missing": {"c", "6", "b", 0, StatusMissing},
		"zero":    ok("c", "6", "b", 0),
		"nan":     ok("c", "6", "b", math.NaN()),
		"inf":     ok("c", "6", "b", math.Inf(1)),
		"neg":     ok("c", "6", "b", -3),
	} {
		g := verdict(t, []Row{ok("c", "6", "a", 150), bad})
		if g.Pass || len(g.Missing) != 1 || math.IsNaN(g.Geomean) || math.Abs(g.Geomean-150) > 1e-9 {
			t.Errorf("%s: %+v", name, g)
		}
	}
}

func TestSummaryInformational(t *testing.T) {
	var out bytes.Buffer
	WriteSummary(&out, []Row{ok("c", "6", "a", 80)}, SummaryOpts{Info: true})
	s := out.String()
	if !strings.Contains(s, "REPORTED ONLY (noasm), not a gate") || strings.Contains(s, "PASS") || strings.Contains(s, "FAIL") {
		t.Errorf("informational summary:\n%s", s)
	}
}

func TestParseRows(t *testing.T) {
	rows, err := ParseRows(strings.NewReader("c\t6\tx/whole\t101.5%\nd\t9\ty/1M\t90\tmismatch\nc\t1\tz\t-\n\n"))
	if err != nil || len(rows) != 3 || rows[0].Pct != 101.5 || rows[0].Status != StatusOK || rows[1].Status != StatusMismatch || rows[2].Status != StatusMissing {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, bad := range []string{"c\t6\tx\n", "x\t6\ty\t1\n", "c\t6\ty\tabc\n", "c\t6\ty\t1\tbogus\n"} {
		if _, err := ParseRows(strings.NewReader(bad)); err == nil {
			t.Errorf("want error for %q", bad)
		}
	}
}

func TestStatCLI(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := statMain([]string{"median"}, strings.NewReader("3 1\n2\n"), &out, &errb); rc != 0 || strings.TrimSpace(out.String()) != "2" {
		t.Errorf("median rc=%d out=%q err=%q", rc, out.String(), errb.String())
	}
	if rc := statMain([]string{"median"}, strings.NewReader(""), &out, &errb); rc != 1 {
		t.Errorf("empty median rc=%d", rc)
	}
	if rc := statMain([]string{"median"}, strings.NewReader("1 x"), &out, &errb); rc != 1 {
		t.Errorf("bad median rc=%d", rc)
	}
	out.Reset()
	if rc := statMain([]string{"summary"}, strings.NewReader("c\t6\ta\t120\tmismatch\n"), &out, &errb); rc != 0 || !strings.Contains(out.String(), "FAIL (MISMATCH)") {
		t.Errorf("summary rc=%d out=%q", rc, out.String())
	}
	out.Reset()
	if rc := statMain([]string{"summary", "-info"}, strings.NewReader("c\t6\ta\t120\n"), &out, &errb); rc != 0 || !strings.Contains(out.String(), "not a gate") {
		t.Errorf("summary -info rc=%d out=%q", rc, out.String())
	}
	if rc := statMain([]string{"summary"}, strings.NewReader("garbage\n"), &out, &errb); rc != 1 {
		t.Errorf("bad summary rc=%d", rc)
	}
	if rc := statMain([]string{"nope"}, strings.NewReader(""), &out, &errb); rc != 2 {
		t.Errorf("unknown rc=%d", rc)
	}
}

// tinyInput is compressible but not trivial, > 1 block at block=1000.
func tinyInput() []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < 5000; i++ {
		b.WriteString("the quick brown fox ")
		b.WriteByte(byte('a' + i%26))
		b.WriteString(" jumps over the lazy dog\n")
	}
	return b.Bytes()
}

func TestAdapterRoundTrips(t *testing.T) {
	in := tinyInput()
	for name, ad := range map[string]Adapter{"stdlib": stdAdapter{}, "klauspost": kpAdapter{}} {
		for _, format := range []string{"raw", "gzip"} {
			for _, level := range []int{1, 6, 9} {
				for _, block := range []int{0, 1000} {
					b, err := prepare(ad, in, level, block, format, nil)
					if err == nil && b.zn >= len(in) {
						err = errors.New("no compression")
					}
					if err != nil {
						t.Errorf("%s %s L%d block=%d: %v", name, format, level, block, err)
					}
				}
			}
		}
	}
}

func TestUnsupportedLevels(t *testing.T) {
	for _, ad := range []Adapter{stdAdapter{}, kpAdapter{}} {
		for _, l := range []int{0, 10, 11, 12} {
			if _, err := ad.NewCompressor(l, "raw"); !errors.Is(err, ErrUnsupportedLevel) {
				t.Errorf("level %d: err=%v, want ErrUnsupportedLevel", l, err)
			}
		}
	}
}

func TestRunEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("takes ~0.6 s")
	}
	mbs, ratio, zn, err := run(stdAdapter{}, tinyInput(), 6, false, 1000, 0.1, 1, "gzip", nil, nil)
	if err != nil || mbs <= 0 || ratio <= 1 || zn <= 0 || zn >= len(tinyInput()) {
		t.Fatalf("mbs=%v ratio=%v err=%v", mbs, ratio, err)
	}
}

// Decompress contract: dst must be exactly the decoded size.
func TestDecompressExactSize(t *testing.T) {
	in := tinyInput()
	for name, ad := range map[string]Adapter{"stdlib": stdAdapter{}, "klauspost": kpAdapter{}} {
		for _, format := range []string{"raw", "gzip"} {
			c, err := ad.NewCompressor(6, format)
			if err != nil {
				t.Fatal(err)
			}
			z := make([]byte, len(in)+len(in)/2+1024)
			n, err := c.Compress(z, in)
			if err != nil {
				t.Fatal(err)
			}
			z = z[:n]
			d, err := ad.NewDecompressor(format)
			if err != nil {
				t.Fatal(err)
			}
			if m, err := d.Decompress(make([]byte, len(in)), z); err != nil || m != len(in) {
				t.Errorf("%s %s exact: m=%d err=%v", name, format, m, err)
			}
			if _, err := d.Decompress(make([]byte, len(in)-1), z); err == nil {
				t.Errorf("%s %s: dst one byte short must error", name, format)
			}
			if _, err := d.Decompress(make([]byte, len(in)+1), z); err == nil {
				t.Errorf("%s %s: dst one byte long must error", name, format)
			}
			// the decompressor is reusable after an error
			if m, err := d.Decompress(make([]byte, len(in)), z); err != nil || m != len(in) {
				t.Errorf("%s %s reuse: m=%d err=%v", name, format, m, err)
			}
			if _, err := c.Compress(make([]byte, 10), in); err == nil {
				t.Errorf("%s %s: too-small dst must error", name, format)
			}
		}
	}
}

// compare.sh -dry-run runs no benchmark: it must print the header and the
// commands and write a result file with the new header fields.
func TestCompareDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds gzbench")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ref/libdeflate")); err != nil {
		t.Skip("ref/libdeflate missing")
	}
	corpus := t.TempDir()
	if err := os.MkdirAll(filepath.Join(corpus, "silesia"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"xml", "reymont", "sao"} {
		if err := os.WriteFile(filepath.Join(corpus, "silesia", f), tinyInput(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outFile := filepath.Join(t.TempDir(), "dry.md")
	cmd := exec.Command("bash", filepath.Join(root, "bench/gzip/compare.sh"), "-dry-run", "-quick", "-noasm", "-format", "gzip",
		"-libs", "ld ours stdlib", "-label", "dry", "-o", outFile)
	cmd.Env = append(os.Environ(), "CORPUS="+corpus)
	log, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, log)
	}
	md, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	s := string(md) + string(log)
	for _, sub := range []string{"DRY RUN", "quick: yes", "files: xml", "-format gzip", "-tags noasm", "DRY: ", "gzbench"} {
		if !strings.Contains(s, sub) {
			t.Errorf("dry-run output missing %q:\n%s", sub, s)
		}
	}
	if strings.Contains(string(md), "PASS") || strings.Contains(string(md), "FAIL") {
		t.Errorf("dry run must not print a gate verdict:\n%s", md)
	}
}

// ours compresses and decompresses: -mode c and -mode d rows carry numbers for
// -format raw and gzip at every level 0..12 (nothing is n/a, both gates are
// evaluated), -mode d decodes the libdeflate streams (-zin) for both formats
// and -mode c compresses without -zin. Runs no benchmark.
func TestCompareDryRunOurs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds gzbench")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ref/libdeflate")); err != nil {
		t.Skip("ref/libdeflate missing")
	}
	corpus := t.TempDir()
	if err := os.MkdirAll(filepath.Join(corpus, "silesia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corpus, "silesia", "xml"), tinyInput(), 0o644); err != nil {
		t.Fatal(err)
	}
	dry := func(format, mode, levels string) (string, string) {
		outFile := filepath.Join(t.TempDir(), "dry.md")
		cmd := exec.Command("bash", filepath.Join(root, "bench/gzip/compare.sh"), "-dry-run", "-mode", mode, "-f", "xml xml:1000",
			"-l", levels, "-format", format, "-libs", "ld ours", "-prof", filepath.Join(t.TempDir(), "prof"), "-label", "dry", "-o", outFile)
		cmd.Env = append(os.Environ(), "CORPUS="+corpus)
		log, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dry run (%s, -mode %s) failed: %v\n%s", format, mode, err, log)
		}
		md, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(md), string(log)
	}
	for _, format := range []string{"raw", "gzip"} {
		md, log := dry(format, "both", "1 6")
		comp, dec, _ := strings.Cut(md, "## Decompression")
		for _, part := range []string{comp, dec} {
			if !strings.Contains(part, "| xml | 1 | whole | 100 | 100 | 100.0% |") {
				t.Errorf("-format %s: ours must have numbers in both modes:\n%s", format, md)
			}
		}
		// 2 files x 2 levels x 2 modes
		if n := strings.Count(md, "| 100.0% |"); n != 8 {
			t.Errorf("-format %s: want 8 ours-vs-C cells, got %d:\n%s", format, n, md)
		}
		for _, sub := range []string{"n/a (not ported)", "not ported yet", "NOT EVALUATED", "INCOMPLETE", "decode-only"} {
			if strings.Contains(md+log, sub) {
				t.Errorf("-format %s: unexpected %q:\n%s\n%s", format, sub, md, log)
			}
		}
		for _, sub := range []string{"Gates evaluated: Compress, Decompress.", "OVERALL: DRY RUN, no verdict;",
			"-cpuprofile ", "xml.whole.L1.c.cpu.prof", "xml.whole.L1.d.cpu.prof", "identity check (-mode d)", "=C (same stream)",
			"ours compresses and decompresses (raw and gzip, levels 0..12)"} {
			if !strings.Contains(md+log, sub) {
				t.Errorf("-format %s: missing %q:\n%s\n%s", format, sub, md, log)
			}
		}
		_, dlog := dry(format, "d", "1")
		if !strings.Contains(dlog, "-lib ours -mode d") || !strings.Contains(dlog, "-zin ") || !strings.Contains(dlog, "-format "+format) {
			t.Errorf("-format %s -mode d dry-run must show the ours command with -zin:\n%s", format, dlog)
		}
		_, clog := dry(format, "c", "1")
		if !strings.Contains(clog, "-lib ours -mode c") || strings.Contains(clog, "-zin ") {
			t.Errorf("-format %s -mode c dry-run must show the ours command without -zin:\n%s", format, clog)
		}
		// every level 0..12 is supported for ours (no exit 4, no n/a)
		md, _ = dry(format, "c", "0 1 2 3 4 5 6 7 8 9 10 11 12")
		if n := strings.Count(md, "| 100.0% |"); n != 26 || strings.Contains(md, "n/a") {
			t.Errorf("-format %s -mode c levels 0..12: want 26 ours-vs-C cells and no n/a, got %d:\n%s", format, n, md)
		}
	}
}
