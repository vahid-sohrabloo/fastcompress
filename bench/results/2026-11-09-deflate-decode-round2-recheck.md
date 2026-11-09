# gzip deflate-decode round 2 recheck (alternating base/after)

Re-measures the round-2 decoder gate (2026-11-09-deflate-decode-round2.md) with alternating runs, and splits
the gain into code alignment and the table-build change.

- Variants, all built from the same worktree with the same Go (go1.27.1) and flags; only internal/deflate
  files differ, swapped in with `GOFLAGS=-overlay=...` for compare.sh's `go build`:
  - base: decode_table.go and decompress_fastloop_amd64.s as before round 2 (a copy kept in the session
    scratchpad, not in the repository), no PCALIGN; the encoder files are the current ones, so the layout before the function is the same.
  - after: the worktree: decode_table.go table-build change, fast loop with PCALIGN $64 at the loop head,
    $16 at the four copy loops, the two fall-through copy loops entered by a JMP over their padding.
  - al (alignment only): after's assembly, base's decode_table.go.
  - tb (table only) and shift (after with 32 dead bytes linked before the function): NOT measured on the
    full matrix (see Not done); the layout question is answered by the A/B below.
- Each run: `bench/gzip/compare.sh -mode d -l "1 6 9 12" -i 3 -g 3 -n 3 -libs "ld ours"`, taskset -c 2
  (nothing started on its SMT sibling CPU 3), under the test flock and a 6G scope, AC0=1. A run started
  only at load1 < 2 with no test/benchmark running; a run during which load1 exceeded 3 was aborted and
  redone from the start (01-base 3 attempts, 02-after 2, 03-base 1, 04-after 7, 05-al 4). Run order
  base, after, base, after, al, 04:56-11:53, 2026-11-09. 60/60 rows round-trip verified in each run.

## Gate (geomean >= 100% in every after run, every row >= 95%)

| run | L1 geomean / min | L6 | L9 | L12 |
|---|---|---|---|---|
| 01 base | 98.8% / 95.8% nci | 98.6% / 95.9% tar/16K | 98.8% / 96.0% tar/16K | 98.4% / 95.6% tar/16K |
| 02 after | 100.5% / 97.6% ooffice | 100.5% / 97.4% ooffice | 100.3% / 97.5% ooffice | 100.3% / 97.0% mozilla |
| 03 base | 98.8% / 95.6% nci | 98.6% / 95.7% tar/16K | 98.8% / 95.9% tar/16K | 98.4% / 95.8% ooffice |
| 04 after | 100.5% / 97.8% ooffice | 100.6% / 97.5% mozilla | 100.2% / 97.4% ooffice | 100.3% / 96.9% mozilla |
| 05 al | 100.2% / 97.6% ooffice | 100.4% / 97.3% mozilla | 100.1% / 97.0% tar/16K | 100.1% / 96.9% ooffice |

After-run geomean spread per level: L1 100.5-100.5, L6 100.5-100.6, L9 100.2-100.3, L12 100.3-100.3; base
runs 98.4-98.8 and reproducible to 0.0-0.1 points. Every after run is >= 100% at every level and its lowest
row is 96.9%: **PASS**, with a margin of only 0.2-0.6 points of geomean. silesia.tar/16K: base 95.6-96.9%,
after 97.3-98.4%.

## Alignment vs table build

Go MB/s geomean over the 15 rows, vs the mean of the two base runs:

| variant | L1 | L6 | L9 | L12 |
|---|---|---|---|---|
| after (2 runs) | +1.74% | +1.88% | +1.51% | +1.89% |
| al, alignment only (1 run) | +1.47% | +1.75% | +1.34% | +1.77% |
| table build (after - al) | ~+0.27 | ~+0.13 | ~+0.17 | ~+0.12 |

About 90% of the gain is the alignment. The table build's share is ~0.1-0.3% here (it is mostly visible on
silesia.tar/16K: al 96.9-97.9% vs after 97.3-98.4%) and is within one run's noise; tb alone was not run.

## Layout dependence (A/B, abrun, 7 rows at L6, 5-6 interleaved rounds, median, % of C)

| row | base | T13.1 asm ($32) | T13.1 asm, function moved +32 B | $64 | $64, moved +32 B | $64 + JMP (kept) | kept, moved +32 B |
|---|---|---|---|---|---|---|---|
| silesia.tar/16K | 95.8 | 97.7 | 96.9 | 98.0 | 97.8 | 97.8 | 97.8 |
| silesia.tar/128K | 97.7 | 99.4 | 98.6 | 99.7 | 99.7 | 99.7 | 99.6 |
| ooffice | 96.1 | 97.6 | 96.4 | 97.5 | 97.4 | 97.4 | 97.3 |
| mozilla | 96.8 | 97.6 | 96.9 | 97.6 | 97.7 | 97.7 | 97.7 |
| nci | 98.4 | 101.2 | 100.3 | 101.1 | 101.1 | 101.2 | 101.0 |
| xml | 97.4 | 100.1 | 99.1 | 100.2 | 100.3 | 100.3 | 100.4 |
| dickens | 101.2 | 103.8 | 101.3 | 103.3 | 103.6 | 103.7 | 103.5 |

"moved +32 B" = 32 dead bytes after compress_fastest_amd64.s's RET, so every later function moves by 32.
With T13.1's PCALIGN $32 the loop head was at 0 mod 64 by luck (function at ...c80, head at ...d00); moved
by 32 it sits at 32 mod 64 and a third to nearly all of the gain is gone (median about half; dickens 96%,
mozilla 88%, ooffice 80%, tar/16K 42%, nci 32%; dickens 103.8 -> 101.3, base 101.2). PCALIGN $64
raises the function's alignment to 64 on go1.27 (objdump: function ...c80 -> ...cc0 when moved, head
...d40, 64-aligned), so the head no longer depends on unrelated code. JMP over the copySmall / copy1Loop
padding: no measurable difference (kept, so the NOPs never execute); removing that padding loses 0.3-0.7
points on tar/16K, tar/128K and xml (first A/B, c1.log). The gain is therefore a front-end/cache-line effect of this i9-13900H (Raptor Lake
P-core); other microarchitectures may prefer other placements, and the 0.2-0.6 point margin above should be
read with that in mind.

## Not done

- tb (table build only) and shift on the full matrix: 10 attempts of tb between 11:53 and 15:25 were all
  aborted at load1 > 3 (afternoon desktop load), and the work was stopped at 16:30. The A/B table above is
  the layout evidence; the table-build share is after - al.

Raw files: 2026-11-09-deflate-decode-round2-recheck-raw/ (the five per-run tables 0[1-5]-*.md and the A/B
rounds ab-c1.log, ab-c2.log; the medians above are computed from those rounds, there is no separate summary
file). Not in the repository: the compare.sh run logs and the overlay sources (session scratchpad only).
