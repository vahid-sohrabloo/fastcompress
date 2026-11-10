package deflate

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// Parallel differential cases and the test tiers (scripts/README-deflate-tests.md).
//
// The big differential tests run their independent cases (a level, a Silesia
// file, a batch of inputs) as parallel subtests inside their own top-level
// test, so a top-level test still finishes before the next one starts: the
// tests that switch the process-global assembly mode (cpu.SetAsmMode,
// withDeflateAsm, cpu.DisableBMI2) never overlap a parallel case. At most
// testPar leaf cases run at once, whatever go test -parallel says.
//
// FASTCOMPRESS_TEST_PAR sets testPar (default 4). 1 (or 0) is the serial mode:
// no t.Parallel at all and the cases run in the old order, each test with a
// single compressor as before. Under -race testPar is 1 (the race lane keeps
// its trimmed, serial shape).

// testPar is the maximum number of parallel leaf cases.
var testPar = func() int {
	if raceEnabled {
		return 1
	}
	if v := os.Getenv("FASTCOMPRESS_TEST_PAR"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return max(n, 1)
		}
		panic("FASTCOMPRESS_TEST_PAR=" + v + ": want a number")
	}
	return 4
}()

var testSlots = make(chan struct{}, testPar)

// parallelGroup marks t, an inner node whose subtests are parallelCase
// leaves (or further groups), as parallel. It holds no slot.
func parallelGroup(t *testing.T) {
	if testPar > 1 {
		t.Parallel()
	}
}

// parallelCase marks t, a leaf, as parallel and holds one of the testPar
// slots until t's function returns. A leaf must not start parallel subtests
// (it would keep its slot while they wait for one).
func parallelCase(t *testing.T) {
	if testPar <= 1 {
		return
	}
	t.Parallel()
	testSlots <- struct{}{}
	t.Cleanup(func() { <-testSlots })
}

// tierQuick reports the quick tier (FASTCOMPRESS_TEST_TIER=quick, or fast, with
// -short): the level differential tests add the quickSilesia prefixes to
// their -short inputs.
func tierQuick() bool {
	v := os.Getenv("FASTCOMPRESS_TEST_TIER")
	return testing.Short() && (v == "quick" || v == "fast")
}

// quickSilesia is the quick tier's sample of Silesia: the first 1 MiB (512 KiB at
// levels 10..12) of 7 files of different kinds, besides the whole xml that
// -short always has (the tar prefix of -short is dickens text).
var quickSilesia = []string{"mozilla", "mr", "nci", "ooffice", "osdb", "sao", "x-ray"}

// silesiaInput is one Silesia input of a level differential test: the file,
// whole (limit 0) or its first limit bytes.
type silesiaInput struct {
	name  string
	file  deflatetest.File
	limit int
}

// silesiaInputs returns the Silesia inputs at level: every whole file; in
// -short only the files short accepts (whole), plus quickSilesia in the quick tier.
func silesiaInputs(t *testing.T, level int, short func(name string) bool) []silesiaInput {
	t.Helper()
	var in []silesiaInput
	for _, f := range deflatetest.Silesia(t) {
		if !testing.Short() || short(f.Name) {
			in = append(in, silesiaInput{f.Name, f, 0})
		}
	}
	if tierQuick() {
		limit := 1 << 20
		if level >= 10 {
			limit = 512 << 10
		}
		for _, f := range deflatetest.Silesia(t) {
			for _, s := range quickSilesia {
				if f.Name == s && !short(f.Name) {
					in = append(in, silesiaInput{fmt.Sprintf("%s[:%d]", f.Name, limit), f, limit})
				}
			}
		}
	}
	return in
}

// load reads the input (inside the leaf that compares it).
func (s silesiaInput) load(t *testing.T) []byte {
	b := s.file.Load(t)
	if s.limit > 0 && len(b) > s.limit {
		b = b[:s.limit:s.limit]
	}
	return b
}

