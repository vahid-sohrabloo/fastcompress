# Profiles of the assembly build (Task 20)

Method: `gobench -level L -block B -secs 5 -cpuprofile`, `taskset -c 2`,
CPU 3 idle, on `silesia.tar -B4M`, `dickens`, `x-ray` and `xml` (whole);
the four profiles of a level are merged (`go tool pprof -top gobench
L*.prof`). Code at commit 5fbcc0e (row, fast, double-fast, sequences, HUF,
hist and DUBT assembly). Level 11 on these inputs is row-based lazy2
(inputs > 256 KiB), plus btlazy2/btopt for the -B128K/-B16K rows profiled
separately below.

Hardware counters: `perf` 6.8 (`/usr/lib/linux-tools/6.8.0-147-generic/perf`,
cpu_core events) on C `zstd -b<L> -i4 -T1` and on gobench, same input,
`taskset -c 2`; see "perf: C vs Go" at the end. (An earlier draft used a
gprof build of C because perf was not yet installed; that section is kept.)

Speeds of these runs (MB/s, % of the C baseline in 2026-10-29-pure-go.md):

| input | L1 | L3 | L7 | L11 |
|---|---|---|---|---|
| silesia.tar -B4M | 409.4 (81%) | 245.0 (81%) | 75.8 (74%) | 31.0 (66%) |
| dickens | 279.3 (80%) | 176.5 (87%) | 51.1 (75%) | 16.6 (65%) |
| x-ray | 672.5 (82%) | 151.0 (88%) | 60.0 (77%) | 31.2 (98%) |
| xml | 637.0 (83%) | 484.9 (82%) | 129.1 (76%) | 59.5 (69%) |
| silesia.tar -B128K (btlazy2) | | | | 23.4 (85%) |
| silesia.tar -B16K (btopt) | | | | 16.3 (71%) |

## Top 15, flat and cum, per level

### Level 1

Flat:

          flat  flat%   sum%        cum   cum%
         9.31s 43.87% 43.87%     11.54s 54.38%  zstd.compressBlockFastAsm
         3.14s 14.80% 58.67%      3.14s 14.80%  internal/huf.compress1XAsm
         2.32s 10.93% 69.60%      2.32s 10.93%  zstd.encodeSequencesAsm
         2.01s  9.47% 79.08%      2.01s  9.47%  internal/hist.countStripesAsm
         1.85s  8.72% 87.79%      1.85s  8.72%  zstd.fastCount
         0.50s  2.36% 90.15%      0.50s  2.36%  internal/fse.BuildCTable
         0.38s  1.79% 91.94%      0.38s  1.79%  zstd.fastCopyLits
         0.25s  1.18% 93.12%      0.25s  1.18%  internal/hist.CountSimple (inline)
         0.17s   0.8% 93.92%      0.35s  1.65%  zstd.seqToCodes
         0.12s  0.57% 94.49%      2.14s 10.08%  internal/hist.countParallel
         0.12s  0.57% 95.05%      0.22s  1.04%  internal/huf.sortSymbols
         0.11s  0.52% 95.57%      0.15s  0.71%  internal/huf.buildTree
         0.10s  0.47% 96.04%      0.14s  0.66%  internal/huf.buildCTableFromTree
         0.02s 0.094% 96.14%      5.78s 27.24%  internal/huf.compressInternal
         0.01s 0.047% 96.18%      0.53s  2.50%  internal/huf.buildCTable

