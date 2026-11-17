# zstd/testdata

| File | Content | Provenance and licence |
|---|---|---|
| `train.dict` | zstd dictionary, 112640 bytes | Trained with `zstd --train` (v1.5.7) on the output of `gentrain.go`: synthetic text from a seeded generator. Contains nothing but that output and zstd's dictionary headers and tables. |
| `train-12k.dict` | zstd dictionary, 12288 bytes | As `train.dict`, with `--maxdict=12288`. |
| `gentrain.go` | Sample generator | Written for this repository. `go run zstd/testdata/gentrain.go DIR`; the exact commands are in `zstd/dict_test.go`. |
| `dickens-32k.raw` | Raw dictionary content | The first 32 KiB of Charles Dickens' collected works (as in the Silesia corpus's `dickens`), including the Project Gutenberg header. The text is in the public domain. |
| `decode/` | Decoder fixtures | See the generators in `zstd/*_test.go` and `internal/creftest`. |
