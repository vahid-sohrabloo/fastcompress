# gzip deflate-decode-recheck-02-after

- date: 2026-11-09T06:40:18+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (0% busy at start)
- load1 at start: 1.90, at end: 1.80; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M silesia.tar:128K silesia.tar:16K 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode d, libs: ld ours, levels: 1 6 9 12; isal: not requested
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- identity check (-mode d): none by size (ours decodes the C streams, so the Go ratio cell is `=C`, not a measurement); integrity = the round trip verified by gzbench before timing (exact-length decode of every block, then byte equality with the input)
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing

## Decompression

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio | stdlib D-MB/s | klauspost D-MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 817.4 | 844.6 | 103.3% | 2.417 | =C (same stream) | - | - |
| dickens | 6 | whole | 863.1 | 895.2 | 103.7% | 2.641 | =C (same stream) | - | - |
| dickens | 9 | whole | 800.1 | 824.1 | 103.0% | 2.675 | =C (same stream) | - | - |
| dickens | 12 | whole | 988.4 | 1030.3 | 104.2% | 2.770 | =C (same stream) | - | - |
| mozilla | 1 | whole | 818 | 807.5 | 98.7% | 2.537 | =C (same stream) | - | - |
| mozilla | 6 | whole | 843.6 | 822.8 | 97.5% | 2.724 | =C (same stream) | - | - |
| mozilla | 9 | whole | 851.9 | 832 | 97.7% | 2.737 | =C (same stream) | - | - |
| mozilla | 12 | whole | 833.6 | 808.7 | 97.0% | 2.808 | =C (same stream) | - | - |
| mr | 1 | whole | 896.7 | 894.2 | 99.7% | 2.665 | =C (same stream) | - | - |
| mr | 6 | whole | 989.9 | 999.6 | 101.0% | 2.733 | =C (same stream) | - | - |
| mr | 9 | whole | 984 | 992.4 | 100.9% | 2.750 | =C (same stream) | - | - |
| mr | 12 | whole | 985.7 | 998.3 | 101.3% | 2.892 | =C (same stream) | - | - |
| nci | 1 | whole | 2578.1 | 2583.4 | 100.2% | 8.743 | =C (same stream) | - | - |
| nci | 6 | whole | 2824.9 | 2854.3 | 101.0% | 10.853 | =C (same stream) | - | - |
| nci | 9 | whole | 2910.4 | 2957 | 101.6% | 11.470 | =C (same stream) | - | - |
| nci | 12 | whole | 2922.3 | 2963.6 | 101.4% | 12.209 | =C (same stream) | - | - |
| ooffice | 1 | whole | 585.3 | 571.1 | 97.6% | 1.878 | =C (same stream) | - | - |
| ooffice | 6 | whole | 579.5 | 564.7 | 97.4% | 2.002 | =C (same stream) | - | - |
| ooffice | 9 | whole | 591.7 | 577 | 97.5% | 2.006 | =C (same stream) | - | - |
| ooffice | 12 | whole | 563.4 | 547.7 | 97.2% | 2.055 | =C (same stream) | - | - |
| osdb | 1 | whole | 1128.8 | 1157.1 | 102.5% | 2.607 | =C (same stream) | - | - |
| osdb | 6 | whole | 1233.3 | 1270 | 103.0% | 2.755 | =C (same stream) | - | - |
| osdb | 9 | whole | 1232.2 | 1267.9 | 102.9% | 2.760 | =C (same stream) | - | - |
| osdb | 12 | whole | 1222.3 | 1261.7 | 103.2% | 2.795 | =C (same stream) | - | - |
| reymont | 1 | whole | 1067.6 | 1076.5 | 100.8% | 3.016 | =C (same stream) | - | - |
| reymont | 6 | whole | 1180.3 | 1200.7 | 101.7% | 3.532 | =C (same stream) | - | - |
| reymont | 9 | whole | 1077 | 1086.1 | 100.8% | 3.669 | =C (same stream) | - | - |
| reymont | 12 | whole | 1284.9 | 1283 | 99.9% | 3.906 | =C (same stream) | - | - |
| samba | 1 | whole | 1273.1 | 1279.7 | 100.5% | 3.683 | =C (same stream) | - | - |
| samba | 6 | whole | 1322.7 | 1324.6 | 100.1% | 4.048 | =C (same stream) | - | - |
| samba | 9 | whole | 1292.4 | 1292.5 | 100.0% | 4.100 | =C (same stream) | - | - |
| samba | 12 | whole | 1301.5 | 1297.7 | 99.7% | 4.222 | =C (same stream) | - | - |
| sao | 1 | whole | 575.8 | 588 | 102.1% | 1.301 | =C (same stream) | - | - |
| sao | 6 | whole | 564.8 | 577.2 | 102.2% | 1.359 | =C (same stream) | - | - |
| sao | 9 | whole | 564.5 | 577 | 102.2% | 1.362 | =C (same stream) | - | - |
| sao | 12 | whole | 582 | 597 | 102.6% | 1.381 | =C (same stream) | - | - |
| webster | 1 | whole | 970.2 | 992 | 102.2% | 3.060 | =C (same stream) | - | - |
| webster | 6 | whole | 1024.2 | 1048 | 102.3% | 3.415 | =C (same stream) | - | - |
| webster | 9 | whole | 982.9 | 993.5 | 101.1% | 3.489 | =C (same stream) | - | - |
| webster | 12 | whole | 1039.9 | 1059.3 | 101.9% | 3.599 | =C (same stream) | - | - |
| xml | 1 | whole | 1976.7 | 1975.9 | 100.0% | 6.021 | =C (same stream) | - | - |
| xml | 6 | whole | 2269.6 | 2278.1 | 100.4% | 7.810 | =C (same stream) | - | - |
| xml | 9 | whole | 2252.4 | 2260.7 | 100.4% | 8.230 | =C (same stream) | - | - |
| xml | 12 | whole | 2355.3 | 2368.2 | 100.5% | 8.493 | =C (same stream) | - | - |
| x-ray | 1 | whole | 495.8 | 498.5 | 100.5% | 1.347 | =C (same stream) | - | - |
| x-ray | 6 | whole | 514.5 | 515.3 | 100.2% | 1.413 | =C (same stream) | - | - |
| x-ray | 9 | whole | 500.1 | 497.4 | 99.5% | 1.415 | =C (same stream) | - | - |
| x-ray | 12 | whole | 549 | 549.1 | 100.0% | 1.475 | =C (same stream) | - | - |
| silesia.tar | 1 | 1M | 970.1 | 974.1 | 100.4% | 2.878 | =C (same stream) | - | - |
| silesia.tar | 6 | 1M | 1013 | 1014.2 | 100.1% | 3.132 | =C (same stream) | - | - |
| silesia.tar | 9 | 1M | 1000.4 | 997.9 | 99.8% | 3.169 | =C (same stream) | - | - |
| silesia.tar | 12 | 1M | 1020.5 | 1018.6 | 99.8% | 3.267 | =C (same stream) | - | - |
| silesia.tar | 1 | 128K | 943.3 | 946.1 | 100.3% | 2.838 | =C (same stream) | - | - |
| silesia.tar | 6 | 128K | 969 | 965.6 | 99.6% | 3.080 | =C (same stream) | - | - |
| silesia.tar | 9 | 128K | 960.3 | 954.7 | 99.4% | 3.113 | =C (same stream) | - | - |
| silesia.tar | 12 | 128K | 969.5 | 962.1 | 99.2% | 3.206 | =C (same stream) | - | - |
| silesia.tar | 1 | 16K | 747.9 | 736 | 98.4% | 2.622 | =C (same stream) | - | - |
| silesia.tar | 6 | 16K | 755.3 | 737.7 | 97.7% | 2.779 | =C (same stream) | - | - |
| silesia.tar | 9 | 16K | 757.9 | 740.2 | 97.7% | 2.798 | =C (same stream) | - | - |
| silesia.tar | 12 | 16K | 739.8 | 720.7 | 97.4% | 2.854 | =C (same stream) | - | - |


**Gates evaluated: Decompress.**

### Decompress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 15 | 100.5% | 97.6% (ooffice/whole) | PASS |
| 6 | 15 | 100.5% | 97.4% (ooffice/whole) | PASS |
| 9 | 15 | 100.3% | 97.5% (ooffice/whole) | PASS |
| 12 | 15 | 100.3% | 97.0% (mozilla/whole) | PASS |

OVERALL: PASS
