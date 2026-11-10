# gzip deflate-compress-round7

Round 7 (T17.1): four amd64 asm changes found by comparing Go pprof with the C RIP sampler per loop region
(see .superpowers/sdd/gzip-libdeflate/T17.1.md):
1. levels 2..9: next_hashes kept in R11 / BP across the greedy / lazy loops (they were reloaded, masked and
   stored in the frame per search and per skipped byte; gcc keeps them in two registers);
2. levels 2..9: the hash-chain walks load next_tab zero-extended (MOVWLZX) and compare the cutoff in 16 bits,
   as gcc does (the sign-extending load was a cycle more on the walk's load chain);
3. levels 10..12: findMinCostPath's backward pass in asm (minCostPathAsm, CMOV form, no bounds checks in the
   length loop, checks per position / per match that fault to address 0);
4. level 1: the hash indexes the bucket directly, bucket[0] zero-extended with a 16-bit cutoff compare.

Method (same as rounds 5 and 6): `bench/gzip/compare.sh -mode c -l "1 3 6 9 12" -i 3 -g 3 -n 3 -libs "ld ours"`,
12 Silesia files whole + silesia.tar in 1M blocks, `taskset -c 2`, C and Go interleaved. "before" = a copy of
the repository with internal/deflate at HEAD 25eaa99 (the round-6 state), "after" = the worktree; back to back
under the test lock and a 6G scope after a precheck (AC0 = 1, load1 < 4, no test/benchmark process).
load1: before 2.30 -> 2.61 (per-row max 3.14, mean 2.16; 03:20-03:59), after 2.61 -> 1.84 (max 2.85, mean
2.11; 03:59-04:36). No run went above 5, none redone. 65/65 rows byte-identical to libdeflate in both runs.

Noise: the "before" numbers repeat round 6's within -0.1..+0.1 geomean points at every level (same code), so
the run-to-run spread is about that size; the slow L12 rows (nci 3.3, xml 4.1 MB/s, one decimal) carry about
+-2-3 points of rounding in "% of C". The in-package A/B (two independent interleaved rounds per change, in
the ledger) gives the finer per-change numbers: L1 +3.5..+7.1%, L3 +4.2..+9.3%, L6 +3.2..+7.8%, L9
+4.6..+10.2% (combined (1)+(2) vs the round-6 tree), L12 +2.2..+20.1% ((3) alone).

## Before / after (geomean % of C, min row)

| level | before geomean | before min row | after geomean | after min row |
|---|---|---|---|---|
| 1 | 99.3% | 94.6% (x-ray) | **103.6%** | **99.6% (x-ray)** |
| 3 | 94.7% | 93.0% (x-ray) | **101.6%** | **100.1% (mozilla)** |
| 6 | 96.1% | 95.0% (nci) | **102.1%** | **101.4% (samba)** |
| 9 | 95.0% | 93.1% (mozilla) | **102.8%** | **101.2% (xml)** |
| 12 | 108.8% | 93.5% (nci) | **119.8%** | **106.5% (nci)** |

The compress gate (geomean >= 100%, every row >= 95%) now PASSES at every measured level (it failed at every
level before). Every row of every level is at or above 99.6% of C; x-ray L1 (99.6%) is the only row below
100%.

## Level 12 rows (C MB/s, Go MB/s, % of C)

| file | before C | before Go | before % | after C | after Go | after % |
|---|---|---|---|---|---|---|
| dickens | 6 | 7.7 | 128.3% | 6 | 8.1 | 135.0% |
| mozilla | 7.1 | 7.5 | 105.6% | 7.1 | 8.5 | 119.7% |
| mr | 4.1 | 4.4 | 107.3% | 4.1 | 4.6 | 112.2% |
| nci | 3.1 | 2.9 | 93.5% | 3.1 | 3.3 | 106.5% |
| ooffice | 9.9 | 10.8 | 109.1% | 9.9 | 11.9 | 120.2% |
| osdb | 9.7 | 10.9 | 112.4% | 9.7 | 12.1 | 124.7% |
| reymont | 4.6 | 6 | 130.4% | 4.6 | 6.3 | 137.0% |
| samba | 5.2 | 5.4 | 103.8% | 5.2 | 6.2 | 119.2% |
| sao | 15.5 | 16.1 | 103.9% | 15.5 | 16.7 | 107.7% |
| webster | 4.8 | 6.1 | 127.1% | 4.8 | 6.7 | 139.6% |
| xml | 3.6 | 3.4 | 94.4% | 3.6 | 4.1 | 113.9% |
| x-ray | 23.2 | 23.2 | 100.0% | 23.3 | 25.3 | 108.6% |
| silesia.tar | 5.1 | 5.4 | 105.9% | 5.1 | 6.1 | 119.6% |

