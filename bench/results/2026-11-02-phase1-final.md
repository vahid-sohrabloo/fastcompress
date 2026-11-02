# phase1-final

## Phase 1 gate summary

Geometric mean of % of C, computed from the table below (64 rows; "whole files" = the 48 rows with block = whole).

| level | default, all rows | default, whole files | huge pages, all rows | huge pages, whole files |
|---|---|---|---|---|
| 1 | 101.1% | 101.0% | 101.1% | 101.0% |
| 3 | 101.3% | 101.5% | 101.4% | 101.6% |
| 7 | 102.1% | 102.2% | 104.0% | 104.5% |
| 11 | 101.2% | 101.1% | 104.5% | 105.0% |
| all | 101.4% | 101.5% | 102.7% | 103.0% |

Rows below 90% of C: none (lowest row is 97.9%). Go and C compressed sizes are identical in every row.

Rows below 100% of C, default pages (17 of 64; run-to-run noise is about 1%):

| file | level | block | % of C |
|---|---|---|---|
| reymont | 3 | whole | 97.9% |
| silesia.tar | 11 | 128K | 98.6% |
| dickens | 11 | whole | 98.8% |
| mozilla | 1 | whole | 98.9% |
| dickens | 3 | whole | 99.0% |
| ooffice | 7 | whole | 99.0% |
| webster | 3 | whole | 99.0% |
| xml | 3 | whole | 99.0% |
| silesia.tar | 7 | 128K | 99.0% |
| ooffice | 11 | whole | 99.3% |
| silesia.tar | 1 | 16K | 99.4% |
| samba | 3 | whole | 99.5% |
| dickens | 1 | whole | 99.6% |
| ooffice | 1 | whole | 99.6% |
| mr | 7 | whole | 99.7% |
| sao | 1 | whole | 99.7% |
| silesia.tar | 3 | 1M | 99.8% |

With huge pages 13 rows are below 100% (lowest reymont L3 97.9%).

Round history of the all-rows default geometric mean (committed tables):

| round | geo-mean % of C |
|---|---|
| asm-20e | 96.7 |
| asm-20f | 97.9 |
| asm-20g | 98.6 |
| asm-20h | 99.2 |
| asm-20i | 99.7 |
| asm-20j | 101.1 |
| asm-20k | 101.6 |
| phase1-final | 101.4 |

The 20k to phase1-final change (-0.2) is within run-to-run noise.

## Results table

- date: 2026-11-02T00:04:08+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of input (compression only)

