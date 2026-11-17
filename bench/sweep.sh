#!/usr/bin/env bash
# Compression speed and ratio at every level for this library and the other
# Go libraries, on silesia.tar (zstd: 4 MiB blocks, DEFLATE: 1 MiB, LZ4:
# 64 KiB), single-threaded, pinned to CPU $CPU (default 2). Two rounds; the
# plot keeps the faster of the two. Writes bench/results/<date>-sweep.tsv
# (codec, library, level, MB/s, ratio) for `bench/plot.py frontier`.
set -euo pipefail
cd "$(dirname "$0")/.."
CPU=${CPU:-2}
T=bench/corpus/silesia.tar
[ -f "$T" ] || { echo "missing $T; run ref/fetch.sh" >&2; exit 1; }
B=$(mktemp -d); trap 'rm -rf "$B"' EXIT
go build -o "$B/gobench" ./bench/cmd/gobench
go build -o "$B/lz4bench" ./bench/cmd/lz4bench
(cd bench/zstd && go build -o "$B/zstdbench" .)
(cd bench/gzip && go build -o "$B/gzbench" .)
(cd bench/lz4 && go build -o "$B/pierrecbench" .)
O=bench/results/$(date +%F)-sweep.tsv; mkdir -p bench/results; : > "$O"
r() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$(taskset -c "$CPU" "${@:4}" 2>&1 | tail -1 | awk '{print $1"\t"$2}')" >> "$O"; }
for round in 1 2; do
  for L in 1 2 3 4 5 6 7 8 9 10 11 12 13 15; do r zstd ours $L "$B/gobench" -level $L -block 4194304 -secs 2 $T; done
  for L in 1 3 7 11; do r zstd klauspost $L "$B/zstdbench" -lib klauspost -level $L -block 4194304 -secs 2 $T; done
  for L in 1 2 3 4 5 6 7 8 9 10 11 12; do r deflate ours $L "$B/gzbench" -lib ours -level $L -block 1048576 -secs 2 $T; done
  for L in 1 2 3 4 5 6 7 8 9; do
    r deflate stdlib $L "$B/gzbench" -lib stdlib -level $L -block 1048576 -secs 2 $T
    r deflate klauspost $L "$B/gzbench" -lib klauspost -level $L -block 1048576 -secs 2 $T
  done
  for L in 1 2 3 4 5 6 7 8 9 10 11 12; do r lz4 ours $L "$B/lz4bench" -level $L -block 65536 -secs 2 $T; done
  r lz4 pierrec 0 "$B/pierrecbench" -level 1 -block 65536 -secs 2 $T
  for P in 1 2 3 4 5 6 7 8 9; do r lz4 pierrec $P "$B/pierrecbench" -level 9 -plevel $P -block 65536 -secs 2 $T; done
done
echo "wrote $O"