Cum:

          flat  flat%   sum%        cum   cum%
             0     0%     0%     21.05s 99.20%  zstd.(*cctx).compressBlockInternal
             0     0%     0%     11.57s 54.52%  zstd.(*cctx).buildSeqStore
             0     0%     0%     11.55s 54.43%  zstd.compressBlockFast
             0     0%     0%     11.55s 54.43%  zstd.compressBlockFastNoDict
         9.31s 43.87% 43.87%     11.54s 54.38%  zstd.compressBlockFastAsm
             0     0% 43.87%      9.48s 44.67%  zstd.entropyCompressSeqStore (inline)
             0     0% 43.87%      9.48s 44.67%  zstd.entropyCompressSeqStoreInternal
             0     0% 43.87%      9.48s 44.67%  zstd.entropyCompressSeqStoreWExtLitBuffer
             0     0% 43.87%      5.78s 27.24%  internal/huf.Compress4XRepeat
         0.02s 0.094% 43.97%      5.78s 27.24%  internal/huf.compressInternal
             0     0% 43.97%      5.78s 27.24%  zstd.compressLiterals
             0     0% 43.97%      3.14s 14.80%  internal/huf.Compress1XUsingCTable (inline)
             0     0% 43.97%      3.14s 14.80%  internal/huf.Compress4XUsingCTable
         3.14s 14.80% 58.77%      3.14s 14.80%  internal/huf.compress1XAsm
             0     0% 58.77%      3.14s 14.80%  internal/huf.compress1XBody

### Level 3

Flat:

          flat  flat%   sum%        cum   cum%
        14.78s 68.52% 68.52%     16.55s 76.73%  zstd.compressBlockDoubleFastAsm
         2.35s 10.89% 79.42%      2.35s 10.89%  zstd.encodeSequencesAsm
         1.48s  6.86% 86.28%      1.48s  6.86%  zstd.fastCount
         0.50s  2.32% 88.60%      0.50s  2.32%  internal/hist.countStripesAsm
         0.49s  2.27% 90.87%      0.49s  2.27%  internal/huf.compress1XAsm
         0.30s  1.39% 92.26%      0.30s  1.39%  internal/fse.BuildCTable
         0.29s  1.34% 93.60%      0.29s  1.34%  zstd.fastCopyLits
         0.25s  1.16% 94.76%      0.35s  1.62%  zstd.seqToCodes
         0.21s  0.97% 95.73%      0.21s  0.97%  zstd.addEventsGeneric (inline)
         0.11s  0.51% 96.24%      0.61s  2.83%  internal/hist.countParallel
         0.10s  0.46% 96.71%      0.11s  0.51%  zstd.mergeEvents (inline)
         0.07s  0.32% 97.03%      0.11s  0.51%  zstd.fpDistance (inline)
         0.02s 0.093% 97.13%      0.25s  1.16%  internal/huf.buildCTable
         0.02s 0.093% 97.22%      0.13s   0.6%  internal/huf.writeCTable
         0.02s 0.093% 97.31%      0.46s  2.13%  zstd.splitBlockByChunks

Cum:

          flat  flat%   sum%        cum   cum%
             0     0%     0%     21.11s 97.87%  zstd.(*cctx).compressBlockInternal
             0     0%     0%     16.56s 76.77%  zstd.(*cctx).buildSeqStore
             0     0%     0%     16.56s 76.77%  zstd.compressBlockDoubleFast
         0.01s 0.046% 0.046%     16.56s 76.77%  zstd.compressBlockDoubleFastNoDict
        14.78s 68.52% 68.57%     16.55s 76.73%  zstd.compressBlockDoubleFastAsm
             0     0% 68.57%      4.55s 21.09%  zstd.entropyCompressSeqStore (inline)
         0.01s 0.046% 68.61%      4.55s 21.09%  zstd.entropyCompressSeqStoreInternal
             0     0% 68.61%      4.55s 21.09%  zstd.entropyCompressSeqStoreWExtLitBuffer
             0     0% 68.61%      2.35s 10.89%  zstd.encodeSequences
         2.35s 10.89% 79.51%      2.35s 10.89%  zstd.encodeSequencesAsm
         1.48s  6.86% 86.37%      1.48s  6.86%  zstd.fastCount
             0     0% 86.37%      1.13s  5.24%  zstd.buildSequencesStatistics
         0.01s 0.046% 86.42%      1.06s  4.91%  zstd.compressLiterals
             0     0% 86.42%      1.05s  4.87%  internal/huf.Compress4XRepeat
             0     0% 86.42%      1.05s  4.87%  internal/huf.compressInternal

### Level 7