| file | level | block | C MB/s | Go MB/s | % of C | Go huge pages MB/s | huge pages % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 348.4 | 346.9 | 99.6% | 347.7 | 99.8% | 2.392 | 2.392 |
| dickens | 3 | whole | 201.2 | 199.1 | 99.0% | 198.1 | 98.5% | 2.782 | 2.782 |
| dickens | 7 | whole | 67.5 | 68.1 | 100.9% | 69.1 | 102.4% | 3.047 | 3.047 |
| dickens | 11 | whole | 25.6 | 25.3 | 98.8% | 26.5 | 103.5% | 3.210 | 3.210 |
| mozilla | 1 | whole | 516.7 | 511.1 | 98.9% | 511.9 | 99.1% | 2.565 | 2.565 |
| mozilla | 3 | whole | 307.7 | 320.2 | 104.1% | 319.7 | 103.9% | 2.810 | 2.810 |
| mozilla | 7 | whole | 103.0 | 103.6 | 100.6% | 105.5 | 102.4% | 3.015 | 3.015 |
| mozilla | 11 | whole | 45.7 | 46.2 | 101.1% | 47.6 | 104.2% | 3.095 | 3.095 |
| mr | 1 | whole | 468.7 | 477.4 | 101.9% | 476.3 | 101.6% | 2.615 | 2.615 |
| mr | 3 | whole | 249.1 | 251.0 | 100.8% | 252.4 | 101.3% | 2.811 | 2.811 |
| mr | 7 | whole | 89.3 | 89.0 | 99.7% | 90.5 | 101.3% | 2.983 | 2.983 |
| mr | 11 | whole | 33.7 | 33.7 | 100.0% | 35.0 | 103.9% | 3.049 | 3.049 |
| nci | 1 | whole | 954.9 | 997.2 | 104.4% | 997.3 | 104.4% | 11.78 | 11.78 |
| nci | 3 | whole | 786.2 | 790.0 | 100.5% | 788.9 | 100.3% | 11.84 | 11.84 |
| nci | 7 | whole | 186.1 | 190.4 | 102.3% | 195.9 | 105.3% | 14.27 | 14.27 |
| nci | 11 | whole | 85.5 | 88.7 | 103.7% | 91.1 | 106.5% | 15.94 | 15.94 |
| ooffice | 1 | whole | 440.5 | 438.6 | 99.6% | 438.7 | 99.6% | 1.712 | 1.712 |
| ooffice | 3 | whole | 228.9 | 240.6 | 105.1% | 240.1 | 104.9% | 1.968 | 1.968 |
| ooffice | 7 | whole | 81.0 | 80.2 | 99.0% | 81.5 | 100.6% | 2.127 | 2.127 |
| ooffice | 11 | whole | 40.4 | 40.1 | 99.3% | 41.6 | 103.0% | 2.172 | 2.172 |
| osdb | 1 | whole | 513.1 | 516.2 | 100.6% | 516.3 | 100.6% | 2.704 | 2.704 |
| osdb | 3 | whole | 310.6 | 320.1 | 103.1% | 321.9 | 103.6% | 2.880 | 2.880 |
| osdb | 7 | whole | 98.3 | 102.8 | 104.6% | 106.0 | 107.8% | 3.006 | 3.006 |
| osdb | 11 | whole | 36.7 | 37.2 | 101.4% | 39.1 | 106.5% | 3.127 | 3.127 |
| reymont | 1 | whole | 339.3 | 340.6 | 100.4% | 340.9 | 100.5% | 3.084 | 3.084 |
| reymont | 3 | whole | 253.2 | 248.0 | 97.9% | 248.0 | 97.9% | 3.420 | 3.420 |
| reymont | 7 | whole | 78.7 | 80.4 | 102.2% | 81.7 | 103.8% | 3.853 | 3.853 |
| reymont | 11 | whole | 30.6 | 31.0 | 101.3% | 32.1 | 104.9% | 4.196 | 4.196 |
| samba | 1 | whole | 578.6 | 584.2 | 101.0% | 584.1 | 101.0% | 3.937 | 3.937 |
| samba | 3 | whole | 391.2 | 389.4 | 99.5% | 389.2 | 99.5% | 4.360 | 4.360 |
| samba | 7 | whole | 129.9 | 134.7 | 103.7% | 137.8 | 106.1% | 4.789 | 4.789 |
| samba | 11 | whole | 58.7 | 59.9 | 102.0% | 62.1 | 105.8% | 5.040 | 5.040 |
| sao | 1 | whole | 431.8 | 430.6 | 99.7% | 429.6 | 99.5% | 1.160 | 1.160 |
| sao | 3 | whole | 184.7 | 193.6 | 104.8% | 194.2 | 105.1% | 1.312 | 1.312 |
| sao | 7 | whole | 75.3 | 76.2 | 101.2% | 77.7 | 103.2% | 1.386 | 1.386 |
| sao | 11 | whole | 35.7 | 35.7 | 100.0% | 37.4 | 104.8% | 1.394 | 1.394 |
| webster | 1 | whole | 393.3 | 398.0 | 101.2% | 398.5 | 101.3% | 3.035 | 3.035 |
| webster | 3 | whole | 238.9 | 236.4 | 99.0% | 237.2 | 99.3% | 3.427 | 3.427 |
| webster | 7 | whole | 82.9 | 86.5 | 104.3% | 88.6 | 106.9% | 3.928 | 3.928 |
| webster | 11 | whole | 33.2 | 33.9 | 102.1% | 35.1 | 105.7% | 4.172 | 4.172 |
| xml | 1 | whole | 780.3 | 805.2 | 103.2% | 806.5 | 103.4% | 7.710 | 7.710 |
| xml | 3 | whole | 623.6 | 617.3 | 99.0% | 620.2 | 99.5% | 8.414 | 8.414 |
| xml | 7 | whole | 174.3 | 184.6 | 105.9% | 191.2 | 109.7% | 10.07 | 10.07 |
| xml | 11 | whole | 89.1 | 92.6 | 103.9% | 95.4 | 107.1% | 10.73 | 10.73 |
| x-ray | 1 | whole | 853.2 | 867.4 | 101.7% | 866.7 | 101.6% | 1.251 | 1.251 |
| x-ray | 3 | whole | 178.2 | 188.1 | 105.6% | 189.5 | 106.3% | 1.393 | 1.393 |
| x-ray | 7 | whole | 85.0 | 87.0 | 102.4% | 89.0 | 104.7% | 1.559 | 1.559 |
| x-ray | 11 | whole | 40.5 | 40.6 | 100.2% | 42.2 | 104.2% | 1.594 | 1.594 |
| silesia.tar | 1 | 1M | 496.5 | 514.9 | 103.7% | 514.3 | 103.6% | 2.880 | 2.880 |
| silesia.tar | 3 | 1M | 312.7 | 312.0 | 99.8% | 311.9 | 99.7% | 3.147 | 3.147 |
| silesia.tar | 7 | 1M | 105.7 | 108.2 | 102.4% | 110.0 | 104.1% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | 54.5 | 55.4 | 101.7% | 56.5 | 103.7% | 3.448 | 3.448 |
| silesia.tar | 1 | 4M | 507.5 | 514.8 | 101.4% | 515.3 | 101.5% | 2.891 | 2.891 |
| silesia.tar | 3 | 4M | 301.7 | 302.1 | 100.1% | 303.8 | 100.7% | 3.188 | 3.188 |
| silesia.tar | 7 | 4M | 102.7 | 104.8 | 102.0% | 107.1 | 104.3% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 46.8 | 47.4 | 101.3% | 49.3 | 105.3% | 3.579 | 3.579 |
| silesia.tar | 1 | 128K | 503.7 | 507.6 | 100.8% | 504.3 | 100.1% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 362.4 | 364.9 | 100.7% | 364.1 | 100.5% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 91.3 | 90.4 | 99.0% | 90.4 | 99.0% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 27.6 | 27.2 | 98.6% | 27.3 | 98.9% | 3.195 | 3.195 |
| silesia.tar | 1 | 16K | 392.9 | 390.7 | 99.4% | 391.8 | 99.7% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 302.5 | 307.9 | 101.8% | 305.7 | 101.1% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 69.3 | 71.3 | 102.9% | 71.5 | 103.2% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 22.9 | 23.7 | 103.5% | 23.7 | 103.5% | 2.806 | 2.806 |

Geometric mean of % of C over 64 rows: **101.4%**
