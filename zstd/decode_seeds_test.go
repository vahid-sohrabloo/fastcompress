package zstd

import (
	"encoding/binary"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

var update = flag.Bool("update", false, "regenerate committed test data (TestGenDecodeSeeds)")

const (
	seedsDir   = "testdata/decode/seeds"
	nSeeds     = 40
	maxSeedLen = 64 << 10
)

// seedFeatures must all be covered by the committed seeds: every block type,
// literals section type and sequence table mode (zstd format spec).
var seedFeatures = []string{
	"block-raw", "block-rle", "block-compressed",
	"lit-raw", "lit-rle", "lit-compressed", "lit-treeless",
	"seq-none",
	"LL-predefined", "LL-rle", "LL-fse", "LL-repeat",
	"OF-predefined", "OF-rle", "OF-fse", "OF-repeat",
	"ML-predefined", "ML-rle", "ML-fse", "ML-repeat",
}

// frameFeatures parses data (zstd and skippable frames, no validation
// beyond what the walk needs) and returns the features it contains: block
// types, literals section types and size formats, sequence count header
// sizes, sequence table modes, and frame-level flags.
func frameFeatures(data []byte) (map[string]bool, bool) {
	fs := map[string]bool{}
	nFrames := 0
	for len(data) > 0 {
		if len(data) < 8 {
			return nil, false
		}
		magic := binary.LittleEndian.Uint32(data)
		if magic&^0xF == 0x184D2A50 {
			n := 8 + int(binary.LittleEndian.Uint32(data[4:]))
			if n > len(data) {
				return nil, false
			}
			fs["skippable"] = true
			data = data[n:]
			continue
		}
		if magic != 0xFD2FB528 {
			return nil, false
		}
		nFrames++
		fhd := data[4]
		single := fhd&0x20 != 0
		pos := 5 + [4]int{0, 1, 2, 4}[fhd&3]
		if !single {
			pos++
		}
		fcs := [4]int{0, 2, 4, 8}[fhd>>6]
		if fhd>>6 == 0 && single {
			fcs = 1
		}
		pos += fcs
		if fhd&4 != 0 {
			fs["checksum"] = true
		}
		if single {
			fs["single-segment"] = true
		}
		blocks := 0
		for {
			if pos+3 > len(data) {
				return nil, false
			}
			h := uint32(data[pos]) | uint32(data[pos+1])<<8 | uint32(data[pos+2])<<16
			pos += 3
			size := int(h >> 3)
			blocks++
			switch (h >> 1) & 3 {
			case 0:
				fs["block-raw"] = true
				if size == 0 {
					fs["block-empty"] = true
				}
			case 1:
				fs["block-rle"] = true
				size = 1
			case 2:
				fs["block-compressed"] = true
				if pos+size > len(data) || !blockFeatures(data[pos:pos+size], fs) {
					return nil, false
				}
			default:
				return nil, false
			}
			pos += size
			if pos > len(data) {
				return nil, false
			}
			if h&1 != 0 {
				break
			}
		}
		if blocks > 1 {
			fs["multi-block"] = true
		}
		if fhd&4 != 0 {
			pos += 4
		}
		if pos > len(data) {
			return nil, false
		}
		data = data[pos:]
	}
	if nFrames > 1 {
		fs["multi-frame"] = true
	}
	return fs, true
}

func blockFeatures(c []byte, fs map[string]bool) bool {
	if len(c) < 1 {
		return false
	}
	lt, sf := c[0]&3, (c[0]>>2)&3
	var hs, n int
	if lt < 2 {
		switch sf {
		case 0, 2:
			hs, n = 1, int(c[0]>>3)
		case 1:
			hs = 2
		case 3:
			hs = 3
		}
		if len(c) < hs {
			return false
		}
		if sf == 1 {
			n = int(c[0]>>4) + int(c[1])<<4
		} else if sf == 3 {
			n = int(c[0]>>4) + int(c[1])<<4 + int(c[2])<<12
		}
		if lt == 1 {
			n = 1
		}
	} else {
		hs = [4]int{3, 3, 4, 5}[sf]
		if len(c) < hs {
			return false
		}
		var lhc uint64
		for i := hs - 1; i >= 0; i-- {
			lhc = lhc<<8 | uint64(c[i])
		}
		bits := [4]uint{10, 10, 14, 18}[sf]
		n = int(lhc >> (4 + bits) & (1<<bits - 1))
		streams := "4"
		if sf == 0 {
			streams = "1"
		}
		fs["lit-streams-"+streams] = true
	}
	fs["lit-"+[4]string{"raw", "rle", "compressed", "treeless"}[lt]] = true
	fs["lit-"+[4]string{"raw", "rle", "compressed", "treeless"}[lt]+"-sf"+strconv.Itoa(int(sf))] = true
	p := hs + n
	if p >= len(c) {
		return false
	}
	switch nb := c[p]; {
	case nb == 0:
		fs["seq-none"] = true
		return true
	case nb < 128:
		fs["nbseq-1byte"] = true
		p++
	case nb < 255:
		fs["nbseq-2byte"] = true
		p += 2
	default:
		fs["nbseq-3byte"] = true
		p += 3
	}
	if p >= len(c) {
		return false
	}
	m := c[p]
	modes := [4]string{"predefined", "rle", "fse", "repeat"}
	fs["LL-"+modes[m>>6]] = true
	fs["OF-"+modes[(m>>4)&3]] = true
	fs["ML-"+modes[(m>>2)&3]] = true
	return true
}

// TestGenDecodeSeeds checks that the committed decoder seed frames in
// testdata/decode/seeds cover every seedFeatures entry. With -update it
// first regenerates them from the cref decode corpus (creftest.Frames):
// nSeeds frames of at most 64 KiB that need no dictionary, chosen greedily
// for the most new features (block types, literal types and size formats,
// sequence header sizes and table modes, frame flags), then filled up with
// evenly spaced corpus entries.
func TestGenDecodeSeeds(t *testing.T) {
	if *update {
		genDecodeSeeds(t)
	}
	ents, err := os.ReadDir(seedsDir)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	n := 0
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(seedsDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > maxSeedLen {
			t.Errorf("%s: %d bytes > %d", e.Name(), len(b), maxSeedLen)
		}
		fs, ok := frameFeatures(b)
		if !ok {
			t.Errorf("%s: does not parse", e.Name())
		}
		for f := range fs {
			have[f] = true
		}
		n++
	}
	if n != nSeeds {
		t.Errorf("%d seeds, want %d", n, nSeeds)
	}
	for _, f := range seedFeatures {
		if !have[f] {
			t.Errorf("no seed covers %s", f)
		}
	}
}

func genDecodeSeeds(t *testing.T) {
	type cand struct {
		name string
		data []byte
		fs   map[string]bool
	}
	var cands []cand
	for name, f := range creftest.Frames(t) {
		if f.Dict != nil || len(f.Data) > maxSeedLen {
			continue
		}
		fs, ok := frameFeatures(f.Data)
		if !ok {
			t.Fatalf("%s: corpus frame does not parse", name)
		}
		cands = append(cands, cand{name, f.Data, fs})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].name < cands[j].name })

	covered := map[string]bool{}
	picked := map[string]bool{}
	var seeds []cand
	for len(seeds) < nSeeds {
		best, bestN := -1, 0
		for i, c := range cands {
			if picked[c.name] {
				continue
			}
			k := 0
			for f := range c.fs {
				if !covered[f] {
					k++
				}
			}
			if k > bestN || (k == bestN && k > 0 && len(c.data) < len(cands[best].data)) {
				best, bestN = i, k
			}
		}
		if best < 0 {
			break
		}
		for f := range cands[best].fs {
			covered[f] = true
		}
		picked[cands[best].name] = true
		seeds = append(seeds, cands[best])
	}
	var rest []cand
	for _, c := range cands {
		if !picked[c.name] {
			rest = append(rest, c)
		}
	}
	for i, need := 0, nSeeds-len(seeds); i < need && len(rest) > 0; i++ {
		seeds = append(seeds, rest[i*len(rest)/need])
	}

	if err := os.RemoveAll(seedsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(seedsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range seeds {
		p := filepath.Join(seedsDir, strings.ReplaceAll(s.name, "/", "_")+".zst")
		if err := os.WriteFile(p, s.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d seeds; features covered: %d", len(seeds), len(covered))
}