// diffShared holds the inputs that the parallel cases of one top-level test
// share read-only (the compressors never write their input): generated or
// loaded once instead of once per level.
type diffShared struct {
	casesOnce sync.Once
	cases     []l1Input
	tarOnce   sync.Once
	tar       []byte
}

// corpusCases returns Cases(true) and, without -short, the cases of
// Cases(false) not in it (the "cases" input list of the level tests).
func (sh *diffShared) corpusCases() []l1Input {
	sh.casesOnce.Do(func() {
		short := map[string]bool{}
		for _, cs := range deflatetest.Cases(true) {
			sh.cases = append(sh.cases, l1Input{cs.Name, cs.Data})
			short[cs.Name] = true
		}
		if !testing.Short() {
			for _, cs := range deflatetest.Cases(false) {
				if !short[cs.Name] {
					sh.cases = append(sh.cases, l1Input{cs.Name, cs.Data})
				}
			}
		}
	})
	return sh.cases
}

// silesiaTar returns the whole silesia.tar, loaded once.
func (sh *diffShared) silesiaTar(t *testing.T) []byte {
	t.Helper()
	f := deflatetest.SilesiaTar(t)
	sh.tarOnce.Do(func() { sh.tar = f.Load(t) })
	if sh.tar == nil {
		t.Fatal("silesia.tar failed to load (see the first case that loaded it)")
	}
	return sh.tar
}

// diffBatch is one ldref process worth of inputs of a differential run.
type diffBatch struct {
	inputs []l1Input
	left   int // inputs after this batch: the seed of its random capacity
}

// diffPlan splits inputs into batches of at most l1BatchBytes (at least one
// input each), as diffLevel always did.
func diffPlan(inputs []l1Input) []diffBatch {
	var bs []diffBatch
	for len(inputs) > 0 {
		k, sz := 0, 0
		for k < len(inputs) && (k == 0 || sz+len(inputs[k].data) <= l1BatchBytes) {
			sz += len(inputs[k].data)
			k++
		}
		bs = append(bs, diffBatch{inputs[:k], len(inputs) - k})
		inputs = inputs[k:]
	}
	return bs
}

// diffRunBatch compresses every input of b at c's level with Go and
// libdeflate (both into a compress_bound buffer) and requires identical
// bytes; on a mismatch the first differing block (walkBlocks) is reported
// before the byte offset. Then, on the Go side for every input: the
// exact-size buffer gives the same bytes and one byte less gives 0; with
// capChecks, the same on the C side (plus a random size in between, from
// PCG(b.left, seed2)) for inputs of at most 1 MiB.
func diffRunBatch(t *testing.T, c *Compressor, b diffBatch, capChecks bool, seed2 uint64) {
	t.Helper()
	level := c.Level()
	recs := make([]ldBatchRec, len(b.inputs))
	for i, in := range b.inputs {
		recs[i] = ldBatchRec{level, -1, in.data}
	}
	wants := ldCompressBatch(t, recs)
	var capRecs []ldBatchRec
	var capWant [][]byte
	r := rand.New(rand.NewPCG(uint64(b.left), seed2))
	for i, in := range b.inputs {
		want := wants[i]
		if want == nil {
			t.Fatalf("%s: libdeflate failed to compress into its bound", in.name)
		}
		got := compressAt(t, c, in.data)
		if !bytes.Equal(got, want) {
			t.Fatalf("level %d %s (%d bytes): go %d bytes, libdeflate %d bytes; %s; first differing byte %d",
				level, in.name, len(in.data), len(got), len(want), firstBlockDiff(got, want),
				firstByteDiff(got, want))
		}
		dst := make([]byte, len(want))
		if n := c.Compress(dst, in.data); n != len(want) || !bytes.Equal(dst, want) {
			t.Fatalf("level %d %s: exact-size buffer gave %d bytes, want %d", level, in.name, n, len(want))
		}
		if n := c.Compress(dst[:len(want)-1], in.data); n != 0 {
			t.Fatalf("level %d %s: one byte short gave %d, want 0", level, in.name, n)
		}
		if capChecks && len(in.data) <= 1<<20 {
			capRecs = append(capRecs, ldBatchRec{level, len(want), in.data},
				ldBatchRec{level, len(want) - 1, in.data}, ldBatchRec{level, r.IntN(len(want)), in.data})
			capWant = append(capWant, want, nil, nil)
		}
	}
	if len(capRecs) > 0 {
		for i, got := range ldCompressBatch(t, capRecs) {
			if !bytes.Equal(got, capWant[i]) || (got == nil) != (capWant[i] == nil) {
				t.Fatalf("level %d: libdeflate with a %d-byte buffer: %d bytes, want %d (nil=%v)", level,
					capRecs[i].capBytes, len(got), len(capWant[i]), capWant[i] == nil)
			}
		}
	}
}

