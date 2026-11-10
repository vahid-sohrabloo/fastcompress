# Task 20a decode CPU profiles: sequence fast loop in amd64 assembly

Top 10 functions by flat time, from `gobench -mode d -level L -block B -secs S -cpuprofile F FILE`, pinned with `taskset -c 2`, each under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0` (GOMEMLIMIT=2GiB), then `go tool pprof -top -nodecount 10`. The package prefix `github.com/vahid-sohrabloo/fastcompress/` is stripped. Go go1.27.1, AC power. Each heading gives the gobench speed of that run.

- Before: e539c49 (Task 19), 2026-11-04 17:32, load1 0.91 to 1.14, `-secs 8`.
- After: the Task 20a tree, 2026-11-04 19:37, load1 1.32 to 1.45, `-secs 8`.
- `perf record` was not available: `kernel.perf_event_paranoid` is 4 and the running kernel's linux-tools are not installed. These are pprof profiles only.

Rows: dickens L3 whole (the worst row of `2026-11-04-asm-huf.md`, 37.8% of C), silesia.tar L1 16K (the 16K group, where table building matters most) and silesia.tar L3 whole.

## Observations

- The target is confirmed. Before, `decompressSequencesBody` was 96% cumulative at dickens L3, 87% at silesia.tar L3 and 67% at L1 16K. Within it, `decodeSequence` (with the bitstream's `LookBits`, `ReadBits` and `Reload`) was 39% to 53% cumulative, and `execSequence` with `copy16` and `wildcopy` 23% to 35%.
- After, `decodeExecSeqsAsm` is the whole loop: 90% flat at dickens L3, 74% at silesia.tar L3 and 47% at L1 16K. The Go loop that finishes each block (`decodeSequence`, `Reload`) is at most 2%.
- Speed: dickens L3 506.7 to 1310.2 MB/s (2.59x), silesia.tar L3 702.7 to 1406.2 MB/s (2.00x), silesia.tar L1 16K 611.7 to 981.8 MB/s (1.61x).
- At 16K the table builders are now the next target: `buildFSETable` 10%, `ReadDTableX1` 9% cumulative, the Huffman fast loops 19%. This is candidate 3 of the brief, carried forward.
- An intermediate version kept the three cells in frame slots (stored, then reloaded field by field). It ran dickens L3 at 1196 MB/s. Keeping the cells in registers, and loading the next sequence's cells right after the state updates, took that to 1308 MB/s (interleaved runs: 1196.4/1309.9, 1196.4/1305.8). The store-forwarded field reads were on the bit-reading dependency chain.

## Before

### dickens L3 whole, before (506.7 MB/s)

          flat  flat%   sum%        cum   cum%
        1900ms 23.54% 23.54%     4310ms 53.41%  zstd.decodeSequence
        1180ms 14.62% 38.17%     1190ms 14.75%  zstd.copy16 (inline)
        1080ms 13.38% 51.55%     2750ms 34.08%  zstd.execSequence
         940ms 11.65% 63.20%      950ms 11.77%  internal/bitstream.(*DStream).LookBits (inline)
         810ms 10.04% 73.23%      810ms 10.04%  internal/bitstream.(*DStream).Reload
         680ms  8.43% 81.66%     7740ms 95.91%  zstd.(*dctx).decompressSequencesBody
         470ms  5.82% 87.48%     1510ms 18.71%  zstd.wildcopy
         270ms  3.35% 90.83%      270ms  3.35%  internal/bitstream.(*DStream).LookBitsFast (inline)
         180ms  2.23% 93.06%     1130ms 14.00%  internal/bitstream.(*DStream).ReadBits (inline)
         160ms  1.98% 95.04%      170ms  2.11%  zstd.buildFSETable

### silesia.tar L1 16K, before (611.7 MB/s)

          flat  flat%   sum%        cum   cum%
        1480ms 17.09% 17.09%     3380ms 39.03%  zstd.decodeSequence
         880ms 10.16% 27.25%      880ms 10.16%  internal/huf.decompress4X1FastAsmLoop
         800ms  9.24% 36.49%      830ms  9.58%  internal/bitstream.(*DStream).Reload
         790ms  9.12% 45.61%     2010ms 23.21%  zstd.execSequence
         780ms  9.01% 54.62%      780ms  9.01%  zstd.copy16 (inline)
         700ms  8.08% 62.70%      700ms  8.08%  internal/bitstream.(*DStream).LookBits (inline)
         550ms  6.35% 69.05%      570ms  6.58%  zstd.buildFSETable
         380ms  4.39% 73.44%     5810ms 67.09%  zstd.(*dctx).decompressSequencesBody
         370ms  4.27% 77.71%      910ms 10.51%  zstd.wildcopy
         270ms  3.12% 80.83%      520ms  6.00%  internal/huf.ReadDTableX1

### silesia.tar L3 whole, before (702.7 MB/s)

          flat  flat%   sum%        cum   cum%
        1840ms 21.03% 21.03%     4080ms 46.63%  zstd.decodeSequence
        1350ms 15.43% 36.46%     1350ms 15.43%  zstd.copy16 (inline)
        1070ms 12.23% 48.69%     3080ms 35.20%  zstd.execSequence
         830ms  9.49% 58.17%      830ms  9.49%  internal/bitstream.(*DStream).LookBits (inline)
         760ms  8.69% 66.86%      760ms  8.69%  internal/bitstream.(*DStream).Reload
         620ms  7.09% 73.94%     1690ms 19.31%  zstd.wildcopy
         460ms  5.26% 79.20%     7630ms 87.20%  zstd.(*dctx).decompressSequencesBody
         350ms  4.00% 83.20%      350ms  4.00%  internal/huf.decompress4X1FastAsmLoop
         230ms  2.63% 85.83%      240ms  2.74%  zstd.buildFSETable
         200ms  2.29% 88.11%     1030ms 11.77%  internal/bitstream.(*DStream).ReadBits (inline)

## After

### dickens L3 whole, after (1310.2 MB/s)

          flat  flat%   sum%        cum   cum%
         7.27s 89.75% 89.75%      7.27s 89.75%  zstd.decodeExecSeqsAsm
         0.26s  3.21% 92.96%      0.26s  3.21%  zstd.buildFSETable
         0.20s  2.47% 95.43%      0.20s  2.47%  internal/huf.decompress4X2FastAsmLoop
         0.05s  0.62% 96.05%      0.07s  0.86%  internal/huf.fillDTableX2ForWeight (inline)
         0.04s  0.49% 96.54%      0.11s  1.36%  internal/huf.fillDTableX2Level2
         0.01s  0.12% 96.67%      0.19s  2.35%  internal/huf.ReadDTableX2
         0.01s  0.12% 96.79%      0.06s  0.74%  internal/huf.ReadStats
         0.01s  0.12% 96.91%      0.21s  2.59%  internal/huf.decompress4X2FastLoop
         0.01s  0.12% 97.04%      0.12s  1.48%  internal/huf.fillDTableX2
         0.01s  0.12% 97.16%      8.10s   100%  zstd.(*dctx).decompressFrames

### silesia.tar L1 16K, after (981.8 MB/s)

          flat  flat%   sum%        cum   cum%
        4140ms 46.83% 46.83%     4140ms 46.83%  zstd.decodeExecSeqsAsm
        1270ms 14.37% 61.20%     1270ms 14.37%  internal/huf.decompress4X1FastAsmLoop
         890ms 10.07% 71.27%      940ms 10.63%  zstd.buildFSETable
         430ms  4.86% 76.13%      800ms  9.05%  internal/huf.ReadDTableX1
         380ms  4.30% 80.43%      380ms  4.30%  internal/huf.decompress4X2FastAsmLoop
         180ms  2.04% 82.47%      180ms  2.04%  internal/bitstream.(*DStream).Reload
         150ms  1.70% 84.16%      270ms  3.05%  internal/huf.fillDTableX2Level2
         130ms  1.47% 85.63%      160ms  1.81%  internal/fse.buildDTable
         120ms  1.36% 86.99%      200ms  2.26%  internal/fse.ReadNCount
         100ms  1.13% 88.12%      160ms  1.81%  internal/huf.fillDTableX2ForWeight (inline)

### silesia.tar L3 whole, after (1406.2 MB/s)

          flat  flat%   sum%        cum   cum%
         6.34s 73.81% 73.81%      6.34s 73.81%  zstd.decodeExecSeqsAsm
         0.76s  8.85% 82.65%      0.76s  8.85%  internal/huf.decompress4X1FastAsmLoop
         0.43s  5.01% 87.66%      0.46s  5.36%  zstd.buildFSETable
         0.31s  3.61% 91.27%      0.31s  3.61%  internal/huf.decompress4X2FastAsmLoop
         0.11s  1.28% 92.55%      0.24s  2.79%  internal/huf.ReadDTableX1
         0.07s  0.81% 93.36%      0.12s  1.40%  internal/fse.ReadNCount
         0.05s  0.58% 93.95%      0.06s   0.7%  internal/bitstream.(*DStream).Reload
         0.04s  0.47% 94.41%      0.05s  0.58%  internal/fse.ReadNCount.func1 (inline)
         0.04s  0.47% 94.88%      0.06s   0.7%  internal/fse.buildDTable
         0.03s  0.35% 95.23%      0.19s  2.21%  internal/huf.ReadStats
