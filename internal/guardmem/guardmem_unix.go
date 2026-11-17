//go:build linux || darwin

package guardmem

import (
	"os"
	"syscall"
	"testing"
)

// Real reports whether After and Before place buffers next to guard pages.
func Real() bool { return true }

// After returns a slice with len = cap = n whose last byte is the last byte
// before a PROT_NONE page.
func After(t testing.TB, n int) []byte {
	t.Helper()
	ps := os.Getpagesize()
	size := (n + ps - 1) &^ (ps - 1)
	mem := mapGuarded(t, size+ps)
	protectNone(t, mem[size:])
	return mem[size-n : size : size]
}

// Before returns a slice with len = cap = n whose first byte is the first
// byte after a PROT_NONE page.
func Before(t testing.TB, n int) []byte {
	t.Helper()
	ps := os.Getpagesize()
	size := (n + ps - 1) &^ (ps - 1)
	mem := mapGuarded(t, ps+size)
	protectNone(t, mem[:ps])
	return mem[ps : ps+n : ps+n]
}

// mapGuarded maps size bytes of anonymous read-write memory and unmaps it
// when the test ends.
func mapGuarded(t testing.TB, size int) []byte {
	t.Helper()
	mem, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatalf("guardmem: mmap %d bytes: %v", size, err)
	}
	t.Cleanup(func() {
		if err := syscall.Munmap(mem); err != nil {
			t.Errorf("guardmem: munmap: %v", err)
		}
	})
	return mem
}

func protectNone(t testing.TB, b []byte) {
	t.Helper()
	if err := syscall.Mprotect(b, syscall.PROT_NONE); err != nil {
		t.Fatalf("guardmem: mprotect: %v", err)
	}
}
