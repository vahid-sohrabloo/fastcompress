// Command pierrecbench times github.com/pierrec/lz4/v4 frames with the method
// of bench/cmd/lz4bench (which mirrors `lz4 -b<level> -T1 -B<size>`), so that
// bench/compare-lz4.sh -p can add it as a column next to C and this port.
//
// Compression: one reused lz4.Writer per pass (Reset into a reused buffer,
// Write in 4 MiB chunks as bench.c's BMK_CHUNK_SIZE, Close), independent
// blocks of -block bytes (64 KiB, 256 KiB, 1 MiB or 4 MiB), no checksums,
// ConcurrencyOption(1). Decompression: one reused lz4.Reader per pass (Reset,
// read into a buffer of the file's size), decoding the frame that this port's
// FrameCompressor produces for the same level and block size, as lz4bench
// -mode d does: the frame is byte-identical to C's, so every decoder sees the
// same input. A run is about 1 s of passes; the reported speed is the best
// mean time per pass over the runs of -secs seconds.
//
// Levels: pierrec's levels are its own and its output differs from C's (the
// ratio is reported next to the speed). -level 1 runs its default Fast
// compressor; -level 2 to 12 run CompressorHC, whose LevelN searches up to
// 1<<(8+N) candidates: by default -level 2 to 9 map to Level1 (512
// candidates, the nearest to C's 256 at level 9) and -level 10 to 12 to
// Level9 (its highest; pierrec has no optimal parser).
// -plevel N (0 = Fast, 1 to 9 = LevelN) overrides the mapping.
//
// Output (stdout, one line): "<MB/s> <ratio>", with MB/s = 1e6 bytes/s of
// input (decoded output with -mode d) and ratio = input/compressed with 3
// decimals.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	plz4 "github.com/pierrec/lz4/v4"
	"github.com/vahid-sohrabloo/fastcompress/lz4"
)

func main() {
	mode := flag.String("mode", "c", "c (compression) or d (decompression)")
	level := flag.Int("level", 1, "C level to map: 1 (Fast), 9 or 12 (CompressorHC); see -plevel")
	plevel := flag.Int("plevel", -1, "pierrec level: 0 = Fast, 1 to 9 = LevelN (overrides the -level mapping)")
	block := flag.Int("block", 4<<20, "block size in bytes: 65536, 262144, 1048576 or 4194304")
	secs := flag.Float64("secs", 3, "seconds of timed passes")
	flag.Parse()
	if flag.NArg() != 1 || (*mode != "c" && *mode != "d") {
		fmt.Fprintln(os.Stderr, "usage: pierrecbench [-mode c|d] [-level N] [-plevel N] [-block N] [-secs S] FILE")
		os.Exit(2)
	}
	if err := run(*mode == "d", *level, *plevel, *block, *secs, flag.Arg(0)); err != nil {
		fmt.Fprintln(os.Stderr, "pierrecbench:", err)
		os.Exit(1)
	}
}

// pierrecLevel maps a C level to pierrec's CompressionLevel.
func pierrecLevel(level, plevel int) (plz4.CompressionLevel, error) {
	if plevel < 0 {
		switch {
		case level < 2:
			plevel = 0
		case level <= 9:
			plevel = 1
		case level <= 12:
			plevel = 9
		default:
			return 0, fmt.Errorf("unsupported level %d", level)
		}
	}
	if plevel == 0 {
		return plz4.Fast, nil
	}
	if plevel > 9 {
		return 0, fmt.Errorf("unsupported -plevel %d (want 0 to 9)", plevel)
	}
	return plz4.Level1 << (plevel - 1), nil
}

func blockSizes(block int) (plz4.BlockSize, lz4.BlockSizeID, error) {
	switch block {
	case 64 << 10:
		return plz4.Block64Kb, lz4.Block64KB, nil
	case 256 << 10:
		return plz4.Block256Kb, lz4.Block256KB, nil
	case 1 << 20:
		return plz4.Block1Mb, lz4.Block1MB, nil
	case 4 << 20:
		return plz4.Block4Mb, lz4.Block4MB, nil
	}
	return 0, 0, fmt.Errorf("unsupported block size %d (want 64 KiB, 256 KiB, 1 MiB or 4 MiB)", block)
}

