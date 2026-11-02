#!/usr/bin/env bash
# apicheck.sh [--subset|--missing]
#
# Compares the exported API of ./zstd against api/klauspost-zstd.txt.
# api/fastzstd-extensions.txt lists the (normalised) declarations this port
# adds on top of klauspost's API; they are removed before comparing.
#   --subset  every normalised declaration of ./zstd must appear in the snapshot
#   --missing print snapshot lines ./zstd does not export yet (always exit 0)
#   (default) additionally every snapshot line must exist in ./zstd
#
# Normalisation of `go doc -all` output (applied identically to the snapshot):
#   - doc prose is dropped: it is indented with spaces, declarations are not
#     (top-level declarations start in column 0 with func/type/const/var,
#     members of struct/interface/const/var blocks are indented with one tab)
#   - tab-indented comment lines and blank lines are dropped
#   - trailing `// ...` comments are stripped, runs of whitespace collapse to
#     one space (gofmt column alignment), trailing space is trimmed
#   - a member line is prefixed with its enclosing declaration header
#     ("type T struct { | Field int") so identical member names in different
#     types stay distinct and const/var block members are still captured
#   - lines that are a lone closing brace/paren are dropped
#   - the result is sorted with LC_ALL=C and de-duplicated
set -euo pipefail

cd "$(dirname "$0")/.."

normalise() {
  awk '
    /^(func|type|const|var) / {
      line = $0; sub(/[ \t]*\/\/.*$/, "", line); gsub(/[ \t]+/, " ", line); sub(/ $/, "", line)
      hdr = line; print line; next
    }
    /^\t/ {
      if ($0 ~ /^\t[ \t]*\/\//) next
      line = $0; sub(/[ \t]*\/\/.*$/, "", line); gsub(/[ \t]+/, " ", line)
      sub(/^ /, "", line); sub(/ $/, "", line)
      if (line == "" || line == "}" || line == ")") next
      print hdr " | " line; next
    }
  ' | LC_ALL=C sort -u
}

mode="${1:-}"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
go doc -all ./zstd | normalise | LC_ALL=C comm -23 - api/fastzstd-extensions.txt > "$tmp"

if [ "$mode" = "--missing" ]; then
  LC_ALL=C comm -13 "$tmp" api/klauspost-zstd.txt
  exit 0
elif [ "$mode" = "--subset" ]; then
  extra="$(LC_ALL=C comm -23 "$tmp" api/klauspost-zstd.txt)"
  if [ -n "$extra" ]; then echo "not in klauspost snapshot:"; echo "$extra"; exit 1; fi
else
  diff="$(LC_ALL=C comm -3 "$tmp" api/klauspost-zstd.txt)"
  if [ -n "$diff" ]; then echo "API mismatch (left: ours only, right: snapshot only):"; echo "$diff"; exit 1; fi
fi
echo "apicheck ok"
