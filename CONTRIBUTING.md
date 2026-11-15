# Contributing

Bug reports and pull requests are welcome.

## Ground rules

- The compressor and decoder follow libzstd v1.5.7 (lz4: lz4 v1.11.1)
  function by function and must produce byte-identical output to C. Keep C's structure and names so the
  code stays comparable with the reference.
- Do not copy code from klauspost/compress or pierrec/lz4.
- Assembly needs a pure-Go fallback, runtime CPU detection, and a bounds note
  (`// BOUNDS:`) on every routine. `scripts/decodercheck.sh` enforces this.

## Setup

    ref/fetch.sh            # clones libzstd v1.5.7, libdeflate v1.26 and lz4 v1.11.1, builds the zstd and lz4 CLIs, fetches the Silesia corpus
    make -C ref/cref        # the zstd reference driver
    make -C ref/ldref       # the libdeflate reference driver
    make -C ref/lz4ref      # the lz4 reference driver
    go test ./...           # the comparison tests use the C reference; takes a while without -short
    go test -short ./...

Without `ref/cref/cref`, `ref/ldref/ldref` and `ref/lz4ref/lz4ref` the comparison tests skip (they
fail under `CI=true`), so build all three before running the tests.

The full suite is memory hungry. Cap it if your machine is small, for example
`GOMEMLIMIT=2GiB go test -p 1 ./...`.

## Checks before a pull request

    gofmt -l .
    go vet ./...
    scripts/decodercheck.sh
    scripts/apicheck.sh
    go test -short ./...

Changes to hot paths need a benchmark (`bench/README.md`) run on a quiet
machine; a slowdown of more than 3% on any row is rejected.

## Fuzzing

`scripts/fuzz.sh -t 5m` runs every fuzz target. Commit any failing input under
`testdata/fuzz/` together with the fix.
