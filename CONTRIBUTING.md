# Contributing

Bug reports and pull requests are welcome.

## Ground rules

- The compressor and decoder follow libzstd v1.5.7 function by function and
  must produce byte-identical output to C. Keep C's structure and names so the
  code stays comparable with the reference.
- Do not copy code from klauspost/compress.
- Assembly needs a pure-Go fallback, runtime CPU detection, and a bounds note
  (`// BOUNDS:`) on every routine. `scripts/decodercheck.sh` enforces this.

## Setup

    ref/fetch.sh            # clones libzstd v1.5.7, builds the zstd CLI, fetches the Silesia corpus
    go test ./...           # the comparison tests use the C reference; takes a while without -short
    go test -short ./...

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
