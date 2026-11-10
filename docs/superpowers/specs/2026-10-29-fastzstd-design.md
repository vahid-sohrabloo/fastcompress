# fastzstd — Design

Date: 2026-10-29
Status: Draft v2 (awaiting approval). Revised because the implementation is now
ported from C libzstd, not forked from klauspost.

## Intent

A pure-Go Zstandard library that matches the speed of C libzstd, with no cgo.
Its implementation is **a port of C libzstd**, and its public API is **a
drop-in replacement for `github.com/klauspost/compress/zstd`**.

**Why:** klauspost/compress/zstd is the standard pure-Go zstd, and it is
noticeably slower than C for both compression and decompression. Calling C
libzstd through cgo works against cross-compilation, static binaries, and Go's
toolchain simplicity. The motivating consumer is **restic**, which uses
klauspost zstd and should get C-class speed just by changing an import path.

**Why port C rather than fork klauspost (the user's decision):** klauspost's
internals are a different design from C's. Using them as a starting point or
reference would steer the work toward klauspost's choices and away from the
code that actually reaches C speed. C libzstd is the only implementation
reference.

## Goals

1. **Implementation follows C libzstd.** The algorithms, data structures,
   parameter tables, and hot loops are ported from the C source
   (`lib/compress`, `lib/decompress`, `lib/common`, and later `lib/dictBuilder`
   where needed).
2. **Drop-in API compatibility with klauspost/compress/zstd, covering the
   entire public API.** Code that compiles against
   `github.com/klauspost/compress/zstd` compiles after an import-path swap.
   That covers types, functions, options, exported errors, dictionaries and
   `BuildDict`, zip helpers, the Snappy converter, header parsing, and every
   other exported identifier.
3. **Speed parity with C.** Within **about 10% of C libzstd throughput** at
   matching levels, on **amd64 and arm64**.
4. **No cgo.** Builds with `CGO_ENABLED=0` and cross-compiles everywhere Go
   does.
5. **Byte-identical output to C libzstd** for the same input and equivalent
   parameters (level, window, checksum, pledged size, and so on).
6. **Open source, public on GitHub**, at `github.com/vahid-sohrabloo/fastcompress`.

## Non-goals

- New APIs beyond klauspost's exported surface.
- Assembly-level speed on architectures other than amd64 and arm64. Those
  architectures get the portable Go code.
- Reproducing klauspost's exact compressed output, internal behaviour, or
  memory profile.

## Anti-goals (failure even if it "works")

- **Any cgo**, including in optional build tags.
- **Any klauspost dependency or klauspost-derived code.** That rules out a
  go.mod require (test-only included), copied code, and klauspost internals
  read as a reference. The *only* klauspost material allowed is its
  **exported API**: signatures, exported identifiers, and doc comments,
  because that is what the drop-in goal requires.
- **Decoder unsafety.** The decoder must never read or write out of bounds on
  malicious input, including in assembly.
- **Divergence from C's output** at a supported level/parameter combination.
  This counts as a bug, not a tolerance.

## Success criteria / how "done" is measured

- **Benchmark reference:** C libzstd **v1.5.7** (the latest release at design
  time), built from source, measured with `zstd -b<level>` on the same machine.
  Both sides run single-threaded and are pinned to the same core.
- **Corpora:** the Silesia corpus, plus restic-like blob chunks (1 MiB and 4
  MiB independent chunks of `silesia.tar`).
- **Level mapping** (klauspost API → C level):

  | API level                | C level |
  |--------------------------|---------|
  | `SpeedFastest`           | 1       |
  | `SpeedDefault`           | 3       |
  | `SpeedBetterCompression` | 7       |
  | `SpeedBestCompression`   | 11      |

- **Pass bar:** per level and direction, throughput ≥ 90% of C's on amd64 and
  on arm64.
- **Correctness bar:**
  - Compressed output equals C's byte for byte at each supported level.
  - Our output decodes with C, and C's output decodes with ours.
  - Fuzzing finds no crashes, including on the assembly paths.
  - API check against the frozen klauspost API snapshot passes.

## Phasing and measurement (the user's decisions)

- **Encoder first.** Phase 1 ports C's compressor: frame and block format,
  the match finders for the strategies behind levels 1/3/7/11 (`fast`,
  `dfast`, `greedy`/`lazy` as C selects them per level and source size), the
  literal and sequence entropy encoders, and xxhash64. It also builds the
  klauspost-API `Encoder` on top. Phase 1 tests decode through the C `zstd`
  CLI.
  The `Decoder` side of the API, plus API pieces that depend on it, arrive in
  Phase 2, which ports C's decompressor. **The library is only a full drop-in
  for restic after Phase 2.**
- **A level can ship** once it meets the bar. A level that can't reach 90% is
  brought back to the user.
- **Benchmark machine: the user's laptop**, an Intel Core i9-13900H (amd64,
  AVX2/BMI2, hybrid P/E cores, Linux). Runs are pinned to P-core CPU 2. CPU 0
  is avoided because it takes most interrupts.
