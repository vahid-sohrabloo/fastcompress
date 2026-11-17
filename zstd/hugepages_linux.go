//go:build linux

package zstd

import (
	"sync/atomic"
	"syscall"
	"unsafe"
)

const hugePagesSupported = true

// hugeLive counts the live mappings.
var hugeLive atomic.Int64

// hugeTrace, when set (by tests only, before any mapping they watch is
// made), is called with each mapping hugeMap makes (mapped true) and each
// one hugeUnmap unmaps; the cleanup goroutine may call it.
var hugeTrace atomic.Pointer[func(mem []byte, mapped bool)]

// hugeMap maps n bytes rounded up to 2 MiB, plus 2 MiB of slack (address
// space only, never touched) so a 2 MiB-aligned start exists inside the
// mapping, and advises the aligned part with MADV_HUGEPAGE before anything
// touches it, so its first faults take huge pages when the kernel's THP
// mode is "always" or "madvise". It returns the whole mapping (for
// hugeUnmap) and the aligned n bytes, or nils if mmap fails. A failed
// madvise (THP compiled out) leaves ordinary pages.
func hugeMap(n int) (mem, b []byte) {
	size := (n + hugePageSize - 1) &^ (hugePageSize - 1)
	mem, err := syscall.Mmap(-1, 0, size+hugePageSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, nil
	}
	off := int(-uintptr(unsafe.Pointer(unsafe.SliceData(mem))) & (hugePageSize - 1))
	_ = syscall.Madvise(mem[off:off+size], syscall.MADV_HUGEPAGE)
	hugeLive.Add(1)
	if f := hugeTrace.Load(); f != nil {
		(*f)(mem, true)
	}
	return mem, mem[off : off+n : off+n]
}

// hugeUnmap unmaps a mapping returned by hugeMap.
func hugeUnmap(mem []byte) {
	if syscall.Munmap(mem) == nil {
		hugeLive.Add(-1)
		if f := hugeTrace.Load(); f != nil {
			(*f)(mem, false)
		}
	}
}
