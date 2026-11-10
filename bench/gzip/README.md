# gzip / DEFLATE benchmarks against libdeflate v1.26

This directory is a **nested Go module** (`github.com/vahid-sohrabloo/fastcompress/bench/gzip`) so the black-box
references (`compress/flate`, `github.com/klauspost/compress`) never enter the root module's `go.mod`. Run its
tests with `cd bench/gzip && go test ./...`.

Setup: `ref/fetch.sh` fetches libdeflate (`ref/libdeflate`) and the Silesia corpus (`bench/corpus/`);
`make -C ref/ldref` builds `ldref` and `ldbench` (compare.sh runs it on every start; it is a no-op when current). Set `CORPUS=DIR` to use a
corpus elsewhere (`DIR/silesia/*`, `DIR/silesia.tar`).

## compare.sh

    bench/gzip/compare.sh [-mode c|d|both] [-l "1 2 6 9 12"] [-f "FILE[:BLOCK]..."] [-i SECS] [-g SECS] [-n RUNS]
        [-c CPU] [-libs "ld ours stdlib klauspost isal"] [-format raw|gzip] [-noasm] [-quick] [-force]
        [-dry-run] [-prof DIR] [-o PATH] -label NAME

Writes `bench/results/<date>-gzip-<label>.md`, or `-o PATH` (a relative PATH is relative to the repository root, the
script changes there). Needs bash >= 4.4 (checked at start). Defaults: `-mode both`, levels 0..12, `-i 5`
(C and ISA-L seconds), `-g` = `-i` (Go seconds), `-n 3`, `-c 2`, `-format raw`.

- **Corpus**: each Silesia file whole, plus `silesia.tar` in independent 1M, 128K and 16K blocks. `FILE:BLOCK`
  benchmarks FILE in independent BLOCK-byte blocks (bare names resolve against `$CORPUS/silesia`). `-quick` =
  files `xml reymont sao`, levels `1 6 12` (smoke use; override with `-f`/`-l`).
- **Timing** (identical for `ldbench` and `gzbench`, copied from benchfn.c via the zstd bench): windows of
  passes sized to about 1 s, window time = mean per pass, windows under 0.5 s discarded, best window reported;
  MB/s = 1e6 bytes of *uncompressed* data per second. `-block B` = independent B-byte blocks on one reused
  (de)compressor. Round trip is verified before timing. Each cell is the median of `-n` runs, all libraries
  interleaved per round, each pinned with `taskset -c $CPU`.
- **Columns**: `file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s`
  (+ `isal MB/s`). "C" = `ref/ldref/ldbench`, "Go" = ours. `MISMATCH` marks rows where ours and libdeflate
  differ in the **exact total compressed byte count**: `ldbench` and `gzbench` print `MB/s ratio bytes`, the first two
  fields unchanged. If a producer prints only two fields (the external ISA-L `gzbench_c`; it is not compared anyway) the
  comparison falls back to the 3-decimal ratio string and the header says so. A level with any MISMATCH row prints
  `FAIL (MISMATCH)` in the gate summary; mismatching rows are excluded from the geomean so they cannot lift it.
- **stdlib / klauspost level mapping**: the same numeric level for 1..9; `-` for 0 and 10..12 (no equivalent).
  Black boxes: public API only (`flate.NewWriter`/`gzip.NewWriterLevel` + `Reset`, flate `Resetter`/`gzip.Reader.Reset`).
- **ISA-L (optional)**: only if `ISAL_BENCH` points to a binary with CLI `-lib isal -level N -mode c|d -secs S FILE`
  printing `MB/s ratio` (e.g. `~/projects/fastzstd-gzbench/gzbench/gzbench_c`). Used for levels 0..3 and whole files
  only; otherwise the column is omitted without error.
- **Header** records: date, git rev + dirty flag, libdeflate tag and `ldref version` (cpu_features, decompress
  impl), C compiler version and flags, Go version, CPU model, load1 at start and end, AC state, pinned CPU and its
  SMT sibling (warns if busy), asm mode, and the -i/-g/-n settings.
- **Gate summary** after the tables, per level and separately for compression and decompression: geometric mean of
  "% of C", the minimum row, and PASS/FAIL (geomean >= 100%, every row >= 95%, hard floor 90%), listing violating rows.
- **Safety**: refuses to run (unless `-force`; AC0 must exist and be 1, so machines without `AC0` need `-force`) if AC
  power is not online, load1 > 4, a `gzbench`/`ldbench`/`gobench`/`zstd.test`/`zstd -b` process is running, or a
  `*.test`/`go`/benchmark process uses >= 10% on the pinned CPU or its SMT sibling; it prints what blocked.
  Keep the SMT sibling and the rest of the machine idle; do not benchmark while tests run.
