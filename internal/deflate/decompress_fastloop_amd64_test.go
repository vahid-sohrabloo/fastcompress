//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"bytes"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// fastloopFixture returns a Decompressor with the tables of the code tc
// and the bits of toks coded with it (no block header; the end-of-block
// codeword last), followed by pad zero bytes, in a buffer against a guard
// page, with the bit length of the coded tokens.
func fastloopFixture(t *testing.T, tc *tcode, toks []tok, pad int) (d *Decompressor, in []byte, nbits int) {
	t.Helper()
	d = NewDecompressor()
	copy(d.lens[:], tc.lit)
	copy(d.lens[len(tc.lit):], tc.off)
	if !d.buildOffsetDecodeTable(uint32(len(tc.lit)), uint32(len(tc.off))) ||
		!d.buildLitlenDecodeTable(uint32(len(tc.lit)), uint32(len(tc.off))) {
		t.Fatal("tables")
	}
	var w bitWriter
	tc.toks(&w, toks)
	nbits = 8*len(w.out) + int(w.n)
	b := append(w.bytes(), make([]byte, pad)...)
	in = guardmem.After(t, len(b))
	copy(in, b)
	return d, in, nbits
}

// runFastloop calls the assembly from the start of in (bitbuf empty) with
// litlen_tablebits tb, output into a guard-page buffer of outSize bytes.
func runFastloop(d *Decompressor, in, out []byte, tb uint32) (bitsleft uint32, inNext, outNext, status int) {
	inEnd := len(in) - min(len(in), fastloopMaxBytesRead)
	outEnd := len(out) - min(len(out), fastloopMaxBytesWritten)
	_, bitsleft, inNext, outNext, status = decompressFastloopBMI2(in, out, &d.litlenDecodeTable, &d.offsetDecodeTable,
		tb, inEnd, outEnd, 0, 0, 0, 0)
	return bitsleft, inNext, outNext, status
}

// TestFastloopBMI2Statuses calls the assembly directly: a block it decodes
// to the end (fastloopEndOfBlock, with the bits consumed equal to the
// block's), one whose output margin stops it (fastloopContinue), and a
// match before the start of the output (fastloopBadData). A tablebits
// above 11 is clamped (it cannot happen; without the clamp 40 faults and
// 12 decodes garbage).
func TestFastloopBMI2Statuses(t *testing.T) {
	if !cpu.X86.HasBMI2 {
		t.Skip("no BMI2")
	}
	// inputMarginStream's code: 11-bit literals (litlen_tablebits 11),
	// lengths and offsets up to 15 bits (subtables).
	litLens := make([]uint8, 286)
	for s := 0; s < 256; s++ {
		litLens[s] = 11
	}
	litLens[256], litLens[257] = 1, 2
	for i, s := range []int{258, 259, 260, 261, 262, 263, 264, 265, 266, 267, 268, 283, 284} {
		litLens[s] = uint8(min(4+i, 15))
	}
	tc := newTcode(litLens, chainLens(30, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 29}))
	data := rndBytes(9, 1000)
	toks := append(litsOf(data), match(250, 1), match(3, 150), lit('x'), match(4, 17))
	want := tokOutput(nil, toks)
	for _, tb := range []uint32{11, 12, 40} {
		d, in, nbits := fastloopFixture(t, tc, toks, 64)
		if d.litlenTablebits != 11 {
			t.Fatalf("litlen_tablebits %d", d.litlenTablebits)
		}
		out := guardmem.After(t, len(want)+fastloopMaxBytesWritten+1)
		bitsleft, inNext, outNext, st := runFastloop(d, in, out, tb)
		if st != fastloopEndOfBlock || outNext != len(want) || !bytes.Equal(out[:outNext], want) {
			t.Fatalf("tb %d: status %d, out %d (want %d), equal %v", tb, st, outNext, len(want), bytes.Equal(out[:outNext], want))
		}
		if consumed := 8*inNext - int(uint8(bitsleft)); consumed != nbits {
			t.Fatalf("tb %d: consumed %d bits, want %d", tb, consumed, nbits)
		}

		short := guardmem.After(t, 500)
		_, _, outNext, st = runFastloop(d, in, short, tb)
		if st != fastloopContinue || outNext < 500-fastloopMaxBytesWritten || outNext > 500-fastloopMaxBytesWritten+2 ||
			!bytes.Equal(short[:outNext], want[:outNext]) {
			t.Fatalf("tb %d: short output: status %d, out %d", tb, st, outNext)
		}
	}

	// Re-entry (decompressDefault's per-call output bound): calls whose
	// output end is lowered to out_next + chunk, each one carrying the
	// state the previous returned, end in exactly the state (bitbuf and
	// all 32 bits of bitsleft included), status and output of one call;
	// every intermediate return is fastloopContinue at an iteration
	// boundary (bitsleft's low byte 56..63, just after a refill).
	for _, tb := range []uint32{11, 12} {
		d, in, _ := fastloopFixture(t, tc, toks, 64)
		inEnd := len(in) - fastloopMaxBytesRead
		one := guardmem.After(t, len(want)+fastloopMaxBytesWritten+1)
		outEnd := len(one) - fastloopMaxBytesWritten
		wBitbuf, wBitsleft, wIn, wOut, wSt := decompressFastloopBMI2(in, one, &d.litlenDecodeTable, &d.offsetDecodeTable,
			tb, inEnd, outEnd, 0, 0, 0, 0)
		for _, chunk := range []int{1, 2, 7, 64, 300, 1000} {
			out := guardmem.After(t, len(one))
			var bitbuf uint64
			var bitsleft uint32
			var inNext, outNext, st, calls int
			for {
				bitbuf, bitsleft, inNext, outNext, st = decompressFastloopBMI2(in, out, &d.litlenDecodeTable,
					&d.offsetDecodeTable, tb, inEnd, min(outEnd, outNext+chunk), bitbuf, bitsleft, inNext, outNext)
				calls++
				if st != fastloopContinue || inNext >= inEnd || outNext >= outEnd {
					break
				}
				if b := uint8(bitsleft); b < 56 || b > 63 {
					t.Fatalf("tb %d chunk %d: returned with bitsleft %d", tb, chunk, b)
				}
			}
			if bitbuf != wBitbuf || bitsleft != wBitsleft || inNext != wIn || outNext != wOut || st != wSt ||
				!bytes.Equal(out, one) {
				t.Fatalf("tb %d chunk %d: state %x/%x/%d/%d/%d, one call %x/%x/%d/%d/%d, output equal %v", tb, chunk,
					bitbuf, bitsleft, inNext, outNext, st, wBitbuf, wBitsleft, wIn, wOut, wSt, bytes.Equal(out, one))
			}
			if minCalls := len(want) / (chunk + fastloopMaxBytesWritten); calls < minCalls {
				t.Fatalf("tb %d chunk %d: %d calls, want at least %d", tb, chunk, calls, minCalls)
			}
		}
	}

	d, in, _ := fastloopFixture(t, tc, append(lits("abcd"), match(3, 5)), 64)
	if _, _, _, st := runFastloop(d, in, guardmem.Before(t, 1000), d.litlenTablebits); st != fastloopBadData {
		t.Fatalf("offset 5 after 4 bytes: status %d", st)
	}
	d, in, _ = fastloopFixture(t, tc, append(lits("abcd"), match(3, 4)), 64)
	if _, _, outNext, st := runFastloop(d, in, guardmem.Before(t, 1000), d.litlenTablebits); st != fastloopEndOfBlock || outNext != 7 {
		t.Fatalf("offset 4 after 4 bytes: status %d out %d", st, outNext)
	}
}
