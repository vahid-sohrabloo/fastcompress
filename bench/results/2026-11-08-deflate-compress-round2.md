# gzip deflate-compress-round2

- date: 2026-11-08T08:36:22+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (6% busy at start)
- load1 at start: 1.71, at end: 2.00; AC power online: 1
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
| dickens | 1 | whole | 205.3 | 150.9 | 73.5% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.4 | 92.7 | 77.0% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.8 | 51.4 | 76.9% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29 | 24.7 | 85.2% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 6.9 | 115.0% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 217.2 | 159.7 | 73.5% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.8 | 116.4 | 74.7% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 106.1 | 77.3 | 72.9% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 29.1 | 24.2 | 83.2% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7.1 | 6.5 | 91.5% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 254.9 | 194.9 | 76.5% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 157 | 118.1 | 75.2% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 93 | 69.3 | 74.5% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.7 | 28.3 | 84.0% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 3.5 | 85.4% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 524.1 | 392 | 74.8% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 348.1 | 273.5 | 78.6% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224.2 | 175.2 | 78.1% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.3 | 48.6 | 86.3% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3.1 | 2.6 | 83.9% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 150 | 111 | 74.0% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.6 | 82.6 | 76.1% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 78.6 | 58 | 73.8% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44.1 | 35.3 | 80.0% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.9 | 9.4 | 94.9% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 243.9 | 180.1 | 73.8% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 167.3 | 129.8 | 77.6% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.4 | 91.5 | 76.6% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.7 | 76.6 | 78.4% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.7 | 9.5 | 97.9% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 252.6 | 184.8 | 73.2% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.6 | 115.9 | 76.0% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.7 | 58.8 | 76.7% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.8 | 15 | 89.3% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.6 | 5.4 | 117.4% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 299 | 222 | 74.2% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 202.3 | 153.8 | 76.0% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.6 | 94 | 75.4% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 35.7 | 86.7% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 4.7 | 90.4% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 142.8 | 101.9 | 71.4% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.6 | 70.5 | 75.3% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.6 | 47.5 | 74.7% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.5 | 35.4 | 79.6% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.5 | 13.7 | 88.4% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 236.1 | 174.4 | 73.9% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 156.9 | 120.9 | 77.1% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93.7 | 71.6 | 76.4% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35.2 | 29.9 | 84.9% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 5.5 | 114.6% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 418.1 | 305.9 | 73.2% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 280.4 | 218 | 77.7% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 175.9 | 138.4 | 78.7% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.3 | 50.5 | 86.6% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.6 | 3.1 | 86.1% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 133 | 92.3 | 69.4% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 107.5 | 80.2 | 74.6% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 92.9 | 64.7 | 69.6% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 79.4 | 56.3 | 70.9% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.2 | 19.3 | 83.2% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 243 | 178.4 | 73.4% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 165.2 | 125.8 | 76.2% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 106.6 | 79.8 | 74.9% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.4 | 31.5 | 84.2% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5.1 | 4.8 | 94.1% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 73.4% | 69.4% (x-ray/whole) | FAIL (below hard floor) |
| 3 | 13 | 76.3% | 74.6% (x-ray/whole) | FAIL (below hard floor) |
| 6 | 13 | 75.3% | 69.6% (x-ray/whole) | FAIL (below hard floor) |
| 9 | 13 | 82.9% | 70.9% (x-ray/whole) | FAIL (below hard floor) |
| 12 | 13 | 94.9% | 83.2% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 73.4% < 100%
- level 1: dickens/whole 73.5% < 90% HARD FLOOR
- level 1: mozilla/whole 73.5% < 90% HARD FLOOR
- level 1: mr/whole 76.5% < 90% HARD FLOOR
- level 1: nci/whole 74.8% < 90% HARD FLOOR
- level 1: ooffice/whole 74.0% < 90% HARD FLOOR
- level 1: osdb/whole 73.8% < 90% HARD FLOOR
- level 1: reymont/whole 73.2% < 90% HARD FLOOR
- level 1: samba/whole 74.2% < 90% HARD FLOOR
- level 1: sao/whole 71.4% < 90% HARD FLOOR
- level 1: webster/whole 73.9% < 90% HARD FLOOR
- level 1: xml/whole 73.2% < 90% HARD FLOOR
- level 1: x-ray/whole 69.4% < 90% HARD FLOOR
- level 1: silesia.tar/1M 73.4% < 90% HARD FLOOR
- level 3: geomean 76.3% < 100%
- level 3: dickens/whole 77.0% < 90% HARD FLOOR
- level 3: mozilla/whole 74.7% < 90% HARD FLOOR
- level 3: mr/whole 75.2% < 90% HARD FLOOR
- level 3: nci/whole 78.6% < 90% HARD FLOOR
- level 3: ooffice/whole 76.1% < 90% HARD FLOOR
- level 3: osdb/whole 77.6% < 90% HARD FLOOR
- level 3: reymont/whole 76.0% < 90% HARD FLOOR
- level 3: samba/whole 76.0% < 90% HARD FLOOR
- level 3: sao/whole 75.3% < 90% HARD FLOOR
- level 3: webster/whole 77.1% < 90% HARD FLOOR
- level 3: xml/whole 77.7% < 90% HARD FLOOR
- level 3: x-ray/whole 74.6% < 90% HARD FLOOR
- level 3: silesia.tar/1M 76.2% < 90% HARD FLOOR
- level 6: geomean 75.3% < 100%
- level 6: dickens/whole 76.9% < 90% HARD FLOOR
- level 6: mozilla/whole 72.9% < 90% HARD FLOOR
- level 6: mr/whole 74.5% < 90% HARD FLOOR
- level 6: nci/whole 78.1% < 90% HARD FLOOR
- level 6: ooffice/whole 73.8% < 90% HARD FLOOR
- level 6: osdb/whole 76.6% < 90% HARD FLOOR
- level 6: reymont/whole 76.7% < 90% HARD FLOOR
- level 6: samba/whole 75.4% < 90% HARD FLOOR
- level 6: sao/whole 74.7% < 90% HARD FLOOR
- level 6: webster/whole 76.4% < 90% HARD FLOOR
- level 6: xml/whole 78.7% < 90% HARD FLOOR
- level 6: x-ray/whole 69.6% < 90% HARD FLOOR
- level 6: silesia.tar/1M 74.9% < 90% HARD FLOOR
- level 9: geomean 82.9% < 100%
- level 9: dickens/whole 85.2% < 90% HARD FLOOR
- level 9: mozilla/whole 83.2% < 90% HARD FLOOR
- level 9: mr/whole 84.0% < 90% HARD FLOOR
- level 9: nci/whole 86.3% < 90% HARD FLOOR
- level 9: ooffice/whole 80.0% < 90% HARD FLOOR
- level 9: osdb/whole 78.4% < 90% HARD FLOOR
- level 9: reymont/whole 89.3% < 90% HARD FLOOR
- level 9: samba/whole 86.7% < 90% HARD FLOOR
- level 9: sao/whole 79.6% < 90% HARD FLOOR
- level 9: webster/whole 84.9% < 90% HARD FLOOR
- level 9: xml/whole 86.6% < 90% HARD FLOOR
- level 9: x-ray/whole 70.9% < 90% HARD FLOOR
- level 9: silesia.tar/1M 84.2% < 90% HARD FLOOR
- level 12: geomean 94.9% < 100%
- level 12: mozilla/whole 91.5% < 95%
- level 12: mr/whole 85.4% < 90% HARD FLOOR
- level 12: nci/whole 83.9% < 90% HARD FLOOR
- level 12: ooffice/whole 94.9% < 95%
- level 12: samba/whole 90.4% < 95%
- level 12: sao/whole 88.4% < 90% HARD FLOOR
- level 12: xml/whole 86.1% < 90% HARD FLOOR
- level 12: x-ray/whole 83.2% < 90% HARD FLOOR
- level 12: silesia.tar/1M 94.1% < 95%

