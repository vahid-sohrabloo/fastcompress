package deflate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/deflatetest"
)

// errStreamLimit: streamDecode's output limit was reached.
var errStreamLimit = errors.New("stream output limit")

// streamDecode drives decompressStream as a reader does, but with full
// control of the pieces: input arrives inStep() bytes at a time (only when
// the decoder asks for more) and each call gets room() bytes of output
// space (room() >= 258). The output buffer keeps everything (no sliding),
// with dict before it. It returns the output, the input consumed, and nil,
// ErrBadData, io.ErrUnexpectedEOF or errStreamLimit.
func streamDecode(t testing.TB, d *Decompressor, src, dict []byte, inStep, room func() int, limit int) ([]byte, int, error) {
	t.Helper()
	var s streamState
	buf := append([]byte(nil), dict...)
	wpos := len(dict)
	ipos, iend := 0, 0
	for stalls := 0; ; {
		need := wpos + room()
		if need > len(buf) {
			nb := make([]byte, max(need, 2*len(buf)))
			copy(nb, buf[:wpos])
			buf = nb
		}
		res, nIn, nOut := d.decompressStream(&s, src[ipos:iend], buf[:need], wpos)
		if res != libdeflateSuccess {
			return nil, 0, ErrBadData
		}
		if nIn < 0 || nIn > iend-ipos || nOut < wpos || nOut > need {
			t.Fatalf("decompressStream counts out of range: in %d of %d, out %d (from %d, end %d)", nIn, iend-ipos, nOut,
				wpos, need)
		}
		progress := nIn > 0 || nOut > wpos
		ipos += nIn
		wpos = nOut
		if wpos-len(dict) > limit {
			return nil, 0, errStreamLimit
		}
		switch s.stop {
		case stopEnd:
			return buf[len(dict):wpos], ipos, nil
		case stopNeedInput:
			if iend == len(src) {
				return buf[len(dict):wpos], ipos, io.ErrUnexpectedEOF
			}
			iend = min(len(src), iend+inStep())
			progress = true
		}
		if s.bitsleft >= 8 || s.bitbuf>>s.bitsleft != 0 {
			t.Fatalf("state not normalized: bitsleft %d bitbuf %#x", s.bitsleft, s.bitbuf)
		}
		if !progress {
			if stalls++; stalls > 2 {
				t.Fatalf("no progress (stop %d, in %d/%d, out %d)", s.stop, ipos, iend, wpos)
			}
		} else {
			stalls = 0
		}
	}
}

// chunking is a way of feeding streamDecode.
type chunking struct {
	name         string
	inStep, room func(r *rand.Rand) int
}

var chunkings = []chunking{
	{"whole", func(*rand.Rand) int { return 1 << 30 }, func(*rand.Rand) int { return 1 << 20 }},
	{"1-byte", func(*rand.Rand) int { return 1 }, func(*rand.Rand) int { return 1 << 16 }},
	{"random", func(r *rand.Rand) int { return 1 + r.IntN(4096) }, func(r *rand.Rand) int { return 258 + r.IntN(70000) }},
	{"tiny-room", func(r *rand.Rand) int { return 1 + r.IntN(64) }, func(r *rand.Rand) int { return 258 + r.IntN(40) }},
	{"room-258", func(r *rand.Rand) int { return 1 + r.IntN(300) }, func(*rand.Rand) int { return 258 }},
}

// wholeResult decodes src with DecompressEx into dst of limit bytes.
func wholeResult(d *Decompressor, src []byte, limit int) (out []byte, nIn int, err error) {
	dst := make([]byte, limit)
	nIn, nOut, err := d.DecompressEx(dst, src)
	return dst[:nOut], nIn, err
}

