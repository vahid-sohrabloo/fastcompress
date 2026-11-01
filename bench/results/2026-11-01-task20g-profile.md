# Task 20g: top-down, profiles and A/Bs

Harnesses as in Tasks 20c-20f: `charn` (C, libzstd 1.5.7 `gcc -O3`) and
`goharn` (EncodeAll, CRC off), one reused context, `taskset -c 2`,
user-mode `cpu_core/` events. Cycle profiles use periods of 1-4M cycles
(not throttled under `perf_event_max_sample_rate` 1500). A/B figures are
medians of per-round paired cycle ratios (`ab.sh`, interquartile range in
brackets). tar4m = the first 4 MiB of silesia.tar. Every variant's output
was checked identical (sha256 of every frame) before its A/B. The machine
was not idle (load 1.3-4, another agent's builds and the ClickHouse
server), so single runs vary by 1-3%; only paired medians are quoted.

## Step 1: top-down, C vs Go (18a1e7b)

`perf stat -M TopdownL1,TopdownL2 --cputype core`, converted to cycles
per input byte (the metric's share of slots times cycles/B):

| row | side | cycles/B | frontend (latency / bandwidth) | bad spec. | backend (memory / core) | retiring |
|---|---|---|---|---|---|---|
| L1 dickens | C | 10.69 | 0.83 (0.34 / 0.50) | 3.61 | 0.88 (0.17 / 0.71) | 5.37 |
| | Go | 11.08 | 1.27 (0.42 / 0.86) | 3.80 | 1.03 (0.31 / 0.72) | 4.98 |
| L3 dickens | C | 19.64 | 1.26 (0.57 / 0.69) | 7.01 | 4.81 (2.22 / 2.59) | 6.58 |
| | Go | 19.37 | 1.47 (0.48 / 0.99) | 6.47 | 5.39 (2.21 / 3.18) | 6.06 |
| L3 tar4m -B16K | C | 20.39 | 2.30 (0.96 / 1.35) | 7.14 | 1.51 (0.24 / 1.26) | 9.44 |
| | Go | 21.08 | 2.26 (0.78 / 1.48) | 7.25 | 2.32 (0.51 / 1.81) | 9.25 |
| L11 dickens | C | 147.2 | 9.86 (5.30 / 4.56) | 30.77 | 39.45 (12.66 / 26.94) | 66.98 |
| | Go | 157.5 | 16.06 (6.30 / 9.76) | 27.40 | 54.79 (16.06 / 38.73) | 59.04 |

(The L1/L3 Go rows were taken after the FSE commit; L3 dickens happened
to run faster than C in that single run, its paired median is +0.6..3%.)
L7 dickens (percent of slots): frontend 18.6 vs C 10.9 (fetch bandwidth
12.0 vs 5.4), backend 12.9 vs 10.1, retiring 40.1 vs 47.9.

- **L1**: front end (fetch bandwidth +0.36 cycles/B, the whole gap), with
  equal mispredicts (0.102/B both) and fewer instructions (32.1 vs 33.7/B)
  but 4.65 vs 3.33 branches/B. DSB coverage is not the issue
  (idq.mite_uops 0.34 vs 0.19/B, dsb_miss 0.03 vs 0.01/B).
- **L3**: core bound (+0.6 cycles/B on dickens, +0.55 on -B16K) and fetch
  bandwidth (+0.3).
- **L7/L11 (row finder)**: memory and core bound plus fetch bandwidth.
  The memory side differs from C in a way C's instruction stream does not
  explain (next section).

### L11 dickens: memory counters per input byte (C / Go)

| counter | C | Go | Go, huge pages |
|---|---|---|---|
| l1d_pend_miss.fb_full (cycles) | 24.1 | 36.8 | 32.6 |
| l1d.hwpf_miss | 0.61 | 2.37 | 2.43 |
| l1d.replacement | 15.5 | 17.2 | 16.1 |
| l2_lines_in.all | 20.3 | 24.4 | - |
| l2_rqsts.all_hwpf | 20.0 | 25.0 | - |
| sw_prefetch_access.t0 | 16.58 | 16.56 | - |
| mem_load_retired.l1_miss (demand) | 1.55 | 1.44 | - |
| dtlb_load_misses.walk_completed | 1.451 | 1.467 | - |
| dtlb_load_misses.walk_active (cycles) | 35.8 | 40.8 | 11.1 |

Same work (precise branch samples: gather-loop and compare-loop trip
counts within 2.5% of C's; equal software prefetches; fewer demand
misses), but the hardware prefetchers fetch 4 more lines per byte into L2
(+20%) and 1.8 more into L1, the fill buffers are full 50% more often,
and page walks take 14% longer. L7 dickens shows the same: l2_lines_in
5.83 vs 3.79/B, l1d.hwpf_miss 1.01 vs 0.30. L1 and L3 do not
(l2_lines_in 0.024 vs 0.023, 0.527 vs 0.529). L2 fills sampled by
region (1M period): gather loop 219 vs C 150, row update 67 vs 52,
compare loop 167 vs 166.

Causes ruled out on L11 dickens (hwpf / L2 lines per byte unchanged, or
cycles not better):
- AVX2 count off (useAVX2 forced false): hwpf 2.38.
- Table placement: tables in mmap'd 4 KiB pages (WithHugePages' mapping
  without the madvise): +2.5% cycles; tables at C's page offsets
  (hash 0xf00, tag 0xf80, from gdb on charn): hwpf 2.35, l2 lines 24.1.
