# Task 20b decode CPU profiles: FSE/HUF table building

Top 10 functions by flat time, from `gobench -mode d -level L -block B -secs 8 -cpuprofile F FILE`, pinned with `taskset -c 2`, each under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0` (GOMEMLIMIT=2GiB), then `go tool pprof -top`. The package prefix `github.com/vahid-sohrabloo/fastcompress/` is stripped. Go go1.27.1, AC power. `perf` is still unavailable (perf_event_paranoid 4, no linux-tools for 6.5.0-28).

- Before: 59c35a4 (Task 20a + its N1/N2 follow-up), 2026-11-04 ~23:40, load1 2.8 to 3.4 (desktop background: Chrome, clickhouse-server). All load1 values here, before and after, are above the < 2 rule; these profiles are indicative of the call mix, not of absolute speed.
- After: the Task 20b tree, 2026-11-05 ~02:45, load1 3.1 to 3.4 (same background).

Rows: silesia.tar L1 and L7 16K (the worst group), silesia.tar L7 128K, nci L1 whole.

## Observations

- At 16K the table builders are 21 to 24% cumulative: `buildFSETable` 10 to 12%, `ReadDTableX1` 9 to 12% (of which `ReadStats`, the FSE-coded weights, about half), `ReadNCount` 3%.
- This round removed the one per-block heap allocation on the decode path (the weight reader's FSE table, `fse_decompress.go:51`, 64 B per Huffman table read: 1 allocation per 16K frame before, 0 after, measured with `testing.AllocsPerRun` over 64 DecodeAll calls), and ported C's length switch for the X1 fill. `ReadDTableX1` microbenchmark (dickens 16K histogram, tableLog 11): 2555 ns, 1 alloc before; 2226 ns, 0 allocs after. C 1.5.7 `HUF_readDTableX1_wksp` on the same header (gcc -O3): 1505 ns.
- `buildFSETable` was NOT changed in the end. A C-vs-Go microbenchmark (same norm, 36 symbols, no low-probability symbols) gives Go 1210 ns vs C 682 ns at tableLog 9 and 244 vs 133 ns at tableLog 6 (1.8x). The last loop (cells) alone is Go ~930 ns vs C 470 ns for 512 cells. Five Go variants were measured and none closed the gap: a written-out spread unroll, one struct store per cell, the loop in its own function, 256-entry workspace arrays indexed by the symbol byte (no bounds checks), and a pointer walk (plain displacement stores). The best was 3% faster in isolation and none was faster inside the real call; the Go loop has about 1.5x C's instructions (address LEAs per field store, BSR via 2x+1, spills), and the copy of the per-symbol tables cost what the bounds checks saved. Closing it needs assembly (a later round).

## Before

### silesia.tar-L1-B16384, before (974.5 MB/s, load1 2.82)

          flat  flat%   sum%        cum   cum%
     4.14s 46.05% 46.05%      4.14s 46.05%  zstd.decodeExecSeqsAsm
     1.23s 13.68% 59.73%      1.23s 13.68%  internal/huf.decompress4X1FastAsmLoop
     1.07s 11.90% 71.64%      1.11s 12.35%  zstd.buildFSETable
     0.44s  4.89% 76.53%      0.44s  4.89%  internal/huf.decompress4X2FastAsmLoop
     0.38s  4.23% 80.76%      0.87s  9.68%  internal/huf.ReadDTableX1
     0.25s  2.78% 83.54%      0.29s  3.23%  internal/fse.ReadNCount
     0.13s  1.45% 84.98%      0.20s  2.22%  internal/fse.(*dState).decodeSymbol (inline)
     0.13s  1.45% 86.43%      0.14s  1.56%  internal/huf.fillDTableX2ForWeight
     0.12s  1.33% 87.76%      0.12s  1.33%  internal/bitstream.(*DStream).Reload
     0.11s  1.22% 88.99%      0.11s  1.22%  internal/fse.buildDTable

### silesia.tar-L7-B16384, before (993.0 MB/s, load1 3.39)

          flat  flat%   sum%        cum   cum%
     4.62s 51.56% 51.56%      4.62s 51.56%  zstd.decodeExecSeqsAsm
     1.33s 14.84% 66.41%      1.33s 14.84%  internal/huf.decompress4X1FastAsmLoop
     0.93s 10.38% 76.79%      1.01s 11.27%  zstd.buildFSETable
     0.56s  6.25% 83.04%      1.12s 12.50%  internal/huf.ReadDTableX1
     0.15s  1.67% 84.71%      0.20s  2.23%  internal/fse.ReadNCount
     0.12s  1.34% 86.05%      0.12s  1.34%  internal/huf.decompress4X2FastAsmLoop
     0.11s  1.23% 87.28%      0.11s  1.23%  internal/bitstream.(*DStream).LookBits (inline)
     0.11s  1.23% 88.50%      0.51s  5.69%  internal/huf.ReadStats
     0.10s  1.12% 89.62%      0.19s  2.12%  internal/fse.(*dState).decodeSymbol (inline)
     0.10s  1.12% 90.74%      0.14s  1.56%  internal/fse.buildDTable

### silesia.tar-L7-B131072, before (1309.9 MB/s, load1 3.25)

          flat  flat%   sum%        cum   cum%
     5.46s 66.59% 66.59%      5.46s 66.59%  zstd.decodeExecSeqsAsm
     0.48s  5.85% 72.44%      0.48s  5.85%  internal/huf.decompress4X1FastAsmLoop
     0.42s  5.12% 77.56%      0.42s  5.12%  internal/huf.decompress4X2FastAsmLoop
     0.35s  4.27% 81.83%      0.39s  4.76%  zstd.buildFSETable
     0.20s  2.44% 84.27%      0.58s  7.07%  zstd.decodeSequence
     0.17s  2.07% 86.34%      0.17s  2.07%  internal/bitstream.(*DStream).Reload
     0.16s  1.95% 88.29%      0.30s  3.66%  zstd.execSequence
     0.12s  1.46% 89.76%      0.13s  1.59%  internal/bitstream.(*DStream).LookBits (inline)
     0.11s  1.34% 91.10%      0.98s 11.95%  zstd.(*dctx).decompressSequencesBodySplitLitBuffer
     0.09s  1.10% 92.20%      0.09s  1.10%  zstd.copy16 (inline)

### nci-L1-B0, before (2271.9 MB/s, load1 3.21)

          flat  flat%   sum%        cum   cum%
     6.81s 82.35% 82.35%      6.81s 82.35%  zstd.decodeExecSeqsAsm
     0.49s  5.93% 88.27%      0.51s  6.17%  zstd.buildFSETable
     0.33s  3.99% 92.26%      0.33s  3.99%  internal/huf.decompress4X2FastAsmLoop
     0.10s  1.21% 93.47%      0.13s  1.57%  internal/huf.fillDTableX2ForWeight (inline)
     0.09s  1.09% 94.56%      0.09s  1.09%  internal/fse.ReadNCount
     0.07s  0.85% 95.41%      0.20s  2.42%  internal/huf.fillDTableX2Level2
     0.03s  0.36% 95.77%      0.17s  2.06%  internal/huf.ReadStats
     0.02s  0.24% 96.01%      0.39s  4.72%  internal/huf.ReadDTableX2
     0.02s  0.24% 96.25%      6.91s 83.56%  zstd.(*dctx).decompressSequencesBody
     0.02s  0.24% 96.49%      0.05s   0.6%  zstd.decodeSequence
         0     0% 96.86%      0.05s   0.6%  internal/fse.decompressUsingDTable
         0     0% 96.86%      0.38s  4.59%  internal/huf.Decompress4X2UsingDTable

## After

### silesia.tar-L1-B16384, after (987.1 MB/s, load1 3.21)

          flat  flat%   sum%        cum   cum%
     4.08s 46.36% 46.36%      4.08s 46.36%  zstd.decodeExecSeqsAsm
     1.31s 14.89% 61.25%      1.31s 14.89%  internal/huf.decompress4X1FastAsmLoop
     0.84s  9.55% 70.80%      0.92s 10.45%  zstd.buildFSETable
     0.51s  5.80% 76.59%      0.51s  5.80%  internal/huf.decompress4X2FastAsmLoop
     0.46s  5.23% 81.82%      0.82s  9.32%  internal/huf.ReadDTableX1
     0.22s  2.50% 84.32%      0.28s  3.18%  internal/fse.ReadNCount
     0.15s  1.70% 86.02%      0.18s  2.05%  internal/huf.fillDTableX2ForWeight
     0.14s  1.59% 87.61%      0.14s  1.59%  internal/bitstream.(*DStream).Reload
     0.09s  1.02% 88.64%      0.09s  1.02%  internal/bitstream.(*DStream).LookBits (inline)
     0.08s  0.91% 89.55%      0.08s  0.91%  internal/fse.buildDTable

### silesia.tar-L7-B16384, after (999.1 MB/s, load1 3.10)

          flat  flat%   sum%        cum   cum%
     4.43s 49.89% 49.89%      4.43s 49.89%  zstd.decodeExecSeqsAsm
     1.29s 14.53% 64.41%      1.29s 14.53%  internal/huf.decompress4X1FastAsmLoop
     1.03s 11.60% 76.01%      1.07s 12.05%  zstd.buildFSETable
     0.49s  5.52% 81.53%      1.05s 11.82%  internal/huf.ReadDTableX1
     0.22s  2.48% 84.01%      0.26s  2.93%  internal/fse.ReadNCount
     0.15s  1.69% 85.70%      0.18s  2.03%  internal/bitstream.(*DStream).Reload
     0.10s  1.13% 86.82%      0.14s  1.58%  internal/fse.(*dState).decodeSymbol (inline)
     0.10s  1.13% 87.95%      0.48s  5.41%  internal/huf.ReadStats
     0.08s   0.9% 88.85%      0.08s   0.9%  internal/huf.rescaleStats
     0.07s  0.79% 89.64%      0.07s  0.79%  internal/bitstream.(*DStream).Init

### silesia.tar-L7-B131072, after (1317.4 MB/s, load1 3.17)

          flat  flat%   sum%        cum   cum%
     5.13s 63.65% 63.65%      5.13s 63.65%  zstd.decodeExecSeqsAsm
     0.63s  7.82% 71.46%      0.63s  7.82%  internal/huf.decompress4X1FastAsmLoop
     0.41s  5.09% 76.55%      0.41s  5.09%  internal/huf.decompress4X2FastAsmLoop
     0.39s  4.84% 81.39%      0.43s  5.33%  zstd.buildFSETable
     0.28s  3.47% 84.86%      0.64s  7.94%  zstd.decodeSequence
     0.17s  2.11% 86.97%      0.17s  2.11%  internal/bitstream.(*DStream).LookBits (inline)
     0.14s  1.74% 88.71%      0.24s  2.98%  zstd.execSequence
     0.12s  1.49% 90.20%      0.12s  1.49%  internal/bitstream.(*DStream).Reload
     0.08s  0.99% 91.19%      0.08s  0.99%  zstd.copy16 (inline)
     0.07s  0.87% 92.06%      0.08s  0.99%  internal/huf.decodeSymbolX2

### nci-L1-B0, after (2295.6 MB/s, load1 3.44)

          flat  flat%   sum%        cum   cum%
     6.65s 80.70% 80.70%      6.65s 80.70%  zstd.decodeExecSeqsAsm
     0.63s  7.65% 88.35%      0.66s  8.01%  zstd.buildFSETable
     0.30s  3.64% 91.99%      0.30s  3.64%  internal/huf.decompress4X2FastAsmLoop
     0.13s  1.58% 93.57%      0.16s  1.94%  internal/huf.fillDTableX2ForWeight (inline)
     0.07s  0.85% 94.42%      0.07s  0.85%  internal/bitstream.(*DStream).Reload
     0.05s  0.61% 95.02%      0.20s  2.43%  internal/huf.fillDTableX2Level2
     0.04s  0.49% 95.51%      0.06s  0.73%  internal/fse.(*dState).decodeSymbol (inline)
     0.04s  0.49% 96.00%      0.06s  0.73%  internal/fse.ReadNCount
     0.01s  0.12% 96.12%      0.34s  4.13%  internal/huf.ReadDTableX2
     0.01s  0.12% 96.24%      0.13s  1.58%  internal/huf.ReadStats
