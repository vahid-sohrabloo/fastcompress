# Task 20c decode CPU profiles: sequence-table build in assembly

Top 10 functions by flat time, from `gobench -mode d -level L -block B -secs 8 -cpuprofile F FILE`, pinned with `taskset -c 2`, each under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0` (GOMEMLIMIT=2GiB), then `go tool pprof -top`. The package prefix `github.com/vahid-sohrabloo/fastcompress/` is stripped. AC power. `perf` is still unavailable (perf_event_paranoid 4, no linux-tools for 6.5.0-28).

- Before: cc7faea (HEAD, Task 20b and its review follow-ups), built from a scratch worktree. 2026-11-05 08:08 to 08:09, load1 1.77 to 1.97.
- After: the Task 20c tree, run right after the before profiles, 2026-11-05 08:09 to 08:10, load1 2.06 to 2.69 (desktop background: Chrome, clickhouse-server).

Rows: silesia.tar L1 and L7 16K (the worst group), silesia.tar L7 128K, nci L1 whole.

## Observations

- `buildFSETable` (Go) was 9.5 to 11.8% at 16K, 4.2% at 128K and 7.5% on nci L1. `buildFSETableAsm` is 5.9 to 6.4% at 16K, 2.2% at 128K and 5.5% on nci L1.
- gobench speed in these profiled runs, before to after: 16K L1 989.7 to 1043.6 MB/s (+5.4%), 16K L7 1014.6 to 1077.3 (+6.2%), 128K L7 1317.5 to 1341.2 (+1.8%), nci L1 2295.8 to 2346.8 (+2.2%).
- The rest of the 16K profile is unchanged: the sequence loop (51 to 54%), the HUF X1 loop (15 to 16%), `ReadDTableX1` (9 to 11% cumulative, about 6% of it `ReadStats`), `ReadNCount` (3%).

## Before

### silesia.tar-L1-B16384, before (989.7 MB/s, load1 1.77)

          flat  flat%   sum%        cum   cum%
         4.30s 48.86% 48.86%      4.30s 48.86%  zstd.decodeExecSeqsAsm
         1.30s 14.77% 63.64%      1.30s 14.77%  internal/huf.decompress4X1FastAsmLoop
         0.80s  9.09% 72.73%      0.84s  9.55%  zstd.buildFSETable
         0.35s  3.98% 76.70%      0.35s  3.98%  internal/huf.decompress4X2FastAsmLoop
         0.31s  3.52% 80.23%      0.84s  9.55%  internal/huf.ReadDTableX1
         0.18s  2.05% 82.27%      0.18s  2.05%  internal/bitstream.(*DStream).LookBits (inline)
         0.16s  1.82% 84.09%      0.24s  2.73%  internal/fse.ReadNCount
         0.12s  1.36% 85.45%      0.27s  3.07%  internal/fse.(*dState).decodeSymbol (inline)
         0.10s  1.14% 86.59%      0.10s  1.14%  internal/bitstream.(*DStream).Reload
         0.10s  1.14% 87.73%      0.10s  1.14%  internal/fse.buildDTable

### silesia.tar-L7-B16384, before (1014.6 MB/s, load1 1.96)

          flat  flat%   sum%        cum   cum%
         4.47s 51.86% 51.86%      4.47s 51.86%  zstd.decodeExecSeqsAsm
         1.34s 15.55% 67.40%      1.34s 15.55%  internal/huf.decompress4X1FastAsmLoop
         0.97s 11.25% 78.65%      1.02s 11.83%  zstd.buildFSETable
         0.38s  4.41% 83.06%      0.83s  9.63%  internal/huf.ReadDTableX1
         0.25s  2.90% 85.96%      0.29s  3.36%  internal/fse.ReadNCount
         0.11s  1.28% 87.24%      0.12s  1.39%  internal/bitstream.(*DStream).Reload
         0.11s  1.28% 88.52%      0.11s  1.28%  internal/huf.rescaleStats
         0.08s  0.93% 89.44%      0.08s  0.93%  internal/fse.buildDTable
         0.08s  0.93% 90.37%      0.08s  0.93%  internal/huf.decompress4X2FastAsmLoop
         0.08s  0.93% 91.30%      0.09s  1.04%  internal/huf.initFastDStream

### silesia.tar-L7-B131072, before (1317.5 MB/s, load1 1.96)

          flat  flat%   sum%        cum   cum%
         5.24s 64.61% 64.61%      5.24s 64.61%  zstd.decodeExecSeqsAsm
         0.71s  8.75% 73.37%      0.71s  8.75%  internal/huf.decompress4X1FastAsmLoop
         0.37s  4.56% 77.93%      0.37s  4.56%  internal/huf.decompress4X2FastAsmLoop
         0.33s  4.07% 82.00%      0.34s  4.19%  zstd.buildFSETable
         0.25s  3.08% 85.08%      0.52s  6.41%  zstd.decodeSequence
         0.18s  2.22% 87.30%      0.37s  4.56%  zstd.execSequence
         0.15s  1.85% 89.15%      0.15s  1.85%  internal/bitstream.(*DStream).Reload
         0.13s  1.60% 90.75%      0.98s 12.08%  zstd.(*dctx).decompressSequencesBodySplitLitBuffer
         0.12s  1.48% 92.23%      0.12s  1.48%  zstd.copy16 (inline)
         0.10s  1.23% 93.46%      0.10s  1.23%  internal/bitstream.(*DStream).LookBits (inline)

### nci-L1-B0, before (2295.8 MB/s, load1 1.97)

          flat  flat%   sum%        cum   cum%
         6.71s 81.23% 81.23%      6.71s 81.23%  zstd.decodeExecSeqsAsm
         0.57s  6.90% 88.14%      0.62s  7.51%  zstd.buildFSETable
         0.39s  4.72% 92.86%      0.39s  4.72%  internal/huf.decompress4X2FastAsmLoop
         0.08s  0.97% 93.83%      0.17s  2.06%  internal/huf.fillDTableX2Level2
         0.06s  0.73% 94.55%      0.09s  1.09%  internal/huf.fillDTableX2ForWeight (inline)
         0.05s  0.61% 95.16%      0.05s  0.61%  zstd.highbit32 (inline)
         0.04s  0.48% 95.64%      0.06s  0.73%  internal/fse.ReadNCount
         0.03s  0.36% 96.00%      6.74s 81.60%  zstd.(*seqFast).runAsm
         0.02s  0.24% 96.25%      0.05s  0.61%  internal/fse.decompressUsingDTable
         0.02s  0.24% 96.49%      0.19s  2.30%  internal/huf.fillDTableX2

## After

### silesia.tar-L1-B16384, after (1043.6 MB/s, load1 2.06)

          flat  flat%   sum%        cum   cum%
        4270ms 51.14% 51.14%     4270ms 51.14%  zstd.decodeExecSeqsAsm
        1230ms 14.73% 65.87%     1230ms 14.73%  internal/huf.decompress4X1FastAsmLoop
         490ms  5.87% 71.74%      490ms  5.87%  zstd.buildFSETableAsm
         320ms  3.83% 75.57%      790ms  9.46%  internal/huf.ReadDTableX1
         290ms  3.47% 79.04%      290ms  3.47%  internal/huf.decompress4X2FastAsmLoop
         220ms  2.63% 81.68%      260ms  3.11%  internal/fse.ReadNCount
         170ms  2.04% 83.71%      170ms  2.04%  internal/bitstream.(*DStream).Reload
         140ms  1.68% 85.39%      560ms  6.71%  internal/huf.ReadStats
         130ms  1.56% 86.95%      170ms  2.04%  internal/huf.fillDTableX2ForWeight
         120ms  1.44% 88.38%      120ms  1.44%  internal/fse.buildDTable

### silesia.tar-L7-B16384, after (1077.3 MB/s, load1 2.29)

          flat  flat%   sum%        cum   cum%
         4.59s 54.26% 54.26%      4.59s 54.26%  zstd.decodeExecSeqsAsm
         1.36s 16.08% 70.33%      1.36s 16.08%  internal/huf.decompress4X1FastAsmLoop
         0.54s  6.38% 76.71%      0.54s  6.38%  zstd.buildFSETableAsm
         0.41s  4.85% 81.56%      0.96s 11.35%  internal/huf.ReadDTableX1
         0.15s  1.77% 83.33%      0.25s  2.96%  internal/fse.ReadNCount
         0.15s  1.77% 85.11%      0.50s  5.91%  internal/huf.ReadStats
         0.12s  1.42% 86.52%      0.19s  2.25%  internal/fse.(*dState).decodeSymbol (inline)
         0.11s  1.30% 87.83%      0.11s  1.30%  internal/huf.decompress4X2FastAsmLoop
         0.10s  1.18% 89.01%      0.10s  1.18%  internal/fse.ReadNCount.func1 (inline)
         0.09s  1.06% 90.07%      0.09s  1.06%  internal/fse.buildDTable

### silesia.tar-L7-B131072, after (1341.2 MB/s, load1 2.63)

          flat  flat%   sum%        cum   cum%
         5.74s 63.15% 63.15%      5.74s 63.15%  zstd.decodeExecSeqsAsm
         0.79s  8.69% 71.84%      0.79s  8.69%  internal/huf.decompress4X1FastAsmLoop
         0.43s  4.73% 76.57%      0.43s  4.73%  internal/huf.decompress4X2FastAsmLoop
         0.36s  3.96% 80.53%      0.78s  8.58%  zstd.decodeSequence
         0.21s  2.31% 82.84%      0.21s  2.31%  internal/bitstream.(*DStream).Reload
         0.20s  2.20% 85.04%      0.20s  2.20%  zstd.buildFSETableAsm
         0.17s  1.87% 86.91%      0.41s  4.51%  zstd.execSequence
         0.13s  1.43% 88.34%      0.13s  1.43%  internal/bitstream.(*DStream).LookBits (inline)
         0.13s  1.43% 89.77%      0.13s  1.43%  zstd.copy16 (inline)
         0.11s  1.21% 90.98%      1.25s 13.75%  zstd.(*dctx).decompressSequencesBodySplitLitBuffer

### nci-L1-B0, after (2346.8 MB/s, load1 2.69)

          flat  flat%   sum%        cum   cum%
         6.81s 83.35% 83.35%      6.81s 83.35%  zstd.decodeExecSeqsAsm
         0.45s  5.51% 88.86%      0.45s  5.51%  zstd.buildFSETableAsm
         0.42s  5.14% 94.00%      0.42s  5.14%  internal/huf.decompress4X2FastAsmLoop
         0.12s  1.47% 95.47%      0.13s  1.59%  internal/huf.fillDTableX2ForWeight (inline)
         0.09s  1.10% 96.57%      0.21s  2.57%  internal/huf.fillDTableX2Level2
         0.06s  0.73% 97.31%      0.06s  0.73%  internal/bitstream.(*DStream).Reload
         0.05s  0.61% 97.92%      0.27s  3.30%  internal/huf.fillDTableX2
         0.02s  0.24% 98.16%      0.08s  0.98%  internal/huf.ReadStats
         0.01s  0.12% 98.29%      0.36s  4.41%  internal/huf.ReadDTableX2
         0.01s  0.12% 98.41%      8.17s   100%  zstd.(*dctx).decompressFrame

