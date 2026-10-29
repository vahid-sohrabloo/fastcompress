package zstd

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkEncodeAll compresses each Silesia file in bench/corpus (see
// ref/fetch.sh) with EncodeAll, one sub-benchmark per file and level. It is
// meant for pprof, e.g.:
//
//	go test ./zstd -run xxx -bench 'EncodeAll/xml/1' -cpuprofile cpu.prof
//
// bench/cmd/gobench and bench/compare.sh are the harness against C.
func BenchmarkEncodeAll(b *testing.B) {
	files, _ := filepath.Glob(filepath.Join("..", "bench", "corpus", "silesia", "*"))
	if len(files) == 0 {
		b.Skip("bench/corpus/silesia missing; run ref/fetch.sh")
	}
	levels := []struct {
		name string
		l    EncoderLevel
	}{
		{"1", SpeedFastest}, {"3", SpeedDefault}, {"7", SpeedBetterCompression}, {"11", SpeedBestCompression},
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(filepath.Base(f), func(b *testing.B) {
			for _, lv := range levels {
				b.Run(lv.name, func(b *testing.B) {
					enc, err := NewWriter(nil, WithEncoderLevel(lv.l), WithEncoderCRC(false))
					if err != nil {
						b.Fatal(err)
					}
					dst := make([]byte, 0, enc.MaxEncodedSize(len(src)))
					b.SetBytes(int64(len(src)))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						dst = enc.EncodeAll(src, dst[:0])
					}
				})
			}
		})
	}
}
