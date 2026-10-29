package zstd

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// crefBlock is one block of `cref seqs` output: C's final block type, the
// source bytes it covers and its real seqStore (nil when ZSTD_buildSeqStore
// declined to build one).
type crefBlock struct {
	typ string // "raw", "rle" or "compressed"
	src int
	ss  *seqStore
}

// parseCrefSeqs parses `cref seqs` output (format: ref/cref/cref.c header).
func parseCrefSeqs(t *testing.T, out []byte) []crefBlock {
	t.Helper()
	var blocks []crefBlock
	var cur *crefBlock
	field := func(line, key string) int {
		i := strings.Index(line, key+"=")
		if i < 0 {
			t.Fatalf("cref seqs: no %s in %q", key, line)
		}
		v := line[i+len(key)+1:]
		if j := strings.IndexByte(v, ' '); j >= 0 {
			v = v[:j]
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("cref seqs: bad %s in %q", key, line)
		}
		return n
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "block "):
			typ := strings.TrimPrefix(strings.Fields(line)[1], "type=")
			blocks = append(blocks, crefBlock{typ: typ, src: field(line, "src"), ss: &seqStore{}})
			cur = &blocks[len(blocks)-1]
		case strings.HasPrefix(line, "seq "):
			cur.ss.sequences = append(cur.ss.sequences, seqDef{
				offBase:   uint32(field(line, "offbase")),
				litLength: uint16(field(line, "lit")),
				mlBase:    uint16(field(line, "ml") - minMatch),
			})
		case strings.HasPrefix(line, "longlen "):
			cur.ss.longLengthType = field(line, "type")
			cur.ss.longLengthPos = uint32(field(line, "pos"))
		case strings.HasPrefix(line, "lits="):
			lits, err := hex.DecodeString(strings.TrimPrefix(line, "lits="))
			if err != nil {
				t.Fatalf("cref seqs: bad lits: %v", err)
			}
			cur.ss.literals = lits
			n := len(cur.ss.sequences)
			if n == 0 && len(lits) == 0 {
				cur.ss = nil // no seqStore (tiny block emitted raw)
				continue
			}
			cur.ss.llCode = make([]byte, n)
			cur.ss.mlCode = make([]byte, n)
			cur.ss.ofCode = make([]byte, n)
			// Sanity: the seqStore covers exactly the block's source bytes.
			covered := len(lits)
			for i, s := range cur.ss.sequences {
				covered += int(s.mlBase) + minMatch
				if uint32(i) == cur.ss.longLengthPos && cur.ss.longLengthType == lltMatchLength {
					covered += 0x10000
				}
			}
			if covered != cur.src {
				t.Fatalf("cref seqs: seqStore covers %d bytes, block has %d", covered, cur.src)
			}
		default:
			t.Fatalf("cref seqs: unexpected line %q", line)
		}
	}
	return blocks
}

// frameBlock is one block of a parsed zstd frame.
type frameBlock struct {
	typ  int // 0 raw, 1 rle, 2 compressed
	last bool
	body []byte
}

// parseFrame splits a single zstd frame into its header, blocks and trailing
// checksum (RFC 8878 section 3.1.1).
func parseFrame(t *testing.T, f []byte) (hdr []byte, blocks []frameBlock, tail []byte) {
	t.Helper()
	if len(f) < 5 || binary.LittleEndian.Uint32(f) != 0xFD2FB528 {
		t.Fatalf("not a zstd frame")
	}
	fhd := f[4]
	single := fhd>>5&1 == 1
	n := 5
	if !single {
		n++ // window descriptor
	}
	n += []int{0, 1, 2, 4}[fhd&3]
	switch fhd >> 6 {
	case 0:
		if single {
			n++
		}
	case 1:
		n += 2
	case 2:
		n += 4
	case 3:
		n += 8
	}
	hdr, p := f[:n], n
	for {
		h := uint32(f[p]) | uint32(f[p+1])<<8 | uint32(f[p+2])<<16
		p += 3
		b := frameBlock{typ: int(h >> 1 & 3), last: h&1 == 1}
		size := int(h >> 3)
		if b.typ == 1 {
			size = 1
		}
		b.body = f[p : p+size]
		p += size
		blocks = append(blocks, b)
		if b.last {
			break
		}
	}
	return hdr, blocks, f[p:]
}

