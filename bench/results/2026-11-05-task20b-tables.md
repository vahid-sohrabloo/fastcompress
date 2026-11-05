# task20b-tables

- date: 2026-11-05T01:41:23+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))
- tree: 59c35a4 plus the Task 20b change (no allocation in the Huffman weight reader; C's length switch for the X1 fill), built by compare.sh from the working tree. One uninterrupted `bench/compare.sh -mode d -label task20b-tables` run, 01:41 to 02:43, under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`. AC power on. load1 2.90 at the start and 3.09 at the end, above the < 2 rule: a Chrome process held about half a core throughout and load1 did not fall below 2.9 in the 20 minutes before the start. C and Go run interleaved, so the ratios are less affected than the absolute speeds (C is 1 to 5% below `2026-11-04-asm-seq.md` on most rows).
- C and Go ratios are equal on every row.

## Summary vs Task 20a (`2026-11-04-asm-seq.md`)

| level | asm-seq | task20b |
|---|---|---|
| 1 | 98.8% | 98.8% |
| 3 | 98.7% | 98.7% |
| 7 | 98.8% | 99.1% |
| 11 | 98.0% | 97.8% |
| all 64 | 98.6% | 98.6% |

By block size: whole files 99.4% to 99.3%, 1M 99.5% to 99.8%, 4M 99.7% to 100.0%, 128K 94.4% to 94.3%, 16K 91.1% to 91.8%. Against the same-session re-measurement before the change (`2026-11-04-task20b-before.md`, 16 rows), the 16K rows gain 0.5 to 1.8 points and nci 0.3 to 1.8. No row loses more than 1.6 points of % of C against asm-seq (mr L11 97.4% to 95.8%, sao L11; neither touches the changed code at a measurable level).

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 1488.6 | 1477.6 | 99.3% | 2.392 | 2.392 |
| dickens | 3 | whole | 1312.7 | 1279.8 | 97.5% | 2.782 | 2.782 |
| dickens | 7 | whole | 1353.6 | 1336.2 | 98.7% | 3.047 | 3.047 |
| dickens | 11 | whole | 1400.0 | 1338.9 | 95.6% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1245.8 | 1248.6 | 100.2% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1190.4 | 1163.9 | 97.8% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1210.0 | 1154.5 | 95.4% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1228.9 | 1153.1 | 93.8% | 3.095 | 3.095 |
| mr | 1 | whole | 1503.3 | 1424.6 | 94.8% | 2.615 | 2.615 |
| mr | 3 | whole | 1373.5 | 1376.2 | 100.2% | 2.811 | 2.811 |
| mr | 7 | whole | 1350.7 | 1351.9 | 100.1% | 2.983 | 2.983 |
| mr | 11 | whole | 1372.9 | 1314.8 | 95.8% | 3.049 | 3.049 |
| nci | 1 | whole | 2433.7 | 2252.2 | 92.5% | 11.78 | 11.78 |
| nci | 3 | whole | 2306.1 | 2191.7 | 95.0% | 11.84 | 11.84 |
| nci | 7 | whole | 2899.8 | 2800.9 | 96.6% | 14.27 | 14.27 |
| nci | 11 | whole | 3340.1 | 3267.0 | 97.8% | 15.94 | 15.94 |
| ooffice | 1 | whole | 1043.7 | 1057.4 | 101.3% | 1.712 | 1.712 |
| ooffice | 3 | whole | 958.0 | 1001.8 | 104.6% | 1.968 | 1.968 |
| ooffice | 7 | whole | 990.7 | 1016.7 | 102.6% | 2.127 | 2.127 |
| ooffice | 11 | whole | 997.7 | 1002.1 | 100.4% | 2.172 | 2.172 |
| osdb | 1 | whole | 1560.4 | 1699.9 | 108.9% | 2.704 | 2.704 |
| osdb | 3 | whole | 1655.3 | 1742.4 | 105.3% | 2.880 | 2.880 |
| osdb | 7 | whole | 1680.7 | 1779.1 | 105.9% | 3.006 | 3.006 |
| osdb | 11 | whole | 1755.2 | 1831.1 | 104.3% | 3.127 | 3.127 |
| reymont | 1 | whole | 1563.6 | 1532.3 | 98.0% | 3.084 | 3.084 |
| reymont | 3 | whole | 1457.7 | 1439.7 | 98.8% | 3.420 | 3.420 |
| reymont | 7 | whole | 1562.5 | 1546.0 | 98.9% | 3.853 | 3.853 |
| reymont | 11 | whole | 1688.7 | 1659.6 | 98.3% | 4.196 | 4.196 |
| samba | 1 | whole | 1968.4 | 1895.4 | 96.3% | 3.937 | 3.937 |
| samba | 3 | whole | 1894.7 | 1833.7 | 96.8% | 4.360 | 4.360 |
| samba | 7 | whole | 1967.8 | 1872.6 | 95.2% | 4.789 | 4.789 |
| samba | 11 | whole | 1997.1 | 1949.0 | 97.6% | 5.040 | 5.040 |
| sao | 1 | whole | 1011.8 | 1164.4 | 115.1% | 1.160 | 1.160 |
| sao | 3 | whole | 857.1 | 885.9 | 103.4% | 1.312 | 1.312 |
| sao | 7 | whole | 1024.9 | 1106.2 | 107.9% | 1.386 | 1.386 |
| sao | 11 | whole | 1023.8 | 1082.3 | 105.7% | 1.394 | 1.394 |
| webster | 1 | whole | 1543.5 | 1528.0 | 99.0% | 3.035 | 3.035 |
| webster | 3 | whole | 1420.8 | 1376.5 | 96.9% | 3.427 | 3.427 |
| webster | 7 | whole | 1518.0 | 1504.9 | 99.1% | 3.928 | 3.928 |
| webster | 11 | whole | 1566.0 | 1489.1 | 95.1% | 4.172 | 4.172 |
| xml | 1 | whole | 2358.2 | 2266.7 | 96.1% | 7.710 | 7.710 |
| xml | 3 | whole | 2338.7 | 2249.6 | 96.2% | 8.414 | 8.414 |
| xml | 7 | whole | 2824.5 | 2721.1 | 96.3% | 10.07 | 10.07 |
| xml | 11 | whole | 3168.3 | 3032.2 | 95.7% | 10.73 | 10.73 |
| x-ray | 1 | whole | 1243.3 | 1195.7 | 96.2% | 1.251 | 1.251 |
| x-ray | 3 | whole | 930.7 | 938.0 | 100.8% | 1.393 | 1.393 |
| x-ray | 7 | whole | 884.8 | 911.1 | 103.0% | 1.559 | 1.559 |
| x-ray | 11 | whole | 829.2 | 832.6 | 100.4% | 1.594 | 1.594 |
| silesia.tar | 1 | 1M | 1528.3 | 1519.0 | 99.4% | 2.880 | 2.880 |
| silesia.tar | 3 | 1M | 1432.8 | 1424.1 | 99.4% | 3.147 | 3.147 |
| silesia.tar | 7 | 1M | 1516.5 | 1523.8 | 100.5% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | 1585.0 | 1583.0 | 99.9% | 3.448 | 3.448 |
| silesia.tar | 1 | 4M | 1528.9 | 1524.4 | 99.7% | 2.891 | 2.891 |
| silesia.tar | 3 | 4M | 1423.3 | 1419.0 | 99.7% | 3.188 | 3.188 |
| silesia.tar | 7 | 4M | 1512.1 | 1517.4 | 100.4% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 1570.7 | 1572.7 | 100.1% | 3.579 | 3.579 |
| silesia.tar | 1 | 128K | 1456.6 | 1390.7 | 95.5% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1421.6 | 1353.3 | 95.2% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1412.8 | 1319.5 | 93.4% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1463.2 | 1364.5 | 93.3% | 3.195 | 3.195 |
| silesia.tar | 1 | 16K | 1081.9 | 988.0 | 91.3% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1030.9 | 947.2 | 91.9% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1100.6 | 1015.5 | 92.3% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1053.3 | 968.5 | 91.9% | 2.806 | 2.806 |

Geometric mean of % of C over 64 rows: **98.6%**
