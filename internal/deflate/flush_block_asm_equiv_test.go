package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// Assembly equivalence of writeSequencesAsm (flush_block_amd64.s) with the Go
// writeSequences: the sequences of a block (built by chooseLiteral /
// chooseMatch, so the frequencies and the dynamic codes are those of the
// block; static codes too) are written by the Go method on heap memory and by
// the assembly with every buffer it reads or writes flush against a guard
// page: the output buffer (its length is exactly the bytes the block writes
// plus 0..9 (0..15 in the fuzz target), so the last flushes take the
// byte-at-a-time branch, and with a layout ending at the page the last
// written byte is the last one before it),
// the block (its last byte is the last before the page), the sequence list
// (exactly up to the terminating sequence), the codes and the full length
// codewords (the structs end at the page), and then the same with every
// buffer starting at a guard page. The output bytes and the bit writer state
// must be identical. In builds without the assembly both runs are the Go
// method.

// wsEnv holds the compressor the blocks are built with and the reused
// sequence store.
type wsEnv struct {
	c    *Compressor
	seqs []deflateSequence
}

// wsTok is one token: length 0 is a literal, else a match.
type wsTok struct{ length, offset uint32 }

// wsRunAsm runs writeSequencesAsm (the Go method in builds without it) on w.
func wsRunAsm(c *Compressor, w *outputBitstream, codes *deflateCodes, full *deflateLengthCodes, block []byte,
	seqs []deflateSequence) {
	withDeflateAsm(true, func() {
		if useWriteSequencesAsm {
			r := w.regs()
			writeSequencesAsm(&r, w.out, codes, full, block, &seqs[0])
			w.setRegs(r)
			return
		}
		saved := c.o.length
		c.o.length = *full
		c.writeSequences(w, codes, block, seqs)
		c.o.length = saved
	})
}

// checkWriteSequencesAsm builds the sequences of block from toks (which must
// cover it exactly), the codes (static or the block's dynamic ones), and
// compares the Go and assembly output for an output buffer starting with
// start bytes, bitcount pending bits bitbuf, and slack bytes beyond what the
// block needs. fastEnd is computed hdr bytes before start, as flushBlock
// computes it before the block header advances next (so next may be past
// fastEnd at entry).
func checkWriteSequencesAsm(t testing.TB, e *wsEnv, block []byte, toks []wsTok, static bool, start, hdr int,
	bitcount uint, bitbuf uint64, slack int) {
	t.Helper()
	c := e.c
	if len(e.seqs) < len(toks)+1 {
		e.seqs = make([]deflateSequence, len(toks)+1)
	}
	seqs := e.seqs
	c.beginSequences(&seqs[0])
	i, pos := 0, 0
	for _, tk := range toks {
		if tk.length == 0 {
			c.chooseLiteral(uint32(block[pos]), &seqs[i])
			pos++
		} else {
			i = c.chooseMatch(tk.length, tk.offset, seqs, i)
			pos += int(tk.length)
		}
	}
	if pos != len(block) {
		t.Fatalf("tokens cover %d of %d bytes", pos, len(block))
	}
	seqs = seqs[:i+1]
	c.freqs.litlen[deflateEndOfBlock]++
	deflateMakeHuffmanCodes(&c.freqs, &c.codes)
	codes := &c.codes
	if static {
		codes = &c.staticCodes
	}
	c.computeFullLenCodewords(codes)
	bitbuf &= 1<<bitcount - 1

	run := func(out []byte, asm bool, codes *deflateCodes, full *deflateLengthCodes, block []byte,
		seqs []deflateSequence) outputBitstream {
		w := outputBitstream{bitbuf: bitbuf, bitcount: bitcount, next: start - hdr, out: out}
		w.setFastEnd()
		w.next = start
		if asm {
			wsRunAsm(c, &w, codes, full, block, seqs)
		} else {
			withDeflateAsm(false, func() { c.writeSequences(&w, codes, block, seqs) })
		}
		return w
	}
	// The bytes the block writes: at most 14 bits per literal and 47 per
	// match (of at least 3 bytes).
	big := make([]byte, start+(16*len(block)+7)/8+16)
	n := run(big, false, codes, &c.o.length, block, seqs).next
	outLen := n + slack
	ref := bytes.Repeat([]byte{0xA5}, outLen)
	want := run(ref, false, codes, &c.o.length, block, seqs)

	for layout, alloc := range []func(testing.TB, int) []byte{guardmem.After, guardmem.Before} {
		out := alloc(t, outLen)
		for k := range out {
			out[k] = 0xA5
		}
		gb := alloc(t, len(block))
		copy(gb, block)
		gs := alloc(t, 8*len(seqs))
		gseqs := unsafe.Slice((*deflateSequence)(unsafe.Pointer(&gs[0])), len(seqs))
		copy(gseqs, seqs)
		gc := (*deflateCodes)(unsafe.Pointer(&alloc(t, int(unsafe.Sizeof(deflateCodes{})))[0]))
		*gc = *codes
		gf := (*deflateLengthCodes)(unsafe.Pointer(&alloc(t, int(unsafe.Sizeof(deflateLengthCodes{})))[0]))
		*gf = c.o.length
		got := run(out, true, gc, gf, gb, gseqs)
		if got.next != want.next || got.bitcount != want.bitcount || got.bitbuf != want.bitbuf {
			t.Fatalf("layout %d, %d bytes, %d sequences, static %v, start %d, hdr %d, slack %d: state next=%d "+
				"bitcount=%d bitbuf=%#x, Go next=%d bitcount=%d bitbuf=%#x", layout, len(block), len(seqs), static,
				start, hdr, slack, got.next, got.bitcount, got.bitbuf, want.next, want.bitcount, want.bitbuf)
		}
		if !bytes.Equal(out, ref) {
			t.Fatalf("layout %d, %d bytes, %d sequences, static %v, start %d, hdr %d, slack %d: output differs",
				layout, len(block), len(seqs), static, start, hdr, slack)
		}
	}
}

