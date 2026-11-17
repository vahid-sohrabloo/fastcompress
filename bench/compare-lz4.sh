#!/usr/bin/env bash
# Compare lz4bench (this port) against `lz4 -b<level>` from C lz4 v1.11.1.
#
#   bench/compare-lz4.sh [-mode c|d] [-l "1 2 9 12"] [-f "FILE..."] [-B BLOCK] [-D] [-S] [-dict] [-favor] [-T N] [-p] [-i SECS] [-g SECS] [-n RUNS] -label NAME
#
# Writes bench/results/<date>-lz4-<label>.md. Each row: taskset -c $CPU, RUNS
# (default 3) interleaved C/Go runs, median speed reported. C runs
# `lz4 -b<L> -T1 -B<BLOCK> -i<SECS> FILE` (one frame per file, independent
# blocks, no checksums; with a custom -B the CLI compresses one block per
# LZ4F_compressUpdate, single-threaded); Go runs `lz4bench` the same way.
# Both report the best mean time per pass of ~1 s runs. Keep CPU $CPU's SMT
# sibling and the rest of the machine idle. Rows whose C and Go ratios differ
# are flagged "MISMATCH" (the port must be byte-identical). -mode d compares
# decompression (lz4 -b's second speed, lz4bench -mode d). -D uses linked
# blocks (lz4 -BD, lz4bench -linked) and C's --single-thread: with -T1 C
# cuts linked frames into multi-threaded jobs (other frames; not ported).
# -S runs C with --single-thread instead of -T1 for independent blocks too
# (-T1 runs C's job mode with one worker thread: the same frames, a little
# slower). -dict compresses each file with a dictionary (lz4 -D, lz4bench
# -dict): 64 KiB from the middle of the file itself. -favor sets
# favorDecSpeed (lz4 --favor-decSpeed, lz4bench -favor). -T N runs C's
# multi-threaded mode with N workers (lz4 -TN; with -D: linked frames cut
# into jobs) and lz4bench -T N; pin to N cores with CPU=LIST.
# -p adds github.com/pierrec/lz4/v4 (bench/lz4, a nested module) as a third
# column, timed the same way, interleaved with the C and Go runs (independent
# blocks of 64 KiB, 256 KiB, 1 MiB or 4 MiB only). Its levels and output are
# its own (level 1: its Fast compressor; 9: CompressorHC Level1; 12: Level9),
# so its ratio is shown; with -mode d it decodes this port's frames (C's).
# PREBUILT=DIR in the environment uses DIR/lz4bench (and DIR/pierrecbench
# with -p) instead of go build.
set -euo pipefail
cd "$(dirname "$0")/.."

LEVELS="1 2 9 12"
FILES=""
LABEL=""
BLOCK=4M
CSECS=3
GSECS=3
RUNS=3
MODE=c
LINKED=""
CTHREAD=-T1
CPU=${CPU:-2}
DICT=""
FAVOR=""
GTHREAD=""
PIERREC=""
while [ $# -gt 0 ]; do
  case "$1" in
    -l) LEVELS=$2; shift 2 ;;
    -f) FILES=$2; shift 2 ;;
    -B) BLOCK=$2; shift 2 ;;
    -D) LINKED=1; CTHREAD=--single-thread; shift ;;
    -S) CTHREAD=--single-thread; shift ;;
    -dict) DICT=1; shift ;;
    -favor) FAVOR=1; shift ;;
    -T) CTHREAD=-T$2; GTHREAD="-T $2"; shift 2 ;;
    -p) PIERREC=1; shift ;;
    -i) CSECS=$2; shift 2 ;;
    -g) GSECS=$2; shift 2 ;;
    -n) RUNS=$2; shift 2 ;;
    -mode) MODE=$2; shift 2 ;;
    -label) LABEL=$2; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
