//go:build linux && (amd64 || arm64 || ppc64le || s390x || riscv64 || loong64 || mips64 || mips64le)

package zstd

import (
	"errors"
	"syscall"
	"testing"
)

// hugeZeroMapping returns n bytes of read-only anonymous memory. Reading it
// maps the shared zero page, so it is never resident (the test memory cap
// holds). It skips the test when the mapping fails.
func hugeZeroMapping(t *testing.T, n int) []byte {
	t.Helper()
	b, err := syscall.Mmap(-1, 0, n, syscall.PROT_READ, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Skipf("mmap %d bytes: %v", n, err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(b) })
	return b
}

// TestBuildDictTooLarge checks C's limit on the history: offcodeMax =
// highbit32(len + 128 KiB) must be at most 30, so a history of
// 2 GiB - 128 KiB fails with dictionaryCreation_failed, before the CDict
// or the output is allocated.
func TestBuildDictTooLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("hashes 2 GiB")
	}
	const n = 2<<30 - 128<<10
	if offcodeMaxFor(n-1) != 30 || offcodeMaxFor(n) != 31 {
		t.Fatalf("offcodeMax %d, %d", offcodeMaxFor(n-1), offcodeMaxFor(n))
	}
	hist := hugeZeroMapping(t, n)
	_, err := BuildDict(BuildDictOptions{ID: 1, History: hist})
	var ce *buildDictError
	if !errors.As(err, &ce) || ce.code != 34 {
		t.Fatalf("err = %v, want C error 34 (dictionaryCreation_failed)", err)
	}
}
