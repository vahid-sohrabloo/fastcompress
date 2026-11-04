# decode-pure-go

## Summary

Pure-Go decode baseline (Phase 2 Task 18, before any decoder optimisation): `bench/compare.sh -mode d -label decode-pure-go`. Every row has equal C and Go ratios (the frames are byte-identical), and gobench checks once that every block decodes back to the input.

Geometric mean of % of C (64 rows; "whole files" = the 48 rows with block = whole):

| level | all rows | whole files |
|---|---|---|
| 1 | 48.6% | 48.4% |
| 3 | 45.9% | 45.5% |
| 7 | 45.8% | 45.5% |
| 11 | 47.0% | 46.9% |
| all | 46.8% | 46.6% |

Range: 37.3% (dickens L3) to 56.1% (sao L1). The lowest rows are text with short matches: dickens, reymont, mr and webster. silesia.tar L3 whole decodes at 654.7 MB/s in the profile run (`decode-pure-go-profiles.md`), above the 567 to 605 MB/s noted before this task.

## Conditions

- Go: go1.27.1 linux/amd64; C: zstd v1.5.7 (`ref/zstd/programs/zstd`, the Phase 1 build); CPU: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2; governor `powersave` (intel_pstate).
- On AC power for the whole run (checked every minute). Whole run under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0`, with GOMEMLIMIT=2GiB. No other benchmark or test was running.
- Load: load1 was 1.49 at the start. During the run it was 1.3 to 3.3, of which 1.0 is the benchmark itself; the rest is desktop background (clickhouse-server, chrome) on other cores, with nothing on CPU 2 or its SMT sibling CPU 3. From 09:48 to 09:53 browser activity pushed load1 to 7.35. That window covers the webster rows. Webster was re-run at load1 of about 2.2 (`compare.sh -mode d -f bench/corpus/silesia/webster`). The L1, L3 and L7 re-runs were within noise of the table (45.3%, 41.4% and 43.3% against 45.2%, 41.2% and 42.7%). For L11, the first run's C cell was depressed (1377.3 against 1604.2 MB/s, which gave 51.8%), so that row shows the re-run values.

## Results table

- date: 2026-11-04T09:13:05+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 1510.8 | 638.3 | 42.2% | 2.392 | 2.392 |
| dickens | 3 | whole | 1343.1 | 500.5 | 37.3% | 2.782 | 2.782 |
| dickens | 7 | whole | 1391.8 | 535.2 | 38.5% | 3.047 | 3.047 |
| dickens | 11 | whole | 1430.7 | 595.6 | 41.6% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1264.5 | 657.1 | 52.0% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1205.9 | 610.0 | 50.6% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1243.0 | 625.6 | 50.3% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1267.5 | 640.5 | 50.5% | 3.095 | 3.095 |
| mr | 1 | whole | 1538.9 | 742.7 | 48.3% | 2.615 | 2.615 |
| mr | 3 | whole | 1405.1 | 569.9 | 40.6% | 2.811 | 2.811 |
| mr | 7 | whole | 1397.5 | 576.9 | 41.3% | 2.983 | 2.983 |
| mr | 11 | whole | 1422.7 | 612.1 | 43.0% | 3.049 | 3.049 |
| nci | 1 | whole | 2496.8 | 1266.4 | 50.7% | 11.78 | 11.78 |
| nci | 3 | whole | 2407.4 | 1251.0 | 52.0% | 11.84 | 11.84 |
| nci | 7 | whole | 3058.6 | 1635.3 | 53.5% | 14.27 | 14.27 |
| nci | 11 | whole | 3536.1 | 1899.8 | 53.7% | 15.94 | 15.94 |
| ooffice | 1 | whole | 1055.3 | 567.8 | 53.8% | 1.712 | 1.712 |
| ooffice | 3 | whole | 975.1 | 489.5 | 50.2% | 1.968 | 1.968 |
| ooffice | 7 | whole | 1004.7 | 486.1 | 48.4% | 2.127 | 2.127 |
| ooffice | 11 | whole | 1005.4 | 490.4 | 48.8% | 2.172 | 2.172 |
| osdb | 1 | whole | 1563.2 | 757.8 | 48.5% | 2.704 | 2.704 |
| osdb | 3 | whole | 1655.3 | 752.1 | 45.4% | 2.880 | 2.880 |
| osdb | 7 | whole | 1691.1 | 783.7 | 46.3% | 3.006 | 3.006 |
| osdb | 11 | whole | 1778.5 | 847.6 | 47.7% | 3.127 | 3.127 |
| reymont | 1 | whole | 1570.4 | 625.5 | 39.8% | 3.084 | 3.084 |
| reymont | 3 | whole | 1457.0 | 587.8 | 40.3% | 3.420 | 3.420 |
| reymont | 7 | whole | 1567.0 | 648.0 | 41.4% | 3.853 | 3.853 |
| reymont | 11 | whole | 1696.9 | 746.5 | 44.0% | 4.196 | 4.196 |
| samba | 1 | whole | 1961.7 | 930.4 | 47.4% | 3.937 | 3.937 |
| samba | 3 | whole | 1899.8 | 864.8 | 45.5% | 4.360 | 4.360 |
| samba | 7 | whole | 1939.5 | 900.6 | 46.4% | 4.789 | 4.789 |
| samba | 11 | whole | 2035.1 | 985.1 | 48.4% | 5.040 | 5.040 |
| sao | 1 | whole | 1011.8 | 568.1 | 56.1% | 1.160 | 1.160 |
| sao | 3 | whole | 854.4 | 448.7 | 52.5% | 1.312 | 1.312 |
| sao | 7 | whole | 1016.6 | 499.4 | 49.1% | 1.386 | 1.386 |
| sao | 11 | whole | 1005.3 | 514.4 | 51.2% | 1.394 | 1.394 |
| webster | 1 | whole | 1552.9 | 701.4 | 45.2% | 3.035 | 3.035 |
| webster | 3 | whole | 1424.5 | 587.0 | 41.2% | 3.427 | 3.427 |
| webster | 7 | whole | 1463.4 | 625.5 | 42.7% | 3.928 | 3.928 |
| webster | 11 | whole | 1604.2 | 722.4 | 45.0% (re-run) | 4.172 | 4.172 |
| xml | 1 | whole | 2357.7 | 1161.9 | 49.3% | 7.710 | 7.710 |
| xml | 3 | whole | 2347.4 | 1122.8 | 47.8% | 8.414 | 8.414 |
| xml | 7 | whole | 2831.0 | 1353.3 | 47.8% | 10.07 | 10.07 |
| xml | 11 | whole | 3080.3 | 1449.6 | 47.1% | 10.73 | 10.73 |
| x-ray | 1 | whole | 1230.1 | 621.2 | 50.5% | 1.251 | 1.251 |
| x-ray | 3 | whole | 920.6 | 421.3 | 45.8% | 1.393 | 1.393 |
| x-ray | 7 | whole | 895.7 | 377.2 | 42.1% | 1.559 | 1.559 |
| x-ray | 11 | whole | 839.3 | 362.4 | 43.2% | 1.594 | 1.594 |
| silesia.tar | 1 | 1M | 1524.9 | 750.5 | 49.2% | 2.880 | 2.880 |
| silesia.tar | 3 | 1M | 1436.3 | 661.7 | 46.1% | 3.147 | 3.147 |
| silesia.tar | 7 | 1M | 1528.4 | 704.7 | 46.1% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | 1589.1 | 741.6 | 46.7% | 3.448 | 3.448 |
| silesia.tar | 1 | 4M | 1533.4 | 751.2 | 49.0% | 2.891 | 2.891 |
| silesia.tar | 3 | 4M | 1427.1 | 661.5 | 46.4% | 3.188 | 3.188 |
| silesia.tar | 7 | 4M | 1518.1 | 707.1 | 46.6% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 1580.4 | 750.5 | 47.5% | 3.579 | 3.579 |
| silesia.tar | 1 | 128K | 1455.4 | 710.9 | 48.8% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1420.5 | 665.4 | 46.8% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1413.2 | 645.3 | 45.7% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1461.6 | 668.5 | 45.7% | 3.195 | 3.195 |
| silesia.tar | 1 | 16K | 1083.4 | 541.4 | 50.0% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1032.1 | 502.7 | 48.7% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1102.4 | 542.0 | 49.2% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1053.7 | 526.3 | 49.9% | 2.806 | 2.806 |

Geometric mean of % of C over 64 rows: **46.8%**
