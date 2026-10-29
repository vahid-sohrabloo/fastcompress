# btlazy2-port

- date: 2026-10-29T15:51:42+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i2 [-B<block>]`; Go: `gobench -iters 5 -secs 1` (best pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of input (compression only)

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens-100K | 11 | whole | 16.0 | 12.9 | 80.6% | 2.650 | 2.650 |
| dickens-192K | 11 | whole | 12.9 | 10.7 | 82.9% | 2.807 | 2.807 |
| mozilla-100K | 11 | whole | 43.7 | 31.8 | 72.8% | 5.614 | 5.614 |
| mozilla-192K | 11 | whole | 63.2 | 43.3 | 68.5% | 3.359 | 3.359 |
| mr-192K | 11 | whole | 12.5 | 10.1 | 80.8% | 3.888 | 3.888 |
| nci-192K | 11 | whole | 35.8 | 25.4 | 70.9% | 15.06 | 15.06 |
| ooffice-192K | 11 | whole | 26.5 | 19.9 | 75.1% | 1.920 | 1.920 |
| osdb-192K | 11 | whole | 37.5 | 27.9 | 74.4% | 2.793 | 2.793 |
| reymont-192K | 11 | whole | 12.1 | 9.8 | 81.0% | 3.802 | 3.802 |
| samba-100K | 11 | whole | 28.5 | 22.0 | 77.2% | 3.413 | 3.413 |
| samba-192K | 11 | whole | 39.2 | 29.5 | 75.3% | 1.882 | 1.882 |
| sao-192K | 11 | whole | 27.0 | 20.0 | 74.1% | 1.361 | 1.361 |
| webster-100K | 11 | whole | 20.7 | 16.2 | 78.3% | 3.119 | 3.119 |
| webster-192K | 11 | whole | 17.2 | 13.5 | 78.5% | 3.445 | 3.445 |
| xml-100K | 11 | whole | 49.0 | 36.1 | 73.7% | 15.28 | 15.28 |
| xml-192K | 11 | whole | 35.4 | 25.3 | 71.5% | 16.74 | 16.74 |
| x-ray-100K | 11 | whole | 45.1 | 33.1 | 73.4% | 1.316 | 1.316 |
| x-ray-192K | 11 | whole | 42.6 | 30.4 | 71.4% | 1.363 | 1.363 |

Geometric mean of % of C over 18 rows: **75.5%**

Inputs: the first 192 KiB (196608 bytes; level 11 table "<= 256 KiB", btlazy2 wlog 18) or 100 KiB (102400 bytes; table "<= 128 KiB", btlazy2 wlog 17) of each Silesia file, made with `head -c`, since compare.sh has no per-file block option and `silesia.tar` is absent. Run as `bench/compare.sh -l 11 -f "<chunks>" -i 2 -g 1 -n 3 -label btlazy2-port` (taskset -c 2, -T1).
