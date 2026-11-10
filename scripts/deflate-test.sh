#!/usr/bin/env bash
# deflate-test.sh - DEFLATE test runs under the mandated memory cap, one at a time (see
# scripts/README-deflate-tests.md).
#
#   deflate-test.sh [-t quick|full] [-a MODES] [-r REGEX] [-p N] [-l LOGDIR] [-c] [-n] [GROUP...] [-- GO-TEST-ARGS...]
#
#   -t quick  tier 0 (every change, default; "fast" is an alias): -short, FASTCOMPRESS_TEST_TIER=quick (adds
#             1 MiB / 512 KiB prefixes of 7 more Silesia files to the level differential tests). Normally
#             with -r from scripts/deflate-groups.sh (only the tests of the changed files).
#   -t full   tier 1 (end of a round, CI): no -short, the full differential matrix
#   -a MODES  FASTCOMPRESS_FUZZ_ASM for the internal/deflate runs, comma-separated: on (default), off,
#             nobmi2; both = on,off
#   -r REGEX  internal/deflate: run the tests matching REGEX (-run) instead of the GROUPs' partitions
#   -p N      FASTCOMPRESS_TEST_PAR and go test -parallel (default $FASTCOMPRESS_TEST_PAR or 8; 1 = serial)
#   -l DIR    log directory (default ${TMPDIR:-/tmp}/fastcompress-deflate-test/<date-time>)
#   -c        also write a coverage profile (internal/deflate runs: DIR/<run>.cover)
#   -n        print the full flock + systemd-run commands only
#   GROUP     deflate (default: the whole internal/deflate package), or internal/deflate partitions
#             (lazy nopt rest equiv dec other: scripts/deflate-groups.sh -list; the ones given are combined
#             into ONE go test per asm mode), noasm (internal/deflate -tags noasm, always -short as in the
#             rounds; CI runs it in full), cover (internal/deflate -short -coverprofile, asm on), flate,
#             gzip, zlib (those packages; -short in tier quick), creftest (internal/creftest's libdeflate driver
#             and ldref cache tests, ^TestLD)
#
# Every go test is
#   flock /tmp/gzip-libdeflate-test.lock systemd-run --user --scope -p MemoryMax=$MEMMAX -p MemorySwapMax=0 \
#     env GOMEMLIMIT=$GOMEMLIMIT ... go test -p 1 -count=1 -timeout 60m -v -parallel N ...
# with MEMMAX = FASTCOMPRESS_TEST_MEM_MAX (default 6G; N, NK, NM or NG, at least 256M, at most 8G) and
# GOMEMLIMIT = FASTCOMPRESS_TEST_GOMEMLIMIT (default 4GiB; N followed by B, KiB, MiB or GiB, nonzero and below
# MEMMAX), leading zeros dropped; any other value (empty, infinity, off, ...) is an error. It runs in the
# foreground after waiting (up to 6 h; then the run is aborted with rc 75, not started) until MemAvailable >= MEMMAX + 2 GiB and no other test
# binary uses more than 3 GB; the same check is made again once the lock is taken (up to 10 min, then
# abort with rc 75). The wall time (from taking the lock), maxrss (the largest process: the
# test binary or the compiler; /usr/bin/time), the scope's memory.peak (all processes, page cache
# included, so it reaches the cap on big file reads without any OOM) and the scope's oom_kill count go
# to DIR/summary.txt. Every line of go test's output is
# prefixed with its Unix time (a parent's "--- PASS (Ns)" excludes its parallel subtests, so
# scripts/deflate-timing.sh DIR/*.log takes the per-test wall time from the timestamps).
set -uo pipefail
cd "$(dirname "$0")/.."
R=$PWD
LOCK=/tmp/gzip-libdeflate-test.lock

