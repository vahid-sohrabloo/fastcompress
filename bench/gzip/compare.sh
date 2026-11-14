#!/usr/bin/env bash
# Compare this repository's gzip/DEFLATE port ("ours") against libdeflate v1.26
# built as it ships (ref/ldref/ldbench), plus the black-box references
# compress/flate (stdlib), klauspost/compress and, optionally, ISA-L.
#
#   bench/gzip/compare.sh [-mode c|d|both] [-l "1 2 6 9 12"] [-f "FILE[:BLOCK]..."]
#       [-i SECS] [-g SECS] [-n RUNS] [-c CPU] [-libs "ld ours stdlib klauspost isal"]
#       [-format raw|gzip] [-noasm] [-quick] [-force] [-dry-run] [-prof DIR] [-o PATH] -label NAME
#
# Writes bench/results/<date>-gzip-<label>.md, or -o PATH (a relative PATH is
# relative to the repository root). Needs bash >= 4.4. See README.md.
# PREBUILT=DIR in the environment uses DIR/gzbench instead of go build.
set -euo pipefail
if ! { [ "${BASH_VERSINFO[0]}" -gt 4 ] || { [ "${BASH_VERSINFO[0]}" -eq 4 ] && [ "${BASH_VERSINFO[1]}" -ge 4 ]; }; }; then
  echo "bash >= 4.4 required" >&2; exit 2
fi
cd "$(dirname "$0")/../.."
ROOT=$PWD

LEVELS="0 1 2 3 4 5 6 7 8 9 10 11 12"
FILES=""
LABEL=""
CSECS=5   # ldbench / ISA-L -secs
GSECS=""  # gzbench -secs; defaults to -i
RUNS=3
MODE=both
CPU=2
LIBS="ld ours stdlib klauspost isal"
FORMAT=raw
NOASM=""
QUICK=""
FORCE=""
DRY=""
PROF=""
OUT=""
CORPUS=${CORPUS:-$ROOT/bench/corpus}
usage() {
  echo "usage: $0 [-mode c|d|both] [-l \"1 6 12\"] [-f \"FILE[:BLOCK]...\"] [-i SECS] [-g SECS] [-n RUNS] [-c CPU]" >&2
  echo "          [-libs \"ld ours stdlib klauspost isal\"] [-format raw|gzip] [-noasm] [-quick] [-force] [-dry-run] [-prof DIR] [-o PATH] -label NAME" >&2
  exit 2
}
while [ $# -gt 0 ]; do
  case "$1" in
    -mode) MODE=${2:?}; shift 2 ;;
    -l) LEVELS=${2:?}; shift 2 ;;
    -f) FILES=${2:?}; shift 2 ;;
    -i) CSECS=${2:?}; shift 2 ;;
    -g) GSECS=${2:?}; shift 2 ;;
    -n) RUNS=${2:?}; shift 2 ;;
    -c) CPU=${2:?}; shift 2 ;;
    -libs) LIBS=${2:?}; shift 2 ;;
    -format) FORMAT=${2:?}; shift 2 ;;
    -noasm) NOASM=1; shift ;;
    -quick) QUICK=1; shift ;;
    -force) FORCE=1; shift ;;
    -dry-run) DRY=1; shift ;;
    -prof) PROF=${2:?}; shift 2 ;;
    -o) OUT=${2:?}; shift 2 ;;
    -label) LABEL=${2:?}; shift 2 ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done
[ -n "$LABEL" ] || usage
[ -n "$GSECS" ] || GSECS=$CSECS
case "$MODE" in c) MODES="c" ;; d) MODES="d" ;; both) MODES="c d" ;; *) echo "-mode must be c, d or both" >&2; exit 2 ;; esac
case "$FORMAT" in raw|gzip) ;; *) echo "-format must be raw or gzip" >&2; exit 2 ;; esac
for v in "$CSECS" "$GSECS"; do [[ $v =~ ^[0-9]+([.][0-9]+)?$ ]] || { echo "bad seconds: $v" >&2; exit 2; }; done
[[ $RUNS =~ ^[1-9][0-9]*$ ]] || { echo "bad -n: $RUNS" >&2; exit 2; }
[[ $CPU =~ ^[0-9]+$ ]] || { echo "bad -c: $CPU" >&2; exit 2; }
for l in $LEVELS; do [[ $l =~ ^([0-9]|1[0-2])$ ]] || { echo "bad level: $l (0..12)" >&2; exit 2; }; done
for l in $LIBS; do case "$l" in ld|ours|stdlib|klauspost|isal) ;; *) echo "unknown lib: $l" >&2; exit 2 ;; esac; done
has_lib() { case " $LIBS " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }
if [ -n "$QUICK" ]; then
  [ -n "$FILES" ] || FILES="xml reymont sao"
  [ "$LEVELS" != "0 1 2 3 4 5 6 7 8 9 10 11 12" ] || LEVELS="1 6 12"
