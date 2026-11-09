package deflate

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// A bit writer script, in ldref dump-bits form.
type bwOp struct {
	op   byte // 0 add, 1 flush, 2 new block (recompute fastEnd)
	n    uint
	bits uint32
}

type bwScript struct {
	cap, start int
	bitbuf     uint64
	bitcount   uint
	ops        []bwOp
}

type bwResult struct {
	buf      []byte
	next     int
	bitcount uint
	bitbuf   uint64
}

const bwFill = 0xA5

// runGo runs the script on outputBitstream. The buffer has spare capacity
// filled with bwFill beyond len so writes past len(out) would be seen.
func (s *bwScript) runGo(t testing.TB) bwResult {
	backing := bytes.Repeat([]byte{bwFill}, s.cap+64)
	w := outputBitstream{bitbuf: s.bitbuf, bitcount: s.bitcount, next: s.start, out: backing[:s.cap]}
	w.setFastEnd()
	for _, o := range s.ops {
		switch o.op {
		case 0:
			w.addBits(o.bits, o.n)
		case 1:
			w.flushBits()
		case 2:
			w.setFastEnd()
		}
		if w.bitcount > outBitbufNbits {
			t.Fatalf("bitcount %d > %d", w.bitcount, outBitbufNbits)
		}
	}
	for i := s.cap; i < len(backing); i++ {
		if backing[i] != bwFill {
			t.Fatalf("byte %d beyond len(out) = %d was written", i, s.cap)
		}
	}
	return bwResult{buf: backing[:s.cap], next: w.next, bitcount: w.bitcount, bitbuf: w.bitbuf}
}

func (s *bwScript) encode() []byte {
	in := binary.LittleEndian.AppendUint32(nil, uint32(s.cap))
	in = binary.LittleEndian.AppendUint32(in, uint32(s.start))
	in = binary.LittleEndian.AppendUint64(in, s.bitbuf)
	in = append(in, byte(s.bitcount))
	for _, o := range s.ops {
		in = append(in, o.op)
		if o.op == 0 {
			in = append(in, byte(o.n))
			in = binary.LittleEndian.AppendUint32(in, o.bits)
		}
	}
	return in
}

func (s *bwScript) runC(t testing.TB) bwResult {
	t.Helper()
	out := creftest.LDRun(t, s.encode(), "dump-bits")
	if len(out) < s.cap {
		t.Fatalf("ldref dump-bits: short output")
	}
	var r bwResult
	r.buf = out[:s.cap]
	if _, err := fmt.Sscanf(string(out[s.cap:]), "next=%d bitcount=%d bitbuf=%x\n", &r.next, &r.bitcount, &r.bitbuf); err != nil {
		t.Fatalf("ldref dump-bits: bad trailer %q: %v", out[s.cap:], err)
	}
	return r
}

// refPack is the trivial reference: the bits of every add, LSB first, as a
// bit list, packed into bytes.
type refPack struct{ bits []byte }

func (p *refPack) add(v uint32, n uint) {
	for i := range n {
		p.bits = append(p.bits, byte(v>>i&1))
	}
}

func (p *refPack) bytesAndRest() ([]byte, uint, uint64) {
	nb := len(p.bits) / 8
	out := make([]byte, nb)
	for i := range nb * 8 {
		out[i/8] |= p.bits[i] << (i % 8)
	}
	rest := uint64(0)
	for i, b := range p.bits[nb*8:] {
		rest |= uint64(b) << i
	}
	return out, uint(len(p.bits) - nb*8), rest
}