// checkStreamVsWhole: every chunking gives the whole-buffer decoder's
// result: the same output and input length on success; on failure
// ErrBadData or io.ErrUnexpectedEOF, the same for every chunking.
func checkStreamVsWhole(t *testing.T, name string, src []byte, limit int, seed uint64) {
	t.Helper()
	d := NewDecompressor()
	wOut, wIn, wErr := wholeResult(d, src, limit)
	if wErr == ErrInsufficientSpace {
		return // larger than the limit: not compared
	}
	var firstErr error
	for i, c := range chunkings {
		r := rand.New(rand.NewPCG(seed, uint64(i)))
		out, nIn, err := streamDecode(t, d, src, nil, func() int { return c.inStep(r) }, func() int { return c.room(r) },
			limit)
		if err == errStreamLimit {
			t.Fatalf("%s/%s: stream output over the limit but whole-buffer result %v", name, c.name, wErr)
		}
		if (err == nil) != (wErr == nil) {
			t.Fatalf("%s/%s: stream err %v, whole-buffer err %v", name, c.name, err, wErr)
		}
		if err == nil {
			if nIn != wIn || !bytes.Equal(out, wOut) {
				t.Fatalf("%s/%s: stream in %d out %d bytes, whole-buffer in %d out %d (equal %v)", name, c.name, nIn,
					len(out), wIn, len(wOut), bytes.Equal(out, wOut))
			}
			continue
		}
		if err != ErrBadData && err != io.ErrUnexpectedEOF {
			t.Fatalf("%s/%s: unexpected error %v", name, c.name, err)
		}
		if i == 0 {
			firstErr = err
		} else if err != firstErr {
			t.Fatalf("%s/%s: error %v, but %v with whole input", name, c.name, err, firstErr)
		}
	}
}

// streamCorpus returns compressed streams of the short corpus at the given
// levels (our compressor: libdeflate's output).
func streamCorpus(t testing.TB, levels []int) (names []string, streams, origs [][]byte) {
	for _, level := range levels {
		c, err := NewCompressor(level)
		if err != nil {
			t.Fatal(err)
		}
		for _, cs := range deflatetest.Cases(true) {
			if raceEnabled && len(cs.Data) > 20000 {
				continue
			}
			dst := make([]byte, c.CompressBound(len(cs.Data)))
			n := c.Compress(dst, cs.Data)
			names = append(names, fmt.Sprintf("%s/L%d", cs.Name, level))
			streams = append(streams, dst[:n])
			origs = append(origs, cs.Data)
		}
	}
	return names, streams, origs
}

// The corpus at several levels, plus stdlib's output, decoded in every
// chunking == the input and == DecompressEx.
func TestStreamCorpus(t *testing.T) {
	levels := []int{0, 1, 4, 6, 9, 12}
	if testing.Short() {
		levels = []int{0, 1, 6, 12}
	}
	names, streams, origs := streamCorpus(t, levels)
	for i, s := range streams {
		checkStreamVsWhole(t, names[i], s, len(origs[i])+64, uint64(i))
		// Trailing bytes are not consumed.
		ext := append(append([]byte(nil), s...), 0xde, 0xad, 0xbe, 0xef)
		out, nIn, err := streamDecode(t, NewDecompressor(), ext, nil, func() int { return 7 }, func() int { return 1000 },
			len(origs[i]))
		if err != nil || nIn != len(s) || !bytes.Equal(out, origs[i]) {
			t.Fatalf("%s + trailing: err %v, in %d of %d", names[i], err, nIn, len(s))
		}
	}
	for _, cs := range deflatetest.Cases(true) {
		if len(cs.Data) > 300000 || (raceEnabled && len(cs.Data) > 20000) {
			continue
		}
		for _, level := range []int{-2, 1, 9} {
			s := stdlibCompress(t, cs.Data, level)
			checkStreamVsWhole(t, cs.Name+"/std", s, len(cs.Data)+64, uint64(level+10))
		}
	}
}

// The crafted golden streams (valid and invalid) and the fuzz seeds.
func TestStreamGoldenAndSeeds(t *testing.T) {
	for i, g := range goldenCases() {
		limit := goldenErrDst
		if g.res == libdeflateSuccess {
			limit = max(len(g.out)+64, goldenErrDst)
		}
		checkStreamVsWhole(t, g.name, g.in, limit, uint64(i))
	}
	for i, s := range fuzzSeeds() {
		if len(s) > 0 {
			s = s[1:]
		}
		checkStreamVsWhole(t, fmt.Sprintf("seed%d", i), s, max(1032*len(s)+64, 1<<16), uint64(i))
	}
}

// Every strict prefix of a valid stream is io.ErrUnexpectedEOF (never
// ErrBadData: no decision is taken on bits past the input), for every
// chunking; the stream itself decodes.
func TestStreamTruncatedEverywhere(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	var ins [][]byte
	for _, g := range goldenCases() {
		if g.res == libdeflateSuccess && len(g.in) < 3000 {
			ins = append(ins, g.in[:g.nIn])
		}
	}
	for _, level := range []int{0, 1, 6, 12} {
		c, _ := NewCompressor(level)
		data := append([]byte(strings.Repeat("the stream decoder, ", 80)), rndBytes(uint64(level), 500)...)
		dst := make([]byte, c.CompressBound(len(data)))
		ins = append(ins, dst[:c.Compress(dst, data)])
	}
	d := NewDecompressor()
	for k, in := range ins {
		for cut := 0; cut < len(in); cut++ {
			for i, c := range chunkings[:3] {
				r := rand.New(rand.NewPCG(uint64(cut), uint64(i)))
				_, _, err := streamDecode(t, d, in[:cut], nil, func() int { return c.inStep(r) },
					func() int { return c.room(r) }, 1<<22)
				if err != io.ErrUnexpectedEOF {
					t.Fatalf("stream %d cut at %d of %d (%s): %v, want io.ErrUnexpectedEOF", k, cut, len(in), c.name, err)
				}
			}
		}
	}
}

