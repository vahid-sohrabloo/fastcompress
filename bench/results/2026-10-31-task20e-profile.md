# Task 20e: profiles and A/Bs

Harnesses as in Task 20c/20d (2026-10-30-l3-smallblock-profile.md): `charn`
(C, libzstd 1.5.7 `gcc -O3`) and `goharn` (EncodeAll, CRC off; `HUGE=1`
turns on WithHugePages), one reused context, one call per block, `perf stat`
`cpu_core/` user-mode counts per input byte, `taskset -c 2`. A/B figures are
medians of per-round paired cycle ratios (`ab.sh`, 5 rounds, interquartile
range in brackets). Background load (clickhouse, chrome) was 2-4 during
the A/Bs.

## Step 1: opt-in huge pages (WithHugePages)

Tables of 1 MiB or more (L7 whole files: 4 MiB hash + 1 MiB tags; L11:
16 MiB + 4 MiB) each get a 2 MiB-aligned anonymous mapping advised with
MADV_HUGEPAGE before first touch.

This machine's THP mode is `madvise` (defrag `madvise`), but memory is
fragmented (23.5 of 31.7 GiB used, 15 GiB swapped, 41 free order-9 blocks
in /proc/buddyinfo) and compaction needs root, so the kernel backed only
part of the tables with huge pages: during an L11 dickens run, smaps showed
6 MiB AnonHugePages of the 16 MiB hash mapping and none of the tag
mapping. The numbers below are therefore a lower bound of the gain.

L11 dickens, 3 passes: dtlb_load_misses.walk_active 1.20G -> 0.36G and
1.27G -> 0.38G cycles (off -> on, two pairs).

Paired A/B, off vs on (5 rounds), Go cycles/B:

| config | off | on | change |
|---|---|---|---|
| L11 dickens | 170.0 | 171.5 | -0.7% [-2.0..+1.0] |
| L7 dickens | 60.37 | 59.09 | **-2.0%** [-3.5..-1.7] |
| L11 mozilla | 85.29 | 81.71 | **-3.6%** [-4.2..-3.2] |
| L7 mozilla | 37.51 | 37.05 | **-1.8%** [-2.0..-1.2] |

Instructions are unchanged (output is identical; TestHugePagesOutput).
