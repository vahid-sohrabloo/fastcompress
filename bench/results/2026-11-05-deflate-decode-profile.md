# T1.4: decode baseline, profile summary and asm shortlist (2026-11-05)

Source: `bench/results/2026-11-05-deflate-decode-go.md` (raw table, gate, full header). Generic pure-Go decoder
(`internal/deflate`, Phase 1, no asm) vs libdeflate v1.26 as shipped (runtime BMI2 decompressor, `-O2`),
i9-13900H pinned to CPU 2, load1 1.56 -> 1.60, AC online, no other benchmark or test running, `-i 3 -g 3 -n 3`,
all 12 Silesia files whole plus silesia.tar in 1M / 128K / 16K independent blocks, levels 1 / 6 / 9 / 12.
Each Go cell decodes exactly the stream libdeflate produced for the same level and blocks (round trip verified
before timing).

## Result

| level | rows | geomean % of C | min row |
|---|---|---|---|
| 1 | 15 | 61.4% | 51.2% (reymont/whole) |
| 6 | 15 | 61.8% | 56.5% (reymont/whole) |
| 9 | 15 | 63.0% | 58.2% (nci/whole) |
| 12 | 15 | 61.1% | 53.8% (dickens/whole) |

Gate (geomean >= 100%, rows >= 95%, hard floor 90%): FAIL at every level, as expected for a straight port without
BMI2. Best rows are the binary-ish files (x-ray 75%, sao 71%); text and XML are the worst (~51-60%). Absolute Go
speeds are 390-1700 MB/s vs C 580-2900 MB/s.

## Profile (pprof, one extra run per row, 3 s)

* `Decompressor.decompressDefault` (the fast + slow loops) is 79-86% flat.
* `encoding/binary` `PutUint64`/`Uint64` (inlined 8-byte stores/loads in the match copy and refill) add 12-19%.
* `buildDecodeTable` and the table wrappers: ~1% even at 16K blocks (13k table builds), so table construction needs
  no work.
* No allocation or runtime overhead is visible (0 allocs/op, confirmed by test).

## Asm shortlist for Phase 4 (decoder), in priority order

1. **amd64 BMI2 fast loop** replacing the body of the fast loop in `decompress_generic.go` (the equivalent of
   libdeflate's `deflate_decompress_bmi2`, which the C baseline uses): `shrx`/`shlx`/`bzhi` for the bit extraction
   (Go at GOAMD64=v1 cannot emit them), the literal/length/offset decode chain with the preloaded entry, and the
   three match-copy cases with 8/16-byte moves. Must keep the 299-byte output and 25-byte input margins so the
   generic slow loop finishes the tail, exactly as the Go fast loop does now (BOUNDS block from those margins).
2. Match copy only (if 1 alone falls short): word-copy of the overlapping-offset cases.
3. arm64 fast loop (last; only testable on the CI arm runner).

Gate check after (1): all rows >= 90% hard floor first, then 95% / 100% geomean; re-profile before choosing anything
else.

Raw profiles were written to the session scratchpad (`.../scratchpad/prof/<file>.<block>.L<level>.d.cpu.prof`); the
workflow to regenerate them is in `bench/gzip/README.md`.