tier=quick asm=on run_re="" par=${FASTCOMPRESS_TEST_PAR:-8} logdir="" cover=0 dry=0
# Memory cap of the scope and Go soft limit (FASTCOMPRESS_TEST_MEM_MAX, FASTCOMPRESS_TEST_GOMEMLIMIT); a set but
# empty variable is rejected below, not defaulted.
MEMMAX=${FASTCOMPRESS_TEST_MEM_MAX-6G} GOMEMLIM=${FASTCOMPRESS_TEST_GOMEMLIMIT-4GiB}
# size_bytes VALUE UNITS...: VALUE = digits + one of UNITS ("" = bytes); prints the byte count, or nothing
# when the number exceeds 8 GiB in that unit (checked before shifting, so no overflow).
size_bytes() {
  local v=$1 n u sh; shift
  for u in "$@"; do
    n=${v%"$u"}
    [ -n "$u" ] && [ "$n" = "$v" ] && continue
    [[ $n =~ ^[0-9]{1,11}$ ]] || continue
    case "$u" in K|KiB) sh=10 ;; M|MiB) sh=20 ;; G|GiB) sh=30 ;; *) sh=0 ;; esac
    n=$((10#$n))
    [ "$n" -le $(( (8 << 30) >> sh )) ] && echo $((n << sh))
    return 0
  done
}
memmax_b="" gomemlim_b=""
[[ $MEMMAX =~ ^[0-9]+[KMG]?$ ]] && memmax_b=$(size_bytes "$MEMMAX" K M G "")
if [ -z "$memmax_b" ] || [ "$memmax_b" -lt $((256 << 20)) ]; then
  echo "FASTCOMPRESS_TEST_MEM_MAX='$MEMMAX': want N, NK, NM or NG, at least 256M and at most 8G" >&2; exit 2
fi
[[ $GOMEMLIM =~ ^[0-9]+(B|KiB|MiB|GiB)$ ]] && gomemlim_b=$(size_bytes "$GOMEMLIM" KiB MiB GiB B)
if [ -z "$gomemlim_b" ] || [ "$gomemlim_b" -eq 0 ] || [ "$gomemlim_b" -ge "$memmax_b" ]; then
  echo "FASTCOMPRESS_TEST_GOMEMLIMIT='$GOMEMLIM': want N followed by B, KiB, MiB or GiB, nonzero and below MemoryMax=$MEMMAX" >&2; exit 2
fi
# Strip leading zeros (validated above: digits then the unit) so systemd-run and Go get plain numbers.
n=${MEMMAX%%[!0-9]*}; MEMMAX=$((10#$n))${MEMMAX#"$n"}
n=${GOMEMLIM%%[!0-9]*}; GOMEMLIM=$((10#$n))${GOMEMLIM#"$n"}
# Memory that must be available before a run (kB, as /proc/meminfo): the cap plus 2 GiB.
NEED_KB=$(( memmax_b / 1024 + 2 * 1024 * 1024 ))
groups=() extra=()
while [ $# -gt 0 ]; do
  case "$1" in
    -t) tier=$2; shift ;;
    -a) asm=$2; shift ;;
    -r) run_re=$2; shift ;;
    -p) par=$2; shift ;;
    -l) logdir=$2; shift ;;
    -c) cover=1 ;;
    -n) dry=1 ;;
    -h|--help) sed -n '2,/^set -uo/p' "$0" | sed '$d'; exit 0 ;;
    --) shift; extra=("$@"); break ;;
    -*) echo "unknown flag $1" >&2; exit 2 ;;
    *) groups+=("$1") ;;
  esac
  shift
