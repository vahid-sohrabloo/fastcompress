package hist

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

func naive(src []byte) (c [256]uint32, maxSym, largest uint32) {
	for _, b := range src {
		c[b]++
	}
	for s := range c {
		if c[s] > 0 {
			maxSym = uint32(s)
		}
		largest = max(largest, c[s])
	}
	return
}

func format(c *[256]uint32, maxSym, largest uint32) string {
	var sb strings.Builder
	for i, v := range c {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprint(&sb, v)
	}
	fmt.Fprintf(&sb, "\nmaxSymbol=%d largest=%d\n", maxSym, largest)
	return sb.String()
}

func TestHistCref(t *testing.T) {
	for name, in := range creftest.Inputs(t) {
		want := string(creftest.Run(t, in, "hist"))
		for _, fn := range []struct {
			n string
			f func(*[256]uint32, *uint32, []byte) uint32
		}{{"Count", Count}, {"CountFast", CountFast}, {"CountSimple", CountSimple}} {
			var c [256]uint32
			// Dirty the table: implementations must overwrite it.
			for i := range c {
				c[i] = 0xDEADBEEF
			}
			maxSym := uint32(255)
			largest := fn.f(&c, &maxSym, in)
			if got := format(&c, maxSym, largest); got != want {
				t.Errorf("%s/%s mismatch:\n got %.120q\nwant %.120q", name, fn.n, got, want)
			}
		}
	}
}

func TestCountSmallMax(t *testing.T) {
	// maxSymbol < 255 takes the checking path (C: checkMaxSymbolValue).
	for _, n := range []int{0, 1, 3, 15, 19, 20, 21, 100, 1499, 1500, 5000} {
		src := make([]byte, n)
		for i := range src {
			src[i] = byte(i*7) % 40
		}
		wc, wm, wl := naive(src)
		var c [256]uint32
		m := uint32(60)
		l := Count(&c, &m, src)
		if l != wl || m != wm {
			t.Fatalf("n=%d largest=%d/%d max=%d/%d", n, l, wl, m, wm)
		}
		for s := uint32(0); s <= m; s++ {
			if c[s] != wc[s] {
				t.Fatalf("n=%d count[%d]=%d want %d", n, s, c[s], wc[s])
			}
		}
		if n > 0 {
			src[n-1] = 200
			m = 60
			if l := Count(&c, &m, src); l != MaxSymbolTooSmall {
				t.Fatalf("n=%d: want MaxSymbolTooSmall, got %d", n, l)
			}
		}
	}
}

func TestCountSizes(t *testing.T) {
	for n := 0; n < 3000; n += 7 {
		src := make([]byte, n)
		for i := range src {
			src[i] = byte(i*i + i/3)
		}
		wc, wm, wl := naive(src)
		var c [256]uint32
		m := uint32(255)
		l := Count(&c, &m, src)
		if l != wl || m != wm || c != wc {
			t.Fatalf("n=%d mismatch", n)
		}
	}
}

func TestNoAlloc(t *testing.T) {
	src := make([]byte, 4096)
	var c [256]uint32
	if n := testing.AllocsPerRun(100, func() {
		m := uint32(255)
		Count(&c, &m, src)
	}); n != 0 {
		t.Fatalf("allocs=%v", n)
	}
}

func BenchmarkCount(b *testing.B) {
	src := make([]byte, 128<<10)
	for i := range src {
		src[i] = byte(i * 31 >> 3)
	}
	var c [256]uint32
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		m := uint32(255)
		Count(&c, &m, src)
	}
}
