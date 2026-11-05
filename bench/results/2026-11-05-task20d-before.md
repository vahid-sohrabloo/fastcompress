# task20d-before

- date: 2026-11-05T11:41:24+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))
- tree: e25e330 (8fe8754's code plus test-only 20c review follow-ups), from a scratch worktree. `compare.sh -mode d -l "1 3 7 11" -f "silesia.tar:16384 silesia.tar:131072 nci dickens mozilla mr" -label task20d-before`, 11:41 to 12:05, under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`. AC power on. load1 3.08 to 3.44 (desktop background: Chrome, clickhouse-server); this breaks the load1 < 2 rule. The same-session after run is `2026-11-05-task20d-after.md`.

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| silesia.tar | 1 | 16K | 1077.6 | 1040.6 | 96.6% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1024.8 | 1001.7 | 97.7% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1099.4 | 1075.6 | 97.8% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1050.6 | 1020.6 | 97.1% | 2.806 | 2.806 |
| silesia.tar | 1 | 128K | 1454.8 | 1408.6 | 96.8% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1422.7 | 1372.2 | 96.5% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1409.1 | 1318.0 | 93.5% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1462.7 | 1377.6 | 94.2% | 3.195 | 3.195 |
| nci | 1 | whole | 2490.6 | 2344.0 | 94.1% | 11.78 | 11.78 |
| nci | 3 | whole | 2379.4 | 2276.4 | 95.7% | 11.84 | 11.84 |
| nci | 7 | whole | 3010.5 | 2962.0 | 98.4% | 14.27 | 14.27 |
| nci | 11 | whole | 3304.7 | 3433.5 | 103.9% | 15.94 | 15.94 |
| dickens | 1 | whole | 1510.1 | 1524.4 | 100.9% | 2.392 | 2.392 |
| dickens | 3 | whole | 1338.3 | 1317.5 | 98.4% | 2.782 | 2.782 |
| dickens | 7 | whole | 1384.0 | 1375.6 | 99.4% | 3.047 | 3.047 |
| dickens | 11 | whole | 1416.9 | 1384.3 | 97.7% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1264.9 | 1296.0 | 102.5% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1199.8 | 1209.7 | 100.8% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1236.7 | 1230.0 | 99.5% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1249.2 | 1235.9 | 98.9% | 3.095 | 3.095 |
| mr | 1 | whole | 1526.8 | 1486.1 | 97.3% | 2.615 | 2.615 |
| mr | 3 | whole | 1374.5 | 1433.1 | 104.3% | 2.811 | 2.811 |
| mr | 7 | whole | 1377.7 | 1387.1 | 100.7% | 2.983 | 2.983 |
| mr | 11 | whole | 1398.0 | 1387.5 | 99.2% | 3.049 | 3.049 |

Geometric mean of % of C over 24 rows: **98.4%**
