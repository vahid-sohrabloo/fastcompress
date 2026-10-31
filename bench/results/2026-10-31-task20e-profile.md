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

## Step 4: review minors (cost check)

matchState.asmCalls (a counter per successful lazyHcRun /
btGetAllNoDict / dubtFindBestMatchFast call) and btGetAllNoDict's check
of the matches table, paired A/B vs f812a38 (5 rounds, tar4m = first
4 MiB of silesia.tar): L11 -B16K +0.1% [-0.2..+0.8], L11 -B128K -0.3%
[-0.8..-0.1], L7 -B16K -0.2% [-0.3..+0.2]. Kept (noise).

## Step 3b: btlazy2 (L11 at 128 KiB blocks)

Before (2337128), tar32m = first 32 MiB of silesia.tar, -B128K:

| side | cycles/B | instr/B | dtlb walk cyc/B | l1d pend cyc/B | br-miss/B |
|---|---|---|---|---|---|
| C | 144.65 | 250.3 | 0.00 | 14.40 | 2.36 |
| Go | 152.39 | 288.7 | 0.02 | 14.32 | 2.38 |

Misses equal C's: the gap is instructions. Instructions profile
(`perf record -e instructions`), in instr/B: C ZSTD_DUBT_findBestMatch 212
+ ZSTD_BtFindBestMatch 16.6, ZSTD_compressBlock_btlazy2 10.6; Go finder
assembly (dubtFindBestMatchAsm, insertDUBT1Asm, dubtSearchAsm) 236.6,
compressBlockLazyGeneric 30.5, the Go wrappers (dubtFindBestMatchFast,
btFindBestMatch, searchMax) 12.7. The lazy parser and its wrappers ran
43 instr/B against C's 10.6.

Fix: the lazy parser in assembly with the binary-tree finder (lazyBt,
generated from the hc/row lazy template by gen_row_{amd64,arm64}.go into
bt_lazy_*.s), whose finder btSearch checks the skipped area and calls
dubtFindBestMatchAsm.

| config | before | after | change | C |
|---|---|---|---|---|
| L11 tar4m -B128K cycles/B | 232.46 | 224.20 | **-3.8%** [-4.1..-3.5] | 226.9 (20d) |
| L11 tar4m -B128K instr/B | 375.2 | 313.3 | | |
| L11 tar32m -B128K cycles/B | 152.60 | 145.91 | **-4.4%** [-4.4..-4.4] | 144.65 |
| L11 tar32m -B128K instr/B | 288.7 | 234.4 | | 250.3 |

Output identical (sha256 of L11 -B128K, L13 whole, L15 -B64K on tar4m).

## Step 2: L1/L3 whole files and small blocks

perf note: kernel.perf_event_max_sample_rate is 1500 here, so `perf
record` with a short period (or -F 4000) silently drops samples; the
per-symbol shares below use a period of 3M cycles (under the limit) and
40 passes.

L1 dickens whole (40 passes), cycles in millions, C / Go: total 4398 /
4749 (+8%); fast loop (asm + fastCount + fastCopyLits + stale loop) 3225 /
3384 (+159); **encodeSequences 519 / 504** (Go is not slower: the "27%"
of Task 20d does not reproduce with an unthrottled profile); seqToCodes
102 / 171 (+69); HUF_compress1X 195 / 231 (+36); FSE_buildCTable 30 / 66
(+36); HIST 282 / 303 (+21).

L1 -B16K (tar32m, 10 passes, -F 4000 so shares only): fast loop +128M,
HIST +68M, FSE_buildCTable +62M, HUF table build +53M, seqToCodes +47M,
FSE_compress_usingCTable +30M, HUF_compress1X -44M and encodeSequences
-48M (Go faster) of a +362M gap.

seqToCodes: gcc's loop is 19 instructions per sequence with the same two
branches (ll > 63, ml > 127); Go's is 30 (no bounds checks, but the
highbit32 LEA/BSR form, address recomputation and jumps). An amd64
assembly loop of gcc's shape (seqCodesAsm):

