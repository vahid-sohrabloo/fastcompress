# gzip deflate-compress-l12-round1

- date: 2026-11-08T04:39:55+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; git: 59c35a4 (dirty+untracked)
- cpu: 13th Gen Intel(R) Core(TM) i9-13900H; pinned to CPU 2 (taskset); SMT sibling: CPU 3 (8% busy at start)
- load1 at start: 1.92, at end: 1.79; AC power online: 1
- C: libdeflate v1.26 (ref/libdeflate); `ldref version`: libdeflate v1.26 arch=x86 cpu_features=0x8000051f decompress=bmi2 
- C compiler: cc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0; flags (from `make -n -B ldbench` in ref/ldref; runtime asm dispatch on; impl above is from the sibling `ldref` binary built from the same sources): `-O2 -I../libdeflate -I../libdeflate/lib`
- Go: go version go1.27.1 linux/amd64; ours asm: on (default build); stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@v1.20.3 (unaffected by -tags noasm)
- quick: no; files: dickens:whole mozilla:whole mr:whole nci:whole ooffice:whole osdb:whole reymont:whole samba:whole sao:whole webster:whole xml:whole x-ray:whole silesia.tar:1M 
- settings: -i 3 (C and ISA-L), -g 3 (Go), -n 3 (median, interleaved), -format raw, -mode c, libs: ld ours, levels: 12; isal: not requested
- identity check (-mode c): exact compressed byte count (third field of ldbench/gzbench)
- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor
- stdlib/klauspost: same numeric level for 1..9; `-` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files
- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by both C and ours; the round trip is verified before timing

## Compression

| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 12 | whole | 6 | 6.9 | 115.0% | 2.770 | 2.770 | - | - |
| mozilla | 12 | whole | 7.1 | 6.5 | 91.5% | 2.808 | 2.808 | - | - |
| mr | 12 | whole | 4.1 | 3.6 | 87.8% | 2.892 | 2.892 | - | - |
| nci | 12 | whole | 3.1 | 2.6 | 83.9% | 12.209 | 12.209 | - | - |
| ooffice | 12 | whole | 9.9 | 9.4 | 94.9% | 2.055 | 2.055 | - | - |
| osdb | 12 | whole | 9.7 | 9.5 | 97.9% | 2.795 | 2.795 | - | - |
| reymont | 12 | whole | 4.6 | 5.4 | 117.4% | 3.906 | 3.906 | - | - |
| samba | 12 | whole | 5.2 | 4.7 | 90.4% | 4.222 | 4.222 | - | - |
| sao | 12 | whole | 15.4 | 13.6 | 88.3% | 1.381 | 1.381 | - | - |
| webster | 12 | whole | 4.8 | 5.5 | 114.6% | 3.599 | 3.599 | - | - |
| xml | 12 | whole | 3.6 | 3.1 | 86.1% | 8.493 | 8.493 | - | - |
| x-ray | 12 | whole | 23.2 | 19 | 81.9% | 1.475 | 1.475 | - | - |
| silesia.tar | 12 | 1M | 5.1 | 4.8 | 94.1% | 3.267 | 3.267 | - | - |


**Gates evaluated: Compress.**

### Compress gate (geomean >= 100%, every row >= 95%, hard floor 90%; MISMATCH/missing rows fail)

| level | rows | geomean % of C | min row | gate |
|---|---|---|---|---|
| 12 | 13 | 95.0% | 81.9% (x-ray/whole) | FAIL (below hard floor) |

Violations:
- level 12: geomean 95.0% < 100%
- level 12: mozilla/whole 91.5% < 95%
- level 12: mr/whole 87.8% < 90% HARD FLOOR
- level 12: nci/whole 83.9% < 90% HARD FLOOR
- level 12: ooffice/whole 94.9% < 95%
- level 12: samba/whole 90.4% < 95%
- level 12: sao/whole 88.3% < 90% HARD FLOOR
- level 12: xml/whole 86.1% < 90% HARD FLOOR
- level 12: x-ray/whole 81.9% < 90% HARD FLOOR
- level 12: silesia.tar/1M 94.1% < 95%

OVERALL: FAIL
