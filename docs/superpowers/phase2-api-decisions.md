# Phase 2 API decisions

Replaces `phase2-api-todo.md`. The list of identifiers still to port is now
`scripts/apicheck.sh --missing` (77 lines at the start of Phase 2).
Line references (`doc:N`) are lines of `api/klauspost-zstd-doc.txt`
(klauspost/compress v1.20.3, `go doc -all` output).

## User decisions (2026-11-02)

These replace the earlier "Open questions" section. Each answer below is binding.

1. **`huff0.Scratch`: option A.** Add a public package `github.com/vahid-sohrabloo/fastzstd/huff0` that exports only an opaque `type Scratch struct{ /* unexported */ }`. The README documents the gap: code that reads Scratch's fields or calls klauspost huff0 functions will not compile.
2. **klauspost doc text: committed.** Use the recommendation: commit `api/klauspost-zstd-doc.txt` in Task 1. This was not asked; it is the designer's call, open to override.
3. **BuildDict: also port COVER/fastCover.** The user chose to port C's dictionary trainers (`cover.c`, `fastcover.c`, `zdict.c`'s `ZDICT_trainFromBuffer*`, and `divsufsort.c` for the legacy trainer only if needed) on top of `ZDICT_finalizeDictionary`. That adds an extension API beyond klauspost's surface. Task 16b defines it, and it is listed in `api/fastzstd-extensions.txt`.
4. **Sentinel mapping: as recommended.** Approve the C-error mapping table under Decisions. This was not asked; it is the designer's call, open to override.
5. **Concurrency: as recommended.** `DecodeAll` stays single-threaded per call, and `WithDecoderConcurrency(n)` caps the number of concurrent `DecodeAll` calls. Streaming with n > 1 runs the 3-stage pipeline, and its output and errors are identical to n == 1.
6. **Speed gate: as recommended.** Per level, the geo-mean is at least 100% of C (`zstd -b<L> -T1` decompression), every row is at least 95%, and 90% is the hard floor. Rows below 100% after the perf rounds go back to the user.

---

## Decisions (designer calls; open to override)

### Error mapping (C error → sentinel)

`TestErrorsIs` pins every row. Errors that match no row are returned as an unexported `*corruptError{code dErr, where string}`, which matches none of the sentinels. `errors.Is(err, io.ErrUnexpectedEOF)` holds for truncation.

