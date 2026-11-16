# fastcompress

Pure-Go compression, ported from C. `zstd` is a port of libzstd v1.5.7 that
exposes the public API of `klauspost/compress/zstd`; `flate`, `gzip` and
`zlib` are a port of libdeflate v1.26; `lz4` is a port of lz4 v1.11.1. No
cgo and no dependencies.

Status: work in progress.

- **Phase 1, the encoder, is done:** `NewWriter`, `Encoder` (`EncodeAll`,
  the streaming `io.WriteCloser`), `EncodeTo`, `ZipCompressor` and the
  `EOption`s.
- **Phase 2, the decoder, is done:** `NewReader`, `Decoder` (`DecodeAll`,
  the streaming reader), the dictionary and trainer functions, and the zip and
  snappy helpers. Every identifier of `klauspost/compress/zstd` is exported.
  Compression is at 100.3% and decompression at 102.6% of C libzstd v1.5.7's
  speed overall (see [zstd speed](#zstd-speed)).

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
any `dst` capacity). C errors map to the klauspost sentinels.

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
  and fastCover), byte-identical to C v1.5.7, parameters included.
- `WithEncoderZstdLevel(n)` is an extension: it runs C compression level
  `n` (1 to 22) directly instead of one of the four `EncoderLevel`s, with
  C's output at that level, including C's long-distance matching, which C
  turns on by itself for levels 16 to 22 with a window of 128 MiB or more
  (level 22 above 64 MiB or for an unknown size, or levels 16 to 21 with a
  `WithWindowSize` of 128 MiB or more).
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
target comparing it with the Go code. On arm64 assembly runs the decoder's
fast loop, the level 1 loop and the hash-chain search of levels 2..9
(`hcLongestMatchAsm`, called from the Go greedy and lazy loops); the rest is
Go there. Every other architecture runs pure Go.

### Speed

