# Pure-Go CPU profiles (baseline for Task 20)

Top 10 functions by flat time, Go go1.27.1, 2026-10-29, pinned with `taskset -c 2`; no other benchmark or build running (desktop background processes only).
Silesia rows: `go test ./zstd -run xxx -bench 'EncodeAll/.*/^L$' -benchtime 10x -cpuprofile` (levels 1 and 3) or `-benchtime 3x` (7, 11),
every Silesia file whole. silesia.tar rows: `gobench -level 11 -block B -secs 10 -cpuprofile`.
Speeds are in bench/results/2026-10-29-pure-go.md.

## Level 1 (Silesia files, whole)

          flat  flat%   sum%        cum   cum%
        3040ms 30.49% 30.49%     6250ms 62.69%  zstd.compressBlockFastNoDictGeneric
         710ms  7.12% 37.61%     1040ms 10.43%  zstd.compressBlockFastNoDictGeneric.func1 (inline)
         630ms  6.32% 43.93%      650ms  6.52%  zstd.count (inline)
         620ms  6.22% 50.15%     1070ms 10.73%  zstd.compressBlockFastNoDictGeneric.func3 (inline)
         600ms  6.02% 56.17%      600ms  6.02%  internal/bitstream.(*CStream).AddBits (inline)
         470ms  4.71% 60.88%      470ms  4.71%  encoding/binary.littleEndian.Uint32 (inline)
         470ms  4.71% 65.60%      510ms  5.12%  internal/hist.countParallel
         430ms  4.31% 69.91%     1060ms 10.63%  internal/fse.EncodeSymbol
         390ms  3.91% 73.82%     1000ms 10.03%  internal/huf.compress1XLoop
         350ms  3.51% 77.33%      350ms  3.51%  encoding/binary.littleEndian.Uint64 (inline)

## Level 3 (Silesia files, whole)

          flat  flat%   sum%        cum   cum%
        4710ms 28.70% 28.70%    12340ms 75.20%  zstd.compressBlockDoubleFastNoDictGeneric
        1890ms 11.52% 40.22%     1890ms 11.52%  zstd.hashPtr
        1880ms 11.46% 51.68%     1910ms 11.64%  zstd.compressBlockDoubleFastNoDictGeneric.func5 (inline)
         900ms  5.48% 57.16%      900ms  5.48%  internal/bitstream.(*CStream).AddBits (inline)
         770ms  4.69% 61.85%      770ms  4.69%  encoding/binary.littleEndian.Uint32 (inline)
         740ms  4.51% 66.36%      810ms  4.94%  zstd.count (inline)
         730ms  4.45% 70.81%     1480ms  9.02%  zstd.compressBlockDoubleFastNoDictGeneric.func4 (inline)
         510ms  3.11% 73.92%     1350ms  8.23%  internal/fse.EncodeSymbol
         380ms  2.32% 76.23%      550ms  3.35%  zstd.storeSeq (inline)
         360ms  2.19% 78.43%      370ms  2.25%  zstd.compressBlockDoubleFastNoDictGeneric.func3 (inline)

## Level 7 (Silesia files, whole)

          flat  flat%   sum%        cum   cum%
        9070ms 44.11% 44.11%    17480ms 85.02%  zstd.rowFindBestMatch
        2500ms 12.16% 56.27%     5180ms 25.19%  zstd.rowUpdateInternalImpl
        1330ms  6.47% 62.74%     1330ms  6.47%  zstd.rowNextIndex (inline)
        1190ms  5.79% 68.53%     2140ms 10.41%  zstd.rowNextCachedHash
        1050ms  5.11% 73.64%     1150ms  5.59%  zstd.count (inline)
         950ms  4.62% 78.26%      950ms  4.62%  zstd.hashPtrSalted
         800ms  3.89% 82.15%    19150ms 93.14%  zstd.compressBlockLazyGeneric
         550ms  2.68% 84.82%      590ms  2.87%  zstd.rowGetMatchMask
         330ms  1.61% 86.43%      330ms  1.61%  internal/bitstream.(*CStream).AddBits
         290ms  1.41% 87.84%      290ms  1.41%  encoding/binary.littleEndian.Uint32 (inline)

