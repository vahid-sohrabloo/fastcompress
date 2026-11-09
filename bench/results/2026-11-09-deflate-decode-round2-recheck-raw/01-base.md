# gzip deflate-decode-recheck-01-base

- date: 2026-11-09T06:03:40+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (4% busy at start); WARNING: pinned CPU 2 is 22% busy.
- load1 at start: 1.73, at end: 2.23; AC power online: 1
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
| dickens | 1 | whole | 816.8 | 830.9 | 101.7% | 2.417 | =C (same stream) | - | - |
| dickens | 6 | whole | 864.2 | 875.8 | 101.3% | 2.641 | =C (same stream) | - | - |
| dickens | 9 | whole | 799.5 | 807.4 | 101.0% | 2.675 | =C (same stream) | - | - |
| dickens | 12 | whole | 984.3 | 1007.2 | 102.3% | 2.770 | =C (same stream) | - | - |
| mozilla | 1 | whole | 817 | 799.5 | 97.9% | 2.537 | =C (same stream) | - | - |
| mozilla | 6 | whole | 841.7 | 813.7 | 96.7% | 2.724 | =C (same stream) | - | - |
| mozilla | 9 | whole | 852.1 | 823.6 | 96.7% | 2.737 | =C (same stream) | - | - |
| mozilla | 12 | whole | 833.9 | 801.9 | 96.2% | 2.808 | =C (same stream) | - | - |
| mr | 1 | whole | 896.1 | 889.8 | 99.3% | 2.665 | =C (same stream) | - | - |
| mr | 6 | whole | 989 | 975.2 | 98.6% | 2.733 | =C (same stream) | - | - |
| mr | 9 | whole | 984.8 | 973.5 | 98.9% | 2.750 | =C (same stream) | - | - |
| mr | 12 | whole | 985.2 | 972 | 98.7% | 2.892 | =C (same stream) | - | - |
| nci | 1 | whole | 2576.8 | 2469.8 | 95.8% | 8.743 | =C (same stream) | - | - |
| nci | 6 | whole | 2822.5 | 2779.7 | 98.5% | 10.853 | =C (same stream) | - | - |
| nci | 9 | whole | 2906.6 | 2909.3 | 100.1% | 11.470 | =C (same stream) | - | - |
| nci | 12 | whole | 2910.8 | 2843.3 | 97.7% | 12.209 | =C (same stream) | - | - |
| ooffice | 1 | whole | 585.2 | 563 | 96.2% | 1.878 | =C (same stream) | - | - |
| ooffice | 6 | whole | 580.8 | 557.9 | 96.1% | 2.002 | =C (same stream) | - | - |
| ooffice | 9 | whole | 591.9 | 571.6 | 96.6% | 2.006 | =C (same stream) | - | - |
| ooffice | 12 | whole | 563.2 | 540.9 | 96.0% | 2.055 | =C (same stream) | - | - |
| osdb | 1 | whole | 1129.7 | 1145 | 101.4% | 2.607 | =C (same stream) | - | - |
| osdb | 6 | whole | 1235.9 | 1259.3 | 101.9% | 2.755 | =C (same stream) | - | - |
| osdb | 9 | whole | 1232.4 | 1254.6 | 101.8% | 2.760 | =C (same stream) | - | - |
| osdb | 12 | whole | 1221.4 | 1246.2 | 102.0% | 2.795 | =C (same stream) | - | - |
| reymont | 1 | whole | 1067.8 | 1058.8 | 99.2% | 3.016 | =C (same stream) | - | - |
| reymont | 6 | whole | 1182.2 | 1174.2 | 99.3% | 3.532 | =C (same stream) | - | - |
| reymont | 9 | whole | 1076.7 | 1077.4 | 100.1% | 3.669 | =C (same stream) | - | - |
| reymont | 12 | whole | 1285.2 | 1259.2 | 98.0% | 3.906 | =C (same stream) | - | - |
| samba | 1 | whole | 1271.7 | 1235.7 | 97.2% | 3.683 | =C (same stream) | - | - |
| samba | 6 | whole | 1322.9 | 1276 | 96.5% | 4.048 | =C (same stream) | - | - |
| samba | 9 | whole | 1292 | 1246.3 | 96.5% | 4.100 | =C (same stream) | - | - |
| samba | 12 | whole | 1303.3 | 1258 | 96.5% | 4.222 | =C (same stream) | - | - |
| sao | 1 | whole | 576.1 | 584.3 | 101.4% | 1.301 | =C (same stream) | - | - |
| sao | 6 | whole | 564.5 | 572.4 | 101.4% | 1.359 | =C (same stream) | - | - |
| sao | 9 | whole | 563.9 | 571.7 | 101.4% | 1.362 | =C (same stream) | - | - |
| sao | 12 | whole | 582.6 | 591.9 | 101.6% | 1.381 | =C (same stream) | - | - |
| webster | 1 | whole | 970 | 972 | 100.2% | 3.060 | =C (same stream) | - | - |
| webster | 6 | whole | 1024.7 | 1029.4 | 100.5% | 3.415 | =C (same stream) | - | - |
| webster | 9 | whole | 982.7 | 982.3 | 100.0% | 3.489 | =C (same stream) | - | - |
| webster | 12 | whole | 1039.5 | 1041.8 | 100.2% | 3.599 | =C (same stream) | - | - |
| xml | 1 | whole | 1975.5 | 1922.2 | 97.3% | 6.021 | =C (same stream) | - | - |
| xml | 6 | whole | 2270.8 | 2216.6 | 97.6% | 7.810 | =C (same stream) | - | - |
| xml | 9 | whole | 2252.6 | 2188.7 | 97.2% | 8.230 | =C (same stream) | - | - |
| xml | 12 | whole | 2359.8 | 2305.3 | 97.7% | 8.493 | =C (same stream) | - | - |
| x-ray | 1 | whole | 495.6 | 494.5 | 99.8% | 1.347 | =C (same stream) | - | - |
| x-ray | 6 | whole | 514.3 | 510.8 | 99.3% | 1.413 | =C (same stream) | - | - |
| x-ray | 9 | whole | 499.8 | 494.9 | 99.0% | 1.415 | =C (same stream) | - | - |
| x-ray | 12 | whole | 549 | 543 | 98.9% | 1.475 | =C (same stream) | - | - |
| silesia.tar | 1 | 1M | 971.6 | 958.4 | 98.6% | 2.878 | =C (same stream) | - | - |
| silesia.tar | 6 | 1M | 1012.6 | 997.3 | 98.5% | 3.132 | =C (same stream) | - | - |
| silesia.tar | 9 | 1M | 1001.1 | 985.9 | 98.5% | 3.169 | =C (same stream) | - | - |
| silesia.tar | 12 | 1M | 1023.5 | 1001.7 | 97.9% | 3.267 | =C (same stream) | - | - |
| silesia.tar | 1 | 128K | 942.2 | 931.3 | 98.8% | 2.838 | =C (same stream) | - | - |
| silesia.tar | 6 | 128K | 968.4 | 946.7 | 97.8% | 3.080 | =C (same stream) | - | - |
| silesia.tar | 9 | 128K | 961.1 | 940 | 97.8% | 3.113 | =C (same stream) | - | - |
| silesia.tar | 12 | 128K | 970.8 | 946.2 | 97.5% | 3.206 | =C (same stream) | - | - |
| silesia.tar | 1 | 16K | 748 | 724.2 | 96.8% | 2.622 | =C (same stream) | - | - |
| silesia.tar | 6 | 16K | 756.8 | 725.7 | 95.9% | 2.779 | =C (same stream) | - | - |
| silesia.tar | 9 | 16K | 757.7 | 727.1 | 96.0% | 2.798 | =C (same stream) | - | - |
| silesia.tar | 12 | 16K | 740.6 | 707.9 | 95.6% | 2.854 | =C (same stream) | - | - |


**Gates evaluated: Decompress.**

### Decompress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 15 | 98.8% | 95.8% (nci/whole) | FAIL |
| 6 | 15 | 98.6% | 95.9% (silesia.tar/16K) | FAIL |
| 9 | 15 | 98.8% | 96.0% (silesia.tar/16K) | FAIL |
| 12 | 15 | 98.4% | 95.6% (silesia.tar/16K) | FAIL |

Violations:
- level 1: geomean 98.8% < 100%
- level 6: geomean 98.6% < 100%
- level 9: geomean 98.8% < 100%
- level 12: geomean 98.4% < 100%

OVERALL: FAIL
