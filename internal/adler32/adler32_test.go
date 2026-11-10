package adler32

import (
	stdadler "hash/adler32"
	"math/rand/v2"
	"testing"
)

// refUpdate is the textbook Adler-32 (RFC 1950 section 8.2, one byte at a
// time, reduced after every byte); a reference independent of the ported
// chunking. adler's halves must be below 65521.
func refUpdate(adler uint32, p []byte) uint32 {
	s1, s2 := adler&0xFFFF, adler>>16
	for _, b := range p {
		s1 = (s1 + uint32(b)) % divisor
		s2 = (s2 + s1) % divisor
	}
	return s2<<16 | s1
}

// selectInitialAdler: 1 half the time, else random halves that are 65520
// (the maximum) a quarter of the time each.
// port of programs/test_checksums.c:select_initial_adler
func selectInitialAdler(r *rand.Rand) uint32 {
	if r.IntN(2) == 0 {
		return 1
	}
	half := func() uint32 {
		if r.IntN(4) == 0 {
			return 65520
		}
		return uint32(r.IntN(65521))
	}
	lo := half()
	hi := half()
	return hi<<16 | lo
}

// check compares Update with the reference (and, from Init, with the
// standard library as a black box) and, like the C test, sometimes checks a
// two-part computation.
// port of programs/test_checksums.c:test_checksums / test_multipart
func check(t *testing.T, r *rand.Rand, p []byte, initial uint32) {
	t.Helper()
	got := Update(initial, p)
	if want := refUpdate(initial, p); got != want {
		t.Fatalf("Update(%#x, %d bytes) = %#x, reference %#x", initial, len(p), got, want)
	}
	if initial == Init {
		if want := stdadler.Checksum(p); got != want {
			t.Fatalf("Checksum(%d bytes) = %#x, hash/adler32 %#x", len(p), got, want)
		}
		if c := Checksum(p); c != got {
			t.Fatalf("Checksum %#x != Update(Init) %#x", c, got)
		}
	}
	if r.IntN(16) == 0 || len(p) < 64 {
		division := r.IntN(len(p) + 1)
		if v := Update(Update(initial, p[:division]), p[division:]); v != got {
			t.Fatalf("multipart (%d + %d bytes) = %#x, want %#x", division, len(p)-division, v, got)
		}
	}
}

// port of programs/test_checksums.c:test_initial_values (adler32). There is
// no NULL buffer in Go: an empty or nil buffer leaves the value unchanged, so
// only the initial value 1 is checked against C's "returns 1".
func TestInitialValues(t *testing.T) {
	if Checksum(nil) != 1 || Checksum([]byte{}) != 1 || Update(Init, nil) != 1 || Init != 1 {
		t.Fatal("initial value is not 1")
	}
	for _, v := range []uint32{0, 1234, 0xFFF0FFF0, 0xFFFFFFFF} {
		if Update(v, nil) != v {
			t.Fatalf("Update(%#x, nil) = %#x", v, Update(v, nil))
		}
	}
	// Known values (RFC 1950 / zlib).
	for _, c := range []struct {
		in   string
		want uint32
	}{{"", 1}, {"a", 0x00620062}, {"abc", 0x024d0127}, {"Wikipedia", 0x11E60398}} {
		if got := Checksum([]byte(c.in)); got != c.want {
			t.Errorf("Checksum(%q) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

// port of programs/test_checksums.c:test_random_buffers (adler32 part): random
// lengths and alignments inside buffers of 256 B .. 256 KiB.
func TestRandomBuffers(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	buf := make([]byte, 262144)
	for _, c := range []struct{ limit, iters int }{{256, 5000}, {1024, 500}, {32768, 50}, {262144, 50}} {
		for i := 0; i < c.iters; i++ {
			start := r.IntN(c.limit)
			n := r.IntN(c.limit - start)
			for j := start; j < start+n; j++ {
				buf[j] = byte(r.Uint32())
			}
			a0 := selectInitialAdler(r)
			check(t, r, buf[start:start+n], a0)
			copy(buf, buf[start:start+n])
			check(t, r, buf[:n], a0)
			copy(buf[len(buf)-n:], buf[:n])
			check(t, r, buf[len(buf)-n:], a0)
		}
	}
}

// TestChunkBoundaries: every size around multiples of MAX_CHUNK_LEN (5552)
// and of 4, random sizes up to 1 MiB, random and 0xFF data, initial values
// from selectInitialAdler and the maximum (65520, 65520).
func TestChunkBoundaries(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	ff := make([]byte, 1<<20)
	for i := range ff {
		ff[i] = 0xFF
	}
	sizes := []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 1 << 20}
	for k := 1; k <= 6; k++ {
		for d := -5; d <= 5; d++ {
			sizes = append(sizes, k*maxChunkLen+d)
		}
	}
	sizes = append(sizes, 188*maxChunkLen-1, 188*maxChunkLen, 188*maxChunkLen+1)
	for i := 0; i < 200; i++ {
		sizes = append(sizes, r.IntN(1<<20+1))
	}
	short := testing.Short()
	for _, n := range sizes {
		n = min(n, 1<<20)
		for _, a0 := range []uint32{1, 65520<<16 | 65520, 0, selectInitialAdler(r)} {
			if short && n > 1<<16 && a0 != 1 && a0 != 65520<<16|65520 {
				continue
			}
			check(t, r, data[:n], a0)
			check(t, r, ff[:n], a0)
			// Unaligned start.
			if n >= 3 {
				check(t, r, data[3:n], a0)
			}
		}
	}
}

// port of programs/test_checksums.c:tmain, the Adler-32 overflow cases: all
// 0xFF bytes (the maximum sums) with the maximum initial value, 5553 bytes
// and random lengths, with bytes progressively changed to 0xFE.
func TestOverflowCases(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	buf := make([]byte, 32768)
	for i := range buf {
		buf[i] = 0xFF
	}
	for i := 0; i < 20; i++ {
		initial := selectInitialAdler(r)
		if i == 0 {
			initial = 65520<<16 | 65520
		}
		check(t, r, buf[:5553], initial)
		check(t, r, buf[:r.IntN(32769)], initial)
		buf[r.IntN(32768)] = 0xFE
	}
}

// TestUnreducedInitial: C does not reduce adler before the first chunk;
// neither does the port (the same uint32 arithmetic, reduced at the end of
// each chunk). With an empty buffer the value is returned unchanged; with
// data, halves of 65521..65535 are reduced like any other sum.
func TestUnreducedInitial(t *testing.T) {
	v := uint32(0xFFFF_FFFF)
	if Update(v, nil) != v {
		t.Fatal("empty update changed the value")
	}
	got := Update(v, []byte{0})
	// s1 = 65535 % 65521 = 14, s2 = (65535 + 65535) % 65521 = 28.
	if got != 28<<16|14 {
		t.Fatalf("Update(0xFFFFFFFF, {0}) = %#x", got)
	}
}

func TestAllocs(t *testing.T) {
	p := make([]byte, 100000)
	if n := testing.AllocsPerRun(10, func() { _ = Checksum(p) }); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

func FuzzAdler32(f *testing.F) {
	f.Add(uint32(1), []byte("hello"))
	f.Add(uint32(65520<<16|65520), make([]byte, 6000))
	f.Fuzz(func(t *testing.T, a0 uint32, p []byte) {
		a0 = (a0>>16%divisor)<<16 | (a0&0xFFFF)%divisor
		if got, want := Update(a0, p), refUpdate(a0, p); got != want {
			t.Fatalf("Update(%#x, %d bytes) = %#x, reference %#x", a0, len(p), got, want)
		}
	})
}
