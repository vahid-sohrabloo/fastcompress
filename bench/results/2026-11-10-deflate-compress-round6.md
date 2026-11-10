# gzip deflate-compress-round6

Round 6 (T16.1): the near-optimal compressor's matchfinding loop (bt_matchfinder get_matches / skip_byte with
lz_extend and the per-position loop of deflate_compress_near_optimal) in amd64 assembly (btMatchLoopAsm,
internal/deflate/bt_matchfinder_amd64.s). Only levels 10..12 run different code; levels 1..9 are byte-identical
code to the before tree.

Method (same as round 5): `bench/gzip/compare.sh -mode c -l "1 3 6 9 12" -i 3 -g 3 -n 3 -libs "ld ours"`, 12
Silesia files whole + silesia.tar in 1M blocks, `taskset -c 2`, C and Go interleaved. "before" = a copy of the
repository with internal/deflate at the round's start (HEAD 3d9f632 state), "after" = the worktree; run back to
back under the test lock and a 6G scope, after a precheck (AC0 = 1, load1 < 4, no test/benchmark process).
load1: before 3.01 -> 1.81 (max 3.41, mean 2.28 over the per-row stamps), after 1.81 -> 1.78 (max 4.09, mean
2.19); no run went above 5, none redone. 65/65 rows byte-identical to libdeflate in both runs.

Noise: L1..L9 code is unchanged, so their before/after differences (-0.1..+0.2 geomean points) are the run-to-run
spread. compare.sh prints MB/s with one decimal, so the slow L12 rows (nci 2.9, xml 3.4 MB/s) carry about +-2
points of rounding in "% of C"; the in-package A/B (T16.1 ledger: two independent interleaved rounds, Go before
vs after on the same binary harness) gives the finer numbers: L12 x-ray +19..+21%, mr +24%, nci +10%, sao
+15..+18%, samba +12..+13%, xml +6..+7%, dickens +11%, mozilla +10%, ooffice +13%, webster +9.5%.

## Before / after (geomean % of C, min row)

| level | before geomean | before min row | after geomean | after min row |
|---|---|---|---|---|
| 1 | 99.1% | 94.7% (x-ray) | 99.3% | 94.8% (x-ray) |
| 3 | 94.7% | 92.5% (x-ray) | 94.6% | 93.0% (x-ray) |
| 6 | 96.0% | 94.9% (nci) | 96.1% | 95.1% (nci) |
| 9 | 95.0% | 92.8% (mozilla) | 95.1% | 93.4% (mozilla) |
| 12 | 94.7% | 82.8% (x-ray) | **108.9%** | **93.5% (nci)** |

## Level 12 rows (C MB/s, Go MB/s, % of C)

| file | before C | before Go | before % | after C | after Go | after % |
|---|---|---|---|---|---|---|
| dickens | 6 | 6.8 | 113.3% | 6 | 7.7 | 128.3% |
| mozilla | 7.1 | 6.5 | 91.5% | 7.1 | 7.5 | 105.6% |
| mr | 4.1 | 3.6 | 87.8% | 4.1 | 4.4 | 107.3% |
| nci | 3.1 | 2.6 | 83.9% | 3.1 | 2.9 | 93.5% |
| ooffice | 9.9 | 9.4 | 94.9% | 9.9 | 10.8 | 109.1% |
| osdb | 9.6 | 9.4 | 97.9% | 9.6 | 10.9 | 113.5% |
| reymont | 4.6 | 5.3 | 115.2% | 4.6 | 6 | 130.4% |
| samba | 5.2 | 4.7 | 90.4% | 5.2 | 5.4 | 103.8% |
| sao | 15.4 | 13.6 | 88.3% | 15.5 | 16.1 | 103.9% |
| webster | 4.8 | 5.4 | 112.5% | 4.8 | 6.1 | 127.1% |
| xml | 3.6 | 3.1 | 86.1% | 3.6 | 3.4 | 94.4% |
| x-ray | 23.2 | 19.2 | 82.8% | 23.1 | 23.2 | 100.4% |
| silesia.tar | 5.1 | 4.8 | 94.1% | 5.1 | 5.4 | 105.9% |

Level 12 is now above the 90% hard floor on every row (it had six rows below it in round 5); nci and xml
(93.5 / 94.4%) remain below 95%: their remaining time is mostly deflate_find_min_cost_path (see the ledger).
The level 1..9 gates are unchanged (that code was not touched).

## After (compare.sh output)

