# fastzstd

A pure-Go port of the C libzstd v1.5.7 compressor, exposing the public API of
`klauspost/compress/zstd`. No cgo and no dependencies.

Status: work in progress.

- **Phase 1, the encoder, is done:** `NewWriter`, `Encoder` (`EncodeAll`,
  the streaming `io.WriteCloser`), `EncodeTo`, `ZipCompressor` and the
  `EOption`s.
- **Phase 2, the decoder, is in progress.** Every identifier of
  `klauspost/compress/zstd` is now exported (`scripts/apicheck.sh` passes in
  full mode); the remaining Phase 2 tasks are still open.

## Encoder output

Each `EncoderLevel` runs C's compression level: `SpeedFastest` 1,
`SpeedDefault` 3, `SpeedBetterCompression` 7, `SpeedBestCompression` 11.
The output is byte-identical to C zstd v1.5.7 with the same parameters,
except that `WithEncoderDictRaw` writes its dictionary ID into the frame
header (C writes none for raw content); everything after that field is C's.

## Decoder

`NewReader` and `Decoder` (`DecodeAll`, the streaming `io.Reader`,
`WriteTo`), dictionaries, `ZipDecompressor` and `SnappyConverter` are
ported from C's `zstd_decompress*.c` and `huf_decompress.c`. Output and
error class are checked against C v1.5.7 by differential tests over a
corpus of C-made frames, and fuzz targets compare the paths against each
other (`DecodeAll`, streaming at any input cut, concurrent streaming, and
any `dst` capacity). C errors map to the klauspost sentinels; the table
and the deliberate deviations from C are in
[docs/superpowers/phase2-api-decisions.md](docs/superpowers/phase2-api-decisions.md).

### Safety

The decoder is written for untrusted input: corrupt, truncated or hostile
data returns an error, never a panic or an out-of-bounds access, and memory
is bounded by `WithDecoderMaxMemory`. CI enforces this:

- `scripts/decodercheck.sh`: no decoder file imports `unsafe` or calls
  `recover` (one exception: the streaming pipeline re-raises a panic of the
  caller's `io.Reader` on the caller's goroutine). Decoder assembly needs a
  `// BOUNDS:` proof per routine and a fuzz target comparing it with the Go
  code.
- `scripts/fuzz.sh`: every fuzz target runs for 20 s per push (amd64),
  and for 10 minutes nightly on amd64 and arm64, with the assembly on, off
  and (amd64) without BMI2. Every target's seed corpus also runs as a test,
  with and without the `noasm` tag.
- A coverage gate: `zstd_decompress*.go` and `huf_decompress.go` must each
  stay at 95% statement coverage or more.

## Deliberate API differences

- `WithHugePages` is an extension (not a klauspost option): it backs the
  large match tables with transparent huge pages on Linux. Speed only; off by
  default.
- `WithSingleSegment` is accepted and ignored: the frame header follows C's
  automatic choice, so output stays identical to C.
- `WithConcurrentBlocks` and `WithLowerEncoderMem` are accepted and have no
  effect. Encoding is single-threaded per call (C's `nbWorkers = 0`).
  `WithEncoderConcurrency(n)` caps the number of concurrent `EncodeAll` calls;
  streams are never async.
- `BuildDict` returns C's `ZDICT_finalizeDictionary` output. Non-zero
  `Offsets` replace C's starting repcodes {1, 4, 8} (the history is
  zero-padded in front up to the largest); `CompatV155` changes nothing, as
  C's dictionaries already load in zstd v1.5.5.
- `TrainDict`, `TrainDictCover`, `TrainDictFastCover` and `CoverParams`
  are extensions: C's dictionary trainers (`ZDICT_trainFromBuffer`, COVER
  and fastCover), byte-identical to C v1.5.7, parameters included. Levels
  that would make C compress with btultra, btultra2, the btopt post-block
  splitter or long-distance matching (not ported; C levels 13 and up)
  return an error.
- `EncoderLevelFromZstd` maps a zstd level to the highest `EncoderLevel`
  whose C level (1, 3, 7, 11) does not exceed it.

## DEFLATE, gzip and zlib

