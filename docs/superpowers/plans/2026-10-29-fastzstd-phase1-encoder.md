# fastzstd Phase 1 (Encoder) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A pure-Go port of C libzstd v1.5.7's compressor that produces byte-identical output to C at levels 1/3/7/11 and runs at ≥90% of C's speed on amd64, exposed through the encoder half of klauspost's `zstd` API.

**Architecture:** This is a straight port of `lib/common` + `lib/compress`. The Go files mirror the C files, and the function names are C's in Go casing (`ZSTD_compressBlock_fast` → `compressBlockFast`). It happens in three stages:
1. A faithful pure-Go port, verified byte-for-byte against a C reference driver at every step.
2. The klauspost-shaped `Encoder` API on top.
3. Profile-driven Go assembly on amd64 (then arm64), with every assembly routine checked against the pure-Go port.

**Tech Stack:** Go 1.25+ (dev box: go1.27.1), Go assembly, C zstd v1.5.7 (reference only, built as a separate executable and never linked into Go), benchstat, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-29-fastzstd-design.md`

**C source reference:** `https://github.com/facebook/zstd` tag `v1.5.7`. Task 2 vendors a checkout into `ref/zstd/` (gitignored).

## Global Constraints

- Module `github.com/vahid-sohrabloo/fastzstd`. The public package is `github.com/vahid-sohrabloo/fastzstd/zstd` (package `zstd`). Internals live in `.../internal/...`.
- `go 1.25` in go.mod. **No dependencies** (stdlib only in the library). No cgo: `CGO_ENABLED=0 go build ./...` must pass.
- **No klauspost code, no klauspost dependency, and klauspost internals are never read.** The only klauspost artifact is `api/klauspost-zstd.txt`, an exported-API snapshot.
- The implementation follows C v1.5.7. A deviation is allowed only when it cannot change output, and it gets a comment `// C: <file>:<function> — <why different>`.
- Output is **byte-identical to C v1.5.7** for equivalent parameters at levels 1, 3, 7 and 11, for all input sizes, in one-shot, streaming and dictionary modes.
- Level mapping: `SpeedFastest`=1, `SpeedDefault`=3, `SpeedBetterCompression`=7, `SpeedBestCompression`=11.
- Assembly runs only under the build tags `<arch> && !appengine && !noasm && !purego && gc`. A pure-Go body is always compiled. BMI2/AVX2 are chosen at runtime, and there is a baseline-amd64 path too.
- Speed bar: Go throughput ≥ 90% of `zstd -b<level>` v1.5.7, both single-threaded and pinned with `taskset -c 2`.
- LICENSE: zstd's BSD license text with "Copyright (c) Meta Platforms, Inc. and affiliates." plus a line for this port. README credits Yann Collet and the zstd contributors.

## Review Focus

1. **Input sizes at C's parameter-table and block boundaries.** These are 0, 1, 16 KiB±1, 128 KiB±1, 256 KiB±1, and window size ±1. C switches strategy and parameters exactly there, and our output must switch the same way. Pinned by `crefInputs` in Task 2, which every byte-identity test uses.
2. **Degenerate data** (all-zero, single repeated byte, random/incompressible, short periods 2–8). These reach the RLE block, raw block, raw-literal and "savings < 3, don't pre-split" paths. Pinned by `crefInputs`.
3. **Index overflow correction on long streams** (more than 3.5 GB total through one `Writer`). Pinned by `TestCrefStreamOverflow` (Task 17, build tag `long`).
4. **Streaming with tiny writes, flushes and `Reset` reuse.** This must equal C `compressStream2` with the same `ZSTD_e_flush` points. Pinned by `TestCrefStreaming` (Task 17).
5. **Concurrent use of one `Encoder`** (`EncodeAll` from many goroutines) must stay race-free and give C-identical output. Pinned by `TestEncodeAllConcurrent` (Task 10, run with `-race`).

---

## File Structure

```
go.mod  LICENSE  README.md  .gitignore
api/klauspost-zstd.txt            frozen exported-API snapshot (klauspost zstd v1.20.3)
scripts/apicheck.sh               compares `go doc -all ./zstd` exports to the snapshot
ref/fetch.sh                      clones zstd v1.5.7 into ref/zstd, builds ref/zstd/programs/zstd, downloads Silesia
ref/cref/cref.c, Makefile         C reference driver, compiled directly from ref/zstd/lib sources
internal/xxhash/xxh64.go          ← common/xxhash.h (XXH64 only)
internal/bitstream/bitstream.go   ← common/bitstream.h (BIT_CStream_t)
internal/hist/hist.go             ← compress/hist.c
internal/fse/fse_compress.go      ← compress/fse_compress.c, common/entropy_common.c (encoder-side parts)
internal/huf/huf_compress.go      ← compress/huf_compress.c
internal/cpu/cpu.go               BMI2/AVX2 detection (golang.org/x/sys is NOT allowed; use the cpuid asm stub)
internal/creftest/creftest.go     Go test helpers that run cref (test-only)
zstd/                             public package (klauspost API) + compressor port:
  clevels.go                      ← clevels.h
  zstd_compress.go                ← zstd_compress.c (cctx, params, frame, blocks)
  zstd_compress_internal.go       ← zstd_compress_internal.h (window, matchState, seqStore, hash fns, count)
  zstd_compress_literals.go       ← zstd_compress_literals.c
  zstd_compress_sequences.go      ← zstd_compress_sequences.c
  zstd_preSplit.go                ← zstd_preSplit.c
  zstd_fast.go  zstd_double_fast.go  zstd_lazy.go  zstd_opt.go
  encoder.go  encoder_options.go  zip.go   klauspost-API encoder surface
bench/cmd/gobench/main.go  bench/compare.sh  bench/results/
.github/workflows/ci.yml
```

