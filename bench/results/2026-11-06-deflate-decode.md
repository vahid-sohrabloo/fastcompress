# gzip deflate-decode

- date: 2026-11-06T07:14:58+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (2% busy at start)
- load1 at start: 1.02, at end: 1.45; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M silesia.tar:128K silesia.tar:16K 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode d, libs: ld ours stdlib klauspost isal, levels: 1 6 9 12; isal: <gzbench>/gzbench_c (levels 0..3, whole files, raw)
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- identity check (-mode d): none by size (ours decodes the C streams, so the Go ratio cell is `=C`, not a measurement); integrity = the round trip verified by gzbench before timing (exact-length decode of every block, then byte equality with the input)
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing

## Decompression

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio | stdlib D-MB/s | klauspost D-MB/s | isal D-MB/s |
|---|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 817.8 | 822.7 | 100.6% | 2.417 | =C (same stream) | 172.7 | 211.9 | 659.2 |
| dickens | 6 | whole | 864.8 | 868.2 | 100.4% | 2.641 | =C (same stream) | 170.6 | 206 | - |
| dickens | 9 | whole | 800 | 792.8 | 99.1% | 2.675 | =C (same stream) | 182.1 | 219.8 | - |
| dickens | 12 | whole | 988.1 | 1003 | 101.5% | 2.770 | =C (same stream) | - | - | - |
| mozilla | 1 | whole | 817.8 | 797.5 | 97.5% | 2.537 | =C (same stream) | 222.9 | 274.2 | 681.3 |
| mozilla | 6 | whole | 842.7 | 812.3 | 96.4% | 2.724 | =C (same stream) | 231.5 | 283 | - |
| mozilla | 9 | whole | 851.8 | 822.5 | 96.6% | 2.737 | =C (same stream) | 230.9 | 283.8 | - |
| mozilla | 12 | whole | 834 | 800.2 | 95.9% | 2.808 | =C (same stream) | - | - | - |
| mr | 1 | whole | 896.7 | 886.8 | 98.9% | 2.665 | =C (same stream) | 220.6 | 277.7 | 710.4 |
| mr | 6 | whole | 990 | 971.8 | 98.2% | 2.733 | =C (same stream) | 212.2 | 262.3 | - |
| mr | 9 | whole | 985.8 | 973.7 | 98.8% | 2.750 | =C (same stream) | 219 | 267.9 | - |
| mr | 12 | whole | 985.1 | 973.5 | 98.8% | 2.892 | =C (same stream) | - | - | - |
| nci | 1 | whole | 2578.1 | 2503.2 | 97.1% | 8.743 | =C (same stream) | 542.6 | 664.1 | 2077 |
| nci | 6 | whole | 2839 | 2812.7 | 99.1% | 10.853 | =C (same stream) | 638 | 766.8 | - |
| nci | 9 | whole | 2911 | 2921.3 | 100.4% | 11.470 | =C (same stream) | 751.3 | 892.2 | - |
| nci | 12 | whole | 2943.1 | 2924.4 | 99.4% | 12.209 | =C (same stream) | - | - | - |
| ooffice | 1 | whole | 584.7 | 565.8 | 96.8% | 1.878 | =C (same stream) | 155.9 | 193.2 | 532.7 |
| ooffice | 6 | whole | 581.7 | 559.2 | 96.1% | 2.002 | =C (same stream) | 153.9 | 188.6 | - |
| ooffice | 9 | whole | 592.3 | 571.7 | 96.5% | 2.006 | =C (same stream) | 158.7 | 195 | - |
| ooffice | 12 | whole | 565 | 541.9 | 95.9% | 2.055 | =C (same stream) | - | - | - |
| osdb | 1 | whole | 1130.7 | 1144 | 101.2% | 2.607 | =C (same stream) | 225.3 | 283.9 | 753.1 |
| osdb | 6 | whole | 1235.8 | 1258.9 | 101.9% | 2.755 | =C (same stream) | 246.2 | 311.4 | - |
| osdb | 9 | whole | 1236 | 1255.4 | 101.6% | 2.760 | =C (same stream) | 252.7 | 321.3 | - |
| osdb | 12 | whole | 1223.9 | 1247.7 | 101.9% | 2.795 | =C (same stream) | - | - | - |
| reymont | 1 | whole | 1068.7 | 1051.6 | 98.4% | 3.016 | =C (same stream) | 204 | 255.2 | 820.5 |
| reymont | 6 | whole | 1181.6 | 1151.9 | 97.5% | 3.532 | =C (same stream) | 220.2 | 267.1 | - |
| reymont | 9 | whole | 1078.1 | 1062.3 | 98.5% | 3.669 | =C (same stream) | 240.6 | 284.4 | - |
| reymont | 12 | whole | 1285.6 | 1233 | 95.9% | 3.906 | =C (same stream) | - | - | - |
| samba | 1 | whole | 1272.7 | 1236 | 97.1% | 3.683 | =C (same stream) | 292.5 | 360.6 | 971.4 |
| samba | 6 | whole | 1324.7 | 1275 | 96.2% | 4.048 | =C (same stream) | 312.8 | 385.5 | - |
| samba | 9 | whole | 1292.1 | 1247.8 | 96.6% | 4.100 | =C (same stream) | 329.6 | 401.8 | - |
| samba | 12 | whole | 1303.8 | 1258.2 | 96.5% | 4.222 | =C (same stream) | - | - | - |
| sao | 1 | whole | 576.4 | 583.2 | 101.2% | 1.301 | =C (same stream) | 164.9 | 213.6 | 436.4 |
| sao | 6 | whole | 565.8 | 568.7 | 100.5% | 1.359 | =C (same stream) | 153.2 | 198.6 | - |
| sao | 9 | whole | 565.4 | 568.5 | 100.5% | 1.362 | =C (same stream) | 160.7 | 207.1 | - |
| sao | 12 | whole | 580.9 | 586.3 | 100.9% | 1.381 | =C (same stream) | - | - | - |
| webster | 1 | whole | 966.5 | 951.1 | 98.4% | 3.060 | =C (same stream) | 206.3 | 251.7 | 766.9 |
| webster | 6 | whole | 1020.6 | 1003.5 | 98.3% | 3.415 | =C (same stream) | 210.1 | 252.7 | - |
| webster | 9 | whole | 981.6 | 964.8 | 98.3% | 3.489 | =C (same stream) | 231.3 | 275.2 | - |
| webster | 12 | whole | 1040.1 | 1021 | 98.2% | 3.599 | =C (same stream) | - | - | - |
| xml | 1 | whole | 1978.8 | 1924.8 | 97.3% | 6.021 | =C (same stream) | 401.1 | 495.1 | 1426.6 |
| xml | 6 | whole | 2273.8 | 2215.4 | 97.4% | 7.810 | =C (same stream) | 478 | 580.4 | - |
| xml | 9 | whole | 2256.2 | 2208.7 | 97.9% | 8.230 | =C (same stream) | 560.1 | 673.7 | - |
| xml | 12 | whole | 2360.5 | 2298.6 | 97.4% | 8.493 | =C (same stream) | - | - | - |
| x-ray | 1 | whole | 496.1 | 495.9 | 100.0% | 1.347 | =C (same stream) | 294.6 | 377.9 | 441.3 |
| x-ray | 6 | whole | 514.4 | 508 | 98.8% | 1.413 | =C (same stream) | 151.9 | 193.9 | - |
| x-ray | 9 | whole | 499 | 492.9 | 98.8% | 1.415 | =C (same stream) | 149.7 | 190.6 | - |
| x-ray | 12 | whole | 550.5 | 544 | 98.8% | 1.475 | =C (same stream) | - | - | - |
| silesia.tar | 1 | 1M | 972.6 | 956.2 | 98.3% | 2.878 | =C (same stream) | 242.1 | 300.7 | - |
| silesia.tar | 6 | 1M | 1015.3 | 994.3 | 97.9% | 3.132 | =C (same stream) | 242.1 | 297.3 | - |
| silesia.tar | 9 | 1M | 997.3 | 980.1 | 98.3% | 3.169 | =C (same stream) | 253 | 309.1 | - |
| silesia.tar | 12 | 1M | 1023.4 | 999.3 | 97.6% | 3.267 | =C (same stream) | - | - | - |
| silesia.tar | 1 | 128K | 943.5 | 927.5 | 98.3% | 2.838 | =C (same stream) | 242.6 | 300.3 | - |
| silesia.tar | 6 | 128K | 969.4 | 945.2 | 97.5% | 3.080 | =C (same stream) | 240.7 | 295.4 | - |
| silesia.tar | 9 | 128K | 962.4 | 939.2 | 97.6% | 3.113 | =C (same stream) | 249 | 305 | - |
| silesia.tar | 12 | 128K | 970.6 | 942.7 | 97.1% | 3.206 | =C (same stream) | - | - | - |
| silesia.tar | 1 | 16K | 749.1 | 717.8 | 95.8% | 2.622 | =C (same stream) | 223.7 | 275.2 | - |
| silesia.tar | 6 | 16K | 756.9 | 721 | 95.3% | 2.779 | =C (same stream) | 217.7 | 266.7 | - |
| silesia.tar | 9 | 16K | 758.9 | 724.3 | 95.4% | 2.798 | =C (same stream) | 218.3 | 268.9 | - |
| silesia.tar | 12 | 16K | 741.5 | 705.1 | 95.1% | 2.854 | =C (same stream) | - | - | - |


**Gates evaluated: Decompress.**

### Decompress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 15 | 98.4% | 95.8% (silesia.tar/16K) | FAIL |
| 6 | 15 | 98.1% | 95.3% (silesia.tar/16K) | FAIL |
| 9 | 15 | 98.3% | 95.4% (silesia.tar/16K) | FAIL |
| 12 | 15 | 98.0% | 95.1% (silesia.tar/16K) | FAIL |

Violations:
- level 1: geomean 98.4% < 100%
- level 6: geomean 98.1% < 100%
- level 9: geomean 98.3% < 100%
- level 12: geomean 98.0% < 100%

OVERALL: FAIL
