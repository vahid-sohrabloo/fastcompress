# Benchmarks against C zstd v1.5.7

Setup: `ref/fetch.sh` builds `ref/zstd/programs/zstd` and fetches the Silesia corpus into `bench/corpus/`.

## compare.sh

    bench/compare.sh [-mode c|d] [-l "1 3 7 11"] [-f "FILE[:BLOCK]..."] [-i SECS] [-g SECS] [-n RUNS] -label NAME

Writes `bench/results/<date>-<label>.md` with columns
`file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio`, plus a geometric mean of "% of C".

`-mode d` (default `c`) compares decompression instead: the columns become `C D-MB/s | Go D-MB/s` (1e6 bytes/s of decoded output), the C speed is the third number of `zstd -b`'s result line, and gobench runs with `-mode d`. The corpus and the ratio check are the same. `-hp` applies to `-mode c` only.

- Corpus: each Silesia file whole, plus `silesia.tar` with 1M, 4M, 128K and 16K independent blocks. At level 11 the 1M/4M rows run row-based lazy2, the 128K row btlazy2 and the 16K row btopt (clevels.h picks parameters by block size). `-f` overrides it; `FILE:BLOCK` benchmarks FILE in independent BLOCK-byte blocks.
- Every cell is the median of `-n` (default 3) runs, C and Go interleaved, each pinned with `taskset -c $CPU` (default 2, a P-core on the i9-13900H).
- Keep the machine idle while benchmarking, in particular CPU 2's SMT sibling, CPU 3 (`/sys/devices/system/cpu/cpu2/topology/thread_siblings_list`): work there shares CPU 2's core and slows whichever side happens to run alongside it. Do not run the test suite concurrently.
- Every level is ported. compare.sh still writes an `n/a (not ported)` row when gobench fails with a "not ported" message, but that failure no longer occurs.
- A row whose C and Go ratios differ gets `MISMATCH`: output must be byte-identical, so equal ratios are the check.
- Runtime: `-i` (default 5) is the seconds `zstd -b` spends on *each* of its compression and decompression loops; `-g` (default 5, the same budget) is the minimum seconds of Go timed passes. The full corpus at all four levels takes over an hour with the defaults; lower `-i`/`-g`/`-n` for quick checks.

### How the C side is run and parsed

`zstd -b<L> -T1 -i<S> [-B<block>] FILE`. benchzstd.c splits the input into independent `-B` blocks (whole file if unset) and compresses each with `ZSTD_compress2` on one reused CCtx. It never sets `ZSTD_c_checksumFlag`, so checksums are off.

The statistic (benchfn.c `BMK_benchTimedFn`): passes are timed in *runs* of `nbLoops` passes, with `nbLoops` re-estimated after each run so the next lasts about 1 s (multiplied by 10 after a run shorter than 20 ms). Runs shorter than 0.5 s are discarded; a run's time is its mean per pass. The reported speed is the best run seen in `-i` seconds. It is therefore a best-of-means, not the best single pass.

`-T1` is essential: without it the CLI benchmarks with worker threads (even under `taskset`), which produces different, multi-job output.

Progress is redrawn with carriage returns. The script converts CR to LF and takes the last line matching `(xRATIO), C MB/s, D MB/s`, using RATIO and the first speed (compression), or the second (decompression) with `-mode d`. `-qq` prints nothing, so it is not used.

Decompression (benchzstd.c `local_defaultDecompress`): each block's frame is decoded with `ZSTD_decompressStream`, given the whole frame and a full-size output buffer, on one reused DCtx. That takes C's single-pass shortcut (`ZSTD_decompressFrame` straight into dst), the same work as Go's `DecodeAll`. The frames are those compressed above (checksum off).

## gobench

    go run ./bench/cmd/gobench -level 1 -block 0 [-iters N] [-secs S] FILE

Mirrors the above: one `Encoder` (pooling its cctx), one `EncodeAll` per independent block, `WithEncoderCRC(false)`, an untimed warm-up pass, then windows of passes sized and scored exactly like benchfn.c's runs (≈1 s, mean time per pass, windows under 0.5 s discarded); it reports the best window after at least `-iters` (default 1) counted windows and `-secs` seconds of timed passes. (Earlier results used the fastest single pass, which reads a few percent higher than C's statistic.) Prints `MB/s ratio` (1e6 bytes/s; ratio with the same decimals zstd prints (3, 2 or 1 below 10, 100, else)). If EncodeAll panics (an internal error; every level is ported) it exits non-zero with a message.

