# gzip deflate-compress-round3

- date: 2026-11-08T13:00:35+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (0% busy at start)
- load1 at start: 0.84, at end: 1.65; AC power online: 1
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
| dickens | 1 | whole | 204.2 | 177.5 | 86.9% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.6 | 108.5 | 90.0% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.8 | 60.2 | 90.1% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29 | 26.5 | 91.4% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 6.8 | 113.3% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 216.3 | 187 | 86.5% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.2 | 136.8 | 88.1% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 105.7 | 94.5 | 89.4% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 29.1 | 26.5 | 91.1% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7.1 | 6.5 | 91.5% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 255 | 243 | 95.3% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 157.3 | 140.7 | 89.4% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 92.9 | 83.2 | 89.6% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.6 | 30.8 | 91.7% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 3.5 | 85.4% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 524.3 | 480 | 91.6% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 347.5 | 315.8 | 90.9% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224.4 | 203.7 | 90.8% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.3 | 53.6 | 95.2% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3.1 | 2.6 | 83.9% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 149.7 | 127.3 | 85.0% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.1 | 94.7 | 87.6% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 78.4 | 70.4 | 89.8% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44 | 40.1 | 91.1% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.8 | 9.4 | 95.9% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 244.3 | 213.1 | 87.2% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 167.2 | 150.3 | 89.9% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.3 | 109 | 91.4% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.6 | 89.3 | 91.5% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.6 | 9.4 | 97.9% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 252.4 | 219.2 | 86.8% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.2 | 138.1 | 90.7% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.5 | 69 | 90.2% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.7 | 15.7 | 94.0% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.6 | 5.3 | 115.2% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 297.8 | 264.5 | 88.8% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 201.9 | 180.4 | 89.4% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.3 | 112 | 90.1% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 38.8 | 94.2% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 4.7 | 90.4% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 142.5 | 120.2 | 84.4% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.5 | 82.7 | 88.4% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.5 | 58.1 | 91.5% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.4 | 40.8 | 91.9% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.4 | 13.5 | 87.7% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 236.2 | 204.9 | 86.7% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 156.5 | 140.2 | 89.6% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93.4 | 84 | 89.9% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35.1 | 32.1 | 91.5% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 5.5 | 114.6% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 418.1 | 371.7 | 88.9% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 280.8 | 256.2 | 91.2% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 176 | 159.9 | 90.9% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.3 | 54 | 92.6% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.5 | 3.1 | 88.6% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 132.5 | 108.7 | 82.0% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 106.9 | 93.3 | 87.3% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 92.8 | 82.6 | 89.0% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 79.1 | 71.5 | 90.4% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.2 | 19.1 | 82.3% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 242.9 | 209.7 | 86.3% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 165.1 | 147.3 | 89.2% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 106.5 | 96 | 90.1% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.4 | 34.4 | 92.0% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5.1 | 4.8 | 94.1% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 87.4% | 82.0% (x-ray/whole) | FAIL (below hard floor) |
| 3 | 13 | 89.4% | 87.3% (x-ray/whole) | FAIL (below hard floor) |
| 6 | 13 | 90.2% | 89.0% (x-ray/whole) | FAIL (below hard floor) |
| 9 | 13 | 92.2% | 90.4% (x-ray/whole) | FAIL |
| 12 | 13 | 94.8% | 82.3% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 87.4% < 100%
- level 1: dickens/whole 86.9% < 90% HARD FLOOR
- level 1: mozilla/whole 86.5% < 90% HARD FLOOR
- level 1: nci/whole 91.6% < 95%
- level 1: ooffice/whole 85.0% < 90% HARD FLOOR
- level 1: osdb/whole 87.2% < 90% HARD FLOOR
- level 1: reymont/whole 86.8% < 90% HARD FLOOR
- level 1: samba/whole 88.8% < 90% HARD FLOOR
- level 1: sao/whole 84.4% < 90% HARD FLOOR
- level 1: webster/whole 86.7% < 90% HARD FLOOR
- level 1: xml/whole 88.9% < 90% HARD FLOOR
- level 1: x-ray/whole 82.0% < 90% HARD FLOOR
- level 1: silesia.tar/1M 86.3% < 90% HARD FLOOR
- level 3: geomean 89.4% < 100%
- level 3: dickens/whole 90.0% < 95%
- level 3: mozilla/whole 88.1% < 90% HARD FLOOR
- level 3: mr/whole 89.4% < 90% HARD FLOOR
- level 3: nci/whole 90.9% < 95%
- level 3: ooffice/whole 87.6% < 90% HARD FLOOR
- level 3: osdb/whole 89.9% < 90% HARD FLOOR
- level 3: reymont/whole 90.7% < 95%
- level 3: samba/whole 89.4% < 90% HARD FLOOR
- level 3: sao/whole 88.4% < 90% HARD FLOOR
- level 3: webster/whole 89.6% < 90% HARD FLOOR
- level 3: xml/whole 91.2% < 95%
- level 3: x-ray/whole 87.3% < 90% HARD FLOOR
- level 3: silesia.tar/1M 89.2% < 90% HARD FLOOR
- level 6: geomean 90.2% < 100%
- level 6: dickens/whole 90.1% < 95%
- level 6: mozilla/whole 89.4% < 90% HARD FLOOR
- level 6: mr/whole 89.6% < 90% HARD FLOOR
- level 6: nci/whole 90.8% < 95%
- level 6: ooffice/whole 89.8% < 90% HARD FLOOR
- level 6: osdb/whole 91.4% < 95%
- level 6: reymont/whole 90.2% < 95%
- level 6: samba/whole 90.1% < 95%
- level 6: sao/whole 91.5% < 95%
- level 6: webster/whole 89.9% < 90% HARD FLOOR
- level 6: xml/whole 90.9% < 95%
- level 6: x-ray/whole 89.0% < 90% HARD FLOOR
- level 6: silesia.tar/1M 90.1% < 95%
- level 9: geomean 92.2% < 100%
- level 9: dickens/whole 91.4% < 95%
- level 9: mozilla/whole 91.1% < 95%
- level 9: mr/whole 91.7% < 95%
- level 9: ooffice/whole 91.1% < 95%
- level 9: osdb/whole 91.5% < 95%
- level 9: reymont/whole 94.0% < 95%
- level 9: samba/whole 94.2% < 95%
- level 9: sao/whole 91.9% < 95%
- level 9: webster/whole 91.5% < 95%
- level 9: xml/whole 92.6% < 95%
- level 9: x-ray/whole 90.4% < 95%
- level 9: silesia.tar/1M 92.0% < 95%
- level 12: geomean 94.8% < 100%
- level 12: mozilla/whole 91.5% < 95%
- level 12: mr/whole 85.4% < 90% HARD FLOOR
- level 12: nci/whole 83.9% < 90% HARD FLOOR
- level 12: samba/whole 90.4% < 95%
- level 12: sao/whole 87.7% < 90% HARD FLOOR
- level 12: xml/whole 88.6% < 90% HARD FLOOR
- level 12: x-ray/whole 82.3% < 90% HARD FLOOR
- level 12: silesia.tar/1M 94.1% < 95%

