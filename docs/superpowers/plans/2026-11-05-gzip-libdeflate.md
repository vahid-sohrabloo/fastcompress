# gzip / DEFLATE port of libdeflate v1.26 (pure Go + optional asm)

Status: **APPROVED 2026-11-05 (all recommended defaults D1-D8 accepted).** Execution starts at Phase 0.
Worktree `~/projects/fastzstd-gzip`, branch `feat/gzip-libdeflate` (from `feat/phase1-encoder` @ 59c35a4).
C reference: `ref/libdeflate` @ **v1.26** (latest tag; MIT). Files read for this plan: `lib/deflate_compress.c`
(4135 lines), `lib/deflate_decompress.c` (1208), `lib/decompress_template.h` (777), `lib/{hc,ht,bt}_matchfinder.h`,
`lib/matchfinder_common.h`, `lib/gzip_*.c`, `lib/zlib_*.c`, `lib/x86/decompress_impl.h`; repo: `internal/cpu`,
`internal/guardmem`, `internal/huf` (dispatch + asm + `BOUNDS:` blocks), `internal/creftest`, `ref/cref`,
`ref/fetch.sh`, `.github/workflows/{ci,fuzz}.yml`, `scripts/{fuzz,decodercheck}.sh`.

## 0. What the C code is (shapes everything below)

* **Whole-buffer only.** `libdeflate_deflate_compress(c, in, n, out, cap)` / `..._decompress`. No streaming, no flush,
  no dictionary. The stdlib-style `Reader`/`Writer` therefore need a design that is **not in C** (decisions D1, D2).
* **Compressor levels** (`libdeflate_alloc_compressor_ex`): 0 stored; 1 `deflate_compress_fastest` (ht_matchfinder);
  2-4 `greedy` (hc, depth 6/12/16, nice 10/14/30); 5-7 `lazy` (16/35/100); 8-9 `lazy2` (300/600, nice 258);
  10-12 `near_optimal` (bt_matchfinder; passes 2/4/10). Inputs <= `55 - 4*level` bytes go out stored.
  Block splitting (`observe_literal/match`, `do_end_block_check`, `MIN_BLOCK_LENGTH` 5000, `SOFT_MAX_BLOCK_LENGTH`
  300000, fast variant 65535/8192) and min-match-length heuristics affect output bytes: they must port exactly.
* **Decompressor**: `decompress_template.h` compiled twice (generic, and BMI2 via `bzhi`); fast loop + slow loop,
  one-pass table-driven Huffman (`make_decode_table_entry`, `build_decode_table`), overread tolerated into zero bits.
* Matchfinder window is 32 KiB (`MATCHFINDER_WINDOW_ORDER` = `DEFLATE_WINDOW_ORDER`), `mf_pos_t` = s16, with
  `matchfinder_rebase` every 32 KiB.

## 1. Decisions I need from you (recommended default first)

