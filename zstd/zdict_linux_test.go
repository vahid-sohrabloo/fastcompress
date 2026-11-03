//go:build linux && (amd64 || arm64 || ppc64le || s390x || riscv64 || loong64 || mips64 || mips64le)

package zstd

import (
	"errors"
	"strings"
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
// or the output is allocated. From 4 GiB - 128 KiB C's 32-bit cast wraps
// (and C goes on into undefined behaviour); Go rejects those with the same
// error. The histories are untouched mappings, never read.
func TestBuildDictTooLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("maps 4 GiB of address space")
	}
	const n = 2<<30 - 128<<10
	if offcodeMaxFor(n-1) != 30 || offcodeMaxFor(n) != 31 {
		t.Fatalf("offcodeMax %d, %d", offcodeMaxFor(n-1), offcodeMaxFor(n))
	}
	const wrap = 4<<30 - 128<<10 // C: uint32(len + 128 KiB) wraps to 0
	for _, size := range []int{n, wrap - 1, wrap, wrap + 1<<20} {
		hist := hugeZeroMapping(t, size)
		_, err := BuildDict(BuildDictOptions{ID: 1, History: hist})
		var ce *buildDictError
		if !errors.As(err, &ce) || ce.code != 34 {
			t.Fatalf("history %d: err = %v, want C error 34 (dictionaryCreation_failed)", size, err)
		}
	}
}

// TestBuildDictLDMRefused checks that parameters C would run with
// long-distance matching (level 22 with a 64 MiB history: btultra2,
// windowLog 27) are refused before the CDict is built, since LDM is not
// ported.
func TestBuildDictLDMRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("maps 64 MiB of address space")
	}
	hist := hugeZeroMapping(t, 64<<20)
	_, err := finalizeDictionary(hist, nil, 22, 1, repStartValue, nil)
	var ce *buildDictError
	if !errors.As(err, &ce) || ce.code != int(dErrGeneric) || !strings.Contains(err.Error(), "long-distance") {
		t.Fatalf("err = %v, want the LDM refusal", err)
	}
}

// TestTrainDictTooLarge checks the trainers' limit on the total sample
// size (4 GiB - 1 on 64-bit): 5 GiB of samples (one untouched 1 GiB
// mapping, five times) fail with srcSize_wrong, as in C, without being
// concatenated or read.
func TestTrainDictTooLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("maps 1 GiB of address space")
	}
	big := hugeZeroMapping(t, 1<<30)
	samples := [][]byte{big, big, big, big, big}
	_, err := TrainDict(samples, 4096)
	_, _, err2 := TrainDictCover(samples, 4096, CoverParams{K: 100, D: 8})
	_, _, err3 := TrainDictFastCover(samples, 4096, CoverParams{})
	for _, e := range []error{err, err2, err3} {
		var ce *buildDictError
		if !errors.As(e, &ce) || ce.code != int(dErrSrcSizeWrong) {
			t.Fatalf("err = %v, want C error 72 (srcSize_wrong)", e)
		}
	}
}
