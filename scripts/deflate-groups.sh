#!/usr/bin/env bash
# deflate-groups.sh - which DEFLATE test groups a change needs (see scripts/README-deflate-tests.md).
#
#   deflate-groups.sh [-t quick|full] [-v] [-since STAMP | -base REF] [-run] [FILE...]
#       Maps the changed files to the scripts/deflate-test.sh runs they need and prints one command per
#       line (with -run: executes them, one at a time, stopping at the first failure; -v: also list the
#       internal/deflate tests the quick selection drops). The changed files
#       are the FILE arguments (paths relative to the repository root, or absolute), else every file
#       under the watched directories modified after STAMP (find -newer; internal/deflate is untracked,
#       so mtime is the only history it has), else `git diff --name-only REF` (default HEAD) plus the
#       untracked files (without -since that is all of internal/deflate: everything runs).
#   deflate-groups.sh -stamp STAMP      touch STAMP: mark the start of a change or round.
#   deflate-groups.sh -regex GROUP      print GROUP's -run regex (other: the -skip regex).
#   deflate-groups.sh -list             list the internal/deflate partitions.
#
# internal/deflate partitions (disjoint; together the whole package):
#   lazy   TestCompressLazyMatchLibdeflate                      (levels 5..9 vs libdeflate)
#   nopt   TestCompressNearOptimalMatchLibdeflate               (levels 10..12 vs libdeflate)
#   rest   every other compressor *MatchLibdeflate test + TestCompressFastestHtEquiv
#   equiv  the compressor assembly equivalence tests (Test*AsmEquiv except the decoder's)
#   dec    TestBuildDecodeTables*MatchLibdeflate, TestDeflateAsmEquiv, TestStream*AsmEquiv
#   other  everything else (unit tests, decoder differential tests run standalone, ...)
#
# Tier full rules (conservative: when a file is not recognised, everything runs):
#   * internal/deflate/*_amd64.s, *_arm64.s (assembly only): its area's partitions + equiv (compressor) or dec
#     (decoder), asm on only. Every other internal/deflate file (Go, *_amd64.go included: it holds the
#     dispatch and layout code) needs its partitions in both FASTCOMPRESS_FUZZ_ASM modes, plus other, noasm
#     and, for a non-test file, the flate, gzip and zlib packages.
#   * areas (basename prefix): compress_fastest/ht_matchfinder/compress_level1 -> rest equiv;
#     compress_greedy/hc_matchfinder/compress_lazy -> lazy rest equiv; compress_near_optimal/
#     bt_matchfinder/costs -> nopt rest; flush_block/bitwriter/huffman/block_split/compress.go/
#     compress_none/compress_chunk/consts*/static_codes -> lazy nopt rest equiv (every level writes
#     blocks); decompress*/decode_table* -> dec; gzip.go/zlib.go -> rest dec; matchfinder* -> lazy nopt
#     rest equiv; anything else -> all partitions.
#   * internal/creftest, internal/deflatetest, internal/cpu, internal/guardmem, ref/ldref, ref/fetch.sh,
#     ref/libdeflate: everything (all partitions, both modes, noasm, flate gzip zlib, creftest's ^TestLD).
#   * internal/adler32, internal/appendbuf: flate gzip zlib + all partitions on.
#   * flate/, gzip/, zlib/: that package.
#   * other paths (docs, bench, scripts, .superpowers, zstd, ...): nothing.
#
# Tier quick (tier 0, every change; default): only the internal/deflate tests of the changed files, -short,
# asm on (plus one FASTCOMPRESS_FUZZ_ASM=nobmi2 pass when an *_amd64.s file changed); no asm-off, noasm or
# package passes for internal/deflate edits (those are tier 1's); flate/gzip/zlib -short only when their
# own files change; everything (-short) for test infrastructure. The tests of a file are the top-level
# Test/Fuzz functions of its _test.go files (foo.go, foo_amd64.s -> foo_test.go, foo_*_test.go; a test
# file -> its own) plus those whose names match its area's keywords:
#   compress_fastest/ht_matchfinder/compress_level1: Fastest Level1 Ht* ChooseMaxBlockEnd
#   compress_greedy/hc_matchfinder: Greedy Lazy Hc* MinMatchLen;  compress_lazy: Lazy Hc* MinMatchLen
#   compress_near_optimal/bt_matchfinder/costs: NearOptimal Bt* Cost Nopt Stages
#   matchfinder*: Matchfinder LzHash LzExtend Hc* Ht* Bt* Fastest Level1 Greedy Lazy NearOptimal
#   flush_block/bitwriter/huffman/block_split/compress.go/compress_none/consts*/static_codes: every
#     compressor test (Compress Flush Bitwriter Huffman BlockSplit StaticCodes WriteSequences ...)
#   compress_chunk: Chunk;  gzip.go/zlib.go: Container Gzip Zlib
#   decompress*/decode_table*: every decoder test (Decompress Decode RoundTrip Stream Diff* Invalid ...)
#   anything else in internal/deflate: the whole package.
# Tier full (tier 1, end of a round and CI) uses the partitions and rules above.
set -euo pipefail
cd "$(dirname "$0")/.."

