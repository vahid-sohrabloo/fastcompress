> **PROVISIONAL / CONTAMINATED RUN.** Preflight was clean (load1 1.39, AC 1, no other test or benchmark), but the zstd session's tests started during the second half of this run (load 2.86/4.27/3.89 at the end). C and Go cells are interleaved per round, so % of C is less affected than absolute MB/s, but these numbers are NOT a valid gate measurement. The clean re-run could not be done on 2026-11-06 (preconditions not met for 4 x 90 min: zstd go test runs, then AC power off). See .superpowers/sdd/gzip-libdeflate/T6.1.md.

# gzip deflate-compress

- date: 2026-11-06T08:02:24+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (6% busy at start)
- load1 at start: 1.39, at end: 2.86; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode c, libs: ld ours stdlib klauspost isal, levels: 1 3 6 9; isal: <gzbench>/gzbench_c (levels 0..3, whole files, raw)
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing

## Compression

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s | isal MB/s |
|---|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 205.1 | 111.6 | 54.4% | 2.417 | 2.417 | 141.1 | 162.2 | 360.8 |
| dickens | 3 | whole | 120.3 | 70 | 58.2% | 2.555 | 2.555 | 124.8 | 142.2 | 246.2 |
| dickens | 6 | whole | 66.7 | 43.9 | 65.8% | 2.641 | 2.641 | 82.1 | 97.8 | - |
| dickens | 9 | whole | 29 | 22.9 | 79.0% | 2.675 | 2.675 | 19.1 | 22.1 | - |
| mozilla | 1 | whole | 217.1 | 117 | 53.9% | 2.537 | 2.537 | 181.6 | 225.3 | 551.9 |
| mozilla | 3 | whole | 155.2 | 83.7 | 53.9% | 2.663 | 2.663 | 148.8 | 185.7 | 265.1 |
| mozilla | 6 | whole | 105.9 | 62 | 58.5% | 2.724 | 2.724 | 97.8 | 118.9 | - |
| mozilla | 9 | whole | 29 | 21.9 | 75.5% | 2.737 | 2.737 | 10.6 | 12.9 | - |
| mr | 1 | whole | 254.7 | 134.3 | 52.7% | 2.665 | 2.665 | 191.3 | 231.8 | 518.5 |
| mr | 3 | whole | 156.9 | 85.1 | 54.2% | 2.715 | 2.715 | 158.7 | 193.4 | 328.8 |
| mr | 6 | whole | 92.9 | 56 | 60.3% | 2.733 | 2.733 | 94.4 | 114.6 | - |
| mr | 9 | whole | 33.7 | 25.5 | 75.7% | 2.750 | 2.750 | 16.4 | 20.4 | - |
| nci | 1 | whole | 523.6 | 235.8 | 45.0% | 8.743 | 8.743 | 442.1 | 536.1 | 1134.7 |
| nci | 3 | whole | 348.1 | 159.7 | 45.9% | 9.322 | 9.322 | 366.6 | 461.7 | 331.9 |
| nci | 6 | whole | 224.3 | 118.1 | 52.7% | 10.853 | 10.853 | 232.6 | 293.6 | - |
| nci | 9 | whole | 56.4 | 41.1 | 72.9% | 11.470 | 11.470 | 21.2 | 24 | - |
| ooffice | 1 | whole | 150 | 85.4 | 56.9% | 1.878 | 1.878 | 132.8 | 162.7 | 428.3 |
| ooffice | 3 | whole | 108.6 | 63.8 | 58.7% | 1.961 | 1.961 | 104.3 | 127.8 | 253.9 |
| ooffice | 6 | whole | 78.7 | 49.2 | 62.5% | 2.002 | 2.002 | 65 | 77 | - |
| ooffice | 9 | whole | 44 | 31.7 | 72.0% | 2.006 | 2.006 | 25.5 | 29.8 | - |
| osdb | 1 | whole | 245.1 | 131 | 53.4% | 2.607 | 2.607 | 216.9 | 259.4 | 600.8 |
| osdb | 3 | whole | 167 | 94.6 | 56.6% | 2.641 | 2.641 | 186.7 | 235 | 285.7 |
| osdb | 6 | whole | 119.3 | 73.6 | 61.7% | 2.755 | 2.755 | 108.9 | 132.8 | - |
| osdb | 9 | whole | 97.7 | 63.4 | 64.9% | 2.760 | 2.760 | 58.1 | 71.6 | - |
| reymont | 1 | whole | 252.3 | 131.3 | 52.0% | 3.016 | 3.016 | 183 | 215.1 | 448.3 |
| reymont | 3 | whole | 152.3 | 83.3 | 54.7% | 3.279 | 3.279 | 159.4 | 186.2 | 243 |
| reymont | 6 | whole | 76.7 | 48.1 | 62.7% | 3.532 | 3.532 | 105.3 | 128.9 | - |
| reymont | 9 | whole | 16.8 | 14.1 | 83.9% | 3.669 | 3.669 | 10 | 10.9 | - |
| samba | 1 | whole | 298.5 | 152.9 | 51.2% | 3.683 | 3.683 | 247.6 | 299.1 | 644.2 |
| samba | 3 | whole | 201.5 | 104.9 | 52.1% | 3.854 | 3.854 | 212.5 | 261.3 | 280.9 |
| samba | 6 | whole | 124.6 | 73.5 | 59.0% | 4.048 | 4.048 | 140.3 | 171.9 | - |
| samba | 9 | whole | 41.2 | 31.9 | 77.4% | 4.100 | 4.100 | 27.8 | 31.6 | - |
| sao | 1 | whole | 143 | 79.3 | 55.5% | 1.301 | 1.301 | 121.8 | 146.5 | 389.8 |
| sao | 3 | whole | 93.8 | 55.2 | 58.8% | 1.343 | 1.343 | 91.8 | 113 | 282 |
| sao | 6 | whole | 63.7 | 41.5 | 65.1% | 1.359 | 1.359 | 57.6 | 68.2 | - |
| sao | 9 | whole | 44.4 | 32.1 | 72.3% | 1.362 | 1.362 | 32.1 | 36.6 | - |
| webster | 1 | whole | 236 | 126.5 | 53.6% | 3.060 | 3.060 | 168.1 | 198.6 | 445.5 |
| webster | 3 | whole | 156.3 | 87.4 | 55.9% | 3.229 | 3.229 | 145.7 | 172.8 | 237.2 |
| webster | 6 | whole | 93.8 | 58.7 | 62.6% | 3.415 | 3.415 | 95.9 | 115.3 | - |
| webster | 9 | whole | 35.2 | 27.3 | 77.6% | 3.489 | 3.489 | 23.5 | 26.6 | - |
| xml | 1 | whole | 417.3 | 195.6 | 46.9% | 6.021 | 6.021 | 335.1 | 404.1 | 820.3 |
| xml | 3 | whole | 280.3 | 136.7 | 48.8% | 6.737 | 6.737 | 268.4 | 335.6 | 294 |
| xml | 6 | whole | 174.8 | 99.2 | 56.8% | 7.810 | 7.810 | 184.4 | 227.9 | - |
| xml | 9 | whole | 58.1 | 43.9 | 75.6% | 8.230 | 8.230 | 35.1 | 38.5 | - |
| x-ray | 1 | whole | 131.4 | 72.3 | 55.0% | 1.347 | 1.347 | 363.5 | 420 | 437.6 |
| x-ray | 3 | whole | 107.2 | 61.9 | 57.7% | 1.399 | 1.399 | 103 | 127.5 | 282.7 |
| x-ray | 6 | whole | 92.9 | 55.1 | 59.3% | 1.413 | 1.413 | 57.3 | 67.1 | - |
| x-ray | 9 | whole | 79.1 | 48.7 | 61.6% | 1.415 | 1.415 | 58.6 | 67.2 | - |
| silesia.tar | 1 | 1M | 242.3 | 127.8 | 52.7% | 2.878 | 2.878 | 202.3 | 243 | - |
| silesia.tar | 3 | 1M | 165.5 | 88.5 | 53.5% | 3.014 | 3.014 | 163.9 | 199.3 | - |
| silesia.tar | 6 | 1M | 105.8 | 64.3 | 60.8% | 3.132 | 3.132 | 104.1 | 125.8 | - |
| silesia.tar | 9 | 1M | 37.4 | 28.4 | 75.9% | 3.169 | 3.169 | 18.2 | 21.4 | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 52.5% | 45.0% (nci/whole) | FAIL (below hard floor) |
| 3 | 13 | 54.4% | 45.9% (nci/whole) | FAIL (below hard floor) |
| 6 | 13 | 60.5% | 52.7% (nci/whole) | FAIL (below hard floor) |
| 9 | 13 | 74.0% | 61.6% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 52.5% < 100%
- level 1: dickens/whole 54.4% < 90% HARD FLOOR
- level 1: mozilla/whole 53.9% < 90% HARD FLOOR
- level 1: mr/whole 52.7% < 90% HARD FLOOR
- level 1: nci/whole 45.0% < 90% HARD FLOOR
- level 1: ooffice/whole 56.9% < 90% HARD FLOOR
- level 1: osdb/whole 53.4% < 90% HARD FLOOR
- level 1: reymont/whole 52.0% < 90% HARD FLOOR
- level 1: samba/whole 51.2% < 90% HARD FLOOR
- level 1: sao/whole 55.5% < 90% HARD FLOOR
- level 1: webster/whole 53.6% < 90% HARD FLOOR
- level 1: xml/whole 46.9% < 90% HARD FLOOR
- level 1: x-ray/whole 55.0% < 90% HARD FLOOR
- level 1: silesia.tar/1M 52.7% < 90% HARD FLOOR
- level 3: geomean 54.4% < 100%
- level 3: dickens/whole 58.2% < 90% HARD FLOOR
- level 3: mozilla/whole 53.9% < 90% HARD FLOOR
- level 3: mr/whole 54.2% < 90% HARD FLOOR
- level 3: nci/whole 45.9% < 90% HARD FLOOR
- level 3: ooffice/whole 58.7% < 90% HARD FLOOR
- level 3: osdb/whole 56.6% < 90% HARD FLOOR
- level 3: reymont/whole 54.7% < 90% HARD FLOOR
- level 3: samba/whole 52.1% < 90% HARD FLOOR
- level 3: sao/whole 58.8% < 90% HARD FLOOR
- level 3: webster/whole 55.9% < 90% HARD FLOOR
- level 3: xml/whole 48.8% < 90% HARD FLOOR
- level 3: x-ray/whole 57.7% < 90% HARD FLOOR
- level 3: silesia.tar/1M 53.5% < 90% HARD FLOOR
- level 6: geomean 60.5% < 100%
- level 6: dickens/whole 65.8% < 90% HARD FLOOR
- level 6: mozilla/whole 58.5% < 90% HARD FLOOR
- level 6: mr/whole 60.3% < 90% HARD FLOOR
- level 6: nci/whole 52.7% < 90% HARD FLOOR
- level 6: ooffice/whole 62.5% < 90% HARD FLOOR
- level 6: osdb/whole 61.7% < 90% HARD FLOOR
- level 6: reymont/whole 62.7% < 90% HARD FLOOR
- level 6: samba/whole 59.0% < 90% HARD FLOOR
- level 6: sao/whole 65.1% < 90% HARD FLOOR
- level 6: webster/whole 62.6% < 90% HARD FLOOR
- level 6: xml/whole 56.8% < 90% HARD FLOOR
- level 6: x-ray/whole 59.3% < 90% HARD FLOOR
- level 6: silesia.tar/1M 60.8% < 90% HARD FLOOR
- level 9: geomean 74.0% < 100%
- level 9: dickens/whole 79.0% < 90% HARD FLOOR
- level 9: mozilla/whole 75.5% < 90% HARD FLOOR
- level 9: mr/whole 75.7% < 90% HARD FLOOR
- level 9: nci/whole 72.9% < 90% HARD FLOOR
- level 9: ooffice/whole 72.0% < 90% HARD FLOOR
- level 9: osdb/whole 64.9% < 90% HARD FLOOR
- level 9: reymont/whole 83.9% < 90% HARD FLOOR
- level 9: samba/whole 77.4% < 90% HARD FLOOR
- level 9: sao/whole 72.3% < 90% HARD FLOOR
- level 9: webster/whole 77.6% < 90% HARD FLOOR
- level 9: xml/whole 75.6% < 90% HARD FLOOR
- level 9: x-ray/whole 61.6% < 90% HARD FLOOR
- level 9: silesia.tar/1M 75.9% < 90% HARD FLOOR

OVERALL: FAIL