| # | Question | Recommended default |
|---|----------|---------------------|
| D1 | `Writer` is streaming but C is one-shot. | **Chunked**: buffer up to `Chunk` (default 1 MiB) plus 32 KiB carried history; each chunk is compressed as non-final blocks, matchfinder state carried (C's own `in_base`/rebase mechanism), `Close` emits the final block, `Flush` = sync flush (empty stored block). Whole-buffer API is byte-identical to libdeflate. Streaming output is identical **only** when total input <= `Chunk` and no `Flush`; this is tested. Alternative: buffer everything until `Close` (identical always, unbounded memory). |
| D2 | `Reader` is streaming; C decompressor is not resumable. | **Own design, approved in this refined form**: the ported fast loop (full libdeflate speed) runs over whatever input/output is in memory and **suspends only at symbol boundaries**, whenever output space left is < 258 bytes (so a match copy is never split: no pending-match state) or input is below C's `FASTLOOP_MAX_BYTES_READ` margin (the C slow loop handles the last symbols). Saved state is small: bitbuf/bitsleft, block type + final flag, built tables, stored-block remaining. A block header (incl. dynamic tables) is parsed all-or-nothing: if not fully buffered, rewind to its start and wait (bounded retry). Output keeps a 32 KiB history. Whole-block decoding was rejected: block size is unbounded. Differentially fuzzed against stdlib at 1-byte..N-byte read sizes. Gets its own short design note before Phase 3 code. |
| D3 | Is "100% of libdeflate" measured with asm on, or pure Go? | **asm build** (amd64 BMI2) is the target; pure-Go fallback is reported, not gated. My honest estimate: pure Go lands ~55-75% decode and ~50-80% compress (no `bzhi`/`shrx` at GOAMD64=v1, bounds checks, no `restrict`); the 95%/90% floors will need asm in the hot loops. |
| D4 | Layout. | Port lives in **`internal/deflate`** (decoder, compressor, matchfinders, huffman, bit writer, asm); `flate/`, `gzip/`, `zlib/` are thin public packages over it. Keeps asm/fuzz/coverage in one place. |
| D5 | `scripts/decodercheck.sh` rules (no `unsafe`/`recover`, `BOUNDS:` in `.s`, `FuzzXxxAsmEquiv`) only cover zstd files. | Extend it **additively** to `internal/deflate/*decompress*` (shared script: needs your OK). |
| D6 | Level/API mapping. | `-1`->6 (libdeflate does the same), `0`->stored, `1..9`->same level, `10..12` accepted as an extension, `HuffmanOnly (-2)` implemented as an extension (all literals, reusing `deflate_flush_block`; no libdeflate identity). `NewWriterDict`/`NewReaderDict` supported via the carried-history mechanism (D1). |
| D7 | "restic-style integration check". | My reading: a test + `scripts/` job that round-trips a repository-like workload (many 512 KiB-8 MiB blobs, pooled `Writer.Reset`/`Reader.Reset` across goroutines under `-race`, outputs cross-read by stdlib and the C `gzip`-compatible tool). Tell me if you meant something else. |
| D8 | C driver. | New **`ref/ldref`** (own Makefile, builds one TU including `lib/*.c` so `static` internals - decode tables, huffman codes, matchfinder results - can be dumped) plus an additive `internal/creftest/libdeflate.go`. cref/zstd untouched. Note `creftest` runs C as a subprocess (no cgo at all), which I keep. |

## 2. Ground rules (apply to every task)

* **Clean-room**: only `ref/libdeflate` as implementation guide. No klauspost/stdlib flate source. They are used as
  black boxes (tests, benches). Each ported function carries `// port of <c_name>` (C file in the file header).
  Ported files keep the libdeflate MIT notice; add `internal/deflate/NOTICE`.
* **Go style as in the repo**: asm `*_amd64.{go,s}` / `*_arm64.{go,s}`; dispatch `*_asm.go`, `*_noasm.go`,
  `*_generic.go`; `cpu.RegisterAsm` + runtime detection + pure-Go fallback always; `// BOUNDS:` block per asm
  TEXT; `FuzzXxxAsmEquiv` on `guardmem` buffers, registered in an asm-equivalence test; `fuzzasm_test.go` per
  package. No `unsafe` in decoder files (decodercheck); compressor files avoid it unless a profile demands it.
* **Memory protocol** (laptop OOM history): every test/fuzz/bench is
  `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0 env GOMEMLIMIT=2GiB go test -p 1 ...`
  (fuzz adds `-parallel 2 -fuzzminimizetime 2s`). Exactly one capped process at a time; before long runs check
  `uptime`, `free -g`, and the zstd session's use (>3 GB: wait). No `&`; background runs with own log+PID.
  Never raise the cap (OOM under cap = bug signal).
* **Benchmarks**: AC online (`/sys/class/power_supply/AC0/online`==1), load1 < 2 (recorded in the result file),
  `taskset -c 2`, not while the zstd session benchmarks. Today's load is 5.5, so nothing was benchmarked for this plan.
* **Git**: commit only on `feat/gzip-libdeflate`, only when you ask (or the plan delegates it); explicit paths,
  never `add -A`; never push/merge; no secrets; don't touch `.omc/`, `docs/superpowers/plans/2026-11-02-*`, or the
  untracked `bench/results` files.
* **Execution**: subagent-driven. Per task: implementer -> separate reviewer -> fix round -> re-review. Ledger at
  `.superpowers/sdd/gzip-libdeflate/` (already git-ignored). Every subagent prompt says: *poll your own log file
  and PID; do not wait for notifications*; I check on them too.
* **Shared internals**: nothing under `zstd/` or existing `internal/*` changes behaviour. Anything that would
  needs your sign-off first (D5 is the one known case; `creftest` gets a new file only).

## 3. Verification strategy (what "correct" means)

1. **Compressor byte-identity** (levels 0..12): for each corpus input, `Compress(level)` == `ldref compress -l N`.
   Corpus = Silesia files (and 1 MiB / 128 KiB / 16 KiB independent blocks of silesia.tar), plus generated cases:
   sizes 0, 1, 2, 54-`55-4L`+-1 (passthrough boundary), 5000, 32767-32769 (rebase), 65535/65536, 299999-300001
   (soft block max), all-zero, all-same-byte, 258-period runs, incompressible random, text-like, tiny alphabets,
   many-block inputs, `<=` 4 GiB-edge synthetic via sparse data (skipped in -short).
2. **Stage-level identity via `ldref` internals** (so a mismatch points at the stage, not just "bytes differ"):
   huffman codeword lengths/codewords per block, precode items, block-split decisions, sequence lists
   (litrunlen/length/offset), min-match-length choice, near-optimal cost tables.
3. **Decompressor**: accept everything libdeflate accepts, reject everything it rejects, same output and same
   `actual_in/out` consumed; plus stdlib `compress/flate` as a second oracle on valid streams. Ports of
   `programs/test_{invalid_streams,incomplete_codes,overread,trailing_bytes,slow_decompression,litrunlen_overflow}.c`.
   Fuzz: arbitrary bytes, and mutations of valid streams; asserts no panic, no overread (guardmem).
4. **Asm vs Go**: `FuzzXxxAsmEquiv` for every loop; seed runs under `FASTZSTD_FUZZ_ASM=off|nobmi2|on`.
5. **API parity** (Phase 3): reflect-based comparison of exported identifiers/method signatures with stdlib
   `compress/{flate,gzip}` (black box), plus behavioural tests (Header fields, Multistream, Reset, errors).

## 4. Performance gates (per level, per direction)

Report (`bench/gzip/compare.sh` -> `bench/results/<date>-gzip-<label>.md`): `file | level | block | C MB/s | Go MB/s
| % of C | C ratio | Go ratio` with MISMATCH on any size difference, plus stdlib and klauspost columns (black boxes).
Same timing method as the zstd bench (best window of ~1 s mean-per-pass, median of `-n` runs, C and Go interleaved,
pinned). **The C baseline is the latest libdeflate (v1.26) built as it ships, i.e. with its own asm/SIMD paths on**:
CMake Release, `-O2`, runtime dispatch to the BMI2 decompressor, AVX2/SSE2 matchfinder init, PCLMUL/VPCLMUL crc32
and AVX2 adler32 (verified by `ldbench` printing the selected impls). Our side is measured with asm **on** (the gated
number) and, separately, `-tags noasm` (reported only). A secondary reference column is ISA-L (igzip, assembly, v2.31.1)
for compression levels where it is comparable, and the black-box stdlib / klauspost columns. Each result file records
the libdeflate tag, compiler version/flags, detected CPU features, load1 and AC state, so "latest with asm" is
reproducible. Gate per level: geomean >= 100%, every row >= 95%, hard floor 90%. Every optimisation is pprof-driven: profile before/after, keep
profiles in `bench/results/*-profile.md`, choose the next target from data, not intuition.

## 5. Phases and tasks

Notation: **Files** (new unless noted) / **Tests** / **C ported** / **Verify**.

### Phase 0 - Reference and harness (needs approval of `fetch.sh` / `.gitignore` edits)

* **T0.1 fetch libdeflate.** Files: `ref/fetch.sh` (edit: after the zstd step, idempotent
  `git clone --depth 1 --branch v1.26` into `ref/libdeflate` if missing; zstd and Silesia steps untouched),
  `.gitignore` (+`ref/libdeflate/`, `ref/ldref/ldref`, `ref/ldref/ldbench`). Verify: rerun twice, second run
  no-op; `git status` clean apart from intended files. (`ref/libdeflate` is already cloned by hand for reading.)
* **T0.2 `ref/ldref`.** Files: `ref/ldref/{ldref.c,ldbench.c,Makefile}`, `internal/creftest/libdeflate.go`.
  Subcommands: `compress -l N`, `decompress` (prints result code, actual in/out, output), `gzip-*`, `zlib-*`,
  `dump-tables|dump-codes|dump-split|dump-seqs`. Tests: `internal/creftest/libdeflate_test.go` (roundtrip via
  stdlib, determinism, CI-fail-when-missing like cref). Verify: `ldref` output decodes with stdlib for levels 0..12.
  *As built (reviewed 2026-11-05, ldref verified byte-identical to a stock cmake-built v1.26 at levels 0..12,
  raw/gzip/zlib):* the CLI uses `-format raw|gzip|zlib` instead of separate `gzip-*`/`zlib-*` subcommands;
  `decompress-ex` prints `result=<0..3> in=<I> out=<O>\n` then the data (`-exact 1` is needed to get
  `short_output`), plain `decompress` exits 10+result on error; `ldref version` prints the tag, arch, cpu
  features and `decompress=bmi2|default`. `ldref.c` is one TU including deflate_{de,}compress.c, gzip_*.c, zlib_*.c
  (decompressor first, then `#undef BITBUF_NBITS` before the compressor: dump-* code needing the decompressor's
  value must redefine it). `dump-*` subcommands are added by the tasks that need them (T1.1, T2.x).
* **T0.3 corpus generators.** Files: `internal/deflate/testcorpus_test.go` (+ shared helper). Verify: seeds stable.
  *T0.3 as built (reviewed, approved):* the helper is the importable package `internal/deflatetest` (not a `_test.go`
  file); `Cases(short)`/`Blocks`/`Levels`/`Silesia`/`SilesiaTar`, integer-only generators, golden hashes pinned on
  amd64. **Carried to T2.3:** add a `dense4` case (one literal + a 4-byte copy, ~5 B/seq) so level 1's
  FAST_SEQ_STORE_LENGTH (8192) block end is really reached (`dense-matches` only reaches SEQ_STORE_LENGTH 50000);
  the 4 GiB sparse synthetic is deferred to the Phase 2 identity task (streamed, never allocated); the short set is at
  7.43 of 8 MiB, trim before adding more.
* **T0.4 bench harness.** (As built: a nested module `bench/gzip/` with `main.go`, `adapter.go`, `ours.go`,
  `timing.go`, `stats.go`, `compare.sh`, `README.md`; the plan's `bench/cmd/gzbench` path is superseded, and
  klauspost/compress is confined to the nested module so the root `go.mod` is untouched.)
  Files: `bench/gzip/compare.sh`, `bench/gzip/README.md`
  (generalises `~/projects/fastzstd-gzbench`, which holds a throwaway v1.23/ISA-L comparison). Verify: dry run
  with `-n 1 -i 1` on one file; no full run while the zstd session is active.

### Phase 1 - Decompressor (`internal/deflate`)

* **T1.1 constants, static tables, decode tables.** Files: `consts.go`, `decode_table.go`, `static_codes.go`.
  C: `make_decode_table_entry`, `build_decode_table`, `build_precode_decode_table`, `build_litlen_decode_table`,
  `build_offset_decode_table`, `deflate_constants.h` tables. Tests: table dumps == `ldref dump-tables` for random
  valid length sets and for the incomplete-code edge cases (single code, empty offset code). Verify: byte equal tables.
* **T1.2 generic decoder core.** Files: `decompress.go`, `decompress_generic.go`, `bitreader.go` (internal).
  C: `decompress_template.h` (`FUNCNAME`: next_block, stored/static/dynamic block setup, fast loop, slow loop,
  `REFILL_BITS`, overread accounting), `libdeflate_deflate_decompress{,_ex}`, `SAFETY_CHECK`s. Public-in-package API:
  `Decompress(dst, src) (nOut int, err)`, `DecompressEx` -> `(nIn, nOut, err)`; error values mirror
  `LIBDEFLATE_{BAD_DATA,SHORT_OUTPUT,INSUFFICIENT_SPACE}`. Tests: golden vectors; `TestMatchesLibdeflate` over corpus;
  FuzzDecompress vs `ldref`. Verify: coverage on `decompress*.go` >= 95%.
  *T1.2 as built:* no separate `bitreader.go` (the refill code is written out inline at each use for speed and
  guarded by a test that the copies stay identical); the decoder always uses the 64-bit bit buffer (amd64 behaviour)
  on every platform; `Decompress` (exact output length) and `DecompressEx` as planned; `ldref decompress-batch`
  was added (additive) to run the differential suites quickly. Reviewed: reviewer A (setup half, ~64M exhaustive/
  crafted differential requests) APPROVE, reviewer B (loops + tests) APPROVE WITH FIXES (test gaps only: refill
  thresholds, output margin, fuzz isolation).
* **T1.3 malformed-input suite.** Files: `decompress_invalid_test.go`, `decompress_overread_test.go`.
  C: ports of the six `programs/test_*.c`. Verify: guardmem: zero out-of-range reads at every truncation point.
  *T1.3 as built (reviewed: APPROVE WITH FIXES, fix round in progress):* `decompress_{invalid,overread,stress,slowrepro}_test.go`
  port the six C test programs (streams verified byte-identical against the C arrays by the reviewer), 100% statement
  coverage of the four decoder files (9 of 11 reviewer mutations killed; coverage is not proof). **Carried forward:**
  (a) T3.1/T3.2 must add the zlib and gzip cases of `test_trailing_bytes.c` (container layers) and the compress-side of
  `test_litrunlen_overflow.c` / `test_trailing_bytes.c`; (b) **T5.1 must add `make -C ref/ldref` to the ci.yml test,
  noasm, 386, race and fuzz jobs and to fuzz.yml (all T1.2/T1.3 tests need `ref/ldref/ldref`, and creftest turns a
  missing tool into a failure under CI), and T5.2 the deflate coverage regexes (`.superpowers/sdd/gzip-libdeflate/covgate.sh`
  is the local prototype, same awk semantics as the zstd gate)**; (c) the guard-page layouts in the overread test cannot
  fault until Phase 4 asm exists.
* **T1.4 prerequisites carried from the T0.4 re-review (do these first, they are small):** (a) `bench/gzip/compare.sh`
  must support a **decode-only `ours`** (Phase 1 has a decompressor but no compressor): `prepare` must create the
  compressed input with ldref/ldbench (or stdlib) and run `ours` only for `-mode d`; today exit 3 from `ours` disables
  all `ours` rows; (b) run `make -C ref/ldref` unconditionally so a stale 2-field `ldbench` can never silently fall back
  to the ratio-string compare; (c) `OURS_NA_LEVELS` must not repeat levels in the header, and a level with no rows must
  still appear in the gate table (as n/a, FAIL).
  *T1.4 DONE 2026-11-05:* `bench/results/2026-11-05-deflate-decode-go.md` (+ `-profile.md`): generic Go decoder is
  **61-63% geomean of libdeflate v1.26** (rows 51-75%); 80-86% of time in the decode loop, 12-19% in the inlined
  8-byte load/stores, ~1% table building. Phase 4 decoder shortlist = one item: the amd64 BMI2 fast-loop asm
  (see the profile doc). Conditions: load1 1.56-1.60, AC online, nothing else running.
* **T1.4 baseline perf + asm decision.** Files: `bench/results/<date>-deflate-decode-go.md`. pprof of generic loop
  per corpus file; written asm shortlist. Verify: report only.

### Phase 2 - Compressor, in C's order

* **T2.1 Huffman construction + bit writer.** Files: `huffman.go`, `bitwriter.go`.
  C: `heapify_*`/`heap_sort`, `sort_symbols`, `build_tree`, `compute_length_counts`, `gen_codewords`,
  `deflate_make_huffman_code(s)`, `deflate_reset_symbol_frequencies`, `deflate_compute_precode_items`,
  `deflate_precompute_huffman_header`, `deflate_init_static_codes`, `deflate_compute_full_len_codewords`,
  `deflate_get_offset_slot`, `ADD_BITS/FLUSH_BITS` output bitstream with overflow flag.
  Tests: code lengths/codewords == `ldref dump-codes` on random frequency vectors (incl. max-length limiting).
  *T2.1 as built (reviewed APPROVE, opus; sign-off is on a snapshot taken before T2.2 added the embedded
  `compressorState` to huffman.go - the T2.2 reviewer must re-run the T2.1 tests on the final files):* Huffman builders,
  precode/header, static codes, slot tables in `huffman.go`, `bitwriter.go` (64-bit buffer on every GOARCH),
  `consts_compress.go`; litlen max codeword length is **14** (offset 15, precode 7); unions are separate fields (checked
  unobservable); unused-symbol codewords follow the x86 build; `ldref` gained `dump-codes|dump-header|dump-static|dump-bits`.
  Open NITs for a cleanup pass: bitwriter.go:94-95 comment ("kept out of line"), a codeword comment needs "(normal path)",
  note the equivalent heap tie-break mutant in the ledger.
* **T2.2 Block writer + level 0.** Files: `compress.go` (compressor struct, alloc, level table),
  `flush_block.go`. C: `libdeflate_alloc_compressor_ex` level table, `libdeflate_deflate_compress`,
  `deflate_flush_block` (+ `deflate_write_*`, `deflate_finish_block`), `deflate_compress_none`,
  `libdeflate_deflate_compress_bound`, `max_passthrough_size`. Tests: level 0 and passthrough sizes byte-identical.
  *T2.2 as built (reviewed APPROVE WITH FIXES, opus; no output-affecting findings):* `NewCompressor`/`Compress`/
  `CompressErr`/`CompressBound`, full level table, `deflate_flush_block` (3 helpers), `deflate_compress_none`;
  `ldref` gained `dump-flush|compress-bound|dump-params`. **Cleanup pass after T2.3 (do not edit compress.go
  concurrently with T2.3):** (1) the compress path must not allocate: the bitstream must live in the Compressor, add
  AllocsPerRun tests for every ported level path; (2) **T2.6 must delete `CompressErr`, `ErrNotPorted` and the panic and
  add a test that every level 1..12 has an impl**; until then adapters (bench/gzip) use `CompressErr`; (3) CompressBound
  doc: saturates at MaxInt (32-bit only), receiver unused, works on nil; (4) stale 'This task (T2.1)' comment on
  `Compressor`, keyed fields for `nearOptimalParams`; (5) keep each parser's state (offsetSlotFull 32 KiB, bt/hc
  matchfinders, sequence store) behind a pointer allocated per level, as C allocates per level; (6) T2.1 NITs
  (bitwriter.go:94-95 comment etc.).
  *T2.3 as built (reviewed APPROVE WITH FIXES, opus; byte-identical on ~2.6 GB of reviewer inputs + the full corpus):*
  level 1 = `ht_matchfinder.go`, `compress_fastest.go`, `matchfinder_common.go`; `os outputBitstream` now lives in
  `compressorState` (no per-call allocation), `pf *fastestState` per-level pointer; ldref gained `compress-batch`,
  `dump-htmf`; corpus gained dense4, ht-collide/3|8, rebase-align, dist-32768 (+ long rebase-align-all). Cleanup pass:
  stale comments in compress.go (ErrNotPorted/impl/Compress say levels 1..12 or >=1 are unported; should be 2..12 then
  shrink as T2.4-T2.6 land), T2.3 ledger wording (4 survivors, 3 named), ldref compress-batch comment (one cached
  compressor re-allocated on level change), rebase-align-all comment.