- **What `ours` covers**: compression and decompression, `-format raw` and `-format gzip`, levels 0..12. `-mode c`
  times ours compressing the input itself; its exact compressed byte count is compared with libdeflate's (the port is
  byte-identical, so any difference is a `MISMATCH`). For `-mode d`, compare.sh first has `ldbench -dumpz` write the
  libdeflate streams of the row (same level, blocks and format) and both C and `ours` (`gzbench -zin`) decode those
  identical bytes; `gzbench` verifies the round trip against the input before timing. The size-identity check is
  trivially satisfied in `-mode d` (same bytes), so `MISMATCH` cannot occur there.
  The not-ported handling stays for future adapters: `ours` exit 3 (not ported) shows `n/a (not ported)` for that
  mode's rows, skips ours for the rest of that mode and the run continues; the header lists what is not ported and a
  mode with no ours rows prints no gate. Exit 4 (unsupported level) shows `n/a (not ported)` for that level only; the
  header lists each such level once.
  In the gate table every level of `-l` appears for a mode that has rows; a level without rows is `n/a` and
  `FAIL (missing rows)`.
- **Gate summary and OVERALL line**: a banner above the gates says which gates were evaluated. A requested mode without
  ours-vs-C rows (e.g. `-libs` without `ours`) prints `### Compress gate: NOT EVALUATED (ours not in -libs; no
  ours-vs-C rows)` with the requested levels as `n/a | NOT EVALUATED`, and the file ends with `OVERALL: PASS`, `OVERALL: FAIL`
  or `OVERALL: INCOMPLETE (compress not evaluated)` (`FAIL; INCOMPLETE ...` when both apply). A file with a NOT EVALUATED gate
  is never an all-pass. `-noasm` prints `OVERALL: REPORTED ONLY (noasm), not a gate;` plus the same INCOMPLETE note.
- **`-mode d` integrity**: there is no size identity check (both sides decode the same libdeflate bytes; the Go ratio cell
  reads `=C (same stream)`, not a measurement). Integrity = the pre-timing round trip in `gzbench` (exact-length decode of
  every block, byte equality with the input).
- **`-prof DIR`**: for `ours` rows, one extra run per (file, level, block, mode) AFTER that row's timed median runs writes
  `DIR/<file>.<block>.L<level>.<mode>.cpu.prof` (CPU profile of the timed windows only; never part of a reported number).
- **TMPDIR**: `-mode d` with `ours` dumps one row's libdeflate streams to `$TMP/z.bin` (mktemp under `$TMPDIR`, default /tmp):
  about the size of the largest input, ~212 MB for `silesia.tar` (16K blocks = ~13k blocks, plus 8 bytes per block). compare.sh
  checks the free space before starting and refuses with a clear message; set `TMPDIR` to a bigger filesystem if needed.
- **Result naming**: the default file is `bench/results/<date>-gzip-<label>.md`. For the Phase-1 decode baseline use
  `-o bench/results/$(date +%F)-deflate-decode-go.md` (compare.sh overwrites `-o`; keep hand-written profile notes in a
  separate file).
- **`-dry-run`**: runs no benchmark; prints the first commands (`DRY: ...`) and writes the header and table skeleton with
  placeholder numbers, the gate summary structure (banner, NOT EVALUATED blocks, `OVERALL: DRY RUN, no verdict`) but no verdicts, preflight refusals reported as notices. Used by the compare.sh test.
- `-noasm` builds `gzbench` with `-tags noasm`, labels the result and prints the summary as "REPORTED ONLY (noasm),
  not a gate" (no PASS/FAIL). It affects only `ours`: the stdlib and klauspost columns are unaffected.
- `-format gzip` applies to ld/ours/stdlib/klauspost; the ISA-L column is raw only and is omitted (header says why)
  with `-format gzip`. The header records the file list (`file:block`), the `-quick` marker and `-format`.

Never record smoke runs (`-quick -i 1 -n 1`) as results.

## gzbench

    cd bench/gzip && go build -o gzbench . && ./gzbench -lib ours|stdlib|klauspost -level N -mode c|d \
        [-block B] [-secs S] [-iters N] [-format raw|gzip] [-zin PATH] [-cpuprofile PATH] [-memprofile PATH] FILE

Prints `MB/s ratio bytes` like `ldbench` (bytes = exact total compressed size). `-iters` (default 1) is the minimum number of counted windows. Exit codes:
1 failure, 2 usage, 3 library not ported yet (`-lib ours`), 4 unsupported level (stdlib/klauspost outside 1..9).
`-cpuprofile PATH` / `-memprofile PATH` write a pprof CPU / allocs profile of the timed windows only (started after the
round-trip preparation, stopped before output). Manual profiling workflow (pin like the benchmark):

    ref/ldref/ldbench -level L [-block B] -dumpz Z FILE            # libdeflate streams (excludes -mode)
    taskset -c 2 gzbench -lib ours -mode d -level L [-block B] -zin Z -secs 5 -cpuprofile P FILE
    go tool pprof -top P        # or: go tool pprof -list 'decompressImpl' P ; -disasm for the asm shortlist

