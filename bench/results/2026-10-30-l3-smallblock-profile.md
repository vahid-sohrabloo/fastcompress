# L3 and small-block gap: C vs Go profile (Task 20c, step 1)

Build 80c3dea (Task 20b). Harnesses, both doing what `zstd -b` / gobench do
(one reused context, one `ZSTD_compress2` / `EncodeAll` per independent
block), with a fixed iteration count so `perf stat` counts can be normalised
per input byte:

- C: `charn LEVEL BLOCK ITERS FILE`, libzstd v1.5.7 from ref/zstd/lib built
  with `gcc -O3` (the library's default flags).
- Go: `goharn LEVEL BLOCK ITERS FILE` (`EncodeAll`, CRC off).

`perf` 6.8 (`/usr/lib/linux-tools/6.8.0-147-generic/perf`), `taskset -c 2`,
`cpu_core/` events, user mode. Counts are per input byte; misses per KiB of
input. Both outputs were checked to be the same size (and the Go output is
byte-identical to C by the test suite).

## 1. Counter comparison (L3)

| config | side | cycles/B | instr/B | branches/B | br-miss/kB | L1D miss/kB | LLC miss/kB | IPC |
|---|---|---|---|---|---|---|---|---|
| tar -B128K | C | 10.82 | 30.86 | 2.73 | 100.7 | 735 | 0.10 | 2.85 |
| tar -B128K | Go | 15.55 | 31.03 | 4.49 | **251.2** | 667 | 0.27 | 2.00 |
| tar -B16K | C | 12.64 | 38.48 | 3.68 | 115.5 | 602 | 0.06 | 3.04 |
| tar -B16K | Go | 17.47 | 41.21 | 6.12 | **246.9** | 548 | 0.12 | 2.36 |
| tar -B1M | C | 12.44 | 30.24 | 2.70 | 97.2 | 805 | 9.67 | 2.43 |
| tar -B1M | Go | 15.24 | 32.14 | 4.80 | 165.5 | 769 | 5.89 | 2.11 |
| xml whole | C | 6.23 | 14.51 | 1.35 | 61.5 | 200 | 4.87 | 2.33 |
| xml whole | Go | 7.29 | 15.23 | 2.21 | 77.5 | 180 | 2.90 | 2.09 |
| nci whole | C | 4.85 | 11.46 | 1.07 | 44.1 | 114 | 10.06 | 2.37 |
| nci whole | Go | 5.58 | 12.15 | 1.78 | 54.0 | 102 | 5.13 | 2.18 |

(cycles include reading the file once; the same for both sides.)

What this says:

- **Instructions are within 1-7% of C everywhere.** The gap is not work done.
- **Cache misses are not it.** Go has fewer L1D and LLC misses than C in
  every row.
- **Branch misses are.** At -B128K Go mispredicts 2.5x as often as C
  (+150 per KiB). At ~20 cycles each that is ~3 cycles/B of the 4.7 cycles/B
  gap. At -B16K: +131/KiB (~2.6 of 4.8 cycles/B). On whole files the excess
  is smaller (xml +16/KiB, nci +10/KiB), as is the gap.
- Go also executes 1.6-1.7x C's branches: gcc turns C's selects and the
  inlined ZSTD_count into CMOVs and straight-line code.

## 2. Where the misses are

`perf record -e cpu_core/br_misp_retired.all_branches/upp` (precise IP),
Go L3, share of compressBlockDoubleFastAsm's mispredicts:

| instruction (dfast asm) | -B4M | -B128K | -B16K |
|---|---|---|---|
| `CMPL idxl0, prefixLowestIndex; JB nolong` | 12% | 38% | 25% |
| `CMPL idxs0, prefixLowestIndex; JB noshort` | 8% | 31% | 45% |
| the two data compares (long, short) | 54% | 18% | 11% |

The dfast asm itself holds 79-88% of all mispredicts.

**Root cause:** the validity test of the hash-table candidates. A context
reused for a new frame keeps its tables and continues the indices
(ZSTDirp_continue); old entries are just below the new window's lowLimit.
So in the first part of every small frame a candidate is valid or stale at
random, and the `JB` on `idx >= prefixLowestIndex` mispredicts. With a
whole file, nearly every candidate is valid and the branch is predictable,
which is why the gap is small there and large on 128K/16K blocks.

C avoids the branch. `zstd_double_fast.c` loads through
`ZSTD_selectAddr(idx, prefixLowestIndex, match, dummy)`, which is inline
asm `cmp; cmova` choosing a 10-byte `dummy[]` on the stack for a stale index,
and checks `match_safe == match` only after the data compare succeeds. The
compare is then the only data-dependent branch. This is why C gets *faster*
on small blocks (L3: 296 MB/s at 4M, 362 MB/s at 128K, smaller tables in
cache) while Go got slower (263 -> 243).

The L1 fast loop has the same pattern: on tar -B128K the two `JB miss`
validity branches take 27% of compressBlockFastAsm's mispredicts, and Go has
118 vs C's 77 misses/KiB (C's zstd_fast.c uses `mval = idx >= prefixStartIndex
? read32(match) : read32(ip0) ^ 1`, which gcc compiles to a CMOV).

