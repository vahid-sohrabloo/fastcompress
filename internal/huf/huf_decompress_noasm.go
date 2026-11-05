//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package huf

func decompress4X1FastLoop(args *decompressFastArgs, _ *DTable, _ int) { decompress4X1FastCLoop(args) }

func decompress4X2FastLoop(args *decompressFastArgs, _ *DTable, _ int) { decompress4X2FastCLoop(args) }
