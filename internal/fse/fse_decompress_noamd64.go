//go:build !amd64 || appengine || noasm || purego || !gc

package fse

import "github.com/vahid-sohrabloo/fastcompress/internal/bitstream"

// decompressFast leaves every stream to decompressUsingDTable's Go loops:
// the assembly is amd64 only.
func decompressFast(_ []byte, _ *bitstream.DStream, _, _ uint64, _ []decodeEntry) (int, bool, error) {
	return 0, false, nil
}
