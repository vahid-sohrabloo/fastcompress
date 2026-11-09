# gzip deflate-compress-round5

- date: 2026-11-08T22:54:28+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (4% busy at start)
- load1 at start: 1.57, at end: 3.14; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode c, libs: ld ours, levels: 1 3 6 9 12; isal: not requested
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing

## Compression

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 204 | 203.6 | 99.8% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 119.7 | 114.5 | 95.7% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.4 | 63.4 | 95.5% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 28.8 | 27.3 | 94.8% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 6.8 | 113.3% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 216.2 | 212.3 | 98.2% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.1 | 145.2 | 93.6% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 104.9 | 100.1 | 95.4% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 28.9 | 27 | 93.4% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7.1 | 6.4 | 90.1% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 253.1 | 275.5 | 108.9% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 155.2 | 148.2 | 95.5% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 91.4 | 88.6 | 96.9% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.5 | 31.7 | 94.6% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 3.5 | 85.4% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 521.6 | 530.6 | 101.7% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 345.2 | 327.3 | 94.8% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 223.2 | 211.5 | 94.8% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56 | 54.2 | 96.8% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3 | 2.6 | 86.7% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 149.2 | 143.5 | 96.2% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 107.3 | 100.3 | 93.5% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 77.8 | 74.1 | 95.2% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 43.6 | 41.5 | 95.2% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.8 | 9.4 | 95.9% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 243.2 | 238.3 | 98.0% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 165.5 | 157.5 | 95.2% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 118.8 | 114.5 | 96.4% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97 | 93.6 | 96.5% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.5 | 9.3 | 97.9% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 250.6 | 248.7 | 99.2% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.2 | 146 | 95.9% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.6 | 72.9 | 95.2% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.6 | 15.8 | 95.2% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.5 | 5.2 | 115.6% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 293 | 294.2 | 100.4% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 199.4 | 189.1 | 94.8% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 123.4 | 117.9 | 95.5% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 40.9 | 39.7 | 97.1% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 4.6 | 88.5% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 140.7 | 135.4 | 96.2% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 92.2 | 87.3 | 94.7% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 62.7 | 61 | 97.3% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44 | 42.3 | 96.1% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.1 | 13.3 | 88.1% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 233.4 | 230.8 | 98.9% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 156.2 | 148.5 | 95.1% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 92.9 | 88.7 | 95.5% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35 | 32.9 | 94.0% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 5.4 | 112.5% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 416.3 | 412.9 | 99.2% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 278.4 | 266.5 | 95.7% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 175.2 | 167.3 | 95.5% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.2 | 54.8 | 94.2% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.5 | 3.1 | 88.6% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 132.3 | 124.1 | 93.8% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 103.8 | 100 | 96.3% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 91.5 | 90.2 | 98.6% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 78.8 | 78.4 | 99.5% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.1 | 18.9 | 81.8% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 241.4 | 238.7 | 98.9% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 164.5 | 156 | 94.8% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 105.9 | 101.1 | 95.5% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.3 | 35.2 | 94.4% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5 | 4.7 | 94.0% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 99.1% | 93.8% (x-ray/whole) | FAIL |
| 3 | 13 | 95.0% | 93.5% (ooffice/whole) | FAIL |
| 6 | 13 | 95.9% | 94.8% (nci/whole) | FAIL |
| 9 | 13 | 95.5% | 93.4% (mozilla/whole) | FAIL |
| 12 | 13 | 94.7% | 81.8% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 99.1% < 100%
- level 1: x-ray/whole 93.8% < 95%
- level 3: geomean 95.0% < 100%
- level 3: mozilla/whole 93.6% < 95%
- level 3: nci/whole 94.8% < 95%
- level 3: ooffice/whole 93.5% < 95%
- level 3: samba/whole 94.8% < 95%
- level 3: sao/whole 94.7% < 95%
- level 3: silesia.tar/1M 94.8% < 95%
- level 6: geomean 95.9% < 100%
- level 6: nci/whole 94.8% < 95%
- level 9: geomean 95.5% < 100%
- level 9: dickens/whole 94.8% < 95%
- level 9: mozilla/whole 93.4% < 95%
- level 9: mr/whole 94.6% < 95%
- level 9: webster/whole 94.0% < 95%
- level 9: xml/whole 94.2% < 95%
- level 9: silesia.tar/1M 94.4% < 95%
- level 12: geomean 94.7% < 100%
- level 12: mozilla/whole 90.1% < 95%
- level 12: mr/whole 85.4% < 90% HARD FLOOR
- level 12: nci/whole 86.7% < 90% HARD FLOOR
- level 12: samba/whole 88.5% < 90% HARD FLOOR
- level 12: sao/whole 88.1% < 90% HARD FLOOR
- level 12: xml/whole 88.6% < 90% HARD FLOOR
- level 12: x-ray/whole 81.8% < 90% HARD FLOOR
- level 12: silesia.tar/1M 94.0% < 95%

OVERALL: FAIL
