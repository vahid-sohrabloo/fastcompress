package deflate

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/deflatetest"
)

// blockInfo describes one DEFLATE block of a stream, as found by
// walkBlocks: the header fields, the bit positions of its first header bit
// and of the bit after its end-of-block symbol (or stored data), the
// uncompressed bytes it produces and how many of them come from matches.
type blockInfo struct {
	final              bool
	btype              int // 0 stored, 1 static, 2 dynamic
	startBit, endBit   int
	ulen, nlit, nmatch int
}

func (b blockInfo) String() string {
	return fmt.Sprintf("{final=%v type=%d bits=%d..%d ulen=%d lits=%d matches=%d}",
		b.final, b.btype, b.startBit, b.endBit, b.ulen, b.nlit, b.nmatch)
}

// blockWalker is a minimal, independent DEFLATE (RFC 1951) block walker:
// it decodes symbols bit by bit with canonical codes built from the code
// lengths (no tables of the package under test), producing no output, only
// the block boundaries. It exists to report the first differing block of
// two streams before their bytes are compared.
type blockWalker struct {
	in  []byte
	pos int // bit position
	err error
}

var errWalk = errors.New("bad or truncated stream")

func (w *blockWalker) bits(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		if w.pos>>3 >= len(w.in) {
			w.err = errWalk
			return 0
		}
		v |= int(w.in[w.pos>>3]>>(w.pos&7)&1) << i
		w.pos++
	}
	return v
}

// walkCode is a canonical Huffman code: count[l] codes of length l, syms in
// canonical order.
type walkCode struct {
	count [16]int
	syms  []int
}

func newWalkCode(lens []int) walkCode {
	var c walkCode
	for _, l := range lens {
		c.count[l]++
	}
	c.count[0] = 0
	for l := 1; l < 16; l++ {
		for s, sl := range lens {
			if sl == l {
				c.syms = append(c.syms, s)
			}
		}
	}
	return c
}

// decode reads one symbol (codes are sent MSB first).
func (w *blockWalker) decode(c *walkCode) int {
	code, first, index := 0, 0, 0
	for l := 1; l < 16; l++ {
		code |= w.bits(1)
		if w.err != nil {
			return 0
		}
		if code-first < c.count[l] {
			return c.syms[index+code-first]
		}
		index += c.count[l]
		first = (first + c.count[l]) << 1
		code <<= 1
	}
	w.err = errWalk
	return 0
}

var walkPrecodeOrder = [19]int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}

