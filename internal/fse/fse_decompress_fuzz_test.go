package fse

import (
	"bytes"
	"testing"
)

// FuzzReadNCount feeds arbitrary headers to ReadNCount, and arbitrary FSE
// streams to DecompressWksp: neither may panic, a header must not be
// longer than its input, and a table that WriteNCount can write reads back
// identically.
func FuzzReadNCount(f *testing.F) {
	for _, s := range []struct {
		maxSV uint8
		hb    []byte
	}{
		{255, nil}, {35, []byte{0x30, 0x6f, 0x9b, 0x03}}, {52, []byte{0x0}},
		{31, bytes.Repeat([]byte{0xff}, 16)}, {255, []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}},
	} {
		f.Add(s.maxSV, s.hb)
	}
	// encoder-written headers and streams
	for _, src := range [][]byte{[]byte("aaaabbbcccddeeeffffggggggg hello hello hello"), bytes.Repeat([]byte{1, 2, 3, 1, 1, 1, 2}, 50)} {
		dst := make([]byte, 1024)
		if n, err := Compress2(dst, src, 255, 6); err == nil && n > 1 {
			f.Add(uint8(255), dst[:n])
		}
	}
	f.Fuzz(func(t *testing.T, maxSV uint8, hb []byte) {
		var norm [256]int16
		m := uint(maxSV)
		tableLog, size, err := ReadNCount(norm[:], &m, hb)
		if err == nil {
			if size > max(len(hb), 0) {
				t.Fatalf("header size %d > input %d", size, len(hb))
			}
			if m > uint(maxSV) {
				t.Fatalf("maxSymbol %d > %d", m, maxSV)
			}
			if tableLog >= MinTableLog && tableLog <= MaxTableLog {
				buf := make([]byte, NCountWriteBound(m, tableLog))
				if n, werr := WriteNCount(buf, norm[:m+1], m, tableLog); werr == nil {
					var again [256]int16
					m2 := uint(255)
					tl2, _, err2 := ReadNCount(again[:], &m2, buf[:n])
					if err2 != nil || tl2 != tableLog || m2 > m || again != norm {
						t.Fatalf("rewritten table reads back differently: %v", err2)
					}
				}
			}
		}
		out := make([]byte, 300)
		if n, err := DecompressWksp(out, hb, 6); err == nil && n > len(out) {
			t.Fatalf("decoded %d > capacity", n)
		}
	})
}