part_regex() {
  case "$1" in
    lazy) echo '^TestCompressLazyMatchLibdeflate$' ;;
    nopt) echo '^TestCompressNearOptimalMatchLibdeflate$' ;;
    rest) echo '^(TestBitwriter|TestBlockSplit|TestBtMatchfinder|TestCompressLevel0|TestCompressPassthrough|TestCompressBound|TestLevelParams|TestCompressGreedy|TestCompressGreedyRandom|TestMinMatchLen|TestCompressLazyRandom|TestHcMatchfinderStateLazy|TestCompressLevel1|TestCompressLevel1Random|TestHtMatchfinderState|TestCompressNearOptimalRandom|TestBtMatchfinderState|TestContainerCompress|TestNearOptimalStages|TestStaticTables|TestFlushBlock|TestHcMatchfinder|TestHcMatchfinderState|TestMakeHuffmanCode|TestMakeHuffmanCodeUnit|TestPrecomputeHuffmanHeader|TestStaticCodes)MatchLibdeflate$|^TestCompressFastestHtEquiv$' ;;
    equiv) echo '^(TestMatchfinder|TestHcLongestMatch|TestFastestLoop|TestGreedyLoop|TestWriteSequences)AsmEquiv$' ;;
    dec) echo '^(TestBuildDecodeTablesUnitMatchLibdeflate|TestBuildDecodeTablesMatchLibdeflate|TestDeflateAsmEquiv|TestStreamAsmEquiv|TestStreamReaderAsmEquiv)$' ;;
    other) echo "$(part_regex lazy)|$(part_regex nopt)|$(part_regex rest)|$(part_regex equiv)|$(part_regex dec)" ;;
    *) echo "unknown partition: $1" >&2; return 2 ;;
  esac
}
PARTS="lazy nopt rest equiv dec other"

tier=quick since="" base=HEAD dorun=0 verbose=0
files=()
while [ $# -gt 0 ]; do
  case "$1" in
    -regex) part_regex "$2"; exit ;;
    -list) echo "$PARTS"; exit ;;
    -stamp) touch "$2"; exit ;;
    -t) tier=$2; shift ;;
    -since) since=$2; shift ;;
    -base) base=$2; shift ;;
    -run) dorun=1 ;;
    -v) verbose=1 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d'; exit ;;
    -*) echo "unknown flag $1" >&2; exit 2 ;;
    *) files+=("$1") ;;
  esac
  shift
done
case "$tier" in quick|fast) tier=quick ;; full) ;; *) echo "-t quick|full" >&2; exit 2 ;; esac

