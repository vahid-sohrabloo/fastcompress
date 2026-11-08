# Security policy

This library decodes data that usually comes from untrusted sources, so
memory-safety and denial-of-service bugs (crashes, panics on malformed input,
unbounded memory use, wrong output) are treated as security issues.

## Reporting a vulnerability

Please do not open a public issue. Report it privately through the
repository's "Report a vulnerability" button (GitHub private vulnerability
reporting), with the input that triggers it if you can share it. You can expect
an acknowledgement within a week.

## Supported versions

Only the latest release receives fixes.

## Hardening already in place

The decoder is fuzzed against the C reference (see `scripts/fuzz.sh`), checked
by `scripts/decodercheck.sh` (no `unsafe` or `recover` in decoder code, bounds
notes on every assembly routine), and every assembly path has a pure-Go
fallback (`-tags noasm`).
