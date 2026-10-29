# btopt-port

- date: 2026-10-29T16:38:03+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i2 [-B<block>]`; Go: `gobench -iters 5 -secs 1` (best pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of input (compression only)

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens-2M | 11 | 16K | 16.7 | 11.6 | 69.5% | 2.351 | 2.351 |
| mozilla-2M | 11 | 16K | 40.8 | 25.3 | 62.0% | 1.285 | 1.285 |
| mr-2M | 11 | 16K | 22.0 | 15.1 | 68.6% | 2.910 | 2.910 |
| nci-2M | 11 | 16K | 21.0 | 14.7 | 70.0% | 8.801 | 8.801 |
| ooffice-2M | 11 | 16K | 24.2 | 16.3 | 67.4% | 1.706 | 1.706 |
| osdb-2M | 11 | 16K | 28.8 | 19.4 | 67.4% | 1.820 | 1.820 |
| reymont-2M | 11 | 16K | 14.4 | 10.4 | 72.2% | 3.169 | 3.169 |
| samba-2M | 11 | 16K | 21.6 | 15.2 | 70.4% | 3.024 | 3.024 |
| sao-2M | 11 | 16K | 27.2 | 16.8 | 61.8% | 1.328 | 1.328 |
| webster-2M | 11 | 16K | 18.1 | 12.9 | 71.3% | 3.017 | 3.017 |
| xml-2M | 11 | 16K | 16.4 | 11.9 | 72.6% | 5.731 | 5.731 |
| x-ray-2M | 11 | 16K | 39.8 | 23.8 | 59.8% | 1.349 | 1.349 |

Geometric mean of % of C over 12 rows: **67.6%**

Inputs: the first 2 MiB (2097152 bytes, `head -c`) of each Silesia file, compressed in independent 16 KiB blocks (`zstd -b11 -B16384`, `gobench -block 16384`): level 11 selects btopt only for inputs of 16 KiB or less (clevels.h "<= 16 KiB" table: wlog 14, clog 15, hlog 14, slog 3, minMatch 4, targetLength 12). Run as `bench/compare.sh -l 11 -f "<chunk>:16384 ..." -i 2 -g 1 -n 3 -label btopt-port` (taskset -c 2, -T1); compare.sh now accepts `FILE:BLOCK` in `-f`.

Profile (dickens, x-ray): 58-73% of the time is in btGetAllMatches (hash/tree/repcode loads, i.e. memory latency), the rest in the price loop. Output-neutral micro-changes tried (non-recursive, inlinable litLengthPrice) gave no measurable gain and were dropped.
