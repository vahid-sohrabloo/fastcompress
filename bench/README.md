# Benchmarks against C zstd v1.5.7

Setup: `ref/fetch.sh` builds `ref/zstd/programs/zstd` and fetches the Silesia corpus into `bench/corpus/`.

## compare.sh

    bench/compare.sh [-l "1 3 7 11"] [-f "FILE[:BLOCK]..."] [-i SECS] [-g SECS] [-n RUNS] -label NAME

Writes `bench/results/<date>-<label>.md` with columns
`file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio`, plus a geometric mean of "% of C".

- Corpus: each Silesia file whole, plus `silesia.tar` with 1M and 4M independent blocks. `-f` overrides it; `FILE:BLOCK` benchmarks FILE in independent BLOCK-byte blocks.
- Every cell is the median of `-n` (default 3) runs, C and Go interleaved, each pinned with `taskset -c $CPU` (default 2, a P-core on the i9-13900H).
- Every level is ported. compare.sh still writes an `n/a (not ported)` row when gobench fails with a "not ported" message, but that failure no longer occurs.
- A row whose C and Go ratios differ gets `MISMATCH`: output must be byte-identical, so equal ratios are the check.
- Runtime: `-i` (default 5) is the seconds `zstd -b` spends on *each* of its compression and decompression loops; `-g` (default 2) is the minimum seconds of Go timed passes. Level 1 on the full corpus takes about 11 minutes with the defaults; lower `-i`/`-n` for quick checks.

### How the C side is run and parsed

`zstd -b<L> -T1 -i<S> [-B<block>] FILE`. benchzstd.c splits the input into independent `-B` blocks (whole file if unset), compresses each with `ZSTD_compress2` on one reused CCtx, loops for at least `-i` seconds and reports the fastest pass. It never sets `ZSTD_c_checksumFlag`, so checksums are off.

`-T1` is essential: without it the CLI benchmarks with worker threads (even under `taskset`), which produces different, multi-job output.

Progress is redrawn with carriage returns. The script converts CR to LF and takes the last line matching `(xRATIO), C MB/s, D MB/s`, using RATIO and the first speed (compression). `-qq` prints nothing, so it is not used.

## gobench

    go run ./bench/cmd/gobench -level 1 -block 0 -iters 5 [-secs S] FILE

Mirrors the above: one `Encoder` (pooling its cctx), one `EncodeAll` per independent block, `WithEncoderCRC(false)`, an untimed warm-up pass, then the best of `-iters` passes (and at least `-secs` seconds). Prints `MB/s ratio` (1e6 bytes/s; ratio with the same decimals zstd prints (3, 2 or 1 below 10, 100, else)). If EncodeAll panics (an internal error; every level is ported) it exits non-zero with a message.

## pprof

    go test ./zstd -run xxx -bench 'EncodeAll/xml/1' -cpuprofile cpu.prof

`BenchmarkEncodeAll/<file>/<level>` skips missing corpus files.