- **arm64 has no benchmark machine yet.** arm64 code is correctness-tested
  (qemu and the GitHub arm64 runner). The arm64 speed bar stays unverified until
  hardware is available.
- **Assembly** (Go `.s`) is allowed on amd64 and arm64, with a pure-Go
  fallback on every architecture.

## Calls made by the designer (not the user's decisions; open to override)

- **API compatibility check without depending on klauspost:** generate a
  frozen text snapshot of klauspost v1.20.3's exported `zstd` API **once**
  (signatures only, via `go doc -all`) and commit it as `api/klauspost-zstd.txt`.
  A script checks that our package exports exactly that surface. Generating
  the snapshot is the only time klauspost is fetched, and nothing from it
  enters the module graph.
- **API pieces with no C counterpart** (the Snappy converter, the zip
  compressor/decompressor hooks, klauspost-style `BuildDict` inputs) are
  implemented from the zstd format spec (RFC 8878), the Snappy format spec,
  and C's `ZDICT` where it applies. They are built from their documented
  behaviour, never from klauspost's code.
- **Errors:** export the same error identifiers as klauspost. Malformed input
  returns an error matching the right exported sentinel under `errors.Is`.
  Messages may differ.
- **Equivalent parameters:** `EncodeAll` maps to `ZSTD_compress2` with the
  mapped level, `pledgedSrcSize = len(src)`, and checksum on or off according
  to `WithEncoderCRC`. Streaming `Writer` maps to `ZSTD_compressStream2` with
  an unknown size, unless the size is known via `ResetContentSize`. Options
  with no C equivalent (padding, zero frames) are applied on top of C-identical
  frames. The byte-identical rule applies wherever a C equivalent exists.
- **Internal structure mirrors C's file layout** (one Go file per C source
  file where practical, keeping C's function names in Go casing) so ports can
  be checked side by side against the C source.
- **CPU features:** BMI2/AVX2 behind runtime detection on amd64, with a
  baseline path as well. ARMv8.0 NEON on arm64.

## Constraints

- C libzstd is dual-licensed BSD/GPLv2. The port is taken under the **BSD**
  terms. LICENSE carries zstd's BSD notice (Meta Platforms, Inc. and
  affiliates), and the README credits zstd's authors.
- Go toolchain in the dev environment: go1.27.1. Module `go` directive: 1.25.
- The local `/usr/bin/zstd` is v1.5.5. Benchmarks and byte-identical tests use
  the v1.5.7 build.
- **To verify against restic's source before relying on it:** which encoder
  level and options restic passes, and whether it uses `EncodeAll`/`DecodeAll`
  or streaming.

## Left to the builder

- Order of the work within Phase 1, the benchmark harness and CI design, and
  fuzzing setup.
- Hand-written assembly versus generating it with avo (avo can't target
  arm64).
- How to test byte-identity at scale. Suggested: a tiny C test driver built
  with the v1.5.7 library, run as a separate process, never linked through cgo.

## Module path

`github.com/vahid-sohrabloo/fastcompress`, with the package at
`github.com/vahid-sohrabloo/fastcompress/zstd`.
