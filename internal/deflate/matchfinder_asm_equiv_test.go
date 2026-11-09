package deflate

import (
	"encoding/binary"
	"slices"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// Assembly equivalence of matchfinderInit / matchfinderRebase
// (matchfinder_amd64.s): each is run with the assembly off (the Go loops, on
// a heap copy) and on, the latter on entries flush against PROT_NONE guard
// pages (after the end, then before the start), so that an access past
// either end faults; the results must be identical. In builds without the
// assembly both runs are the Go loops.

// guardedPos returns n mfPos entries ending at a guard page (after) or
// starting at one (!after); guardmem's mappings are page-aligned, so the
// entries are 2-byte aligned.
func guardedPos(t testing.TB, n int, after bool) []mfPos {
	if n == 0 {
		return nil
	}
	var b []byte
	if after {
		b = guardmem.After(t, 2*n)
	} else {
		b = guardmem.Before(t, 2*n)
	}
	return unsafe.Slice((*mfPos)(unsafe.Pointer(&b[0])), n)
}

// checkMatchfinderAsm compares init and rebase of vals with the assembly
// off and on.
func checkMatchfinderAsm(t testing.TB, vals []mfPos) {
	t.Helper()
	wantRebase := slices.Clone(vals)
	wantInit := slices.Clone(vals)
	withDeflateAsm(false, func() {
		matchfinderRebase(wantRebase)
		matchfinderInit(wantInit)
	})
	for _, after := range []bool{true, false} {
		got := guardedPos(t, len(vals), after)
		copy(got, vals)
		withDeflateAsm(true, func() { matchfinderRebase(got) })
		if !slices.Equal(got, wantRebase) {
			t.Fatalf("matchfinderRebase (n %d, guard after %v): asm differs from Go", len(vals), after)
		}
		copy(got, vals)
		withDeflateAsm(true, func() { matchfinderInit(got) })
		if !slices.Equal(got, wantInit) {
			t.Fatalf("matchfinderInit (n %d, guard after %v): asm differs from Go", len(vals), after)
		}
	}
	// The assembly called directly on exactly the len&^31 entries it
	// processes, so that the guard page sits at its write limit (through
	// matchfinderInit/Rebase the Go tail lies between them for n%32 != 0).
	withDeflateAsm(true, func() {
		if !useMatchfinderSSE2 {
			return // no assembly in this build (the stubs do nothing)
		}
		n := len(vals) &^ 31
		got := guardedPos(t, n, true)
		copy(got, vals)
		matchfinderRebaseSSE2(got)
		if !slices.Equal(got, wantRebase[:n]) {
			t.Fatalf("matchfinderRebaseSSE2 (n %d, guard at %d): differs from Go", len(vals), n)
		}
		copy(got, vals)
		matchfinderInitSSE2(got)
		if !slices.Equal(got, wantInit[:n]) {
			t.Fatalf("matchfinderInitSSE2 (n %d, guard at %d): differs from Go", len(vals), n)
		}
	})
}

// TestMatchfinderAsmEquiv runs every mf_pos_t value through the rebase (as
// one 65536-entry table), then lengths 0..100 (every remainder of 32) and
// the matchfinder table sizes.
func TestMatchfinderAsmEquiv(t *testing.T) {
	all := make([]mfPos, 1<<16)
	for i := range all {
		all[i] = mfPos(i)
	}
	checkMatchfinderAsm(t, all)
	for n := 0; n <= 100; n++ {
		vals := make([]mfPos, n)
		for i := range vals {
			vals[i] = mfPos(i*7919 - 32768)
		}
		checkMatchfinderAsm(t, vals)
	}
	var ht htMatchfinder
	var hc hcMatchfinder
	var bt btMatchfinder
	for _, n := range []int{len(ht.hashTab), len(hc.hash3Tab), len(hc.hash4Tab), len(hc.nextTab),
		len(bt.hash3Tab), len(bt.childTab)} {
		vals := make([]mfPos, n)
		for i := range vals {
			vals[i] = mfPos(i * 40503)
		}
		checkMatchfinderAsm(t, vals)
	}
}

// FuzzDeflateMatchfinderSSE2AsmEquiv: the fuzz input, read as little-endian
// 16-bit entries, through init and rebase with the assembly off and on.
func FuzzDeflateMatchfinderSSE2AsmEquiv(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00, 0x80, 0xFF, 0x7F, 0x00, 0x00, 0xFF, 0xFF})
	seed := make([]byte, 2*67)
	for i := range seed {
		seed[i] = byte(i * 37)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		vals := make([]mfPos, len(data)/2)
		for i := range vals {
			vals[i] = mfPos(binary.LittleEndian.Uint16(data[2*i:]))
		}
		checkMatchfinderAsm(t, vals)
	})
}
