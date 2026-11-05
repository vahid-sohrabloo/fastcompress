# task20b-before

- date: 2026-11-04T23:19:11+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))
- tree: 59c35a4 (Task 20a and its N1/N2 follow-up; the "before" of Task 20b), the 7 rows below 95% in `2026-11-04-asm-seq.md` plus control rows. One `bench/compare.sh -mode d -l "1 3 7 11" -f "silesia.tar:16384 silesia.tar:131072 silesia/nci silesia/dickens" -label task20b-before` run, 23:19 to about 23:37, under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`. AC power on. load1 1.31 at the start and 2.89 at the end (desktop background: Chrome, clickhouse-server).

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| silesia.tar | 1 | 16K | 1080.7 | 967.0 | 89.5% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1025.7 | 937.6 | 91.4% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1100.1 | 1000.7 | 91.0% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1054.4 | 953.7 | 90.4% | 2.806 | 2.806 |
| silesia.tar | 1 | 128K | 1437.3 | 1368.9 | 95.2% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1406.7 | 1336.5 | 95.0% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1410.1 | 1307.4 | 92.7% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1436.6 | 1342.9 | 93.5% | 3.195 | 3.195 |
| nci | 1 | whole | 2449.0 | 2235.6 | 91.3% | 11.78 | 11.78 |
| nci | 3 | whole | 2361.7 | 2216.4 | 93.8% | 11.84 | 11.84 |
| nci | 7 | whole | 2942.9 | 2835.3 | 96.3% | 14.27 | 14.27 |
| nci | 11 | whole | 3377.9 | 3243.1 | 96.0% | 15.94 | 15.94 |
| dickens | 1 | whole | 1502.8 | 1481.6 | 98.6% | 2.392 | 2.392 |
| dickens | 3 | whole | 1340.4 | 1302.8 | 97.2% | 2.782 | 2.782 |
| dickens | 7 | whole | 1380.8 | 1355.2 | 98.1% | 3.047 | 3.047 |
| dickens | 11 | whole | 1435.2 | 1374.1 | 95.7% | 3.210 | 3.210 |

Geometric mean of % of C over 16 rows: **94.1%**
