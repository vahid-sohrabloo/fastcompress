# gzip deflate-compress-round4

- date: 2026-11-08T19:15:49+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (4% busy at start)
- load1 at start: 1.38, at end: 2.62; AC power online: 1
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
| dickens | 1 | whole | 205.5 | 205.1 | 99.8% | 2.417 | 2.417 | - | - |
| dickens | 3 | whole | 120.3 | 113.3 | 94.2% | 2.555 | 2.555 | - | - |
| dickens | 6 | whole | 66.7 | 61.6 | 92.4% | 2.641 | 2.641 | - | - |
| dickens | 9 | whole | 29 | 26.9 | 92.8% | 2.675 | 2.675 | - | - |
| dickens | 12 | whole | 6 | 6.8 | 113.3% | 2.770 | 2.770 | - | - |
| mozilla | 1 | whole | 217 | 213.5 | 98.4% | 2.537 | 2.537 | - | - |
| mozilla | 3 | whole | 155.5 | 143.8 | 92.5% | 2.663 | 2.663 | - | - |
| mozilla | 6 | whole | 105.8 | 97.9 | 92.5% | 2.724 | 2.724 | - | - |
| mozilla | 9 | whole | 28.8 | 26.7 | 92.7% | 2.737 | 2.737 | - | - |
| mozilla | 12 | whole | 7 | 6.5 | 92.9% | 2.808 | 2.808 | - | - |
| mr | 1 | whole | 255.1 | 278.5 | 109.2% | 2.665 | 2.665 | - | - |
| mr | 3 | whole | 156.2 | 148.6 | 95.1% | 2.715 | 2.715 | - | - |
| mr | 6 | whole | 92.9 | 86.3 | 92.9% | 2.733 | 2.733 | - | - |
| mr | 9 | whole | 33.4 | 31.1 | 93.1% | 2.750 | 2.750 | - | - |
| mr | 12 | whole | 4.1 | 3.6 | 87.8% | 2.892 | 2.892 | - | - |
| nci | 1 | whole | 524.6 | 535.5 | 102.1% | 8.743 | 8.743 | - | - |
| nci | 3 | whole | 348.8 | 327.4 | 93.9% | 9.322 | 9.322 | - | - |
| nci | 6 | whole | 224 | 208 | 92.9% | 10.853 | 10.853 | - | - |
| nci | 9 | whole | 56.1 | 53.6 | 95.5% | 11.470 | 11.470 | - | - |
| nci | 12 | whole | 3 | 2.6 | 86.7% | 12.209 | 12.209 | - | - |
| ooffice | 1 | whole | 149.8 | 144.9 | 96.7% | 1.878 | 1.878 | - | - |
| ooffice | 3 | whole | 108.5 | 100.1 | 92.3% | 1.961 | 1.961 | - | - |
| ooffice | 6 | whole | 78.4 | 73.1 | 93.2% | 2.002 | 2.002 | - | - |
| ooffice | 9 | whole | 44 | 40.9 | 93.0% | 2.006 | 2.006 | - | - |
| ooffice | 12 | whole | 9.9 | 9.5 | 96.0% | 2.055 | 2.055 | - | - |
| osdb | 1 | whole | 244.7 | 240.2 | 98.2% | 2.607 | 2.607 | - | - |
| osdb | 3 | whole | 166.9 | 157.4 | 94.3% | 2.641 | 2.641 | - | - |
| osdb | 6 | whole | 119.3 | 112.6 | 94.4% | 2.755 | 2.755 | - | - |
| osdb | 9 | whole | 97.8 | 91.7 | 93.8% | 2.760 | 2.760 | - | - |
| osdb | 12 | whole | 9.7 | 9.5 | 97.9% | 2.795 | 2.795 | - | - |
| reymont | 1 | whole | 252.7 | 251 | 99.3% | 3.016 | 3.016 | - | - |
| reymont | 3 | whole | 152.4 | 145 | 95.1% | 3.279 | 3.279 | - | - |
| reymont | 6 | whole | 76.8 | 70.7 | 92.1% | 3.532 | 3.532 | - | - |
| reymont | 9 | whole | 16.8 | 15.8 | 94.0% | 3.669 | 3.669 | - | - |
| reymont | 12 | whole | 4.6 | 5.3 | 115.2% | 3.906 | 3.906 | - | - |
| samba | 1 | whole | 298 | 299.1 | 100.4% | 3.683 | 3.683 | - | - |
| samba | 3 | whole | 202.2 | 188.6 | 93.3% | 3.854 | 3.854 | - | - |
| samba | 6 | whole | 124.5 | 114.9 | 92.3% | 4.048 | 4.048 | - | - |
| samba | 9 | whole | 41.2 | 39.1 | 94.9% | 4.100 | 4.100 | - | - |
| samba | 12 | whole | 5.2 | 4.7 | 90.4% | 4.222 | 4.222 | - | - |
| sao | 1 | whole | 142.8 | 137.1 | 96.0% | 1.301 | 1.301 | - | - |
| sao | 3 | whole | 93.7 | 86.9 | 92.7% | 1.343 | 1.343 | - | - |
| sao | 6 | whole | 63.6 | 60.1 | 94.5% | 1.359 | 1.359 | - | - |
| sao | 9 | whole | 44.4 | 41.8 | 94.1% | 1.362 | 1.362 | - | - |
| sao | 12 | whole | 15.5 | 13.7 | 88.4% | 1.381 | 1.381 | - | - |
| webster | 1 | whole | 236.1 | 233.2 | 98.8% | 3.060 | 3.060 | - | - |
| webster | 3 | whole | 156.8 | 146.7 | 93.6% | 3.229 | 3.229 | - | - |
| webster | 6 | whole | 93 | 85.5 | 91.9% | 3.415 | 3.415 | - | - |
| webster | 9 | whole | 35 | 32.4 | 92.6% | 3.489 | 3.489 | - | - |
| webster | 12 | whole | 4.8 | 5.4 | 112.5% | 3.599 | 3.599 | - | - |
| xml | 1 | whole | 416.1 | 413.8 | 99.4% | 6.021 | 6.021 | - | - |
| xml | 3 | whole | 279.3 | 264.4 | 94.7% | 6.737 | 6.737 | - | - |
| xml | 6 | whole | 175.1 | 163 | 93.1% | 7.810 | 7.810 | - | - |
| xml | 9 | whole | 58.2 | 54.2 | 93.1% | 8.230 | 8.230 | - | - |
| xml | 12 | whole | 3.6 | 3.1 | 86.1% | 8.493 | 8.493 | - | - |
| x-ray | 1 | whole | 132.8 | 126.1 | 95.0% | 1.347 | 1.347 | - | - |
| x-ray | 3 | whole | 106.5 | 99.4 | 93.3% | 1.399 | 1.399 | - | - |
| x-ray | 6 | whole | 92.1 | 87.8 | 95.3% | 1.413 | 1.413 | - | - |
| x-ray | 9 | whole | 78.8 | 75.1 | 95.3% | 1.415 | 1.415 | - | - |
| x-ray | 12 | whole | 23.1 | 19 | 82.3% | 1.475 | 1.475 | - | - |
| silesia.tar | 1 | 1M | 241.8 | 239.4 | 99.0% | 2.878 | 2.878 | - | - |
| silesia.tar | 3 | 1M | 164.7 | 153.4 | 93.1% | 3.014 | 3.014 | - | - |
| silesia.tar | 6 | 1M | 105.8 | 98.2 | 92.8% | 3.132 | 3.132 | - | - |
| silesia.tar | 9 | 1M | 37.1 | 34.7 | 93.5% | 3.169 | 3.169 | - | - |
| silesia.tar | 12 | 1M | 5 | 4.8 | 96.0% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 13 | 99.4% | 95.0% (x-ray/whole) | FAIL |
| 3 | 13 | 93.7% | 92.3% (ooffice/whole) | FAIL |
| 6 | 13 | 93.1% | 91.9% (webster/whole) | FAIL |
| 9 | 13 | 93.7% | 92.6% (webster/whole) | FAIL |
| 12 | 13 | 95.2% | 82.3% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 99.4% < 100%
- level 3: geomean 93.7% < 100%
- level 3: dickens/whole 94.2% < 95%
- level 3: mozilla/whole 92.5% < 95%
- level 3: nci/whole 93.9% < 95%
- level 3: ooffice/whole 92.3% < 95%
- level 3: osdb/whole 94.3% < 95%
- level 3: samba/whole 93.3% < 95%
- level 3: sao/whole 92.7% < 95%
- level 3: webster/whole 93.6% < 95%
- level 3: xml/whole 94.7% < 95%
- level 3: x-ray/whole 93.3% < 95%
- level 3: silesia.tar/1M 93.1% < 95%
- level 6: geomean 93.1% < 100%
- level 6: dickens/whole 92.4% < 95%
- level 6: mozilla/whole 92.5% < 95%
- level 6: mr/whole 92.9% < 95%
- level 6: nci/whole 92.9% < 95%
- level 6: ooffice/whole 93.2% < 95%
- level 6: osdb/whole 94.4% < 95%
- level 6: reymont/whole 92.1% < 95%
- level 6: samba/whole 92.3% < 95%
- level 6: sao/whole 94.5% < 95%
- level 6: webster/whole 91.9% < 95%
- level 6: xml/whole 93.1% < 95%
- level 6: silesia.tar/1M 92.8% < 95%
- level 9: geomean 93.7% < 100%
- level 9: dickens/whole 92.8% < 95%
- level 9: mozilla/whole 92.7% < 95%
- level 9: mr/whole 93.1% < 95%
- level 9: ooffice/whole 93.0% < 95%
- level 9: osdb/whole 93.8% < 95%
- level 9: reymont/whole 94.0% < 95%
- level 9: samba/whole 94.9% < 95%
- level 9: sao/whole 94.1% < 95%
- level 9: webster/whole 92.6% < 95%
- level 9: xml/whole 93.1% < 95%
- level 9: silesia.tar/1M 93.5% < 95%
- level 12: geomean 95.2% < 100%
- level 12: mozilla/whole 92.9% < 95%
- level 12: mr/whole 87.8% < 90% HARD FLOOR
- level 12: nci/whole 86.7% < 90% HARD FLOOR
- level 12: samba/whole 90.4% < 95%
- level 12: sao/whole 88.4% < 90% HARD FLOOR
- level 12: xml/whole 86.1% < 90% HARD FLOOR
- level 12: x-ray/whole 82.3% < 90% HARD FLOOR