## 3. Per-block work: the same as C

Call counts with gdb breakpoints (`ignore` counts), 32 blocks of 16 KiB
(tar bytes 512K-1M), L3:

| C function | calls | Go function | calls |
|---|---|---|---|
| ZSTD_resetCCtx_internal | 32 | (*cctx).resetCCtxInternal | 32 |
| ZSTD_reset_matchState | 32 | resetMatchState | 32 |
| ZSTD_compressBlock_internal | 32 | (*cctx).compressBlockInternal | 32 |
| ZSTD_buildSeqStore | 32 | (*cctx).buildSeqStore | 32 |
| ZSTD_compressBlock_doubleFast | 32 | compressBlockDoubleFastNoDict | 32 |
| FSE_buildCTable_wksp | 128 | fse.BuildCTable | 128 |
| FSE_normalizeCount / writeNCount | 128 / 128 | fse.NormalizeCount / WriteNCount | 128 / 128 |
| HUF_buildCTable_wksp / writeCTable | 32 / 32 | huf.buildCTable / writeCTable | 32 / 32 |
| ZSTD_compressLiterals | 32 | compressLiterals | 32 |
| ZSTD_reduceIndex | 0 | reduceIndex | 0 |

- No table clearing per frame on either side (indices continue; Go's
  `runtime.memclrNoHeapPointers` ran 41 times in total, all at first use).
- No overflow correction in these runs.
- `runtime.mallocgc` runs ~10 times per EncodeAll, but runtime.* is under
  0.2% of cycles.
- Asm entry/exit: compressBlockDoubleFastNoDict (the Go wrapper, its ABI0
  argument spill included) is 0.13% of cycles at -B16K.

Hypotheses killed: per-block fixed costs C avoids, table clearing or index
reduction, asm entry/exit overhead, cache misses from table sizing.

## 4. Per-function cycles at -B16K (L3), C vs Go

`perf record -e cpu_core/cycles/u`, sample periods summed per symbol; Go
total = 1.17x C:

| work | C | Go | Go - C |
|---|---|---|---|
| dfast loop (+ inlined count and literal copy; Go: + fastCount + fastCopyLits) | 344 | 388 | +44 |
| FSE_buildCTable_wksp / fse.BuildCTable | 14.8 | 32.8 | +18 |
| HUF_buildCTable_wksp / huf.buildTree+sortSymbols+buildCTableFromTree+setMaxHeight+buildCTable | 18.8 | 29.2 | +10 |
| FSE_compress_usingCTable / fse.compressUsingCTableGeneric | 1.8 | 6.4 | +4.6 |
| HIST_count* / hist.* | 34 | 38 | +4 |
| ZSTD_seqToCodes / seqToCodes | 10.4 | 14.2 | +3.8 |
| encodeSequences | 53.8 | 48.0 | -5.8 |

fse.BuildCTable is 2.2x C. Annotated, its hot spot is the "build table"
loop, which packs u16 states into the []uint32 CTable with a branch on
`i&1` (unpredictable) and a read-modify-write, where C stores the u16
directly.

## 5. Other levels on small blocks (same counters)

| config | side | cycles/B | instr/B | br-miss/kB |
|---|---|---|---|---|
| L1 tar -B128K | C / Go | 7.60 / 8.97 | 23.80 / 23.90 | 76.9 / 117.7 |
| L1 tar -B16K | C / Go | 9.62 / 11.95 | 29.72 / 31.63 | 90.8 / 152.7 |
| L7 tar -B16K | C / Go | 54.5 / 93.8 | **124.0 / 422.8** | 685 / 719 |
| L11 tar -B16K | C / Go | 166 / 236 | **406 / 857** | 2695 / 2744 |
| L11 tar -B128K | C / Go | 138 / 161 | 234 / 371 | 2336 / 2360 |

- L1: same story as L3 (validity branches in the fast asm).
- L7 at 16K is a different finder: windowLog 14 disables the row match
  finder (ZSTD_resolveRowMatchFinderMode needs windowLog > 14), so C runs
  ZSTD_HcFindBestMatch (hash chain), and Go its pure-Go hash-chain port.
  Go executes 3.4x C's instructions there: hcFindBestMatch 59%,
  insertAndFindFirstIndexInternal 25%, and hashPtr 13% as a separate,
  not inlined, call.
- L11 at 16K is btopt (pure Go), 2.1x C's instructions; at 128K btlazy2,
  1.6x.

## Targets, in order of measured size

1. dfast (L3): make the two candidate validity tests branchless, as C does.
2. fast (L1): the same.
3. fse.BuildCTable: drop the `i&1` branch in the u16 state packing (every
   level, matters on small blocks).
4. Hash-chain finder (L7 on <= 16K inputs): the pure-Go path's call and
   instruction overhead.
