# Pure-Go decode CPU profiles (baseline for Tasks 19/20)

Top 15 functions by flat time, from `gobench -mode d -level L -block B -secs 10 -cpuprofile F bench/corpus/silesia.tar`, pinned with `taskset -c 2`. Each run is under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0` (GOMEMLIMIT=2GiB) and `go tool pprof -top -nodecount 15`. Go go1.27.1, 2026-11-04, AC power, load1 of 1.6 to 2.6 (desktop background only; no other benchmark or build running). The profile covers only the timed DecodeAll passes. Package prefix `github.com/vahid-sohrabloo/fastzstd/` is stripped. Each heading gives the gobench speed of that run; the C speeds are in `2026-11-04-decode-pure-go.md`.

## Observations

- Sequence decoding dominates at every level and block size. `decompressSequencesBody` is 61% to 84% cumulative. Within it, `decodeSequence` is 15% to 22% flat (35% to 47% cumulative) and `execSequence` is 8% to 14% flat. `copy16` plus `wildcopy` (match and literal copies) are 11% to 27% flat combined. The share is highest at L11 whole (27%, of which 21% is `copy16`), and copies grow with level on whole files. The bitstream reader (`Reload`, `LookBits`, `ReadBits`, `LookBitsFast`, `SkipBits`) is 19% to 24% flat combined, mostly inlined into `decodeSequence`.
- Huffman literals: `decompress4X1FastCLoop` plus `decompress4X2FastCLoop` are 8% to 25% flat (counting the top 15 only). The share is largest at L1 (20% to 25%, with X2 heavy on whole and 1M blocks) and at 16K (15% to 20%).
- At 16K, per-block table building shows up: `buildFSETable` is 5.5% to 7.1% flat, `huf.ReadDTableX1` 5% to 7% cumulative and `fse.ReadNCount` about 1.5%, so roughly 12% to 14% together. For 1M and whole blocks it is under 6%.
- Allocations: `BenchmarkDecodeAll/silesia.tar/3/16K` reports 12485 allocs/op (about one per 16K frame, 1.6 MB/op). `xml/3/whole` reports 43 allocs/op. The CPU profiles show no GC or malloc functions in the top 15.

## L1 silesia.tar whole (745.3 MB/s)

          flat  flat%   sum%        cum   cum%
         2.20s 20.66% 20.66%      4.45s 41.78%  zstd.decodeSequence
         1.45s 13.62% 34.27%      1.53s 14.37%  internal/huf.decompress4X2FastCLoop
         1.09s 10.23% 44.51%      1.11s 10.42%  internal/huf.decompress4X1FastCLoop
         1.01s  9.48% 53.99%      1.02s  9.58%  zstd.copy16 (inline)
         0.95s  8.92% 62.91%      2.51s 23.57%  zstd.execSequence
         0.80s  7.51% 70.42%      0.81s  7.61%  internal/bitstream.(*DStream).Reload
         0.76s  7.14% 77.56%      0.77s  7.23%  internal/bitstream.(*DStream).LookBits (inline)
         0.48s  4.51% 82.07%      1.19s 11.17%  zstd.wildcopy
         0.46s  4.32% 86.38%      7.42s 69.67%  zstd.(*dctx).decompressSequencesBody
         0.30s  2.82% 89.20%      0.30s  2.82%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.20s  1.88% 91.08%      0.22s  2.07%  zstd.buildFSETable
         0.18s  1.69% 92.77%      0.95s  8.92%  internal/bitstream.(*DStream).ReadBits (inline)
         0.11s  1.03% 93.80%      1.05s  9.86%  zstd.updateFseStateWithDInfo (inline)
         0.10s  0.94% 94.74%      0.10s  0.94%  encoding/binary.littleEndian.Uint64 (inline)
         0.10s  0.94% 95.68%      0.10s  0.94%  internal/bitstream.(*DStream).SkipBits (inline)

## L1 silesia.tar 1M (749.0 MB/s)

          flat  flat%   sum%        cum   cum%
         1.69s 15.97% 15.97%      1.76s 16.64%  internal/huf.decompress4X2FastCLoop
         1.60s 15.12% 31.10%      3.70s 34.97%  zstd.decodeSequence
         1.09s 10.30% 41.40%      1.09s 10.30%  zstd.copy16 (inline)
         1.09s 10.30% 51.70%      2.82s 26.65%  zstd.execSequence
         0.99s  9.36% 61.06%         1s  9.45%  internal/huf.decompress4X1FastCLoop
         0.83s  7.84% 68.90%      0.87s  8.22%  internal/bitstream.(*DStream).Reload
         0.71s  6.71% 75.61%      0.71s  6.71%  internal/bitstream.(*DStream).LookBits (inline)
         0.59s  5.58% 81.19%      1.41s 13.33%  zstd.wildcopy
         0.52s  4.91% 86.11%      7.03s 66.45%  zstd.(*dctx).decompressSequencesBody
         0.27s  2.55% 88.66%      0.27s  2.55%  zstd.buildFSETable
         0.26s  2.46% 91.12%      0.26s  2.46%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.13s  1.23% 92.34%      0.84s  7.94%  internal/bitstream.(*DStream).ReadBits (inline)
         0.12s  1.13% 93.48%      0.13s  1.23%  internal/bitstream.(*DStream).SkipBits (inline)
         0.11s  1.04% 94.52%      0.11s  1.04%  encoding/binary.littleEndian.Uint64 (inline)
         0.09s  0.85% 95.37%      0.93s  8.79%  zstd.updateFseStateWithDInfo (inline)

## L1 silesia.tar 16K (540.1 MB/s)

          flat  flat%   sum%        cum   cum%
        1930ms 17.42% 17.42%     4050ms 36.55%  zstd.decodeSequence
        1730ms 15.61% 33.03%     1790ms 16.16%  internal/huf.decompress4X1FastCLoop
         880ms  7.94% 40.97%     2250ms 20.31%  zstd.execSequence
         860ms  7.76% 48.74%      900ms  8.12%  internal/bitstream.(*DStream).Reload
         700ms  6.32% 55.05%      700ms  6.32%  internal/bitstream.(*DStream).LookBits (inline)
         650ms  5.87% 60.92%      660ms  5.96%  zstd.copy16 (inline)
         640ms  5.78% 66.70%      670ms  6.05%  zstd.buildFSETable
         560ms  5.05% 71.75%     1020ms  9.21%  zstd.wildcopy
         480ms  4.33% 76.08%      510ms  4.60%  internal/huf.decompress4X2FastCLoop
         480ms  4.33% 80.42%     6820ms 61.55%  zstd.(*dctx).decompressSequencesBody
         340ms  3.07% 83.48%      740ms  6.68%  internal/huf.ReadDTableX1
         210ms  1.90% 85.38%      210ms  1.90%  internal/bitstream.(*DStream).LookBitsFast (inline)
         180ms  1.62% 87.00%      880ms  7.94%  internal/bitstream.(*DStream).ReadBits (inline)
         160ms  1.44% 88.45%      970ms  8.75%  zstd.updateFseStateWithDInfo (inline)
         140ms  1.26% 89.71%      140ms  1.26%  internal/bitstream.(*DStream).SkipBits (inline)

## L3 silesia.tar whole (654.7 MB/s)

          flat  flat%   sum%        cum   cum%
         2.42s 22.45% 22.45%      5.06s 46.94%  zstd.decodeSequence
         1.39s 12.89% 35.34%      3.35s 31.08%  zstd.execSequence
         1.30s 12.06% 47.40%      1.30s 12.06%  zstd.copy16 (inline)
         0.87s  8.07% 55.47%      0.87s  8.07%  internal/bitstream.(*DStream).LookBits (inline)
         0.87s  8.07% 63.54%      0.88s  8.16%  internal/bitstream.(*DStream).Reload
         0.77s  7.14% 70.69%      0.79s  7.33%  internal/huf.decompress4X1FastCLoop
         0.61s  5.66% 76.35%      1.66s 15.40%  zstd.wildcopy
         0.54s  5.01% 81.35%      8.97s 83.21%  zstd.(*dctx).decompressSequencesBody
         0.40s  3.71% 85.06%      0.41s  3.80%  internal/huf.decompress4X2FastCLoop
         0.31s  2.88% 87.94%      0.31s  2.88%  zstd.buildFSETable
         0.27s  2.50% 90.45%      1.34s 12.43%  zstd.updateFseStateWithDInfo (inline)
         0.25s  2.32% 92.76%      0.25s  2.32%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.22s  2.04% 94.81%      1.10s 10.20%  internal/bitstream.(*DStream).ReadBits (inline)
         0.16s  1.48% 96.29%      0.16s  1.48%  internal/bitstream.(*DStream).SkipBits (inline)
         0.06s  0.56% 96.85%      0.15s  1.39%  internal/huf.ReadDTableX1

## L3 silesia.tar 1M (657.0 MB/s)

          flat  flat%   sum%        cum   cum%
         2.36s 22.12% 22.12%      4.92s 46.11%  zstd.decodeSequence
         1.35s 12.65% 34.77%      1.35s 12.65%  zstd.copy16 (inline)
         1.20s 11.25% 46.02%      3.21s 30.08%  zstd.execSequence
         0.98s  9.18% 55.20%         1s  9.37%  internal/bitstream.(*DStream).Reload
         0.97s  9.09% 64.29%      0.98s  9.18%  internal/huf.decompress4X1FastCLoop
         0.96s  9.00% 73.29%      0.96s  9.00%  internal/bitstream.(*DStream).LookBits (inline)
         0.61s  5.72% 79.01%      1.52s 14.25%  zstd.wildcopy
         0.58s  5.44% 84.44%      8.63s 80.88%  zstd.(*dctx).decompressSequencesBody
         0.40s  3.75% 88.19%      0.43s  4.03%  internal/huf.decompress4X2FastCLoop
         0.23s  2.16% 90.35%      0.23s  2.16%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.19s  1.78% 92.13%      1.15s 10.78%  internal/bitstream.(*DStream).ReadBits (inline)
         0.15s  1.41% 93.53%      1.27s 11.90%  zstd.updateFseStateWithDInfo (inline)
         0.14s  1.31% 94.85%      0.14s  1.31%  zstd.buildFSETable
         0.07s  0.66% 95.50%      0.13s  1.22%  internal/huf.ReadDTableX1
         0.06s  0.56% 96.06%      0.06s  0.56%  internal/bitstream.(*DStream).SkipBits (inline)

## L3 silesia.tar 16K (500.5 MB/s)

          flat  flat%   sum%        cum   cum%
        1740ms 16.34% 16.34%     4110ms 38.59%  zstd.decodeSequence
        1620ms 15.21% 31.55%     1660ms 15.59%  internal/huf.decompress4X1FastCLoop
        1120ms 10.52% 42.07%     2530ms 23.76%  zstd.execSequence
         900ms  8.45% 50.52%      900ms  8.45%  internal/bitstream.(*DStream).Reload
         820ms  7.70% 58.22%      820ms  7.70%  internal/bitstream.(*DStream).LookBits (inline)
         800ms  7.51% 65.73%      800ms  7.51%  zstd.copy16 (inline)
         760ms  7.14% 72.86%      790ms  7.42%  zstd.buildFSETable
         500ms  4.69% 77.56%     7160ms 67.23%  zstd.(*dctx).decompressSequencesBody
         500ms  4.69% 82.25%     1000ms  9.39%  zstd.wildcopy
         250ms  2.35% 84.60%      540ms  5.07%  internal/huf.ReadDTableX1
         230ms  2.16% 86.76%     1160ms 10.89%  zstd.updateFseStateWithDInfo (inline)
         220ms  2.07% 88.83%      220ms  2.07%  internal/bitstream.(*DStream).LookBitsFast (inline)
         180ms  1.69% 90.52%     1010ms  9.48%  internal/bitstream.(*DStream).ReadBits (inline)
         150ms  1.41% 91.92%      150ms  1.41%  internal/bitstream.(*DStream).SkipBits (inline)
         120ms  1.13% 93.05%      170ms  1.60%  internal/fse.ReadNCount

## L7 silesia.tar whole (699.7 MB/s)

          flat  flat%   sum%        cum   cum%
         2.18s 21.48% 21.48%      4.41s 43.45%  zstd.decodeSequence
         1.64s 16.16% 37.64%      1.64s 16.16%  zstd.copy16 (inline)
         1.42s 13.99% 51.63%      3.60s 35.47%  zstd.execSequence
         0.87s  8.57% 60.20%      0.87s  8.57%  internal/bitstream.(*DStream).LookBits (inline)
         0.82s  8.08% 68.28%      0.83s  8.18%  internal/huf.decompress4X1FastCLoop
         0.77s  7.59% 75.86%      0.80s  7.88%  internal/bitstream.(*DStream).Reload
         0.54s  5.32% 81.18%      8.58s 84.53%  zstd.(*dctx).decompressSequencesBody
         0.51s  5.02% 86.21%      1.87s 18.42%  zstd.wildcopy
         0.38s  3.74% 89.95%      0.41s  4.04%  zstd.buildFSETable
         0.24s  2.36% 92.32%      0.24s  2.36%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.17s  1.67% 93.99%      0.17s  1.67%  internal/bitstream.(*DStream).SkipBits (inline)
         0.12s  1.18% 95.17%      0.99s  9.75%  internal/bitstream.(*DStream).ReadBits (inline)
         0.08s  0.79% 95.96%      0.17s  1.67%  internal/huf.ReadDTableX1
         0.06s  0.59% 96.55%      0.06s  0.59%  zstd.getOffsetInfo
         0.06s  0.59% 97.14%         1s  9.85%  zstd.updateFseStateWithDInfo (inline)

## L7 silesia.tar 1M (703.1 MB/s)

          flat  flat%   sum%        cum   cum%
         2.12s 20.91% 20.91%      4.66s 45.96%  zstd.decodeSequence
         1.31s 12.92% 33.83%      1.31s 12.92%  zstd.copy16 (inline)
         1.27s 12.52% 46.35%      3.14s 30.97%  zstd.execSequence
         0.98s  9.66% 56.02%      0.99s  9.76%  internal/huf.decompress4X1FastCLoop
         0.96s  9.47% 65.48%      0.96s  9.47%  internal/bitstream.(*DStream).LookBits (inline)
         0.81s  7.99% 73.47%      0.81s  7.99%  internal/bitstream.(*DStream).Reload
         0.58s  5.72% 79.19%      8.36s 82.45%  zstd.(*dctx).decompressSequencesBody
         0.49s  4.83% 84.02%      1.45s 14.30%  zstd.wildcopy
         0.28s  2.76% 86.79%      0.28s  2.76%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.26s  2.56% 89.35%      0.27s  2.66%  zstd.buildFSETable
         0.21s  2.07% 91.42%      1.17s 11.54%  internal/bitstream.(*DStream).ReadBits (inline)
         0.17s  1.68% 93.10%      0.17s  1.68%  internal/huf.decompress4X2FastCLoop
         0.15s  1.48% 94.58%      0.15s  1.48%  internal/bitstream.(*DStream).SkipBits (inline)
         0.15s  1.48% 96.06%      1.30s 12.82%  zstd.updateFseStateWithDInfo (inline)
         0.06s  0.59% 96.65%      0.16s  1.58%  internal/huf.ReadDTableX1

## L7 silesia.tar 16K (538.9 MB/s)

          flat  flat%   sum%        cum   cum%
        1860ms 16.88% 16.88%     4040ms 36.66%  zstd.decodeSequence
        1770ms 16.06% 32.94%     1810ms 16.42%  internal/huf.decompress4X1FastCLoop
        1130ms 10.25% 43.19%     2590ms 23.50%  zstd.execSequence
         860ms  7.80% 51.00%      870ms  7.89%  zstd.copy16 (inline)
         820ms  7.44% 58.44%      830ms  7.53%  internal/bitstream.(*DStream).Reload
         810ms  7.35% 65.79%      810ms  7.35%  internal/bitstream.(*DStream).LookBits (inline)
         650ms  5.90% 71.69%      720ms  6.53%  zstd.buildFSETable
         510ms  4.63% 76.32%     7180ms 65.15%  zstd.(*dctx).decompressSequencesBody
         500ms  4.54% 80.85%     1050ms  9.53%  zstd.wildcopy
         370ms  3.36% 84.21%      760ms  6.90%  internal/huf.ReadDTableX1
         280ms  2.54% 86.75%      290ms  2.63%  internal/bitstream.(*DStream).LookBitsFast (inline)
         150ms  1.36% 88.11%      960ms  8.71%  internal/bitstream.(*DStream).ReadBits (inline)
         150ms  1.36% 89.47%      220ms  2.00%  internal/fse.ReadNCount
         150ms  1.36% 90.83%      170ms  1.54%  internal/huf.decompress4X2FastCLoop
         150ms  1.36% 92.20%     1030ms  9.35%  zstd.updateFseStateWithDInfo (inline)

## L11 silesia.tar whole (736.4 MB/s)

          flat  flat%   sum%        cum   cum%
         2.31s 21.35% 21.35%      2.31s 21.35%  zstd.copy16 (inline)
         2.03s 18.76% 40.11%      4.15s 38.35%  zstd.decodeSequence
         1.26s 11.65% 51.76%      4.20s 38.82%  zstd.execSequence
         1.01s  9.33% 61.09%      1.03s  9.52%  internal/huf.decompress4X1FastCLoop
         0.86s  7.95% 69.04%      0.86s  7.95%  internal/bitstream.(*DStream).LookBits (inline)
         0.75s  6.93% 75.97%      0.75s  6.93%  internal/bitstream.(*DStream).Reload
         0.69s  6.38% 82.35%      9.07s 83.83%  zstd.(*dctx).decompressSequencesBody
         0.61s  5.64% 87.99%      2.73s 25.23%  zstd.wildcopy
         0.28s  2.59% 90.57%      0.31s  2.87%  zstd.buildFSETable
         0.24s  2.22% 92.79%      0.24s  2.22%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.12s  1.11% 93.90%      0.98s  9.06%  internal/bitstream.(*DStream).ReadBits (inline)
         0.12s  1.11% 95.01%      0.12s  1.11%  internal/bitstream.(*DStream).SkipBits (inline)
         0.10s  0.92% 95.93%      0.19s  1.76%  internal/huf.ReadDTableX1
         0.10s  0.92% 96.86%      0.10s  0.92%  internal/huf.decompress4X2FastCLoop
         0.05s  0.46% 97.32%      1.02s  9.43%  zstd.updateFseStateWithDInfo (inline)

## L11 silesia.tar 1M (741.5 MB/s)

          flat  flat%   sum%        cum   cum%
         2.22s 20.81% 20.81%      4.85s 45.45%  zstd.decodeSequence
         1.42s 13.31% 34.11%      3.21s 30.08%  zstd.execSequence
         1.23s 11.53% 45.64%      1.23s 11.53%  zstd.copy16 (inline)
         1.04s  9.75% 55.39%      1.07s 10.03%  internal/huf.decompress4X1FastCLoop
         0.97s  9.09% 64.48%      0.98s  9.18%  internal/bitstream.(*DStream).Reload
         0.88s  8.25% 72.73%      0.88s  8.25%  internal/bitstream.(*DStream).LookBits (inline)
         0.61s  5.72% 78.44%      8.62s 80.79%  zstd.(*dctx).decompressSequencesBody
         0.51s  4.78% 83.22%      1.51s 14.15%  zstd.wildcopy
         0.30s  2.81% 86.04%      0.31s  2.91%  internal/huf.decompress4X2FastCLoop
         0.25s  2.34% 88.38%      0.25s  2.34%  zstd.buildFSETable
         0.24s  2.25% 90.63%      0.24s  2.25%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.24s  2.25% 92.88%      1.29s 12.09%  zstd.updateFseStateWithDInfo (inline)
         0.17s  1.59% 94.47%      1.05s  9.84%  internal/bitstream.(*DStream).ReadBits (inline)
         0.11s  1.03% 95.50%      0.11s  1.03%  internal/bitstream.(*DStream).SkipBits (inline)
         0.07s  0.66% 96.16%      0.12s  1.12%  internal/huf.ReadDTableX1

## L11 silesia.tar 16K (527.2 MB/s)

          flat  flat%   sum%        cum   cum%
        1970ms 19.49% 19.49%     4170ms 41.25%  zstd.decodeSequence
        1580ms 15.63% 35.11%     1590ms 15.73%  internal/huf.decompress4X1FastCLoop
         930ms  9.20% 44.31%     2120ms 20.97%  zstd.execSequence
         870ms  8.61% 52.92%      880ms  8.70%  internal/bitstream.(*DStream).Reload
         760ms  7.52% 60.44%      760ms  7.52%  zstd.copy16 (inline)
         730ms  7.22% 67.66%      730ms  7.22%  internal/bitstream.(*DStream).LookBits (inline)
         560ms  5.54% 73.19%      610ms  6.03%  zstd.buildFSETable
         450ms  4.45% 77.65%     6790ms 67.16%  zstd.(*dctx).decompressSequencesBody
         360ms  3.56% 81.21%      890ms  8.80%  zstd.wildcopy
         280ms  2.77% 83.98%      280ms  2.77%  internal/bitstream.(*DStream).LookBitsFast (inline)
         230ms  2.27% 86.25%      560ms  5.54%  internal/huf.ReadDTableX1
         190ms  1.88% 88.13%      980ms  9.69%  zstd.updateFseStateWithDInfo (inline)
         160ms  1.58% 89.71%      180ms  1.78%  internal/huf.decompress4X2FastCLoop
         120ms  1.19% 90.90%      150ms  1.48%  internal/fse.ReadNCount
         110ms  1.09% 91.99%      840ms  8.31%  internal/bitstream.(*DStream).ReadBits (inline)