// genScript makes a valid random script: bits masked to n (unless dirty),
// bitcount + n <= outBitbufNbits between flushes (C's CAN_BUFFER contract),
// never flushing past the buffer end. Starts with 0..7 clean pending bits.
func genScript(r *rand.Rand, cap int, dirty bool) (*bwScript, *refPack) {
	s := &bwScript{cap: cap, start: r.IntN(cap + 1)}
	p := &refPack{}
	s.bitcount = uint(r.IntN(8))
	s.bitbuf = r.Uint64() & (1<<s.bitcount - 1)
	p.add(uint32(s.bitbuf), s.bitcount)
	pending := s.bitcount
	next := s.start
	nops := r.IntN(400)
	for range nops {
		switch k := r.IntN(10); {
		case k < 6:
			var n uint
			switch r.IntN(4) {
			case 0:
				n = 32
			case 1:
				n = uint(r.IntN(8))
			default:
				n = uint(r.IntN(33))
			}
			if pending+n > outBitbufNbits {
				n = outBitbufNbits - pending
			}
			// Stop adding when even a flush could not make room.
			if next+int(pending+n)/8 > cap {
				continue
			}
			v := r.Uint32()
			if !dirty && n < 32 {
				v &= 1<<n - 1
			}
			s.ops = append(s.ops, bwOp{op: 0, n: n, bits: v})
			p.add(v, n)
			pending += n
		case k < 9:
			s.ops = append(s.ops, bwOp{op: 1})
			next += int(pending / 8)
			pending %= 8
		default:
			s.ops = append(s.ops, bwOp{op: 2})
		}
	}
	return s, p
}

func randCap(r *rand.Rand) int {
	switch r.IntN(4) {
	case 0:
		return r.IntN(17) // slow path only / at the fast-path boundary
	case 1:
		return 8 + r.IntN(4)
	default:
		return r.IntN(3000)
	}
}

// checkAgainstRef compares a Go result with the reference packer: the
// bytes in [start, next) are the first packed bytes, the pending bits are
// exactly the rest (bitcount == total - 8*(next-start), bitbuf == those bits),
// bytes before start are untouched and bytes from next+8 on are untouched (a
// word store writes at most 8 bytes from the flush position).
func checkAgainstRef(s *bwScript, p *refPack, g bwResult) error {
	flushed := g.next - s.start
	if flushed < 0 || 8*flushed > len(p.bits) {
		return fmt.Errorf("next %d out of range (start %d, %d bits added)", g.next, s.start, len(p.bits))
	}
	packed, _, _ := (&refPack{bits: p.bits[:8*flushed]}).bytesAndRest()
	if !bytes.Equal(g.buf[s.start:g.next], packed) {
		return fmt.Errorf("bytes differ from the reference packer")
	}
	restBits := p.bits[8*flushed:]
	if g.bitcount != uint(len(restBits)) {
		return fmt.Errorf("pending %d bits, want %d", g.bitcount, len(restBits))
	}
	rest := uint64(0)
	for i, b := range restBits {
		rest |= uint64(b) << i
	}
	if g.bitbuf != rest {
		return fmt.Errorf("pending bits %#x, want %#x", g.bitbuf, rest)
	}
	for i := range s.start {
		if g.buf[i] != bwFill {
			return fmt.Errorf("byte %d before start written", i)
		}
	}
	for i := g.next + 8; i < s.cap; i++ {
		if g.buf[i] != bwFill {
			return fmt.Errorf("byte %d (next %d) written", i, g.next)
		}
	}
	return nil
}

func TestBitwriterReference(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 100))
	iters := 20000
	if testing.Short() {
		iters = 3000
	}
	for i := range iters {
		s, p := genScript(r, randCap(r), false)
		g := s.runGo(t)
		if err := checkAgainstRef(s, p, g); err != nil {
			t.Fatalf("script %d (cap %d start %d, %d ops): %v", i, s.cap, s.start, len(s.ops), err)
		}
	}
}

