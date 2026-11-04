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

// BenchmarkDecodeAll decodes the compare.sh corpus (each Silesia file whole,
// silesia.tar in independent 1M, 4M, 128K and 16K blocks) with one DecodeAll
// per block into a reused buffer, one sub-benchmark per file, level and
// block, e.g.:
//
//	go test ./zstd -run xxx -bench 'DecodeAll/silesia.tar/3/16K' -cpuprofile cpu.prof
//
// bench/cmd/gobench -mode d is the harness against C.
func BenchmarkDecodeAll(b *testing.B) {
	files, _ := filepath.Glob(filepath.Join("..", "bench", "corpus", "silesia", "*"))
	if len(files) == 0 {
		b.Skip("bench/corpus/silesia missing; run ref/fetch.sh")
	}
	type block struct {
		name string
		size int // 0 = whole file
	}
	whole := []block{{"whole", 0}}
	tarBlocks := []block{{"1M", 1 << 20}, {"4M", 4 << 20}, {"128K", 128 << 10}, {"16K", 16 << 10}}
	tar := filepath.Join("..", "bench", "corpus", "silesia.tar")
	if _, err := os.Stat(tar); err == nil {
		files = append(files, tar)
	}
	levels := []struct {
		name string
		l    EncoderLevel
	}{
		{"1", SpeedFastest}, {"3", SpeedDefault}, {"7", SpeedBetterCompression}, {"11", SpeedBestCompression},
	}
	dec, err := NewReader(nil, WithDecoderConcurrency(1))
	if err != nil {
		b.Fatal(err)
	}
	defer dec.Close()
	for _, f := range files {
		blocks := whole
		if f == tar {
			blocks = tarBlocks
		}
		b.Run(filepath.Base(f), func(b *testing.B) {
			src, err := os.ReadFile(f)
			if err != nil {
				b.Fatal(err)
			}
			for _, lv := range levels {
				for _, bl := range blocks {
					block := bl.size
					if block <= 0 || block > len(src) {
						block = len(src)
					}
					b.Run(lv.name+"/"+bl.name, func(b *testing.B) {
						enc, err := NewWriter(nil, WithEncoderLevel(lv.l), WithEncoderCRC(false))
						if err != nil {
							b.Fatal(err)
						}
						var frames [][]byte
						for off := 0; off < len(src); off += block {
							frames = append(frames, enc.EncodeAll(src[off:min(off+block, len(src))], nil))
						}
						out := make([]byte, 0, block)
						b.SetBytes(int64(len(src)))
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							for _, fr := range frames {
								if out, err = dec.DecodeAll(fr, out[:0]); err != nil {
									b.Fatal(err)
								}
							}
						}
					})
				}
			}
		})
	}
}
