# gzip deflate-decode round 2 (decoder last ~2%)

Before/after decode benchmark of the round's decoder changes against libdeflate v1.26 (BMI2 build, as shipped).

- Changes measured (internal/deflate): decode_table.go (buildDecodeTable: the count and sort loops range over
  the lengths, the sort loop masks the length so the offsets[] index is checked at compile time; table build
  -15% on the 16K blocks' tables), decompress_fastloop_amd64.s (PCALIGN $32 on the fast loop head, $16 on the
  match copy loops; no instruction changed). The round-5 review test/comment items (Part A) do not touch the
  decoder.
- before = a copy of the repository with internal/deflate as it was at the start of the round
  (git shows "unknown" for it: the copy has no .git); after = the worktree (59c35a4, dirty+untracked).
- Both runs: `bench/gzip/compare.sh -mode d -l "1 6 9 12" -i 3 -g 3 -n 3 -libs "ld ours"`, taskset -c 2,
  under the test flock and a 4G scope, started only after AC0=1, load1 < 2, no test/bench process, lock free.
  Run one after the other (before 02:25:08-02:57:58, after 02:58:28-03:31:14, 2026-11-09). C and Go are
  interleaved per round inside each run, so compare the % columns; the C columns of the two runs agree within
  ~0.5%.
- Load: the start check is the only gate; per-row load1 stayed below 3 except before: mr L12 (5.67) and nci
  L1-L12 (4.75-3.08, the before nci L1 95.8% row is suspect), after: samba L1-L12 (3.66-3.02) and webster L12
  (3.00). The weak rows (silesia.tar 16K/128K, ooffice, mozilla) ran at load1 < 3 in both runs. An earlier
  attempt (02:08) was aborted and discarded when an unrelated disk scan raised load1 to 7-9.
- Integrity: gzbench verifies the round trip (exact-length decode of every block, byte equality) before
  timing; all 60 rows of both runs passed it (rc 0, no missing rows).

## Gate (geomean >= 100%, every row >= 95%, hard floor 90%)

| level | before geomean | before min row | after geomean | after min row | after gate |
|---|---|---|---|---|---|
| 1 | 98.8% | 95.8% nci (loaded row) | 100.4% | 97.7% ooffice | PASS |
| 6 | 98.7% | 96.0% silesia.tar/16K | 100.5% | 97.3% mozilla | PASS |
| 9 | 98.8% | 95.9% silesia.tar/16K | 100.3% | 97.5% ooffice | PASS |
| 12 | 98.5% | 95.7% silesia.tar/16K | 100.4% | 96.9% mozilla | PASS |

Weak rows, after vs before (% of C): silesia.tar/16K 96.9/96.0/95.9/95.7 -> 98.1/97.8/97.6/97.4 (L1/6/9/12),
silesia.tar/128K 98.7/97.9/98.1/97.6 -> 100.3/99.7/99.4/99.2, ooffice 96.4/96.1/96.4/95.8 -> 97.7/97.4/97.5/97.1,
mozilla 97.8/96.6/96.7/96.0 -> 98.5/97.3/97.7/96.9. Go MB/s did not drop on any row; the only
row whose % of C fell is mr L1 (99.6% -> 99.4%, Go 890.1 -> 890.0 MB/s, C 894.1 -> 895.5: noise). The largest
gains are on samba (+3.0..+3.7 points), xml (+2.7..+3.1), nci (+1.5..+4.4, L1 before is a loaded row) and webster (+1.2..+2.2).

## Rows