fi
# ISA-L: needs a gzbench_c-style binary and raw format (it times raw DEFLATE);
# otherwise the column is omitted, with the reason recorded in the header.
ISAL=""; ISALNOTE="not requested"
if has_lib isal; then
  if [ -z "${ISAL_BENCH:-}" ]; then ISALNOTE="omitted (ISAL_BENCH unset)"
  elif [ ! -x "$ISAL_BENCH" ]; then ISALNOTE="omitted (ISAL_BENCH not executable)"
  elif [ "$FORMAT" != raw ]; then ISALNOTE="omitted (-format $FORMAT; ISA-L column is raw only)"
  else ISAL=$ISAL_BENCH; ISALNOTE="$ISAL_BENCH (levels 0..3, whole files, raw)"; fi
fi

# ---- preflight --------------------------------------------------------------
# refuse NAME REASON: stop unless -force (or -dry-run, which only reports).
refuse() {
  if [ -n "$DRY" ]; then echo "notice (dry run, would refuse): $1" >&2; return; fi
  echo "$1" >&2
  [ -n "$FORCE" ] || { echo "refusing to run (use -force to override)" >&2; exit 1; }
}
expand_cpus() { # "2-3" or "2,3" -> "2 3"
  local out="" part
  for part in $(tr ',' ' ' <<<"$1"); do
    case "$part" in *-*) out+=" $(seq "${part%-*}" "${part#*-}" | tr '\n' ' ')" ;; *) out+=" $part" ;; esac
  done
  echo "$out"
}
cpu_busy() { # cpu -> busy percent over 0.5 s from /proc/stat
  local a b
  a=$(grep "^cpu$1 " /proc/stat); sleep 0.5; b=$(grep "^cpu$1 " /proc/stat)
  awk -v a="$a" -v b="$b" 'BEGIN{
    n=split(a,x," "); split(b,y," "); ta=tb=0
    for(i=2;i<=n;i++){ta+=x[i];tb+=y[i]}
    idle=(y[5]+y[6])-(x[5]+x[6]); d=tb-ta
    printf "%.0f", (d>0 ? 100*(d-idle)/d : 0) }'
}

AC=$(cat /sys/class/power_supply/AC0/online 2>/dev/null || echo unknown)
if [ "$AC" != 1 ]; then
  echo "!!!!! WARNING: AC power is not online (AC0/online=$AC); CPU frequency will not be stable !!!!!" >&2
  refuse "AC power not online"
fi
OTHER=$(pgrep -a -x 'gzbench|ldbench|gobench|zstd.test' || true)
OTHER+=$'\n'$(pgrep -af 'zstd.* -b[0-9]' || true)
OTHER=$(printf '%s' "$OTHER" | sed '/^$/d')
[ -z "$OTHER" ] || refuse "another benchmark/test process is running (zstd session?):"$'\n'"$OTHER"
LOAD_START=$(cut -d' ' -f1 /proc/loadavg)
if awk -v l="$LOAD_START" 'BEGIN{exit !(l+0 > 4)}'; then refuse "load1=$LOAD_START > 4"; fi