* **T2.3 Level 1.** Files: `matchfinder_common.go`, `ht_matchfinder.go`, `compress_fastest.go`.
  C: `matchfinder_common.h` (`matchfinder_init/rebase`, `lz_hash`, `matchfinder_longest_match` helpers),
  `ht_matchfinder.h`, `deflate_compress_fastest` (+fast block-length constants). Tests: identity, level 1.
  *T2.4 as built (reviewed APPROVE WITH FIXES, opus; byte-identical on ~3 GB of reviewer inputs, stage dumps match C):*
  `hc_matchfinder.go`, `block_split.go`, `compress_greedy.go` (also holds adjust_max_and_nice_len and the three
  min-match-length functions); `flush_block.go` chooseLiteral/chooseMatch are wrappers over `...Stats` variants;
  ldref gained `dump-split|dump-minlen|dump-hcmf`. **Fix round (run AFTER T2.5 finishes, ldref.c is shared):**
  (1) **MINOR-1 test gap:** C's hash3 empty-slot early return (hc_matchfinder.go ~143-145) is not pinned by any test;
  add the reviewer's repro input (`0x01002e5a` + `"QJ#@!~VKZ"` + 3000 bytes of a 200-symbol alphabet + `F7 F8 F9 FA`
  + the same 13 bytes again + 3000 more bytes; reviewer test at
  `.../scratchpad/r24/t/zz_r24e_test.go`) as a crafted/golden test, and fix the ledger's 'rarely matters' note with the
  exact condition (position 0 inserted in bucket 0 of both tables; first 4 bytes hash4 == 0 but hash3 != 0; later copy
  searched with min_len <= 4 in a non-stored block); (2) NITs: `dump-hcmf` must enforce in-order positions; `dump-split`
  L/M repeat counts bounded; split-shift corpus comment (split lands at 5512, not the segment boundary); one-line
  invariant comment on skip_bytes's early return (can leave cur_pos > 32768 without a slide).
