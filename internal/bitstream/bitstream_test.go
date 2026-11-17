package bitstream

import (
	"bytes"
	"testing"
)

func TestInitTooSmall(t *testing.T) {
	var c CStream
	if err := c.Init(make([]byte, 8)); err == nil {
		t.Fatal("dstCapacity == 8 must fail (C: dstCapacity <= sizeof(container))")
	}
	if err := c.Init(make([]byte, 9)); err != nil {
		t.Fatal(err)
	}
}

func TestBitstream(t *testing.T) {
	// LSB-first, little-endian. 0x5 in 3 bits then 0x1F in 5 bits: byte0 =
	// 0b11111_101 = 0xFD. 0x3 in 2 bits then the end mark (1 bit) at bit 10:
	// byte1 = 0b111 = 0x07.
	t.Run("slow", func(t *testing.T) {
		buf := make([]byte, 16)
		var c CStream
		if err := c.Init(buf); err != nil {
			t.Fatal(err)
		}
		c.AddBits(0x5, 3)
		c.AddBits(0x1F, 5)
		c.AddBits(0x3, 2)
		n := c.Close()
		if n != 2 || !bytes.Equal(buf[:n], []byte{0xFD, 0x07}) {
			t.Fatalf("n=%d out=% x", n, buf[:n])
		}
	})
	// AddBits masks the value to nbBits; AddBitsFast requires a clean value.
	t.Run("mask", func(t *testing.T) {
		buf := make([]byte, 16)
		var c CStream
		c.Init(buf)
		c.AddBits(0xFFFF, 4) // only 0xF kept
		n := c.Close()       // mark at bit 4 -> 0x1F
		if n != 1 || buf[0] != 0x1F {
			t.Fatalf("n=%d out=% x", n, buf[:n])
		}
	})
	// 0x12345678 in 31 bits, flushed: 78 56 34 and 7 leftover bits (0x12).
	// AddBitsFast(0xAB, 8) at bit 7: byte3 = 0x12 | (0xAB&1)<<7 = 0x92, leaving
	// 0xAB>>1 = 0x55 in 7 bits. Close adds the end mark at bit 7: 0xD5.
	t.Run("fast", func(t *testing.T) {
		buf := make([]byte, 16)
		var c CStream
		c.Init(buf)
		c.AddBits(0x12345678, 31)
		c.FlushBits()
		c.AddBitsFast(0xAB, 8)
		c.FlushBitsFast()
		n := c.Close()
		want := []byte{0x78, 0x56, 0x34, 0x92, 0xD5}
		if n != 5 || !bytes.Equal(buf[:n], want) {
			t.Fatalf("n=%d out=% x want % x", n, buf[:n], want)
		}
	})
	// Leftover bits stay in the container after a flush (max 7).
	t.Run("leftover", func(t *testing.T) {
		buf := make([]byte, 16)
		var c CStream
		c.Init(buf)
		c.AddBits(0x1234567F, 31)
		c.FlushBits()
		if c.bitPos != 31&7 {
			t.Fatalf("bitPos=%d", c.bitPos)
		}
		// value bits 24..30 stay: (0x1234567F>>24)&0x7F = 0x12 -> container 0x12
		if c.bitContainer != 0x12 {
			t.Fatalf("container=%#x", c.bitContainer)
		}
		n := c.Close() // end mark at bit 7: 0x12 | 0x80 = 0x92
		want := []byte{0x7F, 0x56, 0x34, 0x92}
		if n != 4 || !bytes.Equal(buf[:n], want) {
			t.Fatalf("n=%d out=% x want % x", n, buf[:n], want)
		}
	})
}

func TestOverflow(t *testing.T) {
	// cap 9 -> endPtr = 1. Three 24-bit writes = 9 bytes: flush clamps ptr
	// to endPtr, and Close reports 0.
	buf := make([]byte, 9)
	var c CStream
	c.Init(buf)
	for i := 0; i < 3; i++ {
		c.AddBits(0xFFFFFF, 24)
		c.FlushBits()
	}
	if n := c.Close(); n != 0 {
		t.Fatalf("Close = %d, want 0 on overflow", n)
	}
	// Nothing may be written past the buffer (Go would panic) and the clamped
	// stream must not have advanced beyond endPtr.
	if c.ptr != c.endPtr {
		t.Fatalf("ptr=%d endPtr=%d", c.ptr, c.endPtr)
	}
}

func TestExactFit(t *testing.T) {
	// cap 10 -> endPtr = 2; 1 byte + mark = 2 bytes; ptr(1) < endPtr(2) ok.
	buf := make([]byte, 10)
	var c CStream
	c.Init(buf)
	c.AddBits(0xAA, 8)
	if n := c.Close(); n != 2 || buf[0] != 0xAA || buf[1] != 0x01 {
		t.Fatalf("n=%d out=% x", n, buf[:2])
	}
	// ptr == endPtr after close means overflow: 2 bytes + mark in cap 10.
	var d CStream
	buf2 := make([]byte, 10)
	d.Init(buf2)
	d.AddBits(0xAAAA, 16)
	if n := d.Close(); n != 0 {
		t.Fatalf("n=%d, want 0 (ptr >= endPtr)", n)
	}
}

func TestNoAllocInline(t *testing.T) {
	buf := make([]byte, 64)
	var c CStream
	if n := testing.AllocsPerRun(100, func() {
		c.Init(buf)
		c.AddBitsFast(5, 3)
		c.FlushBitsFast()
		c.AddBits(5, 3)
		c.FlushBits()
		_ = c.Close()
	}); n != 0 {
		t.Fatalf("allocs=%v", n)
	}
}

// TestReloadOverflowKeepsContainer: once more than 64 bits are consumed,
// Reload reports DStreamOverflow and, as C's BIT_reloadDStream, leaves the
// container untouched (and keeps doing so on later reloads).
func TestReloadOverflowKeepsContainer(t *testing.T) {
	src := []byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x81}
	var d DStream
	if _, err := d.Init(src); err != nil {
		t.Fatal(err)
	}
	d.SkipBits(70)
	before := d.LookBitsFast(11)
	if before == 0 {
		t.Fatal("test needs a nonzero look")
	}
	for i := 0; i < 2; i++ {
		if st := d.Reload(); st != DStreamOverflow {
			t.Fatalf("reload %d: status %d, want DStreamOverflow", i, st)
		}
		if got := d.LookBitsFast(11); got != before {
			t.Fatalf("reload %d: LookBitsFast = %#x, want %#x (container changed)", i, got, before)
		}
	}
	if d.EndOfDStream() {
		t.Fatal("EndOfDStream true after overflow")
	}
}

// TestRawRoundTrip: Raw returns the state SetRaw sets.
func TestRawRoundTrip(t *testing.T) {
	src := make([]byte, 16)
	var d DStream
	d.SetRaw(src, 5, 0x0123456789abcdef, 13)
	s, ptr, c, n := d.Raw()
	if &s[0] != &src[0] || len(s) != len(src) || ptr != 5 || c != 0x0123456789abcdef || n != 13 {
		t.Fatalf("Raw = len %d, %d, %#x, %d", len(s), ptr, c, n)
	}
}