SIBS=""
SIBLIST=$(cat "/sys/devices/system/cpu/cpu$CPU/topology/thread_siblings_list" 2>/dev/null || echo "$CPU")
for s in $(expand_cpus "$SIBLIST"); do [ "$s" = "$CPU" ] || SIBS+="$s "; done
# Any go test / benchmark process currently running on the pinned CPU or its sibling.
CPUSET=" $CPU $SIBS"
BUSYPROCS=$(ps -eo pid=,psr=,pcpu=,comm= | awk -v set="$CPUSET" 'BEGIN{n=split(set,a," "); for(i=1;i<=n;i++) w[a[i]]=1}
  ($2 in w) && ($3+0 >= 10) && ($4 ~ /\.test$|^go$|zstd|ldbench|gzbench|gobench|ldref/) {print}' || true)
[ -z "$BUSYPROCS" ] || refuse "process(es) busy on CPU $CPU or its SMT sibling:"$'\n'"$BUSYPROCS"
SIBNOTE="none"
BUSYNOTE=""
CPUBUSY=$(cpu_busy "$CPU")
[ "$CPUBUSY" -le 20 ] || BUSYNOTE+=" WARNING: pinned CPU $CPU is ${CPUBUSY}% busy."
for s in $SIBS; do
  b=$(cpu_busy "$s")
  SIBNOTE="CPU $s (${b}% busy at start)"
  if [ "$b" -gt 20 ]; then BUSYNOTE+=" WARNING: SMT sibling CPU $s is ${b}% busy; it shares CPU $CPU's core."; fi
done
[ -z "$BUSYNOTE" ] || echo "$BUSYNOTE" >&2

[ -d ref/libdeflate ] || { echo "missing ref/libdeflate; run ref/fetch.sh" >&2; exit 1; }
# Always run make (a no-op when current): a stale ldbench that prints only two fields
# would silently fall back to the ratio-string comparison.
make -C ref/ldref >&2 || { echo "make -C ref/ldref failed" >&2; exit 1; }
LDBENCH=ref/ldref/ldbench

# ---- corpus specs -----------------------------------------------------------
SPECS=()
if [ -n "$FILES" ]; then
  for f in $FILES; do
    case "$f" in *:*) SPECS+=("$f") ;; *) SPECS+=("$f:0") ;; esac
  done
