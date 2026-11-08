# fastzstd

A pure-Go port of the C libzstd v1.5.7 compressor, exposing the public API of
`klauspost/compress/zstd`. No cgo and no dependencies.

Status: work in progress.

- **Phase 1, the encoder, is done:** `NewWriter`, `Encoder` (`EncodeAll`,
  the streaming `io.WriteCloser`), `EncodeTo`, `ZipCompressor` and the
  `EOption`s.
- **Phase 2, the decoder, is done:** `NewReader`, `Decoder` (`DecodeAll`,
  the streaming reader), the dictionary and trainer functions, and the zip and
  snappy helpers. Every identifier of `klauspost/compress/zstd` is exported
  (`scripts/apicheck.sh` passes in full mode). Decode speed is 101.1% of C
  libzstd v1.5.7 overall; two rows are under 95% (nci level 1 at 94.5%,
  silesia 16 KiB level 1 at 93.9%). See `bench/results/2026-11-05-phase2-final.md`.

## Encoder output

Each `EncoderLevel` runs C's compression level: `SpeedFastest` 1,
`SpeedDefault` 3, `SpeedBetterCompression` 7, `SpeedBestCompression` 11.
The output is byte-identical to C zstd v1.5.7 with the same parameters,
except that `WithEncoderDictRaw` writes its dictionary ID into the frame
header (C writes none for raw content); everything after that field is C's.

## Decoder

`NewReader` and `Decoder` (`DecodeAll`, the streaming `io.Reader`,
`WriteTo`), dictionaries, `ZipDecompressor` and `SnappyConverter` are
ported from C's `zstd_decompress*.c` and `huf_decompress.c`. Output and
error class are checked against C v1.5.7 by differential tests over a
corpus of C-made frames, and fuzz targets compare the paths against each
other (`DecodeAll`, streaming at any input cut, concurrent streaming, and
any `dst` capacity). C errors map to the klauspost sentinels; the table
and the deliberate deviations from C are in
[docs/superpowers/phase2-api-decisions.md](docs/superpowers/phase2-api-decisions.md).

### Safety

The decoder is written for untrusted input: corrupt, truncated or hostile
data returns an error, never a panic or an out-of-bounds access, and memory
is bounded by `WithDecoderMaxMemory`. CI enforces this:

- `scripts/decodercheck.sh`: no decoder file imports `unsafe` or calls
  `recover` (one exception: the streaming pipeline re-raises a panic of the
  caller's `io.Reader` on the caller's goroutine). Decoder assembly needs a
  `// BOUNDS:` proof per routine and a fuzz target comparing it with the Go
  code.
- `scripts/fuzz.sh`: every fuzz target runs for 20 s per push (amd64),
  and for 10 minutes nightly on amd64 and arm64, with the assembly on, off
  and (amd64) without BMI2. Every target's seed corpus also runs as a test,
  with and without the `noasm` tag.
- A coverage gate: `zstd_decompress*.go` and `huf_decompress.go` must each
  stay at 95% statement coverage or more.

## Deliberate API differences

- `WithHugePages` is an extension (not a klauspost option): it backs the
  large match tables with transparent huge pages on Linux. Speed only; off by
  default.
- `WithSingleSegment` is accepted and ignored: the frame header follows C's
  automatic choice, so output stays identical to C.
- `WithConcurrentBlocks` and `WithLowerEncoderMem` are accepted and have no
  effect. Encoding is single-threaded per call (C's `nbWorkers = 0`).
  `WithEncoderConcurrency(n)` caps the number of concurrent `EncodeAll` calls;
  streams are never async.
- `BuildDict` returns C's `ZDICT_finalizeDictionary` output. Non-zero
  `Offsets` replace C's starting repcodes {1, 4, 8} (the history is
  zero-padded in front up to the largest); `CompatV155` changes nothing, as
  C's dictionaries already load in zstd v1.5.5.
- `TrainDict`, `TrainDictCover`, `TrainDictFastCover` and `CoverParams`
  are extensions: C's dictionary trainers (`ZDICT_trainFromBuffer`, COVER
  and fastCover), byte-identical to C v1.5.7, parameters included. Levels
  that would make C compress with btultra, btultra2, the btopt post-block
  splitter or long-distance matching (not ported; C levels 13 and up)
  return an error.
- `EncoderLevelFromZstd` maps a zstd level to the highest `EncoderLevel`
  whose C level (1, 3, 7, 11) does not exceed it.

## Speed

On an i9-13900H (one core, checksums off), compression speed against C
`zstd -b` v1.5.7 over the Silesia corpus, as a geometric mean of the
percentage of C's speed: 101.1% at level 1, 101.3% at level 3, 102.1% at
level 7 and 101.2% at level 11 (101.4% overall; lowest row 97.9%). The
compressed sizes are identical in every row. Full tables:
[bench/results/2026-11-02-phase1-final.md](bench/results/2026-11-02-phase1-final.md).

## Credits

This is a port of [zstd](https://github.com/facebook/zstd). Credit goes to
Yann Collet and the zstd contributors. See LICENSE.
