# task20c-before

- date: 2026-11-05T08:10:08+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))
- tree: cc7faea (HEAD: Task 20b and its review follow-ups; the "before" of Task 20c), from a scratch worktree, same session as `task20c-after`. The 9 rows below 95% in `2026-11-05-task20b-tables.md` and their levels' other rows: `bench/compare.sh -mode d -l "1 3 7 11" -f "silesia.tar:16384 silesia.tar:131072 silesia/nci silesia/dickens silesia/mozilla silesia/mr" -label task20c-before`, 08:10 to 08:34, under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`. AC power on. load1 2.64 at the start, up to 4.48 during the run, 3.20 at the end (desktop background: Chrome, clickhouse-server). This breaks the load1 < 2 rule; C and Go are interleaved, so the ratios are less affected than the absolute speeds.

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| silesia.tar | 1 | 16K | 1083.4 | 989.5 | 91.3% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1031.4 | 951.6 | 92.3% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1098.1 | 1011.6 | 92.1% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1054.2 | 966.6 | 91.7% | 2.806 | 2.806 |
| silesia.tar | 1 | 128K | 1458.0 | 1390.8 | 95.4% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1420.6 | 1350.3 | 95.1% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1413.5 | 1318.9 | 93.3% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1460.4 | 1363.3 | 93.4% | 3.195 | 3.195 |
| nci | 1 | whole | 2477.2 | 2286.9 | 92.3% | 11.78 | 11.78 |
| nci | 3 | whole | 2371.3 | 2219.1 | 93.6% | 11.84 | 11.84 |
| nci | 7 | whole | 2975.0 | 2890.1 | 97.1% | 14.27 | 14.27 |
| nci | 11 | whole | 3427.2 | 3352.2 | 97.8% | 15.94 | 15.94 |
| dickens | 1 | whole | 1485.7 | 1470.7 | 99.0% | 2.392 | 2.392 |
| dickens | 3 | whole | 1285.0 | 1246.3 | 97.0% | 2.782 | 2.782 |
| dickens | 7 | whole | 1321.8 | 1344.2 | 101.7% | 3.047 | 3.047 |
| dickens | 11 | whole | 1398.4 | 1274.1 | 91.1% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1256.7 | 1261.4 | 100.4% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1192.0 | 1166.0 | 97.8% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1202.5 | 1170.4 | 97.3% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1241.2 | 1189.7 | 95.9% | 3.095 | 3.095 |
| mr | 1 | whole | 1533.8 | 1457.6 | 95.0% | 2.615 | 2.615 |
| mr | 3 | whole | 1394.6 | 1400.7 | 100.4% | 2.811 | 2.811 |
| mr | 7 | whole | 1370.8 | 1366.1 | 99.7% | 2.983 | 2.983 |
| mr | 11 | whole | 1400.2 | 1365.6 | 97.5% | 3.049 | 3.049 |

Geometric mean of % of C over 24 rows: **95.7%**