else
  for f in "$CORPUS"/silesia/*; do [ -f "$f" ] && SPECS+=("$f:0"); done
  if [ -f "$CORPUS/silesia.tar" ]; then
    SPECS+=("$CORPUS/silesia.tar:1048576" "$CORPUS/silesia.tar:131072" "$CORPUS/silesia.tar:16384")
  fi
fi
[ ${#SPECS[@]} -gt 0 ] || { echo "no corpus files under $CORPUS; run ref/fetch.sh (or set CORPUS=)" >&2; exit 1; }
RESOLVED=()
for spec in "${SPECS[@]}"; do
  path=${spec%:*}; blk=${spec##*:}
  [[ $blk =~ ^[0-9]+$ ]] || { echo "bad FILE:BLOCK '$spec' (BLOCK is a byte count, e.g. xml:1048576)" >&2; exit 2; }
  if [ ! -f "$path" ] && [ -f "$CORPUS/silesia/$path" ]; then path=$CORPUS/silesia/$path; fi
  [ -f "$path" ] || { echo "no such file: $path" >&2; exit 1; }
  RESOLVED+=("$path:$blk")
done
SPECS=("${RESOLVED[@]}")
blocklabel() { # bytes -> whole | 1M | 128K ...
  if [ "$1" = 0 ]; then echo whole
  elif [ $(($1 % 1048576)) = 0 ]; then echo "$(($1 / 1048576))M"
  elif [ $(($1 % 1024)) = 0 ]; then echo "$(($1 / 1024))K"
  else echo "${1}B"; fi
}
FILELIST=""
for spec in "${SPECS[@]}"; do FILELIST+="$(basename "${spec%:*}"):$(blocklabel "${spec##*:}") "; done

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# -mode d with a Go library writes the libdeflate streams of one row to $TMP/z.bin: about the size of the largest
# input (level 0 slightly more; 16K blocks of the 212 MB silesia.tar give ~13k blocks, ~212 MB). Set TMPDIR
# to a roomy filesystem if /tmp is small.
if { has_lib ours || has_lib stdlib || has_lib klauspost; } && [[ " $MODES " == *" d "* ]]; then
  BIGGEST=0
  for spec in "${SPECS[@]}"; do sz=$(stat -c %s "${spec%:*}"); [ "$sz" -le "$BIGGEST" ] || BIGGEST=$sz; done
  NEED=$((BIGGEST + BIGGEST / 50 + 1048576))
  FREE=$(df -P -B1 "$TMP" | awk 'NR==2{print $4}')
  if [ "$FREE" -lt "$NEED" ]; then
    echo "not enough free space in ${TMPDIR:-/tmp} for the stream dump: need ~$((NEED / 1048576)) MiB, have $((FREE / 1048576)) MiB (set TMPDIR to a bigger filesystem)" >&2
    exit 1
  fi
fi
if [ -n "$PROF" ]; then mkdir -p "$PROF"; PROF=$(cd "$PROF" && pwd); fi
BUILDTAGS=()
[ -z "$NOASM" ] || BUILDTAGS=(-tags noasm)
if [ -n "${PREBUILT:-}" ]; then
  # PREBUILT=DIR: use DIR/gzbench instead of building it (a machine without Go).
  [ -z "$NOASM" ] || { echo "-noasm needs a gzbench built with -tags noasm, not PREBUILT" >&2; exit 2; }
  cp "$PREBUILT/gzbench" "$TMP/gzbench"
else
  (cd bench/gzip && go build "${BUILDTAGS[@]}" -o "$TMP/gzbench" .)
fi
GZ=$TMP/gzbench
ASMLABEL="on (default build)"
[ -z "$NOASM" ] || ASMLABEL="OFF (ours built with -tags noasm; reported only, not a gate)"
median() { "$GZ" stat median; }

# One cell = one run of one library: prints "MBps ratio [bytes]" on stdout
# (stderr is kept apart); the return code is the tool's. gzbench codes: 3 not
# ported, 4 unsupported level.
run_cell() { # lib mode level block file [cpuprofile-path (ours only)]
  local lib=$1 mode=$2 lvl=$3 blk=$4 file=$5 prof=${6:-} rc=0 bargs=() cmd
  [ "$blk" = 0 ] || bargs=(-block "$blk")
  case "$lib" in
    ld) cmd=("$LDBENCH" -mode "$mode" -level "$lvl" -secs "$CSECS" "${bargs[@]}" -format "$FORMAT" "$file") ;;
    ours|stdlib|klauspost)
      # -mode d: every Go library decodes the libdeflate streams dumped by dump_z
      # (the same bytes the C side decodes); -mode c compresses itself.
      [ "$mode" != d ] || bargs+=(-zin "$TMP/z.bin")
      [ -z "$prof" ] || bargs+=(-cpuprofile "$prof")
      cmd=("$GZ" -lib "$lib" -mode "$mode" -level "$lvl" -secs "$GSECS" "${bargs[@]}" -format "$FORMAT" "$file") ;;
    isal) cmd=("$ISAL" -lib isal -mode "$mode" -level "$lvl" -secs "$CSECS" "$file") ;;
  esac
  if [ -n "$DRY" ]; then
    # dry run mirrors ours' real coverage: -mode c and d, raw and gzip, levels 0..12 (nothing exits 3 or 4)
    # first two commands per lib and mode
    local dc=$TMP/dryn.$lib${prof:+.prof}.$mode
    [ -f "$dc" ] || : > "$dc"
    if [ "$(wc -l < "$dc")" -lt 2 ]; then echo "DRY: taskset -c $CPU ${cmd[*]}" >&2; echo >> "$dc"; fi
    echo "100.0 1.000 1000"; return 0
  fi
  taskset -c "$CPU" "${cmd[@]}" 2>"$TMP/err" || rc=$?
  [ "$rc" = 0 ] || cat "$TMP/err" >&2
  return "$rc"
}
# dump_z level block file: the libdeflate streams for one row, for ours -mode d.
dump_z() {
  local bargs=()
  [ "$2" = 0 ] || bargs=(-block "$2")
  [ -z "$DRY" ] || return 0
  "$LDBENCH" -level "$1" "${bargs[@]}" -format "$FORMAT" -dumpz "$TMP/z.bin" "$3" || { echo "ldbench -dumpz failed on $3 L$1" >&2; exit 1; }
}
# applicable lib level block: stdlib/klauspost run levels 1..9; isal 0..3, whole files only
applicable() {
  case "$1" in
    stdlib|klauspost) [ "$2" -ge 1 ] && [ "$2" -le 9 ] ;;
    isal) [ "$2" -le 3 ] && [ "$3" = 0 ] ;;
    *) true ;;
  esac
}

BODY=$TMP/body.md
ROWS=$TMP/rows.tsv
: > "$BODY"; : > "$ROWS"
OURS_NP_MODES=""    # modes where ours exited 3 (not ported; none at present): ours skipped for that mode
OURS_NA_LEVELS=""   # levels where ours exited 4 (deduplicated)
COLS="| file | level | block | C MB/s | Go MB/s | % of C | C ratio | Go ratio | stdlib MB/s | klauspost MB/s | stdlib ratio | klauspost ratio |"
SEP="|---|---|---|---|---|---|---|---|---|---|---|---|"
if [ -n "$ISAL" ]; then COLS+=" isal MB/s |"; SEP+="---|"; fi
SIZECMP=exact   # becomes "ratio" if a producer printed no size

for mode in $MODES; do
  if [ "$mode" = c ]; then
    { echo; echo "## Compression"; echo; echo "$COLS"; echo "$SEP"; } >> "$BODY"
  else
    { echo; echo "## Decompression"; echo; echo "${COLS//MB\/s/D-MB\/s}"; echo "$SEP"; } >> "$BODY"
  fi
  for spec in "${SPECS[@]}"; do
    f=${spec%:*}; blk=${spec##*:}
    bname=$(basename "$f"); blabel=$(blocklabel "$blk")
    for lvl in $LEVELS; do
      echo "== $mode $bname L$lvl block=$blabel" >&2
      declare -A RAT=() SZ=() SPEEDS=()
      for lib in ld ours stdlib klauspost isal; do RAT[$lib]=""; SZ[$lib]=""; SPEEDS[$lib]=""; done
      ours_na=""
      case " $OURS_NP_MODES " in *" $mode "*) ours_np=1 ;; *) ours_np="" ;; esac
      if [ "$mode" = d ] && { has_lib ours || has_lib stdlib || has_lib klauspost; }; then dump_z "$lvl" "$blk" "$f"; fi
      for ((r = 0; r < RUNS; r++)); do
        for lib in ld ours stdlib klauspost isal; do
          has_lib "$lib" || continue
          [ "$lib" != isal ] || [ -n "$ISAL" ] || continue
          [ "$lib" != ours ] || { [ -z "$ours_np" ] && [ -z "$ours_na" ]; } || continue
          applicable "$lib" "$lvl" "$blk" || continue
          rc=0; out=$(run_cell "$lib" "$mode" "$lvl" "$blk" "$f") || rc=$?
          if [ "$lib" = ours ] && [ "$rc" = 3 ]; then
            ours_np=1; OURS_NP_MODES+=" $mode"; echo "ours: -mode $mode -format $FORMAT not ported yet (ours skipped for this mode)" >&2; continue
          fi
          if [ "$lib" = ours ] && [ "$rc" = 4 ]; then
            ours_na=1; echo "ours: level $lvl not ported (n/a)" >&2
            case " $OURS_NA_LEVELS " in *" $lvl "*) ;; *) OURS_NA_LEVELS+=" $lvl" ;; esac
            continue
          fi
          if [ "$rc" != 0 ]; then echo "$lib failed (rc=$rc) on $bname L$lvl" >&2; exit 1; fi
          read -r mbs ratio size <<<"$out"
          [ -n "${mbs:-}" ] || { echo "could not parse $lib output: $out" >&2; exit 1; }
          [ "$lib" = isal ] || [ -n "${ratio:-}" ] || { echo "could not parse $lib output: $out" >&2; exit 1; }
          SPEEDS[$lib]+="$mbs"$'\n'; RAT[$lib]=${ratio:-}; SZ[$lib]=${size:-}
        done
      done
      # One extra, untimed-for-the-table profiled run per ours row, AFTER the median runs so it never perturbs them.
      if [ -n "$PROF" ] && has_lib ours && [ -z "$ours_np" ] && [ -z "$ours_na" ]; then
        pf="$PROF/$bname.$blabel.L$lvl.$mode.cpu.prof"
        run_cell ours "$mode" "$lvl" "$blk" "$f" "$pf" >/dev/null || echo "warning: profiled run failed for $bname L$lvl $mode" >&2
        [ -n "$DRY" ] || echo "profile: $pf" >&2
      fi
      med() { if [ -n "${SPEEDS[$1]}" ]; then printf '%s' "${SPEEDS[$1]}" | median; else echo "-"; fi; }
      cmed=$(med ld); gmed=$(med ours); smed=$(med stdlib); kmed=$(med klauspost); imed=$(med isal)
      { [ -z "$ours_na" ] && [ -z "$ours_np" ]; } || gmed="n/a (not ported)"
      pct="-"; flag=""
      if [ "$cmed" != - ] && [ "$gmed" != - ] && [ -z "$ours_na" ] && [ -z "$ours_np" ]; then
        pct=$(awk -v g="$gmed" -v c="$cmed" 'BEGIN{printf "%.1f", 100*g/c}')
        status=ok
        if [ -n "${SZ[ld]}" ] && [ -n "${SZ[ours]}" ]; then
          [ "${SZ[ld]}" = "${SZ[ours]}" ] || { status=mismatch; flag=" MISMATCH (${SZ[ld]} vs ${SZ[ours]} bytes)"; }
        else
          SIZECMP=ratio
          [ "${RAT[ld]}" = "${RAT[ours]}" ] || { status=mismatch; flag=" MISMATCH (ratio)"; }
        fi
        printf '%s\t%s\t%s\t%s\t%s\n' "$mode" "$lvl" "$bname/$blabel" "$pct" "$status" >> "$ROWS"
        pct="$pct%"
      fi
      [ -n "${RAT[ld]}" ] || RAT[ld]="-"
      [ -n "${RAT[ours]}" ] || RAT[ours]="-"
      # mode d: ours decodes C's streams, so its ratio is C's by construction, not a measurement
      if [ "$mode" = d ] && [ "${RAT[ours]}" != - ]; then RAT[ours]="=C (same stream)"; fi
      line="| $bname | $lvl | $blabel | $cmed | $gmed | $pct | ${RAT[ld]} | ${RAT[ours]}$flag | $smed | $kmed | ${RAT[stdlib]:--} | ${RAT[klauspost]:--} |"
      if [ -n "$ISAL" ]; then line+=" $imed |"; fi
      echo "$line" >> "$BODY"
      unset RAT SZ SPEEDS
    done
  done
done

# ---- header and summary -------------------------------------------------------
LOAD_END=$(cut -d' ' -f1 /proc/loadavg)
GITREV=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIRTY=clean
if ! git diff-index --quiet HEAD -- 2>/dev/null; then DIRTY=dirty; fi
if git ls-files --others --exclude-standard | grep -Evq '^(\.omc/|bench/results/|\.superpowers/)'; then DIRTY+="+untracked"; fi
LDTAG=$(git -C ref/libdeflate describe --tags --always 2>/dev/null || echo unknown)
LDVER=$(ref/ldref/ldref version 2>/dev/null | tr '\n' ' ' || true)
CCV=$(${CC:-cc} --version 2>/dev/null | head -1 || echo unknown)
CCFLAGS=$(make -C ref/ldref -n -B ldbench 2>/dev/null | grep -m1 -E '^(cc|gcc|clang|[^ ]*/cc)' | sed -E 's/ -o ldbench.*//; s/^[^ ]+ //' || true)
CPUMODEL=$(grep -m1 'model name' /proc/cpuinfo | sed 's/.*: //')
OUT=${OUT:-bench/results/$(date +%F)-gzip-$LABEL.md}
mkdir -p "$(dirname "$OUT")"
{
  echo "# gzip $LABEL"
  echo
  [ -z "$DRY" ] || echo "- **DRY RUN: no benchmark was executed; the numbers below are placeholders and there is no gate summary**"
  echo "- date: $(date -Is); host: $(uname -n); git: $GITREV ($DIRTY)"
  echo "- cpu: $CPUMODEL; pinned to CPU $CPU (taskset); SMT sibling: $SIBNOTE${BUSYNOTE:+;$BUSYNOTE}"
  echo "- load1 at start: $LOAD_START, at end: $LOAD_END; AC power online: $AC"
  echo "- C: libdeflate $LDTAG (ref/libdeflate); \`ldref version\`: $LDVER"
  echo "- C compiler: $CCV; flags (from \`make -n -B ldbench\` in ref/ldref; runtime asm dispatch on; impl above is from the sibling \`ldref\` binary built from the same sources): \`${CCFLAGS:-unknown}\`"
  # With PREBUILT (no Go toolchain) GO_VERSION and KP_VERSION may name what gzbench was built with.
  GOV=${GO_VERSION:-$(go version 2>/dev/null || echo unknown)}
  KPV=${KP_VERSION:-$(cd bench/gzip && go list -m -f '{{.Version}}' github.com/klauspost/compress 2>/dev/null || echo unknown)}
  echo "- Go: $GOV; ours asm: $ASMLABEL; stdlib = compress/flate+gzip (unaffected by -tags noasm), klauspost = github.com/klauspost/compress@$KPV (unaffected by -tags noasm)"
  echo "- quick: $([ -n "$QUICK" ] && echo yes || echo no); files: $FILELIST"
  echo "- settings: -i $CSECS (C and ISA-L), -g $GSECS (Go), -n $RUNS (median, interleaved), -format $FORMAT, -mode $MODE, libs: $LIBS, levels: $LEVELS; isal: $ISALNOTE"
  echo "- identity check (-mode c): $([ "$SIZECMP" = exact ] && echo 'exact compressed byte count (third field of ldbench/gzbench)' || echo 'ratio string only for rows where a producer printed no size')"
  [[ " $MODES " == *" d "* ]] && has_lib ours && echo "- identity check (-mode d): none by size (ours decodes the C streams, so the Go ratio cell is \`=C\`, not a measurement); integrity = the round trip verified by gzbench before timing (exact-length decode of every block, then byte equality with the input)"
  [ -z "$PROF" ] || echo "- CPU profiles of the ours cells (one extra run per row after the timed runs): $PROF/<file>.<block>.L<level>.<mode>.cpu.prof"
  echo "- timing: windows of ~1 s sized by loops, window mean per pass, windows < 0.5 s discarded, best window; MB/s = 1e6 bytes of uncompressed data per second; -block B = independent B-byte blocks on one reused (de)compressor"
  echo "- stdlib/klauspost: same numeric level for 1..9; \`-\` for levels 0 and 10..12 (no equivalent); ISA-L: levels 0..3, whole files"
  if has_lib ours; then
    echo "- ours compresses and decompresses (raw and gzip, levels 0..12): for -mode c ours compresses the input itself (identity = exact byte count against C); for -mode d the compressed input is the libdeflate stream of the same level and blocks (ldbench -dumpz), decoded by C and by every Go library; the round trip is verified before timing"
  fi
  [ -z "$OURS_NP_MODES" ] || echo "- **ours not ported for -mode${OURS_NP_MODES} -format $FORMAT: those Go cells are n/a and that gate is skipped (no rows)**"
  [ -z "$OURS_NA_LEVELS" ] || echo "- ours not ported (n/a) at levels:$OURS_NA_LEVELS"
  cat "$BODY"
  echo
  {
    SUMARGS=(-levels "$LEVELS" -modes "$MODES")
    [ -z "$DRY" ] || SUMARGS+=(-dry)
    for m in $MODES; do
      if ! has_lib ours; then why="ours not in -libs; no ours-vs-C rows"
      elif [[ " $OURS_NP_MODES " == *" $m "* ]]; then why="ours not ported for -mode $m -format $FORMAT; no ours-vs-C rows"
      else why="no ours-vs-C rows"; fi
      SUMARGS+=("-why-$m" "$why")
    done
    [ -z "$NOASM" ] || SUMARGS+=(-info)
    "$GZ" stat summary "${SUMARGS[@]}" < "$ROWS"
  }
} > "$OUT"
echo "wrote $OUT" >&2
cat "$OUT"