## Before (compare.sh output)


| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 205.3 | 205 | 99.9% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.3 | 115.2 | 95.8% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.8 | 64.1 | 96.0% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29 | 27.3 | 94.1% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 7.7 | 128.3% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 217.1 | 213 | 98.1% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 156 | 146.2 | 93.7% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 106 | 100.9 | 95.2% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 29 | 27 | 93.1% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7.1 | 7.5 | 105.6% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 255.1 | 278.6 | 109.2% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 157.2 | 151.4 | 96.3% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 93 | 90 | 96.8% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.8 | 31.6 | 93.5% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 4.4 | 107.3% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 524.2 | 534 | 101.9% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 348.8 | 329.4 | 94.4% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224.9 | 213.6 | 95.0% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.3 | 54.2 | 96.3% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3.1 | 2.9 | 93.5% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 149.9 | 144.6 | 96.5% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.5 | 101.6 | 93.6% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 78.5 | 75.1 | 95.7% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44 | 41.6 | 94.5% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.9 | 10.8 | 109.1% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 244.8 | 240.2 | 98.1% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 167.1 | 158.4 | 94.8% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.5 | 114.9 | 96.2% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.6 | 94.6 | 96.9% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.7 | 10.9 | 112.4% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 252.2 | 251.1 | 99.6% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.3 | 147 | 96.5% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.9 | 74 | 96.2% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.8 | 15.8 | 94.0% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.6 | 6 | 130.4% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 295.2 | 298.6 | 101.2% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 202.4 | 190.2 | 94.0% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.7 | 118.6 | 95.1% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 39.6 | 96.1% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 5.4 | 103.8% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 143.1 | 137.4 | 96.0% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.5 | 87.5 | 93.6% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.5 | 61.7 | 97.2% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.5 | 42.6 | 95.7% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.5 | 16.1 | 103.9% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 236.4 | 233 | 98.6% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 156.6 | 149.2 | 95.3% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93.5 | 89.1 | 95.3% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35.2 | 32.8 | 93.2% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 6.1 | 127.1% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 417.6 | 414.5 | 99.3% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 280.1 | 266.9 | 95.3% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 175.7 | 167.9 | 95.6% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.3 | 54.7 | 93.8% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.6 | 3.4 | 94.4% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 132.9 | 125.7 | 94.6% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 107.3 | 99.8 | 93.0% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 92.8 | 91.4 | 98.5% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 79 | 78.8 | 99.7% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.2 | 23.2 | 100.0% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 242.9 | 239.7 | 98.7% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 165.6 | 156.4 | 94.4% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 106.4 | 102.1 | 96.0% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.4 | 35.2 | 94.1% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5.1 | 5.4 | 105.9% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 99.3% | 94.6% (x-ray/whole) | FAIL |
| 3 | 13 | 94.7% | 93.0% (x-ray/whole) | FAIL |
| 6 | 13 | 96.1% | 95.0% (nci/whole) | FAIL |
| 9 | 13 | 95.0% | 93.1% (mozilla/whole) | FAIL |
| 12 | 13 | 108.8% | 93.5% (nci/whole) | FAIL |

