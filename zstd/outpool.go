package zstd

import (
	"math/bits"
	"slices"
	"sync"
	"weak"
)

// maxPooledOut is the largest worst-case output (compressBound) that
// EncodeAll compresses into a pooled scratch buffer.
const maxPooledOut = 1 << 27

// smallPooledOut is the largest scratch buffer kept in outPools. Larger
// ones are kept only weakly, in largeOut.
const smallPooledOut = 1 << 16

// outPools recycles the scratch buffers of up to smallPooledOut bytes that
// EncodeAll compresses into when the caller's dst has no room for the worst
// case (typically dst == nil). outPools[c] holds buffers of exactly 1<<c
// bytes. The pools are shared by all Encoders and emptied by the garbage
// collector.
var outPools [17]sync.Pool

// largeOut recycles the larger scratch buffers through weak pointers. A
// buffer in a sync.Pool is reachable: it survives the next collection and
// counts in the live heap that sets the heap goal, so with GOGC=100 an idle
// buffer costs about twice its size in heap. Concurrent calls of mixed
// sizes (restic: blobs of 512 KiB to 8 MiB on GOMAXPROCS goroutines) leave
// idle buffers of every size class, tens of MiB at the peak. A weakly held
// buffer is reused until the next collection, which frees it.
var largeOut struct {
	mu   sync.Mutex
	bufs []largeOutBuf // oldest first
}

// largeOutBuf is an idle buffer of largeOut and its capacity. The capacity
// is kept here so that choosing a buffer does not call Value on the others:
// during a collection's mark phase that would keep them alive.
type largeOutBuf struct {
	p   weak.Pointer[[]byte]
	cap int
}

// largeOutMax bounds the number of buffers largeOut tracks.
const largeOutMax = 64

// getPooledOut returns a scratch buffer of at least n bytes (0 < n <=
// maxPooledOut), whose contents are unspecified.
func getPooledOut(n int) *[]byte {
	if n <= smallPooledOut {
		c := bits.Len(uint(n - 1))
		if v := outPools[c].Get(); v != nil {
			return v.(*[]byte)
		}
		b := make([]byte, 1<<c)
		return &b
	}
	if p := getLargeOut(n); p != nil {
		return p
	}
	b := make([]byte, largeOutSize(n))
	return &b
}

// largeOutSize rounds n (> smallPooledOut) up to a multiple of 1/8 of the
// power of two below it: at most 12.5% more than n.
func largeOutSize(n int) int {
	step := 1 << (bits.Len(uint(n-1)) - 4)
	return (n + step - 1) &^ (step - 1)
}

// getLargeOut takes from largeOut the smallest buffer of n to 2n bytes (a
// larger one would hold more memory than a new one) that the garbage
// collector has not freed, or returns nil. It drops the freed ones it meets.
func getLargeOut(n int) *[]byte {
	largeOut.mu.Lock()
	defer largeOut.mu.Unlock()
	for {
		bi := -1
		for i, b := range largeOut.bufs {
			if b.cap >= n && b.cap/2 <= n && (bi < 0 || b.cap < largeOut.bufs[bi].cap) {
				bi = i
			}
		}
		if bi < 0 {
			return nil
		}
		p := largeOut.bufs[bi].p.Value()
		largeOut.bufs = slices.Delete(largeOut.bufs, bi, bi+1)
		if p != nil {
			return p
		}
	}
}

// putPooledOut returns a buffer from getPooledOut to its pool.
func putPooledOut(p *[]byte) {
	if cap(*p) <= smallPooledOut {
		outPools[bits.Len(uint(cap(*p)-1))].Put(p)
		return
	}
	largeOut.mu.Lock()
	if len(largeOut.bufs) == largeOutMax {
		// Most likely freed by now, or of a size no longer asked for.
		largeOut.bufs = slices.Delete(largeOut.bufs, 0, 1)
	}
	largeOut.bufs = append(largeOut.bufs, largeOutBuf{weak.Make(p), cap(*p)})
	largeOut.mu.Unlock()
}
