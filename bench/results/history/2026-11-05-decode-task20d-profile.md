# Task 20d decode CPU profiles: split-literal-buffer sequences in the assembly loop

Top 10 functions by flat time, from `gobench -mode d -level L -block B -secs 8 -cpuprofile F FILE`, pinned with `taskset -c 2`, each under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0` (GOMEMLIMIT=2GiB), then `go tool pprof -top`. The package prefix `github.com/vahid-sohrabloo/fastcompress/` is stripped. AC power. `perf` is still unavailable (perf_event_paranoid 4, no linux-tools for 6.5.0-28).

- Before: e25e330 (8fe8754's code plus the test-only 20c follow-ups), built from a scratch worktree.
- After: the Task 20d tree (two assembly entries, `decodeExecSeqsAsm` byte-identical to before, plus `decodeExecSeqsSplitAsm`).
- Before and after alternate per row, 2026-11-05 13:13:45 to 13:15:15, load1 2.85 to 3.09 (desktop background: Chrome, clickhouse-server). This breaks the load1 < 2 rule. Before and after run back to back, so the ratios hold better than the speeds.

Rows: silesia.tar 128K at L7, L11 and L1 (the rows whose blocks take `decompressSequencesBodySplitLitBuffer`), and nci L1 whole (a control: its blocks never split).

## Observations

| row | gobench MB/s before | after | change | split body cum before | after | Go `decodeSequence` cum before |
|---|---|---|---|---|---|---|
| silesia.tar 128K L7 | 1339.4 | 1452.3 | +8.4% | 12.1% | 5.5% | 6.8% |
| silesia.tar 128K L11 | 1388.3 | 1496.8 | +7.8% | 12.2% | 5.2% | 7.9% |
| silesia.tar 128K L1 | 1411.1 | 1491.2 | +5.7% | 11.0% | 4.7% | 6.1% |
| nci L1 | 2337.5 | 2347.1 | +0.4% | - | - | - |

- Before, the split blocks ran Go's `decodeSequence` (6 to 8% cumulative) and `execSequence` (3.4 to 4.9%). After, `decodeSequence` is out of the top 60, the first loop is `decodeExecSeqsSplitAsm` (0.7 to 0.8%; few sequences, many literals), and the second loop is `decodeExecSeqsAsm` through `sequencesFast`. What is left of the split body's 5% is mostly that asm time, plus the switch, the last literals and the Go loops' tail sequences.
- nci L1 never splits, and its `decodeExecSeqsAsm` is the same machine code as before.
- An earlier version with one entry and a run-time `split` switch in the shared loop (2 instructions) lost 1 to 2% on non-split rows (nci L1 2341 to 2300 MB/s, 16K L7 1079 to 1063, interleaved). Replacing the 2 instructions with a 12-byte NOP recovered only part of it, so code layout mattered as much as the instructions. That version was dropped for the two-entry layout.

### silesia.tar-L7-B131072, before

```
      flat  flat%   sum%        cum   cum%
    5220ms 65.33% 65.33%     5220ms 65.33%  zstd.decodeExecSeqsAsm
     720ms  9.01% 74.34%      720ms  9.01%  internal/huf.decompress4X1FastAsmLoop
     460ms  5.76% 80.10%      460ms  5.76%  internal/huf.decompress4X2FastAsmLoop
     240ms  3.00% 83.10%      540ms  6.76%  zstd.decodeSequence
     170ms  2.13% 85.23%      170ms  2.13%  zstd.buildFSETableAsm
     160ms  2.00% 87.23%      390ms  4.88%  zstd.execSequence
     150ms  1.88% 89.11%      150ms  1.88%  internal/bitstream.(*DStream).Reload
     140ms  1.75% 90.86%      140ms  1.75%  zstd.copy16 (inline)
     100ms  1.25% 92.12%      100ms  1.25%  internal/bitstream.(*DStream).LookBits (inline)
      80ms  1.00% 93.12%      150ms  1.88%  zstd.wildcopy
