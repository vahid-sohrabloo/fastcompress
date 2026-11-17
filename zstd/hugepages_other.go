//go:build !linux

package zstd

// Huge pages are a Linux-only option (WithHugePages); elsewhere it is a
// no-op and the tables stay on the Go heap.
const hugePagesSupported = false

func hugeMap(int) (mem, b []byte) { return nil, nil }

func hugeUnmap([]byte) {}