case "$MODE" in c|d) ;; *) echo "-mode must be c or d" >&2; exit 2 ;; esac
[ -n "$LABEL" ] || { echo "usage: $0 [-mode c|d] [-l \"1 9\"] [-f FILES] [-B BLOCK] [-p] [-i SECS] [-g SECS] [-n RUNS] -label NAME" >&2; exit 2; }
[ -z "$PIERREC" ] || [ -z "$LINKED$DICT$FAVOR$GTHREAD" ] || { echo "-p supports independent blocks only (no -D, -dict, -favor or -T)" >&2; exit 2; }

LZ4=ref/lz4/programs/lz4
[ -x "$LZ4" ] || { echo "missing $LZ4; run ref/fetch.sh" >&2; exit 1; }
BIN=$(mktemp -d)
trap 'rm -rf "$BIN"' EXIT
if [ -n "${PREBUILT:-}" ]; then
  cp "$PREBUILT/lz4bench" "$BIN/lz4bench"
  [ -z "$PIERREC" ] || cp "$PREBUILT/pierrecbench" "$BIN/pierrecbench"
else
  go build -o "$BIN/lz4bench" ./bench/cmd/lz4bench
  [ -z "$PIERREC" ] || (cd bench/lz4 && go build -o "$BIN/pierrecbench" .)
fi

bytes_of() { # 4M, 256K, 65536 -> bytes
  case "$1" in
    *M) echo $(( ${1%M} * 1048576 )) ;;
    *K) echo $(( ${1%K} * 1024 )) ;;
    *) echo "$1" ;;
  esac
}
GBLOCK=$(bytes_of "$BLOCK")
case "$PIERREC:$GBLOCK" in :*|*:65536|*:262144|*:1048576|*:4194304) ;; *) echo "-p needs -B 64K, 256K, 1M or 4M" >&2; exit 2 ;; esac

if [ -z "$FILES" ]; then
  for f in bench/corpus/silesia/*; do [ -f "$f" ] && FILES="$FILES $f"; done
fi
[ -n "$FILES" ] || { echo "no corpus files; run ref/fetch.sh" >&2; exit 1; }

# C: `lz4 -b` redraws its progress line with carriage returns; the result is
# the last line with "(RATIO), C MB/s, D MB/s".
dict_of() { # file -> dictionary path (64 KiB from the middle of the file)
  local d="$BIN/$(basename "$1").dict"
  if [ ! -f "$d" ]; then
    local size; size=$(stat -c %s "$1")
    tail -c +$(( size / 2 + 1 )) "$1" | head -c 65536 > "$d"
  fi
  echo "$d"
}
c_bench() { # level file -> "MBps ratio"
  taskset -c "$CPU" "$LZ4" -b"$1" $CTHREAD ${LINKED:+-BD} ${FAVOR:+--favor-decSpeed} ${DICT:+-D "$(dict_of "$2")"} -B"$BLOCK" -i"$CSECS" "$2" 2>&1 | tr '\r' '\n' \
    | sed -nE 's/.*\(([0-9.]+)\), *([0-9.]+) MB\/s, *([0-9.]+) MB\/s.*/\2 \3 \1/p' | tail -1 \
    | awk -v m="$MODE" '{print (m == "d" ? $2 : $1), $3}'
}
go_bench() { # level file -> "MBps ratio"
  taskset -c "$CPU" "$BIN/lz4bench" -mode "$MODE" -level "$1" -block "$GBLOCK" ${LINKED:+-linked} ${FAVOR:+-favor} $GTHREAD ${DICT:+-dict "$(dict_of "$2")"} -secs "$GSECS" "$2"
}
p_bench() { # level file -> "MBps ratio"
  taskset -c "$CPU" "$BIN/pierrecbench" -mode "$MODE" -level "$1" -block "$GBLOCK" -secs "$GSECS" "$2"
}
median() { sort -g | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }

