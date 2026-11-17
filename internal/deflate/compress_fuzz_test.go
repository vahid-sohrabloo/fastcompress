package deflate

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// fuzzFlushRec turns fuzz bytes into a dump-flush record:
//
//	data[0]  level (mod 13)
//	data[1]  bits 0-2 initial bitcount, bits 3-4 start, bits 5-7 cap mode
//	         (0-3 big, 4 exact, 5 one byte short, 6-7 in between by data[2])
//	data[3:] ops: op&3 0,1: literal data[i+1]; op&3 2: match of length
//	         3 + data[i+1], offset 1 + (u16 data[i+2:] mod the history) (a
//	         literal without history or room); op&3 3: end the block, final
//	         if op&4 (empty blocks are skipped).
//
// Matches copy real data, so a complete stream (start 0, bitcount 0, last
// block final) decodes.
func fuzzFlushRec(t testing.TB, g *fbGo, data []byte) *fbRec {
	for len(data) < 3 {
		data = append(data, 0)
	}
	rec := &fbRec{level: int(data[0] % 13), bitcount: uint(data[1] & 7), start: int(data[1]>>3) & 3}
	rec.bitbuf = data[2] & byte(1<<rec.bitcount-1)
	var all []byte
	var cur fbBlock
	blockStart := 0
	end := func(final bool) {
		if len(all) > blockStart {
			cur.data, cur.final = all[blockStart:], final
			rec.blocks = append(rec.blocks, cur)
		}
		cur, blockStart = fbBlock{}, len(all)
	}
	ops := data[3:]
	for i := 0; i < len(ops); {
		op := ops[i]
		arg := func(k int) byte {
			if i+k < len(ops) {
				return ops[i+k]
			}
			return 0
		}
		blockLen := len(all) - blockStart
		switch op & 3 {
		case 0, 1:
			all = append(all, arg(1))
			cur.toks = append(cur.toks, fbTok{})
			i += 2
		case 2:
			ln := 3 + int(arg(1))
			hist := len(all)
			if hist == 0 || blockLen+ln > maxBlockLength {
				all = append(all, arg(1))
				cur.toks = append(cur.toks, fbTok{})
			} else {
				off := 1 + int(binary.LittleEndian.Uint16([]byte{arg(2), arg(3)}))%min(hist, deflateMaxMatchOffset)
				for range ln {
					all = append(all, all[len(all)-off])
				}
				cur.toks = append(cur.toks, fbTok{uint16(ln), uint16(off)})
			}
			i += 4
		case 3:
			end(op&4 != 0)
			i++
		}
		if len(all)-blockStart >= maxBlockLength {
			end(false)
		}
	}
	end(true)
	if len(rec.blocks) == 0 {
		rec.blocks = []fbBlock{{true, []byte{data[0]}, []fbTok{{}}}}
	}
	mode := int(data[1] >> 5)
	switch {
	case mode < 4:
		mode = 0
	case mode == 4:
		mode = 1
	case mode == 5:
		mode = 2
	default:
		mode = 3 + int(data[2]%250)
	}
	chooseCap(t, g, rec, mode)
	return rec
}

func FuzzFlushBlock(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 'a'})
	f.Add([]byte{6, 0, 0, 0, 'a', 0, 'b', 2, 10, 1, 0, 2, 255, 0, 0, 7})
	f.Add([]byte{12, 7<<5 | 7, 3, 1, 'x', 2, 255, 0, 0, 2, 255, 0, 0, 3})
	f.Add([]byte{3, 5 << 5, 0, 0, 1, 1, 2, 1, 3, 3, 1, 4, 7, 1, 5})
	r := rand.New(rand.NewPCG(23, 1))
	for i := range 12 {
		d := []byte{byte(i), byte(r.Uint32()), byte(r.Uint32())}
		for range 50 + r.IntN(3000) {
			d = append(d, byte(r.Uint32()&^1|uint32(i&1)), byte('a'+r.IntN(3+i*20)))
		}
		f.Add(d)
	}
	var g fbGo
	f.Fuzz(func(t *testing.T, data []byte) {
		creftest.LDBin(t)
		rec := fuzzFlushRec(t, &g, data)
		gr := g.run(t, rec)
		if err := compareFlush(gr, ldFlush(t, []*fbRec{rec})[0]); err != nil {
			t.Fatalf("level %d, %d blocks, cap %d: %v", rec.level, len(rec.blocks), rec.cap, err)
		}
		if !gr.overflow && rec.decodable() {
			if err := checkDecode(t, fbStream(gr), rec.stream()); err != nil {
				t.Fatal(err)
			}
		}
	})
}