OVERALL: FAIL

## Notes (T9.1, hand-written after the run)
- Code: the T8.1 tree plus the T9.1 amd64 assembly (ledger .superpowers/sdd/gzip-libdeflate/T9.1.md): the whole
  per-position loops of levels 1 (fastestLoopAsm), 2..4 (greedyLoopAsm) and 5..9 (lazyLoopAsm) with the hash-chain
  search (hcsearch<>, C's hc_matchfinder_longest_match with lz_extend, the window slide and the two prefetches) written
  in. Levels 10..12 unchanged. Run via the T6.1 wrapper (flock + `systemd-run --scope -p MemoryMax=4G -p
  MemorySwapMax=0`, no GOMEMLIMIT), libs `ld ours`, after all differential tests passed (asm on and off).
- Preconditions at start: AC0=1, load1 0.84, no test/bench/fuzz process. uptime before 12:21:15 load 0.84/1.66/2.09;
  after 13:00:35 load 1.65/1.98/2.20; rc 0. Per-row load1 1.65-3.37 (chrome used up to ~80% of a core during part of
  the run; C and ours are interleaved per row and the C MB/s are within 0.5% of round 2).
- Byte identity: 65/65 rows identical to libdeflate (no MISMATCH).
- Gate verdict: **FAIL at every level**. vs round 2: L1 73.4 -> 87.4 (min 82.0 x-ray), L3 76.3 -> 89.4 (min 87.3 x-ray),
  L6 75.3 -> 90.2 (min 89.0 x-ray), L9 82.9 -> 92.2 (min 90.4 x-ray; all 13 rows >= 90%), L12 94.9 -> 94.8 (unchanged
  code; min 82.3 x-ray).