## Level 11 (Silesia files, whole)

          flat  flat%   sum%        cum   cum%
        31.85s 63.92% 63.92%     46.28s 92.88%  zstd.rowFindBestMatch
         3.69s  7.41% 71.32%      6.89s 13.83%  zstd.rowUpdateInternalImpl
         2.59s  5.20% 76.52%      2.75s  5.52%  zstd.count (inline)
         2.52s  5.06% 81.58%      2.53s  5.08%  zstd.rowNextIndex (inline)
         1.94s  3.89% 85.47%      2.09s  4.19%  zstd.rowGetMatchMask
         1.15s  2.31% 87.78%      1.15s  2.31%  zstd.hashPtrSalted
            1s  2.01% 89.79%      2.15s  4.31%  zstd.rowNextCachedHash
         0.82s  1.65% 91.43%     48.25s 96.83%  zstd.compressBlockLazyGeneric
         0.54s  1.08% 92.51%      0.54s  1.08%  encoding/binary.littleEndian.Uint32 (inline)
         0.43s  0.86% 93.38%      0.43s  0.86%  zstd.rowVecMaskNext (inline)

## Level 11, silesia.tar -B128K (btlazy2)

          flat  flat%   sum%        cum   cum%
         3.25s 32.05% 32.05%      8.43s 83.14%  zstd.dubtFindBestMatch
         2.58s 25.44% 57.50%      4.20s 41.42%  zstd.insertDUBT1
         2.14s 21.10% 78.60%      2.33s 22.98%  zstd.count (inline)
         0.40s  3.94% 82.54%      0.53s  5.23%  zstd.updateDUBT
         0.39s  3.85% 86.39%      9.79s 96.55%  zstd.compressBlockLazyGeneric
         0.23s  2.27% 88.66%      0.23s  2.27%  zstd.hashPtr
         0.19s  1.87% 90.53%      0.19s  1.87%  encoding/binary.littleEndian.Uint64 (inline)
         0.16s  1.58% 92.11%      0.16s  1.58%  zstd.highbit32 (inline)
         0.13s  1.28% 93.39%      0.13s  1.28%  zstd.compressBlockLazyGeneric.func1 (inline)
         0.08s  0.79% 94.18%      0.08s  0.79%  internal/bitstream.(*CStream).AddBits

## Level 11, silesia.tar -B16K (btopt)

          flat  flat%   sum%        cum   cum%
        3330ms 25.27% 25.27%     5060ms 38.39%  zstd.insertBt1
        3120ms 23.67% 48.94%     4220ms 32.02%  zstd.insertBtAndGetAllMatches
        1950ms 14.80% 63.73%     2070ms 15.71%  zstd.count (inline)
        1450ms 11.00% 74.73%    12730ms 96.59%  zstd.compressBlockOptGeneric
         260ms  1.97% 76.71%      350ms  2.66%  zstd.readMINMATCH (inline)
         240ms  1.82% 78.53%      240ms  1.82%  internal/hist.CountSimple (inline)
         230ms  1.75% 80.27%      230ms  1.75%  zstd.hashPtr
         220ms  1.67% 81.94%      230ms  1.75%  zstd.highbit32 (inline)
         220ms  1.67% 83.61%      310ms  2.35%  zstd.litLengthPrice
         210ms  1.59% 85.20%     9700ms 73.60%  zstd.btGetAllMatches

Notes: `(inline)` rows are inlined into their caller and also counted in its cum. `.funcN` are closures in the named function (hash, read32 helpers). At levels 7/11 (row-based lazy2 on large inputs) rowFindBestMatch's flat time includes the inlined match loop, rowGetMatchMask's caller-side work and the memory latency of the row loads.