Flat:

          flat  flat%   sum%        cum   cum%
        12.64s 55.49% 55.49%     17.04s 74.80%  zstd.rowFindBestMatchAsm
         4.40s 19.32% 74.80%      4.40s 19.32%  zstd.rowUpdateAsmHelper
         1.46s  6.41% 81.21%     20.85s 91.53%  zstd.compressBlockLazyGeneric
         0.98s  4.30% 85.51%      0.98s  4.30%  zstd.encodeSequencesAsm
         0.54s  2.37% 87.88%      0.69s  3.03%  zstd.compressBlockLazyGeneric.func1 (inline)
         0.50s  2.19% 90.08%      0.71s  3.12%  zstd.storeSeq (inline)
         0.48s  2.11% 92.19%     17.52s 76.91%  zstd.rowFindBestMatchAsmCall
         0.22s  0.97% 93.15%     17.74s 77.88%  zstd.searchMax
         0.22s  0.97% 94.12%      0.22s  0.97%  runtime.memmove
         0.17s  0.75% 94.86%      0.17s  0.75%  internal/hist.countStripesAsm
         0.16s   0.7% 95.57%      0.16s   0.7%  zstd.highbit32 (inline)
         0.12s  0.53% 96.71%      0.17s  0.75%  zstd.addEventsGeneric (inline)
         0.08s  0.35% 97.06%      0.12s  0.53%  zstd.fpDistance (inline)
         0.02s 0.088% 97.15%      0.20s  0.88%  internal/hist.countParallel
             0     0% 97.15%      0.21s  0.92%  internal/hist.CountFast

Cum:

          flat  flat%   sum%        cum   cum%
             0     0%     0%     22.44s 98.51%  zstd.(*cctx).compressBlockInternal
             0     0%     0%     20.85s 91.53%  zstd.(*cctx).buildSeqStore
         1.46s  6.41%  6.41%     20.85s 91.53%  zstd.compressBlockLazyGeneric
             0     0%  6.41%     20.85s 91.53%  zstd.compressBlockLazyRow
         0.22s  0.97%  7.37%     17.74s 77.88%  zstd.searchMax
         0.48s  2.11%  9.48%     17.52s 76.91%  zstd.rowFindBestMatchAsmCall
        12.64s 55.49% 64.97%     17.04s 74.80%  zstd.rowFindBestMatchAsm
         4.40s 19.32% 84.28%      4.40s 19.32%  zstd.rowUpdateAsmHelper
             0     0% 84.28%      1.59s  6.98%  zstd.entropyCompressSeqStore (inline)
             0     0% 84.28%      1.59s  6.98%  zstd.entropyCompressSeqStoreInternal
             0     0% 84.28%      1.59s  6.98%  zstd.entropyCompressSeqStoreWExtLitBuffer
             0     0% 84.28%      0.98s  4.30%  zstd.encodeSequences
         0.98s  4.30% 88.59%      0.98s  4.30%  zstd.encodeSequencesAsm
         0.50s  2.19% 90.78%      0.71s  3.12%  zstd.storeSeq (inline)
         0.54s  2.37% 93.15%      0.69s  3.03%  zstd.compressBlockLazyGeneric.func1 (inline)

### Level 11

Flat:

          flat  flat%   sum%        cum   cum%
        17.57s 38.50% 38.50%     20.42s 44.74%  zstd.rowFindBestMatchAsm
         3.26s  7.14% 45.64%      5.35s 11.72%  zstd.insertBt1
         3.09s  6.77% 52.41%      3.09s  6.77%  zstd.insertDUBT1Asm
         3.05s  6.68% 59.09%      4.23s  9.27%  zstd.insertBtAndGetAllMatches
         2.85s  6.24% 65.34%      2.85s  6.24%  zstd.rowUpdateAsmHelper
         2.41s  5.28% 70.62%      2.54s  5.57%  zstd.count (inline)
         1.99s  4.36% 74.98%      1.99s  4.36%  zstd.dubtSearchAsm
         1.63s  3.57% 78.55%      7.40s 16.21%  zstd.dubtFindBestMatch
         1.28s  2.80% 81.35%     12.66s 27.74%  zstd.compressBlockOptGeneric
         0.98s  2.15% 83.50%     31.33s 68.65%  zstd.compressBlockLazyGeneric
         0.75s  1.64% 85.14%      0.75s  1.64%  zstd.encodeSequencesAsm
         0.66s  1.45% 86.59%      0.66s  1.45%  zstd.hashPtr
         0.57s  1.25% 87.84%      0.73s  1.60%  zstd.updateDUBT
         0.37s  0.81% 88.65%      0.50s  1.10%  zstd.compressBlockLazyGeneric.func1 (inline)
         0.32s   0.7% 89.35%      0.44s  0.96%  zstd.litLengthPrice

