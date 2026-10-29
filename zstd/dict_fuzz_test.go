package zstd

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// FuzzEncoderDict builds an Encoder from an arbitrary dictionary (the low
// bits of mode pick the level, bit 2 raw content) and compresses src with
// it. Nothing may panic. When the dictionary is accepted, C must accept it
// too, the frame must equal C's (`cref compress -dict`) and decode with C
// (`zstd -d -D`; skipped for raw content starting with the dictionary
// magic, which the zstd program can only load as a zstd dictionary).
// The seeds are train.dict, its truncations and single-bit flips in its
// header; the >16 MiB repcode case is TestDictRepBeyondLoadedContent.
func FuzzEncoderDict(f *testing.F) {
	dict := readTestdata(f, "train.dict")
	src := readTestdata(f, "dickens-32k.raw")[:3000]
	f.Add(uint8(0), dict, src)
	f.Add(uint8(3), readTestdata(f, "train-12k.dict"), src[:200])
	f.Add(uint8(5), dict[:100], src)
	for i, n := range []int{8, 12, 40, 100, 200, 300, 600, 1000, len(dict) / 2, len(dict) - 1} {
		f.Add(uint8(i), dict[:n], src[:500])
	}
	var bs compressedBlockState
	eSize, err := loadCEntropy(&bs, dict)
	if err != nil {
		f.Fatal(err)
	}
	for i, bit := range []int{8*8 + 3, 9*8 + 1, 40*8 + 5, 70*8 + 2, 120*8 + 7, (eSize-20)*8 + 4, (eSize-12)*8 + 30, (eSize-5)*8 + 1} {
		d := bytes.Clone(dict[:eSize+4000])
		d[bit/8] ^= 1 << (bit % 8)
		f.Add(uint8(i), d, src)
	}
	f.Fuzz(func(t *testing.T, mode uint8, dict, src []byte) {
		if len(src) > 1<<16 || len(dict) > 1<<20 {
			return
		}
		lv := encoderLevels[mode&3]
		raw := mode&4 != 0
		d := dictCase{dict: dict, raw: raw}
		e, err := NewWriter(nil, WithEncoderLevel(lv.l), d.option())
		if err != nil {
			return
		}
		got := e.EncodeAll(src, nil)
		if len(src) == 0 {
			return
		}
		want, err := creftest.RunErr(t, src, append([]string{"compress", "-level", strconv.Itoa(lv.c), "-checksum", "1", "-pledged", "1"}, d.crefArgs(t)...)...)
		if err != nil {
			t.Fatalf("accepted dictionary rejected by C: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("L%d raw %v: frame differs from C", lv.c, raw)
		}
		if raw && len(dict) >= 4 && binary.LittleEndian.Uint32(dict) == dictMagic {
			return // the zstd program would load it as a zstd dictionary, not as raw content
		}
		dec, err := creftest.DecodeCDict(t, got, dict)
		if err != nil || !bytes.Equal(dec, src) {
			t.Fatalf("C decode: %v", err)
		}
	})
}