* **T2.4 Greedy 2-4.** Files: `hc_matchfinder.go`, `block_split.go`, `compress_greedy.go`.
  C: `hc_matchfinder.h`, `init_block_split_stats`, `observe_literal/match`, `merge_new_observations`,
  `do_end_block_check`, `ready_to_check_block`, `should_end_block`, `choose_max_block_end`,
  `adjust_max_and_nice_len`, `choose/calculate/recalculate_min_match_len`, `deflate_choose_literal/match`,
  `deflate_begin_sequences`, `deflate_compress_greedy`. Tests: identity levels 2-4; split decisions == `dump-split`.
  *T2.5 as built (reviewed APPROVE, opus; ~27.5K engineered look-ahead cases per level + 20K random + Silesia, 0
  mismatches; reviewer mutants 6/6 killed):* `compress_lazy.go` (shared body with a runtime `lazy2` flag, two thin
  wrappers); cases 5..9 wired. Cleanup (low priority): MINOR-1 short lazy cases trigger few parser decisions (lazy-pairs at
  levels 5/8: 6/11 lazy1 wins, 0/9 lazy2 wins; build copies from a shared history to make them denser, strengthen
  TestLazyShapes beyond the raw-data threshold of 5); MINOR-2 reword the lazy-recalc/9990 comment + ledger (the 10000
  recalc sees at most 10 text literals); NIT: ledger 'length-3 look-ahead match whatever its offset' (such a match always
  has offset < 2048), compress_lazy.go:41-44 comment (lazy2 branch is tested once per match that lost the first
  look-ahead), document why the lazyNextIsBetter guard keeps bsr32(0) from being evaluated. Phase 4 note: `longestMatch`
  is a real call at each of the three lazy call sites (C force-inlines it).
