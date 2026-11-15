// Command lz4bench measures frame compression or decompression throughput the
// way `lz4 -b<level> -T1 -B<size>` (lz4 v1.11.1 programs/bench.c) does, so the
// two can be compared by bench/compare-lz4.sh.
//
// bench.c compresses each file into one frame with the CLI's preferences:
// independent blocks (linked with -BD, lz4bench -linked), no checksums, and with a custom block size (-B4M) the
// block size ID that holds it, autoFlush, and one LZ4F_compressUpdate per
// block-sized chunk (unstable input), on one reused LZ4F_cctx; a file smaller
// than the block is compressed with LZ4F_compressFrame. Decompression decodes
// the frame with LZ4F_decompress into a buffer of the file's size. A run is
// about 1 s of passes; the reported speed is the best mean time per pass
// over the runs of -secs seconds (bench.c's statistic).
//
// lz4bench mirrors that with lz4.FrameCompressor (Begin, Update per chunk,
// End) and lz4.AppendDecompressFrame. It checks that the decoded output equals
// the input. Output (stdout, one line): "<MB/s> <ratio>", with MB/s = 1e6
// bytes/s of input and ratio = input/compressed with 3 decimals.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"runtime/pprof"
	"time"

	"github.com/vahid-sohrabloo/fastcompress/lz4"
)

func main() {
	mode := flag.String("mode", "c", "c (compression) or d (decompression)")
	level := flag.Int("level", 1, "compression level: below 2 the fast compressor (negative: acceleration -level+1), 2 to 12 LZ4HC")
	block := flag.Int("block", 4<<20, "block size in bytes (lz4 -B; 64 KiB, 256 KiB, 1 MiB or 4 MiB, or any custom size from 32 bytes)")
	secs := flag.Float64("secs", 3, "seconds of timed passes")
	cpuprofile := flag.String("cpuprofile", "", "write a CPU profile of the timed passes to this file")
	linked := flag.Bool("linked", false, "linked blocks (lz4 -BD)")
	flag.Parse()
	if flag.NArg() != 1 || (*mode != "c" && *mode != "d") {
		fmt.Fprintln(os.Stderr, "usage: lz4bench [-mode c|d] [-level N] [-block N] [-linked] [-secs S] [-cpuprofile F] FILE")
		os.Exit(2)
	}
	if err := run(*mode == "d", *level, *block, *linked, *secs, *cpuprofile, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "lz4bench:", err)
		os.Exit(1)
	}
}

// blockSizeID is C's LZ4IO_setBlockSize: the ID of the smallest standard
// size holding blockSize, and whether blockSize is custom (not that size).
func blockSizeID(blockSize int) (lz4.BlockSizeID, bool) {
	bs := blockSize - 1
	id := 0
	for bs >>= 2; bs != 0; bs >>= 2 {
		id++
	}
	if id < 7 {
		id = 7
	}
	std, _ := lz4.BlockSize(lz4.BlockSizeID(id - 3))
	return lz4.BlockSizeID(id - 3), blockSize != std
}

func run(decode bool, level, block int, linked bool, secs float64, cpuprofile, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if block < 32 {
		block = 32
	}
	if block > 4<<20 {
		block = 4 << 20
	}
	id, custom := blockSizeID(block)
	prefs := lz4.Preferences{CompressionLevel: level, AutoFlush: custom}
	prefs.BlockSizeID = id
	prefs.BlockIndependent = !linked
	chunk := 4 << 20 // bench.c BMK_CHUNK_SIZE
	if custom {
		chunk = block
	}

	var fc lz4.FrameCompressor
	frame := make([]byte, lz4.CompressFrameBound(len(src), &prefs)+len(src)/block*8+lz4.CompressBoundFrame(chunk, &prefs)+64)
	compress := func() (int, error) {
		if len(src) < block {
			return fc.CompressFrame(frame, src, &prefs)
		}
		pos, err := fc.Begin(frame, &prefs)
		if err != nil {
			return 0, err
		}
		for done := 0; done < len(src); done += chunk {
			n, err := fc.Update(frame[pos:], src[done:min(done+chunk, len(src))])
			if err != nil {
				return 0, err
			}
			pos += n
		}
		n, err := fc.End(frame[pos:])
		return pos + n, err
	}
	csize, err := compress()
	if err != nil {
		return err
	}
	out := make([]byte, 0, len(src))
	got, err := lz4.AppendDecompressFrame(out, frame[:csize], nil)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, src) {
		return fmt.Errorf("decoded output differs from the input")
	}
	pass := func() { compress() }
	if decode {
		pass = func() { lz4.AppendDecompressFrame(out[:0], frame[:csize], nil) }
	}
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
	// bench.c: runs of nbLoops passes, nbLoops re-estimated after each run to
	// last about 1 s; the speed is the best mean time per pass.
	best := time.Duration(1<<63 - 1)
	nbLoops := 1
	var spent time.Duration
	for spent.Seconds() < secs {
		t := time.Now()
		for i := 0; i < nbLoops; i++ {
			pass()
		}
		d := time.Since(t)
		spent += d
		if d <= 0 {
			nbLoops *= 100
			continue
		}
		best = min(best, d/time.Duration(nbLoops))
		nbLoops = int(time.Second/best) + 1
	}
	fmt.Printf("%.1f %.3f\n", float64(len(src))/best.Seconds()/1e6, float64(len(src))/float64(csize))
	return nil
}
