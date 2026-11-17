// Port of libzstd v1.5.7 lib/compress/hist.c (Copyright (c) Meta
// Platforms, Inc. and affiliates; BSD/GPLv2 dual license): the counting
// loop of HIST_count_parallel_wksp, always compiled; hist_amd64.s and
// hist_arm64.s have equivalent ones.

package hist

import "encoding/binary"

// countStripesGeneric adds the byte counts of src to ws, spread over its four
// 256-entry tables (their sum is the histogram).
func countStripesGeneric(ws *[4 * 256]uint32, src []byte) {
	n := len(src)
	c1 := (*[256]uint32)(ws[0:256])
	c2 := (*[256]uint32)(ws[256:512])
	c3 := (*[256]uint32)(ws[512:768])
	c4 := (*[256]uint32)(ws[768:1024])

	// by stripes of 16 bytes. C reads 4 bytes before checking the size; the
	// loop cannot run for n < 20, so that case is skipped here.
	ip := 0
	if n >= 20 {
		cached := binary.LittleEndian.Uint32(src)
		ip = 4
		for ip < n-15 {
			for range 4 {
				c := cached
				cached = binary.LittleEndian.Uint32(src[ip:])
				ip += 4
				c1[byte(c)]++
				c2[byte(c>>8)]++
				c3[byte(c>>16)]++
				c4[c>>24]++
			}
		}
		ip -= 4
	}
	for ; ip < n; ip++ {
		c1[src[ip]]++
	}
}

// countMergeGeneric adds tables 1-3 of ws into table 0 and returns the
// largest sum.
func countMergeGeneric(ws *[4 * 256]uint32) uint32 {
	c1 := (*[256]uint32)(ws[0:256])
	c2 := (*[256]uint32)(ws[256:512])
	c3 := (*[256]uint32)(ws[512:768])
	c4 := (*[256]uint32)(ws[768:1024])
	var maxCount uint32
	for s := range c1 {
		c1[s] += c2[s] + c3[s] + c4[s]
		if c1[s] > maxCount {
			maxCount = c1[s]
		}
	}
	return maxCount
}