// streamMutate returns a copy of in with one random edit: bit flips, a byte
// change, a deletion, an insertion or a truncation.
func streamMutate(r *rand.Rand, in []byte) []byte {
	m := append([]byte(nil), in...)
	if len(m) == 0 {
		return append(m, byte(r.Uint32()))
	}
	p := r.IntN(len(m))
	switch r.IntN(6) {
	case 0, 1:
		for k := 1 + r.IntN(3); k > 0; k-- {
			q := r.IntN(len(m))
			m[q] ^= 1 << r.IntN(8)
		}
	case 2:
		m[p] = byte(r.Uint32())
	case 3:
		m = append(m[:p], m[p+1:]...)
	case 4:
		m = append(m[:p], append([]byte{byte(r.Uint32())}, m[p:]...)...)
	default:
		m = m[:p]
	}
	return m
}

// Mutated valid streams: the stream decoder takes the whole-buffer
// decoder's accept/reject decisions with the same output.
func TestStreamMutations(t *testing.T) {
	// Single-goroutine exhaustive truncation/mutation sweep: nothing for -race to find.
	if raceEnabled {
		t.Skip("exhaustive sweep: no goroutines, runs in the normal lane")
	}
	var ins [][]byte
	for _, g := range goldenCases() {
		if len(g.in) < 1<<14 {
			ins = append(ins, g.in)
		}
	}
	_, streams, _ := streamCorpus(t, []int{1, 6})
	for _, s := range streams {
		if len(s) < 4000 && len(s) > 20 {
			ins = append(ins, s)
		}
	}
	n := 20000
	if testing.Short() || raceEnabled {
		n = 4000
	}
	r := rand.New(rand.NewPCG(7, 7))
	for k := range n {
		in := ins[r.IntN(len(ins))]
		m := streamMutate(r, in)
		if r.IntN(4) == 0 {
			m = streamMutate(r, m)
		}
		checkStreamVsWhole(t, fmt.Sprintf("mutant%d", k), m, max(1032*len(m)+64, 1<<16), uint64(k))
	}
}

// Streams made with a history (CompressChunk) decode with that history as
// the dictionary; without it the whole-buffer and the stream decoder both
// reject them (offset beyond the output).
func TestStreamDict(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	hist := rndBytes(5, 40000)
	for _, level := range []int{1, 6, 12} {
		c, _ := NewCompressor(level)
		for _, hl := range []int{1, 100, 32768, 40000} {
			h := hist[:hl]
			var src []byte
			for len(src) < 5000 {
				p := r.IntN(len(h))
				src = append(src, h[p:min(len(h), p+3+r.IntN(200))]...)
			}
			dst := make([]byte, c.CompressChunkBound(len(src)))
			s := dst[:c.CompressChunk(dst, h, src, true)]
			for i, ch := range chunkings {
				out, nIn, err := streamDecode(t, NewDecompressor(), s, h, func() int { return ch.inStep(r) },
					func() int { return ch.room(r) }, 1<<20)
				if err != nil || nIn != len(s) || !bytes.Equal(out, src) {
					t.Fatalf("level %d hist %d %s: err %v", level, hl, chunkings[i].name, err)
				}
			}
			if _, _, err := wholeResult(NewDecompressor(), s, 1<<20); err != ErrBadData {
				t.Fatalf("level %d hist %d: whole-buffer without dict: %v", level, hl, err)
			}
			if _, _, err := streamDecode(t, NewDecompressor(), s, nil, func() int { return 100 },
				func() int { return 1000 }, 1<<20); err != ErrBadData {
				t.Fatalf("level %d hist %d: stream without dict: %v", level, hl, err)
			}
		}
	}
}