---

### Task 1: Scaffold, license, API snapshot

**Files:** Create `go.mod`, `LICENSE`, `README.md`, `.gitignore`, `api/klauspost-zstd.txt`, `scripts/apicheck.sh`, `zstd/doc.go`.

**Interfaces:**
- Produces: `scripts/apicheck.sh [--subset]`. It exits 0 when every exported identifier of `./zstd` appears in the snapshot with an identical declaration. Without `--subset`, it also requires that every snapshot identifier exists. Phase 1 runs it with `--subset`.

- [ ] **Step 1:** `git init`. Write go.mod (`module github.com/vahid-sohrabloo/fastzstd`, `go 1.25`), the LICENSE and README described in Global Constraints, `zstd/doc.go` (package comment only), and a `.gitignore` containing `ref/zstd/`, `ref/cref/cref`, `bench/corpus/` and `*.prof`.
- [ ] **Step 2:** Generate the snapshot in a **throwaway module in the scratchpad**, never in this repo:
  ```
  go mod init snap && go get github.com/klauspost/compress@v1.20.3
  go doc -all github.com/klauspost/compress/zstd
  ```
  Normalise it by keeping only declaration lines (`func`, `type`, `const`, `var`, method and struct field lines), sort it, and save it to `api/klauspost-zstd.txt`. Read nothing except this output.
- [ ] **Step 3:** Write `scripts/apicheck.sh` using the same normalisation on `go doc -all ./zstd`, then `comm` against the snapshot. Run `scripts/apicheck.sh --subset`. Expected: exit 0, since the package has nothing exported yet.
- [ ] **Step 4:** Run `go vet ./...`. Expected: clean. Commit `chore: scaffold module, license, klauspost API snapshot`, including the spec and this plan.

### Task 2: C reference driver and test helpers

**Files:** Create `ref/fetch.sh`, `ref/cref/cref.c`, `ref/cref/Makefile`, `internal/creftest/creftest.go`, `internal/creftest/creftest_test.go`.

**Interfaces:**
- `cref` CLI (the C program, built from `ref/zstd/lib` sources with `-DZSTD_STATIC_LINKING_ONLY -DFSE_STATIC_LINKING_ONLY -DHUF_STATIC_LINKING_ONLY -DZSTD_MULTITHREAD=0 -O2`). It reads stdin and writes stdout. Modes:
  - `cref compress -level L [-checksum 0|1] [-pledged 0|1] [-window-log W] [-dict FILE] [-raw-dict 0|1]` runs `ZSTD_compress2` (with `pledged=1`) or `compressStream2` with a single `ZSTD_e_end` (with `pledged=0`, matching `ZSTD_CONTENTSIZE_UNKNOWN`).
  - `cref stream -level L [-checksum] -chunks N1,N2,... -flush-after i,j,...` feeds chunks via `ZSTD_e_continue`, issues `ZSTD_e_flush` after the listed chunk indexes, then `ZSTD_e_end`.
  - `cref xxh64 -seed S` prints the 16 hex digits.
  - `cref hist` prints 256 counts, then `maxSymbol` and `largest`.
  - `cref fse -tablelog T` prints the bytes of `FSE_compress2(src, 255, T)`.
  - `cref huf1x|huf4x -tablelog T` prints the bytes of `HUF_compress1X`/`HUF_compress4X`.
  - `cref seqs -level L` prints C's seqStore for each block as text lines `lit=<n> off=<offBase> ml=<n>`, plus `lits=<hex>`. This is captured by calling `ZSTD_compressBlock_internal`'s seqStore path through `ZSTD_generateSequences` (an experimental API in 1.5.7).
- Go helpers in `internal/creftest`:
  - `func Bin(t testing.TB) string`: the cref path. It fails the test if cref is missing and `CI` is set, and skips otherwise.
  - `func Run(t testing.TB, in []byte, args ...string) []byte`
  - `func Inputs(t testing.TB) map[string][]byte`: the `crefInputs` set from Review Focus items 1–2, plus `bench/corpus/silesia/*` when present.
  - `func DecodeC(t testing.TB, frame []byte) []byte`: pipes the frame through `ref/zstd/programs/zstd -d -c`.

