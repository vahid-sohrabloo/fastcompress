# gzip deflate: confirmation benchmark of HEAD 5c2e383

Independent re-measurement of the claims in 2026-11-10-deflate-compress-round7.md (compress) and
2026-11-09-deflate-decode-round2-recheck.md (decode), from the committed tree (only untracked W20.go/W20.json
present; they are outside bench/gzip, which is its own module, so the harness is unaffected).

## Method

- `bench/gzip/compare.sh -libs "ld ours" -i 3 -g 3 -n 3` (cpu 2, SMT sibling 3 idle, C/Go interleaved, medians of 3), go1.27.1, gcc 13.3.
- Compress: `-mode c -l "1 3 6 9 12"`. c2 uses `-f` with the 12 Silesia files whole + silesia.tar in 1M blocks (the 65 rows of
  round 7). c1 used the harness default file set (adds silesia.tar 128K and 16K: 75 rows); its 65-row subset is also given.
- Decode: `-mode d -l "1 6 9 12"`, default file set (15 rows per level, 60 rows; the recheck's set). Two runs d1, d2.
- Preconditions per run: AC0=1, load1 < 4 at start (polled, bounded), no test/benchmark process, one run at a time.
  load1 sampled every 20 s during each run; none exceeded 5, no run redone.
- Byte identity: no MISMATCH row in c1 (75/75) or c2 (65/65) (compare.sh compares exact compressed byte counts). Decode:
  64 rows per d-run counted with the round-trip verified by gzbench before timing (no failure reported).

| run | what | date, time | load1 start -> end | load1 sampled max / mean |
|---|---|---|---|---|
| c1 | compress, 75 rows | 2026-11-10 05:02:52-06:00:40 | 2.68 -> 2.37 | 3.91 / 2.18 |
| c2 | compress, 65 rows | 2026-11-10 06:01:39-06:39:38 | 1.83 -> 2.02 | 3.04 / 2.03 |
| d1 | decode, 60 rows | 2026-11-10 06:39:55-07:12:42 | 1.65 -> 1.71 | 3.66 / 2.17 |
| d2 | decode, 60 rows | 2026-11-10 07:12:54-07:45:38 | 1.68 -> 2.04 | 4.26 / 2.18 |

## Compress: geomean % of C (min row), vs round 7 "after"

| level | claimed (13 rows) | c2 (65 rows total) | c1 (65-row subset) | c1 (75 rows) |
|---|---|---|---|---|
| 1 | 103.6% (99.6% x-ray) | 103.7% (99.5% x-ray) | 103.6% (100.9% sao) | 103.4% (99.5% x-ray) |
| 3 | 101.6% (100.1% mozilla) | 101.3% (99.9% mozilla) | 101.5% (100.0% ooffice) | 101.3% (99.4% tar/16K) |
| 6 | 102.1% (101.4% samba) | 102.1% (101.2% xml) | 102.0% (101.1% x-ray) | 101.9% (100.3% tar/16K) |
| 9 | 102.8% (101.2% xml) | 102.8% (101.4% webster) | 103.1% (101.4% webster) | 102.9% (100.5% tar/16K) |
| 12 | 119.8% (106.5% nci) | **109.8% (102.8% xml)** | **109.5% (100.0% nci)** | **109.3% (100.0% nci)** |

c2 level 12 rows (C / Go MB/s, % of C); round-7 claim in brackets:
-  dickens :  6  /  6.8 ,  113.3% 
-  mozilla :  7.1  /  7.8 ,  109.9% 
-  mr :  4.1  /  4.6 ,  112.2% 
-  nci :  3.1  /  3.2 ,  103.2% 
-  ooffice :  9.9  /  10.8 ,  109.1% 
-  osdb :  9.5  /  10.6 ,  111.6% 
-  reymont :  4.6  /  5.4 ,  117.4% 
-  samba :  5.2  /  5.7 ,  109.6% 
-  sao :  15.5  /  16.2 ,  104.5% 
-  webster :  4.8  /  5.7 ,  118.8% 
-  xml :  3.6  /  3.7 ,  102.8% 
-  x-ray :  23.2  /  24.8 ,  106.9% 
-  silesia.tar :  5.1  /  5.6 ,  109.8% 

Round-7 L12 Go MB/s for comparison: dickens 8.1, mozilla 8.5, mr 4.6, nci 3.3, ooffice 11.9, osdb 12.1, reymont 6.3,
samba 6.2, sao 16.7, webster 6.7, xml 4.1, x-ray 25.3, tar 6.1. C numbers match the claim to the printed digit.

## Decode: geomean % of C (min row), vs recheck "after" (runs 02/04)

| level | claimed | d1 | d2 |
|---|---|---|---|
| 1 | 100.5% (97.6-97.8% ooffice) | 100.6% (97.7% ooffice) | 100.6% (97.6% ooffice) |
| 6 | 100.5-100.6% (97.4-97.5%) | 100.7% (97.5% ooffice) | 100.7% (97.4% ooffice) |
| 9 | 100.2-100.3% (97.4-97.5%) | 100.4% (97.5% ooffice) | 100.4% (97.6% ooffice) |
| 12 | 100.3% (96.9-97.0%) | 100.5% (97.1% ooffice) | 100.4% (97.1% ooffice) |

## Verdict

- Confirmed (within +-1 geomean point, no row below 95%): compress L1, L3, L6, L9; decode L1, L6, L9, L12
  (decode is 0.1-0.2 points above the claim; margin over 100% is 0.4-0.7 points).
- NOT confirmed: compress L12. Geomean 109.3-109.8% against the claimed 119.8% (about 10 points lower, in all three
  runs, reproducible); min row 102.8% (c2) / 100.0% (c1) against 106.5%. It still passes the gate (>=100%, all rows >=95%),
  but the claimed +20% over the round-6 tree is not reproduced: Go L12 MB/s is ~5-15% below the round-7 numbers on every file
  while C is identical. Cause not investigated.
- Gates: compress and decode PASS at every level in every run; no row below 99.4% (compress) or 97.1% (decode).
