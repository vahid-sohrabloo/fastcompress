# gzip decode-go

- date: 2026-11-05T05:16:15+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (4% busy at start)
- load1 at start: 1.56, at end: 1.60; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M silesia.tar:128K silesia.tar:16K 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode d, libs: ld ours, levels: 1 6 9 12; isal: not requested
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- identity check (-mode d): none by size (ours decodes the C streams, so the Go ratio cell is `=C`, not a measurement); integrity = the round trip verified by gzbench before timing (exact-length decode of every block, then byte equality with the input)
- CPU profiles of the ours cells (one extra run per row after the timed runs): <scratch>/prof/<file>.<block>.L<level>.<mode>.cpu.prof
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours is decode-only (Phase 1, raw DEFLATE): for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing; -mode c and -format gzip are n/a for ours

## Decompression

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio | stdlib D-MB/s | klauspost D-MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 817.3 | 437.9 | 53.6% | 2.417 | =C (same stream) | - | - |
| dickens | 6 | whole | 863.1 | 489.5 | 56.7% | 2.641 | =C (same stream) | - | - |
| dickens | 9 | whole | 794 | 471 | 59.3% | 2.675 | =C (same stream) | - | - |
| dickens | 12 | whole | 988.8 | 531.8 | 53.8% | 2.770 | =C (same stream) | - | - |
| mozilla | 1 | whole | 813.2 | 543.5 | 66.8% | 2.537 | =C (same stream) | - | - |
| mozilla | 6 | whole | 845.1 | 551.4 | 65.2% | 2.724 | =C (same stream) | - | - |
| mozilla | 9 | whole | 854.3 | 561.6 | 65.7% | 2.737 | =C (same stream) | - | - |
| mozilla | 12 | whole | 833.5 | 542.6 | 65.1% | 2.808 | =C (same stream) | - | - |
| mr | 1 | whole | 893.8 | 508.3 | 56.9% | 2.665 | =C (same stream) | - | - |
| mr | 6 | whole | 984.7 | 574.8 | 58.4% | 2.733 | =C (same stream) | - | - |
| mr | 9 | whole | 980.6 | 582.9 | 59.4% | 2.750 | =C (same stream) | - | - |
| mr | 12 | whole | 983.6 | 563.7 | 57.3% | 2.892 | =C (same stream) | - | - |
| nci | 1 | whole | 2572.9 | 1370.3 | 53.3% | 8.743 | =C (same stream) | - | - |
| nci | 6 | whole | 2823.1 | 1628.9 | 57.7% | 10.853 | =C (same stream) | - | - |
| nci | 9 | whole | 2911 | 1694.8 | 58.2% | 11.470 | =C (same stream) | - | - |
| nci | 12 | whole | 2926.8 | 1717 | 58.7% | 12.209 | =C (same stream) | - | - |
| ooffice | 1 | whole | 585.9 | 389.5 | 66.5% | 1.878 | =C (same stream) | - | - |
| ooffice | 6 | whole | 580.7 | 373.8 | 64.4% | 2.002 | =C (same stream) | - | - |
| ooffice | 9 | whole | 592.6 | 382 | 64.5% | 2.006 | =C (same stream) | - | - |
| ooffice | 12 | whole | 564.2 | 360 | 63.8% | 2.055 | =C (same stream) | - | - |
| osdb | 1 | whole | 1130.2 | 767.7 | 67.9% | 2.607 | =C (same stream) | - | - |
| osdb | 6 | whole | 1235 | 818.6 | 66.3% | 2.755 | =C (same stream) | - | - |
| osdb | 9 | whole | 1233.2 | 822.8 | 66.7% | 2.760 | =C (same stream) | - | - |
| osdb | 12 | whole | 1224.3 | 806 | 65.8% | 2.795 | =C (same stream) | - | - |
| reymont | 1 | whole | 1067.8 | 547.1 | 51.2% | 3.016 | =C (same stream) | - | - |
| reymont | 6 | whole | 1181.9 | 667.6 | 56.5% | 3.532 | =C (same stream) | - | - |
| reymont | 9 | whole | 1077.5 | 654.7 | 60.8% | 3.669 | =C (same stream) | - | - |
| reymont | 12 | whole | 1287.3 | 708.4 | 55.0% | 3.906 | =C (same stream) | - | - |
| samba | 1 | whole | 1274 | 764.5 | 60.0% | 3.683 | =C (same stream) | - | - |
| samba | 6 | whole | 1325.4 | 824.1 | 62.2% | 4.048 | =C (same stream) | - | - |
| samba | 9 | whole | 1294.2 | 820.8 | 63.4% | 4.100 | =C (same stream) | - | - |
| samba | 12 | whole | 1303.5 | 810.7 | 62.2% | 4.222 | =C (same stream) | - | - |
| sao | 1 | whole | 576.2 | 411.7 | 71.5% | 1.301 | =C (same stream) | - | - |
| sao | 6 | whole | 565.2 | 394.6 | 69.8% | 1.359 | =C (same stream) | - | - |
| sao | 9 | whole | 565.1 | 396.3 | 70.1% | 1.362 | =C (same stream) | - | - |
| sao | 12 | whole | 582.7 | 403 | 69.2% | 1.381 | =C (same stream) | - | - |
| webster | 1 | whole | 970.7 | 555.6 | 57.2% | 3.060 | =C (same stream) | - | - |
| webster | 6 | whole | 1024.9 | 614.9 | 60.0% | 3.415 | =C (same stream) | - | - |
| webster | 9 | whole | 983.6 | 610 | 62.0% | 3.489 | =C (same stream) | - | - |
| webster | 12 | whole | 1041.5 | 627.2 | 60.2% | 3.599 | =C (same stream) | - | - |
| xml | 1 | whole | 1981.4 | 1064.3 | 53.7% | 6.021 | =C (same stream) | - | - |
| xml | 6 | whole | 2275.9 | 1289.6 | 56.7% | 7.810 | =C (same stream) | - | - |
| xml | 9 | whole | 2254.5 | 1317.9 | 58.5% | 8.230 | =C (same stream) | - | - |
| xml | 12 | whole | 2356 | 1337.5 | 56.8% | 8.493 | =C (same stream) | - | - |
| x-ray | 1 | whole | 496 | 372.6 | 75.1% | 1.347 | =C (same stream) | - | - |
| x-ray | 6 | whole | 513.2 | 319.8 | 62.3% | 1.413 | =C (same stream) | - | - |
| x-ray | 9 | whole | 499.5 | 315.2 | 63.1% | 1.415 | =C (same stream) | - | - |
| x-ray | 12 | whole | 549.3 | 321.5 | 58.5% | 1.475 | =C (same stream) | - | - |
| silesia.tar | 1 | 1M | 971.1 | 601.6 | 62.0% | 2.878 | =C (same stream) | - | - |
| silesia.tar | 6 | 1M | 1013.4 | 631.8 | 62.3% | 3.132 | =C (same stream) | - | - |
| silesia.tar | 9 | 1M | 1002.1 | 635.6 | 63.4% | 3.169 | =C (same stream) | - | - |
| silesia.tar | 12 | 1M | 1024.1 | 635.5 | 62.1% | 3.267 | =C (same stream) | - | - |
| silesia.tar | 1 | 128K | 944.4 | 592.9 | 62.8% | 2.838 | =C (same stream) | - | - |
| silesia.tar | 6 | 128K | 970.3 | 611.1 | 63.0% | 3.080 | =C (same stream) | - | - |
| silesia.tar | 9 | 128K | 961.9 | 616.6 | 64.1% | 3.113 | =C (same stream) | - | - |
| silesia.tar | 12 | 128K | 970.9 | 610.5 | 62.9% | 3.206 | =C (same stream) | - | - |
| silesia.tar | 1 | 16K | 749.6 | 509.9 | 68.0% | 2.622 | =C (same stream) | - | - |
| silesia.tar | 6 | 16K | 756.5 | 507.3 | 67.1% | 2.779 | =C (same stream) | - | - |
| silesia.tar | 9 | 16K | 759.5 | 512.6 | 67.5% | 2.798 | =C (same stream) | - | - |
| silesia.tar | 12 | 16K | 741.2 | 495.1 | 66.8% | 2.854 | =C (same stream) | - | - |


