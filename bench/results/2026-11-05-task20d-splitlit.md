# task20d-splitlit

- date: 2026-11-05T15:07:50+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))
- tree: the Task 20d tree (`decodeExecSeqsSplitAsm`, decompressSequencesBodySplitLitBuffer's sequences in the amd64 loop) on e25e330. One full `bench/compare.sh -mode d -label task20d-splitlit` run (64 rows), 15:07 to 16:09, under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`. AC power on. The run waited for load1 < 2 (from 3.33 at 14:57) and started at load1 1.95. load1 was then 1.5 to 3.9 until about 15:57 and 5.2 to 6.8 from 16:00 to the end (another session's work on top of Chrome and clickhouse-server). That breaks the load1 < 2 rule. The rows run last, silesia.tar 1M, 4M, 128K and 16K, ran under the highest load: both C and Go are 2 to 10% slower there than in the 20c table. C and Go are interleaved, so the ratios are less affected than the speeds. The previous full table is `2026-11-05-task20c-fsetable.md` (8fe8754, 100.9%).

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 1512.1 | 1528.3 | 101.1% | 2.392 | 2.392 |
| dickens | 3 | whole | 1339.4 | 1316.8 | 98.3% | 2.782 | 2.782 |
| dickens | 7 | whole | 1384.4 | 1382.1 | 99.8% | 3.047 | 3.047 |
| dickens | 11 | whole | 1438.3 | 1412.7 | 98.2% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1263.3 | 1297.2 | 102.7% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1202.6 | 1214.4 | 101.0% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1240.9 | 1229.9 | 99.1% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1253.9 | 1237.3 | 98.7% | 3.095 | 3.095 |
| mr | 1 | whole | 1535.9 | 1493.6 | 97.2% | 2.615 | 2.615 |
| mr | 3 | whole | 1393.5 | 1439.5 | 103.3% | 2.811 | 2.811 |
| mr | 7 | whole | 1390.9 | 1402.1 | 100.8% | 2.983 | 2.983 |
| mr | 11 | whole | 1410.5 | 1401.6 | 99.4% | 3.049 | 3.049 |
| nci | 1 | whole | 2478.3 | 2343.9 | 94.6% | 11.78 | 11.78 |
| nci | 3 | whole | 2397.6 | 2310.0 | 96.3% | 11.84 | 11.84 |
| nci | 7 | whole | 3034.3 | 3023.0 | 99.6% | 14.27 | 14.27 |
| nci | 11 | whole | 3508.9 | 3505.5 | 99.9% | 15.94 | 15.94 |
| ooffice | 1 | whole | 1052.6 | 1085.2 | 103.1% | 1.712 | 1.712 |
| ooffice | 3 | whole | 971.9 | 1034.8 | 106.5% | 1.968 | 1.968 |
| ooffice | 7 | whole | 1001.2 | 1043.6 | 104.2% | 2.127 | 2.127 |
| ooffice | 11 | whole | 1002.8 | 1030.4 | 102.8% | 2.172 | 2.172 |
| osdb | 1 | whole | 1560.3 | 1733.8 | 111.1% | 2.704 | 2.704 |
| osdb | 3 | whole | 1654.1 | 1782.8 | 107.8% | 2.880 | 2.880 |
| osdb | 7 | whole | 1688.0 | 1807.6 | 107.1% | 3.006 | 3.006 |
| osdb | 11 | whole | 1768.2 | 1888.1 | 106.8% | 3.127 | 3.127 |
| reymont | 1 | whole | 1570.5 | 1563.2 | 99.5% | 3.084 | 3.084 |
| reymont | 3 | whole | 1456.3 | 1461.6 | 100.4% | 3.420 | 3.420 |
| reymont | 7 | whole | 1562.9 | 1569.3 | 100.4% | 3.853 | 3.853 |
| reymont | 11 | whole | 1691.7 | 1680.9 | 99.4% | 4.196 | 4.196 |
| samba | 1 | whole | 1966.3 | 1944.9 | 98.9% | 3.937 | 3.937 |
| samba | 3 | whole | 1911.2 | 1901.0 | 99.5% | 4.360 | 4.360 |
| samba | 7 | whole | 1988.3 | 1957.0 | 98.4% | 4.789 | 4.789 |
| samba | 11 | whole | 2073.1 | 2028.3 | 97.8% | 5.040 | 5.040 |
| sao | 1 | whole | 1013.4 | 1203.4 | 118.7% | 1.160 | 1.160 |
| sao | 3 | whole | 859.5 | 937.9 | 109.1% | 1.312 | 1.312 |
| sao | 7 | whole | 1035.3 | 1150.7 | 111.1% | 1.386 | 1.386 |
| sao | 11 | whole | 1029.0 | 1136.9 | 110.5% | 1.394 | 1.394 |
| webster | 1 | whole | 1553.3 | 1560.5 | 100.5% | 3.035 | 3.035 |
| webster | 3 | whole | 1430.9 | 1409.5 | 98.5% | 3.427 | 3.427 |
| webster | 7 | whole | 1534.3 | 1529.9 | 99.7% | 3.928 | 3.928 |
| webster | 11 | whole | 1564.4 | 1531.7 | 97.9% | 4.172 | 4.172 |
| xml | 1 | whole | 2361.0 | 2318.0 | 98.2% | 7.710 | 7.710 |
| xml | 3 | whole | 2348.3 | 2302.0 | 98.0% | 8.414 | 8.414 |
| xml | 7 | whole | 2850.7 | 2772.2 | 97.2% | 10.07 | 10.07 |
| xml | 11 | whole | 3167.5 | 3111.7 | 98.2% | 10.73 | 10.73 |
| x-ray | 1 | whole | 1240.0 | 1198.0 | 96.6% | 1.251 | 1.251 |
| x-ray | 3 | whole | 930.6 | 948.9 | 102.0% | 1.393 | 1.393 |
| x-ray | 7 | whole | 892.1 | 925.7 | 103.8% | 1.559 | 1.559 |
| x-ray | 11 | whole | 833.3 | 843.8 | 101.3% | 1.594 | 1.594 |
| silesia.tar | 1 | 1M | 1527.2 | 1551.4 | 101.6% | 2.880 | 2.880 |
| silesia.tar | 3 | 1M | 1428.7 | 1463.0 | 102.4% | 3.147 | 3.147 |
| silesia.tar | 7 | 1M | 1519.3 | 1560.8 | 102.7% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | 1578.7 | 1624.5 | 102.9% | 3.448 | 3.448 |
| silesia.tar | 1 | 4M | 1527.3 | 1552.8 | 101.7% | 2.891 | 2.891 |
| silesia.tar | 3 | 4M | 1419.2 | 1453.0 | 102.4% | 3.188 | 3.188 |
| silesia.tar | 7 | 4M | 1502.4 | 1553.9 | 103.4% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 1555.0 | 1597.6 | 102.7% | 3.579 | 3.579 |
| silesia.tar | 1 | 128K | 1422.2 | 1446.5 | 101.7% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1387.3 | 1426.9 | 102.9% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1349.9 | 1408.8 | 104.4% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1385.7 | 1446.0 | 104.4% | 3.195 | 3.195 |
| silesia.tar | 1 | 16K | 1018.0 | 1008.0 | 99.0% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 930.4 | 902.7 | 97.0% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1031.6 | 997.6 | 96.7% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1016.8 | 987.3 | 97.1% | 2.806 | 2.806 |

Geometric mean of % of C over 64 rows: **101.4%**
