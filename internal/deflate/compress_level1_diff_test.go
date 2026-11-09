package deflate

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// ldBatchRec is one `ldref compress-batch` record: compress data at level
// into a capBytes-byte buffer (-1: libdeflate_deflate_compress_bound).
type ldBatchRec struct {
	level    int
	capBytes int
	data     []byte
}

// ldCompressBatch runs the records through one ldref process and returns
// libdeflate's outputs (nil where libdeflate_deflate_compress returned 0).
func ldCompressBatch(t testing.TB, recs []ldBatchRec) [][]byte {
	t.Helper()
	var in []byte
	for _, r := range recs {
		c := uint32(0xFFFFFFFF)
		if r.capBytes >= 0 {
			c = uint32(r.capBytes)
		}
		in = append(in, byte(r.level))
		in = binary.LittleEndian.AppendUint32(in, c)
		in = binary.LittleEndian.AppendUint32(in, uint32(len(r.data)))
		in = append(in, r.data...)
	}
	out := creftest.LDRun(t, in, "compress-batch")
	res := make([][]byte, len(recs))
	for i := range recs {
		if len(out) < 4 {
			t.Fatalf("compress-batch: output truncated at record %d", i)
		}
		n := int(binary.LittleEndian.Uint32(out))
		if len(out)-4 < n {
			t.Fatalf("compress-batch: record %d: %d bytes announced, %d left", i, n, len(out)-4)
		}
		if n > 0 {
			res[i] = out[4 : 4+n : 4+n]
		}
		out = out[4+n:]
	}
	if len(out) != 0 {
		t.Fatalf("compress-batch: %d trailing output bytes", len(out))
	}
	return res
}

type l1Input struct {
	name string
	data []byte
}

// l1BatchBytes bounds the input bytes sent to one ldref process.
const l1BatchBytes = 48 << 20

func firstByteDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// THE level 1 identity check on the generated corpus, Silesia (whole files)
// and independent 1 MiB / 128 KiB / 16 KiB blocks of silesia.tar. -short:
// Cases(true), Silesia xml, and the first 2 MiB of the tar in each block
// size; the quick tier adds the quickSilesia prefixes.
func TestCompressLevel1MatchLibdeflate(t *testing.T) {
	// Single-goroutine differential vs libdeflate: nothing for -race to find.
	if raceEnabled {
		t.Skip("differential vs libdeflate: no goroutines, runs in the normal lane")
	}
	creftest.LDBin(t)
	diffLevelSuite(t, []int{1}, func(name string) bool { return name == "xml" },
		func(int) int { return 2 << 20 }, func(int) uint64 { return 99 })
}

// 520 random structured inputs of random sizes 0..400000 (-short: 40), plus
// every size 0..80 and a few around the block limits.
func TestCompressLevel1RandomMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	count := 520
	if testing.Short() {
		count = 40
	}
	r := rand.New(rand.NewPCG(2026, 11))
	var in []l1Input
	for i := range count {
		n := r.IntN(400001)
		if i%8 == 0 {
			n = r.IntN(2000)
		}
		in = append(in, l1Input{fmt.Sprintf("structured/%d/%d", i, n), structuredInput(uint64(i)+5000, n)})
	}
	for n := 0; n <= 80; n++ {
		in = append(in, l1Input{fmt.Sprintf("structured-small/%d", n), structuredInput(uint64(n)+90000, n)})
	}
	for _, n := range []int{65535, 70534, 70535, 70536, 65535 + 258, 2*65535 + 5000} {
		in = append(in, l1Input{fmt.Sprintf("structured-edge/%d", n), structuredInput(uint64(n), n)})
	}
	n := diffLevelPar(t, 1, "", in, true, 99)
	t.Logf("%d inputs, %d bytes", len(in), n)
}

// The final ht_matchfinder hash table after a level 1 compression equals
// libdeflate's c->p.f.ht_mf (ldref dump-htmf): a stage-level check of
// insertion, bucket shifting and window rebasing.
func TestHtMatchfinderStateMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	c := mustCompressor(t, 1)
	inputs := []l1Input{{"structured/52", structuredInput(1, 52)}, {"structured/100000", structuredInput(2, 100000)}}
	for _, cs := range deflatetest.Cases(true) {
		switch cs.Name {
		case "dense4", "ht-collide/3", "ht-collide/8", "rebase-align", "dist-32768", "size-text/65537",
			"edge-matches/65535", "edge-matches/65537", "zero", "random", "eof-same/65792":
			inputs = append(inputs, l1Input{cs.Name, cs.Data})
		}
	}
	if len(inputs) != 13 {
		t.Fatalf("%d inputs, want 13 (corpus case names changed?)", len(inputs))
	}
	for _, in := range inputs {
		compressLevel1(t, c, in.data)
		want := creftest.LDRun(t, in.data, "dump-htmf")
		if len(want) != 2*len(c.pf.htMf.hashTab) {
			t.Fatalf("%s: dump-htmf gave %d bytes", in.name, len(want))
		}
		for i, v := range c.pf.htMf.hashTab {
			if w := int16(binary.LittleEndian.Uint16(want[2*i:])); v != w {
				t.Fatalf("%s: hash_tab[%d][%d] = %d, libdeflate %d", in.name, i/2, i%2, v, w)
			}
		}
	}
}
