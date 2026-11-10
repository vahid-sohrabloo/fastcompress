package deflate

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// nearOptimalLevels are the levels deflate_compress_near_optimal serves.
var nearOptimalLevels = []int{10, 11, 12}

// nearOptimalShortSilesia is the fixed Silesia subset of the -short run
// (level 12 runs at a few MB/s in C and in Go).
var nearOptimalShortSilesia = map[string]bool{"xml": true}

// THE levels 10..12 identity check on the generated corpus, Silesia (whole
// files) and independent 1 MiB / 128 KiB / 16 KiB blocks of silesia.tar.
// -short: Cases(true), Silesia xml, and the first 1 MiB of the tar per
// block size (level 12: 512 KiB); the quick tier adds the quickSilesia prefixes.
func TestCompressNearOptimalMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	diffLevelSuite(t, nearOptimalLevels, func(name string) bool { return nearOptimalShortSilesia[name] },
		func(level int) int {
			if level == 12 {
				return 512 << 10
			}
			return 1 << 20
		}, func(level int) uint64 { return uint64(level) })
}

// nearOptimalRandomInput is one random structured input of the
// near-optimal identity test: greedyRandomInput, the bt matchfinder stress
// data, or periodic data with sparse mutations (many equal-cost paths).
func nearOptimalRandomInput(r *rand.Rand, seed uint64, n int) []byte {
	switch r.IntN(4) {
	case 0:
		return btData(r, n)
	case 1:
		p := 1 + r.IntN([]int{3, 30, 300, 3000}[r.IntN(4)])
		b := make([]byte, n)
		for i := range b {
			if i < p {
				b[i] = byte(r.IntN(1 + r.IntN(256)))
			} else {
				b[i] = b[i-p]
			}
			if r.IntN(1+r.IntN(2000)) == 0 {
				b[i] ^= byte(1 + r.IntN(255))
			}
		}
		return b
	default:
		return greedyRandomInput(r, seed, n)
	}
}

// 320 random structured inputs per level of random sizes 0..400000
// (-short: 30, up to 100000), plus every size 0..80 and a few around the
// block limits.
func TestCompressNearOptimalRandomMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	count, maxN := 320, 400000
	if testing.Short() {
		count, maxN = 30, 100000
	}
	for _, level := range nearOptimalLevels {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			parallelGroup(t)
			r := rand.New(rand.NewPCG(2028, uint64(level)))
			var in []l1Input
			for i := range count {
				n := r.IntN(maxN + 1)
				if i%8 == 0 {
					n = r.IntN(2000)
				}
				seed := uint64(i) + 10000*uint64(level)
				in = append(in, l1Input{fmt.Sprintf("random/%d/%d", i, n), nearOptimalRandomInput(r, seed, n)})
			}
			for n := 0; n <= 80; n++ {
				in = append(in, l1Input{fmt.Sprintf("structured-small/%d", n), structuredInput(uint64(n)+93000, n)})
			}
			edges := []int{1000, 1001, 5000, 9999, 10000, 10001, 32768, 32769, 299999, 300000, 305000, 305001}
			if testing.Short() {
				edges = edges[:8]
			}
			for _, n := range edges {
				in = append(in, l1Input{fmt.Sprintf("structured-edge/%d", n), structuredInput(uint64(n)+2, n)})
			}
			in = append(in, l1Input{"cache-exact/2/200000", cacheExactInput()})
			n := diffLevelPar(t, level, "", in, true, uint64(level))
			t.Logf("level %d: %d inputs, %d bytes", level, len(in), n)
		})
	}
}
