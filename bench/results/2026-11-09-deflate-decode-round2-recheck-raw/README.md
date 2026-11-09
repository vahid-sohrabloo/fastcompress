# Raw data of 2026-11-09-deflate-decode-round2-recheck.md

- `01-base.md` .. `05-al.md`: compare.sh's full per-run tables (`-mode d -l "1 6 9 12" -i 3 -g 3 -n 3`),
  in run order base, after, base, after, al.
- `ab-c1.log`, `ab-c2.log`: the A/B rounds (`row variant MB/s` per line, level 6, C first in each row;
  c1 5 rounds, c2 6). The report's layout table is the per-row median over the rounds, as % of C.
  Variant labels: `base` pre-round-2 sources, `cur` T13.1 asm (PCALIGN $32), `pre32` cur with 32 dead
  bytes after compress_fastest_amd64.s's RET (function moved +32 B), `a64` PCALIGN $64, `a64j` $64 + JMP
  over the fall-through copy-loop padding (kept), `pre32a64` / `pre32a64j` those moved +32 B, `j` $32 +
  JMP, `n` $32 without the copy-loop padding.
