//go:build amd64 && !appengine && !noasm && !purego && gc

package fse

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
	"github.com/vahid-sohrabloo/fastcompress/internal/guardmem"
)

// TestDecodeEntryLayout checks the cell layout decompressAsm reads: newState
// bits 0-15, symbol 16-23, nbBits 24-31.
func TestDecodeEntryLayout(t *testing.T) {
	var e decodeEntry
	if unsafe.Sizeof(e) != 4 || unsafe.Offsetof(e.newState) != 0 || unsafe.Offsetof(e.symbol) != 2 || unsafe.Offsetof(e.nbBits) != 3 {
		t.Fatalf("decodeEntry layout: size %d, offsets %d %d %d", unsafe.Sizeof(e),
			unsafe.Offsetof(e.newState), unsafe.Offsetof(e.symbol), unsafe.Offsetof(e.nbBits))
	}
}

// checkDecompressEquiv decodes payload into dstSize bytes with the Go loops
// and with the assembly, from and into buffers against guard pages, and
// fails t unless both leave the same bytes, count and error. It returns the
// Go result.
func checkDecompressEquiv(t testing.TB, payload []byte, dstSize int, dt *dTable, before bool) (int, error) {
	t.Helper()
	alloc := guardmem.After
	if before {
		alloc = guardmem.Before
	}
	src := alloc(t, len(payload))
	copy(src, payload)
	run := func(on bool) (int, error, []byte) {
		dst := alloc(t, dstSize)
		for i := range dst {
			dst[i] = byte(i * 7)
		}
		old := useFSEDecompressAsm
		useFSEDecompressAsm = on
		defer func() { useFSEDecompressAsm = old }()
		n, err := decompressUsingDTable(dst, src, dt)
		return n, err, dst
	}
	gn, gerr, gdst := run(false)
	an, aerr, adst := run(true)
	if an != gn || !errors.Is(aerr, gerr) || !errors.Is(gerr, aerr) {
		t.Fatalf("payload %x, dst %d: asm (%d, %v), Go (%d, %v)", payload, dstSize, an, aerr, gn, gerr)
	}
	if !bytes.Equal(adst, gdst) {
		t.Fatalf("payload %x, dst %d: output differs\nasm %x\nGo  %x", payload, dstSize, adst, gdst)
	}
	return gn, gerr
}

// equivTable parses the NCount header of stream and builds its table, with
// the weight reader's limit on the table log.
func equivTable(stream []byte) (dt *dTable, payload []byte, ok bool) {
	var norm [maxSymbolValue + 1]int16
	maxSV := uint(maxSymbolValue)
	tableLog, n, err := ReadNCount(norm[:], &maxSV, stream)
	if err != nil || tableLog > MaxTableLogForHuffHeader {
		return nil, nil, false
	}
	dt = &dTable{}
	if buildDTable(dt, norm[:], uint32(maxSV), uint32(tableLog)) != nil {
		return nil, nil, false
	}
	return dt, stream[n:], true
}

func TestDecompressAsmEquiv(t *testing.T) {
	if !cpu.X86.HasBMI2 {
		t.Skip("no BMI2")
	}
	rng := rand.New(rand.NewPCG(7, 11))
	var ok, tooSmall, corrupt, long int
	for iter := 0; iter < 20000; iter++ {
		src := make([]byte, 2+rng.IntN(600))
		alphabet := 2 + rng.IntN(14)
		for i := range src {
			// skewed, so that some symbols cost no bits
			src[i] = byte(min(rng.IntN(alphabet), rng.IntN(alphabet), rng.IntN(1+rng.IntN(alphabet))))
		}
		buf := make([]byte, 1024)
		n, err := Compress2(buf, src, 255, uint(5+iter&1))
		if err != nil || n <= 1 {
			continue
		}
		dt, payload, good := equivTable(buf[:n])
		if !good {
			t.Fatal("Compress2 output does not parse")
		}
		if iter%3 == 1 && len(payload) > 0 { // corrupt the bitstream
			for k := rng.IntN(4); k >= 0; k-- {
				payload[rng.IntN(len(payload))] ^= byte(1 + rng.IntN(255))
			}
		}
		if iter%7 == 3 && len(payload) > 0 { // truncate at the front
			payload = payload[rng.IntN(len(payload)):]
		}
		for _, size := range []int{len(src), len(src) + 1 + rng.IntN(64), rng.IntN(len(src) + 1), rng.IntN(8)} {
			_, err := checkDecompressEquiv(t, payload, size, dt, iter&2 != 0)
			switch {
			case err == nil:
				ok++
				if len(payload) >= 8 {
					long++
				}
			case errors.Is(err, ErrDstSizeTooSmall):
				tooSmall++
			default:
				corrupt++
			}
		}
	}
	// Arbitrary bitstreams under arbitrary valid tables.
	for iter := 0; iter < 20000; iter++ {
		src := make([]byte, 2+rng.IntN(300))
		for i := range src {
			src[i] = byte(rng.IntN(2 + iter%11))
		}
		buf := make([]byte, 1024)
		n, err := Compress2(buf, src, 255, 6)
		if err != nil || n <= 1 {
			continue
		}
		dt, _, _ := equivTable(buf[:n])
		payload := make([]byte, 1+rng.IntN(48))
		for i := range payload {
			payload[i] = byte(rng.Uint32())
		}
		checkDecompressEquiv(t, payload, rng.IntN(300), dt, iter&1 != 0)
	}
	t.Logf("%d decoded (%d of 8 bytes or more), %d dst too small, %d corrupted", ok, long, tooSmall, corrupt)
	if long < 1000 || tooSmall < 100 || corrupt < 100 {
		t.Error("the cases do not cover decoding, dst too small and corruption")
	}
}

func FuzzDecompressAsmEquiv(f *testing.F) {
	for _, src := range [][]byte{[]byte("aaaabbbcccddeeeffffggggggg hello hello hello"), bytes.Repeat([]byte{1, 2, 3, 1, 1, 1, 2}, 50), bytes.Repeat([]byte("abcdefgh abc"), 20)} {
		dst := make([]byte, 1024)
		if n, err := Compress2(dst, src, 255, 6); err == nil && n > 1 {
			f.Add(dst[:n], uint16(len(src)), false)
		}
	}
	f.Fuzz(func(t *testing.T, stream []byte, size uint16, before bool) {
		if !cpu.X86.HasBMI2 || len(stream) > 1<<12 {
			return
		}
		dt, payload, ok := equivTable(stream)
		if !ok {
			return
		}
		checkDecompressEquiv(t, payload, int(size%1024), dt, before)
	})
}