// TestBitwriterMatchLibdeflate runs random scripts (including unmasked
// "dirty" bits, which C ORs in unchanged) through C's real ADD_BITS /
// FLUSH_BITS (ldref dump-bits) and compares the whole buffer, including the
// bytes a word store leaves beyond next, and the final state.
func TestBitwriterMatchLibdeflate(t *testing.T) {
	creftest.LDBin(t)
	r := rand.New(rand.NewPCG(21, 101))
	iters := 600
	if testing.Short() {
		iters = 60
	}
	scripts := []*bwScript{
		{cap: 0},
		{cap: 8, ops: []bwOp{{0, 32, 0xdeadbeef}, {0, 31, 0x7fffffff}, {1, 0, 0}}},          // fastEnd 1: one word store
		{cap: 7, ops: []bwOp{{0, 32, 0xffffffff}, {0, 24, 0xabcdef}, {1, 0, 0}}},            // slow path, exactly full
		{cap: 16, start: 9, ops: []bwOp{{0, 32, 0x01020304}, {0, 24, 0x050607}, {1, 0, 0}}}, // next == fastEnd
		{cap: 16, start: 8, ops: []bwOp{{0, 32, 0x01020304}, {0, 24, 0x050607}, {1, 0, 0}}},
		{cap: 20, bitcount: 7, bitbuf: 0x55, ops: []bwOp{{0, 32, 0}, {0, 24, 0xffffff}, {1, 0, 0}, {0, 0, 0}, {1, 0, 0}}},
	}
	for range iters {
		s, _ := genScript(r, randCap(r), r.IntN(3) == 0)
		scripts = append(scripts, s)
	}
	for i, s := range scripts {
		g, c := s.runGo(t), s.runC(t)
		if !bytes.Equal(g.buf, c.buf) || g.next != c.next || g.bitcount != c.bitcount || g.bitbuf != c.bitbuf {
			t.Fatalf("script %d (cap %d start %d): go next=%d bitcount=%d bitbuf=%#x, c next=%d bitcount=%d bitbuf=%#x\n go %x\n  c %x",
				i, s.cap, s.start, g.next, g.bitcount, g.bitbuf, c.next, c.bitcount, c.bitbuf, g.buf, c.buf)
		}
	}
}

// TestBitwriterEdges: init, the fast/slow path selection at the boundary,
// a buffer filled exactly, and the out-of-space contract (the caller must
// check space first; writing past len(out) panics instead of corrupting
// memory like C's unchecked release build).
func TestBitwriterEdges(t *testing.T) {
	var w outputBitstream
	buf := make([]byte, 10)
	w.init(buf)
	if w.next != 0 || w.bitcount != 0 || w.bitbuf != 0 || w.overflow || w.fastEnd != 3 || len(w.out) != 10 {
		t.Fatalf("init: %+v", w)
	}
	w.init(buf[:5])
	if w.fastEnd != 0 {
		t.Fatalf("fastEnd for 5 bytes = %d, want 0 (slow path only)", w.fastEnd)
	}
	// Fill 10 bytes exactly: one word store at next 0 (63 bits -> 7
	// bytes), then 3 bytes through the byte path (next 7 >= fastEnd 3).
	w.init(buf)
	w.addBits(0x7fffffff, 31)
	w.addBits(0xffffffff, 32)
	w.flushBits()
	if w.next != 7 || w.bitcount != 7 {
		t.Fatalf("after word flush: next %d bitcount %d", w.next, w.bitcount)
	}
	w.addBits(0x1ffff, 17)
	w.flushBits()
	if w.next != 10 || w.bitcount != 0 || !bytes.Equal(buf, bytes.Repeat([]byte{0xff}, 10)) {
		t.Fatalf("exact fill: next %d bitcount %d buf %x", w.next, w.bitcount, buf)
	}
	// Flushing with no space panics on the slice bound.
	w.addBits(0xff, 8)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("flush past the end did not panic")
			}
		}()
		w.flushBits()
	}()
	// A flush of fewer than 8 bits writes nothing even with no space.
	w.init(buf[:0])
	w.addBits(0x7f, 7)
	w.flushBits()
	if w.next != 0 || w.bitcount != 7 {
		t.Fatal("partial byte flushed")
	}
}

// TestBitwriterNoAlloc: the writer allocates nothing.
func TestBitwriterNoAlloc(t *testing.T) {
	buf := make([]byte, 4096)
	n := testing.AllocsPerRun(100, func() {
		var w outputBitstream
		w.init(buf)
		for i := range 1000 {
			w.addBits(uint32(i), 13)
			w.addBits(uint32(i)&0x7f, 7)
			w.flushBits()
		}
	})
	if n != 0 {
		t.Fatalf("%v allocs per run, want 0", n)
	}
}