// fastloopRegion returns the assembly hook and the fast loop of a decoder
// source file, each line without its indentation: the hook from its `for
// useFastloopAsm` line to its closing brace, then the fast loop from the
// margin test that enters it to the end of its loop.
//
// Between the two only blank and comment lines may appear; they are left
// out (each file's comment there describes its own context, so they
// differ), and any other line there fails the test, so code added between
// the hook and the loop in one file is not skipped silently. No line of
// the hook or the loop is normalized: the hook's exits are the same in
// both files (goto blockDone at the end of the block; return
// libdeflateBadData, 0, 0 at a bad offset, which in decompressStream is
// also its Go fast loop's exit, and StreamReader reruns the call from the
// block record), so they must match verbatim. Should one file's exit ever
// have to differ, map that line here explicitly.
func fastloopRegion(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	const (
		findHook = iota
		inHook
		betweenHookAndLoop
		inFastloop
	)
	var out []string
	phase, inLoop := findHook, false
	indent := ""
	for _, l := range strings.Split(string(b), "\n") {
		tl := strings.TrimLeft(l, "\t")
		switch phase {
		case findHook:
			if tl != "for useFastloopAsm && inNext < inFastloopEnd && outNext < outFastloopEnd {" {
				continue
			}
			phase, indent = inHook, l[:len(l)-len(tl)]
		case inHook:
			if l == indent+"}" {
				phase = betweenHookAndLoop
			}
		case betweenHookAndLoop:
			if l == indent+"if inNext >= inFastloopEnd || outNext >= outFastloopEnd {" {
				phase = inFastloop
				break
			}
			if tl == "" || strings.HasPrefix(tl, "//") {
				continue
			}
			t.Fatalf("%s: line %q between the assembly hook and the fast loop", file, l)
		}
		out = append(out, tl)
		if l == indent+"for ok := true; ok; ok = inNext < inFastloopEnd && outNext < outFastloopEnd {" {
			inLoop = true
		}
		if inLoop && l == indent+"}" {
			return out
		}
	}
	t.Fatalf("%s: assembly hook and fast loop not found (phase %d)", file, phase)
	return nil
}

// The stream decoder's assembly hook and fast loop are the whole-buffer
// decoder's, textually (modulo indentation; see fastloopRegion). Reads the
// files from disk (see TestRefillCopiesIdentical for what that means for
// overlay mutants).
func TestStreamFastloopCopyIdentical(t *testing.T) {
	a := fastloopRegion(t, "decompress_generic.go")
	b := fastloopRegion(t, "decompress_stream.go")
	if len(a) < 250 {
		t.Fatalf("fast loop of decompress_generic.go: only %d lines", len(a))
	}
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		for i := range min(len(a), len(b)) {
			if a[i] != b[i] {
				t.Fatalf("line %d differs:\n%s\n%s", i, a[i], b[i])
			}
		}
		t.Fatalf("lengths differ: %d vs %d lines", len(a), len(b))
	}
}

// Crafted boundary cases: headers and symbols split at every bit position
// (via every input split point), a stored block body across many calls,
// many empty stored blocks, and output space exactly that of a match.
func TestStreamCrafted(t *testing.T) {
	var cases [][]byte
	var w bitWriter
	// Static block with a max-length match right at the start of the
	// output space; dynamic block with subtables; stored blocks.
	w.static(false, append(lits("abc"), match(258, 3), match(258, 1), lit('z')))
	w.dynamic(false, chainLens(286, []int{'e', 't', 256, 'a', 257, 'o', 258, 'i', 262, 'n', 'z', 285, 'q', 270, 'x', 'j'}),
		chainLens(30, []int{0, 1, 3, 5, 9, 12, 15, 17, 20, 22, 24, 25, 26, 27, 28, 29}),
		append(lits("jxqz"), match(258, 1), match(8, 4), lit('e')))
	w.stored(false, rndBytes(1, 3000))
	for range 5 {
		w.stored(false, nil)
	}
	w.static(true, lits("end"))
	cases = append(cases, w.bytes())
	var w2 bitWriter
	w2.stored(true, rndBytes(2, 65535))
	cases = append(cases, w2.bytes())
	for k, in := range cases {
		d := NewDecompressor()
		want, wIn, err := wholeResult(d, in, 1<<20)
		if err != nil {
			t.Fatalf("case %d: whole-buffer: %v", k, err)
		}
		for split := 0; split <= min(len(in), 400); split++ {
			steps := []int{split, len(in)}
			si := 0
			out, nIn, err := streamDecode(t, d, in, nil, func() int {
				s := steps[min(si, 1)]
				si++
				return s
			}, func() int { return 258 }, 1<<20)
			if err != nil || nIn != wIn || !bytes.Equal(out, want) {
				t.Fatalf("case %d split %d: err %v", k, split, err)
			}
		}
	}
}

