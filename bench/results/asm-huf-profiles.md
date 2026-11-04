# Task 19 decode CPU profiles: pure-Go vs asm Huffman fast loops

Top 15 functions by flat time, from `gobench -mode d -level L -block B -secs 10 -cpuprofile F bench/corpus/silesia.tar`, pinned with `taskset -c 2`. "Before" is the same tree built with `-tags noasm`; the decoder's only assembly is Task 19's, so that is the Task 18 pure-Go fast loops. Each run is under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0` (GOMEMLIMIT=2GiB), followed by `go tool pprof -top -nodecount 15`. Before and after ran back to back. Go go1.27.1, 2026-11-04 16:17, AC power. load1 was 1.94 at the start and rose to 2.48 during the runs (desktop background: clickhouse-server, terminal, Slack). The package prefix `github.com/vahid-sohrabloo/fastzstd/` is stripped. Each heading gives the gobench speed of that run.

## Observations

- Huffman fast loops' flat share, X1 plus X2:
  - L1 16K: 22.7% to 13.3%.
  - L3 whole: 12.6% to 7.2%.
  
  Per decoded byte (share ÷ speed), the loops are about 1.9x faster in both cases. That accounts for the whole-run gains of 1.13x (L1 16K, 540.4 to 610.5 MB/s) and 1.07x (L3 whole, 656.9 to 701.5 MB/s).
- Sequence decoding now dominates even more. `decompressSequencesBody` is 67% cumulative at L1 16K and 88% at L3 whole. Of that, `decodeSequence` is 18% to 21% flat (41% to 46% cumulative), `execSequence` is 9% to 13% flat, and `copy16` plus `wildcopy` is 12% to 21% flat. These are Task 20's targets.
- At 16K, per-block table building is unchanged: `buildFSETable`, `ReadDTableX1` and `fse.ReadNCount` together are about 14% cumulative.
- Some of the small changes in sequence-side functions between the two runs (for example `LookBits` at 8.6% vs 6.5%) are sampling and inlining attribution noise. The sequence code is identical in both builds.

## L1 silesia.tar 16K, before (noasm, 540.4 MB/s)

          flat  flat%   sum%        cum   cum%
        1980ms 18.00% 18.00%     2010ms 18.27%  internal/huf.decompress4X1FastCLoop
        1730ms 15.73% 33.73%     3990ms 36.27%  zstd.decodeSequence
        1030ms  9.36% 43.09%     1040ms  9.45%  internal/bitstream.(*DStream).Reload
         980ms  8.91% 52.00%     2340ms 21.27%  zstd.execSequence
         740ms  6.73% 58.73%      740ms  6.73%  zstd.copy16 (inline)
         710ms  6.45% 65.18%      710ms  6.45%  internal/bitstream.(*DStream).LookBits (inline)
         520ms  4.73% 69.91%      530ms  4.82%  zstd.buildFSETable
         470ms  4.27% 74.18%      490ms  4.45%  internal/huf.decompress4X2FastCLoop
         440ms  4.00% 78.18%      950ms  8.64%  zstd.wildcopy
         360ms  3.27% 81.45%     6740ms 61.27%  zstd.(*dctx).decompressSequencesBody
         260ms  2.36% 83.82%      630ms  5.73%  internal/huf.ReadDTableX1
         210ms  1.91% 85.73%      210ms  1.91%  internal/bitstream.(*DStream).LookBitsFast (inline)
         180ms  1.64% 87.36%     1000ms  9.09%  zstd.updateFseStateWithDInfo (inline)
         160ms  1.45% 88.82%      870ms  7.91%  internal/bitstream.(*DStream).ReadBits (inline)
         130ms  1.18% 90.00%      140ms  1.27%  internal/fse.ReadNCount

## L1 silesia.tar 16K, after (asm, 610.5 MB/s)

          flat  flat%   sum%        cum   cum%
        1980ms 18.40% 18.40%     4390ms 40.80%  zstd.decodeSequence
        1120ms 10.41% 28.81%     1120ms 10.41%  internal/huf.decompress4X1FastAsmLoop
         960ms  8.92% 37.73%     2360ms 21.93%  zstd.execSequence
         920ms  8.55% 46.28%      920ms  8.55%  internal/bitstream.(*DStream).LookBits (inline)
         830ms  7.71% 54.00%      830ms  7.71%  internal/bitstream.(*DStream).Reload
         770ms  7.16% 61.15%      770ms  7.16%  zstd.copy16 (inline)
         590ms  5.48% 66.64%      640ms  5.95%  zstd.buildFSETable
         480ms  4.46% 71.10%      960ms  8.92%  zstd.wildcopy
         410ms  3.81% 74.91%      790ms  7.34%  internal/huf.ReadDTableX1
         370ms  3.44% 78.35%     7160ms 66.54%  zstd.(*dctx).decompressSequencesBody
         310ms  2.88% 81.23%      310ms  2.88%  internal/huf.decompress4X2FastAsmLoop
         250ms  2.32% 83.55%      320ms  2.97%  internal/fse.ReadNCount
         240ms  2.23% 85.78%      240ms  2.23%  internal/bitstream.(*DStream).LookBitsFast (inline)
         230ms  2.14% 87.92%     1150ms 10.69%  internal/bitstream.(*DStream).ReadBits (inline)
         190ms  1.77% 89.68%     1260ms 11.71%  zstd.updateFseStateWithDInfo (inline)

## L3 silesia.tar whole, before (noasm, 656.9 MB/s)

          flat  flat%   sum%        cum   cum%
         1.96s 18.35% 18.35%      4.50s 42.13%  zstd.decodeSequence
         1.63s 15.26% 33.61%      1.63s 15.26%  zstd.copy16 (inline)
         1.18s 11.05% 44.66%      3.56s 33.33%  zstd.execSequence
            1s  9.36% 54.03%         1s  9.36%  internal/huf.decompress4X1FastCLoop
         0.88s  8.24% 62.27%      0.88s  8.24%  internal/bitstream.(*DStream).LookBits (inline)
         0.83s  7.77% 70.04%      0.84s  7.87%  internal/bitstream.(*DStream).Reload
         0.74s  6.93% 76.97%      2.05s 19.19%  zstd.wildcopy
         0.70s  6.55% 83.52%      8.76s 82.02%  zstd.(*dctx).decompressSequencesBody
         0.35s  3.28% 86.80%      0.35s  3.28%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.32s  3.00% 89.79%      0.35s  3.28%  internal/huf.decompress4X2FastCLoop
         0.21s  1.97% 91.76%      0.21s  1.97%  internal/bitstream.(*DStream).SkipBits (inline)
         0.20s  1.87% 93.63%      0.23s  2.15%  zstd.buildFSETable
         0.17s  1.59% 95.22%      1.05s  9.83%  internal/bitstream.(*DStream).ReadBits (inline)
         0.13s  1.22% 96.44%      1.15s 10.77%  zstd.updateFseStateWithDInfo (inline)
         0.06s  0.56% 97.00%      0.06s  0.56%  internal/fse.ReadNCount

## L3 silesia.tar whole, after (asm, 701.5 MB/s)

          flat  flat%   sum%        cum   cum%
         2.36s 21.07% 21.07%      5.20s 46.43%  zstd.decodeSequence
         1.69s 15.09% 36.16%      1.69s 15.09%  zstd.copy16 (inline)
         1.44s 12.86% 49.02%      3.89s 34.73%  zstd.execSequence
         1.15s 10.27% 59.29%      1.15s 10.27%  internal/bitstream.(*DStream).LookBits (inline)
         1.01s  9.02% 68.30%      1.03s  9.20%  internal/bitstream.(*DStream).Reload
         0.72s  6.43% 74.73%      9.81s 87.59%  zstd.(*dctx).decompressSequencesBody
         0.67s  5.98% 80.71%      2.09s 18.66%  zstd.wildcopy
         0.62s  5.54% 86.25%      0.62s  5.54%  internal/huf.decompress4X1FastAsmLoop
         0.35s  3.12% 89.38%      0.37s  3.30%  zstd.buildFSETable
         0.24s  2.14% 91.52%      0.24s  2.14%  internal/bitstream.(*DStream).LookBitsFast (inline)
         0.18s  1.61% 93.12%      0.18s  1.61%  internal/huf.decompress4X2FastAsmLoop
         0.17s  1.52% 94.64%      1.32s 11.79%  internal/bitstream.(*DStream).ReadBits (inline)
         0.14s  1.25% 95.89%      0.14s  1.25%  internal/bitstream.(*DStream).SkipBits (inline)
         0.12s  1.07% 96.96%      1.43s 12.77%  zstd.updateFseStateWithDInfo (inline)
         0.09s   0.8% 97.77%      0.09s   0.8%  zstd.overlapCopy8