### Decode mode

    go run ./bench/cmd/gobench -mode d -level 3 -block 0 [-secs S] [-cpuprofile F] FILE

Compresses each block once with `EncodeAll(WithEncoderLevel, WithEncoderCRC(false))` (the same frames `zstd -b` decodes), decodes each once and checks the output equals the input, then times passes of one `DecodeAll(frame, out[:0])` per block on one `NewReader(nil, WithDecoderConcurrency(1))` with a reused `out`, in the same benchfn.c windows. Prints `<decoded MB/s> <ratio>`. `-hugepages` is rejected in this mode.

Go reuses one block-sized output buffer for every block, while C decodes block *i* into its own region of one file-sized buffer (`benchzstd.c`). The Task 18 review measured the effect at under 1% (at most about 0.5%, at 1M blocks; none at 16K).

## pprof

    go test ./zstd -run xxx -bench 'EncodeAll/xml/1' -cpuprofile cpu.prof

`BenchmarkEncodeAll/<file>/<level>` skips missing corpus files.

    go test ./zstd -run xxx -bench 'DecodeAll/^silesia.tar$/^3$/^16K$' -cpuprofile cpu.prof

`BenchmarkDecodeAll/<file>/<level>/<block>` covers the compare.sh corpus (block `whole` for each Silesia file; `1M`, `4M`, `128K`, `16K` for silesia.tar). `gobench -mode d -cpuprofile` profiles only the timed passes.

## arm64

The assembly routines have arm64 ports (NEON for the row match finder's tag compare), tested for byte-identical output under qemu. There is no arm64 hardware here, so arm64 speed against C is unverified until arm64 hardware is available; qemu timings say nothing about it.

The same holds for the decoder: the Huffman fast loops, the sequence decode-and-execute loop and the sequence-table builder have arm64 ports, tested for state-identical results against the Go loops under qemu, but arm64 decode speed is unverified. None of them has been benchmarked on arm64.

# Benchmarks against C lz4 v1.11.1

Setup: `ref/fetch.sh` builds `ref/lz4/programs/lz4` and fetches the Silesia corpus.

## compare-lz4.sh

    bench/compare-lz4.sh [-mode c|d] [-l "1 2 9 12"] [-f "FILE..."] [-B BLOCK] [-D] [-S] [-i SECS] [-g SECS] [-n RUNS] -label NAME

Writes `bench/results/<date>-lz4-<label>.md` with columns
`file | level | C MB/s | Go MB/s | % of C | C ratio | Go ratio`, plus the geometric mean of "% of C" per level. Every cell is the median of `-n` (default 3) runs, C and Go interleaved, each pinned with `taskset -c $CPU` (default 2). Keep the machine idle, in particular `$CPU`'s SMT sibling. A row whose ratios differ is flagged `MISMATCH` (the output must be byte-identical).

C side: `lz4 -b<L> -T1 -B<BLOCK> -i<SECS> FILE` (default `-B4M`). bench.c compresses each file into one frame with the CLI's preferences (independent blocks, no checksums) in 4 MiB `LZ4F_compressUpdate` chunks for a standard block size (64K, 256K, 1M, 4M), or one block per update with autoFlush for a custom size; a file smaller than the block goes through `LZ4F_compressFrame`. `-T1` runs C's multi-threaded job mode with one worker thread, whose frames are the single-threaded ones for independent blocks; `-S` runs `--single-thread` instead (about 2% faster here). `-D` (linked blocks, `-BD`) always uses `--single-thread`: C's job mode cuts linked frames into 1 MiB jobs, which changes the output and is not ported. Decompression decodes the frame with `LZ4F_decompress`. The statistic is the best mean time per pass over runs of about 1 s. The script takes the last progress line, `(RATIO), C MB/s, D MB/s`.

## lz4bench

    go run ./bench/cmd/lz4bench [-mode c|d] [-level N] [-block N] [-linked] [-secs S] [-cpuprofile F] FILE

Mirrors bench.c: one `lz4.FrameCompressor` reused across passes (`Begin`, one `Update` per 4 MiB chunk or per custom-size block with autoFlush, `End`; `CompressFrame` for a file smaller than the block), `lz4.AppendDecompressFrame` into a buffer of the file's size for `-mode d`, the same timing statistic. `-linked` uses linked blocks (`lz4 -BD`). Prints `MB/s ratio`; it checks that the decoded output equals the input.