Cum:

          flat  flat%   sum%        cum   cum%
             0     0%     0%     45.34s 99.34%  zstd.(*cctx).compressBlockInternal
             0     0%     0%     43.99s 96.38%  zstd.(*cctx).buildSeqStore
         0.98s  2.15%  2.15%     31.33s 68.65%  zstd.compressBlockLazyGeneric
         0.27s  0.59%  2.74%     29.21s 64.00%  zstd.searchMax
             0     0%  2.74%     22.44s 49.17%  zstd.compressBlockLazy2Row
         0.30s  0.66%  3.40%     20.72s 45.40%  zstd.rowFindBestMatchAsmCall
        17.57s 38.50% 41.89%     20.42s 44.74%  zstd.rowFindBestMatchAsm
             0     0% 41.89%     12.66s 27.74%  zstd.compressBlockBtopt
         1.28s  2.80% 44.70%     12.66s 27.74%  zstd.compressBlockOptGeneric
         0.13s  0.28% 44.98%      9.91s 21.71%  zstd.btGetAllMatches
             0     0% 44.98%      8.89s 19.48%  zstd.compressBlockBtlazy2
         0.09s   0.2% 45.18%      8.22s 18.01%  zstd.btFindBestMatch
         1.63s  3.57% 48.75%      7.40s 16.21%  zstd.dubtFindBestMatch
         0.20s  0.44% 49.19%      5.55s 12.16%  zstd.updateTreeInternal
         3.26s  7.14% 56.33%      5.35s 11.72%  zstd.insertBt1

### Level 11, silesia.tar -B16K (btopt)

          flat  flat%   sum%        cum   cum%
        3260ms 25.06% 25.06%     5350ms 41.12%  zstd.insertBt1
        3050ms 23.44% 48.50%     4230ms 32.51%  zstd.insertBtAndGetAllMatches
        2260ms 17.37% 65.87%     2380ms 18.29%  zstd.count (inline)
        1280ms  9.84% 75.71%    12660ms 97.31%  zstd.compressBlockOptGeneric
         340ms  2.61% 78.32%      340ms  2.61%  zstd.hashPtr
         320ms  2.46% 80.78%      440ms  3.38%  zstd.litLengthPrice
         300ms  2.31% 83.09%      340ms  2.61%  zstd.readMINMATCH (inline)
         200ms  1.54% 84.63%     5550ms 42.66%  zstd.updateTreeInternal
         180ms  1.38% 86.01%      180ms  1.38%  zstd.updateRep
         160ms  1.23% 87.24%      160ms  1.23%  internal/hist.CountSimple (inline)
         160ms  1.23% 88.47%      160ms  1.23%  zstd.getLowestMatchIndex (inline)
         140ms  1.08% 89.55%      290ms  2.23%  zstd.getMatchPrice

### Level 11, silesia.tar -B128K (btlazy2)

          flat  flat%   sum%        cum   cum%
        3090ms 34.11% 34.11%     3090ms 34.11%  zstd.insertDUBT1Asm
        1990ms 21.96% 56.07%     1990ms 21.96%  zstd.dubtSearchAsm
        1630ms 17.99% 74.06%     7400ms 81.68%  zstd.dubtFindBestMatch
         570ms  6.29% 80.35%      730ms  8.06%  zstd.updateDUBT
         320ms  3.53% 83.89%      320ms  3.53%  zstd.hashPtr
         260ms  2.87% 86.75%     8890ms 98.12%  zstd.compressBlockLazyGeneric
         200ms  2.21% 88.96%      200ms  2.21%  zstd.dubtAsmOK (inline)
         130ms  1.43% 90.40%     2220ms 24.50%  zstd.dubtSearch
         120ms  1.32% 91.72%     3310ms 36.53%  zstd.insertDUBT1
         100ms  1.10% 92.83%      140ms  1.55%  zstd.compressBlockLazyGeneric.func1 (inline)