// diffLevelPar is diffLevel with each batch a parallel case (subtests
// "<name>batch<i>", a compressor each); serially (testPar 1) it runs the
// batches in place with one compressor, exactly as diffLevel. seed2 is the
// second PCG seed of the random capacity (diffLevel: the level; diffLevel1:
// 99). It returns the number of input bytes (compared once the cases end).
func diffLevelPar(t *testing.T, level int, name string, inputs []l1Input, capChecks bool, seed2 uint64) int {
	t.Helper()
	total := 0
	for _, in := range inputs {
		total += len(in.data)
	}
	bs := diffPlan(inputs)
	if testPar <= 1 {
		c := mustCompressor(t, level)
		for _, b := range bs {
			diffRunBatch(t, c, b, capChecks, seed2)
		}
		return total
	}
	for i, b := range bs {
		t.Run(fmt.Sprintf("%sbatch%d", name, i), func(t *testing.T) {
			parallelCase(t)
			diffRunBatch(t, mustCompressor(t, level), b, capChecks, seed2)
		})
	}
	return total
}

// diffLevelSuite is the body of the level identity tests (levels 1..12 vs
// libdeflate): per level, the generated corpus, Silesia (silesiaInputs; in
// -short whole only the files shortSilesia accepts) and independent 1 MiB /
// 128 KiB / 16 KiB blocks of silesia.tar (in -short its first
// shortTar(level) bytes), every level, file and batch a parallel case.
func diffLevelSuite(t *testing.T, levels []int, shortSilesia func(string) bool, shortTar func(level int) int, seed2 func(level int) uint64) {
	t.Helper()
	sh := &diffShared{}
	for _, level := range levels {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			parallelGroup(t)
			t.Run("cases", func(t *testing.T) {
				parallelGroup(t)
				in := sh.corpusCases()
				n := diffLevelPar(t, level, "", in, true, seed2(level))
				t.Logf("%d inputs, %d bytes", len(in), n)
			})
			t.Run("silesia", func(t *testing.T) {
				parallelGroup(t)
				files := silesiaInputs(t, level, shortSilesia)
				for _, f := range files {
					t.Run(f.name, func(t *testing.T) {
						parallelCase(t)
						data := f.load(t)
						diffRunBatch(t, mustCompressor(t, level), diffBatch{[]l1Input{{f.name, data}}, 0}, false, seed2(level))
					})
				}
				t.Logf("%d files", len(files))
			})
			t.Run("silesia-tar-blocks", func(t *testing.T) {
				parallelGroup(t)
				tar := sh.silesiaTar(t)
				if testing.Short() {
					tar = tar[:shortTar(level)]
				}
				for _, size := range []int{1 << 20, 128 << 10, 16 << 10} {
					var in []l1Input
					for i, b := range deflatetest.Blocks(tar, size) {
						in = append(in, l1Input{fmt.Sprintf("silesia.tar/%d/%d", size, i), b})
					}
					n := diffLevelPar(t, level, fmt.Sprintf("%d-", size), in, size <= 128<<10, seed2(level))
					t.Logf("block size %d: %d blocks, %d bytes", size, len(in), n)
				}
			})
		})
	}
}