## Compression

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 205.1 | 204.7 | 99.8% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.3 | 115.2 | 95.8% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.7 | 64.2 | 96.3% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29 | 27.2 | 93.8% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 7.7 | 128.3% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 217.2 | 213 | 98.1% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.9 | 145.9 | 93.6% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 105.5 | 101.1 | 95.8% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 28.9 | 27 | 93.4% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7.1 | 7.5 | 105.6% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 254.8 | 278.8 | 109.4% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 157.3 | 151.4 | 96.2% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 93.1 | 89.9 | 96.6% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.7 | 31.6 | 93.8% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 4.4 | 107.3% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 525.8 | 534.4 | 101.6% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 349.1 | 329.6 | 94.4% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224.5 | 213.5 | 95.1% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.3 | 54.2 | 96.3% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3.1 | 2.9 | 93.5% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 149.9 | 144.7 | 96.5% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.4 | 101.6 | 93.7% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 76.8 | 73.6 | 95.8% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44.1 | 41.7 | 94.6% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.9 | 10.8 | 109.1% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 244.3 | 239.9 | 98.2% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 167.4 | 158.1 | 94.4% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.3 | 115 | 96.4% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.8 | 94.6 | 96.7% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.6 | 10.9 | 113.5% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 252.3 | 251.1 | 99.5% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.4 | 147.3 | 96.7% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.8 | 74 | 96.4% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.8 | 15.9 | 94.6% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.6 | 6 | 130.4% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 298.6 | 299 | 100.1% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 202.2 | 189.5 | 93.7% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.5 | 118.5 | 95.2% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 39.6 | 96.1% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 5.4 | 103.8% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 142.9 | 137.2 | 96.0% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.8 | 87.4 | 93.2% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.6 | 61.7 | 97.0% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.5 | 42.7 | 96.0% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.5 | 16.1 | 103.9% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 234.7 | 232.4 | 99.0% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 156.6 | 149.3 | 95.3% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93.7 | 89.2 | 95.2% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35.2 | 32.9 | 93.5% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 6.1 | 127.1% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 418.9 | 415.4 | 99.2% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 280.2 | 267.4 | 95.4% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 176 | 167.7 | 95.3% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.4 | 54.7 | 93.7% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.6 | 3.4 | 94.4% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 132.9 | 126 | 94.8% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 107.3 | 99.8 | 93.0% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 92.8 | 91.4 | 98.5% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 79 | 78.7 | 99.6% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.1 | 23.2 | 100.4% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 242.7 | 240.1 | 98.9% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 165.5 | 156.3 | 94.4% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 106.5 | 101.9 | 95.7% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.4 | 35.2 | 94.1% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5.1 | 5.4 | 105.9% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 99.3% | 94.8% (x-ray/whole) | FAIL |
| 3 | 13 | 94.6% | 93.0% (x-ray/whole) | FAIL |
| 6 | 13 | 96.1% | 95.1% (nci/whole) | FAIL |
| 9 | 13 | 95.1% | 93.4% (mozilla/whole) | FAIL |
| 12 | 13 | 108.9% | 93.5% (nci/whole) | FAIL |

Violations:
- level 1: geomean 99.3% < 100%
- level 1: x-ray/whole 94.8% < 95%
- level 3: geomean 94.6% < 100%
- level 3: mozilla/whole 93.6% < 95%
- level 3: nci/whole 94.4% < 95%
- level 3: ooffice/whole 93.7% < 95%
- level 3: osdb/whole 94.4% < 95%
- level 3: samba/whole 93.7% < 95%
- level 3: sao/whole 93.2% < 95%
- level 3: x-ray/whole 93.0% < 95%
- level 3: silesia.tar/1M 94.4% < 95%
- level 6: geomean 96.1% < 100%
- level 9: geomean 95.1% < 100%
- level 9: dickens/whole 93.8% < 95%
- level 9: mozilla/whole 93.4% < 95%
- level 9: mr/whole 93.8% < 95%
- level 9: ooffice/whole 94.6% < 95%
- level 9: reymont/whole 94.6% < 95%
- level 9: webster/whole 93.5% < 95%
- level 9: xml/whole 93.7% < 95%
- level 9: silesia.tar/1M 94.1% < 95%
- level 12: nci/whole 93.5% < 95%
- level 12: xml/whole 94.4% < 95%

OVERALL: FAIL

## Before (compare.sh output, gate summary)

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 99.1% | 94.7% (x-ray/whole) | FAIL |
| 3 | 13 | 94.7% | 92.5% (x-ray/whole) | FAIL |
| 6 | 13 | 96.0% | 94.9% (nci/whole) | FAIL |
| 9 | 13 | 95.0% | 92.8% (mozilla/whole) | FAIL |
| 12 | 13 | 94.7% | 82.8% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 99.1% < 100%
- level 1: x-ray/whole 94.7% < 95%
- level 3: geomean 94.7% < 100%
- level 3: mozilla/whole 93.5% < 95%
- level 3: nci/whole 94.6% < 95%
- level 3: ooffice/whole 93.5% < 95%
- level 3: osdb/whole 94.7% < 95%
- level 3: samba/whole 94.0% < 95%
- level 3: sao/whole 93.6% < 95%
- level 3: x-ray/whole 92.5% < 95%
- level 3: silesia.tar/1M 94.5% < 95%
- level 6: geomean 96.0% < 100%
- level 6: nci/whole 94.9% < 95%
- level 9: geomean 95.0% < 100%
- level 9: dickens/whole 93.8% < 95%
- level 9: mozilla/whole 92.8% < 95%
- level 9: mr/whole 93.8% < 95%
- level 9: ooffice/whole 94.3% < 95%
- level 9: reymont/whole 94.6% < 95%
- level 9: webster/whole 93.5% < 95%
- level 9: xml/whole 93.7% < 95%
- level 9: silesia.tar/1M 94.1% < 95%
- level 12: geomean 94.7% < 100%
- level 12: mozilla/whole 91.5% < 95%
- level 12: mr/whole 87.8% < 90% HARD FLOOR
- level 12: nci/whole 83.9% < 90% HARD FLOOR
- level 12: ooffice/whole 94.9% < 95%
- level 12: samba/whole 90.4% < 95%
- level 12: sao/whole 88.3% < 90% HARD FLOOR
- level 12: xml/whole 86.1% < 90% HARD FLOOR
- level 12: x-ray/whole 82.8% < 90% HARD FLOOR
- level 12: silesia.tar/1M 94.1% < 95%

OVERALL: FAIL
