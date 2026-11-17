# DEFLATE test tiers (internal/deflate, flate, gzip, zlib)

Every run goes through `scripts/deflate-test.sh`, which applies the mandated wrapper
(`flock /tmp/gzip-libdeflate-test.lock systemd-run --user --scope -p MemoryMax=6G -p MemorySwapMax=0
env GOMEMLIMIT=4GiB go test -p 1 -count=1 -timeout 60m ...`; `-n` prints the full commands). The cap
and limit are read from FASTCOMPRESS_TEST_MEM_MAX (default 6G; `N`, `NK`, `NM` or `NG`, at least 256M, at
most 8G) and FASTCOMPRESS_TEST_GOMEMLIMIT (default 4GiB; `N` followed by `B`, `KiB`, `MiB` or `GiB`, nonzero
and below the cap), leading zeros dropped; anything else (empty, `infinity`, `off`, ...) is rejected.
Before each run it waits (up to 6 h, then the run is aborted with rc 75, not started) until
MemAvailable is at least the cap + 2 GiB and no other test binary uses more than 3 GB, and checks again
once the lock is taken (up to 10 min, then abort with rc 75). The script exits 0 when every run passed, 75 when the only failures are such
memory aborts, 1 otherwise. It runs in the foreground and records MemoryMax and GOMEMLIMIT in the log
header and `<logdir>/summary.txt`, and the wall time, the largest process's RSS, the scope's
`memory.peak` (page cache included) and its `oom_kill` count in the log's RESULT line and summary.txt.

## Tier 0 "quick": every change (target 3-5 min wall, par 8)

    scripts/deflate-groups.sh -stamp /tmp/change.stamp      # before editing
    ...edit...
    scripts/deflate-groups.sh -v -run -since /tmp/change.stamp
    scripts/deflate-groups.sh -v -run internal/deflate/hc_matchfinder.go   # or an explicit file list

deflate-groups.sh maps each changed file to the internal/deflate tests that cover it (the Test/Fuzz
functions of its own _test.go files plus the tests of its area, by name keywords: see the script
header; an unrecognised internal/deflate file selects the whole package) and runs them once with
`-short`, `FASTCOMPRESS_TEST_TIER=quick`, asm on, the cases in parallel and the ldref cache on; an
`*_amd64.s` change adds one `FASTCOMPRESS_FUZZ_ASM=nobmi2` pass. -short is the reduced matrix (the generated
edge cases, Silesia xml, the first 1-2 MiB of silesia.tar, fewer random inputs, levels 10..12 on
smaller inputs); the quick tier adds the first 1 MiB (levels 10..12: 512 KiB) of mozilla, mr, nci,
ooffice, osdb, sao and x-ray to the level identity tests. No asm-off, noasm or flate/gzip/zlib pass is
made for an internal/deflate edit (tier 1 does them); a flate/gzip/zlib change runs that package -short;
test infrastructure (creftest, deflatetest, cpu, guardmem, ref/) runs the whole package. `-v` lists the
dropped tests. The whole package with -short (`scripts/deflate-test.sh -t quick`, 100% statement
coverage with `-c`) is the fallback.

## Tier 1 "full": end of a round, and CI

    scripts/deflate-test.sh -t full -a both lazy nopt rest equiv dec other
    scripts/deflate-test.sh -t full noasm flate gzip zlib creftest

or `scripts/deflate-groups.sh -t full -run -since <round stamp>` for the partitions a round touched. No
-short: whole Silesia files, the whole silesia.tar in 1 MiB / 128 KiB / 16 KiB blocks at every level,
all random inputs, both FASTCOMPRESS_FUZZ_ASM modes. The partitions (`scripts/deflate-groups.sh -list`) are
disjoint and together the whole package (`other` was only run by CI before). noasm stays -short as in
the rounds (CI runs it in full). CI (.github/workflows/ci.yml) runs each partition as its own matrix
job (`-run`/`-skip` from `deflate-groups.sh -regex`, default build then `-tags noasm`), so no single go
test of the package has to fit the whole suite in its 60m timeout; the other packages are one more job.

## Parallel cases

The big differential tests run their independent cases (levels, Silesia files, batches of inputs, the
decoder tests of one asm mode) as parallel subtests inside their own top-level test, so the tests that
switch the process-global asm mode never overlap them. `FASTCOMPRESS_TEST_PAR` (default 8; `-p N`) bounds the
concurrent leaf cases and go test's -parallel; 1 is the serial mode (the old order, no t.Parallel). Under
-race it is always 1. A failure names its case (subtest path + input name).

## ldref output cache

`internal/creftest` caches ldref's compressor outputs (`compress`, `compress-batch`, `dump-hcmf`,
`dump-btmf`, `dump-nopt`) by SHA-256 of (libdeflate tag, ldref binary, arguments, stdin) under
`FASTCOMPRESS_LDREF_CACHE_DIR` (default `$XDG_CACHE_HOME/fastcompress-ldref`, i.e. `~/.cache/fastcompress-ldref`;
never inside the repository). Entries are written atomically and checked by length and hash (a corrupt
one is recomputed); 1 hit in 64 is re-run in C and must match. The ldref binary hash is part of the key:
**the first run after rebuilding ldref (changing ref/, ref/ldref/ldref.c or the libdeflate tag)
repopulates the cache** (delete the directory to reclaim the old entries). Size: about 0.5 GB after the
whole package's quick tier, about 10 GB after a full tier 1 of internal/deflate (every level of all of
Silesia and the tar blocks). `FASTCOMPRESS_LDREF_CACHE=off`
disables it, `=verify` re-runs every call in C and fails on a difference. It is off by default when `CI`
is set.

## Timing

`scripts/deflate-timing.sh <logdir>/*.log` prints per log the run's wall time and memory, the summed
top-level test wall time per partition (from the per-line timestamps) and the slowest tests.
