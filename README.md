# fastzstd

A pure-Go port of the C libzstd v1.5.7 compressor, exposing the public API of
`klauspost/compress/zstd`. No cgo and no dependencies.

Status: work in progress.

- **Phase 1, the encoder, is done:** `NewWriter`, `Encoder` (`EncodeAll`,
  the streaming `io.WriteCloser`), `EncodeTo`, `ZipCompressor` and the
  `EOption`s.
- **Phase 2, the decoder, is in progress.** `NewReader`, `Decoder`,
  `DecodeTo`, `BuildDict`, `ZipDecompressor` and the other decoder-side API
  are not there yet, so the package cannot yet replace `klauspost/compress/zstd`
  in code that decompresses.

## Encoder output

Each `EncoderLevel` runs C's compression level: `SpeedFastest` 1,
`SpeedDefault` 3, `SpeedBetterCompression` 7, `SpeedBestCompression` 11.
The output is byte-identical to C zstd v1.5.7 with the same parameters,
except that `WithEncoderDictRaw` writes its dictionary ID into the frame
header (C writes none for raw content); everything after that field is C's.

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