OUT=bench/results/$(date +%F)-lz4-$LABEL.md
mkdir -p bench/results
{
  echo "# lz4 $LABEL"
  echo
  echo "- date: $(date -Is); host: $(uname -n); pinned to CPU $CPU"
  echo "- C: $($LZ4 -V 2>&1 | head -1 | sed 's/^\*\** *//; s/ *\**$//'); \`lz4 -b<L> $CTHREAD ${LINKED:+-BD }${FAVOR:+--favor-decSpeed }${DICT:+-D DICT }-B$BLOCK -i$CSECS\`; Go: \`lz4bench -mode $MODE -block $GBLOCK ${GTHREAD:+$GTHREAD }${LINKED:+-linked }${FAVOR:+-favor }${DICT:+-dict DICT }-secs $GSECS\`${DICT:+ (DICT: 64 KiB from the middle of the file)}"
  echo "- median of $RUNS interleaved runs per cell; MB/s = 1e6 bytes/s of input ($([ "$MODE" = d ] && echo decompression || echo compression))"
  if [ -n "$PIERREC" ]; then echo "- pierrec: \`bench/lz4\` (pierrec/lz4/v4 $(sed -nE 's/.*pierrec\/lz4\/v4 (v[0-9.]+).*/\1/p' bench/lz4/go.mod | head -1), same method; level 1: Fast, 9: CompressorHC Level1, 12: Level9; $([ "$MODE" = d ] && echo "decodes this port's frames (C's), ratio is theirs" || echo "its own output, ratio shown"))"; fi
  echo
  echo "| file | level | C MB/s | Go MB/s | % of C | C ratio | Go ratio |${PIERREC:+ pierrec MB/s | pierrec % of C | pierrec ratio |}"
  echo "|---|---|---|---|---|---|---|${PIERREC:+---|---|---|}"
} > "$OUT"

declare -A PCTS PPCTS
for f in $FILES; do
  bname=$(basename "$f")
  for lvl in $LEVELS; do
    echo "== $bname L$lvl" >&2
    cs=(); gs=(); ps=(); cr=""; gr=""; pr=""
    for ((r = 0; r < RUNS; r++)); do
      read -r cm cr <<<"$(c_bench "$lvl" "$f")"
      read -r gm gr <<<"$(go_bench "$lvl" "$f")"
      cs+=("$cm"); gs+=("$gm")
      if [ -n "$PIERREC" ]; then
        read -r pm pr <<<"$(p_bench "$lvl" "$f")"
        [ -n "$pm" ] && [ -n "$pr" ] || { echo "could not parse pierrecbench output for $f" >&2; exit 1; }
        ps+=("$pm")
      fi
    done
    c=$(printf '%s\n' "${cs[@]}" | median)
    g=$(printf '%s\n' "${gs[@]}" | median)
    pct=$(awk -v g="$g" -v c="$c" 'BEGIN{printf "%.1f", 100*g/c}')
    flag=""
    [ "$cr" = "$gr" ] || flag=" MISMATCH"
    pcols=""
    if [ -n "$PIERREC" ]; then
      p=$(printf '%s\n' "${ps[@]}" | median)
      ppct=$(awk -v g="$p" -v c="$c" 'BEGIN{printf "%.1f", 100*g/c}')
      PPCTS[$lvl]="${PPCTS[$lvl]:-} $ppct"
      pcols=" $p | $ppct% | $pr |"
    fi
    echo "| $bname | $lvl | $c | $g | $pct%$flag | $cr | $gr |$pcols" >> "$OUT"
    PCTS[$lvl]="${PCTS[$lvl]:-} $pct"
  done
done
{
  echo
  geo() { awk '{s=0; for(i=1;i<=NF;i++) s+=log($i); printf "%.1f%%", exp(s/NF)}'; }
  echo "| level | geomean % of C |${PIERREC:+ pierrec geomean % of C |}"
  echo "|---|---|${PIERREC:+---|}"
  for lvl in $LEVELS; do
    pg=""
    [ -z "$PIERREC" ] || pg=" $(echo ${PPCTS[$lvl]} | geo) |"
    echo "| $lvl | $(echo ${PCTS[$lvl]} | geo) |$pg"
  done
} >> "$OUT"
echo "wrote $OUT" >&2
cat "$OUT"
