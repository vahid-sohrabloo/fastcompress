package zstd

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// mixedInput returns n bytes whose statistics change every chunk bytes:
// English-like text, skewed binary with short repeats, numeric records and
// sparse matches in random bytes, so that the post-block splitter finds
// partitions worth cutting inside one 128 KiB block.
func mixedInput(seed uint64, n, chunk int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	words := []string{"the ", "quick ", "brown ", "fox ", "jumps ", "over ", "lazy ", "dog ", "and ", "zstd ", "block ", "split "}
	var b bytes.Buffer
	for kind := 0; b.Len() < n; kind = (kind + 1) % 4 {
		end := min(b.Len()+chunk, n)
		for b.Len() < end {
			switch kind {
			case 0:
				b.WriteString(words[r.IntN(len(words))])
				if r.IntN(9) == 0 {
					b.WriteString(".\n")
				}
			case 1:
				v := byte(r.IntN(4) * r.IntN(4))
				for range 1 + r.IntN(6) {
					b.WriteByte(v + byte(r.IntN(2)))
				}
				if r.IntN(5) == 0 && b.Len() > 64 {
					off := 1 + r.IntN(min(b.Len()-1, 4096))
					p := b.Bytes()
					src := p[len(p)-off:]
					b.Write(src[:min(len(src), 3+r.IntN(20))])
				}
			case 2:
				b.WriteString("id=")
				b.WriteString(string(rune('0' + r.IntN(10))))
				for range 6 {
					b.WriteByte(byte('0' + r.IntN(10)))
				}
				b.WriteString(";v=0x")
				for range 4 {
					b.WriteByte("0123456789abcdef"[r.IntN(16)])
				}
				b.WriteByte('\n')
			case 3:
				for range 16 + r.IntN(48) {
					b.WriteByte(byte(r.Uint32()))
				}
				if b.Len() > 4096 {
					p := b.Bytes()
					off := 1 + r.IntN(4096)
					b.Write(p[len(p)-off : len(p)-off+min(off, 4+r.IntN(8))])
				}
			}
		}
	}
	return b.Bytes()[:n]
}

// rawSplitInput returns n bytes alternating textChunk bytes of words with
// rawChunk bytes of random data holding a 4-byte copy every gap bytes, the
// text starting with two copies at the random part's last offset. The
// splitter then stores some partitions raw (the gain is below minGain)
// and the next partition opens with a repcode whose offset the raw
// partition set, so seqStoreResolveOffCodes must replace it.
func rawSplitInput(seed uint64, n, gap, textChunk, rawChunk int) []byte {
	r := rand.New(rand.NewPCG(seed, 7))
	words := []string{"the ", "quick ", "brown ", "fox ", "jumps ", "over ", "lazy ", "dog "}
	var b bytes.Buffer
	lastOff := 0
	for k := 0; b.Len() < n; k ^= 1 {
		if k == 0 {
			if lastOff > 0 {
				for range 3 {
					b.WriteByte(byte(r.Uint32()))
				}
				for range 2 {
					p := b.Bytes()
					b.Write(p[len(p)-lastOff : len(p)-lastOff+8])
					b.WriteByte('x')
				}
			}
			for end := b.Len() + textChunk; b.Len() < end; {
				b.WriteString(words[r.IntN(len(words))])
			}
			continue
		}
		for end := b.Len() + rawChunk; b.Len() < end; {
			for range gap {
				b.WriteByte(byte(r.Uint32()))
			}
			p := b.Bytes()
			lastOff = 4 + r.IntN(min(len(p)-4, 60000))
			b.Write(p[len(p)-lastOff : len(p)-lastOff+4])
		}
	}
	return b.Bytes()[:n]
}

