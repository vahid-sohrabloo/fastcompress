# phase2-final

## Phase 2 gate summary

Decode, `bench/compare.sh -mode d -label phase2-final`. The full table is under "Results table" below: 64 rows, C and Go interleaved, median of 3 runs, CPU 2 pinned. Go and C ratios are equal in every row, so both sides decode the same frames.

Gate:
- decode geomean at least 100% of C at each level
- every row at least 95%
- a 90% hard floor
- nci L1 at 94.6% was accepted earlier

| level | geomean, all 16 rows | geomean, 12 whole files | minimum row | rows < 100% | rows < 95% | gate |
|---|---|---|---|---|---|---|
| 1 | 101.0% | 101.5% | silesia.tar 16K, 93.9% | 7 | 2 | pass with two accepted rows (nci L1 94.5%, 16K L1 93.9%) |
| 3 | 101.2% | 101.3% | silesia.tar 16K, 95.7% | 7 | 0 | pass |
| 7 | 101.6% | 101.6% | silesia.tar 16K, 97.3% | 6 | 0 | pass |
| 11 | 100.8% | 100.5% | webster whole, 96.8% | 8 | 0 | pass |
| all | 101.1% | 101.2% | silesia.tar 16K L1, 93.9% | 28 | 2 | |

No row is under the 90% floor.

### Rows under 95%

| row | C | Go | % of C | status |
|---|---|---|---|---|
| nci L1 whole | 2499.7 | 2361.5 | 94.5% | accepted by the user at 94.6% (Task 20d); unchanged |
| silesia.tar L1 16K | 1078.4 | 1012.4 | 93.9% | misses the 95% row gate; **accepted by user 2026-11-06, carried forward** |

silesia.tar L1 16K, across the committed decode tables (MB/s):

| table | C | Go | % of C | note |
|---|---|---|---|---|
| 2026-11-05-task20c-fsetable | 1082.7 | 1043.6 | 96.4% | |
| 2026-11-05-task20c-after | 1083.6 | 1044.5 | 96.4% | |
| 2026-11-05-task20d-before | 1077.6 | 1040.6 | 96.6% | |
| 2026-11-05-task20d-after | 1057.2 | 1008.7 | 95.4% | |
| 2026-11-05-task20d-splitlit (Task 20d final) | 1018.0 | 1008.0 | 99.0% | measured at load1 5.2 to 6.8, which slowed C |
| phase2-final (this run) | 1078.4 | 1012.4 | 93.9% | |

C's figure is steady at 1077 to 1084 when the machine is quieter. Go was at 1041 to 1045 up to 20d-before and has been at 1008 to 1012 in all three runs since Task 20d (split literals). So the drop looks like a real Go slowdown of about 3% on this row since 20d, not noise. 20d's 99.0% came from C being slowed by load. The profile below puts decodeExecSeqsAsm at 46% of samples, and the Huffman table read plus the FSE table build at about 20% (huf.ReadDTableX1 11.3% cum, buildFSETableAsm 6.8%, fse.ReadNCount 2.7% cum). That table work is a fixed cost per 16K block. **The row was accepted by user 2026-11-06 and carried forward.** The user will fix it later, and this task changed no code.

### Load during the runs