```

### silesia.tar-L7-B131072, after

```
      flat  flat%   sum%        cum   cum%
     6.13s 73.41% 73.41%      6.13s 73.41%  zstd.decodeExecSeqsAsm
     0.74s  8.86% 82.28%      0.74s  8.86%  internal/huf.decompress4X1FastAsmLoop
     0.55s  6.59% 88.86%      0.55s  6.59%  internal/huf.decompress4X2FastAsmLoop
     0.37s  4.43% 93.29%      0.37s  4.43%  zstd.buildFSETableAsm
     0.06s  0.72% 94.01%      0.06s  0.72%  zstd.decodeExecSeqsSplitAsm
     0.05s   0.6% 94.61%      0.05s   0.6%  internal/bitstream.(*DStream).LookBits (inline)
     0.05s   0.6% 95.21%      0.05s   0.6%  internal/bitstream.(*DStream).Reload
     0.04s  0.48% 95.69%      0.08s  0.96%  internal/huf.fillDTableX2Level2
     0.03s  0.36% 96.05%      0.11s  1.32%  internal/huf.ReadDTableX1
     0.03s  0.36% 96.41%      0.16s  1.92%  internal/huf.ReadDTableX2
```

### silesia.tar-L11-B131072, before

```
      flat  flat%   sum%        cum   cum%
    5660ms 64.61% 64.61%     5660ms 64.61%  zstd.decodeExecSeqsAsm
     700ms  7.99% 72.60%      700ms  7.99%  internal/huf.decompress4X1FastAsmLoop
     510ms  5.82% 78.42%      510ms  5.82%  internal/huf.decompress4X2FastAsmLoop
     240ms  2.74% 81.16%      690ms  7.88%  zstd.decodeSequence
     210ms  2.40% 83.56%      210ms  2.40%  zstd.buildFSETableAsm
     160ms  1.83% 85.39%      160ms  1.83%  internal/bitstream.(*DStream).Reload
     150ms  1.71% 87.10%      150ms  1.71%  internal/bitstream.(*DStream).LookBits (inline)
     150ms  1.71% 88.81%      150ms  1.71%  zstd.copy16 (inline)
      90ms  1.03% 89.84%     1070ms 12.21%  zstd.(*dctx).decompressSequencesBodySplitLitBuffer
      90ms  1.03% 90.87%      320ms  3.65%  zstd.execSequence
```

### silesia.tar-L11-B131072, after

```
      flat  flat%   sum%        cum   cum%
     6.33s 75.18% 75.18%      6.33s 75.18%  zstd.decodeExecSeqsAsm
     0.75s  8.91% 84.09%      0.75s  8.91%  internal/huf.decompress4X1FastAsmLoop
     0.44s  5.23% 89.31%      0.44s  5.23%  internal/huf.decompress4X2FastAsmLoop
     0.25s  2.97% 92.28%      0.25s  2.97%  zstd.buildFSETableAsm
     0.09s  1.07% 93.35%      0.10s  1.19%  internal/huf.fillDTableX2ForWeight (inline)
     0.06s  0.71% 94.06%      0.15s  1.78%  internal/huf.fillDTableX2Level2
     0.04s  0.48% 94.54%      0.07s  0.83%  internal/fse.ReadNCount
     0.03s  0.36% 94.89%      0.12s  1.43%  internal/huf.ReadDTableX1
     0.02s  0.24% 95.13%      0.11s  1.31%  internal/huf.ReadStats
     0.02s  0.24% 95.37%      0.05s  0.59%  zstd.execSequence
```

### silesia.tar-L1-B131072, before

```
      flat  flat%   sum%        cum   cum%
    4770ms 55.79% 55.79%     4770ms 55.79%  zstd.decodeExecSeqsAsm
    1270ms 14.85% 70.64%     1270ms 14.85%  internal/huf.decompress4X2FastAsmLoop
     650ms  7.60% 78.25%      650ms  7.60%  internal/huf.decompress4X1FastAsmLoop
     230ms  2.69% 80.94%      520ms  6.08%  zstd.decodeSequence
     140ms  1.64% 82.57%      200ms  2.34%  internal/huf.fillDTableX2ForWeight
     140ms  1.64% 84.21%      140ms  1.64%  zstd.copy16 (inline)
     130ms  1.52% 85.73%      130ms  1.52%  zstd.buildFSETableAsm
     120ms  1.40% 87.13%      240ms  2.81%  zstd.wildcopy
     110ms  1.29% 88.42%      110ms  1.29%  internal/bitstream.(*DStream).LookBits (inline)
     100ms  1.17% 89.59%      140ms  1.64%  internal/huf.decodeSymbolX2
