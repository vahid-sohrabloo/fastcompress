# Phase 2 API TODO

Identifiers in `api/klauspost-zstd.txt` that `scripts/apicheck.sh` (full mode)
still reports as missing after Phase 1. All are decoder-side; the decoder is
Phase 2.

## DECISION NEEDED: InspectDictionary

`InspectDictionary(b []byte) (interface{ ...; LitEncoder() *huff0.Scratch; ... }, error)`
returns an interface whose method `LitEncoder() *huff0.Scratch` uses a type
from klauspost's `huff0` package. An exact drop-in signature is impossible
without depending on klauspost (forbidden: stdlib only), so the user must
decide: omit it, or define a different (non-identical) type for that method
and drop it from the strict snapshot comparison.

## Decoder

- `type Decoder`, `func NewReader(r io.Reader, opts ...DOption) (*Decoder, error)`: streaming/block frame decoder.
- `Decoder` methods `Close`, `DecodeAll`, `IOReadCloser`, `Read`, `Reset`, `ResetWithOptions`, `WriteTo`: decoder API.
- `func DecodeTo(dst, src []byte) ([]byte, error)`: package-level helper on a shared default Decoder.
- `func ZipDecompressor(opts ...DOption) func(r io.Reader) io.ReadCloser`: zip decompressor on top of Decoder.

## Decoder options

- `type DOption func(*decoderOptions) error`: needs a `decoderOptions` struct.
- `IgnoreChecksum(bool)`: skip content checksum verification.
- `WithDecodeAllCapLimit(bool)`: cap DecodeAll output to the dst capacity.
- `WithDecodeBuffersBelow(int)`: size threshold for decoding synchronously.
- `WithDecoderConcurrency(int)`: concurrent decoders.
- `WithDecoderDictDelete(ids ...uint32)`: remove dictionaries by ID.
- `WithDecoderDictRaw(id uint32, content []byte)`: register a raw-content dictionary.
- `WithDecoderDicts(dicts ...[]byte)`: register zstd-format dictionaries.
- `WithDecoderLowmem(bool)`: low-memory mode.
- `WithDecoderMaxMemory(uint64)`: limit total decoded memory.
- `WithDecoderMaxWindow(uint64)`: limit accepted window size.

## Frame header

- `type Header` (fields Compressed, CompressedSize, DecompressedSize, DictionaryID, FirstBlock, FrameContentSize, HasCheckSum, HasFCS, HeaderSize, Last, OK, SingleSegment, Skippable, SkippableID, SkippableSize, WindowSize): parsed frame header.
- `Header` methods `AppendTo`, `Decode`, `DecodeAndStrip`: parse/serialize frame headers.
- `const HeaderMaxSize = 14 + 3`: maximum header size; ships with Header.

## Dictionaries and conversion

- `func BuildDict(o BuildDictOptions) ([]byte, error)` and `type BuildDictOptions` (CompatV155, Contents, DebugOut, History, ID, Level, Offsets): dictionary builder; needs the encoder's entropy-table construction, not a decoder.
- `func InspectDictionary`: see the decision note above.
- `type SnappyConverter` with `Convert(in io.Reader, w io.Writer) (int64, error)`: snappy-to-zstd stream converter.

## Decoder errors

`ErrBlockTooSmall`, `ErrCRCMismatch`, `ErrCompressedSizeTooBig`, `ErrDecoderClosed`,
`ErrDecoderNilInput`, `ErrDecoderSizeExceeded`, `ErrFrameSizeExceeded`,
`ErrFrameSizeMismatch`, `ErrMagicMismatch`, `ErrReservedBlockType`,
`ErrSnappyCorrupt`, `ErrSnappyTooLarge`, `ErrSnappyUnsupported`,
`ErrUnexpectedBlockSize`, `ErrUnknownDictionary`, `ErrWindowSizeExceeded`,
`ErrWindowSizeTooSmall`: returned by the decoder and Snappy converter.