// RFC 1951 3.2.5 length codes 257..285 and distance codes 0..29.
var (
	walkLenBase   = [29]int{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	walkLenExtra  = [29]int{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	walkDistExtra = [30]int{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
)

// walkBlocks returns the blocks of a complete raw DEFLATE stream.
func walkBlocks(stream []byte) ([]blockInfo, error) {
	w := &blockWalker{in: stream}
	var out []blockInfo
	for {
		b := blockInfo{startBit: w.pos}
		b.final = w.bits(1) == 1
		b.btype = w.bits(2)
		var lit, dist walkCode
		switch b.btype {
		case 0:
			w.pos = (w.pos + 7) &^ 7
			n := w.bits(16)
			if nn := w.bits(16); w.err == nil && n != nn^0xffff {
				return out, fmt.Errorf("block %d: bad stored LEN/NLEN", len(out))
			}
			if w.pos>>3+n > len(stream) {
				return out, fmt.Errorf("block %d: truncated stored block", len(out))
			}
			w.pos += 8 * n
			b.ulen, b.nlit = n, n
		case 1:
			lens := make([]int, 288)
			for i := range lens {
				lens[i] = [4]int{8, 9, 7, 8}[btoi(i >= 144)+btoi(i >= 256)+btoi(i >= 280)]
			}
			lit = newWalkCode(lens)
			dl := make([]int, 30)
			for i := range dl {
				dl[i] = 5
			}
			dist = newWalkCode(dl)
		case 2:
			hlit, hdist, hclen := w.bits(5)+257, w.bits(5)+1, w.bits(4)+4
			pl := make([]int, 19)
			for i := 0; i < hclen; i++ {
				pl[walkPrecodeOrder[i]] = w.bits(3)
			}
			pre := newWalkCode(pl)
			lens := make([]int, 0, hlit+hdist)
			for len(lens) < hlit+hdist && w.err == nil {
				sym := w.decode(&pre)
				switch {
				case sym < 16:
					lens = append(lens, sym)
				case sym == 16:
					if len(lens) == 0 {
						return out, fmt.Errorf("block %d: repeat without previous length", len(out))
					}
					for k := 3 + w.bits(2); k > 0; k-- {
						lens = append(lens, lens[len(lens)-1])
					}
				case sym == 17:
					for k := 3 + w.bits(3); k > 0; k-- {
						lens = append(lens, 0)
					}
				default:
					for k := 11 + w.bits(7); k > 0; k-- {
						lens = append(lens, 0)
					}
				}
			}
			if len(lens) != hlit+hdist {
				return out, fmt.Errorf("block %d: bad code lengths", len(out))
			}
			lit, dist = newWalkCode(lens[:hlit]), newWalkCode(lens[hlit:])
		default:
			return out, fmt.Errorf("block %d: reserved block type", len(out))
		}
		for b.btype != 0 && w.err == nil {
			sym := w.decode(&lit)
			if sym < 256 {
				b.ulen++
				b.nlit++
				continue
			}
			if sym == 256 {
				break
			}
			if sym > 285 {
				return out, fmt.Errorf("block %d: bad length symbol %d", len(out), sym)
			}
			l := walkLenBase[sym-257] + w.bits(walkLenExtra[sym-257])
			ds := w.decode(&dist)
			if ds >= 30 {
				return out, fmt.Errorf("block %d: bad distance symbol %d", len(out), ds)
			}
			w.bits(walkDistExtra[ds])
			b.ulen += l
			b.nmatch++
		}
		if w.err != nil {
			return out, fmt.Errorf("block %d: %w", len(out), w.err)
		}
		b.endBit = w.pos
		out = append(out, b)
		if b.final {
			return out, nil
		}
	}
}

// mergeStored joins the pieces of uncompressed blocks: deflate_flush_block
// writes one block of more than 65535 bytes as several stored blocks, all
// but the last exactly 65535 bytes long and non-final. (Two separate stored
// flushes, the first a multiple of 65535 bytes long, would be joined too;
// that does not happen in these tests, whose non-final blocks are not
// stored at such lengths.)
func mergeStored(bs []blockInfo) []blockInfo {
	var out []blockInfo
	for _, b := range bs {
		if n := len(out); n > 0 && b.btype == 0 && out[n-1].btype == 0 && !out[n-1].final &&
			out[n-1].ulen%65535 == 0 {
			p := &out[n-1]
			p.final, p.endBit = b.final, b.endBit
			p.ulen += b.ulen
			p.nlit += b.nlit
			continue
		}
		out = append(out, b)
	}
	return out
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// firstBlockDiff walks both streams and describes the first block at
// which they differ ("" if the block lists are equal).
func firstBlockDiff(got, want []byte) string {
	gb, gerr := walkBlocks(got)
	wb, werr := walkBlocks(want)
	for i := 0; i < len(gb) || i < len(wb); i++ {
		switch {
		case i >= len(gb):
			return fmt.Sprintf("block %d: go has no block (walk err %v), libdeflate %v", i, gerr, wb[i])
		case i >= len(wb):
			return fmt.Sprintf("block %d: go %v, libdeflate has no block (walk err %v)", i, gb[i], werr)
		case gb[i] != wb[i]:
			return fmt.Sprintf("block %d of go %d / libdeflate %d: go %v, libdeflate %v", i, len(gb), len(wb), gb[i], wb[i])
		}
	}
	if gerr != nil || werr != nil {
		return fmt.Sprintf("walk errors: go %v, libdeflate %v", gerr, werr)
	}
	return ""
}

// structuredInput builds a random input of n bytes from a mix of random
// literals, copies (lengths 3..300, distances 1..32769 incl. the window
// edges), byte runs and small-alphabet text, so that level 1 sees matches of
// every length and distance, window slides and block ends.
func structuredInput(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 0x6c31))
	b := make([]byte, 0, n+300)
	alpha := []byte("etaoin shrdlu")
	for len(b) < n {
		switch r.IntN(6) {
		case 0:
			for k := 1 + r.IntN(64); k > 0; k-- {
				b = append(b, byte(r.Uint32()))
			}
		case 1, 2:
			if len(b) == 0 {
				continue
			}
			var d int
			switch r.IntN(4) {
			case 0:
				d = 1 + r.IntN(min(len(b), 16))
			case 1:
				d = max(1, len(b)-r.IntN(len(b))) // anywhere
				d = min(d, 32768)
			case 2:
				d = min(len(b), [4]int{32767, 32768, 32769, 4}[r.IntN(4)])
			default:
				d = 1 + r.IntN(min(len(b), 4096))
			}
			l := [4]int{3 + r.IntN(4), 4 + r.IntN(40), 250 + r.IntN(50), 3 + r.IntN(300)}[r.IntN(4)]
			for k := 0; k < l; k++ {
				b = append(b, b[len(b)-d])
			}
		case 3:
			v := byte(r.Uint32())
			for k := 1 + r.IntN(600); k > 0; k-- {
				b = append(b, v)
			}
		default:
			for k := 1 + r.IntN(200); k > 0; k-- {
				b = append(b, alpha[r.IntN(len(alpha))])
			}
		}
	}
	return b[:n]
}

// compressLevel1 compresses src at level 1 into a CompressBound buffer.
func compressLevel1(t testing.TB, c *Compressor, src []byte) []byte {
	t.Helper()
	dst := make([]byte, c.CompressBound(len(src)))
	n := c.Compress(dst, src)
	if n == 0 {
		t.Fatalf("level 1: compress of %d bytes into the bound (%d) failed", len(src), len(dst))
	}
	return dst[:n]
}

func TestChooseMaxBlockEnd(t *testing.T) {
	for _, tc := range []struct{ begin, end, soft, want int }{
		{0, 0, 65535, 0},
		{0, 70534, 65535, 70534}, // 65535 + 5000 - 1 left: one block to the end
		{0, 70535, 65535, 65535}, // exactly soft + MIN_BLOCK_LENGTH: split
		{100, 70635, 65535, 65635},
		{100, 70634, 65535, 70634},
		{7, 1 << 30, 300000, 300007},
	} {
		if got := chooseMaxBlockEnd(tc.begin, tc.end, tc.soft); got != tc.want {
			t.Errorf("chooseMaxBlockEnd(%d, %d, %d) = %d, want %d", tc.begin, tc.end, tc.soft, got, tc.want)
		}
	}
}

func TestNewCompressorLevel1State(t *testing.T) {
	c := mustCompressor(t, 1)
	if c.impl == nil || c.pf == nil || c.niceMatchLength != 32 || c.maxPassthroughSize != 51 {
		t.Fatalf("level 1: impl set %v, pf %v, nice %d, passthrough %d", c.impl != nil, c.pf != nil,
			c.niceMatchLength, c.maxPassthroughSize)
	}
	for l := 0; l <= 12; l++ {
		if l != 1 && mustCompressor(t, l).pf != nil {
			t.Errorf("level %d allocated the level 1 state", l)
		}
	}
}

// Level 1 round trip through the T1.2 decoder and compress/flate, plus the
// block walker's view of the stream (block lengths add up, non-final blocks
// end at the fast block limits).
func TestCompressLevel1RoundTrip(t *testing.T) {
	c := mustCompressor(t, 1)
	cases := deflatetest.Cases(true)
	nRand := 60
	if testing.Short() {
		nRand = 15
	}
	for i := range nRand {
		n := []int{52, 53, 100, 1000, 32767, 32768, 32769, 65535, 70534, 70535}[i%10] + i*997
		cases = append(cases, deflatetest.Case{Name: fmt.Sprintf("structured/%d", i), Data: structuredInput(uint64(i), n)})
	}
	for _, cs := range cases {
		out := compressLevel1(t, c, cs.Data)
		if err := checkDecode(t, out, cs.Data); err != nil {
			t.Fatalf("%s: %v", cs.Name, err)
		}
		raw, err := walkBlocks(out)
		if err != nil {
			t.Fatalf("%s: walk: %v", cs.Name, err)
		}
		blocks := mergeStored(raw)
		total := 0
		for i, b := range blocks {
			total += b.ulen
			if len(cs.Data) > c.maxPassthroughSize && i < len(blocks)-1 &&
				(b.ulen < minBlockLength || b.ulen > fastSoftMaxBlockLength+deflateMaxMatchLen-1 ||
					(b.ulen < fastSoftMaxBlockLength && b.nmatch != fastSeqStoreLength)) {
				t.Errorf("%s: non-final block %d %v breaks the fast block limits", cs.Name, i, b)
			}
		}
		if total != len(cs.Data) || !blocks[len(blocks)-1].final || blocks[len(blocks)-1].endBit > 8*len(out) ||
			blocks[len(blocks)-1].endBit <= 8*(len(out)-1) {
			t.Fatalf("%s: blocks %v cover %d of %d bytes / %d stream bytes", cs.Name, blocks, total, len(cs.Data), len(out))
		}
	}
}

// dense4 really ends level 1 blocks by FAST_SEQ_STORE_LENGTH: its
// non-final blocks hold exactly 8192 matches and are well below
// FAST_SOFT_MAX_BLOCK_LENGTH.
func TestCompressLevel1Dense4SeqStore(t *testing.T) {
	var data []byte
	for _, cs := range deflatetest.Cases(true) {
		if cs.Name == "dense4" {
			data = cs.Data
		}
	}
	if data == nil {
		t.Fatal("no dense4 case")
	}
	blocks, err := walkBlocks(compressLevel1(t, mustCompressor(t, 1), data))
	if err != nil {
		t.Fatal(err)
	}
	full := 0
	for _, b := range blocks[:len(blocks)-1] {
		if b.nmatch == fastSeqStoreLength && b.ulen < fastSoftMaxBlockLength {
			full++
		}
	}
	t.Logf("dense4: %d blocks, %d ended by the sequence store; first %v", len(blocks), full, blocks[0])
	if full < 5 {
		t.Errorf("only %d of %d blocks ended by FAST_SEQ_STORE_LENGTH", full, len(blocks))
	}
}

// Every output size below the needed one gives 0; the exact size gives the
// same bytes (C: overflow is decided per block, the block choice does not
// depend on the buffer size).
func TestCompressLevel1SmallDst(t *testing.T) {
	c := mustCompressor(t, 1)
	for i, n := range []int{52, 300, 5000, 70535, 140000} {
		src := structuredInput(uint64(1000+i), n)
		want := compressLevel1(t, c, src)
		dst := make([]byte, len(want))
		if m := c.Compress(dst, src); m != len(want) || !bytes.Equal(dst, want) {
			t.Fatalf("n=%d: exact buffer gave %d", n, m)
		}
		for _, short := range []int{1, 2, 7, len(want) / 2, len(want) - 1, len(want)} {
			if m := c.Compress(dst[:len(want)-short], src); m != 0 {
				t.Fatalf("n=%d: buffer %d bytes short gave %d", n, short, m)
			}
		}
		if c.os.out != nil {
			t.Fatal("Compress retained dst")
		}
	}
}

func TestCompressLevel1NoAlloc(t *testing.T) {
	c := mustCompressor(t, 1)
	src := structuredInput(7, 200000)
	dst := make([]byte, c.CompressBound(len(src)))
	for _, n := range []int{52, 1000, 70535, 200000} {
		allocs := testing.AllocsPerRun(20, func() {
			if c.Compress(dst, src[:n]) == 0 {
				t.Fatal("compress failed")
			}
		})
		if allocs != 0 {
			t.Errorf("n=%d: %v allocs per Compress", n, allocs)
		}
	}
	allocs := testing.AllocsPerRun(20, func() {
		if c.Compress(dst[:10], src) != 0 {
			t.Fatal("overflow not reported")
		}
	})
	if allocs != 0 {
		t.Errorf("overflow: %v allocs per Compress", allocs)
	}
}

// The walker itself, on streams of known shape.
func TestBlockWalker(t *testing.T) {
	buf := make([]byte, 70010)
	n := deflateCompressNone(make([]byte, 70000), buf)
	bs, err := walkBlocks(buf[:n])
	if err != nil || len(bs) != 2 || bs[0] != (blockInfo{false, 0, 0, 8 * 65540, 65535, 65535, 0}) ||
		bs[1] != (blockInfo{true, 0, 8 * 65540, 8 * n, 4465, 4465, 0}) {
		t.Fatalf("stored: %v %v", bs, err)
	}
	if m := mergeStored(bs); len(m) != 1 || m[0] != (blockInfo{true, 0, 0, 8 * n, 70000, 70000, 0}) {
		t.Fatalf("merged stored: %v", m)
	}
	// "a" as a static block: 4b 04 00 (T2.2 golden); and a hand-made static
	// match: literal 'a', match length 3 distance 1, EOB.
	if bs, err := walkBlocks([]byte{0x4b, 0x04, 0x00}); err != nil || len(bs) != 1 ||
		bs[0] != (blockInfo{true, 1, 0, 3 + 8 + 7, 1, 1, 0}) {
		t.Fatalf("static: %v %v", bs, err)
	}
	c := mustCompressor(t, 1)
	src := bytes.Repeat([]byte("abcd"), 1000)
	bs, err = walkBlocks(compressLevel1(t, c, src))
	if err != nil || len(bs) != 1 || bs[0].ulen != 4000 || bs[0].nlit < 4 || bs[0].nlit > 8 || bs[0].nmatch < 15 {
		t.Fatalf("repeat: %v %v", bs, err)
	}
	for _, bad := range [][]byte{{}, {0x07}, {0x01, 0x05, 0x00, 0x00, 0x00}, {0x4b}} {
		if _, err := walkBlocks(bad); err == nil {
			t.Errorf("walkBlocks(%x) accepted", bad)
		}
	}
	if d := firstBlockDiff([]byte{0x4b, 0x04, 0x00}, []byte{0x4b, 0x04, 0x00}); d != "" {
		t.Errorf("equal streams differ: %s", d)
	}
	if d := firstBlockDiff([]byte{0x4b, 0x04, 0x00}, buf[:n]); d == "" {
		t.Error("different streams compare equal")
	}
}
