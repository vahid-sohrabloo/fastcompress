package lz4

import (
	"bytes"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// withAsm runs fn with every registered assembly routine on (when the CPU
// supports it) or off, then restores the switches.
func withAsm(t testing.TB, on bool, fn func()) {
	t.Helper()
	routines := cpu.AsmRoutines()
	saved := make([]bool, len(routines))
	for i, r := range routines {
		saved[i] = *r.Use
		*r.Use = on && r.Available()
	}
	defer func() {
		for i, r := range routines {
			*r.Use = saved[i]
		}
	}()
	fn()
}

// FuzzDecodeFastAsmEquiv decodes arbitrary input with the assembly fast loop
// on and off, whole, into any capacity and after a prefix: the results and
// the decoded bytes must be equal.
func FuzzDecodeFastAsmEquiv(f *testing.F) {
	for i, b := range fuzzBlocks(f) {
		f.Add(uint32(i*7919), uint16(i*31), b)
	}
	f.Fuzz(func(t *testing.T, capa uint32, prefix uint16, src []byte) {
		c := int(capa % (1 << 20))
		p := int(prefix % 2048)
		run := func() ([]byte, error) {
			out := make([]byte, p+c)
			for i := 0; i < p; i++ {
				out[i] = byte(i * 7)
			}
			n, err := decompressBlock(src, out, p, c, noDict, 0, nil)
			return out[:p+n], err
		}
		var on, off []byte
		var eon, eoff error
		withAsm(t, true, func() { on, eon = run() })
		withAsm(t, false, func() { off, eoff = run() })
		if eon != eoff || !bytes.Equal(on, off) {
			t.Fatalf("asm %v (%d bytes), Go %v (%d bytes)", eon, len(on), eoff, len(off))
		}
	})
}

// FuzzCompressFastAsmEquiv compresses arbitrary input with the assembly main
// loop on and off: one-shot, then as a sequence of independent blocks on one
// reused Stream (byU16 tables reused with dictSmall, byU32 beyond 64 KB),
// with capacities from tight (limitedOutput, possibly failing) to the bound,
// and as a stream in prefix mode, with and without a dictionary. The blocks
// must be equal.
func FuzzCompressFastAsmEquiv(f *testing.F) {
	i := 0
	for _, b := range fuzzBlocks(f) {
		f.Add(uint8(i), uint16(i*13), b)
		i++
	}
	for _, in := range []string{"text-65547", "text128KiB", "random-70K", "period7-100K"} {
		f.Add(uint8(i), uint16(i*101), testInput(f, in))
		i++
	}
	f.Fuzz(func(t *testing.T, sel uint8, cut uint16, src []byte) {
		if len(src) > 1<<18 {
			return
		}
		accel := 1 + int(sel%5)
		if sel&0x80 != 0 {
			accel = 1 + int(sel)*257
		}
		run := func() [][]byte {
			var out [][]byte
			var s Stream
			step := 1 + int(cut)%(len(src)+1)
			for _, capMode := range []int{0, 1, 2} {
				for off := 0; off < len(src) || off == 0; off += step {
					piece := src[off:min(off+step, len(src))]
					capa := CompressBound(len(piece))
					switch capMode {
					case 1:
						capa = len(piece) - len(piece)/8
					case 2:
						capa = len(piece) / 2
					}
					dst := make([]byte, max(capa, 1))
					n := s.CompressFast(dst, piece, accel)
					out = append(out, dst[:n])
					if off+step >= len(src) {
						break
					}
				}
			}
			dst := make([]byte, CompressBound(len(src)))
			out = append(out, dst[:CompressFast(dst, src, accel)])
			// streaming: consecutive pieces of one buffer (prefix mode),
			// after a dictionary in the same buffer, with tight capacities too
			for _, withDict := range []bool{false, true} {
				var ps Stream
				buf := src
				start := 0
				if withDict {
					buf = append(append([]byte{}, testInput(t, "text-65547")[:5000+int(cut)%60000]...), src...)
					start = len(buf) - len(src)
					ps.LoadDict(buf[:start])
				}
				for off := start; off < len(buf); off += step {
					piece := buf[off:min(off+step, len(buf))]
					if withDict && (off/step)%3 == 2 {
						// a separate allocation: extDict, resumed in prefix mode past 64 KB
						piece = append([]byte(nil), piece...)
					}
					capa := CompressBound(len(piece))
					if (off/step)%2 == 1 {
						capa = len(piece) - len(piece)/4
					}
					d := make([]byte, max(capa, 1))
					out = append(out, d[:ps.CompressFastContinue(d, piece, accel)])
				}
			}
			return out
		}
		var on, off [][]byte
		withAsm(t, true, func() { on = run() })
		withAsm(t, false, func() { off = run() })
		if len(on) != len(off) {
			t.Fatalf("%d blocks with assembly, %d without", len(on), len(off))
		}
		for i := range on {
			if !bytes.Equal(on[i], off[i]) {
				t.Fatalf("block %d: assembly %d bytes, Go %d bytes", i, len(on[i]), len(off[i]))
			}
		}
	})
}

func testInput(t testing.TB, name string) []byte {
	return creftest.LZ4Inputs(t)[name]
}

// FuzzHCChainAsmEquiv compresses arbitrary input at the HC levels (2 to
// 12, favorDecSpeed too) with the assembly on and off: one-shot with the
// bound and with tight capacities (limitedOutput, possibly failing),
// independent blocks on one reused stream (stale table entries), and
// streaming with a dictionary (external dictionary candidates) and in
// prefix mode; blocks must be equal.
func FuzzHCChainAsmEquiv(f *testing.F) {
	i := 0
	for _, in := range []string{"text-65547", "text128KiB", "random-70K", "period3-200K", "period7-100K", "zeros-1M"} {
		b := testInput(f, in)
		for lv := 0; lv < 11; lv++ {
			f.Add(uint8(i*11+lv), b[:min(len(b), 200000)])
		}
		i++
	}
	// repeats of 1 to 4 bytes inside text: the pattern analysis (level 9+)
	pa := append([]byte(nil), testInput(f, "text-65547")[:30000]...)
	for k, r := range []string{"a", "ab", "abc", "abcd", "\x00", "xyxyxyxyz"} {
		at := 1000 + k*4000
		for j := 0; j < 1500; j++ {
			pa[at+j] = r[j%len(r)]
		}
	}
	for lv := 0; lv < 11; lv++ {
		f.Add(uint8(lv), pa)
	}
	dict := testInput(f, "text-65547")[:20000]
	f.Fuzz(func(t *testing.T, sel uint8, src []byte) {
		if len(src) > 1<<18 {
			return
		}
		level := 2 + int(sel)%11
		run := func() [][]byte {
			dst := make([]byte, CompressBound(len(src)))
			out := [][]byte{append([]byte(nil), dst[:CompressHC(dst, src, level)]...)}
			for _, capa := range []int{len(src) - len(src)/8, len(src) / 2, len(src) / 3} {
				d := make([]byte, max(capa, 1))
				out = append(out, d[:CompressHC(d, src, level)])
			}
			rs := NewStreamHC(level)
			for k := 0; k < 3; k++ {
				p := src[k*len(src)/3 : (k+1)*len(src)/3]
				d := make([]byte, CompressBound(len(p)))
				out = append(out, d[:rs.Compress(d, p, level)])
			}
			ps := NewStreamHC(level)
			for k := 0; k < 3; k++ {
				p := src[k*len(src)/3 : (k+1)*len(src)/3]
				d := make([]byte, CompressBound(len(p)))
				out = append(out, d[:ps.CompressContinue(d, p)])
			}
			fs := NewStreamHC(level)
			fs.FavorDecompressionSpeed(true)
			out = append(out, append([]byte(nil), dst[:fs.Compress(dst, src, level)]...))
			s := NewStreamHC(level)
			s.LoadDict(dict)
			half := len(src) / 2
			for _, p := range [][]byte{append([]byte(nil), src[:half]...), append([]byte(nil), src[half:]...)} {
				d := make([]byte, CompressBound(len(p)))
				out = append(out, d[:s.CompressContinue(d, p)])
			}
			return out
		}
		var on, off [][]byte
		withAsm(t, true, func() { on = run() })
		withAsm(t, false, func() { off = run() })
		for i := range on {
			if !bytes.Equal(on[i], off[i]) {
				t.Fatalf("level %d block %d: assembly %d bytes, Go %d bytes", level, i, len(on[i]), len(off[i]))
			}
		}
	})
}