* **T2.5 Lazy 5-7, lazy2 8-9.** Files: `compress_lazy.go`. C: `deflate_compress_lazy_generic`,
  `deflate_compress_lazy`, `deflate_compress_lazy2`. Tests: identity levels 5-9.
* **T2.6 Near-optimal 10-12.** Files: `bt_matchfinder.go`, `compress_near_optimal.go`, `costs.go`.
  C: `bt_matchfinder.h`, `deflate_tally_item_list`, `deflate_choose_all_literals`, `deflate_compute_true_cost`,
  `deflate_set_costs_from_codes`, `deflate_choose_default_litlen_costs`, `deflate_default_*_cost`,
  `deflate_set_default_costs`, `deflate_adjust_cost(s)`, `deflate_set_initial_costs`,
  `deflate_find_min_cost_path`, `deflate_optimize_and_flush_block`, `deflate_near_optimal_*_stats`,
  `deflate_compress_near_optimal`, `deflate_init_offset_slot_full`. Tests: identity levels 10-12 (slow: -short
  uses small inputs only); cost tables == `dump-*`. Memory: `c->p.n` is several MB; pooled per compressor.
* Each T2.x ends with a pprof pass and a recorded baseline; no perf *gating* until Phase 4.

### Phase 3 - Containers and the stdlib-compatible API

