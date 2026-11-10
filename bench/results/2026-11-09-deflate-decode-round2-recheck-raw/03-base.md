# gzip deflate-decode-recheck-03-base

- date: 2026-11-09T07:13:09+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (4% busy at start)
- load1 at start: 1.81, at end: 2.18; AC power online: 1
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
| dickens | 1 | whole | 818.3 | 832.7 | 101.8% | 2.417 | =C (same stream) | - | - |
| dickens | 6 | whole | 863.7 | 875.9 | 101.4% | 2.641 | =C (same stream) | - | - |
| dickens | 9 | whole | 798.6 | 809.7 | 101.4% | 2.675 | =C (same stream) | - | - |
| dickens | 12 | whole | 986 | 1009.8 | 102.4% | 2.770 | =C (same stream) | - | - |
| mozilla | 1 | whole | 817.7 | 800.3 | 97.9% | 2.537 | =C (same stream) | - | - |
| mozilla | 6 | whole | 843.5 | 814.1 | 96.5% | 2.724 | =C (same stream) | - | - |
| mozilla | 9 | whole | 852.1 | 824.1 | 96.7% | 2.737 | =C (same stream) | - | - |
| mozilla | 12 | whole | 834.4 | 803.1 | 96.2% | 2.808 | =C (same stream) | - | - |
| mr | 1 | whole | 896 | 891.8 | 99.5% | 2.665 | =C (same stream) | - | - |
| mr | 6 | whole | 989.4 | 978.3 | 98.9% | 2.733 | =C (same stream) | - | - |
| mr | 9 | whole | 986.3 | 973.8 | 98.7% | 2.750 | =C (same stream) | - | - |
| mr | 12 | whole | 983.5 | 970.5 | 98.7% | 2.892 | =C (same stream) | - | - |
| nci | 1 | whole | 2584.5 | 2471.1 | 95.6% | 8.743 | =C (same stream) | - | - |
| nci | 6 | whole | 2824.7 | 2786.5 | 98.6% | 10.853 | =C (same stream) | - | - |
| nci | 9 | whole | 2908.7 | 2910.1 | 100.0% | 11.470 | =C (same stream) | - | - |
| nci | 12 | whole | 2933.4 | 2863.3 | 97.6% | 12.209 | =C (same stream) | - | - |
| ooffice | 1 | whole | 585.2 | 563.8 | 96.3% | 1.878 | =C (same stream) | - | - |
| ooffice | 6 | whole | 580.5 | 557.6 | 96.1% | 2.002 | =C (same stream) | - | - |
| ooffice | 9 | whole | 591.5 | 571 | 96.5% | 2.006 | =C (same stream) | - | - |
| ooffice | 12 | whole | 563.8 | 540.2 | 95.8% | 2.055 | =C (same stream) | - | - |
| osdb | 1 | whole | 1129.4 | 1145.3 | 101.4% | 2.607 | =C (same stream) | - | - |
| osdb | 6 | whole | 1234.9 | 1259.3 | 102.0% | 2.755 | =C (same stream) | - | - |
| osdb | 9 | whole | 1230.9 | 1256 | 102.0% | 2.760 | =C (same stream) | - | - |
| osdb | 12 | whole | 1224.3 | 1248.7 | 102.0% | 2.795 | =C (same stream) | - | - |
| reymont | 1 | whole | 1068.7 | 1057.8 | 99.0% | 3.016 | =C (same stream) | - | - |
| reymont | 6 | whole | 1181.5 | 1173.7 | 99.3% | 3.532 | =C (same stream) | - | - |
| reymont | 9 | whole | 1077.7 | 1077.8 | 100.0% | 3.669 | =C (same stream) | - | - |
| reymont | 12 | whole | 1286.1 | 1262.2 | 98.1% | 3.906 | =C (same stream) | - | - |
| samba | 1 | whole | 1270.9 | 1235 | 97.2% | 3.683 | =C (same stream) | - | - |
| samba | 6 | whole | 1322.8 | 1276.8 | 96.5% | 4.048 | =C (same stream) | - | - |
| samba | 9 | whole | 1292.3 | 1248 | 96.6% | 4.100 | =C (same stream) | - | - |
| samba | 12 | whole | 1303.2 | 1256.6 | 96.4% | 4.222 | =C (same stream) | - | - |
| sao | 1 | whole | 574.9 | 583.7 | 101.5% | 1.301 | =C (same stream) | - | - |
| sao | 6 | whole | 564.6 | 572.1 | 101.3% | 1.359 | =C (same stream) | - | - |
| sao | 9 | whole | 564.9 | 571.9 | 101.2% | 1.362 | =C (same stream) | - | - |
| sao | 12 | whole | 582.5 | 591.6 | 101.6% | 1.381 | =C (same stream) | - | - |
| webster | 1 | whole | 969.9 | 972.4 | 100.3% | 3.060 | =C (same stream) | - | - |
| webster | 6 | whole | 1023.8 | 1029.1 | 100.5% | 3.415 | =C (same stream) | - | - |
| webster | 9 | whole | 982.3 | 983.7 | 100.1% | 3.489 | =C (same stream) | - | - |
| webster | 12 | whole | 1039.6 | 1041.5 | 100.2% | 3.599 | =C (same stream) | - | - |
| xml | 1 | whole | 1976.5 | 1923.3 | 97.3% | 6.021 | =C (same stream) | - | - |
| xml | 6 | whole | 2272.8 | 2219.5 | 97.7% | 7.810 | =C (same stream) | - | - |
| xml | 9 | whole | 2253.1 | 2193.8 | 97.4% | 8.230 | =C (same stream) | - | - |
| xml | 12 | whole | 2357.8 | 2303.1 | 97.7% | 8.493 | =C (same stream) | - | - |
| x-ray | 1 | whole | 495.7 | 494.7 | 99.8% | 1.347 | =C (same stream) | - | - |
| x-ray | 6 | whole | 514.1 | 509.9 | 99.2% | 1.413 | =C (same stream) | - | - |
| x-ray | 9 | whole | 499.4 | 494.5 | 99.0% | 1.415 | =C (same stream) | - | - |
| x-ray | 12 | whole | 549.1 | 542.5 | 98.8% | 1.475 | =C (same stream) | - | - |
| silesia.tar | 1 | 1M | 972.1 | 960 | 98.8% | 2.878 | =C (same stream) | - | - |
| silesia.tar | 6 | 1M | 1014.4 | 998.3 | 98.4% | 3.132 | =C (same stream) | - | - |
| silesia.tar | 9 | 1M | 999 | 985 | 98.6% | 3.169 | =C (same stream) | - | - |
| silesia.tar | 12 | 1M | 1023.7 | 1004.3 | 98.1% | 3.267 | =C (same stream) | - | - |
| silesia.tar | 1 | 128K | 942.7 | 930.2 | 98.7% | 2.838 | =C (same stream) | - | - |
| silesia.tar | 6 | 128K | 968.1 | 948.2 | 97.9% | 3.080 | =C (same stream) | - | - |
| silesia.tar | 9 | 128K | 959.3 | 940.3 | 98.0% | 3.113 | =C (same stream) | - | - |
| silesia.tar | 12 | 128K | 969.8 | 945.4 | 97.5% | 3.206 | =C (same stream) | - | - |
| silesia.tar | 1 | 16K | 747.1 | 724 | 96.9% | 2.622 | =C (same stream) | - | - |
| silesia.tar | 6 | 16K | 755.6 | 723 | 95.7% | 2.779 | =C (same stream) | - | - |
| silesia.tar | 9 | 16K | 758 | 727 | 95.9% | 2.798 | =C (same stream) | - | - |
| silesia.tar | 12 | 16K | 739.7 | 708.3 | 95.8% | 2.854 | =C (same stream) | - | - |


**Gates evaluated: Decompress.**

### Decompress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 15 | 98.8% | 95.6% (nci/whole) | FAIL |
| 6 | 15 | 98.6% | 95.7% (silesia.tar/16K) | FAIL |
| 9 | 15 | 98.8% | 95.9% (silesia.tar/16K) | FAIL |
| 12 | 15 | 98.4% | 95.8% (ooffice/whole) | FAIL |

Violations:
- level 1: geomean 98.8% < 100%
- level 6: geomean 98.6% < 100%
- level 9: geomean 98.8% < 100%
- level 12: geomean 98.4% < 100%

OVERALL: FAIL