// testCompressBound is ZSTD_COMPRESSBOUND (zstd.h).
func testCompressBound(n int) int {
	b := n + n>>8
	if n < 128<<10 {
		b += (128<<10 - n) >> 11
	}
	return b
}

func allSame(b []byte) bool {
	for _, c := range b {
		if c != b[0] {
			return false
		}
	}
	return true
}

// TestEntropyBlockCref replays the real per-block seqStores of C's
// ZSTD_compress2 through entropyCompressSeqStore, carrying the entropy tables
// across blocks the way ZSTD_compressBlock_internal does, and requires every
// block C emitted as compressed to be byte-identical.
func TestEntropyBlockCref(t *testing.T) {
	inputs := creftest.Inputs(t)
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, level := range []int{1, 3} {
		// C: clevels.h:ZSTD_defaultCParameters. Levels 1 and 3 use ZSTD_fast
		// and ZSTD_dfast respectively, with targetLength 0, in all four
		// srcSize classes (> 256 KiB, <= 256 KiB, <= 128 KiB, <= 16 KiB), and
		// ZSTD_adjustCParams_internal never changes the strategy, so these
		// hold for every input size.
		params := &cctxParams{
			cParams: compressionParameters{strategy: zstdFast, targetLength: 0},
			// C: zstd_compress.c:ZSTD_CCtxParams_init — literalCompressionMode
			// is ZSTD_ps_auto, which ZSTD_literalsCompressionIsDisabled
			// resolves to "enabled" here (targetLength == 0).
			literalCompressionMode: psAuto,
		}
		if level == 3 {
			params.cParams.strategy = zstdDfast
		}
		for _, name := range names {
			src := inputs[name]
			t.Run("L"+strconv.Itoa(level)+"/"+name, func(t *testing.T) {
				t.Parallel()
				testEntropyReplay(t, src, level, params)
			})
		}
	}
}

