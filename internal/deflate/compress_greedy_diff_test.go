package deflate

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// greedyLevels are the levels deflate_compress_greedy serves.
var greedyLevels = []int{2, 3, 4}

// diffLevel compresses every input at level with Go and libdeflate (both
// into a compress_bound buffer) and requires identical bytes, then checks
// the exact-size and one-byte-short buffers (diffRunBatch); the batches run
// as parallel cases (diffLevelPar). It returns the number of input bytes
// compared.
func diffLevel(t *testing.T, level int, inputs []l1Input, capChecks bool) int {
	t.Helper()
	return diffLevelPar(t, level, "", inputs, capChecks, uint64(level))
}

// THE levels 2..4 identity check on the generated corpus, Silesia (whole
// files) and independent 1 MiB / 128 KiB / 16 KiB blocks of silesia.tar.
// -short: Cases(true), Silesia xml, and the first 2 MiB of the tar per
// block size; the quick tier adds the quickSilesia prefixes.
func TestCompressGreedyMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	diffLevelSuite(t, greedyLevels, func(name string) bool { return name == "xml" },
		func(int) int { return 2 << 20 }, func(level int) uint64 { return uint64(level) })
}

// greedyRandomInput is one random structured input of the random identity
// test: structuredInput, the hc matchfinder stress data of
// hc_matchfinder_test.go, or a concatenation of differently distributed
// segments (block splitting).
func greedyRandomInput(r *rand.Rand, seed uint64, n int) []byte {
	switch r.IntN(4) {
	case 0, 1:
		return structuredInput(seed, n)
	case 2:
		return hcData(r, n)
	default:
		b := make([]byte, 0, n)
		for len(b) < n {
			seg := min(n-len(b), []int{4999, 5000, 5001, 9000, 20000, 1 + r.IntN(60000)}[r.IntN(6)])
			b = append(b, hcData(r, seg)...)
		}
		return b
	}
}

// 520 random structured inputs per level of random sizes 0..400000
// (-short: 40), plus every size 0..80 and a few around the block limits.
func TestCompressGreedyRandomMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	count := 520
	if testing.Short() {
		count = 40
	}
	for _, level := range greedyLevels {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			parallelGroup(t)
			r := rand.New(rand.NewPCG(2026, uint64(level)))
			var in []l1Input
			for i := range count {
				n := r.IntN(400001)
				if i%8 == 0 {
					n = r.IntN(2000)
				}
				seed := uint64(i) + 8000*uint64(level)
				in = append(in, l1Input{fmt.Sprintf("random/%d/%d", i, n), greedyRandomInput(r, seed, n)})
			}
			for n := 0; n <= 80; n++ {
				in = append(in, l1Input{fmt.Sprintf("structured-small/%d", n), structuredInput(uint64(n)+91000, n)})
			}
			for _, n := range []int{5000, 9999, 10000, 50000 * 3, 299999, 300000, 305000, 305001, 600000} {
				in = append(in, l1Input{fmt.Sprintf("structured-edge/%d", n), structuredInput(uint64(n), n)})
			}
			n := diffLevel(t, level, in, true)
			t.Logf("level %d: %d inputs, %d bytes", level, len(in), n)
		})
	}
}