// wsGen draws a block of n bytes and its tokens: kind selects the literal
// share (none, 10%, 50%, 90%, all) and skew a geometric literal distribution
// (long codewords); lengths and offsets are uniform or slot boundaries.
func wsGen(r *rand.Rand, n int) ([]byte, []wsTok) {
	block := make([]byte, n)
	pLit := []int{0, 10, 50, 90, 100}[r.IntN(5)]
	skew := r.IntN(2) == 0
	var toks []wsTok
	for pos := 0; pos < n; {
		if n-pos >= 3 && r.IntN(100) >= pLit {
			var ln, off uint32
			if r.IntN(4) == 0 {
				ln = uint32(fbLengths[r.IntN(len(fbLengths))])
				off = uint32(fbOffsets[r.IntN(len(fbOffsets))])
			} else {
				ln = 3 + uint32(r.IntN(256))
				off = 1 + uint32(r.IntN(deflateMaxMatchOffset))
			}
			ln = min(ln, uint32(n-pos))
			toks = append(toks, wsTok{ln, off})
			for k := range ln {
				block[pos+int(k)] = byte(r.Uint32())
			}
			pos += int(ln)
			continue
		}
		b := byte(r.Uint32())
		if skew {
			b = 0
			for b < 255 && r.IntN(4) != 0 {
				b++
			}
		}
		block[pos] = b
		toks = append(toks, wsTok{})
		pos++
	}
	return block, toks
}

func newWsEnv(t testing.TB) *wsEnv { return &wsEnv{c: mustCompressor(t, 6)} }

func checkWsRandom(t testing.TB, e *wsEnv, r *rand.Rand, n int) {
	block, toks := wsGen(r, n)
	start := 0
	if r.IntN(2) == 0 {
		start = r.IntN(20)
	}
	checkWriteSequencesAsm(t, e, block, toks, r.IntN(3) == 0, start, 0, uint(r.IntN(8)), r.Uint64(), r.IntN(10))
}

// TestWriteSequencesAsmEquiv: every block length 1..300 and slack 0..9, random
// blocks up to MAX_BLOCK_LENGTH, and the extremes (a block of only literals
// of MAX_BLOCK_LENGTH, only 258-byte matches at offset 32768, an output buffer
// too short for any whole-word flush).
func TestWriteSequencesAsmEquiv(t *testing.T) {
	e := newWsEnv(t)
	r := rand.New(rand.NewPCG(101, 1))
	for n := 1; n <= 300; n++ {
		checkWsRandom(t, e, r, n)
	}
	for slack := range 10 {
		block, toks := wsGen(r, 2000)
		for _, static := range []bool{false, true} {
			checkWriteSequencesAsm(t, e, block, toks, static, 3, 0, 5, r.Uint64(), slack)
		}
	}
	rounds := 200
	if testing.Short() {
		rounds = 50
	}
	for range rounds {
		var n int
		switch k := r.IntN(10); {
		case k < 6:
			n = 1 + r.IntN(5000)
		case k < 9:
			n = 5000 + r.IntN(70000)
		default:
			n = maxBlockLength - r.IntN(70000)
		}
		checkWsRandom(t, e, r, n)
	}
	// Only literals: one run of MAX_BLOCK_LENGTH (litrunlen at its maximum).
	lits := make([]byte, maxBlockLength)
	ltoks := make([]wsTok, maxBlockLength)
	for k := range lits {
		lits[k] = byte(r.IntN(256))
	}
	checkWriteSequencesAsm(t, e, lits, ltoks, false, 0, 0, 0, 0, 0)
	checkWriteSequencesAsm(t, e, lits, ltoks, true, 7, 0, 7, r.Uint64(), 9)
	// Only matches of 258 at offset 32768 (the last length and offset slots).
	mtoks := make([]wsTok, 1000)
	for k := range mtoks {
		mtoks[k] = wsTok{deflateMaxMatchLen, deflateMaxMatchOffset}
	}
	checkWriteSequencesAsm(t, e, make([]byte, 258*1000), mtoks, false, 0, 0, 1, 1, 0)
	// Output buffers of 1..8 bytes beyond start: no whole-word flush up to 7;
	// the last one takes exactly one word store ending at the page.
	for slack := range 8 {
		checkWriteSequencesAsm(t, e, []byte{'a'}, []wsTok{{}}, true, 1, 0, 3, 5, slack)
	}
	// fastEnd computed before 1..8 header bytes advanced next: with fewer
	// than 7 bytes beyond start next is past fastEnd at entry, so every flush
	// is a byte flush; with more, fastEnd = len(out) - 7 > next.
	for hdr := 1; hdr <= 8; hdr++ {
		for slack := range 10 {
			checkWriteSequencesAsm(t, e, []byte("abcdef"), make([]wsTok, 6), true, 8, hdr, 2, 3, slack)
		}
	}
}