## What the top functions spend their time on (`pprof -list` / `-disasm`)

None of the top functions has bounds checks, write barriers, allocations or
memmove left in its hot path: they are assembly, and the Go around them
(cctx, entropy table construction) is below 3% per level.

- **L1 `compressBlockFastAsm` (44% flat).** No single hot instruction: the
  samples spread over the loop, the largest being the 4-byte compares right
  after loading a candidate (`CMPL DX, (SI)(R8*1)`, 0.62 s of 9.3 s) and the
  hash-table loads (`MOVL (DI)(R12*4), R13`): table and candidate cache
  misses, which C has as well. The hash parameters are read from the
  argument frame (`IMULQ prime(FP)`), a few L1 loads per position.
- **L1 `compress1XAsm` (15%; 39% on x-ray, whose literals are most of the
  input).** Serial dependency through the container: every code does
  `SHRQ CL, R8; ORQ AX, R8`. Fixed in 8d8eedb (below).
- **L1/L3 `encodeSequencesAsm` (11%).** Flat: three FSE_encodeSymbol and
  the bit adds per sequence, instruction-bound (~60 instructions/sequence
  including table-pointer reloads from the frame).
- **L1 `countStripesAsm` (9.5%; mostly x-ray).** `INCL (DI)(CX*4)`
  read-modify-write chains on equal bytes; the same 4-table scheme as C.
- **L3 `compressBlockDoubleFastAsm` (69%).** Hot spots are the long-match
  8-byte compare after loading the candidate (`CMPQ AX, (SI)(R8*1)`,
  0.81 s of 3.1 s on the tar) and the short compare: candidate cache misses.
  Spills: idxs0/idxl1/hl1 live in the frame (`MOVL DX, 0x10(SP)`, 0.31 s).
- **L7 `rowFindBestMatchAsm` (55%) / `rowUpdateAsmHelper` (19%).** The
  hottest instructions are the store of a candidate right after its
  PREFETCHT0 (1.22 s of 7.8 s; the prefetch waits for a fill buffer) and the
  4-byte compare of a candidate (1.21 s): cache misses on candidate data,
  which C prefetches identically (PREFETCH_L1 in the same loop). The helper
  is spread over the row update (tag RMW, prefetches).
- **L7 `compressBlockLazyGeneric` (6.4% flat).** The Go lazy loop around the
  searches: repcode checks (`read32` closure, inlined) and gain arithmetic;
  bounds checks on `b[...]` loads remain here.
- **L11 (row) as L7**, plus btopt on <=16 KiB blocks: `insertBt1` (25%) and
  `insertBtAndGetAllMatches` (24%) with `count` inlined (17%) are still Go;
  they would be the next assembly candidates for the btopt row.

## Before / after per optimisation

Function shares are flat % of the level's merged profile unless noted.

