package zstd

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// trainXMLSamples returns 1000 samples of 1 KiB: the start of Silesia's
// xml. Without the corpus (CI fetches none) it falls back to 1 KiB slices
// of the gentrain samples (800 of them).
func trainXMLSamples(t testing.TB) [][]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia", "xml"))
	if err != nil || len(b) < 1000<<10 {
		b = slices.Concat(buildDictSamples(t)...)
	}
	var out [][]byte
	for i := 0; i+1024 <= len(b) && len(out) < 1000; i += 1024 {
		out = append(out, b[i:i+1024:i+1024])
	}
	return out
}

// trainSampleSets are the training sets of the differential tests.
func trainSampleSets(t testing.TB) []struct {
	name    string
	samples [][]byte
} {
	return []struct {
		name    string
		samples [][]byte
	}{
		{"gentrain", buildDictSamples(t)},
		{"xml1k", trainXMLSamples(t)},
	}
}

var trainDictSizes = []int{4 << 10, 16 << 10, 110 << 10}

// crefTrainArgs are the cref train-cover / train-fastcover arguments of p.
func crefTrainArgs(p CoverParams) []string {
	shrink := "0"
	if p.ShrinkDict {
		shrink = "1"
	}
	return []string{
		"-k", strconv.FormatUint(uint64(p.K), 10), "-d", strconv.FormatUint(uint64(p.D), 10),
		"-steps", strconv.FormatUint(uint64(p.Steps), 10), "-f", strconv.FormatUint(uint64(p.F), 10),
		"-accel", strconv.FormatUint(uint64(p.Accel), 10), "-split", strconv.FormatFloat(p.SplitPoint, 'g', -1, 64),
		"-shrink", shrink, "-shrink-reg", strconv.FormatUint(uint64(p.ShrinkDictMaxRegression), 10),
		"-level", strconv.Itoa(p.Level), "-dict-id", strconv.FormatUint(uint64(p.DictID), 10),
	}
}

// crefParams parses cref's PARAMS fields.
func crefParams(t testing.TB, m map[string]string) CoverParams {
	t.Helper()
	u := func(k string) uint32 {
		v, err := strconv.ParseUint(m[k], 10, 32)
		if err != nil {
			t.Fatalf("PARAMS %s=%q: %v", k, m[k], err)
		}
		return uint32(v)
	}
	split, err := strconv.ParseFloat(m["split"], 64)
	if err != nil {
		t.Fatal(err)
	}
	level, err := strconv.Atoi(m["level"])
	if err != nil {
		t.Fatal(err)
	}
	return CoverParams{K: u("k"), D: u("d"), Steps: u("steps"), F: u("f"), Accel: u("accel"), SplitPoint: split,
		ShrinkDict: u("shrink") != 0, ShrinkDictMaxRegression: u("shrinkReg"), Level: level, DictID: u("dictID")}
}

// sameParams fails unless got equals C's params (NaN split points equal).
func sameParams(t testing.TB, got, want CoverParams) {
	t.Helper()
	if math.IsNaN(got.SplitPoint) && math.IsNaN(want.SplitPoint) {
		got.SplitPoint, want.SplitPoint = 0, 0
	}
	if got != want {
		t.Fatalf("params %+v, C %+v", got, want)
	}
}

// trainCode is the C error code of a training error (0: none, -1: not a C
// error).
func trainCode(err error) int {
	if err == nil {
		return 0
	}
	var ce *buildDictError
	if errors.As(err, &ce) {
		return ce.code
	}
	return -1
}

// checkTrain compares one training call with cref: mode is train,
// train-cover or train-fastcover.
func checkTrain(t *testing.T, mode string, samples [][]byte, maxDict int, p CoverParams) []byte {
	t.Helper()
	var (
		got  []byte
		gotP CoverParams
		err  error
		args []string
	)
	switch mode {
	case "train":
		got, err = TrainDict(samples, maxDict)
	case "train-cover":
		got, gotP, err = TrainDictCover(samples, maxDict, p)
		args = crefTrainArgs(p)
	case "train-fastcover":
		got, gotP, err = TrainDictFastCover(samples, maxDict, p)
		args = crefTrainArgs(p)
	}
	want, wantP, cerr := creftest.TrainCRef(t, mode, samples, maxDict, args...)
	if cerr != nil || err != nil {
		if cerr == nil || trainCode(err) != cerr.Code {
			t.Fatalf("err %v, C %v", err, cerr)
		}
		return nil
	}
	sameDict(t, got, want)
	if mode != "train" {
		sameParams(t, gotP, crefParams(t, wantP))
	}
	return got
}