Violations:
- level 1: geomean 99.3% < 100%
- level 1: x-ray/whole 94.6% < 95%
- level 3: geomean 94.7% < 100%
- level 3: mozilla/whole 93.7% < 95%
- level 3: nci/whole 94.4% < 95%
- level 3: ooffice/whole 93.6% < 95%
- level 3: osdb/whole 94.8% < 95%
- level 3: samba/whole 94.0% < 95%
- level 3: sao/whole 93.6% < 95%
- level 3: x-ray/whole 93.0% < 95%
- level 3: silesia.tar/1M 94.4% < 95%
- level 6: geomean 96.1% < 100%
- level 9: geomean 95.0% < 100%
- level 9: dickens/whole 94.1% < 95%
- level 9: mozilla/whole 93.1% < 95%
- level 9: mr/whole 93.5% < 95%
- level 9: ooffice/whole 94.5% < 95%
- level 9: reymont/whole 94.0% < 95%
- level 9: webster/whole 93.2% < 95%
- level 9: xml/whole 93.8% < 95%
- level 9: silesia.tar/1M 94.1% < 95%
- level 12: nci/whole 93.5% < 95%
- level 12: xml/whole 94.4% < 95%

OVERALL: FAIL

## After (compare.sh output)


| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 205.3 | 212.5 | 103.5% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.1 | 122.6 | 102.1% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.7 | 68.3 | 102.4% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29 | 29.6 | 102.1% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 8.1 | 135.0% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 216.9 | 222.7 | 102.7% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.7 | 155.9 | 100.1% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 105.8 | 107.5 | 101.6% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 29 | 29.6 | 102.1% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7.1 | 8.5 | 119.7% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 255.3 | 289.4 | 113.4% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 157 | 161.9 | 103.1% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 93 | 94.9 | 102.0% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.7 | 34.2 | 101.5% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 4.6 | 112.2% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 524.7 | 552.9 | 105.4% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 347.8 | 357.3 | 102.7% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224.3 | 228 | 101.6% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.3 | 58.5 | 103.9% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3.1 | 3.3 | 106.5% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 149.8 | 152.4 | 101.7% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.4 | 108.9 | 100.5% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 78.6 | 80 | 101.8% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44.1 | 45.2 | 102.5% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.9 | 11.9 | 120.2% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 244.2 | 249.9 | 102.3% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 166.7 | 169.6 | 101.7% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.3 | 122 | 102.3% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.6 | 101 | 103.5% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.7 | 12.1 | 124.7% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 252.5 | 261.9 | 103.7% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.3 | 155.9 | 102.4% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.8 | 79.1 | 103.0% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.8 | 17.3 | 103.0% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.6 | 6.3 | 137.0% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 298.8 | 314.3 | 105.2% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 202.4 | 203.5 | 100.5% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.4 | 126.1 | 101.4% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 43.2 | 104.9% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 6.2 | 119.2% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 142.9 | 144.1 | 100.8% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.8 | 94.4 | 100.6% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.6 | 65.3 | 102.7% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.4 | 45.6 | 102.7% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.5 | 16.7 | 107.7% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 236.3 | 243.5 | 103.0% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 157.2 | 159.8 | 101.7% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93.8 | 95.4 | 101.7% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35.2 | 35.8 | 101.7% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 6.7 | 139.6% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 418.4 | 432.1 | 103.3% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 280.4 | 285.4 | 101.8% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 175.8 | 178.6 | 101.6% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.5 | 59.2 | 101.2% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.6 | 4.1 | 113.9% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 132.8 | 132.3 | 99.6% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 107.1 | 109.2 | 102.0% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 92.7 | 95.4 | 102.9% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 79.1 | 82.4 | 104.2% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.3 | 25.3 | 108.6% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 243.2 | 250.9 | 103.2% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 165.2 | 167.5 | 101.4% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 106.6 | 108.4 | 101.7% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.4 | 38.4 | 102.7% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5.1 | 6.1 | 119.6% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 103.6% | 99.6% (x-ray/whole) | PASS |
| 3 | 13 | 101.6% | 100.1% (mozilla/whole) | PASS |
| 6 | 13 | 102.1% | 101.4% (samba/whole) | PASS |
| 9 | 13 | 102.8% | 101.2% (xml/whole) | PASS |
| 12 | 13 | 119.8% | 106.5% (nci/whole) | PASS |

OVERALL: PASS

## Addendum: independent confirmation

A later independent re-run at HEAD (`2026-11-10-deflate-confirm-head.md`, three runs) reproduced L1, L3, L6 and L9 within
0.3 geomean points, but measured L12 at 109.3-109.8% instead of the 119.8% above (C speeds identical, Go L12 5-15% lower
on every file). The L12 figure above is therefore not confirmed; use about 110% (lowest row 100.0-102.8%). The gate still
passes at L12.
