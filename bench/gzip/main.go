// gzbench: single-thread DEFLATE/gzip throughput of ours, compress/flate
// (stdlib) and klauspost/compress, with the same method and output as
// ref/ldref/ldbench.c. usage:
//
//	gzbench -lib ours|stdlib|klauspost -level N -mode c|d [-block B] [-secs S]
//	        [-iters N] [-format raw|gzip] [-zin PATH] [-cpuprofile PATH] [-memprofile PATH] FILE
//
// prints "MB/s ratio bytes" (1e6 bytes/s of uncompressed data; ratio = in/out;
// bytes = exact total compressed size).
// -zin PATH (mode d only): use the compressed blocks recorded in PATH (records
// "u64 LE length, bytes" per block, written by `ldbench -dumpz`) instead of
// compressing FILE, so a decode-only library decodes libdeflate's streams.
// Helpers for compare.sh: `gzbench stat median` (numbers on stdin) and
// `gzbench stat summary [-info] [-levels "0 1 ..."] [-modes "c d"] [-why-c TEXT] [-why-d TEXT]` (tab-separated "mode level label pct [status]" on stdin).
//
// Exit codes: 1 failure, 2 usage, 3 library not ported yet, 4 unsupported level.
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "stat" {
		os.Exit(statMain(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	fs := flag.NewFlagSet("gzbench", flag.ContinueOnError)
	lib := fs.String("lib", "stdlib", "ours|stdlib|klauspost")
	level := fs.Int("level", 6, "compression level (0..12)")
	mode := fs.String("mode", "c", "c|d")
	block := fs.Int("block", 0, "independent block size in bytes (0 = whole file)")
	secs := fs.Float64("secs", 3, "minimum seconds of timed windows")
	iters := fs.Int("iters", 1, "minimum number of counted (>= 0.5 s) windows")
	format := fs.String("format", "raw", "raw|gzip")
	cpuProf := fs.String("cpuprofile", "", "write a CPU profile of the timed windows only (not prepare) to this file")
	memProf := fs.String("memprofile", "", "write an allocs profile after the timed windows to this file")
	zinPath := fs.String("zin", "", "precompressed blocks (ldbench -dumpz); mode d only")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 1 || (*mode != "c" && *mode != "d") || (*format != "raw" && *format != "gzip") ||
		*level < 0 || *level > 12 || *block < 0 || !(*secs > 0 && *secs <= 1e6) || *iters < 0 || (*zinPath != "" && *mode != "d") {
		fmt.Fprintln(os.Stderr, "usage: gzbench -lib ours|stdlib|klauspost -level N -mode c|d [-block B] [-secs S] [-iters N] [-format raw|gzip] [-zin PATH] [-cpuprofile PATH] [-memprofile PATH] FILE")
		os.Exit(2)
	}
	var ad Adapter
	switch *lib {
	case "ours":
		ad = ours
	case "stdlib":
		ad = stdAdapter{}
	case "klauspost":
		ad = kpAdapter{}
	default:
		fmt.Fprintf(os.Stderr, "gzbench: unknown -lib %q\n", *lib)
		os.Exit(2)
	}
	in, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fatal(err)
	}
	var zin []byte
	if *zinPath != "" {
		if zin, err = os.ReadFile(*zinPath); err != nil {
			fatal(err)
		}
	}
	mbs, ratio, zn, err := run(ad, in, *level, *mode == "d", *block, *secs, *iters, *format, zin, &profiler{*cpuProf, *memProf})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gzbench: %v\n", err)
		switch {
		case errors.Is(err, ErrUnsupportedLevel):
			os.Exit(4)
		case errors.Is(err, ErrNotPorted):
			os.Exit(3)
		}
		os.Exit(1)
	}
	fmt.Printf("%.1f %.3f %d\n", mbs, ratio, zn)
}

// profiler writes pprof files around the timed windows only (after prepare).
// A nil profiler or empty paths do nothing.
type profiler struct{ cpu, mem string }

func (p *profiler) start() (stop func() error, err error) {
	stop = func() error { return nil }
	if p == nil {
		return stop, nil
	}
	var cf *os.File
	if p.cpu != "" {
		if cf, err = os.Create(p.cpu); err != nil {
			return nil, err
		}
		if err = pprof.StartCPUProfile(cf); err != nil {
			cf.Close()
			return nil, err
		}
	}
	return func() error {
		if cf != nil {
			pprof.StopCPUProfile()
			if err := cf.Close(); err != nil {
				return err
			}
		}
		if p.mem != "" {
			f, err := os.Create(p.mem)
			if err != nil {
				return err
			}
			if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		}
		return nil
	}, nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "gzbench: %v\n", err)
	os.Exit(1)
}

// bench holds the prepared state of one run: independent blocks compressed
// into z, with one reused compressor and decompressor.
type bench struct {
	in, z, back  []byte
	comp         Compressor
	decomp       Decompressor
	n, nb, block int
	bcap         int
	zl           []int
	zn           int
}

func (b *bench) blen(i int) int {
	if i+1 == b.nb {
		return b.n - i*b.block
	}
	return b.block
}
func (b *bench) src(i int) []byte  { return b.in[i*b.block : i*b.block+b.blen(i)] }
func (b *bench) zbuf(i int) []byte { return b.z[i*b.bcap : (i+1)*b.bcap] }
func (b *bench) zdat(i int) []byte { return b.z[i*b.bcap : i*b.bcap+b.zl[i]] }
func (b *bench) dst(i int) []byte  { return b.back[i*b.block : i*b.block+b.blen(i)] }