Packages `flate`, `gzip` and `zlib` are a Go port of
[libdeflate](https://github.com/ebiggers/libdeflate) v1.26 (no cgo; amd64
assembly for the hot loops, see below). Each package has two APIs:

- **Streaming, a drop-in for the standard library's `compress/flate`,
  `compress/gzip` and `compress/zlib`**: `NewWriter`, `NewWriterLevel`
  (gzip, zlib), `NewWriterDict` / `NewWriterLevelDict`, `Flush`, `Reset`,
  `HuffmanOnly`, gzip `Header` fields; `NewReader`, `NewReaderDict`,
  `Resetter`, `Multistream`, and the standard library's error types.
  `scripts/apicheck.sh` compares the exported API with the standard
  library's; the extra identifiers are listed in `api/stdlib-*-delta.txt`.
  C has no streaming API, so both sides are new design around the port:
  the `Writer` compresses 1 MiB chunks with 32 KiB of history, and its
  output is byte-identical to libdeflate only when at most 1 MiB is
  written and `Flush` is never called; the `Reader` runs libdeflate's
  decoder and suspends at symbol boundaries. Details in the `flate`
  package documentation.
- **Whole-buffer, libdeflate's own API**, described below.

The whole-buffer API:

- `NewCompressor(level)` with levels -1..12: `NoCompression` (0),
  `BestSpeed` (1) .. `BestCompression` (9), `DefaultCompression` (-1 =
  level 6), and the extensions `Level10`..`Level12` (libdeflate's
  near-optimal parsing). `Compress(dst, src)` into a caller buffer
  (`ErrShortBuffer` if too small; `CompressBound` is always enough) and
  `AppendCompress`. The output is byte-identical to libdeflate's at the
  same level (gzip: one member, no name, MTIME 0, OS 255; zlib: no
  dictionary).
- `NewDecompressor()`: `Decompress(dst, src)` needs the exact
  decompressed size (libdeflate semantics: `ErrShortOutput` /
  `ErrInsufficientSpace` otherwise); `DecompressEx` accepts a larger
  buffer and reports the input and output sizes; `AppendDecompress`
  handles unknown sizes, bounded by `SetMaxOutput` (1 GiB by default)
  against decompression bombs. gzip decodes the first member only; zlib
  rejects preset dictionaries. Inputs are accepted and rejected exactly as
  libdeflate does (differential tests against the C library).
- No allocation per call with reused instances, except when an
  `AppendCompress` / `AppendDecompress` dst lacks the capacity (pool them:
  a level 6 compressor holds about 660 KiB, levels 10..12 about 8.6 MiB,
  a decompressor 13 KiB); instances are not safe for concurrent use. A
  zero `Compressor` is not usable (use `NewCompressor`); a zero
  `Decompressor` is.
- dst must not overlap src (for the `Append` forms: the whole capacity of
  dst); in-place use silently gives bad output or `ErrBadData`.
  `AppendDecompress` allocates its `sizeHint` up front, so do not pass an
  untrusted hint (e.g. a gzip ISIZE) without lowering `SetMaxOutput`.
- The error values are shared by the three packages
  (`gzip.ErrBadData == flate.ErrBadData`).

`HuffmanOnly` (-2) is accepted by the streaming `Writer` only; it has no
libdeflate counterpart, so its output is not compared with libdeflate's.

### Assembly

On amd64 (unless built with `-tags noasm` or `purego`) assembly replaces
the Go code of the decoder's fast loop (only on CPUs with BMI2, as
libdeflate's own BMI2 decoder; other CPUs run the Go loop), the level 1
loop, the greedy / lazy / lazy2 loops of levels 2..9 with the hash-chain
match finder, the sequence writer of the block flush, the match finder's
table init / rebase (SSE2), and, for the near-optimal levels 10..12, the
binary-tree match finding loop (`btMatchLoopAsm`) and the backward
min-cost-path pass (`minCostPathAsm`). The rest of levels 10..12 and
Adler-32 are Go only; CRC-32 is `hash/crc32`. Each routine has a fuzz
target comparing it with the Go code. arm64 and every other architecture
run pure Go: there is no arm64 assembly, and arm64 is only tested on CI.

### Speed