// splitInputs are TestCompress2BlockSplitCref's inputs: the shared corpus
// above 64 KiB (where the default window log reaches 17) and up to 4 MiB
// (the Silesia files would take hours at these levels) plus mixed inputs
// built to split, single- and multi-block, and inputs with raw partitions.
func splitInputs(t *testing.T) ([]string, map[string][]byte) {
	names, inputs := sortedInputs(t)
	var keep []string
	for _, name := range names {
		if n := len(inputs[name]); n > 64<<10 && n <= 4<<20 {
			keep = append(keep, name)
		}
	}
	for _, m := range []struct {
		name         string
		n, chunk     int
		seed         uint64
		skipForShort bool
	}{
		{"mixed100KiB", 100 << 10, 6 << 10, 1, false},
		{"mixed128KiB", 128 << 10, 9 << 10, 2, false},
		{"mixed300KiB", 300 << 10, 11 << 10, 3, false},
		{"mixed1MiB", 1 << 20, 23 << 10, 4, true},
	} {
		if m.skipForShort && testing.Short() {
			continue
		}
		inputs[m.name] = mixedInput(m.seed, m.n, m.chunk)
		keep = append(keep, m.name)
	}
	inputs["splitraw128KiB"] = rawSplitInput(1, 128<<10, 192, 16<<10, 90<<10)
	inputs["splitraw512KiB"] = rawSplitInput(2, 512<<10, 160, 16<<10, 70<<10)
	keep = append(keep, "splitraw128KiB", "splitraw512KiB")
	return keep, inputs
}

// splitCases are levels 13 to 22 with the level's window log and with
// window logs 17 and 19. -short keeps levels 13, 16, 19 and 22 and the
// level's window log only.
func splitCases() []compress2Case {
	step, wls := 1, []uint32{0, 17, 19}
	if testing.Short() {
		step, wls = 3, []uint32{0}
	}
	var cases []compress2Case
	for level := 13; level <= 22; level += step {
		for i, wl := range wls {
			cases = append(cases, compress2Case{level: level, checksum: (level+i)%2 == 1, windowLog: wl})
		}
	}
	return cases
}

// TestCompress2BlockSplitCref requires cctx.compress2 to produce exactly the
// frame of C's ZSTD_compress2 wherever the post-block splitter runs
// (btopt and above with window log 17 or more), at levels 13 to 22, on
// single- and multi-block inputs, and requires the splitter to have cut
// some block into several partitions for every strategy that runs it.
func TestCompress2BlockSplitCref(t *testing.T) {
	names, inputs := splitInputs(t)
	ran := map[strategy]int{}
	split := map[strategy]int{}
	multi := 0
	for _, c := range splitCases() {
		for _, name := range names {
			src := inputs[name]
			if !c.splitterOn(len(src)) || !c.ported(len(src)) {
				continue
			}
			cc := c.newCCtx(t)
			got, err := cc.compress2(nil, src)
			if err != nil {
				t.Fatalf("%s/%s: compress2: %v", c, name, err)
			}
			want := creftest.Run(t, src, c.crefArgs("compress")...)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s/%s (strategy %d): frame differs from C (Go %d bytes, C %d bytes)%s",
					c, name, cc.appliedParams.cParams.strategy, len(got), len(want), firstDiff(got, want))
			}
			strat := cc.appliedParams.cParams.strategy
			ran[strat]++
			split[strat] += cc.splitBlocks
			if len(src) > cc.blockSizeMax {
				multi++
			}
		}
	}
	t.Logf("frames per strategy %v, split blocks %v, %d multi-block", ran, split, multi)
	for _, s := range []strategy{zstdBtopt, zstdBtultra, zstdBtultra2} {
		if ran[s] == 0 || split[s] == 0 {
			t.Errorf("strategy %d: %d frames with the splitter, %d split blocks", s, ran[s], split[s])
		}
	}
	if multi == 0 {
		t.Errorf("no multi-block frame ran the splitter")
	}
}

// TestCompress2BlockSplitReuse compresses inputs with ONE context,
// alternating splitter parameter sets with non-splitter ones (level 1,
// level 11, btopt and btultra2 with window log 16), and requires each
// frame to equal C's from ONE reused C context: the partitions' entropy
// tables and repcode history must carry over to the next block and frame
// exactly as in C.
func TestCompress2BlockSplitReuse(t *testing.T) {
	names, inputs := splitInputs(t)
	cases := []compress2Case{{19, false, 0}, {1, true, 0}, {16, true, 17}, {11, false, 0}, {22, false, 19}, {19, true, 16}, {13, false, 0}, {18, true, 0}}
	var steps []seqStep
	for i, name := range names {
		src := inputs[name]
		if testing.Short() && len(src) > 512<<10 {
			continue
		}
		for j := range 2 {
			if c := cases[(i+j)%len(cases)]; c.ported(len(src)) {
				steps = append(steps, seqStep{name, c, src})
			}
		}
	}
	runSeq(t, steps)
}