done
case "$tier" in quick|fast) tier=quick ;; full) ;; *) echo "-t quick|full" >&2; exit 2 ;; esac
[ "$asm" = both ] && asm=on,off
modes=${asm//,/ }
for m in $modes; do case "$m" in on|off|nobmi2) ;; *) echo "-a on|off|nobmi2[,...]|both" >&2; exit 2 ;; esac; done
case "$par" in ''|*[!0-9]*) echo "-p N" >&2; exit 2 ;; esac
[ "$par" -ge 1 ] || par=1
[ ${#groups[@]} -gt 0 ] || [ -n "$run_re" ] || groups=(deflate)
logdir=${logdir:-${TMPDIR:-/tmp}/fastcompress-deflate-test/$(date +%Y%m%d-%H%M%S)}
[ "$dry" = 1 ] || mkdir -p "$logdir"
G="$R/scripts/deflate-groups.sh"

short=()
[ "$tier" = quick ] && short=(-short)

# memok: 0 if MemAvailable >= NEED_KB and no test binary uses more than 3 GB; prints the state.
# (The same check runs inside the locked command, as CHECK below.)
CHECK='av=$(awk "/^MemAvailable:/{print \$2}" /proc/meminfo)
  big=$(ps -eo rss=,comm= | awk "\$2 ~ /\\.test\$/ && \$1 > 3000000" | wc -l)
  echo "available $((av >> 20))G (need $((NEED_KB >> 20))G), test binaries > 3 GB: $big"
  [ "$av" -ge "$NEED_KB" ] && [ "$big" -eq 0 ]'
memok() { NEED_KB=$NEED_KB bash -c "$CHECK"; }

# capped(): wait for memory, then one capped go test; args: NAME ENV... -- GO-TEST-ARGS...
# fails counts the failed runs, aborts those of them aborted for memory (rc 75, nothing tested).
fails=0 aborts=0
capped() {
  local name=$1; shift
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  local log=$logdir/$name.log
  local cmd=(flock "$LOCK" systemd-run --user --scope -q -p MemoryMax="$MEMMAX" -p MemorySwapMax=0
    env GOMEMLIMIT="$GOMEMLIM" FASTCOMPRESS_TEST_PAR="$par" TIMEF="$log.time" NEED_KB="$NEED_KB" "${envs[@]}"
    bash -c "$INNER" "$par" "$@")
  if [ "$dry" = 1 ]; then
    printf '%q ' "${cmd[@]}"; echo
    return 0
  fi
  local i st ok=0
  for i in $(seq 1 720); do
    if st=$(memok); then ok=1; break; fi
    echo "$(date +%T) $name: waiting ($st)"
    sleep 30
  done
  if [ "$ok" = 0 ]; then
    echo "$(date '+%F %T') $name tier=$tier par=$par MemoryMax=$MEMMAX GOMEMLIMIT=$GOMEMLIM rc=75 ABORTED (memory wait timed out: $st)" | tee -a "$logdir/summary.txt"
    fails=$((fails+1)) aborts=$((aborts+1))
    return 0
  fi
  { echo "# $name: tier=$tier par=$par MemoryMax=$MEMMAX GOMEMLIMIT=$GOMEMLIM ${envs[*]} go test $*"; uptime; free -g | head -2; } >"$log"
  echo "$(date +%T) $name: start (log $log)"
  "${cmd[@]}" >>"$log" 2>&1
  local rc=$?
  local res
  res=$(grep '^RESULT ' "$log" | tail -1)
  [ -n "$res" ] || res="RESULT rc=$rc (no result line)"
  echo "$(date '+%F %T') $name tier=$tier par=$par MemoryMax=$MEMMAX GOMEMLIMIT=$GOMEMLIM ${res#RESULT }" | tee -a "$logdir/summary.txt"
  sed 's/^[0-9]* //' "$log" | grep -E '^(--- FAIL|FAIL|ok|panic:)' | tail -5
  [ "$rc" = 0 ] || fails=$((fails+1))
  case "$res" in *ABORTED*) aborts=$((aborts+1)) ;; esac
  return 0
}

# The command run under the lock and in the scope: the memory check again (up to 10 min, else rc 75
# without running), then go test with the timing, maxrss, memory.peak and oom_kill summary.
INNER='for i in $(seq 1 20); do
        st=$('"$CHECK"') && break
        [ "$i" = 20 ] && { echo "RESULT rc=75 ABORTED after the lock (memory: $st)"; exit 75; }
        echo "$(date +%T) after the lock: waiting ($st)"; sleep 30
      done
      t0=$(date +%s.%N)
      /usr/bin/time -o "$TIMEF" -f "%M" go test -p 1 -count=1 -timeout 60m -v -parallel "$0" "$@" 2>&1 | gawk "{ print systime() \" \" \$0; fflush() }"
      rc=${PIPESTATUS[0]}
      t1=$(date +%s.%N)
      cg=$(sed -n "s/^0:://p" /proc/self/cgroup)
      oom=$(awk "/^oom_kill /{print \$2}" "/sys/fs/cgroup$cg/memory.events" 2>/dev/null)
      echo "RESULT rc=$rc wall=$(echo "$t1 - $t0" | bc)s maxrss=$(( $(tail -1 "$TIMEF" 2>/dev/null || echo 0) / 1024 ))MiB memory.peak=$(( $(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null || echo 0) >> 20 ))MiB oom_kill=${oom:-?}"
      exit $rc'

parts=() pkgs=()
for g in "${groups[@]}"; do
  case "$g" in
    lazy|nopt|rest|equiv|dec|other) parts+=("$g") ;;
    deflate) parts+=(lazy nopt rest equiv dec other) ;;
    noasm|cover|creftest) ;;
    flate|gzip|zlib) pkgs+=("$g") ;;
    *) echo "unknown group $g" >&2; exit 2 ;;
  esac
