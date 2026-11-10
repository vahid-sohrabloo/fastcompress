package deflate

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// Assembly equivalence of hcLongestMatchAsmForTest (hc_matchfinder_amd64.s)
// with hcMatchfinder.longestMatch: dump-hcmf scripts (hcScript, hcScriptFor) run
// on the Go method (heap buffers) and on the assembly with the matchfinder
// flush against a guard page and the input buffer flush against one after
// its end and, separately, before its start, so that any access past the
// struct or the input faults. Every result (length, offset), *inBase and
// nextHashes are compared after each search, the whole matchfinder state at
// each 'h' and at the end (the skips are the Go method in all runs). The
// searches with max_len 258 alternate between hcLongestMatchAsmForTest and
// hcLongestMatch258AsmForTest (HCSEARCH258), each layout on the other entry.
// In builds without the assembly both entries are the Go method.

// hcAsmMaxBuf is the largest script buffer the guarded buffers hold.
const hcAsmMaxBuf = 1 << 18

// hcAsmState is one matchfinder run of a script.
type hcAsmState struct {
	mf         *hcMatchfinder
	buf        []byte
	base       int
	nextHashes [2]uint32
}

// hcAsmEnv holds the guarded memory of the assembly runs, mapped once.
type hcAsmEnv struct {
	mfAfter, mfBefore   *hcMatchfinder
	bufAfter, bufBefore []byte
}

func newHcAsmEnv(t testing.TB) *hcAsmEnv {
	size := int(unsafe.Sizeof(hcMatchfinder{}))
	a := guardmem.After(t, size)
	b := guardmem.Before(t, size)
	return &hcAsmEnv{
		mfAfter:   (*hcMatchfinder)(unsafe.Pointer(&a[0])),
		mfBefore:  (*hcMatchfinder)(unsafe.Pointer(&b[0])),
		bufAfter:  guardmem.After(t, hcAsmMaxBuf),
		bufBefore: guardmem.Before(t, hcAsmMaxBuf),
	}
}

// runHcAsmScript runs script (dump-hcmf framing, see runHcmfScript) on the
// Go method and on the assembly in both guard layouts and fails on the
// first difference.
func runHcAsmScript(t testing.TB, env *hcAsmEnv, script []byte) {
	t.Helper()
	ref := &hcAsmState{mf: new(hcMatchfinder)}
	asm := []*hcAsmState{{mf: env.mfAfter}, {mf: env.mfBefore}}
	all := []*hcAsmState{ref, asm[0], asm[1]}
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(script[i:]) }
	same := func(where string, p int) {
		t.Helper()
		for i, s := range asm {
			if s.base != ref.base || s.nextHashes != ref.nextHashes {
				t.Fatalf("%s at script byte %d (layout %d): inBase %d nextHashes %v, Go %d %v", where, p, i,
					s.base, s.nextHashes, ref.base, ref.nextHashes)
			}
			if *s.mf != *ref.mf {
				t.Fatalf("%s at script byte %d (layout %d): matchfinder tables differ", where, p, i)
			}
		}
	}
	searches := 0
	for p := 0; p < len(script); {
		op := script[p]
		p++
		switch op {
		case 'b':
			n := int(u32(p))
			data := script[p+4 : p+4+n]
			p += 4 + n
			if n > hcAsmMaxBuf {
				t.Fatalf("script buffer of %d bytes > %d", n, hcAsmMaxBuf)
			}
			ref.buf = bytes.Clone(data)
			asm[0].buf = env.bufAfter[hcAsmMaxBuf-n:]
			asm[1].buf = env.bufBefore[:n:n]
			for _, s := range all {
				copy(s.buf, data)
				s.mf.nextTab = [matchfinderWindowSize]mfPos{}
				s.mf.init()
				s.base = 0
				s.nextHashes = [2]uint32{}
			}
		case 'f':
			inNext, bestLen, maxLen, niceLen, depth := int(u32(p)), u32(p+4), u32(p+8), u32(p+12), u32(p+16)
			wl, wo := ref.mf.longestMatch(ref.buf, &ref.base, inNext, bestLen, maxLen, niceLen, depth,
				&ref.nextHashes)
			for i, s := range asm {
				var l, o uint32
				// With max_len 258 the layouts take turns on the
				// HCSEARCH258 entry (greedyLoopAsm's expansion).
				v258 := maxLen == deflateMaxMatchLen && (searches+i)%2 == 1
				withDeflateAsm(true, func() {
					if v258 {
						l, o = hcLongestMatch258AsmForTest(s.mf, s.buf, &s.base, inNext, bestLen, niceLen, depth,
							&s.nextHashes)
					} else {
						l, o = hcLongestMatchAsmForTest(s.mf, s.buf, &s.base, inNext, bestLen, maxLen, niceLen, depth,
							&s.nextHashes)
					}
				})
				if l != wl || o != wo {
					t.Fatalf("longest match at %d (best %d max %d nice %d depth %d, layout %d, HCSEARCH258 %v): asm (%d, %d), Go (%d, %d)",
						inNext, bestLen, maxLen, niceLen, depth, i, v258, l, o, wl, wo)
				}
				if s.base != ref.base || s.nextHashes != ref.nextHashes {
					t.Fatalf("longest match at %d (layout %d): inBase %d nextHashes %v, Go %d %v", inNext, i,
						s.base, s.nextHashes, ref.base, ref.nextHashes)
				}
			}
			searches++
			p += 20
		case 'k':
			for _, s := range all {
				s.mf.skipBytes(s.buf, &s.base, int(u32(p)), len(s.buf), u32(p+4), &s.nextHashes)
			}
			p += 8
		case 'h':
			same("state", p-1)
		case 'c':
			// A whole level 2..4 Compress of the buffer, assembly off and on.
			level := int(script[p])
			p += 2
			var out [2][]byte
			var st [2]hcMatchfinder
			for i, on := range []bool{false, true} {
				c := mustCompressor(t, level)
				dst := make([]byte, c.CompressBound(len(ref.buf)))
				withDeflateAsm(on, func() { out[i] = dst[:c.Compress(dst, ref.buf)] })
				st[i] = c.pg.hcMf
			}
			if !bytes.Equal(out[0], out[1]) || st[0] != st[1] {
				t.Fatalf("level %d Compress of %d bytes: asm differs from Go", level, len(ref.buf))
			}
		default:
			t.Fatalf("bad script op %q at %d", op, p-1)
		}
	}
	same("end", len(script))
}

