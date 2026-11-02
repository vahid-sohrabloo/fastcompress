#!/usr/bin/env bash
# decodercheck.sh
#
# Decoder safety rules (Phase 2 plan, Global Constraints). Fails when:
#   1. a decoder Go file (non-test) imports "unsafe" or calls recover(.
#      Decoder files: zstd/zstd_decompress*.go, zstd/zstd_ddict.go,
#      zstd/decoder*.go, zstd/header.go, zstd/snappy.go,
#      internal/huf/huf_decompress*.go.
#   2. a *_decompress*_{amd64,arm64}.s file under zstd/ or internal/ has a
#      TEXT symbol whose comment block (the contiguous // lines directly
#      above the TEXT line) has no "// BOUNDS:" line.
#   3. a cpu.RegisterAsm name in a decoder file has no fuzz target
#      func FuzzXxxAsmEquiv( in a _test.go file of the same directory, where
#      Xxx matches (ignoring case) either the name's part before the first
#      "." or the whole name with non-alphanumerics removed.
#      ("huf.decompress4X1Fast" -> FuzzHUFAsmEquiv.)
# Exit 0 when nothing is found (also when no decoder files exist yet).
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0
err() {
  echo "decodercheck: $*" >&2
  fail=1
}

shopt -s nullglob
decoder=()
for f in zstd/zstd_decompress*.go zstd/zstd_ddict.go zstd/decoder*.go zstd/header.go zstd/snappy.go \
  internal/huf/huf_decompress*.go; do
  [[ -f $f && $f != *_test.go ]] && decoder+=("$f")
done

# 1. unsafe imports and recover calls (comments stripped first).
for f in "${decoder[@]}"; do
  code=$(sed -e 's://.*$::' "$f")
  if grep -Eq '^[[:space:]]*(import[[:space:]]+)?([A-Za-z_.][A-Za-z0-9_]*[[:space:]]+)?"unsafe"' <<<"$code"; then
    err "$f imports \"unsafe\""
  fi
  if grep -Eq '(^|[^A-Za-z0-9_.])recover[[:space:]]*\(' <<<"$code"; then
    err "$f calls recover()"
  fi
done

# 2. BOUNDS blocks on decoder assembly.
while IFS= read -r -d '' s; do
  out=$(awk '
    /^[[:space:]]*\/\// { block = block "\n" $0; next }
    /^TEXT[[:space:]]/ {
      if (block !~ /\n[[:space:]]*\/\/[[:space:]]*BOUNDS:/) { sym = $2; sub(/\(SB\).*/, "", sym); print sym }
      block = ""; next
    }
    { block = "" }
  ' "$s")
  for sym in $out; do
    err "$s: TEXT $sym has no // BOUNDS: block in the comment above it"
  done
done < <(find zstd internal -type f \( -name '*_decompress*_amd64.s' -o -name '*_decompress*_arm64.s' \) -print0)

# 3. Every decoder-side asm routine has a FuzzXxxAsmEquiv target.
for f in "${decoder[@]}"; do
  dir=$(dirname "$f")
  while IFS= read -r name; do
    [[ -z $name ]] && continue
    prefix=${name%%.*}
    whole=${name//[^A-Za-z0-9]/}
    found=0
    for t in "$dir"/*_test.go; do
      if grep -Eiq "^func Fuzz(${prefix}|${whole})AsmEquiv\(" "$t"; then
        found=1
        break
      fi
    done
    [[ $found == 1 ]] || err "$f: cpu.RegisterAsm(\"$name\") has no Fuzz${prefix}AsmEquiv / Fuzz${whole}AsmEquiv in $dir/*_test.go"
  done < <(sed -e 's://.*$::' "$f" | grep -Eo 'cpu\.RegisterAsm\("[^"]+"' | sed -E 's/.*\("([^"]+)"/\1/' || true)
done

if [[ $fail != 0 ]]; then
  exit 1
fi
echo "decodercheck: ok (${#decoder[@]} decoder files)"