Single-threaded speed of raw DEFLATE against libdeflate v1.26 (built as it
ships, with its BMI2 decoder and SIMD paths on), Go's `compress/flate` and
[klauspost/compress/flate](https://github.com/klauspost/compress) v1.20.3,
over the Silesia corpus: the 12 files whole, plus `silesia.tar` in
independent 16 KiB, 128 KiB and 1 MiB blocks (15 rows per level). Each cell
is the median of three interleaved runs of `bench/gzip/compare.sh`; amd64
assembly on. Treat differences of a few percent as noise. The Go standard
library and klauspost have no levels 10 to 12.

![DEFLATE compression: speed vs ratio](bench/img/deflate-compress.svg)

**Compression.** Our output is byte-identical to libdeflate's at every
level (all 75 rows), so speed is the only difference between the two. The
Go standard library and klauspost produce other output (2-9% larger than
libdeflate's at the same level number), so the chart plots speed against
compression ratio. A point further up and to the right is better.

| level | % of libdeflate's speed (geomean) | lowest row |
|---|---|---|
| 1 | 101.6% | 99.5% (ooffice) |
| 3 | 99.1% | 96.5% (nci) |
| 6 | 98.7% | 74.1% (nci; the other 14 rows are 96-101%) |
| 9 | 98.0% | 86.4% (`silesia.tar`, 128 KiB blocks) |
| 12 | 118.2% | 104.3% (sao) |

![DEFLATE decompression: % of libdeflate](bench/img/deflate-decompress.svg)

**Decompression.** Every library decodes the same libdeflate streams, so
the bars compare like with like. Geometric mean of the percentage of
libdeflate's speed:

| level | ours | Go `compress/flate` | klauspost |
|---|---|---|---|
| 1 | 98.8% | 22.4% | 30.3% |
| 3 | 97.6% | 21.7% | 28.6% |
| 6 | 97.9% | 21.9% | 29.6% |
| 9 | 97.4% | 23.0% | 30.5% |
| 12 | 97.4% | - | - |

Our lowest decompression row is samba at level 3 (91.6%). We decompress
about 4.4 times as fast as the Go standard library and 3.3 times as fast as
klauspost.

The decoder's margin against libdeflate depends on the CPU and on code
layout: the fast loop's alignment is tuned, and on another CPU the same
code measured 100.2-100.6% of libdeflate. The pure-Go build (`-tags noasm`)
is slower and is not measured here; arm64 has not been benchmarked. The
charts are drawn from the `bench/gzip/compare.sh` output by
`bench/plot.py`.

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

## zstd speed

Single-threaded speed against C `zstd` v1.5.7 (`zstd -b -T1`) and
[klauspost/compress/zstd](https://github.com/klauspost/compress) v1.20.3,
over the Silesia corpus: the 12 files whole, plus `silesia.tar` in
independent 16 KiB, 128 KiB, 1 MiB and 4 MiB blocks (16 rows per level).
Checksums are off, and each cell is the median of three interleaved runs
of `bench/compare.sh -k`. Treat differences of a few percent as noise.

![zstd compression: speed vs ratio](bench/img/zstd-compress.svg)

**Compression.** Our output is byte-identical to C's, so speed is the only
difference between the two. klauspost produces different output: its named
levels are its own and do not match C's, so the chart plots speed against
compression ratio instead of speed alone. A point further up and to the
right is better.

| level | % of C's speed (geomean) | lowest row |
|---|---|---|
| 1 | 100.3% | 97.2% (x-ray) |
| 3 | 97.8% | 81.2% (xml)* |
| 7 | 102.3% | 98.1% (`silesia.tar`, 16 KiB blocks) |
| 11 | 105.6% | 95.2% (reymont) |
| all | 101.5% | |

\* The xml row at level 3 swings between runs on a shared machine (81% in
this run, 87% and 98.5% in earlier ones); on a quiet core it measures
about 101% of C. Without it, level 3's lowest row is 94.2% (`silesia.tar`,
4 MiB blocks).

![zstd decompression: % of C](bench/img/zstd-decompress.svg)

**Decompression.** Every library decodes the same frames (ours, which are
C's), so the bars compare like with like. Geometric mean of the
percentage of C's speed:

| level | ours | klauspost |
|---|---|---|
| 1 | 102.1% | 82.7% |
| 3 | 103.8% | 87.6% |
| 7 | 103.8% | 87.4% |
| 11 | 103.0% | 86.7% |
| all | 103.2% | 86.1% |

Our lowest decompression row is `silesia.tar` in 16 KiB blocks at level 1
(95.8%). klauspost falls to about 66% of C on the 128 KiB and 16 KiB rows.

**Higher levels (13 to 22).** Ported from C as well (btultra, btultra2, the
post-block splitter and long-distance matching), reached with
`WithEncoderZstdLevel`; the output is C's. Speed as a percentage of C's, on
four inputs (xml, dickens, ooffice and `silesia.tar` in 1 MiB blocks):

| level | compression | decompression |
|---|---|---|
| 13 | 95-100% | 100-105% |
| 16 | 103-112% | 101-104% |
| 19 | 87-114% | 101-105% |
| 22 | 97-132% | 102-105% |

Expect other CPUs to give different numbers. The charts are drawn from the
`bench/compare.sh` output by `bench/plot.py`.

## LZ4

Package `lz4` is a port of [lz4](https://github.com/lz4/lz4) v1.11.1
(`lz4.c`, `lz4hc.c`, `lz4frame.c`): the block format, the fast compressor,
the high compression levels 2 to 12 and the frame format. Every compressor
produces C's bytes for the same calls in the same order; the decoders accept
and reject what C accepts and rejects, with C's error class.

**Blocks** (C's API, with `dst` as a buffer whose length is the capacity):

- `CompressFast(dst, src, acceleration)` / `CompressDefault` /
  `CompressDestSize` (`LZ4_compress_fast`, `_default`, `_destSize`), and
  `CompressHC(dst, src, level)` / `CompressHCDestSize` (`LZ4_compress_HC`,
  levels 2 to 12: LZ4MID at 2, hash chains at 3 to 9, the optimal parser at
  10 to 12). `CompressBound(n)` bytes are always enough; otherwise 0 means
  the block did not fit.
- `Stream` (`LZ4_stream_t`) and `StreamHC` (`LZ4_streamHC_t`): independent
  blocks that reuse the tables (`CompressFast`, `Compress`), streaming with
  `CompressFastContinue` / `CompressContinue` (prefix and external
  dictionary modes), `LoadDict`, `LoadDictSlow`, `AttachDictionary`,
  `SaveDict`, `FavorDecompressionSpeed`.
- `DecompressSafe`, `DecompressSafePartial` and the `UsingDict` variants
  return `ErrCorrupt`, `ErrDstTooSmall` or `ErrOffset` (C's -1, -2, -3).

C chooses between its prefix and external-dictionary modes by comparing
pointers. Here an input continues the history only when both are parts of
one slice (the history's capacity reaches the input); two separate
allocations that happen to be adjacent are compressed as an external
dictionary, where C would use the prefix. Both are valid; only the bytes
differ from C's in that case.

**Frames** (`lz4frame.c`):

- `CompressFrame` / `AppendFrame` / `CompressFrameUsingCDict`
  (`LZ4F_compressFrame`), `FrameCompressor` (`LZ4F_compressBegin`, `Update`,
  `Flush`, `End`) and `Writer` (an `io.WriteCloser`: each `Write` is one
  `LZ4F_compressUpdate`, so the frame is C's for the same sequence of writes
  and flushes). `Preferences` has C's fields: level, block size ID, linked or
  independent blocks, block and content checksums, content size, dictionary
  ID, `AutoFlush`, `FavorDecSpeed`, `NbWorkers`; `NewCDict`, `WithDict`,
  `WithCDict` for dictionaries, `WriteUncompressed` for uncompressed blocks,
  `UpdateStable` for input that stays until the next flush.
- Multi-threaded frames (C's `nbWorkers`, `lz4 -T#`): with
  `Preferences.NbWorkers` (or `WithConcurrency(n)` on a `Writer`) the
  content is cut into jobs at fixed positions (1 MB, or one block when
  larger, restarting at each flush), compressed by up to `NbWorkers`
  goroutines and emitted in order. The frame depends only on the content
  and the flush points, never on the number of workers or the scheduling:
  with independent blocks it is the single-threaded frame, with linked
  blocks it is C's multi-threaded frame (each job restarts with the
  previous 64 KB as dictionary). Output lags behind input: `Flush` and
  `Close`/`End` emit the jobs in flight; with `FrameCompressor`,
  `CompressBoundFrame` counts them (C emits them over several calls
  instead).
- `AppendDecompressFrame(dst, src, dict)` and `Reader` (`io.Reader`,
  `io.WriterTo`) decode concatenated frames and skip skippable frames, with
  C's header checks, checksums and error classes (`ErrFrameType`,
  `ErrHeaderChecksum`, `ErrBlockChecksum`, `ErrContentChecksum`,
  `ErrFrameSize`, `ErrDecompressionFailed`, `io.ErrUnexpectedEOF` for a
  truncated frame, ...).

Usage:

    dst := make([]byte, lz4.CompressBound(len(src)))
    n := lz4.CompressFast(dst, src, 1)
    out := make([]byte, len(src))
    m, err := lz4.DecompressSafe(out, dst[:n])

    w := lz4.NewWriter(f, lz4.WithLevel(9))
    io.Copy(w, r)
    w.Close()

    content, err := lz4.AppendDecompressFrame(nil, frame, nil)

Not ported: `LZ4_decompress_fast` (unsafe by design), the streaming block decoder
(`LZ4_decompress_safe_continue`; the frame decoders cover streams), the
legacy frame format of the `lz4` command, and the `lz4file.h` API.

### Safety

The decoder files (`lz4_decompress*.go`, `lz4frame_decompress*.go`) import
no `unsafe` and call no `recover` (`scripts/decodercheck.sh`); malformed
input returns an error, never a panic or an out-of-bounds access. The
decoder's assembly has a `// BOUNDS:` proof and a fuzz target comparing it
with the Go loop. `FuzzDecompress` and `FuzzDecompressFrame` compare every
result with C's decoders.

### Assembly

On amd64 (unless built with `-tags noasm` or `purego`) assembly runs the
decoder's fast loop (C's `LZ4_FAST_DEC_LOOP` part, without external
dictionary; linked frames decode with the history in the output, in
prefix mode), the fast compressor's main loop (one-shot with byU16 and
byU32 tables, the streaming prefix and external-dictionary modes with C's
resume-in-prefix split, the attached dictionary (`dictCtx`) of small
blocks, and `destSize`; limited or unlimited output), the LZ4MID main loop
of level 2, and the whole parsers of levels 3 to 12:
`LZ4HC_compress_hashChain` (levels 3 to 9) and
`LZ4HC_compress_optimal` (10 to 12), with the match search (table
insertion, candidate loop, pattern analysis, `chainSwap`) and the price
updates in the same routine. The LZ4MID loop and the parsers cover every
mode of C's: a prefix alone, an external dictionary (linked blocks, and
CDict frames, whose dictionary state C copies into the stream), an
attached dictionary (`dictCtx`: blocks below `dictCopyMin`, and LZ4MID
with any CDict), `favorDecSpeed`, and `destSize` (the assembly stops at
the sequence that does not fit, which Go encodes as C does). Each mode is
a separate routine assembled from one source (`#ifdef` variants), so the
prefix-only loops keep their instruction stream. Each falls back to Go
where C's code takes a path the assembly does not cover, and each has a
fuzz target comparing it with the Go code.
Baseline instructions only (SSE2; no BMI2 or AVX2 needed): 16-byte match
compares measured slower than 8-byte ones on the short matches that
dominate, except in LZ4MID's count past the first 8 bytes.
On arm64 assembly runs the decoder's fast loop, the fast compressor's
main loop (one-shot, the streaming prefix mode with its resume and
`destSize`; external and attached dictionaries run the Go loop), the LZ4MID
loop without dictionary, the candidate loop of the hash chain search, and
the whole hash chain and optimal parsers for a prefix without dictionary and
without `favorDecSpeed` (the other modes run the Go parsers with the
assembly candidate loop). The other architectures run the Go code.

### Speed

Single-threaded frame speed against C `lz4` v1.11.1 (`lz4 -b<L> -T1 -B4M`:
one frame per file, 4 MiB independent blocks, no checksums) on three Silesia
files, and on `silesia.tar` in 64 KiB blocks (`-B64K`), measured with
`bench/compare-lz4.sh` (median of three interleaved runs, each the best
mean of ~1 s windows over 3 s, pinned to one core). The output is
byte-identical (equal ratios on every row). Treat a few percent as noise
(the closest rows, level 1 on xml and the decoder on dickens, are within
it); other CPUs give other numbers.

| level | xml | dickens | ooffice | geomean | silesia.tar, 64 KiB |
|---|---|---|---|---|---|
| 1 (fast) compression | 101.0% | 102.4% | 106.0% | 103.1% | 106.8% |
| 2 (LZ4MID) compression | 101.8% | 102.2% | 103.8% | 102.6% | 103.1% |
| 3 (hash chain) compression | 107.7% | 111.3% | 109.9% | 109.6% | 111.5% |
| 4 compression | 109.3% | 115.5% | 112.2% | 112.3% | 112.7% |
| 5 compression | 108.9% | 115.9% | 112.2% | 112.3% | 112.5% |
| 6 compression | 107.9% | 112.7% | 111.5% | 110.7% | 111.2% |
| 7 compression | 105.9% | 110.4% | 110.5% | 108.9% | 110.0% |
| 8 compression | 104.8% | 108.6% | 109.9% | 107.7% | 108.8% |
| 9 (hash chain) compression | 104.2% | 107.5% | 107.7% | 106.5% | 107.7% |
| 10 (optimal) compression | 104.7% | 104.0% | 108.1% | 105.6% | 108.5% |
| 11 compression | 104.6% | 103.6% | 108.6% | 105.6% | 108.6% |
| 12 (optimal) compression | 106.2% | 104.2% | 109.7% | 106.7% | 110.2% |
| 1 decompression | 104.4% | 101.9% | 103.4% | 103.2% | 102.4% |
| 9 decompression | 102.3% | 101.4% | 103.0% | 102.2% | 101.7% |
| 12 decompression | 102.5% | 101.1% | 102.0% | 101.9% | 102.1% |

The HC levels gain most from running the whole parser in one routine: C
inlines `LZ4HC_InsertAndGetWiderMatch` into each parser, and so does the
assembly. C's `-T1` runs its job mode with one worker thread, which costs C
about 2% against `--single-thread`. The pure-Go build (`-tags noasm`) is
slower (measured before the assembly: 40 to 55% of C at level 1, 15 to 25%
for decoding); arm64 is not measured.

The other frame modes, against C with `--single-thread` (the same method;
xml, dickens and ooffice; equal ratios on every row): linked blocks
(`-BD`), a dictionary (`-D`: 64 KiB from the middle of the file, digested
once into a CDict; with 4 MiB blocks C copies the dictionary state into the
stream at every level but 2, which searches the attached dictionary for
the whole block), and `--favor-decSpeed`. The lowest rows, level 1 with a
dictionary on xml in 4 MiB blocks (98.5%, four runs between 97.8 and
98.7%) and `favorDecSpeed` at level 10 on xml (98.9 to 99.3%), are below C
by about 1%; cells within 2% of 100% are noise-level.

| mode, level | 4 MiB: xml | dickens | ooffice | 64 KiB: xml | dickens | ooffice |
|---|---|---|---|---|---|---|
| linked, 1 | 101.5% | 101.7% | 102.0% | 101.2% | 102.2% | 100.6% |
| linked, 2 | 100.4% | 100.6% | 103.3% | 100.1% | 100.3% | 102.8% |
| linked, 4 | 107.2% | 112.1% | 108.9% | 106.8% | 107.5% | 109.2% |
| linked, 9 | 103.8% | 106.7% | 107.4% | 103.9% | 106.4% | 106.3% |
| linked, 12 | 104.4% | 103.0% | 107.7% | 104.1% | 103.0% | 108.2% |
| dictionary, 1 | 98.5% | 100.2% | 101.5% | 104.5% | 100.3% | 103.1% |
| dictionary, 2 | 101.0% | 100.9% | 106.0% | 103.2% | 102.4% | 105.3% |
| dictionary, 4 | 104.8% | 112.1% | 109.2% | 103.9% | 110.6% | 108.5% |
| dictionary, 9 | 103.5% | 106.6% | 105.4% | 103.7% | 105.8% | 104.4% |
| dictionary, 12 | 103.8% | 103.1% | 107.3% | 103.8% | 100.7% | 105.7% |
| favorDecSpeed, 10 | 99.3% | 100.6% | 103.8% | 98.9% | 100.4% | 103.9% |
| favorDecSpeed, 11 | 100.3% | 100.0% | 104.3% | 99.5% | 100.0% | 104.3% |
| favorDecSpeed, 12 | 102.3% | 100.0% | 104.4% | 102.1% | 100.0% | 105.2% |
| linked, 1 decompression | 103.6% | 101.1% | 103.2% | 101.4% | 100.5% | 98.8% |
| linked, 9 decompression | 103.4% | 101.8% | 104.0% | 100.8% | 101.0% | 101.0% |

Before this assembly the same rows measured 58 to 64% (level 2 with a
dictionary), 57 to 87% (HC levels with a dictionary), 62 to 81%
(`favorDecSpeed`) and 70 to 98% (HC levels, linked 64 KiB blocks).

Multi-threaded frames (`NbWorkers`, against C's `lz4 -T<n>`, which reads
its stable input in place as `UpdateStable` does) on `silesia.tar`, 64 KiB
blocks, both pinned to the same 8 cores (one thread each, no SMT
siblings), median of three interleaved runs (MB/s, Go then C, and Go's %
of C); the ratios are equal, linked frames included (C's job output):

| threads | independent, level 1 | independent, level 9 | linked, level 1 | linked, level 9 |
|---|---|---|---|---|
| 1 | 398 / 383 (103.8%) | 21.5 / 19.4 (110.8%) | 379 / 366 (103.7%) | 21.0 / 19.2 (109.4%) |
| 2 | 807 / 680 (118.7%) | 41.7 / 38.4 (108.6%) | 749 / 723 (103.6%) | 42.2 / 37.7 (111.9%) |
| 4 | 1534 / 1483 (103.4%) | 79.0 / 74.8 (105.6%) | 1492 / 1413 (105.6%) | 80.9 / 75.1 (107.7%) |
| 8 | 2543 / 2576 (98.7%) | 147 / 145 (101.2%) | 2485 / 2409 (103.1%) | 147 / 145 (101.0%) |

Level 9 scales 6.8 times from 1 to 8 threads (C 7.5 times, from a slower
start); level 1 at 8 threads (2.5 GB/s) is within noise of C. The 2-thread
independent row's C value was low in each of its runs.

### Tests

The tests compare against lz4 v1.11.1 run as a C subprocess
(`ref/fetch.sh`, then `make -C ref/lz4ref`): every encoder on the creftest
inputs and the Silesia corpus (the fast compressor at several accelerations
and with reused tables, every HC level 0 to 13 with and without
`favorDecSpeed`, streaming in prefix, external-dictionary and saved-history
layouts with loaded, slow-loaded and attached dictionaries, limited
capacities and `destSize`), frames for 64 preference sets and plans of
writes, flushes and uncompressed writes with raw and digested dictionaries,
multi-threaded frames (1, 3 and 8 workers, one-shot, `Writer` and
`UpdateStable`, against C's frames with `nbWorkers`), and the decoders on
valid, truncated and mutated blocks and frames. Fuzz targets compare the
assembly with the Go code in every compression mode (`FuzzHCDictAsmEquiv`,
`FuzzFastDictAsmEquiv` and the others), the multi-threaded frames across
worker counts (`FuzzFrameMT`), and the decoders and encoders with C's;
the concurrent paths run under the race detector. `go test -short ./lz4`
runs a subset; the full run takes about 40 minutes.

## Credits

- `zstd` is a port of [zstd](https://github.com/facebook/zstd) v1.5.7, by
  Yann Collet and the zstd contributors (Copyright Meta Platforms, Inc. and
  affiliates; BSD 3-Clause, see [LICENSE](LICENSE)).
- `flate`, `gzip` and `zlib` are a port of
  [libdeflate](https://github.com/ebiggers/libdeflate) v1.26, by Eric
  Biggers and the libdeflate contributors (MIT, see
  [internal/deflate/NOTICE](internal/deflate/NOTICE)).
- `lz4` is a port of [lz4](https://github.com/lz4/lz4) v1.11.1 (`lz4.c`,
  `lz4hc.c`, `lz4frame.c`), by Yann Collet and the lz4 contributors (BSD
  2-Clause, see [lz4/NOTICE](lz4/NOTICE)); no code from pierrec/lz4 is used.
- The `zstd` API follows
  [klauspost/compress/zstd](https://github.com/klauspost/compress) so that it
  can replace it; no code from that project is used.
