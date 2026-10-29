#!/usr/bin/env bash
# Compare gobench (this port) against `zstd -b<level>` from C zstd v1.5.7.
#
#   bench/compare.sh [-l "1 3 7 11"] [-f "FILE[:BLOCK]..."] [-i SECS] [-g SECS] [-n RUNS] -label NAME
#
# Writes bench/results/<date>-<label>.md. Each row: taskset -c $CPU, RUNS
# (default 3) interleaved C/Go runs, median speed reported. Both sides report
# the best ~1 s window (mean time per pass) seen in their time budget. Keep
# CPU $CPU's SMT sibling (CPU 3 for CPU 2) and the rest of the machine idle. (All levels are
# ported; the "n/a (not ported)" row for a gobench failure mentioning "not
# ported" is no longer reachable.) Rows whose C and Go ratios differ are
# flagged "MISMATCH" (the port must be byte-identical, so ratios must be equal).
set -euo pipefail
cd "$(dirname "$0")/.."

LEVELS="1 3 7 11"
FILES=""
LABEL=""
CSECS=5   # zstd -i: seconds per benchmark pass (it times compression and decompression)
GSECS=5   # gobench -secs: minimum seconds of timed passes (same budget as zstd -i)
RUNS=3
CPU=${CPU:-2}
while [ $# -gt 0 ]; do
  case "$1" in
    -l) LEVELS=$2; shift 2 ;;
    -f) FILES=$2; shift 2 ;;
    -i) CSECS=$2; shift 2 ;;
    -g) GSECS=$2; shift 2 ;;
    -n) RUNS=$2; shift 2 ;;
    -label) LABEL=$2; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
[ -n "$LABEL" ] || { echo "usage: $0 [-l \"1 3\"] [-f FILES] [-i SECS] [-g SECS] [-n RUNS] -label NAME" >&2; exit 2; }

ZSTD=ref/zstd/programs/zstd
[ -x "$ZSTD" ] || { echo "missing $ZSTD; run ref/fetch.sh" >&2; exit 1; }
BIN=$(mktemp -d)
trap 'rm -rf "$BIN"' EXIT
go build -o "$BIN/gobench" ./bench/cmd/gobench

# "file:block" entries; block 0 = whole file.
SPECS=()
if [ -n "$FILES" ]; then
  for f in $FILES; do case "$f" in *:*) SPECS+=("$f") ;; *) SPECS+=("$f:0") ;; esac; done
else
  for f in bench/corpus/silesia/*; do [ -f "$f" ] && SPECS+=("$f:0"); done
  if [ -f bench/corpus/silesia.tar ]; then
    # At level 11 the 1M/4M rows run row-based lazy2, 128K runs btlazy2 and
    # 16K runs btopt (clevels.h srcSize tables).
    SPECS+=("bench/corpus/silesia.tar:1048576" "bench/corpus/silesia.tar:4194304"
            "bench/corpus/silesia.tar:131072" "bench/corpus/silesia.tar:16384")
  fi
fi
[ ${#SPECS[@]} -gt 0 ] || { echo "no corpus files; run ref/fetch.sh" >&2; exit 1; }

# C: `zstd -b` redraws its progress line with carriage returns; the final
# result is the last line containing "(xRATIO), C MB/s, D MB/s". Convert CR to
# LF and take that line's ratio and compression speed. -qq would print nothing.
# -T1 is required: without it the CLI benchmarks with worker threads (even under
# taskset), whose multi-job output differs from ZSTD_compress2 single-threaded
# (different size, and not what this port reproduces).
c_bench() { # level block file -> "MBps xRatio"
  local args=(-b"$1" -i"$CSECS" -T1)
  [ "$2" != 0 ] && args+=(-B"$2")
  taskset -c "$CPU" "$ZSTD" "${args[@]}" "$3" 2>&1 | tr '\r' '\n' \
    | sed -nE 's/.*\(x([0-9.]+)\), *([0-9.]+) MB\/s, .*/\2 \1/p' | tail -1
}
go_bench() { # level block file -> "MBps ratio" ; non-zero exit on failure
  taskset -c "$CPU" "$BIN/gobench" -level "$1" -block "$2" -secs "$GSECS" "$3"
}
median() { sort -g | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }

OUT=bench/results/$(date +%F)-$LABEL.md
mkdir -p bench/results
{
  echo "# $LABEL"
  echo
  echo "- date: $(date -Is); host: $(uname -n); cpu: $(grep -m1 'model name' /proc/cpuinfo | sed 's/.*: //'), pinned to CPU $CPU"
  echo "- C: $($ZSTD -V | head -1); \`zstd -b<L> -i$CSECS [-B<block>]\`; Go: \`gobench -secs $GSECS\`; both report the best ~1 s window (mean per pass), CRC off"
  echo "- median of $RUNS interleaved runs per cell; MB/s = 1e6 bytes/s of input (compression only)"
  echo
  echo "| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio |"
  echo "|---|---|---|---|---|---|---|---|"
} > "$OUT"

PCTS=()
for spec in "${SPECS[@]}"; do
  f=${spec%:*}; blk=${spec##*:}
  bname=$(basename "$f"); blabel=whole
  if [ "$blk" != 0 ]; then
    if [ $((blk % 1048576)) = 0 ]; then blabel=$((blk / 1048576))M; else blabel=$((blk / 1024))K; fi
  fi
  for lvl in $LEVELS; do
    echo "== $bname L$lvl block=$blabel" >&2
    if ! probe=$(go_bench "$lvl" "$blk" "$f" 2>&1); then
      if grep -q "not ported" <<<"$probe"; then
        echo "| $bname | $lvl | $blabel | n/a (not ported) | n/a (not ported) | n/a | n/a | n/a |" >> "$OUT"
        continue
      fi
      echo "gobench failed: $probe" >&2; exit 1
    fi
    cs=(); gs=(); cr=""; gr=""
    for ((r = 0; r < RUNS; r++)); do
      read -r cm cr <<<"$(c_bench "$lvl" "$blk" "$f")"
      read -r gm gr <<<"$(go_bench "$lvl" "$blk" "$f")"
      [ -n "$cm" ] && [ -n "$cr" ] || { echo "could not parse zstd -b output for $f" >&2; exit 1; }
      [ -n "$gm" ] && [ -n "$gr" ] || { echo "could not parse gobench output for $f" >&2; exit 1; }
      cs+=("$cm"); gs+=("$gm")
    done
    cmed=$(printf '%s\n' "${cs[@]}" | median)
    gmed=$(printf '%s\n' "${gs[@]}" | median)
    pct=$(awk -v g="$gmed" -v c="$cmed" 'BEGIN{printf "%.1f", 100*g/c}')
    PCTS+=("$pct")
    cr=${cr#x}; flag=""
    [ "$cr" = "$gr" ] || flag=" MISMATCH"
    echo "| $bname | $lvl | $blabel | $cmed | $gmed | $pct% | $cr | $gr$flag |" >> "$OUT"
  done
done

if [ ${#PCTS[@]} -gt 0 ]; then
  geo=$(printf '%s\n' "${PCTS[@]}" | awk '{s+=log($1); n++} END{printf "%.1f", exp(s/n)}')
  { echo; echo "Geometric mean of % of C over ${#PCTS[@]} rows: **$geo%**"; } >> "$OUT"
fi
echo "wrote $OUT" >&2
cat "$OUT"
