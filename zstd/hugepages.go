package zstd

import (
	"runtime"
	"unsafe"
)

// Huge-page backing for a context's large match tables (WithHugePages).
// Speed only: the tables hold the same values wherever they live.
//
// GC safety: on Linux a table of at least hugeMinBytes is a slice of an
// anonymous mmap made by hugeMap, outside the Go heap. The GC neither scans
// nor frees that memory, which is safe because the tables hold only uint32
// indices, tag bytes and LDM entries (two uint32s), never Go pointers. A
// mapping is unmapped in two places only:
//   - hugeTables.get, when a table outgrows its mapping: the old slice is
//     the table being replaced, and the match state (the LDM state for the
//     LDM table) is its only holder (rowAsm/btAsm's raw pointers are
//     cleared at every block end, and CDicts never use hugeTables);
//   - hugeTables.free, the cleanup registered on the owning cctx: it runs
//     once the cctx is unreachable, so nothing can still use its tables.
//     The cctx is released by the GC when the Encoder no longer holds it
//     (EncodeAll drops a context after a panic; otherwise the Encoder keeps
//     its contexts for its lifetime); Close does not unmap, as the Encoder
//     may still be reused.

// hugeMinBytes is the smallest table backed by huge pages. A table below
// it would waste most of a 2 MiB page and fits the TLB's reach anyway.
const hugeMinBytes = 1 << 20

// hugePageSize is the x86-64 / arm64 (4 KiB granule) transparent huge page.
const hugePageSize = 2 << 20

// Table slots of hugeTables.
const (
	hugeHash = iota
	hugeChain
	hugeHash3
	hugeTag
	hugeLdm // the long-distance matcher's hash table
	hugeSlots
)

// hugeTables owns the mmap'd regions behind a context's match tables, one
// per slot. Each region is the whole mapping (for munmap); the table is a
// 2 MiB-aligned slice of it.
type hugeTables struct {
	mem [hugeSlots][]byte
}

// newHugeTables returns the huge-page table set for c, or nil where huge
// pages are not supported. Its mappings are unmapped when c is collected.
func newHugeTables(c *cctx) *hugeTables {
	if !hugePagesSupported {
		return nil
	}
	h := &hugeTables{}
	runtime.AddCleanup(c, (*hugeTables).free, h)
	return h
}

// get returns n zeroed bytes, 2 MiB-aligned and advised for huge pages,
// replacing (and unmapping) the slot's previous mapping; or nil if h is nil,
// n is below hugeMinBytes or the mapping fails (the caller then allocates on
// the Go heap).
func (h *hugeTables) get(slot, n int) []byte {
	if h == nil || n < hugeMinBytes {
		return nil
	}
	mem, b := hugeMap(n)
	if b == nil {
		return nil
	}
	if h.mem[slot] != nil {
		hugeUnmap(h.mem[slot])
	}
	h.mem[slot] = mem
	return b
}

// free unmaps every region.
func (h *hugeTables) free() {
	for i, m := range h.mem {
		if m != nil {
			hugeUnmap(m)
			h.mem[i] = nil
		}
	}
}

// resizeHugeTable is resizeTable, taking a new table from h when t must
// grow and h can supply it.
func resizeHugeTable(h *hugeTables, slot int, t []uint32, n int) []uint32 {
	if cap(t) < n {
		if b := h.get(slot, n*4); b != nil {
			return unsafe.Slice((*uint32)(unsafe.Pointer(unsafe.SliceData(b))), n)
		}
	}
	return resizeTable(t, n)
}

// resizeHugeTagTable is resizeTagTable, taking a new table from h when t
// must grow and h can supply it.
func resizeHugeTagTable(h *hugeTables, t []byte, n int) []byte {
	if cap(t) < n {
		if b := h.get(hugeTag, n); b != nil {
			return b
		}
	}
	return resizeTagTable(t, n)
}

// resizeHugeLdmTable returns the LDM hash table t with n entries, taking a
// new table from h when t must grow and h can supply it. Its content is
// not kept current (the caller clears it, as C does on every reset).
func resizeHugeLdmTable(h *hugeTables, t []ldmEntry, n int) []ldmEntry {
	if cap(t) < n {
		if b := h.get(hugeLdm, n*int(unsafe.Sizeof(ldmEntry{}))); b != nil {
			return unsafe.Slice((*ldmEntry)(unsafe.Pointer(unsafe.SliceData(b))), n)
		}
		return make([]ldmEntry, n)
	}
	return t[:n]
}