OVERALL: FAIL

## Notes (T10.1, hand-written after the run)
- Code: the T9.1/T9.2 tree plus T10.1 (ledger .superpowers/sdd/gzip-libdeflate/T10.1.md): writeSequencesAsm (the
  sequence output of deflate_flush_block in amd64 assembly, levels 1..9) and C's two ht_matchfinder prefetchw in
  fastestLoopAsm (level 1). Levels 10..12 unchanged. Run via the T6.1 wrapper (flock + `systemd-run --scope -p
  MemoryMax=4G -p MemorySwapMax=0`, no GOMEMLIMIT), libs `ld ours`, after all differential tests passed (asm on and off).
- Preconditions at start: AC0=1, load1 1.38, no test/bench/fuzz process (the run waited 18:00-18:36 for another
  session's tests and the load). uptime before 18:36:24 load 1.38/2.06/2.32; after 19:15:49 load 2.62/2.61/2.78; rc 0.
- Byte identity: 65/65 rows identical to libdeflate (no MISMATCH row).
- Gate verdict: **FAIL at every level**. vs round 3: L1 87.4 -> 99.4 (min 82.0 -> 95.0 x-ray; all 13 rows >= 95%, geomean 0.6 short of 100%),
  L3 89.4 -> 93.7 (min 87.3 -> 92.3 ooffice), L6 90.2 -> 93.1 (min 89.0 -> 91.9 webster), L9 92.2 -> 93.7 (min 90.4 ->
  92.6 webster), L12 94.8 -> 95.2 (unchanged code; min 82.3 x-ray). No row below the 90% floor at L1..L9.