```

### silesia.tar-L1-B131072, after

```
      flat  flat%   sum%        cum   cum%
     5.70s 62.57% 62.57%      5.70s 62.57%  zstd.decodeExecSeqsAsm
     1.68s 18.44% 81.01%      1.68s 18.44%  internal/huf.decompress4X2FastAsmLoop
     0.69s  7.57% 88.58%      0.69s  7.57%  internal/huf.decompress4X1FastAsmLoop
     0.20s  2.20% 90.78%      0.20s  2.20%  zstd.buildFSETableAsm
     0.10s  1.10% 91.88%      0.12s  1.32%  internal/huf.fillDTableX2ForWeight
     0.08s  0.88% 92.76%      0.11s  1.21%  internal/huf.decodeSymbolX2
     0.07s  0.77% 93.52%      0.07s  0.77%  zstd.decodeExecSeqsSplitAsm
     0.07s  0.77% 94.29%      0.07s  0.77%  runtime.memmove
     0.06s  0.66% 94.95%      0.06s  0.66%  internal/bitstream.(*DStream).LookBits (inline)
     0.06s  0.66% 95.61%      0.12s  1.32%  internal/huf.ReadDTableX1
```

### nci-L1-B0, before

```
      flat  flat%   sum%        cum   cum%
     6.87s 83.68% 83.68%      6.87s 83.68%  zstd.decodeExecSeqsAsm
     0.41s  4.99% 88.67%      0.41s  4.99%  zstd.buildFSETableAsm
     0.33s  4.02% 92.69%      0.33s  4.02%  internal/huf.decompress4X2FastAsmLoop
     0.10s  1.22% 93.91%      0.10s  1.22%  internal/huf.fillDTableX2ForWeight (inline)
     0.07s  0.85% 94.76%      0.10s  1.22%  internal/fse.ReadNCount
     0.07s  0.85% 95.62%      0.18s  2.19%  internal/huf.fillDTableX2Level2
     0.03s  0.37% 95.98%      0.21s  2.56%  internal/huf.fillDTableX2
     0.02s  0.24% 96.22%      8.21s   100%  zstd.(*dctx).decompressBlockInternal
     0.02s  0.24% 96.47%      0.55s  6.70%  zstd.decodeSeqHeaders
     0.01s  0.12% 96.59%      0.40s  4.87%  internal/huf.Decompress4X2UsingDTable
```

### nci-L1-B0, after

```
      flat  flat%   sum%        cum   cum%
     6.82s 82.57% 82.57%      6.82s 82.57%  zstd.decodeExecSeqsAsm
     0.42s  5.08% 87.65%      0.42s  5.08%  zstd.buildFSETableAsm
     0.39s  4.72% 92.37%      0.39s  4.72%  internal/huf.decompress4X2FastAsmLoop
     0.12s  1.45% 93.83%      0.18s  2.18%  internal/huf.fillDTableX2Level2
     0.06s  0.73% 94.55%      0.06s  0.73%  internal/huf.fillDTableX2ForWeight (inline)
     0.05s  0.61% 95.16%      0.05s  0.61%  internal/huf.decompress4X1FastAsmLoop
     0.04s  0.48% 95.64%      0.05s  0.61%  internal/fse.ReadNCount
     0.03s  0.36% 96.00%      0.32s  3.87%  internal/huf.ReadDTableX2
     0.02s  0.24% 96.25%      0.11s  1.33%  internal/huf.ReadStats
     0.02s  0.24% 96.49%      6.84s 82.81%  zstd.(*seqFast).runAsm
```

