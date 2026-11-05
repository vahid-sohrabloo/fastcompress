# task20c-fsetable

- date: 2026-11-05T10:23:21+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))
- tree: the Task 20c tree (`buildFSETableAsm`, the sequence-table build in amd64 assembly) on cc7faea. One full `bench/compare.sh -mode d -label task20c-fsetable` run (64 rows), 10:23 to 11:25, under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`. AC power on. The run waited for load1 < 2 (from 3.33 at 10:20) and started at load1 1.87; load1 then rose to 2.0 to 3.8 for most of the run (desktop background: Chrome, clickhouse-server), 3.77 at the end. That breaks the load1 < 2 rule for most rows; C and Go are interleaved, so the ratios are less affected than the absolute speeds. The previous full table is `2026-11-05-task20b-tables.md` (cc7faea's code, 98.6%).

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 1501.2 | 1516.6 | 101.0% | 2.392 | 2.392 |
| dickens | 3 | whole | 1340.9 | 1315.1 | 98.1% | 2.782 | 2.782 |
| dickens | 7 | whole | 1361.5 | 1376.6 | 101.1% | 3.047 | 3.047 |
| dickens | 11 | whole | 1432.3 | 1400.7 | 97.8% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1264.9 | 1297.2 | 102.6% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1205.1 | 1218.3 | 101.1% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1239.0 | 1236.4 | 99.8% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1253.2 | 1243.6 | 99.2% | 3.095 | 3.095 |
| mr | 1 | whole | 1493.0 | 1451.9 | 97.2% | 2.615 | 2.615 |
| mr | 3 | whole | 1398.2 | 1441.0 | 103.1% | 2.811 | 2.811 |
| mr | 7 | whole | 1387.2 | 1395.4 | 100.6% | 2.983 | 2.983 |
| mr | 11 | whole | 1401.1 | 1388.8 | 99.1% | 3.049 | 3.049 |
| nci | 1 | whole | 2490.3 | 2342.0 | 94.0% | 11.78 | 11.78 |
| nci | 3 | whole | 2383.3 | 2294.3 | 96.3% | 11.84 | 11.84 |
| nci | 7 | whole | 3036.7 | 3020.9 | 99.5% | 14.27 | 14.27 |
| nci | 11 | whole | 3442.0 | 3459.2 | 100.5% | 15.94 | 15.94 |
| ooffice | 1 | whole | 1055.4 | 1082.5 | 102.6% | 1.712 | 1.712 |
| ooffice | 3 | whole | 970.6 | 1038.0 | 106.9% | 1.968 | 1.968 |
| ooffice | 7 | whole | 1001.9 | 1043.4 | 104.1% | 2.127 | 2.127 |
| ooffice | 11 | whole | 998.9 | 1026.8 | 102.8% | 2.172 | 2.172 |
| osdb | 1 | whole | 1560.7 | 1732.9 | 111.0% | 2.704 | 2.704 |
| osdb | 3 | whole | 1654.4 | 1783.7 | 107.8% | 2.880 | 2.880 |
| osdb | 7 | whole | 1685.2 | 1805.5 | 107.1% | 3.006 | 3.006 |
| osdb | 11 | whole | 1761.1 | 1885.5 | 107.1% | 3.127 | 3.127 |
| reymont | 1 | whole | 1567.4 | 1562.8 | 99.7% | 3.084 | 3.084 |
| reymont | 3 | whole | 1454.8 | 1461.4 | 100.5% | 3.420 | 3.420 |
| reymont | 7 | whole | 1560.2 | 1561.1 | 100.1% | 3.853 | 3.853 |
| reymont | 11 | whole | 1685.2 | 1667.8 | 99.0% | 4.196 | 4.196 |
| samba | 1 | whole | 1960.1 | 1934.1 | 98.7% | 3.937 | 3.937 |
| samba | 3 | whole | 1903.1 | 1900.9 | 99.9% | 4.360 | 4.360 |
| samba | 7 | whole | 1969.7 | 1937.8 | 98.4% | 4.789 | 4.789 |
| samba | 11 | whole | 2013.6 | 2004.6 | 99.6% | 5.040 | 5.040 |
| sao | 1 | whole | 1012.4 | 1195.3 | 118.1% | 1.160 | 1.160 |
| sao | 3 | whole | 858.5 | 927.7 | 108.1% | 1.312 | 1.312 |
| sao | 7 | whole | 1028.8 | 1128.7 | 109.7% | 1.386 | 1.386 |
| sao | 11 | whole | 1021.2 | 1098.3 | 107.5% | 1.394 | 1.394 |
| webster | 1 | whole | 1556.3 | 1561.2 | 100.3% | 3.035 | 3.035 |
| webster | 3 | whole | 1428.9 | 1407.1 | 98.5% | 3.427 | 3.427 |
| webster | 7 | whole | 1530.4 | 1525.5 | 99.7% | 3.928 | 3.928 |
| webster | 11 | whole | 1575.9 | 1530.3 | 97.1% | 4.172 | 4.172 |
| xml | 1 | whole | 2359.4 | 2315.1 | 98.1% | 7.710 | 7.710 |
| xml | 3 | whole | 2344.7 | 2300.7 | 98.1% | 8.414 | 8.414 |
| xml | 7 | whole | 2843.4 | 2797.2 | 98.4% | 10.07 | 10.07 |
| xml | 11 | whole | 3171.1 | 3124.8 | 98.5% | 10.73 | 10.73 |
| x-ray | 1 | whole | 1244.1 | 1196.3 | 96.2% | 1.251 | 1.251 |
| x-ray | 3 | whole | 932.4 | 951.0 | 102.0% | 1.393 | 1.393 |
| x-ray | 7 | whole | 883.7 | 918.6 | 103.9% | 1.559 | 1.559 |
| x-ray | 11 | whole | 828.7 | 839.7 | 101.3% | 1.594 | 1.594 |
| silesia.tar | 1 | 1M | 1526.3 | 1546.3 | 101.3% | 2.880 | 2.880 |
| silesia.tar | 3 | 1M | 1433.1 | 1453.1 | 101.4% | 3.147 | 3.147 |
| silesia.tar | 7 | 1M | 1514.0 | 1557.5 | 102.9% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | 1580.2 | 1619.0 | 102.5% | 3.448 | 3.448 |
| silesia.tar | 1 | 4M | 1528.4 | 1528.1 | 100.0% | 2.891 | 2.891 |
| silesia.tar | 3 | 4M | 1416.5 | 1451.5 | 102.5% | 3.188 | 3.188 |
| silesia.tar | 7 | 4M | 1503.7 | 1556.5 | 103.5% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 1552.6 | 1606.4 | 103.5% | 3.579 | 3.579 |
| silesia.tar | 1 | 128K | 1454.4 | 1411.5 | 97.1% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1425.0 | 1373.1 | 96.4% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1416.1 | 1339.7 | 94.6% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1464.1 | 1388.1 | 94.8% | 3.195 | 3.195 |
| silesia.tar | 1 | 16K | 1082.7 | 1043.6 | 96.4% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1030.1 | 1006.1 | 97.7% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1095.5 | 1077.2 | 98.3% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1035.7 | 1020.6 | 98.5% | 2.806 | 2.806 |

Geometric mean of % of C over 64 rows: **100.9%**