The desktop background (Chrome, clickhouse-server, another session's tests) never stayed below load1 2 for long:
- The decode run waited from 21:14 (load1 4.3 to 5.5) and started at 21:22:44 at load1 1.89. It ended at 22:24:06 at load1 4.11. Load was not sampled in between.
- The encode recheck started at 22:28:06 at load1 1.92 and ended at 23:31:36 at load1 4.63.

C and Go are interleaved, so the % of C figures hold up better than the absolute speeds.

### Top profile functions of the rows under 100%

`gobench -mode d -secs 3 -cpuprofile` (capped, `taskset -c 2`), `go tool pprof -top`, run after the benchmarks. load1 was 11 to 25 during profiling (another session), so the absolute speeds from those runs mean nothing. The flat shares are this process's own samples.

| row | % of C | top functions (flat %) |
|---|---|---|
| silesia.tar L1 16K | 93.9% | decodeExecSeqsAsm 45.7, huf.decompress4X1FastAsmLoop 16.3, buildFSETableAsm 6.8, huf.ReadDTableX1 5.9 (cum 11.3), huf.decompress4X2FastAsmLoop 4.2, fse.ReadNCount 2.1 |
| nci L1 whole | 94.5% | decodeExecSeqsAsm 84.2, huf.decompress4X2FastAsmLoop 5.0, buildFSETableAsm 3.7, huf.fillDTableX2ForWeight 1.7 |
| silesia.tar L3 16K | 95.7% | decodeExecSeqsAsm 56.4, huf.decompress4X1FastAsmLoop 12.6, buildFSETableAsm 5.3 |
| nci L3 whole | 96.2% | decodeExecSeqsAsm 85.3, buildFSETableAsm 5.7, huf.decompress4X1FastAsmLoop 1.7 |
| x-ray L1 whole | 96.7% | huf.decompress4X2FastAsmLoop 84.1, huf.decodeSymbolX2 3.2, runtime.memmove 2.3 |
| webster L11 whole | 96.8% | decodeExecSeqsAsm 87.8, huf.decompress4X1FastAsmLoop 3.0, huf.decompress4X2FastAsmLoop 2.3 |
| silesia.tar L11 16K | 97.0% | decodeExecSeqsAsm 57.0, huf.decompress4X1FastAsmLoop 16.7, buildFSETableAsm 5.4 |
| mr L1 whole | 97.0% | decodeExecSeqsAsm 61.9, huf.decompress4X2FastAsmLoop 20.9, huf.decodeSymbolX2 3.6 |
| silesia.tar L7 16K | 97.3% | decodeExecSeqsAsm 55.2, huf.decompress4X1FastAsmLoop 14.1, buildFSETableAsm 6.1 |
| samba L11 whole | 97.5% | decodeExecSeqsAsm 82.3, buildFSETableAsm 4.7, huf.decompress4X1FastAsmLoop 2.6 |
| webster L3 whole | 97.5% | decodeExecSeqsAsm 89.9, huf.decompress4X2FastAsmLoop 3.2, buildFSETableAsm 1.9 |
| dickens L11 whole | 97.7% | decodeExecSeqsAsm 91.0, buildFSETableAsm 2.9, huf.decompress4X2FastAsmLoop 2.6 |
| samba L7 whole | 97.8% | decodeExecSeqsAsm 82.0, buildFSETableAsm 6.0, huf.decompress4X1FastAsmLoop 2.5 |
| mozilla L11 whole | 97.9% | decodeExecSeqsAsm 71.1, huf.decompress4X1FastAsmLoop 10.9, buildFSETableAsm 5.5 |
| xml L7 whole | 98.0% | decodeExecSeqsAsm 83.0, buildFSETableAsm 5.9, huf.ReadDTableX1 1.9 |
| xml L11 whole | 98.2% | decodeExecSeqsAsm 81.5, buildFSETableAsm 5.3, huf.ReadDTableX1 1.9 |
| xml L1 whole | 98.4% | decodeExecSeqsAsm 78.9, huf.decompress4X2FastAsmLoop 4.7, buildFSETableAsm 4.0 |
| mozilla L7 whole | 98.4% | decodeExecSeqsAsm 72.9, huf.decompress4X1FastAsmLoop 10.9, huf.ReadDTableX1 3.0 |
| dickens L3 whole | 98.5% | decodeExecSeqsAsm 88.4, huf.decompress4X2FastAsmLoop 3.2, buildFSETableAsm 2.6 |
| samba L1 whole | 98.5% | decodeExecSeqsAsm 73.0, huf.decompress4X2FastAsmLoop 11.6, buildFSETableAsm 4.3 |
| xml L3 whole | 98.5% | decodeExecSeqsAsm 86.1, buildFSETableAsm 3.6, huf.decompress4X1FastAsmLoop 2.3 |
| mr L11 whole | 98.8% | decodeExecSeqsAsm 88.7, huf.decompress4X1FastAsmLoop 4.4, buildFSETableAsm 1.8 |
| nci L7 whole | 98.8% | decodeExecSeqsAsm 88.7, buildFSETableAsm 3.3, huf.decompress4X1FastAsmLoop 2.7 |
| webster L7 whole | 99.1% | decodeExecSeqsAsm 88.7, huf.decompress4X1FastAsmLoop 4.6, buildFSETableAsm 2.3 |
| reymont L3 whole | 99.4% | decodeExecSeqsAsm 93.1, huf.decompress4X1FastAsmLoop 3.8, buildFSETableAsm 1.0 |
| reymont L1 whole | 99.6% | decodeExecSeqsAsm 80.5, huf.decompress4X2FastAsmLoop 10.8, buildFSETableAsm 2.9 |
| samba L3 whole | 99.6% | decodeExecSeqsAsm 82.2, huf.decompress4X1FastAsmLoop 3.7, buildFSETableAsm 3.7 |
| nci L11 whole | 99.9% | decodeExecSeqsAsm 88.0, huf.decompress4X1FastAsmLoop 3.4, buildFSETableAsm 2.4 |

## Encode recheck

The command was `bench/compare.sh -mode c -l "1 3 7 11" -label phase2-encode-recheck` (`2026-11-05-phase2-encode-recheck.md`), compared with `2026-11-02-phase1-final.md`, the last encoder results. Go and C compressed sizes are identical in all 64 rows.

| level | phase1-final geomean | recheck geomean | change | recheck minimum row | rows < 100% |
|---|---|---|---|---|---|
| 1 | 101.1% | 101.0% | -0.1 | mozilla whole 98.5% | 6 |
| 3 | 101.3% | 101.4% | +0.1 | reymont whole 97.7% | 4 |
| 7 | 102.1% | 101.4% | -0.7 | ooffice whole 97.7% | 3 |
| 11 | 101.2% | 101.5% | +0.3 | dickens whole 98.8% | 4 |
| all | 101.4% | 101.3% | -0.1 | ooffice L7 97.7% | 17 |

No % of C figure fell more than 1.9 points; the largest fall is mozilla L3, 104.1% to 102.2%. **The encoder has not regressed relative to C (limit 3%).**

Absolute Go MB/s is lower than in phase1-final: the per-level geomean of the Go speed ratio is L1 -0.6%, L3 -0.8%, L7 -2.2%, L11 -3.8%. C dropped by about the same amount in the same interleaved runs, which is why the % of C figures hold. Load explains it: this run went from load1 1.9 to 4.6, against a quieter phase1-final run.

## Results table

- date: 2026-11-05T21:22:44+01:00; host: vahid-Zenbook-UX6404VV-UX6404VV; cpu: 13th Gen Intel(R) Core(TM) i9-13900H, pinned to CPU 2
- C: *** Zstandard CLI (64-bit) v1.5.7, by Yann Collet ***; `zstd -b<L> -i5 [-B<block>]`; Go: `gobench -mode d -secs 5`; both report the best ~1 s window (mean per pass), CRC off
- median of 3 interleaved runs per cell; MB/s = 1e6 bytes/s of decoded output (decompression only; Go: one DecodeAll per block, WithDecoderConcurrency(1))

| file | level | block | C D-MB/s | Go D-MB/s | % of C | C ratio | Go ratio |
|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 1502.5 | 1516.7 | 100.9% | 2.392 | 2.392 |
| dickens | 3 | whole | 1338.7 | 1319.1 | 98.5% | 2.782 | 2.782 |
| dickens | 7 | whole | 1379.3 | 1380.9 | 100.1% | 3.047 | 3.047 |
| dickens | 11 | whole | 1436.5 | 1402.9 | 97.7% | 3.210 | 3.210 |
| mozilla | 1 | whole | 1262.2 | 1290.0 | 102.2% | 2.565 | 2.565 |
| mozilla | 3 | whole | 1206.8 | 1215.4 | 100.7% | 2.810 | 2.810 |
| mozilla | 7 | whole | 1242.7 | 1222.4 | 98.4% | 3.015 | 3.015 |
| mozilla | 11 | whole | 1255.3 | 1229.5 | 97.9% | 3.095 | 3.095 |
| mr | 1 | whole | 1536.5 | 1490.7 | 97.0% | 2.615 | 2.615 |
| mr | 3 | whole | 1403.0 | 1440.6 | 102.7% | 2.811 | 2.811 |
| mr | 7 | whole | 1389.0 | 1400.8 | 100.8% | 2.983 | 2.983 |
| mr | 11 | whole | 1414.4 | 1397.6 | 98.8% | 3.049 | 3.049 |
| nci | 1 | whole | 2499.7 | 2361.5 | 94.5% | 11.78 | 11.78 |
| nci | 3 | whole | 2394.1 | 2303.2 | 96.2% | 11.84 | 11.84 |
| nci | 7 | whole | 3028.1 | 2992.3 | 98.8% | 14.27 | 14.27 |
| nci | 11 | whole | 3495.2 | 3493.1 | 99.9% | 15.94 | 15.94 |
| ooffice | 1 | whole | 1055.2 | 1088.1 | 103.1% | 1.712 | 1.712 |
| ooffice | 3 | whole | 973.0 | 1034.4 | 106.3% | 1.968 | 1.968 |
| ooffice | 7 | whole | 1004.6 | 1043.3 | 103.9% | 2.127 | 2.127 |
| ooffice | 11 | whole | 1002.5 | 1027.6 | 102.5% | 2.172 | 2.172 |
| osdb | 1 | whole | 1555.9 | 1730.6 | 111.2% | 2.704 | 2.704 |
| osdb | 3 | whole | 1655.4 | 1778.1 | 107.4% | 2.880 | 2.880 |
| osdb | 7 | whole | 1686.9 | 1798.9 | 106.6% | 3.006 | 3.006 |
| osdb | 11 | whole | 1761.0 | 1884.9 | 107.0% | 3.127 | 3.127 |
| reymont | 1 | whole | 1569.3 | 1562.8 | 99.6% | 3.084 | 3.084 |
| reymont | 3 | whole | 1453.4 | 1444.7 | 99.4% | 3.420 | 3.420 |
| reymont | 7 | whole | 1550.7 | 1551.7 | 100.1% | 3.853 | 3.853 |
| reymont | 11 | whole | 1664.9 | 1667.3 | 100.1% | 4.196 | 4.196 |
| samba | 1 | whole | 1959.0 | 1930.5 | 98.5% | 3.937 | 3.937 |
| samba | 3 | whole | 1911.0 | 1902.7 | 99.6% | 4.360 | 4.360 |
| samba | 7 | whole | 1989.1 | 1945.4 | 97.8% | 4.789 | 4.789 |
| samba | 11 | whole | 2075.0 | 2023.7 | 97.5% | 5.040 | 5.040 |
| sao | 1 | whole | 1012.6 | 1188.7 | 117.4% | 1.160 | 1.160 |
| sao | 3 | whole | 859.8 | 929.9 | 108.2% | 1.312 | 1.312 |
| sao | 7 | whole | 1037.5 | 1158.7 | 111.7% | 1.386 | 1.386 |
| sao | 11 | whole | 1030.7 | 1136.7 | 110.3% | 1.394 | 1.394 |
| webster | 1 | whole | 1550.9 | 1553.6 | 100.2% | 3.035 | 3.035 |
| webster | 3 | whole | 1422.7 | 1387.0 | 97.5% | 3.427 | 3.427 |
| webster | 7 | whole | 1534.2 | 1519.7 | 99.1% | 3.928 | 3.928 |
| webster | 11 | whole | 1577.6 | 1527.7 | 96.8% | 4.172 | 4.172 |
| xml | 1 | whole | 2361.5 | 2322.7 | 98.4% | 7.710 | 7.710 |
| xml | 3 | whole | 2347.6 | 2312.8 | 98.5% | 8.414 | 8.414 |
| xml | 7 | whole | 2844.3 | 2786.4 | 98.0% | 10.07 | 10.07 |
| xml | 11 | whole | 3176.6 | 3118.5 | 98.2% | 10.73 | 10.73 |
| x-ray | 1 | whole | 1244.7 | 1203.4 | 96.7% | 1.251 | 1.251 |
| x-ray | 3 | whole | 932.9 | 951.9 | 102.0% | 1.393 | 1.393 |
| x-ray | 7 | whole | 885.8 | 926.5 | 104.6% | 1.559 | 1.559 |
| x-ray | 11 | whole | 834.8 | 841.4 | 100.8% | 1.594 | 1.594 |
| silesia.tar | 1 | 1M | 1524.9 | 1548.3 | 101.5% | 2.880 | 2.880 |
| silesia.tar | 3 | 1M | 1428.8 | 1461.3 | 102.3% | 3.147 | 3.147 |
| silesia.tar | 7 | 1M | 1520.1 | 1554.7 | 102.3% | 3.386 | 3.386 |
| silesia.tar | 11 | 1M | 1585.3 | 1621.6 | 102.3% | 3.448 | 3.448 |
| silesia.tar | 1 | 4M | 1531.0 | 1551.6 | 101.3% | 2.891 | 2.891 |
| silesia.tar | 3 | 4M | 1388.6 | 1430.2 | 103.0% | 3.188 | 3.188 |
| silesia.tar | 7 | 4M | 1465.5 | 1523.7 | 104.0% | 3.478 | 3.478 |
| silesia.tar | 11 | 4M | 1516.7 | 1584.2 | 104.5% | 3.579 | 3.579 |
| silesia.tar | 1 | 128K | 1443.1 | 1473.1 | 102.1% | 2.806 | 2.806 |
| silesia.tar | 3 | 128K | 1409.2 | 1440.2 | 102.2% | 2.958 | 2.958 |
| silesia.tar | 7 | 128K | 1408.0 | 1445.4 | 102.7% | 3.148 | 3.148 |
| silesia.tar | 11 | 128K | 1458.0 | 1495.8 | 102.6% | 3.195 | 3.195 |
| silesia.tar | 1 | 16K | 1078.4 | 1012.4 | 93.9% | 2.620 | 2.620 |
| silesia.tar | 3 | 16K | 1018.0 | 974.3 | 95.7% | 2.675 | 2.675 |
| silesia.tar | 7 | 16K | 1088.4 | 1059.5 | 97.3% | 2.796 | 2.796 |
| silesia.tar | 11 | 16K | 1039.7 | 1009.0 | 97.0% | 2.806 | 2.806 |

Geometric mean of % of C over 64 rows: **101.1%**

## Verification evidence (Task 22, tree c3f5431)

Every go command ran capped, with `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0 env GOMEMLIMIT=2GiB go test -p 1 -count=1`, one process at a time, from 2026-11-05 23:59 to 2026-11-06 05:34. load1 was 3.2 to 7.2.

| command | result |
|---|---|
| `go test ./...` | **pass**: every package ok (zstd 2193.4 s) |
| `go test -tags noasm ./...` | **pass**: every package ok (zstd 1416.9 s) |
| `go test -race -short ./...` | **pass**: every package ok (zstd 2514.8 s) |
| qemu arm64, `-run 'AsmEquiv\|Cref\|Malicious' ./zstd/... ./internal/...` | **pass, reused from Task 21, not rerun.** Task 21 ran it at 4d42162 (every package ok; zstd 7069.8 s). The only code changes from 4d42162 to c3f5431 are comment lines in the two huf .s files, plus bench/README.md |
| `go test -tags long ./zstd` | **FAIL: build failure** (see below) |
| `scripts/apicheck.sh` (full) | **pass**: `apicheck ok` |
| `scripts/decodercheck.sh` | **pass**: `decodercheck: ok (19 decoder files)` |
| coverage gate (CI's command and awk) | **pass**: zstd 96.7%, huf 90.1% of statements; every decoder file is at or above 95% (table below) |
| `scripts/fuzz.sh -t 10m` | **FAIL: 3 of 24 targets** (see below) |
| `grep -rn "TODO\|t.Skip(" zstd internal huff0` | no TODO; 23 conditional `t.Skip` calls, all explained below |

### `-tags long` build failure

```
# github.com/vahid-sohrabloo/fastzstd/zstd [github.com/vahid-sohrabloo/fastzstd/zstd.test]
zstd/stream_long_test.go:77:32: cannot use &nOut (value of type *int64) as int64 value in struct literal
zstd/stream_long_test.go:109:6: countWriter redeclared in this block
	zstd/snappy_test.go:16:6: other declaration of countWriter
zstd/stream_long_test.go:114:23: method countWriter.Write already declared at zstd/snappy_test.go:21:23
zstd/stream_long_test.go:115:3: invalid operation: cannot indirect c.n (variable of type int64)
FAIL	github.com/vahid-sohrabloo/fastzstd/zstd [build failed]
```

`zstd/snappy_test.go` declares a `countWriter` with an `int64` field (first added in 818339f or 9fae87f, 2026-11-03). `stream_long_test.go` (`//go:build long`) declares its own `countWriter` with a `*int64` field. The two clash only under `-tags long`, so the long tests (`stream_long_test.go`, `zstd_decompress_block_long_test.go`) did not run. This is a test-only break. Nothing was changed here.

### Fuzz failures

Each target ran 10 minutes with `-parallel 2 -fuzzminimizetime 2s`, `FASTZSTD_FUZZ_ASM=on`, `FASTZSTD_FUZZ_CAP=1`. 21 targets passed. The three below failed, and each failing input reproduces with `FASTZSTD_FUZZ_ASM=on` **and** `off`, so the asm is not the cause. The inputs are left untracked under `zstd/testdata/fuzz/` for the fix.

```
--- FAIL: FuzzDecoderReadConcurrent (90.79s)
    decoder_mt_test.go:1257: read=false n=5: n>1 2048 bytes, zstd: stream: Data corruption detected; n=1 2048 bytes, unexpected block size
    Failing input written to testdata/fuzz/FuzzDecoderReadConcurrent/9d53354a4fa91e63

--- FAIL: FuzzDecompressDCtx (301.91s)
    decompress_test.go:994: reused dctx: unexpected block size vs zstd: decompress: Data corruption detected
    Failing input written to testdata/fuzz/FuzzDecompressDCtx/e48c5dd8290a083d

--- FAIL: FuzzDecompressBlock (458.43s)
    zstd_decompress_block_test.go:1471: history written
    Failing input written to testdata/fuzz/FuzzDecompressBlock/1db2654624f08464

fuzz.sh: 3 failure(s) (24 targets run)
```

- **FuzzDecompressBlock, "history written".** A block decode returned success but changed bytes before the output position (the earlier history in `dst`) or in `extDict`. If real, this is a correctness bug in the block decoder (Go path), not just an error-text difference.
- **FuzzDecompressDCtx.** The same input decoded twice on one reused dctx gives two different errors: `unexpected block size` on the first decode, `Data corruption detected` on the second. So state leaks from one call into the next.
- **FuzzDecoderReadConcurrent.** The streaming reader with concurrency n>1 and n=1 returns different errors for the same input. This is the same pair of errors, probably the same root cause.

Not investigated further (out of scope for this task).

Passing targets: fse FuzzReadNCount; huf FuzzHUFAsmEquiv, FuzzHUFDecompress4X1, FuzzHUFDecompress4X2, FuzzHUFDecompressX1VsX2, FuzzReadCTable; zstd FuzzEncodeAsmEquiv, FuzzInspectDictionary, FuzzDecodeDict, FuzzDecodeAllCapacity, FuzzDecodeAll, FuzzDecoderRead, FuzzEncoderDict, FuzzDecompressStream, FuzzHeaderDecode, FuzzSnappyConvert, FuzzBuildDict, FuzzTrainDict (60 s cap), FuzzSeqAsmEquiv, FuzzBuildFSETableAsmEquiv, FuzzDecodeSeqHeaders.

### Coverage gate output

```
ok  	github.com/vahid-sohrabloo/fastzstd/zstd	848.731s	coverage: 96.7% of statements
ok  	github.com/vahid-sohrabloo/fastzstd/internal/huf	2.611s	coverage: 90.1% of statements
zstd/zstd_decompress_internal.go     100.00% (1/1)
zstd/zstd_decompress.go               96.96% (797/822)
internal/huf/huf_decompress.go        96.31% (784/814)
zstd/zstd_decompress_block_asm.go    100.00% (43/43)
zstd/zstd_decompress_block.go         99.02% (1012/1022)
internal/huf/huf_decompress_generic.go  98.68% (149/151)
internal/huf/huf_decompress_asm.go   100.00% (13/13)
internal/huf/huf_decompress_amd64.go 100.00% (3/3)
zstd/zstd_decompress_block_amd64.go  100.00% (3/3)
```

### TODO / t.Skip grep

There are no TODO hits. The plan expected only the creftest and guardmem skips, but there are 23 `t.Skip(` hits. They fall into these groups:
- **Expected:**
  - `internal/creftest/creftest.go:41`: the cref binary is missing.
  - `internal/guardmem/guardmem_test.go:20,50`: a child process, or no guard pages on this platform.
- **`testing.Short()` only:**
  - `zstd/zdict_linux_test.go:33,56,72`: maps 4 GiB / 64 MiB / 1 GiB of address space.
  - `zstd/dict_test.go:553`: 17 MiB dictionaries.
  - `zstd/zdict_train_test.go:184,197`: long training cases.
- **Platform only:**
  - `zstd/decoder_test.go:338,368` and `zstd/decoder_mt_test.go:1204`: 32-bit-only tests.
- **Asm mode only, when that asm is off:**
  - `zstd/zstd_decompress_block_asm_test.go:350` and `zstd/zstd_decompress_block_fse_asm_test.go:399`: no BMI2.
  - `internal/huf/huf_buildctable_amd64_test.go:58,108` and `internal/fse/fse_buildctable_amd64_test.go:15,85,112,201,304`.
- **Per-entry and data skips:**
  - `zstd/dstream_test.go:601`: C error corpus entries.
  - `zstd/stream_long_test.go:54`: silesia.tar is missing.

All of them are conditional. None skips unconditionally in the default full amd64 run.
