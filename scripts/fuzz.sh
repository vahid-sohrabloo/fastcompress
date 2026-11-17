#!/usr/bin/env bash
# fuzz.sh [-t DURATION] [-p PKG]...
#
# Runs every Fuzz* target (found with go test -list '^Fuzz') of each package
# for -fuzztime DURATION (default 30s), one target at a time. -p limits the
# run to the given packages (repeatable; directories or import paths; default
# ./...; .github/workflows/fuzz.yml passes one suite's import paths).
# FuzzTrainDict runs at most 60s (when -t parses to more): each of its execs
# trains a dictionary, in Go and in cref. Differential targets need the C
# drivers built: make -C ref/cref (zstd) and make -C ref/ldref (flate, gzip,
# zlib, internal/deflate). A package with Fuzz targets must have a
# fuzzasm_test.go (the FASTCOMPRESS_FUZZ_ASM hook; flate, gzip, zlib and
# internal/adler32 have one although only internal/deflate, which they
# call, has assembly), and a package whose test binary fails to build or
# list counts as failed.
#
# Every go test runs with -parallel 2 -fuzzminimizetime 2s. Outside CI (CI
# unset) and when systemd-run exists, each also runs in a scope capped at
# MemoryMax=4G, no swap, GOMEMLIMIT=2GiB; FASTCOMPRESS_FUZZ_CAP=0 turns the cap
# off, FASTCOMPRESS_FUZZ_CAP=1 requires it.
#
# FASTCOMPRESS_FUZZ_ASM=off|nobmi2|on is passed through to the tests, whose
# init switches the assembly routines accordingly (internal/cpu SetAsmMode).
#
# Exits non-zero when a target fails, after running the remaining ones, and
# prints the testdata/fuzz path of each failing input found.
set -euo pipefail

# Fuzz inputs are one-use: keep them out of the persistent libdeflate cache.
export FASTCOMPRESS_LDREF_CACHE=off

cd "$(dirname "$0")/.."

fuzztime=30s
pkgs=()
while getopts t:p: opt; do
  case $opt in
    t) fuzztime=$OPTARG ;;
    p) pkgs+=("$OPTARG") ;;
    *)
      echo "usage: $0 [-t DURATION] [-p PKG]..." >&2
      exit 2
      ;;
  esac
done
if [[ ${#pkgs[@]} == 0 ]]; then
  pkgs=(./...)
fi

cap=()
case ${FASTCOMPRESS_FUZZ_CAP:-} in
  0) ;;
  1) cap=(systemd-run --user --scope --quiet -p MemoryMax=4G -p MemorySwapMax=0 env GOMEMLIMIT=2GiB) ;;
  *)
    if [[ -z ${CI:-} ]] && command -v systemd-run >/dev/null; then
      cap=(systemd-run --user --scope --quiet -p MemoryMax=4G -p MemorySwapMax=0 env GOMEMLIMIT=2GiB)
    fi
    ;;
esac

gotest() {
  ${cap[@]+"${cap[@]}"} go test -p 1 "$@"
}

# seconds DURATION prints DURATION in seconds when it is a whole number of
# hours, minutes and seconds (e.g. 30s, 10m, 1m30s), and nothing otherwise
# (e.g. 500ms, 1000x).
seconds() {
  if [[ $1 =~ ^([0-9]+h)?([0-9]+m)?([0-9]+s)?$ && -n $1 ]]; then
    local h=${BASH_REMATCH[1]%h} m=${BASH_REMATCH[2]%m} s=${BASH_REMATCH[3]%s}
    echo $((10#${h:-0} * 3600 + 10#${m:-0} * 60 + 10#${s:-0}))
  fi
}

log=$(mktemp)
trap 'rm -f "$log"' EXIT
failed=()
ntargets=0
for pkg in $(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' "${pkgs[@]}"); do
  dir=$(go list -f '{{.Dir}}' "$pkg")
  if ! out=$(gotest -list '^Fuzz' "$pkg" 2>&1); then
    printf '%s\n' "$out" >&2
    failed+=("$pkg (go test -list)")
    continue
  fi
  targets=$(grep '^Fuzz' <<<"$out" || true)
  # The FASTCOMPRESS_FUZZ_ASM hook: without it the asm modes do nothing here.
  if [[ -n $targets && ! -f $dir/fuzzasm_test.go ]]; then
    echo "fuzz.sh: $pkg has Fuzz targets but no fuzzasm_test.go" >&2
    failed+=("$pkg (no fuzzasm_test.go)")
  fi
  for target in $targets; do
    t=$fuzztime
    if [[ $target == FuzzTrainDict ]]; then
      s=$(seconds "$t")
      if [[ -n $s && $s -gt 60 ]]; then
        t=60s
      fi
    fi
    ntargets=$((ntargets + 1))
    echo "=== $pkg $target ($t, FASTCOMPRESS_FUZZ_ASM=${FASTCOMPRESS_FUZZ_ASM:-on})"
    ok=0
    for try in 1 2; do
      if gotest -run '^$' -fuzz "^${target}\$" -fuzztime "$t" -parallel 2 -fuzzminimizetime 2s "$pkg" 2>&1 | tee "$log"; then
        ok=1
        break
      fi
      # The Go fuzzer sometimes ends a run with only "context deadline
      # exceeded" when -fuzztime runs out while it is still handling a new
      # input: no failing input, no panic. That is the toolchain, not the
      # target: run once more, and only warn if it happens again.
      if grep -q 'context deadline exceeded' "$log" && ! grep -qE 'Failing input written|panic:' "$log"; then
        if [[ $try == 2 ]]; then
          echo "fuzz.sh: $target: fuzzer deadline flake twice, no failing input (warning only)" >&2
          ok=1
        else
          echo "fuzz.sh: $target: fuzzer deadline flake, no failing input; running again" >&2
        fi
        continue
      fi
      break
    done
    if [[ $ok == 0 ]]; then
      failed+=("$pkg $target")
      # go test prints "Failing input written to testdata/fuzz/<target>/<id>".
      sed -n 's|.*Failing input written to \(testdata/fuzz/[^ ]*\).*|\1|p' "$log" | while IFS= read -r p; do
        echo "fuzz.sh: $target: failing input: ${dir#"$PWD"/}/$p"
      done
    fi
  done
done

if [[ ${#failed[@]} != 0 ]]; then
  echo "fuzz.sh: ${#failed[@]} failure(s) ($ntargets targets run):" >&2
  printf '  %s\n' "${failed[@]}" >&2
  exit 1
fi
if [[ $ntargets == 0 ]]; then
  echo "fuzz.sh: no Fuzz targets found" >&2
  exit 1
fi
echo "fuzz.sh: ok ($ntargets targets, $fuzztime each)"