| config | before cyc/B | after | change (7 or 5 rounds) |
|---|---|---|---|
| L1 dickens | 11.71 | 11.62 | -0.7% [-1.0..-0.4] (5 rounds: -1.1%) |
| L1 mozilla | 7.90 | 7.90 | -0.2% [-0.2..+0.6] |
| L1 xml | 5.05 | 5.02 | -0.6% [-0.8..-0.1] |
| L3 dickens | 20.17 | 20.25 | +0.2% [-0.7..+0.9] |
| L3 mozilla | 13.15 | 13.15 | +0.0% [-0.4..+1.0] |
| L3 xml | 6.87 | 6.83 | -0.6% [-1.0..-0.5] |
| L1 tar4m -B16K | 15.75 | 15.68 | -0.4% [-0.4..+0.0] |
| L3 tar4m -B16K | 22.43 | 22.37 | -0.7% [-1.1..-0.1] |

Instructions -2 to -3% everywhere; kept (no whole-file regression beyond
noise; load average 3-4 during these runs).

L1 dickens branches (perf stat, 5 passes): C 3.33/B, Go 4.87/B; misses
0.102 vs 0.109/B. Branch profile (`br_inst_retired.all_branches/upp`,
proportions): fast loop and its helpers C 2.87/B, Go 3.75/B;
encodeSequences C 0.15/B, Go 0.53/B (but equal cycles); HUF_compress1X
C 0.03/B, Go 0.16/B. In the fast loop's search the misses (the common
case) jumped: per two positions the repcode test, both candidate tests,
the step test and the loop's back-edge were taken branches, against
gcc's single back-edge.

Fix: the fast loop's misses fall through (CANDIDATE(hit, miss) jumps on
a hit; the step increase and the repcode hit move out of line), so the
loop's only taken branch is the one back to inner. Same branch count,
fewer taken branches. Output identical.

| config | before cyc/B | after | change (5 rounds) |
|---|---|---|---|
| L1 dickens | 11.58 | 11.43 | **-1.3%** [-1.3..-0.2] |
| L1 mozilla | 7.79 | 7.77 | -0.3% [-0.6..-0.0] |
| L1 xml | 4.98 | 4.99 | +0.2% [-0.1..+0.4] |
| L1 sao | 9.30 | 9.19 | **-1.2%** [-2.0..-0.5] |
| L1 x-ray | 4.73 | 4.70 | -0.2% [-0.4..-0.1] |
| L1 samba | 6.78 | 6.77 | -0.2% [-0.4..-0.1] |
| L1 tar4m -B16K | 15.61 | 15.30 | **-2.0%** [-2.3..-1.9] |
| L1 tar4m -B128K | 12.18 | 11.81 | **-3.1%** [-3.5..-2.8] |

The double-fast loop (L3) had the same shape: per position the repcode
miss, the long-candidate miss, the step test and the back-edge were taken
jumps. Same fix (LONGCAND jumps to longmatch on a hit; the repcode hit,
the long-match body and the step increase move out of line). Output
identical.

| config | before cyc/B | after | change (5 rounds) |
|---|---|---|---|
| L3 dickens | 20.11 | 19.84 | **-1.3%** [-1.6..-0.2] |
| L3 mozilla | 13.09 | 13.01 | -0.6% [-1.6..-0.5] |
| L3 xml | 6.73 | 6.69 | -0.6% [-0.8..-0.2] |
| L3 sao | 22.25 | 22.07 | **-1.2%** [-1.5..-0.0] |
| L3 samba | 10.53 | 10.39 | **-1.4%** [-1.4..-1.2] |
| L3 ooffice | 17.67 | 17.41 | -1.0% [-2.0..+0.7] |
| L3 tar4m -B16K | 22.23 | 21.98 | **-1.1%** [-2.0..-0.7] |
| L3 tar4m -B128K | 18.67 | 18.35 | **-2.1%** [-2.3..-1.6] |

Tried and dropped: the same layout in the lazy-parser template (row, hc
and bt; the repcode hit at ip+1 and a better first match out of line).
Paired, 5 rounds: L7 dickens +0.3%, mozilla -0.3%, xml +0.5%, L11
dickens +0.8%, samba -0.6%, L7 -B16K +1.4%, L7 -B128K +0.5%, L11 -B128K
+0.0%. The finder call dominates there; the branches around it do not
matter.
