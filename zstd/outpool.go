package zstd

import (
	"math/bits"
	"sync"
)

// maxPooledOut is the largest worst-case output (compressBound) that
// EncodeAll compresses into a pooled scratch buffer.
const maxPooledOut = 1 << 27

// outPools recycles the scratch buffers EncodeAll compresses into when the
// caller's dst has no room for the worst case (typically dst == nil).
// outPools[c] holds buffers of exactly 1<<c bytes. The pools are shared by
// all Encoders and emptied by the garbage collector.
var outPools [28]sync.Pool

// getPooledOut returns a scratch buffer of at least n bytes (0 < n <=
// maxPooledOut), whose contents are unspecified.
func getPooledOut(n int) *[]byte {
	c := bits.Len(uint(n - 1))
	if v := outPools[c].Get(); v != nil {
		return v.(*[]byte)
	}
	b := make([]byte, 1<<c)
	return &b
}

// putPooledOut returns a buffer from getPooledOut to its pool.
func putPooledOut(p *[]byte) {
	outPools[bits.Len(uint(cap(*p)-1))].Put(p)
}