- [ ] **Step 1:** Write `ref/fetch.sh`. It is idempotent and does three things:
  - clone the tag into `ref/zstd`
  - build the `zstd` program (`make -C ref/zstd/programs zstd`)
  - download Silesia into `bench/corpus/silesia/` and make `bench/corpus/silesia.tar`
- [ ] **Step 2:** Write `cref.c` and its Makefile. Test: `echo -n hello | ref/cref/cref compress -level 3 | ref/zstd/programs/zstd -d -c`. Expected: `hello`.
- [ ] **Step 3:** Write `creftest_test.go` with `TestCrefRoundTrip`. For every `Inputs` entry, it runs `DecodeC(Run(in, "compress","-level","3"))` and checks the result equals `in`. Run `go test ./internal/creftest`. Expected: PASS.
- [ ] **Step 4:** Commit `test: C reference driver and helpers`.

### Task 3: CI

**Files:** Create `.github/workflows/ci.yml`.

- [ ] **Step 1:** Write the CI jobs. Runners are `ubuntu-latest` (amd64) and `ubuntu-24.04-arm` (arm64). Each job does `ref/fetch.sh` (without Silesia) and `make -C ref/cref`, then:
  - `go test ./...`
  - `go test -tags noasm ./...`
  - `go test -race -short ./...` (amd64)
  - `scripts/apicheck.sh --subset`
  - a `CGO_ENABLED=0` cross-build matrix: `linux/{386,arm,ppc64le,s390x,riscv64}`, `darwin/arm64`, `windows/amd64`

  Cache `ref/zstd` keyed by tag.
- [ ] **Step 2:** Run `go run github.com/rhysd/actionlint/cmd/actionlint@latest` (a dev tool, not a module dependency). Expected: no findings. If the GitHub remote doesn't exist yet, note it, because creating and pushing it is the user's call. Commit `ci: …`.

### Task 4: Common primitives (xxh64, bitstream, hist)

**Files:** Create `internal/xxhash/xxh64.go` with its `_test.go`, `internal/bitstream/bitstream.go` with its `_test.go`, `internal/hist/hist.go` with its `_test.go`.

**Interfaces:**
- `xxhash`: `type Digest struct{…}`, `func New(seed uint64) *Digest`, `(*Digest).Reset()`, `(*Digest).Write([]byte) (int, error)`, `(*Digest).Sum64() uint64`, `func Sum64(b []byte, seed uint64) uint64`.
- `bitstream`: `type CStream struct{ bitContainer uint64; bitPos uint; buf []byte; … }`, with `Init(dst []byte) error`, `AddBits(v uint64, n uint)`, `AddBitsFast`, `FlushBits()`, `FlushBitsFast()`, `Close() (n int)`. These mirror `BIT_*` in bitstream.h, including the 64-bit container semantics.
- `hist`: `func Count(count *[256]uint32, maxSymbol *uint32, src []byte) (largest uint32)` (= `HIST_count_wksp`), `func CountFast(…)` (= `HIST_countFast_wksp`), `func CountSimple(…)`.

- [ ] **Step 1:** Write the tests.
  - `TestXXH64Cref`: for every `creftest.Inputs` entry and seeds {0, 1, 0x9E3779B185EBCA87}, `Sum64` equals `cref xxh64`. Also check streaming writes in 1/7/64/4096-byte pieces.
  - `TestHistCref`: counts, maxSymbol and largest equal `cref hist`.
  - `TestBitstream`: writes known bit patterns and checks the exact output bytes. Derive the expected values by hand from bitstream.h semantics (little-endian, LSB-first) and write them into the test.