| commit | target (from profile) | before | after |
|---|---|---|---|
| f2db896 perf(row) | L7 `rowFindBestMatch` 44% + `rowUpdateInternalImpl` 12% (pure Go, spills, `hashPtrSalted` switch) | dickens L7 28.0, xml L7 72.7 MB/s | `rowSearchAsm` 46%, `rowUpdateCachedAsm` 14%; dickens L7 41.1, xml 118.8 |
| c6a236e perf(fast) | L1 `compressBlockFastNoDictGeneric` 63% cum (bounds checks: 50 in the loop; spills) | dickens L1 159 | `compressBlockFastAsm` 49% cum; dickens 231 |
| 769a3c8 perf(dfast) | L3 `compressBlockDoubleFastNoDictGeneric` 75% cum | dickens L3 100 | `compressBlockDoubleFastAsm` 61% cum; dickens 146 |
| 0f31ad5 perf(sequences) | L1 `encodeSequences` 23% cum (`EncodeSymbol`, `AddBits` via pointers) | L1 dickens 231 | `encodeSequencesAsm` 12%; with HUF: 270 |
| 635e742 perf(huf) | L1 `compress1XLoop` 10% cum (closures, runtime unroll args) | | `compress1XAsm` 6.5% |
| 8ded374 perf(hist) | L1 `countParallel` 7% | x-ray L1 646 | 665 |
| 27da9cc perf(row) | L7 Go wrappers ~20% (`rowFindBestMatch` 5%, `rowSearch` 3%, `rowUpdateCached` 3%, `hashPtrSalted` 3%, ...) and no row prefetch for searched positions | `rowSearchAsm` 47%, `rowUpdateCachedAsm` 16% ; x-ray L11 17.6 | `rowFindBestMatchAsm` 59%, helper 18%, wrappers 2%; x-ray L11 31.3 |
| 5fbcc0e perf(dubt) | L11 -B128K `dubtFindBestMatch` 34%, `insertDUBT1` 26%, `count` 21% | 20.0 MB/s | `insertDUBT1Asm` 34%, `dubtSearchAsm` 25%; 23.0 MB/s |
| 8d8eedb perf(huf) | x-ray L1 `compress1XAsm` 39% (serial container chain) | 672 MB/s, 39% | `compress1XAsmBMI2` 37.6%; 734 MB/s (dickens L1 share 6.8% -> 4.9%) |
| 376f9c1 perf(sequences) BMI2 | `encodeSequencesAsm` 14% of dickens L1 (C's ZSTD_encodeSequences_bmi2 is 12% of a faster run: ~50% more time per byte in Go); CL-shift juggling in every BIT_addBits/FSE_encodeSymbol | dickens L1 282.9 (13.7%), L3 174.7 (14.5%); xml L1 634.2 (13.9%), L3 477.5 (12.1%) | dickens L1 296.9 (9.4%), L3 188.2 (9.3%); xml L1 674.8 (11.1%), L3 495.8 (6.7%) |

## C side (gprof, as perf is unavailable)

C zstd v1.5.7 built with `gcc -O3 -pg` into the scratch directory (the
reference binary is untouched; -pg costs it under 2% at these levels),
`zstd-pg -b<L> -i3 -T1 dickens` pinned to CPU 2. Shares are of the
compression functions only (the benchmark also decompresses), converted to
milliseconds per MB with the real C speed, next to Go's at commit 8d8eedb:

| level | function | C ms/MB | Go ms/MB | Go / C |
|---|---|---|---|---|
| 1 | match finder (ZSTD_compressBlock_fast vs compressBlockFastAsm + fastCount + fastCopyLits) | 2.15 | 2.48 | 1.15 |
| 1 | encodeSequences | 0.34 | 0.51 | 1.50 (-> 0.32 after the BMI2 loop) |
| 1 | HUF_compress1X | 0.16 | 0.26 | 1.6 (BMI2 writer since: ~0.18) |
| 1 | HIST_count_parallel | 0.16 | 0.18 | 1.1 |
| 3 | match finder (doubleFast) | 3.94 | 4.59 | 1.16 |
| 3 | encodeSequences | 0.73 | 0.68 | 0.93 |
| 7 | ZSTD_RowFindBestMatch (+ row update) | 12.2 | 15.7 | 1.29 |
| 7 | lazy loop (ZSTD_compressBlock_lazy_row vs compressBlockLazyGeneric + storeSeq + glue) | 1.42 | 2.0 | 1.4 |
| 11 | ZSTD_RowFindBestMatch (+ row update) | 37.6 | 54.2 | 1.44 |

The remaining gap at levels 7 and 11 is inside the row search itself. Its
hottest instructions are the stores right after the candidate PREFETCHT0s
and the first compare of each candidate (cache misses on candidate data);
the instruction sequence mirrors C's (same prefetches, SSE2 tag match, same
candidate order), so the difference is not visible at function granularity
and `perf stat` counters (not installed) would be needed to see whether it
is TLB/fill-buffer pressure or code layout.

## perf: C vs Go on the row search (dickens, levels 7 and 11)