// TestTrainDictCref checks TrainDict (C's ZDICT_trainFromBuffer) against
// `cref train`.
func TestTrainDictCref(t *testing.T) {
	for _, set := range trainSampleSets(t) {
		for _, size := range trainDictSizes {
			t.Run(fmt.Sprintf("%s/%d", set.name, size), func(t *testing.T) {
				checkTrain(t, "train", set.samples, size, CoverParams{})
			})
		}
	}
}

// trainParamCases are fixed and optimized parameter sets for both trainers.
var trainParamCases = []struct {
	name  string
	mode  string
	p     CoverParams
	short bool // also run under -short
}{
	{"cover-fixed", "train-cover", CoverParams{K: 200, D: 8}, true},
	{"cover-fixed-d12-l1-id", "train-cover", CoverParams{K: 1000, D: 12, Level: 1, DictID: 7777, SplitPoint: 0.5}, false},
	{"cover-opt-k", "train-cover", CoverParams{D: 8, Steps: 4}, true},
	{"cover-opt-d-split", "train-cover", CoverParams{K: 300, SplitPoint: 0.8, Level: 7}, false},
	{"fastcover-fixed", "train-fastcover", CoverParams{K: 200, D: 8}, true},
	{"fastcover-fixed-f-accel", "train-fastcover", CoverParams{K: 500, D: 6, F: 18, Accel: 3, Level: 11}, false},
	{"fastcover-opt", "train-fastcover", CoverParams{Steps: 4, ShrinkDict: true, ShrinkDictMaxRegression: 3}, true},
	{"fastcover-opt-split1", "train-fastcover", CoverParams{D: 8, Steps: 6, F: 16, Accel: 2, SplitPoint: 1, Level: -1, DictID: 1}, false},
}

// TestTrainDictCoverCref checks TrainDictCover and TrainDictFastCover, fixed
// and optimized, against `cref train-cover` / `train-fastcover`: the
// dictionary and the returned parameters.
func TestTrainDictCoverCref(t *testing.T) {
	for _, set := range trainSampleSets(t) {
		for _, size := range trainDictSizes {
			for _, c := range trainParamCases {
				t.Run(fmt.Sprintf("%s/%d/%s", set.name, size, c.name), func(t *testing.T) {
					if testing.Short() && !c.short {
						t.Skip("short")
					}
					checkTrain(t, c.mode, set.samples, size, c.p)
				})
			}
		}
	}
}

// TestTrainDictCoverDefaults runs COVER's full default search (d 6 and 8,
// 40 steps of k: 82 dictionaries) once.
func TestTrainDictCoverDefaults(t *testing.T) {
	if testing.Short() {
		t.Skip("82 trainings")
	}
	checkTrain(t, "train-cover", buildDictSamples(t), 16<<10, CoverParams{})
}

// TestTrainDictLargeSamples trains on samples of 160 KiB: compressing
// them for the search goes through C's other start, loading the dictionary
// into the context (a sample of 128 KiB or more and of 6 times the
// dictionary or more), and finalizeDictionary cuts them to a block.
func TestTrainDictLargeSamples(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "bench", "corpus", "silesia", "xml"))
	if err != nil {
		b = slices.Concat(buildDictSamples(t)...)
	}
	var samples [][]byte
	for i := 0; i+160<<10 <= len(b) && len(samples) < 8; i += 160 << 10 {
		samples = append(samples, b[i:i+160<<10])
	}
	if len(samples) < 5 {
		t.Fatalf("%d samples", len(samples))
	}
	// At 32 KiB, 160 KiB is below 6 times the dictionary: the CDict's
	// tables again.
	for _, size := range []int{4 << 10, 16 << 10, 32 << 10} {
		for _, lv := range []int{1, 3, 7, 11} {
			t.Run(fmt.Sprintf("%d/L%d", size, lv), func(t *testing.T) {
				checkTrain(t, "train-cover", samples, size, CoverParams{D: 8, Steps: 2, Level: lv})
				checkTrain(t, "train-fastcover", samples, size, CoverParams{D: 8, Steps: 2, SplitPoint: 1, Level: lv})
			})
		}
	}
}

