# gzip deflate-compress-round1

- date: 2026-11-08T04:20:18+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (2% busy at start)
- load1 at start: 1.01, at end: 1.69; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode c, libs: ld ours, levels: 1 3 6 9; isal: not requested
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing

## Compression

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 205.5 | 141.6 | 68.9% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.3 | 87.4 | 72.7% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.8 | 50.9 | 76.2% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29.1 | 24.5 | 84.2% | 2.675 | 2.675 | - | - |
| mozilla | 1 | whole | 217.2 | 153.3 | 70.6% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.9 | 108.9 | 69.9% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 106.1 | 76.6 | 72.2% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 29.1 | 23.8 | 81.8% | 2.737 | 2.737 | - | - |
| mr | 1 | whole | 255 | 181.8 | 71.3% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 157.4 | 112.8 | 71.7% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 93 | 68.3 | 73.4% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.7 | 27.8 | 82.5% | 2.750 | 2.750 | - | - |
| nci | 1 | whole | 522.9 | 357.6 | 68.4% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 348.6 | 251 | 72.0% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224.3 | 167.4 | 74.6% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.3 | 47.5 | 84.4% | 11.470 | 11.470 | - | - |
| ooffice | 1 | whole | 149.8 | 106.8 | 71.3% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.5 | 77.6 | 71.5% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 78.6 | 57.8 | 73.5% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44 | 35.2 | 80.0% | 2.006 | 2.006 | - | - |
| osdb | 1 | whole | 244.7 | 174 | 71.1% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 167 | 122.3 | 73.2% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.4 | 90.8 | 76.0% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.9 | 76.5 | 78.1% | 2.760 | 2.760 | - | - |
| reymont | 1 | whole | 252.3 | 171.6 | 68.0% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.3 | 109.9 | 72.2% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.7 | 58 | 75.6% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.8 | 14.8 | 88.1% | 3.669 | 3.669 | - | - |
| samba | 1 | whole | 298.5 | 208.5 | 69.8% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 202.1 | 142.8 | 70.7% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.5 | 92.5 | 74.3% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 35.1 | 85.2% | 4.100 | 4.100 | - | - |
| sao | 1 | whole | 142.8 | 99 | 69.3% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.6 | 66.1 | 70.6% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.6 | 47.8 | 75.2% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.3 | 35.6 | 80.4% | 1.362 | 1.362 | - | - |
| webster | 1 | whole | 236.1 | 163.7 | 69.3% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 157 | 113.2 | 72.1% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93.8 | 70.7 | 75.4% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35.2 | 29.5 | 83.8% | 3.489 | 3.489 | - | - |
| xml | 1 | whole | 418.1 | 281.5 | 67.3% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 280.2 | 202.8 | 72.4% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 176 | 134.2 | 76.2% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.4 | 49.8 | 85.3% | 8.230 | 8.230 | - | - |
| x-ray | 1 | whole | 132.9 | 90.7 | 68.2% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 107.2 | 75.2 | 70.1% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 93 | 65 | 69.9% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 79.2 | 56.7 | 71.6% | 1.415 | 1.415 | - | - |
| silesia.tar | 1 | 1M | 243 | 169.5 | 69.8% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 165.5 | 118 | 71.3% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 106.8 | 79.1 | 74.1% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.4 | 31.1 | 83.2% | 3.169 | 3.169 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 69.5% | 67.3% (xml/whole) | FAIL (below hard floor) |
| 3 | 13 | 71.6% | 69.9% (mozilla/whole) | FAIL (below hard floor) |
| 6 | 13 | 74.3% | 69.9% (x-ray/whole) | FAIL (below hard floor) |
| 9 | 13 | 82.1% | 71.6% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 69.5% < 100%
- level 1: dickens/whole 68.9% < 90% HARD FLOOR
- level 1: mozilla/whole 70.6% < 90% HARD FLOOR
- level 1: mr/whole 71.3% < 90% HARD FLOOR
- level 1: nci/whole 68.4% < 90% HARD FLOOR
- level 1: ooffice/whole 71.3% < 90% HARD FLOOR
- level 1: osdb/whole 71.1% < 90% HARD FLOOR
- level 1: reymont/whole 68.0% < 90% HARD FLOOR
- level 1: samba/whole 69.8% < 90% HARD FLOOR
- level 1: sao/whole 69.3% < 90% HARD FLOOR
- level 1: webster/whole 69.3% < 90% HARD FLOOR
- level 1: xml/whole 67.3% < 90% HARD FLOOR
- level 1: x-ray/whole 68.2% < 90% HARD FLOOR
- level 1: silesia.tar/1M 69.8% < 90% HARD FLOOR
- level 3: geomean 71.6% < 100%
- level 3: dickens/whole 72.7% < 90% HARD FLOOR
- level 3: mozilla/whole 69.9% < 90% HARD FLOOR
- level 3: mr/whole 71.7% < 90% HARD FLOOR
- level 3: nci/whole 72.0% < 90% HARD FLOOR
- level 3: ooffice/whole 71.5% < 90% HARD FLOOR
- level 3: osdb/whole 73.2% < 90% HARD FLOOR
- level 3: reymont/whole 72.2% < 90% HARD FLOOR
- level 3: samba/whole 70.7% < 90% HARD FLOOR
- level 3: sao/whole 70.6% < 90% HARD FLOOR
- level 3: webster/whole 72.1% < 90% HARD FLOOR
- level 3: xml/whole 72.4% < 90% HARD FLOOR
- level 3: x-ray/whole 70.1% < 90% HARD FLOOR
- level 3: silesia.tar/1M 71.3% < 90% HARD FLOOR
- level 6: geomean 74.3% < 100%
- level 6: dickens/whole 76.2% < 90% HARD FLOOR
- level 6: mozilla/whole 72.2% < 90% HARD FLOOR
- level 6: mr/whole 73.4% < 90% HARD FLOOR
- level 6: nci/whole 74.6% < 90% HARD FLOOR
- level 6: ooffice/whole 73.5% < 90% HARD FLOOR
- level 6: osdb/whole 76.0% < 90% HARD FLOOR
- level 6: reymont/whole 75.6% < 90% HARD FLOOR
- level 6: samba/whole 74.3% < 90% HARD FLOOR
- level 6: sao/whole 75.2% < 90% HARD FLOOR
- level 6: webster/whole 75.4% < 90% HARD FLOOR
- level 6: xml/whole 76.2% < 90% HARD FLOOR
- level 6: x-ray/whole 69.9% < 90% HARD FLOOR
- level 6: silesia.tar/1M 74.1% < 90% HARD FLOOR
- level 9: geomean 82.1% < 100%
- level 9: dickens/whole 84.2% < 90% HARD FLOOR
- level 9: mozilla/whole 81.8% < 90% HARD FLOOR
- level 9: mr/whole 82.5% < 90% HARD FLOOR
- level 9: nci/whole 84.4% < 90% HARD FLOOR
- level 9: ooffice/whole 80.0% < 90% HARD FLOOR
- level 9: osdb/whole 78.1% < 90% HARD FLOOR
- level 9: reymont/whole 88.1% < 90% HARD FLOOR
- level 9: samba/whole 85.2% < 90% HARD FLOOR
- level 9: sao/whole 80.4% < 90% HARD FLOOR
- level 9: webster/whole 83.8% < 90% HARD FLOOR
- level 9: xml/whole 85.3% < 90% HARD FLOOR
- level 9: x-ray/whole 71.6% < 90% HARD FLOOR
- level 9: silesia.tar/1M 83.2% < 90% HARD FLOOR

