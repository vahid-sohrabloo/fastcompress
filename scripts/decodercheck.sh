#!/usr/bin/env bash
# decodercheck.sh
#
# Decoder safety rules (Phase 2 plan, Global Constraints). Fails when:
#   1. a decoder Go file (non-test) imports "unsafe" or calls recover(
#      (comments and string contents are ignored; needs perl). One
#      exception: zstd/decoder_mt.go may call recover() once, in the
#      pipeline's reader goroutine, to raise an io.Reader panic again on
#      the caller's goroutine (Task 13 fix round 1); a second call fails.
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
# Runs on bash 3.2 (macOS).
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0
err() {
  echo "decodercheck: $*" >&2
  fail=1
}

shopt -s nullglob
decoder=()
ndecoder=0
for f in zstd/zstd_decompress*.go zstd/zstd_ddict.go zstd/decoder*.go zstd/header.go zstd/snappy.go \
  internal/huf/huf_decompress*.go; do
  if [[ -f $f && $f != *_test.go ]]; then
    decoder+=("$f")
    ndecoder=$((ndecoder + 1))
  fi
done

# scan FILE prints one line per finding in a Go file: "unsafe", "recover",
# and "asm <name>" for each cpu.RegisterAsm("<name>", ...). It tokenizes
# comments, interpreted and raw strings and runes, so none of them can hide
# or fake a match; imports are read from import declarations only, and a
# call may span lines.
scan() {
  perl -0777 -ne '
    my ($code, $blank) = ("", "");   # comments removed; also string contents blanked
    while (length) {
      if    (s{\A//[^\n]*}{})        { }
      elsif (s{\A/\*.*?\*/}{}s)       { $code .= " "; $blank .= " "; }
      elsif (s{\A("(?:[^"\\\n]|\\.)*")}{}) { $code .= $1; $blank .= "\"\""; }
      elsif (s{\A(`[^`]*`)}{})       { $code .= $1; $blank .= "\"\""; }
      elsif (s{\A(\x27(?:[^\x27\\\n]|\\.)+\x27)}{}) { $code .= $1; $blank .= "0"; }
      elsif (s{\A([^/"`\x27]+|.)}{}s) { $code .= $1; $blank .= $1; }
    }
    my @specs;
    while ($code =~ /\bimport\s*\((.*?)\)/gs) { push @specs, $1 }
    while ($code =~ /\bimport\s+((?:[\w.]+\s+)?(?:"[^"]*"|`[^`]*`))/g) { push @specs, $1 }
    for (@specs) { if (/(?:"unsafe"|`unsafe`)/) { print "unsafe\n"; last } }
    my $nrec = () = $blank =~ /(?<![\w.])recover\s*\(/g;
    print "recover\n" for 1..$nrec;
    while ($code =~ /\bcpu\s*\.\s*RegisterAsm\s*\(\s*(?:"([^"]+)"|`([^`]+)`)/g) { print "asm ", $1 // $2, "\n" }
  ' "$1"
}

# 1. unsafe imports and recover calls.
for f in ${decoder[@]+"${decoder[@]}"}; do
  findings=$(scan "$f")
  if grep -qx unsafe <<<"$findings"; then
    err "$f imports \"unsafe\""
  fi
  nrec=$(grep -cx recover <<<"$findings" || true)
  allowed=0
  [[ $f == zstd/decoder_mt.go ]] && allowed=1
  if ((nrec > allowed)); then
    err "$f calls recover() $nrec time(s) (allowed: $allowed)"
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
for f in ${decoder[@]+"${decoder[@]}"}; do
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
  done < <(scan "$f" | sed -n 's/^asm //p')
done

if [[ $fail != 0 ]]; then
  exit 1
fi
echo "decodercheck: ok ($ndecoder decoder files)"