`ldbench -dumpz` writes PATH.tmp.PID and renames on success (no partial file on failure); a missing PATH or `-mode` with
`-dumpz` is a usage error (exit 2). `ours` demands the exact decoded length while libdeflate (as ldbench calls it) tolerates a
shorter output; negligible for timing.

`-zin PATH` (mode d only) takes the compressed blocks from PATH instead of compressing FILE: records `u64 LE length,
bytes` per block, as written by `ldbench ... -dumpz PATH` (which compresses, verifies, writes and exits).
`gzbench stat median` / `gzbench stat summary [-info] [-dry] [-levels "0 1 ..."] [-modes "c d"] [-why-c TEXT] [-why-d TEXT]` are the helpers compare.sh uses (numbers / tab-separated
`mode level label pct [ok|mismatch|missing]` on stdin).

The stdlib/klauspost baselines run with Go defaults (GC on, GOMAXPROCS=1 under `taskset`); the steady state
allocates nothing per call except what the libraries themselves allocate, and the compress side copies the
stream once into the caller buffer through a `Write`. `ours` is called directly and must not allocate per
`Compress`/`Decompress` call.

## Plugging in `ours`

`ours.go` holds `var ours Adapter = oursAdapter{}`: `NewCompressor(level, "raw")` = `flate.NewCompressor`,
`NewCompressor(level, "gzip")` = `gzip.NewCompressor` (levels 0..12; others `ErrUnsupportedLevel`, exit 4);
`NewDecompressor("raw")` = `internal/deflate.Decompressor`, `NewDecompressor("gzip")` = `gzip.NewDecompressor` (both
have the exact-length contract). Any other format returns `ErrNotPorted` (exit 3). The nested module imports
`github.com/vahid-sohrabloo/fastcompress/...` (including `internal/deflate`) through `replace ... => ../..` and a
`require ... v0.0.0` line (Go's `internal` rule is path-based, so the nested module's path `.../fastcompress/bench/gzip`
may import it).

`Adapter` (adapter.go) is whole-buffer, like libdeflate:

- `NewCompressor(level, format) (Compressor, error)`; `Compressor.Compress(dst, src []byte) (n int, err error)`
  writes the whole raw-DEFLATE (`format=="raw"`) or gzip (`"gzip"`) stream into the caller's `dst`
  (capacity `len(src) + len(src)/2 + 1024`) and errors if it does not fit. Levels 0..12; reuse internal state
  across calls (that is what `-block` measures).
- `NewDecompressor(format) (Decompressor, error)`; `Decompressor.Decompress(dst, src []byte) (n int, err error)`
  decodes into `dst` whose length is exactly the decoded size and errors on any other length.
- Return an error wrapping `ErrUnsupportedLevel` for a level the library lacks (exit 4) and `ErrNotPorted` for what
  does not exist yet (exit 3).

`-mode c` rows are gated against C with the compressed byte count (third output field) compared with libdeflate's.
`-mode d` does not need `-zin` for `ours` correctness, but compare.sh keeps feeding libdeflate's streams (raw and gzip)
so both sides decode the same input. For the `-noasm` row, build with `compare.sh -noasm`.

## Current results (i9-13900H, asm on)

- Decompression: geomean 100.2-100.6% of libdeflate at levels 1, 6, 9 and 12, lowest row 96.9%; gate passed by a
  0.2-0.6 point margin that depends on the fast loop's code alignment (tuned on this CPU):
  `bench/results/2026-11-09-deflate-decode-round2-recheck.md`.
- Compression (round 7): geomean 103.6% (L1), 101.6% (L3), 102.1% (L6), 102.8% (L9), about 110% (L12; round 7 measured 119.8%, the independent confirmation run 109.3-109.8%); the gate
  (geomean >= 100%, every row >= 95%, none below 90%) passes at every level. Lowest rows: L1 99.6% (x-ray, the only
  row below 100%), L3 100.1% (mozilla), L6 101.4% (samba), L9 101.2% (xml), L12 100.0-106.5% (nci). The slow L12 rows
  carry about +-2-3 points of rounding. Compressed sizes match libdeflate in all 65 rows:
  `bench/results/2026-11-10-deflate-compress-round7.md`, confirmed at HEAD in
  `bench/results/2026-11-10-deflate-confirm-head.md` (L1-L9 and decode reproduce within 0.3 points; L12 does not). Older rounds, kept as history (all failed the gate):
  `bench/results/2026-11-08-deflate-compress-round5.md` (geomean 94.7-99.1%, L12 lowest row 81.8%) and the
  other `bench/results/*deflate-compress*` files.
- Results come from one CPU; expect other CPUs to differ.
- `-noasm` (pure Go) is reported only, not gated; arm64 has not been benchmarked.