**Gates evaluated: Decompress.**

### Decompress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 1 | 15 | 61.4% | 51.2% (reymont/whole) | FAIL (below hard floor) |
| 6 | 15 | 61.8% | 56.5% (reymont/whole) | FAIL (below hard floor) |
| 9 | 15 | 63.0% | 58.2% (nci/whole) | FAIL (below hard floor) |
| 12 | 15 | 61.1% | 53.8% (dickens/whole) | FAIL (below hard floor) |

Violations:
- level 1: geomean 61.4% < 100%
- level 1: dickens/whole 53.6% < 90% HARD FLOOR
- level 1: mozilla/whole 66.8% < 90% HARD FLOOR
- level 1: mr/whole 56.9% < 90% HARD FLOOR
- level 1: nci/whole 53.3% < 90% HARD FLOOR
- level 1: ooffice/whole 66.5% < 90% HARD FLOOR
- level 1: osdb/whole 67.9% < 90% HARD FLOOR
- level 1: reymont/whole 51.2% < 90% HARD FLOOR
- level 1: samba/whole 60.0% < 90% HARD FLOOR
- level 1: sao/whole 71.5% < 90% HARD FLOOR
- level 1: webster/whole 57.2% < 90% HARD FLOOR
- level 1: xml/whole 53.7% < 90% HARD FLOOR
- level 1: x-ray/whole 75.1% < 90% HARD FLOOR
- level 1: silesia.tar/1M 62.0% < 90% HARD FLOOR
- level 1: silesia.tar/128K 62.8% < 90% HARD FLOOR
- level 1: silesia.tar/16K 68.0% < 90% HARD FLOOR
- level 6: geomean 61.8% < 100%
- level 6: dickens/whole 56.7% < 90% HARD FLOOR
- level 6: mozilla/whole 65.2% < 90% HARD FLOOR
- level 6: mr/whole 58.4% < 90% HARD FLOOR
- level 6: nci/whole 57.7% < 90% HARD FLOOR
- level 6: ooffice/whole 64.4% < 90% HARD FLOOR
- level 6: osdb/whole 66.3% < 90% HARD FLOOR
- level 6: reymont/whole 56.5% < 90% HARD FLOOR
- level 6: samba/whole 62.2% < 90% HARD FLOOR
- level 6: sao/whole 69.8% < 90% HARD FLOOR
- level 6: webster/whole 60.0% < 90% HARD FLOOR
- level 6: xml/whole 56.7% < 90% HARD FLOOR
- level 6: x-ray/whole 62.3% < 90% HARD FLOOR
- level 6: silesia.tar/1M 62.3% < 90% HARD FLOOR
- level 6: silesia.tar/128K 63.0% < 90% HARD FLOOR
- level 6: silesia.tar/16K 67.1% < 90% HARD FLOOR
- level 9: geomean 63.0% < 100%
- level 9: dickens/whole 59.3% < 90% HARD FLOOR
- level 9: mozilla/whole 65.7% < 90% HARD FLOOR
- level 9: mr/whole 59.4% < 90% HARD FLOOR
- level 9: nci/whole 58.2% < 90% HARD FLOOR
- level 9: ooffice/whole 64.5% < 90% HARD FLOOR
- level 9: osdb/whole 66.7% < 90% HARD FLOOR
- level 9: reymont/whole 60.8% < 90% HARD FLOOR
- level 9: samba/whole 63.4% < 90% HARD FLOOR
- level 9: sao/whole 70.1% < 90% HARD FLOOR
- level 9: webster/whole 62.0% < 90% HARD FLOOR
- level 9: xml/whole 58.5% < 90% HARD FLOOR
- level 9: x-ray/whole 63.1% < 90% HARD FLOOR
- level 9: silesia.tar/1M 63.4% < 90% HARD FLOOR
- level 9: silesia.tar/128K 64.1% < 90% HARD FLOOR
- level 9: silesia.tar/16K 67.5% < 90% HARD FLOOR
- level 12: geomean 61.1% < 100%
- level 12: dickens/whole 53.8% < 90% HARD FLOOR
- level 12: mozilla/whole 65.1% < 90% HARD FLOOR
- level 12: mr/whole 57.3% < 90% HARD FLOOR
- level 12: nci/whole 58.7% < 90% HARD FLOOR
- level 12: ooffice/whole 63.8% < 90% HARD FLOOR
- level 12: osdb/whole 65.8% < 90% HARD FLOOR
- level 12: reymont/whole 55.0% < 90% HARD FLOOR
- level 12: samba/whole 62.2% < 90% HARD FLOOR
- level 12: sao/whole 69.2% < 90% HARD FLOOR
- level 12: webster/whole 60.2% < 90% HARD FLOOR
- level 12: xml/whole 56.8% < 90% HARD FLOOR
- level 12: x-ray/whole 58.5% < 90% HARD FLOOR
- level 12: silesia.tar/1M 62.1% < 90% HARD FLOOR
- level 12: silesia.tar/128K 62.9% < 90% HARD FLOOR
- level 12: silesia.tar/16K 66.8% < 90% HARD FLOOR

OVERALL: FAIL
