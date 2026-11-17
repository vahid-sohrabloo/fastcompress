package lz4

import (
	"bytes"
	"io"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// fuzzSeeds adds valid blocks of the small differential inputs.
func fuzzBlocks(f *testing.F) [][]byte {
	var out [][]byte
	for _, in := range creftest.LZ4Inputs(f) {
		if len(in) > 1<<17 {
			continue
		}
		dst := make([]byte, CompressBound(len(in)))
		out = append(out, dst[:CompressFast(dst, in, 1)])
		dst = make([]byte, CompressBound(len(in)))
		out = append(out, dst[:CompressHC(dst, in, 9)])
	}
	return out
}

// FuzzDecompress decodes arbitrary input as a block, whole, partial and with
// a dictionary, into buffers of any capacity. The decoder must never panic,
// and must return C's result and output (lz4ref).
func FuzzDecompress(f *testing.F) {
	for i, b := range fuzzBlocks(f) {
		f.Add(uint16(i*977), uint8(i), b)
	}
	f.Add(uint16(100), uint8(0), []byte{0xF0, 0xFF, 0x05, 'a', 'b', 'c'})
	f.Add(uint16(30), uint8(1), []byte{0x1F, 'x', 0, 0, 0xFF, 0x10})
	dict := creftest.LZ4Inputs(f)["text-65547"][:3000]
	df := creftest.LZ4TempFile(f, dict)
	f.Fuzz(func(t *testing.T, capa uint16, mode uint8, src []byte) {
		if len(src) > 1<<17 {
			return
		}
		c := int(capa)
		dst := make([]byte, c)
		var n int
		var err error
		var want []byte
		var code int
		switch mode % 4 {
		case 0:
			n, err = DecompressSafe(dst, src)
			want, code = cDecodeResult(t, src, "dec", "cap="+strconv.Itoa(c))
		case 1:
			target := int(mode) * 97 % (c + 1)
			n, err = DecompressSafePartial(dst, src, target)
			want, code = cDecodeResult(t, src, "decpartial", "target="+strconv.Itoa(target), "cap="+strconv.Itoa(c))
		case 2:
			n, err = DecompressSafeUsingDict(dst, src, dict)
			want, code = cDecodeResult(t, src, "decdict", "dict="+df, "cap="+strconv.Itoa(c))
		default:
			buf := append(append([]byte{}, dict...), make([]byte, c)...)
			n, err = DecompressSafeUsingDict(buf[len(dict):], src, buf[:len(dict)])
			dst = buf[len(dict):]
			want, code = cDecodeResult(t, src, "decdict", "dict="+df, "prefix=1", "cap="+strconv.Itoa(c))
		}
		if n < 0 || n > c {
			t.Fatalf("decoded size %d out of range", n)
		}
		checkDecode(t, "fuzz", dst[:n], err, want, code)
	})
}

// FuzzDecompressFrame decodes arbitrary input as frames, one-shot and with
// the Reader, and compares with C's LZ4F_decompress (lz4ref).
func FuzzDecompressFrame(f *testing.F) {
	for _, in := range []string{"text-17", "text128KiB", "period3-200K"} {
		src := creftest.LZ4Inputs(f)[in]
		for _, p := range []Preferences{{}, {FrameInfo: FrameInfo{BlockChecksum: true, ContentChecksum: true, ContentSize: uint64(len(src))}},
			{CompressionLevel: 9, FrameInfo: FrameInfo{BlockIndependent: true}}} {
			fr, _ := AppendFrame(nil, src, &p)
			f.Add(fr)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<18 {
			return
		}
		want, cerr := creftest.LZ4Dec(t, b, "framedec")
		got, gerr := AppendDecompressFrame(nil, b, nil)
		cname := ""
		if cerr != nil {
			cname = cerr.Error()
		}
		if frameErrName(gerr) != cname {
			t.Fatalf("error %q, C %q", frameErrName(gerr), cname)
		}
		if cerr == nil && !bytes.Equal(got, want) {
			t.Fatalf("output differs from C")
		}
		rgot, rerr := io.ReadAll(NewReader(bytes.NewReader(b)))
		if frameErrName(rerr) != cname || (cerr == nil && !bytes.Equal(rgot, want)) {
			t.Fatalf("Reader: %v, C %q", rerr, cname)
		}
	})
}

// FuzzCompress compresses arbitrary input at a fast acceleration and an HC
// level; the blocks must equal C's and decode to the input.
func FuzzCompress(f *testing.F) {
	i := 0
	for _, in := range creftest.LZ4Inputs(f) {
		if len(in) <= 1<<17 {
			f.Add(uint8(i), in)
			i++
		}
	}
	f.Fuzz(func(t *testing.T, sel uint8, src []byte) {
		if len(src) > 1<<18 {
			return
		}
		accel := 1 + int(sel%8)
		level := 2 + int(sel>>3)%11
		dst := make([]byte, CompressBound(len(src)))
		fast := append([]byte(nil), dst[:CompressFast(dst, src, accel)]...)
		if want := creftest.LZ4Run(t, src, "fast", "accel="+strconv.Itoa(accel)); !bytes.Equal(fast, want) {
			t.Fatalf("fast a%d differs from C", accel)
		}
		hc := append([]byte(nil), dst[:CompressHC(dst, src, level)]...)
		if want := creftest.LZ4Run(t, src, "hc", "level="+strconv.Itoa(level)); !bytes.Equal(hc, want) {
			t.Fatalf("hc l%d differs from C", level)
		}
		for _, b := range [][]byte{fast, hc} {
			out := make([]byte, len(src))
			n, err := DecompressSafe(out, b)
			if err != nil || n != len(src) || !bytes.Equal(out, src) {
				t.Fatalf("round trip: %v", err)
			}
		}
	})
}

// FuzzFrameMT compresses arbitrary input into frames cut into small jobs
// (a test job size of 1 to 4 blocks of 64 KB, so that small inputs make
// several jobs) through the Writer with writes, uncompressed writes and
// flushes chosen by plan: NbWorkers 2 and 7 must produce NbWorkers 1's
// frame, which must decode to the input.
func FuzzFrameMT(f *testing.F) {
	m := creftest.LZ4Inputs(f)
	f.Add(uint8(1), uint16(0x1234), m["text128KiB"])
	f.Add(uint8(0x42), uint16(0xfff0), m["period3-200K"])
	f.Add(uint8(0x83), uint16(7), append(append([]byte(nil), m["random-70K"]...), m["text128KiB"]...))
	f.Fuzz(func(t *testing.T, sel uint8, plan uint16, src []byte) {
		if len(src) > 1<<19 {
			return
		}
		saved := mtJobSizeMax
		mtJobSizeMax = 64 * kb * (1 + int(sel>>6))
		defer func() { mtJobSizeMax = saved }()
		p := Preferences{CompressionLevel: []int{1, -2, 2, 3, 9, 10}[int(sel)%6], AutoFlush: sel&8 != 0}
		p.BlockIndependent = sel&16 != 0
		p.BlockChecksum = sel&32 != 0
		p.ContentChecksum = true
		run := func(nw int) []byte {
			p.NbWorkers = nw
			var buf bytes.Buffer
			w := NewWriter(&buf, WithPreferences(p))
			pl := plan
			for off := 0; off < len(src); {
				n := min(len(src)-off, 1+int(pl&0xff)*601)
				var err error
				if pl&0x300 == 0x300 {
					_, err = w.WriteUncompressed(src[off : off+n])
				} else {
					_, err = w.Write(src[off : off+n])
				}
				if err == nil && pl&0xc00 == 0xc00 {
					err = w.Flush()
				}
				if err != nil {
					t.Fatal(err)
				}
				off += n
				pl = pl>>3 | pl<<13
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		}
		want := run(1)
		for _, nw := range []int{2, 7} {
			if got := run(nw); !bytes.Equal(got, want) {
				t.Fatalf("NbWorkers %d: %d bytes, NbWorkers 1 %d", nw, len(got), len(want))
			}
		}
		dec, err := AppendDecompressFrame(nil, want, nil)
		if err != nil || !bytes.Equal(dec, src) {
			t.Fatalf("decoding: %v", err)
		}
	})
}