| Condition (C v1.5.7 location) | Returned error |
|---|---|
| Bad magic at a frame start (`prefix_unknown`, `ZSTD_getFrameHeader_advanced`) | `ErrMagicMismatch` |
| Window > `min(MaxWindow, MaxMemory)` (`frameParameter_windowTooLarge`). Also applied in `DecodeAll` (a deviation: C's one-shot path doesn't check it) | `ErrWindowSizeExceeded` |
| A match offset beyond the available history plus dictionary (`corruption_detected` in `ZSTD_execSequence*`, "offset beyond prefix/dict") | `ErrWindowSizeTooSmall` |
| Block type 3 (`ZSTD_getcBlockSize`, `corruption_detected`) | `ErrReservedBlockType` |
| Compressed block size > `ZSTD_blockSizeMax(dctx)` (`srcSize_wrong` in `ZSTD_decompressBlock_internal`) | `ErrCompressedSizeTooBig` |
| Block content shorter than its literals or sequences headers need (`srcSize < MIN_CBLOCK_SIZE`, `lhSize+litCSize > srcSize`, a seq header beyond the block) | `ErrBlockTooSmall` |
| A block regenerating more than `min(window, 128 KiB)`, or an RLE/raw block size above that (`dstSize_tooSmall` or `corruption_detected` in the block paths) | `ErrUnexpectedBlockSize` |
| Output grows beyond FCS (`corruption_detected`, `ZSTD_decompressFrame`) | `ErrFrameSizeExceeded` |
| Frame ends with output ≠ FCS (same C check, short side) | `ErrFrameSizeMismatch` |
| Checksum mismatch (`checksum_wrong`) | `ErrCRCMismatch` |
| Frame dictID not registered (`dictionary_wrong`, `ZSTD_decodeFrameHeader`) | `ErrUnknownDictionary` |
| Output would exceed `WithDecoderMaxMemory`, or `cap(dst)-len(dst)` under `WithDecodeAllCapLimit` | `ErrDecoderSizeExceeded` |
| Input ends inside a frame (`srcSize_wrong` at end of input, or C's streaming "need more input" at EOF) | `io.ErrUnexpectedEOF` |
| Use after `Close` | `ErrDecoderClosed` |
| `Read`/`WriteTo` with no reader (`NewReader(nil)` without `Reset`) | `ErrDecoderNilInput` |
| Snappy: bad chunk, CRC mismatch, bad copy offset or length, bad varint | `ErrSnappyCorrupt` |
| Snappy: a chunk decodes to more than 65536 bytes (framing spec) | `ErrSnappyTooLarge` |
| Snappy: a reserved unskippable chunk type 0x02–0x7f | `ErrSnappyUnsupported` |

### Concurrency (`WithDecoderConcurrency`)

C's decoder is single-threaded. The design adds threads only at points C's own structure already separates. The doc text decides the defaults: n=0 means GOMAXPROCS, and the default is min(4, GOMAXPROCS).

- **`DecodeAll`:** each call is one C `ZSTD_decompressMultiFrame` on one `dctx`. The `Decoder` owns a pool of `n` `dctx` objects behind a buffered channel used as a semaphore. Call n+1 blocks until one is free. No goroutines are spawned. The result is identical for any n.
- **Streaming, n == 1:** synchronous C `ZSTD_decompressStream`. `Read` and `WriteTo` are thin loops over it. No goroutines.
- **Streaming, n > 1:** a pipeline started lazily on the first `Read`/`WriteTo` after `Reset`. Channels with capacity n bound it, so at most n blocks are in flight between any two stages.
  - **S1 framer** (goroutine). Reads the `io.Reader` and cuts the input into units with C's bufferless state machine (`ZSTD_nextSrcSizeToDecompress`, `ZSTD_nextInputType`):
    - frame header
    - one compressed, raw or RLE block (≤ 128 KiB + 3)
    - checksum
    - skippable frame (skipped)

    It validates the frame header and window here, so the window buffer is sized before any data flows.
  - **S2 entropy** (goroutine, serial within a frame). For compressed blocks it runs `ZSTD_decodeLiteralsBlock` and `ZSTD_decodeSeqHeaders`, then `ZSTD_decodeSequence` for every sequence. The result is literals plus a `[]seq` with offsets already resolved through the repcode history.
    - This is C's own split: `ZSTD_decompressSequencesLong` already decodes sequences ahead of executing them.
    - It is legal because entropy state (Huffman table for treeless literals, FSE repeat tables, rep offsets) depends only on earlier blocks' *headers and sequences*, never on their output.
  - **S3 executor** (the caller's goroutine inside `Read`/`WriteTo`):
    - executes the sequences into the window buffer (C's `ZSTD_execSequence*` with S2's output), and copies raw blocks and expands RLE blocks
    - updates XXH64 and checks the checksum unit
    - hands the produced bytes to the caller
  - Window plus in-flight memory ≈ `window + (2n+2) × 128 KiB`. A `Close` or `Reset` cancels S1 and S2 through a done channel and drains them before returning.
  - **Equivalence rule.** Bytes and the error are identical to n == 1. An error from S1 or S2 is delivered only after S3 has delivered every byte that precedes the error position.
- **`WithDecodeBuffersBelow(size)`:** on `Reset(r)`, if `r` has `Bytes() []byte` and `Len() int` and `Len() < size`, the decoder runs `DecodeAll` into an internal buffer and serves `Read`/`WriteTo` from it (doc semantics).
- **`WithDecoderLowmem(true)`:** releases `dctx` and stream buffers above 1 MiB after each `DecodeAll` or frame, instead of keeping them.

### Other decisions

- **`DecodeAll` per frame** follows C's `ZSTD_decompressMultiFrame`:
  - skip skippable frames
  - run `findFrameSizeInfo` to get the compressed size and `decompressedBound`
  - check the header, the window and the dictionary
  - grow `dst` by `min(FCS or bound, remaining limit)` and run `decompressFrame` into `dst[len:cap]`

  Writes stay inside `dst`'s capacity. `dst[:len(dst)]` is never modified.
- **Dictionary selection** (C `ZSTD_d_refMultipleDDicts` plus `ZSTD_DDictHashSet`):
  - A frame with dictID ≠ 0 uses the registered dictionary with that ID, or fails with `ErrUnknownDictionary`.
  - A frame with dictID 0 uses the dictionary registered with ID 0 (only `WithDecoderDictRaw(0, …)` can do that), or no dictionary.
- **Defaults** come from the doc text: max window = `MaxWindowSize` (1<<29), not C's windowLogMax 27, and max memory 64 GiB. Differential tests run cref with `-max-window-log 29`.
- **`BuildDict`:**
  - ID 0 → C's XXH64-derived compliant ID.
  - Zero `Offsets` → C's `repStartValue` {1,4,8}, giving byte-identical output.
  - Non-zero `Offsets` → written instead. A commented deviation; `minContentSize` is the maximum of the offsets.
  - `Level` maps through Phase 1's table (`EncoderLevel` 0 → C level 3).
  - `CompatV155`: C's `ZDICT_finalizeDictionary` output already loads in zstd ≤ v1.5.5, so both values produce C's bytes. Task 16 verifies this against `/usr/bin/zstd` (v1.5.5).
  - `DebugOut` receives C's `DISPLAYLEVEL` messages at notification level 4.
- **Snappy:** each snappy chunk is decoded into a 64 KiB scratch buffer, which is needed to validate offsets and to turn copies shorter than 3 bytes into literals. Its *parse* (literal and copy elements) becomes the zstd sequences directly: `offBase = offset + 3`, no repcodes, and no match search. Those sequences are encoded with Phase 1's `entropyCompressSeqStore` as one zstd block per chunk, inside one frame (FCS unknown, window 64 KiB, no checksum). CRC-32C is checked on every chunk, since the data is decoded anyway. That is stricter than "not all CRC values are checked", but still conforming.

---

## Doc-comment facts later tasks rely on

| Topic | Fact | Doc line |
|---|---|---|
| `WithDecoderMaxMemory` | Max decoded size for in-memory non-streaming ops, or max window for streaming. Maximum `1 << 63`. Default 64 GiB. Changeable with `ResetWithOptions`. | doc:227-232 |
| `WithDecoderMaxWindow` | Default 512 MB (= `MaxWindowSize`, `1<<29`, doc:20). Maximum ~3.75 TB (spec). A lower `MaxMemory` wins. Changeable with `ResetWithOptions`. | doc:234-240 |
| `WithDecoderConcurrency` | n >= 0; 0 means GOMAXPROCS; default is min(4, GOMAXPROCS). Limits concurrent `DecodeAll` calls; for streams limits in-flight blocks; n == 1 means no async decoding. NOT changeable with `ResetWithOptions`. | doc:192-199 |
| `WithDecodeBuffersBelow` | Readers with `Bytes() []byte` and `Len() int` are fully decoded. `WithDecodeAllCapLimit` disables it, as does size <= 0. Default 128 KiB. NOT changeable with `ResetWithOptions`. | doc:185-190 |
| `WithDecodeAllCapLimit` | Limits `DecodeAll` to `cap(dst)-len(dst)` bytes, or `WithDecoderMaxMemory` if set. Disabled by default. Changeable with `ResetWithOptions`. | doc:179-183 |
| `WithDecoderLowmem` | Lower memory, possibly more allocation. NOT changeable with `ResetWithOptions`. | doc:222-225 |
| `IgnoreChecksum` | Changeable with `ResetWithOptions`. | doc:175-177 |
| `WithDecoderDicts` | Each slice is in `zstd --train` format; for equal IDs the last wins; changeable with `ResetWithOptions`. | doc:210-218 |
| `WithDecoderDictRaw` | Content may be arbitrary data; changeable with `ResetWithOptions`. | doc:205-208 |
| `WithDecoderDictDelete` | Removes by ID; no ids means delete all; meant for `ResetWithOptions`. | doc:201-203 |
| `Header` nesting | Top level: `SingleSegment`, `WindowSize`, `DictionaryID`, `HasFCS`, `FrameContentSize`, `Skippable`, `SkippableID`, `SkippableSize`, `HeaderSize`, `HasCheckSum`. Under `FirstBlock` (anonymous struct): `OK`, `Last`, `Compressed`, `DecompressedSize`, `CompressedSize`. `HasCheckSum` is top level, not under `FirstBlock`. | doc:508-580 (`FirstBlock` at 554-575, `HasCheckSum` at 579) |
| `Header` field semantics | `WindowSize` is set only if not `SingleSegment`. `SingleSegment` implies `FrameContentSize` is valid. `Skippable` implies `FirstBlock.OK` is false. `SkippableID` is 0-15. `HeaderSize` includes magic for normal frames, and for skippable frames counts magic plus the size field, excluding the payload. `FirstBlock.DecompressedSize` is 0 if unknown. `CompressedSize` excludes the block header and equals `DecompressedSize` when not compressed. | doc:509-574 |
| `Header.Decode` / `DecodeAndStrip` | Decodes the frame header plus the first block header if enough bytes. Recommends at least `HeaderMaxSize` bytes. Frame header unreadable returns an error. Not enough input returns `io.ErrUnexpectedEOF`. `FirstBlock.OK` says whether the first block header was available. `DecodeAndStrip` also returns the remaining bytes. | doc:587-602 |
| `Header.AppendTo` | Appends the encoded header; no error checking of the values. | doc:583-585 |
| `DecodeTo` | Appends to `dst`; the maximum decoded size is 1 GiB, not counting what is already in `dst`. | doc:109-111 |
| `ZipDecompressor` | `WithDecoderConcurrency(1)` is forced. Default max window is 128 MB (overridable by options). | doc:135-140 |
| `InspectDictionary` | Returns an interface with `ID() uint32`, `ContentSize() int`, `Content() []byte`, `Offsets() [3]int`, `LitEncoder() *huff0.Scratch` (doc:121-127). The doc says only "loads a zstd dictionary and provides functions to inspect the content" (doc:128-129); **it states nothing about raw (non-dictionary-format) content**. Behaviour on raw content is therefore unspecified by the doc and must be decided by Task 16 (proposal: return an error, since `WithDecoderDicts` requires `zstd --train` format, doc:214-215). |
| `SnappyConverter` | The converter can be reused to avoid allocations, even after errors. Deprecated in the doc. Converts directly without full decoding; no CRC is generated and not all input CRCs are checked (this port is stricter, see Snappy decision above). `Convert(in, w) (int64, error)`. | doc:604-621 |
| `BuildDictOptions` | `ID` dictionary ID; `Contents [][]byte` content for the tables; `History []byte` history for all blocks; `Offsets [3]int` offsets to use; `CompatV155` compatible with v1.5.5 and earlier (facebook/zstd#3724); `Level EncoderLevel` builds for that level, **"If not set SpeedBestCompression will be used"**; `DebugOut io.Writer` stats and details. | doc:145-170 |

### Points where the doc and the plan's Decisions differ (for the reviewer)

- **`BuildDictOptions.Level`:** the doc says an unset Level means `SpeedBestCompression`. The Decisions section says `EncoderLevel` 0 maps to C level 3. Task 16 must pick one and record it. The plan's choice (level 3) is a deviation from the doc.
- **`DecodeTo`:** the doc limit (1 GiB output) is not in the Decisions section. Task for `DecodeTo` must enforce it with `ErrDecoderSizeExceeded`.
- **`ZipDecompressor`:** the 128 MB default window is not in the Decisions section's Defaults; it overrides the 512 MB default for this entry point only.
- **`WithDecoderMaxWindow`:** the doc maximum is ~3.75 TB; the Decisions section's default of `1<<29` matches the doc default of 512 MB.
- **Error-mapping table:** sentinel messages are in `api/klauspost-zstd.txt` (the `Err*` lines in `apicheck.sh --missing`). The doc text has no per-error comments, so the table is a designer call not verifiable against doc text.
