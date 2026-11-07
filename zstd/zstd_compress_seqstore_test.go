package zstd

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// TestSeqCodesAsm compares seqCodes with the assembly on and off over
// random sequences: literal and match lengths around the table limits (63,
// 127) and up to 0xFFFF, offBase from 1 to 2^32-1.
func TestSeqCodesAsm(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 32))
	for _, n := range []int{1, 2, 3, 7, 8, 100, 1000} {
		seqs := make([]seqDef, n)
		for i := range seqs {
			seqs[i] = seqDef{
				offBase:   1 + r.Uint32N(1<<uint(r.IntN(32))),
				litLength: uint16(r.IntN(1 << uint(r.IntN(17)))),
				mlBase:    uint16(r.IntN(1 << uint(r.IntN(17)))),
			}
		}
		seqs[0].litLength, seqs[n-1].mlBase = 63, 127
		if n > 2 {
			seqs[1].litLength, seqs[1].mlBase, seqs[2].offBase = 64, 128, 1<<32-1
		}
		var codes [2][3][]byte
		for i, on := range []bool{false, true} {
			for j := range codes[i] {
				codes[i][j] = make([]byte, n)
			}
			withAsm(t, on, func() { seqCodes(seqs, codes[i][0], codes[i][1], codes[i][2]) })
		}
		for j, name := range []string{"ll", "of", "ml"} {
			if !bytes.Equal(codes[0][j], codes[1][j]) {
				t.Fatalf("n %d: %s codes differ with the assembly", n, name)
			}
		}
	}
}
