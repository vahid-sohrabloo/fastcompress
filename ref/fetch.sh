#!/usr/bin/env bash
# Idempotent: clone zstd v1.5.7, build the zstd CLI, clone libdeflate v1.26, clone lz4 v1.11.1
# and build its CLI, fetch Silesia (unless --no-corpus).
set -euo pipefail
cd "$(dirname "$0")/.."
TAG=v1.5.7
LDTAG=v1.26
LZ4TAG=v1.11.1
CORPUS=1
for a in "$@"; do
  case "$a" in
    --no-corpus) CORPUS=0 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

if [ ! -d ref/zstd/.git ]; then
  git clone --depth 1 --branch "$TAG" https://github.com/facebook/zstd.git ref/zstd
fi
make -C ref/zstd/programs zstd -j"$(nproc)"

if [ ! -d ref/libdeflate/.git ]; then
  git clone --depth 1 --branch "$LDTAG" https://github.com/ebiggers/libdeflate.git ref/libdeflate
fi

if [ ! -d ref/lz4/.git ]; then
  git clone --depth 1 --branch "$LZ4TAG" https://github.com/lz4/lz4.git ref/lz4
fi
make -C ref/lz4/programs lz4 -j"$(nproc)"

if [ "$CORPUS" = 1 ]; then
  dir=bench/corpus/silesia
  if [ ! -d "$dir" ] || [ -z "$(ls -A "$dir" 2>/dev/null)" ]; then
    mkdir -p "$dir" bench/corpus
    tmp=$(mktemp -d)
    curl -fL -o "$tmp/silesia.zip" https://sun.aei.polsl.pl/~sdeor/corpus/silesia.zip \
      || curl -fL -o "$tmp/silesia.zip" https://mattmahoney.net/dc/silesia.zip
    unzip -q -o "$tmp/silesia.zip" -d "$dir"
    rm -rf "$tmp"
  fi
  if [ ! -f bench/corpus/silesia.tar ]; then
    (cd "$dir" && LC_ALL=C tar -cf ../silesia.tar $(ls | LC_ALL=C sort))
  fi
fi