// ourFrame compresses src as lz4bench does for a standard block size: one
// frame, independent blocks, no checksums, one Update per 4 MiB chunk.
func ourFrame(src []byte, level int, id lz4.BlockSizeID, block int) ([]byte, error) {
	prefs := lz4.Preferences{CompressionLevel: level}
	prefs.BlockSizeID = id
	prefs.BlockIndependent = true
	const chunk = 4 << 20
	frame := make([]byte, lz4.CompressFrameBound(len(src), &prefs)+lz4.CompressBoundFrame(chunk, &prefs)+64)
	var fc lz4.FrameCompressor
	if len(src) < block {
		n, err := fc.CompressFrame(frame, src, &prefs)
		return frame[:n], err
	}
	pos, err := fc.Begin(frame, &prefs)
	if err != nil {
		return nil, err
	}
	for done := 0; done < len(src); done += chunk {
		n, err := fc.Update(frame[pos:], src[done:min(done+chunk, len(src))])
		if err != nil {
			return nil, err
		}
		pos += n
	}
	n, err := fc.End(frame[pos:])
	return frame[:pos+n], err
}

func run(decode bool, level, plevel, block int, secs float64, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(src) == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	pl, err := pierrecLevel(level, plevel)
	if err != nil {
		return err
	}
	pbs, id, err := blockSizes(block)
	if err != nil {
		return err
	}

	var pass func() error
	var csize int
	if !decode {
		w := plz4.NewWriter(nil)
		if err := w.Apply(plz4.BlockSizeOption(pbs), plz4.CompressionLevelOption(pl),
			plz4.ChecksumOption(false), plz4.BlockChecksumOption(false), plz4.ConcurrencyOption(1)); err != nil {
			return err
		}
		var buf bytes.Buffer
		buf.Grow(len(src) + len(src)/100 + 1<<16)
		const chunk = 4 << 20 // bench.c BMK_CHUNK_SIZE
		pass = func() error {
			buf.Reset()
			w.Reset(&buf)
			for done := 0; done < len(src); done += chunk {
				if _, err := w.Write(src[done:min(done+chunk, len(src))]); err != nil {
					return err
				}
			}
			return w.Close()
		}
		if err := pass(); err != nil { // also the untimed warm-up
			return err
		}
		csize = buf.Len()
		// Check pierrec's frame with this port's decoder.
		got, err := lz4.AppendDecompressFrame(nil, buf.Bytes(), nil)
		if err != nil {
			return fmt.Errorf("decoding pierrec's frame: %w", err)
		}
		if !bytes.Equal(got, src) {
			return errors.New("pierrec's frame decodes to other bytes than the input")
		}
	} else {
		frame, err := ourFrame(src, level, id, block)
		if err != nil {
			return err
		}
		csize = len(frame)
		var br bytes.Reader
		r := plz4.NewReader(nil)
		if err := r.Apply(plz4.ConcurrencyOption(1)); err != nil {
			return err
		}
		// One block of slack, so that the last block also decodes in place.
		out := make([]byte, len(src)+block)
		var n int
		pass = func() error {
			br.Reset(frame)
			r.Reset(&br)
			var err error
			n, err = io.ReadFull(r, out)
			if err == io.ErrUnexpectedEOF {
				err = nil
			}
			return err
		}
		if err := pass(); err != nil { // also the untimed warm-up
			return err
		}
		if !bytes.Equal(out[:n], src) {
			return errors.New("decoded output differs from the input")
		}
	}

	// bench.c: runs of nbLoops passes, nbLoops re-estimated after each run to
	// last about 1 s; the speed is the best mean time per pass.
	best := time.Duration(1<<63 - 1)
	nbLoops := 1
	var spent time.Duration
	for spent.Seconds() < secs {
		t := time.Now()
		for i := 0; i < nbLoops; i++ {
			if err := pass(); err != nil {
				return err
			}
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
