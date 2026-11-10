package deflate

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// THE levels 5..9 identity check on the generated corpus, Silesia (whole
// files) and independent 1 MiB / 128 KiB / 16 KiB blocks of silesia.tar.
// -short: Cases(true), Silesia xml, and the first 2 MiB of the tar per
// block size (levels 8, 9: 1 MiB); the quick tier adds the quickSilesia prefixes.
func TestCompressLazyMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	diffLevelSuite(t, lazyLevels, func(name string) bool { return name == "xml" },
		func(level int) int {
			if level >= 8 {
				return 1 << 20
			}
			return 2 << 20
		}, func(level int) uint64 { return uint64(level) })
}

// lazyRandomInput is one random structured input of the lazy identity
// test: greedyRandomInput, or records whose matches sit at chain depths
// around the lazy levels' max_search_depth (and half and a quarter of it,
// the look-ahead depths), with lengths around their nice lengths.
func lazyRandomInput(r *rand.Rand, seed uint64, n int) []byte {
	if r.IntN(4) == 0 {
		return hcRecords(r, n, []int{8, 16, 18, 36, 50, 75, 100, 150, 300}[r.IntN(9)])
	}
	return greedyRandomInput(r, seed, n)
}

// 520 random structured inputs per level of random sizes 0..400000
// (-short: 40), plus every size 0..80
// and a few around the block limits.
func TestCompressLazyRandomMatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	count := 520
	if testing.Short() {
		count = 40
	}
	for _, level := range lazyLevels {
		t.Run(fmt.Sprintf("level%d", level), func(t *testing.T) {
			parallelGroup(t)
			r := rand.New(rand.NewPCG(2027, uint64(level)))
			var in []l1Input
			for i := range count {
				n := r.IntN(400001)
				if i%8 == 0 {
					n = r.IntN(2000)
				}
				seed := uint64(i) + 9000*uint64(level)
				in = append(in, l1Input{fmt.Sprintf("random/%d/%d", i, n), lazyRandomInput(r, seed, n)})
			}
			for n := 0; n <= 80; n++ {
				in = append(in, l1Input{fmt.Sprintf("structured-small/%d", n), structuredInput(uint64(n)+92000, n)})
			}
			for _, n := range []int{5000, 9999, 10000, 10001, 20001, 50000 * 3, 299999, 300000, 305000, 305001, 600000} {
				in = append(in, l1Input{fmt.Sprintf("structured-edge/%d", n), structuredInput(uint64(n)+1, n)})
			}
			n := diffLevel(t, level, in, true)
			t.Logf("level %d: %d inputs, %d bytes", level, len(in), n)
		})
	}
}