func testEntropyReplay(t *testing.T, src []byte, level int, params *cctxParams) {
	framePath := filepath.Join(t.TempDir(), "frame.zst")
	out := creftest.Run(t, src, "seqs", "-level", strconv.Itoa(level), "-frame-out", framePath)
	frame, err := os.ReadFile(framePath)
	if err != nil {
		t.Fatal(err)
	}
	cblocks := parseCrefSeqs(t, out)
	hdr, fblocks, tail := parseFrame(t, frame)
	if len(cblocks) != len(fblocks) {
		t.Fatalf("cref seqs dumped %d blocks, frame has %d", len(cblocks), len(fblocks))
	}

	// C: zstd_compress.c:ZSTD_reset_compressedBlockState — fresh tables with
	// every repeat mode none.
	prev, next := &entropyCTables{}, &entropyCTables{}
	wksp := make([]byte, entropyWorkspaceSize)
	// C: cref seqs gives ZSTD_compress2 a ZSTD_compressBound(n) buffer;
	// ZSTD_compress_frameChunk hands each block what is left of it, minus
	// the block header.
	remaining := testCompressBound(len(src)) - len(hdr)
	rebuilt := append([]byte(nil), hdr...)
	pos := 0
	stats := map[string]int{}
	for i, cb := range cblocks {
		block := src[pos : pos+cb.src]
		fb := fblocks[i]
		var cSize int
		var body []byte
		if cb.ss != nil {
			dst := make([]byte, remaining-3)
			n, err := entropyCompressSeqStore(dst, cb.ss, prev, next, params, len(block), wksp, false)
			if err != nil {
				t.Fatalf("block %d: entropyCompressSeqStore: %v", i, err)
			}
			cSize, body = n, dst[:n]
			// C: zstd_compress.c:ZSTD_compressBlock_internal — RLE override
			// for any block but the frame's first.
			if i != 0 && cSize < 25 && allSame(block) {
				cSize = 1
			}
		}
		got := "raw"
		switch {
		case cSize == 1:
			got = "rle"
		case cSize > 1:
			got = "compressed"
		}
		if got != cb.typ {
			t.Fatalf("block %d (%d bytes): Go chose %s (cSize %d), C chose %s", i, cb.src, got, cSize, cb.typ)
		}
		stats[got]++
		last := uint32(0)
		if i == len(cblocks)-1 {
			last = 1
		}
		var bh uint32
		switch got {
		case "compressed":
			if !bytes.Equal(body, fb.body) {
				t.Fatalf("block %d (%d bytes, %d seqs, %d lits): compressed body differs from C (Go %d bytes, C %d bytes)%s",
					i, cb.src, len(cb.ss.sequences), len(cb.ss.literals), len(body), len(fb.body), firstDiff(body, fb.body))
			}
			bh = last | 2<<1 | uint32(cSize)<<3
			for _, m := range blockModes(body) {
				stats[m]++
			}
		case "rle":
			bh = last | 1<<1 | uint32(len(block))<<3
			body = block[:1]
		default:
			bh = last | uint32(len(block))<<3
			body = block
		}
		rebuilt = append(rebuilt, byte(bh), byte(bh>>8), byte(bh>>16))
		rebuilt = append(rebuilt, body...)
		remaining -= 3 + len(body)
		// C: ZSTD_compressBlock_internal — confirm the new tables only when a
		// compressed block was emitted (cSize > 1).
		if cSize > 1 {
			prev, next = next, prev
		}
		if prev.fse.offcodeRepeatMode == fseRepeatValid {
			prev.fse.offcodeRepeatMode = fseRepeatCheck
		}
		pos += cb.src
	}
	if pos != len(src) {
		t.Fatalf("blocks cover %d bytes, input has %d", pos, len(src))
	}
	rebuilt = append(rebuilt, tail...)
	if !bytes.Equal(creftest.DecodeC(t, rebuilt), src) {
		t.Fatal("round trip through C decoder differs from input")
	}
	t.Logf("blocks: %v", stats)
}

// blockModes names the literals mode and the three sequence-table modes of
// a compressed block body (RFC 8878 sections 3.1.1.3.1 and 3.1.1.3.2), so the
// log shows which entropy paths a replay exercised.
func blockModes(b []byte) []string {
	names := [4]string{"basic", "rle", "compressed", "repeat"}
	litType := b[0] & 3
	sizeFormat := b[0] >> 2 & 3
	var hdr, size int
	switch {
	case litType < 2: // raw or RLE: regenerated size only
		hdr = []int{1, 2, 1, 3}[sizeFormat]
		size = []int{0, 1}[litType]
		if litType == 0 {
			h := int(b[0]) | int(b[1])<<8 | int(b[2])<<16
			size = h >> []int{3, 4, 3, 4}[sizeFormat] & (1<<[]int{5, 12, 5, 20}[sizeFormat] - 1)
		}
	default:
		hdr = []int{3, 3, 4, 5}[sizeFormat]
		h := uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 | uint64(b[4])<<32
		bitsPer := []int{10, 10, 14, 18}[sizeFormat]
		size = int(h >> (4 + bitsPer) & (1<<bitsPer - 1))
	}
	modes := []string{"lit=" + names[litType]}
	p := hdr + size
	nbSeq := int(b[p])
	switch {
	case nbSeq == 0:
		return modes
	case nbSeq < 128:
		p++
	case nbSeq < 255:
		p += 2
	default:
		p += 3
	}
	m := b[p]
	return append(modes, "ll="+names[m>>6], "of="+names[m>>4&3], "ml="+names[m>>2&3])
}

func firstDiff(a, b []byte) string {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return " first difference at byte " + strconv.Itoa(i)
		}
	}
	return ""
}