// FuzzStream: arbitrary input, the first two bytes choose the input step
// and output room; the stream decoder must agree with DecompressEx.
func FuzzStream(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(append([]byte{byte(len(s)), byte(len(s) >> 3)}, s...))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 {
			return
		}
		step, room := 1+int(data[0])%64, 258+int(data[1])*4
		src := data[2:]
		limit := max(1032*len(src)+64, 1<<16)
		d := NewDecompressor()
		wOut, wIn, wErr := wholeResult(d, src, limit)
		out, nIn, err := streamDecode(t, d, src, nil, func() int { return step }, func() int { return room }, limit)
		if wErr == ErrInsufficientSpace {
			return
		}
		if (err == nil) != (wErr == nil) || (err == nil && (nIn != wIn || !bytes.Equal(out, wOut))) {
			t.Fatalf("stream err %v in %d, whole-buffer err %v in %d", err, nIn, wErr, wIn)
		}
	})
}

// A block header that is invalid exactly at the last bit of the input is
// ErrBadData, not io.ErrUnexpectedEOF: the decision used only real bits.
// (A dynamic header with 5 precode lengths ends on a byte boundary: 17 + 5*3
// = 32 bits; five 1-bit precode codewords are over-subscribed.)
func TestStreamHeaderInvalidAtInputEnd(t *testing.T) {
	var w bitWriter
	w.bits(1, 1)  // BFINAL
	w.bits(2, 2)  // BTYPE dynamic
	w.bits(0, 5)  // HLIT
	w.bits(0, 5)  // HDIST
	w.bits(1, 4)  // HCLEN: 5 precode lengths
	for range 5 { // all length 1
		w.bits(1, 3)
	}
	in := w.bytes()
	if len(in) != 4 {
		t.Fatalf("crafted header is %d bytes", len(in))
	}
	if _, _, err := wholeResult(NewDecompressor(), in, 1<<10); err != ErrBadData {
		t.Fatalf("whole-buffer: %v", err)
	}
	for i, c := range chunkings {
		r := rand.New(rand.NewPCG(uint64(i), 9))
		if _, _, err := streamDecode(t, NewDecompressor(), in, nil, func() int { return c.inStep(r) },
			func() int { return c.room(r) }, 1<<10); err != ErrBadData {
			t.Fatalf("%s: %v, want ErrBadData", c.name, err)
		}
	}
}

// A stored body that exhausts the input and the output space at once stops
// with stopOutputFull (the output is returned before more input is read).
func TestStreamStoredInputAndOutputFull(t *testing.T) {
	var w bitWriter
	w.stored(true, rndBytes(3, 1000))
	in := w.bytes()
	var s streamState
	out := make([]byte, 500)
	res, nIn, nOut := NewDecompressor().decompressStream(&s, in[:5+500], out, 0)
	if res != libdeflateSuccess || nIn != 505 || nOut != 500 || s.stop != stopOutputFull || s.state != stStored {
		t.Fatalf("res %v in %d out %d stop %d state %d", res, nIn, nOut, s.stop, s.state)
	}
}

// On libdeflateBadData the generic loop reports the output before the
// invalid item; the fast loop (shared with the whole-buffer decoder)
// reports 0, so slow disables it. The block start record locates the last
// block start.
func TestStreamBadDataOutput(t *testing.T) {
	names, ins, dicts, wants := badStreams()
	for i, in := range ins {
		for _, slow := range []bool{false, true} {
			out := make([]byte, 1<<20)
			n := copy(out, dicts[i])
			s := streamState{slow: slow}
			res, _, nOut := NewDecompressor().decompressStream(&s, in, out, n)
			want := n + len(wants[i])
			if !slow && strings.HasPrefix(names[i], "offset") {
				want = 0 // a bad offset found by the fast loop
			}
			if res != libdeflateBadData || nOut != want || !s.blkSet {
				t.Fatalf("%s slow %v: res %v, out %d (want %d), blkSet %v", names[i], slow, res, nOut, want, s.blkSet)
			}
		}
	}
	// The record of a second block: its input and output start.
	_, ins2, _, wants2 := badStreams()
	s := streamState{slow: true}
	NewDecompressor().decompressStream(&s, ins2[1], make([]byte, 1<<20), 0)
	if s.blkOut != 5000 || s.blkIn <= 0 || s.blkIn >= len(ins2[1]) || len(wants2[1]) != 8000 {
		t.Fatalf("block start record: in %d out %d", s.blkIn, s.blkOut)
	}
}