// prepare compresses every block once and verifies the round trip before any
// timing, like ldbench.
// With zin (records "u64 LE length, bytes", one per block) the streams are taken
// from there and no compressor is created.
func prepare(ad Adapter, in []byte, level, block int, format string, zin []byte) (*bench, error) {
	b := &bench{in: in, n: len(in), block: block, nb: 1}
	if b.block == 0 || b.block > b.n {
		b.block = b.n
	}
	if b.block == 0 {
		b.block = 1
	}
	if b.n > 0 {
		b.nb = (b.n + b.block - 1) / b.block
	}
	var err error
	if zin == nil {
		if b.comp, err = ad.NewCompressor(level, format); err != nil {
			return nil, err
		}
	}
	if b.decomp, err = ad.NewDecompressor(format); err != nil {
		return nil, err
	}
	b.bcap = b.block + b.block/2 + 1024
	b.z = make([]byte, b.nb*b.bcap)
	b.zl = make([]int, b.nb)
	b.back = make([]byte, b.n)
	for i := 0; i < b.nb; i++ {
		var l int
		if zin != nil {
			if l, zin, err = nextRecord(b.zbuf(i), zin); err != nil {
				return nil, fmt.Errorf("-zin block %d: %w", i, err)
			}
		} else if l, err = b.comp.Compress(b.zbuf(i), b.src(i)); err != nil {
			return nil, fmt.Errorf("compress failed: %w", err)
		}
		b.zl[i], b.zn = l, b.zn+l
	}
	if zin != nil && len(zin) != 0 {
		return nil, errors.New("-zin: more records than blocks")
	}
	for i := 0; i < b.nb; i++ {
		l, err := b.decomp.Decompress(b.dst(i), b.zdat(i))
		if err != nil {
			return nil, fmt.Errorf("decompress failed: %w", err)
		}
		if l != b.blen(i) {
			return nil, errors.New("roundtrip mismatch")
		}
	}
	if !bytes.Equal(in, b.back) {
		return nil, errors.New("roundtrip mismatch")
	}
	return b, nil
}

// nextRecord copies the next "u64 LE length, bytes" record of zin into dst and
// returns its length and the rest of zin.
func nextRecord(dst, zin []byte) (int, []byte, error) {
	if len(zin) < 8 {
		return 0, nil, errors.New("truncated record header")
	}
	n := binary.LittleEndian.Uint64(zin)
	zin = zin[8:]
	if n > uint64(len(dst)) || n > uint64(len(zin)) {
		return 0, nil, fmt.Errorf("record of %d bytes does not fit", n)
	}
	copy(dst, zin[:n])
	return int(n), zin[n:], nil
}

// run times compression or decompression of in split into independent
// block-byte blocks (0 = one block). Returns MB/s and in/out ratio.
func run(ad Adapter, in []byte, level int, dec bool, block int, secs float64, iters int, format string, zin []byte, prof *profiler) (float64, float64, int, error) {
	b, err := prepare(ad, in, level, block, format, zin)
	if err != nil {
		return 0, 0, 0, err
	}
	var passErr error
	pass := func(loops int) {
		for l := 0; l < loops; l++ {
			for i := 0; i < b.nb; i++ {
				var err error
				if dec {
					_, err = b.decomp.Decompress(b.dst(i), b.zdat(i))
				} else {
					_, err = b.comp.Compress(b.zbuf(i), b.src(i))
				}
				if err != nil && passErr == nil {
					passErr = err
				}
			}
		}
	}
	stop, err := prof.start()
	if err != nil {
		return 0, 0, 0, err
	}
	t0 := time.Now()
	mbs := Measure(pass, float64(b.n), secs, iters, func() float64 { return time.Since(t0).Seconds() })
	if err := stop(); err != nil {
		return 0, 0, 0, err
	}
	if passErr != nil {
		return 0, 0, 0, passErr
	}
	if mbs == 0 {
		return 0, 0, 0, errors.New("no window >= 0.5s")
	}
	return mbs, float64(b.n) / float64(b.zn), b.zn, nil
}

func statMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch args[0] {
	case "median":
		var xs []float64
		for {
			var x float64
			if _, err := fmt.Fscan(stdin, &x); err != nil {
				if err == io.EOF {
					break
				}
				fmt.Fprintf(stderr, "gzbench stat median: %v\n", err)
				return 1
			}
			xs = append(xs, x)
		}
		if len(xs) == 0 {
			fmt.Fprintln(stderr, "gzbench stat median: no input")
			return 1
		}
		fmt.Fprintf(stdout, "%v\n", Median(xs))
	case "summary":
		o := SummaryOpts{Why: map[string]string{}}
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "-info":
				o.Info = true
			case args[i] == "-dry":
				o.Dry = true
			case args[i] == "-levels" && i+1 < len(args):
				i++
				o.Levels = strings.Fields(args[i])
			case args[i] == "-modes" && i+1 < len(args):
				i++
				o.Modes = strings.Fields(args[i])
			case (args[i] == "-why-c" || args[i] == "-why-d") && i+1 < len(args):
				o.Why[args[i][len(args[i])-1:]] = args[i+1]
				i++
			default:
				fmt.Fprintf(stderr, "gzbench stat summary: bad argument %q\n", args[i])
				return 2
			}
		}
		rows, err := ParseRows(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "gzbench stat summary: %v\n", err)
			return 1
		}
		WriteSummary(stdout, rows, o)
	default:
		fmt.Fprintf(stderr, "gzbench stat: unknown command %q\n", args[0])
		return 2
	}
	return 0
}
