# fastzstd

A pure-Go port of the C libzstd v1.5.7 compressor, exposing the public API of
`klauspost/compress/zstd`. Output is byte-identical to C zstd v1.5.7 for
equivalent parameters. No cgo and no dependencies.

Status: work in progress (phase 1: encoder).

## Credits

This is a port of [zstd](https://github.com/facebook/zstd). Credit goes to
Yann Collet and the zstd contributors. See LICENSE.