OVERALL: FAIL

## Notes (T8.1, hand-written after the run)
- Code: the T7.3 tree plus the T8.1 compressor changes (ledger .superpowers/sdd/gzip-libdeflate/T8.1.md): findMinCostPath
  inner loop (node window + locals), bounds-check-free skip loops (hc.skipBytes, level 1), level 1 deflate_choose_match and
  the first lzExtend word written out, greedy (levels 2..4) with hc_matchfinder_longest_match / skip_bytes written out.
  Lazy (5..9) and near-optimal matchfinding unchanged. Run via the T6.1 wrapper (flock + `systemd-run --scope -p
  MemoryMax=4G -p MemorySwapMax=0`, no GOMEMLIMIT), libs `ld ours`, after all differential tests passed.
- Preconditions at start: AC0=1, load1 1.71, no test/bench/fuzz process. uptime before 07:56:53 load 1.71/1.79/1.87;
  after 08:36:22 load 2.00/2.14/1.94; rc 0. Per-row load1 1.38-2.58 (2.0-2.58 around mozilla L12 and reymont/samba;
  the benchmark's own pinned core is ~1 of it).
- Byte identity: 65/65 rows identical to libdeflate (no MISMATCH).
- Gate verdict: **FAIL at every level**. vs round 1 (same day, same files/settings; L12 from the separate l12-round1 file):
  L1 69.5 -> 73.4 (min 69.4 x-ray), L3 71.6 -> 76.3 (min 74.6 x-ray), L6 74.3 -> 75.3 (min 69.6 x-ray), L9 82.1 -> 82.9
  (min 70.9 x-ray), L12 95.0 -> 94.9 (min 83.2 x-ray; the L12 cells have one decimal at 3-23 MB/s, ~3% rounding, so L12
  is unchanged within resolution: the in-package A/B measured +0.8..+1.6%).