* **T3.1 containers.** Files: `internal/deflate/{gzip.go,zlib.go}`, `internal/adler32/adler32.go`.
  C: `gzip_compress.c`, `gzip_decompress.c` (header flags, FEXTRA/FNAME/FCOMMENT/FHCRC, trailer), `zlib_*.c`,
  `adler32.c`. CRC-32 uses `hash/crc32` (stdlib, PCLMUL on amd64/arm64: not a compression implementation).
  Tests: byte-identical gzip/zlib output vs `ldref`; header edge cases; bad CRC/ISIZE.
* **T3.2 whole-buffer public API.** Files: `flate/buffer.go`, `gzip/buffer.go`, `zlib/buffer.go`
  (`Compress`, `Decompress`, `CompressBound`, levels 0..12). Tests: identity through the public API.
* **T3.3 streaming Writer** (D1): `flate/writer.go`, `gzip/writer.go`, `zlib/writer.go`; `NewWriter`,
  `NewWriterLevel`, `NewWriterDict`, `Write/Flush/Close/Reset`, `Header` fields, constants. Compressor gets
  chunk-continuation (carry matchfinder window across chunks; sync flush). Tests: roundtrip vs stdlib reader,
  `total<=Chunk` identity, Flush semantics, Reset reuse.
* **T3.4 streaming Reader** (D2, after its design note): `flate/reader.go`, `gzip/reader.go`, `zlib/reader.go`;
  `NewReader`, `NewReaderDict`, `Reset`, `Multistream`, `Header`, `io.ByteReader`-consistent read-ahead
  (leftover bytes after a member stay unread like stdlib does). Tests: differential fuzz vs stdlib, 1-byte reads,
  truncated/corrupt streams -> same error classes (`gzip.ErrChecksum/ErrHeader`, `flate.CorruptInputError`).