Measured with `bench/gzip/compare.sh` on an i9-13900H (one core pinned,
raw DEFLATE, the 12 Silesia files whole plus `silesia.tar` in independent
blocks; amd64 assembly on) against libdeflate v1.26 built as it ships (its
BMI2 decoder and SIMD paths on). Geometric mean of our speed as a
percentage of libdeflate's:

- **Decompression** (`silesia.tar` in 1 MiB, 128 KiB and 16 KiB blocks):
  100.2% to 100.6% at levels 1, 6, 9 and 12, lowest row 96.9%, in two
  runs alternated with a pre-change build
  ([bench/results/2026-11-09-deflate-decode-round2-recheck.md](bench/results/2026-11-09-deflate-decode-round2-recheck.md)).
  The margin over libdeflate is 0.2-0.6 points and depends on code
  layout: most of the last 2% came from aligning the fast loop, tuned on
  this one CPU (an earlier, 32-byte aligned version lost about half of
  that gain when moved by 32 bytes; the current 64-byte alignment did
  not). Expect parity, not a lead, on other CPUs.
- **Compression** (the 12 Silesia files whole plus `silesia.tar` in 1 MiB
  blocks, 13 rows per level), at or above libdeflate at every measured
  level
  ([bench/results/2026-11-10-deflate-compress-round7.md](bench/results/2026-11-10-deflate-compress-round7.md)):

  | level | geomean | lowest row |
  |---|---|---|
  | 1 | 103.6% | 99.6% (x-ray) |
  | 3 | 101.6% | 100.1% (mozilla) |
  | 6 | 102.1% | 101.4% (samba) |
  | 9 | 102.8% | 101.2% (xml) |
  | 12 | about 110% | 100.0-106.5% (nci) |

  The project's gate (geomean >= 100%, every row >= 95%, no row below
  90%) is met for compression at all five measured levels: every
  geomean is above 100% and no row is below 99.4%. The rows below
  100% are x-ray at level 1 (99.5-99.6%) and, in the larger confirmation set, tar/16K at level 3 (99.4%). The slow level 12 rows (nci 3.3,
  xml 4.1 MB/s) carry about +-2-3 points of rounding in "% of C". The
  compressed output is byte-identical to libdeflate's in all 65 rows.

The pure-Go build (`-tags noasm`) is slower and is not gated; arm64 has
not been benchmarked. All results come from one CPU (the i9-13900H
above); expect other CPUs to differ.

### Tests

The tests compare against libdeflate v1.26 run as a C subprocess
(`ref/fetch.sh`, then `make -C ref/ldref`). The full suite is heavy (whole
Silesia files and `silesia.tar` blocks at every level, against C), so run
it through `scripts/deflate-test.sh`, which serialises runs with `flock`
and caps each one with `systemd-run` (6G `MemoryMax`, no swap,
`GOMEMLIMIT` 4GiB by default):

    scripts/deflate-groups.sh -v -run FILE...      # quick tier: the tests of the changed files
    scripts/deflate-test.sh -t quick               # whole internal/deflate package, -short
    scripts/deflate-test.sh -t full -a on,off,nobmi2 lazy nopt rest equiv dec other
    scripts/deflate-test.sh -t full noasm flate gzip zlib creftest

On one 20-thread laptop a full-tier pass of internal/deflate takes about
21 to 25 minutes per assembly mode. CI splits internal/deflate into those six
partitions (`scripts/deflate-groups.sh -regex`), each a matrix job on amd64
and arm64 (default build, then `-tags noasm`), plus jobs for the other
packages and for 386 / race `-short`. Details:
[scripts/README-deflate-tests.md](scripts/README-deflate-tests.md).

## Speed

On an i9-13900H (one core, checksums off), compression speed against C
`zstd -b` v1.5.7 over the Silesia corpus, as a geometric mean of the
percentage of C's speed: 101.1% at level 1, 101.3% at level 3, 102.1% at
level 7 and 101.2% at level 11 (101.4% overall; lowest row 97.9%). The
compressed sizes are identical in every row. Full tables:
[bench/results/2026-11-02-phase1-final.md](bench/results/2026-11-02-phase1-final.md).

## Credits

This is a port of [zstd](https://github.com/facebook/zstd). Credit goes to
Yann Collet and the zstd contributors. See LICENSE.
