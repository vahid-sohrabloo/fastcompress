# restic encoder-option check

Source: restic 0.19.1 (latest release at 2026-11-02), source tarball read only, nothing built or run.
All encoder construction is in `internal/repository/repository.go` (`getZstdEncoder`, line 320); both call sites use `EncodeAll` (lines 391 and 456, the latter with a 1-byte version prefix already in `dst`). The decoder (`NewReader`, `DecodeAll`, `WithDecoderConcurrency`, `WithDecoderMaxMemory`) is Phase 2 and out of scope here.

## Options restic passes

| restic call | restic mode | fastzstd | supported |
|---|---|---|---|
| `zstd.NewWriter(nil, opts...)` | all | `zstd.NewWriter` (encoder.go:56) | yes |
| `WithEncoderLevel(SpeedFastest)` | `fastest` | C level 1 | yes |
| `WithEncoderLevel(SpeedDefault)` | `auto` (default) | C level 3 | yes |
| `WithEncoderLevel(SpeedBetterCompression)` | `better` | C level 7 | yes |
| `WithEncoderLevel(SpeedBestCompression)` | `max` | C level 11 | yes |
| `WithEncoderCRC(false)` | all | C checksumFlag=0 (encoder_options.go:96) | yes |
| `WithWindowSize(512 * 1024)` | all | C windowLog=19 (power of two in range; encoder_options.go:143) | yes |
| `Encoder.EncodeAll(src, dst)` | all | appends to dst (encoder.go:146) | yes |

`scripts/apicheck.sh --subset` confirms every exported declaration of `./zstd` is in the klauspost snapshot except those listed in `api/fastzstd-extensions.txt` (only `WithHugePages`).

Smoke check (scratch program outside the repo, importing fastzstd with the exact option set above and `EncodeAll(data, []byte{2})`): for each of the four levels the output after the prefix byte decodes with the C `zstd -d` CLI to the original input, and the prefix byte is preserved.

Meaning matches: level mapping is the same four-point table (1/3/7/11), CRC off omits the 4-byte checksum, window is the maximum back-reference distance.

Auto and max are `SpeedDefault` and `SpeedBestCompression`, i.e. C levels 3 and 11.

## Performance at restic's levels

All of restic's levels (1, 3, 7, 11) are in the Task 22 table, so `bench/compare.sh` was not rerun. Rows below are cited from `2026-11-02-phase1-final.md` (silesia.tar, median of 3, pinned CPU).

| restic mode | level | block | C MB/s | Go MB/s | % of C | Go ratio = C ratio |
|---|---|---|---|---|---|---|
| fastest | 1 | 1M | 496.5 | 514.9 | 103.7% | 2.880 |
| fastest | 1 | 4M | 507.5 | 514.8 | 101.4% | 2.891 |
| auto | 3 | 1M | 312.7 | 312.0 | 99.8% | 3.147 |
| auto | 3 | 4M | 301.7 | 302.1 | 100.1% | 3.188 |
| better | 7 | 1M | 105.7 | 108.2 | 102.4% | 3.386 |
| better | 7 | 4M | 102.7 | 104.8 | 102.0% | 3.478 |
| max | 11 | 1M | 54.5 | 55.4 | 101.7% | 3.448 |
| max | 11 | 4M | 46.8 | 47.4 | 101.3% | 3.579 |

## Caveat

The benchmark rows use each level's default window; restic forces windowLog=19. Blobs in restic are at most a few MB and the window is clamped to the input size as in C, so the effect is expected to be small, but no window-19 cell was benchmarked. Restic cannot swap imports until Phase 2 (decoder).