OVERALL: FAIL

## Notes (T7.3, hand-written after the run)
- Code: the uncommitted T7.1 compressor changes (SSE2 matchfinder init/rebase, bounded loads, register bit writer,
  level-1 ht_matchfinder written out in the loop) plus the T7.3 test-only state fields. Run via the T6.1 wrapper
  (flock + `systemd-run --scope -p MemoryMax=4G -p MemorySwapMax=0`, no GOMEMLIMIT), libs `ld ours`.
- Preconditions at start: AC0=1, load1 1.01, no test/bench/fuzz process. uptime before 03:59:49 load 1.01/1.41/1.54;
  after 04:20:18 load 1.69/1.81/1.77; rc 0. Per-row load1 1.0-2.44 (the benchmark's own pinned core accounts for
  ~1; nothing else above 8% CPU when checked at 2.4).
- Byte identity: 52/52 rows identical to libdeflate (no MISMATCH).
- Gate verdict: **FAIL at every level** (geomean >= 100, rows >= 95, floor 90): L1 69.5% (min 67.3% xml),
  L3 71.6% (min 69.9% mozilla), L6 74.3% (min 69.9% x-ray), L9 82.1% (min 71.6% x-ray); every row below the 90% floor.
- vs the pre-T7.1 provisional run (bench/results/2026-11-06-deflate-compress-provisional.md, contaminated): L1 52.5 ->
  69.5, L3 54.4 -> 71.6, L6 60.5 -> 74.3, L9 74.0 -> 82.1. The level-1 inlining is kept (revert only if L1 < 52.5%).
- L12, same day, separate file bench/results/2026-11-08-deflate-compress-l12-round1.md (uptime before 04:20:54 load
  1.92/1.87/1.79, after 04:39:55 load 1.79/1.98/1.93, rc 0, per-row load1 up to 2.34, 13/13 byte-identical): geomean
  95.0% (T6.1: 89.1%), min 81.9% x-ray: FAIL (below floor). At 3-23 MB/s the cells have one decimal (up to ~3% rounding).
- Profiles and the optimisation shortlist: .superpowers/sdd/gzip-libdeflate/T7.3.md.
