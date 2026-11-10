#!/usr/bin/env bash
# deflate-timing.sh LOG... - per-group wall time of scripts/deflate-test.sh logs (go test -v output).
#
# For each log: the run's wall time and memory.peak (its RESULT line), then the summed top-level test
# wall times (from deflate-test.sh's per-line timestamps, 1 s resolution; else go test's durations,
# which exclude parallel subtests) per internal/deflate partition (scripts/deflate-groups.sh; tests run one after another, so the
# sums add up to the run, minus build and process start), and the 5 slowest top-level tests.
set -euo pipefail
G="$(dirname "$0")/deflate-groups.sh"
[ $# -gt 0 ] || { echo "usage: $0 LOG..." >&2; exit 2; }
args=()
for p in lazy nopt rest equiv dec; do args+=(-v "re_$p=$("$G" -regex "$p")"); done
for log in "$@"; do
  awk "${args[@]}" -v file="$log" '
    function part(name) {
      if (name ~ re_lazy) return "lazy"
      if (name ~ re_nopt) return "nopt"
      if (name ~ re_rest) return "rest"
      if (name ~ re_equiv) return "equiv"
      if (name ~ re_dec) return "dec"
      return "other"
    }
    { ts = ""; if (match($0, /^[0-9]+ /)) { ts = substr($0, 1, RLENGTH - 1) + 0; $0 = substr($0, RLENGTH + 1) } }
    /^=== RUN   Test[^\/]*$/ && ts != "" { start[$3] = ts }
    /^RESULT / { result = substr($0, 8) }
    /^# / && NR == 1 { head = substr($0, 3) }
    /^--- (PASS|FAIL|SKIP): Test/ {
      name = $3; s = $4; gsub(/[()s]/, "", s)
      if (ts != "" && (name in start)) s = ts - start[name] # wall time, parallel subtests included
      p = part(name); sum[p] += s; n[p]++
      if ($2 == "FAIL:") fail[p]++
      tests[name] = s
    }
    /^(ok|FAIL)[ \t]+github.com/ { pkg = $2; pkgt = $3 }
    END {
      printf "%s\n  %s\n  %s %s  %s\n", file, head, pkg, pkgt, result
      for (p in sum) printf "  %-6s %9.1f s  %4d tests%s\n", p, sum[p], n[p], (p in fail) ? sprintf("  %d FAILED", fail[p]) : ""
      k = 0
      for (t in tests) order[++k] = t
      for (i = 1; i <= k; i++) for (j = i + 1; j <= k; j++) if (tests[order[j]] + 0 > tests[order[i]] + 0) { x = order[i]; order[i] = order[j]; order[j] = x }
      for (i = 1; i <= k && i <= 5; i++) printf "    %8.1f s  %s\n", tests[order[i]], order[i]
    }' "$log"
done