// TestWriteSequencesAsmOverrun: an output buffer too short for the block
// (the cost precondition violated, so a byte flush reaches len(out)) makes the
// assembly fault at address 0 (a nil pointer dereference panic) instead of
// storing past the slice, which ends at a guard page. Buffers of 0..4 bytes
// (no word store: fastEnd <= next) and of 8..16 bytes (fastEnd = len(out) - 7
// > next: word stores first, then the byte flush that reaches the end), each
// with a 64-literal block longer than the buffer. The fault is checked to be
// the one at address 0: run with debug.SetPanicOnFault, so that a fault at a
// guard page would also be recovered (with its address) rather than crash.
func TestWriteSequencesAsmOverrun(t *testing.T) {
	if !useWriteSequencesAsm {
		t.Skip("no writeSequencesAsm in this build")
	}
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	c := mustCompressor(t, 6)
	c.computeFullLenCodewords(&c.staticCodes)
	block := bytes.Repeat([]byte{'a'}, 64)
	seqs := make([]deflateSequence, 1)
	c.beginSequences(&seqs[0])
	for _, b := range block {
		c.chooseLiteral(uint32(b), &seqs[0])
	}
	for _, n := range []int{0, 1, 2, 3, 4, 8, 9, 10, 11, 12, 13, 14, 15, 16} {
		out := guardmem.After(t, n)
		w := outputBitstream{out: out}
		w.setFastEnd()
		r := w.regs()
		func() {
			defer func() {
				e, ok := recover().(runtime.Error)
				if !ok || !strings.Contains(e.Error(), "nil pointer") {
					t.Fatalf("len(out) %d: recovered %v, want a nil pointer dereference", n, e)
				}
				if a, ok := e.(interface{ Addr() uintptr }); ok && a.Addr() != 0 {
					t.Fatalf("len(out) %d: fault at %#x, want address 0", n, a.Addr())
				}
			}()
			writeSequencesAsm(&r, out, &c.staticCodes, &c.o.length, block, &seqs[0])
		}()
		for k, b := range out {
			if want := byte(c.staticCodes.codewords.litlen['a']); b != want {
				t.Fatalf("len(out) %d: out[%d] = %#x, want the static codeword of 'a' %#x", n, k, b, want)
			}
		}
	}
}

// FuzzDeflateWriteSequencesAsmEquiv: the fuzz input after its first 4 bytes
// (the seed of the token choice; the static flag, start, bitcount, slack and
// hdr come from it) as the block.
func FuzzDeflateWriteSequencesAsmEquiv(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4, 'a'})
	f.Add(append([]byte{0, 0, 0, 0}, bytes.Repeat([]byte("abcdefgh"), 100)...))
	r := rand.New(rand.NewPCG(5, 7))
	b, _ := wsGen(r, 3000)
	f.Add(append([]byte{9, 8, 7, 6}, b...))
	const maxIn = 1 << 17
	e := newWsEnv(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 5 {
			return
		}
		seed := binary.LittleEndian.Uint32(data)
		block := data[4:min(len(data), 4+maxIn)]
		r := rand.New(rand.NewPCG(uint64(seed), 0))
		var toks []wsTok
		for pos := 0; pos < len(block); {
			if len(block)-pos >= 3 && r.IntN(2) == 0 {
				ln := min(3+uint32(r.IntN(256)), uint32(len(block)-pos))
				toks = append(toks, wsTok{ln, 1 + uint32(r.IntN(deflateMaxMatchOffset))})
				pos += int(ln)
				continue
			}
			toks = append(toks, wsTok{})
			pos++
		}
		start := int(seed >> 1 & 15)
		checkWriteSequencesAsm(t, e, block, toks, seed&1 != 0, start, min(int(seed>>12&15), start),
			uint(seed>>5&7), r.Uint64(), int(seed>>8&15))
	})
}
