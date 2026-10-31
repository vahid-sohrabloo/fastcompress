# Task 20d: profiles and A/Bs

Harnesses as in Task 20c (2026-10-30-l3-smallblock-profile.md): `charn`
(C, libzstd 1.5.7 `gcc -O3`) and `goharn` (EncodeAll, CRC off), one reused
context, one call per block, `perf stat` `cpu_core/` user-mode counts per
input byte, `taskset -c 2`. A/B figures are medians of per-round paired cycle
ratios (`ab.sh`, 5 rounds, interquartile range in brackets). `tar32m` is the
first 32 MiB of silesia.tar. Background load (clickhouse, chrome) was 1.5-5
during the A/Bs; the paired ranges were tight.

## Step 1: hash-chain finder (L7 at 16 KiB blocks)

Before (93ea0fb), L7 tar32m -B16K:

| side | cycles/B | instr/B | branches/B | br-miss/KiB |
|---|---|---|---|---|
| C | 60.7 | 140.2 | 23.0 | 714 |
| Go | 91.8 | 392.2 | 62.9 | 742 |

`perf record` (Go): hcFindBestMatch 54%, insertAndFindFirstIndexInternal
23%, compressBlockLazyGeneric 13%, searchMax 1%: 91% in the pure-Go lazy
parser and hash-chain finder (windowLog 14 turns the row finder off).

Fix: the whole lazy loop in assembly with the hash-chain finder, generated
per mls (lazyHc4/5/6 + hcSearch4/5/6, the row lazy parser's template with
the finder swapped).

| config | Go before | Go after | change | C |
|---|---|---|---|---|
| L7 -B16K cycles/B | 91.79 | 60.84 | **-33.8%** [-33.8..-33.7] | 60.62 |
| L7 -B16K instr/B | 392.2 | 126.6 | | 140.2 |
| L7 -B128K cycles/B (row finder, unchanged code) | 44.15 | 43.38 | -1.9% | 44.70 |