done

# internal/deflate: one run per mode over the union of the partitions (or -r).
if [ -n "$run_re" ]; then
  for m in $modes; do
    cov=()
    [ "$cover" = 1 ] && cov=(-coverprofile="$logdir/sel-$tier-$m.cover")
    capped "sel-$tier-$m" FASTCOMPRESS_FUZZ_ASM="$m" FASTCOMPRESS_TEST_TIER="$tier" -- \
      "${short[@]}" -run "$run_re" "${cov[@]}" "${extra[@]}" ./internal/deflate
  done
elif [ ${#parts[@]} -gt 0 ]; then
  mapfile -t parts < <(printf '%s\n' "${parts[@]}" | sort -u)
  sel=() name=""
  has_other=0
  for p in "${parts[@]}"; do [ "$p" = other ] && has_other=1; name="$name+$p"; done
  name=${name#+}
  [ ${#parts[@]} -eq 6 ] && name=deflate
  if [ "$has_other" = 1 ]; then
    skip=""
    for p in lazy nopt rest equiv dec; do
      case " ${parts[*]} " in *" $p "*) ;; *) skip="$skip|$("$G" -regex "$p")" ;; esac
    done
    [ -n "$skip" ] && sel=(-skip "${skip#|}")
  else
    run=""
    for p in "${parts[@]}"; do run="$run|$("$G" -regex "$p")"; done
    sel=(-run "${run#|}")
  fi
  for m in $modes; do
    cov=()
    [ "$cover" = 1 ] && cov=(-coverprofile="$logdir/$name-$tier-$m.cover")
    capped "$name-$tier-$m" FASTCOMPRESS_FUZZ_ASM="$m" FASTCOMPRESS_TEST_TIER="$tier" -- \
      "${short[@]}" "${sel[@]}" "${cov[@]}" "${extra[@]}" ./internal/deflate
  done
fi
for g in "${groups[@]}"; do
  case "$g" in
    noasm) capped "noasm-$tier" FASTCOMPRESS_TEST_TIER="$tier" -- -short -tags noasm "${extra[@]}" ./internal/deflate ;;
    cover) capped "cover" FASTCOMPRESS_FUZZ_ASM=on -- -short -coverprofile="$logdir/cover.out" "${extra[@]}" ./internal/deflate ;;
    creftest) capped "creftest-$tier" -- "${short[@]}" -run '^TestLD' "${extra[@]}" ./internal/creftest ;;
  esac
done
for p in "${pkgs[@]}"; do
  capped "$p-$tier" FASTCOMPRESS_TEST_TIER="$tier" -- "${short[@]}" "${extra[@]}" "./$p"
done
echo "logs: $logdir (summary.txt); failures: $fails (memory aborts: $aborts)"
# rc 0 all passed, 75 only memory aborts (retry later), 1 a test failure.
[ "$fails" = 0 ] && exit 0
[ "$fails" = "$aborts" ] && exit 75
exit 1