- Input 16 bytes past a page (as glibc's malloc returns it): no change.
- Go runtime: 297 context switches per run (sysmon) vs C's 6; a process
  waking every 2.9 ms on CPU 2 next to charn changes nothing; no GC in
  the timed loop.
- Removing the candidate, row-update or next-hash prefetches (diagnostic
  only): hwpf rises to 2.6-3.0.
- Removing the gather loop's idx < curr check: hwpf 2.28, cycles equal.
- Gather loop in C's instruction order: +0.6% [+0.4..+1.5].
- rowUpd inlined into rowSearch (as C inlines it), with and without
  PCALIGN: +1.7% / +1.5% (noise level).
- Gather loop at 0/16/32/48 mod 64: L11 dickens +0.2/+0.4/+2.0/+0.6%,
  L7 dickens -0.5/+0.1/+1.5/+1.2% vs the current layout. Not changed.

Huge pages remove the walk cycles (41 -> 11/B) and bring L11 dickens to
C's cycles (148.9 vs 149.8), with the extra prefetch traffic unchanged.

## Step 2: transparent huge pages (measurement only)

- `/sys/kernel/mm/transparent_hugepage/enabled`: `always [madvise] never`;
  `defrag`: `[madvise]`.
- C, `zstd -b11 -i6 -T1 dickens`, 3 s in: AnonHugePages 0 kB in
  smaps_rollup; the 50 MB workspace mapping is `THPeligible: 0` (glibc
  does not madvise it, and THP is madvise-only).
- gobench -level 11 dickens: AnonHugePages 0 kB; with -hugepages: 16384 kB.

So C does not get huge pages by default on this machine; neither does Go.
WithHugePages remains a Go-only opt-in.

## Kept changes

| commit | change | measured (paired, cycles/B) |
|---|---|---|
| 06d193f | hist: 8-byte-aligned workspace (REP STOSQ was misaligned) | Count 2 KiB 773 -> 636 ns; L1 -B16K -0.9%, L3 -B16K -0.7% |
| af6a8ba | FSE_buildCTable: states straight into the table (u16 view), spread loop unrolled as in C | BuildCTable 1378 -> 940 ns; L3 -B16K -1.4%, L1 dickens -0.7% |
| 666ef01 | presplit: merge/zero only the 1 << hashLog entries in use; hashLog-specialised addEvents; branch-free fpDistance | L3 nci -1.4, xml -1.6, dickens -0.9; L7 samba -2.0; L11 samba -1.0 |
| e7e068d | fast/double-fast loops pinned 16 bytes past a 64-byte line | L1 -B16K -0.9, L3 samba -1.2, L3 -B16K -1.4; L1 dickens +0.5 |
| 7987121 | fast/double-fast: C's PREFETCH_L1(ip1 + 64/128) when the step grows | L1 sao -0.7, L3 x-ray -0.9, L1 dickens -0.6 |

## Dropped experiments

- L1, branch-free candidate check (step 3): a CMOV clamp of matchIdx to
  current0 (memory-safe: an impossible index reads ip0, and the hit paths
  reject matchIdx == current0) instead of the `idx < current0` branch:
  L1 dickens -0.3% [-1.1..-0.2], samba -0.0%, xml -0.1%, mozilla -0.5%,
  tar4m -B16K +1.9% [+1.4..+2.1]. The CMOV puts the candidate load behind
  the compare (the branch was predicted and speculated past). Dropped.
  Removing the check outright (unsafe, diagnostic only): L1 dickens
  +0.6%, -B16K -1.7%; L3 -0.3% / +0.4%: the branch is not the gap.
- L1: current0 stored to the frame only on match paths (one store less
  per position): +0.3% [-0.4..+0.7] dickens, -0.3% -B16K; with SHRX for
  the hash shift as well: -0.0% / -0.8%. Noise; dropped.
- L3: forcing the branchy (non-Stale) double-fast loop: dickens +0.3%,
  samba +1.8%, xml +5.1%, mozilla +1.0%: the Stale choice stands.
- HUF node table 8-byte aligned (its REP STOSQ was misaligned too): L1/L3
  -B16K -0.1% / +0.0%. Dropped.
- FSE_buildCTable: the remaining time is the `tableU16[cumul[s]++]`
  store-forwarding chain, which C has too.

## Where each level stands (bench/results/2026-11-01-asm-20g.md)

| level | % of C, all rows | huge pages | measured reason for the rest (vs C) |
|---|---|---|---|
| L1 | 99.2 (whole files 99.7) | - | front end: fetch-bandwidth-bound 0.86 vs 0.50 cycles/B on dickens, with 4.65 vs 3.33 branches/B (the candidate `idx < current0` checks; dropping them, unsafely, did not help) and equal mispredicts; -B16K 94.3: per-frame entropy setup (HIST, FSE/HUF builds) |
| L3 | 97.4 | - | core-bound +0.55-0.6 and fetch bandwidth +0.3 cycles/B in the double-fast loop (the Stale variant: two more uops per candidate than C's CMOV select, one of them the `idx < curr` check) |
| L7 | 100.4 | 102.0 | above C; dickens 97.5, mr 97.1, ooffice 97.2: row finder, as L11 |
| L11 | 97.6 (whole files 97.1) | 100.7 | row finder memory traffic: on dickens +20% L2 line fills and 3.9x L1 hardware prefetches vs C for the same work, fill buffers full 36.8 vs 24.1 cycles/B, page walks 40.8 vs 35.8 cycles/B; cause not found (see Step 1) |
