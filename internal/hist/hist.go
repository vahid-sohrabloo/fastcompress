// Package hist ports lib/compress/hist.c from libzstd v1.5.7 (Copyright (c)
// Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license).
package hist

// MaxSymbolTooSmall is returned by Count (as C returns
// ERROR(maxSymbolValue_tooSmall)) when src holds a symbol above *maxSymbol.
// count and *maxSymbol are left untouched in that case.
const MaxSymbolTooSmall = ^uint32(0)

// CountSimple counts bytes with a single table. It zeroes count[0..*maxSymbol],
// sets *maxSymbol to the largest symbol present and returns the largest count.
// src must not contain symbols above *maxSymbol.
// C: hist.c:HIST_count_simple
func CountSimple(count *[256]uint32, maxSymbol *uint32, src []byte) uint32 {
	maxSymbolValue := *maxSymbol
	clear(count[:maxSymbolValue+1])
	if len(src) == 0 {
		*maxSymbol = 0
		return 0
	}
	for _, b := range src {
		count[b]++
	}
	for count[maxSymbolValue] == 0 {
		maxSymbolValue--
	}
	*maxSymbol = maxSymbolValue
	var largest uint32
	for s := uint32(0); s <= maxSymbolValue; s++ {
		if count[s] > largest {
			largest = count[s]
		}
	}
	return largest
}

// countParallel counts into 4 tables, recombined at the end.
// C: hist.c:HIST_count_parallel_wksp. The workspace is a local array (4 KiB,
// stack-allocated) instead of a caller-provided buffer.
func countParallel(count *[256]uint32, maxSymbol *uint32, src []byte, check bool) uint32 {
	n := len(src)
	countSize := int(*maxSymbol) + 1
	if n == 0 {
		clear(count[:countSize])
		*maxSymbol = 0
		return 0
	}
	// The [0]uint64 field aligns ws to 8 bytes: the compiler zeroes it with
	// REP STOSQ, which is much slower on a 4-byte-aligned address.
	var w struct {
		_  [0]uint64
		ws [4 * 256]uint32
	}
	ws := &w.ws
	countStripes(ws, src)
	maxCount := countMerge(ws, n)
	c1 := (*[256]uint32)(ws[0:256])

	maxSymbolValue := uint32(255)
	for c1[maxSymbolValue] == 0 {
		maxSymbolValue--
	}
	if check && maxSymbolValue > *maxSymbol {
		return MaxSymbolTooSmall
	}
	*maxSymbol = maxSymbolValue
	copy(count[:countSize], c1[:countSize])
	return maxCount
}

// CountFast is Count without the symbol-range check; it uses the simple
// counter for inputs under 1500 bytes. C: hist.c:HIST_countFast_wksp
func CountFast(count *[256]uint32, maxSymbol *uint32, src []byte) uint32 {
	if len(src) < 1500 { // heuristic threshold
		return CountSimple(count, maxSymbol, src)
	}
	return countParallel(count, maxSymbol, src, false)
}

// Count builds the histogram of src. On return *maxSymbol is the largest
// symbol present and the result is the largest count, or MaxSymbolTooSmall if
// *maxSymbol < 255 on entry and src holds a larger symbol.
// C: hist.c:HIST_count_wksp
func Count(count *[256]uint32, maxSymbol *uint32, src []byte) uint32 {
	if *maxSymbol < 255 {
		return countParallel(count, maxSymbol, src, true)
	}
	*maxSymbol = 255
	return CountFast(count, maxSymbol, src)
}