WATCH="internal/deflate internal/creftest internal/deflatetest internal/cpu internal/guardmem internal/adler32 internal/appendbuf flate gzip zlib ref/ldref ref/fetch.sh"
if [ ${#files[@]} -eq 0 ]; then
  if [ -n "$since" ]; then
    [ -e "$since" ] || { echo "no stamp $since (deflate-groups.sh -stamp $since first)" >&2; exit 2; }
    mapfile -t files < <(find $WATCH -type f -newer "$since" ! -path '*/testdata/fuzz/*' 2>/dev/null | sort)
  else
    mapfile -t files < <({ git diff --name-only "$base" -- $WATCH; git ls-files --others --exclude-standard -- $WATCH; } | sort -u)
  fi
fi

declare -A P=() # partitions -> "on" or "both"
pkgs="" noasm=0 creftest=0 n=0 why=()
qre=() qall=0 qnobmi2=0 qpkgs="" # quick tier: -run parts, whole package, nobmi2 pass, own-package changes
D=internal/deflate
testfuncs() { grep -ho '^func \(Test\|Fuzz\)[A-Za-z0-9_]*' "$@" 2>/dev/null | sed 's/^func //' | sort -u; }
add() { # add MODE PART...
  local m=$1 p; shift
  for p in "$@"; do
    if [ "$m" = both ] || [ -z "${P[$p]:-}" ]; then P[$p]=$m; fi
  done
}
addpkg() { local p; for p in "$@"; do case " $pkgs " in *" $p "*) ;; *) pkgs="$pkgs $p" ;; esac; done; }
everything() { add both $PARTS; noasm=1; addpkg flate gzip zlib; }

for f in "${files[@]}"; do
  f=${f#"$PWD"/}; f=${f#./}
  b=$(basename "$f")
  case "$f" in
    internal/deflate/*)
      n=$((n+1))
      mode=both
      case "$b" in *_amd64.s|*_arm64.s) mode=on ;; *_asm_equiv_test.go) mode=on ;; esac
      case "$b" in
        compress_fastest*|ht_matchfinder*|compress_level1*) areas="rest equiv" ;;
        compress_greedy*|hc_matchfinder*|compress_lazy*) areas="lazy rest equiv" ;;
        compress_near_optimal*|bt_matchfinder*|costs*) areas="nopt rest" ;;
        flush_block*|bitwriter*|huffman*|block_split*|compress.go|compress_test.go|compress_none*|compress_chunk*|consts*|static_codes*) areas="lazy nopt rest equiv" ;;
        decompress*|decode_table*) areas="dec" ;;
        gzip.go|zlib.go|container_test.go) areas="rest dec" ;;
        matchfinder*) areas="lazy nopt rest equiv" ;;
        *) areas="$PARTS" ;;
      esac
      case "$b" in static_codes*) areas="$areas dec" ;; esac
      # quick tier
      case "$b" in
        compress_fastest*|ht_matchfinder*|compress_level1*) kw='Fastest|Level1|Ht[A-Z]|ChooseMaxBlockEnd' ;;
        compress_greedy*|hc_matchfinder*) kw='Greedy|Lazy|Hc[A-Z]|MinMatchLen' ;;
        compress_lazy*) kw='Lazy|Hc[A-Z]|MinMatchLen' ;;
        compress_near_optimal*|bt_matchfinder*|costs*) kw='NearOptimal|Bt[A-Z]|Cost|Nopt|Stages' ;;
        matchfinder*) kw='Matchfinder|LzHash|LzExtend|Hc[A-Z]|Ht[A-Z]|Bt[A-Z]|Fastest|Level1|Greedy|Lazy|NearOptimal' ;;
        flush_block*|bitwriter*|huffman*|block_split*|compress.go|compress_test.go|compress_none*|consts*|static_codes*)
          kw='Compress|Flush|Bitwriter|BitPrimitives|Huffman|HeapSort|BlockSplit|StaticCodes|StaticTables|WriteSequences|OffsetSlots|LevelParams|EveryLevel|Container|Precompute' ;;
        compress_chunk*) kw='Chunk' ;;
        gzip.go|zlib.go|container_test.go) kw='Container|Gzip|Zlib' ;;
        decompress*|decode_table*)
          kw='Decompress|Decode|RoundTrip|Stream|Diff[A-Z]|Invalid|Overread|Fastloop|Probe|Port|Golden|Offsets|LongCodes|NoEOB|FuzzSeeds|DeflateAsmEquiv|Trailing|Stress|Slow|Truncat|Mutation|Result|ContainerDecompress|ContainerStdlib' ;;
        *) kw=""; qall=1 ;;
      esac
      case "$b" in *_amd64.s) qnobmi2=1 ;; esac
      if [ -n "$kw" ]; then
        qre+=("$kw")
        case "$b" in
          *_test.go) own=("$D/$b") ;;
          *) stem=${b%.*}; stem=${stem%_amd64}; stem=${stem%_arm64}; stem=${stem%_noasm}; own=("$D/${stem}_test.go" $D/${stem}_*_test.go) ;;
        esac
        for t in $(testfuncs "${own[@]}"); do qre+=("^$t\$"); done
      fi
      if [ "$mode" = on ]; then
        add on $areas
        case "$areas" in *dec*) add on dec ;; *) add on equiv ;; esac
      else
        add both $areas other
        noasm=1
        case "$b" in *_test.go) ;; *) addpkg flate gzip zlib ;; esac
      fi
      why+=("$f: $mode $areas")
      ;;
    internal/creftest/*|internal/deflatetest/*|internal/cpu/*|internal/guardmem/*|ref/ldref/*|ref/fetch.sh|ref/libdeflate/*)
      n=$((n+1)); everything; creftest=1; qall=1; qpkgs="flate gzip zlib"; why+=("$f: everything") ;;
    internal/adler32/*|internal/appendbuf/*)
      n=$((n+1)); addpkg flate gzip zlib; add on $PARTS; qpkgs="flate gzip zlib"; why+=("$f: packages + partitions on") ;;
    flate/*|gzip/*|zlib/*)
      n=$((n+1)); addpkg "${f%%/*}"; case " $qpkgs " in *" ${f%%/*} "*) ;; *) qpkgs="$qpkgs ${f%%/*}" ;; esac
      why+=("$f: ${f%%/*}") ;;
    internal/*.go|internal/*/*.go)
      ;; # other internal packages (zstd's): not DEFLATE
    *) ;;
  esac