// TestHcLongestMatchAsmEquiv: 3000 random hcScript scripts (-short: 400),
// then every buffer length 0..80 with all-equal, two-letter and random
// bytes, and a 2-letter buffer over four window slides searched at every
// position with depth 1..300 (long chains, every slide inside a search).
func TestHcLongestMatchAsmEquiv(t *testing.T) {
	env := newHcAsmEnv(t)
	n := 3000
	if testing.Short() {
		n = 400
	}
	r := rand.New(rand.NewPCG(91, 1))
	for range n {
		runHcAsmScript(t, env, hcScript(r, testing.Short()))
	}
	for n := 0; n <= 80; n++ {
		for k := range 3 {
			data := make([]byte, n)
			for i := range data {
				switch k {
				case 1:
					data[i] = 'a' + byte(r.IntN(2))
				case 2:
					data[i] = byte(r.Uint32())
				}
			}
			runHcAsmScript(t, env, hcScriptFor(r, data))
		}
	}
	// Walks over 4 slides with long chains: every position searched, then
	// with skips of 1..258 positions after a third of the searches (slides
	// inside skips).
	data := make([]byte, 4*matchfinderWindowSize+100)
	for i := range data {
		data[i] = 'a' + byte(r.IntN(2))
	}
	for _, skips := range []bool{false, true} {
		var s []byte
		u32 := func(vs ...uint32) {
			for _, v := range vs {
				s = binary.LittleEndian.AppendUint32(s, v)
			}
		}
		s = append(s, 'b')
		u32(uint32(len(data)))
		s = append(s, data...)
		for p := 0; p < len(data); {
			maxLen := uint32(min(len(data)-p, deflateMaxMatchLen))
			s = append(s, 'f')
			u32(uint32(p), uint32(r.IntN(6)), maxLen, min(258, maxLen), 1+uint32(r.IntN(300)))
			p++
			if skips && p+5 < len(data) && r.IntN(3) == 0 {
				count := 1 + r.IntN(min(258, len(data)-p-5))
				s = append(s, 'k')
				u32(uint32(p), uint32(count))
				p += count
			}
			if p%10007 == 0 {
				s = append(s, 'h')
			}
		}
		runHcAsmScript(t, env, s)
	}
}

// FuzzDeflateHcLongestMatchAsmEquiv: the fuzz input after its first 8
// bytes (the parameter seed) is the buffer of an hcScriptFor script.
func FuzzDeflateHcLongestMatchAsmEquiv(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("01234567abcabcabcabcabcabcabcabcabcabcabc"))
	f.Add(append([]byte("seedseed"), bytes.Repeat([]byte("WXYZhello, world WXYZhello, there "), 40)...))
	r := rand.New(rand.NewPCG(3, 4))
	f.Add(append([]byte("8bytes!!"), hcRecords(r, 3000, 20)...))
	env := newHcAsmEnv(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var seed uint64
		if len(data) >= 8 {
			seed = binary.LittleEndian.Uint64(data)
			data = data[8:]
		}
		if len(data) > hcAsmMaxBuf {
			data = data[:hcAsmMaxBuf]
		}
		runHcAsmScript(t, env, hcScriptFor(rand.New(rand.NewPCG(seed, 9)), data))
	})
}