- [ ] **Step 2:** Run them. Expected: FAIL (the code isn't written).
- [ ] **Step 3:** Port the code. `hist.Count` keeps C's 4-table parallel counting (`HIST_count_parallel_wksp`).
- [ ] **Step 4:** Run `go test ./internal/...`. Expected: PASS. Commit `feat: port xxh64, bitstream, hist`.

### Task 5: FSE compression

**Files:** Create `internal/fse/fse_compress.go` with its `_test.go`.

**Interfaces:**
- `type CTable []uint32`, laid out as C's `FSE_CTable`.
- `func OptimalTableLog(maxTableLog uint, srcSize int, maxSymbol uint) uint`
- `func NormalizeCount(norm []int16, tableLog uint, count []uint32, total int, maxSymbol uint, useLowProbCount bool) (uint, error)`
- `func WriteNCount(dst []byte, norm []int16, maxSymbol, tableLog uint) (int, error)`
- `func BuildCTable(ct CTable, norm []int16, maxSymbol, tableLog uint, wksp []byte) error`
- `func CompressUsingCTable(dst, src []byte, ct CTable) (int, error)`
- `func Compress2(dst, src []byte, maxSymbol, tableLog uint) (int, error)`
- `type CState` with `InitCState`, `InitCState2`, `EncodeSymbol(bs *bitstream.CStream, s *CState, sym uint32)`, `FlushCState`. These are used by Task 7.
- `func BitCost(…)` / `GetMaxNbBits` for the encoding-type cost estimates.

- [ ] **Step 1:** Write `TestFSECref`. For each `Inputs` entry (truncated to 128 KiB) and tableLog ∈ {5, 9, 11, 12}, `Compress2` output equals `cref fse`, including the "not compressible" (0) and RLE (1) return conventions.
- [ ] **Step 2:** Run it. Expected: FAIL. Port the code. Run it again. Expected: PASS. Commit `feat: port FSE compression`.

### Task 6: Huffman compression

**Files:** Create `internal/huf/huf_compress.go` with its `_test.go`.

**Interfaces:**
- `type CElt uint64` and `type CTable []CElt`, as in the 1.5.7 layout (`HUF_CElt` with the header in elt 0).
- `func BuildCTable(ct CTable, count *[256]uint32, maxSymbol, maxNbBits uint, wksp []byte) (uint, error)`
- `func WriteCTable(dst []byte, ct CTable, maxSymbol, huffLog uint, wksp []byte) (int, error)`
- `func Compress1XUsingCTable`, `func Compress4XUsingCTable(dst, src []byte, ct CTable, flags int) int`
- `type Repeat int` (`RepeatNone`/`RepeatCheck`/`RepeatValid`)
- `func Compress1XRepeat`, `func Compress4XRepeat(dst, src []byte, maxSymbol, huffLog uint, wksp []byte, hufTable CTable, repeat *Repeat, flags int) int`
- `func OptimalTableLog(…)`, including the 1.5.7 `HUF_flags_optimalDepth` search.

- [ ] **Step 1:** Write `TestHUFCref`. For each input (≤128 KiB) and tableLog ∈ {6, 11}, `Compress1XRepeat`/`Compress4XRepeat` with `RepeatNone` and flags 0 equal `cref huf1x`/`huf4x`. Add a case with `HUF_flags_optimalDepth` set (a cref flag `-optimal 1`).
- [ ] **Step 2:** FAIL → port → PASS. Commit `feat: port Huffman compression`.

### Task 7: Block entropy stage (literals + sequences)

**Files:** Create `zstd/zstd_compress_literals.go`, `zstd/zstd_compress_sequences.go`, `zstd/seqstore.go`, `zstd/entropy_test.go`.

**Interfaces:**
- `type seqDef struct{ offBase uint32; litLength, mlBase uint16 }` and `type seqStore struct{ sequences []seqDef; literals []byte; llCode, mlCode, ofCode []byte; longLengthType int; longLengthPos uint32 }`, mirroring `SeqStore_t`.
- `type entropyCTables struct{ huf hufCTables; fse fseCTables }` and `type entropyCTablesMetadata`, mirroring C.
- `func compressLiterals(dst []byte, src []byte, prevHuf *hufCTables, nextHuf *hufCTables, strategy strategy, disableLiteralCompression bool, suspectUncompressible bool, bmi2 bool, wksp []byte) (int, error)`
- `func encodeSequences(dst []byte, ctML, ctOF, ctLL fse.CTable, seqs []seqDef, llCode, mlCode, ofCode []byte, longOffsets bool, bmi2 bool) (int, error)`
- `func selectEncodingType(…)`, `func buildCTable(…)`
- `func entropyCompressSeqStore(dst []byte, ss *seqStore, prev, next *entropyCTables, params *cctxParams, srcSize int, wksp []byte, bmi2 bool) (int, error)`. This also covers the `ZSTD_entropyCompressSeqStore_internal` and `ZSTD_seqToCodes` parts.

- [ ] **Step 1:** Write `TestEntropyBlockCref`. For each input ≤ 128 KiB at level 1, parse `cref seqs -level 1` into a `seqStore`. Run `entropyCompressSeqStore` with fresh entropy tables, wrap the result in a single compressed block inside a minimal frame (`writeFrameHeader` from Task 8 isn't ready yet, so write a fixed test-local header: magic, FHD=0x20, single segment with the content size), and check that `DecodeC` returns the input. **Equality of the block bytes** with C's first block from `cref compress` is asserted for inputs that C emits as one compressed block (not raw or RLE).
- [ ] **Step 2:** FAIL → port → PASS. Commit `feat: port literal and sequence entropy encoding`.

### Task 8: Compression context, frame, block loop, `fast` strategy (level 1)

**Files:** Create `zstd/clevels.go`, `zstd/zstd_compress.go`, `zstd/zstd_compress_internal.go`, `zstd/zstd_fast.go`, `zstd/cctx_test.go`.

**Interfaces:**
- `type compressionParameters struct{ windowLog, chainLog, hashLog, searchLog, minMatch, targetLength uint32; strategy strategy }`, plus `defaultCParameters [4][maxCLevel+1]compressionParameters` (from clevels.h; all 23 levels per table are copied verbatim, even though only 4 are supported).
- `func getCParams(level int, srcSizeHint uint64, dictSize int) compressionParameters` (C: `ZSTD_getCParams_internal` + `ZSTD_adjustCParams_internal`, mode `ZSTD_cpm_noAttachDict`).
- `type cctx struct{…}` with:
  - `func newCCtx() *cctx`
  - `(*cctx).setLevel(int)`, `setChecksum(bool)`, `setWindowLog(uint32)`, `setPledgedSrcSize(uint64)`
  - `(*cctx).compress2(dst, src []byte) ([]byte, error)` (appends to dst)
  - `(*cctx).reset()`
- `type window struct{ base []byte; dictLimit, lowLimit, nextSrc uint32; … }` with `windowUpdate`, `windowNeedOverflowCorrection`, `windowCorrectOverflow`. **Indices are `uint32` offsets into `base`**, exactly like C's `base + idx`.
- `type matchState struct{ window window; hashTable, chainTable, hashTable3 []uint32; nextToUpdate uint32; cParams compressionParameters; … }`
- `func count(in []byte, ip, match, iEnd int) int` (= `ZSTD_count`), `hashPtr(src []byte, i int, hBits, mls uint32) uint32` (= `ZSTD_hashPtr`, with C's prime constants), `storeSeq(ss *seqStore, litLen int, lits []byte, offBase uint32, matchLength int)`.
- `type blockCompressor func(ms *matchState, ss *seqStore, rep *[3]uint32, src []byte /* the block */, srcIdx uint32) (lastLLSize int)`, plus `selectBlockCompressor(strategy, rowMatchfinder bool, dictMode) blockCompressor`.
- `func compressBlockFast(…)`: a port of `ZSTD_compressBlock_fast` (noDict, with both of 1.5.7's `_withPrefetch` / `_noPrefetch` variant selection) for minMatch 4–7.

- [ ] **Step 1:** Write `TestCompress2Cref`. For each `Inputs` entry **of size ≤ 128 KiB** (pre-split arrives in Task 9), at level 1 and checksum ∈ {0, 1}, `cctx.compress2` equals `cref compress -level 1 -checksum c -pledged 1`. Add `-window-log 10/17` cases.
- [ ] **Step 2:** Run it. Expected: FAIL.
- [ ] **Step 3:** Port the pieces:
  - parameter selection
  - frame header (`ZSTD_writeFrameHeader`)
  - `ZSTD_compress_frameChunk` and `ZSTD_compressBlock_internal`, including `ZSTD_isRLE`, the raw-block fallback and `ZSTD_maybeRLE`
  - the repcode confirm/blockState swap (`ZSTD_blockState_confirmRepcodesAndEntropyTables`)
  - window handling and overflow correction
  - the checksum
  - `compressBlockFast`

  Replace `ZSTD_cwksp` with plain Go slices that are reused across calls. That can't change output.
- [ ] **Step 4:** Run it. Expected: PASS. Commit `feat: compression context, frame/block loop, fast strategy`.

### Task 9: Block pre-splitting (inputs > 128 KiB)

**Files:** Create `zstd/zstd_preSplit.go`. Modify `zstd/zstd_compress.go` (`optimalBlockSize`).

**Interfaces:** `func splitBlock(src []byte, blockSizeMax int, level int, wksp []byte) int` (= `ZSTD_splitBlock`), and `func (c *cctx) optimalBlockSize(src []byte, blockSizeMax int, splitLevel int, strat strategy, savings int64) int`.

- [ ] **Step 1:** Extend `TestCompress2Cref` to all `Inputs` sizes, including whole Silesia files, at level 1. Expected: FAIL on inputs over 128 KiB.
- [ ] **Step 2:** Port `zstd_preSplit.c` and `ZSTD_optimalBlockSize` (the `splitLevels` table, the `savings < 3` rule, and the "first full block is not split" rule). Expected: PASS. Commit `feat: port block pre-splitter`.

### Task 10: klauspost-API Encoder (one-shot)

**Files:** Create `zstd/encoder.go`, `zstd/encoder_options.go`, `zstd/errors.go`, `zstd/encoder_test.go`.

**Interfaces:** Every encoder-side exported identifier in `api/klauspost-zstd.txt`, with the exact declarations from the snapshot. That includes `Encoder`, `NewWriter`, `EncodeAll`, `EOption`, all `WithEncoder*`/`WithWindowSize`/`WithZeroFrames`/`WithAllLitEntropyCompression`/`WithNoEntropyCompression`/`WithSingleSegment`/`WithLowerEncoderMem`/`WithEncoderPadding`, `EncoderLevel` with its constants, `EncoderLevelFromString`, `EncoderLevelFromZstd`, `(EncoderLevel).String`, `MaxEncodedSize`, the exported errors, and `ErrEncoderClosed`.
- Semantics are taken from the snapshot's doc comments. `EncodeAll` → `cctx.compress2` with `pledged=len(src)`.
- An `Encoder` holds a `sync.Pool` of `*cctx`, which makes it safe for concurrent `EncodeAll`.
- **Options with no C equivalent:** padding (skippable frame, per the doc comment), zero frames, `WithNoEntropyCompression` (`ZSTD_c_literalCompressionMode=ZSTD_ps_disable`) and `WithAllLitEntropyCompression` (`ZSTD_ps_enable`). Each one maps to the C parameter where one exists. Otherwise it's applied as documented, and the byte-identity tests use the C mapping.
- `WithEncoderConcurrency(n)` is accepted and stored. Encoding stays single-threaded, which is C `nbWorkers=0` (spec: designer call; `EncodeAll` output never depends on it).
- **Strategies not yet ported** return the unexported `errStrategyNotPorted` from `EncodeAll`. Task 21 Step 3 requires that it no longer exists.

- [ ] **Step 1:** Write the tests.
  - `TestEncodeAllCref`: at `SpeedFastest`, for all `Inputs`, `EncodeAll` equals `cref compress -level 1 -checksum 1 -pledged 1`. The checksum default must match the doc comment.
  - `TestEncodeAllConcurrent` (Review Focus 5): 8 goroutines × all inputs on one `Encoder`. Every output equals cref's.
  - `TestEncoderLevelFromZstd`: the mapping table from the doc comment.
  - Run `scripts/apicheck.sh --subset`.
- [ ] **Step 2:** FAIL → implement → PASS, including `go test -race -run Concurrent ./zstd`. Commit `feat: klauspost-compatible Encoder (one-shot)`.

### Task 11: Benchmark harness against C

**Files:** Create `bench/cmd/gobench/main.go`, `bench/compare.sh`, `bench/README.md`, `zstd/bench_test.go`.

**Interfaces:**
- `gobench -level {1,3,7,11} -block N -iters N FILE` splits FILE into independent `-block`-sized chunks (0 = the whole file), runs `EncodeAll` with `WithEncoderLevel(map)` and `WithEncoderCRC(false)` (`zstd -b` doesn't checksum), and prints `MB/s ratio` for the best pass.
- `bench/compare.sh [-l "1 3 7 11"] [-f FILES] -label NAME` writes `bench/results/<date>-<label>.md` with the columns `file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio`. For each row it runs `taskset -c 2 ref/zstd/programs/zstd -b<L> -i5 [-B<block>]` and `taskset -c 2 gobench`, three times each, interleaved, and reports the median.
- Corpus: each Silesia file whole, plus `silesia.tar` with `-B1M` and `-B4M`.
- `BenchmarkEncodeAll/<file>/<level>` is for pprof.

- [ ] **Step 1:** Write it, then run `bench/compare.sh -l 1 -label level1-port`. Expected: every row has `Go ratio == C ratio` (byte-identical) and non-zero speeds. Commit the table and code: `bench: harness vs C zstd v1.5.7`.

### Task 12: `dfast` strategy (level 3)

**Files:** Create `zstd/zstd_double_fast.go`. Modify the block-compressor selection.

**Interfaces:** `func compressBlockDoubleFast(…) int` (blockCompressor signature), a port of `ZSTD_compressBlock_doubleFast` noDict (mls 4–7, with the `_noDict_generic` specialisations), plus `fillDoubleHashTable`.

- [ ] **Step 1:** Add level 3 to `TestCompress2Cref` and `TestEncodeAllCref`. FAIL → port → PASS. Commit `feat: port double-fast strategy`.

### Task 13: Row-based lazy matchfinder (`greedy`/`lazy`/`lazy2`, windowLog > 14)

**Files:** Create `zstd/zstd_lazy.go` (row part), `zstd/zstd_lazy_test.go`.

**Interfaces:**
- `func compressBlockLazyRow(…)`: a single generic `ZSTD_compressBlock_lazy_generic` port parameterised by `searchMethod=row` and `depth ∈ {0,1,2}`. It exposes `compressBlockGreedyRow`, `compressBlockLazyRow` and `compressBlockLazy2Row`.
- `rowFindBestMatch`, `rowUpdate`, and `rowGetMatchMask(tagRow []byte, tag byte, headGrouped uint32, rowEntries uint32) uint64` in pure Go (the scalar `ZSTD_row_getMatchMask` fallback). This is the main later assembly candidate.
- The `ZSTD_resolveRowMatchFinderMode` rule (enabled for greedy..lazy2 when windowLog > 14).

- [ ] **Step 1:** Add level 7 (lazy) and level 11 (lazy2) to the Cref tests for inputs > 256 KiB, and level 7 for 16 KiB < size ≤ 256 KiB. FAIL → port → PASS. Commit `feat: port row-based lazy matchfinder`.

### Task 14: Hash-chain lazy2 (windowLog ≤ 14)

**Files:** Modify `zstd/zstd_lazy.go`, adding `hcFindBestMatch`, `insertAndFindFirstIndex` and the hash-chain dispatch.

- [ ] **Step 1:** Add level 7 for sizes ≤ 16 KiB (lazy2, wlog 14) and for 16–128 KiB (lazy2, wlog 17, which is row) to the Cref tests. FAIL → port → PASS. Commit `feat: port hash-chain lazy search`.

### Task 15: Binary-tree lazy2 (`btlazy2`)

**Files:** Modify `zstd/zstd_lazy.go`, adding `updateDUBT`, `insertDUBT1`, `DUBTFindBestMatch` and `BtFindBestMatch`.

- [ ] **Step 1:** Add level 11 for 16 KiB < size ≤ 256 KiB to the Cref tests. FAIL → port → PASS. Commit `feat: port btlazy2`.

### Task 16: Optimal parser (`btopt`)

**Files:** Create `zstd/zstd_opt.go`.

**Interfaces:** `func compressBlockBtopt(…) int`, porting `ZSTD_compressBlock_opt_generic` with `optLevel=0`, plus `ZSTD_rescaleFreqs`, `ZSTD_insertBtAndGetAllMatches`, `ZSTD_updateStats`, and the price functions.

- [ ] **Step 1:** Add level 11 for sizes ≤ 16 KiB to the Cref tests. FAIL → port → PASS. Commit `feat: port btopt optimal parser`.

### Task 17: Streaming (`Writer`) and extDict variants

**Files:** Modify `zstd/zstd_compress.go` (`compressStream2`, input/output buffering per C's `ZSTD_compressStream_generic`), `zstd/encoder.go` (`Write`, `ReadFrom`, `Flush`, `Close`, `Reset`, `ResetContentSize`). Add `_extDict` variants to each strategy file. Create `zstd/stream_test.go`.

**Interfaces:**
- `(*cctx).compressStream2(dst []byte, in []byte, op endDirective) (out []byte, consumed int, remaining int, err error)`
- `Encoder.Write` → `continue`, `Flush` → `flush`, `Close` → `end`, and `Reset(w)` → `ZSTD_CCtx_reset(session_only)` with an unknown pledged size.
- `ResetContentSize(w, size)` sets the pledged size.

- [ ] **Step 1:** Write the tests.
  - `TestCrefStreaming` (Review Focus 4): for all levels, chunk patterns {1-byte, 1000-byte, random 1..200000 with seed 1}, and a flush every k chunks, `NewWriter` + `Write`/`Flush`/`Close` output equals `cref stream` with the same chunks and flushes.
  - `Reset` reuse across 3 inputs.
  - `ReadFrom`.
  - `TestCrefStreamOverflow` (Review Focus 3; build tag `long`): 4.5 GB of `silesia.tar` repeated through one `Writer` at level 1 and level 3, compared against `cref stream` via SHA-256 of the two outputs.
- [ ] **Step 2:** FAIL → port (including every strategy's `_extDict` block compressor and `ZSTD_window_update` with non-contiguous input) → PASS (`go test ./zstd` plus `go test -tags long -run Overflow ./zstd`). Commit `feat: streaming Writer and extDict strategies`.

### Task 18: Dictionaries

**Files:** Create `zstd/zstd_compress_dict.go` (CDict, `ZSTD_loadCEntropy`, `ZSTD_loadDictionaryContent`, `ZSTD_shouldAttachDict`, the attach/copy paths) and `_dictMatchState` variants in each strategy file. Modify `zstd/encoder_options.go` (`WithEncoderDict`, `WithEncoderDictRaw`). Create `zstd/dict_test.go`.

**Interfaces:** `type cdict struct{…}`, `func createCDict(dict []byte, raw bool, cParams compressionParameters) (*cdict, error)`, and `(*cctx).refCDict(*cdict)`. Dictionary ID handling follows the klauspost doc comments for the option semantics, and C for the bytes.

- [ ] **Step 1:** Write `TestDictCref`. Train one dictionary with `ref/zstd/programs/zstd --train` on a fixed 200-sample subset of Silesia split into 4 KiB pieces, and commit it as `zstd/testdata/train.dict`. Then for all levels, input sizes {100 B, 4 KiB, 64 KiB, 1 MiB}, and both structured and raw (`-raw-dict 1` with the first 32 KiB of `dickens`) dictionaries, `EncodeAll` and `Writer` outputs equal `cref compress -dict`.
- [ ] **Step 2:** FAIL → port → PASS. Commit `feat: dictionary compression`.

### Task 19: Remaining Phase 1 API surface

**Files:** Create `zstd/zip.go`. Modify `zstd/encoder.go`.

- [ ] **Step 1:** Implement the encoder-side identifiers still missing from the snapshot according to `scripts/apicheck.sh` (for example `ZipCompressor`, `ZipMethodWinZip`, `ZipMethodPKWare`, `Encoder.EncodeAll` corner cases, `MaxWindowSize`/`MinWindowSize` constants) from their doc comments. Decoder-side identifiers (`Decoder`, `NewReader`, `DOption`, `Header`, `BuildDict`, the Snappy converter and the decoder-related `Zip*` functions) are **Phase 2** and are listed in `docs/superpowers/phase2-api-todo.md`.
- [ ] **Step 2:** Test: a `ZipCompressor` registered with `archive/zip` writes an archive, which is extracted by decoding each entry via `DecodeC`. Run `scripts/apicheck.sh --subset`. Expected: PASS. Commit `feat: zip compressor and remaining encoder API`.

### Task 20: Profile and amd64 assembly

**Files:** Create per hot path: `<file>_amd64.go` (declaration + `use<Name>Asm` toggle from `internal/cpu`), `<file>_amd64.s`, and `<file>_generic.go` (the current Go body, always compiled). Create `zstd/asm_equiv_test.go`.

**Interfaces:**
- `func withAsm(t testing.TB, on bool, fn func())` toggles every registered `use*Asm`.
- `TestAsmEquiv` runs every Cref test body with assembly off and on and requires identical output. It also repeats under `cpu.DisableBMI2()`.
- `FuzzEncodeAsmEquiv` checks equal output with assembly on and off, plus `DecodeC` round-trip, at all levels.

- [ ] **Step 1:** Run `bench/compare.sh -label pure-go` across all levels, then collect `go test -bench EncodeAll -cpuprofile` for each level with `taskset -c 2`. Record the top 10 flat functions per level in `bench/results/pure-go-profiles.md`. Commit.
- [ ] **Step 2:** For each hot path, in order of its profile share, and **only while its level is below 90%**: move the body to `_generic.go`, write the assembly, add the toggle, run `TestAsmEquiv` and `go test -fuzz FuzzEncodeAsmEquiv -fuzztime 5m`, rerun `compare.sh` for the affected levels, and commit `perf(<fn>): amd64 asm` together with the table. Expected: no regression greater than 3% in any row.
- The expected candidates, which C also SIMD-optimises or which are its tightest loops:
  - `count` (`ZSTD_count`, with 8-byte compare and `TZCNT`)
  - `rowGetMatchMask` (SSE2 `PCMPEQB`/`PMOVMSKB`, as C's `ZSTD_row_getSSEMask`)
  - the inner loops of `compressBlockFast` and `compressBlockDoubleFast`
  - `huf.Compress4XUsingCTable`'s inner loop
  - `encodeSequences`
  - `hist.Count`
- [ ] **Step 3:** If a level stays below 90% after its candidates are done, stop and record it for Task 22. **Do not change the algorithm**, because output must stay C-identical.

### Task 21: arm64 ports

**Files:** Create `<file>_arm64.go` and `<file>_arm64.s` for each assembly routine from Task 20. `rowGetMatchMask` uses NEON (C's `ZSTD_row_getNEONMask` is the reference).

- [ ] **Step 1:** For each routine, run `GOARCH=arm64 go test -exec "qemu-aarch64 -L /usr/aarch64-linux-gnu" -run 'AsmEquiv|Cref' ./zstd/... ./internal/...` (cref runs natively on the host, since it's a separate process). Expected: PASS. Commit `perf(<fn>): arm64 asm`.
- [ ] **Step 2:** Add to `bench/README.md` that arm64 speed against C is unverified until arm64 hardware is available.
- [ ] **Step 3:** Run `grep -rn "errStrategyNotPorted\|TODO\|t.Skip(" zstd internal`. Expected: no hits from this plan, except `Skip` in the `creftest.Bin` helper.

### Task 22: Restic check and Phase 1 gate report

**Files:** Create `bench/results/restic-check.md` and `bench/results/<date>-phase1-final.md`.

- [ ] **Step 1:** Read restic's latest release source (in the scratchpad). Record the encoder options restic uses for its `auto` and `max` modes and confirm that every one of them is implemented. Run `bench/compare.sh` with restic's levels on the `-B1M`/`-B4M` sets. Restic can only swap imports after Phase 2 (decoder).
- [ ] **Step 2:** Run the full `bench/compare.sh -label phase1-final`. List every row with `% of C` < 90, together with its profile top functions, and bring that list to the user (spec: levels below the bar).
- [ ] **Step 3:** Run the verification evidence:
  - `go test ./...`
  - `go test -tags noasm ./...`
  - the qemu arm64 run
  - `go test -tags long -run Overflow ./zstd`
  - `scripts/apicheck.sh --subset`
  - 10 minutes of fuzzing

  Report the outputs.