| file | level | block | before C | before Go | before % | after C | after Go | after % | Go after/before |
|---|---|---|---|---|---|---|---|---|---|
| dickens | 1 | whole | 816.9 | 831.5 | 101.8% | 817.4 | 843.8 | 103.2% | +1.5% |
| dickens | 6 | whole | 863.2 | 876.3 | 101.5% | 862.2 | 893.6 | 103.6% | +2.0% |
| dickens | 9 | whole | 798.3 | 806.8 | 101.1% | 798.8 | 822.9 | 103.0% | +2.0% |
| dickens | 12 | whole | 986.3 | 1009.0 | 102.3% | 986.1 | 1028.6 | 104.3% | +1.9% |
| mozilla | 1 | whole | 818.2 | 800.3 | 97.8% | 815.2 | 803.3 | 98.5% | +0.4% |
| mozilla | 6 | whole | 842.2 | 813.4 | 96.6% | 842.7 | 819.9 | 97.3% | +0.8% |
| mozilla | 9 | whole | 850.2 | 822.2 | 96.7% | 850.5 | 831.0 | 97.7% | +1.1% |
| mozilla | 12 | whole | 834.1 | 800.9 | 96.0% | 833.7 | 808.2 | 96.9% | +0.9% |
| mr | 1 | whole | 894.1 | 890.1 | 99.6% | 895.5 | 890.0 | 99.4% | -0.0% |
| mr | 6 | whole | 986.6 | 974.4 | 98.8% | 987.8 | 997.7 | 101.0% | +2.4% |
| mr | 9 | whole | 982.2 | 972.1 | 99.0% | 983.2 | 990.7 | 100.8% | +1.9% |
| mr | 12 | whole | 979.1 | 969.9 | 99.1% | 981.8 | 998.7 | 101.7% | +3.0% |
| nci | 1 | whole | 2563.6 | 2456.5 | 95.8% | 2574.3 | 2579.7 | 100.2% | +5.0% |
| nci | 6 | whole | 2818.3 | 2774.7 | 98.5% | 2832.0 | 2856.9 | 100.9% | +3.0% |
| nci | 9 | whole | 2904.8 | 2909.0 | 100.1% | 2907.5 | 2953.8 | 101.6% | +1.5% |
| nci | 12 | whole | 2919.6 | 2861.8 | 98.0% | 2923.1 | 2959.7 | 101.3% | +3.4% |
| ooffice | 1 | whole | 585.2 | 564.3 | 96.4% | 584.8 | 571.1 | 97.7% | +1.2% |
| ooffice | 6 | whole | 580.0 | 557.2 | 96.1% | 579.8 | 564.6 | 97.4% | +1.3% |
| ooffice | 9 | whole | 591.8 | 570.7 | 96.4% | 591.7 | 577.0 | 97.5% | +1.1% |
| ooffice | 12 | whole | 563.7 | 540.1 | 95.8% | 563.8 | 547.7 | 97.1% | +1.4% |
| osdb | 1 | whole | 1129.9 | 1146.9 | 101.5% | 1129.3 | 1156.8 | 102.4% | +0.9% |
| osdb | 6 | whole | 1235.3 | 1259.3 | 101.9% | 1234.7 | 1270.6 | 102.9% | +0.9% |
| osdb | 9 | whole | 1232.5 | 1256.1 | 101.9% | 1233.3 | 1268.6 | 102.9% | +1.0% |
| osdb | 12 | whole | 1223.4 | 1246.9 | 101.9% | 1224.7 | 1262.0 | 103.0% | +1.2% |
| reymont | 1 | whole | 1063.4 | 1051.9 | 98.9% | 1067.9 | 1077.2 | 100.9% | +2.4% |
| reymont | 6 | whole | 1180.2 | 1173.6 | 99.4% | 1180.1 | 1202.0 | 101.9% | +2.4% |
| reymont | 9 | whole | 1076.0 | 1076.2 | 100.0% | 1076.8 | 1085.6 | 100.8% | +0.9% |
| reymont | 12 | whole | 1280.6 | 1258.1 | 98.2% | 1282.9 | 1282.3 | 100.0% | +1.9% |
| samba | 1 | whole | 1271.3 | 1234.6 | 97.1% | 1271.6 | 1277.1 | 100.4% | +3.4% |
| samba | 6 | whole | 1320.1 | 1273.7 | 96.5% | 1320.3 | 1323.2 | 100.2% | +3.9% |
| samba | 9 | whole | 1291.8 | 1245.4 | 96.4% | 1291.7 | 1291.7 | 100.0% | +3.7% |
| samba | 12 | whole | 1301.7 | 1258.7 | 96.7% | 1301.2 | 1297.2 | 99.7% | +3.1% |
| sao | 1 | whole | 576.2 | 584.0 | 101.4% | 576.2 | 588.2 | 102.1% | +0.7% |
| sao | 6 | whole | 563.6 | 572.0 | 101.5% | 564.1 | 576.6 | 102.2% | +0.8% |
| sao | 9 | whole | 565.1 | 571.6 | 101.2% | 564.4 | 576.2 | 102.1% | +0.8% |
| sao | 12 | whole | 581.8 | 591.0 | 101.6% | 582.1 | 597.0 | 102.6% | +1.0% |
| webster | 1 | whole | 969.2 | 970.7 | 100.2% | 970.2 | 993.5 | 102.4% | +2.3% |
| webster | 6 | whole | 1024.3 | 1028.3 | 100.4% | 1024.1 | 1048.6 | 102.4% | +2.0% |
| webster | 9 | whole | 983.3 | 982.4 | 99.9% | 983.3 | 994.0 | 101.1% | +1.2% |
| webster | 12 | whole | 1039.5 | 1040.4 | 100.1% | 1038.7 | 1059.1 | 102.0% | +1.8% |
| xml | 1 | whole | 1972.6 | 1919.2 | 97.3% | 1974.8 | 1974.1 | 100.0% | +2.9% |
| xml | 6 | whole | 2270.7 | 2214.2 | 97.5% | 2270.7 | 2277.0 | 100.3% | +2.8% |
| xml | 9 | whole | 2250.7 | 2191.3 | 97.4% | 2251.1 | 2260.2 | 100.4% | +3.1% |
| xml | 12 | whole | 2359.1 | 2299.1 | 97.5% | 2359.0 | 2373.7 | 100.6% | +3.2% |
| x-ray | 1 | whole | 495.9 | 494.4 | 99.7% | 496.0 | 498.6 | 100.5% | +0.8% |
| x-ray | 6 | whole | 513.6 | 510.8 | 99.5% | 513.5 | 515.5 | 100.4% | +0.9% |
| x-ray | 9 | whole | 499.5 | 494.8 | 99.1% | 500.0 | 497.6 | 99.5% | +0.6% |
| x-ray | 12 | whole | 550.3 | 542.7 | 98.6% | 548.5 | 550.7 | 100.4% | +1.5% |
| silesia.tar | 1 | 1M | 969.5 | 959.3 | 98.9% | 970.1 | 974.5 | 100.5% | +1.6% |
| silesia.tar | 6 | 1M | 1014.3 | 998.9 | 98.5% | 1012.7 | 1014.4 | 100.2% | +1.6% |
| silesia.tar | 9 | 1M | 999.8 | 985.6 | 98.6% | 1000.4 | 998.8 | 99.8% | +1.3% |
| silesia.tar | 12 | 1M | 1020.8 | 1002.7 | 98.2% | 1022.4 | 1018.8 | 99.6% | +1.6% |
| silesia.tar | 1 | 128K | 940.9 | 929.0 | 98.7% | 942.1 | 944.8 | 100.3% | +1.7% |
| silesia.tar | 6 | 128K | 968.3 | 947.5 | 97.9% | 967.7 | 964.9 | 99.7% | +1.8% |
| silesia.tar | 9 | 128K | 958.3 | 940.3 | 98.1% | 961.3 | 955.2 | 99.4% | +1.6% |
| silesia.tar | 12 | 128K | 969.3 | 946.2 | 97.6% | 970.2 | 962.8 | 99.2% | +1.8% |
| silesia.tar | 1 | 16K | 747.9 | 724.7 | 96.9% | 749.1 | 735.0 | 98.1% | +1.4% |
| silesia.tar | 6 | 16K | 755.8 | 725.5 | 96.0% | 755.1 | 738.6 | 97.8% | +1.8% |
| silesia.tar | 9 | 16K | 758.0 | 727.3 | 95.9% | 759.2 | 741.0 | 97.6% | +1.9% |
| silesia.tar | 12 | 16K | 739.4 | 707.5 | 95.7% | 740.4 | 721.3 | 97.4% | +2.0% |

Superseded as the gate evidence by 2026-11-09-deflate-decode-round2-recheck.md (alternating base/after runs).
Most of this gain is code alignment, not the table build, and it is fragile and microarchitecture specific:
with the PCALIGN $32 measured here the loop head happened to sit at 0 mod 64, and 32 bytes of unrelated
code linked before the function moved it to 32 mod 64 and gave back most of the gain on this i9-13900H
(the recheck switched the head to PCALIGN $64, which also aligns the function and removes that layout
dependence); other CPUs may prefer a different placement.

Raw result files of the two runs: scratchpad t131/before.md, t131/after.md (logs t131/before.log, after.log).
