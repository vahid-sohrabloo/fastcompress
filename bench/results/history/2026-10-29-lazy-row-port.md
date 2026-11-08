# lazy-row-port

- date: 2026-10-29T14:44:48+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -iters 5 -secs 2` (best pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of input (compression only)

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens | 7 | whole | 45.4 | 18.3 | 40.3% | 3.047 | 3.047 |
| dickens | 11 | whole | 19.3 | 7.9 | 40.9% | 3.210 | 3.210 |
| mozilla | 7 | whole | 73.8 | 29.5 | 40.0% | 3.015 | 3.015 |
| mozilla | 11 | whole | 29.0 | 12.4 | 42.8% | 3.095 | 3.095 |
| mr | 7 | whole | 59.5 | 21.5 | 36.1% | 2.983 | 2.983 |
| mr | 11 | whole | 23.9 | 8.8 | 36.8% | 3.049 | 3.049 |
| nci | 7 | whole | 131.8 | 54.5 | 41.4% | 14.27 | 14.27 |
| nci | 11 | whole | 68.0 | 30.6 | 45.0% | 15.94 | 15.94 |
| ooffice | 7 | whole | 63.0 | 26.3 | 41.7% | 2.127 | 2.127 |
| ooffice | 11 | whole | 34.3 | 14.5 | 42.3% | 2.172 | 2.172 |
| osdb | 7 | whole | 79.6 | 31.8 | 39.9% | 3.006 | 3.006 |
| osdb | 11 | whole | 32.2 | 13.9 | 43.2% | 3.127 | 3.127 |
| reymont | 7 | whole | 63.7 | 28.4 | 44.6% | 3.853 | 3.853 |
| reymont | 11 | whole | 26.8 | 11.5 | 42.9% | 4.196 | 4.196 |
| samba | 7 | whole | 110.1 | 48.0 | 43.6% | 4.789 | 4.789 |
| samba | 11 | whole | 53.3 | 23.2 | 43.5% | 5.040 | 5.040 |
| sao | 7 | whole | 65.3 | 24.6 | 37.7% | 1.386 | 1.386 |
| sao | 11 | whole | 29.9 | 11.3 | 37.8% | 1.394 | 1.394 |
| webster | 7 | whole | 68.4 | 30.1 | 44.0% | 3.928 | 3.928 |
| webster | 11 | whole | 26.4 | 10.8 | 40.9% | 4.172 | 4.172 |
| xml | 7 | whole | 138.1 | 59.5 | 43.1% | 10.07 | 10.07 |
| xml | 11 | whole | 78.1 | 35.3 | 45.2% | 10.73 | 10.73 |
| x-ray | 7 | whole | 71.8 | 27.2 | 37.9% | 1.559 | 1.559 |
| x-ray | 11 | whole | 36.2 | 12.8 | 35.4% | 1.594 | 1.594 |
| silesia.tar | 7 | 1M | 92.2 | 38.6 | 41.9% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | n/a (not ported) | n/a (not ported) | n/a | n/a | n/a |
| silesia.tar | 7 | 4M | 91.9 | 38.5 | 41.9% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 43.4 | 18.0 | 41.5% | 3.579 | 3.579 |

Geometric mean of % of C over 27 rows: **41.1%**
