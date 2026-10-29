// Command gobench measures EncodeAll throughput the way `zstd -b<level>`
// (programs/benchzstd.c) does, so the two can be compared by bench/compare.sh.
//
// benchzstd splits the input into independent blocks of -B bytes (the whole
// file when -B is unset) and compresses each with ZSTD_compress2 on one reused
// CCtx. benchfn.c:BMK_benchTimedFn times "runs" of nbLoops passes, sizing
// nbLoops so a run lasts about 1 s and discarding runs shorter than 0.5 s; a
// run's speed is its mean time per pass, and the reported speed is the best
// run seen during -i seconds. ZSTD_c_checksumFlag is left at its default
// (off), hence WithEncoderCRC(false).
// gobench mirrors that: one Encoder (which pools its cctx), one EncodeAll per
// block, ~1 s windows of passes scored by mean time per pass, best of at least
// -iters counted windows and at least -secs seconds of timed passes (compare.sh
// passes -secs equal to zstd's -i).
//
// Output (stdout, one line): "<MB/s> <ratio>", with MB/s = 1e6 bytes/s of
// input and ratio = input/compressed, with the number of decimals zstd prints.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"time"

	"github.com/vahid-sohrabloo/fastzstd/zstd"
)

func main() {
	level := flag.Int("level", 1, "C compression level: 1, 3, 7 or 11")
	block := flag.Int("block", 0, "independent block size in bytes (0 = whole file)")
	iters := flag.Int("iters", 1, "minimum number of counted ~1 s windows")
	secs := flag.Float64("secs", 0, "also keep running until this many seconds of passes have been timed")
	cpuprofile := flag.String("cpuprofile", "", "write a CPU profile of the timed passes to this file")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: gobench [-level N] [-block N] [-iters N] [-secs S] [-cpuprofile F] FILE")
		os.Exit(2)
	}
	if err := run(*level, *block, *iters, *secs, *cpuprofile, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "gobench:", err)
		os.Exit(1)
	}
}

func run(level, block, iters int, secs float64, cpuprofile, path string) (err error) {
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
	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}

	// C: benchfn.c:BMK_benchTimedFn — windows of nbLoops passes; nbLoops is
	// re-estimated to last runBudget (1 s), or multiplied by 10 after a window
	// shorter than runBudget/50; windows shorter than runBudget/2 are not counted.
	const runBudget = time.Second
	best := time.Duration(1<<63 - 1) // best mean time per pass
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
