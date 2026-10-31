# Task 20f: profiles and A/Bs

Harnesses as in Task 20c-20e: `charn` (C, libzstd 1.5.7 `gcc -O3`) and
`goharn` (EncodeAll, CRC off), one reused context, one call per block,
`perf stat`/`perf record` with `cpu_core/` user-mode events, `taskset -c
2`, sample periods of 0.5-3M cycles (under `perf_event_max_sample_rate`
1500, so not throttled). A/B figures are medians of per-round paired cycle
ratios (`ab.sh`, interquartile range in brackets). tar4m = the first 4 MiB
of silesia.tar. Every variant's output was checked identical (sha256)
against the previous binary before its A/B.

## Step 1: btopt (L11 -B16K)

Cycle profile before (5aad89e), tar4m -B16K, 6 passes, samples of 3M
cycles:

| Go | samples | C | samples |
|---|---|---|---|
| btGetAllNoDictAsm | 739 | ZSTD_btGetAllMatches_noDict_4 | 740 |
| insertBt1Asm | 201 | ZSTD_insertBt1 | 256 |
| insertBt1 + insertBt1Loop + updateTreeInternal + btGetAllMatches + btGetAllNoDict (Go wrappers) | 232 | (inlined) | - |
| compressBlockOptGeneric + llIncPrice + litLengthPrice + updateRep | 753 | ZSTD_compressBlock_opt0 | 687 |
| updateStats | 21 | ZSTD_updateStats | 22 |

The assembly finders are faster than C's (940 vs 996); the Go wrappers
around them (232) and the Go parser (+66 over C) are the gap.

- Piece 1 (btGetAllNoDictAsm also does the skip check and the tree
  update, so the parser calls one assembly function per position):
  5 rounds, -4.0% [-4.1..-3.9] cycles, -11% instructions (601 vs 667
  instr/B). Go 230.9 cycles/B against C's 211.5.
- Piece 2 (optParseAsm: the match loop up to each segment to store, in
  assembly; the sequences of each segment are stored by Go): 5 rounds,
  -13.9% [-14.0..-13.8] vs piece 1. Go 198.8 cycles/B and 403.5 instr/B,
  C 210.7 and 525.7: Go is now 6% faster than C on this row.

Profile after piece 2 (same run): btGetAllNoDictAsm 1047 (finder and
tree update; C's two functions 996), optParseAsm 479 (C's opt0 687),
optParseFast (Go store loop) 32, updateStats 28.

Both pieces passed TestAsmEquiv (four passes, with the corpus) before the
next step. mls 3 (minMatch 3, levels whose btopt tables use it) stays on
the Go loop: no benchmark row reaches it (L11 -B16K is btopt with
minMatch 4).

## Step 2: L1/L3

Branch profile, L1 dickens (br_inst_retired.all_branches, period 2M, not
throttled), samples: Go fast loop 176, C 144; encodeSequences 27 vs 9
(its cycles are below C's); seqToCodes 8 vs 10. With the misses already
falling through (20e), the loop's remaining extra branches are the
candidate bound checks (idx < current0; C has none). L3 dickens: Go's
double-fast loop now runs fewer branches than C's (546 vs 607 samples).

Cycle profile, L3 tar4m -B16K (1M-cycle samples): double-fast loop 689 +
fastCount 64 against C's 709 (C inlines ZSTD_count).

| change | L1 dickens | L1 -B16K | L3 dickens | L3 -B16K | L3 -B128K | L3 samba |
|---|---|---|---|---|---|---|
| COUNTI: first 8 bytes of a match compared inline, fastCount only past them (kept) | -1.8% [-2.0..-1.8] | -1.1% [-1.3..-0.7] | -0.6% [-0.7..-0.6] | -0.4% [-3.6..-0.3] | -0.4% [-0.6..-0.2] | +0.2% [-0.2..+0.3] |
| double-fast: idxl1 and idxs0 kept in registers (nextStep moved to the frame) (dropped: noise) | - | - | +0.8% [+0.2..+0.8] | -0.3% [-1.0..+0.1] | -0.6% [-0.8..-0.5] | +0.2% [-0.2..+0.7] |

All paired perf-stat, 5 rounds, quiet machine (load < 1.5).

## Step 3: entropy stage (small blocks)

L1 tar4m -B16K, 1M-cycle samples, Go vs C: HIST 56 (countStripesAsm 40
+ countParallel 16) vs 31; FSE_buildCTable 30 vs 22; HUF table build
(sortSymbols, buildTree, buildCTableFromTree, simpleQuickSort) 20 vs 15;
FSE_compress_usingCTable 8 vs 2; seqToCodes 22 vs 17. At L3 -B16K
FSE_buildCTable is 47 vs 46 and HIST 51 vs 52.

- HIST (kept): the counting loop now reads the second byte of each pair
  from AH/BH as gcc does (one shift per two bytes, not per byte), and the
  four tables are merged with the maximum taken 4 lanes at a time (SSE2),
  as gcc vectorizes C's loop. Paired, vs the previous commit: L1 dickens
  -0.9%, L1 -B16K -1.2%, L3 dickens -1.1%.
- FSE_buildCTable and the HUF build: equal to C at L3; at L1 a few
  samples each (under 1%), with no single hot spot in the annotation. Not
  changed.

## Where each level stands (charn vs goharn, quiet machine, 3 rounds)

| row | Go cycles/B | C cycles/B | Go vs C |
|---|---|---|---|
| L1 dickens | 11.12 | 10.69 | C 3.9% faster |
| L3 dickens | 19.42 | 18.92 | C 2.7% faster |
| L7 dickens | 58.07 | 56.10 | C 3.4% faster |
| L11 dickens | 162.9 | 150.2 | C 8.2% faster |
| L1 tar4m -B16K | 15.04 | 14.15 | C 5.9% faster |
| L3 tar4m -B16K | 21.46 | 20.42 | C 4.7% faster |
| L7 tar4m -B16K | 78.67 | 78.43 | C 0.3% faster |
| L11 tar4m -B16K | 198.7 | 211.0 | Go 6.2% faster |

L11 dickens (row lazy2, rowLog 6): rowSearch56 + rowUpd56 + lazyRow56 511
samples against C's ZSTD_RowFindBestMatch + lazy2_row 481 (+6%). Go runs
fewer instructions (450 vs 478 instr/B); L1D, L2, L3 and dTLB misses and
branch misses are equal (dTLB walks 38.6 vs 37.6 cycles/B, a quarter of
the time on both sides). Both spend the most cycles on the same two
loads: the row entry in the gather loop (22% of the function on both) and
the candidate's first bytes (16% vs 14%). The prefetches match C's
(ZSTD_row_prefetch: two lines of the hash row and of the tag row at
rowLog 6). I found no structural difference to remove.
