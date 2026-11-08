# restic encoder-option check

Source: restic 0.19.1 (latest release at 2026-11-02), source tarball read only, nothing built or run.
All encoder construction is in `internal/repository/repository.go` (`getZstdEncoder`, line 320); both call sites use `EncodeAll` (lines 391 and 456, the latter with a 1-byte version prefix already in `dst`). The decoder is checked in the Decoder section below (Phase 2 Task 12).

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

The benchmark rows use each level's default window; restic forces windowLog=19. Blobs in restic are at most a few MB and the window is clamped to the input size as in C, so the effect is expected to be small, but no window-19 cell was benchmarked. Restic cannot swap imports until Phase 2 (decoder) is complete.

## Decoder (Phase 2 Task 12)

Source: the same restic 0.19.1 tarball, read only, nothing built or run. restic 0.19.1 pins `github.com/klauspost/compress v1.18.6` (`go.mod:25`). It uses only `DecodeAll`, never a decoder stream.

### Construction

| restic call site | construction | use |
|---|---|---|
| `internal/repository/repository.go:355-372` (`getZstdDecoder`, once per repository) | `zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(16 * 1024 * 1024 * 1024))` | `DecodeAll(plaintext, nil)` (line 436, unpacked files), `DecodeAll(p[1:], nil)` (line 479, versioned JSON), and through `streamPack` |
| `internal/repository/repository.go:1282` (`packBlobIterator`) | the decoder passed in | `DecodeAll(plaintext, b.decode[:0])`: a reused buffer |
| `internal/repository/checker.go:254` (one per check worker, `defer dec.Close()`) | `zstd.NewReader(nil)` | `CheckPack` → `packBlobIterator` |
| `cmd/restic/cmd_debug.go:343` (`loadBlobs`) | `zstd.NewReader(nil)` | `DecodeAll(plaintext, nil)` (line 387) |

### Options restic passes

| restic option | fastzstd | supported |
|---|---|---|
| `NewReader(nil, …)` | `NewReader` (decoder.go): nil reader, DecodeAll only | yes |
| `WithDecoderConcurrency(0)` | GOMAXPROCS decompression contexts, one per concurrent `DecodeAll` (decoder_options.go) | yes |
| `WithDecoderMaxMemory(16 GiB)` | `DecodeAll` output limit 16 GiB, window limit min(512 MiB, 16 GiB). On 32-bit platforms the limit in effect is 1 GiB (see the decisions doc) | yes |
| `DecodeAll(src, nil)` / `DecodeAll(src, buf[:0])` | appends to dst; a frame with a content size (restic's `EncodeAll` frames always have one) decodes in dst's spare capacity, or into one allocation of its size | yes |
| `Close()` | releases the stream state; later calls return `ErrDecoderClosed` | yes |

`TestResticDecoderOptions` (zstd/decoder_test.go) builds both constructions exactly as above and decodes 1 MiB and 4 MiB blobs written with restic's encoder options (CRC off, 512 KiB window) at levels 1/3/7/11, into nil and into a reused buffer. `scripts/apicheck.sh --subset` passes.

## Phase 2 swap check (Task 22)

Source: restic 0.19.1 release tarball (`restic-0.19.1.tar.gz` from the GitHub release), unpacked in a scratch directory outside this repo. Go 1.27.1, fastzstd at c3f5431. Every go command ran under `systemd-run --user --scope -p MemoryMax=4G -p MemorySwapMax=0 env GOMEMLIMIT=2GiB`.

### The swap

- The five files that import `github.com/klauspost/compress/zstd` now import `zstd "github.com/vahid-sohrabloo/fastzstd/zstd"` instead. Nothing else was edited:
  - `internal/repository/repository.go`
  - `internal/repository/check.go`
  - `internal/repository/checker.go`
  - `internal/repository/repository_internal_test.go`
  - `cmd/restic/cmd_debug.go`
- `go.mod` gets `require github.com/vahid-sohrabloo/fastzstd v0.0.0` and `replace github.com/vahid-sohrabloo/fastzstd => ../fastzstd` (a local checkout).
- `go mod tidy`: exit 0. It turns `github.com/klauspost/compress v1.18.6` into `// indirect`, because restic no longer imports it directly.

### What still needs klauspost/compress

`go list -deps -test ./...` lists three klauspost/compress packages and no klauspost zstd:
- `github.com/klauspost/compress/s2`
- `github.com/klauspost/compress/internal/le`
- `github.com/klauspost/compress/internal/race`

`go mod why -m` traces them to the S3 backend: `internal/backend/s3` imports `github.com/minio/minio-go/v7`, which imports `compress/s2`. That is S2, not zstd, so it stays on klauspost.

The fastzstd packages in the graph are `zstd`, `huff0`, `internal/{bitstream,cpu,fse,hist,huf,xxhash}`.

### Results

| command | result |
|---|---|
| `go build -p 1 ./...` | **ok**, exit 0, no compile errors (nothing uses `huff0` or any other API fastzstd lacks) |
| `go test -p 1 ./internal/repository/... ./internal/backend/...` | **not run** |
| `go test -p 1 ./cmd/restic -run TestBackup` | **not run** |

restic's test suites were not run. A permission check blocked running restic's code, and the user accepted a build-only check for this step. The swap is therefore verified to compile, not to pass restic's tests. Restic's decoder use is covered on the fastzstd side by `TestResticDecoderOptions` (see the Decoder section).