`perf stat` shows both processes at the same clock (3.79 GHz on CPU 2), so
the gap is cycles per byte, not frequency. `perf record` with cycles,
instructions, cache-misses (LLC), dTLB-load-misses and L1-dcache-load-misses
on `zstd -b<L> -i4 -T1` and `gobench -secs 4`; counts per input byte, the
C ones over its compression functions only (the -b run also decompresses;
bytes = speed x compression cycles / clock). Go at commit 4dbadc3.

| L | function | cycles/B | instr/B | IPC | LLC miss/B | dTLB miss/B | L1D miss/B |
|---|---|---|---|---|---|---|---|
| 7 | C ZSTD_RowFindBestMatch_noDict_5_4 (update inlined) | 46.5 | 144.6 | 3.11 | 0.018 | 0.040 | 0.232 |
| 7 | Go rowFindBestMatchAsm + rowUpdateAsmHelper | 62.1 | 177.6 | 2.86 | 0.017 | 0.029 | 0.233 |
| 7 | C ZSTD_compressBlock_lazy_row | 5.1 | 14.2 | | | | |
| 7 | Go compressBlockLazyGeneric (+ AsmCall, searchMax) | 6.5+ | 26.2+ | | | | |
| 7 | all compression | C 56.7 | C 178 | | C 0.04 | C 0.04 | C 0.29 |
| 7 | all compression | Go 75.1 | Go 232 | | Go 0.04 | Go 0.03 | Go 0.28 |
| 11 | C ZSTD_RowFindBestMatch_noDict_5_6 | 137.9 | 430.9 | 3.13 | 0.93 | 1.42 | 2.78 |
| 11 | Go rowFindBestMatchAsm + rowUpdateAsmHelper | 218.5 | 522.5 | 2.39 | 1.61 | 1.46 | 2.67 |
| 11 | all compression | C 152 | C 475 | | C 1.00 | C 1.43 | C 2.85 |
| 11 | all compression | Go 236 | Go 585 | | Go 1.71 | Go 1.47 | Go 2.78 |

What this says:

- **Level 7 is instruction-bound, not memory-bound.** Cache, TLB and L1
  misses per byte are the same as C's; Go executes ~30% more instructions
  per byte at a similar IPC. The excess is in the row update helper (41
  instructions/byte: it reloads ten parameters from rowAsmParams on every
  call and reads rowLog/rowMask/table bases from memory inside its loop,
  where C has them as compile-time constants of the `_5_4` specialisation)
  and in the Go lazy loop (26 vs 14 instructions/byte: bounds checks on the
  repcode `read32`s, the searchMax/rowFindBestMatchAsmCall dispatch).
- **Level 11 has 1.7x C's LLC misses per byte** on top of ~20% more
  instructions; TLB and L1 misses match C's. Instruction-level annotation
  (`perf annotate`, cycles and cache-misses) gives the same shape on both
  sides: the store into the candidate buffer just after the candidate
  PREFETCHT0 is the hottest instruction (C 27.6%, Go 42% of LLC-miss
  samples), i.e. the misses are taken on row entries / candidate data in
  the same loop, Go just takes more of them.
- **Huge pages and alignment do not explain it:** transparent huge pages
  are `madvise` on this machine and neither process gets any
  (`AnonHugePages: 0 kB` in both /proc/PID/smaps_rollup); Go's tables are
  large heap allocations and so page aligned (rows are 64-byte aligned on
  both sides). Neither side clears its tables between passes
  (memset/memclr < 0.1%).
- Experiments: prefetching all four lines of a 64-entry hash row (instead
  of C's two) made L11 1% and L7 5% slower; dropping the candidate
  PREFETCHT0 made L11 3-6% slower. Both reverted.

Targets that follow (not done in this task; recorded for Task 22):
specialise the row search/update per (mls, rowLog) like C's
`ZSTD_RowFindBestMatch_noDict_<mls>_<rowLog>` so table bases, rowLog and
hash constants are immediates or stay in registers; move the lazy parser
loop (compressBlockLazyGeneric) into the same assembly to remove the
per-search call and parameter reloads; then re-measure the L11 LLC excess,
which needs a finer split (e.g. `mem_load_retired.l3_miss` per load
address) to attribute.