// TestTrainDictDeterministic checks that the parameter search gives the
// same dictionary and parameters with one goroutine and with many.
func TestTrainDictDeterministic(t *testing.T) {
	samples := buildDictSamples(t)
	type result struct {
		dict []byte
		p    CoverParams
	}
	run := func(procs int) (r [2]result) {
		defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))
		var err error
		if r[0].dict, r[0].p, err = TrainDictCover(samples, 8<<10, CoverParams{D: 6, Steps: 12}); err != nil {
			t.Fatal(err)
		}
		if r[1].dict, r[1].p, err = TrainDictFastCover(samples, 8<<10, CoverParams{Steps: 12}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	one := run(1)
	for _, n := range []int{2, 7, max(runtime.NumCPU(), 8)} {
		many := run(n)
		for i := range one {
			if !bytes.Equal(one[i].dict, many[i].dict) || one[i].p != many[i].p {
				t.Fatalf("GOMAXPROCS %d: trainer %d differs from GOMAXPROCS 1 (%+v vs %+v)", n, i, many[i].p, one[i].p)
			}
		}
	}
}

// TestTrainDictUsable checks that trained dictionaries work with our
// Encoder and Decoder and with the zstd program (-D), both ways.
func TestTrainDictUsable(t *testing.T) {
	samples := buildDictSamples(t)[:190]
	cli := creftest.ZstdCLI(t)
	dict, err := TrainDict(samples, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	cover, _, err := TrainDictCover(samples, 16<<10, CoverParams{K: 200, D: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range [][]byte{dict, cover} {
		roundTrips(t, cli, d, SpeedDefault, usableSrcs(t))
		roundTrips(t, cli, d, SpeedBestCompression, usableSrcs(t))
	}
}

// trainDegenerate are degenerate and invalid inputs: each must give C's
// error code or C's dictionary.
func trainDegenerate(t testing.TB) []struct {
	name    string
	samples [][]byte
	maxDict int
	p       CoverParams
} {
	gen := buildDictSamples(t)
	same := make([][]byte, 50)
	for i := range same {
		same[i] = bytes.Repeat([]byte("abc"), 100)
	}
	tiny := make([][]byte, 300)
	for i := range tiny {
		tiny[i] = []byte{byte(i), byte(i >> 3)}
	}
	manyTiny := make([][]byte, 30000)
	for i := range manyTiny {
		manyTiny[i] = gen[i%200][i%97 : i%97+i%4]
	}
	ones := make([][]byte, 40)
	for i := range ones {
		ones[i] = []byte{'q'}
	}
	// 7 training samples smaller than a dmer in total, then large test
	// samples (C reads out of bounds or divides by zero here).
	smallTrain := append(slices.Repeat([][]byte{{1}}, 7), gen[:3]...)
	return []struct {
		name    string
		samples [][]byte
		maxDict int
		p       CoverParams
	}{
		{"none", nil, 4096, CoverParams{}},
		{"empty", make([][]byte, 20), 4096, CoverParams{}},
		{"four", gen[:4], 4096, CoverParams{}},
		{"five", gen[:5], 4096, CoverParams{}},
		{"seven", gen[:7], 4096, CoverParams{}},
		{"identical", same, 4096, CoverParams{}},
		{"tiny", tiny, 4096, CoverParams{}},
		{"single-bytes", ones, 4096, CoverParams{}},
		{"many-tiny", manyTiny, 4096, CoverParams{}},
		{"total-below-dict", gen[:10], 64 << 10, CoverParams{}},
		{"dict-255", gen[:20], 255, CoverParams{}},
		{"dict-256", gen[:20], 256, CoverParams{}},
		{"k-above-dict", gen[:20], 4096, CoverParams{K: 5000, D: 8}},
		{"d-above-k", gen[:20], 4096, CoverParams{K: 6, D: 8}},
		{"k-below-d-opt", gen[:20], 4096, CoverParams{K: 7}},
		{"split-above-1", gen[:20], 4096, CoverParams{SplitPoint: 1.5}},
		{"split-nan", gen[:20], 4096, CoverParams{D: 8, Steps: 2, SplitPoint: math.NaN()}},
		{"split-negative", gen[:20], 4096, CoverParams{D: 6, Steps: 2, SplitPoint: -3}},
		{"accel-11", gen[:20], 4096, CoverParams{Accel: 11}},
		{"f-32", gen[:20], 4096, CoverParams{K: 100, D: 8, F: 32}},
		{"f-32-search", gen[:40], 4096, CoverParams{D: 8, Steps: 2, F: 32}},
		{"f-32-search-d", gen[:40], 4096, CoverParams{K: 200, F: 32}},
		{"d-7", gen[:20], 4096, CoverParams{K: 100, D: 7}},
		{"d-huge", gen[:20], 4096, CoverParams{K: 1 << 30, D: 1 << 29}},
		{"small-train", smallTrain, 4096, CoverParams{}},
		{"small-train-cover", smallTrain, 4096, CoverParams{K: 100, D: 8, SplitPoint: 0.7}},
		{"level-12", gen[:30], 4096, CoverParams{K: 100, D: 8, Level: 12}},
		{"level-neg", gen[:30], 4096, CoverParams{D: 8, Steps: 2, Level: -5}},
	}
}

// TestTrainDictDegenerate checks degenerate and invalid inputs against
// cref with every trainer (cases where C crashes only need a Go error).
func TestTrainDictDegenerate(t *testing.T) {
	for _, c := range trainDegenerate(t) {
		for _, mode := range []string{"train", "train-cover", "train-fastcover"} {
			if mode == "train" && c.p != (CoverParams{}) {
				continue
			}
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				checkTrainOrCrash(t, mode, c.samples, c.maxDict, c.p)
			})
		}
	}
}

// checkTrainOrCrash is checkTrain, except that where C crashes (undefined
// behaviour) Go must return an error marked undefined, or C's malloc
// failure code where Go guards it up front.
func checkTrainOrCrash(t *testing.T, mode string, samples [][]byte, maxDict int, p CoverParams) {
	t.Helper()
	var args []string
	if mode != "train" {
		args = crefTrainArgs(p)
	}
	// F above 31 makes C allocate gigabytes: on a machine without the
	// memory, C's malloc fails (a plain error, not a crash) where Go refuses
	// the parameters up front. Both are errors; the codes may differ.
	if _, _, cerr := creftest.TrainCRef(t, mode, samples, maxDict, args...); cerr != nil &&
		(cerr.Code == -2 || (p.F >= 32 && cerr.Code == cErrMemoryAllocation)) {
		var err error
		switch mode {
		case "train":
			_, err = TrainDict(samples, maxDict)
		case "train-cover":
			_, _, err = TrainDictCover(samples, maxDict, p)
		default:
			_, _, err = TrainDictFastCover(samples, maxDict, p)
		}
		var ce *buildDictError
		if cerr.Code != -2 {
			if err == nil {
				t.Fatalf("C fails (%s), Go returned no error", cerr.Name)
			}
			return
		}
		if !errors.As(err, &ce) || !(ce.undefined || ce.code == cErrMemoryAllocation) {
			t.Fatalf("C crashes (%s), Go returned %v, want an undefined-behaviour error", cerr.Name, err)
		}
		return
	}
	checkTrain(t, mode, samples, maxDict, p)
}

// TestTrainDictLevelRefused checks the extension limit: levels whose
// parameters need a strategy this port lacks (btultra at C level 13 for
// 4 KiB samples, btultra2 at 22) fail with an error marked not ported,
// from the finalize step (fixed parameters) and from the search.
func TestTrainDictLevelRefused(t *testing.T) {
	samples := buildDictSamples(t)[:30]
	for _, lv := range []int{13, 22} {
		for _, p := range []CoverParams{{K: 100, D: 8, Level: lv}, {D: 8, Steps: 2, Level: lv}} {
			_, _, err := TrainDictCover(samples, 4096, p)
			_, _, err2 := TrainDictFastCover(samples, 4096, p)
			for _, e := range []error{err, err2} {
				var ce *buildDictError
				if !errors.As(e, &ce) || !ce.notPorted || ce.code != int(dErrGeneric) {
					t.Fatalf("level %d %+v: err = %v, want the not-ported refusal", lv, p, e)
				}
			}
		}
	}
}

// TestTrainDictNegativeSize checks the Go-only error for a negative size.
func TestTrainDictNegativeSize(t *testing.T) {
	if _, err := TrainDict(buildDictSamples(t)[:10], -1); err == nil || trainCode(err) != -1 {
		t.Fatalf("err = %v, want a plain error", err)
	}
}

// FuzzTrainDict trains on arbitrary samples (split from blob at the cut
// lengths in cuts) with a trainer and parameters picked by mode, and a
// level and a maxDict above 64 KiB picked by sel. Nothing may panic; the
// dictionary and parameters, or the C error code, must equal cref's
// (where C crashes, Go must return an error; at levels 13 and up, Go may
// refuse as not ported). F and K stay small: large ones take gigabytes.
func FuzzTrainDict(f *testing.F) {
	gen := buildDictSamples(f)
	f.Add(uint8(0), uint8(0), uint16(4096), slices.Concat(gen[:20]...), []byte{64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64})
	f.Add(uint8(1), uint8(5), uint16(300), bytes.Repeat([]byte{'x'}, 5000), []byte{5, 5, 5, 5, 5, 5, 5, 5})
	f.Add(uint8(2), uint8(0), uint16(1024), []byte{}, []byte{0, 0, 0, 0, 0, 0})
	f.Add(uint8(3), uint8(9), uint16(2000), []byte("abcdefghijklmnop"), []byte{0, 0, 0, 0, 0, 0, 0, 1})
	f.Add(uint8(4), uint8(24), uint16(256), slices.Concat(gen[:3]...), []byte{1, 1, 1, 1, 1, 1, 1})
	f.Add(uint8(5), uint8(3), uint16(9000), slices.Concat(gen[:6]...), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(uint8(6), uint8(6), uint16(255), []byte{}, []byte{})
	f.Add(uint8(7), uint8(7), uint16(65535), slices.Concat(gen[:40]...), []byte{})
	f.Fuzz(func(t *testing.T, mode, sel uint8, maxDict uint16, blob, cuts []byte) {
		if len(blob) > 256<<10 || len(cuts) > 2048 {
			return
		}
		var samples [][]byte
		for _, c := range cuts {
			n := min(int(c)*16, len(blob))
			samples = append(samples, blob[:n:n])
			blob = blob[n:]
		}
		if len(blob) > 0 {
			samples = append(samples, blob)
		}
		var p CoverParams
		trainer := []string{"train", "train-cover", "train-fastcover"}[int(mode&3)%3]
		if mode&4 != 0 {
			p.K = uint32(mode>>3)*40 + 8
			p.D = []uint32{6, 8, 0, 16}[mode>>6]
		} else {
			p.Steps = uint32(mode>>3)%4 + 1
		}
		if mode&32 != 0 {
			p.SplitPoint = 0.6
			p.F = 12
			p.Accel = uint32(mode>>6) + 1
		}
		p.Level = []int{0, 1, 3, 7, 11, 13, 19, -2}[sel&7]
		size := int(maxDict) + int(sel>>3&3)<<16
		if trainer == "train" {
			p = CoverParams{}
		}
		if p.Level >= 13 {
			var err error
			if trainer == "train-cover" {
				_, _, err = TrainDictCover(samples, size, p)
			} else {
				_, _, err = TrainDictFastCover(samples, size, p)
			}
			var ce *buildDictError
			if errors.As(err, &ce) && ce.notPorted {
				// Only the two refusals may skip C: a missing strategy
				// (errStrategyUnsupported) and long-distance matching.
				if !strings.Contains(ce.err, errStrategyUnsupported.Error()) && !strings.Contains(ce.err, "long-distance matching") {
					t.Fatalf("not-ported refusal from an unexpected path: %v", err)
				}
				return
			}
		}
		checkTrainOrCrash(t, trainer, samples, size, p)
	})
}
