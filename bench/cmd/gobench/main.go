// Command gobench measures EncodeAll throughput the way `zstd -b<level>`
// (programs/benchzstd.c) does, so the two can be compared by bench/compare.sh.
//
// benchzstd splits the input into independent blocks of -B bytes (the whole
// file when -B is unset), compresses each with ZSTD_compress2 on one reused
// CCtx, repeats the pass for at least -i seconds and reports the fastest pass.
// ZSTD_c_checksumFlag is left at its default (off), hence WithEncoderCRC(false).
// gobench mirrors that: one Encoder (which pools its cctx), one EncodeAll per
// block, best of -iters passes (and at least -secs seconds).
//
// Output (stdout, one line): "<MB/s> <ratio>", with MB/s = 1e6 bytes/s of
// input and ratio = input/compressed, with the number of decimals zstd prints.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/zstd"
)

func main() {
	level := flag.Int("level", 1, "C compression level: 1, 3, 7 or 11")
	block := flag.Int("block", 0, "independent block size in bytes (0 = whole file)")
	iters := flag.Int("iters", 5, "minimum number of timed passes")
	secs := flag.Float64("secs", 0, "also keep running until this many seconds have elapsed")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: gobench [-level N] [-block N] [-iters N] [-secs S] FILE")
		os.Exit(2)
	}
	if err := run(*level, *block, *iters, *secs, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "gobench:", err)
		os.Exit(1)
	}
}

func run(level, block, iters int, secs float64, path string) (err error) {
	var l zstd.EncoderLevel
	switch level {
	case 1:
		l = zstd.SpeedFastest
	case 3:
		l = zstd.SpeedDefault
	case 7:
		l = zstd.SpeedBetterCompression
	case 11:
		l = zstd.SpeedBestCompression
	default:
		return fmt.Errorf("unsupported level %d (want 1, 3, 7 or 11)", level)
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
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(l), zstd.WithEncoderCRC(false))
	if err != nil {
		return err
	}
	// Every level is ported; EncodeAll panics only on an internal error,
	// which is reported as an error.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("level %d: %v", level, r)
		}
	}()

	dst := make([]byte, 0, enc.MaxEncodedSize(block))
	pass := func() (csize int) {
		for off := 0; off < len(src); off += block {
			end := min(off+block, len(src))
			csize += len(enc.EncodeAll(src[off:end], dst[:0]))
		}
		return csize
	}

	csize := pass() // untimed warm-up: allocates the pooled cctx, faults in pages
	best := time.Duration(1<<63 - 1)
	start := time.Now()
	for i := 0; i < iters || time.Since(start).Seconds() < secs; i++ {
		t := time.Now()
		pass()
		if d := time.Since(t); d < best {
			best = d
		}
	}
	ratio := float64(len(src)) / float64(csize)
	// C: benchzstd.c:BMK_benchMemAdvancedNoAlloc — ratio digits are 3, 2 or 1 below 10, 100, else.
	digits := 1
	if ratio < 100 {
		digits++
	}
	if ratio < 10 {
		digits++
	}
	fmt.Printf("%.1f %.*f\n", float64(len(src))/best.Seconds()/1e6, digits, ratio)
	return nil
}
