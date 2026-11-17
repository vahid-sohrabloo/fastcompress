#!/usr/bin/env bash
# apicheck.sh [--subset|--missing]
#
# Compares the exported API of ./flate, ./gzip and ./zlib against the standard
# library's compress/flate, compress/gzip and compress/zlib.
#   --subset  every normalised declaration of ours must appear in the standard
#             library's
#   --missing print standard library lines ours does not export (always exit 0)
#   (default) additionally every standard library line must exist in ours
#
# Normalisation of `go doc -all` output (applied identically to both sides):
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
#
# The standard library side is `go doc -all` of the installed Go. api/stdlib-<pkg>-delta.txt
# lists the expected difference: "+ line" only in ours, "- line" only in the
# standard library ("#" lines are comments). The three modes apply to them in
# the same way (--subset: ours minus "+" lines must appear in the standard
# library's; --missing: print standard library lines not in ours).
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

ref="$(mktemp)"; ours="$(mktemp)"; trap 'rm -f "$ref" "$ours"' EXIT
for p in flate gzip zlib; do
  delta="api/stdlib-$p-delta.txt"
  go doc -all "./$p" | normalise | LC_ALL=C comm -23 - <(sed -n 's/^+ //p' "$delta" | LC_ALL=C sort -u) > "$ours"
  go doc -all "compress/$p" | normalise | LC_ALL=C comm -23 - <(sed -n 's/^- //p' "$delta" | LC_ALL=C sort -u) > "$ref"
  if [ "$mode" = "--missing" ]; then
    LC_ALL=C comm -13 "$ours" "$ref" | sed "s|^|$p: |"
    continue
  elif [ "$mode" = "--subset" ]; then
    extra="$(LC_ALL=C comm -23 "$ours" "$ref")"
    if [ -n "$extra" ]; then echo "./$p: not in compress/$p:"; echo "$extra"; exit 1; fi
  else
    diff="$(LC_ALL=C comm -3 "$ours" "$ref")"
    if [ -n "$diff" ]; then echo "./$p API mismatch vs compress/$p (left: ours only, right: standard library only; see $delta):"; echo "$diff"; exit 1; fi
  fi
done
[ "$mode" = "--missing" ] && exit 0
echo "apicheck ok"
