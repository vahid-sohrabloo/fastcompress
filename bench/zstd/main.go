// Command zstdbench times fastcompress/zstd against klauspost/compress/zstd
// with the same method as bench/cmd/gobench (which mirrors `zstd -b`): one
// reused Encoder or Decoder, one EncodeAll or DecodeAll per independent block,
// ~1 s windows of passes scored by mean time per pass, best window reported.
// With -mode d every library decodes the same frames, produced by our
// encoder (byte-identical to C), so the decoders see identical input.
// Output (stdout, one line): "<MB/s> <ratio>", MB/s = 1e6 bytes/s of input
// (decoded output with -mode d).
//
// Levels 1, 3, 7 and 11 map to klauspost's SpeedFastest, SpeedDefault,
// SpeedBetterCompression and SpeedBestCompression. The two libraries do not
// produce the same bytes (ours is byte-identical to C), so the ratio is
// reported next to the speed.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	kzstd "github.com/klauspost/compress/zstd"
	ozstd "github.com/vahid-sohrabloo/fastcompress/zstd"
)

type codec struct {
	encode func(src, dst []byte) []byte
	decode func(frame, dst []byte) ([]byte, error)
	maxEnc func(n int) int
}

func newOurs(level int) (codec, error) {
	var l ozstd.EncoderLevel
	switch level {
	case 1:
		l = ozstd.SpeedFastest
	case 3:
		l = ozstd.SpeedDefault
	case 7:
		l = ozstd.SpeedBetterCompression
	case 11:
		l = ozstd.SpeedBestCompression
	default:
		return codec{}, fmt.Errorf("unsupported level %d (want 1, 3, 7 or 11)", level)
	}
	enc, err := ozstd.NewWriter(nil, ozstd.WithEncoderLevel(l), ozstd.WithEncoderCRC(false))
	if err != nil {
		return codec{}, err
	}
	dec, err := ozstd.NewReader(nil, ozstd.WithDecoderConcurrency(1))
	if err != nil {
		return codec{}, err
	}
	return codec{enc.EncodeAll, dec.DecodeAll, enc.MaxEncodedSize}, nil
}

func newKlauspost(level int) (codec, error) {
	var l kzstd.EncoderLevel
	switch level {
	case 1:
		l = kzstd.SpeedFastest
	case 3:
		l = kzstd.SpeedDefault
	case 7:
		l = kzstd.SpeedBetterCompression
	case 11:
		l = kzstd.SpeedBestCompression
	default:
		return codec{}, fmt.Errorf("unsupported level %d (want 1, 3, 7 or 11)", level)
	}
	enc, err := kzstd.NewWriter(nil, kzstd.WithEncoderLevel(l), kzstd.WithEncoderCRC(false), kzstd.WithEncoderConcurrency(1))
	if err != nil {
		return codec{}, err
	}
	dec, err := kzstd.NewReader(nil, kzstd.WithDecoderConcurrency(1))
	if err != nil {
		return codec{}, err
	}
	return codec{enc.EncodeAll, dec.DecodeAll, enc.MaxEncodedSize}, nil
}

func main() {
	lib := flag.String("lib", "ours", "ours or klauspost")
	mode := flag.String("mode", "c", "c (compression) or d (decompression)")
	level := flag.Int("level", 1, "level: 1, 3, 7 or 11")
	block := flag.Int("block", 0, "independent block size in bytes (0 = whole file)")
	iters := flag.Int("iters", 1, "minimum number of counted ~1 s windows")
	secs := flag.Float64("secs", 0, "also keep running until this many seconds of passes have been timed")
	flag.Parse()
	if flag.NArg() != 1 || (*mode != "c" && *mode != "d") || (*lib != "ours" && *lib != "klauspost") {
		fmt.Fprintln(os.Stderr, "usage: zstdbench [-lib ours|klauspost] [-mode c|d] [-level N] [-block N] [-iters N] [-secs S] FILE")
		os.Exit(2)
	}
	if err := run(*lib, *mode == "d", *level, *block, *iters, *secs, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "zstdbench:", err)
		os.Exit(1)
	}
}

func run(lib string, decode bool, level, block, iters int, secs float64, path string) error {
	mk := newOurs
	if lib == "klauspost" {
		mk = newKlauspost
	}
	c, err := mk(level)
	if err != nil {
		return err
	}
	// Frames to decode come from our encoder whichever library is timed.
	fc := c
	if decode && lib != "ours" {
		if fc, err = newOurs(level); err != nil {
			return err
		}
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(src) == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	if block <= 0 || block > len(src) {
		block = len(src)
	}
	dst := make([]byte, 0, c.maxEnc(block))
	pass := func() (csize int) {
		for off := 0; off < len(src); off += block {
			csize += len(c.encode(src[off:min(off+block, len(src))], dst[:0]))
		}
		return csize
	}
	var csize int
	if !decode {
		csize = pass() // untimed warm-up
	} else {
		var frames [][]byte
		for off := 0; off < len(src); off += block {
			fr := fc.encode(src[off:min(off+block, len(src))], nil)
			frames = append(frames, fr)
			csize += len(fr)
		}
		out := make([]byte, 0, block)
		for i, fr := range frames { // also the untimed warm-up
			got, err := c.decode(fr, out[:0])
			if err != nil {
				return fmt.Errorf("block %d: %w", i, err)
			}
			if !bytes.Equal(got, src[i*block:min((i+1)*block, len(src))]) {
				return fmt.Errorf("block %d: decoded output differs from the input", i)
			}
		}
		pass = func() int {
			for _, fr := range frames {
				out, _ = c.decode(fr, out[:0])
			}
			return 0
		}
	}
	// Same statistic as zstd -b (benchfn.c:BMK_benchTimedFn): windows of
	// nbLoops passes sized to about 1 s, windows under 0.5 s not counted,
	// best mean time per pass reported.
	const runBudget = time.Second
	best := time.Duration(1<<63 - 1)
	nbLoops, counted := 1, 0
	var spent time.Duration
	for counted < iters || spent.Seconds() < secs {
		t := time.Now()
		for i := 0; i < nbLoops; i++ {
			pass()
		}
		d := time.Since(t)
		spent += d
		perPass := d / time.Duration(nbLoops)
		if d > runBudget/50 {
			nbLoops = int(runBudget/min(best, perPass)) + 1
		} else {
			nbLoops *= 10
		}
		if d < runBudget/2 {
			continue
		}
		counted++
		best = min(best, perPass)
	}
	ratio := float64(len(src)) / float64(csize)
	fmt.Printf("%.1f %.3f\n", float64(len(src))/best.Seconds()/1e6, ratio)
	return nil
}
