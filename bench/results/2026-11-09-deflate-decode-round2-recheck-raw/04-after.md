# gzip deflate-decode-recheck-04-after

- date: 2026-11-09T09:06:15+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (0% busy at start)
- load1 at start: 1.87, at end: 1.56; AC power online: 1
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
| dickens | 1 | whole | 818.1 | 845.9 | 103.4% | 2.417 | =C (same stream) | - | - |
| dickens | 6 | whole | 864.1 | 896.4 | 103.7% | 2.641 | =C (same stream) | - | - |
| dickens | 9 | whole | 799.4 | 822.8 | 102.9% | 2.675 | =C (same stream) | - | - |
| dickens | 12 | whole | 986.5 | 1030.4 | 104.5% | 2.770 | =C (same stream) | - | - |
| mozilla | 1 | whole | 817.9 | 806.8 | 98.6% | 2.537 | =C (same stream) | - | - |
| mozilla | 6 | whole | 841.1 | 820.4 | 97.5% | 2.724 | =C (same stream) | - | - |
| mozilla | 9 | whole | 850.7 | 830.1 | 97.6% | 2.737 | =C (same stream) | - | - |
| mozilla | 12 | whole | 834.4 | 808.7 | 96.9% | 2.808 | =C (same stream) | - | - |
| mr | 1 | whole | 897.3 | 892.7 | 99.5% | 2.665 | =C (same stream) | - | - |
| mr | 6 | whole | 989.2 | 999.7 | 101.1% | 2.733 | =C (same stream) | - | - |
| mr | 9 | whole | 984.8 | 991.1 | 100.6% | 2.750 | =C (same stream) | - | - |
| mr | 12 | whole | 984.8 | 997.7 | 101.3% | 2.892 | =C (same stream) | - | - |
| nci | 1 | whole | 2581.8 | 2583.5 | 100.1% | 8.743 | =C (same stream) | - | - |
| nci | 6 | whole | 2826.2 | 2858.8 | 101.2% | 10.853 | =C (same stream) | - | - |
| nci | 9 | whole | 2913.9 | 2955.9 | 101.4% | 11.470 | =C (same stream) | - | - |
| nci | 12 | whole | 2939.1 | 2959.9 | 100.7% | 12.209 | =C (same stream) | - | - |
| ooffice | 1 | whole | 584.9 | 572 | 97.8% | 1.878 | =C (same stream) | - | - |
| ooffice | 6 | whole | 577.8 | 565.1 | 97.8% | 2.002 | =C (same stream) | - | - |
| ooffice | 9 | whole | 592 | 576.8 | 97.4% | 2.006 | =C (same stream) | - | - |
| ooffice | 12 | whole | 564.4 | 548 | 97.1% | 2.055 | =C (same stream) | - | - |
| osdb | 1 | whole | 1128.1 | 1155.2 | 102.4% | 2.607 | =C (same stream) | - | - |
| osdb | 6 | whole | 1235.1 | 1270 | 102.8% | 2.755 | =C (same stream) | - | - |
| osdb | 9 | whole | 1232.4 | 1265.1 | 102.7% | 2.760 | =C (same stream) | - | - |
| osdb | 12 | whole | 1223.8 | 1261.9 | 103.1% | 2.795 | =C (same stream) | - | - |
| reymont | 1 | whole | 1065.9 | 1076.5 | 101.0% | 3.016 | =C (same stream) | - | - |
| reymont | 6 | whole | 1180.8 | 1202.3 | 101.8% | 3.532 | =C (same stream) | - | - |
| reymont | 9 | whole | 1077.4 | 1085.2 | 100.7% | 3.669 | =C (same stream) | - | - |
| reymont | 12 | whole | 1284.5 | 1282.1 | 99.8% | 3.906 | =C (same stream) | - | - |
| samba | 1 | whole | 1271.4 | 1277.9 | 100.5% | 3.683 | =C (same stream) | - | - |
| samba | 6 | whole | 1323.6 | 1326 | 100.2% | 4.048 | =C (same stream) | - | - |
| samba | 9 | whole | 1293 | 1292.4 | 100.0% | 4.100 | =C (same stream) | - | - |
| samba | 12 | whole | 1299.9 | 1295.8 | 99.7% | 4.222 | =C (same stream) | - | - |
| sao | 1 | whole | 576 | 588.3 | 102.1% | 1.301 | =C (same stream) | - | - |
| sao | 6 | whole | 565 | 577.1 | 102.1% | 1.359 | =C (same stream) | - | - |
| sao | 9 | whole | 563.5 | 575.1 | 102.1% | 1.362 | =C (same stream) | - | - |
| sao | 12 | whole | 581.4 | 595.8 | 102.5% | 1.381 | =C (same stream) | - | - |
| webster | 1 | whole | 968.8 | 991.8 | 102.4% | 3.060 | =C (same stream) | - | - |
| webster | 6 | whole | 1023.4 | 1048.5 | 102.5% | 3.415 | =C (same stream) | - | - |
| webster | 9 | whole | 983.2 | 993.9 | 101.1% | 3.489 | =C (same stream) | - | - |
| webster | 12 | whole | 1040.8 | 1059.8 | 101.8% | 3.599 | =C (same stream) | - | - |
| xml | 1 | whole | 1978.7 | 1978.1 | 100.0% | 6.021 | =C (same stream) | - | - |
| xml | 6 | whole | 2272.1 | 2278.6 | 100.3% | 7.810 | =C (same stream) | - | - |
| xml | 9 | whole | 2251.7 | 2263.8 | 100.5% | 8.230 | =C (same stream) | - | - |
| xml | 12 | whole | 2356 | 2369.3 | 100.6% | 8.493 | =C (same stream) | - | - |
| x-ray | 1 | whole | 495 | 498.3 | 100.7% | 1.347 | =C (same stream) | - | - |
| x-ray | 6 | whole | 513.7 | 515.1 | 100.3% | 1.413 | =C (same stream) | - | - |
| x-ray | 9 | whole | 499.1 | 496.6 | 99.5% | 1.415 | =C (same stream) | - | - |
| x-ray | 12 | whole | 549.9 | 548.9 | 99.8% | 1.475 | =C (same stream) | - | - |
| silesia.tar | 1 | 1M | 970.6 | 974.8 | 100.4% | 2.878 | =C (same stream) | - | - |
| silesia.tar | 6 | 1M | 1012.8 | 1013.6 | 100.1% | 3.132 | =C (same stream) | - | - |
| silesia.tar | 9 | 1M | 1000.9 | 998.4 | 99.8% | 3.169 | =C (same stream) | - | - |
| silesia.tar | 12 | 1M | 1022.1 | 1018.8 | 99.7% | 3.267 | =C (same stream) | - | - |
| silesia.tar | 1 | 128K | 942.6 | 945.5 | 100.3% | 2.838 | =C (same stream) | - | - |
| silesia.tar | 6 | 128K | 968.1 | 965.2 | 99.7% | 3.080 | =C (same stream) | - | - |
| silesia.tar | 9 | 128K | 960.3 | 954.1 | 99.4% | 3.113 | =C (same stream) | - | - |
| silesia.tar | 12 | 128K | 969.4 | 962.2 | 99.3% | 3.206 | =C (same stream) | - | - |
| silesia.tar | 1 | 16K | 749.1 | 736.3 | 98.3% | 2.622 | =C (same stream) | - | - |
| silesia.tar | 6 | 16K | 756.2 | 738.4 | 97.6% | 2.779 | =C (same stream) | - | - |
| silesia.tar | 9 | 16K | 759.2 | 740.9 | 97.6% | 2.798 | =C (same stream) | - | - |
| silesia.tar | 12 | 16K | 741.3 | 721.4 | 97.3% | 2.854 | =C (same stream) | - | - |


**Gates evaluated: Decompress.**

### Decompress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 15 | 100.5% | 97.8% (ooffice/whole) | PASS |
| 6 | 15 | 100.6% | 97.5% (mozilla/whole) | PASS |
| 9 | 15 | 100.2% | 97.4% (ooffice/whole) | PASS |
| 12 | 15 | 100.3% | 96.9% (mozilla/whole) | PASS |

OVERALL: PASS