done

for w in "${why[@]}"; do echo "# $w" >&2; done
if [ "$n" -eq 0 ]; then echo "# no DEFLATE-relevant change" >&2; exit 0; fi

cmds=()
on="" both=""
for p in $PARTS; do
  case "${P[$p]:-}" in on) on="$on $p" ;; both) both="$both $p" ;; esac
done
if [ "$tier" = quick ]; then
  modes=on
  [ "$qnobmi2" = 1 ] && modes=on,nobmi2
  all=$(testfuncs $D/*_test.go)
  if [ "$qall" = 1 ]; then
    cmds+=("scripts/deflate-test.sh -t quick -a $modes deflate")
    echo "# quick: all $(wc -l <<<"$all") internal/deflate tests" >&2
  elif [ ${#qre[@]} -gt 0 ]; then
    re=$(printf '%s\n' "${qre[@]}" | sort -u | paste -sd'|')
    cmds+=("scripts/deflate-test.sh -t quick -a $modes -r '$re'")
    sel=$(grep -E "$re" <<<"$all" || true)
    echo "# quick: $(grep -c . <<<"$sel") of $(wc -l <<<"$all") internal/deflate tests" >&2
    [ "$verbose" = 1 ] && echo "# dropped: $(grep -vxF -f <(echo "$sel") <<<"$all" | paste -sd' ')" >&2
  fi
  [ "$creftest" = 1 ] && cmds+=("scripts/deflate-test.sh -t quick creftest")
  [ -n "$qpkgs" ] && cmds+=("scripts/deflate-test.sh -t quick$(printf ' %s' $qpkgs)")
else
  [ -n "$on$both" ] && cmds+=("scripts/deflate-test.sh -t full -a on$on$both")
  [ -n "$both" ] && cmds+=("scripts/deflate-test.sh -t full -a off$both")
  [ "$noasm" = 1 ] && cmds+=("scripts/deflate-test.sh -t full noasm")
  [ "$creftest" = 1 ] && cmds+=("scripts/deflate-test.sh -t full creftest")
  [ -n "$pkgs" ] && cmds+=("scripts/deflate-test.sh -t full$pkgs")
fi

for c in "${cmds[@]}"; do echo "$c"; done
if [ "$dorun" = 1 ]; then
  for c in "${cmds[@]}"; do eval "$c" || exit 1; done
fi