* **T3.5 API parity + drop-in test.** `api_parity_test.go` (reflect vs stdlib), `scripts/apicheck.sh` extended
  only if it can be done additively.

### Phase 4 - Assembly (driven by the T1.4/T2.x profiles; candidate list, order set by data)

* amd64 BMI2 decode fast loop (`deflate_decompress_bmi2` equivalent), match-copy in decoder; arm64 decode loop.
* `hc_matchfinder` longest-match + insertion, `ht_matchfinder`, `bt_matchfinder_advance_one_byte`,
  `matchfinder_init/rebase` (SSE2/AVX2), sequence/bit-packing loop in `deflate_write_sequences`,
  adler32 (AVX2 / NEON).
* Per asm unit: files `*_amd64.{go,s}`; `cpu.RegisterAsm("deflate.<name>")`; `BOUNDS:` block derived from C's
  checks; `FuzzXxxAsmEquiv` with guardmem; entry in the asm-equivalence test; `fuzzasm_test.go`; Go fallback kept.
  Each unit is merged only with a before/after profile and the per-level % table. arm64 can only be validated on the
  CI arm runner (no arm machine locally): arm64 asm comes last and is optional against the gate.

### Phase 5 - CI and integration

* **T5.1 CI**: `.github/workflows/ci.yml` (cache + fetch libdeflate `LIBDEFLATE_TAG: v1.26`, build `ldref`; add
  `./internal/deflate ./flate ./gzip ./zlib` to test, noasm, 386-short, race; vet/gofmt already global),
  `.github/workflows/fuzz.yml` (+ldref step), `scripts/fuzz.sh` (new packages need `fuzzasm_test.go`; cap logic
  unchanged), `scripts/decodercheck.sh` (D5).
* **T5.2 coverage gate**: in the `fuzz-coverage` job, same awk gate (>= 95%) over `internal/deflate/{decompress,decode_table}*.go`
  and the compressor core (`compress*.go`, `huffman.go`, `*matchfinder.go`) files.
* **T5.3 integration check** (D7) + docs (`flate/doc.go`, README section, `NOTICE`).

## 6. Risks (called out so they aren't a surprise later)

1. **Perf parity in Go** (D3): biggest risk; mitigated by asm in Phase 4, but the 100%-per-level geomean on
   near-optimal levels 10-12 (cache-bound, `bt_matchfinder`) is uncertain until measured.
2. **Resumable decoder** (D2) has no C reference; correctness rests on differential fuzzing.
3. **Streaming writer vs identity** (D1): chunking changes block boundaries; identity is promised only for the
   whole-buffer API and the single-chunk case.
4. **Byte identity is brittle**: one off-by-one in a heuristic changes output. Stage-level dumps from `ldref` are
   there to localise it quickly; expect most Phase 2 effort here.
5. **Local machine is amd64 only**: arm64 paths are only exercised on CI.
6. Shared machine: tests/benches are serialised with the zstd session by the memory protocol above.
